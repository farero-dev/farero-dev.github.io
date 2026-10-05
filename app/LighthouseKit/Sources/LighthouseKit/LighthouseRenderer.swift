import SwiftUI

/// Draws one frame of the lighthouse into a `GraphicsContext`.
///
/// Geometry is laid out on a grid 22 units tall and `unitWidth` units wide, so
/// at 22 pt one unit is one point. Outline edges sit on half units, which keeps
/// 1-unit strokes on whole pixels at 22 pt.
struct LighthouseRenderer {
    static let unitHeight: CGFloat = 22
    static let unitWidth: CGFloat = 24

    let state: LighthouseState
    let time: TimeInterval
    let reduceMotion: Bool

    // Tower center line and key heights (units, y grows downward).
    private let cx: CGFloat = 11
    private let baseTop: CGFloat = 19.5
    private let lanternTop: CGFloat = 4.5
    private let lanternBottom: CGFloat = 9.5
    private let lanternHalfWidth: CGFloat = 3.5
    private let eyeY: CGFloat = 14.6
    private let eyeSpacing: CGFloat = 2.3

    func draw(in context: GraphicsContext, size: CGSize) {
        let u = size.height / Self.unitHeight
        guard u > 0 else { return }
        var ctx = context
        ctx.scaleBy(x: u, y: u)

        let pointHeight = size.height
        let style = Style(pointHeight: pointHeight, unit: u)
        let lamp = LighthouseLight.output(for: state, at: time, reduceMotion: reduceMotion)
        let blinking = LighthouseLight.isBlinking(state, at: time, reduceMotion: reduceMotion)
        let hop = LighthouseLight.hopHeight(state, at: time, reduceMotion: reduceMotion)
        let gesture = state.gesture

        // The base only hops. The rest leans and squashes around the top of the base.
        var grounded = ctx
        grounded.translateBy(x: 0, y: -CGFloat(hop) * 1.4)
        var body = grounded
        body.translateBy(x: cx, y: baseTop)
        body.rotate(by: .degrees(gesture.leanDegrees))
        body.scaleBy(x: 1, y: gesture.heightScale)
        body.translateBy(x: -cx, y: -baseTop)

        if let angle = lamp.beamAngle, let color = lamp.color {
            drawBeam(in: body, angle: angle, color: color, style: style)
        }
        drawTower(in: body, lamp: lamp, style: style)
        drawBase(in: grounded, style: style)
        drawEyes(in: body, eyes: blinking ? nil : state.eyes, style: style)
        if state.badge == .slash {
            drawLanternSlash(in: body, style: style)
        }

        // Badges stay upright next to the lamp and follow the hop.
        var badge = ctx
        badge.translateBy(x: 0, y: -CGFloat(hop) * 1.4)
        drawBadge(in: badge, style: style)
    }

    // MARK: - Style

    struct Style {
        /// Outline width in units: 1 pt at 22 pt, growing a bit slower than the size.
        let line: CGFloat
        /// Whether to add details that only read at larger sizes.
        let detailed: Bool

        init(pointHeight: CGFloat, unit: CGFloat) {
            let scale = pointHeight / LighthouseRenderer.unitHeight
            let points = max(0.75, pow(scale, 0.78))
            line = points / unit
            detailed = pointHeight >= 40
        }

        func stroke(_ width: CGFloat? = nil) -> StrokeStyle {
            StrokeStyle(lineWidth: width ?? line, lineCap: .round, lineJoin: .round)
        }
    }

    private var ink: Color { LighthousePalette.ink.color }
    private var white: Color { LighthousePalette.body.color }
    private var gray: Color { LighthousePalette.structure.color }

    private func fillAndStroke(_ path: Path, _ fill: Color, in ctx: GraphicsContext, style: Style) {
        ctx.fill(path, with: .color(fill))
        ctx.stroke(path, with: .color(ink), style: style.stroke())
    }

    // MARK: - Tower

