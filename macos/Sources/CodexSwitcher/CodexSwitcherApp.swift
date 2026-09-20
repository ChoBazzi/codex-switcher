import AppKit
import SwiftUI
import SwitcherUIModel

@MainActor
final class MenuStore: ObservableObject {
    let login = AccountLoginStore()
    let direct = DirectProxyStore()
    @Published var accounts = AccountSlots.all.map { AccountSnapshot.empty($0) }
    @Published var phase: SessionPhase = .disconnected
    @Published var message: String?
    @Published var now = Date()
    @Published var helper: URL?
    @Published var sessions: [LiveSession] = []
    @Published var selectedSessionID: String?
    @Published var sessionsConnected = false
    @Published var sessionMessage = "세션 상태 연결 설정이 필요합니다."
    @Published var startingServer = false
    private let managedServer = ManagedStatusServer()
    private var connectionGeneration = 0
    private var terminationObserver: NSObjectProtocol?
    let externalStatus: Bool
    private var sessionBridge: SessionBridge?
    private var readingSessions = false
    private var lastSessionRead = Date.distantPast
    let demo: Bool
    private var timer: Timer?

    init() {
        let args = ProcessInfo.processInfo.arguments
        demo = args.contains("--demo")
        externalStatus = args.contains("--external-status")
        SwitcherAppDelegate.currentStore = self
        if !demo && externalStatus { sessionBridge = SessionBridge(environment: ProcessInfo.processInfo.environment) }
        if !demo, let index = args.firstIndex(of: "--helper"), args.indices.contains(index + 1) {
            helper = URL(fileURLWithPath: args[index + 1])
        }
        if demo {
            phase = .active
            loadDemo()
        }
        timer = Timer.scheduledTimer(withTimeInterval: 1, repeats: true) { [weak self] _ in
            Task { @MainActor [weak self] in
                guard let self else { return }
                self.now = Date()
                if !self.demo {
                    if self.now.timeIntervalSince(self.lastSessionRead) >= 3 { self.sessionsConnected = false }
                    self.readSessions()
                    self.direct.poll()
                }
            }
        }
        terminationObserver = NotificationCenter.default.addObserver(forName: NSApplication.willTerminateNotification, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated { self?.shutdown() }
        }
        if !demo {
            direct.onUsage = { [weak self] snapshot in
                self?.accounts = snapshot.accounts.sorted { $0.slot < $1.slot }
                self?.message = nil
            }
            direct.onUsageUnavailable = { [weak self] in
                guard let self else { return }
                self.accounts = self.accounts.map { var value = $0; value.stale = true; return value }
                self.message = "프록시 사용량 연결 미확인 · 이전 정보는 전환 판단에 사용하지 않습니다."
            }
            startStatusServer()
            if let helper { direct.start(helper: helper) }
        }
    }

    func shutdown() {
        login.cancel()
        connectionGeneration += 1
        timer?.invalidate()
        sessionBridge?.close()
        managedServer.stop()
        direct.stop()
    }

    func startStatusServer() {
        guard !demo, !externalStatus else { return }
        connectionGeneration += 1
        let generation = connectionGeneration
        sessionBridge?.close(); sessionBridge = nil
        managedServer.stop()
        sessionsConnected = false; readingSessions = false
        lastSessionRead = .distantPast
        guard let helper else {
            startingServer = false
            sessionMessage = "백엔드 경로가 없습니다. --helper 인수에 빌드한 switcher-helper 경로를 지정해 앱을 실행하세요."
            return
        }
        startingServer = true
        sessionMessage = "상태 서버 시작 중…"
        Task {
            defer { if generation == connectionGeneration { startingServer = false } }
            do {
                let bridge = try await managedServer.start(helper: helper)
                guard generation == connectionGeneration else { bridge.close(); return }
                sessionBridge = bridge
                readSessions()
            } catch {
                guard generation == connectionGeneration else { return }
                sessionMessage = "상태 서버 시작 실패 · helper를 확인하고 재시작하세요."
            }
        }
    }

