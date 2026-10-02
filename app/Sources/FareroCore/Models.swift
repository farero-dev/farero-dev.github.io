import Foundation

// Swift mirrors of daemon/internal/model and daemon/internal/ipc (docs/ipc.md).
// Decoding is tolerant: unknown fields are ignored, missing fields and Go's
// `null` slices fall back to empty values, and a field with an unexpected type
// does not fail the whole message.

/// Session status (기능 명세서 5-1). Unrecognized values decode as `.unknown`.
public enum SessionStatus: String, Sendable, Codable, CaseIterable {
    case running
    case waitingInput = "waiting_input"
    case waitingApproval = "waiting_approval"
    case ended
    case unknown

    public init(from decoder: Decoder) throws {
        let raw = try decoder.singleValueContainer().decode(String.self)
        self = SessionStatus(rawValue: raw) ?? .unknown
    }

    /// The session is live: not ended and not marked unknown by the sweeper.
    public var isActive: Bool {
        switch self {
        case .running, .waitingInput, .waitingApproval: true
        case .ended, .unknown: false
        }
    }

    /// Korean label for the session list.
    public var label: String {
        switch self {
        case .running: "실행 중"
        case .waitingInput: "입력 대기"
        case .waitingApproval: "승인 대기"
        case .ended: "종료됨"
        case .unknown: "상태 불명"
        }
    }
}

/// One agent session.
public struct Session: Sendable, Equatable, Identifiable, Decodable {
    public var id: String
    public var agent: String
    public var agentSessionID: String
    public var cwd: String
    public var tty: String
    public var pid: Int
    public var status: SessionStatus
    public var currentTool: String
    public var tainted: Bool
    public var startedAt: Date?
    public var lastEventAt: Date?

    public init(id: String, agent: String = "claude", agentSessionID: String = "", cwd: String = "",
                tty: String = "", pid: Int = 0, status: SessionStatus = .running, currentTool: String = "",
                tainted: Bool = false, startedAt: Date? = nil, lastEventAt: Date? = nil) {
        self.id = id
        self.agent = agent
        self.agentSessionID = agentSessionID
        self.cwd = cwd
        self.tty = tty
        self.pid = pid
        self.status = status
        self.currentTool = currentTool
        self.tainted = tainted
        self.startedAt = startedAt
        self.lastEventAt = lastEventAt
    }

    enum CodingKeys: String, CodingKey {
        case id, agent, cwd, tty, pid, status, tainted
        case agentSessionID = "agent_session_id"
        case currentTool = "current_tool"
        case startedAt = "started_at"
        case lastEventAt = "last_event_at"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = c.value(.id, "")
        agent = c.value(.agent, "")
        agentSessionID = c.value(.agentSessionID, "")
        cwd = c.value(.cwd, "")
        tty = c.value(.tty, "")
        pid = c.value(.pid, 0)
        status = c.value(.status, .unknown)
        currentTool = c.value(.currentTool, "")
        tainted = c.value(.tainted, false)
        startedAt = c.time(.startedAt)
        lastEventAt = c.time(.lastEventAt)
    }
}

/// The user's answer to an approval card.
public enum ApprovalAnswer: String, Sendable, Codable {
    case allow
    case allowSession = "allow_session"
    case deny
}

/// A pending approval card (기능 명세서 5-3).
public struct Approval: Sendable, Equatable, Identifiable, Decodable {
    public var id: String
    /// "plugin" (gateway call) or "agent" (the agent's own tool).
    public var kind: String
    public var sessionID: String
    public var sessionLabel: String
    public var agent: String
    public var plugin: String
    public var tool: String
    public var input: JSONValue
    public var reasons: [String]
    public var allowSession: Bool
    public var createdAt: Date?
    public var deadline: Date?

    public init(id: String, kind: String = "agent", sessionID: String = "", sessionLabel: String = "",
                agent: String = "claude", plugin: String = "", tool: String = "", input: JSONValue = .object([:]),
                reasons: [String] = [], allowSession: Bool = true, createdAt: Date? = nil, deadline: Date? = nil) {
        self.id = id
        self.kind = kind
        self.sessionID = sessionID
        self.sessionLabel = sessionLabel
        self.agent = agent
        self.plugin = plugin
        self.tool = tool
        self.input = input
        self.reasons = reasons
        self.allowSession = allowSession
        self.createdAt = createdAt
        self.deadline = deadline
    }

