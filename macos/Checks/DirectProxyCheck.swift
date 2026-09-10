import Foundation

@main
struct DirectProxyCheck {
    @MainActor static func main() async throws {
        if CommandLine.arguments.contains("switch-probe") {
            func emit(_ value: [String: Any]) {
                let data = try! JSONSerialization.data(withJSONObject: value)
                try! FileHandle.standardOutput.write(contentsOf: data + Data([10]))
            }
            var slot = "a", revision = 0, toolWaiting = false, failed = false, usageReads = 0
            func state(_ event: String, _ accepted: Bool = false) {
                emit(["event":event,"slot":slot,"busy":toolWaiting,"failed":failed,"connected":true,"revision":revision,"accepted":accepted,"can_abandon_turn":toolWaiting])
            }
            emit(["event":"probe_ready","codex_home":"/private/tmp/synthetic-unused-home"])
            state("probe_state")
            while let line = readLine(), let data = line.data(using: .utf8),
                  let command = try JSONSerialization.jsonObject(with: data) as? [String: Any] {
                if command["action"] as? String == "status" { state("probe_state"); continue }
                if command["action"] as? String == "abandon_turn" {
                    let accepted = toolWaiting && command["revision"] as? Int == revision
                    if accepted { toolWaiting = false; failed = true; revision += 1 }
                    state("probe_abandonment", accepted)
                    continue
                }
                if command["action"] as? String == "usage_refresh" {
                    let requestID = command["request_id"] as! Int
                    usageReads += 1
                    emit(["event":"usage_refresh","request_id":requestID,"status":"started"])
                    if usageReads == 2 {
                        // An old completion must not finish this request.
                        emit(["event":"usage_refresh","request_id":requestID-1,"status":"finished","succeeded":true])
                        continue
                    }
                    if usageReads == 3 {
                        emit(["event":"usage_snapshot","accounts":[]])
                        continue
                    }
                    var accounts = ["a","b","c","d","e"].map { ["slot":$0,"state":"not_registered","stale":true,"registered":false] as [String:Any] }
                    accounts[1] = ["slot":"b","state":"ok","stale":false,"registered":true,"last_success":"2026-09-10T00:00:00Z",
                                   "usage":["remaining_percent":30,"primary":["remaining_percent":70,"limit_seconds":18000],"secondary":["remaining_percent":30,"limit_seconds":604800]]]
                    emit(["event":"usage_snapshot","accounts":accounts])
                    emit(["event":"usage_refresh","request_id":requestID,"status":"finished","succeeded":true])
                    continue
                }
                let accepted = command["revision"] as? Int == revision
                if accepted { slot = command["slot"] as! String; revision += 1 }
                state("probe_selection", accepted)
                if slot == "e" { toolWaiting = true; state("probe_state") }
            }
            return
        }
        func waitFor(_ condition: () -> Bool) async throws {
            for _ in 0..<100 {
                if condition() { return }
                try await Task.sleep(nanoseconds: 20_000_000)
            }
            fatalError("direct proxy check timed out")
        }
        let store = DirectProxyStore(usageReadTimeout: 200_000_000)
        var usageCount = 0
        var unavailableCount = 0
        store.onUsage = { snapshot in
            precondition(snapshot.accounts.count == 5 && snapshot.accounts[0].state == "not_registered")
            precondition(snapshot.accounts[1].registered == true && snapshot.accounts[1].state == "ok")
            precondition(snapshot.accounts[1].usage?.primary.remainingPercent == 70)
            precondition(snapshot.accounts[1].usage?.secondary.remainingPercent == 30)
            usageCount += 1
        }
        store.onUsageUnavailable = { unavailableCount += 1 }
        store.start(helper: URL(fileURLWithPath: CommandLine.arguments[0]))
        try await waitFor { store.ready }
        precondition(store.canReadUsage)
        store.readUsage()
        precondition(store.usageRefreshing && !store.canReadUsage)
        store.readUsage() // Duplicate request must be suppressed.
        try await waitFor { usageCount == 1 && !store.usageRefreshing }
        precondition(store.usageRefreshMessage!.contains("사용량 확인 완료"))
        store.readUsage()
        try await waitFor { !store.usageRefreshing }
        precondition(store.usageRefreshMessage!.contains("시간 초과") && usageCount == 1 && store.canReadUsage)
        store.readUsage()
        try await waitFor { !store.usageRefreshing }
        precondition(store.usageRefreshMessage == "사용량 데이터 확인 실패" && usageCount == 1)
        store.readUsage()
        try await waitFor { usageCount == 2 && !store.usageRefreshing }
        precondition(store.usageRefreshMessage!.contains("사용량 확인 완료"))
        precondition(store.slot == "a" && store.canSelect("b") && !store.canSelect("a"))
        store.failed = true
        precondition(store.canSelect("b") && !store.canSelect("a"))
        store.busy = true
        precondition(!store.canSelect("b"))
        store.busy = false
        store.select("b")
        precondition(store.pending && !store.canSelect("a"))
        try await waitFor { !store.pending }
        precondition(store.slot == "b" && store.connected && !store.busy)
        for slot in ["c", "d", "e"] {
            precondition(store.canSelect(slot))
            store.select(slot)
            try await waitFor { !store.pending }
            precondition(store.slot == slot)
        }
        precondition(!store.canSelect("f"))
        try await waitFor { store.canAbandonTurn }
        precondition(store.busy && !store.canSelect("a"))
        store.abandonTurn()
        precondition(store.pending && !store.canAbandonTurn)
        try await waitFor { !store.pending }
        precondition(store.failed && !store.busy && store.canSelect("a") && !store.canAbandonTurn)
        store.poll()
        store.stop()
        try await Task.sleep(nanoseconds: 100_000_000)
        precondition(!store.ready && store.home == nil && !store.canSelect("a"))
        precondition(!store.canReadUsage && !store.usageRefreshing)
        store.readUsage()
        precondition(!store.usageRefreshing && store.usageRefreshMessage!.contains("연결 후"))
        precondition(unavailableCount >= 2)
        print("PASS: direct proxy selection; remote refresh protocol, duplicate suppression, timeout, old reply isolation, malformed reply, recovery and disconnect (synthetic)")
    }
}
