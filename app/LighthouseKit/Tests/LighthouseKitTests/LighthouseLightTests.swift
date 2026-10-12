import SwiftUI
import XCTest

@testable import LighthouseKit

final class LighthouseLightTests: XCTestCase {
    /// Start times to sample from: zero, a typical `timeIntervalSinceReferenceDate`,
    /// and a negative time (phases must still work).
    private let epochs: [TimeInterval] = [0, 812_345_678.937, -1234.5]
    private let step: TimeInterval = 0.001
    private let duration: TimeInterval = 30

    private let darkStates: Set<LighthouseState> = [.sleeping, .denied, .disconnected]

    // MARK: - Flash rate (F-12, WCAG 2.3.1)

    /// A "flash" is the lamp going from dark to lit, or a lit lamp changing color
    /// (Al). No state may flash more than once per second.
    func testNoStateFlashesMoreThanOncePerSecond() {
        for state in LighthouseState.allCases {
            for epoch in epochs {
                var flashes: [TimeInterval] = []
                var previous = LighthouseLight.output(for: state, at: epoch, reduceMotion: false)
                var t = epoch
                let samples = Int(duration / step)
                for i in 1...samples {
                    t = epoch + Double(i) * step
                    let current = LighthouseLight.output(for: state, at: t, reduceMotion: false)
                    let turnedOn = !previous.isOn && current.isOn
                    let changedColor = previous.isOn && current.isOn && previous.color != current.color
                    if turnedOn || changedColor { flashes.append(t) }
                    previous = current
                }
                // Count flashes in every whole-second window.
                var perSecond: [Int: Int] = [:]
                for f in flashes { perSecond[Int(((f - epoch) / 1.0).rounded(.down)), default: 0] += 1 }
                XCTAssertLessThanOrEqual(perSecond.values.max() ?? 0, 1, "\(state) at epoch \(epoch)")
                // And consecutive flashes are at least ~1 s apart.
                for (a, b) in zip(flashes, flashes.dropFirst()) {
                    XCTAssertGreaterThanOrEqual(b - a, 1.0 - 2 * step, "\(state) flashes \(a) → \(b)")
                }
            }
        }
    }

    /// The beam counts as a flash for a viewer each time it sweeps past, so one
    /// sweep must take more than a second (3.2 s, or 1.6 s with many sessions).
    func testBeamSweepsSlowerThanOncePerSecond() throws {
        for state in [LighthouseState.working, .workingMany] {
            let period = try XCTUnwrap(state.beamPeriod)
            XCTAssertGreaterThanOrEqual(period, 1, "\(state)")
            let epoch = 812_345_678.0
            var reaches: [Double] = []
            for i in 0..<Int(period / 0.01) {
                let out = LighthouseLight.output(for: state, at: epoch + Double(i) * 0.01, reduceMotion: false)
                XCTAssertTrue(out.isOn)
                reaches.append(try XCTUnwrap(out.beamReach))
            }
            XCTAssertTrue(reaches.allSatisfy { $0 >= -1 && $0 <= 1 }, "\(state)")
            // One period reaches full length on both sides and crosses the
            // middle exactly twice.
            XCTAssertGreaterThan(reaches.max()!, 0.99, "\(state)")
            XCTAssertLessThan(reaches.min()!, -0.99, "\(state)")
            let crossings = zip(reaches, reaches.dropFirst()).filter { ($0 > 0) != ($1 > 0) }.count
            XCTAssertLessThanOrEqual(crossings, 2, "\(state)")
        }
        XCTAssertNil(LighthouseState.waitingInput.beamPeriod)
    }

    // MARK: - Canvas timings and motions (등대 v2 (확정))

    func testLightTimingsMatchTheCanvas() {
        // Q: lit 45 % of 1 s. Fl: 25 % of 2 s. Oc: 80 % of 3 s. Iso: 50 % of 2 s.
        XCTAssertEqual(LighthouseLight.Timing.quickOn / LighthouseLight.Timing.quickPeriod, 0.45, accuracy: 1e-9)
        XCTAssertEqual(LighthouseLight.Timing.flashOn / LighthouseLight.Timing.flashPeriod, 0.25, accuracy: 1e-9)
        XCTAssertEqual(
            1 - LighthouseLight.Timing.occultDark / LighthouseLight.Timing.occultPeriod, 0.8,
            accuracy: 1e-9)
        XCTAssertEqual(LighthouseLight.Timing.isoPeriod, 2)
        XCTAssertEqual(LighthouseState.working.beamPeriod, 3.2)
    }

