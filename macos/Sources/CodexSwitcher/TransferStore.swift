import AppKit
import SwiftUI
import SwitcherUIModel

@MainActor
final class TransferStore: ObservableObject {
    @Published private(set) var context: TransferContext?
    @Published private(set) var busy = false
    @Published private(set) var stage = "idle"
    @Published private(set) var message = ""
    @Published private(set) var answer = ""
    @Published var input = ""
    @Published var boundary = false
    @Published var presented = false
    @Published private(set) var startedAt: Date?
    private let defaults: UserDefaults
    private let run: (TransferAction, TransferContext, String, Bool) async throws -> TransferOutput
    private let key = "pending-transfer-v1"
    private struct Saved: Codable { let context: TransferContext; let stage: String }

    init(defaults: UserDefaults = .standard,
         run: @escaping (TransferAction, TransferContext, String, Bool) async throws -> TransferOutput = { action, context, input, boundary in
             try await TransferBridge.run(action, context: context, input: input, boundary: boundary)
         }) {
        self.defaults = defaults
        self.run = run
        if let data = defaults.data(forKey: key), let saved = try? JSONDecoder().decode(Saved.self, from: data),
           (try? saved.context.arguments(.inspect)) != nil || (try? saved.context.arguments(.write)) != nil {
            context = saved.context
            stage = "uncertain"
            message = "이전 전환 기록이 있습니다. 자동 실행하지 않습니다. 예약 상태 또는 Wiki 저장을 확인하세요."
        }
    }

    var canBegin: Bool { context == nil || ["finished", "cancelled"].contains(stage) }
    var canForget: Bool { !busy && context?.reservation == nil && (["idle", "wikiReady", "writeFailed", "cancelled", "finished"].contains(stage) || context?.checkpoint == nil) }

    func begin(helper: URL, session: LiveSession, target: String) {
        guard canBegin, session.state == "closed", session.slot != target else { return }
        let panel = NSOpenPanel()
        panel.title = "선택한 세션의 프로젝트 루트 선택"
        panel.canChooseDirectories = true; panel.canChooseFiles = false; panel.allowsMultipleSelection = false
        guard panel.runModal() == .OK, let directory = panel.url else { return }
        configure(TransferContext(helper: helper.path, directory: directory.path, conversation: session.id, source: session.slot, target: target))
    }

    func configure(_ selected: TransferContext) {
        guard canBegin, (try? selected.arguments(.write)) != nil else { return }
        context = selected
        stage = "idle"; answer = ""; input = ""; boundary = false
        message = "Wiki 작성은 원본 계정의 모델을 호출합니다. 프로젝트·worktree·브랜치는 helper가 검증합니다."
        save(); presented = true
    }

    func perform(_ action: TransferAction) {
        guard !busy, var frozen = context else { return }
        switch action {
        case .write: guard stage == "idle" else { return }
        case .recheck: guard frozen.checkpoint != nil, frozen.reservation == nil else { return }
        case .prepare: guard stage == "wikiReady", boundary else { return }
        case .send: guard stage == "waiting" else { return }
        case .cancel: guard frozen.reservation != nil else { return }
        case .inspect: guard frozen.checkpoint != nil else { return }
        }
        let prompt = input, confirmed = boundary
        guard (try? frozen.arguments(action, input: prompt, boundary: confirmed)) != nil else { return }
        busy = true; startedAt = Date(); boundary = false
        stage = action == .write ? "writing" : action == .send ? "sending" : "working"
        message = action == .write ? "Wiki 작성·검증 중 · 실제 요청 전송부터 최대 3분" : "처리 중 · 자동 재시도 없음"
        save()
        Task {
            defer { busy = false; startedAt = nil; save() }
            do {
                let output = try await run(action, frozen, prompt, confirmed)
                if let checkpoint = output.result.checkpoint { frozen.checkpoint = checkpoint }
                if let reservation = output.result.reservation { frozen.reservation = reservation }
                context = frozen
                if action == .send { answer = output.answer; input = "" }
                guard output.result.succeeded else {
                    stage = action == .write ? "writeFailed" : "uncertain"
                    message = "완료를 확인하지 못했습니다. 요청을 재전송하지 않았습니다. Wiki 저장 재확인 또는 예약 상태 확인을 사용하세요."
                    return
                }
                switch action {
                case .write, .recheck:
                    stage = "wikiReady"
                    message = "Wiki 저장·백업 확인 완료. 미해결 작업이 없고 Wiki가 최신인지 확인한 뒤 인계를 준비하세요."
                case .prepare:
                    stage = "waiting"
                    message = "\(frozen.target.uppercased()) 계정 인계 준비 완료 · 사용자 입력 대기. 아직 대상 모델을 호출하지 않았습니다."
                case .cancel:
                    context?.reservation = nil; stage = "cancelled"
                    message = "예약 취소 완료. Wiki는 보존했습니다."
                case .send:
                    stage = "finished"
                    message = "새 세션 실행 완료 · \(output.result.conversation ?? "")"
                case .inspect:
                    switch output.result.reservationState {
                    case "pending": stage = "waiting"; message = "미사용 예약 확인 · 새 입력을 기다립니다."
                    case "consumed": stage = "finished"; message = "이미 사용된 예약입니다. 실행 성공 여부와 별개이며 다시 전송하지 않습니다."
                    default:
                        context?.reservation = nil; stage = "writeFailed"
                        message = "예약 없음. Wiki 저장을 재확인한 뒤 준비할 수 있습니다."
                    }
                }
            } catch {
                stage = "uncertain"
                message = "실행 결과 미확인. 자동 재시도하지 않습니다. helper와 예약 상태를 확인하세요."
            }
        }
    }

