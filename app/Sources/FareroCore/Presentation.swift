import Foundation

/// Text the UI shows for sessions (기능 명세서 F-01, 5-1).
public enum SessionDisplay {
    /// Agent kind for display.
    public static func agentName(_ agent: String) -> String {
        switch agent {
        case "claude": "Claude Code"
        case "codex": "Codex"
        case "": "에이전트"
        default: agent
        }
    }

    /// The folder name of `cwd` (or a short session id when unknown).
    public static func folderName(_ s: Session) -> String {
        let trimmed = s.cwd.hasSuffix("/") && s.cwd.count > 1 ? String(s.cwd.dropLast()) : s.cwd
        if !trimmed.isEmpty {
            let name = (trimmed as NSString).lastPathComponent
            if !name.isEmpty { return name }
        }
        let sid = s.agentSessionID.isEmpty ? s.id : s.agentSessionID
        return String(sid.prefix(8))
    }

    /// Start time: "09:41" today, "10/1 09:41" on another day.
    public static func startTime(_ date: Date?, now: Date, calendar: Calendar = .current) -> String {
        guard let date else { return "" }
        let hm = calendar.dateComponents([.month, .day, .hour, .minute], from: date)
        let time = String(format: "%02d:%02d", hm.hour ?? 0, hm.minute ?? 0)
        if calendar.isDate(date, inSameDayAs: now) { return time }
        return "\(hm.month ?? 0)/\(hm.day ?? 0) \(time)"
    }

    /// "proj · Claude Code · 09:41"
    public static func name(_ s: Session, now: Date, calendar: Calendar = .current) -> String {
        var parts = [folderName(s), agentName(s.agent)]
        let t = startTime(s.startedAt, now: now, calendar: calendar)
        if !t.isEmpty { parts.append(t) }
        return parts.joined(separator: " · ")
    }
}

/// Text the approval card shows (기능 명세서 5-3).
public enum ApprovalPresentation {
    /// Why farero is asking, for each reason code.
    public static func reasonText(_ reason: String) -> String {
        switch reason {
        case "policy": "분류표에서 승인이 필요한 도구"
        case "tainted": "이 세션이 신뢰할 수 없는 콘텐츠를 읽음"
        case "unknown_session": "어느 세션의 호출인지 알 수 없음"
        case "agent_request": "에이전트가 권한을 요청함"
        default: reason
        }
    }

    /// "github · issue_write" for plugin calls, "Bash" for agent tools.
    public static func toolTitle(_ a: Approval) -> String {
        a.plugin.isEmpty ? a.tool : "\(a.plugin) · \(a.tool)"
    }

    /// The input field that says the most, shown above the full JSON: the
    /// command for Bash, the path for file tools, the URL for fetches.
    public static func headline(_ a: Approval) -> String? {
        guard a.plugin.isEmpty else { return nil }
        let key: String?
        switch a.tool {
        case "Bash": key = "command"
        case "Edit", "MultiEdit", "Write", "Read": key = "file_path"
        case "NotebookEdit": key = "notebook_path"
        case "WebFetch": key = "url"
        case "WebSearch": key = "query"
        default: key = nil
        }
        guard let key, let v = a.input[key]?.stringValue, !v.isEmpty else { return nil }
        return truncated(v, maxBytes: 1000).text
    }

    /// Bytes of input text shown on the card. Larger inputs (a Write of a
    /// whole file) show their beginning; the full text can be copied.
    public static let displayLimitBytes = 256 << 10

    /// The input text for the card: the pretty-printed JSON, cut at
    /// `limit` bytes on a character boundary.
    public struct InputDisplay: Sendable, Equatable {
        public var text: String
        public var full: String
        public var isTruncated: Bool
        public var totalBytes: Int
    }

    public static func inputDisplay(_ a: Approval, limit: Int = displayLimitBytes) -> InputDisplay {
        let full = a.input.prettyPrinted()
        let cut = truncated(full, maxBytes: limit)
        return InputDisplay(text: cut.text, full: full, isTruncated: cut.isTruncated, totalBytes: full.utf8.count)
    }

    /// `s` cut to at most `maxBytes` UTF-8 bytes without splitting a character.
    public static func truncated(_ s: String, maxBytes: Int) -> (text: String, isTruncated: Bool) {
        guard s.utf8.count > maxBytes else { return (s, false) }
        var i = s.utf8.index(s.utf8.startIndex, offsetBy: max(0, maxBytes))
        while i > s.utf8.startIndex, i.samePosition(in: s) == nil {
            i = s.utf8.index(before: i)
        }
        return (String(s[..<i]), true)
    }

