import FareroCore
import SwiftUI

/// The black island: square top edge flaring into the menu bar, rounded
/// bottom corners, like the notch it extends.
struct NotchShape: Shape {
    var topRadius: CGFloat
    var bottomRadius: CGFloat

    var animatableData: AnimatablePair<CGFloat, CGFloat> {
        get { AnimatablePair(topRadius, bottomRadius) }
        set {
            topRadius = newValue.first
            bottomRadius = newValue.second
        }
    }

    func path(in rect: CGRect) -> Path {
        let t = min(topRadius, rect.width / 4)
        let b = max(0, min(bottomRadius, (rect.width - 2 * t) / 2, rect.height - t))
        var p = Path()
        p.move(to: CGPoint(x: rect.minX, y: rect.minY))
        p.addQuadCurve(to: CGPoint(x: rect.minX + t, y: rect.minY + t), control: CGPoint(x: rect.minX + t, y: rect.minY))
        p.addLine(to: CGPoint(x: rect.minX + t, y: rect.maxY - b))
        p.addQuadCurve(to: CGPoint(x: rect.minX + t + b, y: rect.maxY), control: CGPoint(x: rect.minX + t, y: rect.maxY))
        p.addLine(to: CGPoint(x: rect.maxX - t - b, y: rect.maxY))
        p.addQuadCurve(to: CGPoint(x: rect.maxX - t, y: rect.maxY - b), control: CGPoint(x: rect.maxX - t, y: rect.maxY))
        p.addLine(to: CGPoint(x: rect.maxX - t, y: rect.minY + t))
        p.addQuadCurve(to: CGPoint(x: rect.maxX, y: rect.minY), control: CGPoint(x: rect.maxX - t, y: rect.minY))
        p.closeSubpath()
        return p
    }
}

enum NotchStyle {
    static let wing: CGFloat = 46
    static let compactCharacter: CGFloat = 22
    static let expandedWidth: CGFloat = 440
    static let approvalWidth: CGFloat = 520
    static let rowHeight: CGFloat = 48
    static let maxVisibleRows = 5

    static func topRadius(_ mode: NotchMode) -> CGFloat {
        switch mode {
        case .hidden, .compact: 6
        case .expanded, .approval: 12
        }
    }

    static func bottomRadius(_ mode: NotchMode) -> CGFloat {
        switch mode {
        case .hidden, .compact: 10
        case .expanded, .approval: 24
        }
    }
}

/// The panel's whole content: the island at the top centre, transparent
/// everywhere else.
struct NotchRootView: View {
    let model: AppModel
    /// Reports the island frame (panel coordinates, top-left origin) for
    /// hit testing.
    let onIslandFrame: (CGRect) -> Void

    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let mode = model.mode
        let metrics = model.metrics
        VStack(spacing: 0) {
            island(mode: mode, metrics: metrics)
            Spacer(minLength: 0)
        }
        .frame(width: metrics.panelSize.width, height: metrics.panelSize.height, alignment: .top)
        .animation(animation, value: mode)
        .animation(animation, value: model.state.approvals.map(\.id))
        .animation(animation, value: model.state.activeSessions.count)
        .animation(animation, value: model.toast)
        .environment(\.colorScheme, .dark)
    }

    private var animation: Animation {
        reduceMotion ? .easeInOut(duration: 0.15) : .spring(response: 0.42, dampingFraction: 0.8)
    }

    /// Height of the strip level with the notch (or the menu bar).
    private func barHeight(_ m: NotchMetrics) -> CGFloat {
        m.hasNotch ? m.notchSize.height : max(m.notchSize.height, 30)
    }

    @ViewBuilder
    private func island(mode: NotchMode, metrics: NotchMetrics) -> some View {
        let t = NotchStyle.topRadius(mode)
        let bar = barHeight(metrics)
        let shape = NotchShape(topRadius: t, bottomRadius: NotchStyle.bottomRadius(mode))
        Group {
            switch mode {
            case .hidden:
                // On a notch the island shrinks into it; elsewhere it slides up.
                Color.clear.frame(width: metrics.hasNotch ? metrics.notchSize.width : NotchStyle.wing * 2, height: bar)
            case .compact:
                CompactContent(model: model, metrics: metrics, barHeight: bar)
                    .transition(.opacity)
            case .expanded:
                ExpandedContent(model: model, barHeight: bar)
                    .transition(.opacity)
            case .approval:
                ApprovalContent(model: model, barHeight: bar)
                    .transition(.opacity)
            }
        }
        .padding(.horizontal, t)
        .background(shape.fill(Color.black))
        .clipShape(shape)
        .contentShape(shape)
        .opacity(mode == .hidden ? 0 : 1)
        .offset(y: mode == .hidden && !metrics.hasNotch ? -bar - 8 : 0)
        .onGeometryChange(for: CGRect.self, of: { $0.frame(in: .global) }, action: onIslandFrame)
    }
}

