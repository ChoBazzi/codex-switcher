import Foundation
import Darwin

@main
struct DirectProxyCheck {
    private static let brokenRelayDirectoryKey = "SWITCHER_CHECK_BROKEN_RELAY_DIRECTORY"

    private static func trace(_ event: String, directory: URL) throws {
        let url = directory.appendingPathComponent("relay-\(getpid()).log")
        if !FileManager.default.fileExists(atPath: url.path) {
            precondition(FileManager.default.createFile(atPath: url.path, contents: nil))
        }
        let handle = try FileHandle(forWritingTo: url)
        defer { try? handle.close() }
        try handle.seekToEnd()
        try handle.write(contentsOf: Data((event + "\n").utf8))
    }

    @MainActor private static func waitFor(_ condition: () -> Bool) async throws {
        for _ in 0..<150 {
            if condition() { return }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        fatalError("direct proxy check timed out")
    }

    private static func assertDefaultSIGPIPE() {
        var disposition = sigaction()
        precondition(sigaction(SIGPIPE, nil, &disposition) == 0)
        precondition(unsafeBitCast(disposition.__sigaction_u.__sa_handler, to: UInt.self) == 0,
                     "relay protection must preserve the process-wide default SIGPIPE disposition")
    }

    @MainActor private static func checkBrokenRelay(_ action: String, directory: URL) async throws {
        signal(SIGPIPE, SIG_DFL)
        assertDefaultSIGPIPE()
        func logs() -> [String] {
            let files = try! FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)
            return files.filter { $0.pathExtension == "log" }.map { try! String(contentsOf: $0, encoding: .utf8) }
        }
        let store = DirectProxyStore(usageReadTimeout: 10_000_000_000)
        var invalidations = 0
        var shutdownResults: [Bool] = []
        store.onUsageUnavailable = { invalidations += 1 }
        let helper = URL(fileURLWithPath: CommandLine.arguments[0])
        store.start(helper: helper)
        try await waitFor { store.ready }
        precondition(store.auxiliaryCount == 127 && store.auxiliaryLimit == 128 && store.auxiliaryCapacityText.contains("1개 가능"))
        if action != "refresh" {
            // Leave a real usage request pending when a different command loses the relay.
            store.readUsage()
            try await waitFor { store.usageRefreshMessage == "사용량 조회 중…" && logs().contains { $0.contains("closed\n") } }
        }
        let previousInvalidations = invalidations
        switch action {
        case "poll":
            store.busy = true // The last busy observation must be cleared on disconnect.
            store.poll()
        case "select": store.select("b")
        case "refresh": store.readUsage()
        case "shutdown": store.shutdownService { shutdownResults.append($0) }
        default: fatalError("unknown broken relay action")
        }
        precondition(!store.ready && !store.starting && !store.pending && !store.busy && !store.connected)
        precondition(store.home == nil && !store.stopping && !store.canReadUsage && !store.usageRefreshing)
        precondition(store.message.contains("연결 끊김") && invalidations > previousInvalidations)
        precondition(shutdownResults == (action == "shutdown" ? [false] : []))
        assertDefaultSIGPIPE()
        store.poll()
        store.select("b")
        try await Task.sleep(nanoseconds: 100_000_000)
        precondition(logs().count == 1, "a failed command must not relaunch its relay")
        let oldLog = logs()[0]
        precondition(!oldLog.contains("command:status") && !oldLog.contains("command:select") && !oldLog.contains("command:shutdown"))
        precondition(oldLog.components(separatedBy: "command:usage_refresh").count - 1 == (action == "refresh" ? 0 : 1))

        // Only an explicit reconnect may start another helper. Its log must contain no replay.
        try Data().write(to: directory.appendingPathComponent("healthy"))
        store.start(helper: helper)
        try await waitFor { store.ready && logs().count == 2 }
        precondition(store.slot == "a" && !store.busy && !store.pending && !store.usageRefreshing)
        precondition(logs().first { $0.hasPrefix("healthy\n") } == "healthy\n")
        let reconnectedInvalidations = invalidations
        try Data().write(to: directory.appendingPathComponent("release-old-relay"))
        try await waitFor { logs().contains { $0.contains("released\n") } }
        // The old helper emits stale state, a shutdown ACK and EOF after the new relay is ready.
        for _ in 0..<10 {
            try await Task.sleep(nanoseconds: 20_000_000)
            precondition(store.ready && store.slot == "a" && !store.busy && !store.pending)
            precondition(invalidations == reconnectedInvalidations)
            precondition(shutdownResults == (action == "shutdown" ? [false] : []))
        }
        store.select("b")
        try await waitFor { store.slot == "b" && !store.pending }
        var finished: Bool?
        store.shutdownService { finished = $0 }
        try await waitFor { finished != nil }
        precondition(finished == true && !store.ready)
        precondition(shutdownResults == (action == "shutdown" ? [false] : []))
        assertDefaultSIGPIPE()
    }

