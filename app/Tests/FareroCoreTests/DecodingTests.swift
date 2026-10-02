import Foundation
import Testing
@testable import FareroCore

/// Sample lines shaped like docs/ipc.md and what Go's encoding/json emits.
enum Samples {
    static let session = #"""
    {"id":"claude:6fc2350b","agent":"claude","agent_session_id":"6fc2350b","cwd":"/Users/me/proj","tty":"ttys019","pid":25145,"status":"running","current_tool":"Bash","tainted":false,"started_at":"2026-10-02T09:41:37.397123+09:00","last_event_at":"2026-10-02T09:41:51+09:00"}
    """#
    static let approval = #"""
    {"id":"9f2c","kind":"plugin","session_id":"claude:6fc2350b","session_label":"proj","agent":"claude","plugin":"github","tool":"issue_write","input":{"title":"버그","labels":["a","b"],"n":3,"draft":false,"meta":null},"reasons":["policy","tainted"],"allow_session":true,"created_at":"2026-10-02T09:42:00.123456789+09:00","deadline":"2026-10-02T09:52:00.123456789+09:00"}
    """#
    static let call = #"""
    {"id":42,"session_id":"claude:6fc2350b","conn_id":"a1b2","ts":"2026-10-02T00:42:05Z","kind":"plugin","agent":"claude","plugin":"github","tool":"issue_write","input":{"title":"x"},"result_text":"잘린 결과","result_bytes":20480,"decision":"user_allowed","reason":"allow_session","duration_ms":5321,"error":""}
    """#
    static let plugin = #"""
    {"plugin":"github","status":"connected","account_label":"octocat","connected_at":"2026-10-01T12:00:00Z","options":{"read_only":"false"}}
    """#

    static var snapshot: String {
        #"{"type":"state.snapshot","id":"hello","data":{"version":"0.1.0","sessions":["# + session + #"],"approvals":["# + approval + #"],"plugins":["# + plugin + #",{"plugin":"railway","status":"disconnected","account_label":""}],"gateway":{"running":true,"port":61511,"url":"http://127.0.0.1:61511/mcp"}}}"#
    }

    static func line(_ s: String) -> Data { Data(s.utf8) }
}

@Suite("Decoding server messages")
struct DecodingTests {
    func decode(_ s: String) throws -> IncomingMessage {
        try IncomingMessage.decode(Samples.line(s))
    }

    @Test func snapshot() throws {
        let m = try decode(Samples.snapshot)
        #expect(m.type == "state.snapshot")
        #expect(m.id == "hello")
        guard case .stateSnapshot(let snap) = m.payload else { Issue.record("wrong payload \(m.payload)"); return }
        #expect(snap.version == "0.1.0")
        #expect(snap.sessions.count == 1)
        #expect(snap.approvals.count == 1)
        #expect(snap.plugins.map(\.plugin) == ["github", "railway"])
        #expect(snap.plugins[0].options == ["read_only": "false"])
        #expect(snap.plugins[0].connectedAt != nil)
        #expect(snap.plugins[1].connectedAt == nil)
        #expect(snap.gateway == GatewayInfo(running: true, port: 61511, url: "http://127.0.0.1:61511/mcp"))
    }

    @Test func snapshotWithNullSlicesAndGatewayError() throws {
        let m = try decode(#"{"type":"state.snapshot","id":"hello","data":{"version":"dev","sessions":[],"approvals":[],"plugins":null,"gateway":{"running":false,"port":61511,"url":"","error":"address in use"}}}"#)
        guard case .stateSnapshot(let snap) = m.payload else { Issue.record("wrong payload"); return }
        #expect(snap.plugins.isEmpty)
        #expect(snap.gateway.error == "address in use")
        #expect(!snap.gateway.running)
    }

    @Test func sessionUpdated() throws {
        let m = try decode(#"{"type":"session.updated","data":"# + Samples.session + "}")
        #expect(m.id == nil)
        guard case .sessionUpdated(let s) = m.payload else { Issue.record("wrong payload"); return }
        #expect(s.id == "claude:6fc2350b")
        #expect(s.agent == "claude")
        #expect(s.agentSessionID == "6fc2350b")
        #expect(s.cwd == "/Users/me/proj")
        #expect(s.tty == "ttys019")
        #expect(s.pid == 25145)
        #expect(s.status == .running)
        #expect(s.currentTool == "Bash")
        #expect(!s.tainted)
        // 09:41:37.397123 +09:00 == 00:41:37.397123 UTC
        let expected = GoTime.parse("2026-10-02T00:41:37.397123Z")!
        #expect(abs(s.startedAt!.timeIntervalSince(expected)) < 1e-6)
        #expect(s.lastEventAt != nil)
    }

    @Test func sessionWithUnknownStatusAndZeroTime() throws {
        let m = try decode(#"{"type":"session.updated","data":{"id":"claude:x","status":"hibernating","started_at":"0001-01-01T00:00:00Z","extra_field":{"nested":1}}}"#)
        guard case .sessionUpdated(let s) = m.payload else { Issue.record("wrong payload"); return }
        #expect(s.status == .unknown)
        #expect(s.startedAt == nil)
        #expect(s.cwd == "")
    }

    @Test func sessionRemoved() throws {
        let m = try decode(#"{"type":"session.removed","data":{"session_id":"claude:x"}}"#)
        #expect(m.payload == .sessionRemoved(SessionRef(sessionID: "claude:x")))
    }

    @Test func approvalRequest() throws {
        let m = try decode(#"{"type":"approval.request","data":"# + Samples.approval + "}")
        guard case .approvalRequest(let a) = m.payload else { Issue.record("wrong payload"); return }
        #expect(a.id == "9f2c")
        #expect(a.kind == "plugin")
        #expect(a.isPlugin)
        #expect(a.sessionID == "claude:6fc2350b")
        #expect(a.sessionLabel == "proj")
        #expect(a.plugin == "github")
        #expect(a.tool == "issue_write")
        #expect(a.reasons == ["policy", "tainted"])
        #expect(a.allowSession)
        #expect(a.input["title"] == .string("버그"))
        #expect(a.input["labels"] == .array([.string("a"), .string("b")]))
        #expect(a.input["n"] == .int(3))
        #expect(a.input["draft"] == .bool(false))
        #expect(a.input["meta"] == .null)
        let created = try #require(a.createdAt)
        let deadline = try #require(a.deadline)
        #expect(abs(deadline.timeIntervalSince(created) - 600) < 1e-6)
    }

    @Test func agentApprovalForUnknownSession() throws {
        let m = try decode(#"{"type":"approval.request","data":{"id":"a1","kind":"agent","session_id":"","session_label":"세션 불명","agent":"claude","plugin":"","tool":"Bash","input":{"command":"npm install"},"reasons":["agent_request"],"allow_session":false,"created_at":"2026-10-02T09:42:00+09:00","deadline":"2026-10-02T09:52:00+09:00"}}"#)
        guard case .approvalRequest(let a) = m.payload else { Issue.record("wrong payload"); return }
        #expect(!a.isPlugin)
        #expect(a.sessionLabel == "세션 불명")
        #expect(!a.allowSession)
        #expect(a.input["command"]?.stringValue == "npm install")
    }

    @Test func approvalCancelled() throws {
        let m = try decode(#"{"type":"approval.cancelled","data":{"approval_id":"9f2c","reason":"timeout"}}"#)
        #expect(m.payload == .approvalCancelled(ApprovalCancelled(approvalID: "9f2c", reason: "timeout")))
    }

    @Test func callLogged() throws {
        let m = try decode(#"{"type":"call.logged","data":"# + Samples.call + "}")
        guard case .callLogged(let c) = m.payload else { Issue.record("wrong payload"); return }
        #expect(c.id == 42)
        #expect(c.connID == "a1b2")
        #expect(c.resultText == "잘린 결과")
        #expect(c.resultBytes == 20480)
        #expect(c.decision == CallDecision.userAllowed)
        #expect(c.reason == "allow_session")
        #expect(c.durationMS == 5321)
        #expect(c.error == "")
        #expect(c.ts == GoTime.parse("2026-10-02T00:42:05Z"))
    }

    @Test func pluginUpdatedAndPrompt() throws {
        let u = try decode(#"{"type":"plugin.updated","data":"# + Samples.plugin + "}")
        guard case .pluginUpdated(let p) = u.payload else { Issue.record("wrong payload"); return }
        #expect(p.accountLabel == "octocat")
        let pr = try decode(#"{"type":"plugin.prompt","data":{"plugin":"github","user_code":"ABCD-1234","url":"https://github.com/login/device","expires_at":"2026-10-02T10:00:00+09:00"}}"#)
        guard case .pluginPrompt(let prompt) = pr.payload else { Issue.record("wrong payload"); return }
        #expect(prompt.userCode == "ABCD-1234")
        #expect(prompt.url == "https://github.com/login/device")
        #expect(prompt.expiresAt != nil)
        let browser = try decode(#"{"type":"plugin.prompt","data":{"plugin":"railway","url":"https://railway.com/oauth"}}"#)
        guard case .pluginPrompt(let b) = browser.payload else { Issue.record("wrong payload"); return }
        #expect(b.userCode == "")
        #expect(b.expiresAt == nil)
    }

    @Test func removedGatewayStatusIsJustUnknown() throws {
        let m = try decode(#"{"type":"gateway.status","data":{"running":true,"port":1,"url":"u"}}"#)
        #expect(m.payload == .unknown(type: "gateway.status"))
    }

    @Test func okAndErrorReplies() throws {
        #expect(try decode(#"{"type":"ok","id":"r1"}"#) == IncomingMessage(type: "ok", id: "r1", payload: .ok))
        let e = try decode(#"{"type":"error","id":"r2","data":{"message":"approval not pending"}}"#)
        #expect(e.payload == .error(ErrorData(message: "approval not pending")))
        #expect(e.id == "r2")
    }

    @Test func requestReplies() throws {
        let log = try decode(#"{"type":"log.result","id":"r3","data":["# + Samples.call + "]}")
        guard case .logResult(let calls) = log.payload else { Issue.record("wrong payload"); return }
        #expect(calls.count == 1)

        let pol = try decode(#"{"type":"policy.state","id":"r4","data":[{"plugin":"github","tool":"issue_write","level":"ask","destructive":true,"default_level":"ask","overridden":false},{"plugin":"gmail","tool":"read","level":"auto","taint":true,"read_only":true,"default_level":"auto","overridden":false}]}"#)
        guard case .policyState(let tools) = pol.payload else { Issue.record("wrong payload"); return }
        #expect(tools.count == 2)
        #expect(tools[0].destructive && !tools[0].noSession)
        #expect(tools[1].taint)

        let list = try decode(#"{"type":"plugin.list","id":"r5","data":["# + Samples.plugin + "]}")
        guard case .pluginList(let plugins) = list.payload else { Issue.record("wrong payload"); return }
        #expect(plugins.count == 1)

        let settings = try decode(#"{"type":"settings.get","id":"r6","data":{"result_limit_bytes":16384,"update_check":true}}"#)
        #expect(settings.payload == .settings(DaemonSettings(resultLimitBytes: 16384, updateCheck: true)))

        let st = try decode(#"{"type":"agentcfg.status","id":"r7","data":{"agent":"claude","cli_found":true,"cli_path":"/Users/me/.local/bin/claude","version":"2.1.287","min_version":"2.1.203","version_ok":true,"settings_path":"/Users/me/.claude/settings.json","hooks_installed":true,"allow_installed":true,"mcp_installed":false,"hook_path":"/Applications/Farero.app/Contents/MacOS/farero-hook","stale_path":false,"gateway_url":"http://127.0.0.1:61511/mcp"}}"#)
        guard case .agentCfgStatus(let status) = st.payload else { Issue.record("wrong payload"); return }
        #expect(status.versionOK && status.cliFound)
        #expect(!status.isFullyInstalled)
        #expect(status.backupPath == "")

        let plan = try decode(#"{"type":"agentcfg.plan","id":"r8","data":{"agent":"claude","changes":[{"path":"/x/settings.json","before":"{}","after":"{\"hooks\":{}}"}],"commands":["claude mcp add-json farero ..."]}}"#)
        guard case .agentCfgPlan(let p) = plan.payload else { Issue.record("wrong payload"); return }
        #expect(p.changes.first?.after == #"{"hooks":{}}"#)
        #expect(p.commands.count == 1)
    }

    @Test func unknownTypeIsTolerated() throws {
        let m = try decode(#"{"type":"future.thing","data":{"x":1},"extra":true}"#)
        #expect(m.payload == .unknown(type: "future.thing"))
    }

    @Test func missingTypeFails() {
        #expect(throws: (any Error).self) { try decode(#"{"data":{}}"#) }
    }
}

@Suite("Go time parsing")
struct GoTimeTests {
    @Test(arguments: [
        ("2026-10-02T00:41:37Z", 0.0),
        ("2026-10-02T09:41:37+09:00", 0.0),
        ("2026-10-01T19:41:37-05:00", 0.0),
        ("2026-10-02T00:41:37.5Z", 0.5),
        ("2026-10-02T09:41:37.397123+09:00", 0.397123),
        ("2026-10-02T09:41:37.123456789+09:00", 0.123456789),
    ])
    func parses(_ s: String, _ fraction: Double) throws {
        let d = try #require(GoTime.parse(s))
        // 2026-10-02T00:41:37Z
        #expect(abs(d.timeIntervalSince1970 - (1_790_901_697 + fraction)) < 1e-6)
    }

    @Test func rejectsGarbageAndZeroTime() {
        #expect(GoTime.parse("") == nil)
        #expect(GoTime.parse("yesterday") == nil)
        #expect(GoTime.parse("2026-10-02T00:41:37") == nil) // no zone
        #expect(GoTime.parse("2026-10-02T00:41:37.Z") == nil)
        #expect(GoTime.parse("2026-13-02T00:41:37Z") == nil)
        #expect(GoTime.parse("0001-01-01T00:00:00Z") == nil)
    }

    @Test func formatRoundTrips() throws {
        let d = Date(timeIntervalSince1970: 1_790_901_697.25)
        let s = GoTime.format(d)
        #expect(s == "2026-10-02T00:41:37.250Z")
        #expect(GoTime.parse(s) == d)
    }
}

@Suite("JSONValue")
struct JSONValueTests {
    @Test func prettyPrintsSortedAndUnescapedKorean() throws {
        let v = try JSONDecoder().decode(JSONValue.self, from: Data(#"{"b":[1,2.5,"줄\n바꿈"],"a":{"x":null,"y":true},"c":{},"d":[]}"#.utf8))
        #expect(v.prettyPrinted() == """
        {
          "a": {
            "x": null,
            "y": true
          },
          "b": [
            1,
            2.5,
            "줄\\n바꿈"
          ],
          "c": {},
          "d": []
        }
        """)
        #expect(v.compact() == #"{"a":{"x":null,"y":true},"b":[1,2.5,"줄\n바꿈"],"c":{},"d":[]}"#)
    }

    @Test func escapesControlCharactersAndQuotes() {
        #expect(JSONValue.string("a\"b\\c\u{01}").compact() == #""a\"b\\c\u0001""#)
    }

    @Test func roundTripsThroughCodable() throws {
        let v: JSONValue = .object(["k": .array([.int(-3), .double(0.25), .bool(true), .null, .string("s")])])
        let data = try JSONEncoder().encode(v)
        #expect(try JSONDecoder().decode(JSONValue.self, from: data) == v)
    }

    @Test func largeIntegersKeepPrecision() throws {
        let v = try JSONDecoder().decode(JSONValue.self, from: Data("9007199254740993".utf8))
        #expect(v == .int(9_007_199_254_740_993))
    }
}

@Suite("Encoding requests")
struct EncodingTests {
    func object(_ data: Data) throws -> [String: Any] {
        #expect(data.last == 0x0A)
        return try JSONSerialization.jsonObject(with: data.dropLast()) as! [String: Any]
    }

    @Test func helloLine() throws {
        let o = try object(OutgoingMessage.line(type: MessageType.uiHello, id: "hello", data: UIHello(version: "0.1")))
        #expect(o["type"] as? String == "ui.hello")
        #expect(o["id"] as? String == "hello")
        #expect((o["data"] as? [String: Any])?["version"] as? String == "0.1")
    }

    @Test func approvalResponseLine() throws {
        let o = try object(OutgoingMessage.line(type: MessageType.approvalResponse, id: "r1",
                                                data: ApprovalResponse(approvalID: "9f2c", answer: .allowSession)))
        let data = try #require(o["data"] as? [String: String])
        #expect(data == ["approval_id": "9f2c", "answer": "allow_session"])
    }

    @Test func requestWithoutData() throws {
        let o = try object(OutgoingMessage.line(type: MessageType.policyGet, id: "r2"))
        #expect(o["data"] == nil)
        #expect(o.count == 2)
    }

    @Test func logQueryOmitsEmptyFields() throws {
        var q = LogQuery(query: "배포", limit: 20)
        q.from = Date(timeIntervalSince1970: 1_790_901_697)
        let o = try object(OutgoingMessage.line(type: MessageType.logQuery, id: "r3", data: q))
        let data = try #require(o["data"] as? [String: Any])
        #expect(Set(data.keys) == ["query", "limit", "from"])
        #expect(data["from"] as? String == "2026-10-02T00:41:37.000Z")
    }
}
