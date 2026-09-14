import Foundation
import Darwin
import SwitcherUIModel

enum BridgeError: Error { case unavailable, failed }

/// Temporary, read-only adapter. Never runs a shell, login, exec, or handoff.
enum UsageBridge {
    static func read(helper: URL) throws -> UsageSnapshot {
        guard helper.isFileURL, FileManager.default.isExecutableFile(atPath: helper.path) else {
            throw BridgeError.unavailable
        }
        let process = Process()
        let pipe = Pipe()
        process.executableURL = helper
        process.arguments = ["usage"]
        process.standardInput = FileHandle.nullDevice
        process.standardOutput = pipe
        // Do not surface raw helper diagnostics or authentication material in UI.
        process.standardError = FileHandle.nullDevice
        try process.run()
        let timeout = DispatchWorkItem {
            if process.isRunning { process.terminate() }
        }
        let hardTimeout = DispatchWorkItem {
            if process.isRunning { kill(process.processIdentifier, SIGKILL) }
        }
        DispatchQueue.global().asyncAfter(deadline: .now() + 30, execute: timeout)
        DispatchQueue.global().asyncAfter(deadline: .now() + 32, execute: hardTimeout)
        defer {
            timeout.cancel()
            hardTimeout.cancel()
            try? pipe.fileHandleForReading.close()
        }
        var data = Data()
        while true {
            let chunk = pipe.fileHandleForReading.readData(ofLength: 4096)
            if chunk.isEmpty { break }
            data.append(chunk)
            guard data.count <= 128 * 1024 else {
                if process.isRunning { kill(process.processIdentifier, SIGKILL) }
                process.waitUntilExit()
                throw BridgeError.failed
            }
        }
        process.waitUntilExit()
        // Exit 1 may still carry a valid snapshot with per-account errors.
        guard process.terminationReason == .exit,
              [0, 1].contains(process.terminationStatus) else { throw BridgeError.failed }
        return try UsageSnapshot.decode(data)
    }
}
