import AppKit
import CoreText
import SwiftUI

/// Draws one frame of the lighthouse into a `GraphicsContext`.
///
/// The drawing follows the SVG of the design canvas "등대 v2 (확정)" on its
/// 24 × 24 unit grid, so at 22 pt one unit is 22/24 pt and the eyes are about
/// 2.2 pt wide. It is the same picture at every size: outlines scale with it.
/// Like the canvas (`overflow: visible`), the hop and the stretch may draw a
/// little above the frame.
struct LighthouseRenderer {
    static let unitHeight: CGFloat = 24
    static let unitWidth: CGFloat = 24

    /// The separately animated parts of the drawing, in back-to-front order.
    /// They match the canvas's animated groups: the turning beam, the body
    /// (which breathes, hops or shakes) and the badge (which stays put).
    struct Parts: OptionSet, Hashable, Sendable {
        let rawValue: Int
        static let beam = Parts(rawValue: 1)
        static let body = Parts(rawValue: 2)
        static let badge = Parts(rawValue: 4)
        static let all: Parts = [.beam, .body, .badge]
    }

    let state: LighthouseState
    let lamp: LampOutput
    let pose: LighthousePose
    let sleepZ: SleepZFrame
    let sessionCount: Int?
    var parts: Parts = .all

    /// The drawing at time `time` for a frame `size` points tall.
    init(state: LighthouseState, time: TimeInterval, reduceMotion: Bool, size: CGFloat, sessionCount: Int?) {
        self.state = state
        lamp = LighthouseLight.output(for: state, at: time, reduceMotion: reduceMotion)
        pose = LighthouseLight.pose(for: state, at: time, reduceMotion: reduceMotion, size: size)
        sleepZ = LighthouseLight.sleepZ(at: time, reduceMotion: reduceMotion)
        self.sessionCount = sessionCount
    }

    /// The drawing with the given lamp, motion and "z", limited to `parts`.
    init(
        state: LighthouseState, lamp: LampOutput, pose: LighthousePose = .still, sleepZ: SleepZFrame = .rest,
        sessionCount: Int?, parts: Parts = .all
    ) {
        self.state = state
        self.lamp = lamp
        self.pose = pose
        self.sleepZ = sleepZ
        self.sessionCount = sessionCount
        self.parts = parts
    }

    /// The foot of the tower: gestures lean and scale around it.
    static let foot = CGPoint(x: 12, y: 23.2)
    /// The lamp, and the pivot of the beam.
    static let lampCenter = CGPoint(x: 12, y: 8.3)
    /// The center of the badge, up and to the left of the lantern.
    private static let badgeCenter = CGPoint(x: 3.9, y: 4.3)
    /// The outline width. Outlines are drawn under the fill (SVG
    /// `paint-order: stroke`), so half of it shows.
    private static let outline: CGFloat = 1.7

    /// Draws into a frame of `size`, the character's 24-unit square scaled to its height.
    func draw(in context: GraphicsContext, size: CGSize) {
        let u = size.height / Self.unitHeight
        guard u > 0 else { return }
        var ctx = context
        ctx.translateBy(x: (size.width - Self.unitWidth * u) / 2, y: 0)
        ctx.scaleBy(x: u, y: u)
        let gesture = state.gesture

        // The beam turns behind the tower and does not breathe with it.
        if parts.contains(.beam), let reach = lamp.beamReach, let color = lamp.color {
            drawBeam(in: ctx, reach: reach, color: color)
        }

        // Motion (hop, shake, breath), then the posture, both around the foot.
        if parts.contains(.body) {
            var body = ctx
            body.translateBy(x: pose.offsetX, y: pose.offsetY)
            Self.scale(&body, x: 1, y: pose.breath, around: Self.foot)
            Self.rotate(&body, degrees: gesture.leanDegrees, around: Self.foot)
            Self.scale(&body, x: gesture.widthScale, y: gesture.heightScale, around: Self.foot)
            drawBody(in: body, lamp: lamp)
        }

        // The badge stays where it is while the body moves.
        if parts.contains(.badge) {
            drawBadge(in: ctx, lamp: lamp)
        }
    }

    // MARK: - Helpers

