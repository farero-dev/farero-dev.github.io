import Foundation

/// The log search form (기능 명세서 F-09), turned into a `log.query`.
public struct LogFilter: Sendable, Equatable {
    public enum Period: String, Sendable, CaseIterable, Identifiable {
        case all, today, week, month, custom
        public var id: String { rawValue }
        public var label: String {
            switch self {
            case .all: "전체 기간"
            case .today: "오늘"
            case .week: "최근 7일"
            case .month: "최근 30일"
            case .custom: "직접 지정"
            }
        }
    }

    public var query = ""
    public var sessionID = ""
    public var agent = ""
    public var plugin = ""
    /// "" (any), "plugin" or "agent".
    public var kind = ""
    public var tool = ""
    public var decision = ""
    public var period: Period = .all
    public var customFrom = Date()
    public var customTo = Date()

    public init() {}

    /// The request for one page of results. Custom ranges cover whole days.
    public func logQuery(now: Date, calendar: Calendar = .current, limit: Int, offset: Int = 0) -> LogQuery {
        var q = LogQuery(query: query.trimmingCharacters(in: .whitespacesAndNewlines), limit: limit)
        q.sessionID = sessionID
        q.agent = agent
        q.plugin = plugin
        q.kind = kind
        q.tool = tool.trimmingCharacters(in: .whitespaces)
        q.decision = decision
        q.offset = offset
        switch period {
        case .all:
            break
        case .today:
            q.from = calendar.startOfDay(for: now)
        case .week:
            q.from = now.addingTimeInterval(-7 * 86_400)
        case .month:
            q.from = now.addingTimeInterval(-30 * 86_400)
        case .custom:
            let lo = min(customFrom, customTo), hi = max(customFrom, customTo)
            q.from = calendar.startOfDay(for: lo)
            q.to = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: hi))
        }
        return q
    }

    /// How farerod will match the text (Q64): three or more characters use
    /// the trigram index, shorter ones (Korean words are often two
    /// syllables) a substring match.
    public static func searchModeLabel(_ query: String) -> String? {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        if q.isEmpty { return nil }
        return q.count >= 3 ? "전문 검색" : "부분 일치"
    }

    /// Decisions offered in the filter, in display order.
    public static let decisions = [
        CallDecision.autoAllowed, CallDecision.userAllowed, CallDecision.sessionAllowed,
        CallDecision.denied, CallDecision.autoDenied, CallDecision.timeout,
        CallDecision.blocked, CallDecision.passthrough, CallDecision.cancelled,
    ]
}

/// Text for one audit log row's detail.
public enum CallPresentation {
    /// "잘림: 20480 bytes 중 16384 bytes 저장" when the stored result is
    /// shorter than the original (Q46).
    public static func truncationNote(_ c: Call) -> String? {
        let stored = c.resultText.utf8.count
        guard c.resultBytes > stored else { return nil }
        return "잘림: \(c.resultBytes) bytes 중 \(stored) bytes 저장"
    }

    /// "github · issue_write", or the agent tool name.
    public static func toolTitle(_ c: Call) -> String {
        c.plugin.isEmpty ? c.tool : "\(c.plugin) · \(c.tool)"
    }

    /// Duration as "850ms" / "5.3초" / "2분 5초".
    public static func duration(_ ms: Int64) -> String {
        if ms < 1000 { return "\(ms)ms" }
        let s = Double(ms) / 1000
        if s < 60 { return String(format: "%.1f초", s) }
        let total = Int(s.rounded())
        return "\(total / 60)분 \(total % 60)초"
    }

    /// Reason codes ("policy,tainted", "allow_session") in Korean. In the
    /// log a reason explains any decision, so the labels are neutral
    /// ("분류표", not "승인이 필요한 도구": an auto-allowed call has it too).
    public static func reasonText(_ reason: String) -> String {
        guard !reason.isEmpty else { return "" }
        return reason.split(separator: ",").map { code -> String in
            switch code {
            case "policy": "분류표"
            case "tainted": "오염된 세션"
            case "unknown_session": "세션 불명"
            case "agent_request": "에이전트 권한 요청"
            case "allow_session": "이번 세션 동안 허용"
            case "app_not_running": "앱이 실행 중이 아님"
            case "unclassified": "분류표에 없는 도구"
            default: String(code)
            }
        }.joined(separator: ", ")
    }
}

/// The approval policy table (기능 명세서 6-5).
public enum PolicyPresentation {
    public static let pluginOrder = ["github", "railway", "resend", "gmail", "dev"]

    public struct Group: Sendable, Equatable, Identifiable {
        public var plugin: String
        public var tools: [PolicyTool]
        public var id: String { plugin }
    }

    /// Tools grouped by plugin in display order; unknown plugins follow,
    /// alphabetically.
    public static func grouped(_ tools: [PolicyTool]) -> [Group] {
        let byPlugin = Dictionary(grouping: tools, by: \.plugin)
        let extra = byPlugin.keys.filter { !pluginOrder.contains($0) }.sorted()
        return (pluginOrder + extra).compactMap { p in
            guard let list = byPlugin[p], !list.isEmpty else { return nil }
            return Group(plugin: p, tools: list.sorted { $0.tool < $1.tool })
        }
    }

    /// Levels the user may pick. A `no_session` tool cannot be auto-allowed
    /// (farerod refuses it too).
    public static func levelOptions(_ t: PolicyTool) -> [String] {
        t.noSession ? ["ask", "block"] : ["auto", "ask", "block"]
    }

    public static func levelLabel(_ level: String, noSession: Bool = false) -> String {
        switch level {
        case "auto": "자동 허용"
        case "ask": noSession ? "매번 승인" : "승인"
        case "block": "차단"
        default: level
        }
    }

    public static func pluginTitle(_ plugin: String) -> String {
        switch plugin {
        case "github": "GitHub"
        case "railway": "Railway"
        case "resend": "Resend"
        case "gmail": "Gmail"
        case "dev": "개발용 (dev)"
        default: plugin
        }
    }
}

/// Plugin connection text (기능 명세서 F-07).
public enum PluginPresentation {
    public enum Action: Sendable, Equatable { case connect, reconnect, disconnect }

    public static func statusLabel(_ status: String) -> String {
        switch status {
        case "connected": "연결됨"
        case "expired": "토큰 만료"
        case "error": "오류"
        case "disconnected", "": "연결 안 됨"
        default: status
        }
    }

    /// Buttons for a status.
    public static func actions(_ status: String) -> [Action] {
        switch status {
        case "connected": [.reconnect, .disconnect]
        case "expired", "error": [.reconnect, .disconnect]
        default: [.connect]
        }
    }

    /// The OAuth scopes each plugin asks for (Q59, Q60).
    public static func scopes(_ plugin: String) -> String? {
        switch plugin {
        case "github": "repo, read:org"
        case "railway": "openid, profile, email, offline_access, workspace:member"
        case "resend": "full_access (발송 전용 범위로는 보낸 메일을 읽을 수 없어 전체 권한이 필요함)"
        case "gmail": "gmail.readonly (읽기 전용)"
        default: nil
        }
    }

    /// How the plugin signs in. Railway refuses the device flow for
    /// dynamically registered clients, so only GitHub uses a device code.
    public static func usesDeviceCode(_ plugin: String) -> Bool {
        plugin == "github"
    }
}
