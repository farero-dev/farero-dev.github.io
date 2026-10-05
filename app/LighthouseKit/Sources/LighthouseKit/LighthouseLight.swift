import Foundation

// The lamp follows real lighthouse light characteristics (기능 명세서 F-12).
// Everything here is a pure function of (state, time) so the drawing code,
// the timeline schedule and the tests all agree on what the lamp does.
//
// Blinking is capped at 1 per second (F-12; WCAG 2.3.1 allows 3).

/// A lighthouse light characteristic, as printed on nautical charts.
public enum LightCharacteristic: String, CaseIterable, Sendable {
    /// Lamp off.
    case off
    /// F: fixed, steady light.
    case fixed
    /// Q: quick flashing. Here 1 flash per second, the fastest the spec allows.
    case quickFlashing
    /// Fl: flashing. One short flash every 2 s.
    case flashing
    /// Oc: occulting. Mostly on, briefly eclipsed.
    case occulting
    /// Iso: isophase. Equal light and dark.
    case isophase
    /// Al: alternating between two colors, always lit.
    case alternating
    /// A rotating beam that sweeps left and right, lamp lit.
    case rotatingBeam

    /// The chart abbreviation ("F", "Q", "Fl", ...).
    public var abbreviation: String {
        switch self {
        case .off: "off"
        case .fixed: "F"
        case .quickFlashing: "Q"
        case .flashing: "Fl"
        case .occulting: "Oc"
        case .isophase: "Iso"
        case .alternating: "Al"
        case .rotatingBeam: "beam"
        }
    }
}

/// The lamp's state color. Pre-blended with the lantern glass when drawn,
/// never transparent.
public enum LampColor: String, CaseIterable, Sendable {
    case warmWhite
    case softBlue
    case warmYellow
    case amber
    case green
    case red
    case orange
}

/// What the lamp shows at one instant.
public struct LampOutput: Equatable, Sendable {
    /// Whether the lantern glass is lit.
    public var isOn: Bool
    /// The lamp color while lit, `nil` while dark.
    public var color: LampColor?
    /// The beam direction in radians for rotating lights, `nil` when there is no beam.
    /// 0 points at the viewer, π/2 to the right, π away, 3π/2 to the left.
    public var beamAngle: Double?

    public init(isOn: Bool, color: LampColor?, beamAngle: Double? = nil) {
        self.isOn = isOn
        self.color = isOn ? color : nil
        self.beamAngle = isOn ? beamAngle : nil
    }

    public static let dark = LampOutput(isOn: false, color: nil)
}

extension LighthouseState {
    /// The light characteristic the lamp uses in this state.
    public var lightCharacteristic: LightCharacteristic {
        switch self {
        case .sleeping, .denied, .disconnected: .off
        case .waitingInput, .allowed: .fixed
        case .needsApproval: .quickFlashing
        case .error: .flashing
        case .caution: .occulting
        case .thinking: .isophase
        case .greeting: .alternating
        case .working, .workingMany: .rotatingBeam
        }
    }

    /// The lamp color (the first color for alternating lights), `nil` for dark states.
    public var lampColor: LampColor? {
        switch self {
        case .sleeping, .denied, .disconnected: nil
        case .waitingInput: .warmWhite
        case .allowed: .green
        case .needsApproval: .amber
        case .error: .red
        case .caution: .orange
        case .thinking: .softBlue
        case .greeting: .warmWhite
        case .working, .workingMany: .warmYellow
        }
    }

    /// The second color of an alternating light.
    public var alternateLampColor: LampColor? {
        self == .greeting ? .softBlue : nil
    }
}

/// Timing and pure functions for the lamp, the eyes and the body.
public enum LighthouseLight {
    /// Periods and phase lengths in seconds.
    public enum Timing {
        /// Q: one flash per second, lit for 0.4 s.
        public static let quickPeriod: TimeInterval = 1.0
        public static let quickOn: TimeInterval = 0.4
        /// Fl: one 0.5 s flash every 2 s.
        public static let flashPeriod: TimeInterval = 2.0
        public static let flashOn: TimeInterval = 0.5
        /// Oc: 3 s period, dark for the last 0.5 s.
        public static let occultPeriod: TimeInterval = 3.0
        public static let occultDark: TimeInterval = 0.5
        /// Iso: 2 s period, 1 s lit, 1 s dark.
        public static let isoPeriod: TimeInterval = 2.0
        /// Al: the color changes every second.
        public static let alternatePeriod: TimeInterval = 2.0
        /// Rotating beam: one revolution every 4 s.
        public static let beamRevolution: TimeInterval = 4.0
        /// The fixed beam direction used when motion is reduced (pointing right).
        public static let staticBeamAngle: Double = .pi / 2
        /// For two beams (workingMany), how far the second beam trails the first, in radians.
        public static let secondBeamLag: Double = .pi * 35 / 180
        /// Eye blink while waiting for input: once every 5 s, closed for 0.15 s.
        public static let blinkPeriod: TimeInterval = 5.0
        public static let blinkClosed: TimeInterval = 0.15
        /// Greeting hop: one 0.4 s hop per second.
        public static let hopPeriod: TimeInterval = 1.0
        public static let hopDuration: TimeInterval = 0.4
        /// Frame interval for smooth motion (beam, hop).
        public static let smoothFrameInterval: TimeInterval = 1.0 / 30.0
    }