    private static func scale(_ ctx: inout GraphicsContext, x: CGFloat, y: CGFloat, around p: CGPoint) {
        guard x != 1 || y != 1 else { return }
        ctx.translateBy(x: p.x, y: p.y)
        ctx.scaleBy(x: x, y: y)
        ctx.translateBy(x: -p.x, y: -p.y)
    }

    private static func rotate(_ ctx: inout GraphicsContext, degrees: Double, around p: CGPoint) {
        guard degrees != 0 else { return }
        ctx.translateBy(x: p.x, y: p.y)
        ctx.rotate(by: .degrees(degrees))
        ctx.translateBy(x: -p.x, y: -p.y)
    }

    private static func circle(_ c: CGPoint, _ r: CGFloat) -> Path {
        Path(ellipseIn: CGRect(x: c.x - r, y: c.y - r, width: r * 2, height: r * 2))
    }

    private static func polygon(_ points: [CGPoint]) -> Path {
        var p = Path()
        p.addLines(points)
        p.closeSubpath()
        return p
    }

    private static func line(_ width: CGFloat) -> StrokeStyle {
        StrokeStyle(lineWidth: width, lineCap: .round, lineJoin: .round)
    }

    /// The ink outline under the fill, like SVG `paint-order: stroke`.
    private static func outlined(
        _ path: Path, _ fill: LighthouseRGB, in ctx: GraphicsContext,
        width: CGFloat = outline, join: CGLineJoin = .miter
    ) {
        ctx.stroke(
            path, with: .color(LighthousePalette.ink.color),
            style: StrokeStyle(lineWidth: width, lineJoin: join, miterLimit: 4))
        ctx.fill(path, with: .color(fill.color))
    }

    // MARK: - Body

    private func drawBody(in ctx: GraphicsContext, lamp: LampOutput) {
        let palette = LighthousePalette.self
        let bodyColor = state.isDimmed ? palette.dimBody : palette.body
        let structure = state.isDimmed ? palette.dimStructure : palette.structure
        let ink = palette.ink.color

        // Tower: a trapezoid, wide and short, with rounded bottom corners.
        var tower = Path()
        tower.move(to: CGPoint(x: 8.0, y: 12.4))
        tower.addLine(to: CGPoint(x: 16.0, y: 12.4))
        tower.addLine(to: CGPoint(x: 17.8, y: 22.6))
        tower.addQuadCurve(to: CGPoint(x: 17.3, y: 23.2), control: CGPoint(x: 17.9, y: 23.2))
        tower.addLine(to: CGPoint(x: 6.7, y: 23.2))
        tower.addQuadCurve(to: CGPoint(x: 6.2, y: 22.6), control: CGPoint(x: 6.1, y: 23.2))
        tower.closeSubpath()
        Self.outlined(tower, bodyColor, in: ctx, join: .round)

        // One band across the tower.
        let band = Self.polygon([
            CGPoint(x: 6.81, y: 19.2), CGPoint(x: 17.19, y: 19.2),
            CGPoint(x: 17.48, y: 20.8), CGPoint(x: 6.52, y: 20.8),
        ])
        ctx.fill(band, with: .color(structure.color))

        drawEyes(in: ctx)

        if state.isCracked {
            var crack = Path()
            crack.addLines([
                CGPoint(x: 13.6, y: 17.9), CGPoint(x: 12.5, y: 19.2),
                CGPoint(x: 13.8, y: 20.2), CGPoint(x: 12.8, y: 21.7),
            ])
            ctx.stroke(crack, with: .color(palette.structure.color), style: Self.line(0.9))
        }

        // Lantern: a thick ink outline, the glass (ink when dark, the lamp
        // color mixed with ink when lit), a thin frame, and the round lamp.
        let glass = Path(
            roundedRect: CGRect(x: 8.0, y: 5.6, width: 8.0, height: 5.4), cornerRadius: 1.2,
            style: .circular)
        ctx.stroke(glass, with: .color(ink), lineWidth: 2.7)
        ctx.fill(glass, with: .color(lamp.glassColor.map(palette.glass)?.color ?? ink))
        ctx.stroke(glass, with: .color(structure.color), lineWidth: 1.0)
        if let color = lamp.color {
            ctx.fill(Self.circle(Self.lampCenter, 1.9), with: .color(palette.lamp(color).color))
        }

        // Roof, the ball on top, and the gallery.
        let roof = Self.polygon([CGPoint(x: 7.3, y: 5.9), CGPoint(x: 12, y: 2.4), CGPoint(x: 16.7, y: 5.9)])
        Self.outlined(roof, structure, in: ctx, join: .round)
        Self.outlined(Self.circle(CGPoint(x: 12, y: 2.1), 0.95), structure, in: ctx)
        let gallery = Path(
            roundedRect: CGRect(x: 6.4, y: 10.6, width: 11.2, height: 1.9), cornerRadius: 0.95,
            style: .circular)
        Self.outlined(gallery, structure, in: ctx)
    }