    func testBeamFlipsLikeTheCanvas() {
        // scaleX 1 → -1 → 1 over 3.2 s, easing each half.
        func reach(_ t: TimeInterval) -> Double {
            LighthouseLight.output(for: .working, at: t, reduceMotion: false).beamReach!
        }
        XCTAssertEqual(reach(0), 1, accuracy: 1e-6)
        XCTAssertEqual(reach(0.8), 0, accuracy: 1e-6)
        XCTAssertEqual(reach(1.6), -1, accuracy: 1e-6)
        XCTAssertEqual(reach(2.4), 0, accuracy: 1e-6)
        // Ease-in-out: slow near the ends.
        XCTAssertGreaterThan(reach(0.1), 0.95)
    }

    func testBodyMotionsMatchTheCanvas() {
        func pose(_ state: LighthouseState, _ t: TimeInterval) -> LighthousePose {
            LighthouseLight.pose(for: state, at: t, reduceMotion: false)
        }
        // Hop: up 1.4 by 40 %, held to 60 %, down by 100 %.
        XCTAssertEqual(pose(.greeting, 0).offsetY, 0, accuracy: 1e-9)
        XCTAssertEqual(pose(.greeting, 0.4).offsetY, -1.4, accuracy: 1e-6)
        XCTAssertEqual(pose(.allowed, 0.5).offsetY, -1.4, accuracy: 1e-6)
        XCTAssertEqual(pose(.greeting, 0.999).offsetY, 0, accuracy: 1e-3)
        // Shake: ±0.4 at 25 % and 75 % of 0.25 s.
        XCTAssertEqual(pose(.error, 0.0625).offsetX, -0.4, accuracy: 1e-9)
        XCTAssertEqual(pose(.error, 0.1875).offsetX, 0.4, accuracy: 1e-9)
        // Breath: 98.5 % at the middle of 3.6 s.
        XCTAssertEqual(pose(.waitingInput, 1.8).breath, 0.985, accuracy: 1e-9)
        XCTAssertEqual(pose(.waitingInput, 0).breath, 1, accuracy: 1e-9)
        // Still states.
        for state in [LighthouseState.needsApproval, .denied, .disconnected] {
            for t in stride(from: 0.0, to: 4, by: 0.05) { XCTAssertEqual(pose(state, t), .still, "\(state)") }
        }
        // Sleeping "z": fully drawn at 40 % of 2.4 s, gone at the start.
        XCTAssertEqual(LighthouseLight.sleepZ(at: 0.96, reduceMotion: false).opacity, 1, accuracy: 1e-6)
        XCTAssertEqual(LighthouseLight.sleepZ(at: 0, reduceMotion: false).opacity, 0, accuracy: 1e-9)
        XCTAssertEqual(LighthouseLight.sleepZ(at: 0, reduceMotion: false).offsetY, 0.4, accuracy: 1e-9)
    }

    func testEaseInOutIsCSSEaseInOut() {
        // cubic-bezier(0.42, 0, 0.58, 1): symmetric, fixed ends, known midpoint values.
        XCTAssertEqual(LighthouseLight.easeInOut(0), 0, accuracy: 1e-9)
        XCTAssertEqual(LighthouseLight.easeInOut(1), 1, accuracy: 1e-9)
        XCTAssertEqual(LighthouseLight.easeInOut(0.5), 0.5, accuracy: 1e-6)
        for x in stride(from: 0.0, through: 1, by: 0.05) {
            XCTAssertEqual(LighthouseLight.easeInOut(x) + LighthouseLight.easeInOut(1 - x), 1, accuracy: 1e-6)
        }
        XCTAssertEqual(LighthouseLight.easeInOut(0.25), 0.1291, accuracy: 0.001)
    }

    // MARK: - Reduce Motion

