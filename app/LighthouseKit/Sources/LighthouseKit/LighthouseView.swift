import SwiftUI

/// The lighthouse character (등대 v2, 기능 명세서 F-12). `size` is the height in
/// points: 22 next to the notch, around 120 in the expanded view. The view is
/// `size * LighthouseView.aspectRatio` wide; the extra width on the right holds
/// the badge and the beam.
///
/// The lamp blinks at most once per second. With Reduce Motion, or with
/// `animated: false`, nothing blinks, rotates or hops: lit states hold a steady
/// light and dark states stay dark.
public struct LighthouseView: View {
    public let state: LighthouseState
    public let size: CGFloat
    public let animated: Bool

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// Width divided by height.
    public static let aspectRatio: CGFloat = LighthouseRenderer.unitWidth / LighthouseRenderer.unitHeight

    public init(state: LighthouseState, size: CGFloat = 22, animated: Bool = true) {
        self.state = state
        self.size = size
        self.animated = animated
    }

    /// The lamp output for `state` at time `t`. See `LighthouseLight.output`.
    public static func lampOutput(for state: LighthouseState, at t: TimeInterval, reduceMotion: Bool) -> LampOutput {
        LighthouseLight.output(for: state, at: t, reduceMotion: reduceMotion)
    }

    public var body: some View {
        let still = !animated || reduceMotion
        Group {
            if !still, LighthouseLight.isAnimated(state, reduceMotion: false) {
                TimelineView(LighthouseSchedule(state: state)) { context in
                    LighthouseFrame(state: state, size: size,
                                    time: context.date.timeIntervalSinceReferenceDate,
                                    reduceMotion: context.cadence != .live)
                }
            } else {
                LighthouseFrame(state: state, size: size, time: 0, reduceMotion: true)
            }
        }
        .accessibilityElement()
        .accessibilityLabel(Text("farero \(state.label)"))
    }
}

/// One still frame of the lighthouse at time `time` (seconds since the
/// reference date, the same clock `LighthouseView` uses). With `reduceMotion`
/// the frame is the steady, motionless look. Useful for rendering images.
public struct LighthouseFrame: View {
    public let state: LighthouseState
    public let size: CGFloat
    public let time: TimeInterval
    public let reduceMotion: Bool

    public init(state: LighthouseState, size: CGFloat = 22, time: TimeInterval, reduceMotion: Bool = false) {
        self.state = state
        self.size = size
        self.time = time
        self.reduceMotion = reduceMotion
    }

    public var body: some View {
        let renderer = LighthouseRenderer(state: state, time: time, reduceMotion: reduceMotion)
        Canvas(rendersAsynchronously: false) { context, canvasSize in
            renderer.draw(in: context, size: canvasSize)
        }
        .frame(width: size * LighthouseView.aspectRatio, height: size)
    }
}

/// Fires only when the drawing changes: at each step of a blinking light, or
/// at 30 fps while a beam turns or the body hops.
struct LighthouseSchedule: TimelineSchedule {
    let state: LighthouseState

    func entries(from startDate: Date, mode: TimelineScheduleMode) -> Entries {
        Entries(state: state, upcoming: startDate, lowFrequency: mode == .lowFrequency)
    }

    struct Entries: Sequence, IteratorProtocol {
        let state: LighthouseState
        var upcoming: Date?
        let lowFrequency: Bool

        mutating func next() -> Date? {
            guard let current = upcoming else { return nil }
            if lowFrequency {
                // In low-frequency mode SwiftUI renders the still look (cadence != .live).
                upcoming = nil
                return current
            }
            let t = current.timeIntervalSinceReferenceDate
            // Land just after each boundary so the frame shows the new step.
            upcoming = LighthouseLight.nextChange(for: state, after: t, reduceMotion: false)
                .map { Date(timeIntervalSinceReferenceDate: $0 + 0.001) }
            return current
        }
    }
}
