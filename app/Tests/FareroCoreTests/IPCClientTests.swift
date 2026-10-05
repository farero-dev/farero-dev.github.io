import Foundation
import Synchronization
import Testing
@testable import FareroCore

/// A minimal farerod stand-in on a real Unix socket.
final class FakeServer: Sendable {
    let path: String
    let listenFD: Int32

    init(path: String = "/tmp/frt-\(getpid())-\(UInt32.random(in: 0...UInt32.max)).sock") throws {
        self.path = path
        unlink(path)
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        var addr = sockaddr_un()
        addr.sun_family = sa_family_t(AF_UNIX)
        let bytes = Array(path.utf8)
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: bytes)
            raw[bytes.count] = 0
        }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) { bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size)) }
        }
        guard rc == 0, listen(fd, 4) == 0 else { throw POSIXError(.EADDRINUSE) }
        listenFD = fd
    }

    func accept() -> Int32 { Darwin.accept(listenFD, nil, nil) }

    func readLine(_ fd: Int32) -> String? {
        var bytes: [UInt8] = []
        var b: UInt8 = 0
        while read(fd, &b, 1) == 1 {
            if b == 0x0A { return String(decoding: bytes, as: UTF8.self) }
            bytes.append(b)
        }
        return nil
    }

    func readJSON(_ fd: Int32) -> [String: Any]? {
        guard let line = readLine(fd) else { return nil }
        return try? JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any]
    }

    func write(_ fd: Int32, _ s: String) {
        let bytes = Array(s.utf8)
        _ = bytes.withUnsafeBytes { Darwin.write(fd, $0.baseAddress, $0.count) }
    }

    func close() {
        Darwin.close(listenFD)
        unlink(path)
    }
}

/// Collects client events so tests can wait for them with a timeout.
final class EventLog: Sendable {
    private let events = Mutex<[IPCEvent]>([])

    func append(_ e: IPCEvent) { events.withLock { $0.append(e) } }
    var all: [IPCEvent] { events.withLock { $0 } }

    /// Waits until at least `count` events arrived.
    func wait(count: Int, timeout: TimeInterval = 5) async -> [IPCEvent] {
        let end = Date().addingTimeInterval(timeout)
        while all.count < count, Date() < end {
            try? await Task.sleep(for: .milliseconds(10))
        }
        return all
    }
}

/// Strings recorded by the fake server thread.
final class Recorder: Sendable {
    private let items = Mutex<[String]>([])
    func add(_ s: String) { items.withLock { $0.append(s) } }
    var all: [String] { items.withLock { $0 } }
}

func startCollecting(_ client: IPCClient) -> EventLog {
    let log = EventLog()
    Task.detached {
        for await e in client.events { log.append(e) }
    }
    return log
}

