import AppKit
import QuartzCore
import SwiftUI
import XCTest

@testable import LighthouseKit

/// The Core Animation playback of `LighthouseView` (`LighthouseAnimationPlan`,
/// `LighthouseLayerView`).
final class LighthouseAnimationTests: XCTestCase {
    private let sizes: [CGFloat] = [22, 48, 120]

    // MARK: - Plan

    func testPlanExistsExactlyForAnimatedStates() {
        for size in sizes {
            for state in LighthouseState.allCases {
                let animated = LighthouseLight.isAnimated(state, reduceMotion: false, size: size)
                XCTAssertEqual(LighthouseAnimationPlan(state: state, size: size) != nil, animated, "\(state) @ \(size)")
            }
        }
        // Next to the notch the breath is not drawn, so 입력 대기 stands still.
        XCTAssertNil(LighthouseAnimationPlan(state: .waitingInput, size: 22))
        XCTAssertNotNil(LighthouseAnimationPlan(state: .waitingInput, size: 120))
    }

    /// The whole drawing repeats every `period`: the lamp, the motion and the
    /// "z" agree at t and t + period.
    func testCyclePeriodRepeatsTheDrawing() {
        for size in [22.0, 120.0] as [CGFloat] {
            for state in LighthouseState.allCases {
                guard let plan = LighthouseAnimationPlan(state: state, size: size) else { continue }
                let p = plan.period
                XCTAssertGreaterThan(p, 0, "\(state)")
                for track in [
                    plan.lamp?.period, plan.beamPeriod, LighthouseAnimationPlan.period(of: plan.motion),
                    plan.risingZ ? LighthouseLight.Timing.sleepZPeriod : nil,
                ].compactMap({ $0 }) {
                    let cycles = p / track
                    XCTAssertEqual(cycles, cycles.rounded(), accuracy: 1e-9, "\(state): \(track) divides \(p)")
                }
                for epoch in [0.000_31, 812_345_678.000_31] {
                    for i in 0..<400 {
                        let t = epoch + p * Double(i) / 400
                        let a = LighthouseLight.output(for: state, at: t, reduceMotion: false)
                        let b = LighthouseLight.output(for: state, at: t + p, reduceMotion: false)
                        XCTAssertEqual(a.isOn, b.isOn, "\(state) @ \(size) t=\(t)")
                        XCTAssertEqual(a.color, b.color, "\(state) @ \(size) t=\(t)")
                        XCTAssertEqual(a.glassColor, b.glassColor, "\(state) @ \(size) t=\(t)")
                        XCTAssertEqual(a.beamReach ?? 9, b.beamReach ?? 9, accuracy: 1e-5, "\(state) t=\(t)")
                        let pa = LighthouseLight.pose(for: state, at: t, reduceMotion: false, size: size)
                        let pb = LighthouseLight.pose(for: state, at: t + p, reduceMotion: false, size: size)
                        XCTAssertEqual(pa.offsetX, pb.offsetX, accuracy: 1e-5, "\(state) t=\(t)")
                        XCTAssertEqual(pa.offsetY, pb.offsetY, accuracy: 1e-5, "\(state) t=\(t)")
                        XCTAssertEqual(pa.breath, pb.breath, accuracy: 1e-7, "\(state) t=\(t)")
                        guard state.badge == .sleepZ else { continue }
                        let za = LighthouseLight.sleepZ(at: t, reduceMotion: false)
                        let zb = LighthouseLight.sleepZ(at: t + p, reduceMotion: false)
                        XCTAssertEqual(za.offsetY, zb.offsetY, accuracy: 1e-5, "\(state) t=\(t)")
                        XCTAssertEqual(za.opacity, zb.opacity, accuracy: 1e-5, "\(state) t=\(t)")
                    }
                }
            }
        }
    }

