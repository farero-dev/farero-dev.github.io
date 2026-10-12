import Foundation
import Testing
@testable import FareroCore

/// Builders for reducer and resolver tests.
enum Fixture {
    static let t0 = Date(timeIntervalSince1970: 1_790_901_697)

    static func session(_ id: String, status: SessionStatus = .running, tool: String = "", tainted: Bool = false,
                        startedAt: Date = t0) -> Session {
        Session(id: id, agent: "claude", agentSessionID: id, cwd: "/tmp/\(id)", status: status,
                currentTool: tool, tainted: tainted, startedAt: startedAt)
    }

    static func approval(_ id: String, session: String = "s1", allowSession: Bool = true) -> Approval {
        Approval(id: id, kind: "agent", sessionID: session, sessionLabel: session, tool: "Bash",
                 input: .object(["command": .string("npm install")]), reasons: ["agent_request"],
                 allowSession: allowSession, createdAt: t0, deadline: t0.addingTimeInterval(600))
    }

    static func msg(_ p: ServerPayload, id: String? = nil) -> AppAction {
        .ipc(.message(IncomingMessage(type: "", id: id, payload: p)))
    }

    static func snapshot(sessions: [Session] = [], approvals: [Approval] = [],
                         plugins: [PluginState] = []) -> AppAction {
        msg(.stateSnapshot(StateSnapshot(version: "dev", sessions: sessions, approvals: approvals, plugins: plugins,
                                         gateway: GatewayInfo(running: true, port: 1, url: "http://127.0.0.1:1/mcp"))),
            id: "hello")
    }

    /// A connected state built from a snapshot.
    static func connected(sessions: [Session] = [], approvals: [Approval] = []) -> AppState {
        var s = AppState()
        s.reduce(.ipc(.connected), now: t0)
        s.reduce(snapshot(sessions: sessions, approvals: approvals), now: t0)
        return s
    }
}

@Suite("App state reducer")
struct AppStateTests {
    let t0 = Fixture.t0

    @Test func connectionLifecycle() {
        var s = AppState()
        #expect(s.connection == .connecting)
        s.reduce(.ipc(.disconnected(reason: "소켓이 없음")), now: t0)
        #expect(s.connection == .disconnected(reason: "소켓이 없음"))
        s.reduce(.ipc(.connected), now: t0)
        #expect(s.connection == .connecting)
        #expect(!s.isConnected)
        s.reduce(Fixture.snapshot(), now: t0)
        #expect(s.isConnected)
        #expect(s.gateway.url == "http://127.0.0.1:1/mcp")
        #expect(s.daemonVersion == "dev")
    }

    @Test func snapshotThenUpdates() {
        var s = Fixture.connected(sessions: [Fixture.session("s1"), Fixture.session("s2", status: .ended)])
        #expect(s.sessions.count == 2)
        #expect(s.activeSessions.map(\.id) == ["s1"])

        s.reduce(Fixture.msg(.sessionUpdated(Fixture.session("s1", tool: "Bash"))), now: t0)
        #expect(s.sessions["s1"]?.currentTool == "Bash")
        #expect(s.sessionAppearedAt["s1"] == nil) // known session: no greeting

        s.reduce(Fixture.msg(.sessionUpdated(Fixture.session("s3", status: .waitingInput,
                                                             startedAt: t0.addingTimeInterval(5)))), now: t0)
        #expect(s.sessionAppearedAt["s3"] == t0)
        #expect(s.activeSessions.map(\.id) == ["s3", "s1"]) // newest first

        s.reduce(Fixture.msg(.sessionRemoved(SessionRef(sessionID: "s3"))), now: t0)
        #expect(s.sessions["s3"] == nil)
        #expect(s.sessionAppearedAt["s3"] == nil)
    }

    @Test func endedSessionComingBackGreets() {
        var s = Fixture.connected(sessions: [Fixture.session("s1", status: .ended)])
        s.reduce(Fixture.msg(.sessionUpdated(Fixture.session("s1", status: .waitingInput))), now: t0)
        #expect(s.sessionAppearedAt["s1"] == t0)
    }

    @Test func newSnapshotReplacesEverything() {
        var s = Fixture.connected(sessions: [Fixture.session("s1")], approvals: [Fixture.approval("a1")])
        s.reduce(.answerSent(approvalID: "a1", answer: .allow), now: t0)
        s.reduce(Fixture.snapshot(sessions: [Fixture.session("s9")], approvals: [Fixture.approval("a9")]), now: t0)
        #expect(Array(s.sessions.keys) == ["s9"])
        #expect(s.approvals.map(\.id) == ["a9"])
        #expect(s.answering.isEmpty)
    }

