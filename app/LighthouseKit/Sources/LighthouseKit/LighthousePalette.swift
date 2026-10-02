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
        LighthouseRGB(red + (other.red - red) * amount,
                      green + (other.green - green) * amount,
                      blue + (other.blue - blue) * amount)
    }

    public var color: Color { Color(.sRGB, red: red, green: green, blue: blue, opacity: 1) }
}

/// The four colors of the character (F-12): body white, structure gray, ink,
/// and the lamp's state color. The lit lantern glass is the lamp color
/// pre-blended with the glass, so it is still one solid color.
public enum LighthousePalette {
    /// Tower body.
    public static let body = LighthouseRGB(hex: 0xF7F4EC)
    /// Roof, gallery, base, and the unlit lantern glass.
    public static let structure = LighthouseRGB(hex: 0x98A1B3)
    /// Outlines, eyes, glyph strokes.
    public static let ink = LighthouseRGB(hex: 0x1C2233)
    /// The unlit glass. Lit glass is `lamp(color)` (the lamp mixed with this).
    public static let glass = structure
    /// How much of the glass shows through a lit lamp.
    public static let glassMix: Double = 0.12

    /// The raw lamp color.
    public static func rawLamp(_ color: LampColor) -> LighthouseRGB {
        switch color {
        case .warmWhite: LighthouseRGB(hex: 0xFFEDB0)
        case .softBlue: LighthouseRGB(hex: 0x6FC0FF)
        case .warmYellow: LighthouseRGB(hex: 0xFFCE3A)
        case .amber: LighthouseRGB(hex: 0xFFB01F)
        case .green: LighthouseRGB(hex: 0x3BD67E)
        case .red: LighthouseRGB(hex: 0xF5424B)
        case .orange: LighthouseRGB(hex: 0xFF7A2E)
        }
    }

    /// The lit lantern glass: the lamp color pre-blended with the glass.
    public static func lamp(_ color: LampColor) -> LighthouseRGB {
        rawLamp(color).mixed(with: glass, amount: glassMix)
    }
}