    var selectedSession: LiveSession? {
        if let selectedSessionID { return sessions.first { $0.id == selectedSessionID } }
        return sessions.first(where: { $0.phase(at: now) == .active }) ?? sessions.first
    }
    var visiblePhase: SessionPhase {
        if !demo, direct.ready { return direct.failed ? .failed : direct.busy ? .active : .waiting }
        return demo ? phase : sessionsConnected ? selectedSession?.phase(at: now) ?? .disconnected : .disconnected
    }
    var menuLabel: String {
        if demo { return "A · 데모" }
        if direct.ready { return "\(direct.slot.uppercased()) · \(direct.failed ? "실패" : direct.busy ? "활성" : "대기")" }
        guard sessionsConnected, let selectedSession else { return "Switcher" }
        let label: String
        switch visiblePhase {
        case .active: label = "활성"
        case .closed: label = "종료"
        case .failed: label = "실패"
        case .disconnected: label = "미확인"
        default: label = "대기"
        }
        return "\(selectedSession.slot.uppercased()) · \(label)"
    }

    func readSessions() {
        if !externalStatus && sessionBridge != nil && !managedServer.isRunning {
            sessionsConnected = false
            sessionBridge?.close(); sessionBridge = nil
            sessionMessage = "상태 서버가 종료됐습니다. 재시작 버튼을 눌러주세요."
            return
        }
        guard !readingSessions, let sessionBridge else { return }
        let generation = connectionGeneration
        readingSessions = true
        Task {
            defer { if generation == connectionGeneration { readingSessions = false } }
            do {
                let snapshot = try await sessionBridge.read()
                guard generation == connectionGeneration, self.sessionBridge === sessionBridge else { return }
                sessions = snapshot.sessions
                if !sessions.contains(where: { $0.id == selectedSessionID }) {
                    selectedSessionID = nil
                }
                lastSessionRead = Date()
                sessionsConnected = true
                sessionMessage = sessions.isEmpty ? "등록된 CLI 실행 없음" : "세션 상태 연결됨"
            } catch {
                guard generation == connectionGeneration, self.sessionBridge === sessionBridge else { return }
                sessionsConnected = false
                sessionMessage = "세션 상태 연결 끊김 · 상태 서버와 연결 설정을 확인하세요."
            }
        }
    }

    func refresh() {
        guard !demo, !login.busy else { return }
        direct.readUsage()
    }

    func connectAccount(_ account: AccountSnapshot) {
        guard !demo, !direct.usageRefreshing, !login.busy, !direct.busy, let helper,
              let command = AccountLogin.command(for: account.state) else { return }
        direct.accountChanged(account.slot, changing: true)
        login.start(helper: helper, slot: account.slot, command: command) { [weak self] in
            self?.direct.accountChanged(account.slot, changing: false)
        }
    }

    func logoutAccount(_ slot: String) {
        guard !demo, !direct.usageRefreshing, !login.busy, !direct.busy, let helper else { return }
        direct.accountChanged(slot, changing: true)
        login.start(helper: helper, slot: slot, command: "logout") { [weak self] in
            self?.direct.accountChanged(slot, changing: false)
        }
    }

    func addAccount() {
        guard let slot = AccountSlots.firstVacancy(in: accounts),
              let account = accounts.first(where: { $0.slot == slot }) else { return }
        connectAccount(account)
    }

    private func loadDemo() {
        // Deliberately synthetic. No Keychain, network, CLI, or session mutation.
        let sample = """
        {"event":"usage_snapshot","accounts":[
        {"slot":"a","state":"ok","stale":false,"last_success":"2026-09-07T14:00:00Z",
        "usage":{"remaining_percent":24,"primary":{"remaining_percent":24,"limit_seconds":18000},"secondary":{"remaining_percent":62,"limit_seconds":604800}}},
        {"slot":"b","state":"ok","stale":false,"last_success":"2026-09-07T14:00:00Z",
        "usage":{"remaining_percent":76,"primary":{"remaining_percent":89,"limit_seconds":18000},"secondary":{"remaining_percent":76,"limit_seconds":604800}}}]}
        """
        if let snapshot = try? UsageSnapshot.decode(Data(sample.utf8)) {
            accounts = snapshot.accounts.map { var item = $0; item.lastSuccess = Date(); return item }
            for slot in ["c", "d", "e"] {
                var item = AccountSnapshot.empty(slot)
                item.registered = true; item.state = "ok"; item.lastSuccess = Date()
                item.stale = false; item.usage = accounts[1].usage
                accounts.append(item)
            }
        }
    }
}

