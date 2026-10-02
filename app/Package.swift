// swift-tools-version: 6.0
import PackageDescription

// The farero macOS app (기능 명세서 F-01, 5-3, F-11, F-12). It is a UI client of
// farerod over the local Unix socket (docs/ipc.md).
//
//   FareroCore: Foundation-only IPC client, models, state reducer and the
//               pure decisions (character state, notch mode, notch geometry).
//   Farero:     the AppKit/SwiftUI accessory app (menu bar, notch panel,
//               approval cards, shortcuts, daemon registration).
let package = Package(
    name: "Farero",
    platforms: [.macOS(.v15)],
    products: [
        .executable(name: "Farero", targets: ["Farero"]),
        .library(name: "FareroCore", targets: ["FareroCore"]),
    ],
    dependencies: [
        .package(path: "LighthouseKit"),
    ],
    targets: [
        .target(name: "FareroCore"),
        .executableTarget(
            name: "Farero",
            dependencies: [
                "FareroCore",
                .product(name: "LighthouseKit", package: "LighthouseKit"),
            ]
        ),
        .testTarget(name: "FareroCoreTests", dependencies: ["FareroCore"]),
    ]
)
