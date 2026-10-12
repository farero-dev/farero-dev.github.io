import AppKit
import FareroCore

/// Wires the app together: an accessory app (no Dock icon, never activates
/// on its own) with a status item and the notch panel.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let model = AppModel()
    private var notch: NotchController?
    private var statusItem: StatusItemController?
    private var windows: WindowManager?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let windows = WindowManager(model: model)
        self.windows = windows
        statusItem = StatusItemController(model: model, windows: windows)
        let notch = NotchController(model: model)
        self.notch = notch
        notch.start()
        model.start()

        // Register farerod as a LaunchAgent (skipped in development runs).
        model.registrar.registerIfNeeded { [model] in
            model.registrationLabel = model.registrar.status.label
        }
        model.registrationLabel = model.registrar.status.label
        windows.showOnboardingIfFirstLaunch()
        windows.openDebugWindows()
    }

    /// The notch and its cards never take focus (기능 명세서 5-3); only the
    /// menu's windows activate the app. scripts/e2e-notch.py reads this line.
    func applicationDidBecomeActive(_ notification: Notification) {
        model.debugLog("app became active")
    }

    func applicationWillTerminate(_ notification: Notification) {
        model.stop()
    }
}

let app = NSApplication.shared
let delegate = AppDelegate()
app.delegate = delegate
app.setActivationPolicy(.accessory)
withExtendedLifetime(delegate) {
    app.run()
}
