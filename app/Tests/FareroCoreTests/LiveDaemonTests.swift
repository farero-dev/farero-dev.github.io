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
