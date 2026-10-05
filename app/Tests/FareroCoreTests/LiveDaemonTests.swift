import Foundation
import Testing
@testable import FareroCore

/// Talks to a real farerod when `FARERO_TEST_SOCKET` points at one, e.g.
///
///     FARERO_HOME=/tmp/fr FARERO_SOCKET=/tmp/fr/d.sock build/dev/farerod --dev &
///     FARERO_TEST_SOCKET=/tmp/fr/d.sock swift test --filter LiveDaemon
///
/// Read-only requests only. Skipped otherwise.
@Suite("Live daemon", .enabled(if: ProcessInfo.processInfo.environment["FARERO_TEST_SOCKET"] != nil),
       .timeLimit(.minutes(1)))
struct LiveDaemonTests {
    @Test func snapshotAndReadOnlyRequestsDecode() async throws {
        let path = try #require(ProcessInfo.processInfo.environment["FARERO_TEST_SOCKET"])
        let client = IPCClient(socketPath: path, clientVersion: "farero-tests")
        let log = startCollecting(client)
        client.start()
        defer { client.stop() }

        let events = await log.wait(count: 2)
        #expect(events.first == .connected)
        guard events.count >= 2, case .message(let m) = events[1], case .stateSnapshot(let snap) = m.payload else {
            Issue.record("no snapshot: \(events)")
            return
        }
        #expect(!snap.version.isEmpty)
        #expect(!snap.plugins.isEmpty)

        let logReply = try await client.request(MessageType.logQuery, LogQuery(limit: 5))
        guard case .logResult = logReply.payload else { Issue.record("log: \(logReply.payload)"); return }

        let policy = try await client.request(MessageType.policyGet)
        guard case .policyState(let tools) = policy.payload else { Issue.record("policy: \(policy.payload)"); return }
        #expect(!tools.isEmpty)
        #expect(tools.allSatisfy { ["auto", "ask", "block"].contains($0.level) })

        let settings = try await client.request(MessageType.settingsGet)
        guard case .settings(let s) = settings.payload else { Issue.record("settings: \(settings.payload)"); return }
        #expect(s.resultLimitBytes > 0)

        let plugins = try await client.request(MessageType.pluginList)
        guard case .pluginList(let list) = plugins.payload else { Issue.record("plugins: \(plugins.payload)"); return }
        #expect(list.map(\.plugin) == snap.plugins.map(\.plugin))

        await #expect(throws: IPCError.self) {
            try await client.request(MessageType.approvalResponse, ApprovalResponse(approvalID: "nope", answer: .deny))
        }
    }
}

/// Opens a hook connection (the role farero-hook plays) and sends one event.
/// Returns the connected socket; the caller reads the reply and closes it.
func openHookConnection(path: String, input: [String: Any]) throws -> Int32 {
    let fd = socket(AF_UNIX, SOCK_STREAM, 0)
    var addr = sockaddr_un()
    addr.sun_family = sa_family_t(AF_UNIX)
    let bytes = Array(path.utf8)
    withUnsafeMutableBytes(of: &addr.sun_path) { raw in
        raw.copyBytes(from: bytes)
        raw[bytes.count] = 0
    }
    let rc = withUnsafePointer(to: &addr) {
        $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
    }
    guard rc == 0 else { throw POSIXError(.ECONNREFUSED) }
    let msg: [String: Any] = ["type": "hook.event", "id": "h", "data": ["agent": "claude", "input": input, "tty": "", "pid": 0]]
    var line = try JSONSerialization.data(withJSONObject: msg)
    line.append(0x0A)
    _ = line.withUnsafeBytes { write(fd, $0.baseAddress, $0.count) }
    return fd
}

func readLine(fd: Int32) -> String? {
    var bytes: [UInt8] = []
    var b: UInt8 = 0
    while read(fd, &b, 1) == 1 {
        if b == 0x0A { return String(decoding: bytes, as: UTF8.self) }
        bytes.append(b)
    }
    return nil
}

/// Writes to the daemon's state. Run only against a throwaway farerod:
///
///     FARERO_TEST_SOCKET=/tmp/fr/d.sock FARERO_TEST_CLAUDE_DIR=/tmp/fr/claude \
///     FARERO_TEST_ALLOW_WRITES=1 swift test --filter LiveDaemon
@Suite("Live daemon writes", .serialized,
       .enabled(if: ProcessInfo.processInfo.environment["FARERO_TEST_SOCKET"] != nil
                && ProcessInfo.processInfo.environment["FARERO_TEST_ALLOW_WRITES"] == "1"),
       .timeLimit(.minutes(1)))
struct LiveDaemonWriteTests {
    let env = ProcessInfo.processInfo.environment

    func connectedClient() async throws -> (IPCClient, EventLog) {
        let client = IPCClient(socketPath: env["FARERO_TEST_SOCKET"]!, clientVersion: "farero-tests")
        let log = startCollecting(client)
        client.start()
        let events = await log.wait(count: 2)
        try #require(events.count >= 2)
        return (client, log)
    }

    /// Waits for a notification matching `match` after `from` events.
    func waitFor(_ log: EventLog, after from: Int, _ match: (ServerPayload) -> Bool) async -> ServerPayload? {
        let end = Date().addingTimeInterval(5)
        while Date() < end {
            for e in log.all.dropFirst(from) {
                if case .message(let m) = e, match(m.payload) { return m.payload }
            }
            try? await Task.sleep(for: .milliseconds(20))
        }
        return nil
    }

