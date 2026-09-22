import AppKit
import Darwin
import Foundation
import SwitcherUIModel

// Preserve UTF-8 bytes across pipe reads. Reject overlong records before decoding.
struct ProxyEventFrames {
    enum Failure: Error { case oversized }
    private var pending = Data()
    mutating func append(_ chunk: Data) throws -> [Data] {
        var lines: [Data] = []
        for byte in chunk {
            if byte == 10 {
                lines.append(pending)
                pending.removeAll(keepingCapacity: true)
            } else {
                guard pending.count < 8192 else { throw Failure.oversized }
                pending.append(byte)
            }
        }
        return lines
    }
}

/// Owns a disposable relay. The independent service retains the CLI and proxy.
@MainActor
final class DirectProxyStore: ObservableObject {
    @Published private(set) var connections: [ProxyConnection] = []
    @Published private(set) var selectedConnection = "1"
    @Published private(set) var conversation: String?
    var allBusy: Bool { busy || connections.contains { $0.busy || !$0.ready } }
    var canAddConnection: Bool { ready && !pending && !stopping && !connections.isEmpty && connections.count < 5 }
    var canChooseConnection: Bool { ready && !pending && !stopping && !connections.isEmpty }

    func chooseConnection(_ id: String) {
        guard canChooseConnection, id != selectedConnection, connections.contains(where: { $0.id == id && $0.ready }) else { return }
        control(["action": "connection_select", "connection_id": id])
    }
    func addConnection() {
        guard canAddConnection else { return }
        control(["action": "connection_create"])
    }
    // The coordinator serializes selection and all observations. Never carry
    // another conversation's profile, revision, failure or diagnostics across it.
    @discardableResult func receiveConnections(_ data: Data) -> Bool {
        struct Event: Decodable { let event: String; let selected: String?; let connections: [ProxyConnection]?; let limit: Int? }
        guard let e = try? JSONDecoder().decode(Event.self, from: data), e.event == "connection_list" else { return false }
        guard e.limit == 5, let rows = e.connections, !rows.isEmpty, rows.count <= 5,
              Set(rows.map(\.id)).count == rows.count, rows.allSatisfy({ ["1", "2", "3", "4", "5"].contains($0.id) }),
              let selected = e.selected, rows.contains(where: { $0.id == selected }) else {
            ready = false; connections = []; return true
        }
        connections = rows
        if selected != selectedConnection {
            selectedConnection = selected
            ready = false; home = nil; conversation = nil; revision = 0
            busy = false; failed = false; connected = false; canRecoverCurrent = false; toolWaiting = false
            auxiliaryCount = nil; auxiliaryLimit = nil; authentication = nil
            rootDiagnostic = nil; auxiliaryDiagnostic = nil; diagnosticsFromPreviousConnection = false
            lastRead = .distantPast; waitingStatus = false; uncertain = false
        }
        return true
    }
    @Published var slot = "a"
    @Published var busy = false
    @Published var failed = false
    @Published var canRecoverCurrent = false
    @Published var connected = false
    @Published var ready = false
    @Published var starting = false
    @Published var pending = false
    @Published private(set) var usageRefreshing = false
    @Published private(set) var usageRefreshMessage: String?
    @Published private(set) var helperBuild: String?
    @Published private(set) var proxyBuild: String?
    @Published private(set) var proxyProtocol: Int?
    @Published private(set) var rootDiagnostic: ProxyDiagnostic?
    @Published private(set) var auxiliaryDiagnostic: ProxyDiagnostic?
    @Published private(set) var diagnosticsFromPreviousConnection = false
    @Published private(set) var auxiliaryCount: Int?
    @Published private(set) var auxiliaryLimit: Int?
    @Published private(set) var authentication: [ProxyAuthentication]?
    var authenticationMessage: String? {
        guard ready, let authentication, !authentication.isEmpty else { return nil }
        return authentication.map(\.title).joined(separator: " · ")
    }
    var authenticationGuidance: String? {
        guard authenticationMessage != nil else { return nil }
        if authentication?.contains(where: { $0.canceled }) == true {
            return "취소된 요청의 인증 갱신을 안전하게 마무리하고 있습니다. 모델 요청은 자동으로 다시 보내지 않습니다."
        }
        return "인증 확인이 끝나면 요청을 진행합니다. 이미 시작한 토큰 갱신은 요청을 취소해도 결과 저장까지 마칩니다."
    }
    var authenticationText: String {
        guard ready, let authentication else { return "인증 상태: 미확인" }
        return authentication.isEmpty ? "인증 대기·갱신 없음" : authentication.map(\.title).joined(separator: " · ")
    }