    /// Remaining time as "m:ss" (never negative).
    public static func countdown(deadline: Date?, now: Date) -> String? {
        guard let deadline else { return nil }
        let left = max(0, Int(deadline.timeIntervalSince(now).rounded(.up)))
        return String(format: "%d:%02d", left / 60, left % 60)
    }

}

/// The global shortcuts for the front approval card (기능 명세서 5-3): ⌃⌥Y
/// allow, ⌃⌥S allow for the session (only when offered), ⌃⌥N deny.
public enum ApprovalShortcut: String, Sendable, CaseIterable {
    case allow
    case allowSession
    case deny

    public var answer: ApprovalAnswer {
        switch self {
        case .allow: .allow
        case .allowSession: .allowSession
        case .deny: .deny
        }
    }

    /// The letter pressed with ⌃⌥.
    public var key: Character {
        switch self {
        case .allow: "Y"
        case .allowSession: "S"
        case .deny: "N"
        }
    }

    /// Hint shown on the button.
    public var hint: String { "⌃⌥\(key)" }

    /// The shortcuts to register for the front card: none without a card, and
    /// ⌃⌥S only when the card offers "이번 세션 동안 허용".
    public static func available(front: Approval?) -> [ApprovalShortcut] {
        guard let front else { return [] }
        return front.allowSession ? [.allow, .allowSession, .deny] : [.allow, .deny]
    }
}

/// What the menu bar says about the Claude Code registration, so the user
/// learns it without opening 설정 (기능 명세서 F-06): an installed Claude
/// Code older than farero supports ("v2.1.203 미만이면 업데이트를 안내한다"),
/// and farerod's notice after it fixed farero's paths because the app moved
/// (Q63).
public struct AgentCfgAlert: Sendable, Equatable {
    public enum Kind: Sendable, Equatable {
        /// Claude Code is older than `min_version`.
        case outdatedCLI
        /// farerod's `message`, such as the automatic path fix.
        case notice
    }

    public var kind: Kind
    /// One line for the menu.
    public var title: String
    /// The whole text, for a tooltip.
    public var detail: String

    public init(kind: Kind, title: String, detail: String) {
        self.kind = kind
        self.title = title
        self.detail = detail
    }

    /// The alerts for `status`, the outdated Claude Code first: it keeps
    /// mattering until the user updates, while a notice is a one-off. A
    /// notice equal to `seenMessage` (already seen in 설정 > Claude Code) is
    /// left out. A missing Claude Code is not an alert here; the onboarding
    /// covers installing it.
    public static func alerts(for status: AgentCfgStatus?, seenMessage: String = "") -> [AgentCfgAlert] {
        guard let s = status else { return [] }
        var out: [AgentCfgAlert] = []
        if s.cliFound && !s.versionOK {
            out.append(outdated(version: s.version, minVersion: s.minVersion))
        }
        let message = s.message.trimmingCharacters(in: .whitespacesAndNewlines)
        if !message.isEmpty && s.message != seenMessage {
            out.append(AgentCfgAlert(kind: .notice, title: summary(message), detail: message))
        }
        return out
    }

    static func outdated(version: String, minVersion: String) -> AgentCfgAlert {
        let min = minVersion.isEmpty ? "지원 버전" : minVersion
        let fix = "터미널에서 claude update로 업데이트하세요."
        if version.isEmpty {
            return AgentCfgAlert(kind: .outdatedCLI,
                                 title: "Claude Code 버전을 확인하지 못함 (\(min) 이상 필요)",
                                 detail: "Claude Code 버전을 확인하지 못했습니다. farero는 \(min) 이상에서 동작합니다. \(fix)")
        }
        return AgentCfgAlert(kind: .outdatedCLI,
                             title: "Claude Code \(version) → \(min) 이상으로 업데이트 필요 (claude update)",
                             detail: "설치된 Claude Code \(version)은(는) farero가 지원하는 최소 버전 \(min)보다 낮습니다. \(fix)")
    }

    /// The menu line for a notice: the text before the first ": " (farerod
    /// puts paths after it), at most `maxLength` characters.
    static func summary(_ message: String, maxLength: Int = 60) -> String {
        var head = message
        if let r = message.range(of: ": ") { head = String(message[..<r.lowerBound]) }
        head = head.trimmingCharacters(in: .whitespacesAndNewlines)
        return head.count > maxLength ? String(head.prefix(maxLength - 1)) + "…" : head
    }
}
