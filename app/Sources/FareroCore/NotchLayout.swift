import CoreGraphics
import Foundation

/// The notch panel's display states (기능 명세서 F-01), after coucou's
/// hidden → petit → home.
public enum NotchMode: Sendable, Equatable {
    /// No live session and no pending approval: nothing is drawn.
    case hidden
    /// Sessions in progress: a thin strip around the notch with the character.
    case compact
    /// Hovered or clicked: the session list grows below the notch.
    case expanded
    /// An approval is pending: the card stack grows below the notch.
    case approval

    /// Chooses the mode. An approval always shows; otherwise hovering or a
    /// click (pinned) expands a visible notch.
    public static func resolve(hasActiveSessions: Bool, pendingApprovals: Int,
                               hovering: Bool, pinned: Bool) -> NotchMode {
        if pendingApprovals > 0 { return .approval }
        guard hasActiveSessions else { return .hidden }
        return hovering || pinned ? .expanded : .compact
    }

    public static func resolve(_ state: AppState, hovering: Bool, pinned: Bool) -> NotchMode {
        resolve(hasActiveSessions: !state.activeSessions.isEmpty, pendingApprovals: state.approvals.count,
                hovering: hovering, pinned: pinned)
    }
}

/// Where the hardware notch is, from `NSScreen` values. Pure so it can be
/// tested without a notched display.
public enum NotchGeometry {
    /// The notch rectangle in the same (global) coordinates as `screenFrame`,
    /// or nil when the screen has no notch.
    ///
    /// - Parameters:
    ///   - screenFrame: `NSScreen.frame`.
    ///   - safeAreaTop: `NSScreen.safeAreaInsets.top`; 0 without a notch.
    ///   - auxiliaryTopLeft: `NSScreen.auxiliaryTopLeftArea`, the usable menu
    ///     bar area left of the notch.
    ///   - auxiliaryTopRight: `NSScreen.auxiliaryTopRightArea`.
    ///
    /// Only the widths of the auxiliary areas are used, so the result does not
    /// depend on which coordinate space they are reported in.
    public static func notchRect(screenFrame: CGRect, safeAreaTop: CGFloat,
                                 auxiliaryTopLeft: CGRect?, auxiliaryTopRight: CGRect?) -> CGRect? {
        guard safeAreaTop > 0, let left = auxiliaryTopLeft, let right = auxiliaryTopRight else { return nil }
        let width = screenFrame.width - left.width - right.width
        guard width > 0, left.width > 0, right.width > 0 else { return nil }
        return CGRect(x: screenFrame.minX + left.width, y: screenFrame.maxY - safeAreaTop,
                      width: width, height: safeAreaTop)
    }

    /// The anchor the panel hangs from: the notch, or for a screen without one
    /// a zero-width point at the top centre with the menu bar's height.
    public static func anchorRect(screenFrame: CGRect, visibleFrame: CGRect, safeAreaTop: CGFloat,
                                  auxiliaryTopLeft: CGRect?, auxiliaryTopRight: CGRect?) -> CGRect {
        if let notch = notchRect(screenFrame: screenFrame, safeAreaTop: safeAreaTop,
                                 auxiliaryTopLeft: auxiliaryTopLeft, auxiliaryTopRight: auxiliaryTopRight) {
            return notch
        }
        var menuBar = screenFrame.maxY - visibleFrame.maxY
        if menuBar <= 0 || menuBar > 60 { menuBar = 24 } // hidden menu bar or odd values
        return CGRect(x: screenFrame.midX, y: screenFrame.maxY - menuBar, width: 0, height: menuBar)
    }

    /// The panel frame: `size` wide and tall, centred on the anchor and
    /// flush with the top of the screen.
    public static func panelFrame(anchor: CGRect, screenFrame: CGRect, size: CGSize) -> CGRect {
        var x = anchor.midX - size.width / 2
        x = min(max(x, screenFrame.minX), screenFrame.maxX - size.width)
        return CGRect(x: x.rounded(), y: screenFrame.maxY - size.height, width: size.width, height: size.height)
    }
}