    func testReduceMotionIsSteady() {
        for state in LighthouseState.allCases {
            let reference = LighthouseLight.output(for: state, at: 0, reduceMotion: true)
            XCTAssertEqual(reference.isOn, !darkStates.contains(state), "\(state)")
            // Like the canvas with animations off: an alternating light shows
            // its second color's lamp over its first color's glass.
            XCTAssertEqual(reference.color, state.alternateLampColor ?? state.lampColor, "\(state)")
            XCTAssertEqual(reference.glassColor, state.lampColor, "\(state)")
            for epoch in epochs {
                for i in 0..<Int(10 / 0.01) {
                    let t = epoch + Double(i) * 0.01
                    XCTAssertEqual(
                        LighthouseLight.output(for: state, at: t, reduceMotion: true), reference, "\(state) t=\(t)")
                    XCTAssertEqual(LighthouseLight.pose(for: state, at: t, reduceMotion: true), .still)
                    XCTAssertEqual(LighthouseLight.sleepZ(at: t, reduceMotion: true), .rest)
                }
            }
            XCTAssertFalse(LighthouseLight.isAnimated(state, reduceMotion: true))
            XCTAssertNil(LighthouseLight.frameInterval(for: state, reduceMotion: true))
            XCTAssertNil(LighthouseLight.nextChange(for: state, after: 0, reduceMotion: true))
        }
    }

    func testReduceMotionKeepsAStillBeamForWorkingStates() {
        for state in [LighthouseState.working, .workingMany] {
            let out = LighthouseLight.output(for: state, at: 123, reduceMotion: true)
            XCTAssertEqual(out.beamReach, LighthouseLight.Timing.staticBeamReach)
        }
    }

    // MARK: - Characteristics

    func testDarkStatesAreAlwaysDark() {
        for state in darkStates {
            XCTAssertEqual(state.lightCharacteristic, .off)
            XCTAssertNil(state.lampColor)
            for reduceMotion in [false, true] {
                for i in 0..<20_000 {
                    let out = LighthouseLight.output(for: state, at: Double(i) * 0.001, reduceMotion: reduceMotion)
                    XCTAssertEqual(out, .dark, "\(state)")
                }
            }
        }
    }

    func testCharacteristicTable() {
        let expected: [LighthouseState: LightCharacteristic] = [
            .sleeping: .off, .greeting: .alternating, .thinking: .isophase, .working: .rotatingBeam,
            .workingMany: .rotatingBeam, .needsApproval: .quickFlashing, .allowed: .fixed, .denied: .off,
            .caution: .occulting, .waitingInput: .fixed, .error: .flashing, .disconnected: .off,
        ]
        for state in LighthouseState.allCases {
            XCTAssertEqual(state.lightCharacteristic, expected[state], "\(state)")
        }
    }

    /// The fraction of time the lamp is lit over one minute.
    private func litFraction(_ state: LighthouseState) -> Double {
        var lit = 0
        let n = 60_000
        for i in 0..<n where LighthouseLight.output(for: state, at: Double(i) * 0.001, reduceMotion: false).isOn {
            lit += 1
        }
        return Double(lit) / Double(n)
    }

    func testDutyCyclesMatchTheCharacteristic() {
        XCTAssertEqual(litFraction(.waitingInput), 1)
        XCTAssertEqual(litFraction(.allowed), 1)
        XCTAssertEqual(litFraction(.greeting), 1, "Al is always lit")
        XCTAssertEqual(litFraction(.working), 1)
        XCTAssertEqual(litFraction(.thinking), 0.5, accuracy: 0.01, "Iso: equal light and dark")
        XCTAssertGreaterThan(litFraction(.caution), 0.75, "Oc: light longer than dark")
        XCTAssertLessThan(litFraction(.caution), 1)
        XCTAssertLessThan(litFraction(.needsApproval), 0.5, "Q: flash shorter than eclipse")
        XCTAssertLessThan(litFraction(.error), 0.5, "Fl: flash shorter than eclipse")
        XCTAssertLessThan(litFraction(.error), litFraction(.needsApproval), "Fl flashes less often than Q")
    }

    func testAlternatingUsesTwoColorsOverOneGlass() {
        var colors = Set<LampColor>()
        for i in 0..<4000 {
            let out = LighthouseLight.output(for: .greeting, at: Double(i) * 0.001, reduceMotion: false)
            if let c = out.color { colors.insert(c) }
            XCTAssertEqual(out.glassColor, .yellow)
        }
        XCTAssertEqual(colors, [LampColor.yellow, .teal])
    }

    // MARK: - Schedule

