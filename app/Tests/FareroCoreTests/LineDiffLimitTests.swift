import Foundation
import Testing
@testable import FareroCore

/// Trimming and tail handling in the diff, observable through the size limit.
@Suite("Line diff trimming")
struct LineDiffLimitTests {
    /// Enough lines that an untrimmed LCS table would pass `maxCells`.
    static let big = 2_100

    @Test func commonPrefixIsTrimmedBeforeTheSizeLimit() {
        let prefix = (0..<Self.big).map { "same \($0)" }
        let ops = LineDiff.diff(prefix + ["old"], prefix + ["new"])
        #expect(ops.count == Self.big + 2)
        #expect(ops.prefix(Self.big).allSatisfy { if case .same = $0 { true } else { false } })
        #expect(Array(ops.suffix(2)) == [.removed("old"), .added("new")])
    }

    @Test func commonSuffixIsTrimmedBeforeTheSizeLimit() {
        let suffix = (0..<Self.big).map { "same \($0)" }
        let ops = LineDiff.diff(["old"] + suffix, ["new"] + suffix)
        #expect(ops.count == Self.big + 2)
        #expect(Array(ops.prefix(2)) == [.removed("old"), .added("new")])
        #expect(ops.suffix(Self.big).allSatisfy { if case .same = $0 { true } else { false } })
    }

    @Test func prefixAndSuffixTrimmingStopAtTheShorterSide() {
        // b is entirely contained in a's prefix and suffix; nothing may be trimmed twice.
        #expect(LineDiff.diff(["a", "b", "a"], ["a"]) == [.same("a"), .removed("b"), .removed("a")])
        #expect(LineDiff.diff(["a"], ["a", "b", "a"]) == [.same("a"), .added("b"), .added("a")])
    }

    @Test func linesLeftOverAfterTheOtherSideIsUsedUpAreStillEmitted() {
        // The new side is used up first, so the trailing old line comes from the tail loop.
        #expect(LineDiff.diff(["A", "B", "C"], ["B"]) == [.removed("A"), .same("B"), .removed("C")])
        // And the other way round.
        #expect(LineDiff.diff(["B"], ["A", "B", "C"]) == [.added("A"), .same("B"), .added("C")])
    }

    @Test func equalLinesExtendTheCommonSubsequence() {
        // The LCS must grow by one for each matching pair, not take the max of the neighbors.
        let ops = LineDiff.diff(["x", "a", "b", "c", "y"], ["z", "a", "b", "c", "w"])
        #expect(ops.filter { if case .same = $0 { true } else { false } }.count == 3)
        #expect(ops.contains(.removed("x")) && ops.contains(.added("z")))
    }
}
