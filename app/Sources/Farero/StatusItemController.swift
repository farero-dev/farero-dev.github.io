import AppKit
import FareroCore

/// The menu bar icon and its menu (기능 명세서 F-01): 설정, 플러그인 연결,
/// 로그 검색, 데몬 상태, 종료, with the daemon connection shown on top and,
/// below it, what needs attention in the Claude Code registration (F-06).
@MainActor
final class StatusItemController: NSObject, NSMenuDelegate {
    private let model: AppModel
    private let windows: WindowManager
    private var registrar: DaemonRegistrar { model.registrar }
    private let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.variableLength)
    private var shownCharacter: CharacterState?

    init(model: AppModel, windows: WindowManager) {
        self.model = model
        self.windows = windows
        super.init()
        let menu = NSMenu()
        menu.delegate = self
        menu.autoenablesItems = false
        item.menu = menu
        item.button?.imagePosition = .imageOnly
        update()
        model.onChange.append { [weak self] in self?.update() }
    }

    private func update() {
        guard let button = item.button else { return }
        let state = model.character
        if state != shownCharacter {
            shownCharacter = state
            if let img = CharacterImages.statusItemImage(state) {
                button.image = img
            } else {
                button.image = NSImage(systemSymbolName: "light.beacon.max", accessibilityDescription: "farero")
            }
        }
        let tip = (["farero · \(state.lighthouse.label)"] + model.state.agentCfgAlerts.map(\.title))
            .joined(separator: "\n")
        if button.toolTip != tip { button.toolTip = tip }
    }

    func menuNeedsUpdate(_ menu: NSMenu) {
        registrar.refresh()
        menu.removeAllItems()
        let s = model.state

        switch s.connection {
        case .connected:
            menu.addItem(info("데몬 연결됨", symbol: "circle.fill", tint: .systemGreen))
            if s.gateway.running, !s.gateway.url.isEmpty {
                menu.addItem(info("게이트웨이 \(s.gateway.url)"))
            }
            if !s.gateway.error.isEmpty {
                menu.addItem(info("게이트웨이 오류: \(s.gateway.error)", tint: .systemOrange))
            }
        case .connecting:
            menu.addItem(info("데몬 연결 중…", symbol: "circle.dotted", tint: .systemYellow))
        case .disconnected(let reason):
            menu.addItem(info("데몬 연결 끊김", symbol: "circle.fill", tint: .systemRed))
            menu.addItem(info(reason))
        }
        let live = s.activeSessions.count
        menu.addItem(info("세션 \(live)개 · 승인 대기 \(s.approvals.count)개"))
        for alert in s.agentCfgAlerts {
            menu.addItem(alertItem(alert))
        }
        if case .failed = registrar.status {
            menu.addItem(action("로그인 항목에서 데몬 허용…", #selector(openLoginItems)))
        }
        if let notice = model.versionNotice {
            menu.addItem(info(notice))
        }
        menu.addItem(.separator())
        menu.addItem(action("설정…", #selector(openSettings), key: ","))
        menu.addItem(action("플러그인 연결…", #selector(openPlugins)))
        menu.addItem(action("로그 검색…", #selector(openLog), key: "f"))
        menu.addItem(action("설정 도우미…", #selector(openOnboarding)))
        menu.addItem(action("데몬 상태…", #selector(openDaemonStatus)))
        if let update = model.availableUpdate {
            menu.addItem(.separator())
            let item = action("새 버전 \(update.version) 받기", #selector(openUpdate))
            item.image = NSImage(systemSymbolName: "arrow.down.circle", accessibilityDescription: nil)
            menu.addItem(item)
        }
        menu.addItem(.separator())
        menu.addItem(action("farero 종료", #selector(quit), key: "q"))
    }

    private func info(_ title: String, symbol: String? = nil, tint: NSColor? = nil) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        if let symbol {
            item.image = Self.symbol(symbol, pointSize: 8, tint: tint ?? .secondaryLabelColor)
        }
        return item
    }

    /// A Claude Code registration alert; choosing it opens 설정 > Claude Code.
    private func alertItem(_ alert: AgentCfgAlert) -> NSMenuItem {
        let item = action(alert.title, #selector(openAgentCfgSettings))
        item.toolTip = alert.detail
        item.image = switch alert.kind {
        case .outdatedCLI: Self.symbol("exclamationmark.triangle.fill", pointSize: 11, tint: .systemOrange)
        case .notice: Self.symbol("info.circle.fill", pointSize: 11, tint: .systemBlue)
        }
        return item
    }

    private static func symbol(_ name: String, pointSize: CGFloat, tint: NSColor) -> NSImage? {
        let config = NSImage.SymbolConfiguration(pointSize: pointSize, weight: .regular)
            .applying(.init(paletteColors: [tint]))
        return NSImage(systemSymbolName: name, accessibilityDescription: nil)?.withSymbolConfiguration(config)
    }

    private func action(_ title: String, _ selector: Selector, key: String = "") -> NSMenuItem {
        let item = NSMenuItem(title: title, action: selector, keyEquivalent: key)
        item.target = self
        return item
    }

    @objc private func openSettings() { windows.show(.settings) }
    @objc private func openAgentCfgSettings() {
        model.dispatch(.agentCfgNoticeSeen)
        windows.showSettings(.claude)
    }
    @objc private func openPlugins() { windows.show(.plugins) }
    @objc private func openLog() { windows.show(.logSearch) }
    @objc private func openDaemonStatus() { windows.show(.daemonStatus) }
    @objc private func openOnboarding() { windows.show(.onboarding) }
    @objc private func openUpdate() {
        if let url = model.availableUpdate?.url { NSWorkspace.shared.open(url) }
    }
    @objc private func openLoginItems() { registrar.openLoginItemsSettings() }
    @objc private func quit() { NSApp.terminate(nil) }
}
