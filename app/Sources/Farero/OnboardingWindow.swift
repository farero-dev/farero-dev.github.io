import FareroCore
import SwiftUI

/// 설정 도우미 (기능 명세서 F-13): daemon, Claude Code, plugins, done.
/// Shown on first launch and from the menu.
struct OnboardingView: View {
    let model: AppModel
    let openPlugins: () -> Void
    let finish: () -> Void
    @State private var step = 0
    @State private var agentCfg: AgentCfgModel

    static let completedKey = "onboardingCompleted"
    static let titles = ["데몬", "Claude Code", "플러그인", "완료"]

    init(model: AppModel, openPlugins: @escaping () -> Void, finish: @escaping () -> Void) {
        self.model = model
        self.openPlugins = openPlugins
        self.finish = finish
        _agentCfg = State(initialValue: AgentCfgModel(app: model))
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack(spacing: 8) {
                ForEach(Self.titles.indices, id: \.self) { i in
                    HStack(spacing: 6) {
                        Text("\(i + 1)")
                            .font(.caption.bold())
                            .frame(width: 20, height: 20)
                            .background(Circle().fill(i <= step ? Color.accentColor : Color.secondary.opacity(0.25)))
                            .foregroundStyle(i <= step ? Color.white : Color.secondary)
                        Text(Self.titles[i]).font(.callout).foregroundStyle(i == step ? Color.primary : Color.secondary)
                    }
                    if i < Self.titles.count - 1 {
                        Rectangle().fill(Color.secondary.opacity(0.3)).frame(height: 1)
                    }
                }
            }
            .padding(16)
            Divider()
            ScrollView {
                Group {
                    switch step {
                    case 0: DaemonStep(model: model)
                    case 1: claudeStep
                    case 2: pluginStep
                    default: doneStep
                    }
                }
                .padding(20)
                .frame(maxWidth: .infinity, alignment: .leading)
            }
            Divider()
            HStack {
                if step > 0 { Button("이전") { step -= 1 } }
                Spacer()
                if step < Self.titles.count - 1 {
                    if step == 2 { Button("건너뛰기") { step += 1 } }
                    Button("다음") { step += 1 }.keyboardShortcut(.defaultAction)
                } else {
                    Button("시작하기") { finish() }.keyboardShortcut(.defaultAction)
                }
            }
            .padding(16)
        }
        .frame(width: 640, height: 600)
    }

    private var claudeStep: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Claude Code에 farero 등록").font(.title2.bold())
            Text("farero는 Claude Code의 훅으로 세션을 보고, MCP 서버로 플러그인 도구를 제공합니다. 등록하면 바뀌는 내용을 먼저 보여 주고, 적용하기 전에 기존 설정을 백업합니다.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            AgentCfgPanel(model: agentCfg)
        }
    }

    private var pluginStep: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("플러그인 연결 (선택)").font(.title2.bold())
            Text("GitHub, Railway, Resend, Gmail을 연결하면 그 서비스의 도구를 에이전트가 farero를 거쳐 씁니다. 지금 건너뛰고 나중에 메뉴의 ‘플러그인 연결…’에서 연결해도 됩니다.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            ForEach(model.state.plugins) { p in
                HStack {
                    Text(PolicyPresentation.pluginTitle(p.plugin))
                    Spacer()
                    Text(PluginPresentation.statusLabel(p.status)).foregroundStyle(.secondary)
                }
            }
            Button("플러그인 연결 창 열기") { openPlugins() }
        }
    }

    private var doneStep: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("준비가 끝났습니다").font(.title2.bold())
            Label("세션이 진행 중이면 노치 옆에 등대가 보입니다. 마우스를 올리거나 누르면 세션 목록이 펼쳐지고, 터미널 버튼으로 그 세션의 터미널로 갈 수 있습니다.",
                  systemImage: "rectangle.topthird.inset.filled")
            Label("승인이 필요한 도구 호출은 노치 아래 카드로 뜹니다. 입력값 전체와 승인을 묻는 이유를 확인하고 고르세요. 10분 안에 답하지 않으면 자동으로 거부됩니다.",
                  systemImage: "checkmark.shield")
            VStack(alignment: .leading, spacing: 6) {
                Label("카드가 떠 있는 동안에는 어느 앱에 있든 단축키로 답할 수 있습니다.", systemImage: "keyboard")
                Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 4) {
                    GridRow { Text("⌃⌥Y").monospaced(); Text("허용") }
                    GridRow { Text("⌃⌥S").monospaced(); Text("이번 세션 동안 허용 (카드가 허락할 때만)") }
                    GridRow { Text("⌃⌥N").monospaced(); Text("거부") }
                }
                .padding(.leading, 28)
            }
            Label("메뉴바의 등대 아이콘에서 설정, 플러그인 연결, 로그 검색을 열 수 있습니다.", systemImage: "menubar.rectangle")
        }
        .fixedSize(horizontal: false, vertical: true)
    }
}

/// Step 1: is farerod registered and running?
struct DaemonStep: View {
    let model: AppModel

    var body: some View {
        let registrar = model.registrar
        VStack(alignment: .leading, spacing: 12) {
            Text("farero 데몬").font(.title2.bold())
            Text("farerod는 로그인 항목으로 백그라운드에서 실행되며 세션 상태, 승인 대기열, 감사 로그를 맡습니다. 앱은 그 화면입니다.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            Form {
                LabeledContent("로그인 항목") { Text(model.registrationLabel) }
                LabeledContent("연결") {
                    switch model.state.connection {
                    case .connected: Text("연결됨").foregroundStyle(.green)
                    case .connecting: Text("연결 중…")
                    case .disconnected(let reason): Text("끊김: \(reason)").foregroundStyle(.red)
                    }
                }
                if model.state.isConnected {
                    LabeledContent("데몬 버전") { Text(model.state.daemonVersion) }
                }
            }
            .formStyle(.grouped)
            if registrar.status == .requiresApproval {
                Banner(text: "macOS가 farero 데몬의 실행 허락을 기다립니다. 시스템 설정 > 일반 > 로그인 항목에서 Farero를 켜세요. 켜면 곧바로 연결됩니다.",
                       style: .warning)
                Button("로그인 항목 설정 열기") { registrar.openLoginItemsSettings() }
            } else if case .failed(let message) = registrar.status {
                Banner(text: "데몬을 등록하지 못했습니다: \(message)", style: .error)
            } else if !model.state.isConnected {
                Banner(text: "데몬에 연결하는 중입니다. 계속 연결되지 않으면 앱을 다시 실행해 보세요.", style: .info)
            } else {
                Banner(text: "데몬이 실행 중입니다.", style: .success)
            }
            Button("다시 확인") {
                registrar.refresh()
                model.registrationLabel = registrar.status.label
            }
        }
    }
}
