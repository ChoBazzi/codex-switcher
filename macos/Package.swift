// swift-tools-version: 5.9
import PackageDescription

let package = Package(
    name: "CodexSwitcher",
    platforms: [.macOS(.v13)],
    products: [.executable(name: "CodexSwitcher", targets: ["CodexSwitcher"])],
    targets: [
        // Archived Wiki harness sources remain available to explicit checks,
        // but are not compiled into the account-switching application.
        .target(name: "SwitcherUIModel", exclude: ["Transfer.swift"]),
        .executableTarget(name: "CodexSwitcher", dependencies: ["SwitcherUIModel"],
                          exclude: ["TransferStore.swift", "TransferBridge.swift"]),
        .testTarget(name: "SwitcherUIModelTests", dependencies: ["SwitcherUIModel"])
    ]
)