    // MARK: - Eyes

    private func drawEyes(in ctx: GraphicsContext) {
        let ink = GraphicsContext.Shading.color(LighthousePalette.ink.color)
        let white = GraphicsContext.Shading.color(LighthousePalette.highlight.color)

        func round(_ cx: CGFloat, y: CGFloat, r: CGFloat, highlight hr: CGFloat, dx: CGFloat, dy: CGFloat) {
            ctx.fill(Self.circle(CGPoint(x: cx, y: y), r), with: ink)
            ctx.fill(Self.circle(CGPoint(x: cx + dx, y: y + dy), hr), with: white)
        }
        /// A disc cut flat on top.
        func halfDisc(_ cx: CGFloat, y: CGFloat, r: CGFloat) {
            let k: CGFloat = 0.5523 * r
            var p = Path()
            p.move(to: CGPoint(x: cx - r, y: y))
            p.addLine(to: CGPoint(x: cx + r, y: y))
            p.addCurve(
                to: CGPoint(x: cx, y: y + r), control1: CGPoint(x: cx + r, y: y + k),
                control2: CGPoint(x: cx + k, y: y + r))
            p.addCurve(
                to: CGPoint(x: cx - r, y: y), control1: CGPoint(x: cx - k, y: y + r),
                control2: CGPoint(x: cx - r, y: y + k))
            p.closeSubpath()
            ctx.fill(p, with: ink)
        }
        func arc(_ cx: CGFloat, y: CGFloat, controlY: CGFloat) {
            var p = Path()
            p.move(to: CGPoint(x: cx - 1.15, y: y))
            p.addQuadCurve(to: CGPoint(x: cx + 1.15, y: y), control: CGPoint(x: cx, y: controlY))
            ctx.stroke(p, with: ink, style: Self.line(1.0))
        }

        for cx in [10.2, 13.8] as [CGFloat] {
            switch state.eyes {
            case .open: round(cx, y: 15.8, r: 1.2, highlight: 0.43, dx: 0.43, dy: -0.46)
            case .wide: round(cx, y: 15.6, r: 1.5, highlight: 0.54, dx: 0.54, dy: -0.57)
            case .lookingUp: round(cx, y: 15.3, r: 1.2, highlight: 0.45, dx: 0.25, dy: -0.55)
            case .happy: arc(cx, y: 16.3, controlY: 14.8)
            case .closed: arc(cx, y: 15.6, controlY: 16.7)
            case .focused: halfDisc(cx, y: 15.65, r: 1.2)
            case .sideGlance: halfDisc(cx - 0.45, y: 15.65, r: 1.2)
            case .dim: halfDisc(cx, y: 15.9, r: 1.0)
            case .squeezed:
                // ">" on the left, "<" on the right.
                let s: CGFloat = cx < 12 ? 1 : -1
                var p = Path()
                p.addLines([
                    CGPoint(x: cx - 0.9 * s, y: 14.8), CGPoint(x: cx + 0.9 * s, y: 15.8),
                    CGPoint(x: cx - 0.9 * s, y: 16.8),
                ])
                ctx.stroke(p, with: ink, style: Self.line(1.0))
            case .crossed:
                var p = Path()
                p.move(to: CGPoint(x: cx - 0.9, y: 14.9))
                p.addLine(to: CGPoint(x: cx + 0.9, y: 16.7))
                p.move(to: CGPoint(x: cx + 0.9, y: 14.9))
                p.addLine(to: CGPoint(x: cx - 0.9, y: 16.7))
                ctx.stroke(p, with: ink, style: Self.line(1.0))
            }
        }
    }

