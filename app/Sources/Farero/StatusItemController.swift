import AppKit
import FareroCore

/// The menu bar icon and its menu (기능 명세서 F-01): 설정, 플러그인 연결,
/// 로그 검색, 데몬 상태, 종료, with the daemon connection shown on top.
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
        let state = model.character
        guard state != shownCharacter, let button = item.button else { return }
        shownCharacter = state
        if let img = CharacterImages.statusItemImage(state) {
            button.image = img
        } else {
            button.image = NSImage(systemSymbolName: "light.beacon.max", accessibilityDescription: "farero")
        }
        button.toolTip = "farero · \(state.lighthouse.label)"
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
        if let symbol, let img = NSImage(systemSymbolName: symbol, accessibilityDescription: nil) {
            let config = NSImage.SymbolConfiguration(pointSize: 8, weight: .regular)
                .applying(.init(paletteColors: [tint ?? .secondaryLabelColor]))
            item.image = img.withSymbolConfiguration(config)
        }
        return item
    }

    private func action(_ title: String, _ selector: Selector, key: String = "") -> NSMenuItem {
        let item = NSMenuItem(title: title, action: selector, keyEquivalent: key)
        item.target = self
        return item
    }

    @objc private func openSettings() { windows.show(.settings) }
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
