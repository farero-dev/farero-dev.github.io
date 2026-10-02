import AppKit
import FareroCore

/// A connect flow started from this app (기능 명세서 F-07).
struct PluginActivity: Equatable {
    var connecting = false
    var notice: String?
    var noticeIsError = false
}

enum ReregistrationState: Equatable {
    case idle
    /// The daemon is another version; waiting for the approval queue to empty.
    case pending
    case done
}

extension AppModel {
    /// CFBundleShortVersionString, or `FARERO_DEBUG_APP_VERSION` in
    /// development. nil when running unbundled.
    static var appVersion: String? {
        if let v = ProcessInfo.processInfo.environment["FARERO_DEBUG_APP_VERSION"], !v.isEmpty { return v }
        return Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String
    }

    /// Reactions to messages beyond the state reducer: fetching settings
    /// after a snapshot, opening OAuth pages, tracking connect flows and the
    /// daemon version check.
    func handleSideEffects(of action: AppAction) {
        guard case .ipc(.message(let m)) = action else { return }
        switch m.payload {
        case .stateSnapshot(let snap):
            Task { await refreshDaemonSettings() }
            checkDaemonVersion(snap.version)
        case .pluginPrompt(let p):
            // Open the browser only for a flow the user started here.
            if pluginActivity[p.plugin]?.connecting == true, !openedPromptURLs.contains(p.url),
               let url = URL(string: p.url), url.scheme == "https" || url.scheme == "http" {
                openedPromptURLs.insert(p.url)
                NSWorkspace.shared.open(url)
            }
        case .pluginUpdated(let p):
            guard pluginActivity[p.plugin]?.connecting == true else { break }
            if p.status == "connected" {
                pluginActivity[p.plugin] = PluginActivity(notice: p.accountLabel.isEmpty ? "연결됨" : "\(p.accountLabel) 계정으로 연결됨")
            } else {
                // farerod reports a failed first connect only as an unchanged
                // status, so the reason is in its log.
                let reason = p.error.isEmpty ? "자세한 이유는 데몬 로그를 확인하세요" : p.error
                pluginActivity[p.plugin] = PluginActivity(notice: "연결하지 못했습니다: \(reason)", noticeIsError: true)
            }
        case .approvalCancelled, .callLogged:
            if reregistration == .pending { checkDaemonVersion(state.daemonVersion) }
        default:
            break
        }
    }

    // MARK: - Daemon settings and update check (Q41, Q43)

    func refreshDaemonSettings() async {
        guard let reply = try? await client.request(MessageType.settingsGet),
              case .settings(let s) = reply.payload else { return }
        let firstLoad = daemonSettings == nil
        daemonSettings = s
        if firstLoad { startUpdateChecks() }
    }

    /// Saves daemon settings and returns an error message on failure.
    func saveDaemonSettings(_ s: DaemonSettings) async -> String? {
        do {
            let reply = try await client.request(MessageType.settingsSet, s)
            if case .settings(let saved) = reply.payload {
                let turnedOn = saved.updateCheck && daemonSettings?.updateCheck == false
                daemonSettings = saved
                if turnedOn { Task { await checkForUpdate() } }
            }
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    /// Checks now and then every 24 hours while the setting is on.
    func startUpdateChecks() {
        guard updateTask == nil, UpdateCheck.isCheckable(currentVersion: Self.appVersion) else { return }
        updateTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.checkForUpdate()
                try? await Task.sleep(for: .seconds(UpdateCheck.interval))
            }
        }
    }

    /// One GET to the GitHub releases API: the only request the app itself
    /// makes. A 404 (no release yet) is not an error.
    func checkForUpdate() async {
        guard daemonSettings?.updateCheck == true, let current = Self.appVersion,
              UpdateCheck.isCheckable(currentVersion: current) else { return }
        var req = URLRequest(url: UpdateCheck.latestReleaseURL, timeoutInterval: 20)
        req.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        req.setValue("farero/\(current)", forHTTPHeaderField: "User-Agent")
        do {
            let (data, response) = try await URLSession.shared.data(for: req)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            availableUpdate = try UpdateCheck.evaluate(statusCode: status, body: data, currentVersion: current)
            debugLog("update check: HTTP \(status), newer: \(availableUpdate?.version ?? "none")")
        } catch {
            debugLog("update check failed: \(error)")
        }
        for f in onChange { f() }
    }