    func testCyclePeriods() {
        func period(_ state: LighthouseState, _ size: CGFloat) -> TimeInterval? {
            LighthouseAnimationPlan(state: state, size: size)?.period
        }
        // 22 pt: no breath.
        XCTAssertEqual(period(.working, 22), 3.2)
        XCTAssertEqual(period(.workingMany, 22), 1.6)
        XCTAssertEqual(period(.thinking, 22), 2)
        XCTAssertEqual(period(.needsApproval, 22), 1)
        XCTAssertEqual(period(.error, 22), 2)
        XCTAssertEqual(period(.sleeping, 22), 2.4)
        // 120 pt: the 3.6 s breath joins in (each track still loops on its own).
        XCTAssertEqual(period(.working, 120), 28.8)
        XCTAssertEqual(period(.waitingInput, 120), 3.6)
    }

    /// Key times start at 0, increase and stay below 1; each step shows the
    /// lamp `LighthouseLight` gives for that moment.
    func testLampKeyTimes() {
        for size in sizes {
            for state in LighthouseState.allCases {
                guard let lamp = LighthouseAnimationPlan(state: state, size: size)?.lamp else { continue }
                XCTAssertEqual(lamp.keyTimes.first, 0, "\(state)")
                XCTAssertEqual(lamp.keyTimes.count, lamp.outputs.count)
                XCTAssertTrue(zip(lamp.keyTimes, lamp.keyTimes.dropFirst()).allSatisfy { $0 < $1 }, "\(state)")
                XCTAssertTrue(lamp.keyTimes.allSatisfy { $0 >= 0 && $0 < 1 }, "\(state)")
                for (i, start) in lamp.keyTimes.enumerated() {
                    let end = i + 1 < lamp.keyTimes.count ? lamp.keyTimes[i + 1] : 1
                    for f in stride(from: start + 0.001, to: end, by: 0.01) {
                        let truth = LighthouseLight.output(for: state, at: f * lamp.period, reduceMotion: false)
                        XCTAssertEqual(truth.isOn, lamp.outputs[i].isOn, "\(state) f=\(f)")
                        XCTAssertEqual(truth.color, lamp.outputs[i].color, "\(state) f=\(f)")
                    }
                }
            }
        }
        let q = LighthouseAnimationPlan(state: .needsApproval, size: 22)!.lamp!
        XCTAssertEqual(q.keyTimes, [0, 0.45])
        XCTAssertEqual(q.outputs.map(\.isOn), [true, false])
    }

    /// A state needs at most four images (two lamp steps, beam, badge),
    /// whatever the size: continuous motion is a transform, not frames.
    func testImageCountBounds() {
        for size in sizes {
            for state in LighthouseState.allCases {
                guard let plan = LighthouseAnimationPlan(state: state, size: size) else { continue }
                XCTAssertGreaterThanOrEqual(plan.imageCount(for: state), 1)
                XCTAssertLessThanOrEqual(plan.imageCount(for: state), 4, "\(state)")
                XCTAssertLessThanOrEqual(plan.lamp?.outputs.count ?? 0, 2, "\(state)")
            }
        }
    }

    /// Played in a loop, the lamp (and the badge blinking with it) never
    /// flashes more than once per second.
    func testLoopedLampNeverFlashesFasterThanOncePerSecond() {
        for state in LighthouseState.allCases {
            guard let plan = LighthouseAnimationPlan(state: state, size: 22), let lamp = plan.lamp else { continue }
            var events: [(TimeInterval, LampOutput)] = []
            var cycle = 0.0
            while cycle < 30 {
                for (k, o) in zip(lamp.keyTimes, lamp.outputs) { events.append((cycle + k * lamp.period, o)) }
                cycle += lamp.period
            }
            var flashes: [TimeInterval] = []
            for (previous, current) in zip(events, events.dropFirst()) {
                let turnedOn = !previous.1.isOn && current.1.isOn
                let changedColor = previous.1.isOn && current.1.isOn && previous.1.color != current.1.color
                if turnedOn || changedColor { flashes.append(current.0) }
            }
            XCTAssertFalse(flashes.isEmpty, "\(state) does change")
            for (a, b) in zip(flashes, flashes.dropFirst()) {
                XCTAssertGreaterThanOrEqual(b - a, 1 - 1e-9, "\(state)")
            }
        }
    }