    /// Replays the view's timeline schedule (an entry 1 ms after each change)
    /// and checks that the lamp on screen always matches the pure function,
    /// apart from that 1 ms, and that moving states get a frame at least
    /// every frame interval.
    func testScheduleCatchesEveryStep() {
        for state in LighthouseState.allCases {
            let epoch = 812_345_678.937
            guard LighthouseLight.isAnimated(state, reduceMotion: false) else {
                XCTAssertNil(LighthouseLight.nextChange(for: state, after: epoch, reduceMotion: false), "\(state)")
                continue
            }
            let interval = LighthouseLight.frameInterval(for: state, reduceMotion: false)
            var entries = [epoch]
            while let last = entries.last, last < epoch + duration {
                let next = try! XCTUnwrap(LighthouseLight.nextChange(for: state, after: last, reduceMotion: false))
                XCTAssertGreaterThan(next, last)
                if let interval { XCTAssertLessThanOrEqual(next - last, interval + 1e-6, "\(state)") }
                entries.append(next + 0.001)
            }
            func lamp(_ t: TimeInterval) -> (Bool, LampColor?, LampColor?) {
                let out = LighthouseLight.output(for: state, at: t, reduceMotion: false)
                return (out.isOn, out.color, out.glassColor)
            }
            var index = 0
            var mismatches = 0
            for i in 0..<Int(duration / step) {
                let t = epoch + Double(i) * step + step / 2
                while index + 1 < entries.count, entries[index + 1] <= t { index += 1 }
                let shown = lamp(entries[index])
                let truth = lamp(t)
                if shown != truth {
                    let upcoming = index + 1 < entries.count ? entries[index + 1] : .infinity
                    if upcoming - t > 0.0011 { mismatches += 1 }
                }
            }
            XCTAssertEqual(mismatches, 0, "\(state): schedule missed a step")
        }
    }

    func testOnlyMovingStatesGetSmoothFrames() {
        XCTAssertNil(LighthouseLight.frameInterval(for: .needsApproval, reduceMotion: false), "Q only steps")
        XCTAssertFalse(LighthouseLight.isAnimated(.denied, reduceMotion: false))
        XCTAssertFalse(LighthouseLight.isAnimated(.disconnected, reduceMotion: false))
        XCTAssertEqual(
            LighthouseLight.frameInterval(for: .waitingInput, reduceMotion: false),
            LighthouseLight.Timing.breathFrameInterval, "only breathing")
        for state in [LighthouseState.working, .workingMany, .greeting, .allowed, .error, .sleeping] {
            XCTAssertEqual(
                LighthouseLight.frameInterval(for: state, reduceMotion: false),
                LighthouseLight.Timing.smoothFrameInterval, "\(state)")
        }
    }

    /// Next to the notch (22 pt) the breath would move 0.3 pt: it is not
    /// drawn, so an idle session's character stands still.
    func testCompactSizeSkipsTheBreath() {
        XCTAssertFalse(LighthouseLight.breathes(at: 22))
        XCTAssertTrue(LighthouseLight.breathes(at: 48))
        XCTAssertFalse(LighthouseLight.isAnimated(.waitingInput, reduceMotion: false, size: 22))
        XCTAssertNil(LighthouseLight.nextChange(for: .waitingInput, after: 0, reduceMotion: false, size: 22))
        XCTAssertEqual(LighthouseLight.pose(for: .waitingInput, at: 0.9, reduceMotion: false, size: 22), .still)
        XCTAssertNotEqual(LighthouseLight.pose(for: .waitingInput, at: 0.9, reduceMotion: false, size: 48), .still)
        // Thinking and caution still step their light, no more than once a second.
        for state in [LighthouseState.thinking, .caution] {
            XCTAssertNil(LighthouseLight.frameInterval(for: state, reduceMotion: false, size: 22), "\(state)")
            let next = try! XCTUnwrap(LighthouseLight.nextChange(for: state, after: 100, reduceMotion: false, size: 22))
            XCTAssertGreaterThan(next - 100, 0.1, "\(state)")
        }
    }

    // MARK: - Not relying on color (WCAG 1.4.1)

    /// At 22 pt, where the breath is not drawn, states still differ in two
    /// cues other than color.
    func testStatesDifferBeyondColorWithoutTheBreath() {
        func motion(_ s: LighthouseState) -> LighthouseMotion {
            s.gesture.motion == .breathe ? .none : s.gesture.motion
        }
        let states = LighthouseState.allCases
        for (i, a) in states.enumerated() {
            for b in states[(i + 1)...] {
                var cues = 0
                if a.lightCharacteristic != b.lightCharacteristic || a.beamPeriod != b.beamPeriod { cues += 1 }
                if a.eyes != b.eyes { cues += 1 }
                if a.badge != b.badge { cues += 1 }
                let ga = a.gesture, gb = b.gesture
                if motion(a) != motion(b) || ga.leanDegrees != gb.leanDegrees || ga.widthScale != gb.widthScale
                    || ga.heightScale != gb.heightScale || a.isDimmed != b.isDimmed || a.isCracked != b.isCracked
                {
                    cues += 1
                }
                XCTAssertGreaterThanOrEqual(cues, 2, "\(a) vs \(b)")
            }
        }
    }

