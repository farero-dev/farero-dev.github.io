import Foundation
import Testing
@testable import FareroCore

@Suite("SemVer")
struct SemVerTests {
    @Test func parses() throws {
        let v = try #require(SemVer("v1.2.3-beta.2+sha.abc"))
        #expect(v.major == 1 && v.minor == 2 && v.patch == 3)
        #expect(v.prerelease == ["beta", "2"])
        #expect(v.description == "1.2.3-beta.2")
        #expect(SemVer("0.1")?.description == "0.1.0")
        #expect(SemVer("2")?.description == "2.0.0")
    }

    @Test(arguments: ["", "dev", "__VERSION__", "1.2.3.4", "1..2", "v", "1.2.x", "1.2.3-", "1.2.3-a..b", "-1.0.0"])
    func rejects(_ s: String) {
        #expect(SemVer(s) == nil)
    }

    @Test func orders() {
        let ordered = ["0.9.9", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
                       "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0", "1.0.1", "1.1.0", "2.0.0"]
        let parsed = ordered.compactMap(SemVer.init)
        #expect(parsed.count == ordered.count)
        for i in 1..<parsed.count {
            #expect(parsed[i - 1] < parsed[i], "\(ordered[i - 1]) < \(ordered[i])")
            #expect(!(parsed[i] < parsed[i - 1]))
        }
        #expect(SemVer("v1.2.0") == SemVer("1.2"))
        #expect(SemVer("1.10.0")! > SemVer("1.9.0")!)
    }
}

@Suite("Update check")
struct UpdateCheckTests {
    let release = UpdateCheck.Release(tagName: "v0.2.0", htmlURL: "https://github.com/farero-dev/farero-dev.github.io/releases/tag/v0.2.0")

    @Test func newerReleaseIsOffered() {
        let a = UpdateCheck.newer(release: release, currentVersion: "0.1.3")
        #expect(a?.version == "0.2.0")
        #expect(a?.url.absoluteString == release.htmlURL)
    }

    @Test func sameOrOlderOrDevIsNot() {
        #expect(UpdateCheck.newer(release: release, currentVersion: "0.2.0") == nil)
        #expect(UpdateCheck.newer(release: release, currentVersion: "0.3.0") == nil)
        #expect(UpdateCheck.newer(release: release, currentVersion: "dev") == nil)
        #expect(UpdateCheck.newer(release: release, currentVersion: "__VERSION__") == nil)
        #expect(UpdateCheck.newer(release: release, currentVersion: nil) == nil)
        var pre = release
        pre.prerelease = true
        #expect(UpdateCheck.newer(release: pre, currentVersion: "0.1.0") == nil)
        var insecure = release
        insecure.htmlURL = "http://example.com"
        #expect(UpdateCheck.newer(release: insecure, currentVersion: "0.1.0") == nil)
    }

    @Test func httpResponses() throws {
        let body = Data(#"{"tag_name":"v1.0.0","html_url":"https://github.com/x/y/releases/tag/v1.0.0","draft":false,"prerelease":false,"assets":[]}"#.utf8)
        #expect(try UpdateCheck.evaluate(statusCode: 200, body: body, currentVersion: "0.9.0")?.version == "1.0.0")
        // No releases yet.
        #expect(try UpdateCheck.evaluate(statusCode: 404, body: Data(#"{"message":"Not Found"}"#.utf8), currentVersion: "0.9.0") == nil)
        #expect(throws: UpdateCheck.UpdateCheckError.http(403)) {
            try UpdateCheck.evaluate(statusCode: 403, body: Data(), currentVersion: "0.9.0")
        }
        #expect(throws: (any Error).self) {
            try UpdateCheck.evaluate(statusCode: 200, body: Data("{}".utf8), currentVersion: "0.9.0")
        }
    }
}

@Suite("Daemon version check")
struct DaemonVersionCheckTests {
    @Test func mismatchNeedsReregistration() {
        #expect(DaemonVersionCheck.needsReregistration(appVersion: "0.2.0", daemonVersion: "0.1.0"))
        #expect(DaemonVersionCheck.needsReregistration(appVersion: "0.2.0", daemonVersion: "v0.1.0"))
    }

    @Test func matchesAndDevBuildsAreIgnored() {
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: "0.2.0", daemonVersion: "0.2.0"))
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: "0.2.0", daemonVersion: "v0.2.0"))
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: "0.2.0", daemonVersion: "dev"))
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: "0.2.0", daemonVersion: ""))
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: "dev", daemonVersion: "0.1.0"))
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: nil, daemonVersion: "0.1.0"))
        #expect(!DaemonVersionCheck.needsReregistration(appVersion: "__VERSION__", daemonVersion: "0.1.0"))
    }
}