/// Compact: the character left of the notch and the session count right of it.
struct CompactContent: View {
    let model: AppModel
    let metrics: NotchMetrics
    let barHeight: CGFloat

    var body: some View {
        let sessions = model.state.activeSessions
        HStack(spacing: 0) {
            CharacterView(state: model.character, size: NotchStyle.compactCharacter)
                .frame(width: NotchStyle.wing)
            Color.clear.frame(width: metrics.hasNotch ? metrics.notchSize.width : 0)
            HStack(spacing: 3) {
                if sessions.contains(where: \.tainted) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.system(size: 9, weight: .bold))
                        .foregroundStyle(.orange)
                }
                Text("\(sessions.count)")
                    .font(.system(size: 13, weight: .semibold, design: .rounded))
                    .monospacedDigit()
                    .foregroundStyle(.white)
            }
            .frame(width: NotchStyle.wing)
        }
        .frame(height: barHeight)
        .contentShape(Rectangle())
        .onTapGesture { model.togglePinned() }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("farero: 세션 \(sessions.count)개, \(model.character.lighthouse.label)")
    }
}

/// Expanded: the character, a summary and the session list.
struct ExpandedContent: View {
    let model: AppModel
    let barHeight: CGFloat

    var body: some View {
        let state = model.state
        let sessions = state.activeSessions
        VStack(alignment: .leading, spacing: 10) {
            Color.clear.frame(height: barHeight - 6)
                .contentShape(Rectangle())
                .onTapGesture { model.togglePinned() }
            HStack(spacing: 12) {
                CharacterView(state: model.character, size: 48)
                VStack(alignment: .leading, spacing: 3) {
                    Text(model.character.lighthouse.label)
                        .font(.system(size: 15, weight: .semibold))
                    Text("세션 \(sessions.count)개 · 승인 대기 \(state.approvals.count)개")
                        .font(.system(size: 12))
                        .foregroundStyle(.secondary)
                }
                Spacer(minLength: 0)
                if model.pinned {
                    Image(systemName: "pin.fill")
                        .font(.system(size: 11))
                        .foregroundStyle(.secondary)
                        .help("고정됨: 밖을 클릭하면 닫힘")
                }
            }
            if case .disconnected(let reason) = state.connection {
                Label("데몬 연결 끊김 · 다시 연결 중 (\(reason))", systemImage: "bolt.horizontal.circle")
                    .font(.system(size: 11))
                    .foregroundStyle(.orange)
                    .lineLimit(2)
            }
            if let toast = model.toast {
                ToastView(text: toast)
            }
            sessionList(sessions)
        }
        .padding(.horizontal, 16)
        .padding(.bottom, 14)
        .frame(width: NotchStyle.expandedWidth)
    }

    @ViewBuilder
    private func sessionList(_ sessions: [Session]) -> some View {
        if sessions.isEmpty {
            Text("진행 중인 세션 없음")
                .font(.system(size: 12))
                .foregroundStyle(.secondary)
        } else {
            let visible = CGFloat(min(sessions.count, NotchStyle.maxVisibleRows))
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(sessions) { s in
                        SessionRow(model: model, session: s)
                            .frame(height: NotchStyle.rowHeight)
                    }
                }
            }
            .scrollIndicators(sessions.count > NotchStyle.maxVisibleRows ? .automatic : .never)
            .frame(height: visible * NotchStyle.rowHeight)
        }
    }
}

struct SessionRow: View {
    let model: AppModel
    let session: Session
    /// Deleting asks inline: an alert would activate the app and take focus.
    @State private var confirmingDelete = false
    @State private var deleting = false

