import FareroCore
import SwiftUI

/// 설정: approval policy, general daemon settings, Claude Code registration.
struct SettingsView: View {
    let model: AppModel
    @State private var agentCfg: AgentCfgModel
    /// `FARERO_DEBUG_SETTINGS_TAB=policy|general|claude` picks the first tab (development).
    @State private var tab = ProcessInfo.processInfo.environment["FARERO_DEBUG_SETTINGS_TAB"] ?? "policy"

    init(model: AppModel) {
        self.model = model
        _agentCfg = State(initialValue: AgentCfgModel(app: model))
    }

    var body: some View {
        TabView(selection: $tab) {
            PolicyTab(model: model)
                .tabItem { Label("승인 정책", systemImage: "checklist") }
                .tag("policy")
            GeneralTab(model: model)
                .tabItem { Label("일반", systemImage: "gearshape") }
                .tag("general")
            ScrollView {
                AgentCfgPanel(model: agentCfg).padding(20)
            }
            .tabItem { Label("Claude Code", systemImage: "terminal") }
            .tag("claude")
        }
        .frame(minWidth: 620, minHeight: 520)
    }
}

// MARK: - 승인 정책 (기능 명세서 6-5)

@MainActor
@Observable
final class PolicyModel {
    private let app: AppModel
    private(set) var tools: [PolicyTool] = []
    private(set) var loading = false
    private(set) var error: String?
    private(set) var saving: Set<String> = []

    init(app: AppModel) {
        self.app = app
    }

    func load() async {
        loading = true
        defer { loading = false }
        do {
            let reply = try await app.client.request(MessageType.policyGet)
            if case .policyState(let list) = reply.payload {
                tools = list
                error = nil
            }
        } catch {
            self.error = error.localizedDescription
        }
    }

    /// Sets a tool's level; "" resets it to the default.
    func set(_ tool: PolicyTool, level: String) async {
        saving.insert(tool.id)
        defer { saving.remove(tool.id) }
        do {
            let reply = try await app.client.request(MessageType.policySet,
                                                     PolicySet(plugin: tool.plugin, tool: tool.tool, level: level))
            if case .policyState(let list) = reply.payload { tools = list }
            error = nil
        } catch {
            self.error = "\(tool.plugin) · \(tool.tool): \(error.localizedDescription)"
        }
    }
}

struct PolicyTab: View {
    let model: AppModel
    @State private var policy: PolicyModel

    init(model: AppModel) {
        self.model = model
        _policy = State(initialValue: PolicyModel(app: model))
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("플러그인 도구마다 자동 허용, 승인, 차단을 고릅니다. 차단한 도구는 에이전트에게 보이지 않습니다. ‘매번 승인’ 도구는 자동 허용으로 바꿀 수 없습니다.")
                .font(.callout)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let error = policy.error {
                Banner(text: error, style: .error)
            }
            List {
                ForEach(PolicyPresentation.grouped(policy.tools)) { group in
                    Section(PolicyPresentation.pluginTitle(group.plugin)) {
                        ForEach(group.tools) { tool in
                            PolicyRow(tool: tool, saving: policy.saving.contains(tool.id)) { level in
                                Task { await policy.set(tool, level: level) }
                            }
                        }
                    }
                }
            }
            .overlay {
                if policy.tools.isEmpty {
                    if policy.loading {
                        ProgressView()
                    } else {
                        Text(model.state.isConnected ? "분류된 도구가 없습니다" : "데몬에 연결되어 있지 않음")
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
        .padding(16)
        .task { await policy.load() }
    }
}

struct PolicyRow: View {
    let tool: PolicyTool
    let saving: Bool
    let onChange: (String) -> Void

    var body: some View {
        HStack(alignment: .center, spacing: 10) {
            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 6) {
                    Text(tool.tool).font(.system(.body, design: .monospaced))
                    if tool.noSession { Badge(text: "매번 승인", color: .purple) }
                    if tool.taint { Badge(text: "오염 소스", color: .orange) }
                    if tool.destructive { Badge(text: "파괴적", color: .red) }
                }
                HStack(spacing: 6) {
                    Text("기본값: \(PolicyPresentation.levelLabel(tool.defaultLevel, noSession: tool.noSession))")
                    if tool.overridden {
                        Text("변경됨").foregroundStyle(.orange)
                        Button("기본값으로") { onChange("") }
                            .buttonStyle(.link)
                    }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }
            Spacer()
            if saving { ProgressView().controlSize(.small) }
            Picker("", selection: Binding(get: { tool.level }, set: { if $0 != tool.level { onChange($0) } })) {
                ForEach(options, id: \.self) { level in
                    Text(PolicyPresentation.levelLabel(level, noSession: tool.noSession)).tag(level)
                }
            }
            .pickerStyle(.segmented)
            .labelsHidden()
            .frame(width: tool.noSession ? 160 : 230)
            .disabled(saving)
        }
        .padding(.vertical, 2)
    }

    private var options: [String] {
        var o = PolicyPresentation.levelOptions(tool)
        if !o.contains(tool.level) { o.insert(tool.level, at: 0) }
        return o
    }
}

// MARK: - 일반

struct GeneralTab: View {
    let model: AppModel
    @State private var error: String?

    static let limits = [4 << 10, 16 << 10, 64 << 10, 256 << 10, 1 << 20]

    var body: some View {
        Form {
            if let error {
                Banner(text: error, style: .error)
            }
            if let s = model.daemonSettings {
                Section {
                    Picker("결과 저장 한도", selection: Binding(get: { s.resultLimitBytes }, set: { v in
                        save(DaemonSettings(resultLimitBytes: v, updateCheck: s.updateCheck))
                    })) {
                        ForEach(limitOptions(current: s.resultLimitBytes), id: \.self) { n in
                            Text(label(n)).tag(n)
                        }
                    }
                } footer: {
                    Text("도구 호출 결과는 로그에 이 크기까지만 저장합니다. 넘는 부분은 잘리고 원래 크기만 기록됩니다.")
                }
                Section {
                    Toggle("업데이트 확인", isOn: Binding(get: { s.updateCheck }, set: { v in
                        save(DaemonSettings(resultLimitBytes: s.resultLimitBytes, updateCheck: v))
                    }))
                    if let update = model.availableUpdate {
                        LabeledContent("새 버전") {
                            Link("\(update.version) 받기", destination: update.url)
                        }
                    }
                } footer: {
                    Text("켜 두면 실행할 때와 하루에 한 번 GitHub에서 최신 릴리스를 확인합니다. 앱이 직접 보내는 네트워크 요청은 이것뿐입니다.")
                }
            } else {
                Text(model.state.isConnected ? "설정을 불러오는 중…" : "데몬에 연결되어 있지 않음")
                    .foregroundStyle(.secondary)
            }
        }
        .formStyle(.grouped)
        .task { await model.refreshDaemonSettings() }
    }

    private func save(_ s: DaemonSettings) {
        Task { error = await model.saveDaemonSettings(s) }
    }

    private func limitOptions(current: Int) -> [Int] {
        Self.limits.contains(current) ? Self.limits : (Self.limits + [current]).sorted()
    }

    private func label(_ n: Int) -> String {
        let text = n >= 1 << 20 && n % (1 << 20) == 0 ? "\(n >> 20) MB" : n % 1024 == 0 ? "\(n >> 10) KB" : "\(n) bytes"
        return n == 16 << 10 ? "\(text) (기본)" : text
    }
}