    @Test func approvalsStackInArrivalOrder() {
        var s = Fixture.connected(approvals: [Fixture.approval("a1")])
        s.reduce(Fixture.msg(.approvalRequest(Fixture.approval("a2"))), now: t0)
        s.reduce(Fixture.msg(.approvalRequest(Fixture.approval("a3"))), now: t0)
        #expect(s.approvals.map(\.id) == ["a1", "a2", "a3"])
        #expect(s.frontApproval?.id == "a1")

        // A repeated request updates in place rather than duplicating.
        var again = Fixture.approval("a2")
        again.tool = "Edit"
        s.reduce(Fixture.msg(.approvalRequest(again)), now: t0)
        #expect(s.approvals.map(\.id) == ["a1", "a2", "a3"])
        #expect(s.approvals[1].tool == "Edit")

        s.reduce(Fixture.msg(.approvalCancelled(ApprovalCancelled(approvalID: "a2", reason: "cancelled"))), now: t0)
        #expect(s.approvals.map(\.id) == ["a1", "a3"])
        s.reduce(Fixture.msg(.approvalCancelled(ApprovalCancelled(approvalID: "nope", reason: "timeout"))), now: t0)
        #expect(s.approvals.map(\.id) == ["a1", "a3"])
        #expect(s.pendingApprovalCount(sessionID: "s1") == 2)
    }

    @Test func answerFlow() {
        var s = Fixture.connected(approvals: [Fixture.approval("a1"), Fixture.approval("a2")])
        s.reduce(.answerSent(approvalID: "a1", answer: .allow), now: t0)
        #expect(s.answering == ["a1": .allow])
        #expect(s.approvals.count == 2) // the card stays until ok

        s.reduce(.answerAccepted(approvalID: "a1", answer: .allow), now: t0)
        #expect(s.approvals.map(\.id) == ["a2"])
        #expect(s.answering.isEmpty)
        #expect(s.lastAllowedAt == t0)

        s.reduce(.answerSent(approvalID: "a2", answer: .deny), now: t0)
        s.reduce(.answerAccepted(approvalID: "a2", answer: .deny), now: t0.addingTimeInterval(1))
        #expect(s.approvals.isEmpty)
        #expect(s.lastDeniedAt == t0.addingTimeInterval(1))
    }

    @Test func rejectedAndFailedAnswers() {
        var s = Fixture.connected(approvals: [Fixture.approval("a1"), Fixture.approval("a2")])
        s.reduce(.answerSent(approvalID: "a1", answer: .allow), now: t0)
        s.reduce(.answerRejected(approvalID: "a1"), now: t0)
        #expect(s.approvals.map(\.id) == ["a2"])
        #expect(s.lastAllowedAt == nil)

        s.reduce(.answerSent(approvalID: "a2", answer: .allowSession), now: t0)
        s.reduce(.answerFailed(approvalID: "a2"), now: t0)
        #expect(s.approvals.map(\.id) == ["a2"])
        #expect(s.answering.isEmpty)

        // Answering a card that is not there is ignored.
        s.reduce(.answerSent(approvalID: "zzz", answer: .deny), now: t0)
        #expect(s.answering.isEmpty)
    }

    @Test func disconnectDropsCardsButKeepsSessions() {
        var s = Fixture.connected(sessions: [Fixture.session("s1")], approvals: [Fixture.approval("a1")])
        s.reduce(.answerSent(approvalID: "a1", answer: .allow), now: t0)
        s.reduce(.ipc(.disconnected(reason: "farerod가 연결을 닫음")), now: t0)
        #expect(s.approvals.isEmpty)
        #expect(s.answering.isEmpty)
        #expect(s.sessions.count == 1)
        #expect(!s.isConnected)
    }

    @Test func callLoggedMarksDeniedAndError() {
        var s = Fixture.connected()
        s.reduce(Fixture.msg(.callLogged(Call(decision: CallDecision.autoAllowed))), now: t0)
        #expect(s.lastDeniedAt == nil && s.lastErrorAt == nil)
        for (i, d) in [CallDecision.denied, CallDecision.autoDenied, CallDecision.timeout].enumerated() {
            let at = t0.addingTimeInterval(Double(i))
            s.reduce(Fixture.msg(.callLogged(Call(decision: d))), now: at)
            #expect(s.lastDeniedAt == at)
        }
        s.reduce(Fixture.msg(.callLogged(Call(decision: CallDecision.userAllowed, error: "upstream returned an error"))),
                 now: t0.addingTimeInterval(9))
        #expect(s.lastErrorAt == t0.addingTimeInterval(9))
    }

