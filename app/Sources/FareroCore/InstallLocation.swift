import Foundation

/// Where the app bundle runs from. A quarantined download opened straight
/// from ~/Downloads runs from a random, read-only copy that Gatekeeper's App
/// Translocation makes under `.../AppTranslocation/<uuid>/d/`; the copy is
/// gone the next time, so nothing may be registered by that path: not the
/// LaunchAgent (farerod would vanish) and not the Claude Code hook (Q63
/// would rewrite it to the copy). The check is by path, so it does not
/// depend on the private Security API; `translocated` is final either way.
public enum InstallLocation: Sendable, Equatable {
    case translocated
    /// /Applications or ~/Applications (any depth).
    case applications
    /// A working location that may still move (build/Farero.app, a
    /// dequarantined download): Q63 fixes paths after a move.
    case elsewhere

    public static func classify(bundlePath: String, home: URL) -> InstallLocation {
        let path = URL(fileURLWithPath: bundlePath).standardizedFileURL.path
        if path.contains("/AppTranslocation/") { return .translocated }
        let homeApps = home.standardizedFileURL.appendingPathComponent("Applications").path
        if path.hasPrefix("/Applications/") || path.hasPrefix(homeApps + "/") { return .applications }
        return .elsewhere
    }
}
