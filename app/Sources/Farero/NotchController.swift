import AppKit
import FareroCore
import SwiftUI

/// Places the notch panel on one screen and tracks the pointer over the
/// island (기능 명세서 F-01).
///
/// The panel has a fixed size and is transparent; only the black island at
/// its top is visible. Mouse events are ignored everywhere except over the
/// island, so the rest of the panel never blocks clicks on the menu bar or
/// windows below it.
@MainActor
final class NotchController {
    private let model: AppModel
    private let panel: NotchPanel
    private var islandFrameInView: CGRect = .zero
    private var monitors: [Any] = []
    private var pollTimer: Timer?
    private var screenObserver: NSObjectProtocol?

    init(model: AppModel) {
        self.model = model
        panel = NotchPanel(contentRect: CGRect(origin: .zero, size: NotchMetrics.default.panelSize))
        let root = NotchRootView(model: model) { [weak self] frame in
            self?.islandFrameInView = frame
            self?.updatePointer()
        }
        let host = FirstMouseHostingView(rootView: root)
        host.frame = CGRect(origin: .zero, size: NotchMetrics.default.panelSize)
        host.autoresizingMask = [.width, .height]
        panel.contentView = host
    }

    func start() {
        layout()
        panel.orderFrontRegardless()
        screenObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.layout() }
        }
        // Mouse-moved monitors need no permission (only key monitors do).
        if let m = NSEvent.addGlobalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged], handler: { [weak self] _ in
            MainActor.assumeIsolated { self?.updatePointer() }
        }) { monitors.append(m) }
        if let m = NSEvent.addLocalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged], handler: { [weak self] event in
            MainActor.assumeIsolated { self?.updatePointer() }
            return event
        }) { monitors.append(m) }
        // A click anywhere else closes a pinned island.
        if let m = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown], handler: { [weak self] _ in
            MainActor.assumeIsolated { self?.model.unpin() }
        }) { monitors.append(m) }
        // Safety net for pointer exits no event reports (e.g. Mission Control).
        pollTimer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.updatePointer() }
        }
        model.onChange.append { [weak self] in self?.stateChanged() }
    }

    private func stateChanged() {
        if model.mode != .hidden, !panel.isVisible { panel.orderFrontRegardless() }
        updatePointer()
    }

    /// The screen to use: the built-in notched screen if there is one,
    /// otherwise the primary screen.
    static func targetScreen() -> NSScreen? {
        NSScreen.screens.first { $0.safeAreaInsets.top > 0 && $0.auxiliaryTopLeftArea != nil }
            ?? NSScreen.screens.first
    }

    func layout() {
        guard let screen = Self.targetScreen() else { return }
        // FARERO_DEBUG_NO_NOTCH=1 lays out as on a screen without a notch.
        let ignoreNotch = ProcessInfo.processInfo.environment["FARERO_DEBUG_NO_NOTCH"] == "1"
        let anchor = NotchGeometry.anchorRect(
            screenFrame: screen.frame, visibleFrame: screen.visibleFrame,
            safeAreaTop: ignoreNotch ? 0 : screen.safeAreaInsets.top,
            auxiliaryTopLeft: screen.auxiliaryTopLeftArea, auxiliaryTopRight: screen.auxiliaryTopRightArea)
        let hasNotch = anchor.width > 0
        let size = NotchMetrics.default.panelSize
        let frame = NotchGeometry.panelFrame(anchor: anchor, screenFrame: screen.frame, size: size)
        model.metrics = NotchMetrics(notchSize: anchor.size, hasNotch: hasNotch, panelSize: size)
        panel.setFrame(frame, display: true)
        updatePointer()
    }

    /// The island in screen coordinates, extended up to the top edge so the
    /// pointer pushed against the top of the screen still counts.
    private var islandScreenRect: CGRect {
        let f = islandFrameInView
        guard f.width > 0, f.height > 0 else { return .null }
        let pf = panel.frame
        return CGRect(x: pf.minX + f.minX, y: pf.maxY - f.maxY, width: f.width, height: f.height + 4)
    }

    private func updatePointer() {
        let visible = model.mode != .hidden
        let rect = islandScreenRect
        let inside = visible && !rect.isNull && rect.insetBy(dx: -2, dy: -2).contains(NSEvent.mouseLocation)
        if panel.ignoresMouseEvents == inside { panel.ignoresMouseEvents = !inside }
        model.pointerInside(inside)
    }
}
