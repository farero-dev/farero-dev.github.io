import Foundation
import Testing
@testable import FareroCore

@Suite("farerod LaunchAgent")
struct LaunchAgentTests {
    let path = "/Applications/Farero.app/Contents/MacOS/farerod"

    private func decode(_ data: Data) throws -> [String: Any] {
        try #require(try PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
    }

    @Test func runsFarerodByPathAndKeepsItAlive() throws {
        let p = try decode(try LaunchAgent.encode(farerodPath: path))
        #expect(p["Label"] as? String == "dev.farero.farerod")
        #expect(p["ProgramArguments"] as? [String] == [path])
        #expect(p["RunAtLoad"] as? Bool == true)
        // Kept alive only while farerod exists: after the app is deleted
        // launchd stops retrying, and a reinstall starts it again (M0).
        let keepAlive = try #require(p["KeepAlive"] as? [String: Any])
        #expect(keepAlive["PathState"] as? [String: Bool] == [path: true])
        #expect(p["ProcessType"] as? String == "Interactive")
        // SMAppService keys would tie the job to the bundle's signature again.
        #expect(p["BundleProgram"] == nil)
    }

    @Test func keepsUnusualPaths() throws {
        let odd = "/Users/me/Apps 앱/Farero.app/Contents/MacOS/farerod"
        let p = try decode(try LaunchAgent.encode(farerodPath: odd))
        #expect(p["ProgramArguments"] as? [String] == [odd])
    }

    @Test func plistLivesInUserLaunchAgents() {
        let url = LaunchAgent.plistURL(home: URL(fileURLWithPath: "/Users/me"))
        #expect(url.path == "/Users/me/Library/LaunchAgents/dev.farero.farerod.plist")
    }

    @Test func rewritesOnlyWhenMissingOrDifferent() throws {
        let current = try LaunchAgent.encode(farerodPath: path)
        #expect(LaunchAgent.needsWrite(existing: nil, farerodPath: path))
        #expect(!LaunchAgent.needsWrite(existing: current, farerodPath: path))
        // The app moved (Q63).
        #expect(LaunchAgent.needsWrite(existing: current, farerodPath: "/Users/me/Applications/Farero.app/Contents/MacOS/farerod"))
        #expect(LaunchAgent.needsWrite(existing: Data("not a plist".utf8), farerodPath: path))
    }
}
