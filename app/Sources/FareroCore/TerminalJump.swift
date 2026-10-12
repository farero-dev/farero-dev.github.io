import Foundation

/// The pure parts of the terminal jump (기능 명세서 F-11, scenario E): which
/// tty may name a Terminal.app tab, the AppleScript that brings that tab to
/// the front, and what each result means for the user. The app
/// (TerminalJumper) settles the Automation permission and runs the script.
public enum TerminalJump {
    public enum Outcome: Sendable, Equatable {
        case jumped
        case notFound
        case failed(String)

        /// What the notch shows; nil when the jump worked.
        public var message: String? {
            switch self {
            case .jumped: nil
            case .notFound: TerminalJump.notFoundMessage
            case .failed(let m): m
            }
        }
    }

    public static let terminalBundleID = "com.apple.Terminal"
    public static let notFoundMessage = "터미널 창을 찾을 수 없음"
    public static let notPermittedMessage = "터미널 제어 권한이 없음 (시스템 설정 > 개인정보 보호 및 보안 > 자동화)"
    public static let notRespondingMessage = "터미널이 응답하지 않음"
    /// How long the script waits for each reply from Terminal, so a hung
    /// Terminal cannot hold the main thread for AppleScript's default two
    /// minutes.
    public static let timeoutSeconds = 3

    // OSStatus / AppleScript error numbers (MacErrors.h).
    static let procNotFound = -600
    static let connectionInvalid = -609
    static let errAETimeout = -1712
    static let errAEEventNotPermitted = -1743
    static let errAEEventWouldRequireUserConsent = -1744

    /// The device name in `tty`, or nil when it is not one. farerod reports
    /// what `ps -o tty=` prints for the agent ("ttys019"); a "/dev/" prefix
    /// is tolerated. Only "tty" followed by ASCII letters and digits passes,
    /// so nothing farerod relays can end the AppleScript string literal.
    public static func deviceName(_ tty: String) -> String? {
        var name = Substring(tty)
        if name.hasPrefix("/dev/") { name = name.dropFirst("/dev/".count) }
        guard name.hasPrefix("tty"), name.utf8.count > 3, name.utf8.count <= 32 else { return nil }
        let ok = name.unicodeScalars.allSatisfy { s in
            ("a"..."z").contains(s) || ("A"..."Z").contains(s) || ("0"..."9").contains(s)
        }
        return ok ? String(name) : nil
    }

    /// The script that finds the tab whose tty is `/dev/<tty>` and brings it
    /// to the front; it returns "ok" or "notfound". nil when `tty` is not a
    /// device name.
    ///
    /// - Terminal is never launched for this: a Terminal that is not running
    ///   has no tab to find.
    /// - `get every window` yields `window id N` references, which keep
    ///   pointing at the same window when it moves to the front (an index
    ///   reference from `repeat with w in windows` would not).
    /// - All ttys of a window come in one Apple event.
    /// - Terminal activates first: while inactive it ignores `set index`
    ///   (seen on macOS 26, Terminal 2.15), and activating it afterwards
    ///   left its previous front window on top. Then the window is
    ///   un-minimized (a no-op otherwise), raised, and the tab selected.
    public static func script(tty: String) -> String? {
        guard let name = deviceName(tty) else { return nil }
        return """
        if application id "\(terminalBundleID)" is not running then return "notfound"
        with timeout of \(timeoutSeconds) seconds
            tell application id "\(terminalBundleID)"
                repeat with windowRef in (get every window)
                    set w to contents of windowRef
                    set ttys to {}
                    try
                        set ttys to tty of every tab of w
                    on error errText number n
                        -- A window without tabs; anything else (a timeout) stops the search.
                        if n is not -1728 then error errText number n
                    end try
                    repeat with k from 1 to count of ttys
                        if item k of ttys is "/dev/\(name)" then
                            activate
                            set miniaturized of w to false
                            set index of w to 1
                            set selected of tab k of w to true
                            return "ok"
                        end if
                    end repeat
                end repeat
            end tell
        end timeout
        return "notfound"
        """
    }

    /// What `AEDeterminePermissionToAutomateTarget` (asking the user if
    /// needed) means for the jump: nil to go ahead and run the script.
    public static func outcome(permissionStatus status: Int) -> Outcome? {
        switch status {
        case 0: nil
        case errAEEventNotPermitted, errAEEventWouldRequireUserConsent: .failed(notPermittedMessage)
        case procNotFound: .notFound // Terminal quit meanwhile
        default: .failed("터미널 제어 권한을 확인하지 못함 (오류 \(status))")
        }
    }

    /// What the script's result, or its error, means for the user.
    public static func outcome(scriptResult: String?, errorNumber: Int?, errorMessage: String? = nil) -> Outcome {
        guard let n = errorNumber else { return scriptResult == "ok" ? .jumped : .notFound }
        switch n {
        case errAEEventNotPermitted, errAEEventWouldRequireUserConsent: return .failed(notPermittedMessage)
        case errAETimeout: return .failed(notRespondingMessage)
        case procNotFound, connectionInvalid: return .notFound // Terminal quit meanwhile
        default:
            let detail = errorMessage.flatMap { $0.isEmpty ? nil : $0 } ?? "AppleScript 오류 \(n)"
            return .failed("터미널로 이동하지 못함: \(detail)")
        }
    }
}