    enum CodingKeys: String, CodingKey {
        case id, kind, agent, plugin, tool, input, reasons, deadline
        case sessionID = "session_id"
        case sessionLabel = "session_label"
        case allowSession = "allow_session"
        case createdAt = "created_at"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = c.value(.id, "")
        kind = c.value(.kind, "")
        sessionID = c.value(.sessionID, "")
        sessionLabel = c.value(.sessionLabel, "")
        agent = c.value(.agent, "")
        plugin = c.value(.plugin, "")
        tool = c.value(.tool, "")
        input = c.value(.input, .null)
        reasons = c.value(.reasons, [])
        allowSession = c.value(.allowSession, false)
        createdAt = c.time(.createdAt)
        deadline = c.time(.deadline)
    }

    public var isPlugin: Bool { kind == "plugin" }
}

/// Audit log decisions (기능 명세서 F-09).
public enum CallDecision {
    public static let autoAllowed = "auto_allowed"
    public static let userAllowed = "user_allowed"
    public static let sessionAllowed = "session_allowed"
    public static let denied = "denied"
    public static let autoDenied = "auto_denied"
    public static let timeout = "timeout"
    public static let blocked = "blocked"
    public static let passthrough = "passthrough"
    public static let cancelled = "cancelled"

    /// Decisions that show the "denied" character (F-12).
    public static let showsDenied: Set<String> = [denied, autoDenied, timeout]

    /// Korean label for the log.
    public static func label(_ decision: String) -> String {
        switch decision {
        case autoAllowed: "자동 허용"
        case userAllowed: "사용자 허용"
        case sessionAllowed: "세션 허용"
        case denied: "거부"
        case autoDenied: "자동 거부"
        case timeout: "시간 초과"
        case blocked: "차단"
        case passthrough: "터미널로 넘김"
        case cancelled: "취소됨"
        default: decision
        }
    }
}

/// One audit log row.
public struct Call: Sendable, Equatable, Identifiable, Decodable {
    public var id: Int64
    public var sessionID: String
    public var connID: String
    public var ts: Date?
    public var kind: String
    public var agent: String
    public var plugin: String
    public var tool: String
    public var input: JSONValue
    public var resultText: String
    public var resultBytes: Int
    public var decision: String
    public var reason: String
    public var durationMS: Int64
    public var error: String

    public init(id: Int64 = 0, sessionID: String = "", connID: String = "", ts: Date? = nil, kind: String = "plugin",
                agent: String = "claude", plugin: String = "", tool: String = "", input: JSONValue = .object([:]),
                resultText: String = "", resultBytes: Int = 0, decision: String = "", reason: String = "",
                durationMS: Int64 = 0, error: String = "") {
        self.id = id
        self.sessionID = sessionID
        self.connID = connID
        self.ts = ts
        self.kind = kind
        self.agent = agent
        self.plugin = plugin
        self.tool = tool
        self.input = input
        self.resultText = resultText
        self.resultBytes = resultBytes
        self.decision = decision
        self.reason = reason
        self.durationMS = durationMS
        self.error = error
    }

    enum CodingKeys: String, CodingKey {
        case id, ts, kind, agent, plugin, tool, input, decision, reason, error
        case sessionID = "session_id"
        case connID = "conn_id"
        case resultText = "result_text"
        case resultBytes = "result_bytes"
        case durationMS = "duration_ms"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = c.value(.id, 0)
        sessionID = c.value(.sessionID, "")
        connID = c.value(.connID, "")
        ts = c.time(.ts)
        kind = c.value(.kind, "")
        agent = c.value(.agent, "")
        plugin = c.value(.plugin, "")
        tool = c.value(.tool, "")
        input = c.value(.input, .null)
        resultText = c.value(.resultText, "")
        resultBytes = c.value(.resultBytes, 0)
        decision = c.value(.decision, "")
        reason = c.value(.reason, "")
        durationMS = c.value(.durationMS, 0)
        error = c.value(.error, "")
    }
}

