// lighthouse-icon renders the app icon: the lighthouse (waitingInput, lamp lit,
// still frame) on a night sky, on the macOS icon grid (1024 canvas, 824 body,
// ~100 px margin, continuous corners of radius ~185).
// Usage: swift run lighthouse-icon <output.iconset>
// It writes the ten standard iconset PNGs; with a second argument it also
// writes icon-1024.png and a few preview sizes into that directory.

import AppKit
import ImageIO
import LighthouseKit
import SwiftUI
import UniformTypeIdentifiers

struct IconView: View {
    static let canvas: CGFloat = 1024
    static let body: CGFloat = 824
    static let radius: CGFloat = 185

    var body: some View {
        let shape = RoundedRectangle(cornerRadius: Self.radius, style: .continuous)
        let lighthouseHeight = Self.body * 0.80
        ZStack {
            Color.clear
            ZStack {
                LinearGradient(
                    stops: [
                        .init(color: Color(red: 0.05, green: 0.08, blue: 0.20), location: 0),
                        .init(color: Color(red: 0.12, green: 0.22, blue: 0.42), location: 0.62),
                        .init(color: Color(red: 0.17, green: 0.45, blue: 0.55), location: 1),
                    ],
                    startPoint: .top, endPoint: .bottom)
                // Soft glow around the lamp.
                RadialGradient(
                    colors: [LighthousePalette.lamp(.warmWhite).color.opacity(0.30), .clear],
                    center: UnitPoint(x: 0.5, y: 0.30), startRadius: 0, endRadius: Self.body * 0.42)
                LighthouseFrame(state: .waitingInput, size: lighthouseHeight, time: 0.1, reduceMotion: true)
                    .offset(y: Self.body * 0.015)
            }
            .frame(width: Self.body, height: Self.body)
            .clipShape(shape)
            .overlay(shape.strokeBorder(Color.white.opacity(0.10), lineWidth: 2))
            .shadow(color: .black.opacity(0.35), radius: 14, x: 0, y: 8)
        }
        .frame(width: Self.canvas, height: Self.canvas)
    }
}

@MainActor
func renderIcon() -> CGImage {
    let renderer = ImageRenderer(content: IconView())
    renderer.scale = 1
    renderer.isOpaque = false
    guard let image = renderer.cgImage, image.width == 1024, image.height == 1024 else {
        fatalError("render failed or wrong size")
    }
    return image
}

func resized(_ image: CGImage, to side: Int) -> CGImage {
    if image.width == side { return image }
    let context = CGContext(
        data: nil, width: side, height: side, bitsPerComponent: 8, bytesPerRow: 0,
        space: CGColorSpace(name: CGColorSpace.sRGB)!,
        bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    context.interpolationQuality = .high
    context.draw(image, in: CGRect(x: 0, y: 0, width: side, height: side))
    return context.makeImage()!
}

func write(_ image: CGImage, to url: URL) {
    let destination = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(destination, image, nil)
    guard CGImageDestinationFinalize(destination) else { fatalError("write failed: \(url.path)") }
}

let arguments = Array(CommandLine.arguments.dropFirst())
guard let iconsetPath = arguments.first else {
    FileHandle.standardError.write(Data("usage: lighthouse-icon <output.iconset> [preview-dir]\n".utf8))
    exit(2)
}
let iconset = URL(fileURLWithPath: iconsetPath, isDirectory: true)
try FileManager.default.createDirectory(at: iconset, withIntermediateDirectories: true)

MainActor.assumeIsolated {
    let master = renderIcon()
    for base in [16, 32, 128, 256, 512] {
        write(resized(master, to: base), to: iconset.appendingPathComponent("icon_\(base)x\(base).png"))
        write(resized(master, to: base * 2), to: iconset.appendingPathComponent("icon_\(base)x\(base)@2x.png"))
    }
    if arguments.count > 1 {
        let preview = URL(fileURLWithPath: arguments[1], isDirectory: true)
        try? FileManager.default.createDirectory(at: preview, withIntermediateDirectories: true)
        for side in [1024, 128, 32, 16] {
            write(resized(master, to: side), to: preview.appendingPathComponent("icon-\(side).png"))
        }
    }
}
