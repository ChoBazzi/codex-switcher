import Foundation

@main
enum ManagedServerCheck {
    @MainActor static func main() async throws {
        precondition(CommandLine.arguments.count == 2)
        let helper = URL(fileURLWithPath: CommandLine.arguments[1])
        let temporary = URL(fileURLWithPath: NSTemporaryDirectory()).appendingPathComponent("switcher-owned-server-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: temporary, withIntermediateDirectories: false)
        defer { try? FileManager.default.removeItem(at: temporary) }
        var env = ProcessInfo.processInfo.environment
        env["HOME"] = temporary.path // No real status files, Wiki, credentials or cleanup.
        env["SWITCHER_CONTROL_TOKEN"] = "inherited-token-must-be-replaced"
        let first = ManagedStatusServer(), second = ManagedStatusServer()
        defer { first.stop(); second.stop() }
        let a = try await first.start(helper: helper, environment: env)
        let b = try await second.start(helper: helper, environment: env)
        defer { a.close(); b.close() }
        precondition(first.isRunning && second.isRunning)
        let sample = try await a.read()
        precondition(sample.sessions.isEmpty)
        first.stop()
        try await disconnected(a)
        // Stopping our child cannot stop another app's server.
        _ = try await b.read()
        let restarted = try await first.start(helper: helper, environment: env)
        _ = try await restarted.read()
        first.stop()
        try await disconnected(restarted)
        restarted.close()
        second.stop()
        try await disconnected(b)
        do {
            _ = try await first.start(helper: URL(fileURLWithPath: "/usr/bin/false"), environment: env)
            preconditionFailure("failed child accepted")
        } catch { precondition(!first.isRunning) }
        print("PASS: owned server automatic start/authentication, concurrent ports, isolated stop, restart, child failure")
    }

    static func disconnected(_ bridge: SessionBridge) async throws {
        for _ in 0..<50 {
            do { _ = try await bridge.read() } catch { return }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        preconditionFailure("server survived owner stop")
    }
}
