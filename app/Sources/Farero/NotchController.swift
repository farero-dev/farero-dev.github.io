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
    /// Hover tracking. It runs only while the island is visible: with the
    /// island hidden nothing can be hovered, and farero keeps no timer or
    /// event monitor alive then (기능 명세서 F-12, Q80: 0% CPU while hidden).
    private var pointerMonitors: [Any] = []
    private var pollTimer: Timer?
    /// Closes a pinned island on a click elsewhere; installed only while pinned.
    private var outsideClickMonitor: Any?
    /// Takes the panel off screen once the hide animation has played.
    private var orderOutWork: DispatchWorkItem?
    private var screenObserver: NSObjectProtocol?
    private var loggedMode: NotchMode?
    private var loggedIsland: CGRect?

    init(model: AppModel) {
        self.model = model
        panel = NotchPanel(contentRect: CGRect(origin: .zero, size: NotchMetrics.default.panelSize))
        let root = NotchRootView(model: model) { [weak self] frame in
            self?.islandFrameInView = frame
            self?.logIsland(frame)
            self?.updatePointer()
        }
        let host = FirstMouseHostingView(rootView: root)
        host.frame = CGRect(origin: .zero, size: NotchMetrics.default.panelSize)
        host.autoresizingMask = [.width, .height]
        panel.contentView = host
    }

    func start() {
        layout()
        screenObserver = NotificationCenter.default.addObserver(
            forName: NSApplication.didChangeScreenParametersNotification, object: nil, queue: .main
        ) { [weak self] _ in
            MainActor.assumeIsolated { self?.layout() }
        }
        model.onChange.append { [weak self] in self?.stateChanged() }
        stateChanged()
    }

    private func stateChanged() {
        if model.mode != loggedMode {
            loggedMode = model.mode
            model.debugLog("notch \(model.mode)")
        }
        if model.mode == .hidden {
            stopPointerTracking()
            scheduleOrderOut()
        } else {
            orderOutWork?.cancel()
            orderOutWork = nil
            if !panel.isVisible {
                panel.orderFrontRegardless()
                model.debugLog("panel ordered front")
            }
            startPointerTracking()
        }
        setOutsideClickMonitor(model.pinned)
        updatePointer()
    }

    /// FARERO_DEBUG=1: the island's laid-out frame in screen coordinates
    /// (top-left origin, as `screencapture -R` takes it), for scripts/e2e-notch.py.
    private func logIsland(_ frame: CGRect) {
        let f = CGRect(x: (panel.frame.minX + frame.minX).rounded(),
                       y: ((NSScreen.screens.first?.frame.maxY ?? 0) - panel.frame.maxY + frame.minY).rounded(),
                       width: frame.width.rounded(), height: frame.height.rounded())
        guard f != loggedIsland else { return }
        loggedIsland = f
        model.debugLog("island \(Int(f.minX)),\(Int(f.minY)) \(Int(f.width))x\(Int(f.height))")
    }

    private func startPointerTracking() {
        guard pointerMonitors.isEmpty else { return }
        // Mouse-moved monitors need no permission (only key monitors do).
        if let m = NSEvent.addGlobalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged], handler: { [weak self] _ in
            MainActor.assumeIsolated { self?.updatePointer() }
        }) { pointerMonitors.append(m) }
        if let m = NSEvent.addLocalMonitorForEvents(matching: [.mouseMoved, .leftMouseDragged], handler: { [weak self] event in
            MainActor.assumeIsolated { self?.updatePointer() }
            return event
        }) { pointerMonitors.append(m) }
        // Safety net for pointer exits no event reports (e.g. Mission Control).
        pollTimer = Timer.scheduledTimer(withTimeInterval: 0.5, repeats: true) { [weak self] _ in
            MainActor.assumeIsolated { self?.updatePointer() }
        }
    }

    private func stopPointerTracking() {
        for m in pointerMonitors { NSEvent.removeMonitor(m) }
        pointerMonitors = []
        pollTimer?.invalidate()
        pollTimer = nil
    }

    private func setOutsideClickMonitor(_ on: Bool) {
        if on, outsideClickMonitor == nil {
            outsideClickMonitor = NSEvent.addGlobalMonitorForEvents(matching: [.leftMouseDown, .rightMouseDown]) { [weak self] _ in
                MainActor.assumeIsolated { self?.model.unpin() }
            }
        } else if !on, let m = outsideClickMonitor {
            NSEvent.removeMonitor(m)
            outsideClickMonitor = nil
        }
    }

    /// Orders the panel out after the island has faded or slid away, so a
    /// hidden notch leaves no window on screen either.
    private func scheduleOrderOut() {
        guard panel.isVisible, orderOutWork == nil else { return }
        let work = DispatchWorkItem { [weak self] in
            MainActor.assumeIsolated {
                guard let self else { return }
                self.orderOutWork = nil
                if self.model.mode == .hidden {
                    self.panel.orderOut(nil)
                    self.model.debugLog("panel ordered out")
                }
            }
        }
        orderOutWork = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 1, execute: work)
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