    /// Every pair of states differs in at least two cues other than color:
    /// light rhythm (characteristic and beam speed), eyes, badge, gesture.
    func testStatesDifferBeyondColor() {
        let states = LighthouseState.allCases
        for (i, a) in states.enumerated() {
            for b in states[(i + 1)...] {
                var cues = 0
                if a.lightCharacteristic != b.lightCharacteristic || a.beamPeriod != b.beamPeriod { cues += 1 }
                if a.eyes != b.eyes { cues += 1 }
                if a.badge != b.badge { cues += 1 }
                if a.gesture != b.gesture || a.isDimmed != b.isDimmed || a.isCracked != b.isCracked { cues += 1 }
                XCTAssertGreaterThanOrEqual(cues, 2, "\(a) vs \(b)")
            }
        }
    }

    func testEyesAndBadgesAreMostlyUnique() {
        // Only working/workingMany share eyes (focused) and greeting/allowed (happy).
        let eyeGroups = Dictionary(grouping: LighthouseState.allCases, by: \.eyes).values.filter { $0.count > 1 }
        XCTAssertEqual(Set(eyeGroups.map { Set($0) }), [[.working, .workingMany], [.greeting, .allowed]])
        // Only working and waitingInput go without a badge.
        XCTAssertEqual(Set(LighthouseState.allCases.filter { $0.badge == .none }), [.working, .waitingInput])
    }

    // MARK: - Palette

    /// The lit glass is the lamp color mixed with ink, and the beam the lamp
    /// color mixed with black: single opaque colors (F-12), the same as the
    /// canvas hex values.
    func testGlassAndBeamArePremixedLikeTheCanvas() {
        let canvasGlass: [LampColor: UInt32] = [
            .warmWhite: 0x767368, .yellow: 0x76683E, .teal: 0x276764, .orange: 0x76532C,
            .green: 0x2F684C, .amber: 0x766331, .red: 0x76343C,
        ]
        func assertClose(_ a: LighthouseRGB, _ hex: UInt32, _ message: String) {
            let b = LighthouseRGB(hex: hex)
            XCTAssertEqual(a.red, b.red, accuracy: 1 / 255, message)
            XCTAssertEqual(a.green, b.green, accuracy: 1 / 255, message)
            XCTAssertEqual(a.blue, b.blue, accuracy: 1 / 255, message)
        }
        for color in LampColor.allCases {
            assertClose(LighthousePalette.glass(color), canvasGlass[color]!, "glass \(color)")
            let mixed = LighthousePalette.ink.mixed(
                with: LighthousePalette.lamp(color), amount: LighthousePalette.glassMix)
            XCTAssertEqual(LighthousePalette.glass(color), mixed)
        }
        assertClose(LighthousePalette.beam(.teal), 0x1A746A, "beam teal")
    }

    // MARK: - Rendering

