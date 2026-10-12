// swift-tools-version: 6.0
import PackageDescription

// LighthouseKit draws farero's character, the lighthouse (등대 v2, 기능 명세서 F-12),
// with SwiftUI shapes.
let package = Package(
    name: "LighthouseKit",
    platforms: [.macOS(.v15)],
    products: [
        .library(name: "LighthouseKit", targets: ["LighthouseKit"]),
    ],
    targets: [
        .target(name: "LighthouseKit"),
        // Renders every state to PNG contact sheets for design review:
        // swift run lighthouse-gallery /tmp/lighthouse-gallery
        .executableTarget(name: "lighthouse-gallery", dependencies: ["LighthouseKit"]),
        // Renders the app icon (iconset PNGs) from the character; see scripts/make-icon.sh:
        // swift run lighthouse-icon /tmp/AppIcon.iconset
        .executableTarget(name: "lighthouse-icon", dependencies: ["LighthouseKit"]),
        .testTarget(name: "LighthouseKitTests", dependencies: ["LighthouseKit"]),
    ]
)
