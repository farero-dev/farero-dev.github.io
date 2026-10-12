// What scripts/e2e-notch.py cannot see from Python: a process's windows and
// which app is frontmost. Compiled by the script (swiftc) into the dev binary
// folder; it reads only, and needs no permission.
//
//   notchprobe windows <pid>   [{"x","y","w","h","layer","alpha","onscreen"}]
//                              on-screen windows, global top-left coordinates
//   notchprobe front <pid>     {"pid","name","bundle","target_active"}: the
//                              frontmost app, and whether <pid> is active
import AppKit
import CoreGraphics
import Foundation

func emit(_ value: Any) {
    let data = try! JSONSerialization.data(withJSONObject: value, options: [.sortedKeys])
    FileHandle.standardOutput.write(data)
    FileHandle.standardOutput.write(Data("\n".utf8))
}

let args = CommandLine.arguments
guard args.count == 3, let pid = Int32(args[2]) else {
    FileHandle.standardError.write(Data("usage: notchprobe windows|front <pid>\n".utf8))
    exit(2)
}

switch args[1] {
case "windows":
    let list = CGWindowListCopyWindowInfo([.optionOnScreenOnly], kCGNullWindowID) as? [[String: Any]] ?? []
    var out: [[String: Any]] = []
    for w in list where (w[kCGWindowOwnerPID as String] as? Int32) == pid {
        let b = w[kCGWindowBounds as String] as? [String: Any] ?? [:]
        out.append([
            "x": b["X"] ?? 0, "y": b["Y"] ?? 0, "w": b["Width"] ?? 0, "h": b["Height"] ?? 0,
            "layer": w[kCGWindowLayer as String] ?? 0, "alpha": w[kCGWindowAlpha as String] ?? 1,
            "onscreen": w[kCGWindowIsOnscreen as String] ?? false,
        ])
    }
    emit(out)
case "front":
    let front = NSWorkspace.shared.frontmostApplication
    emit([
        "pid": front?.processIdentifier ?? 0, "name": front?.localizedName ?? "",
        "bundle": front?.bundleIdentifier ?? "",
        "target_active": NSRunningApplication(processIdentifier: pid)?.isActive ?? false,
    ])
default:
    FileHandle.standardError.write(Data("usage: notchprobe windows|front <pid>\n".utf8))
    exit(2)
}