    @MainActor private static func checkBrokenRelaySubprocesses() async throws {
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("broken-relay-check-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        var children: [Process] = []
        defer { children.filter(\.isRunning).forEach { $0.terminate() } }
        for action in ["poll", "select", "refresh", "shutdown"] {
            let fixture = directory.appendingPathComponent(action)
            try FileManager.default.createDirectory(at: fixture, withIntermediateDirectories: true)
            let process = Process()
            process.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
            process.arguments = ["--broken-relay-check", action]
            var environment = ProcessInfo.processInfo.environment
            environment[brokenRelayDirectoryKey] = fixture.path
            process.environment = environment
            try process.run()
            children.append(process)
        }
        try await waitFor { children.allSatisfy { !$0.isRunning } }
        for process in children {
            precondition(process.terminationReason == .exit && process.terminationStatus == 0,
                         "broken relay \(process.arguments!.last!) failed: \(process.terminationReason), \(process.terminationStatus)")
        }
        print("PASS: broken relay poll/select/refresh/shutdown survive default SIGPIPE, detach without replay, reconnect explicitly and ignore stale EOF")
    }

    @MainActor private static func checkDiagnostics() {
        let store = DirectProxyStore()
        func event(_ object: [String: Any]) {
            store.receiveDiagnosticEvent(try! JSONSerialization.data(withJSONObject: object))
        }
        let old = String(repeating: "a", count: 64), new = String(repeating: "b", count: 64)
        store.ready = true
        precondition(store.buildWarning != nil)
        event(["event":"helper_build", "build_id":new, "protocol_version":1])
        event(["event":"probe_ready", "build_id":new, "protocol_version":1])
        precondition(store.buildWarning == nil && store.helperBuild == new && store.proxyBuild == new)
        event(["event":"probe_ready", "build_id":old, "protocol_version":1])
        precondition(store.buildWarning!.contains("다릅니다"))
        event(["event":"probe_ready", "build_id":new, "protocol_version":99])
        precondition(store.buildWarning!.contains("제어 버전"))
        event(["event":"probe_ready", "codex_home":"/synthetic-secret"])
        precondition(store.proxyBuild == nil && store.buildWarning!.contains("확인할 수 없습니다"))
        let at = "2026-09-21T00:00:00Z"
        event(["event":"probe_diagnostic", "scope":"root", "code":"history_owner_unavailable", "at":at,
               "body":"synthetic-secret", "account_id":"synthetic-secret"])
        event(["event":"probe_diagnostic", "scope":"auxiliary", "code":"agent_message_shape_unsupported", "at":at])
        precondition(store.diagnostics.count == 2 && store.rootDiagnostic?.code == "history_owner_unavailable")
        precondition(store.auxiliaryDiagnostic?.title.contains("에이전트") == true)
        event(["event":"probe_diagnostic", "scope":"root", "code":"synthetic-secret", "at":at])
        event(["event":"probe_diagnostic", "scope":"root", "code":"history_unsupported", "at":"synthetic-secret"])
        event(["event":"helper_build", "build_id":"/synthetic-secret"])
        precondition(store.helperBuild == nil && store.rootDiagnostic?.code == "history_owner_unavailable")
        precondition(!store.diagnosticText.contains("synthetic-secret"))
        event(["event":"probe_diagnostic", "scope":"root", "code":"request_canceled", "at":at])
        precondition(store.rootDiagnostic?.title.contains("취소") == true)
        precondition(store.rootDiagnostic?.guidance.contains("자동으로 다시 보내지 않습니다") == true)
        store.stop()
        precondition(store.diagnosticsFromPreviousConnection && store.diagnostics.count == 2 && store.buildWarning == nil)
        print("PASS: build match/mismatch/legacy protocol, diagnostic redaction, independent root/auxiliary guidance and stale connection labeling")
    }

    private static func multiRelayFixture() {
        func emit(_ object: [String: Any]) {
            try! FileHandle.standardOutput.write(contentsOf: JSONSerialization.data(withJSONObject: object) + Data([10]))
        }
        var selected = "1", count = 1
        func show() {
            let rows = (1...count).map { ["id":String($0), "ready":true, "busy":$0 == 1, "failed":false] as [String: Any] }
            emit(["event":"connection_list", "selected":selected, "connections":rows, "limit":5])
            emit(["event":"probe_ready", "connection_id":selected, "codex_home":"/synthetic-" + selected])
            emit(["event":"probe_state", "connection_id":selected, "slot":selected == "1" ? "a" : "b", "busy":selected == "1", "failed":false, "connected":true, "revision":1,
                  "conversation":"12345678-1234-4234-8234-12345678900" + selected])
        }
        show()
        while let line = readLine(), let data = line.data(using: .utf8),
              let command = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
            switch command["action"] as? String {
            case "connection_create": count += 1; selected = String(count); show(); emit(["event":"connection_result", "accepted":true])
            case "connection_select": selected = command["connection_id"] as! String; show(); emit(["event":"connection_result", "accepted":true])
            case "status":
                show()
                // An out-of-date, differently tagged frame must not replace the selection.
                emit(["event":"probe_state", "connection_id":"5", "slot":"e", "busy":false, "failed":true, "connected":true, "revision":99])
            case "select":
                precondition(command["connection_id"] as? String == selected)
                emit(["event":"probe_selection", "connection_id":selected, "slot":command["slot"]!, "busy":false, "failed":false, "connected":true, "revision":2, "accepted":true])
            case "shutdown": emit(["event":"probe_shutdown", "accepted":false])
            default: break
            }
        }
    }

