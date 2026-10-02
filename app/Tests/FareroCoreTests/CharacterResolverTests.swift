import Foundation
import Testing
@testable import FareroCore

@Suite("Character state resolver (F-12)")
struct CharacterResolverTests {
    let t0 = Fixture.t0

    func state(_ s: AppState, at offset: TimeInterval = 0) -> CharacterState {
        CharacterResolver.resolve(s, now: t0.addingTimeInterval(offset)).state
    }

    @Test func casesMirrorLighthouseStates() {
        #expect(CharacterState.allCases.count == 12)
    }

    @Test func disconnectedBeatsEverything() {
        var s = Fixture.connected(sessions: [Fixture.session("s1", tool: "Bash")], approvals: [Fixture.approval("a1")])
        s.lastErrorAt = t0
        s.reduce(.ipc(.disconnected(reason: "x")), now: t0)
        #expect(state(s) == .disconnected)
        // Before the first snapshot as well.
        #expect(state(AppState()) == .disconnected)
        var connecting = AppState()
        connecting.reduce(.ipc(.connected), now: t0)
        #expect(state(connecting) == .disconnected)
    }

    @Test func needsApprovalBeatsTimedStates() {
        var s = Fixture.connected(sessions: [Fixture.session("s1", tainted: true)], approvals: [Fixture.approval("a1")])
        s.lastAllowedAt = t0
        s.lastDeniedAt = t0
        s.lastErrorAt = t0
        #expect(state(s) == .needsApproval)
    }

    @Test func allowedForOneAndAHalfSeconds() {
        var s = Fixture.connected(sessions: [Fixture.session("s1", tool: "Bash")], approvals: [Fixture.approval("a1")])
        s.reduce(.answerAccepted(approvalID: "a1", answer: .allow), now: t0)
        let r = CharacterResolver.resolve(s, now: t0)
        #expect(r.state == .allowed)
        #expect(r.reevaluateAt == t0.addingTimeInterval(1.5))
        #expect(state(s, at: 1.49) == .allowed)
        #expect(state(s, at: 1.5) == .working)
    }

    @Test func allowedBeatsDeniedBeatsError() {
        var s = Fixture.connected()
        s.lastAllowedAt = t0
        s.lastDeniedAt = t0
        s.lastErrorAt = t0
        #expect(state(s) == .allowed)
        #expect(state(s, at: 1.6) == .error) // allowed and denied are over, error lasts 3 s
        s.lastAllowedAt = nil
        #expect(state(s) == .denied)
        #expect(state(s, at: 2.9) == .error)
        #expect(state(s, at: 3.0) == .sleeping)
    }

    @Test func deniedFromAnswerAndFromCallLog() {
        var s = Fixture.connected(approvals: [Fixture.approval("a1")])
        s.reduce(.answerAccepted(approvalID: "a1", answer: .deny), now: t0)
        #expect(state(s) == .denied)
        #expect(state(s, at: 1.5) == .sleeping)

        var auto = Fixture.connected()
        auto.reduce(Fixture.msg(.callLogged(Call(decision: CallDecision.timeout))), now: t0)
        #expect(state(auto, at: 1) == .denied)
    }

    @Test func errorForThreeSeconds() {
        var s = Fixture.connected(sessions: [Fixture.session("s1", tainted: true)])
        s.reduce(Fixture.msg(.callLogged(Call(decision: CallDecision.autoAllowed, error: "boom"))), now: t0)
        let r = CharacterResolver.resolve(s, now: t0.addingTimeInterval(1))
        #expect(r.state == .error)
        #expect(r.reevaluateAt == t0.addingTimeInterval(3))
        #expect(state(s, at: 3) == .caution)
    }

    @Test func cautionForTaintedLiveSessionOnly() {
        let s = Fixture.connected(sessions: [Fixture.session("s1", tool: "Bash"), Fixture.session("s2", status: .waitingInput, tainted: true)])
        #expect(state(s) == .caution)
        let ended = Fixture.connected(sessions: [Fixture.session("s1", status: .ended, tainted: true),
                                                 Fixture.session("s2", status: .unknown, tainted: true)])
        #expect(state(ended) == .sleeping)
    }

    @Test func workingStates() {
        let many = Fixture.connected(sessions: [Fixture.session("s1"), Fixture.session("s2", tool: "Edit"),
                                                Fixture.session("s3", status: .waitingInput)])
        #expect(state(many) == .workingMany)
        let one = Fixture.connected(sessions: [Fixture.session("s1", tool: "Bash"), Fixture.session("s2", status: .waitingInput)])
        #expect(state(one) == .working)
        let thinking = Fixture.connected(sessions: [Fixture.session("s1")])
        #expect(state(thinking) == .thinking)
        // waiting_approval is not running.
        let waiting = Fixture.connected(sessions: [Fixture.session("s1", status: .waitingApproval, tool: "Bash")])
        #expect(state(waiting) == .sleeping)
    }

    @Test func greetingForTwoSecondsThenWaitingInput() {
        var s = Fixture.connected()
        s.reduce(Fixture.msg(.sessionUpdated(Fixture.session("s1", status: .waitingInput))), now: t0)
        let r = CharacterResolver.resolve(s, now: t0.addingTimeInterval(0.5))
        #expect(r.state == .greeting)
        #expect(r.reevaluateAt == t0.addingTimeInterval(2))
        #expect(state(s, at: 2) == .waitingInput)
    }

    @Test func runningBeatsGreeting() {
        var s = Fixture.connected()
        s.reduce(Fixture.msg(.sessionUpdated(Fixture.session("s1"))), now: t0)
        #expect(state(s) == .thinking)
    }

    @Test func sleepingWithoutLiveSessions() {
        #expect(state(Fixture.connected()) == .sleeping)
        #expect(state(Fixture.connected(sessions: [Fixture.session("s1", status: .ended)])) == .sleeping)
        #expect(CharacterResolver.resolve(Fixture.connected(), now: t0).reevaluateAt == nil)
    }

    @Test func reevaluateAtIsTheEarliestRunningTimer() {
        var s = Fixture.connected()
        s.lastErrorAt = t0 // ends at +3
        s.lastDeniedAt = t0.addingTimeInterval(1) // ends at +2.5
        #expect(CharacterResolver.resolve(s, now: t0.addingTimeInterval(1)).reevaluateAt == t0.addingTimeInterval(2.5))
    }

    @Test func terminalPromptIsWaitingInput() {
        // PermissionRequest passed through to the terminal: no farero card.
        let s = Fixture.connected(sessions: [Fixture.session("s1", status: .waitingApproval)])
        #expect(CharacterResolver.resolve(s, now: Date()).state == .waitingInput)
    }
}
