import Foundation
import Testing
@testable import FareroCore

@Suite("Terminal jump")
struct TerminalJumpTests {
    @Test func acceptsOnlyDeviceNames() {
        #expect(TerminalJump.deviceName("ttys019") == "ttys019")
        #expect(TerminalJump.deviceName("/dev/ttys019") == "ttys019")
        #expect(TerminalJump.deviceName("ttyp0") == "ttyp0")
        for bad in ["", "??", "console", "tty", "/dev/", "s019", " ttys019", "ttys019\n",
                    "ttys019\" & (do shell script \"true\") & \"", "ttys 019", "ttys0/19",
                    "ttys٠١٩", "ｔｔｙs019", "tty" + String(repeating: "9", count: 40)] {
            #expect(TerminalJump.deviceName(bad) == nil, "\(bad)")
        }
    }

    @Test func scriptFindsTheTabByTTY() throws {
        #expect(TerminalJump.script(tty: "") == nil)
        #expect(TerminalJump.script(tty: "ttys019\"") == nil)
        let s = try #require(TerminalJump.script(tty: "ttys019"))
        #expect(s.contains(#"if item k of ttys is "/dev/ttys019" then"#))
        #expect(s.contains(#"tell application id "com.apple.Terminal""#))
        // Never launches Terminal, and a hung Terminal gets 3 seconds.
        #expect(s.hasPrefix(#"if application id "com.apple.Terminal" is not running then return "notfound""#))
        #expect(s.contains("with timeout of 3 seconds"))
        // Stable window references, not indexes that shift when one is raised.
        #expect(s.contains("repeat with windowRef in (get every window)"))
        // Activate, un-minimize, raise, then select the tab.
        let steps = ["activate", "set miniaturized of w to false", "set index of w to 1",
                     "set selected of tab k of w to true", #"return "ok""#]
        let positions = try steps.map { step in try #require(s.range(of: step)).lowerBound }
        #expect(positions == positions.sorted())
    }

    /// The generated source is valid AppleScript for Terminal's dictionary
    /// (compiling reads the dictionary; it sends Terminal nothing).
    @MainActor
    @Test func scriptCompiles() throws {
        let source = try #require(TerminalJump.script(tty: "ttys019"))
        let script = try #require(NSAppleScript(source: source))
        var error: NSDictionary?
        let compiled = script.compileAndReturnError(&error)
        #expect(compiled, "\(String(describing: error))")
    }

    @Test func permissionOutcomes() {
        #expect(TerminalJump.outcome(permissionStatus: 0) == nil)
        #expect(TerminalJump.outcome(permissionStatus: -1743) == .failed(TerminalJump.notPermittedMessage))
        #expect(TerminalJump.outcome(permissionStatus: -1744) == .failed(TerminalJump.notPermittedMessage))
        #expect(TerminalJump.outcome(permissionStatus: -600) == .notFound)
        #expect(TerminalJump.outcome(permissionStatus: -50) == .failed("터미널 제어 권한을 확인하지 못함 (오류 -50)"))
    }

    @Test func scriptOutcomes() {
        #expect(TerminalJump.outcome(scriptResult: "ok", errorNumber: nil) == .jumped)
        #expect(TerminalJump.outcome(scriptResult: "notfound", errorNumber: nil) == .notFound)
        #expect(TerminalJump.outcome(scriptResult: nil, errorNumber: nil) == .notFound)
        #expect(TerminalJump.outcome(scriptResult: nil, errorNumber: -1743) == .failed(TerminalJump.notPermittedMessage))
        #expect(TerminalJump.outcome(scriptResult: nil, errorNumber: -1712) == .failed("터미널이 응답하지 않음"))
        #expect(TerminalJump.outcome(scriptResult: nil, errorNumber: -609) == .notFound)
        #expect(TerminalJump.outcome(scriptResult: nil, errorNumber: -2700, errorMessage: "Boom")
            == .failed("터미널로 이동하지 못함: Boom"))
        #expect(TerminalJump.outcome(scriptResult: nil, errorNumber: -2700, errorMessage: "")
            == .failed("터미널로 이동하지 못함: AppleScript 오류 -2700"))
    }

    @Test func messages() {
        #expect(TerminalJump.Outcome.jumped.message == nil)
        #expect(TerminalJump.Outcome.notFound.message == "터미널 창을 찾을 수 없음")
        #expect(TerminalJump.Outcome.failed("x").message == "x")
    }
}
