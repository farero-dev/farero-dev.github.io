import AppKit

/// Brings the Terminal.app tab running a session to the front (기능 명세서 F-11).
@MainActor
enum TerminalJumper {
    enum Outcome: Equatable {
        case jumped
        case notFound
        case failed(String)
    }

    static let notFoundMessage = "터미널 창을 찾을 수 없음"

    /// Finds the Terminal tab whose tty is `/dev/<tty>` and brings its window
    /// and tab to the front. Terminal is never launched for this.
    static func jump(tty: String) -> Outcome {
        // The tty comes from farerod; allow only a device name in the script.
        guard !tty.isEmpty, tty.allSatisfy({ $0.isASCII && ($0.isLetter || $0.isNumber) }) else { return .notFound }
        guard !NSRunningApplication.runningApplications(withBundleIdentifier: "com.apple.Terminal").isEmpty else {
            return .notFound
        }
        let source = """
        tell application id "com.apple.Terminal"
            repeat with w in windows
                repeat with t in tabs of w
                    if tty of t is "/dev/\(tty)" then
                        set selected of t to true
                        set index of w to 1
                        activate
                        return "ok"
                    end if
                end repeat
            end repeat
        end tell
        return "notfound"
        """
        guard let script = NSAppleScript(source: source) else { return .failed("스크립트를 만들 수 없음") }
        var error: NSDictionary?
        let result = script.executeAndReturnError(&error)
        if let error {
            let code = error[NSAppleScript.errorNumber] as? Int ?? 0
            if code == -1743 {
                return .failed("터미널 제어 권한이 없음 (시스템 설정 > 개인정보 보호 및 보안 > 자동화)")
            }
            return .failed(error[NSAppleScript.errorMessage] as? String ?? "AppleScript 오류 \(code)")
        }
        return result.stringValue == "ok" ? .jumped : .notFound
    }
}