    // MARK: - Beam

    /// The beam seen from the side: full length to the right at `reach` 1,
    /// gone at 0, full length to the left at -1.
    private func drawBeam(in context: GraphicsContext, reach: Double, color: LampColor) {
        guard abs(reach) > 0.001 else { return }
        var ctx = context
        Self.scale(&ctx, x: reach, y: 1, around: Self.lampCenter)
        var beam = Path()
        beam.move(to: CGPoint(x: 12, y: 7.6))
        beam.addLine(to: CGPoint(x: 22.6, y: 5.6))
        beam.addQuadCurve(to: CGPoint(x: 22.6, y: 11.0), control: CGPoint(x: 23.8, y: 8.3))
        beam.addLine(to: CGPoint(x: 12, y: 9.0))
        beam.closeSubpath()
        ctx.fill(beam, with: .color(LighthousePalette.beam(color).color))
    }

    // MARK: - Badges

    private func drawBadge(in ctx: GraphicsContext, lamp: LampOutput) {
        let b = Self.badgeCenter
        let fill = LighthousePalette.badge(state.badge, lampColor: state.lampColor)
        let ink = GraphicsContext.Shading.color(LighthousePalette.ink.color)
        func disc() { Self.outlined(Self.circle(b, 2.9), fill, in: ctx) }

        switch state.badge {
        case .none:
            return

        case .sleepZ:
            drawSleepZ(in: ctx, fill: fill)

        case .sparkle:
            var p = Path()
            p.move(to: CGPoint(x: 0, y: -2.6))
            p.addQuadCurve(to: CGPoint(x: 2.6, y: 0), control: CGPoint(x: 0.5, y: -0.5))
            p.addQuadCurve(to: CGPoint(x: 0, y: 2.6), control: CGPoint(x: 0.5, y: 0.5))
            p.addQuadCurve(to: CGPoint(x: -2.6, y: 0), control: CGPoint(x: -0.5, y: 0.5))
            p.addQuadCurve(to: CGPoint(x: 0, y: -2.6), control: CGPoint(x: -0.5, y: -0.5))
            p.closeSubpath()
            Self.outlined(p.offsetBy(dx: b.x, dy: b.y), fill, in: ctx, width: 1.36, join: .round)

        case .thoughtDots:
            disc()
            for x in [2.7, 3.9, 5.1] as [CGFloat] {
                ctx.fill(Self.circle(CGPoint(x: x, y: 4.3), 0.55), with: ink)
            }

        case .count:
            disc()
            let text: String
            switch sessionCount {
            case .some(let n) where n > 9: text = "9+"
            case .some(let n) where n > 0: text = String(n)
            default: text = "+"
            }
            if let glyph = LighthouseGlyphs.count(text) {
                ctx.fill(glyph.path.offsetBy(dx: b.x - glyph.advance / 2, dy: 5.8), with: ink)
            }

        case .exclamation:
            // Blinks with the lamp (Q).
            guard lamp.isOn else { return }
            disc()
            ctx.fill(
                Path(
                    roundedRect: CGRect(x: 3.35, y: 2.4, width: 1.1, height: 2.4), cornerRadius: 0.5,
                    style: .circular), with: ink)
            ctx.fill(Self.circle(CGPoint(x: 3.9, y: 5.65), 0.6), with: ink)

        case .check:
            disc()
            var p = Path()
            p.addLines([CGPoint(x: 2.6, y: 4.4), CGPoint(x: 3.5, y: 5.3), CGPoint(x: 5.3, y: 3.4)])
            ctx.stroke(p, with: ink, style: Self.line(0.95))

        case .cross:
            disc()
            var p = Path()
            p.move(to: CGPoint(x: 2.9, y: 3.3))
            p.addLine(to: CGPoint(x: 4.9, y: 5.3))
            p.move(to: CGPoint(x: 4.9, y: 3.3))
            p.addLine(to: CGPoint(x: 2.9, y: 5.3))
            ctx.stroke(p, with: ink, style: Self.line(0.95))

        case .triangle:
            let p = Self.polygon([CGPoint(x: 3.9, y: 1.5), CGPoint(x: 6.9, y: 6.7), CGPoint(x: 0.9, y: 6.7)])
            Self.outlined(p, fill, in: ctx, join: .round)
            ctx.fill(
                Path(
                    roundedRect: CGRect(x: 3.4, y: 3.3, width: 1.0, height: 1.9), cornerRadius: 0.4,
                    style: .circular), with: ink)
            ctx.fill(Self.circle(CGPoint(x: 3.9, y: 5.8), 0.5), with: ink)

        case .bolt:
            let p = Self.polygon([
                CGPoint(x: 4.7, y: 1.3), CGPoint(x: 1.9, y: 4.9), CGPoint(x: 3.9, y: 4.9),
                CGPoint(x: 3.0, y: 7.7), CGPoint(x: 6.1, y: 3.9), CGPoint(x: 4.1, y: 3.9),
            ])
            Self.outlined(p, fill, in: ctx, join: .round)

        case .question:
            disc()
            let glyph = LighthouseGlyphs.question
            ctx.fill(glyph.path.offsetBy(dx: b.x - glyph.advance / 2, dy: 5.85), with: ink)
        }
    }