@MainActor
final class SwitcherAppDelegate: NSObject, NSApplicationDelegate {
    weak var store: MenuStore?
    static weak var currentStore: MenuStore?
    private var terminating = false

    func applicationShouldTerminate(_ sender: NSApplication) -> NSApplication.TerminateReply {
        guard let store = store ?? Self.currentStore else { return .terminateCancel }
        if store.demo { return .terminateNow }
        if terminating { return .terminateLater }
        if store.login.busy {
            showTerminationMessage("계정 로그인·로그아웃 작업을 마친 뒤 종료하세요.")
            return .terminateCancel
        }
        terminating = true
        // Return terminateLater before completing even an immediate rejection.
        Task { @MainActor in
            store.direct.shutdownService { [weak self] success in
                guard let self else { return }
                self.terminating = false
                if !success { self.showTerminationMessage(store.direct.message) }
                sender.reply(toApplicationShouldTerminate: success)
            }
        }
        return .terminateLater
    }

    private func showTerminationMessage(_ message: String) {
        let alert = NSAlert()
        alert.messageText = "앱과 프록시를 종료하지 못했습니다"
        alert.informativeText = message
        alert.addButton(withTitle: "확인")
        NSApplication.shared.activate(ignoringOtherApps: true)
        alert.runModal()
    }
}

#if !MENU_LAYOUT_CHECK
@main
struct CodexSwitcherApp: App {
    @NSApplicationDelegateAdaptor(SwitcherAppDelegate.self) private var appDelegate
    @StateObject private var store = MenuStore()

    var body: some Scene {
        MenuBarExtra(store.menuLabel, systemImage: "arrow.triangle.swap") {
            MenuPanel(store: store)
                .onAppear { appDelegate.store = store }
        }
        .menuBarExtraStyle(.window)
    }
}
#endif

struct MenuPanel: View {
    @State private var logoutConfirmation = LogoutConfirmation()
    @StateObject private var codexSettings = CodexSettingsStore()
    @State private var showingCodexSettings = false
    @State private var showingDiagnostics = false
    @ObservedObject var store: MenuStore
    @ObservedObject var login: AccountLoginStore
    @ObservedObject var direct: DirectProxyStore
    init(store: MenuStore) {
        self.store = store
        self.login = store.login
        self.direct = store.direct
    }
    // Dark saturated blue gives white text strong contrast in either appearance.
    private let activeBlue = Color(red: 0.12, green: 0.25, blue: 0.68)

    var body: some View {
        if showingCodexSettings {
            CodexSettingsPanel(settings: codexSettings, direct: direct) {
                showingCodexSettings = false
            }
        } else if showingDiagnostics {
            ProxyDiagnosticsPanel(direct: direct) { showingDiagnostics = false }
        } else {
            accountPanel
        }
    }

