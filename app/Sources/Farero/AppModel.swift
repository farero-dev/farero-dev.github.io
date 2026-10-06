import AppKit
import FareroCore
import Observation

/// Geometry of the notch the panel hangs from, in points.
struct NotchMetrics: Equatable {
    /// The hardware notch (or 0 wide on a screen without one).
    var notchSize: CGSize
    var hasNotch: Bool
    /// The panel's fixed size; the island is drawn at its top centre.
    var panelSize: CGSize

    static let `default` = NotchMetrics(notchSize: CGSize(width: 0, height: 24), hasNotch: false,
                                        panelSize: CGSize(width: 640, height: 640))
}

/// The app's UI state on the main actor. It feeds IPC events into the pure
/// `AppState` reducer and derives what the views show.
@MainActor
@Observable
final class AppModel {
    private(set) var state = AppState()
    private(set) var character: CharacterState = .disconnected
    private(set) var hovering = false
    /// Opened by a click; stays open until clicked again or outside.
    var pinned = false
    /// A short message shown in the notch (terminal jump failures and such).
    private(set) var toast: String?
    var metrics = NotchMetrics.default
    /// The daemon registration status line for the menu.
    var registrationLabel = ""
    /// farerod's user settings (`settings.get`), fetched after each snapshot.
    var daemonSettings: DaemonSettings?
    /// A newer release found by the update check (Q41).
    var availableUpdate: UpdateCheck.Available?
    /// Connect flows started from this app and how they ended, by plugin.
    var pluginActivity: [String: PluginActivity] = [:]
    /// What the daemon version check did (shown in 데몬 상태).
    var versionNotice: String?
    /// The selected tab of 설정, so the menu can open it on Claude Code.
    var settingsTab = SettingsTab.initial

    var mode: NotchMode { NotchMode.resolve(state, hovering: hovering, pinned: pinned) }

    @ObservationIgnored let socketPath: String
    @ObservationIgnored let client: IPCClient
    @ObservationIgnored let hotKeys = HotKeyCenter()
    @ObservationIgnored let registrar = DaemonRegistrar()
    @ObservationIgnored var updateTask: Task<Void, Never>?
    @ObservationIgnored var reregistration = ReregistrationState.idle
    @ObservationIgnored var openedPromptURLs: Set<String> = []
    /// An `agentcfg.status` fetch is in flight / another snapshot asked for one meanwhile.
    @ObservationIgnored var agentCfgFetching = false
    @ObservationIgnored var agentCfgFetchAgain = false
    /// AppKit controllers that follow the state (status item, panel).
    @ObservationIgnored var onChange: [() -> Void] = []

    @ObservationIgnored private var characterTimer: Task<Void, Never>?
    @ObservationIgnored private var hoverTask: Task<Void, Never>?
    @ObservationIgnored private var toastTask: Task<Void, Never>?
    @ObservationIgnored private var inputTextCache: [String: ApprovalPresentation.InputDisplay] = [:]

    init(socketPath: String = IPCClient.defaultSocketPath()) {
        self.socketPath = socketPath
        client = IPCClient(socketPath: socketPath, clientVersion: "farero-app/\(Self.appVersion ?? "dev")")
        hotKeys.onPress = { [weak self] shortcut in self?.answerFront(shortcut) }
        if Self.debug {
            hotKeys.debugLog = { FileHandle.standardError.write(Data("farero: \($0)\n".utf8)) }
            installDebugSignals()
        }
    }

    @ObservationIgnored private var signalSources: [DispatchSourceSignal] = []

    /// Development aids (FARERO_DEBUG=1): SIGUSR1 toggles the pinned
    /// (expanded) notch, SIGUSR2 denies the front card through the same
    /// path the ⌃⌥N shortcut uses.
    private func installDebugSignals() {
        signal(SIGUSR1, SIG_IGN)
        signal(SIGUSR2, SIG_IGN)
        let pin = DispatchSource.makeSignalSource(signal: SIGUSR1, queue: .main)
        pin.setEventHandler { [weak self] in MainActor.assumeIsolated { self?.togglePinned() } }
        let deny = DispatchSource.makeSignalSource(signal: SIGUSR2, queue: .main)
        deny.setEventHandler { [weak self] in MainActor.assumeIsolated { self?.answerFront(.deny) } }
        for source in [pin, deny] {
            source.resume()
            signalSources.append(source)
        }
    }

    func start() {
        let events = client.events
        Task { [weak self] in
            for await event in events {
                self?.dispatch(.ipc(event))
            }
        }
        client.start()
    }

    func stop() {
        client.stop()
        hotKeys.update([])
    }

    // MARK: - State

