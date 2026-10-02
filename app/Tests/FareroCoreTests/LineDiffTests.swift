import Foundation
import Testing
@testable import FareroCore

@Suite("Line diff")
struct LineDiffTests {
    @Test func linesIgnoreTrailingNewline() {
        #expect(LineDiff.lines("") == [])
        #expect(LineDiff.lines("a\nb\n") == ["a", "b"])
        #expect(LineDiff.lines("a\nb") == ["a", "b"])
        #expect(LineDiff.lines("a\n\nb\n") == ["a", "", "b"])
    }

    @Test func identicalTextsHaveNoHunks() {
        let t = "{\n  \"a\": 1\n}\n"
        #expect(LineDiff.hunks(LineDiff.diff(LineDiff.lines(t), LineDiff.lines(t))).isEmpty)
        #expect(LineDiff.unified(before: t, after: t, path: "/x") == "")
        #expect(LineDiff.rows(before: t, after: t).isEmpty)
    }

    @Test func editScript() {
        let ops = LineDiff.diff(["a", "b", "c", "d"], ["a", "x", "c", "d", "e"])
        #expect(ops == [.same("a"), .removed("b"), .added("x"), .same("c"), .same("d"), .added("e")])
    }

    @Test func unifiedOutputMatchesDiffU() {
        let before = """
        {
          "model": "opus",
          "permissions": {
            "allow": [
              "Bash(ls)"
            ]
          }
        }

        """
        let after = """
        {
          "model": "opus",
          "permissions": {
            "allow": [
              "Bash(ls)",
              "mcp__farero__*"
            ]
          }
        }

        """
        let expected = """
        --- a/Users/me/.claude/settings.json
        +++ b/Users/me/.claude/settings.json
        @@ -2,7 +2,8 @@
           "model": "opus",
           "permissions": {
             "allow": [
        -      "Bash(ls)"
        +      "Bash(ls)",
        +      "mcp__farero__*"
             ]
           }
         }

        """
        #expect(LineDiff.unified(before: before, after: after, path: "/Users/me/.claude/settings.json") == expected)
    }

    @Test func newFileDiffsFromDevNull() {
        let u = LineDiff.unified(before: "", after: "{\n}\n", path: "/tmp/s.json")
        #expect(u == "--- /dev/null\n+++ b/tmp/s.json\n@@ -0,0 +1,2 @@\n+{\n+}\n")
    }

    @Test func distantChangesMakeSeparateHunks() {
        let a = (1...20).map { "line \($0)" }
        var b = a
        b[1] = "changed 2"
        b[17] = "changed 18"
        let hunks = LineDiff.hunks(LineDiff.diff(a, b), context: 3)
        #expect(hunks.count == 2)
        #expect(hunks[0].header == "@@ -1,5 +1,5 @@")
        #expect(hunks[1].header == "@@ -15,6 +15,6 @@")
        // Close changes merge into one hunk.
        var c = a
        c[5] = "x"
        c[9] = "y"
        #expect(LineDiff.hunks(LineDiff.diff(a, c), context: 3).count == 1)
    }

    @Test func rowsCarryLineNumbers() {
        let rows = LineDiff.rows(before: "a\nb\nc\n", after: "a\nB\nc\nd\n")
        #expect(rows.map(\.kind) == [.hunkHeader, .context, .removed, .added, .context, .added])
        #expect(rows[1].oldLine == 1 && rows[1].newLine == 1)
        #expect(rows[2].oldLine == 2 && rows[2].newLine == nil)
        #expect(rows[3].oldLine == nil && rows[3].newLine == 2)
        #expect(rows[5].newLine == 4)
        #expect(Set(rows.map(\.id)).count == rows.count)
    }

    @Test func hugeInputsFallBackToReplacement() {
        let a = (0..<2500).map { "a\($0)" }
        let b = (0..<2500).map { "b\($0)" }
        let ops = LineDiff.diff(a, b)
        #expect(ops.count == 5000)
        #expect(ops.first == .removed("a0"))
        #expect(ops.last == .added("b2499"))
    }
}

@Suite("PermissionRequest hook scan")
struct HookScanTests {
    @Test func findsOtherToolsHooks() {
        let settings = #"""
        {
          "hooks": {
            "PermissionRequest": [
              {"matcher": "*", "hooks": [
                {"type": "command", "command": "\"/Applications/Farero.app/Contents/MacOS/farero-hook\" --agent claude", "timeout": 660},
                {"type": "command", "command": "/usr/local/bin/other-notifier --approve"}
              ]},
              {"matcher": "Bash", "hooks": [{"type": "http", "url": "http://localhost:9000/hook"}]}
            ],
            "PreToolUse": [{"matcher": "*", "hooks": [{"type": "command", "command": "/usr/local/bin/logger"}]}]
          }
        }
        """#
        #expect(HookScan.foreignPermissionHooks(settingsJSON: settings) == [
            "/usr/local/bin/other-notifier --approve", "http://localhost:9000/hook",
        ])
    }

    @Test func faroOnlyOrMissingIsClean() {
        #expect(HookScan.foreignPermissionHooks(settingsJSON: "").isEmpty)
        #expect(HookScan.foreignPermissionHooks(settingsJSON: "not json").isEmpty)
        #expect(HookScan.foreignPermissionHooks(settingsJSON: #"{"model":"opus"}"#).isEmpty)
        #expect(HookScan.foreignPermissionHooks(settingsJSON: #"{"hooks":{"PermissionRequest":[{"hooks":[{"type":"command","command":"/x/farero-hook --agent claude"}]}]}}"#).isEmpty)
        // Other events do not count.
        #expect(HookScan.foreignPermissionHooks(settingsJSON: #"{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"say done"}]}]}}"#).isEmpty)
    }
}