    func testLeastCommonMultiple() {
        XCTAssertEqual(LighthouseAnimationPlan.leastCommonMultiple([3.2, 3.6]), 28.8)
        XCTAssertEqual(LighthouseAnimationPlan.leastCommonMultiple([2, 0.25]), 2)
        XCTAssertEqual(LighthouseAnimationPlan.leastCommonMultiple([2.4]), 2.4)
        XCTAssertEqual(LighthouseAnimationPlan.leastCommonMultiple([]), 0)
    }

    // MARK: - Layers

    @MainActor
    private func layerView(_ state: LighthouseState, size: CGFloat = 22) -> LighthouseLayerView {
        let view = LighthouseLayerView(frame: NSRect(x: 0, y: 0, width: size, height: size))
        view.show(.init(state: state, size: size, sessionCount: 2))
        return view
    }

    /// Where an infinitely repeating animation on `layer` is now, in seconds into its period.
    @MainActor
    private func localTime(_ animation: CAAnimation, on layer: CALayer) -> TimeInterval {
        let elapsed = layer.convertTime(CACurrentMediaTime(), from: nil) - animation.beginTime
        return LighthouseLight.phase(elapsed, animation.duration)
    }

    @MainActor
    func testLayersPlayTheLampStepsInPhaseWithAbsoluteTime() throws {
        let view = layerView(.thinking)
        let steps = try XCTUnwrap(view.bodyLayer.animation(forKey: "lamp") as? CAKeyframeAnimation)
        XCTAssertEqual(steps.keyPath, "contents")
        XCTAssertEqual(steps.calculationMode, .discrete)
        XCTAssertEqual(steps.values?.count, 2)
        XCTAssertEqual(steps.keyTimes, [0, 0.5, 1])
        XCTAssertEqual(steps.duration, 2)
        XCTAssertEqual(steps.repeatCount, .infinity)
        XCTAssertFalse(steps.isRemovedOnCompletion)
        let expected = LighthouseLight.phase(Date().timeIntervalSinceReferenceDate, 2)
        let actual = localTime(steps, on: view.bodyLayer)
        let skew = abs(expected - actual)
        XCTAssertLessThan(min(skew, 2 - skew), 0.05, "phased to absolute time")
        // Thinking does not breathe at 22 pt and has no beam.
        XCTAssertNil(view.bodyLayer.animation(forKey: "motion"))
        XCTAssertNil(view.beamLayer.animation(forKey: "sweep"))
    }

    @MainActor
    func testLayersForEachMotion() throws {
        let working = layerView(.working)
        let sweep = try XCTUnwrap(working.beamLayer.animation(forKey: "sweep") as? CAKeyframeAnimation)
        XCTAssertEqual(sweep.keyPath, "transform.scale.x")
        XCTAssertEqual(sweep.duration, LighthouseLight.Timing.beamPeriod)
        XCTAssertNotNil(working.beamLayer.contents)
        XCTAssertNil(working.bodyLayer.animation(forKey: "lamp"), "the lamp is steady")

        let greeting = layerView(.greeting)
        let hop = try XCTUnwrap(greeting.bodyLayer.animation(forKey: "motion") as? CAKeyframeAnimation)
        XCTAssertEqual(hop.keyPath, "transform.translation.y")
        XCTAssertEqual(hop.keyTimes, [0, 0.4, 0.6, 1])

        let error = layerView(.error)
        let shake = try XCTUnwrap(error.bodyLayer.animation(forKey: "motion") as? CAKeyframeAnimation)
        XCTAssertEqual(shake.keyPath, "transform.translation.x")
        XCTAssertEqual(shake.duration, LighthouseLight.Timing.shakePeriod)

        let approval = layerView(.needsApproval)
        let blink = try XCTUnwrap(approval.badgeLayer.animation(forKey: "blink") as? CAKeyframeAnimation)
        XCTAssertEqual(blink.values as? [Int], [1, 0])
        XCTAssertEqual(blink.keyTimes, [0, 0.45, 1])

        let sleeping = layerView(.sleeping)
        XCTAssertNotNil(sleeping.badgeLayer.animation(forKey: "z"))

        let breathing = layerView(.waitingInput, size: 120)
        let breath = try XCTUnwrap(breathing.bodyLayer.animation(forKey: "motion") as? CAKeyframeAnimation)
        XCTAssertEqual(breath.keyPath, "transform.scale.y")
    }

