import Foundation
import Testing
@testable import FareroCore

@Suite("Formatting edges")
struct FormattingEdgeTests {
    @Test func wholeDoublesPrintWithoutFraction() {
        #expect(JSONValue.double(3).compact() == "3")
        #expect(JSONValue.double(-2).compact() == "-2")
        #expect(JSONValue.double(0.5).compact() == "0.5")
    }

    @Test func hugeDoublesDoNotOverflowInt64() {
        // At and above 1e15 the value is printed as a double instead of converting to Int64.
        #expect(JSONValue.double(2e15).compact() == "2000000000000000.0")
        #expect(JSONValue.double(1e300).compact() == String(1e300))
        #expect(JSONValue.double(999_999_999_999_999).compact() == "999999999999999")
    }

    @Test func folderNameForOddWorkingDirectories() {
        func name(_ cwd: String) -> String {
            SessionDisplay.folderName(Session(id: "s1", agentSessionID: "abcdef123456", cwd: cwd))
        }
        #expect(name("/work/proj") == "proj")
        #expect(name("/work/proj/") == "proj")
        #expect(name("/") == "/") // the root is a name of its own, not an empty one
        #expect(name("") == "abcdef12") // falls back to the session id
    }

    @Test func policyToolDefaultLevelFallsBackToLevel() {
        #expect(PolicyTool(plugin: "p", tool: "t", level: "ask").defaultLevel == "ask")
        #expect(PolicyTool(plugin: "p", tool: "t", level: "ask", defaultLevel: "auto").defaultLevel == "auto")
    }

    @Test func versionNormalizationStripsOneLeadingV() {
        #expect(DaemonVersionCheck.normalize("v1.2.3") == "1.2.3")
        #expect(DaemonVersionCheck.normalize("vnext") == "next") // not a semantic version
        #expect(DaemonVersionCheck.normalize("next") == "next")
        #expect(DaemonVersionCheck.normalize("dev-build") == "dev-build")
    }
}