    private var accountPanel: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Image(systemName: "arrow.triangle.swap").foregroundStyle(.blue)
                Text("Codex Switcher").font(.headline)
                Spacer()
                Text(store.demo ? "데모" : "사용량 모니터")
                    .font(.caption).foregroundStyle(.secondary)
            }
            sessionCard
            if !store.demo && !store.sessions.isEmpty {
                Picker("확인할 세션", selection: $store.selectedSessionID) {
                    Text("최근 활성 세션 자동 선택").tag(Optional<String>.none)
                    ForEach(store.sessions) { session in
                        Text(session.title).tag(Optional(session.id))
                    }
                }
                .disabled(!store.sessionsConnected)
            }
            if store.demo {
                Picker("화면 상태 미리보기", selection: $store.phase) {
                    ForEach(SessionPhase.allCases, id: \.self) { phase in
                        Text(phase.label).tag(phase)
                    }
                }
                Text("합성 데이터 · 실제 계정이나 세션을 변경하지 않습니다.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            HStack {
                Text("등록 계정 \(store.accounts.filter { $0.registered == true || ($0.state != "unknown" && $0.state != "not_registered" && $0.state != "auth_error") }.count) / 5")
                    .font(.subheadline)
                Spacer()
                Button("계정 추가") { store.addAccount() }
                    .disabled(store.demo || !direct.ready || direct.busy || login.busy || store.helper == nil || AccountSlots.firstVacancy(in: store.accounts) == nil)
            }
            // Cancellation must remain reachable even when the account list is
            // empty, scrolled away, or being refreshed during browser login.
            if login.busy {
                VStack(alignment: .leading, spacing: 6) {
                    Text(login.message).font(.caption).fixedSize(horizontal: false, vertical: true)
                    if login.isBrowserLogin {
                        Text("브라우저를 닫았다면 아래에서 취소하세요. 창을 닫아도 로그인 대기는 종료되지 않습니다.")
                            .font(.caption2).foregroundStyle(.secondary)
                        HStack {
                            ProgressView().controlSize(.small)
                            Button("계정 추가 / 로그인 취소") { login.cancel() }
                                .disabled(!login.canCancel)
                        }
                    }
                }
            }
            if !visibleAccounts.isEmpty {
                Grid(horizontalSpacing: 10, verticalSpacing: 10) {
                    ForEach(0..<((visibleAccounts.count + 1) / 2), id: \.self) { row in
                        GridRow(alignment: .top) {
                            accountCard(visibleAccounts[row * 2])
                            if row * 2 + 1 < visibleAccounts.count {
                                accountCard(visibleAccounts[row * 2 + 1])
                            } else {
                                Color.clear.frame(width: 287, height: 1).accessibilityHidden(true)
                            }
                        }
                    }
                }
            } else {
                Text(AccountSlots.firstVacancy(in: store.accounts) != nil ? "등록된 계정이 없습니다. 계정 추가로 브라우저 로그인을 시작하세요." : "계정 저장 상태를 확인하고 있습니다.")
                    .font(.caption).foregroundStyle(.secondary)
            }
            if let slot = logoutConfirmation.slot {
                logoutPanel(slot)
            }
            if !login.busy, let slot = login.slot, !visibleAccounts.contains(where: { $0.slot == slot }) {
                Text(login.message).font(.caption).fixedSize(horizontal: false, vertical: true)
            }
            if let message = store.message {
                Label(message, systemImage: "exclamationmark.triangle")
                    .font(.caption).foregroundStyle(.orange)
            }
            if !store.demo {
                Text("로컬 도구 전환 실험 · 작업 턴 완료 후 적용")
                    .font(.caption).foregroundStyle(.secondary)
                if direct.toolWaiting {
                    Text("CLI에서 Esc로 취소하고 입력창으로 돌아온 경우에만 잠금을 해제하세요. 이 버튼은 실행 중인 로컬 도구를 종료하지 않습니다.")
                        .font(.caption2).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
                    Button("CLI 중단 확인 · 전환 잠금 해제") { direct.abandonTurn() }
                        .disabled(login.busy || !direct.canAbandonTurn)
                }
                HStack {
                    Button("Codex 설정") { showingCodexSettings = true }
                    Button("상태·진단") { showingDiagnostics = true }
                    Button(direct.connected ? "대화 재개 명령 복사" : "Codex CLI 연결 명령 복사") {
                        direct.copyCommand(resume: direct.connected)
                    }.disabled(!direct.ready)
                }
                if direct.buildWarning != nil {
                    Label("프록시 빌드 확인 필요 · 상태·진단에서 확인하세요", systemImage: "exclamationmark.triangle")
                        .font(.caption).foregroundStyle(.orange)
                } else if !direct.diagnostics.isEmpty {
                    Text("최근 요청 오류가 있습니다 · 상태·진단에서 원인과 해결 방법을 확인하세요")
                        .font(.caption).foregroundStyle(.secondary)
                }
                if !direct.ready || direct.failed {
                    Button(direct.starting ? "프록시 시작 중…" : (direct.ready && direct.failed ? "사용 가능한 계정으로 복구 준비" : "모델 프록시 다시 연결")) {
                        if direct.ready && direct.failed { direct.prepareRecovery() }
                        else if let helper = store.helper { direct.start(helper: helper) }
                    }.disabled(direct.starting || (direct.ready && (direct.busy || direct.pending)) || store.helper == nil || login.busy)
                    if direct.failed {
                        Text("CLI가 입력창으로 돌아온 뒤 복구 준비를 누르고 새 지시를 입력하세요. 실패한 요청은 재전송하지 않습니다.")
                            .font(.caption2).foregroundStyle(.secondary)
                    }
                }
                Text(store.sessionMessage)
                    .font(.caption).foregroundStyle(.secondary)
                if !store.externalStatus && store.helper != nil && !store.sessionsConnected {
                    Button(store.startingServer ? "상태 서버 시작 중…" : "상태 서버 재시작") { store.startStatusServer() }
                        .disabled(store.startingServer)
                }
            }
            Divider()
            if let feedback = direct.usageRefreshMessage, !store.demo {
                Text(feedback).font(.caption2).foregroundStyle(.secondary)
            }
            HStack {
                if direct.usageRefreshing { ProgressView().controlSize(.small) }
                Text(store.demo ? "미리보기 모드" : "60초마다 사용량 확인")
                    .font(.caption).foregroundStyle(.secondary)
                Spacer()
                Button { store.refresh() } label: { Label("새로고침", systemImage: "arrow.clockwise") }
                    .help("프록시가 실제 사용량을 조회합니다. 중복 조회를 막고 완료 후 60초 뒤 자동 조회합니다. 연속 조회는 5초 간격으로 제한합니다.")
                    .disabled(store.demo || !direct.canReadUsage || login.busy || store.helper == nil)
                Button(direct.stopping ? "프록시 종료 중…" : "앱·프록시 종료") { NSApplication.shared.terminate(nil) }.disabled(direct.stopping)
            }
        }
        .padding(18)
        .frame(width: 620)
        .onDisappear { logoutConfirmation.cancel() }
    }

    private var sessionCard: some View {
        let highlighted = store.visiblePhase.highlightsActiveSession
        return VStack(alignment: .leading, spacing: 6) {
            Label(!store.demo && store.visiblePhase == .failed ? "실행 실패" : store.visiblePhase.label, systemImage: highlighted ? "bolt.fill" : "circle.dashed")
                .font(.subheadline.weight(.semibold))
            Text(store.demo ? "codex-switcher / dev" : direct.ready ? "계정 \(direct.slot.uppercased()) · 직접 연결" : store.selectedSession?.title ?? "CLI 실행 없음")
                .font(.title3.weight(.semibold))
            Text(store.demo ? sessionDetail : direct.ready ? direct.message : store.sessionsConnected ? store.selectedSession?.detail ?? "새 CLI 실행을 기다리고 있습니다." : "연결이 끊겨 활성 여부를 확인할 수 없습니다.")
                .font(.caption)
                .fixedSize(horizontal: false, vertical: true)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .foregroundStyle(highlighted ? Color.white : Color.primary)
        .background(highlighted ? activeBlue : Color(nsColor: .controlBackgroundColor),
                    in: RoundedRectangle(cornerRadius: 14))
        .accessibilityElement(children: .combine)
    }

    private var sessionDetail: String {
        switch store.phase {
        case .active: return "계정 A 연결 · 활성 상태 예시"
        case .starting: return "CLI 등록 완료 · 요청 전송 준비 중"
        case .preparing: return "A → B 계정 선택 확인 중"
        case .waiting: return "B 연결 준비 완료 · 새 사용자 입력 전에는 요청하지 않습니다."
        case .failed: return "요청 실패 · 자동 재전송하지 않습니다."
        case .closed: return "단발 실행이 종료되었습니다. 다음 입력은 CLI에서 시작하세요."
        case .disconnected: return "활성 세션 정보가 없습니다."
        }
    }

    private func accountCard(_ account: AccountSnapshot) -> some View {
        let stale = account.isStale(at: store.now)
        let selected = store.demo ? account.slot == "a" : direct.ready && direct.slot == account.slot
        let canLogout = store.helper != nil && !direct.usageRefreshing && !login.busy && !direct.busy
            && account.canLogoutAccount
        return VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("계정 \(account.slot.uppercased())").font(.subheadline.weight(.semibold))
                Spacer()
                Text(selected ? "현재 선택 · \(account.stateLabel)" : account.stateLabel).font(.caption2)
            }
            Text("\(account.planLabel)\(stale && account.usage?.planType != nil ? " · 이전 조회 정보" : "")")
                .font(.caption2).foregroundStyle(.secondary)
            quota(account.usage?.primary, stale: stale)
            quota(account.usage?.secondary, stale: stale)
            if !store.demo {
                if let command = AccountLogin.command(for: account.state), command == "reauth" {
                    Button(command == "login" ? "계정 연결 · 브라우저 로그인" : "다시 로그인") { store.connectAccount(account) }
                        .disabled(store.helper == nil || direct.usageRefreshing || login.busy || direct.busy)
                }
                if account.state == "auth_error" {
                    Text("등록 해제를 뜻하지 않습니다. Keychain 접근 승인과 인증 상태를 확인하세요.")
                        .font(.caption2).foregroundStyle(.secondary)
                }
                if login.slot == account.slot && !login.busy {
                    Text(login.message).font(.caption).fixedSize(horizontal: false, vertical: true)
                }
            }
            HStack {
                Button(selected && direct.canRecoverCurrent ? "복구 준비 · 새 입력 대기" : selected ? "\(account.slot.uppercased()) 선택됨" : "\(account.slot.uppercased())로 전환") {
                    direct.select(account.slot)
                }
                .disabled(store.demo || login.busy || !direct.canSelect(account.slot) || stale || account.state != "ok" || (account.usage?.remainingPercent ?? 0) <= 5)
                .help("로컬 도구 실행까지 끝난 뒤 다음 사용자 요청의 계정을 변경합니다. 텍스트 압축은 지원하며 암호화 압축은 원본 계정에 고정됩니다.")
                Spacer()
                if let success = account.lastSuccess {
                    HStack(spacing: 3) {
                        Text(stale ? "오래됨" : "확인")
                        Text(success, style: .relative)
                    }
                    .font(.caption2).foregroundStyle(stale ? .orange : .secondary)
                    .lineLimit(1)
                    .help("마지막 사용량 조회 성공: \(success.formatted())")
                } else {
                    Text("사용량 미확인").font(.caption2).foregroundStyle(.secondary)
                }
                if !store.demo, account.canLogoutAccount {
                    Button("로그아웃") { logoutConfirmation.begin(slot: account.slot) }
                        .disabled(!canLogout || logoutConfirmation.slot != nil)
                }
            }.controlSize(.small)
        }
        .padding(10)
        .frame(width: 287, alignment: .leading)
        .background(selected ? activeBlue.opacity(0.16) : Color(nsColor: .controlBackgroundColor), in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).strokeBorder(selected ? activeBlue : .clear, lineWidth: 2))
    }

    private func logoutPanel(_ slot: String) -> some View {
        let allowed = store.helper != nil && !direct.usageRefreshing && !login.busy && !direct.busy
            && store.accounts.contains { $0.slot == slot && $0.canLogoutAccount }
        return VStack(alignment: .leading, spacing: 6) {
            Text("계정 \(slot.uppercased()) 연결을 해제할까요?").font(.subheadline.weight(.semibold))
            Text("저장된 인증과 기존 세션 재개·관련 인계 예약을 무효화합니다. 대화·Wiki 파일, 다른 계정 인증과 브라우저 로그인은 유지됩니다.")
                .font(.caption).fixedSize(horizontal: false, vertical: true)
            HStack {
                Button("취소") { logoutConfirmation.cancel() }
                Button("로그아웃 확인", role: .destructive) {
                    if let target = logoutConfirmation.confirm(allowed: allowed) { store.logoutAccount(target) }
                }.disabled(!allowed)
            }
        }
    }

    private var visibleAccounts: [AccountSnapshot] {
        store.accounts.filter {
            $0.registered == true || ($0.state != "not_registered" && $0.state != "unknown") ||
            (login.slot == $0.slot && login.busy) || logoutConfirmation.slot == $0.slot
        }
    }

    private func quota(_ window: UsageWindow?, stale: Bool) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text(window?.title ?? "구간 미확인")
                Spacer()
                Text(window?.remainingPercent.map { "\(Int($0))% 남음" } ?? "—")
                    .monospacedDigit()
            }.font(.caption)
            if let remaining = window?.remainingPercent {
                GeometryReader { geometry in
                    Capsule().fill(Color.secondary.opacity(0.15))
                        .overlay(alignment: .leading) {
                            Capsule().fill(stale ? Color.gray : remaining <= 10 ? .orange : .blue)
                                .frame(width: geometry.size.width * remaining / 100)
                        }
                }
                    .frame(height: 4)
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("\(window?.title ?? "한도") 잔여율")
                    .accessibilityValue("\(Int(remaining))퍼센트\(stale ? ", 오래된 정보" : "")")
            }
            if let reset = window?.resetAt {
                Text("초기화 \(reset.formatted(date: .abbreviated, time: .shortened))")
                    .font(.caption2).foregroundStyle(.secondary)
            } else {
                Text("초기화 시각 미확인").font(.caption2).foregroundStyle(.secondary)
            }
        }
    }
}


