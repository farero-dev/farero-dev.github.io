import FareroCore
import SwiftUI

/// Claude Code registration (기능 명세서 F-06): status, the plan with a
/// before/after diff, apply and remove. The status lives in `AppState`, so
/// the menu and every panel show the same one, including farerod's push
/// after it fixed paths.
@MainActor
@Observable
final class AgentCfgModel {
    private let app: AppModel
    private(set) var plan: AgentCfgPlan?
    private(set) var busy = false
    private(set) var error: String?
    private(set) var result: String?
    var showingPlan = false
    var confirmingRemove = false

    init(app: AppModel) {
        self.app = app
    }

    var status: AgentCfgStatus? { app.state.agentCfg }

    /// The panel is on screen, showing farerod's notice if there is one.
    func markNoticeSeen() {
        app.dispatch(.agentCfgNoticeSeen)
    }

    func refresh() async {
        await run {
            let reply = try await self.app.client.request(MessageType.agentCfgStatus, AgentRef())
            self.app.receiveAgentCfgReply(reply)
        }
    }

    func preparePlan() async {
        await run {
            let reply = try await self.app.client.request(MessageType.agentCfgPlan, AgentRef())
            if case .agentCfgPlan(let p) = reply.payload {
                self.plan = p
                self.showingPlan = true
            }
        }
    }

    func apply() async {
        await run {
            // Running the claude CLI can take a while.
            let reply = try await self.app.client.request(MessageType.agentCfgApply, AgentRef(), timeout: .seconds(90))
            self.app.receiveAgentCfgReply(reply)
            if case .agentCfgStatus(let s) = reply.payload {
                self.result = s.backupPath.isEmpty
                    ? "등록했습니다. Claude Code를 다시 시작하면 적용됩니다."
                    : "등록했습니다. 이전 설정은 \(s.backupPath)에 백업했습니다. Claude Code를 다시 시작하면 적용됩니다."
            }
            self.showingPlan = false
        }
    }

    func remove() async {
        await run {
            let reply = try await self.app.client.request(MessageType.agentCfgRemove, AgentRef(), timeout: .seconds(90))
            self.app.receiveAgentCfgReply(reply)
            if case .agentCfgStatus(let s) = reply.payload {
                self.result = s.backupPath.isEmpty
                    ? "farero 설정을 제거했습니다."
                    : "farero 설정을 제거했습니다. 이전 설정은 \(s.backupPath)에 백업했습니다."
            }
        }
    }

    private func run(_ body: @escaping @MainActor () async throws -> Void) async {
        busy = true
        error = nil
        defer { busy = false }
        do {
            try await body()
        } catch {
            self.error = error.localizedDescription
        }
    }

    var anythingInstalled: Bool {
        guard let s = status else { return false }
        return s.hooksInstalled || s.allowInstalled || s.mcpInstalled
    }
}

/// The status rows and buttons, shared by 설정 and 설정 도우미.
struct AgentCfgPanel: View {
    @Bindable var model: AgentCfgModel

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let message = model.status?.message, !message.isEmpty {
                Banner(text: message, style: .info)
            }
            if let error = model.error {
                Banner(text: error, style: .error)
            }
            if let result = model.result {
                Banner(text: result, style: .success)
            }
            if let s = model.status {
                Form {
                    CheckRow(title: "Claude Code", ok: s.cliFound, okText: "찾음", missingText: "찾을 수 없음",
                             detail: s.cliFound ? s.cliPath : "Claude Code를 설치한 뒤 새로고침하세요")
                    LabeledContent("버전") {
                        VStack(alignment: .trailing, spacing: 2) {
                            Text(s.version.isEmpty ? "-" : s.version)
                                .foregroundStyle(s.versionOK ? Color.primary : Color.orange)
                            Text("최소 \(s.minVersion)").font(.caption).foregroundStyle(.secondary)
                        }
                    }
                    if s.cliFound && !s.versionOK {
                        Banner(text: "Claude Code \(s.version.isEmpty ? "버전을 알 수 없음" : s.version)은(는) farero가 지원하는 최소 버전 \(s.minVersion)보다 낮습니다. 터미널에서 `claude update`로 업데이트하세요.",
                               style: .warning)
                    }
                    CheckRow(title: "훅 (모든 이벤트)", ok: s.hooksInstalled)
                    CheckRow(title: "자동 허용 규칙 (mcp__farero__*)", ok: s.allowInstalled)
                    CheckRow(title: "MCP 서버 (farero)", ok: s.mcpInstalled)
                    if s.stalePath {
                        Banner(text: "설치된 항목이 다른 위치의 farero를 가리킵니다. 다시 등록하세요.", style: .warning)
                    }
                    LabeledContent("게이트웨이") { Text(s.gatewayURL.isEmpty ? "-" : s.gatewayURL).textSelection(.enabled) }
                    LabeledContent("설정 파일") { Text(s.settingsPath).textSelection(.enabled).font(.caption) }
                }
                .formStyle(.grouped)
            } else if !model.busy {
                Text("상태를 불러오지 못했습니다.").foregroundStyle(.secondary)
            }
            HStack {
                Button("새로고침") { Task { await model.refresh() } }
                Spacer()
                if model.busy { ProgressView().controlSize(.small) }
                if model.anythingInstalled {
                    Button("설정 제거…") { model.confirmingRemove = true }
                }
                Button(model.status?.isFullyInstalled == true ? "다시 등록…" : "등록…") {
                    Task { await model.preparePlan() }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(model.status?.cliFound != true || model.busy)
            }
        }
        .task {
            // Show what is known at once, then re-read it: Claude Code may
            // have been installed or updated since the last fetch.
            model.markNoticeSeen()
            await model.refresh()
            // Development aid: FARERO_DEBUG_AUTOPLAN=1 opens the plan sheet.
            if ProcessInfo.processInfo.environment["FARERO_DEBUG_AUTOPLAN"] == "1", model.plan == nil {
                await model.preparePlan()
            }
        }
        .sheet(isPresented: $model.showingPlan) {
            if let plan = model.plan {
                PlanSheet(model: model, plan: plan)
            }
        }
        .confirmationDialog("Claude Code에서 farero 설정을 제거할까요?", isPresented: $model.confirmingRemove) {
            Button("설정 제거", role: .destructive) { Task { await model.remove() } }
            Button("취소", role: .cancel) {}
        } message: {
            Text("farero가 넣은 훅, 자동 허용 규칙, MCP 서버만 지웁니다. 지우기 전에 settings.json을 백업합니다.")
        }
    }
}

