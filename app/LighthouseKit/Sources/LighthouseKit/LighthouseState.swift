import Foundation

/// The 12 character states (기능 명세서 F-12, Q24).
public enum LighthouseState: String, CaseIterable, Sendable {
    /// 잠듦: no active session. Lamp off.
    case sleeping
    /// 인사: a session just started (SessionStart). Lamp Al (alternating).
    case greeting
    /// 생각 중: after UserPromptSubmit, before a tool runs. Lamp Iso (equal on/off).
    case thinking
    /// 작업 중: a tool is running (PreToolUse … PostToolUse). Rotating beam.
    case working
    /// 여럿 작업 중: two or more sessions running. Rotating beam.
    case workingMany
    /// 승인 필요: an approval card is waiting. Lamp Q (quick flashing, ≤ 1 Hz).
    case needsApproval
    /// 허용함: shown for 1.5 s after an allow. Lamp F (fixed).
    case allowed
    /// 거부함: denied, auto-denied or timed out. Lamp off.
    case denied
    /// 조심: the session is tainted. Lamp Oc (occulting).
    case caution
    /// 입력 대기: Stop / Notification. Lamp F (fixed).
    case waitingInput
    /// 오류: a tool or upstream call failed. Lamp Fl (flashing).
    case error
    /// 연결 끊김: no daemon connection, or an unknown session. Lamp off.
    case disconnected

    /// Korean label for accessibility and debugging.
    public var label: String {
        switch self {
        case .sleeping: "잠듦"
        case .greeting: "인사"
        case .thinking: "생각 중"
        case .working: "작업 중"
        case .workingMany: "여럿 작업 중"
        case .needsApproval: "승인 필요"
        case .allowed: "허용함"
        case .denied: "거부함"
        case .caution: "조심"
        case .waitingInput: "입력 대기"
        case .error: "오류"
        case .disconnected: "연결 끊김"
        }
    }
}
