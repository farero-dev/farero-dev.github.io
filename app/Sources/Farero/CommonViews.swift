import AppKit
import SwiftUI

/// Read-only, selectable, scrollable monospaced text for regular windows
/// (inputs, results, commands). Copes with large text, unlike `Text`.
struct ReadOnlyTextView: NSViewRepresentable {
    let text: String
    var fontSize: CGFloat = 11

    func makeCoordinator() -> Coordinator { Coordinator() }

    final class Coordinator {
        var shown: String?
    }

    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSTextView.scrollableTextView()
        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.borderType = .bezelBorder
        if let tv = scroll.documentView as? NSTextView {
            tv.isEditable = false
            tv.isSelectable = true
            tv.isRichText = false
            tv.font = .monospacedSystemFont(ofSize: fontSize, weight: .regular)
            tv.textColor = .labelColor
            tv.backgroundColor = .textBackgroundColor
            tv.textContainerInset = NSSize(width: 4, height: 4)
            tv.isAutomaticLinkDetectionEnabled = false
            tv.layoutManager?.allowsNonContiguousLayout = true
            tv.string = text
            context.coordinator.shown = text
        }
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let tv = scroll.documentView as? NSTextView else { return }
        if let shown = context.coordinator.shown, shown.utf8.count == text.utf8.count, shown == text { return }
        context.coordinator.shown = text
        tv.string = text
        tv.scroll(.zero)
    }
}

/// A coloured notice line at the top of a window section.
struct Banner: View {
    enum Style { case info, warning, error, success }

    let text: String
    var style: Style = .info

    var body: some View {
        Label {
            Text(text).textSelection(.enabled).fixedSize(horizontal: false, vertical: true)
        } icon: {
            Image(systemName: icon)
        }
        .font(.callout)
        .padding(.horizontal, 10)
        .padding(.vertical, 7)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 8).fill(color.opacity(0.13)))
        .foregroundStyle(style == .info ? Color.primary : color)
    }

    private var icon: String {
        switch style {
        case .info: "info.circle"
        case .warning: "exclamationmark.triangle.fill"
        case .error: "xmark.octagon.fill"
        case .success: "checkmark.circle.fill"
        }
    }

    private var color: Color {
        switch style {
        case .info: .blue
        case .warning: .orange
        case .error: .red
        case .success: .green
        }
    }
}

/// A small coloured capsule label.
struct Badge: View {
    let text: String
    var color: Color = .gray

    var body: some View {
        Text(text)
            .font(.caption2.weight(.semibold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(Capsule().fill(color.opacity(0.18)))
            .foregroundStyle(color)
    }
}

/// A yes/no status line: "✓ 설치됨" / "✗ 없음".
struct CheckRow: View {
    let title: String
    let ok: Bool
    var okText = "설치됨"
    var missingText = "없음"
    var detail: String?

    var body: some View {
        LabeledContent(title) {
            VStack(alignment: .trailing, spacing: 2) {
                Label(ok ? okText : missingText, systemImage: ok ? "checkmark.circle.fill" : "xmark.circle")
                    .foregroundStyle(ok ? Color.green : Color.secondary)
                if let detail, !detail.isEmpty {
                    Text(detail)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .multilineTextAlignment(.trailing)
                }
            }
        }
    }
}
