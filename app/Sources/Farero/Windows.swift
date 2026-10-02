import AppKit
import FareroCore
import SwiftUI

/// Regular windows opened from the menu. These are first-pass versions;
/// settings, plugin connection and onboarding get their full UI later.
@MainActor
final class WindowManager {
    enum Kind: String {
        case settings, plugins, logSearch, daemonStatus

        var title: String {
            switch self {
            case .settings: "farero 설정"
            case .plugins: "플러그인 연결"
            case .logSearch: "로그 검색"
            case .daemonStatus: "데몬 상태"
            }
        }

        var size: CGSize {
            switch self {
            case .settings: CGSize(width: 460, height: 260)
            case .plugins: CGSize(width: 480, height: 360)
            case .logSearch: CGSize(width: 720, height: 520)
            case .daemonStatus: CGSize(width: 460, height: 320)
            }
        }
    }

    private let model: AppModel
    private let registrar: DaemonRegistrar
    private var windows: [Kind: NSWindow] = [:]

    init(model: AppModel, registrar: DaemonRegistrar) {
        self.model = model
        self.registrar = registrar
    }

    func show(_ kind: Kind) {
        let window = windows[kind] ?? make(kind)
        windows[kind] = window
        NSApp.activate()
        window.makeKeyAndOrderFront(nil)
    }

    private func make(_ kind: Kind) -> NSWindow {
        let content: AnyView = switch kind {
        case .settings: AnyView(SettingsPlaceholderView())
        case .plugins: AnyView(PluginsView(model: model))
        case .logSearch: AnyView(LogSearchView(model: model))
        case .daemonStatus: AnyView(DaemonStatusView(model: model, registrar: registrar))
        }
        let window = NSWindow(contentRect: CGRect(origin: .zero, size: kind.size),
                              styleMask: [.titled, .closable, .miniaturizable, .resizable],
                              backing: .buffered, defer: false)
        window.title = kind.title
        window.contentViewController = NSHostingController(rootView: content)
        window.setContentSize(kind.size)
        window.isReleasedWhenClosed = false
        window.center()
        return window
    }
}

struct SettingsPlaceholderView: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("설정").font(.title2.bold())
            Text("승인 정책, 결과 저장 한도, 업데이트 확인과 Claude Code 연결 설정은 다음 단계에서 이 창에 들어갑니다.")
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }
}

struct PluginsView: View {
    let model: AppModel

    var body: some View {
        let state = model.state
        VStack(alignment: .leading, spacing: 12) {
            Text("플러그인").font(.title2.bold())
            if state.plugins.isEmpty {
                Text(state.isConnected ? "플러그인 정보 없음" : "데몬에 연결되어 있지 않음")
                    .foregroundStyle(.secondary)
            }
            ForEach(state.plugins) { p in
                VStack(alignment: .leading, spacing: 4) {
                    HStack {
                        Text(p.plugin).font(.headline)
                        Text(Self.statusLabel(p.status))
                            .font(.caption)
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(Capsule().fill(Self.statusColor(p.status).opacity(0.2)))
                        if !p.accountLabel.isEmpty {
                            Text(p.accountLabel).foregroundStyle(.secondary)
                        }
                    }
                    if !p.error.isEmpty {
                        Text(p.error).font(.caption).foregroundStyle(.red)
                    }
                    if let prompt = state.pluginPrompts[p.plugin] {
                        HStack {
                            if !prompt.userCode.isEmpty {
                                Text("코드 \(prompt.userCode)").font(.system(.body, design: .monospaced)).textSelection(.enabled)
                            }
                            Button("브라우저 열기") {
                                if let url = URL(string: prompt.url) { NSWorkspace.shared.open(url) }
                            }
                        }
                    }
                }
            }
            Text("연결과 해제는 다음 단계에서 이 창에 추가됩니다.")
                .font(.caption)
                .foregroundStyle(.secondary)
            Spacer()
        }
        .padding(20)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
    }

    static func statusLabel(_ s: String) -> String {
        switch s {
        case "connected": "연결됨"
        case "disconnected": "연결 안 됨"
        case "expired": "만료됨"
        case "error": "오류"
        default: s
        }
    }

    static func statusColor(_ s: String) -> Color {
        switch s {
        case "connected": .green
        case "expired", "error": .red
        default: .gray
        }
    }
}

struct LogSearchView: View {
    let model: AppModel
    @State private var query = ""
    @State private var calls: [Call] = []
    @State private var message: String?
    @State private var selection: Call.ID?

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                TextField("도구, 입력값, 결과 검색", text: $query)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { Task { await search() } }
                Button("검색") { Task { await search() } }
            }
            .padding(12)
            if let message {
                Text(message).font(.caption).foregroundStyle(.secondary).padding(.bottom, 6)
            }
            Table(calls, selection: $selection) {
                TableColumn("시각") { c in
                    Text(c.ts.map { $0.formatted(date: .numeric, time: .standard) } ?? "")
                }
                .width(min: 120, ideal: 150)
                TableColumn("도구") { c in
                    Text(c.plugin.isEmpty ? c.tool : "\(c.plugin) · \(c.tool)")
                }
                TableColumn("결정") { c in Text(CallDecision.label(c.decision)) }
                    .width(min: 70, ideal: 90)
                TableColumn("입력값") { c in
                    Text(c.input.compact()).lineLimit(1).truncationMode(.tail)
                }
                TableColumn("오류") { c in Text(c.error).foregroundStyle(.red) }
                    .width(min: 40, ideal: 80)
            }
        }
        .task { await search() }
    }

    private func search() async {
        do {
            let reply = try await model.client.request(MessageType.logQuery, LogQuery(query: query, limit: 200))
            if case .logResult(let rows) = reply.payload {
                calls = rows
                message = rows.isEmpty ? "결과 없음" : "\(rows.count)건"
            }
        } catch {
            message = error.localizedDescription
        }
    }
}

struct DaemonStatusView: View {
    let model: AppModel
    let registrar: DaemonRegistrar

    var body: some View {
        let s = model.state
        Form {
            LabeledContent("연결") {
                switch s.connection {
                case .connected: Text("연결됨").foregroundStyle(.green)
                case .connecting: Text("연결 중…")
                case .disconnected(let reason): Text("끊김: \(reason)").foregroundStyle(.red)
                }
            }
            LabeledContent("소켓") { Text(model.socketPath).textSelection(.enabled) }
            LabeledContent("데몬 버전") { Text(s.daemonVersion.isEmpty ? "-" : s.daemonVersion) }
            LabeledContent("게이트웨이") {
                if s.gateway.running {
                    Text(s.gateway.url).textSelection(.enabled)
                } else {
                    Text(s.gateway.error.isEmpty ? "실행 안 됨" : s.gateway.error).foregroundStyle(.orange)
                }
            }
            LabeledContent("세션") { Text("진행 중 \(s.activeSessions.count)개 · 전체 \(s.sessions.count)개") }
            LabeledContent("로그인 항목") { Text(model.registrationLabel) }
            if registrar.status == .requiresApproval || registrar.status == .notRegistered {
                Button("로그인 항목 설정 열기") { registrar.openLoginItemsSettings() }
            }
        }
        .formStyle(.grouped)
        .onAppear {
            registrar.refresh()
            model.registrationLabel = registrar.status.label
        }
    }
}
