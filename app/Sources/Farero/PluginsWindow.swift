import AppKit
import FareroCore
import SwiftUI

/// 플러그인 연결 (기능 명세서 F-07): one row per plugin with its status, the
/// sign-in prompt while connecting, and GitHub's read-only switch.
struct PluginsView: View {
    let model: AppModel
    @State private var gmailSheet = false

    var body: some View {
        let state = model.state
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text("플러그인을 연결하면 그 서비스의 도구가 farero 게이트웨이를 거쳐 에이전트에게 보입니다. 토큰은 Keychain에 저장되고 이 Mac 밖으로 나가지 않습니다.")
                    .font(.callout)
                    .foregroundStyle(.secondary)
                    .fixedSize(horizontal: false, vertical: true)
                if !state.isConnected {
                    Banner(text: "데몬에 연결되어 있지 않음", style: .warning)
                }
                ForEach(state.plugins) { p in
                    PluginRow(model: model, plugin: p, prompt: state.pluginPrompts[p.plugin],
                              activity: model.pluginActivity[p.plugin] ?? PluginActivity()) {
                        if p.plugin == "gmail" {
                            gmailSheet = true
                        } else {
                            model.connectPlugin(p.plugin)
                        }
                    }
                }
            }
            .padding(20)
        }
        .frame(minWidth: 560, minHeight: 480)
        .sheet(isPresented: $gmailSheet) {
            GmailSetupSheet(model: model, status: state.plugins.first { $0.plugin == "gmail" }?.status ?? "")
        }
    }
}

struct PluginRow: View {
    let model: AppModel
    let plugin: PluginState
    let prompt: PluginPrompt?
    let activity: PluginActivity
    let onConnect: () -> Void

    var body: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: 8) {
                HStack(alignment: .firstTextBaseline) {
                    Text(PolicyPresentation.pluginTitle(plugin.plugin)).font(.headline)
                    Badge(text: PluginPresentation.statusLabel(plugin.status), color: statusColor)
                    Spacer()
                    if activity.connecting { ProgressView().controlSize(.small) }
                    ForEach(PluginPresentation.actions(plugin.status), id: \.self) { action in
                        switch action {
                        case .connect:
                            Button("연결") { onConnect() }.disabled(activity.connecting || !model.state.isConnected)
                        case .reconnect:
                            Button("다시 연결") { onConnect() }.disabled(activity.connecting || !model.state.isConnected)
                        case .disconnect:
                            Button("연결 해제") { model.disconnectPlugin(plugin.plugin) }.disabled(activity.connecting)
                        }
                    }
                }
                if !plugin.accountLabel.isEmpty || plugin.connectedAt != nil {
                    HStack(spacing: 6) {
                        if !plugin.accountLabel.isEmpty {
                            Label(plugin.accountLabel, systemImage: "person.crop.circle")
                        }
                        if let at = plugin.connectedAt {
                            Text("· \(at.formatted(date: .abbreviated, time: .shortened)) 연결")
                        }
                    }
                    .font(.callout)
                    .foregroundStyle(.secondary)
                }
                if let scopes = PluginPresentation.scopes(plugin.plugin) {
                    Text("요청 권한: \(scopes)").font(.caption).foregroundStyle(.secondary)
                }
                if !plugin.error.isEmpty {
                    Text(plugin.error).font(.caption).foregroundStyle(.red).textSelection(.enabled)
                }
                if plugin.plugin == "github" {
                    Toggle(isOn: Binding(get: { plugin.options["read_only"] == "true" }, set: { on in
                        model.setPluginOption("github", key: "read_only", value: on ? "true" : "false")
                    })) {
                        VStack(alignment: .leading, spacing: 1) {
                            Text("읽기 전용")
                            Text("켜면 GitHub의 쓰기 도구를 에이전트에게 보이지 않습니다").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    .toggleStyle(.switch)
                }
                if let prompt {
                    PromptView(prompt: prompt)
                } else if activity.connecting {
                    Text("연결을 시작하는 중…").font(.callout).foregroundStyle(.secondary)
                }
                if let notice = activity.notice {
                    Text(notice)
                        .font(.callout)
                        .foregroundStyle(activity.noticeIsError ? Color.red : Color.green)
                        .textSelection(.enabled)
                }
            }
            .padding(6)
        }
    }

    private var statusColor: Color {
        switch plugin.status {
        case "connected": .green
        case "expired": .orange
        case "error": .red
        default: .gray
        }
    }
}

