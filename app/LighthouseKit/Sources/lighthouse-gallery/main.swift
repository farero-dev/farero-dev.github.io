// lighthouse-gallery renders every lighthouse state to PNG files for design
// review. Usage: swift run lighthouse-gallery [output-dir]
// (default /tmp/lighthouse-gallery).

import AppKit
import ImageIO
import LighthouseKit
import SwiftUI
import UniformTypeIdentifiers

let outputDirectory = URL(fileURLWithPath: CommandLine.arguments.dropFirst().first ?? "/tmp/lighthouse-gallery",
                          isDirectory: true)
try FileManager.default.createDirectory(at: outputDirectory, withIntermediateDirectories: true)

let states = LighthouseState.allCases
let black = Color.black
let white = Color.white

// MARK: - Rendering helpers

@MainActor
func render<V: View>(_ view: V, scale: CGFloat) -> CGImage {
    let renderer = ImageRenderer(content: view)
    renderer.scale = scale
    renderer.isOpaque = true
    guard let image = renderer.cgImage else { fatalError("render failed") }
    return image
}

/// Enlarges with nearest-neighbor sampling so single pixels stay visible.
func enlarge(_ image: CGImage, by factor: Int) -> CGImage {
    let width = image.width * factor
    let height = image.height * factor
    let context = CGContext(data: nil, width: width, height: height, bitsPerComponent: 8, bytesPerRow: 0,
                            space: CGColorSpace(name: CGColorSpace.sRGB)!,
                            bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)!
    context.interpolationQuality = .none
    context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
    return context.makeImage()!
}

func write(_ image: CGImage, _ name: String) {
    let url = outputDirectory.appendingPathComponent(name)
    let destination = CGImageDestinationCreateWithURL(url as CFURL, UTType.png.identifier as CFString, 1, nil)!
    CGImageDestinationAddImage(destination, image, nil)
    CGImageDestinationFinalize(destination)
    print("wrote \(url.path) (\(image.width)×\(image.height))")
}

/// A time at which every blinking lamp is lit (phase 0.1 s into each period).
let litTime: TimeInterval = 1_000_000.1
/// A time at which Q, Fl and Iso lamps are dark and Oc is eclipsed.
let darkTime: TimeInterval = 1_000_000.0 + 2.7 // Q phase .7, Fl phase .7, Oc phase 2.7, Iso phase .7

// MARK: - Views

struct Cell: View {
    let state: LighthouseState
    let size: CGFloat
    let time: TimeInterval
    let reduceMotion: Bool
    let background: Color
    let showLabel: Bool

    var body: some View {
        VStack(spacing: max(2, size * 0.08)) {
            LighthouseFrame(state: state, size: size, time: time, reduceMotion: reduceMotion)
            if showLabel {
                Text(size < 40 ? state.label : "\(state.label) · \(state.rawValue)")
                    .font(.system(size: max(7, size * 0.1), weight: .medium))
                    .foregroundStyle(background == black ? Color(white: 0.75) : Color(white: 0.3))
                    .lineLimit(1)
                    .fixedSize()
            }
        }
        .frame(width: showLabel ? max(size * LighthouseView.aspectRatio + size * 0.5, 64)
                                : size * LighthouseView.aspectRatio + size * 0.5)
        .padding(.vertical, size * 0.25)
    }
}

/// One row per background/mode, one column per state.
struct ContactSheet: View {
    let size: CGFloat
    let labels: Bool

    var body: some View {
        VStack(spacing: 0) {
            row(black, time: litTime, reduceMotion: true)
            row(black, time: darkTime, reduceMotion: false)
            row(white, time: litTime, reduceMotion: true)
        }
    }

    func row(_ background: Color, time: TimeInterval, reduceMotion: Bool) -> some View {
        HStack(spacing: 0) {
            ForEach(states, id: \.self) { state in
                Cell(state: state, size: size, time: time, reduceMotion: reduceMotion,
                     background: background, showLabel: labels)
            }
        }
        .padding(.horizontal, size * 0.3)
        .background(background)
    }
}

