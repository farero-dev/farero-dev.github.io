import AppKit
import FareroCore
import Observation
import ServiceManagement
import SwiftUI

/// 로그인할 때 열기 (Q85): keeps `AppLoginItem`'s plist in line with the
/// user's choice, which is on unless they turn it off. Like farerod's
/// registration it is skipped in development runs and while the app runs
/// translocated (that path disappears). The plist is only written, not
/// loaded: launchd loads it at the next login, and loading it now would just
/// open the running app again.
@MainActor
@Observable
final class OpenAtLogin {
    enum Status: Equatable {
        case unavailable(String)
        case on
        case off
        /// On here, but turned off in System Settings > General > Login Items.
        case offInSystemSettings
        case failed(String)

        var label: String {
            switch self {
            case .unavailable(let why): "사용 안 함 (\(why))"
            case .on: "켜짐"
            case .off: "꺼짐"
            case .offInSystemSettings: "시스템 설정의 로그인 항목에서 꺼져 있음"
            case .failed(let m): "실패: \(m)"
            }
        }
    }

    private(set) var isOn: Bool
    private(set) var status: Status = .off

    var isAvailable: Bool {
        if case .unavailable = status { return false }
        return true
    }

    @ObservationIgnored private let defaults = WindowManager.defaults

    init() {
        isOn = AppLoginItem.isEnabled(stored: defaults.object(forKey: AppLoginItem.defaultsKey))
    }

    private static var plistURL: URL {
        AppLoginItem.plistURL(home: FileManager.default.homeDirectoryForCurrentUser)
    }

    /// On launch and after the user changes the choice: writes or removes
    /// the plist (and rewrites it when the app moved, Q63).
    func apply() {
        if let why = DaemonRegistrar.skipReason() {
            status = .unavailable(why)
            return
        }
        if DaemonRegistrar.isTranslocated {
            status = .unavailable("앱을 응용 프로그램 폴더로 옮긴 뒤 다시 여세요")
            return
        }
        guard let executable = Bundle.main.executableURL?.path else {
            status = .failed("앱 실행 파일을 찾지 못함")
            return
        }
        let url = Self.plistURL
        let fm = FileManager.default
        // An unreadable plist counts as one that must be replaced.
        let existing: Data? = fm.fileExists(atPath: url.path) ? (try? Data(contentsOf: url)) ?? Data() : nil
        do {
            switch AppLoginItem.change(enabled: isOn, existing: existing, executablePath: executable) {
            case .write:
                try fm.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
                try AppLoginItem.encode(executablePath: executable).write(to: url, options: .atomic)
            case .remove:
                try fm.removeItem(at: url)
            case .none:
                break
            }
        } catch {
            status = .failed(error.localizedDescription)
            return
        }
        status = currentStatus()
    }

    func set(_ on: Bool) {
        isOn = on
        defaults.set(on, forKey: AppLoginItem.defaultsKey)
        apply()
    }

    /// Re-reads whether System Settings lets the item run.
    func refresh() {
        switch status {
        case .unavailable, .failed: return
        case .on, .off, .offInSystemSettings: status = currentStatus()
        }
    }

    private func currentStatus() -> Status {
        guard isOn else { return .off }
        return SMAppService.statusForLegacyPlist(at: Self.plistURL) == .requiresApproval ? .offInSystemSettings : .on
    }

    func openLoginItemsSettings() {
        SMAppService.openSystemSettingsLoginItems()
    }
}

/// The 로그인할 때 열기 switch, in 설정 > 일반 and the onboarding's daemon step.
struct OpenAtLoginSection: View {
    let item: OpenAtLogin

    var body: some View {
        Section {
            Toggle("로그인할 때 Farero 열기", isOn: Binding(get: { item.isOn }, set: { item.set($0) }))
                .disabled(!item.isAvailable)
            switch item.status {
            case .offInSystemSettings:
                Banner(text: "시스템 설정 > 일반 > 로그인 항목에서 Farero의 백그라운드 실행이 꺼져 있어 로그인할 때 열리지 않습니다.", style: .warning)
                Button("로그인 항목 설정 열기") { item.openLoginItemsSettings() }
            case .unavailable, .failed:
                Text(item.status.label).foregroundStyle(.secondary)
            case .on, .off:
                EmptyView()
            }
        } footer: {
            Text("앱이 꺼져 있으면 승인이 필요한 플러그인 호출은 자동으로 거부됩니다. 켜 두면 로그인할 때 메뉴바에 farero가 뜹니다. 메뉴에서 종료하면 다음 로그인까지 꺼져 있습니다.")
        }
    }
}
