import Foundation

// Per-state appearance (F-12), as drawn on the design canvas "등대 v2 (확정) ·
// 상태 12종". The lamp carries the state; the eyes carry the mood. Color is
// never the only cue (WCAG 1.4.1): every state also differs in light rhythm,
// eye shape, badge shape or body gesture.

/// The shape of the tower's two eyes.
public enum LighthouseEyes: String, CaseIterable, Sendable {
    /// Round eyes with a highlight: the default face.
    case open
    /// Bigger round eyes: alert, asking.
    case wide
    /// Upward arcs (^ ^): happy.
    case happy
    /// Round eyes raised a little: looking up, thinking.
    case lookingUp
    /// Half discs, flat on top: focused on the work.
    case focused
    /// Half discs shifted to the viewer's left: a wary side glance.
    case sideGlance
    /// Downward arcs: asleep.
    case closed
    /// "> <": squeezed shut, refusing.
    case squeezed
    /// × marks: something broke.
    case crossed
    /// Small half discs: dim, nobody home.
    case dim
}

/// A small badge up and to the left of the lantern. Never a circle around the
/// character.
public enum LighthouseBadge: String, CaseIterable, Sendable {
    case none
    /// Two "z" letters rising beside the lantern: asleep.
    case sleepZ
    /// A four-point sparkle: hello.
    case sparkle
    /// Three dots in a disc: thinking.
    case thoughtDots
    /// The number of running sessions in a disc ("+" when the count is unknown).
    case count
    /// "!" in a disc, blinking with the lamp: needs you.
    case exclamation
    /// A check mark in a disc: allowed.
    case check
    /// An × in a disc: denied.
    case cross
    /// A warning triangle: tainted session.
    case triangle
    /// A lightning bolt: error.
    case bolt
    /// "?" in a gray disc: no connection.
    case question
}

/// The repeating body motion of a state.
public enum LighthouseMotion: String, CaseIterable, Sendable {
    /// Still.
    case none
    /// A slow breath: the body shrinks 1.5 % in height and back every 3.6 s.
    case breathe
    /// A hop of 1.4 units every second.
    case hop
    /// A small sideways shake, 0.4 units each way, four times a second.
    case shake
}

/// A static body posture plus a repeating motion.
public struct LighthouseGesture: Equatable, Sendable {
    /// Lean in degrees around the foot of the tower; positive leans right (clockwise).
    public var leanDegrees: Double
    /// Horizontal scale around the foot of the tower (1 = normal).
    public var widthScale: Double
    /// Vertical scale around the foot of the tower (1 = normal).
    public var heightScale: Double
    /// The repeating motion.
    public var motion: LighthouseMotion

    public init(
        leanDegrees: Double = 0, widthScale: Double = 1, heightScale: Double = 1,
        motion: LighthouseMotion = .none
    ) {
        self.leanDegrees = leanDegrees
        self.widthScale = widthScale
        self.heightScale = heightScale
        self.motion = motion
    }
}

extension LighthouseState {
    /// The eye shape for this state.
    public var eyes: LighthouseEyes {
        switch self {
        case .sleeping: .closed
        case .greeting: .happy
        case .thinking: .lookingUp
        case .working, .workingMany: .focused
        case .needsApproval: .wide
        case .allowed: .happy
        case .denied: .squeezed
        case .caution: .sideGlance
        case .waitingInput: .open
        case .error: .crossed
        case .disconnected: .dim
        }
    }

    /// The badge for this state.
    public var badge: LighthouseBadge {
        switch self {
        case .sleeping: .sleepZ
        case .greeting: .sparkle
        case .thinking: .thoughtDots
        case .working: .none
        case .workingMany: .count
        case .needsApproval: .exclamation
        case .allowed: .check
        case .denied: .cross
        case .caution: .triangle
        case .waitingInput: .none
        case .error: .bolt
        case .disconnected: .question
        }
    }

    /// The body gesture for this state.
    public var gesture: LighthouseGesture {
        switch self {
        case .sleeping: LighthouseGesture(motion: .breathe)
        case .greeting: LighthouseGesture(motion: .hop)
        case .thinking: LighthouseGesture(motion: .breathe)
        case .working, .workingMany: LighthouseGesture(motion: .breathe)
        // Stretched up, eyes wide.
        case .needsApproval: LighthouseGesture(widthScale: 0.94, heightScale: 1.06)
        case .allowed: LighthouseGesture(motion: .hop)
        // Shrinks and turns away.
        case .denied: LighthouseGesture(leanDegrees: -4, widthScale: 1.05, heightScale: 0.93)
        case .caution: LighthouseGesture(leanDegrees: 3, motion: .breathe)
        case .waitingInput: LighthouseGesture(motion: .breathe)
        case .error: LighthouseGesture(leanDegrees: 6, motion: .shake)
        case .disconnected: LighthouseGesture()
        }
    }

    /// Whether the body is drawn with its color drained (disconnected).
    public var isDimmed: Bool { self == .disconnected }

    /// Whether the tower shows a crack (error).
    public var isCracked: Bool { self == .error }
}