    var body: some View {
        if confirmingDelete {
            deleteConfirmation
        } else {
            summary
                .contextMenu {
                    Button("터미널로 이동") { model.jump(to: session) }.disabled(session.tty.isEmpty)
                    Divider()
                    Button("세션 삭제…", role: .destructive) { confirmingDelete = true }
                }
        }
    }

    private var deleteConfirmation: some View {
        HStack(spacing: 8) {
            Image(systemName: "trash").foregroundStyle(.red)
            VStack(alignment: .leading, spacing: 2) {
                Text("세션과 그 로그를 삭제합니다")
                    .font(.system(size: 12, weight: .semibold))
                Text(SessionDisplay.name(session, now: .now))
                    .font(.system(size: 11))
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer(minLength: 0)
            if deleting { ProgressView().controlSize(.small) }
            Button("취소") { confirmingDelete = false }
                .buttonStyle(.plain)
                .font(.system(size: 12))
                .padding(.horizontal, 10)
                .padding(.vertical, 5)
                .background(RoundedRectangle(cornerRadius: 7).fill(Color.white.opacity(0.12)))
            Button("삭제") {
                deleting = true
                Task {
                    if let error = await model.deleteSession(session.id) {
                        model.showToast("삭제하지 못함: \(error)")
                        confirmingDelete = false
                    }
                    deleting = false
                }
            }
            .buttonStyle(.plain)
            .font(.system(size: 12, weight: .semibold))
            .foregroundStyle(Color(red: 1, green: 0.55, blue: 0.55))
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(RoundedRectangle(cornerRadius: 7).fill(Color.red.opacity(0.25)))
            .disabled(deleting)
        }
    }

    private var summary: some View {
        let pending = model.state.pendingApprovalCount(sessionID: session.id)
        return HStack(spacing: 10) {
            Circle()
                .fill(statusColor)
                .frame(width: 8, height: 8)
            VStack(alignment: .leading, spacing: 3) {
                Text(SessionDisplay.name(session, now: .now))
                    .font(.system(size: 13, weight: .medium))
                    .lineLimit(1)
                    .truncationMode(.middle)
                HStack(spacing: 6) {
                    Text(session.status.label)
                    if !session.currentTool.isEmpty {
                        Text(session.currentTool)
                            .font(.system(size: 10, design: .monospaced))
                            .padding(.horizontal, 5)
                            .padding(.vertical, 1)
                            .background(Capsule().fill(Color.white.opacity(0.12)))
                    }
                    if session.tainted {
                        Label("오염됨", systemImage: "exclamationmark.triangle.fill")
                            .foregroundStyle(.orange)
                    }
                    if pending > 0 {
                        Text("승인 \(pending)개 대기")
                            .foregroundStyle(.yellow)
                    }
                }
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .lineLimit(1)
            }
            Spacer(minLength: 0)
            Button {
                model.jump(to: session)
            } label: {
                Image(systemName: "terminal")
                    .font(.system(size: 13))
                    .frame(width: 30, height: 26)
                    .background(RoundedRectangle(cornerRadius: 7).fill(Color.white.opacity(0.1)))
            }
            .buttonStyle(.plain)
            .disabled(session.tty.isEmpty)
            .help(session.tty.isEmpty ? "터미널 정보 없음" : "터미널로 이동 (\(session.tty))")
            .accessibilityLabel("터미널로 이동")
            Button {
                confirmingDelete = true
            } label: {
                Image(systemName: "trash")
                    .font(.system(size: 12))
                    .frame(width: 26, height: 26)
                    .background(RoundedRectangle(cornerRadius: 7).fill(Color.white.opacity(0.06)))
            }
            .buttonStyle(.plain)
            .foregroundStyle(.secondary)
            .help("세션 삭제")
            .accessibilityLabel("세션 삭제")
        }
    }

    private var statusColor: Color {
        switch session.status {
        case .running: .green
        case .waitingApproval: .yellow
        case .waitingInput: .blue
        case .ended, .unknown: .gray
        }
    }
}

struct ToastView: View {
    let text: String

    var body: some View {
        Text(text)
            .font(.system(size: 11, weight: .medium))
            .padding(.horizontal, 10)
            .padding(.vertical, 5)
            .background(Capsule().fill(Color.white.opacity(0.14)))
            .transition(.opacity)
    }
}