    @MainActor private static func checkMultipleConnections() async throws {
        setenv("SWITCHER_MULTI_TEST", "1", 1)
        let store = DirectProxyStore()
        store.start(helper: URL(fileURLWithPath: CommandLine.arguments[0]))
        unsetenv("SWITCHER_MULTI_TEST")
        try await waitFor { store.ready }
        precondition(store.busy && store.canAddConnection)
        store.addConnection()
        try await waitFor { store.ready && !store.pending && store.selectedConnection == "2" }
        precondition(!store.busy && store.allBusy && store.home == "/synthetic-2" && store.slot == "b")
        precondition(store.canSelect("c"))
        precondition(DirectProxyStore.connectionCommand(home: store.home!, resume: true, conversation: store.conversation).hasSuffix("resume 12345678-1234-4234-8234-123456789002"))
        store.select("c")
        try await waitFor { !store.pending && store.slot == "c" }
        store.poll()
        try await waitFor { store.slot == "b" }
        precondition(!store.failed && store.selectedConnection == "2" && store.allBusy)
        store.chooseConnection("1")
        try await waitFor { !store.pending && store.home == "/synthetic-1" }
        precondition(store.busy && store.slot == "a")
        store.chooseConnection("2")
        try await waitFor { !store.pending && store.home == "/synthetic-2" }
        var stopped: Bool?
        store.shutdownService { stopped = $0 }
        try await waitFor { stopped != nil }
        precondition(stopped == false && store.ready)
        store.stop()
        precondition(store.connections.isEmpty && store.conversation == nil)
        print("PASS: multiple connection selection, targeted controls, aggregate busy, bound resume, stale-event isolation and shutdown rejection")
    }