    /// The sRGB pixel at (x, y) points of `state` drawn at 120 pt, 1x.
    @MainActor
    private func pixel(
        _ state: LighthouseState, at point: CGPoint, time: TimeInterval = 0,
        reduceMotion: Bool = true
    ) throws -> LighthouseRGB {
        let renderer = ImageRenderer(
            content: LighthouseFrame(
                state: state, size: 120, time: time,
                reduceMotion: reduceMotion))
        renderer.scale = 1
        let image = try XCTUnwrap(renderer.cgImage)
        var bytes = [UInt8](repeating: 0, count: 4)
        let context = try XCTUnwrap(
            CGContext(
                data: &bytes, width: 1, height: 1, bitsPerComponent: 8, bytesPerRow: 4,
                space: CGColorSpace(name: CGColorSpace.sRGB)!,
                bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        context.draw(
            image,
            in: CGRect(
                x: -point.x, y: point.y - CGFloat(image.height) + 1,
                width: CGFloat(image.width), height: CGFloat(image.height)))
        return LighthouseRGB(Double(bytes[0]) / 255, Double(bytes[1]) / 255, Double(bytes[2]) / 255)
    }

    private func assertColor(
        _ a: LighthouseRGB, _ b: LighthouseRGB, _ message: String,
        file: StaticString = #filePath, line: UInt = #line
    ) {
        XCTAssertEqual(a.red, b.red, accuracy: 3 / 255, message, file: file, line: line)
        XCTAssertEqual(a.green, b.green, accuracy: 3 / 255, message, file: file, line: line)
        XCTAssertEqual(a.blue, b.blue, accuracy: 3 / 255, message, file: file, line: line)
    }

    /// Where the drawing-unit point `p` of the body lands at 120 pt (5 pt per
    /// unit) after the state's lean and stretch around the foot (12, 23.2).
    private func bodyPixel(_ state: LighthouseState, _ p: CGPoint) -> CGPoint {
        let g = state.gesture
        let dx = (p.x - 12) * g.widthScale
        let dy = (p.y - 23.2) * g.heightScale
        let a = g.leanDegrees * .pi / 180
        let x = 12 + dx * cos(a) - dy * sin(a)
        let y = 23.2 + dx * sin(a) + dy * cos(a)
        return CGPoint(x: (x * 5).rounded(.down), y: (y * 5).rounded(.down))
    }

    /// The lamp at (12, 8.3) and the glass beside it show the lamp output.
    @MainActor
    func testLanternIsLitOrDarkAsTheLampSays() throws {
        for state in LighthouseState.allCases {
            let lamp = LighthouseLight.output(for: state, at: 0, reduceMotion: true)
            let expectedLamp = lamp.color.map(LighthousePalette.lamp) ?? LighthousePalette.ink
            let expectedGlass = lamp.glassColor.map(LighthousePalette.glass) ?? LighthousePalette.ink
            try assertColor(pixel(state, at: bodyPixel(state, CGPoint(x: 12, y: 8.3))), expectedLamp, "\(state) lamp")
            try assertColor(
                pixel(state, at: bodyPixel(state, CGPoint(x: 9.2, y: 8.0))), expectedGlass, "\(state) glass")
        }
    }

    /// The badge disc around (3.9, 4.3) blinks with the Q light.
    @MainActor
    func testApprovalBadgeBlinksWithTheLamp() throws {
        let discPoint = CGPoint(x: 10, y: 21)
        let orange = LighthousePalette.lamp(.orange)
        try assertColor(pixel(.needsApproval, at: discPoint, time: 0.1, reduceMotion: false), orange, "lit")
        let dark = try pixel(.needsApproval, at: discPoint, time: 0.7, reduceMotion: false)
        XCTAssertLessThan(dark.red + dark.green + dark.blue, 0.05, "the badge is gone while the lamp is dark")
        try assertColor(pixel(.needsApproval, at: discPoint), orange, "Reduce Motion keeps it")
    }

    // MARK: - Public API

    @MainActor
    func testPublicAPIStaysSourceCompatible() {
        XCTAssertEqual(LighthouseState.allCases.count, 12)
        XCTAssertTrue(LighthouseState.allCases.allSatisfy { !$0.label.isEmpty })
        _ = LighthouseView(state: .sleeping)
        _ = LighthouseView(state: .working, size: 120)
        _ = LighthouseView(state: .error, size: 22, animated: false)
        _ = LighthouseView(state: .workingMany, size: 22, sessionCount: 3)
        _ = LighthouseFrame(state: .greeting, size: 22, time: 0)
        XCTAssertEqual(
            LighthouseView.lampOutput(for: .waitingInput, at: 0, reduceMotion: false),
            LampOutput(isOn: true, color: .warmWhite))
    }

    /// The canvas draws the character on a 24 × 24 grid in a square: at 22 pt
    /// one unit is 22/24 pt, so the 2.4-unit eyes are 2.2 pt wide (F-12).
    @MainActor
    func testSizeMatchesTheCanvas() {
        XCTAssertEqual(LighthouseView.aspectRatio, 1)
        XCTAssertEqual(2.4 * 22 / LighthouseRenderer.unitHeight, 2.2, accuracy: 1e-9)
    }

    @MainActor
    func testEveryStateRendersAtBothSizes() throws {
        for state in LighthouseState.allCases {
            for size in [22.0, 120.0] {
                let renderer = ImageRenderer(content: LighthouseFrame(state: state, size: size, time: 0.1))
                renderer.scale = 2
                let image = try XCTUnwrap(renderer.cgImage, "\(state) @ \(size)")
                XCTAssertEqual(image.height, Int(size * 2))
                XCTAssertEqual(image.width, Int((size * LighthouseView.aspectRatio * 2).rounded()))
            }
        }
    }
}
