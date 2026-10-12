import Foundation
import Synchronization
import Testing
@testable import FareroCore

/// Shutdown and reporting behavior of the reader loop, using `FakeServer`.
@Suite("IPC client lifecycle", .serialized, .timeLimit(.minutes(1)))
struct IPCClientLifecycleTests {
    @Test func stopClosesTheConnectionAndEndsTheEventStream() async throws {
        let server = try FakeServer()
        defer { server.close() }
        let sawEOF = Mutex(false)
        Thread.detachNewThread {
            let c = server.accept()
            _ = server.readLine(c) // ui.hello
            while server.readLine(c) != nil {} // returns once the client shuts the socket down
            sawEOF.withLock { $0 = true }
            Darwin.close(c)
        }

        let client = IPCClient(socketPath: server.path, clientVersion: "test", retryInterval: .milliseconds(50))
        let finished = Mutex(false)
        let log = EventLog()
        Task.detached {
            for await e in client.events { log.append(e) }
            finished.withLock { $0 = true }
        }
        client.start()
        let events = await log.wait(count: 1)
        #expect(events.first == .connected)

        client.stop()
        let end = Date().addingTimeInterval(5)
        while !sawEOF.withLock({ $0 }), Date() < end { try? await Task.sleep(for: .milliseconds(10)) }
        #expect(sawEOF.withLock { $0 }, "the server never saw the connection close")
        while !finished.withLock({ $0 }), Date() < end { try? await Task.sleep(for: .milliseconds(10)) }
        #expect(finished.withLock { $0 }, "events did not finish after stop()")
        #expect(log.all.last == .disconnected(reason: "연결 종료"))
    }

    @Test func aChangedFailureReasonIsReportedAgain() async throws {
        let path = "/tmp/frt-\(getpid())-\(UInt32.random(in: 0...UInt32.max)).sock"
        unlink(path)
        let client = IPCClient(socketPath: path, clientVersion: "test", retryInterval: .milliseconds(30))
        let log = startCollecting(client)
        client.start()
        defer { client.stop(); unlink(path) }

        var events = await log.wait(count: 1)
        guard case .disconnected(let first)? = events.first else {
            Issue.record("expected a disconnected event, got \(events)")
            return
        }
        // A plain file is not a socket, so connect fails with a different error than "no such file".
        #expect(FileManager.default.createFile(atPath: path, contents: Data()))
        events = await log.wait(count: 2)
        guard events.count >= 2, case .disconnected(let second) = events[1] else {
            Issue.record("the new failure reason was not reported: \(events)")
            return
        }
        #expect(first != second)

        // The same reason again is not repeated.
        try await Task.sleep(for: .milliseconds(300))
        #expect(log.all.count == 2)
    }

    @Test func reconnectsWaitForTheRetryIntervalInsteadOfSpinning() async throws {
        let server = try FakeServer()
        let accepted = Mutex(0)
        // A daemon that drops every connection right away.
        Thread.detachNewThread {
            while true {
                let c = server.accept()
                if c < 0 { break }
                accepted.withLock { $0 += 1 }
                Darwin.close(c)
            }
        }
        let client = IPCClient(socketPath: server.path, clientVersion: "test", retryInterval: .milliseconds(100))
        _ = startCollecting(client)
        client.start()
        try await Task.sleep(for: .milliseconds(550))
        client.stop()
        server.close()
        let n = accepted.withLock { $0 }
        // About one attempt per 100 ms; a loop that ignores the interval would make thousands.
        #expect((2...15).contains(n), "\(n) connection attempts in 550 ms")
    }
}
