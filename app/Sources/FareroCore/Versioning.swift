import Foundation

/// A semantic version (`1.2.3`, `v1.2.3-beta.2+build`).
public struct SemVer: Sendable, Equatable, Comparable, CustomStringConvertible {
    public var major: Int
    public var minor: Int
    public var patch: Int
    public var prerelease: [String]

    /// Parses a version, accepting a leading "v" and missing minor/patch
    /// ("1.2" is 1.2.0). Build metadata after "+" is ignored.
    public init?(_ text: String) {
        var s = text.trimmingCharacters(in: .whitespaces)
        if s.hasPrefix("v") || s.hasPrefix("V") { s.removeFirst() }
        if let plus = s.firstIndex(of: "+") { s = String(s[..<plus]) }
        var pre: [String] = []
        if let dash = s.firstIndex(of: "-") {
            pre = s[s.index(after: dash)...].split(separator: ".", omittingEmptySubsequences: false).map(String.init)
            s = String(s[..<dash])
            guard !pre.isEmpty, pre.allSatisfy({ !$0.isEmpty }) else { return nil }
        }
        let nums = s.split(separator: ".", omittingEmptySubsequences: false)
        guard (1...3).contains(nums.count) else { return nil }
        var parts: [Int] = []
        for n in nums {
            guard !n.isEmpty, n.allSatisfy(\.isASCII), let v = Int(n), v >= 0 else { return nil }
            parts.append(v)
        }
        while parts.count < 3 { parts.append(0) }
        major = parts[0]
        minor = parts[1]
        patch = parts[2]
        prerelease = pre
    }

    public var description: String {
        "\(major).\(minor).\(patch)" + (prerelease.isEmpty ? "" : "-" + prerelease.joined(separator: "."))
    }

    public static func < (a: SemVer, b: SemVer) -> Bool {
        if a.major != b.major { return a.major < b.major }
        if a.minor != b.minor { return a.minor < b.minor }
        if a.patch != b.patch { return a.patch < b.patch }
        // A release ranks above its prereleases.
        switch (a.prerelease.isEmpty, b.prerelease.isEmpty) {
        case (true, true): return false
        case (true, false): return false
        case (false, true): return true
        case (false, false): break
        }
        for (x, y) in zip(a.prerelease, b.prerelease) where x != y {
            switch (Int(x), Int(y)) {
            case let (nx?, ny?): return nx < ny
            case (.some, nil): return true // numeric identifiers rank lower
            case (nil, .some): return false
            case (nil, nil): return x < y
            }
        }
        return a.prerelease.count < b.prerelease.count
    }
}

/// The update check (Q41, Q43): the only request the app makes itself.
public enum UpdateCheck {
    public static let latestReleaseURL = URL(string: "https://api.github.com/repos/farero-dev/farero-dev.github.io/releases/latest")!
    public static let interval: TimeInterval = 24 * 60 * 60

    /// The fields of a GitHub release the app uses.
    public struct Release: Sendable, Equatable, Decodable {
        public var tagName: String
        public var htmlURL: String
        public var draft: Bool
        public var prerelease: Bool

        public init(tagName: String, htmlURL: String, draft: Bool = false, prerelease: Bool = false) {
            self.tagName = tagName
            self.htmlURL = htmlURL
            self.draft = draft
            self.prerelease = prerelease
        }

        enum CodingKeys: String, CodingKey {
            case draft, prerelease
            case tagName = "tag_name"
            case htmlURL = "html_url"
        }

        public init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            tagName = try c.decode(String.self, forKey: .tagName)
            htmlURL = c.value(.htmlURL, "")
            draft = c.value(.draft, false)
            prerelease = c.value(.prerelease, false)
        }
    }

    public struct Available: Sendable, Equatable {
        public var version: String
        public var url: URL
    }

    /// Whether this build takes part: a real version, not a dev build or an
    /// unfilled Info.plist template.
    public static func isCheckable(currentVersion: String?) -> Bool {
        guard let v = currentVersion, !v.isEmpty, v != "dev", !v.contains("__") else { return false }
        return SemVer(v) != nil
    }

    /// The newer release, or nil when the current version is up to date,
    /// not checkable, or the release is a draft or prerelease.
    public static func newer(release: Release, currentVersion: String?) -> Available? {
        guard isCheckable(currentVersion: currentVersion), let current = currentVersion.flatMap(SemVer.init),
              !release.draft, !release.prerelease,
              let latest = SemVer(release.tagName), current < latest,
              let url = URL(string: release.htmlURL), url.scheme == "https"
        else { return nil }
        return Available(version: latest.description, url: url)
    }

    /// Interprets an HTTP response: 404 means there is no release yet,
    /// which is not an error.
    public static func evaluate(statusCode: Int, body: Data, currentVersion: String?) throws -> Available? {
        switch statusCode {
        case 200:
            let release = try JSONDecoder().decode(Release.self, from: body)
            return newer(release: release, currentVersion: currentVersion)
        case 404:
            return nil
        default:
            throw UpdateCheckError.http(statusCode)
        }
    }

    public enum UpdateCheckError: Error, Equatable {
        case http(Int)
    }
}

/// Whether the daemon must be re-registered because it is a different
/// version from the app (아키텍처 16장, 제안): the app bundle was replaced
/// while the old farerod kept running.
public enum DaemonVersionCheck {
    public static func needsReregistration(appVersion: String?, daemonVersion: String) -> Bool {
        guard UpdateCheck.isCheckable(currentVersion: appVersion), let app = appVersion,
              !daemonVersion.isEmpty, daemonVersion != "dev" else { return false }
        return normalize(app) != normalize(daemonVersion)
    }

    static func normalize(_ v: String) -> String {
        if let s = SemVer(v) { return s.description }
        return v.hasPrefix("v") ? String(v.dropFirst()) : v
    }
}

/// Whether the LaunchAgent must be registered again because the farerod it
/// runs is not the one this bundle ships. With ad-hoc signing (Q47) the
/// background item is tied to the executable's code hash: replacing the app
/// bundle (an update or a rebuild) invalidates it, and launchd then fails to
/// start farerod ("needs LWCR update", EX_CONFIG; M0 2026-10-06). The daemon
/// never comes up to report its version, so DaemonVersionCheck cannot catch
/// this.
public enum DaemonRegistrationCheck {
    /// The registered farerod: its path (moving the app changes it) and its
    /// code directory hash.
    public static func identity(executablePath: String, cdhash: Data) -> String {
        executablePath + "#" + cdhash.map { String(format: "%02x", $0) }.joined()
    }

    /// A missing record means a build that did not record it registered the
    /// agent, so it is registered once more. An unreadable signature
    /// (`current` nil) keeps the registration as it is.
    public static func needsReregistration(recorded: String?, current: String?) -> Bool {
        guard let current else { return false }
        return recorded != current
    }
}
