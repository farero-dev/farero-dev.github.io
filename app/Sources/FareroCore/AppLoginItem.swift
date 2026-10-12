import Foundation

/// The app's own user LaunchAgent, `~/Library/LaunchAgents/dev.farero.app.plist`,
/// which opens Farero.app when the user logs in (Q85). farerod starts at
/// login on its own (`LaunchAgent`), but approval cards need the app: while
/// it is not running, gateway calls that need approval are auto-denied
/// (기능 명세서 6-2). It is on unless the user turns it off.
///
/// A plain plist by path, like farerod's: SMAppService items are tied to an
/// ad-hoc binary's code hash (Q47, Q55). launchd runs the app's executable
/// with `argument`, and that process only hands over to `/usr/bin/open`, so
/// LaunchServices starts the app as if the user had opened it and does not
/// start a second one when it already runs. No KeepAlive: quitting the app
/// lasts until the next login.
public enum AppLoginItem {
    public static let label = "dev.farero.app"
    /// Marks the launchd run that hands over to LaunchServices.
    public static let argument = "--open-at-login"
    /// UserDefaults key of the user's choice; absent means on.
    public static let defaultsKey = "openAtLogin"

    public static func plistURL(home: URL) -> URL {
        home.appendingPathComponent("Library/LaunchAgents/\(label).plist")
    }

    static func plist(executablePath: String) -> [String: Any] {
        [
            "Label": label,
            "ProgramArguments": [executablePath, argument],
            "RunAtLoad": true,
            "LimitLoadToSessionType": "Aqua",
            "ProcessType": "Interactive",
        ]
    }

    public static func encode(executablePath: String) throws -> Data {
        try PropertyListSerialization.data(fromPropertyList: plist(executablePath: executablePath), format: .xml, options: 0)
    }

    /// The user's choice as stored in UserDefaults.
    public static func isEnabled(stored: Any?) -> Bool {
        stored as? Bool ?? true
    }

    public enum Change: Equatable {
        case none
        case write
        case remove
    }

    /// What to do so that the plist on disk (`existing`, nil when there is
    /// none) matches the choice: write it when it is missing, unreadable or
    /// runs an app at another path (the app moved, Q63), remove it when off.
    public static func change(enabled: Bool, existing: Data?, executablePath: String) -> Change {
        guard enabled else { return existing == nil ? .none : .remove }
        guard let existing,
              let current = try? PropertyListSerialization.propertyList(from: existing, format: nil) as? NSDictionary
        else { return .write }
        return current.isEqual(to: plist(executablePath: executablePath)) ? .none : .write
    }

    /// Whether this process is the launchd run at login.
    public static func isLoginLaunch(arguments: [String]) -> Bool {
        arguments.dropFirst().contains(argument)
    }

    /// `/usr/bin/open` arguments for the hand-over: open the bundle without
    /// bringing it to the front.
    public static func openArguments(bundlePath: String) -> [String] {
        ["-g", bundlePath]
    }
}