    @Test func pluginsAndPrompts() {
        var s = AppState()
        s.reduce(Fixture.snapshot(plugins: [PluginState(plugin: "github"), PluginState(plugin: "railway")]), now: t0)
        s.reduce(Fixture.msg(.pluginPrompt(PluginPrompt(plugin: "github", userCode: "AB-12", url: "https://x"))), now: t0)
        #expect(s.pluginPrompts["github"]?.userCode == "AB-12")
        s.reduce(Fixture.msg(.pluginUpdated(PluginState(plugin: "github", status: "connected", accountLabel: "me"))), now: t0)
        #expect(s.plugins.map(\.status) == ["connected", "disconnected"])
        #expect(s.pluginPrompts["github"] == nil)
        s.reduce(Fixture.msg(.pluginUpdated(PluginState(plugin: "gmail"))), now: t0)
        #expect(s.plugins.map(\.plugin) == ["github", "railway", "gmail"])
    }

    @Test func agentCfgPushAndRepliesUpdateStatus() throws {
        var s = Fixture.connected()
        #expect(s.agentCfg == nil && s.agentCfgAlerts.isEmpty)

        // The unsolicited push farerod sends after it fixed paths (Q63): no id.
        let push = try IncomingMessage.decode(Data(#"{"type":"agentcfg.status","data":{"agent":"claude","cli_found":true,"cli_path":"/opt/homebrew/bin/claude","version":"2.1.287","min_version":"2.1.203","version_ok":true,"settings_path":"/Users/me/.claude/settings.json","hooks_installed":true,"allow_installed":true,"mcp_installed":true,"hook_path":"/Applications/Farero.app/Contents/MacOS/farero-hook","stale_path":false,"gateway_url":"http://127.0.0.1:61511/mcp","message":"farero 앱 위치가 바뀌어 Claude Code 설정의 경로를 새 위치로 고쳤습니다: /Applications/Farero.app/Contents/MacOS/farero-hook (이전 settings.json 백업: /x/backups/claude-settings-20261006-101500.000.json)"}}"#.utf8))
        #expect(push.id == nil)
        s.reduce(.ipc(.message(push)), now: t0)
        #expect(s.agentCfg?.hookPath == "/Applications/Farero.app/Contents/MacOS/farero-hook")
        #expect(s.agentCfg?.isFullyInstalled == true)
        #expect(s.agentCfgAlerts.map(\.kind) == [.notice])
        #expect(s.agentCfgAlerts.first?.detail.contains("claude-settings-20261006-101500.000.json") == true)

        // Seen in 설정 > Claude Code: the menu stops repeating it, also when a
        // later reply carries the same message (farerod keeps it in memory).
        s.reduce(.agentCfgNoticeSeen, now: t0)
        #expect(s.agentCfgAlerts.isEmpty)
        guard case .agentCfgStatus(let st) = push.payload else { Issue.record("wrong payload"); return }
        s.reduce(Fixture.msg(.agentCfgStatus(st), id: "r3"), now: t0)
        #expect(s.agentCfgAlerts.isEmpty)

        // A new push is a new event, even with the same text.
        s.reduce(.ipc(.message(push)), now: t0)
        #expect(s.agentCfgAlerts.map(\.kind) == [.notice])

        // A reply replaces the status; reconnecting and a new snapshot keep it.
        var old = st
        old.message = ""
        old.version = "2.1.150"
        old.versionOK = false
        s.reduce(Fixture.msg(.agentCfgStatus(old), id: "r4"), now: t0)
        #expect(s.agentCfg == old)
        s.reduce(.ipc(.disconnected(reason: "farerod가 연결을 닫음")), now: t0)
        s.reduce(.ipc(.connected), now: t0)
        s.reduce(Fixture.snapshot(), now: t0)
        #expect(s.agentCfgAlerts.map(\.kind) == [.outdatedCLI])
    }

    @Test func repliesAndUnknownMessagesChangeNothing() {
        let before = Fixture.connected(sessions: [Fixture.session("s1")])
        var s = before
        s.reduce(Fixture.msg(.ok, id: "r1"), now: t0)
        s.reduce(Fixture.msg(.unknown(type: "future.thing")), now: t0)
        s.reduce(Fixture.msg(.error(ErrorData(message: "x")), id: "r2"), now: t0)
        #expect(s == before)
    }
}
