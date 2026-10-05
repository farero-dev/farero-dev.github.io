import Foundation

/// The character's state (기능 명세서 F-12). Mirrors `LighthouseKit.LighthouseState`
/// case for case; FareroCore stays free of SwiftUI, and the app maps between
/// the two with an exhaustive switch.
public enum CharacterState: String, Sendable, CaseIterable {
    case sleeping
    case greeting
    case thinking
    case working
    case workingMany
    case needsApproval
    case allowed
    case denied
    case caution
    case waitingInput
    case error
    case disconnected
}

public struct CharacterResolution: Sendable, Equatable {
    public var state: CharacterState
    /// When a timed state (allowed, denied, error, greeting) runs out and the
    /// state should be resolved again. nil if nothing is timed.
    public var reevaluateAt: Date?

    public init(state: CharacterState, reevaluateAt: Date? = nil) {
        self.state = state
        self.reevaluateAt = reevaluateAt
    }
}

/// Picks the character state from the app state (F-12). Highest priority
/// first: disconnected > needsApproval > allowed > denied > error > caution >
/// workingMany > working > thinking > greeting > waitingInput > sleeping.
public enum CharacterResolver {
    public static let allowedDuration: TimeInterval = 1.5
    public static let deniedDuration: TimeInterval = 1.5
    public static let errorDuration: TimeInterval = 3
    public static let greetingDuration: TimeInterval = 2

    public static func resolve(_ s: AppState, now: Date) -> CharacterResolution {
        // Every timer still running; any of them ending can change the result.
        var deadlines: [Date] = []
        func running(_ start: Date?, _ duration: TimeInterval) -> Bool {
            guard let start else { return false }
            let end = start.addingTimeInterval(duration)
            guard end > now else { return false }
            deadlines.append(end)
            return true
        }
        let allowed = running(s.lastAllowedAt, allowedDuration)
        let denied = running(s.lastDeniedAt, deniedDuration)
        let error = running(s.lastErrorAt, errorDuration)
        let active = s.sessions.values.filter { $0.status.isActive }
        let greeting = !active.filter { running(s.sessionAppearedAt[$0.id], greetingDuration) }.isEmpty
        let next = deadlines.min()

        func result(_ state: CharacterState) -> CharacterResolution {
            CharacterResolution(state: state, reevaluateAt: next)
        }
        guard s.isConnected else { return CharacterResolution(state: .disconnected) }
        if !s.approvals.isEmpty { return result(.needsApproval) }
        if allowed { return result(.allowed) }
        if denied { return result(.denied) }
        if error { return result(.error) }
        if active.contains(where: \.tainted) { return result(.caution) }
        let runningSessions = active.filter { $0.status == .running }
        if runningSessions.count >= 2 { return result(.workingMany) }
        if let only = runningSessions.first {
            return result(only.currentTool.isEmpty ? .thinking : .working)
        }
        if greeting { return result(.greeting) }
        // A session still waiting for approval with no farero card is waiting
        // on the terminal's own prompt (the app was down, or the user answers
        // there): to the user that is "waiting for input".
        if active.contains(where: { $0.status == .waitingInput || $0.status == .waitingApproval }) {
            return result(.waitingInput)
        }
        return result(.sleeping)
    }
}
