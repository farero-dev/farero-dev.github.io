import Foundation

/// A line diff of two texts, for showing what the Claude Code installer will
/// change in settings.json (F-06, coucou 방식: show before/after and confirm).
public enum LineDiff {
    public enum Op: Sendable, Equatable {
        case same(String)
        case removed(String)
        case added(String)
    }

    /// A unified-diff hunk. Starts are 1-based; a zero count starts at the
    /// line before the hunk (as in `diff -u`).
    public struct Hunk: Sendable, Equatable {
        public var oldStart: Int
        public var oldCount: Int
        public var newStart: Int
        public var newCount: Int
        public var ops: [Op]

        public var header: String { "@@ -\(oldStart),\(oldCount) +\(newStart),\(newCount) @@" }
    }

    /// One displayable row.
    public struct Row: Sendable, Equatable, Identifiable {
        public enum Kind: Sendable, Equatable { case hunkHeader, context, removed, added }
        public var id: Int
        public var kind: Kind
        public var text: String
        public var oldLine: Int?
        public var newLine: Int?
    }

    /// Splits text into lines. A trailing newline does not add an empty line.
    public static func lines(_ s: String) -> [String] {
        if s.isEmpty { return [] }
        var parts = s.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        if parts.last == "" { parts.removeLast() }
        return parts
    }

    /// Above this many cells the LCS table is skipped and the whole file is
    /// shown as replaced (settings files are far smaller).
    static let maxCells = 4_000_000

    /// The edit script turning `a` into `b`: a longest-common-subsequence
    /// diff with the common prefix and suffix trimmed first.
    public static func diff(_ a: [String], _ b: [String]) -> [Op] {
        var prefix = 0
        while prefix < a.count, prefix < b.count, a[prefix] == b[prefix] { prefix += 1 }
        var suffix = 0
        while suffix < a.count - prefix, suffix < b.count - prefix,
              a[a.count - 1 - suffix] == b[b.count - 1 - suffix] { suffix += 1 }
        let a1 = Array(a[prefix..<(a.count - suffix)])
        let b1 = Array(b[prefix..<(b.count - suffix)])

        var ops: [Op] = a[..<prefix].map { .same($0) }
        let n = a1.count, m = b1.count
        if n == 0 {
            ops += b1.map { .added($0) }
        } else if m == 0 {
            ops += a1.map { .removed($0) }
        } else if (n + 1) * (m + 1) > maxCells {
            ops += a1.map { .removed($0) }
            ops += b1.map { .added($0) }
        } else {
            // lcs[i][j]: LCS length of a1[i...] and b1[j...].
            let w = m + 1
            var lcs = [Int32](repeating: 0, count: (n + 1) * w)
            for i in stride(from: n - 1, through: 0, by: -1) {
                for j in stride(from: m - 1, through: 0, by: -1) {
                    lcs[i * w + j] = a1[i] == b1[j]
                        ? lcs[(i + 1) * w + j + 1] + 1
                        : max(lcs[(i + 1) * w + j], lcs[i * w + j + 1])
                }
            }
            var i = 0, j = 0
            while i < n, j < m {
                if a1[i] == b1[j] {
                    ops.append(.same(a1[i])); i += 1; j += 1
                } else if lcs[(i + 1) * w + j] >= lcs[i * w + j + 1] {
                    ops.append(.removed(a1[i])); i += 1
                } else {
                    ops.append(.added(b1[j])); j += 1
                }
            }
            while i < n { ops.append(.removed(a1[i])); i += 1 }
            while j < m { ops.append(.added(b1[j])); j += 1 }
        }
        ops += a[(a.count - suffix)...].map { .same($0) }
        return ops
    }