    // MARK: - Version mismatch (아키텍처 16장)

    /// A bundled app talking to a farerod of another version re-registers
    /// it once, after the approval queue is empty, so launchd starts the
    /// daemon from the current bundle.
    func checkDaemonVersion(_ daemonVersion: String) {
        guard reregistration != .done, DaemonRegistrar.skipReason() == nil else { return }
        let app = Self.appVersion
        guard DaemonVersionCheck.needsReregistration(appVersion: app, daemonVersion: daemonVersion) else {
            reregistration = .idle
            return
        }
        guard state.approvals.isEmpty else {
            reregistration = .pending
            versionNotice = "데몬 버전(\(daemonVersion))이 앱(\(app ?? "?"))과 다름: 대기 중인 승인이 끝나면 다시 등록합니다"
            return
        }
        reregistration = .done
        versionNotice = "데몬 버전(\(daemonVersion))이 앱(\(app ?? "?"))과 달라 다시 등록하는 중…"
        Task { [weak self] in
            guard let self else { return }
            if let error = await registrar.reregister() {
                versionNotice = "데몬 다시 등록 실패: \(error)"
            } else {
                versionNotice = "데몬 버전(\(daemonVersion))이 앱(\(app ?? "?"))과 달라 다시 등록함"
            }
            registrationLabel = registrar.status.label
        }
    }

    // MARK: - Plugins (F-07)

    func connectPlugin(_ plugin: String, params: [String: String]? = nil) {
        pluginActivity[plugin] = PluginActivity(connecting: true)
        let client = self.client
        Task { [weak self] in
            do {
                try await client.request(MessageType.pluginConnect, PluginConnect(plugin: plugin, params: params))
            } catch {
                self?.pluginActivity[plugin] = PluginActivity(notice: error.localizedDescription, noticeIsError: true)
            }
        }
    }

    func disconnectPlugin(_ plugin: String) {
        let client = self.client
        Task { [weak self] in
            do {
                try await client.request(MessageType.pluginDisconnect, PluginRef(plugin: plugin))
                self?.pluginActivity[plugin] = PluginActivity(notice: "연결을 해제했습니다")
            } catch {
                self?.pluginActivity[plugin] = PluginActivity(notice: error.localizedDescription, noticeIsError: true)
            }
        }
    }

    /// Sets a plugin option. farerod saves the option before it reconnects
    /// the plugin, and reports a failed reconnect (for example while the
    /// plugin is not connected) as an error without broadcasting the change,
    /// so the list is re-read to show what was actually saved.
    func setPluginOption(_ plugin: String, key: String, value: String) {
        let client = self.client
        Task { [weak self] in
            var failure: String?
            do {
                try await client.request(MessageType.pluginSetOption, PluginSetOption(plugin: plugin, key: key, value: value))
            } catch {
                failure = error.localizedDescription
            }
            if let reply = try? await client.request(MessageType.pluginList) {
                self?.dispatch(.ipc(.message(reply)))
            }
            guard let self else { return }
            let saved = state.plugins.first { $0.plugin == plugin }?.options[key] == value
            if let failure {
                pluginActivity[plugin] = saved
                    ? PluginActivity(notice: "설정을 저장했습니다. 플러그인을 연결하면 적용됩니다.")
                    : PluginActivity(notice: failure, noticeIsError: true)
            }
        }
    }

    // MARK: - Sessions (5-1)

    /// Deletes a session and its log. Returns an error message on failure;
    /// the list updates from the `session.removed` broadcast.
    func deleteSession(_ sessionID: String) async -> String? {
        do {
            try await client.request(MessageType.sessionDelete, SessionRef(sessionID: sessionID))
            return nil
        } catch {
            return error.localizedDescription
        }
    }

    func debugLog(_ text: String) {
        guard ProcessInfo.processInfo.environment["FARERO_DEBUG"] == "1" else { return }
        FileHandle.standardError.write(Data("farero: \(text)\n".utf8))
    }
}
