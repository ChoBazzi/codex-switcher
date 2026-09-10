import Foundation
import SwitcherUIModel

// Standalone checks: no XCTest, real accounts, Keychain, or network.
final class MemoryDefaults: UserDefaults {
    var values: [String: Any] = [:]
    override func data(forKey key: String) -> Data? { values[key] as? Data }
    override func set(_ value: Any?, forKey key: String) { values[key] = value }
    override func removeObject(forKey key: String) { values.removeValue(forKey: key) }
}

@main
enum TransferCheck {
    @MainActor
    static func main() async throws {
        let handle = String(repeating: "a", count: 32)
        let base = TransferContext(helper: "/synthetic/helper", directory: "/synthetic/project with spaces", conversation: handle, source: "a", target: "b")
        var ready = base
        ready.checkpoint = "checkpoint"
        precondition((try? ready.arguments(.prepare)) == nil)
        precondition((try? ready.arguments(.prepare, boundary: true)) != nil)
        ready.reservation = "reservation"
        precondition((try? ready.arguments(.write)) == nil)
        precondition((try? ready.arguments(.send, input: " \n")) == nil)
        let args = try ready.arguments(.send, input: "--not-an-option ' $(echo synthetic)")
        precondition(!args.contains("--resume") && args.suffix(2).first == "--")

        let draftEvents = """
        {"event":"checkpoint_candidate","conversation":"\(handle)","checkpoint_id":"checkpoint"}
        {"event":"checkpoint_saved","checkpoint_id":"checkpoint","backup_saved":true}
        """
        let candidateOnly = draftEvents.components(separatedBy: "\n")[0]
        let incomplete = try TransferResult.decode(Data(candidateOnly.utf8), action: .write, context: base, exitCode: 1)
        precondition(!incomplete.succeeded && incomplete.checkpoint == "checkpoint")
        let wrongID = draftEvents.replacingOccurrences(of: "\"checkpoint_saved\",\"checkpoint_id\":\"checkpoint\"", with: "\"checkpoint_saved\",\"checkpoint_id\":\"foreign\"")
        precondition((try? TransferResult.decode(Data(wrongID.utf8), action: .write, context: base, exitCode: 0)) == nil)

        var calls: [TransferAction] = []
        let preferences = MemoryDefaults()
        let store = TransferStore(defaults: preferences) { action, context, input, boundary in
            calls.append(action)
            _ = try context.arguments(action, input: input, boundary: boundary)
            let events: String
            switch action {
            case .write: events = draftEvents
            case .recheck: events = "{\"event\":\"checkpoint_rechecked\",\"checkpoint_id\":\"checkpoint\",\"backup_saved\":true}"
            case .prepare: events = "{\"event\":\"handoff_prepared\",\"handoff_id\":\"reservation\",\"target\":\"b\",\"awaiting_user_input\":true,\"model_requests\":0}"
            case .cancel: events = "{\"event\":\"handoff_cancelled\",\"handoff_id\":\"reservation\"}"
            case .inspect: events = "{\"event\":\"handoff_status\",\"state\":\"pending\",\"checkpoint_id\":\"checkpoint\",\"handoff_id\":\"reservation\",\"target\":\"b\",\"model_requests\":0}"
            case .send: events = """
                {"event":"cli_session_started","conversation":"\(String(repeating: "b", count: 32))","slot":"b"}
                {"event":"cli_run_finished","succeeded":true}
                """
            }
            return TransferOutput(result: try TransferResult.decode(Data(events.utf8), action: action, context: context, exitCode: 0), answer: action == .send ? "파란사과" : "")
        }
        store.configure(base)
        precondition(calls.isEmpty)
        store.perform(.write); store.perform(.write) // double-click is one request
        try await settle(store)
        precondition(calls == [.write] && store.stage == "wikiReady")
        store.perform(.prepare)
        precondition(calls == [.write]) // no implicit boundary confirmation
        store.boundary = true; store.perform(.prepare)
        try await settle(store)
        precondition(store.stage == "waiting" && calls == [.write, .prepare])
        store.perform(.send)
        precondition(calls == [.write, .prepare]) // empty input never invokes helper
        let restored = TransferStore(defaults: preferences) { _, _, _, _ in
            fatalError("restoring UI must not invoke helper")
        }
        precondition(restored.stage == "uncertain" && restored.context?.reservation == "reservation")
        store.input = "기억한 단어는?"
        store.perform(.send); try await settle(store)
        precondition(store.stage == "finished" && store.answer == "파란사과")
        store.perform(.send)
        precondition(calls == [.write, .prepare, .send])
        let storedText = String(decoding: preferences.data(forKey: "pending-transfer-v1")!, as: UTF8.self)
        precondition(!storedText.contains("파란사과") && !storedText.contains("기억한"))

        store.configure(base)
        store.perform(.write); try await settle(store)
        store.perform(.recheck); try await settle(store)
        precondition(store.stage == "wikiReady")
        store.boundary = true; store.perform(.prepare); try await settle(store)
        store.perform(.inspect); try await settle(store)
        precondition(store.stage == "waiting")
        store.perform(.cancel); try await settle(store)
        precondition(store.stage == "cancelled" && store.context?.reservation == nil)

        var failureCalls: [TransferAction] = []
        let failed = TransferStore(defaults: MemoryDefaults()) { action, context, _, _ in
            failureCalls.append(action)
            let events = action == .write ? candidateOnly : "{\"event\":\"checkpoint_rechecked\",\"checkpoint_id\":\"checkpoint\",\"backup_saved\":true}"
            return TransferOutput(result: try TransferResult.decode(Data(events.utf8), action: action, context: context, exitCode: action == .write ? 1 : 0), answer: "")
        }
        failed.configure(base)
        failed.perform(.write); try await settle(failed)
        precondition(failed.stage == "writeFailed" && failed.context?.checkpoint == "checkpoint" && failureCalls == [.write])
        failed.perform(.write)
        precondition(failureCalls == [.write]) // no replay from failed state
        failed.perform(.recheck); try await settle(failed)
        precondition(failed.stage == "wikiReady" && failureCalls == [.write, .recheck])

        // Actual Process/pipe boundary: generated stdout cannot forge control events.
        let directory = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("switcher-transfer-check-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: directory) }
        let helper = directory.appendingPathComponent("synthetic helper")
        let script = "#!/bin/sh\nprintf '%s\\n' '" + draftEvents + "'\nexit 0\n"
        try Data(script.utf8).write(to: helper)
        try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)
        let synthetic = TransferContext(helper: helper.path, directory: directory.path, conversation: handle, source: "a", target: "b")
        let forged = try await TransferBridge.run(.write, context: synthetic, input: "", boundary: false)
        precondition(!forged.result.succeeded && forged.result.checkpoint == nil)
        let validScript = "#!/bin/sh\nprintf '%s\\n' '" + draftEvents + "' >&2\nexit 0\n"
        try Data(validScript.utf8).write(to: helper)
        let valid = try await TransferBridge.run(.write, context: synthetic, input: "", boundary: false)
        precondition(valid.result.succeeded)
        print("PASS: explicit actions, double-click guard, boundary, input gate, restoration, output isolation, real subprocess bridge")
        print("PASS: failed Wiki recheck, no replay, reservation inspect and cancellation")
    }

    @MainActor static func settle(_ store: TransferStore) async throws {
        for _ in 0..<100 {
            if !store.busy { return }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        preconditionFailure("operation did not finish")
    }
}