/// What the user must do to finish signing in (`plugin.prompt`).
struct PromptView: View {
    let prompt: PluginPrompt
    @State private var copied = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !prompt.userCode.isEmpty {
                Text("브라우저에서 열린 페이지에 이 코드를 입력하세요").font(.callout)
                HStack(spacing: 12) {
                    Text(prompt.userCode)
                        .font(.system(size: 30, weight: .bold, design: .monospaced))
                        .textSelection(.enabled)
                    Button(copied ? "복사함" : "복사") {
                        NSPasteboard.general.clearContents()
                        NSPasteboard.general.setString(prompt.userCode, forType: .string)
                        copied = true
                    }
                }
            } else {
                Text("브라우저에서 로그인을 마치세요").font(.callout)
            }
            HStack {
                Button("브라우저 다시 열기") {
                    if let url = URL(string: prompt.url) { NSWorkspace.shared.open(url) }
                }
                Text(prompt.url).font(.caption).foregroundStyle(.secondary).lineLimit(1).truncationMode(.middle)
            }
            if let expires = prompt.expiresAt {
                TimelineView(.periodic(from: .now, by: 1)) { ctx in
                    if let left = ApprovalPresentation.countdown(deadline: expires, now: ctx.date) {
                        Text("남은 시간 \(left)").font(.caption).foregroundStyle(.secondary).monospacedDigit()
                    }
                }
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 8).fill(Color.accentColor.opacity(0.08)))
    }
}

/// Gmail needs the user's own Google Cloud OAuth client (Q59).
struct GmailSetupSheet: View {
    let model: AppModel
    /// The plugin's current status; a previously connected Gmail can reuse
    /// the saved client.
    let status: String
    @Environment(\.dismiss) private var dismiss
    @State private var clientID = ""
    @State private var clientSecret = ""

    private struct Step: Identifiable {
        let id: Int
        let title: String
        let detail: String
        let link: (String, String)?
    }

    private var steps: [Step] {
        [
            Step(id: 1, title: "Google Cloud 프로젝트 만들기",
                 detail: "farero 전용 프로젝트를 하나 만듭니다.",
                 link: ("프로젝트 만들기", "https://console.cloud.google.com/projectcreate")),
            Step(id: 2, title: "Gmail API 사용 설정",
                 detail: "만든 프로젝트를 고른 뒤 ‘사용’을 누릅니다.",
                 link: ("Gmail API", "https://console.cloud.google.com/apis/library/gmail.googleapis.com")),
            Step(id: 3, title: "OAuth 동의 화면 구성",
                 detail: "사용자 유형은 ‘외부’로 합니다. 게시 상태는 ‘프로덕션(In production)’을 권장합니다(테스트 상태는 토큰이 7일 뒤 만료됩니다). 검증을 받지 않은 앱이므로 로그인할 때 ‘확인되지 않은 앱’ 경고 화면을 한 번 넘겨야 합니다.",
                 link: ("OAuth 동의 화면", "https://console.cloud.google.com/apis/credentials/consent")),
            Step(id: 4, title: "OAuth 클라이언트 ID 만들기",
                 detail: "‘사용자 인증 정보 만들기 → OAuth 클라이언트 ID’에서 애플리케이션 유형을 ‘데스크톱 앱’으로 고릅니다.",
                 link: ("사용자 인증 정보", "https://console.cloud.google.com/apis/credentials")),
            Step(id: 5, title: "클라이언트 ID와 보안 비밀 입력",
                 detail: "만든 클라이언트의 ID와 보안 비밀을 아래에 붙여 넣습니다. farerod가 Keychain에 저장합니다.",
                 link: nil),
        ]
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Gmail 연결 준비").font(.title2.bold())
            Text("Gmail은 각자 만든 Google Cloud OAuth 클라이언트로 연결합니다. 요청 권한은 gmail.readonly(읽기 전용)뿐입니다.")
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            ForEach(steps) { step in
                HStack(alignment: .top, spacing: 10) {
                    Text("\(step.id)")
                        .font(.headline)
                        .frame(width: 24, height: 24)
                        .background(Circle().fill(Color.accentColor.opacity(0.15)))
                    VStack(alignment: .leading, spacing: 3) {
                        Text(step.title).font(.headline)
                        Text(step.detail).font(.callout).foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                        if let (title, url) = step.link, let u = URL(string: url) {
                            Link(title, destination: u).font(.callout)
                        }
                    }
                }
            }
            Form {
                TextField("클라이언트 ID", text: $clientID, prompt: Text("1234567890-xxxx.apps.googleusercontent.com"))
                SecureField("클라이언트 보안 비밀", text: $clientSecret)
            }
            .formStyle(.grouped)
            HStack {
                if status == "expired" || status == "error" || status == "connected" {
                    Button("저장된 클라이언트로 다시 연결") {
                        model.connectPlugin("gmail")
                        dismiss()
                    }
                }
                Spacer()
                Button("취소") { dismiss() }.keyboardShortcut(.cancelAction)
                Button("연결") {
                    model.connectPlugin("gmail", params: [
                        "client_id": clientID.trimmingCharacters(in: .whitespacesAndNewlines),
                        "client_secret": clientSecret.trimmingCharacters(in: .whitespacesAndNewlines),
                    ])
                    dismiss()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(clientID.trimmingCharacters(in: .whitespaces).isEmpty
                          || clientSecret.trimmingCharacters(in: .whitespaces).isEmpty)
            }
        }
        .padding(20)
        .frame(width: 600)
    }
}