/// Large sheet: two rows of six.
struct LargeSheet: View {
    let size: CGFloat
    let background: Color

    var body: some View {
        VStack(spacing: 0) {
            ForEach(0..<2) { row in
                HStack(spacing: 0) {
                    ForEach(states[(row * 6)..<(row * 6 + 6)], id: \.self) { state in
                        Cell(state: state, size: size, time: litTime, reduceMotion: true,
                             background: background, showLabel: true)
                    }
                }
            }
        }
        .padding(size * 0.15)
        .background(background)
    }
}

/// Frames across one period for an animated state.
struct FrameStrip: View {
    let state: LighthouseState
    let size: CGFloat
    let times: [TimeInterval]

    var body: some View {
        HStack(spacing: size * 0.2) {
            ForEach(Array(times.enumerated()), id: \.offset) { _, t in
                VStack(spacing: 2) {
                    LighthouseFrame(state: state, size: size, time: litTime - 0.1 + t)
                    Text(String(format: "%.2fs", t))
                        .font(.system(size: max(6, size * 0.1), design: .monospaced))
                        .foregroundStyle(Color(white: 0.7))
                }
            }
        }
        .padding(size * 0.2)
        .background(black)
    }
}

// MARK: - Output

MainActor.assumeIsolated {
    // 22 pt: true size at 1x and 2x, plus nearest-neighbor enlargements for inspection.
    let sheet22x1 = render(ContactSheet(size: 22, labels: false), scale: 1)
    write(sheet22x1, "sheet-22pt@1x.png")
    write(enlarge(sheet22x1, by: 6), "sheet-22pt@1x-zoom6.png")
    let sheet22x2 = render(ContactSheet(size: 22, labels: false), scale: 2)
    write(sheet22x2, "sheet-22pt@2x.png")
    write(enlarge(sheet22x2, by: 3), "sheet-22pt@2x-zoom3.png")
    write(render(ContactSheet(size: 22, labels: true), scale: 6), "sheet-22pt-labeled@6x.png")

    // 120 pt.
    write(render(LargeSheet(size: 120, background: black), scale: 1), "sheet-120pt-black.png")
    write(render(LargeSheet(size: 120, background: white), scale: 1), "sheet-120pt-white.png")
    for state in states {
        let view = HStack(spacing: 0) {
            Cell(state: state, size: 120, time: litTime, reduceMotion: true, background: black, showLabel: false)
                .background(black)
            Cell(state: state, size: 120, time: litTime, reduceMotion: true, background: white, showLabel: false)
                .background(white)
        }
        write(render(view, scale: 2), "state-120pt-\(state.rawValue).png")
    }

    // Animated states: frames across one period.
    let strips: [(LighthouseState, [TimeInterval])] = [
        (.greeting, [0, 0.1, 0.2, 0.3, 0.5, 1.0, 1.2, 1.5]),
        (.thinking, [0, 0.5, 1.0, 1.5]),
        (.working, [0, 0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 3.5]),
        (.workingMany, [0, 0.5, 1.0, 1.5, 2.0, 2.5, 3.0, 3.5]),
        (.needsApproval, [0, 0.2, 0.4, 0.7]),
        (.error, [0, 0.4, 0.5, 1.0, 1.5]),
        (.caution, [0, 1.0, 2.0, 2.5, 2.9]),
        (.waitingInput, [0, 4.8, 4.9]),
    ]
    for (state, times) in strips {
        write(render(FrameStrip(state: state, size: 120, times: times), scale: 1), "frames-120pt-\(state.rawValue).png")
        write(enlarge(render(FrameStrip(state: state, size: 22, times: times), scale: 2), by: 3),
              "frames-22pt@2x-zoom3-\(state.rawValue).png")
    }
}
