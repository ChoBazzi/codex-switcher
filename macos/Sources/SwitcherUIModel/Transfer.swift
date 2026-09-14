import Foundation

public enum TransferAction: String, Codable { case write, recheck, prepare, cancel, send, inspect }

/// Frozen source identity: changing the menu selection cannot redirect an action.
public struct TransferContext: Codable {
    public let helper: String
    public let directory: String
    public let conversation: String
    public let source: String
    public let target: String
    public var checkpoint: String?
    public var reservation: String?

    public init(helper: String, directory: String, conversation: String, source: String, target: String) {
        self.helper = helper; self.directory = directory; self.conversation = conversation
        self.source = source; self.target = target
    }

    public func arguments(_ action: TransferAction, input: String = "", boundary: Bool = false) throws -> [String] {
        guard helper.hasPrefix("/"), directory.hasPrefix("/"), conversation.count == 32,
              conversation.utf8.allSatisfy({ (48...57).contains($0) || (97...102).contains($0) }),
              AccountSlots.all.contains(source), AccountSlots.all.contains(target), source != target else { throw SnapshotError.invalid }
        let origin = ["-C", directory, "--conversation", conversation]
        switch action {
        case .inspect:
            guard let checkpoint, !checkpoint.isEmpty else { throw SnapshotError.invalid }
            return ["handoff", "status"] + origin + ["--checkpoint", checkpoint]
        case .write:
            guard reservation == nil else { throw SnapshotError.invalid }
            return ["exec", "-C", directory, "--resume", conversation, "--checkpoint"]
        case .recheck:
            guard let checkpoint, !checkpoint.isEmpty, reservation == nil else { throw SnapshotError.invalid }
            return ["checkpoint", "recheck"] + origin + ["--id", checkpoint]
        case .prepare:
            guard boundary, let checkpoint, !checkpoint.isEmpty, reservation == nil else { throw SnapshotError.invalid }
            return ["handoff", "prepare"] + origin + ["--checkpoint", checkpoint, "--to", target, "--confirm-boundary"]
        case .cancel:
            guard let reservation, !reservation.isEmpty else { throw SnapshotError.invalid }
            return ["handoff", "cancel"] + origin + ["--id", reservation]
        case .send:
            guard let reservation, !reservation.isEmpty, !input.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty,
                  input.utf8.count <= 1_048_576 else { throw SnapshotError.invalid }
            return ["exec", "-C", directory, "--handoff", reservation, "--", input]
        }
    }
}

public struct TransferResult {
    public var reservationState: String?
    public var checkpoint: String?
    public var reservation: String?
    public var conversation: String?
    public var succeeded = false

    // Parse only the helper's control channel, NEVER generated assistant text.
    public static func decode(_ data: Data, action: TransferAction, context: TransferContext, exitCode: Int32) throws -> Self {
        guard data.count <= 131_072 else { throw SnapshotError.invalid }
        var result = Self()
        var completed = false
        func identifier(_ object: [String: Any], _ key: String) -> String? {
            guard let value = object[key] as? String, !value.isEmpty, value.utf8.count <= 128,
                  value.utf8.allSatisfy({ (48...57).contains($0) || (65...90).contains($0) || (97...122).contains($0) || $0 == 45 || $0 == 95 }) else { return nil }
            return value
        }
        for line in data.split(separator: 10) {
            guard let object = try? JSONSerialization.jsonObject(with: Data(line)) as? [String: Any], let event = object["event"] as? String else { continue }
            switch (action, event) {
            case (.inspect, "handoff_status"):
                guard object["checkpoint_id"] as? String == context.checkpoint,
                      object["model_requests"] as? Int == 0,
                      let state = object["state"] as? String, ["absent", "pending", "consumed"].contains(state) else { throw SnapshotError.invalid }
                if state != "absent" {
                    guard object["target"] as? String == context.target, let id = identifier(object, "handoff_id") else { throw SnapshotError.invalid }
                    result.reservation = id
                }
                result.reservationState = state; completed = true
            case (.write, "checkpoint_candidate"):
                guard object["conversation"] as? String == context.conversation else { throw SnapshotError.invalid }
                result.checkpoint = identifier(object, "checkpoint_id")
            case (.write, "checkpoint_saved"):
                guard let id = identifier(object, "checkpoint_id"), id == result.checkpoint else { throw SnapshotError.invalid }
                completed = object["backup_saved"] as? Bool == true
            case (.recheck, "checkpoint_rechecked"):
                guard let id = identifier(object, "checkpoint_id"), id == context.checkpoint else { throw SnapshotError.invalid }
                result.checkpoint = id
                completed = object["backup_saved"] as? Bool == true
            case (.prepare, "handoff_prepared"):
                guard object["target"] as? String == context.target, object["awaiting_user_input"] as? Bool == true,
                      object["model_requests"] as? Int == 0, let id = identifier(object, "handoff_id") else { throw SnapshotError.invalid }
                result.reservation = id; completed = true
            case (.cancel, "handoff_cancelled"):
                completed = object["handoff_id"] as? String == context.reservation
            case (.send, "cli_session_started"):
                guard object["slot"] as? String == context.target, let id = identifier(object, "conversation"),
                      id.count == 32, id != context.conversation else { throw SnapshotError.invalid }
                result.conversation = id
            case (.send, "cli_run_finished"):
                completed = object["succeeded"] as? Bool == true && result.conversation != nil
            default: break
            }
        }
        result.succeeded = completed && exitCode == 0
        return result
    }
}
