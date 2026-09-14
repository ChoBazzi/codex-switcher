import Foundation
import SwitcherUIModel

@main
enum AccountLoginCheck {
    @MainActor static func main() async throws {
        precondition(AccountLogin.command(for: "not_registered") == "login")
        precondition(AccountLogin.command(for: "auth_expired") == "reauth")
        precondition(AccountLogin.command(for: "auth_error") == "reauth")
        precondition(AccountLogin.command(for: "fetch_error") == nil)
        precondition(AccountLogin.command(for: "unknown") == nil)
        precondition(AccountLogin.errorMessage(from: Data("Authorization: synthetic-secret".utf8)) == nil)
        let event = Data("{\"event\":\"account_login\",\"slot\":\"b\",\"state\":\"succeeded\"}".utf8)
        precondition(AccountLogin.state(from: event, slot: "a") == nil)

        let dir = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("switcher-login-check-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: dir) }
        let helper = dir.appendingPathComponent("synthetic helper")
        func script(_ text: String) throws {
            try Data(("#!/bin/sh\n" + text).utf8).write(to: helper)
            try FileManager.default.setAttributes([.posixPermissions: 0o700], ofItemAtPath: helper.path)
        }
        try script("""
        [ "$1" = account ] && [ "$2" = login ] && [ "$3" = a ] || exit 1
        printf '%s\\n' '{"event":"account_login","slot":"a","state":"browser_waiting"}' '{"event":"account_login","slot":"a","state":"succeeded"}'
        """)
        let store = AccountLoginStore()
        var completions = 0
        store.start(helper: helper, slot: "a", command: "login") { completions += 1 }
        store.start(helper: helper, slot: "b", command: "login") { completions += 1 }
        try await settle(store)
        precondition(completions == 1 && store.message.contains("연결 완료"))

        try script("printf '%s\\n' 'account_already_registered' >&2\nexit 1\n")
        store.start(helper: helper, slot: "b", command: "login") { completions += 1 }
        try await settle(store)
        precondition(completions == 2 && store.message.contains("다른 슬롯"))

        try script("printf '%s\\n' '{\"event\":\"account_login\",\"slot\":\"a\",\"state\":\"succeeded\"}'\nexit 1\n")
        store.start(helper: helper, slot: "a", command: "reauth") { completions += 1 }
        try await settle(store)
        precondition(!store.message.contains("연결 완료"))

        try script("trap 'exit 1' TERM\nwhile :; do /bin/sleep 0.1; done\n")
        store.start(helper: helper, slot: "a", command: "login") { completions += 1 }
        try await Task.sleep(nanoseconds: 100_000_000)
        precondition(store.isBrowserLogin && store.canCancel)
        store.cancel()
        precondition(!store.canCancel)
        try await settle(store)
        precondition(completions == 4 && store.message.contains("취소"))
        store.start(helper: helper, slot: "a", command: "login") { completions += 1 }
        store.cancel()
        try await settle(store)
        precondition(completions == 5 && store.message.contains("시작하기 전에"))
        try script("printf '%s\\n' '{\"event\":\"account_logout\",\"slot\":\"a\",\"state\":\"succeeded\"}'\n")
        store.start(helper: helper, slot: "a", command: "logout") { completions += 1 }
        try await settle(store)
        precondition(completions == 6 && store.message.contains("로그아웃 완료"))
        precondition(!store.canCancel && !store.isBrowserLogin)
        // Closing a browser sends no callback. Simulate an idle OAuth helper:
        // timeout must terminate it, reconcile once, and allow another login.
        try script("trap 'exit 1' TERM\nwhile :; do /bin/sleep 0.1; done\n")
        let abandoned = AccountLoginStore(loginTimeoutNanoseconds: 200_000_000)
        var reconciliations = 0
        abandoned.start(helper: helper, slot: "c", command: "login") { reconciliations += 1 }
        try await settle(abandoned)
        precondition(reconciliations == 1 && !abandoned.busy && abandoned.message.contains("취소"))
        try script("printf '%s\\n' '{\"event\":\"account_login\",\"slot\":\"c\",\"state\":\"succeeded\"}'\n")
        abandoned.start(helper: helper, slot: "c", command: "login") { reconciliations += 1 }
        try await settle(abandoned)
        precondition(reconciliations == 2 && abandoned.message.contains("연결 완료"))
        precondition(AccountLogin.state(from: event, slot: "b", command: "logout") == nil)
        print("PASS: login/reauth routing, progress protocol, redaction, duplicate prevention, exit validation, cancellation")
    }

    @MainActor static func settle(_ store: AccountLoginStore) async throws {
        for _ in 0..<700 {
            if !store.busy { return }
            try await Task.sleep(nanoseconds: 10_000_000)
        }
        preconditionFailure("login did not finish")
    }
}