    // Called only for accepted state events after the relay generation check.
    // Malformed/legacy observations clear the display without altering busy state.
    func receiveAuthenticationState(_ data: Data) {
        struct Event: Decodable { let authentication: [ProxyAuthentication]? }
        guard let event = try? JSONDecoder().decode(Event.self, from: data),
              let rows = event.authentication, rows.count <= AccountSlots.all.count,
              Set(rows.map(\.slot)).count == rows.count,
              rows.allSatisfy({ $0.isValid }) else { authentication = nil; return }
        authentication = rows.sorted { $0.slot < $1.slot }
    }
    var auxiliaryCapacityText: String {
        guard let count = auxiliaryCount, let limit = auxiliaryLimit else { return "보조 작업 용량: 미확인" }
        return "보조 작업 기록: \(count)/\(limit) · 새 작업 \(limit - count)개 가능"
    }
    var diagnostics: [ProxyDiagnostic] { [rootDiagnostic, auxiliaryDiagnostic].compactMap { $0 } }
    var appVersion: String {
        guard let value = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String,
              !value.isEmpty, value.count <= 40, value.allSatisfy({ "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ.-".contains($0) }) else { return "개발 빌드" }
        return value
    }
    var buildWarning: String? {
        guard ready else { return nil }
        if let proxyProtocol, proxyProtocol != 1 { return "프록시 제어 버전을 확인해야 합니다. 모든 작업을 마친 뒤 앱·프록시를 종료하고 다시 실행하세요." }
        guard let helperBuild, let proxyBuild else { return "실행 빌드를 확인할 수 없습니다. 구버전 프록시일 수 있으므로 작업을 마친 뒤 앱·프록시를 종료하고 다시 실행하세요." }
        return helperBuild == proxyBuild ? nil : "실행 중인 프록시와 연결용 helper의 빌드가 다릅니다. 모든 작업을 마친 뒤 앱·프록시를 종료하고 다시 실행하세요."
    }
    var diagnosticText: String {
        let state = ready ? (busy ? "작업 중" : "연결됨") : "연결 미확인"
        let rows = diagnostics.map { "\($0.scopeLabel): \($0.code) · \($0.at)" }
        return (["Codex Switcher \(appVersion)", "연결: \(state)",
                 "연결 시 helper: \(helperBuild ?? "미확인")", "실행 프록시: \(proxyBuild ?? "미확인")",
                 "프록시 제어 버전: \(proxyProtocol.map(String.init) ?? "미확인")", auxiliaryCapacityText, authenticationText,
                 diagnosticsFromPreviousConnection ? "오류 기록: 이전 연결" : "오류 기록: 현재 연결"] + rows).joined(separator: "\n")
    }
    func copyDiagnostics() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(diagnosticText, forType: .string)
    }

    // Called after the relay generation check. Whitelist again at the UI boundary.
    @discardableResult func receiveDiagnosticEvent(_ data: Data) -> Bool {
        struct Event: Decodable { let event: String; var build_id: String?; var protocol_version: Int?; var scope: String?; var code: String?; var at: String? }
        guard let e = try? JSONDecoder().decode(Event.self, from: data) else { return false }
        if e.event == "helper_build" || e.event == "probe_ready" {
            let id = e.build_id.flatMap { value in value.count == 64 && value.allSatisfy { "0123456789abcdef".contains($0) } ? value : nil }
            if e.event == "helper_build" { helperBuild = id; return true }
            proxyBuild = id
            proxyProtocol = e.protocol_version.flatMap { (0...999).contains($0) ? $0 : nil }
            return false // probe_ready also supplies the existing connection profile.
        }
        guard e.event == "probe_diagnostic" else { return false }
        guard let scope = e.scope, ["root", "auxiliary"].contains(scope), let code = e.code,
              let at = e.at, let value = ProxyDiagnostic(scope: scope, code: code, at: at) else { return true }
        if scope == "root" { rootDiagnostic = value } else { auxiliaryDiagnostic = value }
        diagnosticsFromPreviousConnection = false
        return true
    }
    var canReadUsage: Bool { ready && child != nil && !usageRefreshing }
    private var usageReadID = UUID()
    private var usageSequence: UInt64 = 0
    private let usageReadTimeout: UInt64

    init(usageReadTimeout: UInt64 = 30_000_000_000) {
        self.usageReadTimeout = usageReadTimeout
    }
    @Published private(set) var toolWaiting = false
    var canAbandonTurn: Bool { ready && connected && toolWaiting && !pending }
    @Published var home: String?
    var onUsage: ((UsageSnapshot) -> Void)?
    var onUsageUnavailable: (() -> Void)?
    @Published var message = "로컬 도구 프록시 연결 전"
    private var child: Process?
    private var input: Pipe?
    private var generation = UUID()
    private var revision: UInt64 = 0
    private var lastRead = Date.distantPast
    private var waitingStatus = false
    private var uncertain = false

    func canSelect(_ target: String) -> Bool {
        ready && !busy && !pending && (target != slot || (failed && canRecoverCurrent)) && AccountSlots.all.contains(target)
    }

    @Published private(set) var stopping = false
    private var shutdownAccepted = false
    private var shutdownCompletion: ((Bool) -> Void)?
    private var shutdownID = UUID()

    func shutdownService(completion: @escaping (Bool) -> Void) {
        guard !stopping else { return }
        guard ready, !starting, !pending else {
            message = "프록시 연결 상태를 확인한 뒤 다시 종료하세요."
            completion(false); return
        }
        stopping = true; shutdownAccepted = false
        shutdownCompletion = completion
        let id = UUID(); shutdownID = id
        guard send(["action": "shutdown", "new_session": false]) else {
            finishShutdown(false); return
        }
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 8_000_000_000)
            guard let self, self.stopping, self.shutdownID == id else { return }
            self.message = "프록시 종료를 확인하지 못했습니다. 연결 상태를 확인하세요."
            self.finishShutdown(false)
        }
    }

    private func finishShutdown(_ success: Bool) {
        let completion = shutdownCompletion
        shutdownCompletion = nil; stopping = false; shutdownAccepted = false
        completion?(success)
    }

    func start(helper: URL) {
        // A stale busy observation must not prevent reconnecting a dead relay.
        // Detaching the relay never cancels a live daemon/model request.
        guard !stopping && !(ready && busy) && !starting else { return }
        stop()
        rootDiagnostic = nil; auxiliaryDiagnostic = nil; diagnosticsFromPreviousConnection = false
        let run = UUID(); generation = run
        let process = Process(), source = Pipe(), sink = Pipe()
        process.executableURL = helper
        process.arguments = ["proxy-connect"]
        process.standardInput = source; process.standardOutput = sink
        process.standardError = FileHandle.nullDevice
        // A relay may exit before its EOF callback reaches the main actor.
        // Suppress SIGPIPE only on this writer so EPIPE reaches send's catch;
        // do not change the app's or child processes' global signal handling.
        guard fcntl(source.fileHandleForWriting.fileDescriptor, F_SETNOSIGPIPE, 1) == 0 else {
            message = "프록시 제어 연결 준비 실패 · 다시 연결해 주세요"
            return
        }
        do { try process.run() } catch { message = "프록시 실행 실패 · helper 확인 필요"; return }
        try? source.fileHandleForReading.close(); try? sink.fileHandleForWriting.close()
        child = process; input = source; starting = true
        message = "로컬 도구 프록시 시작 중…"
        Task.detached { [weak self] in
            defer { try? sink.fileHandleForReading.close() }
            do {
                var frames = ProxyEventFrames()
                var buffer = [UInt8](repeating: 0, count: 4096)
                while true {
                    let count = buffer.withUnsafeMutableBytes {
                        Darwin.read(sink.fileHandleForReading.fileDescriptor, $0.baseAddress, $0.count)
                    }
                    if count < 0 && errno == EINTR { continue }
                    if count <= 0 { break }
                    for line in try frames.append(Data(buffer.prefix(count))) {
                        await self?.receive(line, run: run)
                    }
                }
            } catch {}
            await self?.ended(run: run)
        }
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 15_000_000_000)
            guard let self, self.generation == run, self.starting else { return }
            self.stop(); self.message = "프록시 연결 시간 초과 · helper 확인 후 다시 연결"
        }
    }

    private func receive(_ data: Data, run: UUID) {
        guard generation == run else { return }
        if receiveConnections(data) { return }
        // Ignore a tagged observation for another connection, even at UI handoff.
        if let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
           let id = object["connection_id"] as? String, id != selectedConnection { return }
        if receiveDiagnosticEvent(data) { return }
        struct Event: Decodable {
            let event: String
            var slot: String?; var busy: Bool?; var failed: Bool?; var connected: Bool?
            var revision: UInt64?; var accepted: Bool?; var codex_home: String?
            var conversation: String?
            var can_abandon_turn: Bool?
            var can_recover_current: Bool?
            var request_id: UInt64?; var status: String?; var succeeded: Bool?
            var auxiliary_count: Int?; var auxiliary_limit: Int?
        }
        guard let e = try? JSONDecoder().decode(Event.self, from: data) else { return }
        if e.event == "connection_result" {
            pending = false
            if e.accepted != true { message = "연결 변경 거절 · 현재 상태를 다시 확인하세요" }
            return
        }
        if e.event == "probe_shutdown", stopping {
            if e.accepted == true { shutdownAccepted = true }
            else {
                message = "Codex 요청이나 도구가 실행 중입니다. 모든 작업을 마친 뒤 다시 종료하세요."
                finishShutdown(false)
            }
            return
        }
        if e.event == "usage_refresh" {
            guard usageRefreshing, e.request_id == usageSequence else { return }
            switch e.status {
            case "started": usageRefreshMessage = "사용량 조회 중…"
            case "finished":
                finishUsageRead(e.succeeded == true ? "사용량 확인 완료 · 다음 자동 조회는 60초 후" : "일부 계정 조회 실패 · 이전 값은 오래된 정보로 표시합니다.")
            case "cooldown": finishUsageRead("방금 조회했습니다. 5초 후 다시 확인할 수 있습니다.")
            case "busy": finishUsageRead("이미 사용량을 조회 중입니다. 결과가 자동 반영됩니다.")
            default: finishUsageRead("사용량 조회 요청 거절")
            }
            return
        }
        if e.event == "usage_snapshot" {
            if let snapshot = try? UsageSnapshot.decode(data) {
                onUsage?(snapshot)
            } else {
                finishUsageRead("사용량 데이터 확인 실패")
                onUsageUnavailable?()
            }
            return
        }
        if e.event == "probe_ready", let path = e.codex_home, path.hasPrefix("/") {
            home = path; return
        }
        guard ["probe_state", "probe_selection", "probe_abandonment"].contains(e.event), let nextSlot = e.slot,
              AccountSlots.all.contains(nextSlot), let nextBusy = e.busy, let nextFailed = e.failed,
              let nextConnected = e.connected, let nextRevision = e.revision else { return }
        conversation = e.conversation.flatMap { UUID(uuidString: $0)?.uuidString.lowercased() }
        slot = nextSlot; busy = nextBusy; failed = nextFailed; connected = nextConnected
        receiveAuthenticationState(data)
        if let count = e.auxiliary_count, let limit = e.auxiliary_limit,
           (1...128).contains(limit), (0...limit).contains(count) {
            auxiliaryCount = count; auxiliaryLimit = limit
        } else { auxiliaryCount = nil; auxiliaryLimit = nil }
        canRecoverCurrent = e.can_recover_current == true && nextFailed && !nextBusy
        toolWaiting = e.can_abandon_turn == true && nextBusy
        revision = nextRevision; lastRead = Date(); waitingStatus = false
        ready = home != nil && !uncertain; starting = false
        if e.event == "probe_selection" || e.event == "probe_abandonment" {
            uncertain = false; ready = home != nil
            pending = false
            if e.event == "probe_abandonment" {
                message = e.accepted == true ? "중단 확인 · 다른 계정을 선택한 뒤 CLI에 새 지시를 입력하세요" : "잠금 해제 거절 · 요청 진행 여부를 다시 확인하세요"
            } else {
                message = e.accepted == true ? "다음 요청은 \(slot.uppercased()) · CLI 입력 대기" : "전환 거절 · 상태가 바뀌었거나 계정 사용 불가"
            }
        } else if uncertain { message = "전환 결과 미확인 · 명령을 다시 보내지 않습니다" }
        else if failed { message = canRecoverCurrent ? "요청 중단 · 계정의 복구 준비를 누른 뒤 CLI에 새 지시를 입력하세요" : "요청 실패 · 다른 계정 수동 선택 후 새 입력 가능" }
        else if toolWaiting { message = "계정 \(slot.uppercased()) · CLI 도구 결과 대기" }
        else if busy { message = "계정 \(slot.uppercased()) 요청 처리 중" }
        else { message = connected ? "계정 \(slot.uppercased()) · CLI 입력 대기" : "CLI 연결 명령을 복사해 터미널에서 실행하세요" }
    }

    func poll() {
        guard child != nil, !starting, !stopping else { return }
        if Date().timeIntervalSince(lastRead) > 4 {
            authentication = nil
            ready = false; message = "프록시 상태 미확인 · 전환 차단"
            if usageRefreshing { finishUsageRead("프록시 연결 미확인 · 사용량 조회 실패") }
            onUsageUnavailable?()
        }
        guard !waitingStatus else { return }
        waitingStatus = true
        send(["action": "status"])
    }

    func select(_ target: String) {
        guard canSelect(target) else { return }
        control(["action": "select", "slot": target, "revision": revision])
    }

    func prepareRecovery() {
        guard ready, failed, !busy, !pending else { return }
        control(["action": "recover", "revision": revision])
    }

    func abandonTurn() {
        guard canAbandonTurn else { return }
        control(["action": "abandon_turn", "slot": slot, "revision": revision])
    }

    private func control(_ command: [String: Any]) {
        pending = true
        guard send(command) else { return }
        let run = generation
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 3_000_000_000)
            guard let self, self.generation == run, self.pending else { return }
            self.uncertain = true
            self.ready = false
            self.message = "전환 결과 미확인 · 명령을 다시 보내지 않습니다"
        }
    }

    @discardableResult
    private func send(_ object: [String: Any]) -> Bool {
        var command = object
        if !connections.isEmpty && command["connection_id"] == nil { command["connection_id"] = selectedConnection }
        guard let handle = input?.fileHandleForWriting, let data = try? JSONSerialization.data(withJSONObject: command) else { return false }
        do { try handle.write(contentsOf: data + Data([10])); return true }
        catch {
            // Detach only our relay; never stop the daemon or replay the command.
            // Rotating generation also discards its queued events/timeouts.
            stop()
            message = "프록시 제어 연결 끊김 · 다시 연결해 상태 확인"
            if stopping { finishShutdown(false) }
            return false
        }
    }

    func readUsage() {
        guard !usageRefreshing else { return }
        guard canReadUsage else {
            finishUsageRead("프록시 연결 후 사용량을 확인할 수 있습니다.")
            onUsageUnavailable?()
            return
        }
        usageRefreshing = true
        usageRefreshMessage = "사용량 조회 요청 중…"
        let request = UUID(), run = generation
        usageReadID = request
        usageSequence += 1
        guard send(["action":"usage_refresh", "request_id":usageSequence]) else {
            finishUsageRead("프록시 제어 연결 끊김 · 사용량 조회 실패")
            onUsageUnavailable?()
            return
        }
        Task { [weak self, usageReadTimeout] in
            try? await Task.sleep(nanoseconds: usageReadTimeout)
            guard let self, self.generation == run, self.usageReadID == request, self.usageRefreshing else { return }
            self.finishUsageRead("사용량 응답 시간 초과 · 자동 재요청하지 않습니다.")
            self.onUsageUnavailable?()
        }
    }

    private func finishUsageRead(_ text: String) {
        usageReadID = UUID()
        usageRefreshing = false
        usageRefreshMessage = text
    }

    func accountChanged(_ slot: String, changing: Bool) {
        send(["action":changing ? "account_changing" : "account_changed", "slot":slot])
    }

    static func connectionCommand(home: String, resume: Bool, conversation: String? = nil) -> String {
        let quoted = "'" + home.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        let bound = conversation.flatMap { UUID(uuidString: $0)?.uuidString.lowercased() }
        return "CODEX_HOME=\(quoted) codex" + (resume ? " resume" + (bound.map { " " + $0 } ?? "") : "")
    }

    func copyCommand(resume: Bool = false) {
        guard ready, !pending, let home else { return }
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(Self.connectionCommand(home: home, resume: resume, conversation: conversation), forType: .string)
    }

    private func ended(run: UUID) {
        guard generation == run else { return }
        let stopped = stopping && shutdownAccepted
        stop()
        message = stopped ? "앱과 프록시 종료 준비 완료" : "프록시 제어 연결 종료 · 다시 연결해 상태 확인"
        if stopping { finishShutdown(stopped) }
    }

    func stop() {
        finishUsageRead("프록시 연결 전")
        onUsageUnavailable?()
        generation = UUID()
        connections = []; selectedConnection = "1"; conversation = nil
        helperBuild = nil; proxyBuild = nil; proxyProtocol = nil
        auxiliaryCount = nil; auxiliaryLimit = nil
        authentication = nil
        diagnosticsFromPreviousConnection = !diagnostics.isEmpty
        let owned = child; child = nil
        try? input?.fileHandleForWriting.close(); input = nil
        ready = false; starting = false; pending = false; busy = false; failed = false; canRecoverCurrent = false; toolWaiting = false
        connected = false; home = nil; waitingStatus = false; uncertain = false; lastRead = .distantPast
        if let owned {
            DispatchQueue.global().asyncAfter(deadline: .now() + 2) { if owned.isRunning { owned.terminate() } }
            DispatchQueue.global().asyncAfter(deadline: .now() + 4) { if owned.isRunning { kill(owned.processIdentifier, SIGKILL) } }
        }
    }
}