struct CodexSettingsPanel: View {
    @ObservedObject var settings: CodexSettingsStore
    @ObservedObject var direct: DirectProxyStore
    var onClose: () -> Void = {}
    @State private var confirmDiscard = false
    @State private var confirmReload = false

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Codex 파일 설정").font(.title2.bold())
            Text("앱 연결용 CODEX_HOME의 설정과 공통 작업 지침을 편집합니다.")
                .font(.callout).foregroundStyle(.secondary)
            Text(settings.home ?? "프록시 연결 후 파일을 불러올 수 있습니다.")
                .font(.caption.monospaced()).textSelection(.enabled)
            Picker("파일", selection: $settings.selected) {
                ForEach(CodexSettingsStore.files, id: \.self) { file in
                    Text(file + (settings.isDirty(file) ? " · 수정됨" : "")).tag(file)
                }
            }.pickerStyle(.segmented)
            TextEditor(text: Binding(get: { settings.drafts[settings.selected] ?? "" },
                                     set: { settings.drafts[settings.selected] = $0 }))
                .font(.system(.body, design: .monospaced))
                .disableAutocorrection(true)
                .frame(height: 340)
                .border(Color.secondary.opacity(0.3))
                .disabled(settings.drafts[settings.selected] == nil)
            Text(settings.selected == "config.toml"
                 ? "TOML 원문을 저장합니다. 프록시 연결 설정과 재시도 0 값은 유지하세요. 문법 검사는 Codex 실행 시 수행됩니다."
                 : "이 CLI 홈에서 사용할 공통 지침입니다. 프로젝트별 AGENTS.md도 함께 적용될 수 있습니다.")
                .font(.caption).foregroundStyle(.secondary).fixedSize(horizontal: false, vertical: true)
            if let message = settings.message {
                Text(message).font(.caption).fixedSize(horizontal: false, vertical: true)
            }
            if confirmDiscard {
                HStack {
                    Text("저장하지 않은 변경을 버릴까요?").font(.caption)
                    Button("계속 편집") { confirmDiscard = false }
                    Button("변경 버리고 돌아가기") {
                        settings.load(home: direct.home)
                        onClose()
                    }
                }
            }
            if confirmReload {
                HStack {
                    Text("초안을 버리고 두 파일을 다시 불러올까요?").font(.caption)
                    Button("취소") { confirmReload = false }
                    Button("버리고 불러오기") {
                        if let home = settings.home { settings.load(home: home) }
                        confirmReload = false
                    }
                }
            }
            HStack {
                Button("다시 불러오기") {
                    confirmDiscard = false
                    if settings.dirty { confirmReload = true }
                    else if let home = settings.home { settings.load(home: home) }
                }.disabled(settings.home == nil)
                Spacer()
                Button("계정 목록으로") {
                    confirmReload = false
                    if settings.dirty { confirmDiscard = true } else { onClose() }
                }
                Button("현재 파일 저장") { settings.save() }.disabled(!settings.canSave)
            }
            Text("저장 후 현재 CLI 작업을 마치고 종료한 뒤 같은 대화를 재개하세요.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .padding(24)
        .frame(width: 620)
        .onAppear {
            // MenuBarExtra can disappear on outside clicks; preserve an editing draft.
            if !settings.dirty { settings.load(home: direct.home) }
        }
        .onChange(of: direct.home) { home in
            if !settings.dirty { settings.load(home: home) }
        }
    }
}

