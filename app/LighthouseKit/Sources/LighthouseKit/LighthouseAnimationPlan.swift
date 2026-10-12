import Foundation

/// How `LighthouseView` plays an animated state with Core Animation, so the
/// app does no work per frame.
///
/// The drawing splits into the canvas's animated groups, each one layer:
/// the beam (turns), the body (breathes, hops or shakes, and shows the lamp)
/// and the badge (rises and fades for "z", blinks with the lamp for "!").
/// Light steps swap pre-rendered images at their exact times (discrete
/// keyframes); continuous motion is a transform that Core Animation
/// interpolates, with the canvas's own keyframes and easing. Every track
/// repeats on its own period and is phased to absolute time, so every
/// lighthouse on screen agrees with `LighthouseLight` and with each other.
struct LighthouseAnimationPlan: Equatable, Sendable {
    /// The lamp's repeating steps. The body shows one image per step; the
    /// "!" badge of `needsApproval` blinks with it.
    struct LampTrack: Equatable, Sendable {
        /// One cycle of the light, in seconds.
        var period: TimeInterval
        /// When each step starts, as a fraction of `period`: 0 first, increasing, below 1.
        var keyTimes: [Double]
        /// The lamp during each step.
        var outputs: [LampOutput]
    }

    /// `nil` when the lamp is steady.
    var lamp: LampTrack?
    /// The steady lamp, when `lamp` is `nil`.
    var steadyLamp: LampOutput
    /// One full beam sweep, `nil` without a beam.
    var beamPeriod: TimeInterval?
    /// The body motion drawn at this size (`.none` when the breath is too small to see).
    var motion: LighthouseMotion
    /// Whether the sleeping "z" rises and fades.
    var risingZ: Bool
    /// Whether the badge blinks with the lamp (`needsApproval`).
    var badgeBlinksWithLamp: Bool
    /// The whole drawing repeats every `period` seconds: the least common
    /// multiple of the tracks above.
    var period: TimeInterval

    /// The plan for `state` drawn `size` points tall, or `nil` when nothing
    /// moves at that size (draw `LighthouseFrame` instead).
    init?(state: LighthouseState, size: CGFloat) {
        guard LighthouseLight.isAnimated(state, reduceMotion: false, size: size) else { return nil }
        if let steps = LighthouseLight.lightSteps(state) {
            let offsets = steps.offsets.sorted()
            // Sample each step just after it starts, away from the boundary.
            let outputs = offsets.map {
                LighthouseLight.output(for: state, at: $0 + steps.period * 1e-6, reduceMotion: false)
            }
            lamp = LampTrack(period: steps.period, keyTimes: offsets.map { $0 / steps.period }, outputs: outputs)
        } else {
            lamp = nil
        }
        steadyLamp = LighthouseLight.output(for: state, at: 0, reduceMotion: false)
        beamPeriod = state.beamPeriod
        let motion = state.gesture.motion
        self.motion = motion == .breathe && !LighthouseLight.breathes(at: size) ? .none : motion
        risingZ = state.badge == .sleepZ
        badgeBlinksWithLamp = state.badge == .exclamation && lamp != nil

        var periods: [TimeInterval] = []
        if let lamp { periods.append(lamp.period) }
        if let beamPeriod { periods.append(beamPeriod) }
        if let p = Self.period(of: self.motion) { periods.append(p) }
        if risingZ { periods.append(LighthouseLight.Timing.sleepZPeriod) }
        period = Self.leastCommonMultiple(periods)
    }

    /// The images the layers need: one body image per lamp step (or one),
    /// plus the beam and the badge when the state has them.
    func imageCount(for state: LighthouseState) -> Int {
        (lamp?.outputs.count ?? 1) + (beamPeriod == nil ? 0 : 1) + (state.badge == .none ? 0 : 1)
    }

    static func period(of motion: LighthouseMotion) -> TimeInterval? {
        switch motion {
        case .none: nil
        case .breathe: LighthouseLight.Timing.breathePeriod
        case .hop: LighthouseLight.Timing.hopPeriod
        case .shake: LighthouseLight.Timing.shakePeriod
        }
    }

    /// The least common multiple of periods given to the millisecond.
    static func leastCommonMultiple(_ periods: [TimeInterval]) -> TimeInterval {
        func gcd(_ a: Int, _ b: Int) -> Int { b == 0 ? a : gcd(b, a % b) }
        let millis = periods.map { Int(($0 * 1000).rounded()) }.filter { $0 > 0 }
        guard let first = millis.first else { return 0 }
        let lcm = millis.dropFirst().reduce(first) { $0 / gcd($0, $1) * $1 }
        return TimeInterval(lcm) / 1000
    }
}
