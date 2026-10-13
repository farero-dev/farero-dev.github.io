import Foundation
import Testing
@testable import FareroCore

@Suite("App login item")
struct AppLoginItemTests {
    let exe = "/Applications/Farero.app/Contents/MacOS/Farero"

    private func decode(_ data: Data) throws -> [String: Any] {
        try #require(try PropertyListSerialization.propertyList(from: data, format: nil) as? [String: Any])
    }

    @Test func runsTheAppExecutableOnceAtLogin() throws {
        let p = try decode(try AppLoginItem.encode(executablePath: exe))
        #expect(p["Label"] as? String == "dev.farero.app")
        #expect(p["ProgramArguments"] as? [String] == [exe, "--open-at-login"])
        #expect(p["RunAtLoad"] as? Bool == true)
        #expect(p["LimitLoadToSessionType"] as? String == "Aqua")
        #expect(p["ProcessType"] as? String == "Interactive")
        // Quitting the app lasts until the next login.
        #expect(p["KeepAlive"] == nil)
        // SMAppService keys would tie the job to the bundle's signature.
        #expect(p["BundleProgram"] == nil)
    }

    @Test func differsFromFarerodsAgent() {
        #expect(AppLoginItem.label != LaunchAgent.label)
        let home = URL(fileURLWithPath: "/Users/me")
        #expect(AppLoginItem.plistURL(home: home).path == "/Users/me/Library/LaunchAgents/dev.farero.app.plist")
        #expect(AppLoginItem.plistURL(home: home) != LaunchAgent.plistURL(home: home))
    }

    @Test func onUnlessTurnedOff() {
        #expect(AppLoginItem.isEnabled(stored: nil))
        #expect(AppLoginItem.isEnabled(stored: true))
        #expect(!AppLoginItem.isEnabled(stored: false))
        #expect(AppLoginItem.isEnabled(stored: "garbage"))
    }

    @Test func writesWhenOnAndMissingOrDifferent() throws {
        let current = try AppLoginItem.encode(executablePath: exe)
        #expect(AppLoginItem.change(enabled: true, existing: nil, executablePath: exe) == .write)
        #expect(AppLoginItem.change(enabled: true, existing: current, executablePath: exe) == .none)
        // The app moved (Q63).
        #expect(AppLoginItem.change(enabled: true, existing: current,
                                    executablePath: "/Users/me/Applications/Farero.app/Contents/MacOS/Farero") == .write)
        #expect(AppLoginItem.change(enabled: true, existing: Data("not a plist".utf8), executablePath: exe) == .write)
        #expect(AppLoginItem.change(enabled: true, existing: Data(), executablePath: exe) == .write)
    }

    @Test func removesWhenOff() throws {
        let current = try AppLoginItem.encode(executablePath: exe)
        #expect(AppLoginItem.change(enabled: false, existing: current, executablePath: exe) == .remove)
        #expect(AppLoginItem.change(enabled: false, existing: Data(), executablePath: exe) == .remove)
        #expect(AppLoginItem.change(enabled: false, existing: nil, executablePath: exe) == .none)
    }

    @Test func recognizesOnlyTheLaunchdRun() {
        #expect(AppLoginItem.isLoginLaunch(arguments: [exe, "--open-at-login"]))
        #expect(!AppLoginItem.isLoginLaunch(arguments: [exe]))
        #expect(!AppLoginItem.isLoginLaunch(arguments: ["--open-at-login"]))
        #expect(!AppLoginItem.isLoginLaunch(arguments: [exe, "-NSDocumentRevisionsDebugMode", "YES"]))
    }

    @Test func handsOverToLaunchServicesInTheBackground() {
        let bundle = "/Applications/Farero 앱.app"
        #expect(AppLoginItem.openArguments(bundlePath: bundle) == ["-g", bundle])
    }
}