    /// The lamp output for `state` at time `t` (seconds; any epoch, phases are
    /// taken modulo each period so every lighthouse on screen stays in sync).
    /// With `reduceMotion` the lamp never blinks or rotates: lit states are
    /// steadily lit (alternating lights hold their first color, beams hold still)
    /// and dark states stay dark.
    public static func output(for state: LighthouseState, at t: TimeInterval, reduceMotion: Bool) -> LampOutput {
        let color = state.lampColor
        switch state.lightCharacteristic {
        case .off:
            return .dark
        case .fixed:
            return LampOutput(isOn: true, color: color)
        case .quickFlashing:
            if reduceMotion { return LampOutput(isOn: true, color: color) }
            return LampOutput(isOn: phase(t, Timing.quickPeriod) < Timing.quickOn, color: color)
        case .flashing:
            if reduceMotion { return LampOutput(isOn: true, color: color) }
            return LampOutput(isOn: phase(t, Timing.flashPeriod) < Timing.flashOn, color: color)
        case .occulting:
            if reduceMotion { return LampOutput(isOn: true, color: color) }
            let lit = phase(t, Timing.occultPeriod) < Timing.occultPeriod - Timing.occultDark
            return LampOutput(isOn: lit, color: color)
        case .isophase:
            if reduceMotion { return LampOutput(isOn: true, color: color) }
            return LampOutput(isOn: phase(t, Timing.isoPeriod) < Timing.isoPeriod / 2, color: color)
        case .alternating:
            if reduceMotion { return LampOutput(isOn: true, color: color) }
            let first = phase(t, Timing.alternatePeriod) < Timing.alternatePeriod / 2
            return LampOutput(isOn: true, color: first ? color : state.alternateLampColor)
        case .rotatingBeam:
            if reduceMotion {
                return LampOutput(isOn: true, color: color, beamAngle: Timing.staticBeamAngle)
            }
            let angle = 2 * Double.pi * phase(t, Timing.beamRevolution) / Timing.beamRevolution
            return LampOutput(isOn: true, color: color, beamAngle: angle)
        }
    }

    /// Whether the eyes are closed for a blink at `t`. Only `waitingInput` blinks.
    public static func isBlinking(_ state: LighthouseState, at t: TimeInterval, reduceMotion: Bool) -> Bool {
        guard state == .waitingInput, !reduceMotion else { return false }
        return phase(t, Timing.blinkPeriod) >= Timing.blinkPeriod - Timing.blinkClosed
    }

    /// How far the body is lifted for the greeting hop at `t`, from 0 (on the
    /// ground) to 1 (top of the hop).
    public static func hopHeight(_ state: LighthouseState, at t: TimeInterval, reduceMotion: Bool) -> Double {
        guard state == .greeting, !reduceMotion else { return 0 }
        let p = phase(t, Timing.hopPeriod)
        guard p < Timing.hopDuration else { return 0 }
        return sin(Double.pi * p / Timing.hopDuration)
    }

    /// Whether anything in the drawing changes over time for `state`.
    public static func isAnimated(_ state: LighthouseState, reduceMotion: Bool) -> Bool {
        if reduceMotion { return false }
        switch state {
        case .sleeping, .denied, .disconnected, .allowed: return false
        case .waitingInput: return true  // eye blink
        default: return state.lightCharacteristic != .fixed
        }
    }

    /// Whether `state` needs smooth frames (a moving beam or a hop) rather than
    /// a redraw only at discrete step times.
    public static func needsSmoothFrames(_ state: LighthouseState, reduceMotion: Bool) -> Bool {
        if reduceMotion { return false }
        return state == .greeting || state.lightCharacteristic == .rotatingBeam
    }

    /// The next time strictly after `t` at which the drawing changes, or `nil`
    /// when the drawing never changes. Step states return the next step boundary;
    /// smooth states return the next frame.
    public static func nextChange(for state: LighthouseState, after t: TimeInterval, reduceMotion: Bool) -> TimeInterval? {
        guard isAnimated(state, reduceMotion: reduceMotion) else { return nil }
        if needsSmoothFrames(state, reduceMotion: reduceMotion) {
            return nextBoundary(after: t, period: Timing.smoothFrameInterval, offsets: [0])
        }
        switch state {
        case .waitingInput:
            return nextBoundary(after: t, period: Timing.blinkPeriod,
                                offsets: [0, Timing.blinkPeriod - Timing.blinkClosed])
        case .needsApproval:
            return nextBoundary(after: t, period: Timing.quickPeriod, offsets: [0, Timing.quickOn])
        case .error:
            return nextBoundary(after: t, period: Timing.flashPeriod, offsets: [0, Timing.flashOn])
        case .caution:
            return nextBoundary(after: t, period: Timing.occultPeriod,
                                offsets: [0, Timing.occultPeriod - Timing.occultDark])
        case .thinking:
            return nextBoundary(after: t, period: Timing.isoPeriod, offsets: [0, Timing.isoPeriod / 2])
        default:
            return nil
        }
    }

    // MARK: - Helpers

    /// `t` modulo `period`, in [0, period).
    static func phase(_ t: TimeInterval, _ period: TimeInterval) -> TimeInterval {
        let p = t - (t / period).rounded(.down) * period
        return p >= period ? 0 : max(0, p)
    }

    /// The smallest `k * period + offset` strictly greater than `t`.
    static func nextBoundary(after t: TimeInterval, period: TimeInterval, offsets: [TimeInterval]) -> TimeInterval {
        let base = (t / period).rounded(.down) * period
        var best = Double.infinity
        for k in 0...1 {
            for offset in offsets {
                let candidate = base + Double(k) * period + offset
                if candidate > t, candidate < best { best = candidate }
            }
        }
        return best
    }
}