struct ProxyConnection: Decodable, Identifiable {
    let id: String
    let ready: Bool
    let busy: Bool
    let failed: Bool
    var title: String { "연결 \(id) · " + (!ready ? "준비 중" : busy ? "작업 중" : failed ? "실패" : "대기") }
}

struct ProxyAuthentication: Decodable {
    let slot: String
    let waiting: Int
    let refreshing: Bool
    let canceled: Bool
    var isValid: Bool {
        AccountSlots.all.contains(slot) && (0...1_000_000).contains(waiting)
            && (refreshing || waiting > 0) && (!canceled || refreshing)
    }
    var title: String {
        let prefix = "계정 \(slot.uppercased()) · "
        if canceled { return prefix + "취소 후 인증 갱신 마무리 중" }
        if refreshing { return prefix + "토큰 갱신 중" + (waiting > 0 ? " (인증 대기 \(waiting)건)" : "") }
        return prefix + "인증 대기 중 (\(waiting)건)"
    }
}

/// Edits only the two explicitly named files in the selected CLI home.
@MainActor
final class CodexSettingsStore: ObservableObject {
    static let files = ["config.toml", "AGENTS.md"]
    @Published var selected = "config.toml"
    @Published var drafts: [String: String] = [:]
    @Published private(set) var home: String?
    @Published private(set) var message: String?
    private var originals: [String: Data] = [:]
    private var loaded: Set<String> = []
    private var existed: Set<String> = []
    var dirty: Bool { Self.files.contains { isDirty($0) } }
    var canSave: Bool { loaded.contains(selected) && (isDirty(selected) || !existed.contains(selected)) }
    func isDirty(_ file: String) -> Bool {
        loaded.contains(file) && Data((drafts[file] ?? "").utf8) != originals[file]
    }
    private func read(_ url: URL) throws -> Data? {
        let fm = FileManager.default
        do {
            let attributes = try fm.attributesOfItem(atPath: url.path)
            guard attributes[.type] as? FileAttributeType == .typeRegular,
                  (attributes[.size] as? NSNumber)?.intValue ?? Int.max <= 1_048_576 else {
                throw CocoaError(.fileReadUnknown)
            }
            return try Data(contentsOf: url)
        } catch let error as NSError where error.domain == NSCocoaErrorDomain && [NSFileReadNoSuchFileError, NSFileNoSuchFileError].contains(error.code) {
            return nil
        }
    }
    func load(home: String?) {
        self.home = home
        drafts = [:]; originals = [:]; loaded = []; existed = []; message = nil
        guard let home else { return }
        for file in Self.files {
            do {
                let data = try read(URL(fileURLWithPath: home).appendingPathComponent(file))
                guard let text = String(data: data ?? Data(), encoding: .utf8) else { throw CocoaError(.fileReadInapplicableStringEncoding) }
                drafts[file] = text; originals[file] = data ?? Data(); loaded.insert(file)
                if data != nil { existed.insert(file) }
            } catch { message = "\(file)을 읽지 못했습니다. 일반 UTF-8 파일과 접근 권한을 확인하세요." }
        }
    }
    @discardableResult
    func save() -> Bool {
        guard let home, loaded.contains(selected) else { return false }
        let url = URL(fileURLWithPath: home).appendingPathComponent(selected)
        do {
            let current = try read(url)
            guard (current != nil) == existed.contains(selected), current ?? Data() == originals[selected] else {
                message = "파일이 외부에서 변경됐습니다. 초안을 따로 복사한 뒤 다시 불러오세요."
                return false
            }
            let data = Data((drafts[selected] ?? "").utf8)
            guard data.count <= 1_048_576 else { message = "파일은 1MB 이하로 저장하세요."; return false }
            // Atomic replacement and private permissions, including newly created AGENTS.md.
            let temporary = url.deletingLastPathComponent().appendingPathComponent(".codex-edit-" + UUID().uuidString)
            defer { try? FileManager.default.removeItem(at: temporary) }
            guard FileManager.default.createFile(atPath: temporary.path, contents: data, attributes: [.posixPermissions: 0o600]),
                  rename(temporary.path, url.path) == 0 else { throw CocoaError(.fileWriteUnknown) }
            originals[selected] = data; existed.insert(selected)
            message = "\(selected)을 저장했습니다. 다음 CLI 실행부터 사용하세요."
            return true
        } catch {
            message = "저장하지 못했습니다. 파일 상태와 폴더 접근 권한을 확인하세요."
            return false
        }
    }
}

