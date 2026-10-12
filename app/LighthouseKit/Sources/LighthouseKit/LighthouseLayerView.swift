import AppKit
import QuartzCore
import SwiftUI

/// An animated lighthouse played by Core Animation (see
/// `LighthouseAnimationPlan`). The app renders a few images once per state,
/// size and screen scale; after that the window server runs the animation
/// and the app does nothing per frame.
struct LighthouseAnimatedView: NSViewRepresentable {
    let state: LighthouseState
    let size: CGFloat
    let sessionCount: Int?

    func makeNSView(context: Context) -> LighthouseLayerView {
        LighthouseLayerView()
    }

    func updateNSView(_ view: LighthouseLayerView, context: Context) {
        view.show(LighthouseLayerView.Content(state: state, size: size, sessionCount: sessionCount))
    }
}

/// Three layers, back to front: beam, body, badge. Each holds a pre-rendered
/// image as large as the frame plus a margin, so the hop and the stretch can
/// reach above the frame like the canvas (`overflow: visible`).
final class LighthouseLayerView: NSView {
    struct Content: Hashable {
        var state: LighthouseState
        var size: CGFloat
        var sessionCount: Int?
    }

    private(set) var content: Content?
    /// What the layers show now, and at which scale.
    private var installed: (content: Content, scale: CGFloat)?

