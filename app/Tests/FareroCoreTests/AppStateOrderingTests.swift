import Foundation
import Testing
@testable import FareroCore

@Suite("App state ordering and expiry")
struct AppStateOrderingTests {
    let t0 = Fixture.t0

    @Test func runningSessionCountOnlyCountsRunning() {
        let s = Fixture.connected(sessions: [
            Fixture.session("a", status: .running),
            Fixture.session("b", status: .running),
            Fixture.session("c", status: .waitingInput),
            Fixture.session("d", status: .waitingApproval),
            Fixture.session("e", status: .ended),
        ])
        #expect(s.runningSessionCount == 2)
    }

    @Test func sessionsStartedTogetherAreOrderedById() {
        let s = Fixture.connected(sessions: [Fixture.session("b"), Fixture.session("c"), Fixture.session("a")])
        #expect(s.activeSessions.map(\.id) == ["a", "b", "c"])
    }

    @Test func greetingTimesExpireOnAnyLaterAction() {
        var s = Fixture.connected()
        s.reduce(Fixture.msg(.sessionUpdated(Fixture.session("s1", status: .waitingInput))), now: t0)
        #expect(s.sessionAppearedAt["s1"] == t0)

        // Any action prunes; this one changes nothing else.
        s.reduce(.agentCfgNoticeSeen, now: t0.addingTimeInterval(59))
        #expect(s.sessionAppearedAt["s1"] == t0)
        s.reduce(.agentCfgNoticeSeen, now: t0.addingTimeInterval(61))
        #expect(s.sessionAppearedAt["s1"] == nil)
    }
}
