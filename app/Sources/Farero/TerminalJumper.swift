import AppKit
import FareroCore

/// Brings the Terminal.app tab running a session to the front (기능 명세서 F-11).
///
/// The Automation permission is settled first, off the main thread: asking
/// shows the system consent dialog and blocks until the user answers. Only
/// then does the script run, on the main thread (NSAppleScript requires it),
/// with `TerminalJump.timeoutSeconds` for each reply from Terminal.
@MainActor
enum TerminalJumper {
    typealias Outcome = TerminalJump.Outcome

    /// Finds the Terminal tab whose tty is `/dev/<tty>` and brings its window
    /// and tab to the front. Terminal is never launched for this (the
    /// permission check and the script both stop if it is not running).
    /// `log` gets one line per step (development logging).
    static func jump(tty: String, log: (String) -> Void = { _ in }) async -> Outcome {
        // The tty comes from farerod; the script takes only a device name.
        // Whether Terminal runs is left to the permission check (-600) and
        // the script's first line: NSRunningApplication once said it did not
        // while its windows were open.
        guard let source = TerminalJump.script(tty: tty) else {
            log("terminal: \(tty) is not a device name")
            return .notFound
        }
        let start = Date()
        let status = await automationPermission()
        log("terminal permission \(status) after \(elapsed(since: start))")
        if let outcome = TerminalJump.outcome(permissionStatus: Int(status)) {
            return outcome
        }
        let outcome = run(source, log: log)
        log("terminal script done after \(elapsed(since: start))")
        return outcome
    }

    private static func elapsed(since start: Date) -> String {
        String(format: "%.2f s", Date().timeIntervalSince(start))
    }

    /// `AEDeterminePermissionToAutomateTarget` for Terminal, asking the user
    /// if they have not decided yet. It blocks while the consent dialog is
    /// up, so it runs on a global queue rather than the main thread or a
    /// Swift concurrency thread.
    nonisolated private static func automationPermission() async -> OSStatus {
        await withCheckedContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let target = NSAppleEventDescriptor(bundleIdentifier: TerminalJump.terminalBundleID)
                let status = withExtendedLifetime(target) {
                    AEDeterminePermissionToAutomateTarget(target.aeDesc, AEEventClass(typeWildCard),
                                                          AEEventID(typeWildCard), true)
                }
                continuation.resume(returning: status)
            }
        }
    }

    private static func run(_ source: String, log: (String) -> Void) -> Outcome {
        guard let script = NSAppleScript(source: source) else { return .failed("스크립트를 만들 수 없음") }
        var error: NSDictionary?
        let result = script.executeAndReturnError(&error)
        guard let error else {
            log("terminal script result \(result.stringValue ?? "nil")")
            return TerminalJump.outcome(scriptResult: result.stringValue, errorNumber: nil)
        }
        let number = error[NSAppleScript.errorNumber] as? Int ?? 0
        let message = error[NSAppleScript.errorMessage] as? String
        log("terminal script error \(number) \(message ?? "")")
        return TerminalJump.outcome(scriptResult: nil, errorNumber: number, errorMessage: message)
    }
}
