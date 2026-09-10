import Foundation

// Standalone synthetic checks for Macs with Command Line Tools but no XCTest.
@main
enum ModelCheck {
    static func main() throws {
        var unknownAccount = AccountSnapshot.empty("a")
        precondition(!unknownAccount.canLogoutAccount)
        unknownAccount.registered = true
        precondition(unknownAccount.canLogoutAccount)
        unknownAccount.registered = false
        precondition(!unknownAccount.canLogoutAccount)
        print("PASS registered account logout independent of unknown quota")
        for (plan, label) in [("free", "Free · 무료"), ("plus", "Plus"), ("business", "Business"), ("unrecognized", "요금제 미확인")] {
            let data = Data("""
            {"event":"usage_snapshot","accounts":[{"slot":"a","registered":true,"state":"unknown","stale":false,"usage":{"plan_type":"\(plan)","primary":{},"secondary":{}}}]}
            """.utf8)
            let account = try UsageSnapshot.decode(data).accounts[0]
            precondition(account.planLabel == label && account.canLogoutAccount)
            precondition(account.usage?.remainingPercent == nil)
        }
        precondition(AccountSnapshot.empty("a").planLabel == "요금제 미확인")
        print("PASS optional plan display without inventing quota or disabling logout")
        var logout = LogoutConfirmation()
        logout.begin(slot: "invalid")
        precondition(logout.slot == nil)
        logout.begin(slot: "a")
        logout.begin(slot: "b")
        precondition(logout.slot == "a")
        precondition(logout.confirm(allowed: false) == nil && logout.slot == "a")
        logout.cancel()
        precondition(logout.confirm(allowed: true) == nil)
        logout.begin(slot: "b")
        precondition(logout.confirm(allowed: true) == "b" && logout.slot == nil)
        precondition(logout.confirm(allowed: true) == nil)
        print("PASS inline logout confirmation: cancel, busy guard, exact slot, single submission")
        let five = try UsageSnapshot.decode(Data("""
        {"event":"usage_snapshot","accounts":[
        {"slot":"a","registered":true,"state":"stored_unverified","stale":true},
        {"slot":"b","registered":true,"state":"auth_expired","stale":true},
        {"slot":"c","registered":true,"state":"auth_error","stale":true},
        {"slot":"d","registered":true,"state":"limit_reached","stale":true},
        {"slot":"e","registered":true,"state":"ok","stale":false}]}
        """.utf8))
        precondition(five.accounts.count == 5 && AccountSlots.firstVacancy(in: five.accounts) == nil)
        var reusable = five.accounts
        reusable[2].registered = false
        reusable[2].state = "not_registered"
        precondition(AccountSlots.firstVacancy(in: reusable) == "c")
        reusable[2].state = "unknown"
        precondition(AccountSlots.firstVacancy(in: reusable) == nil)
        reusable[2].state = "auth_error"
        precondition(AccountSlots.firstVacancy(in: reusable) == nil)
        logout.begin(slot: "e")
        precondition(logout.confirm(allowed: true) == "e")
        print("PASS five account capacity, expired/full occupancy, confirmed vacancy reuse")
        let raw = """
        {"event":"usage_snapshot","accounts":[
        {"slot":"a","state":"ok","stale":false,"last_success":"2026-09-07T14:00:00.123456Z",
        "usage":{"remaining_percent":0,"primary":{"remaining_percent":0,"limit_seconds":18000},"secondary":{"remaining_percent":null,"limit_seconds":604800}}},
        {"slot":"b","state":"not_registered","stale":true,"last_success":null,"usage":null}]}
        """
        let sample = try UsageSnapshot.decode(Data(raw.utf8))
        let account = sample.accounts[0]
        precondition(account.usage?.primary.remainingPercent == 0)
        precondition(account.usage?.secondary.remainingPercent == nil)
        precondition(account.usage?.primary.title == "5시간")
        let date = account.lastSuccess!
        precondition(!account.isStale(at: date.addingTimeInterval(119)))
        precondition(account.isStale(at: date.addingTimeInterval(120)))
        for phase in SessionPhase.allCases {
            precondition(phase.highlightsActiveSession == (phase == .active))
        }
        for invalid in [raw.replacingOccurrences(of: "usage_snapshot", with: "bad"),
                        raw.replacingOccurrences(of: "\"slot\":\"b\"", with: "\"slot\":\"a\""),
                        raw.replacingOccurrences(of: "\"remaining_percent\":0", with: "\"remaining_percent\":101")] {
            precondition((try? UsageSnapshot.decode(Data(invalid.utf8))) == nil)
        }
        precondition((try? UsageSnapshot.decode(Data(repeating: 32, count: 131_073))) == nil)
        let failed = """
        {"event":"usage_snapshot","accounts":[
        {"slot":"a","state":"rate_limited","stale":true,"last_success":null,"usage":null},
        {"slot":"b","state":"not_registered","stale":true,"last_success":null,"usage":null}]}
        """
        let merged = try UsageSnapshot.decode(Data(failed.utf8)).merging(previous: sample.accounts)
        precondition(merged[0].usage == account.usage && merged[0].stale)
        precondition(merged[0].stateLabel == "조회 제한")
        let cleared = failed.replacingOccurrences(of: "rate_limited", with: "not_registered")
        let unregistered = try UsageSnapshot.decode(Data(cleared.utf8)).merging(previous: sample.accounts)
        precondition(unregistered[0].usage == nil)
        let sessionJSON = """
        {"event":"session_snapshot","generated_at":"2026-09-07T14:00:00Z","sessions":[
        {"conversation":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","project":"\(String(repeating: "1", count: 64))",
        "worktree":"\(String(repeating: "2", count: 64))","branch":"\(String(repeating: "3", count: 64))",
        "slot":"b","state":"active","updated_at":"2026-09-07T14:00:00Z","heartbeat":"2026-09-07T14:00:00Z"}]}
        """
        let sessions = try SessionSnapshot.decode(Data(sessionJSON.utf8))
        precondition(sessions.sessions[0].phase(at: sessions.generatedAt) == .active)
        precondition(sessions.sessions[0].phase(at: sessions.generatedAt.addingTimeInterval(5)) == .disconnected)
        precondition(sessions.sessions[0].phase(at: sessions.generatedAt.addingTimeInterval(-2)) == .disconnected)
        let closedJSON = sessionJSON.replacingOccurrences(of: "\"active\"", with: "\"closed\"")
        let closed = try SessionSnapshot.decode(Data(closedJSON.utf8))
        precondition(closed.sessions[0].phase(at: sessions.generatedAt.addingTimeInterval(6)) == .closed)
        let badSession = sessionJSON.replacingOccurrences(of: "\"slot\":\"b\"", with: "\"slot\":\"f\"")
        precondition((try? SessionSnapshot.decode(Data(badSession.utf8))) == nil)
        print("PASS: usage parsing, unknown/zero, staleness, retention, active-session highlighting")
        print("PASS: session snapshot, active/closed, heartbeat expiry, invalid slot rejection")
    }
}