/// What 등록 will change: each file as a diff, plus the CLI commands.
struct PlanSheet: View {
    let model: AgentCfgModel
    let plan: AgentCfgPlan
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("Claude Code 설정 변경 확인").font(.title2.bold())
            Text("아래 내용을 적용합니다. 적용하기 전에 기존 파일을 백업합니다.")
                .foregroundStyle(.secondary)
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    ForEach(Array(plan.changes.enumerated()), id: \.offset) { _, change in
                        FileChangeView(change: change)
                    }
                    if !plan.commands.isEmpty {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("적용 후 실행할 명령").font(.headline)
                            ForEach(Array(plan.commands.enumerated()), id: \.offset) { _, cmd in
                                Text(cmd)
                                    .font(.system(size: 11, design: .monospaced))
                                    .textSelection(.enabled)
                                    .padding(6)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .background(RoundedRectangle(cornerRadius: 6).fill(Color.secondary.opacity(0.1)))
                            }
                        }
                    }
                }
            }
            if let error = model.error {
                Banner(text: error, style: .error)
            }
            HStack {
                Spacer()
                if model.busy { ProgressView().controlSize(.small) }
                Button("취소") { dismiss() }
                    .keyboardShortcut(.cancelAction)
                Button("적용") { Task { await model.apply() } }
                    .keyboardShortcut(.defaultAction)
                    .disabled(model.busy)
            }
        }
        .padding(20)
        .frame(minWidth: 680, idealWidth: 760, minHeight: 520, idealHeight: 640)
    }
}

/// One file's change as a coloured unified diff.
struct FileChangeView: View {
    let change: FileChange

    var body: some View {
        let rows = LineDiff.rows(before: change.before, after: change.after)
        let foreign = HookScan.foreignPermissionHooks(settingsJSON: change.before)
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Image(systemName: "doc.text")
                Text(change.path).font(.headline).textSelection(.enabled)
                if change.before.isEmpty { Badge(text: "새 파일", color: .green) }
            }
            if !foreign.isEmpty {
                VStack(alignment: .leading, spacing: 4) {
                    Banner(text: HookScan.warning, style: .warning)
                    ForEach(foreign, id: \.self) { cmd in
                        Text("• \(cmd)").font(.system(size: 11, design: .monospaced)).textSelection(.enabled)
                    }
                }
            }
            if rows.isEmpty {
                Text("바뀌는 내용 없음").foregroundStyle(.secondary)
            } else {
                VStack(alignment: .leading, spacing: 0) {
                    ForEach(rows) { row in DiffRowView(row: row) }
                }
                .padding(.vertical, 4)
                .background(RoundedRectangle(cornerRadius: 6).fill(Color(nsColor: .textBackgroundColor)))
                .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.secondary.opacity(0.25)))
                .textSelection(.enabled)
            }
        }
    }
}

struct DiffRowView: View {
    let row: LineDiff.Row

    var body: some View {
        HStack(spacing: 0) {
            Text(row.oldLine.map(String.init) ?? "")
                .frame(width: 36, alignment: .trailing)
                .foregroundStyle(.tertiary)
            Text(row.newLine.map(String.init) ?? "")
                .frame(width: 36, alignment: .trailing)
                .foregroundStyle(.tertiary)
            Text(marker)
                .frame(width: 18)
                .foregroundStyle(color)
            Text(row.text.isEmpty ? " " : row.text)
                .foregroundStyle(row.kind == .hunkHeader ? Color.secondary : Color.primary)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .font(.system(size: 11, design: .monospaced))
        .padding(.vertical, 1)
        .background(background)
    }

    private var marker: String {
        switch row.kind {
        case .hunkHeader, .context: ""
        case .removed: "-"
        case .added: "+"
        }
    }

    private var color: Color {
        switch row.kind {
        case .removed: .red
        case .added: .green
        default: .secondary
        }
    }

    private var background: Color {
        switch row.kind {
        case .removed: Color.red.opacity(0.12)
        case .added: Color.green.opacity(0.12)
        case .hunkHeader: Color.blue.opacity(0.07)
        case .context: .clear
        }
    }
}