/// A plugin's connection state.
public struct PluginState: Sendable, Equatable, Identifiable, Decodable {
    public var plugin: String
    /// disconnected | connected | expired | error
    public var status: String
    public var accountLabel: String
    public var connectedAt: Date?
    public var error: String
    public var options: [String: String]

    public var id: String { plugin }

    public init(plugin: String, status: String = "disconnected", accountLabel: String = "",
                connectedAt: Date? = nil, error: String = "", options: [String: String] = [:]) {
        self.plugin = plugin
        self.status = status
        self.accountLabel = accountLabel
        self.connectedAt = connectedAt
        self.error = error
        self.options = options
    }

    enum CodingKeys: String, CodingKey {
        case plugin, status, error, options
        case accountLabel = "account_label"
        case connectedAt = "connected_at"
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        plugin = c.value(.plugin, "")
        status = c.value(.status, "")
        accountLabel = c.value(.accountLabel, "")
        connectedAt = c.time(.connectedAt)
        error = c.value(.error, "")
        options = c.value(.options, [:])
    }
}

/// The MCP gateway listener.
public struct GatewayInfo: Sendable, Equatable, Decodable {
    public var running: Bool
    public var port: Int
    public var url: String
    public var error: String

    public init(running: Bool = false, port: Int = 0, url: String = "", error: String = "") {
        self.running = running
        self.port = port
        self.url = url
        self.error = error
    }

    enum CodingKeys: String, CodingKey { case running, port, url, error }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        running = c.value(.running, false)
        port = c.value(.port, 0)
        url = c.value(.url, "")
        error = c.value(.error, "")
    }
}

/// The reply to `ui.hello`: the whole state to rebuild the UI from.
public struct StateSnapshot: Sendable, Equatable, Decodable {
    public var version: String
    public var sessions: [Session]
    public var approvals: [Approval]
    public var plugins: [PluginState]
    public var gateway: GatewayInfo

    public init(version: String = "", sessions: [Session] = [], approvals: [Approval] = [],
                plugins: [PluginState] = [], gateway: GatewayInfo = GatewayInfo()) {
        self.version = version
        self.sessions = sessions
        self.approvals = approvals
        self.plugins = plugins
        self.gateway = gateway
    }

    enum CodingKeys: String, CodingKey { case version, sessions, approvals, plugins, gateway }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = c.value(.version, "")
        sessions = c.value(.sessions, [])
        approvals = c.value(.approvals, [])
        plugins = c.value(.plugins, [])
        gateway = c.value(.gateway, GatewayInfo())
    }
}

/// `session.removed`, and the `session.delete` request.
public struct SessionRef: Sendable, Equatable, Codable {
    public var sessionID: String
    public init(sessionID: String) { self.sessionID = sessionID }
    enum CodingKeys: String, CodingKey { case sessionID = "session_id" }
    public init(from decoder: Decoder) throws {
        sessionID = try decoder.container(keyedBy: CodingKeys.self).value(.sessionID, "")
    }
}

/// `approval.cancelled`: the card is withdrawn ("timeout" or "cancelled").
public struct ApprovalCancelled: Sendable, Equatable, Decodable {
    public var approvalID: String
    public var reason: String
    public init(approvalID: String, reason: String) {
        self.approvalID = approvalID
        self.reason = reason
    }
    enum CodingKeys: String, CodingKey {
        case reason
        case approvalID = "approval_id"
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        approvalID = c.value(.approvalID, "")
        reason = c.value(.reason, "")
    }
}

/// `plugin.prompt`: what the user must do during OAuth.
public struct PluginPrompt: Sendable, Equatable, Decodable {
    public var plugin: String
    public var userCode: String
    public var url: String
    public var expiresAt: Date?
    enum CodingKeys: String, CodingKey {
        case plugin, url
        case userCode = "user_code"
        case expiresAt = "expires_at"
    }
    public init(plugin: String, userCode: String = "", url: String, expiresAt: Date? = nil) {
        self.plugin = plugin
        self.userCode = userCode
        self.url = url
        self.expiresAt = expiresAt
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        plugin = c.value(.plugin, "")
        userCode = c.value(.userCode, "")
        url = c.value(.url, "")
        expiresAt = c.time(.expiresAt)
    }
}