    /// The layers cover the frame plus a margin, so drawing above the frame
    /// (hop, stretch) is kept, and nothing clips them.
    @MainActor
    func testLayersOverflowTheFrame() {
        let view = layerView(.needsApproval)
        XCTAssertEqual(view.layer?.masksToBounds, false)
        for layer in [view.beamLayer, view.bodyLayer, view.badgeLayer] {
            XCTAssertFalse(layer.masksToBounds)
            XCTAssertGreaterThanOrEqual(layer.bounds.width, 22 + 2 * (2 * 22 / 24) - 0.01)
            XCTAssertEqual(layer.frame.midX, 11, accuracy: 0.01)
            XCTAssertEqual(layer.frame.midY, 11, accuracy: 0.01)
        }
        XCTAssertNil(view.hitTest(NSPoint(x: 11, y: 11)), "clicks pass through")
    }

    @MainActor
    func testStillStatesHaveNoLayersInstalled() {
        let view = layerView(.denied)
        XCTAssertNil(view.bodyLayer.contents)
        XCTAssertNil(view.bodyLayer.animationKeys())
    }

    /// Building the images happens on the main actor at every state change,
    /// so it has to be quick. The first build in a process also warms up
    /// SwiftUI's renderer and the badge fonts; it is measured separately.
    @MainActor
    func testBuildingTheImagesIsQuick() {
        func build(_ state: LighthouseState, _ size: CGFloat, _ plan: LighthouseAnimationPlan) -> TimeInterval {
            let start = CACurrentMediaTime()
            let images = LighthouseLayerImages.build(
                .init(state: state, size: size, sessionCount: 3), plan: plan, scale: 2)
            // ImageRenderer's CGImage renders lazily: count the pixels in.
            for image in images.body + [images.beam, images.badge].compactMap({ $0 }) {
                XCTAssertGreaterThan(CFDataGetLength(image.dataProvider?.data), 0)
            }
            let elapsed = CACurrentMediaTime() - start
            XCTAssertEqual(images.body.count, plan.lamp?.outputs.count ?? 1, "\(state)")
            XCTAssertEqual(images.beam != nil, plan.beamPeriod != nil, "\(state)")
            XCTAssertEqual(images.badge != nil, state.badge != .none, "\(state)")
            let side = Int((images.cell * 2).rounded())
            XCTAssertEqual(images.body.first?.width, side)
            XCTAssertEqual(images.body.first?.height, side)
            return elapsed
        }
        let cold = build(.sleeping, 22, LighthouseAnimationPlan(state: .sleeping, size: 22)!)
        var report = [String(format: "first build %.1f ms", cold * 1000)]
        for size in [22.0, 48.0, 120.0] as [CGFloat] {
            var worst = 0.0
            for state in LighthouseState.allCases {
                guard let plan = LighthouseAnimationPlan(state: state, size: size) else { continue }
                worst = max(worst, build(state, size, plan))
            }
            report.append(String(format: "%.0f pt worst %.1f ms", size, worst * 1000))
            XCTAssertLessThan(worst, 0.03, "\(size) pt")
        }
        print("LighthouseLayerImages build time @2x: " + report.joined(separator: ", "))
    }
}
