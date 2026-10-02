import Foundation
import Synchronization

/// What the IPC client reports to its consumer, in order.
public enum IPCEvent: Sendable, Equatable {
    /// The socket connected and `ui.hello` was sent; `state.snapshot` follows.
    case connected
    /// A server notification, or a reply nobody is waiting for (the snapshot).
    case message(IncomingMessage)
    /// The connection failed or was lost. Reported once per failure streak
    /// (and again when the reason changes); the client keeps retrying.
    case disconnected(reason: String)
}

public enum IPCError: Error, Equatable, LocalizedError {
    case notConnected
    case disconnected
    case timedOut
    case server(String)
    case badReply(String)

    public var errorDescription: String? {
        switch self {
        case .notConnected: "farerod에 연결되어 있지 않음"
        case .disconnected: "farerod 연결이 끊김"
        case .timedOut: "farerod 응답 시간 초과"
        case .server(let m): m
        case .badReply(let m): "farerod 응답을 읽을 수 없음: \(m)"
        }
    }
}

/// The app's connection to farerod (docs/ipc.md, "앱").
///
/// A dedicated thread connects to the Unix socket, sends `ui.hello`, and reads
/// JSON Lines; lines are decoded on that thread and delivered through
/// `events`. When the connection drops it retries every `retryInterval`,
/// because farerod auto-denies gateway approvals while no app is connected.
/// Writes are serialized on their own queue. Replies are matched to requests
/// by `id`.
public final class IPCClient: Sendable {
    public let socketPath: String
    public let clientVersion: String
    public let retryInterval: Duration
    /// Single consumer. Iterate it on the main actor to apply state changes.
    public let events: AsyncStream<IPCEvent>

    private let continuation: AsyncStream<IPCEvent>.Continuation
    private let state = Mutex(State())
    private let writeQueue = DispatchQueue(label: "dev.farero.ipc.write", qos: .userInitiated)
    private let wake = DispatchSemaphore(value: 0)

    private struct State {
        var started = false
        var stopped = false
        var fd: Int32 = -1
        var connected = false
        var nextID = 0
        var pending: [String: CheckedContinuation<IncomingMessage, Error>] = [:]
    }

    /// The socket path farerod uses: `FARERO_SOCKET`, or
    /// `~/Library/Application Support/Farero/farerod.sock`.
    public static func defaultSocketPath(environment: [String: String] = ProcessInfo.processInfo.environment) -> String {
        if let s = environment["FARERO_SOCKET"], !s.isEmpty { return s }
        let base: String
        if let home = environment["FARERO_HOME"], !home.isEmpty {
            base = home
        } else {
            base = (NSHomeDirectory() as NSString).appendingPathComponent("Library/Application Support/Farero")
        }
        return (base as NSString).appendingPathComponent("farerod.sock")
    }

    public init(socketPath: String, clientVersion: String, retryInterval: Duration = .seconds(1)) {
        self.socketPath = socketPath
        self.clientVersion = clientVersion
        self.retryInterval = retryInterval
        (events, continuation) = AsyncStream.makeStream(of: IPCEvent.self, bufferingPolicy: .unbounded)
    }

    /// Starts the connect/read loop. Calling it again does nothing.
    public func start() {
        let first = state.withLock { s -> Bool in
            defer { s.started = true }
            return !s.started
        }
        guard first else { return }
        let thread = Thread { [self] in run() }
        thread.name = "farero.ipc.reader"
        thread.qualityOfService = .userInitiated
        thread.start()
    }

    /// Stops the loop and closes the connection. The client cannot restart.
    public func stop() {
        let fd = state.withLock { s -> Int32 in
            s.stopped = true
            return s.fd
        }
        if fd >= 0 { shutdown(fd, SHUT_RDWR) }
        wake.signal()
    }

    public var isConnected: Bool { state.withLock { $0.connected } }

    /// Sends a request and waits for the reply with the same id. An `error`
    /// reply throws `IPCError.server`.
    @discardableResult
    public func request<D: Encodable & Sendable>(_ type: String, _ data: D?,
                                                 timeout: Duration = .seconds(30)) async throws -> IncomingMessage {
        let id = state.withLock { s -> String in
            s.nextID += 1
            return "r\(s.nextID)"
        }
        let line = try OutgoingMessage.line(type: type, id: id, data: data)
        let reply: IncomingMessage = try await withCheckedThrowingContinuation { cont in
            let registered = state.withLock { s -> Bool in
                guard s.connected else { return false }
                s.pending[id] = cont
                return true
            }
            guard registered else {
                cont.resume(throwing: IPCError.notConnected)
                return
            }
            writeQueue.async { [self] in writeCurrent(line) }
            Task { [self] in
                try? await Task.sleep(for: timeout)
                takePending(id)?.resume(throwing: IPCError.timedOut)
            }
        }
        if case .error(let e) = reply.payload { throw IPCError.server(e.message) }
        return reply
    }

    /// A request without data.
    @discardableResult
    public func request(_ type: String, timeout: Duration = .seconds(30)) async throws -> IncomingMessage {
        try await request(type, Optional<OutgoingMessage.Empty>.none, timeout: timeout)
    }

    // MARK: - Reader thread

    private var isStopped: Bool { state.withLock { $0.stopped } }

