import CoreGraphics
import Foundation
import Testing
@testable import FareroCore

@Suite("Notch geometry")
struct NotchGeometryTests {
    // MacBook Pro 14" at the default scale: 1512 × 982, a 32 pt notch, and
    // auxiliary areas 662 pt wide (as reported in screen coordinates).
    let builtIn = CGRect(x: 0, y: 0, width: 1512, height: 982)

    @Test func notchFromAuxiliaryAreas() throws {
        let r = try #require(NotchGeometry.notchRect(
            screenFrame: builtIn, safeAreaTop: 32,
            auxiliaryTopLeft: CGRect(x: 0, y: 950, width: 662, height: 32),
            auxiliaryTopRight: CGRect(x: 850, y: 950, width: 662, height: 32)))
        #expect(r == CGRect(x: 662, y: 950, width: 188, height: 32))
    }

    @Test func notchOnASecondaryArrangementUsesGlobalOrigin() throws {
        // The built-in display sits left of and below the main display.
        let frame = CGRect(x: -1512, y: -982, width: 1512, height: 982)
        // Auxiliary areas reported relative to the screen: only widths matter.
        let r = try #require(NotchGeometry.notchRect(
            screenFrame: frame, safeAreaTop: 32,
            auxiliaryTopLeft: CGRect(x: 0, y: 950, width: 662, height: 32),
            auxiliaryTopRight: CGRect(x: 850, y: 950, width: 662, height: 32)))
        #expect(r == CGRect(x: -850, y: -32, width: 188, height: 32))
    }

    @Test func noNotch() {
        let external = CGRect(x: 0, y: 0, width: 2560, height: 1440)
        #expect(NotchGeometry.notchRect(screenFrame: external, safeAreaTop: 0, auxiliaryTopLeft: nil, auxiliaryTopRight: nil) == nil)
        // Insets without auxiliary areas (should not happen) are not a notch either.
        #expect(NotchGeometry.notchRect(screenFrame: external, safeAreaTop: 24, auxiliaryTopLeft: nil, auxiliaryTopRight: nil) == nil)
        // Auxiliary areas that cover the whole width leave no notch.
        #expect(NotchGeometry.notchRect(screenFrame: external, safeAreaTop: 24,
                                        auxiliaryTopLeft: CGRect(x: 0, y: 0, width: 1280, height: 24),
                                        auxiliaryTopRight: CGRect(x: 1280, y: 0, width: 1280, height: 24)) == nil)
    }

    @Test func anchorWithoutNotchIsTopCentreAtMenuBarHeight() {
        let frame = CGRect(x: 0, y: 0, width: 2560, height: 1440)
        let visible = CGRect(x: 0, y: 0, width: 2560, height: 1415)
        let a = NotchGeometry.anchorRect(screenFrame: frame, visibleFrame: visible, safeAreaTop: 0,
                                         auxiliaryTopLeft: nil, auxiliaryTopRight: nil)
        #expect(a == CGRect(x: 1280, y: 1415, width: 0, height: 25))
        // Auto-hidden menu bar: fall back to 24 pt.
        let hidden = NotchGeometry.anchorRect(screenFrame: frame, visibleFrame: frame, safeAreaTop: 0,
                                              auxiliaryTopLeft: nil, auxiliaryTopRight: nil)
        #expect(hidden.height == 24)
    }

    @Test func panelFrameIsCentredAndFlushWithTop() {
        let anchor = CGRect(x: 662, y: 950, width: 188, height: 32)
        let p = NotchGeometry.panelFrame(anchor: anchor, screenFrame: builtIn, size: CGSize(width: 600, height: 500))
        #expect(p == CGRect(x: 456, y: 482, width: 600, height: 500))
        // Clamped to the screen edge.
        let edge = NotchGeometry.panelFrame(anchor: CGRect(x: 10, y: 950, width: 0, height: 32), screenFrame: builtIn,
                                            size: CGSize(width: 600, height: 500))
        #expect(edge.minX == 0)
    }
}

@Suite("Notch display mode")
struct NotchModeTests {
    @Test func stateMachine() {
        #expect(NotchMode.resolve(hasActiveSessions: false, pendingApprovals: 0, hovering: false, pinned: false) == .hidden)
        #expect(NotchMode.resolve(hasActiveSessions: false, pendingApprovals: 0, hovering: true, pinned: true) == .hidden)
        #expect(NotchMode.resolve(hasActiveSessions: true, pendingApprovals: 0, hovering: false, pinned: false) == .compact)
        #expect(NotchMode.resolve(hasActiveSessions: true, pendingApprovals: 0, hovering: true, pinned: false) == .expanded)
        #expect(NotchMode.resolve(hasActiveSessions: true, pendingApprovals: 0, hovering: false, pinned: true) == .expanded)
        #expect(NotchMode.resolve(hasActiveSessions: true, pendingApprovals: 2, hovering: true, pinned: false) == .approval)
        // An approval for an unknown session shows even with no live session.
        #expect(NotchMode.resolve(hasActiveSessions: false, pendingApprovals: 1, hovering: false, pinned: false) == .approval)
    }

    @Test func fromAppState() {
        let s = Fixture.connected(sessions: [Fixture.session("s1", status: .ended)])
        #expect(NotchMode.resolve(s, hovering: false, pinned: false) == .hidden)
        let live = Fixture.connected(sessions: [Fixture.session("s1", status: .waitingInput)])
        #expect(NotchMode.resolve(live, hovering: false, pinned: false) == .compact)
    }
}
