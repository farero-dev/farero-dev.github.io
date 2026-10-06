import Foundation

/// farerod's user LaunchAgent, `~/Library/LaunchAgents/dev.farero.farerod.plist`
/// (Q55 changed by the M0 result of 2026-10-06). SMAppService ties the
/// background item to an ad-hoc binary's code hash (Q47), so after an app
/// update launchd refused the new farerod ("Launch Constraint Violation") and
/// registering again did not help. A plain plist runs farerod by path, so a
/// replaced farerod starts.
public enum LaunchAgent {
    public static let label = "dev.farero.farerod"

    public static func plistURL(home: URL) -> URL {
        home.appendingPathComponent("Library/LaunchAgents/\(label).plist")
    }

    static func plist(farerodPath: String) -> [String: Any] {
        [
            "Label": label,
            "ProgramArguments": [farerodPath],
            "RunAtLoad": true,
            "KeepAlive": true,
            "ProcessType": "Interactive",
        ]
    }

    public static func encode(farerodPath: String) throws -> Data {
        try PropertyListSerialization.data(fromPropertyList: plist(farerodPath: farerodPath), format: .xml, options: 0)
    }

    /// Whether the plist on disk must be written: it is missing, unreadable,
    /// or runs a farerod at another path (the app moved, Q63).
    public static func needsWrite(existing: Data?, farerodPath: String) -> Bool {
        guard let existing,
              let current = try? PropertyListSerialization.propertyList(from: existing, format: nil) as? NSDictionary
        else { return true }
        return !current.isEqual(to: plist(farerodPath: farerodPath))
    }
}