    private func run() {
        var lastReported: String?
        var everAttempted = false
        while !isStopped {
            let fd: Int32
            switch Self.connect(path: socketPath) {
            case .failure(let reason):
                if !everAttempted || reason != lastReported {
                    continuation.yield(.disconnected(reason: reason))
                    lastReported = reason
                }
                everAttempted = true
                waitForRetry()
                continue
            case .success(let s):
                fd = s
            }
            everAttempted = true

            // ui.hello must be the first message, before any request.
            let hello = (try? OutgoingMessage.line(type: MessageType.uiHello, id: "hello",
                                                   data: UIHello(version: clientVersion))) ?? Data()
            let sent = writeQueue.sync { Self.writeAll(fd, hello) }
            if !sent {
                close(fd)
                let reason = "ui.hello를 보내지 못함"
                if reason != lastReported { continuation.yield(.disconnected(reason: reason)) }
                lastReported = reason
                waitForRetry()
                continue
            }
            let stoppedMeanwhile = state.withLock { s -> Bool in
                s.fd = fd
                s.connected = true
                return s.stopped
            }
            if stoppedMeanwhile { shutdown(fd, SHUT_RDWR) }
            continuation.yield(.connected)
            lastReported = nil

            let reason = readLoop(fd)

            let pending = writeQueue.sync { () -> [CheckedContinuation<IncomingMessage, Error>] in
                let p = state.withLock { s -> [CheckedContinuation<IncomingMessage, Error>] in
                    s.fd = -1
                    s.connected = false
                    defer { s.pending = [:] }
                    return Array(s.pending.values)
                }
                close(fd)
                return p
            }
            for c in pending { c.resume(throwing: IPCError.disconnected) }
            continuation.yield(.disconnected(reason: reason))
            lastReported = reason
            if !isStopped { waitForRetry() }
        }
        continuation.finish()
    }

    /// Reads until EOF or an error and returns why the connection ended.
    private func readLoop(_ fd: Int32) -> String {
        var framer = LineFramer()
        let size = 256 << 10
        let buf = UnsafeMutableRawPointer.allocate(byteCount: size, alignment: 16)
        defer { buf.deallocate() }
        while true {
            let n = read(fd, buf, size)
            if n > 0 {
                do {
                    for line in try framer.append(UnsafeRawBufferPointer(start: buf, count: n)) {
                        handle(line)
                    }
                } catch {
                    return "메시지가 너무 김"
                }
            } else if n == 0 {
                return isStopped ? "연결 종료" : "farerod가 연결을 닫음"
            } else if errno == EINTR {
                continue
            } else {
                return String(cString: strerror(errno))
            }
        }
    }

    private func handle(_ line: Data) {
        do {
            let m = try IncomingMessage.decode(line)
            if let id = m.id, let cont = takePending(id) {
                cont.resume(returning: m)
                return
            }
            continuation.yield(.message(m))
        } catch {
            // A reply we cannot decode must still release its waiter.
            if let h = try? JSONDecoder().decode(EnvelopeHeader.self, from: line), let id = h.id,
               let cont = takePending(id) {
                cont.resume(throwing: IPCError.badReply("\(h.type): \(error)"))
            }
        }
    }

    private func takePending(_ id: String) -> CheckedContinuation<IncomingMessage, Error>? {
        state.withLock { $0.pending.removeValue(forKey: id) }
    }

    private func waitForRetry() {
        let (sec, atto) = retryInterval.components
        let ms = Int(sec) * 1000 + Int(atto / 1_000_000_000_000_000)
        _ = wake.wait(timeout: .now() + .milliseconds(max(ms, 10)))
    }

    /// On the write queue: writes to the current connection, shutting it down
    /// on failure so the reader notices.
    private func writeCurrent(_ data: Data) {
        let fd = state.withLock { $0.fd }
        guard fd >= 0 else { return }
        if !Self.writeAll(fd, data) { shutdown(fd, SHUT_RDWR) }
    }

    // MARK: - POSIX

    private enum ConnectResult {
        case success(Int32)
        case failure(String)
    }

    private static func connect(path: String) -> ConnectResult {
        var addr = sockaddr_un()
        let pathBytes = Array(path.utf8)
        guard pathBytes.count < MemoryLayout.size(ofValue: addr.sun_path) else {
            return .failure("소켓 경로가 너무 김: \(path)")
        }
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        guard fd >= 0 else { return .failure(String(cString: strerror(errno))) }
        var on: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &on, socklen_t(MemoryLayout<Int32>.size))
        _ = fcntl(fd, F_SETFD, FD_CLOEXEC)

        addr.sun_family = sa_family_t(AF_UNIX)
        addr.sun_len = UInt8(MemoryLayout<sockaddr_un>.size)
        withUnsafeMutableBytes(of: &addr.sun_path) { raw in
            raw.copyBytes(from: pathBytes)
            raw[pathBytes.count] = 0
        }
        let rc = withUnsafePointer(to: &addr) {
            $0.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.connect(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        if rc != 0 {
            let err = errno
            close(fd)
            switch err {
            case ENOENT: return .failure("소켓이 없음 (farerod가 실행 중이 아님)")
            case ECONNREFUSED: return .failure("연결 거부됨 (farerod가 실행 중이 아님)")
            case EACCES: return .failure("소켓에 접근할 수 없음")
            default: return .failure(String(cString: strerror(err)))
            }
        }
        return .success(fd)
    }

    private static func writeAll(_ fd: Int32, _ data: Data) -> Bool {
        data.withUnsafeBytes { raw -> Bool in
            guard var p = raw.baseAddress else { return true }
            var left = raw.count
            while left > 0 {
                let n = write(fd, p, left)
                if n < 0 {
                    if errno == EINTR { continue }
                    return false
                }
                p += n
                left -= n
            }
            return true
        }
    }
}
