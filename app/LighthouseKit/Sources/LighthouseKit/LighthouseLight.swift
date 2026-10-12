import Foundation

// The lamp follows real lighthouse light characteristics (기능 명세서 F-12).
// Timings and motions come from the design canvas "등대 v2 (확정)" (its CSS
// keyframes). Everything here is a pure function of (state, time) so the
// drawing code, the timeline schedule and the tests all agree on what the
// lighthouse does.
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
    /// A beam that turns, seen from the side: it sweeps right, shrinks, and
    /// comes back out on the left.
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

/// The lamp's state color. The lit glass and the beam use it pre-mixed
/// (see `LighthousePalette`), never transparent.
public enum LampColor: String, CaseIterable, Sendable {
    case warmWhite
    case yellow
    case teal
    case orange
    case green
    case amber
    case red
}

/// What the lamp shows at one instant.
public struct LampOutput: Equatable, Sendable {
    /// Whether the lamp is lit.
    public var isOn: Bool
    /// The color of the lamp itself while lit, `nil` while dark.
    public var color: LampColor?
    /// The color the lantern glass is tinted with while lit, `nil` while dark.
    /// It is `color` except for an alternating light, whose glass keeps its
    /// first color while the lamp changes.
    public var glassColor: LampColor?
    /// For a rotating light, how far the beam reaches sideways: 1 is full
    /// length to the right, -1 full length to the left, 0 pointing at or away
    /// from the viewer. `nil` when there is no beam.
    public var beamReach: Double?

    public init(isOn: Bool, color: LampColor?, glassColor: LampColor? = nil, beamReach: Double? = nil) {
        self.isOn = isOn
        self.color = isOn ? color : nil
        self.glassColor = isOn ? (glassColor ?? color) : nil
        self.beamReach = isOn ? beamReach : nil
    }

    public static let dark = LampOutput(isOn: false, color: nil)
}

/// Where the body is at one instant of its repeating motion. Units are the
/// drawing's 24-unit grid.
public struct LighthousePose: Equatable, Sendable {
    /// Sideways offset (shake).
    public var offsetX: Double
    /// Vertical offset, negative is up (hop).
    public var offsetY: Double
    /// Vertical scale around the foot of the tower (breath).
    public var breath: Double

    public init(offsetX: Double = 0, offsetY: Double = 0, breath: Double = 1) {
        self.offsetX = offsetX
        self.offsetY = offsetY
        self.breath = breath
    }

    public static let still = LighthousePose()
}

/// The rising "z" letters of the sleeping badge at one instant.
public struct SleepZFrame: Equatable, Sendable {
    /// Vertical offset in units, negative is up.
    public var offsetY: Double
    /// From 0 (gone) to 1 (fully drawn). The letters fade in and out as on the canvas.
    public var opacity: Double

    public static let rest = SleepZFrame(offsetY: 0, opacity: 1)
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

    /// The lamp color (for an alternating light, the color of its glass),
    /// `nil` for dark states.
    public var lampColor: LampColor? {
        switch self {
        case .sleeping, .denied, .disconnected: nil
        case .waitingInput, .thinking: .warmWhite
        case .greeting: .yellow
        case .working, .workingMany: .teal
        case .needsApproval: .orange
        case .allowed: .green
        case .caution: .amber
        case .error: .red
        }
    }

    /// The second color of an alternating light.
    public var alternateLampColor: LampColor? {
        self == .greeting ? .teal : nil
    }

    /// For a rotating light, the time of one full sweep (right, left, right).
    /// With more than one session working the beam turns twice as fast, so
    /// the two working states differ in rhythm as well as in the badge.
    public var beamPeriod: TimeInterval? {
        switch self {
        case .working: LighthouseLight.Timing.beamPeriod
        case .workingMany: LighthouseLight.Timing.manyBeamPeriod
        default: nil
        }
    }
}

