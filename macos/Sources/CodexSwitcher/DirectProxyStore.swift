import AppKit
import Foundation
import SwitcherUIModel

/// Controls only its own helper through inherited pipes. Never launches Codex.
@MainActor
final class DirectProxyStore: ObservableObject {
    @Published var slot = "a"
    @Published var busy = false
    @Published var failed = false
    @Published var connected = false
    @Published var ready = false
    @Published var starting = false
    @Published var pending = false
    @Published private(set) var usageRefreshing = false
    @Published private(set) var usageRefreshMessage: String?
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
        ready && !busy && !pending && target != slot && AccountSlots.all.contains(target)
    }

    func start(helper: URL) {
        guard !busy && !starting else { return }
        stop()
        let run = UUID(); generation = run
        let process = Process(), source = Pipe(), sink = Pipe()
        process.executableURL = helper
        process.arguments = ["switch-probe", "--allow-live", "--managed", "--auto", "--tools"]
        process.standardInput = source; process.standardOutput = sink
        process.standardError = FileHandle.nullDevice
        do { try process.run() } catch { message = "프록시 실행 실패 · helper 확인 필요"; return }
        try? source.fileHandleForReading.close(); try? sink.fileHandleForWriting.close()
        child = process; input = source; starting = true
        message = "로컬 도구 프록시 시작 중…"
        Task.detached { [weak self] in
            defer { try? sink.fileHandleForReading.close() }
            do {
                var line = Data()
                while let byte = try sink.fileHandleForReading.read(upToCount: 1), !byte.isEmpty {
                    if byte.first == 10 {
                        await self?.receive(line, run: run); line.removeAll(keepingCapacity: true)
                    } else {
                        guard line.count < 8192 else { break }
                        line.append(byte)
                    }
                }
            } catch {}
            await self?.ended(run: run)
        }
        Task { [weak self] in
            try? await Task.sleep(nanoseconds: 15_000_000_000)
            guard let self, self.generation == run, self.starting else { return }
            self.stop(); self.message = "프록시 시작 시간 초과 · 계정 로그인 확인 후 재시작"
        }
    }

    private func receive(_ data: Data, run: UUID) {
        guard generation == run else { return }
        struct Event: Decodable {
            let event: String
            var slot: String?; var busy: Bool?; var failed: Bool?; var connected: Bool?
            var revision: UInt64?; var accepted: Bool?; var codex_home: String?
            var can_abandon_turn: Bool?
            var request_id: UInt64?; var status: String?; var succeeded: Bool?
        }
        guard let e = try? JSONDecoder().decode(Event.self, from: data) else { return }
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
        slot = nextSlot; busy = nextBusy; failed = nextFailed; connected = nextConnected
        toolWaiting = e.can_abandon_turn == true && nextBusy && !nextFailed
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
        else if failed { message = "요청 실패 · 다른 계정 수동 선택 후 새 입력 가능" }
        else if toolWaiting { message = "계정 \(slot.uppercased()) · CLI 도구 결과 대기" }
        else if busy { message = "계정 \(slot.uppercased()) 요청 처리 중" }
        else { message = connected ? "계정 \(slot.uppercased()) · CLI 입력 대기" : "CLI 연결 명령을 복사해 터미널에서 실행하세요" }
    }

    func poll() {
        guard child != nil, !starting else { return }
        if Date().timeIntervalSince(lastRead) > 4 {
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

    func abandonTurn() {
        guard canAbandonTurn else { return }
        control(["action": "abandon_turn", "slot": slot, "revision": revision])
    }

    private func control(_ command: [String: Any]) {
        pending = true
        send(command)
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
        guard let handle = input?.fileHandleForWriting, let data = try? JSONSerialization.data(withJSONObject: object) else { return false }
        do { try handle.write(contentsOf: data + Data([10])); return true }
        catch { ready = false; message = "프록시 제어 연결 끊김"; return false }
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

    func copyCommand() {
        guard ready, let home else { return }
        let quoted = "'" + home.replacingOccurrences(of: "'", with: "'\"'\"'") + "'"
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString("CODEX_HOME=\(quoted) codex", forType: .string)
    }

    private func ended(run: UUID) {
        guard generation == run else { return }
        stop(); message = "프록시 종료 · A/B 로그인과 helper 확인 후 재시작"
    }

    func stop() {
        finishUsageRead("프록시 연결 전")
        onUsageUnavailable?()
        generation = UUID()
        let owned = child; child = nil
        try? input?.fileHandleForWriting.close(); input = nil
        ready = false; starting = false; pending = false; busy = false; failed = false; toolWaiting = false
        connected = false; home = nil; waitingStatus = false; uncertain = false; lastRead = .distantPast
        if let owned {
            DispatchQueue.global().asyncAfter(deadline: .now() + 2) { if owned.isRunning { owned.terminate() } }
            DispatchQueue.global().asyncAfter(deadline: .now() + 4) { if owned.isRunning { kill(owned.processIdentifier, SIGKILL) } }
        }
    }
}
