import Foundation
import SwiftUI
import Darwin
import SwitcherUIModel

@MainActor
final class AccountLoginStore: ObservableObject {
    @Published private(set) var busy = false
    @Published private(set) var slot: String?
    @Published private(set) var message = ""
    private var process: Process?
    private var cancelRequested = false
    private var successSeen = false
    private var failureMessage: String?
    private var command = "login"
    private let loginTimeoutNanoseconds: UInt64
    var isBrowserLogin: Bool { command == "login" || command == "reauth" }
    var canCancel: Bool { busy && isBrowserLogin && !cancelRequested }

    init(loginTimeoutNanoseconds: UInt64 = 305_000_000_000) {
        self.loginTimeoutNanoseconds = loginTimeoutNanoseconds
    }

    func start(helper: URL, slot: String, command: String, completion: @escaping () -> Void) {
        guard !busy, AccountSlots.all.contains(slot), ["login", "reauth", "logout"].contains(command) else { return }
        self.command = command
        busy = true; self.slot = slot; cancelRequested = false; successSeen = false; failureMessage = nil
        message = command == "logout" ? "저장된 계정 연결을 해제하는 중…" : "브라우저 로그인 준비 중 · 최대 5분"
        let child = Process(), pipe = Pipe()
        child.executableURL = helper
        child.arguments = ["account", command, slot]
        child.standardInput = FileHandle.nullDevice
        // Both streams originate from the trusted helper, not model responses.
        // Parse only fixed events/error codes; raw output is discarded.
        child.standardOutput = pipe; child.standardError = pipe
        process = child
        Task {
            guard !cancelRequested else {
                process = nil; busy = false
                message = "로그인을 시작하기 전에 취소했습니다."
                completion()
                return
            }
            do {
                try child.run()
                try? pipe.fileHandleForWriting.close()
                let timeout = Task { @MainActor [weak self] in
                    try? await Task.sleep(nanoseconds: command == "logout" ? 30_000_000_000 : self?.loginTimeoutNanoseconds ?? 305_000_000_000)
                    if !Task.isCancelled, self?.process === child { self?.cancel() }
                }
                defer { timeout.cancel() }
                try await Task.detached { [weak self] in
                    defer { try? pipe.fileHandleForReading.close() }
                    var line = Data(), total = 0
                    while let byte = try pipe.fileHandleForReading.read(upToCount: 1), !byte.isEmpty {
                        total += 1
                        guard total <= 65_536, line.count < 4096 else {
                            if child.isRunning { child.terminate() }
                            throw SnapshotError.invalid
                        }
                        if byte.first == 10 {
                            await self?.receive(line, slot: slot)
                            line.removeAll(keepingCapacity: true)
                        } else { line.append(byte) }
                    }
                    if !line.isEmpty { await self?.receive(line, slot: slot) }
                    child.waitUntilExit()
                }.value
                if child.terminationReason == .exit && child.terminationStatus == 0 && successSeen {
                    message = command == "logout" ? "로그아웃 완료 · 대화와 Wiki 파일은 보존했습니다." : "계정 연결 완료 · 사용량을 확인합니다."
                } else {
                    message = failureMessage ?? (command == "logout" ? "로그아웃 결과 미확인. 계정 상태를 다시 확인합니다." : cancelRequested ? "로그인 취소 요청을 처리했습니다. 저장 여부를 다시 확인합니다." : "로그인에 실패했습니다. 브라우저와 Keychain 접근을 확인하세요.")
                }
            } catch {
                if child.isRunning { child.terminate() }
                message = "로그인 실행 결과를 확인하지 못했습니다. helper 경로와 계정 상태를 확인하세요."
            }
            if child.isRunning {
                DispatchQueue.global().asyncAfter(deadline: .now() + 5) {
                    if child.isRunning { kill(child.processIdentifier, SIGKILL) }
                }
            }
            process = nil; busy = false
            completion() // Read-only refresh also reconciles cancellation/save races.
        }
    }

    private func receive(_ line: Data, slot: String) {
        if let error = AccountLogin.errorMessage(from: line) { failureMessage = error }
        guard let state = AccountLogin.state(from: line, slot: slot, command: command) else { return }
        if state == "succeeded" { successSeen = true }
        guard !cancelRequested else { return }
        switch state {
        case "launching": message = "브라우저 로그인을 시작합니다."
        case "browser_waiting": message = "브라우저에서 로그인을 완료하세요 · 최대 5분"
        case "importing": message = "인증 정보를 Keychain에 저장 중입니다."
        default: break // Only successful process completion confirms the outcome.
        }
    }

    func cancel() {
        guard busy, let child = process, !cancelRequested else { return }
        cancelRequested = true
        message = "로그인 취소 중…"
        if child.isRunning { child.terminate() }
        DispatchQueue.global().asyncAfter(deadline: .now() + 5) {
            if child.isRunning { kill(child.processIdentifier, SIGKILL) }
        }
    }
}
