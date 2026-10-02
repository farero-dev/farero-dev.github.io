import Foundation

// Per-state appearance (F-12). The lamp carries the state; the eyes carry the
// mood. Color is never the only cue (WCAG 1.4.1): every state also differs in
// light rhythm, eye shape, badge glyph and body gesture.

/// The shape of the tower's two eyes.
public enum LighthouseEyes: String, CaseIterable, Sendable {
    /// Calm round eyes.
    case open
    /// Big round eyes with a highlight: alert, asking.
    case wide
    /// Upward arcs (^ ^): happy.
    case happy
    /// Eyes shifted up and to the side: looking up, thinking.
    case lookingUp
    /// Flat-topped, slightly slanted: focused on the work.
    case focused
    /// Narrow slits glancing to the side: wary.
    case narrowSideGlance
    /// Downward arcs: asleep.
    case closed
    /// Flat lines: shut, refusing.
    case flat
    /// × marks: something broke.
    case crossed
    /// Hollow rings: dim, nobody home.
    case hollow
}

/// A small glyph next to the lamp. Never a circle around the character.
public enum LighthouseBadge: String, CaseIterable, Sendable {
    case none
    /// "z": asleep.
    case sleepZ
    /// A four-point sparkle: hello.
    case sparkle
    /// Rising thought dots.
    case thoughtDots
    /// "!": needs you.
    case exclamation
    /// A check mark: allowed.
    case check
    /// A minus bar: denied.
    case minus
    /// A warning triangle: tainted session.
    case triangle
    /// A lightning bolt: error.
    case bolt
    /// A slash through the lantern: no connection.
    case slash
    /// "+": more than one session.
    case plus
}

/// A static body posture plus the animated hop.
public struct LighthouseGesture: Equatable, Sendable {
    /// Lean in degrees; positive leans right (clockwise), pivoting at the base.
    public var leanDegrees: Double
    /// Vertical scale of everything above the ground (1 = normal).
    public var heightScale: Double
    /// Whether the body hops (greeting).
    public var hops: Bool

    public init(leanDegrees: Double = 0, heightScale: Double = 1, hops: Bool = false) {
        self.leanDegrees = leanDegrees
        self.heightScale = heightScale
        self.hops = hops
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
        case .denied: .flat
        case .caution: .narrowSideGlance
        case .waitingInput: .open
        case .error: .crossed
        case .disconnected: .hollow
        }
    }

    /// The badge glyph for this state.
    public var badge: LighthouseBadge {
        switch self {
        case .sleeping: .sleepZ
        case .greeting: .sparkle
        case .thinking: .thoughtDots
        case .working: .none
        case .workingMany: .plus
        case .needsApproval: .exclamation
        case .allowed: .check
        case .denied: .minus
        case .caution: .triangle
        case .waitingInput: .none
        case .error: .bolt
        case .disconnected: .slash
        }
    }

    /// The body gesture for this state.
    public var gesture: LighthouseGesture {
        switch self {
        case .sleeping: LighthouseGesture(leanDegrees: -5, heightScale: 0.96)
        case .greeting: LighthouseGesture(hops: true)
        case .thinking: LighthouseGesture(leanDegrees: -3)
        case .working, .workingMany: LighthouseGesture()
        case .needsApproval: LighthouseGesture(leanDegrees: 5)
        case .allowed: LighthouseGesture(heightScale: 1.03)
        case .denied: LighthouseGesture(heightScale: 0.94)
        case .caution: LighthouseGesture(leanDegrees: -4)
        case .waitingInput: LighthouseGesture()
        case .error: LighthouseGesture(leanDegrees: 3)
        case .disconnected: LighthouseGesture(leanDegrees: -2)
        }
    }

    /// Whether the body is drawn dimmed (structure gray instead of white).
    public var isDimmed: Bool { self == .disconnected }

    /// How many beams the rotating light shows: 1 for working, 2 for
    /// workingMany, 0 for every other state.
    public var beamCount: Int {
        switch self {
        case .working: 1
        case .workingMany: 2
        default: 0
        }
    }
}
