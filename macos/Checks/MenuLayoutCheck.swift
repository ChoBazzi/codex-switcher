import AppKit
import SwiftUI

@main
struct MenuLayoutCheck {
    @MainActor static func main() throws {
        // --demo makes MenuStore skip Keychain, child processes and networking.
        precondition(CommandLine.arguments.contains("--demo"))
        let store = MenuStore()
        defer { store.shutdown() }
        precondition(store.accounts.count == 5)
        let settings = CodexSettingsStore()
        let direct = store.direct
        direct.ready = true
        for event: [String: Any] in [
            ["event":"helper_build", "build_id":String(repeating:"a",count:64)],
            ["event":"probe_ready", "build_id":String(repeating:"b",count:64), "protocol_version":1],
            ["event":"probe_diagnostic", "scope":"root", "code":"compaction_owner_unavailable", "at":"2026-09-21T00:00:00Z"],
            ["event":"probe_diagnostic", "scope":"auxiliary", "code":"auxiliary_credential_changed", "at":"2026-09-21T00:00:00Z"]
        ] { direct.receiveDiagnosticEvent(try JSONSerialization.data(withJSONObject:event)) }
        direct.receiveAuthenticationState(try JSONSerialization.data(withJSONObject: ["authentication": [
            ["slot":"a", "waiting":4, "refreshing":true, "canceled":true],
            ["slot":"b", "waiting":2, "refreshing":false, "canceled":false],
            ["slot":"c", "waiting":1, "refreshing":false, "canceled":false],
            ["slot":"d", "waiting":1, "refreshing":false, "canceled":false],
            ["slot":"e", "waiting":1, "refreshing":false, "canceled":false]
        ]]))
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: ProxyDiagnosticsPanel(direct: direct, back: {})
                .background(Color(nsColor: .windowBackgroundColor)).environment(\.colorScheme, scheme).fixedSize())
            guard let image = renderer.nsImage else { fatalError("diagnostics render failed") }
            precondition(abs(image.size.width - 620) < 1 && image.size.height < 800, "diagnostics exceed menu size budget")
            if let path = ProcessInfo.processInfo.environment["SWITCHER_LAYOUT_PREVIEW"],
               let tiff = image.tiffRepresentation, let bitmap = NSBitmapImageRep(data:tiff),
               let png = bitmap.representation(using:.png,properties:[:]) {
                try png.write(to:URL(fileURLWithPath:path + "-diagnostics-\(scheme == .light ? "light" : "dark").png"))
            }
            print("PASS: diagnostics render \(scheme), \(Int(image.size.width))×\(Int(image.size.height))")
        }
        direct.stop()
        for scheme in [ColorScheme.light, .dark] {
            let renderer = ImageRenderer(content: CodexSettingsPanel(settings: settings, direct: store.direct)
                .background(Color(nsColor: .windowBackgroundColor))
                .environment(\.colorScheme, scheme).fixedSize())
            guard let image = renderer.nsImage else { fatalError("settings render failed") }
            precondition(image.size.width <= 640 && image.size.height < 750, "settings panel exceeds size budget")
            if let path = ProcessInfo.processInfo.environment["SWITCHER_LAYOUT_PREVIEW"],
               let tiff = image.tiffRepresentation, let bitmap = NSBitmapImageRep(data: tiff),
               let png = bitmap.representation(using: .png, properties: [:]) {
                try png.write(to: URL(fileURLWithPath: path + "-settings-\(scheme == .light ? "light" : "dark").png"))
            }
            print("PASS: Codex settings render \(scheme), \(Int(image.size.width))×\(Int(image.size.height))")
        }
        for count in [0, 1, 2, 3, 5] {
            let saved = store.accounts
            store.accounts = Array(saved.prefix(count))
            if count == 5 {
                let rows = (1...5).map { ["id":String($0), "ready":true, "busy":$0 == 2, "failed":$0 == 3] as [String: Any] }
                direct.receiveConnections(try JSONSerialization.data(withJSONObject: ["event":"connection_list", "selected":"1", "connections":rows, "limit":5, "can_delete":true]))
            }
            for scheme in [ColorScheme.light, .dark] {
                let renderer = ImageRenderer(content: MenuPanel(store: store)
                    .background(Color(nsColor: .windowBackgroundColor))
                    .environment(\.colorScheme, scheme).fixedSize())
                guard let image = renderer.nsImage else { fatalError("menu render failed") }
                try FileHandle.standardError.write(contentsOf: Data("menu size: \(count), \(scheme), \(image.size)\n".utf8))
                precondition(abs(image.size.width - 620) < 1, "unexpected menu width")
                if count == 5, let path = ProcessInfo.processInfo.environment["SWITCHER_LAYOUT_PREVIEW"] {
                    guard let tiff = image.tiffRepresentation,
                          let bitmap = NSBitmapImageRep(data: tiff),
                          let png = bitmap.representation(using: .png, properties: [:]) else { fatalError("PNG render failed") }
                    try png.write(to: URL(fileURLWithPath: path + "-\(scheme == .light ? "light" : "dark").png"))
                }
                precondition(image.size.height < 850, "account grid exceeds menu height budget")
                print("PASS: menu render \(count) accounts, \(scheme), \(Int(image.size.width))×\(Int(image.size.height))")
                if count == 5 {
                    let confirmation = ProxyConnectionDeletion(id: "3", home: "/synthetic", revision: 1)
                    let deletionRenderer = ImageRenderer(content: MenuPanel(store: store, connectionDeletion: confirmation)
                        .background(Color(nsColor: .windowBackgroundColor))
                        .environment(\.colorScheme, scheme).fixedSize())
                    guard let confirmationImage = deletionRenderer.nsImage else { fatalError("deletion confirmation render failed") }
                    try FileHandle.standardError.write(contentsOf: Data("deletion confirmation size: \(scheme), \(confirmationImage.size)\n".utf8))
                    precondition(abs(confirmationImage.size.width - 620) < 1 && confirmationImage.size.height < 850,
                                 "deletion confirmation exceeds menu size budget")
                    print("PASS: deletion confirmation render \(scheme), \(Int(confirmationImage.size.width))×\(Int(confirmationImage.size.height))")
                }
            }
            store.accounts = saved
        }
    }
}
