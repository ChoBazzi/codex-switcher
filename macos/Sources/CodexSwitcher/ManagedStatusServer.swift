import Foundation
import Security
import Darwin

/// Owns only a child launched by this instance. EOF on stdin also handles app crashes.
@MainActor
final class ManagedStatusServer {
    private var process: Process?
    private var lifetime: Pipe?
    var isRunning: Bool { process?.isRunning == true }

    func start(helper: URL, environment: [String: String] = ProcessInfo.processInfo.environment) async throws -> SessionBridge {
        stop()
        var bytes = [UInt8](repeating: 0, count: 32)
        guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else { throw BridgeError.failed }
        let token = bytes.map { String(format: "%02x", $0) }.joined()
        let child = Process(), input = Pipe(), output = Pipe()
        child.executableURL = helper
        child.arguments = ["status-server", "--managed", "--port", "0"]
        var env = environment
        env["SWITCHER_CONTROL_TOKEN"] = token
        child.environment = env
        child.standardInput = input
        child.standardOutput = output
        child.standardError = FileHandle.nullDevice
        try child.run()
        // The parent keeps only the write end of the lifetime pipe.
        try? input.fileHandleForReading.close()
        try? output.fileHandleForWriting.close()
        process = child; lifetime = input
        let timeout = Task { @MainActor in
            try? await Task.sleep(nanoseconds: 5_000_000_000)
            if !Task.isCancelled && self.process === child { self.stop() }
        }
        defer { timeout.cancel() }
        var pendingBridge: SessionBridge?
        do {
            let data = try await Task.detached {
                defer { try? output.fileHandleForReading.close() }
                var data = Data()
                while data.count < 4096 {
                    guard let byte = try output.fileHandleForReading.read(upToCount: 1), !byte.isEmpty else { throw BridgeError.failed }
                    if byte.first == 10 { return data }
                    data.append(byte)
                }
                throw BridgeError.failed
            }.value
            struct Ready: Decodable { let event: String; let port: Int }
            let ready = try JSONDecoder().decode(Ready.self, from: data)
            guard process === child, child.isRunning, ready.event == "status_server_ready", (1...65535).contains(ready.port),
                  let bridge = SessionBridge(environment: ["SWITCHER_CONTROL_TOKEN": token, "SWITCHER_CONTROL_URL": "http://127.0.0.1:\(ready.port)"]) else { throw BridgeError.failed }
            pendingBridge = bridge
            _ = try await bridge.read() // Authentication and schema, not just a listening socket.
            guard process === child, child.isRunning else { bridge.close(); throw BridgeError.failed }
            return bridge
        } catch {
            pendingBridge?.close()
            if process === child { stop() }
            throw error
        }
    }

    func stop() {
        let child = process
        process = nil
        try? lifetime?.fileHandleForWriting.close()
        lifetime = nil
        // Closing the owned pipe is the normal shutdown. Escalate only this child.
        if let child {
            DispatchQueue.global().asyncAfter(deadline: .now() + 2) {
                if child.isRunning { child.terminate() }
            }
            DispatchQueue.global().asyncAfter(deadline: .now() + 4) {
                if child.isRunning { kill(child.processIdentifier, SIGKILL) }
            }
        }
    }
}
