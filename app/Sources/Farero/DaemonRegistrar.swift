import AppKit
import ServiceManagement

/// Registers farerod as a LaunchAgent through `SMAppService` (M1, Q55).
@MainActor
final class DaemonRegistrar {
    enum Status: Equatable {
        /// Development run (`FARERO_SOCKET` set, or not inside an .app bundle).
        case skipped(String)
        case enabled
        case requiresApproval
        case notRegistered
        case notFound
        case failed(String)

        var label: String {
            switch self {
            case .skipped(let why): "등록 안 함 (\(why))"
            case .enabled: "로그인 항목 등록됨"
            case .requiresApproval: "로그인 항목 승인 필요"
            case .notRegistered: "로그인 항목 미등록"
            case .notFound: "데몬 설정 파일을 찾을 수 없음"
            case .failed(let m): "등록 실패: \(m)"
            }
        }
    }

    static let plistName = "dev.farero.farerod.plist"

    private(set) var status: Status = .notRegistered
    private var alertShown = false

    private var service: SMAppService { SMAppService.agent(plistName: Self.plistName) }

    /// Why registration is skipped, or nil when it should run.
    static func skipReason(environment: [String: String] = ProcessInfo.processInfo.environment,
                           bundle: Bundle = .main) -> String? {
        if let s = environment["FARERO_SOCKET"], !s.isEmpty { return "개발 모드: FARERO_SOCKET" }
        if bundle.bundleURL.pathExtension != "app" || bundle.bundleIdentifier == nil { return "앱 번들 밖에서 실행" }
        return nil
    }

    /// On launch: register when needed and ask the user to approve when
    /// macOS requires it.
    func registerIfNeeded() {
        if let why = Self.skipReason() {
            status = .skipped(why)
            return
        }
        switch service.status {
        case .notRegistered, .notFound:
            do {
                try service.register()
                refresh()
            } catch {
                refresh()
                if status != .requiresApproval && status != .enabled {
                    status = .failed(error.localizedDescription)
                }
            }
        default:
            refresh()
        }
        if status == .requiresApproval { showApprovalAlert() }
    }

    /// Re-reads the service status (the user may have changed it in System Settings).
    func refresh() {
        if let why = Self.skipReason() {
            status = .skipped(why)
            return
        }
        switch service.status {
        case .enabled: status = .enabled
        case .requiresApproval: status = .requiresApproval
        case .notRegistered: status = .notRegistered
        case .notFound: status = .notFound
        @unknown default: status = .failed("알 수 없는 상태")
        }
    }

    func openLoginItemsSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }

    private func showApprovalAlert() {
        guard !alertShown else { return }
        alertShown = true
        let alert = NSAlert()
        alert.messageText = "farero 데몬을 허용해 주세요"
        alert.informativeText = "시스템 설정 > 일반 > 로그인 항목에서 Farero를 켜야 세션 표시와 승인 카드가 동작합니다."
        alert.addButton(withTitle: "시스템 설정 열기")
        alert.addButton(withTitle: "나중에")
        NSApp.activate()
        if alert.runModal() == .alertFirstButtonReturn {
            openLoginItemsSettings()
        }
    }
}
