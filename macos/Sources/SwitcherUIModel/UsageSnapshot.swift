import Foundation

public struct UsageWindow: Decodable, Equatable {
    public let remainingPercent: Double?
    public let limitSeconds: Int?
    public let resetAt: Date?

    public var title: String {
        switch limitSeconds {
        case 18_000: return "5시간"
        case 604_800: return "주간"
        case let seconds? where seconds > 0: return "\(seconds / 60)분 구간"
        default: return "구간 미확인"
        }
    }
}

public struct UsageData: Decodable, Equatable {
    public let primary: UsageWindow
    public let secondary: UsageWindow
    public let remainingPercent: Double?
}

public struct AccountSnapshot: Decodable, Equatable, Identifiable {
    public var id: String { slot }
    public let slot: String
    public var registered: Bool? = nil
    public var state: String
    public var lastSuccess: Date?
    public var stale: Bool
    public var usage: UsageData?

    public func isStale(at now: Date) -> Bool {
        stale || lastSuccess == nil || now.timeIntervalSince(lastSuccess!) >= 120
    }

    public var stateLabel: String {
        switch state {
        case "ok": return "조회 정상"
        case "not_registered": return "미등록"
        case "auth_expired": return "인증 만료"
        case "auth_error": return "인증 확인 필요"
        case "stored_unverified": return "등록됨 · 사용량 확인 대기"
        case "limit_reached": return "한도 소진"
        case "rate_limited": return "조회 제한"
        case "fetch_error": return "조회 실패"
        default: return "확인 필요"
        }
    }

    public static func empty(_ slot: String) -> Self {
        Self(slot: slot, state: "unknown", lastSuccess: nil, stale: true, usage: nil)
    }
}

public enum SnapshotError: Error { case invalid }

public struct UsageSnapshot: Decodable {
    public let event: String
    public let accounts: [AccountSnapshot]

    public static func decode(_ data: Data) throws -> Self {
        guard data.count <= 128 * 1024 else { throw SnapshotError.invalid }
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        decoder.dateDecodingStrategy = .custom { decoder in
            let text = try decoder.singleValueContainer().decode(String.self)
            let formatter = ISO8601DateFormatter()
            formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
            if let date = formatter.date(from: text) { return date }
            formatter.formatOptions = [.withInternetDateTime]
            guard let date = formatter.date(from: text) else { throw SnapshotError.invalid }
            return date
        }
        let snapshot = try decoder.decode(Self.self, from: data)
        guard snapshot.event == "usage_snapshot",
              !snapshot.accounts.isEmpty, snapshot.accounts.count <= AccountSlots.capacity,
              Set(snapshot.accounts.map(\.slot)).count == snapshot.accounts.count,
              Set(snapshot.accounts.map(\.slot)).isSubset(of: Set(AccountSlots.all)) else {
            throw SnapshotError.invalid
        }
        for account in snapshot.accounts {
            if account.state == "not_registered" && account.registered == true { throw SnapshotError.invalid }
            for value in [account.usage?.remainingPercent, account.usage?.primary.remainingPercent,
                          account.usage?.secondary.remainingPercent].compactMap({ $0 }) {
                guard value.isFinite, (0...100).contains(value) else { throw SnapshotError.invalid }
            }
        }
        return snapshot
    }

    // The temporary one-shot bridge has no helper-side history between refreshes.
    // Retain earlier successful values only as explicitly stale observations.
    public func merging(previous: [AccountSnapshot]) -> [AccountSnapshot] {
        accounts.sorted { $0.slot < $1.slot }.map { incoming in
            var result = incoming
            if incoming.stale && incoming.state != "not_registered", incoming.usage == nil,
               let old = previous.first(where: { $0.slot == incoming.slot }) {
                result.usage = old.usage
                result.lastSuccess = old.lastSuccess
            }
            return result
        }
    }
}

public enum SessionPhase: String, CaseIterable {
    case active, starting, preparing, waiting, failed, closed, disconnected

    public var highlightsActiveSession: Bool { self == .active }
    public var label: String {
        switch self {
        case .active: return "활성 세션"
        case .starting: return "요청 준비"
        case .preparing: return "계정 전환 준비 중"
        case .waiting: return "사용자 입력 대기"
        case .failed: return "요청 실패"
        case .closed: return "실행 종료"
        case .disconnected: return "세션 연동 대기"
        }
    }
}
