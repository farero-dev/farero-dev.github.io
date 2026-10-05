import AppKit
import FareroCore
import SwiftUI

/// The approval card stack below the notch (기능 명세서 5-3). Cards stack in
/// arrival order; the front (oldest) card is shown in full and is the one
/// the shortcuts act on.
struct ApprovalContent: View {
    let model: AppModel
    let barHeight: CGFloat

    var body: some View {
        let approvals = model.state.approvals
        VStack(spacing: 8) {
            HStack {
                CharacterView(state: model.character, size: NotchStyle.compactCharacter)
                Spacer()
                if approvals.count > 1 {
                    Text("\(approvals.count)개 대기")
                        .font(.system(size: 11, weight: .semibold))
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                        .background(Capsule().fill(Color.yellow.opacity(0.25)))
                        .foregroundStyle(.yellow)
                }
            }
            .padding(.horizontal, 14)
            .frame(height: barHeight)

            if let front = approvals.first {
                ApprovalCard(model: model, approval: front)
                    .id(front.id)
                    .transition(.asymmetric(insertion: .move(edge: .bottom).combined(with: .opacity),
                                            removal: .opacity))
            }
            if approvals.count > 1 {
                // The cards waiting behind the front one.
                VStack(spacing: 3) {
                    ForEach(0..<min(approvals.count - 1, 2), id: \.self) { i in
                        RoundedRectangle(cornerRadius: 3)
                            .fill(Color.white.opacity(0.14 - Double(i) * 0.05))
                            .frame(height: 4)
                            .padding(.horizontal, 24 + CGFloat(i) * 14)
                    }
                }
            }
            if let toast = model.toast {
                ToastView(text: toast)
            }
        }
        .padding(.horizontal, 10)
        .padding(.bottom, 14)
        .frame(width: NotchStyle.approvalWidth)
    }
}

struct ApprovalCard: View {
    let model: AppModel
    let approval: Approval

    var body: some View {
        let answering = model.state.answering[approval.id]
        VStack(alignment: .leading, spacing: 10) {
            header
            if let headline = ApprovalPresentation.headline(approval) {
                Text(headline)
                    .font(.system(size: 12, design: .monospaced))
                    .lineLimit(3)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(RoundedRectangle(cornerRadius: 8).fill(Color.white.opacity(0.08)))
            }
            reasons
            inputSection(model.inputDisplay(for: approval))
            buttons(answering: answering)
        }
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 16).fill(Color.white.opacity(0.07)))
    }

    private func inputSection(_ input: ApprovalPresentation.InputDisplay) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack {
                Text("입력값 전체")
                    .font(.system(size: 11, weight: .medium))
                    .foregroundStyle(.secondary)
                if input.isTruncated {
                    Text("· \(ByteCountFormatter.string(fromByteCount: Int64(input.totalBytes), countStyle: .file)) 중 앞부분만 표시")
                        .font(.system(size: 11))
                        .foregroundStyle(.orange)
                }
                Spacer()
                Button("전체 복사") {
                    NSPasteboard.general.clearContents()
                    NSPasteboard.general.setString(input.full, forType: .string)
                    model.showToast("입력값 전체를 복사함")
                }
                .buttonStyle(.plain)
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
            }
            JSONTextView(text: input.text)
                .frame(height: JSONTextView.height(for: input.text))
                .clipShape(RoundedRectangle(cornerRadius: 8))
        }
    }

    private var header: some View {
        HStack(alignment: .top, spacing: 10) {
            VStack(alignment: .leading, spacing: 4) {
                Text(ApprovalPresentation.toolTitle(approval))
                    .font(.system(size: 15, weight: .semibold, design: .monospaced))
                    .lineLimit(1)
                    .truncationMode(.middle)
                HStack(spacing: 6) {
                    Text(SessionDisplay.agentName(approval.agent))
                    Text("·")
                    Text(approval.sessionLabel.isEmpty ? "세션 불명" : approval.sessionLabel)
                    if approval.isPlugin {
                        Text("·")
                        Text("플러그인 \(approval.plugin)")
                    } else {
                        Text("·")
                        Text("에이전트 도구")
                    }
                }
                .font(.system(size: 11))
                .foregroundStyle(.secondary)
                .lineLimit(1)
            }
            Spacer(minLength: 0)
            if approval.deadline != nil {
                TimelineView(.periodic(from: .now, by: 1)) { context in
                    Label(ApprovalPresentation.countdown(deadline: approval.deadline, now: context.date) ?? "",
                          systemImage: "timer")
                        .font(.system(size: 11, weight: .medium).monospacedDigit())
                        .foregroundStyle(.secondary)
                }
                .help("이 시간이 지나면 자동으로 거부됨")
            }
        }
    }

    private var reasons: some View {
        VStack(alignment: .leading, spacing: 3) {
            ForEach(approval.reasons, id: \.self) { reason in
                Label(ApprovalPresentation.reasonText(reason), systemImage: Self.icon(reason))
                    .font(.system(size: 11))
                    .foregroundStyle(reason == "tainted" ? Color.orange : Color.secondary)
            }
        }
    }

    private func buttons(answering: ApprovalAnswer?) -> some View {
        HStack(spacing: 8) {
            ApprovalButton(title: "허용", hint: ApprovalShortcut.allow.hint, role: .primary,
                           busy: answering == .allow) {
                model.answer(approval.id, .allow)
            }
            if approval.allowSession {
                ApprovalButton(title: "이번 세션 동안 허용", hint: ApprovalShortcut.allowSession.hint, role: .secondary,
                               busy: answering == .allowSession) {
                    model.answer(approval.id, .allowSession)
                }
            }
            Spacer(minLength: 0)
            ApprovalButton(title: "거부", hint: ApprovalShortcut.deny.hint, role: .destructive,
                           busy: answering == .deny) {
                model.answer(approval.id, .deny)
            }
        }
        .disabled(answering != nil)
    }

    static func icon(_ reason: String) -> String {
        switch reason {
        case "policy": "list.bullet.rectangle"
        case "tainted": "exclamationmark.triangle.fill"
        case "unknown_session": "questionmark.circle"
        case "agent_request": "hand.raised"
        default: "info.circle"
        }
    }
}

