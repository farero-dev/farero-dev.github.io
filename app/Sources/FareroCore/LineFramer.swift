import Foundation

/// Splits a byte stream into newline-terminated lines (JSON Lines).
///
/// Bytes arrive in arbitrary chunks, so a line may span many reads, and a
/// line may be megabytes long (a Write tool input carries a whole file).
/// Each byte is scanned once: the search for "\n" resumes where the previous
/// chunk ended instead of starting over.
public struct LineFramer: Sendable {
    public enum FramingError: Error, Equatable {
        case lineTooLong(Int)
    }

    /// farerod caps a message at 32 MiB (ipc.MaxLine); allow a little more.
    public static let defaultMaxLineBytes = 40 << 20

    public let maxLineBytes: Int
    private var buffer: [UInt8] = []
    private var start = 0 // first byte of the current (incomplete) line
    private var scanFrom = 0 // bytes before this index hold no newline

    public init(maxLineBytes: Int = LineFramer.defaultMaxLineBytes) {
        self.maxLineBytes = maxLineBytes
    }

    /// Bytes buffered for the incomplete line.
    public var pendingByteCount: Int { buffer.count - start }

    /// Appends a chunk and returns the complete lines it finished, without
    /// their newline (a trailing "\r" is dropped too). Empty lines are skipped.
    public mutating func append<S: Sequence>(_ bytes: S) throws -> [Data] where S.Element == UInt8 {
        buffer.append(contentsOf: bytes)
        var lines: [Data] = []
        var i = max(scanFrom, start)
        while let nl = buffer[i...].firstIndex(of: 0x0A) {
            var end = nl
            if end > start, buffer[end - 1] == 0x0D { end -= 1 }
            if end > start { lines.append(Data(buffer[start..<end])) }
            start = nl + 1
            i = start
        }
        scanFrom = buffer.count
        // Drop consumed bytes once they are worth moving.
        if start == buffer.count {
            buffer.removeAll(keepingCapacity: buffer.count <= 1 << 20)
            start = 0
            scanFrom = 0
        } else if start >= 64 << 10 {
            buffer.removeFirst(start)
            scanFrom -= start
            start = 0
        }
        if buffer.count - start > maxLineBytes {
            let n = buffer.count - start
            reset()
            throw FramingError.lineTooLong(n)
        }
        return lines
    }

    /// Forgets any partial line (after a reconnect).
    public mutating func reset() {
        buffer = []
        start = 0
        scanFrom = 0
    }
}