struct ProxyDiagnostic: Identifiable {
    let scope: String
    let code: String
    let at: String
    var id: String { scope }
    var scopeLabel: String { scope == "root" ? "메인 대화" : "보조 작업" }
    static let descriptions: [String: (String, String)] = [
        "request_canceled": ("모델 전송 전에 요청이 취소됐습니다", "CLI 입력창으로 돌아온 뒤 필요한 경우 새 지시를 입력하세요. 취소된 요청은 자동으로 다시 보내지 않습니다."),
        "request_body_timeout": ("요청 본문 수신 시간이 초과됐습니다", "CLI 연결 상태를 확인한 뒤 새 지시를 입력하세요. 중단된 요청은 자동으로 다시 보내지 않습니다."),
        "request_too_large": ("요청이 4 MiB 제한을 초과했습니다", "이미지나 도구 결과 크기를 줄이고 새 지시를 입력하세요."),
        "request_unreadable": ("요청 본문을 끝까지 받지 못했습니다", "CLI 연결 상태를 확인하고 새 지시를 입력하세요."),
        "credential_store_unavailable": ("계정 인증 저장소를 읽거나 저장하지 못했습니다", "macOS Keychain 접근 상태를 확인하세요. 기존 대화를 지울 필요는 없습니다."),
        "auxiliary_capacity_reached": ("보조 작업 기록 한도에 도달했습니다", "일반 재시작으로 한도가 초기화되지 않습니다. 모든 작업을 마친 뒤 README의 새 연결 준비 절차를 진행하세요."),
        "history_owner_unavailable": ("이전 이력의 인증 신원이 다릅니다", "CLI 작업을 마치고 새 지시를 입력하세요. 같은 오류가 반복되면 이전 암호화 이력을 사용하지 않는 새 대화가 필요합니다."),
        "compaction_owner_unavailable": ("암호화 압축 이력의 소유권을 확인할 수 없습니다", "재로그인·확인되지 않은 인증 변경·구버전 이력 때문일 수 있습니다. 기존 기록을 보존하고 새 대화로 시작하세요."),
        "auxiliary_credential_changed": ("보조 작업의 인증이 변경됐습니다", "중단된 작업을 재전송하지 말고 새 리뷰 또는 새 보조 작업을 명시적으로 시작하세요."),
        "authentication_expired": ("인증이 만료됐거나 갱신이 필요합니다", "모든 작업을 마친 뒤 해당 계정에 다시 로그인하세요. 실패한 요청은 자동으로 다시 보내지 않습니다."),
        "account_unavailable": ("사용 가능한 계정을 확인할 수 없습니다", "계정 등록 상태와 사용량을 확인하세요."),
        "session_unavailable": ("요청 인증을 확인하지 못했습니다", "계정 상태를 확인하세요. 이 코드만으로 정확한 인증 실패 원인을 확정할 수 없습니다."),
        "cli_identity_invalid": ("CLI 대화 식별 형식을 확인하지 못했습니다", "아래 프록시 빌드와 CLI 호환성을 확인하세요. 새 helper를 빌드했다면 작업 종료 후 앱·프록시를 다시 실행하세요."),
        "conversation_changed": ("다른 대화가 같은 연결을 사용했습니다", "이 연결의 원래 대화를 재개하거나 새 대화를 위한 별도 연결을 사용하세요."),
        "previous_request_failed": ("이전 실패로 요청이 잠겨 있습니다", "이 코드는 최초 실패 원인이 아닙니다. CLI 입력창으로 돌아온 뒤 복구 준비를 하고 새 지시를 입력하세요."),
        "new_input_required": ("새 사용자 입력을 기다리고 있습니다", "복구 준비 후 CLI에 새 지시를 입력하세요. 이전 요청은 재전송하지 않습니다."),
        "auxiliary_compaction_unsupported": ("보조 작업의 암호화 압축은 지원하지 않습니다", "필요한 내용을 정리해 새 보조 작업을 시작하세요."),
        "tool_history_incomplete": ("도구 호출과 결과 이력이 완전하지 않습니다", "CLI 도구가 종료됐는지 확인하세요. 새 입력으로도 반복되면 새 대화가 필요합니다."),
        "history_unsupported": ("지원하지 않는 CLI 이력 형식입니다", "프록시 빌드와 CLI 기능 호환성을 확인하세요. 이 오류만으로 어떤 이력 항목인지 확정할 수 없습니다."),
        "history_type_unsupported": ("지원하지 않는 종류의 이력 항목입니다", "프록시 빌드와 CLI 기능 호환성을 확인하세요. 진단 코드에는 항목 내용이 포함되지 않습니다."),
        "message_shape_unsupported": ("메시지 필드 형식이 지원 범위를 벗어났습니다", "CLI와 프록시의 호환성을 확인하세요."),
        "message_content_unsupported": ("메시지 내용 형식이 지원 범위를 벗어났습니다", "이미지·파일 등 사용한 기능의 지원 여부를 확인하세요."),
        "agent_message_shape_unsupported": ("에이전트 메시지 형식이 지원 범위를 벗어났습니다", "실행 중인 프록시에 최신 이력 형식 지원이 적용됐는지 확인하세요."),
        "reasoning_shape_unsupported": ("내부 추론 이력 형식을 확인하지 못했습니다", "CLI와 프록시의 호환성을 확인하세요. 이력 내용은 진단에 포함하지 않습니다."),
        "tool_declaration_unsupported": ("지원하지 않는 도구 선언입니다", "서버 실행 도구 등 현재 지원하지 않는 기능을 사용 중인지 확인하세요."),
        "tool_call_invalid": ("도구 호출 이력 형식을 확인하지 못했습니다", "CLI 작업 상태와 기능 호환성을 확인하세요."),
        "tool_output_invalid": ("도구 결과 이력 형식을 확인하지 못했습니다", "도구 결과가 지원하는 텍스트 형식인지 확인하세요."),
        "checkpoint_unavailable": ("대화 복구 상태를 저장하지 못했습니다", "로컬 저장 공간과 앱 데이터 접근 권한을 확인한 뒤 다시 연결하세요."),
        "request_busy": ("기존 요청 처리가 끝나지 않았습니다", "진행 중인 요청과 도구가 끝날 때까지 기다리세요."),
        "account_busy": ("계정 작업이 진행 중입니다", "로그인·로그아웃이 끝난 뒤 상태를 다시 확인하세요."),
        "duplicate_followup": ("중복 후속 요청을 차단했습니다", "CLI 상태를 확인하고 필요한 경우 새 지시를 입력하세요."),
        "usage_limit_reached": ("사용량 한도에 도달했고 자동 전환을 완료하지 못했습니다", "다른 계정의 최신 잔여량과 이력 상태를 확인하세요. 암호화 압축 이력은 다른 계정으로 옮길 수 없습니다."),
        "upstream_rate_limited": ("서버가 한도 또는 요청 제한을 반환했습니다", "계정 사용량을 확인하고 작업 종료 후 복구를 준비하세요."),
        "upstream_auth_rejected": ("서버가 인증 또는 접근을 거절했습니다", "계정 로그인 상태와 사용 권한을 확인하세요."),
        "response_interrupted": ("응답이 중단됐거나 형식을 확인하지 못했습니다", "네트워크와 CLI 상태를 확인한 뒤 새 지시로 이어가세요. 기존 요청은 자동 재전송하지 않습니다."),
        "upstream_rejected": ("서버가 요청을 거절했습니다", "지원 기능과 계정 상태를 확인하세요. 서버 원문은 진단에 수집하지 않습니다."),
        "request_rejected": ("프록시가 요청을 거절했습니다", "연결과 계정 상태, 지원 기능을 확인하세요.")
    ]
    init?(scope: String, code: String, at: String) {
        guard ["root", "auxiliary"].contains(scope), Self.descriptions[code] != nil,
              at.count == 20, let date = ISO8601DateFormatter().date(from: at) else { return nil }
        self.scope = scope; self.code = code
        self.at = ISO8601DateFormatter().string(from: date)
    }
    var title: String { Self.descriptions[code]!.0 }
    var guidance: String { Self.descriptions[code]!.1 }
}