struct ProxyDiagnosticsPanel: View {
    @ObservedObject var direct: DirectProxyStore
    var back: () -> Void
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Button("계정 목록으로", action: back)
                Spacer()
                Text("상태·진단").font(.headline)
                Spacer()
                Button("진단 복사") { direct.copyDiagnostics() }
            }
            Text(direct.ready ? (direct.busy ? "프록시 연결됨 · 작업 중" : "프록시 연결됨") : "프록시 연결 미확인")
            Text("앱: \(direct.appVersion)")
            Text("연결 시 helper: \(direct.helperBuild.map { String($0.prefix(12)) } ?? "미확인")")
            Text("실행 프록시: \(direct.proxyBuild.map { String($0.prefix(12)) } ?? "미확인")")
            if let warning = direct.buildWarning {
                Label(warning, systemImage: "exclamationmark.triangle").foregroundStyle(.orange)
            } else if direct.ready {
                Text("연결 시 helper와 실행 프록시의 빌드가 일치합니다.").foregroundStyle(.secondary)
            }
            Text("빌드 정보는 연결 시 확인합니다. 재빌드 후에는 다시 연결해 확인하세요.")
                .font(.caption).foregroundStyle(.secondary)
            Divider()
            Text(direct.diagnosticsFromPreviousConnection ? "마지막 오류 · 이전 연결 기록" : "마지막 오류 · 대화별 최근 1건").font(.headline)
            if direct.diagnostics.isEmpty {
                Text("수집된 오류가 없습니다. 구버전 프록시나 재시작 전의 오류는 표시되지 않을 수 있습니다.")
                    .foregroundStyle(.secondary)
            }
            ForEach(direct.diagnostics) { diagnostic in
                VStack(alignment: .leading, spacing: 5) {
                    Text("\(diagnostic.scopeLabel) · \(diagnostic.title)").fontWeight(.medium)
                    Text(diagnostic.guidance)
                    Text("\(diagnostic.code) · \(diagnostic.at)").font(.caption2).foregroundStyle(.secondary)
                }
            }
            Text("오류 기록은 현재 작업의 실패 여부와 별개입니다. 진단 복사에는 토큰·계정 식별자·로컬 경로·대화 내용이 포함되지 않습니다.")
                .font(.caption).foregroundStyle(.secondary)
        }
        .font(.callout)
        .fixedSize(horizontal: false, vertical: true)
        .padding(18)
        .frame(width: 620)
    }
}
