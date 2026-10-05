import Foundation

/// The app's connection to farerod.
public enum ConnectionState: Sendable, Equatable {
    /// Starting up, or the socket connected and the snapshot has not arrived.
    case connecting
    /// The snapshot arrived; the state mirrors farerod.
    case connected
    /// No connection. The client retries every second.
    case disconnected(reason: String)
}

/// Everything that changes `AppState`.
public enum AppAction: Sendable, Equatable {
    case ipc(IPCEvent)
    /// The user answered a card; the request is in flight.
    case answerSent(approvalID: String, answer: ApprovalAnswer)
    /// farerod replied `ok` to the answer.
    case answerAccepted(approvalID: String, answer: ApprovalAnswer)
    /// farerod replied `error`: the card was already resolved (timed out,
    /// cancelled or answered elsewhere), so it is gone.
    case answerRejected(approvalID: String)
    /// The answer could not be delivered (connection lost); buttons re-enable.
    case answerFailed(approvalID: String)
}

/// The app's view of farerod, rebuilt from every `state.snapshot` and kept
/// current by notifications. Pure value type: `reduce` is the only mutation,
/// and time is passed in so tests control it.
public struct AppState: Sendable, Equatable {
    public var connection: ConnectionState = .connecting
    public var daemonVersion = ""
    public var sessions: [String: Session] = [:]
    /// Pending approvals in arrival order; the first is the front card.
    public var approvals: [Approval] = []
    /// Answers sent but not yet acknowledged, by approval id.
    public var answering: [String: ApprovalAnswer] = [:]
    public var plugins: [PluginState] = []
    public var gateway = GatewayInfo()
    /// OAuth prompts in progress, by plugin.
    public var pluginPrompts: [String: PluginPrompt] = [:]

    // Moments that drive short-lived character states (F-12).
    public var lastAllowedAt: Date?
    public var lastDeniedAt: Date?
    public var lastErrorAt: Date?
    /// When each new session appeared (greeting).
    public var sessionAppearedAt: [String: Date] = [:]

    public init() {}

    public var isConnected: Bool { connection == .connected }

    /// Live sessions, newest first.
    public var activeSessions: [Session] {
        sessions.values.filter { $0.status.isActive }.sorted(by: AppState.newestFirst)
    }

    /// The card the shortcuts act on (the oldest).
    public var frontApproval: Approval? { approvals.first }

    public func pendingApprovalCount(sessionID: String) -> Int {
        approvals.filter { $0.sessionID == sessionID }.count
    }

    static func newestFirst(_ a: Session, _ b: Session) -> Bool {
        let ta = a.startedAt ?? .distantPast
        let tb = b.startedAt ?? .distantPast
        return ta != tb ? ta > tb : a.id < b.id
    }

    public mutating func reduce(_ action: AppAction, now: Date) {
        switch action {
        case .ipc(.connected):
            connection = .connecting
        case .ipc(.disconnected(let reason)):
            connection = .disconnected(reason: reason)
            // farerod resolves every card when the last app leaves, and the
            // next snapshot carries whatever is still pending.
            approvals = []
            answering = [:]
        case .ipc(.message(let m)):
            apply(m.payload, now: now)
        case .answerSent(let id, let answer):
            if approvals.contains(where: { $0.id == id }) { answering[id] = answer }
        case .answerAccepted(let id, let answer):
            removeApproval(id)
            if answer == .deny {
                lastDeniedAt = now
            } else {
                lastAllowedAt = now
            }
        case .answerRejected(let id):
            removeApproval(id)
        case .answerFailed(let id):
            answering[id] = nil
        }
        pruneGreetings(now: now)
    }

    private mutating func apply(_ payload: ServerPayload, now: Date) {
        switch payload {
        case .stateSnapshot(let snap):
            connection = .connected
            daemonVersion = snap.version
            sessions = Dictionary(snap.sessions.map { ($0.id, $0) }, uniquingKeysWith: { _, last in last })
            approvals = []
            for a in snap.approvals { upsertApproval(a) }
            answering = [:]
            plugins = snap.plugins
            gateway = snap.gateway
            sessionAppearedAt = [:]
        case .sessionUpdated(let s):
            let previous = sessions[s.id]
            if s.status.isActive, previous == nil || previous?.status == .ended {
                sessionAppearedAt[s.id] = now
            }
            sessions[s.id] = s
        case .sessionRemoved(let ref):
            sessions[ref.sessionID] = nil
            sessionAppearedAt[ref.sessionID] = nil
        case .approvalRequest(let a):
            upsertApproval(a)
        case .approvalCancelled(let c):
            removeApproval(c.approvalID)
        case .callLogged(let call):
            if CallDecision.showsDenied.contains(call.decision) { lastDeniedAt = now }
            if !call.error.isEmpty { lastErrorAt = now }
        case .pluginUpdated(let p):
            if let i = plugins.firstIndex(where: { $0.plugin == p.plugin }) {
                plugins[i] = p
            } else {
                plugins.append(p)
            }
            // plugin.updated follows the end of a connect flow, either way.
            pluginPrompts[p.plugin] = nil
        case .pluginPrompt(let p):
            pluginPrompts[p.plugin] = p
        case .pluginList(let list):
            plugins = list
        case .logResult, .policyState, .settings, .agentCfgStatus, .agentCfgPlan, .ok, .error, .unknown:
            break
        }
    }

    private mutating func upsertApproval(_ a: Approval) {
        if let i = approvals.firstIndex(where: { $0.id == a.id }) {
            approvals[i] = a
        } else {
            approvals.append(a)
        }
    }

    private mutating func removeApproval(_ id: String) {
        approvals.removeAll { $0.id == id }
        answering[id] = nil
    }

    private mutating func pruneGreetings(now: Date) {
        guard !sessionAppearedAt.isEmpty else { return }
        sessionAppearedAt = sessionAppearedAt.filter { now.timeIntervalSince($0.value) < 60 }
    }
}
