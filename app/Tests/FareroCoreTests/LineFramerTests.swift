import Foundation
import Testing
@testable import FareroCore

@Suite("Line framing")
struct LineFramerTests {
    func strings(_ lines: [Data]) -> [String] { lines.map { String(decoding: $0, as: UTF8.self) } }

    @Test func splitsCompleteLines() throws {
        var f = LineFramer()
        #expect(strings(try f.append(Array("a\nbb\nccc\n".utf8))) == ["a", "bb", "ccc"])
        #expect(f.pendingByteCount == 0)
    }

    @Test func joinsPartialReads() throws {
        var f = LineFramer()
        #expect(try f.append(Array("{\"type\":".utf8)).isEmpty)
        #expect(try f.append(Array("\"ok\"".utf8)).isEmpty)
        #expect(strings(try f.append(Array("}\n{\"ty".utf8))) == [#"{"type":"ok"}"#])
        #expect(f.pendingByteCount == 4)
        #expect(strings(try f.append(Array("pe\":1}\n".utf8))) == [#"{"type":1}"#])
    }

    @Test func byteAtATime() throws {
        var f = LineFramer()
        var out: [String] = []
        for b in "한글 줄\n두번째\n".utf8 {
            out += strings(try f.append([b]))
        }
        #expect(out == ["한글 줄", "두번째"])
    }

    @Test func dropsCarriageReturnsAndEmptyLines() throws {
        var f = LineFramer()
        #expect(strings(try f.append(Array("a\r\n\n\r\nb\n".utf8))) == ["a", "b"])
    }

    @Test func veryLongLineAcrossManyChunks() throws {
        var f = LineFramer()
        let payload = String(repeating: "x", count: 5_000_000)
        let bytes = Array("\(payload)\nnext\n".utf8)
        var out: [Data] = []
        var i = 0
        while i < bytes.count {
            let end = min(i + 65_536, bytes.count)
            out += try f.append(bytes[i..<end])
            i = end
        }
        #expect(out.count == 2)
        #expect(out[0].count == 5_000_000)
        #expect(String(decoding: out[1], as: UTF8.self) == "next")
    }

    @Test func rejectsOversizedLine() throws {
        var f = LineFramer(maxLineBytes: 10)
        #expect(throws: LineFramer.FramingError.lineTooLong(11)) {
            _ = try f.append(Array("01234567890".utf8))
        }
        // The framer recovers after the reset.
        #expect(strings(try f.append(Array("ok\n".utf8))) == ["ok"])
    }

    @Test func compactsAfterManyLines() throws {
        var f = LineFramer()
        let line = String(repeating: "y", count: 1000) + "\n"
        var count = 0
        for _ in 0..<200 {
            count += try f.append(Array((line + "partial").utf8)).count
            count += try f.append(Array("\n".utf8)).count
        }
        #expect(count == 400)
        #expect(f.pendingByteCount == 0)
    }
}
