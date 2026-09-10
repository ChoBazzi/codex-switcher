import Foundation

public enum AccountSlots {
    public static let all = ["a", "b", "c", "d", "e"]
    public static let capacity = all.count

    // Missing/unknown/failed observations are not empty slots.
    public static func firstVacancy(in accounts: [AccountSnapshot]) -> String? {
        all.first { slot in accounts.contains { $0.slot == slot && $0.state == "not_registered" && $0.registered != true } }
    }
}

/// Confirmation stays inside the menu-bar panel; presenting it performs no account operation.
public struct LogoutConfirmation {
    public private(set) var slot: String?
    public init() {}

    public mutating func begin(slot: String) {
        guard AccountSlots.all.contains(slot), self.slot == nil else { return }
        self.slot = slot
    }

    public mutating func cancel() { slot = nil }

    public mutating func confirm(allowed: Bool) -> String? {
        guard allowed, let target = slot else { return nil }
        slot = nil
        return target
    }
}

public enum AccountLogin {
    public static func command(for state: String) -> String? {
        switch state {
        case "not_registered": return "login"
        case "auth_expired", "auth_error": return "reauth"
        default: return nil
        }
    }

    public static func state(from line: Data, slot: String, command: String = "login") -> String? {
        guard line.count <= 4096,
              let value = try? JSONSerialization.jsonObject(with: line) as? [String: Any],
              value["event"] as? String == (command == "logout" ? "account_logout" : "account_login"), value["slot"] as? String == slot,
              let state = value["state"] as? String,
              ["launching", "browser_waiting", "importing", "succeeded", "failed", "cancelled"].contains(state) else { return nil }
        return state
    }

    public static func errorMessage(from line: Data) -> String? {
        // Only exact known helper error codes may reach UI; never raw diagnostics.
        let code = String(decoding: line, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        return [
            "affinity_storage_unavailable": "실행 중인 CLI가 있거나 세션 저장소에 접근할 수 없습니다. CLI 종료 후 다시 확인하세요.",
            "account_already_registered": "다른 슬롯에 이미 등록된 계정입니다. 브라우저에서 다른 계정으로 로그인하세요.",
            "account_slot_already_registered": "이미 등록된 슬롯입니다. 사용량을 새로고침한 뒤 다시 로그인을 사용하세요.",
            "reauth_account_mismatch": "기존에 등록한 계정과 다릅니다. 같은 계정으로 다시 로그인하세요.",
            "account_store_unavailable": "Keychain 접근을 확인하세요. 재빌드 후 macOS 접근 승인이 다시 필요할 수 있습니다.",
            "account_operation_busy": "다른 계정 작업이 진행 중입니다. 완료 후 다시 시도하세요.",
            "codex_executable_not_found": "Codex CLI를 찾지 못했습니다. 앱을 실행한 터미널의 PATH를 확인하세요.",
            "browser_login_cancelled": "로그인이 취소됐거나 제한 시간이 지났습니다.",
            "login_cleanup_required": "로그인 임시 파일 정리가 필요합니다. 계정 상태를 확인하세요.",
            "account_slot_not_registered": "저장된 계정이 없습니다. 사용량을 새로고침한 뒤 계정 연결을 사용하세요."
        ][code]
    }
}
