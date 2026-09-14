import Foundation

public struct LiveSession: Decodable, Identifiable {
    public var id: String { conversation }
    public let conversation: String
    public let project: String
    public let worktree: String
    public let branch: String
    public let slot: String
    public let state: String
    public let updatedAt: Date
    public let heartbeat: Date

    public var title: String { "프로젝트 \(project.prefix(8)) · \(conversation.prefix(8))" }
    public var detail: String { "계정 \(slot.uppercased()) · worktree \(worktree.prefix(8)) · 브랜치 \(branch.prefix(8))" }
    public func phase(at now: Date) -> SessionPhase {
        let age = now.timeIntervalSince(heartbeat)
        if state == "active" || state == "waiting" {
            guard age >= -1, age < 5 else { return .disconnected }
        }
        switch state {
        case "active": return .active
        case "waiting": return .starting
        case "failed": return .failed
        case "closed": return .closed
        default: return .disconnected
        }
    }
}

public struct SessionSnapshot: Decodable {
    public let event: String
    public let generatedAt: Date
    public let sessions: [LiveSession]

    public static func decode(_ data: Data) throws -> Self {
        guard data.count <= 128 * 1024 else { throw SnapshotError.invalid }
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            let f = ISO8601DateFormatter()
            f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = f.date(from: text) { return date }
            f.formatOptions = [.withInternetDateTime]
            guard let date = f.date(from: text) else { throw SnapshotError.invalid }
            return date
        }
        let result = try decoder.decode(Self.self, from: data)
        guard result.event == "session_snapshot", result.sessions.count <= 64,
              Set(result.sessions.map(\.id)).count == result.sessions.count else { throw SnapshotError.invalid }
        func hex(_ text: String, _ count: Int) -> Bool {
            text.utf8.count == count && text.utf8.allSatisfy { (48...57).contains($0) || (97...102).contains($0) }
        }
        for s in result.sessions {
            guard hex(s.conversation, 32), hex(s.project, 64), hex(s.worktree, 64), hex(s.branch, 64),
                  AccountSlots.all.contains(s.slot),
                  ["active", "waiting", "closed", "failed", "disconnected"].contains(s.state) else { throw SnapshotError.invalid }
        }
        return result
    }
}
