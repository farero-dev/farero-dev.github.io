import Foundation
import Testing
@testable import FareroCore

@Suite("Log filter")
struct LogFilterTests {
    var seoul: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "Asia/Seoul")!
        return c
    }

    func encoded(_ q: LogQuery) throws -> [String: Any] {
        let line = try OutgoingMessage.line(type: MessageType.logQuery, id: "r", data: q)
        let o = try JSONSerialization.jsonObject(with: line.dropLast()) as! [String: Any]
        return o["data"] as! [String: Any]
    }

    @Test func emptyFilterSendsOnlyLimit() throws {
        let q = LogFilter().logQuery(now: Fixture.t0, calendar: seoul, limit: 200)
        #expect(try encoded(q).keys.sorted() == ["limit"])
    }

    @Test func allFieldsEncode() throws {
        var f = LogFilter()
        f.query = "  배포  "
        f.sessionID = "claude:s1"
        f.agent = "claude"
        f.plugin = "github"
        f.kind = "plugin"
        f.tool = " issue_write "
        f.decision = CallDecision.denied
        f.period = .today
        let d = try encoded(f.logQuery(now: Fixture.t0, calendar: seoul, limit: 50, offset: 100))
        #expect(d["query"] as? String == "배포")
        #expect(d["session_id"] as? String == "claude:s1")
        #expect(d["agent"] as? String == "claude")
        #expect(d["plugin"] as? String == "github")
        #expect(d["kind"] as? String == "plugin")
        #expect(d["tool"] as? String == "issue_write")
        #expect(d["decision"] as? String == "denied")
        #expect(d["limit"] as? Int == 50)
        #expect(d["offset"] as? Int == 100)
        // Fixture.t0 is 2026-10-02 09:41 KST; today starts at 00:00 KST = 10-01 15:00 UTC.
        #expect(d["from"] as? String == "2026-10-01T15:00:00.000Z")
        #expect(d["to"] == nil)
    }

    @Test func periods() {
        var f = LogFilter()
        f.period = .week
        #expect(f.logQuery(now: Fixture.t0, limit: 1).from == Fixture.t0.addingTimeInterval(-7 * 86_400))
        f.period = .month
        #expect(f.logQuery(now: Fixture.t0, limit: 1).from == Fixture.t0.addingTimeInterval(-30 * 86_400))
        // A custom range covers whole days, in either order.
        f.period = .custom
        f.customFrom = Fixture.t0.addingTimeInterval(86_400) // 10-03
        f.customTo = Fixture.t0.addingTimeInterval(-86_400) // 10-01
        let q = f.logQuery(now: Fixture.t0, calendar: seoul, limit: 1)
        #expect(q.from.map(GoTime.format) == "2026-09-30T15:00:00.000Z") // 10-01 00:00 KST
        #expect(q.to.map(GoTime.format) == "2026-10-03T15:00:00.000Z") // 10-04 00:00 KST
    }

    @Test func searchModeFollowsLength() {
        #expect(LogFilter.searchModeLabel("") == nil)
        #expect(LogFilter.searchModeLabel(" 배포 ") == "부분 일치") // two Korean syllables
        #expect(LogFilter.searchModeLabel("ab") == "부분 일치")
        #expect(LogFilter.searchModeLabel("이슈 작성") == "전문 검색")
        #expect(LogFilter.searchModeLabel("npm") == "전문 검색")
    }
}

@Suite("Call presentation")
struct CallPresentationTests {
    @Test func truncationNote() {
        #expect(CallPresentation.truncationNote(Call(resultText: "abc", resultBytes: 3)) == nil)
        #expect(CallPresentation.truncationNote(Call(resultText: "결과", resultBytes: 20480)) == "잘림: 20480 bytes 중 6 bytes 저장")
    }

    @Test func durationsAndReasons() {
        #expect(CallPresentation.duration(850) == "850ms")
        #expect(CallPresentation.duration(5321) == "5.3초")
        #expect(CallPresentation.duration(125_000) == "2분 5초")
        #expect(CallPresentation.reasonText("") == "")
        #expect(CallPresentation.reasonText("policy,tainted") == "분류표, 오염된 세션")
        #expect(CallPresentation.reasonText("app_not_running") == "앱이 실행 중이 아님")
        #expect(CallPresentation.reasonText("answered_in_agent") == "터미널(또는 다른 훅)에서 먼저 답함")
        #expect(CallPresentation.reasonText("future_code") == "future_code")
        #expect(CallPresentation.reasonText("allow_session") == "이번 세션 동안 허용")
        #expect(CallPresentation.toolTitle(Call(plugin: "github", tool: "issue_write")) == "github · issue_write")
        #expect(CallPresentation.toolTitle(Call(plugin: "", tool: "Bash")) == "Bash")
    }
}