/// Timing and pure functions for the lamp and the body.
public enum LighthouseLight {
    /// Periods and phase lengths in seconds, distances in drawing units.
    public enum Timing {
        /// Q: one flash per second, lit for 0.45 s.
        public static let quickPeriod: TimeInterval = 1.0
        public static let quickOn: TimeInterval = 0.45
        /// Fl: one 0.5 s flash every 2 s.
        public static let flashPeriod: TimeInterval = 2.0
        public static let flashOn: TimeInterval = 0.5
        /// Oc: 3 s period, dark for the last 0.6 s.
        public static let occultPeriod: TimeInterval = 3.0
        public static let occultDark: TimeInterval = 0.6
        /// Iso: 2 s period, 1 s lit, 1 s dark.
        public static let isoPeriod: TimeInterval = 2.0
        /// Al: the lamp changes color every second. The canvas changes it every
        /// 0.8 s; F-12 caps changes at one per second, so this is slower.
        public static let alternatePeriod: TimeInterval = 2.0
        /// Rotating beam: one sweep (right, left, right) every 3.2 s.
        public static let beamPeriod: TimeInterval = 3.2
        /// Rotating beam with more than one session working.
        public static let manyBeamPeriod: TimeInterval = 1.6
        /// The beam reach used when motion is reduced (full length to the right).
        public static let staticBeamReach: Double = 1
        /// Breath: 3.6 s, the body down to 98.5 % height at the middle.
        public static let breathePeriod: TimeInterval = 3.6
        public static let breatheDepth: Double = 0.015
        /// Hop: once per second, 1.4 units high.
        public static let hopPeriod: TimeInterval = 1.0
        public static let hopHeight: Double = 1.4
        /// Shake: four times per second, 0.4 units each way.
        public static let shakePeriod: TimeInterval = 0.25
        public static let shakeDistance: Double = 0.4
        /// Sleeping "z": rises from 0.4 below to 0.8 above its place every 2.4 s.
        public static let sleepZPeriod: TimeInterval = 2.4
        /// Frame interval for the beam, the hop, the shake and the rising "z".
        public static let smoothFrameInterval: TimeInterval = 1.0 / 30.0
        /// Frame interval when only the slow breath moves (it moves 0.3 pt at 22 pt).
        public static let breathFrameInterval: TimeInterval = 1.0 / 15.0
    }

    /// The smallest movement worth a redraw: half a point, one pixel on a
    /// Retina screen.
    public static let minVisibleMotion: CGFloat = 0.5

    /// Whether the breath moves the body by at least `minVisibleMotion` at
    /// height `size`. At 22 pt it moves 0.3 pt: the body stands still there,
    /// and no redraw is spent on it.
    public static func breathes(at size: CGFloat) -> Bool {
        size * CGFloat(Timing.breatheDepth) >= minVisibleMotion
    }