    func dispatch(_ action: AppAction) {
        if Self.debug { Self.log(action) }
        state.reduce(action, now: .now)
        if mode == .hidden { pinned = false }
        let live = Set(state.approvals.map(\.id))
        inputTextCache = inputTextCache.filter { live.contains($0.key) }
        hotKeys.update(ApprovalShortcut.available(front: state.frontApproval))
        refreshCharacter()
        handleSideEffects(of: action)
        for f in onChange { f() }
    }

    /// `FARERO_DEBUG=1` logs every action to stderr.
    @ObservationIgnored private static let debug = ProcessInfo.processInfo.environment["FARERO_DEBUG"] == "1"

    private static func log(_ action: AppAction) {
        let text: String
        switch action {
        case .ipc(.message(let m)): text = "recv \(m.type)\(m.id.map { " id=\($0)" } ?? "")"
        case .ipc(let e): text = "ipc \(e)"
        default: text = "\(action)"
        }
        FileHandle.standardError.write(Data("farero: \(text)\n".utf8))
    }

    private func refreshCharacter() {
        let r = CharacterResolver.resolve(state, now: .now)
        if character != r.state {
            character = r.state
            if Self.debug { FileHandle.standardError.write(Data("farero: character \(r.state.rawValue) mode \(mode)\n".utf8)) }
        }
        characterTimer?.cancel()
        characterTimer = nil
        guard let at = r.reevaluateAt else { return }
        let delay = max(0.01, at.timeIntervalSinceNow + 0.02)
        characterTimer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(delay))
            guard !Task.isCancelled, let self else { return }
            self.refreshCharacter()
            for f in self.onChange { f() }
        }
    }

    // MARK: - Approvals

    /// Sends the user's answer. The card stays (buttons disabled) until
    /// farerod replies `ok`; an `error` reply means the card is already gone.
    func answer(_ approvalID: String, _ answer: ApprovalAnswer) {
        guard let approval = state.approvals.first(where: { $0.id == approvalID }),
              state.answering[approvalID] == nil else { return }
        if answer == .allowSession && !approval.allowSession { return }
        dispatch(.answerSent(approvalID: approvalID, answer: answer))
        let client = self.client
        Task { [weak self] in
            do {
                try await client.request(MessageType.approvalResponse,
                                         ApprovalResponse(approvalID: approvalID, answer: answer))
                self?.dispatch(.answerAccepted(approvalID: approvalID, answer: answer))
            } catch IPCError.server(let message) {
                self?.dispatch(.answerRejected(approvalID: approvalID))
                self?.showToast("이미 끝난 요청 (\(message))")
            } catch {
                self?.dispatch(.answerFailed(approvalID: approvalID))
                self?.showToast("응답을 보내지 못함: \(error.localizedDescription)")
            }
        }
    }

    /// A global shortcut acts on the front (oldest) card.
    func answerFront(_ shortcut: ApprovalShortcut) {
        guard let front = state.frontApproval else { return }
        answer(front.id, shortcut.answer)
    }

    /// Pretty-printed input, computed once per card (inputs can be large).
    func inputDisplay(for a: Approval) -> ApprovalPresentation.InputDisplay {
        if let d = inputTextCache[a.id] { return d }
        let d = ApprovalPresentation.inputDisplay(a)
        inputTextCache[a.id] = d
        return d
    }

    // MARK: - Notch interaction

    /// The pointer entered or left the island. Expanding waits a moment so
    /// passing over the menu bar does not open it; collapsing waits a little
    /// longer so the edge is forgiving.
    func pointerInside(_ inside: Bool) {
        if inside == hovering {
            hoverTask?.cancel()
            hoverTask = nil
            return
        }
        guard hoverTask == nil else { return }
        let delay: Duration = inside ? .milliseconds(120) : .milliseconds(350)
        hoverTask = Task { [weak self] in
            try? await Task.sleep(for: delay)
            guard !Task.isCancelled, let self else { return }
            self.hoverTask = nil
            self.hovering = inside
            for f in self.onChange { f() }
        }
    }

    func togglePinned() {
        pinned.toggle()
        for f in onChange { f() }
    }

    func unpin() {
        guard pinned else { return }
        pinned = false
        for f in onChange { f() }
    }

    // MARK: - Terminal jump (F-11)

    func jump(to session: Session) {
        switch TerminalJumper.jump(tty: session.tty) {
        case .jumped:
            pinned = false
        case .notFound:
            showToast(TerminalJumper.notFoundMessage)
        case .failed(let message):
            showToast(message)
        }
    }

    func showToast(_ message: String) {
        toast = message
        toastTask?.cancel()
        toastTask = Task { [weak self] in
            try? await Task.sleep(for: .seconds(3))
            guard !Task.isCancelled else { return }
            self?.toast = nil
        }
    }
}