struct ApprovalButton: View {
    enum Role { case primary, secondary, destructive }

    let title: String
    let hint: String
    let role: Role
    let busy: Bool
    let action: () -> Void

    @Environment(\.isEnabled) private var isEnabled

    var body: some View {
        Button(action: action) {
            HStack(spacing: 6) {
                if busy {
                    ProgressView().controlSize(.mini)
                }
                Text(title)
                    .font(.system(size: 12, weight: .semibold))
                Text(hint)
                    .font(.system(size: 10, weight: .medium))
                    .opacity(0.6)
            }
            .padding(.horizontal, 12)
            .padding(.vertical, 7)
            .foregroundStyle(foreground)
            .background(RoundedRectangle(cornerRadius: 9).fill(background))
            .opacity(isEnabled ? 1 : 0.55)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(title)
        .accessibilityHint("단축키 \(hint)")
    }

    private var foreground: Color {
        switch role {
        case .primary: .black
        case .secondary: .white
        case .destructive: Color(red: 1, green: 0.55, blue: 0.55)
        }
    }

    private var background: Color {
        switch role {
        case .primary: .white
        case .secondary: Color.white.opacity(0.14)
        case .destructive: Color.red.opacity(0.22)
        }
    }
}

/// Read-only, scrollable, selectable text for the full input. An NSTextView
/// copes with megabyte inputs far better than a SwiftUI Text.
struct JSONTextView: NSViewRepresentable {
    let text: String

    static let maxHeight: CGFloat = 200

    /// Fits short inputs, scrolls long ones.
    static func height(for text: String) -> CGFloat {
        // Newlines plus a rough count of wrapped lines (about 70 columns).
        var lines = 1 + text.utf8.count / 70
        for c in text.utf8 where c == 0x0A {
            lines += 1
            if lines > 16 { break }
        }
        return min(CGFloat(lines) * 15 + 14, maxHeight)
    }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        scroll.drawsBackground = true
        scroll.backgroundColor = NSColor(white: 1, alpha: 0.06)
        scroll.hasVerticalScroller = true
        scroll.hasHorizontalScroller = false
        scroll.autohidesScrollers = true
        scroll.scrollerStyle = .overlay
        if let tv = scroll.documentView as? NSTextView {
            tv.isEditable = false
            tv.isSelectable = true
            tv.isRichText = false
            tv.drawsBackground = false
            tv.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
            tv.textColor = NSColor(white: 0.92, alpha: 1)
            tv.textContainerInset = NSSize(width: 6, height: 6)
            tv.isAutomaticLinkDetectionEnabled = false
            tv.layoutManager?.allowsNonContiguousLayout = true
            tv.string = text
            context.coordinator.shown = text
        }
        return scroll
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator {
        var shown: String?
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        // The text rarely changes (cards are keyed by approval id). Compare
        // byte lengths first so a large input is not compared every second.
        guard let tv = scroll.documentView as? NSTextView else { return }
        if let shown = context.coordinator.shown, shown.utf8.count == text.utf8.count, shown == text { return }
        context.coordinator.shown = text
        tv.string = text
    }
}