@Suite("Policy presentation")
struct PolicyPresentationTests {
    @Test func groupsInPluginOrder() {
        let tools = [
            PolicyTool(plugin: "gmail", tool: "search", level: "auto", taint: true),
            PolicyTool(plugin: "zeta", tool: "x", level: "ask"),
            PolicyTool(plugin: "github", tool: "issue_write", level: "ask"),
            PolicyTool(plugin: "github", tool: "get_me", level: "auto"),
            PolicyTool(plugin: "dev", tool: "echo", level: "auto"),
            PolicyTool(plugin: "alpha", tool: "y", level: "ask"),
        ]
        let groups = PolicyPresentation.grouped(tools)
        #expect(groups.map(\.plugin) == ["github", "gmail", "dev", "alpha", "zeta"])
        #expect(groups[0].tools.map(\.tool) == ["get_me", "issue_write"])
    }

    @Test func noSessionToolsCannotBeAutoAllowed() {
        let normal = PolicyTool(plugin: "github", tool: "issue_write", level: "ask")
        let always = PolicyTool(plugin: "railway", tool: "service_delete", level: "ask", noSession: true, destructive: true)
        #expect(PolicyPresentation.levelOptions(normal) == ["auto", "ask", "block"])
        #expect(PolicyPresentation.levelOptions(always) == ["ask", "block"])
        #expect(PolicyPresentation.levelLabel("ask", noSession: true) == "매번 승인")
        #expect(PolicyPresentation.levelLabel("ask") == "승인")
        #expect(PolicyPresentation.levelLabel("auto") == "자동 허용")
        #expect(PolicyPresentation.levelLabel("block") == "차단")
        #expect(PolicyPresentation.pluginTitle("github") == "GitHub")
    }
}

@Suite("Plugin presentation")
struct PluginPresentationTests {
    @Test func statusAndActions() {
        #expect(PluginPresentation.statusLabel("connected") == "연결됨")
        #expect(PluginPresentation.statusLabel("expired") == "토큰 만료")
        #expect(PluginPresentation.statusLabel("error") == "오류")
        #expect(PluginPresentation.statusLabel("disconnected") == "연결 안 됨")
        #expect(PluginPresentation.actions("disconnected") == [.connect])
        #expect(PluginPresentation.actions("expired") == [.reconnect, .disconnect])
        #expect(PluginPresentation.actions("connected") == [.reconnect, .disconnect])
        #expect(PluginPresentation.scopes("github") == "repo, read:org")
        #expect(PluginPresentation.scopes("gmail")?.hasPrefix("gmail.readonly") == true)
        #expect(PluginPresentation.usesDeviceCode("github"))
        // Railway refuses the device flow for DCR clients (M0), so it signs
        // in through the browser like Resend.
        #expect(!PluginPresentation.usesDeviceCode("railway"))
        #expect(!PluginPresentation.usesDeviceCode("resend"))
    }
}

@Suite("Settings payload decoding")
struct SettingsPayloadDecodingTests {
    @Test func policyStateAsFarerodSendsIt() throws {
        // Effective embeds Rule, so its fields sit at the top level; false
        // flags are omitted and read_only is a pointer.
        let line = #"{"type":"policy.state","id":"r1","data":[{"plugin":"github","tool":"get_me","level":"auto","read_only":true,"default_level":"auto","overridden":false},{"plugin":"railway","tool":"service_delete","level":"ask","no_session":true,"destructive":true,"default_level":"ask","overridden":false},{"plugin":"gmail","tool":"search","level":"block","taint":true,"default_level":"auto","overridden":true}]}"#
        let m = try IncomingMessage.decode(Data(line.utf8))
        guard case .policyState(let tools) = m.payload else { Issue.record("wrong payload"); return }
        #expect(tools.count == 3)
        #expect(tools[0] == PolicyTool(plugin: "github", tool: "get_me", level: "auto", defaultLevel: "auto"))
        #expect(tools[1].noSession && tools[1].destructive && !tools[1].taint)
        #expect(tools[2].taint && tools[2].overridden && tools[2].defaultLevel == "auto" && tools[2].level == "block")
    }

    @Test func agentCfgStatusWithNoticeAndBackup() throws {
        let line = #"{"type":"agentcfg.status","id":"r2","data":{"agent":"claude","cli_found":true,"cli_path":"/opt/homebrew/bin/claude","version":"2.1.150","min_version":"2.1.203","version_ok":false,"settings_path":"/tmp/c/settings.json","hooks_installed":true,"allow_installed":true,"mcp_installed":true,"hook_path":"/Applications/Farero.app/Contents/MacOS/farero-hook","stale_path":true,"gateway_url":"http://127.0.0.1:61511/mcp","backup_path":"/x/backups/claude-settings-20261002-104000.000.json","message":"farero 앱 위치가 바뀌어 Claude Code 설정의 경로를 새 위치로 고쳤습니다: /Applications/Farero.app/Contents/MacOS/farero-hook"}}"#
        let m = try IncomingMessage.decode(Data(line.utf8))
        guard case .agentCfgStatus(let s) = m.payload else { Issue.record("wrong payload"); return }
        #expect(!s.versionOK && s.version == "2.1.150" && s.minVersion == "2.1.203")
        #expect(s.stalePath && !s.isFullyInstalled)
        #expect(s.backupPath.hasSuffix(".json"))
        #expect(s.message.hasPrefix("farero 앱 위치가"))
    }

