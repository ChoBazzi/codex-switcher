import Foundation
import Darwin
import SwitcherUIModel

struct TransferOutput {
    let result: TransferResult
    let answer: String
}

/// One explicit action, no shell and no retries. Keep generated output separate
/// from helper diagnostics so a model cannot forge a successful handoff event.
enum TransferBridge {
    static func run(_ action: TransferAction, context: TransferContext, input: String, boundary: Bool) async throws -> TransferOutput {
        let arguments = try context.arguments(action, input: input, boundary: boundary)
        return try await Task.detached(priority: .userInitiated) {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: context.helper)
            process.arguments = arguments
            process.standardInput = FileHandle.nullDevice
            let stdout = Pipe(), stderr = Pipe()
            process.standardOutput = stdout; process.standardError = stderr
            try process.run()
            let timeout: Double = action == .write ? 240 : action == .send ? 600 : 60
            let terminate = DispatchWorkItem { if process.isRunning { process.terminate() } }
            let killTask = DispatchWorkItem { if process.isRunning { kill(process.processIdentifier, SIGKILL) } }
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout, execute: terminate)
            DispatchQueue.global().asyncAfter(deadline: .now() + timeout + 5, execute: killTask)
            defer { terminate.cancel(); killTask.cancel() }
            // Drain both pipes concurrently; a full diagnostic pipe must not block exec.
            async let out = read(stdout.fileHandleForReading, limit: 1_048_576, process: process)
            async let err = read(stderr.fileHandleForReading, limit: 131_072, process: process)
            let output: Data, diagnostics: Data
            do {
                (output, diagnostics) = try await (out, err)
            } catch {
                if process.isRunning { kill(process.processIdentifier, SIGKILL) }
                process.waitUntilExit()
                throw error
            }
            process.waitUntilExit()
            let control = action == .write || action == .send ? diagnostics : output
            let code: Int32 = process.terminationReason == .exit ? process.terminationStatus : -1
            let result = try TransferResult.decode(control, action: action, context: context, exitCode: code)
            return TransferOutput(result: result, answer: action == .send ? String(decoding: output, as: UTF8.self) : "")
        }.value
    }

    private static func read(_ handle: FileHandle, limit: Int, process: Process) async throws -> Data {
        try await Task.detached {
            defer { try? handle.close() }
            var data = Data()
            while true {
                let chunk = try handle.read(upToCount: 4096) ?? Data()
                if chunk.isEmpty { return data }
                guard data.count + chunk.count <= limit else {
                    if process.isRunning { process.terminate() }
                    throw SnapshotError.invalid
                }
                data.append(chunk)
            }
        }.value
    }
}
