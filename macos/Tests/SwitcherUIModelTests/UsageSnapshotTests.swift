import XCTest
@testable import SwitcherUIModel

final class UsageSnapshotTests: XCTestCase {
    private func sample(_ a: String = "24", state: String = "ok", stale: Bool = false,
                        success: String = "\"2026-09-07T14:00:00.123456Z\"") -> Data {
        Data("""
        {"event":"usage_snapshot","accounts":[
        {"slot":"a","state":"\(state)","stale":\(stale),"last_success":\(success),
        "usage":{"remaining_percent":\(a),"primary":{"remaining_percent":\(a),"limit_seconds":18000},"secondary":{"remaining_percent":62,"limit_seconds":604800}}},
        {"slot":"b","state":"not_registered","stale":true,"last_success":null,"usage":null}]}
        """.utf8)
    }

    func testZeroAndUnknownDiffer() throws {
        let zero = try UsageSnapshot.decode(sample("0"))
        let unknown = try UsageSnapshot.decode(sample("null"))
        XCTAssertEqual(zero.accounts[0].usage?.primary.remainingPercent, 0)
        XCTAssertNil(unknown.accounts[0].usage?.primary.remainingPercent)
        XCTAssertEqual(zero.accounts[0].usage?.primary.title, "5시간")
        XCTAssertEqual(zero.accounts[0].usage?.secondary.title, "주간")
    }

    func testAgesAtTwoMinutesAndAcceptsBothDateFormats() throws {
        let item = try UsageSnapshot.decode(sample()).accounts[0]
        let date = try XCTUnwrap(item.lastSuccess)
        XCTAssertFalse(item.isStale(at: date.addingTimeInterval(119)))
        XCTAssertTrue(item.isStale(at: date.addingTimeInterval(120)))
        XCTAssertNoThrow(try UsageSnapshot.decode(sample(success: "\"2026-09-07T14:00:00Z\"")))
    }

    func testRejectsMalformedContract() {
        XCTAssertThrowsError(try UsageSnapshot.decode(sample("101")))
        XCTAssertThrowsError(try UsageSnapshot.decode(sample("-1")))
        let wrongEvent = String(decoding: sample(), as: UTF8.self)
            .replacingOccurrences(of: "usage_snapshot", with: "account_login")
        XCTAssertThrowsError(try UsageSnapshot.decode(Data(wrongEvent.utf8)))
        let duplicate = String(decoding: sample(), as: UTF8.self)
            .replacingOccurrences(of: "\"slot\":\"b\"", with: "\"slot\":\"a\"")
        XCTAssertThrowsError(try UsageSnapshot.decode(Data(duplicate.utf8)))
        XCTAssertThrowsError(try UsageSnapshot.decode(Data(repeating: 32, count: 131_073)))
    }

    func testFailureRetainsStaleValuesButUnregisteredClearsThem() throws {
        let old = try UsageSnapshot.decode(sample()).accounts
        let raw = """
        {"event":"usage_snapshot","accounts":[
        {"slot":"a","state":"rate_limited","stale":true,"last_success":null,"usage":null},
        {"slot":"b","state":"not_registered","stale":true,"last_success":null,"usage":null}]}
        """
        let failed = try UsageSnapshot.decode(Data(raw.utf8)).merging(previous: old)
        XCTAssertEqual(failed[0].usage, old[0].usage)
        XCTAssertEqual(failed[0].stateLabel, "조회 제한")
        XCTAssertTrue(failed[0].stale)
        let cleared = raw.replacingOccurrences(of: "rate_limited", with: "not_registered")
        XCTAssertNil(try UsageSnapshot.decode(Data(cleared.utf8)).merging(previous: old)[0].usage)
    }

    func testOnlyActualActivePhaseIsHighlighted() {
        for phase in SessionPhase.allCases {
            XCTAssertEqual(phase.highlightsActiveSession, phase == .active)
        }
    }
}
