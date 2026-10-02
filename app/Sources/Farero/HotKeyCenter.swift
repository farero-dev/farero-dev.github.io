import AppKit
import Carbon.HIToolbox
import FareroCore

/// Global approval shortcuts (기능 명세서 5-3) through Carbon's
/// `RegisterEventHotKey`, which needs neither focus nor Accessibility
/// permission. Keys are registered only while a card is pending so ⌃⌥Y/S/N
/// stay free for other apps the rest of the time.
@MainActor
final class HotKeyCenter {
    var onPress: ((ApprovalShortcut) -> Void)?
    /// Logs registration changes (FARERO_DEBUG=1).
    var debugLog: ((String) -> Void)?

    private var registered: [ApprovalShortcut: EventHotKeyRef] = [:]
    private var handler: EventHandlerRef?
    private static let signature: OSType = 0x4652_524F // "FRRO"

    /// Registers exactly `wanted`.
    func update(_ wanted: [ApprovalShortcut]) {
        let want = Set(wanted)
        for (shortcut, ref) in registered where !want.contains(shortcut) {
            UnregisterEventHotKey(ref)
            registered[shortcut] = nil
            debugLog?("hotkey unregistered \(shortcut.hint)")
        }
        if !want.isEmpty { installHandler() }
        for shortcut in wanted where registered[shortcut] == nil {
            var ref: EventHotKeyRef?
            let id = EventHotKeyID(signature: Self.signature, id: Self.id(shortcut))
            let status = RegisterEventHotKey(Self.keyCode(shortcut), UInt32(controlKey | optionKey), id,
                                             GetApplicationEventTarget(), 0, &ref)
            if status == noErr, let ref {
                registered[shortcut] = ref
                debugLog?("hotkey registered \(shortcut.hint)")
            } else {
                NSLog("farero: RegisterEventHotKey %@ failed: %d", shortcut.hint, status)
            }
        }
        if registered.isEmpty { removeHandler() }
    }

    private func installHandler() {
        guard handler == nil else { return }
        var spec = EventTypeSpec(eventClass: OSType(kEventClassKeyboard), eventKind: UInt32(kEventHotKeyPressed))
        let callback: EventHandlerUPP = { _, event, userData in
            guard let event, let userData else { return OSStatus(eventNotHandledErr) }
            var hotKey = EventHotKeyID()
            let err = GetEventParameter(event, EventParamName(kEventParamDirectObject), EventParamType(typeEventHotKeyID),
                                        nil, MemoryLayout<EventHotKeyID>.size, nil, &hotKey)
            guard err == noErr, hotKey.signature == HotKeyCenter.signature else { return OSStatus(eventNotHandledErr) }
            let center = Unmanaged<HotKeyCenter>.fromOpaque(userData).takeUnretainedValue()
            let id = hotKey.id
            // Carbon delivers hot keys on the main thread's event loop.
            MainActor.assumeIsolated { center.fire(id) }
            return noErr
        }
        let status = InstallEventHandler(GetApplicationEventTarget(), callback, 1, &spec,
                                         Unmanaged.passUnretained(self).toOpaque(), &handler)
        if status != noErr { NSLog("farero: InstallEventHandler failed: %d", status) }
    }

    private func removeHandler() {
        if let handler { RemoveEventHandler(handler) }
        handler = nil
    }

    private func fire(_ id: UInt32) {
        guard let shortcut = ApprovalShortcut.allCases.first(where: { Self.id($0) == id }) else { return }
        onPress?(shortcut)
    }

    private static func id(_ s: ApprovalShortcut) -> UInt32 {
        switch s {
        case .allow: 1
        case .allowSession: 2
        case .deny: 3
        }
    }

    private static func keyCode(_ s: ApprovalShortcut) -> UInt32 {
        switch s {
        case .allow: UInt32(kVK_ANSI_Y)
        case .allowSession: UInt32(kVK_ANSI_S)
        case .deny: UInt32(kVK_ANSI_N)
        }
    }
}