    /// The lamp output for `state` at time `t` (seconds; any epoch, phases are
    /// taken modulo each period so every lighthouse on screen stays in sync).
    /// With `reduceMotion` the lamp never blinks or turns: lit states are
    /// steadily lit and dark states stay dark. As on the canvas with motion
    /// off, an alternating light shows its second color over its first
    /// color's glass and a beam points right.
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
            let second = state.alternateLampColor ?? color
            if reduceMotion { return LampOutput(isOn: true, color: second, glassColor: color) }
            let first = phase(t, Timing.alternatePeriod) < Timing.alternatePeriod / 2
            return LampOutput(isOn: true, color: first ? second : color, glassColor: color)
        case .rotatingBeam:
            guard !reduceMotion, let period = state.beamPeriod else {
                return LampOutput(isOn: true, color: color, beamReach: Timing.staticBeamReach)
            }
            // The canvas flips the beam with scaleX 1 → -1 → 1, easing each half.
            let p = phase(t, period) / period
            let reach = p < 0.5 ? 1 - 2 * easeInOut(p * 2) : -1 + 2 * easeInOut(p * 2 - 1)
            return LampOutput(isOn: true, color: color, beamReach: reach)
        }
    }

    /// The body's motion offsets for `state` at `t`, drawn `size` points
    /// tall. `.still` with `reduceMotion`, and for a breath too small to see.
    public static func pose(
        for state: LighthouseState, at t: TimeInterval, reduceMotion: Bool, size: CGFloat = .infinity
    ) -> LighthousePose {
        if reduceMotion { return .still }
        switch state.gesture.motion {
        case .none:
            return .still
        case .breathe:
            guard breathes(at: size) else { return .still }
            let p = phase(t, Timing.breathePeriod) / Timing.breathePeriod
            let depth = p < 0.5 ? easeInOut(p * 2) : 1 - easeInOut(p * 2 - 1)
            return LighthousePose(breath: 1 - Timing.breatheDepth * depth)
        case .hop:
            // Up by 40 %, held until 60 %, down by 100 %.
            let p = phase(t, Timing.hopPeriod) / Timing.hopPeriod
            let lift: Double
            if p < 0.4 {
                lift = easeInOut(p / 0.4)
            } else if p < 0.6 {
                lift = 1
            } else {
                lift = 1 - easeInOut((p - 0.6) / 0.4)
            }
            return LighthousePose(offsetY: -Timing.hopHeight * lift)
        case .shake:
            // Linear: left by 25 %, right by 75 %, back by 100 %.
            let p = phase(t, Timing.shakePeriod) / Timing.shakePeriod
            let d = Timing.shakeDistance
            let x: Double
            if p < 0.25 {
                x = -d * p / 0.25
            } else if p < 0.75 {
                x = -d + 2 * d * (p - 0.25) / 0.5
            } else {
                x = d * (1 - (p - 0.75) / 0.25)
            }
            return LighthousePose(offsetX: x)
        }
    }

    /// The rising "z" letters of the sleeping badge at `t`. `.rest` with `reduceMotion`.
    public static func sleepZ(at t: TimeInterval, reduceMotion: Bool) -> SleepZFrame {
        if reduceMotion { return .rest }
        let p = phase(t, Timing.sleepZPeriod) / Timing.sleepZPeriod
        let opacity = p < 0.4 ? easeInOut(p / 0.4) : 1 - easeInOut((p - 0.4) / 0.6)
        return SleepZFrame(offsetY: 0.4 - 1.2 * easeInOut(p), opacity: opacity)
    }

    /// Whether anything in the drawing changes over time for `state` drawn
    /// `size` points tall.
    public static func isAnimated(_ state: LighthouseState, reduceMotion: Bool, size: CGFloat = .infinity) -> Bool {
        if reduceMotion { return false }
        switch state.lightCharacteristic {
        case .off, .fixed:
            let motion = state.gesture.motion
            let moves = motion != .none && (motion != .breathe || breathes(at: size))
            return moves || state.badge == .sleepZ
        default:
            return true
        }
    }

    /// How often to redraw for smooth motion (beam, hop, shake, breath, rising
    /// "z"), or `nil` when `state` only changes at discrete steps or not at all.
    public static func frameInterval(
        for state: LighthouseState, reduceMotion: Bool, size: CGFloat = .infinity
    ) -> TimeInterval? {
        if reduceMotion { return nil }
        let motion = state.gesture.motion
        if state.beamPeriod != nil || motion == .hop || motion == .shake || state.badge == .sleepZ {
            return Timing.smoothFrameInterval
        }
        return motion == .breathe && breathes(at: size) ? Timing.breathFrameInterval : nil
    }

    /// The next time strictly after `t` at which the drawing changes, or `nil`
    /// when the drawing never changes: the next light step or the next smooth
    /// frame, whichever comes first.
    public static func nextChange(
        for state: LighthouseState, after t: TimeInterval, reduceMotion: Bool, size: CGFloat = .infinity
    ) -> TimeInterval? {
        guard isAnimated(state, reduceMotion: reduceMotion, size: size) else { return nil }
        var next = Double.infinity
        if let interval = frameInterval(for: state, reduceMotion: reduceMotion, size: size) {
            next = nextBoundary(after: t, period: interval, offsets: [0])
        }
        if let steps = lightSteps(state) {
            next = min(next, nextBoundary(after: t, period: steps.period, offsets: steps.offsets))
        }
        return next.isFinite ? next : nil
    }

    // MARK: - Helpers

    /// The period and the step times within it at which the lamp switches.
    static func lightSteps(_ state: LighthouseState) -> (period: TimeInterval, offsets: [TimeInterval])? {
        switch state.lightCharacteristic {
        case .quickFlashing: (Timing.quickPeriod, [0, Timing.quickOn])
        case .flashing: (Timing.flashPeriod, [0, Timing.flashOn])
        case .occulting: (Timing.occultPeriod, [0, Timing.occultPeriod - Timing.occultDark])
        case .isophase: (Timing.isoPeriod, [0, Timing.isoPeriod / 2])
        case .alternating: (Timing.alternatePeriod, [0, Timing.alternatePeriod / 2])
        case .off, .fixed, .rotatingBeam: nil
        }
    }

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

    /// CSS `ease-in-out` (cubic-bezier(0.42, 0, 0.58, 1)), the easing the
    /// canvas animations use. `x` in [0, 1].
    static func easeInOut(_ x: Double) -> Double {
        let x = min(max(x, 0), 1)
        // Solve bx(s) = x for s by bisection (bx is increasing), then return by(s).
        func bx(_ s: Double) -> Double { 3 * (1 - s) * (1 - s) * s * 0.42 + 3 * (1 - s) * s * s * 0.58 + s * s * s }
        var lo = 0.0
        var hi = 1.0
        for _ in 0..<30 {
            let mid = (lo + hi) / 2
            if bx(mid) < x { lo = mid } else { hi = mid }
        }
        let s = (lo + hi) / 2
        return s * s * (3 - 2 * s)
    }
}