    /// Groups changes into hunks with `context` unchanged lines around them.
    public static func hunks(_ ops: [Op], context: Int = 3) -> [Hunk] {
        let changed = ops.indices.filter { if case .same = ops[$0] { false } else { true } }
        guard !changed.isEmpty else { return [] }
        // Merge change positions whose context windows touch.
        var ranges: [ClosedRange<Int>] = []
        for idx in changed {
            let lo = max(0, idx - context), hi = min(ops.count - 1, idx + context)
            if let last = ranges.last, lo <= last.upperBound + 1 {
                ranges[ranges.count - 1] = last.lowerBound...max(last.upperBound, hi)
            } else {
                ranges.append(lo...hi)
            }
        }
        // Line numbers before each op.
        var oldBefore = [Int](repeating: 0, count: ops.count + 1)
        var newBefore = [Int](repeating: 0, count: ops.count + 1)
        for (k, op) in ops.enumerated() {
            oldBefore[k + 1] = oldBefore[k] + (op.isAdded ? 0 : 1)
            newBefore[k + 1] = newBefore[k] + (op.isRemoved ? 0 : 1)
        }
        return ranges.map { r in
            let slice = Array(ops[r])
            let oldCount = slice.filter { !$0.isAdded }.count
            let newCount = slice.filter { !$0.isRemoved }.count
            let oldStart = oldCount == 0 ? oldBefore[r.lowerBound] : oldBefore[r.lowerBound] + 1
            let newStart = newCount == 0 ? newBefore[r.lowerBound] : newBefore[r.lowerBound] + 1
            return Hunk(oldStart: oldStart, oldCount: oldCount, newStart: newStart, newCount: newCount, ops: slice)
        }
    }

    /// `diff -u` style text.
    public static func unified(before: String, after: String, path: String, context: Int = 3) -> String {
        let hs = hunks(diff(lines(before), lines(after)), context: context)
        guard !hs.isEmpty else { return "" }
        var out = before.isEmpty ? "--- /dev/null\n" : "--- a\(path.hasPrefix("/") ? "" : "/")\(path)\n"
        out += "+++ b\(path.hasPrefix("/") ? "" : "/")\(path)\n"
        for h in hs {
            out += h.header + "\n"
            for op in h.ops {
                switch op {
                case .same(let s): out += " " + s + "\n"
                case .removed(let s): out += "-" + s + "\n"
                case .added(let s): out += "+" + s + "\n"
                }
            }
        }
        return out
    }

    /// Rows for display, with line numbers.
    public static func rows(before: String, after: String, context: Int = 3) -> [Row] {
        var rows: [Row] = []
        for h in hunks(diff(lines(before), lines(after)), context: context) {
            rows.append(Row(id: rows.count, kind: .hunkHeader, text: h.header))
            // A side with a zero count numbers no rows, so its start is unused.
            var old = h.oldStart, new = h.newStart
            for op in h.ops {
                switch op {
                case .same(let s):
                    rows.append(Row(id: rows.count, kind: .context, text: s, oldLine: old, newLine: new))
                    old += 1; new += 1
                case .removed(let s):
                    rows.append(Row(id: rows.count, kind: .removed, text: s, oldLine: old))
                    old += 1
                case .added(let s):
                    rows.append(Row(id: rows.count, kind: .added, text: s, newLine: new))
                    new += 1
                }
            }
        }
        return rows
    }
}

extension LineDiff.Op {
    var isAdded: Bool { if case .added = self { true } else { false } }
    var isRemoved: Bool { if case .removed = self { true } else { false } }
}

/// Finds other tools' `PermissionRequest` hooks in a Claude Code
/// settings.json. Their answer can override farero's (M0), so the installer
/// warns about them.
public enum HookScan {
    public static let warning = "다른 앱의 PermissionRequest 훅이 있으면 그 앱의 응답이 farero보다 먼저 적용될 수 있습니다"

    /// The command (or URL) of every `PermissionRequest` hook that is not
    /// farero-hook. Invalid or missing JSON yields none.
    public static func foreignPermissionHooks(settingsJSON: String) -> [String] {
        guard let data = settingsJSON.data(using: .utf8), !data.isEmpty,
              let root = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let hooks = root["hooks"] as? [String: Any],
              let entries = hooks["PermissionRequest"] as? [[String: Any]]
        else { return [] }
        var found: [String] = []
        for entry in entries {
            for hook in entry["hooks"] as? [[String: Any]] ?? [] {
                let target = (hook["command"] as? String) ?? (hook["url"] as? String) ?? ""
                if target.isEmpty || target.contains("farero-hook") { continue }
                found.append(target)
            }
        }
        return found
    }
}
