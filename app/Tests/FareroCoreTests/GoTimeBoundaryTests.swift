import Foundation
import Testing
@testable import FareroCore

/// Edges of the RFC 3339 parser that the happy-path tests do not reach.
@Suite("Go time boundaries")
struct GoTimeBoundaryTests {
    @Test func acceptsEveryDateTimeSeparatorGoAllows() throws {
        let t = try #require(GoTime.parse("2026-10-02T00:41:37Z"))
        #expect(GoTime.parse("2026-10-02t00:41:37Z") == t)
        #expect(GoTime.parse("2026-10-02 00:41:37Z") == t)
    }

    @Test func rejectsOtherSeparators() {
        #expect(GoTime.parse("2026-10-02x00:41:37Z") == nil)
        #expect(GoTime.parse("2026-10-02_00:41:37Z") == nil)
    }

    @Test func offsetNeedsAllSixCharacters() {
        // "+09:" and "+09:0" are too short for hh:mm.
        #expect(GoTime.parse("2026-10-02T00:41:37+09:") == nil)
        #expect(GoTime.parse("2026-10-02T00:41:37+09:0") == nil)
        #expect(GoTime.parse("2026-10-02T00:41:37+09") == nil)
        #expect(GoTime.parse("2026-10-02T00:41:37+0900") == nil)
        #expect(GoTime.parse("2026-10-02T00:41:37.5+09:0") == nil)
    }

    @Test func onlyTheFullZeroTimeIsNil() {
        // Each field alone must not make a real date look like Go's zero time.
        #expect(GoTime.parse("0001-06-15T12:30:45Z") != nil) // year 1
        #expect(GoTime.parse("0001-01-02T12:30:45Z") != nil) // year 1, month 1
        #expect(GoTime.parse("2026-10-01T00:00:00Z") != nil) // day 1, midnight
        #expect(GoTime.parse("2026-10-02T05:00:00Z") != nil) // minute, second and fraction zero
        #expect(GoTime.parse("0001-01-01T00:00:01Z") != nil)
        #expect(GoTime.parse("0001-01-01T00:00:00.5Z") != nil)
        #expect(GoTime.parse("0001-01-01T00:00:00Z") == nil)
        #expect(GoTime.parse("0001-01-01T00:00:00+00:00") == nil)
    }

    @Test func fractionNeedsAZoneAfterIt() {
        #expect(GoTime.parse("2026-10-02T00:41:37.123") == nil)
        #expect(GoTime.parse("2026-10-02T00:41:37.1234567890Z") != nil) // more digits than Go sends still parse
    }
}
