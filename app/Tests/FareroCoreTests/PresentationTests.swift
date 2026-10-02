import Foundation
import Testing
@testable import FareroCore

@Suite("Presentation text")
struct PresentationTests {
    var seoul: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "Asia/Seoul")!
        return c
    }

    @Test func sessionDisplayName() {
        // 2026-10-02 09:41:37 KST
        let s = Session(id: "claude:abc", agent: "claude", agentSessionID: "abcdef123456", cwd: "/Users/me/proj/",
                        startedAt: Fixture.t0)
        #expect(SessionDisplay.name(s, now: Fixture.t0.addingTimeInterval(3600), calendar: seoul) == "proj · Claude Code · 09:41")
        #expect(SessionDisplay.name(s, now: Fixture.t0.addingTimeInterval(86_400), calendar: seoul) == "proj · Claude Code · 10/2 09:41")
        var noCwd = s
        noCwd.cwd = ""
        noCwd.startedAt = nil
        noCwd.agent = "codex"
        #expect(SessionDisplay.name(noCwd, now: Fixture.t0, calendar: seoul) == "abcdef12 · Codex")
    }

    @Test func reasonTexts() {
        #expect(ApprovalPresentation.reasonText("policy") == "분류표에서 승인이 필요한 도구")
        #expect(ApprovalPresentation.reasonText("tainted") == "이 세션이 신뢰할 수 없는 콘텐츠를 읽음")
        #expect(ApprovalPresentation.reasonText("unknown_session") == "어느 세션의 호출인지 알 수 없음")
        #expect(ApprovalPresentation.reasonText("agent_request") == "에이전트가 권한을 요청함")
        #expect(ApprovalPresentation.reasonText("new_reason") == "new_reason")
    }

    @Test func toolTitleAndHeadline() {
        let bash = Fixture.approval("a1")
        #expect(ApprovalPresentation.toolTitle(bash) == "Bash")
        #expect(ApprovalPresentation.headline(bash) == "npm install")
        var edit = bash
        edit.tool = "Edit"
        edit.input = .object(["file_path": .string("/tmp/a.swift"), "old_string": .string("x")])
        #expect(ApprovalPresentation.headline(edit) == "/tmp/a.swift")
        var plugin = bash
        plugin.kind = "plugin"
        plugin.plugin = "github"
        plugin.tool = "issue_write"
        #expect(ApprovalPresentation.toolTitle(plugin) == "github · issue_write")
        #expect(ApprovalPresentation.headline(plugin) == nil)
    }

    @Test func countdown() {
        let d = Fixture.t0.addingTimeInterval(600)
        #expect(ApprovalPresentation.countdown(deadline: d, now: Fixture.t0) == "10:00")
        #expect(ApprovalPresentation.countdown(deadline: d, now: Fixture.t0.addingTimeInterval(0.2)) == "10:00")
        #expect(ApprovalPresentation.countdown(deadline: d, now: Fixture.t0.addingTimeInterval(541)) == "0:59")
        #expect(ApprovalPresentation.countdown(deadline: d, now: Fixture.t0.addingTimeInterval(700)) == "0:00")
        #expect(ApprovalPresentation.countdown(deadline: nil, now: Fixture.t0) == nil)
    }

    @Test func shortcutsFollowFrontCard() {
        #expect(ApprovalShortcut.available(front: nil).isEmpty)
        #expect(ApprovalShortcut.available(front: Fixture.approval("a1")) == [.allow, .allowSession, .deny])
        #expect(ApprovalShortcut.available(front: Fixture.approval("a1", allowSession: false)) == [.allow, .deny])
        #expect(ApprovalShortcut.allow.hint == "⌃⌥Y")
        #expect(ApprovalShortcut.allowSession.answer == .allowSession)
    }

    @Test func socketPathFromEnvironment() {
        #expect(IPCClient.defaultSocketPath(environment: ["FARERO_SOCKET": "/tmp/x.sock"]) == "/tmp/x.sock")
        #expect(IPCClient.defaultSocketPath(environment: ["FARERO_HOME": "/tmp/fr"]) == "/tmp/fr/farerod.sock")
        #expect(IPCClient.defaultSocketPath(environment: [:]).hasSuffix("Library/Application Support/Farero/farerod.sock"))
    }
}

@Suite("Large inputs")
struct LargeInputTests {
    @Test func truncatesOnCharacterBoundary() {
        // "가" is 3 bytes in UTF-8.
        let s = String(repeating: "가", count: 10)
        let cut = ApprovalPresentation.truncated(s, maxBytes: 7)
        #expect(cut.text == "가가")
        #expect(cut.isTruncated)
        #expect(ApprovalPresentation.truncated("abc", maxBytes: 3) == ("abc", false))
    }

    @Test func inputDisplayCapsBigWrites() {
        var a = Fixture.approval("big")
        a.tool = "Write"
        a.input = .object(["file_path": .string("/tmp/x"), "content": .string(String(repeating: "줄\n", count: 200_000))])
        let d = ApprovalPresentation.inputDisplay(a, limit: 64 << 10)
        #expect(d.isTruncated)
        #expect(d.text.utf8.count <= 64 << 10)
        #expect(d.full.utf8.count == d.totalBytes)
        #expect(d.totalBytes > 800_000)
        #expect(d.full.hasPrefix(d.text))
        let small = ApprovalPresentation.inputDisplay(Fixture.approval("s"))
        #expect(!small.isTruncated && small.text == small.full)
    }

    @Test func headlineIsCapped() {
        var a = Fixture.approval("cmd")
        a.input = .object(["command": .string(String(repeating: "x", count: 5000))])
        #expect(ApprovalPresentation.headline(a)?.utf8.count == 1000)
    }
}