    /// Two "z" letters to the right of the roof, rising and fading as on the canvas.
    private func drawSleepZ(in context: GraphicsContext, fill: LighthouseRGB) {
        let frame = sleepZ
        guard frame.opacity > 0 else { return }
        var ctx = context
        ctx.opacity = frame.opacity
        ctx.translateBy(x: 0, y: frame.offsetY)
        // One layer, so the fade applies to each letter as a whole.
        ctx.drawLayer { layer in
            let letters: [(LighthouseGlyphs.Run, CGPoint, CGFloat)] = [
                (LighthouseGlyphs.bigZ, CGPoint(x: 17.4, y: 5.6), 0.9),
                (LighthouseGlyphs.smallZ, CGPoint(x: 20.2, y: 3.0), 0.8),
            ]
            for (glyph, origin, width) in letters {
                Self.outlined(glyph.path.offsetBy(dx: origin.x, dy: origin.y), fill, in: layer, width: width)
            }
        }
    }
}

/// Letter outlines for the badges, in drawing units with the baseline at
/// y = 0 and the pen starting at x = 0 (SVG `<text>` placement).
enum LighthouseGlyphs {
    struct Run: Sendable {
        let path: Path
        let advance: CGFloat
    }

    static let bigZ = make("z", size: 4.2, weight: .heavy)
    static let smallZ = make("z", size: 3.0, weight: .heavy)
    static let question = make("?", size: 4.3, weight: .bold)
    private static let counts: [String: Run] = {
        var runs: [String: Run] = [
            "+": make("+", size: 4.1, weight: .bold),
            "9+": make("9+", size: 3.2, weight: .bold),
        ]
        for n in 1...9 { runs[String(n)] = make(String(n), size: 4.1, weight: .bold) }
        return runs
    }()

    static func count(_ text: String) -> Run? { counts[text] }

    private static func make(_ text: String, size: CGFloat, weight: NSFont.Weight) -> Run {
        let font = NSFont.systemFont(ofSize: size, weight: weight) as CTFont
        let characters = Array(text.utf16)
        var glyphs = [CGGlyph](repeating: 0, count: characters.count)
        CTFontGetGlyphsForCharacters(font, characters, &glyphs, characters.count)
        var advances = [CGSize](repeating: .zero, count: glyphs.count)
        CTFontGetAdvancesForGlyphs(font, .horizontal, glyphs, &advances, glyphs.count)
        var path = Path()
        var x: CGFloat = 0
        for (glyph, advance) in zip(glyphs, advances) {
            // Core Text draws y-up; flip to the drawing's y-down.
            var transform = CGAffineTransform(a: 1, b: 0, c: 0, d: -1, tx: x, ty: 0)
            if let outline = CTFontCreatePathForGlyph(font, glyph, &transform) {
                path.addPath(Path(outline))
            }
            x += advance.width
        }
        return Run(path: path, advance: x)
    }
}