    private let container = CALayer()
    let beamLayer = CALayer()
    let bodyLayer = CALayer()
    let badgeLayer = CALayer()

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.masksToBounds = false
        container.masksToBounds = false
        for sublayer in [beamLayer, bodyLayer, badgeLayer] {
            sublayer.masksToBounds = false
            sublayer.contentsGravity = .resize
            container.addSublayer(sublayer)
        }
        layer?.addSublayer(container)
        setAccessibilityElement(false)
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not used")
    }

    override var isFlipped: Bool { true }

    /// Clicks go to the SwiftUI view around the character.
    override func hitTest(_ point: NSPoint) -> NSView? { nil }

    override func viewDidChangeBackingProperties() {
        super.viewDidChangeBackingProperties()
        refresh()
    }

    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        refresh()
    }

    override func layout() {
        super.layout()
        refresh()
    }

    func show(_ content: Content) {
        self.content = content
        refresh()
    }

    private var backingScale: CGFloat {
        window?.backingScaleFactor ?? NSScreen.main?.backingScaleFactor ?? 2
    }

    /// Lays out the layers and, when the content or the scale changed,
    /// installs new images and animations.
    private func refresh() {
        guard let content, let plan = LighthouseAnimationPlan(state: content.state, size: content.size) else {
            clear()
            return
        }
        let scale = backingScale
        let images = LighthouseLayerImages.images(for: content, plan: plan, scale: scale)
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        defer { CATransaction.commit() }

        // Work top-down (y grows downward) in the container whatever the
        // view's layer does.
        container.isGeometryFlipped = false
        container.frame = layer?.bounds ?? bounds
        container.isGeometryFlipped = !(layer?.contentsAreFlipped() ?? false)

        let u = content.size / LighthouseRenderer.unitHeight
        // The character's frame inside the view (centered like LighthouseFrame).
        let frameOrigin = CGPoint(x: (container.bounds.width - content.size * LighthouseView.aspectRatio) / 2, y: 0)
        func place(_ sublayer: CALayer, anchorUnits: CGPoint) {
            let anchor = CGPoint(
                x: (images.margin + anchorUnits.x * u) / images.cell,
                y: (images.margin + anchorUnits.y * u) / images.cell)
            sublayer.bounds = CGRect(x: 0, y: 0, width: images.cell, height: images.cell)
            sublayer.anchorPoint = anchor
            sublayer.position = CGPoint(
                x: frameOrigin.x - images.margin + anchor.x * images.cell,
                y: frameOrigin.y - images.margin + anchor.y * images.cell)
            sublayer.contentsScale = scale
        }
        place(beamLayer, anchorUnits: LighthouseRenderer.lampCenter)
        place(bodyLayer, anchorUnits: LighthouseRenderer.foot)
        place(badgeLayer, anchorUnits: CGPoint(x: 12, y: 12))

        if let installed, installed.content == content, installed.scale == scale { return }
        installed = (content, scale)
        install(plan: plan, images: images, unit: u)
    }

    private func clear() {
        guard installed != nil else { return }
        installed = nil
        for sublayer in [beamLayer, bodyLayer, badgeLayer] {
            sublayer.removeAllAnimations()
            sublayer.contents = nil
        }
    }

    // MARK: - Animations

    private func install(plan: LighthouseAnimationPlan, images: LighthouseLayerImages.Images, unit u: CGFloat) {
        for sublayer in [beamLayer, bodyLayer, badgeLayer] {
            sublayer.removeAllAnimations()
            sublayer.transform = CATransform3DIdentity
            sublayer.opacity = 1
        }
        let easeInOut = CAMediaTimingFunction(name: .easeInEaseOut)  // cubic-bezier(0.42, 0, 0.58, 1), as CSS
        let linear = CAMediaTimingFunction(name: .linear)
        // Phase every track to absolute time, as `LighthouseLight` does.
        let now = (media: CACurrentMediaTime(), absolute: Date().timeIntervalSinceReferenceDate)
        func loop(_ animation: CAAnimation, on layer: CALayer, key: String, period: TimeInterval) {
            animation.duration = period
            animation.repeatCount = .infinity
            animation.isRemovedOnCompletion = false
            animation.beginTime = layer.convertTime(now.media, from: nil) - LighthouseLight.phase(now.absolute, period)
            layer.add(animation, forKey: key)
        }

        // Beam: scaleX 1 → -1 → 1 around the lamp, easing each half.
        beamLayer.contents = images.beam
        if let period = plan.beamPeriod {
            let sweep = CAKeyframeAnimation(keyPath: "transform.scale.x")
            sweep.values = [1, -1, 1]
            sweep.keyTimes = [0, 0.5, 1]
            sweep.timingFunctions = [easeInOut, easeInOut]
            loop(sweep, on: beamLayer, key: "sweep", period: period)
        }

        // Body: one image per lamp step, swapped at the step times.
        bodyLayer.contents = images.body.first
        if let lamp = plan.lamp {
            let steps = CAKeyframeAnimation(keyPath: "contents")
            steps.calculationMode = .discrete
            steps.values = images.body
            steps.keyTimes = (lamp.keyTimes + [1]).map { NSNumber(value: $0) }
            loop(steps, on: bodyLayer, key: "lamp", period: lamp.period)
        }
        switch plan.motion {
        case .none:
            break
        case .breathe:
            // scaleY 1 → 0.985 → 1 around the foot.
            let breath = CAKeyframeAnimation(keyPath: "transform.scale.y")
            breath.values = [1, 1 - LighthouseLight.Timing.breatheDepth, 1]
            breath.keyTimes = [0, 0.5, 1]
            breath.timingFunctions = [easeInOut, easeInOut]
            loop(breath, on: bodyLayer, key: "motion", period: LighthouseLight.Timing.breathePeriod)
        case .hop:
            // Up by 40 %, held until 60 %, down by 100 %.
            let h = LighthouseLight.Timing.hopHeight * u
            let hop = CAKeyframeAnimation(keyPath: "transform.translation.y")
            hop.values = [0, -h, -h, 0]
            hop.keyTimes = [0, 0.4, 0.6, 1]
            hop.timingFunctions = [easeInOut, linear, easeInOut]
            loop(hop, on: bodyLayer, key: "motion", period: LighthouseLight.Timing.hopPeriod)
        case .shake:
            let d = LighthouseLight.Timing.shakeDistance * u
            let shake = CAKeyframeAnimation(keyPath: "transform.translation.x")
            shake.values = [0, -d, d, 0]
            shake.keyTimes = [0, 0.25, 0.75, 1]
            shake.timingFunctions = [linear, linear, linear]
            loop(shake, on: bodyLayer, key: "motion", period: LighthouseLight.Timing.shakePeriod)
        }

        // Badge: still, blinking with the lamp, or the rising "z".
        badgeLayer.contents = images.badge
        if plan.badgeBlinksWithLamp, let lamp = plan.lamp {
            let blink = CAKeyframeAnimation(keyPath: "opacity")
            blink.calculationMode = .discrete
            blink.values = lamp.outputs.map { $0.isOn ? 1 : 0 }
            blink.keyTimes = (lamp.keyTimes + [1]).map { NSNumber(value: $0) }
            loop(blink, on: badgeLayer, key: "blink", period: lamp.period)
        }
        if plan.risingZ {
            // Rises from 0.4 below to 0.8 above its place, fading in by 40 % and out by 100 %.
            let rise = CAKeyframeAnimation(keyPath: "transform.translation.y")
            rise.values = [0.4 * u, -0.8 * u]
            rise.keyTimes = [0, 1]
            rise.timingFunctions = [easeInOut]
            let fade = CAKeyframeAnimation(keyPath: "opacity")
            fade.values = [0, 1, 0]
            fade.keyTimes = [0, 0.4, 1]
            fade.timingFunctions = [easeInOut, easeInOut]
            let group = CAAnimationGroup()
            group.animations = [rise, fade]
            rise.duration = LighthouseLight.Timing.sleepZPeriod
            fade.duration = LighthouseLight.Timing.sleepZPeriod
            loop(group, on: badgeLayer, key: "z", period: LighthouseLight.Timing.sleepZPeriod)
        }
    }
}

