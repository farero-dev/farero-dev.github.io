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
    /// revolution must take more than a second (it takes 4).
    func testBeamRevolvesSlowerThanOncePerSecond() {
        XCTAssertGreaterThanOrEqual(LighthouseLight.Timing.beamRevolution, 1)
        for state in [LighthouseState.working, .workingMany] {
            let epoch = 812_345_678.0
            var angles: [Double] = []
            for i in 0..<400 {
                let t = epoch + Double(i) * 0.01
                let out = LighthouseLight.output(for: state, at: t, reduceMotion: false)
                XCTAssertTrue(out.isOn)
                angles.append(try! XCTUnwrap(out.beamAngle))
            }
            XCTAssertGreaterThan(Set(angles.map { ($0 * 100).rounded() }).count, 100, "\(state) beam moves")
            XCTAssertTrue(angles.allSatisfy { $0 >= 0 && $0 < 2 * .pi })
        }
    }

    func testEyeBlinkAtMostOncePerSecond() {
        for epoch in epochs {
            var onsets: [TimeInterval] = []
            var wasClosed = LighthouseLight.isBlinking(.waitingInput, at: epoch, reduceMotion: false)
            for i in 1...Int(duration / step) {
                let t = epoch + Double(i) * step
                let closed = LighthouseLight.isBlinking(.waitingInput, at: t, reduceMotion: false)
                if closed, !wasClosed { onsets.append(t) }
                wasClosed = closed
            }
            XCTAssertFalse(onsets.isEmpty, "waitingInput blinks now and then")
            for (a, b) in zip(onsets, onsets.dropFirst()) {
                XCTAssertGreaterThanOrEqual(b - a, 1.0)
            }
        }
        for state in LighthouseState.allCases where state != .waitingInput {
            for i in 0..<10_000 {
                XCTAssertFalse(LighthouseLight.isBlinking(state, at: Double(i) * 0.001, reduceMotion: false))
            }
        }
    }

    // MARK: - Reduce Motion

    func testReduceMotionIsSteady() {
        for state in LighthouseState.allCases {
            let reference = LighthouseLight.output(for: state, at: 0, reduceMotion: true)
            XCTAssertEqual(reference.isOn, !darkStates.contains(state), "\(state)")
            XCTAssertEqual(reference.color, state.lampColor, "\(state)")
            for epoch in epochs {
                for i in 0..<Int(10 / 0.01) {
                    let t = epoch + Double(i) * 0.01
                    XCTAssertEqual(LighthouseLight.output(for: state, at: t, reduceMotion: true), reference, "\(state) t=\(t)")
                    XCTAssertFalse(LighthouseLight.isBlinking(state, at: t, reduceMotion: true))
                    XCTAssertEqual(LighthouseLight.hopHeight(state, at: t, reduceMotion: true), 0)
                }
            }
            XCTAssertFalse(LighthouseLight.isAnimated(state, reduceMotion: true))
            XCTAssertNil(LighthouseLight.nextChange(for: state, after: 0, reduceMotion: true))
        }
    }

    func testReduceMotionKeepsAStillBeamForWorkingStates() {
        for state in [LighthouseState.working, .workingMany] {
            let out = LighthouseLight.output(for: state, at: 123, reduceMotion: true)
            XCTAssertEqual(out.beamAngle, LighthouseLight.Timing.staticBeamAngle)
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

    func testAlternatingUsesTwoColors() {
        var colors = Set<LampColor>()
        for i in 0..<4000 {
            if let c = LighthouseLight.output(for: .greeting, at: Double(i) * 0.001, reduceMotion: false).color {
                colors.insert(c)
            }
        }
        XCTAssertEqual(colors, [LampColor.warmWhite, .softBlue])
    }

    // MARK: - Schedule

    /// Replays the view's timeline schedule (an entry 1 ms after each change)
    /// and checks that the frame on screen always matches the pure function,
    /// apart from that 1 ms.
    func testScheduleCatchesEveryStep() {
        for state in LighthouseState.allCases {
            let epoch = 812_345_678.937
            guard LighthouseLight.isAnimated(state, reduceMotion: false) else {
                XCTAssertNil(LighthouseLight.nextChange(for: state, after: epoch, reduceMotion: false), "\(state)")
                continue
            }
            if LighthouseLight.needsSmoothFrames(state, reduceMotion: false) {
                let next = try! XCTUnwrap(LighthouseLight.nextChange(for: state, after: epoch, reduceMotion: false))
                XCTAssertLessThanOrEqual(next - epoch, LighthouseLight.Timing.smoothFrameInterval + 1e-9)
                XCTAssertGreaterThan(next, epoch)
                continue
            }
            var entries = [epoch]
            while let last = entries.last, last < epoch + duration {
                let next = try! XCTUnwrap(LighthouseLight.nextChange(for: state, after: last, reduceMotion: false))
                XCTAssertGreaterThan(next, last)
                entries.append(next + 0.001)
            }
            func frame(_ t: TimeInterval) -> (LampOutput, Bool) {
                (LighthouseLight.output(for: state, at: t, reduceMotion: false),
                 LighthouseLight.isBlinking(state, at: t, reduceMotion: false))
            }
            var index = 0
            var mismatches = 0
            for i in 0..<Int(duration / step) {
                let t = epoch + Double(i) * step + step / 2
                while index + 1 < entries.count, entries[index + 1] <= t { index += 1 }
                let shown = frame(entries[index])
                let truth = frame(t)
                if shown.0 != truth.0 || shown.1 != truth.1 {
                    let upcoming = index + 1 < entries.count ? entries[index + 1] : .infinity
                    if upcoming - t > 0.0011 { mismatches += 1 }
                }
            }
            XCTAssertEqual(mismatches, 0, "\(state): schedule missed a step")
        }
    }

    // MARK: - Not relying on color (WCAG 1.4.1)

    /// Every pair of states differs in at least two cues other than color:
    /// light rhythm (characteristic and beam count), eyes, badge, gesture.
    func testStatesDifferBeyondColor() {
        let states = LighthouseState.allCases
        for (i, a) in states.enumerated() {
            for b in states[(i + 1)...] {
                var cues = 0
                if a.lightCharacteristic != b.lightCharacteristic || a.beamCount != b.beamCount { cues += 1 }
                if a.eyes != b.eyes { cues += 1 }
                if a.badge != b.badge { cues += 1 }
                if a.gesture != b.gesture || a.isDimmed != b.isDimmed { cues += 1 }
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

    func testLitGlassIsOpaqueBlend() {
        for color in LampColor.allCases {
            let raw = LighthousePalette.rawLamp(color)
            let lit = LighthousePalette.lamp(color)
            let glass = LighthousePalette.glass
            let t = LighthousePalette.glassMix
            XCTAssertEqual(lit.red, raw.red + (glass.red - raw.red) * t, accuracy: 1e-9)
            XCTAssertEqual(lit.green, raw.green + (glass.green - raw.green) * t, accuracy: 1e-9)
            XCTAssertEqual(lit.blue, raw.blue + (glass.blue - raw.blue) * t, accuracy: 1e-9)
        }
    }

    // MARK: - Public API

    @MainActor
    func testPublicAPIStaysSourceCompatible() {
        XCTAssertEqual(LighthouseState.allCases.count, 12)
        XCTAssertTrue(LighthouseState.allCases.allSatisfy { !$0.label.isEmpty })
        _ = LighthouseView(state: .sleeping)
        _ = LighthouseView(state: .working, size: 120)
        _ = LighthouseView(state: .error, size: 22, animated: false)
        _ = LighthouseFrame(state: .greeting, size: 22, time: 0)
        XCTAssertEqual(LighthouseView.lampOutput(for: .waitingInput, at: 0, reduceMotion: false),
                       LampOutput(isOn: true, color: .warmWhite))
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