    @MainActor static func main() async throws {
        if CommandLine.arguments.contains("proxy-connect"), ProcessInfo.processInfo.environment["SWITCHER_MULTI_TEST"] == "1" {
            multiRelayFixture(); return
        }
        if CommandLine.arguments.contains("proxy-connect") {
            func emit(_ value: [String: Any]) {
                let data = try! JSONSerialization.data(withJSONObject: value)
                try! FileHandle.standardOutput.write(contentsOf: data + Data([10]))
            }
            var slot = "a", revision = 0, toolWaiting = false, failed = false, usageReads = 0
            func state(_ event: String, _ accepted: Bool = false) {
                emit(["event":event,"slot":slot,"busy":toolWaiting,"failed":failed,"connected":true,"revision":revision,"accepted":accepted,"can_abandon_turn":toolWaiting,"auxiliary_count":127,"auxiliary_limit":128])
            }
            let fixture = ProcessInfo.processInfo.environment[brokenRelayDirectoryKey].map { URL(fileURLWithPath: $0) }
            let broken = fixture.map { !FileManager.default.fileExists(atPath: $0.appendingPathComponent("healthy").path) } ?? false
            let breaksOnRefresh = broken && fixture!.lastPathComponent != "refresh"
            if let fixture { try trace(broken ? "broken" : "healthy", directory: fixture) }
            func closeInput() throws {
                try FileHandle.standardInput.close()
                try trace("closed", directory: fixture!)
            }
            func holdBrokenRelay() throws {
                for _ in 0..<250 {
                    if FileManager.default.fileExists(atPath: fixture!.appendingPathComponent("release-old-relay").path) {
                        slot = "e"; toolWaiting = true
                        state("probe_state")
                        state("probe_shutdown", true)
                        try FileHandle.standardOutput.close()
                        try trace("released", directory: fixture!)
                        return
                    }
                    Thread.sleep(forTimeInterval: 0.02)
                }
                fatalError("broken relay fixture was not released")
            }
            if broken && !breaksOnRefresh { try closeInput() }
            emit(["event":"probe_ready","codex_home":"/private/tmp/synthetic-unused-home"])
            state("probe_state")
            if broken && !breaksOnRefresh { try holdBrokenRelay(); return }
            while let line = readLine(), let data = line.data(using: .utf8),
                  let command = try JSONSerialization.jsonObject(with: data) as? [String: Any] {
                if let fixture { try trace("command:" + (command["action"] as! String), directory: fixture) }
                if breaksOnRefresh {
                    precondition(command["action"] as? String == "usage_refresh")
                    try closeInput()
                    emit(["event":"usage_refresh", "request_id":command["request_id"]!, "status":"started"])
                    try holdBrokenRelay()
                    return
                }
                if command["action"] as? String == "shutdown" {
                    precondition(command["new_session"] as? Bool == false)
                    state("probe_shutdown", !toolWaiting)
                    if !toolWaiting { return }
                    continue
                }
                if command["action"] as? String == "status" { state("probe_state"); continue }
                if command["action"] as? String == "recover" {
                    let accepted = failed && !toolWaiting && command["revision"] as? Int == revision
                    if accepted { slot = "b"; failed = false; revision += 1 }
                    state("probe_selection", accepted)
                    continue
                }
                if command["action"] as? String == "abandon_turn" {
                    let accepted = toolWaiting && command["revision"] as? Int == revision
                    if accepted { toolWaiting = false; failed = true; revision += 1 }
                    state("probe_abandonment", accepted)
                    continue
                }
                if command["action"] as? String == "usage_refresh" {
                    let requestID = command["request_id"] as! Int
                    usageReads += 1
                    emit(["event":"usage_refresh","request_id":requestID,"status":"started"])
                    if usageReads == 2 {
                        // An old completion must not finish this request.
                        emit(["event":"usage_refresh","request_id":requestID-1,"status":"finished","succeeded":true])
                        continue
                    }
                    if usageReads == 3 {
                        emit(["event":"usage_snapshot","accounts":[]])
                        continue
                    }
                    var accounts = ["a","b","c","d","e"].map { ["slot":$0,"state":"not_registered","stale":true,"registered":false] as [String:Any] }
                    accounts[1] = ["slot":"b","state":"ok","stale":false,"registered":true,"last_success":"2026-09-10T00:00:00Z",
                                   "usage":["remaining_percent":30,"primary":["remaining_percent":70,"limit_seconds":18000],"secondary":["remaining_percent":30,"limit_seconds":604800]]]
                    emit(["event":"usage_snapshot","accounts":accounts])
                    emit(["event":"usage_refresh","request_id":requestID,"status":"finished","succeeded":true])
                    continue
                }
                let accepted = command["revision"] as? Int == revision
                if accepted { slot = command["slot"] as! String; revision += 1 }
                state("probe_selection", accepted)
                if slot == "e" { toolWaiting = true; state("probe_state") }
            }
            return
        }
        if CommandLine.arguments.contains("--broken-relay-check") {
            let directory = URL(fileURLWithPath: ProcessInfo.processInfo.environment[brokenRelayDirectoryKey]!)
            try await checkBrokenRelay(CommandLine.arguments.last!, directory: directory)
            return
        }
        checkDiagnostics()
        try await checkMultipleConnections()
        let quotaDiagnostic = ProxyDiagnostic(scope: "root", code: "usage_limit_reached", at: "2026-09-22T00:00:00Z")
        precondition(quotaDiagnostic != nil && quotaDiagnostic!.title.contains("자동 전환"))
        let authenticationStore = DirectProxyStore()
        authenticationStore.ready = true
        func auth(_ value: [String: Any]) {
            authenticationStore.receiveAuthenticationState(try! JSONSerialization.data(withJSONObject: value))
        }
        let waiting: [String: Any] = ["slot":"b", "waiting":2, "refreshing":false, "canceled":false]
        let refreshing: [String: Any] = ["slot":"a", "waiting":1, "refreshing":true, "canceled":false]
        let canceled: [String: Any] = ["slot":"a", "waiting":0, "refreshing":true, "canceled":true]
        auth(["authentication":[waiting, refreshing], "token":"synthetic-secret"])
        precondition(authenticationStore.authenticationMessage!.contains("토큰 갱신 중"))
        precondition(authenticationStore.authenticationMessage!.contains("계정 B · 인증 대기 중"))
        precondition(!authenticationStore.diagnosticText.contains("synthetic-secret"))
        auth(["authentication":[canceled]])
        precondition(authenticationStore.authenticationMessage!.contains("취소 후 인증 갱신 마무리"))
        precondition(authenticationStore.authenticationGuidance!.contains("자동으로 다시 보내지 않습니다"))
        authenticationStore.ready = false
        precondition(authenticationStore.authenticationMessage == nil && authenticationStore.authenticationText.contains("미확인"))
        authenticationStore.ready = true
        for invalid: [String: Any] in [[:], ["authentication":NSNull()], ["authentication":[waiting, waiting]],
            ["authentication":[["slot":"synthetic-secret", "waiting":1, "refreshing":false, "canceled":false]]],
            ["authentication":[["slot":"a", "waiting":-1, "refreshing":false, "canceled":false]]],
            ["authentication":[["slot":"a", "waiting":1, "refreshing":false, "canceled":true]]],
            ["authentication":"synthetic-secret"]] {
            auth(invalid)
            precondition(authenticationStore.authentication == nil && authenticationStore.authenticationMessage == nil)
        }
        auth(["authentication":[]])
        precondition(authenticationStore.authenticationText == "인증 대기·갱신 없음")
        auth(["authentication":[refreshing]])
        authenticationStore.stop()
        precondition(authenticationStore.authentication == nil)
        print("PASS: authentication waiting/refresh/canceled completion, redaction, invalid and legacy snapshots, disconnect cleanup")
        var frames = ProxyEventFrames()
        let wire = Data("{\"text\":\"합성\"}\n{\"event\":\"probe_state\"}\n".utf8)
        var decoded: [Data] = []
        // A one-byte split also cuts inside multibyte Korean characters.
        for byte in wire { decoded += try frames.append(Data([byte])) }
        precondition(decoded.count == 2 && String(data: decoded[0], encoding: .utf8) == "{\"text\":\"합성\"}")
        var batch = ProxyEventFrames()
        let batched = try batch.append(wire)
        precondition(batched == decoded)
        let partial = try batch.append(Data("partial".utf8))
        precondition(partial.isEmpty)
        var oversized = ProxyEventFrames()
        let exact = try oversized.append(Data(repeating: 97, count: 8192))
        precondition(exact.isEmpty)
        let exactLine = try oversized.append(Data([10]))
        precondition(exactLine.first?.count == 8192)
        do {
            _ = try oversized.append(Data(repeating: 97, count: 8193))
            preconditionFailure("oversized relay record accepted")
        } catch ProxyEventFrames.Failure.oversized {}
        for code in ["request_body_timeout", "request_too_large", "request_unreadable", "credential_store_unavailable", "auxiliary_capacity_reached"] {
            precondition(ProxyDiagnostic(scope: "root", code: code, at: "2026-09-21T00:00:00Z") != nil)
        }
        let directory = FileManager.default.temporaryDirectory.appendingPathComponent("settings-check-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let config = directory.appendingPathComponent("config.toml")
        let original = "# preserve comments\nmodel = \"synthetic-model\"\n"
        try Data(original.utf8).write(to: config)
        let settings = CodexSettingsStore()
        settings.load(home: directory.path)
        precondition(settings.drafts["config.toml"] == original && !settings.dirty)
        precondition(settings.drafts["AGENTS.md"] == "")
        settings.drafts["config.toml"] = original + "model_reasoning_effort = \"high\"\n"
        precondition(settings.canSave && settings.save() && !settings.dirty)
        let savedConfig = try String(contentsOf: config, encoding: .utf8)
        precondition(savedConfig == settings.drafts["config.toml"])
        settings.selected = "AGENTS.md"
        settings.drafts["AGENTS.md"] = "# 합성 지침\n테스트 먼저 실행하기\n"
        precondition(settings.save())
        let attributes = try FileManager.default.attributesOfItem(atPath: directory.appendingPathComponent("AGENTS.md").path)
        precondition((attributes[.posixPermissions] as? NSNumber)?.intValue == 0o600)
        settings.selected = "config.toml"
        settings.drafts["config.toml"] = "draft"
        try Data("external edit".utf8).write(to: config)
        precondition(!settings.save() && settings.drafts["config.toml"] == "draft")
        let external = try String(contentsOf: config, encoding: .utf8)
        precondition(external == "external edit")
        settings.load(home: directory.path)
        precondition(!settings.dirty && settings.drafts["config.toml"] == external)
        try FileManager.default.removeItem(at: config)
        try FileManager.default.createSymbolicLink(at: config, withDestinationURL: directory.appendingPathComponent("AGENTS.md"))
        settings.load(home: directory.path)
        precondition(settings.drafts["config.toml"] == nil && !settings.save())
        settings.load(home: nil)
        precondition(settings.home == nil && settings.drafts.isEmpty && !settings.canSave)
        let command = DirectProxyStore.connectionCommand(home: "/private/tmp/synthetic home'quoted", resume: true)
        let shell = Process(), capture = Pipe()
        shell.executableURL = URL(fileURLWithPath: "/bin/sh")
        shell.arguments = ["-c", "codex() { printf '%s\\n' \"$CODEX_HOME\" \"$@\"; }; " + command]
        shell.standardOutput = capture
        try shell.run()
        let captured = capture.fileHandleForReading.readDataToEndOfFile()
        shell.waitUntilExit()
        precondition(shell.terminationStatus == 0)
        precondition(String(data: captured, encoding: .utf8) == "/private/tmp/synthetic home'quoted\nresume\n")
        print("PASS: file settings read, edit, private save, creation, conflict, symlink rejection and resume quoting")
        let store = DirectProxyStore(usageReadTimeout: 200_000_000)
        var usageCount = 0
        var unavailableCount = 0
        store.onUsage = { snapshot in
            precondition(snapshot.accounts.count == 5 && snapshot.accounts[0].state == "not_registered")
            precondition(snapshot.accounts[1].registered == true && snapshot.accounts[1].state == "ok")
            precondition(snapshot.accounts[1].usage?.primary.remainingPercent == 70)
            precondition(snapshot.accounts[1].usage?.secondary.remainingPercent == 30)
            usageCount += 1
        }
        store.onUsageUnavailable = { unavailableCount += 1 }
        store.start(helper: URL(fileURLWithPath: CommandLine.arguments[0]))
        try await waitFor { store.ready }
        precondition(store.canReadUsage)
        store.readUsage()
        precondition(store.usageRefreshing && !store.canReadUsage)
        store.readUsage() // Duplicate request must be suppressed.
        try await waitFor { usageCount == 1 && !store.usageRefreshing }
        precondition(store.usageRefreshMessage!.contains("사용량 확인 완료"))
        store.readUsage()
        try await waitFor { !store.usageRefreshing }
        precondition(store.usageRefreshMessage!.contains("시간 초과") && usageCount == 1 && store.canReadUsage)
        store.readUsage()
        try await waitFor { !store.usageRefreshing }
        precondition(store.usageRefreshMessage == "사용량 데이터 확인 실패" && usageCount == 1)
        store.readUsage()
        try await waitFor { usageCount == 2 && !store.usageRefreshing }
        precondition(store.usageRefreshMessage!.contains("사용량 확인 완료"))
        precondition(store.slot == "a" && store.canSelect("b") && !store.canSelect("a"))
        store.failed = true
        precondition(store.canSelect("b") && !store.canSelect("a"))
        store.canRecoverCurrent = true
        precondition(store.canSelect("a"))
        store.busy = true
        precondition(!store.canSelect("a") && !store.canSelect("b"))
        store.canRecoverCurrent = false
        store.busy = false
        store.select("b")
        precondition(store.pending && !store.canSelect("a"))
        try await waitFor { !store.pending }
        precondition(store.slot == "b" && store.connected && !store.busy)
        for slot in ["c", "d", "e"] {
            precondition(store.canSelect(slot))
            store.select(slot)
            try await waitFor { !store.pending }
            precondition(store.slot == slot)
        }
        precondition(!store.canSelect("f"))
        try await waitFor { store.canAbandonTurn }
        precondition(store.busy && !store.canSelect("a"))
        var rejectedShutdown: Bool?
        store.shutdownService { rejectedShutdown = $0 }
        try await waitFor { rejectedShutdown != nil }
        precondition(rejectedShutdown == false && store.ready && store.busy)
        store.abandonTurn()
        precondition(store.pending && !store.canAbandonTurn)
        try await waitFor { !store.pending }
        precondition(store.failed && !store.busy && store.canSelect("a") && !store.canAbandonTurn)
        store.prepareRecovery()
        precondition(store.pending)
        try await waitFor { !store.pending }
        precondition(store.slot == "b" && !store.failed && store.message.contains("CLI 입력 대기"))
        // A lost relay can leave the last observed busy bit set.
        store.ready = false; store.busy = true
        store.start(helper: URL(fileURLWithPath: CommandLine.arguments[0]))
        try await waitFor { store.ready }
        precondition(!store.busy)
        store.poll()
        var acceptedShutdown: Bool?
        store.shutdownService { acceptedShutdown = $0 }
        try await waitFor { acceptedShutdown != nil }
        precondition(acceptedShutdown == true && !store.stopping && !store.ready)
        var disconnectedShutdown: Bool?
        store.shutdownService { disconnectedShutdown = $0 }
        precondition(disconnectedShutdown == false)
        print("PASS: shutdown rejects active tools, waits for ACK and disconnect, preserves session and rejects unknown state")
        store.stop()
        try await Task.sleep(nanoseconds: 100_000_000)
        precondition(!store.ready && store.home == nil && !store.canSelect("a"))
        precondition(!store.canReadUsage && !store.usageRefreshing)
        store.readUsage()
        precondition(!store.usageRefreshing && store.usageRefreshMessage!.contains("연결 후"))
        precondition(unavailableCount >= 2)
        print("PASS: direct proxy selection; remote refresh protocol, duplicate suppression, timeout, old reply isolation, malformed reply, recovery and disconnect (synthetic)")
        try await checkBrokenRelaySubprocesses()
    }
}
