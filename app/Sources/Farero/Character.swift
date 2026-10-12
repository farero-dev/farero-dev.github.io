import AppKit
import FareroCore
import LighthouseKit
import SwiftUI

extension CharacterState {
    /// The LighthouseKit state to draw. Exhaustive in both directions so a
    /// change on either side fails to compile.
    var lighthouse: LighthouseState {
        switch self {
        case .sleeping: .sleeping
        case .greeting: .greeting
        case .thinking: .thinking
        case .working: .working
        case .workingMany: .workingMany
        case .needsApproval: .needsApproval
        case .allowed: .allowed
        case .denied: .denied
        case .caution: .caution
        case .waitingInput: .waitingInput
        case .error: .error
        case .disconnected: .disconnected
        }
    }
}

/// The character at a given height, sized by LighthouseKit's aspect ratio.
struct CharacterView: View {
    let state: CharacterState
    let size: CGFloat
    /// Running sessions, the number in the 여럿 작업 중 badge.
    var sessionCount: Int?
    var animated = true

    var body: some View {
        LighthouseView(state: state.lighthouse, size: size, animated: animated, sessionCount: sessionCount)
            .frame(width: size * LighthouseView.aspectRatio, height: size)
    }
}

/// Still images of the character for the status item, one per state.
@MainActor
enum CharacterImages {
    private static var cache: [CharacterState: NSImage] = [:]

    static func statusItemImage(_ state: CharacterState) -> NSImage? {
        if let img = cache[state] { return img }
        let height: CGFloat = 18
        // The hop and the stretch of 승인 필요 reach about 1 pt above the
        // frame, as in the design canvas: leave room so the image keeps it.
        let renderer = ImageRenderer(content:
            LighthouseFrame(state: state.lighthouse, size: height, time: 0, reduceMotion: true)
                .padding(1)
                .environment(\.colorScheme, .dark))
        renderer.scale = NSScreen.main?.backingScaleFactor ?? 2
        guard let img = renderer.nsImage else { return nil }
        img.accessibilityDescription = "farero \(state.lighthouse.label)"
        cache[state] = img
        return img
    }
}