    @Test func agentCfgPlanCarriesFileTexts() throws {
        let before = "{\n  \"model\": \"opus\"\n}\n"
        let after = "{\n  \"model\": \"opus\",\n  \"permissions\": {\n    \"allow\": [\n      \"mcp__farero__*\"\n    ]\n  }\n}\n"
        let payload: [String: Any] = ["type": "agentcfg.plan", "id": "r3", "data": [
            "agent": "claude",
            "changes": [["path": "/tmp/c/settings.json", "before": before, "after": after]],
            "commands": ["claude mcp remove farero --scope user", "claude mcp add-json farero '{\"type\":\"http\"}' --scope user"],
        ]]
        let m = try IncomingMessage.decode(JSONSerialization.data(withJSONObject: payload))
        guard case .agentCfgPlan(let p) = m.payload else { Issue.record("wrong payload"); return }
        #expect(p.changes.count == 1)
        #expect(p.changes[0].before == before && p.changes[0].after == after)
        #expect(p.commands.count == 2)
        let rows = LineDiff.rows(before: p.changes[0].before, after: p.changes[0].after)
        #expect(rows.filter { $0.kind == .added }.count == 6)
        #expect(rows.filter { $0.kind == .removed }.map(\.text) == [#"  "model": "opus""#])
    }
}

@Suite("Claude Code alerts")
struct AgentCfgAlertTests {
    let notice = "farero 앱 위치가 바뀌어 Claude Code 설정의 경로를 새 위치로 고쳤습니다: /Applications/Farero.app/Contents/MacOS/farero-hook (이전 settings.json 백업: /x/backups/claude-settings-20261006-101500.000.json)"

    func status(cliFound: Bool = true, version: String = "2.1.287", versionOK: Bool = true,
                message: String = "") -> AgentCfgStatus {
        AgentCfgStatus(cliFound: cliFound, cliPath: cliFound ? "/opt/homebrew/bin/claude" : "", version: version,
                       minVersion: "2.1.203", versionOK: versionOK, hooksInstalled: true, allowInstalled: true,
                       mcpInstalled: true, message: message)
    }

    @Test func nothingToSay() {
        #expect(AgentCfgAlert.alerts(for: nil).isEmpty)
        #expect(AgentCfgAlert.alerts(for: status()).isEmpty)
        #expect(AgentCfgAlert.alerts(for: status(message: "  \n")).isEmpty)
    }

    @Test func pathFixNotice() {
        let alerts = AgentCfgAlert.alerts(for: status(message: notice))
        #expect(alerts == [AgentCfgAlert(kind: .notice,
                                         title: "farero 앱 위치가 바뀌어 Claude Code 설정의 경로를 새 위치로 고쳤습니다",
                                         detail: notice)])
        // Seen in 설정 > Claude Code.
        #expect(AgentCfgAlert.alerts(for: status(message: notice), seenMessage: notice).isEmpty)
        // A notice without ": " is cut to fit the menu.
        let long = String(repeating: "가", count: 80)
        #expect(AgentCfgAlert.alerts(for: status(message: long)).first?.title == String(repeating: "가", count: 59) + "…")
    }

    @Test func outdatedClaudeCode() {
        let alerts = AgentCfgAlert.alerts(for: status(version: "2.1.150", versionOK: false))
        #expect(alerts.count == 1)
        #expect(alerts.first?.kind == .outdatedCLI)
        #expect(alerts.first?.title == "Claude Code 2.1.150 → 2.1.203 이상으로 업데이트 필요 (claude update)")
        #expect(alerts.first?.detail == "설치된 Claude Code 2.1.150은(는) farero가 지원하는 최소 버전 2.1.203보다 낮습니다. 터미널에서 claude update로 업데이트하세요.")

        // `claude --version` failed: the version is unknown.
        let unknown = AgentCfgAlert.alerts(for: status(version: "", versionOK: false))
        #expect(unknown.first?.title == "Claude Code 버전을 확인하지 못함 (2.1.203 이상 필요)")
    }

    @Test func bothOutdatedFirst() {
        let alerts = AgentCfgAlert.alerts(for: status(version: "2.1.150", versionOK: false, message: notice))
        #expect(alerts.map(\.kind) == [.outdatedCLI, .notice])
        // Seeing the notice does not dismiss the version warning.
        #expect(AgentCfgAlert.alerts(for: status(version: "2.1.150", versionOK: false, message: notice),
                                     seenMessage: notice).map(\.kind) == [.outdatedCLI])
    }

    @Test func claudeCodeNotInstalled() {
        // version_ok is false without a CLI; installing is the onboarding's job.
        #expect(AgentCfgAlert.alerts(for: status(cliFound: false, version: "", versionOK: false)).isEmpty)
    }
}