    @Test func agentCfgStatusAndPlanStayInTheTestDirectory() async throws {
        let dir = try #require(env["FARERO_TEST_CLAUDE_DIR"], "set FARERO_TEST_CLAUDE_DIR to the daemon's FARERO_CLAUDE_CONFIG_DIR")
        let (client, _) = try await connectedClient()
        defer { client.stop() }
        let reply = try await client.request(MessageType.agentCfgStatus, AgentRef())
        guard case .agentCfgStatus(let status) = reply.payload else { Issue.record("\(reply.payload)"); return }
        // Never look at a plan for the real ~/.claude.
        try #require(status.settingsPath.hasPrefix(dir), "settings path \(status.settingsPath) is outside \(dir)")
        #expect(status.minVersion == "2.1.203")

        let planReply = try await client.request(MessageType.agentCfgPlan, AgentRef())
        guard case .agentCfgPlan(let plan) = planReply.payload else { Issue.record("\(planReply.payload)"); return }
        #expect(plan.changes.count == 1)
        #expect(plan.changes.first?.path == status.settingsPath)
        let change = try #require(plan.changes.first)
        #expect(!LineDiff.rows(before: change.before, after: change.after).isEmpty)
        #expect(change.after.contains("farero-hook"))
        #expect(change.after.contains("mcp__farero__*"))
        #expect(plan.commands.count == 2)
        // The test directory's settings.json carries another tool's hook.
        if change.before.contains("other-notifier") {
            #expect(HookScan.foreignPermissionHooks(settingsJSON: change.before).contains { $0.contains("other-notifier") })
        }
    }

    @Test func policyAndSettingsRoundTrip() async throws {
        let (client, _) = try await connectedClient()
        defer { client.stop() }
        let set = try await client.request(MessageType.policySet, PolicySet(plugin: "dev", tool: "write_sim", level: "block"))
        guard case .policyState(let tools) = set.payload else { Issue.record("\(set.payload)"); return }
        let tool = try #require(tools.first { $0.plugin == "dev" && $0.tool == "write_sim" })
        #expect(tool.level == "block" && tool.overridden && tool.defaultLevel == "ask")
        let reset = try await client.request(MessageType.policySet, PolicySet(plugin: "dev", tool: "write_sim", level: ""))
        guard case .policyState(let after) = reset.payload else { Issue.record("\(reset.payload)"); return }
        #expect(after.first { $0.id == "dev_write_sim" }?.overridden == false)
        // A no_session tool cannot be auto-allowed.
        await #expect(throws: IPCError.self) {
            try await client.request(MessageType.policySet, PolicySet(plugin: "dev", tool: "destroy_sim", level: "auto"))
        }

        let current = try await client.request(MessageType.settingsGet)
        guard case .settings(let original) = current.payload else { Issue.record("\(current.payload)"); return }
        let changed = try await client.request(MessageType.settingsSet, DaemonSettings(resultLimitBytes: 4096, updateCheck: false))
        #expect(changed.payload == .settings(DaemonSettings(resultLimitBytes: 4096, updateCheck: false)))
        try await client.request(MessageType.settingsSet, original)
    }

    @Test func koreanSearchFiltersAndSessionDelete() async throws {
        let path = env["FARERO_TEST_SOCKET"]!
        let (client, log) = try await connectedClient()
        defer { client.stop() }
        let sid = "live-\(UInt32.random(in: 0...UInt32.max))"
        let mark = log.all.count

        // An agent permission request reaches this UI client as a card.
        let hook = try openHookConnection(path: path, input: [
            "session_id": sid, "cwd": "/tmp/farero-live", "hook_event_name": "PermissionRequest",
            "tool_name": "Bash", "tool_input": ["command": "./배포 스크립트 실행.sh"],
        ])
        defer { close(hook) }
        let request = await waitFor(log, after: mark) {
            if case .approvalRequest(let a) = $0 { return a.sessionID == "claude:\(sid)" }
            return false
        }
        guard case .approvalRequest(let approval) = request else { Issue.record("no approval card"); return }
        try await client.request(MessageType.approvalResponse, ApprovalResponse(approvalID: approval.id, answer: .deny))
        let decision = try #require(readLine(fd: hook))
        #expect(decision.contains("\"behavior\":\"deny\""))
        // farerod tells every UI the card is settled.
        let cancelled = await waitFor(log, after: mark) {
            if case .approvalCancelled(let c) = $0 { return c.approvalID == approval.id }
            return false
        }
        #expect(cancelled == .approvalCancelled(ApprovalCancelled(approvalID: approval.id, reason: "answered")))
        _ = await waitFor(log, after: mark) { if case .callLogged = $0 { true } else { false } }

        func search(_ f: LogFilter) async throws -> [Call] {
            let reply = try await client.request(MessageType.logQuery, f.logQuery(now: .now, limit: 50))
            guard case .logResult(let rows) = reply.payload else { return [] }
            return rows.filter { $0.sessionID == "claude:\(sid)" }
        }
        var f = LogFilter()
        f.query = "배포" // two syllables: substring match
        #expect(try await search(f).count == 1)
        f.query = "배포 스크립트" // full-text
        #expect(try await search(f).count == 1)
        f.query = ""
        f.sessionID = "claude:\(sid)"
        f.decision = CallDecision.denied
        f.kind = "agent"
        f.period = .today
        let rows = try await search(f)
        #expect(rows.count == 1)
        #expect(rows.first?.tool == "Bash")
        f.decision = CallDecision.userAllowed
        #expect(try await search(f).isEmpty)

        // Deleting the session removes it and its log.
        let before = log.all.count
        try await client.request(MessageType.sessionDelete, SessionRef(sessionID: "claude:\(sid)"))
        let removed = await waitFor(log, after: before) {
            if case .sessionRemoved(let r) = $0 { return r.sessionID == "claude:\(sid)" }
            return false
        }
        #expect(removed != nil)
        f = LogFilter()
        f.sessionID = "claude:\(sid)"
        #expect(try await search(f).isEmpty)
    }
}
