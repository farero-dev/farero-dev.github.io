import FareroCore
import SwiftUI

/// 로그 검색 (기능 명세서 F-09, M7): filters, sessions, results and the
/// selected call's full detail.
@MainActor
@Observable
final class LogSearchModel {
    private let app: AppModel
    var filter = LogFilter()
    private(set) var calls: [Call] = []
    private(set) var loading = false
    private(set) var message: String?
    private(set) var canLoadMore = false
    var selection: Call.ID?

    static let pageSize = 200

    init(app: AppModel) {
        self.app = app
    }

    var selectedCall: Call? { calls.first { $0.id == selection } }

    func search() async {
        await load(offset: 0)
    }

    func loadMore() async {
        await load(offset: calls.count)
    }

    private func load(offset: Int) async {
        loading = true
        defer { loading = false }
        do {
            let q = filter.logQuery(now: .now, limit: Self.pageSize, offset: offset)
            let reply = try await app.client.request(MessageType.logQuery, q)
            guard case .logResult(let rows) = reply.payload else { return }
            calls = offset == 0 ? rows : calls + rows
            canLoadMore = rows.count == Self.pageSize
            if offset == 0, !calls.contains(where: { $0.id == selection }) { selection = calls.first?.id }
            message = calls.isEmpty ? "결과 없음" : "\(calls.count)건\(canLoadMore ? " 이상" : "")"
        } catch {
            message = error.localizedDescription
        }
    }

    func sessionName(_ id: String) -> String {
        guard !id.isEmpty else { return "세션 불명" }
        if let s = app.state.sessions[id] { return SessionDisplay.name(s, now: .now) }
        return id
    }
}

struct LogSearchView: View {
    let model: AppModel
    @State private var search: LogSearchModel
    @State private var deleting: Session?
    @State private var deleteError: String?

    init(model: AppModel) {
        self.model = model
        _search = State(initialValue: LogSearchModel(app: model))
    }

    var body: some View {
        HSplitView {
            sessionList
                .frame(minWidth: 190, idealWidth: 220, maxWidth: 320)
            VStack(spacing: 0) {
                filterBar
                Divider()
                VSplitView {
                    resultTable.frame(minHeight: 160)
                    detail.frame(minHeight: 200)
                }
            }
            .frame(minWidth: 560)
        }
        .frame(minWidth: 820, minHeight: 560)
        .task { await search.search() }
        .onChange(of: search.filter) { old, new in
            // Text fields search on Return; pickers and dates right away.
            var a = old, b = new
            a.query = ""; b.query = ""; a.tool = ""; b.tool = ""
            if a != b { Task { await search.search() } }
        }
        .alert("세션과 그 로그를 삭제합니다", isPresented: Binding(get: { deleting != nil }, set: { if !$0 { deleting = nil } }),
               presenting: deleting) { s in
            Button("삭제", role: .destructive) {
                Task {
                    deleteError = await model.deleteSession(s.id)
                    if deleteError == nil {
                        if search.filter.sessionID == s.id { search.filter.sessionID = "" }
                        await search.search()
                    }
                }
            }
            Button("취소", role: .cancel) {}
        } message: { s in
            Text("\(SessionDisplay.name(s, now: .now))의 훅 기록과 도구 호출 로그가 모두 지워집니다. 되돌릴 수 없습니다.")
        }
    }

    // MARK: Sessions

    private var sessions: [Session] {
        model.state.sessions.values.sorted { ($0.startedAt ?? .distantPast) > ($1.startedAt ?? .distantPast) }
    }

    private var sessionList: some View {
        VStack(alignment: .leading, spacing: 0) {
            List(selection: Binding(get: { search.filter.sessionID }, set: { search.filter.sessionID = $0 ?? "" })) {
                Text("모든 세션").tag("")
                Section("세션") {
                    ForEach(sessions) { s in
                        VStack(alignment: .leading, spacing: 2) {
                            Text(SessionDisplay.name(s, now: .now)).lineLimit(1)
                            HStack(spacing: 4) {
                                Text(s.status.label)
                                if s.tainted { Text("· 오염됨").foregroundStyle(.orange) }
                            }
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        }
                        .tag(s.id)
                        .contextMenu {
                            Button("세션 삭제…", role: .destructive) { deleting = s }
                        }
                    }
                }
            }
            if let deleteError {
                Text(deleteError).font(.caption).foregroundStyle(.red).padding(6)
            }
            HStack {
                Button {
                    deleting = model.state.sessions[search.filter.sessionID]
                } label: {
                    Label("세션 삭제…", systemImage: "trash")
                }
                .disabled(search.filter.sessionID.isEmpty)
                Spacer()
            }
            .padding(8)
        }
    }

    // MARK: Filters