/// The payload of an `error` reply.
public struct ErrorData: Sendable, Equatable, Decodable {
    public var message: String
    public init(message: String) { self.message = message }
    enum CodingKeys: String, CodingKey { case message }
    public init(from decoder: Decoder) throws {
        message = try decoder.container(keyedBy: CodingKeys.self).value(.message, "")
    }
}

/// User-adjustable daemon settings (`settings.get` / `settings.set`).
public struct DaemonSettings: Sendable, Equatable, Codable {
    public var resultLimitBytes: Int
    public var updateCheck: Bool
    public init(resultLimitBytes: Int, updateCheck: Bool) {
        self.resultLimitBytes = resultLimitBytes
        self.updateCheck = updateCheck
    }
    enum CodingKeys: String, CodingKey {
        case resultLimitBytes = "result_limit_bytes"
        case updateCheck = "update_check"
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        resultLimitBytes = c.value(.resultLimitBytes, 0)
        updateCheck = c.value(.updateCheck, true)
    }
}

/// One row of `policy.state`.
public struct PolicyTool: Sendable, Equatable, Identifiable, Decodable {
    public var plugin: String
    public var tool: String
    /// auto | ask | block
    public var level: String
    public var noSession: Bool
    public var taint: Bool
    public var destructive: Bool
    public var defaultLevel: String
    public var overridden: Bool
    public var id: String { plugin + "_" + tool }
    enum CodingKeys: String, CodingKey {
        case plugin, tool, level, taint, destructive, overridden
        case noSession = "no_session"
        case defaultLevel = "default_level"
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        plugin = c.value(.plugin, "")
        tool = c.value(.tool, "")
        level = c.value(.level, "")
        noSession = c.value(.noSession, false)
        taint = c.value(.taint, false)
        destructive = c.value(.destructive, false)
        defaultLevel = c.value(.defaultLevel, "")
        overridden = c.value(.overridden, false)
    }
}

/// Whether farero is registered with an agent (F-06).
public struct AgentCfgStatus: Sendable, Equatable, Decodable {
    public var agent: String
    public var cliFound: Bool
    public var cliPath: String
    public var version: String
    public var minVersion: String
    public var versionOK: Bool
    public var settingsPath: String
    public var hooksInstalled: Bool
    public var allowInstalled: Bool
    public var mcpInstalled: Bool
    public var hookPath: String
    public var stalePath: Bool
    public var gatewayURL: String
    public var backupPath: String
    public var message: String
    enum CodingKeys: String, CodingKey {
        case agent, version, message
        case cliFound = "cli_found"
        case cliPath = "cli_path"
        case minVersion = "min_version"
        case versionOK = "version_ok"
        case settingsPath = "settings_path"
        case hooksInstalled = "hooks_installed"
        case allowInstalled = "allow_installed"
        case mcpInstalled = "mcp_installed"
        case hookPath = "hook_path"
        case stalePath = "stale_path"
        case gatewayURL = "gateway_url"
        case backupPath = "backup_path"
    }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agent = c.value(.agent, "")
        cliFound = c.value(.cliFound, false)
        cliPath = c.value(.cliPath, "")
        version = c.value(.version, "")
        minVersion = c.value(.minVersion, "")
        versionOK = c.value(.versionOK, false)
        settingsPath = c.value(.settingsPath, "")
        hooksInstalled = c.value(.hooksInstalled, false)
        allowInstalled = c.value(.allowInstalled, false)
        mcpInstalled = c.value(.mcpInstalled, false)
        hookPath = c.value(.hookPath, "")
        stalePath = c.value(.stalePath, false)
        gatewayURL = c.value(.gatewayURL, "")
        backupPath = c.value(.backupPath, "")
        message = c.value(.message, "")
    }

    /// Every piece farero needs is in place.
    public var isFullyInstalled: Bool { hooksInstalled && allowInstalled && mcpInstalled && !stalePath }
}

