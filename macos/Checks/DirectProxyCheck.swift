import Foundation

@main
struct DirectProxyCheck {
    @MainActor static func main() async throws {
        if CommandLine.arguments.contains("proxy-connect") {
            func emit(_ value: [String: Any]) {
                let data = try! JSONSerialization.data(withJSONObject: value)
                try! FileHandle.standardOutput.write(contentsOf: data + Data([10]))
            }
            var slot = "a", revision = 0, toolWaiting = false, failed = false, usageReads = 0
            func state(_ event: String, _ accepted: Bool = false) {
                emit(["event":event,"slot":slot,"busy":toolWaiting,"failed":failed,"connected":true,"revision":revision,"accepted":accepted,"can_abandon_turn":toolWaiting])
            }
            emit(["event":"probe_ready","codex_home":"/private/tmp/synthetic-unused-home"])
            state("probe_state")
            while let line = readLine(), let data = line.data(using: .utf8),
                  let command = try JSONSerialization.jsonObject(with: data) as? [String: Any] {
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
        func waitFor(_ condition: () -> Bool) async throws {
            for _ in 0..<100 {
                if condition() { return }
                try await Task.sleep(nanoseconds: 20_000_000)
            }
            fatalError("direct proxy check timed out")
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
    }
}
