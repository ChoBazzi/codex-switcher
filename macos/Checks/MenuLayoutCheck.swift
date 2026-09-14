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
            }
            store.accounts = saved
        }
    }
}
