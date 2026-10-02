import AppKit
import FareroCore

/// Wires the app together: an accessory app (no Dock icon, never activates
/// on its own) with a status item and the notch panel.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate {
    private let model = AppModel()
    private let registrar = DaemonRegistrar()
    private var notch: NotchController?
    private var statusItem: StatusItemController?
    private var windows: WindowManager?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let windows = WindowManager(model: model, registrar: registrar)
        self.windows = windows
        statusItem = StatusItemController(model: model, windows: windows, registrar: registrar)
        let notch = NotchController(model: model)
        self.notch = notch
        notch.start()
        model.start()

        // Register farerod as a LaunchAgent (skipped in development runs).
        registrar.registerIfNeeded()
        model.registrationLabel = registrar.status.label
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