@Suite("IPC client", .serialized, .timeLimit(.minutes(1)))
struct IPCClientTests {
    @Test func helloSnapshotRequestsAndReconnect() async throws {
        let server = try FakeServer()
        defer { server.close() }
        let seen = Recorder()

        let serverDone = DispatchSemaphore(value: 0)
        Thread.detachNewThread {
            // First connection.
            let c = server.accept()
            if let hello = server.readJSON(c) {
                seen.add("\(hello["type"]!)/\(hello["id"]!)")
            }
            // The snapshot arrives in pieces.
            let snap = Samples.snapshot + "\n"
            let cut1 = snap.index(snap.startIndex, offsetBy: 40)
            let cut2 = snap.index(cut1, offsetBy: 200)
            server.write(c, String(snap[..<cut1]))
            usleep(20_000)
            server.write(c, String(snap[cut1..<cut2]))
            usleep(20_000)
            server.write(c, String(snap[cut2...]))

            // A request: a notification arrives before its reply.
            if let req = server.readJSON(c) {
                let id = req["id"] as! String
                let data = req["data"] as! [String: Any]
                seen.add("\(req["type"]!)/\(data["approval_id"]!)/\(data["answer"]!)")
                server.write(c, #"{"type":"approval.cancelled","data":{"approval_id":"other","reason":"timeout"}}"# + "\n" +
                                #"{"type":"ok","id":""# + id + "\"}\n")
            }
            // A request answered with an error.
            if let req = server.readJSON(c) {
                server.write(c, #"{"type":"error","id":""# + (req["id"] as! String) + #"","data":{"message":"approval not pending"}}"# + "\n")
            }
            Darwin.close(c)

            // The client reconnects and says hello again.
            let c2 = server.accept()
            if let hello = server.readJSON(c2) {
                seen.add("\(hello["type"]!)/\(hello["id"]!)")
            }
            server.write(c2, #"{"type":"state.snapshot","id":"hello","data":{"version":"2","sessions":[],"approvals":[],"plugins":[],"gateway":{"running":false,"port":0,"url":""}}}"# + "\n")
            serverDone.wait()
            Darwin.close(c2)
        }

        let client = IPCClient(socketPath: server.path, clientVersion: "test", retryInterval: .milliseconds(50))
        let log = startCollecting(client)
        client.start()
        defer {
            client.stop()
            serverDone.signal()
        }

        var events = await log.wait(count: 2)
        #expect(events.first == .connected)
        guard events.count >= 2, case .message(let snap) = events[1], case .stateSnapshot(let s) = snap.payload else {
            Issue.record("no snapshot: \(events)")
            return
        }
        #expect(snap.id == "hello")
        #expect(s.sessions.count == 1)
        #expect(client.isConnected)

        let reply = try await client.request(MessageType.approvalResponse,
                                             ApprovalResponse(approvalID: "9f2c", answer: .allowSession))
        #expect(reply.payload == .ok)

        await #expect(throws: IPCError.server("approval not pending")) {
            try await client.request(MessageType.approvalResponse, ApprovalResponse(approvalID: "x", answer: .deny))
        }

        events = await log.wait(count: 6)
        // connected, snapshot, cancelled (the notification), disconnected, connected, snapshot
        #expect(events.count == 6)
        if events.count == 6 {
            #expect(events[2] == .message(IncomingMessage(type: "approval.cancelled",
                                                          payload: .approvalCancelled(ApprovalCancelled(approvalID: "other", reason: "timeout")))))
            #expect(events[3] == .disconnected(reason: "farerod가 연결을 닫음"))
            #expect(events[4] == .connected)
            if case .message(let m) = events[5], case .stateSnapshot(let s2) = m.payload {
                #expect(s2.version == "2")
            } else {
                Issue.record("no second snapshot")
            }
        }
        #expect(seen.all == ["ui.hello/hello", "approval.response/9f2c/allow_session", "ui.hello/hello"])
    }

    @Test func retriesUntilTheDaemonAppears() async throws {
        let path = "/tmp/frt-\(getpid())-late.sock"
        unlink(path)
        let client = IPCClient(socketPath: path, clientVersion: "test", retryInterval: .milliseconds(50))
        let log = startCollecting(client)
        client.start()
        defer { client.stop() }

        var events = await log.wait(count: 1)
        #expect(events == [.disconnected(reason: "소켓이 없음 (farerod가 실행 중이 아님)")])

        await #expect(throws: IPCError.notConnected) {
            try await client.request(MessageType.policyGet)
        }

        // Several retries fail silently: the same reason is reported once.
        try await Task.sleep(for: .milliseconds(200))
        #expect(log.all.count == 1)

        let server = try FakeServer(path: path)
        defer { server.close() }
        Thread.detachNewThread {
            let c = server.accept()
            _ = server.readLine(c)
            usleep(300_000)
            Darwin.close(c)
        }
        events = await log.wait(count: 2)
        #expect(events.last == .connected)
    }

    @Test func pendingRequestFailsWhenTheConnectionDrops() async throws {
        let server = try FakeServer()
        defer { server.close() }
        Thread.detachNewThread {
            let c = server.accept()
            _ = server.readLine(c) // hello
            server.write(c, #"{"type":"state.snapshot","id":"hello","data":{}}"# + "\n")
            _ = server.readLine(c) // the request, never answered
            Darwin.close(c)
        }
        let client = IPCClient(socketPath: server.path, clientVersion: "test", retryInterval: .seconds(5))
        let log = startCollecting(client)
        client.start()
        defer { client.stop() }
        _ = await log.wait(count: 2)
        await #expect(throws: IPCError.disconnected) {
            try await client.request(MessageType.policyGet)
        }
    }
}
