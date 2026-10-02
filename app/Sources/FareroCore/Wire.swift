import Foundation

/// Message types (daemon/internal/ipc/protocol.go).
public enum MessageType {
    // ui client
    public static let uiHello = "ui.hello"
    public static let stateSnapshot = "state.snapshot"
    public static let sessionUpdated = "session.updated"
    public static let sessionRemoved = "session.removed"
    public static let approvalRequest = "approval.request"
    public static let approvalResponse = "approval.response"
    public static let approvalCancelled = "approval.cancelled"
    public static let callLogged = "call.logged"
    public static let gatewayStatus = "gateway.status"
    public static let pluginUpdated = "plugin.updated"
    public static let pluginPrompt = "plugin.prompt"
    public static let logQuery = "log.query"
    public static let logResult = "log.result"
    public static let policyGet = "policy.get"
    public static let policySet = "policy.set"
    public static let policyState = "policy.state"
    public static let sessionDelete = "session.delete"
    public static let pluginList = "plugin.list"
    public static let pluginConnect = "plugin.connect"
    public static let pluginDisconnect = "plugin.disconnect"
    public static let pluginSetOption = "plugin.set_option"
    public static let agentCfgStatus = "agentcfg.status"
    public static let agentCfgPlan = "agentcfg.plan"
    public static let agentCfgApply = "agentcfg.apply"
    public static let agentCfgRemove = "agentcfg.remove"
    public static let settingsGet = "settings.get"
    public static let settingsSet = "settings.set"
    // generic replies
    public static let ok = "ok"
    public static let error = "error"
}

/// The decoded `data` of a message from farerod. Server notifications and
/// replies share this enum because the payload shape depends only on `type`.
public enum ServerPayload: Sendable, Equatable {
    case stateSnapshot(StateSnapshot)
    case sessionUpdated(Session)
    case sessionRemoved(SessionRef)
    case approvalRequest(Approval)
    case approvalCancelled(ApprovalCancelled)
    case callLogged(Call)
    case gatewayStatus(GatewayInfo)
    case pluginUpdated(PluginState)
    case pluginPrompt(PluginPrompt)
    case logResult([Call])
    case policyState([PolicyTool])
    case pluginList([PluginState])
    case settings(DaemonSettings)
    case agentCfgStatus(AgentCfgStatus)
    case agentCfgPlan(AgentCfgPlan)
    case ok
    case error(ErrorData)
    /// A type this app does not know (newer daemon). Ignored.
    case unknown(type: String)
}

/// One line from farerod.
public struct IncomingMessage: Sendable, Equatable {
    public var type: String
    /// Set on replies: the id of the request being answered.
    public var id: String?
    public var payload: ServerPayload

    public init(type: String, id: String? = nil, payload: ServerPayload) {
        self.type = type
        self.id = id
        self.payload = payload
    }

    /// Decodes one JSON line.
    public static func decode(_ line: Data) throws -> IncomingMessage {
        try JSONDecoder().decode(IncomingMessage.self, from: line)
    }
}

extension IncomingMessage: Decodable {
    enum CodingKeys: String, CodingKey { case type, id, data }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        type = try c.decode(String.self, forKey: .type)
        id = (try? c.decodeIfPresent(String.self, forKey: .id)) ?? nil
        if id?.isEmpty == true { id = nil }

        func data<T: Decodable>(_: T.Type) throws -> T {
            try c.decode(T.self, forKey: .data)
        }
        func list<T: Decodable>(_: T.Type) throws -> [T] {
            try c.decodeIfPresent([T].self, forKey: .data) ?? []
        }
        switch type {
        case MessageType.stateSnapshot: payload = .stateSnapshot(try data(StateSnapshot.self))
        case MessageType.sessionUpdated: payload = .sessionUpdated(try data(Session.self))
        case MessageType.sessionRemoved: payload = .sessionRemoved(try data(SessionRef.self))
        case MessageType.approvalRequest: payload = .approvalRequest(try data(Approval.self))
        case MessageType.approvalCancelled: payload = .approvalCancelled(try data(ApprovalCancelled.self))
        case MessageType.callLogged: payload = .callLogged(try data(Call.self))
        case MessageType.gatewayStatus: payload = .gatewayStatus(try data(GatewayInfo.self))
        case MessageType.pluginUpdated: payload = .pluginUpdated(try data(PluginState.self))
        case MessageType.pluginPrompt: payload = .pluginPrompt(try data(PluginPrompt.self))
        case MessageType.logResult: payload = .logResult(try list(Call.self))
        case MessageType.policyState: payload = .policyState(try list(PolicyTool.self))
        case MessageType.pluginList: payload = .pluginList(try list(PluginState.self))
        case MessageType.settingsGet: payload = .settings(try data(DaemonSettings.self))
        case MessageType.agentCfgStatus: payload = .agentCfgStatus(try data(AgentCfgStatus.self))
        case MessageType.agentCfgPlan: payload = .agentCfgPlan(try data(AgentCfgPlan.self))
        case MessageType.ok: payload = .ok
        case MessageType.error:
            payload = .error((try? c.decodeIfPresent(ErrorData.self, forKey: .data)) ?? ErrorData(message: "알 수 없는 오류"))
        default: payload = .unknown(type: type)
        }
    }
}

/// Just the routing fields of a line, used when the payload fails to decode
/// so a waiting request can still be failed.
struct EnvelopeHeader: Decodable {
    var type: String
    var id: String?
}

/// Encodes an outgoing message as one JSON line (with the trailing newline).
public enum OutgoingMessage {
    struct Envelope<D: Encodable>: Encodable {
        var type: String
        var id: String?
        var data: D?

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: IncomingMessage.CodingKeys.self)
            try c.encode(type, forKey: .type)
            if let id, !id.isEmpty { try c.encode(id, forKey: .id) }
            if let data { try c.encode(data, forKey: .data) }
        }
    }

    struct Empty: Encodable {}

    public static func line<D: Encodable>(type: String, id: String?, data: D?) throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        var out = try encoder.encode(Envelope(type: type, id: id, data: data))
        out.append(0x0A)
        return out
    }

    public static func line(type: String, id: String?) throws -> Data {
        try line(type: type, id: id, data: Optional<Empty>.none)
    }
}