    private func drawTower(in ctx: GraphicsContext, lamp: LampOutput, style: Style) {
        let bodyColor = state.isDimmed ? gray : white

        // Tower: short and wide, slightly flared toward the base. It runs a
        // little into the base so a lean never opens a gap.
        var tower = Path()
        tower.move(to: CGPoint(x: cx - 4.5, y: 11.5))
        tower.addLine(to: CGPoint(x: cx + 4.5, y: 11.5))
        tower.addQuadCurve(to: CGPoint(x: cx + 6, y: baseTop), control: CGPoint(x: cx + 5.0, y: 16.5))
        tower.addLine(to: CGPoint(x: cx + 6.1, y: baseTop + 1))
        tower.addLine(to: CGPoint(x: cx - 6.1, y: baseTop + 1))
        tower.addLine(to: CGPoint(x: cx - 6, y: baseTop))
        tower.addQuadCurve(to: CGPoint(x: cx - 4.5, y: 11.5), control: CGPoint(x: cx - 5.0, y: 16.5))
        tower.closeSubpath()
        fillAndStroke(tower, bodyColor, in: ctx, style: style)

        // Lantern glass: one solid color, lit or not.
        let glassRect = CGRect(x: cx - lanternHalfWidth, y: lanternTop,
                               width: lanternHalfWidth * 2, height: lanternBottom - lanternTop)
        let glassColor = lamp.color.map { LighthousePalette.lamp($0).color } ?? LighthousePalette.glass.color
        ctx.fill(Path(glassRect), with: .color(glassColor))
        if style.detailed {
            // Mullions.
            var mullions = Path()
            for dx in [-1.2, 1.2] as [CGFloat] {
                mullions.move(to: CGPoint(x: cx + dx, y: lanternTop))
                mullions.addLine(to: CGPoint(x: cx + dx, y: lanternBottom))
            }
            ctx.stroke(mullions, with: .color(ink), style: style.stroke(style.line * 0.55))
        }
        ctx.stroke(Path(glassRect), with: .color(ink), style: style.stroke())

        // Gallery deck.
        let deck = Path(roundedRect: CGRect(x: cx - 5.5, y: 9.5, width: 11, height: 2), cornerRadius: 0.6)
        fillAndStroke(deck, gray, in: ctx, style: style)

        if style.detailed {
            // Railing on the deck, beside the lantern.
            var rail = Path()
            for side in [-1.0, 1.0] as [CGFloat] {
                rail.move(to: CGPoint(x: cx + side * lanternHalfWidth, y: 8.2))
                rail.addLine(to: CGPoint(x: cx + side * 5.0, y: 8.2))
                rail.addLine(to: CGPoint(x: cx + side * 5.0, y: 9.5))
                rail.move(to: CGPoint(x: cx + side * 4.25, y: 8.2))
                rail.addLine(to: CGPoint(x: cx + side * 4.25, y: 9.5))
            }
            ctx.stroke(rail, with: .color(ink), style: style.stroke(style.line * 0.55))
        }

        // Domed roof with a small ball on top.
        var roof = Path()
        roof.move(to: CGPoint(x: cx - 4.6, y: lanternTop))
        roof.addCurve(to: CGPoint(x: cx, y: 1.9),
                      control1: CGPoint(x: cx - 4.4, y: 2.6), control2: CGPoint(x: cx - 2.4, y: 1.9))
        roof.addCurve(to: CGPoint(x: cx + 4.6, y: lanternTop),
                      control1: CGPoint(x: cx + 2.4, y: 1.9), control2: CGPoint(x: cx + 4.4, y: 2.6))
        roof.closeSubpath()
        fillAndStroke(roof, gray, in: ctx, style: style)

        // Finial: a small ball on a short stem.
        var stem = Path()
        stem.move(to: CGPoint(x: cx, y: 2.0))
        stem.addLine(to: CGPoint(x: cx, y: 1.4))
        ctx.stroke(stem, with: .color(ink), style: style.stroke())
        let ball = Path(ellipseIn: CGRect(x: cx - 0.75, y: 0.55, width: 1.5, height: 1.5))
        ctx.fill(ball, with: .color(gray))
        ctx.stroke(ball, with: .color(ink), style: style.stroke(style.detailed ? style.line * 0.7 : style.line))
    }

