import SwiftUI

/// The lighthouse character (등대 v2, 기능 명세서 F-12). `size` is the height in
/// points: 22 next to the notch, around 120 in the expanded view. The view is
/// `size * LighthouseView.aspectRatio` wide (square, like the design canvas);
/// the badge sits in its upper left corner and the beam sweeps across it.
///
/// The lamp blinks at most once per second. With Reduce Motion, or with
/// `animated: false`, nothing blinks, turns, breathes or hops: lit states hold
/// a steady light and dark states stay dark.
///
/// Animated states are played by Core Animation from a few images rendered
/// once (see `LighthouseAnimationPlan`), so the view costs no app CPU per
/// frame. Being an AppKit view inside, an animated `LighthouseView` does not
/// show up in `ImageRenderer`; render `LighthouseFrame` for images.
public struct LighthouseView: View {
    public let state: LighthouseState
    public let size: CGFloat
    public let animated: Bool
    /// For `workingMany`, the number of running sessions shown in the badge
    /// (`nil` shows "+").
    public let sessionCount: Int?

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    /// Width divided by height.
    public static let aspectRatio: CGFloat = LighthouseRenderer.unitWidth / LighthouseRenderer.unitHeight

    public init(state: LighthouseState, size: CGFloat = 22, animated: Bool = true, sessionCount: Int? = nil) {
        self.state = state
        self.size = size
        self.animated = animated
        self.sessionCount = sessionCount
    }

    /// The lamp output for `state` at time `t`. See `LighthouseLight.output`.
    public static func lampOutput(for state: LighthouseState, at t: TimeInterval, reduceMotion: Bool) -> LampOutput {
        LighthouseLight.output(for: state, at: t, reduceMotion: reduceMotion)
    }

    public var body: some View {
        let still = !animated || reduceMotion
        Group {
            if !still, LighthouseLight.isAnimated(state, reduceMotion: false, size: size) {
                // Played by Core Animation: no per-frame work in the app.
                LighthouseAnimatedView(state: state, size: size, sessionCount: sessionCount)
                    .frame(width: size * Self.aspectRatio, height: size)
            } else {
                LighthouseFrame(state: state, size: size, time: 0, reduceMotion: true, sessionCount: sessionCount)
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
    /// For `workingMany`, the number of running sessions shown in the badge.
    public let sessionCount: Int?

    public init(
        state: LighthouseState, size: CGFloat = 22, time: TimeInterval, reduceMotion: Bool = false,
        sessionCount: Int? = nil
    ) {
        self.state = state
        self.size = size
        self.time = time
        self.reduceMotion = reduceMotion
        self.sessionCount = sessionCount
    }

    public var body: some View {
        let renderer = LighthouseRenderer(
            state: state, time: time, reduceMotion: reduceMotion, size: size,
            sessionCount: sessionCount)
        Canvas(rendersAsynchronously: false) { context, canvasSize in
            renderer.draw(in: context, size: canvasSize)
        }
        .frame(width: size * LighthouseView.aspectRatio, height: size)
    }
}