/// Renders and caches the layer images of `LighthouseLayerView`.
@MainActor
enum LighthouseLayerImages {
    struct Images {
        /// One per lamp step (or one when the lamp is steady).
        var body: [CGImage]
        var beam: CGImage?
        var badge: CGImage?
        /// The margin around the frame, in points (whole pixels).
        var margin: CGFloat
        /// The side of every image, in points (whole pixels).
        var cell: CGFloat
    }

    private struct Key: Hashable {
        var content: LighthouseLayerView.Content
        var scale: CGFloat
    }

    private static var cache: [Key: Images] = [:]
    /// Enough for the animated states at the app's sizes (22 and 48 pt) on
    /// two screen scales, with a few session counts.
    private static let cacheLimit = 32

    static func images(
        for content: LighthouseLayerView.Content, plan: LighthouseAnimationPlan, scale: CGFloat
    ) -> Images {
        let key = Key(content: content, scale: scale)
        if let images = cache[key] { return images }
        let images = build(content, plan: plan, scale: scale)
        if cache.count >= cacheLimit { cache.removeAll() }
        cache[key] = images
        return images
    }

    /// Draws every image side by side in one `ImageRenderer` pass and cuts
    /// the strip apart.
    static func build(_ content: LighthouseLayerView.Content, plan: LighthouseAnimationPlan, scale: CGFloat) -> Images {
        let state = content.state
        let size = content.size
        let u = size / LighthouseRenderer.unitHeight
        // Two units around the frame, rounded up to whole pixels.
        let margin = (2 * u * scale).rounded(.up) / scale
        let cellPixels = Int(((size * LighthouseView.aspectRatio + 2 * margin) * scale).rounded(.up))
        let cell = CGFloat(cellPixels) / scale

        var jobs: [LighthouseRenderer] = []
        for lamp in plan.lamp?.outputs ?? [plan.steadyLamp] {
            jobs.append(LighthouseRenderer(state: state, lamp: lamp, sessionCount: content.sessionCount, parts: .body))
        }
        let hasBeam = plan.beamPeriod != nil
        if hasBeam {
            let beam = LampOutput(isOn: true, color: state.lampColor, beamReach: 1)
            jobs.append(LighthouseRenderer(state: state, lamp: beam, sessionCount: content.sessionCount, parts: .beam))
        }
        let hasBadge = state.badge != .none
        if hasBadge {
            // The badge as it looks while the lamp is lit; a blinking badge is hidden by opacity.
            let lit = LampOutput(isOn: state.lampColor != nil, color: state.lampColor)
            jobs.append(LighthouseRenderer(state: state, lamp: lit, sessionCount: content.sessionCount, parts: .badge))
        }

        let strip = HStack(spacing: 0) {
            ForEach(jobs.indices, id: \.self) { i in
                let renderer = jobs[i]
                Canvas(rendersAsynchronously: false) { context, _ in
                    var context = context
                    context.translateBy(x: margin, y: margin)
                    renderer.draw(in: context, size: CGSize(width: size * LighthouseView.aspectRatio, height: size))
                }
                .frame(width: cell, height: cell)
            }
        }
        let renderer = ImageRenderer(content: strip)
        renderer.scale = scale
        renderer.isOpaque = false
        var cut: [CGImage] = []
        if let image = renderer.cgImage {
            for i in jobs.indices {
                let rect = CGRect(x: i * cellPixels, y: 0, width: cellPixels, height: min(cellPixels, image.height))
                if let piece = image.cropping(to: rect) { cut.append(piece) }
            }
        }
        guard cut.count == jobs.count else {
            return Images(body: [], beam: nil, badge: nil, margin: margin, cell: cell)
        }
        let bodyCount = jobs.count - (hasBeam ? 1 : 0) - (hasBadge ? 1 : 0)
        return Images(
            body: Array(cut[0..<bodyCount]),
            beam: hasBeam ? cut[bodyCount] : nil,
            badge: hasBadge ? cut[jobs.count - 1] : nil,
            margin: margin, cell: cell)
    }
}