    private func drawBase(in ctx: GraphicsContext, style: Style) {
        let base = Path(roundedRect: CGRect(x: cx - 7.5, y: baseTop, width: 15, height: 2), cornerRadius: 1)
        fillAndStroke(base, gray, in: ctx, style: style)
    }

    // MARK: - Beam

    private func drawBeam(in ctx: GraphicsContext, angle: Double, color: LampColor, style: Style) {
        let centerY = (lanternTop + lanternBottom) / 2
        if state.beamCount >= 2 {
            // Two narrower beams from one lens, the lower one a little behind.
            drawCone(in: ctx, angle: angle - LighthouseLight.Timing.secondBeamLag, centerY: centerY + 1.25,
                     nearHalf: 0.55, farHalf: 0.65, spread: 1.25, color: color, style: style)
            drawCone(in: ctx, angle: angle, centerY: centerY - 1.25,
                     nearHalf: 0.55, farHalf: 0.65, spread: 1.25, color: color, style: style)
        } else {
            drawCone(in: ctx, angle: angle, centerY: centerY,
                     nearHalf: 0.9, farHalf: 1.0, spread: 2.1, color: color, style: style)
        }
    }

    /// One light cone seen from the side: its length and spread follow how far
    /// the beam points sideways (`sin(angle)`).
    private func drawCone(in ctx: GraphicsContext, angle: Double, centerY: CGFloat, nearHalf: CGFloat,
                          farHalf baseFarHalf: CGFloat, spread: CGFloat, color: LampColor, style: Style) {
        let s = CGFloat(sin(angle))
        let reach = abs(s)
        guard reach > 0.08 else { return }
        let side: CGFloat = s > 0 ? 1 : -1
        let nearX = cx
        let farX = cx + side * (lanternHalfWidth + 6.9 * reach)
        let farHalf = baseFarHalf + spread * reach
        let top0 = CGPoint(x: nearX, y: centerY - nearHalf)
        let top1 = CGPoint(x: farX, y: centerY - farHalf)
        let bottom1 = CGPoint(x: farX, y: centerY + farHalf)
        let bottom0 = CGPoint(x: nearX, y: centerY + nearHalf)

        var cone = Path()
        cone.move(to: top0)
        cone.addLine(to: top1)
        cone.addQuadCurve(to: bottom1, control: CGPoint(x: farX + side * 0.9 * reach * farHalf / 3.1, y: centerY))
        cone.addLine(to: bottom0)
        cone.closeSubpath()
        ctx.fill(cone, with: .color(LighthousePalette.lamp(color).color))
        // Outline the sides only: the open end reads as light, not a flag.
        var edges = Path()
        edges.move(to: top0)
        edges.addLine(to: top1)
        edges.move(to: bottom0)
        edges.addLine(to: bottom1)
        ctx.stroke(edges, with: .color(ink), style: style.stroke())
    }

    // MARK: - Eyes

    private func drawEyes(in ctx: GraphicsContext, eyes: LighthouseEyes?, style: Style) {
        for side in [-1.0, 1.0] as [CGFloat] {
            let center = CGPoint(x: cx + side * eyeSpacing, y: eyeY)
            drawEye(in: ctx, eyes: eyes, center: center, side: side, style: style)
        }
    }