    private var filterBar: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                TextField("도구 이름, 입력값, 결과에서 검색", text: $search.filter.query)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit { Task { await search.search() } }
                if let mode = LogFilter.searchModeLabel(search.filter.query) {
                    Badge(text: mode, color: .blue)
                        .help("세 글자 이상은 전문 검색, 두 글자 이하는 부분 일치로 찾습니다")
                }
                Button("검색") { Task { await search.search() } }
                    .keyboardShortcut(.defaultAction)
            }
            HStack(spacing: 10) {
                Picker("에이전트", selection: $search.filter.agent) {
                    Text("전체").tag("")
                    Text("Claude Code").tag("claude")
                    Text("Codex").tag("codex")
                }
                .frame(width: 170)
                Picker("출처", selection: sourceBinding) {
                    Text("전체").tag("")
                    Text("에이전트 도구").tag("agent:")
                    ForEach(PolicyPresentation.pluginOrder, id: \.self) { p in
                        Text(PolicyPresentation.pluginTitle(p)).tag("plugin:" + p)
                    }
                }
                .frame(width: 180)
                TextField("도구", text: $search.filter.tool)
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 120)
                    .onSubmit { Task { await search.search() } }
                Picker("판단", selection: $search.filter.decision) {
                    Text("전체").tag("")
                    ForEach(LogFilter.decisions, id: \.self) { d in
                        Text(CallDecision.label(d)).tag(d)
                    }
                }
                .frame(width: 150)
            }
            HStack(spacing: 10) {
                Picker("기간", selection: $search.filter.period) {
                    ForEach(LogFilter.Period.allCases) { p in Text(p.label).tag(p) }
                }
                .frame(width: 170)
                if search.filter.period == .custom {
                    DatePicker("시작", selection: $search.filter.customFrom, displayedComponents: .date)
                        .frame(width: 170)
                    DatePicker("끝", selection: $search.filter.customTo, displayedComponents: .date)
                        .frame(width: 160)
                }
                Spacer()
                if search.loading { ProgressView().controlSize(.small) }
                if let message = search.message {
                    Text(message).font(.caption).foregroundStyle(.secondary)
                }
            }
        }
        .padding(10)
    }

    /// One picker for "agent tools" (kind) and a plugin.
    private var sourceBinding: Binding<String> {
        Binding(get: {
            if search.filter.kind == "agent" { return "agent:" }
            if !search.filter.plugin.isEmpty { return "plugin:" + search.filter.plugin }
            return ""
        }, set: { v in
            if v == "agent:" {
                search.filter.kind = "agent"
                search.filter.plugin = ""
            } else if v.hasPrefix("plugin:") {
                search.filter.kind = "plugin"
                search.filter.plugin = String(v.dropFirst("plugin:".count))
            } else {
                search.filter.kind = ""
                search.filter.plugin = ""
            }
        })
    }

    // MARK: Results

    private var resultTable: some View {
        VStack(spacing: 0) {
            Table(search.calls, selection: $search.selection) {
                TableColumn("시각") { c in
                    Text(c.ts.map { $0.formatted(.dateTime.month(.twoDigits).day(.twoDigits).hour().minute().second()) } ?? "")
                        .monospacedDigit()
                }
                .width(min: 110, ideal: 140)
                TableColumn("세션") { c in Text(search.sessionName(c.sessionID)).lineLimit(1) }
                    .width(min: 80, ideal: 150)
                TableColumn("도구") { c in Text(CallPresentation.toolTitle(c)).lineLimit(1) }
                    .width(min: 80, ideal: 150)
                TableColumn("판단") { c in
                    Text(CallDecision.label(c.decision))
                        .foregroundStyle(CallDecision.showsDenied.contains(c.decision) || c.decision == CallDecision.blocked
                                         ? Color.red : Color.primary)
                }
                .width(min: 60, ideal: 80)
                TableColumn("오류") { c in Text(c.error).foregroundStyle(.red).lineLimit(1) }
                    .width(min: 40, ideal: 90)
            }
            if search.canLoadMore {
                Button("더 보기") { Task { await search.loadMore() } }
                    .padding(6)
            }
        }
    }

    @ViewBuilder
    private var detail: some View {
        if let c = search.selectedCall {
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    Grid(alignment: .leadingFirstTextBaseline, horizontalSpacing: 12, verticalSpacing: 4) {
                        detailRow("시각", c.ts.map { $0.formatted(date: .complete, time: .standard) } ?? "-")
                        detailRow("세션", search.sessionName(c.sessionID))
                        detailRow("에이전트", SessionDisplay.agentName(c.agent))
                        detailRow("종류", c.kind == "agent" ? "에이전트 도구" : "플러그인 \(c.plugin)")
                        detailRow("도구", CallPresentation.toolTitle(c))
                        detailRow("판단", CallDecision.label(c.decision))
                        if !c.reason.isEmpty { detailRow("이유", CallPresentation.reasonText(c.reason)) }
                        detailRow("걸린 시간", CallPresentation.duration(c.durationMS))
                        if !c.error.isEmpty { detailRow("오류", c.error, color: .red) }
                    }
                    Text("입력값").font(.headline)
                    ReadOnlyTextView(text: c.input.prettyPrinted())
                        .frame(height: 150)
                    HStack {
                        Text("결과").font(.headline)
                        if let note = CallPresentation.truncationNote(c) {
                            Text(note).font(.caption).foregroundStyle(.orange)
                        }
                    }
                    if c.resultText.isEmpty {
                        Text("없음").foregroundStyle(.secondary)
                    } else {
                        ReadOnlyTextView(text: c.resultText)
                            .frame(height: 150)
                    }
                }
                .padding(12)
            }
        } else {
            Text("호출을 선택하세요")
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
        }
    }

    private func detailRow(_ title: String, _ value: String, color: Color = .primary) -> some View {
        GridRow {
            Text(title).foregroundStyle(.secondary)
            Text(value).foregroundStyle(color).textSelection(.enabled)
        }
    }
}