/// One file the agent config installer will rewrite.
public struct FileChange: Sendable, Equatable, Decodable {
    public var path: String
    public var before: String
    public var after: String
    enum CodingKeys: String, CodingKey { case path, before, after }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        path = c.value(.path, "")
        before = c.value(.before, "")
        after = c.value(.after, "")
    }
}

/// What `agentcfg.apply` would do.
public struct AgentCfgPlan: Sendable, Equatable, Decodable {
    public var agent: String
    public var changes: [FileChange]
    public var commands: [String]
    enum CodingKeys: String, CodingKey { case agent, changes, commands }
    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        agent = c.value(.agent, "")
        changes = c.value(.changes, [])
        commands = c.value(.commands, [])
    }
}

// MARK: - Requests (app → farerod)

public struct UIHello: Sendable, Encodable {
    public var version: String
    public init(version: String) { self.version = version }
}

public struct ApprovalResponse: Sendable, Encodable, Equatable {
    public var approvalID: String
    public var answer: ApprovalAnswer
    public init(approvalID: String, answer: ApprovalAnswer) {
        self.approvalID = approvalID
        self.answer = answer
    }
    enum CodingKeys: String, CodingKey {
        case answer
        case approvalID = "approval_id"
    }
}

/// `log.query`. Empty fields are omitted (Go reads them as "any").
public struct LogQuery: Sendable, Encodable, Equatable {
    public var query = ""
    public var sessionID = ""
    public var agent = ""
    public var plugin = ""
    public var tool = ""
    public var decision = ""
    public var kind = ""
    public var from: Date?
    public var to: Date?
    public var limit = 0
    public var offset = 0

    public init(query: String = "", limit: Int = 100) {
        self.query = query
        self.limit = limit
    }

    enum CodingKeys: String, CodingKey {
        case query, agent, plugin, tool, decision, kind, from, to, limit, offset
        case sessionID = "session_id"
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        func put(_ v: String, _ k: CodingKeys) throws { if !v.isEmpty { try c.encode(v, forKey: k) } }
        try put(query, .query)
        try put(sessionID, .sessionID)
        try put(agent, .agent)
        try put(plugin, .plugin)
        try put(tool, .tool)
        try put(decision, .decision)
        try put(kind, .kind)
        if let from { try c.encode(GoTime.format(from), forKey: .from) }
        if let to { try c.encode(GoTime.format(to), forKey: .to) }
        if limit > 0 { try c.encode(limit, forKey: .limit) }
        if offset > 0 { try c.encode(offset, forKey: .offset) }
    }
}

public struct PolicySet: Sendable, Encodable {
    public var plugin: String
    public var tool: String
    public var level: String
    public init(plugin: String, tool: String, level: String) {
        self.plugin = plugin
        self.tool = tool
        self.level = level
    }
}

public struct PluginRef: Sendable, Encodable {
    public var plugin: String
    public init(plugin: String) { self.plugin = plugin }
}

public struct PluginConnect: Sendable, Encodable {
    public var plugin: String
    public var params: [String: String]?
    public init(plugin: String, params: [String: String]? = nil) {
        self.plugin = plugin
        self.params = params
    }
}

public struct PluginSetOption: Sendable, Encodable {
    public var plugin: String
    public var key: String
    public var value: String
    public init(plugin: String, key: String, value: String) {
        self.plugin = plugin
        self.key = key
        self.value = value
    }
}

public struct AgentRef: Sendable, Encodable {
    public var agent: String
    public init(agent: String = "claude") { self.agent = agent }
}

// MARK: - Tolerant decoding helpers

extension KeyedDecodingContainer {
    /// The value at `key`, or `fallback` when it is missing, null or of the
    /// wrong type.
    func value<T: Decodable>(_ key: Key, _ fallback: T) -> T {
        ((try? decodeIfPresent(T.self, forKey: key)) ?? nil) ?? fallback
    }

    /// An RFC 3339 time, nil when missing, malformed or Go's zero time.
    func time(_ key: Key) -> Date? {
        guard let s = (try? decodeIfPresent(String.self, forKey: key)) ?? nil else { return nil }
        return GoTime.parse(s)
    }
}