    /// `side` is -1 for the left eye, +1 for the right eye.
    private func drawEye(in ctx: GraphicsContext, eyes: LighthouseEyes?, center c: CGPoint, side: CGFloat, style: Style) {
        let strokeWidth = max(style.line * 0.95, 0.9)
        func ellipse(_ w: CGFloat, _ h: CGFloat, at p: CGPoint) -> Path {
            Path(ellipseIn: CGRect(x: p.x - w / 2, y: p.y - h / 2, width: w, height: h))
        }
        func highlight(_ r: CGFloat, at p: CGPoint) {
            ctx.fill(ellipse(r * 2, r * 2, at: p), with: .color(white))
        }

        guard let eyes else {
            // Blink: a short flat line.
            var p = Path()
            p.move(to: CGPoint(x: c.x - 1.1, y: c.y + 0.4))
            p.addLine(to: CGPoint(x: c.x + 1.1, y: c.y + 0.4))
            ctx.stroke(p, with: .color(ink), style: style.stroke(strokeWidth))
            return
        }

        switch eyes {
        case .open:
            ctx.fill(ellipse(2.2, 2.6, at: c), with: .color(ink))
            if style.detailed { highlight(0.42, at: CGPoint(x: c.x + 0.4, y: c.y - 0.5)) }

        case .wide:
            ctx.fill(ellipse(2.9, 3.3, at: c), with: .color(ink))
            highlight(style.detailed ? 0.55 : 0.6, at: CGPoint(x: c.x + 0.5, y: c.y - 0.6))

        case .happy:
            var p = Path()
            p.move(to: CGPoint(x: c.x - 1.3, y: c.y + 0.7))
            p.addQuadCurve(to: CGPoint(x: c.x + 1.3, y: c.y + 0.7), control: CGPoint(x: c.x, y: c.y - 1.7))
            ctx.stroke(p, with: .color(ink), style: style.stroke(strokeWidth))

        case .lookingUp:
            let p = CGPoint(x: c.x + 0.5, y: c.y - 1.0)
            ctx.fill(ellipse(2.0, 2.3, at: p), with: .color(ink))
            if style.detailed { highlight(0.38, at: CGPoint(x: p.x + 0.35, y: p.y - 0.45)) }

        case .focused:
            // Flat, slanted lid: lower toward the nose.
            var clip = Path()
            let outer = -side
            clip.move(to: CGPoint(x: c.x + outer * 2, y: c.y - 0.85))
            clip.addLine(to: CGPoint(x: c.x - outer * 2, y: c.y - 0.25))
            clip.addLine(to: CGPoint(x: c.x - outer * 2, y: c.y + 3))
            clip.addLine(to: CGPoint(x: c.x + outer * 2, y: c.y + 3))
            clip.closeSubpath()
            var clipped = ctx
            clipped.clip(to: clip)
            clipped.fill(ellipse(2.5, 2.7, at: CGPoint(x: c.x, y: c.y + 0.1)), with: .color(ink))
            if style.detailed { highlight(0.36, at: CGPoint(x: c.x + 0.35, y: c.y + 0.35)) }

        case .narrowSideGlance:
            // Half-lidded, glancing to the viewer's left.
            let p = CGPoint(x: c.x - 0.7, y: c.y)
            var clip = Path()
            clip.addRect(CGRect(x: p.x - 3, y: p.y - 0.15, width: 6, height: 3))
            var clipped = ctx
            clipped.clip(to: clip)
            clipped.fill(ellipse(2.4, 3.0, at: p), with: .color(ink))
            var lid = Path()
            lid.move(to: CGPoint(x: c.x - 1.6, y: p.y - 0.15))
            lid.addLine(to: CGPoint(x: c.x + 1.3, y: p.y - 0.15))
            ctx.stroke(lid, with: .color(ink), style: style.stroke(strokeWidth * 0.9))

        case .closed:
            var p = Path()
            p.move(to: CGPoint(x: c.x - 1.2, y: c.y + 0.1))
            p.addQuadCurve(to: CGPoint(x: c.x + 1.2, y: c.y + 0.1), control: CGPoint(x: c.x, y: c.y + 1.7))
            ctx.stroke(p, with: .color(ink), style: style.stroke(strokeWidth))

        case .flat:
            var p = Path()
            p.move(to: CGPoint(x: c.x - 1.3, y: c.y + 0.3))
            p.addLine(to: CGPoint(x: c.x + 1.3, y: c.y + 0.3))
            ctx.stroke(p, with: .color(ink), style: style.stroke(strokeWidth * 1.05))

        case .crossed:
            let r: CGFloat = 1.05
            var p = Path()
            p.move(to: CGPoint(x: c.x - r, y: c.y - r))
            p.addLine(to: CGPoint(x: c.x + r, y: c.y + r))
            p.move(to: CGPoint(x: c.x + r, y: c.y - r))
            p.addLine(to: CGPoint(x: c.x - r, y: c.y + r))
            ctx.stroke(p, with: .color(ink), style: style.stroke(strokeWidth * 0.9))

        case .hollow:
            let ringWidth = max(style.line * 0.7, 0.7)
            ctx.stroke(ellipse(2.3, 2.3, at: c), with: .color(ink), style: style.stroke(ringWidth))
        }
    }