    func forget() {
        guard canForget || (!busy && ["finished", "cancelled"].contains(stage)) else { return }
        context = nil; input = ""; answer = ""; stage = "idle"; presented = false
        defaults.removeObject(forKey: key)
    }

    private func save() {
        guard let context, let data = try? JSONEncoder().encode(Saved(context: context, stage: stage)) else { return }
        // Only local handles/paths; no prompt, answer, Wiki or credentials.
        defaults.set(data, forKey: key)
    }
}

struct TransferPanel: View {
    @ObservedObject var store: TransferStore
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text("계정 전환").font(.title2.bold())
                if let context = store.context {
                    Text("\(context.source.uppercased()) → \(context.target.uppercased()) · \(context.conversation.prefix(8))")
                    Text(context.directory).font(.caption).textSelection(.enabled)
                    Text(store.message).fixedSize(horizontal: false, vertical: true)
                    if store.busy {
                        ProgressView()
                        if let start = store.startedAt { Text(start, style: .timer).monospacedDigit() }
                    }
                    if store.stage == "idle" {
                        Button("Wiki 작성 시작 · 모델 호출") { store.perform(.write) }.buttonStyle(.borderedProminent)
                    }
                    if context.checkpoint != nil && context.reservation == nil {
                        Button("Wiki 저장 재확인 · 모델 호출 없음") { store.perform(.recheck) }.disabled(store.busy)
                    }
                    if store.stage == "wikiReady" {
                        Toggle("미해결 작업이 없고 Wiki가 최신임을 확인했습니다", isOn: $store.boundary)
                        Button("\(context.target.uppercased())로 인계 준비") { store.perform(.prepare) }
                            .disabled(!store.boundary || store.busy)
                    }
                    if context.checkpoint != nil {
                        Button("예약 상태 확인 · 모델 호출 없음") { store.perform(.inspect) }.disabled(store.busy)
                    }
                    if store.stage == "waiting" {
                        Text("새 입력과 Wiki만 새 CLI 세션에 전달합니다.").font(.caption)
                        TextField("새 세션에 보낼 요청", text: $store.input, axis: .vertical).lineLimit(3...6)
                        Button("새 세션 실행 · 모델 호출") { store.perform(.send) }
                            .buttonStyle(.borderedProminent)
                            .disabled(store.busy || store.input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                    if context.reservation != nil && store.stage != "finished" {
                        Button("인계 예약 취소") { store.perform(.cancel) }.disabled(store.busy)
                    }
                    if !store.answer.isEmpty { Divider(); Text(store.answer).textSelection(.enabled) }
                    if store.stage == "uncertain" && context.checkpoint == nil {
                        Text("후보 ID를 받기 전에 중단됐습니다. CLI 기록 확인이 필요합니다. 새 모델 요청은 자동 실행되지 않습니다.").font(.caption)
                    }
                    Divider()
                    HStack {
                        Button("닫기") { store.presented = false }
                        if store.canForget || ["finished", "cancelled"].contains(store.stage) {
                            Button("전환 화면 정리") { store.forget() }.disabled(store.busy)
                        }
                    }
                }
            }.padding(20)
        }.frame(width: 480, height: 560)
    }
}
