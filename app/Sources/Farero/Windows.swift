import AppKit
import FareroCore
import SwiftUI

/// Regular windows opened from the menu. Opening one activates the app,
/// which is fine for something the user asked for.
@MainActor
final class WindowManager {
    enum Kind: String {
        case settings, plugins, logSearch, daemonStatus, onboarding

        var title: String {
            switch self {
            case .settings: "farero 설정"
            case .plugins: "플러그인 연결"
            case .logSearch: "로그 검색"
            case .daemonStatus: "데몬 상태"
            case .onboarding: "farero 설정 도우미"
            }
        }

        var size: CGSize {
            switch self {
            case .settings: CGSize(width: 680, height: 600)
            case .plugins: CGSize(width: 600, height: 640)
            case .logSearch: CGSize(width: 1040, height: 700)
            case .daemonStatus: CGSize(width: 500, height: 420)
            case .onboarding: CGSize(width: 640, height: 600)
            }
        }

        var resizable: Bool { self != .onboarding }
    }

    private let model: AppModel
    private var windows: [Kind: NSWindow] = [:]

    static let defaults = UserDefaults(suiteName: "dev.farero.Farero") ?? .standard

    init(model: AppModel) {
        self.model = model
    }

    func show(_ kind: Kind) {
        let window = windows[kind] ?? make(kind)
        windows[kind] = window
        NSApp.activate()
        window.makeKeyAndOrderFront(nil)
    }

    func close(_ kind: Kind) {
        windows[kind]?.close()
    }

    /// The onboarding runs once, on the first launch of a real install
    /// (`FARERO_SHOW_ONBOARDING=1` forces it in development).
    func showOnboardingIfFirstLaunch() {
        let forced = ProcessInfo.processInfo.environment["FARERO_SHOW_ONBOARDING"] == "1"
        guard forced || (DaemonRegistrar.skipReason() == nil
                         && !Self.defaults.bool(forKey: OnboardingView.completedKey)) else { return }
        show(.onboarding)
    }

    /// Development aid: `FARERO_DEBUG_OPEN=settings,plugins,logSearch,daemonStatus,onboarding`
    /// opens windows at launch so they can be checked without clicking.
    func openDebugWindows() {
        guard let list = ProcessInfo.processInfo.environment["FARERO_DEBUG_OPEN"] else { return }
        for name in list.split(separator: ",") {
            if let kind = Kind(rawValue: String(name)) { show(kind) }
        }
    }

    private func make(_ kind: Kind) -> NSWindow {
        let content: AnyView = switch kind {
        case .settings: AnyView(SettingsView(model: model))
        case .plugins: AnyView(PluginsView(model: model))
        case .logSearch: AnyView(LogSearchView(model: model))
        case .daemonStatus: AnyView(DaemonStatusView(model: model))
        case .onboarding:
            AnyView(OnboardingView(model: model, openPlugins: { [weak self] in self?.show(.plugins) },
                                   finish: { [weak self] in
                                       Self.defaults.set(true, forKey: OnboardingView.completedKey)
                                       self?.close(.onboarding)
                                   }))
        }
        var style: NSWindow.StyleMask = [.titled, .closable, .miniaturizable]
        if kind.resizable { style.insert(.resizable) }
        let window = NSWindow(contentRect: CGRect(origin: .zero, size: kind.size), styleMask: style,
                              backing: .buffered, defer: false)
        window.title = kind.title
        window.contentViewController = NSHostingController(rootView: content)
        window.setContentSize(kind.size)
        window.isReleasedWhenClosed = false
        window.center()
        return window
    }
}

struct DaemonStatusView: View {
    let model: AppModel

    var body: some View {
        let s = model.state
        let registrar = model.registrar
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
            LabeledContent("앱 버전") { Text(AppModel.appVersion ?? "개발 빌드") }
            if let notice = model.versionNotice {
                Banner(text: notice, style: .info)
            }
            LabeledContent("게이트웨이") {
                if s.gateway.running {
                    Text(s.gateway.url).textSelection(.enabled)
                } else {
                    Text(s.gateway.error.isEmpty ? "실행 안 됨" : s.gateway.error).foregroundStyle(.orange)
                }
            }
            LabeledContent("세션") { Text("진행 중 \(s.activeSessions.count)개 · 전체 \(s.sessions.count)개") }
            LabeledContent("승인 대기") { Text("\(s.approvals.count)개") }
            LabeledContent("로그인 항목") { Text(model.registrationLabel) }
            if registrar.status.needsAttention {
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