    // MARK: - Badges

    /// Center of the badge area, up and to the right of the lamp.
    private let badgeCenter = CGPoint(x: 20.6, y: 3.8)

    /// Draws `path` as a white glyph stroke with an ink outline.
    private func stickerStroke(_ path: Path, in ctx: GraphicsContext, style: Style, weight: CGFloat = 1.4) {
        let outline = style.line * 0.7
        ctx.stroke(path, with: .color(ink), style: style.stroke(weight + outline * 2))
        ctx.stroke(path, with: .color(white), style: style.stroke(weight))
    }

    private func drawBadge(in ctx: GraphicsContext, style: Style) {
        let b = badgeCenter
        switch state.badge {
        case .none, .slash:
            return

        case .exclamation:
            var bar = Path()
            bar.move(to: CGPoint(x: b.x, y: b.y - 2.6))
            bar.addLine(to: CGPoint(x: b.x, y: b.y + 0.4))
            stickerStroke(bar, in: ctx, style: style, weight: 1.5)
            let dot = Path(ellipseIn: CGRect(x: b.x - 0.95, y: b.y + 1.65, width: 1.9, height: 1.9))
            ctx.fill(dot, with: .color(white))
            ctx.stroke(dot, with: .color(ink), style: style.stroke(style.line * 0.7 * 2 / 1.4))

        case .check:
            var p = Path()
            p.move(to: CGPoint(x: b.x - 2.1, y: b.y + 0.1))
            p.addLine(to: CGPoint(x: b.x - 0.6, y: b.y + 1.6))
            p.addLine(to: CGPoint(x: b.x + 2.1, y: b.y - 1.8))
            stickerStroke(p, in: ctx, style: style)

        case .minus:
            var p = Path()
            p.move(to: CGPoint(x: b.x - 2.0, y: b.y))
            p.addLine(to: CGPoint(x: b.x + 2.0, y: b.y))
            stickerStroke(p, in: ctx, style: style, weight: 1.5)

        case .plus:
            var p = Path()
            p.move(to: CGPoint(x: b.x - 2.0, y: b.y - 0.4))
            p.addLine(to: CGPoint(x: b.x + 2.0, y: b.y - 0.4))
            p.move(to: CGPoint(x: b.x, y: b.y - 2.4))
            p.addLine(to: CGPoint(x: b.x, y: b.y + 1.6))
            stickerStroke(p, in: ctx, style: style)

        case .sleepZ:
            var z = Path()
            z.move(to: CGPoint(x: b.x - 1.6, y: b.y - 1.7))
            z.addLine(to: CGPoint(x: b.x + 1.6, y: b.y - 1.7))
            z.addLine(to: CGPoint(x: b.x - 1.6, y: b.y + 1.7))
            z.addLine(to: CGPoint(x: b.x + 1.6, y: b.y + 1.7))
            stickerStroke(z, in: ctx, style: style, weight: 1.2)
            if style.detailed {
                // A smaller second "z" drifting off.
                let o = CGPoint(x: b.x - 3.4, y: b.y + 3.0)
                var small = Path()
                small.move(to: CGPoint(x: o.x - 0.8, y: o.y - 0.85))
                small.addLine(to: CGPoint(x: o.x + 0.8, y: o.y - 0.85))
                small.addLine(to: CGPoint(x: o.x - 0.8, y: o.y + 0.85))
                small.addLine(to: CGPoint(x: o.x + 0.8, y: o.y + 0.85))
                stickerStroke(small, in: ctx, style: style, weight: 0.7)
            }

        case .sparkle:
            let r: CGFloat = 3.2
            let k: CGFloat = 0.45
            var p = Path()
            p.move(to: CGPoint(x: b.x, y: b.y - r))
            p.addQuadCurve(to: CGPoint(x: b.x + r, y: b.y), control: CGPoint(x: b.x + k, y: b.y - k))
            p.addQuadCurve(to: CGPoint(x: b.x, y: b.y + r), control: CGPoint(x: b.x + k, y: b.y + k))
            p.addQuadCurve(to: CGPoint(x: b.x - r, y: b.y), control: CGPoint(x: b.x - k, y: b.y + k))
            p.addQuadCurve(to: CGPoint(x: b.x, y: b.y - r), control: CGPoint(x: b.x - k, y: b.y - k))
            p.closeSubpath()
            ctx.fill(p, with: .color(white))
            ctx.stroke(p, with: .color(ink), style: style.stroke(style.line * 0.8))

        case .thoughtDots:
            let dots: [(CGFloat, CGFloat, CGFloat)] = [(16.9, 5.4, 0.85), (19.1, 3.6, 1.2), (21.8, 1.9, 1.6)]
            for (x, y, r) in dots {
                let p = Path(ellipseIn: CGRect(x: x - r, y: y - r, width: r * 2, height: r * 2))
                ctx.fill(p, with: .color(white))
                ctx.stroke(p, with: .color(ink), style: style.stroke(style.line * 0.6))
            }

        case .triangle:
            var p = Path()
            p.move(to: CGPoint(x: b.x, y: b.y - 3.1))
            p.addLine(to: CGPoint(x: b.x + 3.2, y: b.y + 2.4))
            p.addLine(to: CGPoint(x: b.x - 3.2, y: b.y + 2.4))
            p.closeSubpath()
            ctx.fill(p, with: .color(white))
            ctx.stroke(p, with: .color(ink), style: style.stroke(style.line * 0.85))
            var mark = Path()
            mark.move(to: CGPoint(x: b.x, y: b.y - 0.9))
            mark.addLine(to: CGPoint(x: b.x, y: b.y + 0.4))
            mark.move(to: CGPoint(x: b.x, y: b.y + 1.35))
            mark.addLine(to: CGPoint(x: b.x, y: b.y + 1.36))
            ctx.stroke(mark, with: .color(ink), style: style.stroke(max(style.line * 0.75, 0.75)))

        case .bolt:
            var p = Path()
            p.move(to: CGPoint(x: b.x + 0.4, y: b.y - 3.4))
            p.addLine(to: CGPoint(x: b.x + 2.4, y: b.y - 3.4))
            p.addLine(to: CGPoint(x: b.x + 1.0, y: b.y - 0.6))
            p.addLine(to: CGPoint(x: b.x + 2.6, y: b.y - 0.6))
            p.addLine(to: CGPoint(x: b.x - 1.4, y: b.y + 3.8))
            p.addLine(to: CGPoint(x: b.x - 0.4, y: b.y + 0.7))
            p.addLine(to: CGPoint(x: b.x - 2.2, y: b.y + 0.7))
            p.closeSubpath()
            ctx.fill(p, with: .color(white))
            ctx.stroke(p, with: .color(ink), style: style.stroke(style.line * 0.7))
        }
    }

    /// Disconnected: a slash across the dark lantern.
    private func drawLanternSlash(in ctx: GraphicsContext, style: Style) {
        var p = Path()
        p.move(to: CGPoint(x: cx - 4.4, y: lanternBottom + 0.6))
        p.addLine(to: CGPoint(x: cx + 4.4, y: lanternTop - 0.9))
        stickerStroke(p, in: ctx, style: style, weight: 1.1)
    }
}
