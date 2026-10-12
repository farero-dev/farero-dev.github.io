import SwiftUI

/// An opaque sRGB color. The character never uses transparency or gradients.
public struct LighthouseRGB: Equatable, Sendable {
    public var red: Double
    public var green: Double
    public var blue: Double

    public init(_ red: Double, _ green: Double, _ blue: Double) {
        self.red = red
        self.green = green
        self.blue = blue
    }

    /// `0xRRGGBB`.
    public init(hex: UInt32) {
        self.init(Double((hex >> 16) & 0xFF) / 255, Double((hex >> 8) & 0xFF) / 255, Double(hex & 0xFF) / 255)
    }

    /// Mixes `other` into `self`: 0 is `self`, 1 is `other`. The result is opaque.
    public func mixed(with other: LighthouseRGB, amount: Double) -> LighthouseRGB {
        LighthouseRGB(
            red + (other.red - red) * amount,
            green + (other.green - green) * amount,
            blue + (other.blue - blue) * amount)
    }

    public var color: Color { Color(.sRGB, red: red, green: green, blue: blue, opacity: 1) }
}

/// The colors of the character, taken from the design canvas "등대 v2 (확정)".
///
/// F-12 allows four colors: body white, structure gray, ink, and the lamp's
/// state color. The lit lantern glass and the beam are the lamp color mixed in
/// advance with ink or black, so they are still single solid colors. Badges
/// are filled with their state color.
public enum LighthousePalette {
    /// Tower body.
    public static let body = LighthouseRGB(hex: 0xECEEF1)
    /// Band, lantern frame, roof, finial and gallery.
    public static let structure = LighthouseRGB(hex: 0x9AA3AD)
    /// Outlines, eyes, the dark lantern glass and badge glyphs.
    public static let ink = LighthouseRGB(hex: 0x22252B)
    /// The small highlight in round eyes.
    public static let highlight = LighthouseRGB(hex: 0xFFFFFF)

    /// Disconnected: the body with its color drained.
    public static let dimBody = LighthouseRGB(hex: 0xA9AEB6)
    /// Disconnected: the structure with its color drained.
    public static let dimStructure = LighthouseRGB(hex: 0x6E747D)

    /// How much of the lamp color the lit glass takes (the rest is ink).
    public static let glassMix: Double = 0.38
    /// How much of the lamp color the beam takes (the rest is black, the notch).
    public static let beamMix: Double = 0.55

    /// The lamp (the round light inside the lantern).
    public static func lamp(_ color: LampColor) -> LighthouseRGB {
        switch color {
        case .warmWhite: LighthouseRGB(hex: 0xFFF1CC)
        case .yellow: LighthouseRGB(hex: 0xFFD45C)
        case .teal: LighthouseRGB(hex: 0x2FD3C0)
        case .orange: LighthouseRGB(hex: 0xFF9F2E)
        case .green: LighthouseRGB(hex: 0x45D483)
        case .amber: LighthouseRGB(hex: 0xFFC93C)
        case .red: LighthouseRGB(hex: 0xFF4D57)
        }
    }

    /// The lit lantern glass: the lamp color pre-mixed with ink.
    public static func glass(_ color: LampColor) -> LighthouseRGB {
        ink.mixed(with: lamp(color), amount: glassMix)
    }

    /// The beam: the lamp color pre-mixed with black.
    public static func beam(_ color: LampColor) -> LighthouseRGB {
        LighthouseRGB(0, 0, 0).mixed(with: lamp(color), amount: beamMix)
    }

    /// The fill of a state's badge.
    public static func badge(_ badge: LighthouseBadge, lampColor: LampColor?) -> LighthouseRGB {
        switch badge {
        case .sleepZ: LighthouseRGB(hex: 0xC9CDD3)
        case .thoughtDots: LighthouseRGB(hex: 0xE4E7EB)
        case .cross: LighthouseRGB(hex: 0xFF6B6B)
        case .question: LighthouseRGB(hex: 0x8A8F98)
        default: lampColor.map(lamp) ?? structure
        }
    }
}
