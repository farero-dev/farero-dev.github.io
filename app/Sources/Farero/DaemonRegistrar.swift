import AppKit
import FareroCore
import Security
import ServiceManagement

/// Runs farerod as a user LaunchAgent: writes
/// `~/Library/LaunchAgents/dev.farero.farerod.plist` and loads it with
/// launchctl (M1; Q55 changed by the M0 result of 2026-10-06). SMAppService
/// ties the job to an ad-hoc binary's code hash, so launchd refused the
/// farerod of every updated bundle (see `LaunchAgent`).
@MainActor
final class DaemonRegistrar {
    enum Status: Equatable {
        /// Development run (`FARERO_SOCKET` set, or not inside an .app bundle).
        case skipped(String)
        case enabled
        case notRegistered
        case failed(String)

        var label: String {
            switch self {
            case .skipped(let why): "등록 안 함 (\(why))"
            case .enabled: "로그인 항목 등록됨"
            case .notRegistered: "로그인 항목 미등록"
            case .failed(let m): "등록 실패: \(m)"
            }
        }

        /// The daemon should run but does not: point the user at Login Items.
        var needsAttention: Bool {
            switch self {
            case .notRegistered, .failed: true
            case .skipped, .enabled: false
            }
        }
    }

    /// The plist SMAppService registered in earlier builds. It stays in the
    /// bundle only so that registration can be removed.
    nonisolated static let smAppServicePlistName = "dev.farero.farerod.plist"
    /// UserDefaults key for the farerod identity last started
    /// (DaemonRegistrationCheck).
    static let registeredIdentityKey = "registeredDaemonIdentity"

    private(set) var status: Status = .notRegistered
    private var alertShown = false

    private static var domain: String { "gui/\(getuid())" }
    private static var target: String { "\(domain)/\(LaunchAgent.label)" }

    /// Why registration is skipped, or nil when it should run.
    static func skipReason(environment: [String: String] = ProcessInfo.processInfo.environment,
                           bundle: Bundle = .main) -> String? {
        if let s = environment["FARERO_SOCKET"], !s.isEmpty { return "개발 모드: FARERO_SOCKET" }
        if bundle.bundleURL.pathExtension != "app" || bundle.bundleIdentifier == nil { return "앱 번들 밖에서 실행" }
        return nil
    }

    /// On launch: installs and loads the LaunchAgent, and restarts farerod
    /// when this bundle ships a different one (an update or a rebuild).
    /// `onChange` runs when the status is known.
    func registerIfNeeded(onChange: @escaping @MainActor () -> Void = {}) {
        if let why = Self.skipReason() {
            status = .skipped(why)
            return
        }
        Task { [weak self] in
            guard let self else { return }
            await self.install()
            onChange()
            if case .failed = self.status { self.showFailureAlert() }
        }
    }

    private func install() async {
        await Self.removeSMAppServiceRegistration()
        let farerod = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/farerod").path
        let url = LaunchAgent.plistURL(home: FileManager.default.homeDirectoryForCurrentUser)
        let rewrite = LaunchAgent.needsWrite(existing: try? Data(contentsOf: url), farerodPath: farerod)
        if rewrite {
            do {
                try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
                try LaunchAgent.encode(farerodPath: farerod).write(to: url, options: .atomic)
            } catch {
                status = .failed(error.localizedDescription)
                return
            }
        }
        let loaded = await Self.launchctl("print", Self.target).status == 0
        if rewrite && loaded {
            _ = await Self.launchctl("bootout", Self.target)
        }
        if rewrite || !loaded {
            if let error = await Self.bootstrap(url) {
                status = .failed(error)
                return
            }
        } else if DaemonRegistrationCheck.needsReregistration(
            recorded: UserDefaults.standard.string(forKey: Self.registeredIdentityKey),
            current: Self.currentIdentity()) {
            // Same path, new binary: the running farerod is the old one.
            _ = await Self.launchctl("kickstart", "-k", Self.target)
        }
        if let id = Self.currentIdentity() {
            UserDefaults.standard.set(id, forKey: Self.registeredIdentityKey)
        }
        refresh()
    }

    /// launchd may still be tearing the old job down right after bootout.
    private static func bootstrap(_ url: URL) async -> String? {
        var last = ""
        for attempt in 0..<5 {
            if attempt > 0 { try? await Task.sleep(for: .milliseconds(400)) }
            let r = await launchctl("bootstrap", domain, url.path)
            if r.status == 0 { return nil }
            last = r.output.trimmingCharacters(in: .whitespacesAndNewlines)
        }
        return last.isEmpty ? "launchctl bootstrap 실패" : last
    }

    /// Re-reads whether launchd has the job (the user may have turned it
    /// off in System Settings > General > Login Items).
    func refresh() {
        if let why = Self.skipReason() {
            status = .skipped(why)
            return
        }
        if case .failed = status { return }
        status = Self.launchctlSync("print", Self.target) == 0 ? .enabled : .notRegistered
    }

    /// Restarts farerod from the current bundle (the daemon reported another
    /// version). Returns an error message on failure.
    func reregister() async -> String? {
        if Self.skipReason() != nil { return "개발 모드에서는 다시 등록하지 않음" }
        let r = await Self.launchctl("kickstart", "-k", Self.target)
        refresh()
        return r.status == 0 ? nil : r.output.trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// The farerod this bundle ships, or nil when its signature can't be read.
    static func currentIdentity(bundle: Bundle = .main) -> String? {
        let url = bundle.bundleURL.appendingPathComponent("Contents/MacOS/farerod")
        var code: SecStaticCode?
        guard SecStaticCodeCreateWithPath(url as CFURL, [], &code) == errSecSuccess, let code else { return nil }
        var info: CFDictionary?
        guard SecCodeCopySigningInformation(code, [], &info) == errSecSuccess,
              let dict = info as? [String: Any], let hash = dict[kSecCodeInfoUnique as String] as? Data
        else { return nil }
        return DaemonRegistrationCheck.identity(executablePath: url.path, cdhash: hash)
    }

    /// Earlier builds registered farerod with SMAppService under the same
    /// label; that job must go before the LaunchAgent can load.
    nonisolated private static func removeSMAppServiceRegistration() async {
        let service = SMAppService.agent(plistName: smAppServicePlistName)
        guard service.status == .enabled || service.status == .requiresApproval else { return }
        try? await service.unregister()
    }

    nonisolated private static func launchctl(_ args: String...) async -> (status: Int32, output: String) {
        await Task.detached { run(args) }.value
    }

    private static func launchctlSync(_ args: String...) -> Int32 { run(args).status }

    nonisolated private static func run(_ args: [String]) -> (status: Int32, output: String) {
        let p = Process()
        p.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        p.arguments = args
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = pipe
        do { try p.run() } catch { return (-1, error.localizedDescription) }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        p.waitUntilExit()
        return (p.terminationStatus, String(decoding: data, as: UTF8.self))
    }

    func openLoginItemsSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }

    private func showFailureAlert() {
        guard !alertShown else { return }
        alertShown = true
        let alert = NSAlert()
        alert.messageText = "farero 데몬을 시작하지 못했습니다"
        alert.informativeText = "시스템 설정 > 일반 > 로그인 항목에서 farero의 백그라운드 실행이 꺼져 있다면 켜 주세요.\n\n\(status.label)"
        alert.addButton(withTitle: "시스템 설정 열기")
        alert.addButton(withTitle: "나중에")
        NSApp.activate()
        if alert.runModal() == .alertFirstButtonReturn {
            openLoginItemsSettings()
        }
    }
}
