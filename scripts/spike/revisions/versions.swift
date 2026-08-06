// Probes macOS Versions (.DocumentRevisions-V100) via NSFileVersion.
//
// Swift rather than Go because NSFileVersion is the only unprivileged way in.
// The store itself is d--x--x--x root:wheel with SIP enabled, so its sqlite
// index cannot be read directly without elevation — but NSFileVersion asks
// the system for versions of a file the caller already has access to, and
// needs no special permission at all.
//
// Result on this machine: neither Motion nor Photoshop creates versions, so
// this source does not reach the acorn/ creative work.
//
// That zero survived four escalating controls, because "the API is denying
// us" is the obvious competing explanation and a same-process control does
// not rule it out:
//
//   1. Same process creates two versions and reads them back — works.
//   2. A second process reads versions the first created — works, so this is
//      not an artifact of reading what we just wrote.
//   3. The same control run inside acorn/scanline/, alongside the .motn files
//      — works, so it is not the directory or the volume.
//   4. Scanning 654 documents under ~/Documents and ~/Desktop surfaces 15
//      versions in Numbers and TextEdit files — so versions created by other
//      applications are readable, which is the case that actually matters.
//
// Only after 4 does zero mean the versions were never created. Note that
// versioning is sparse even among Apple apps: just 7 of those 654 documents
// had any versions at all.
//
//   swift scripts/spike/revisions/versions.swift <file> [<file>...]
//   swift scripts/spike/revisions/versions.swift -control /tmp/probe.txt

import Foundation

let iso = ISO8601DateFormatter()
var args = Array(CommandLine.arguments.dropFirst())

// The control is load-bearing. Without it, "0 versions" is indistinguishable
// from an API that silently fails for this process or volume.
if args.first == "-control" {
    args.removeFirst()
    guard let path = args.first else {
        FileHandle.standardError.write("usage: -control <scratch-path>\n".data(using: .utf8)!)
        exit(2)
    }
    let f = URL(fileURLWithPath: path)
    do {
        try "one".write(to: f, atomically: true, encoding: .utf8)
        _ = try NSFileVersion.addOfItem(at: f, withContentsOf: f)
        try "two".write(to: f, atomically: true, encoding: .utf8)
        _ = try NSFileVersion.addOfItem(at: f, withContentsOf: f)
        let v = NSFileVersion.otherVersionsOfItem(at: f) ?? []
        print("control: \(v.count) versions readable — API works for this process and volume")
    } catch {
        print("control FAILED: \(error) — a zero count elsewhere proves nothing")
        exit(1)
    }
    exit(0)
}

guard !args.isEmpty else {
    FileHandle.standardError.write("usage: versions.swift [-control] <file>...\n".data(using: .utf8)!)
    exit(2)
}

var totalVersions = 0
var filesWithNone = 0

for path in args {
    let url = URL(fileURLWithPath: path)
    let versions = NSFileVersion.otherVersionsOfItem(at: url) ?? []
    totalVersions += versions.count
    if versions.isEmpty { filesWithNone += 1 }

    print("\(versions.count)\t\(url.lastPathComponent)")

    // Spacing is the interesting part: a fixed cadence means autosave and
    // tells us only that the document was open, while irregular spacing
    // tracks actual editing and can support duration inference.
    let dates = versions.compactMap(\.modificationDate).sorted()
    for (i, d) in dates.enumerated() {
        let delta = i > 0 ? String(format: " (+%.1fm)", d.timeIntervalSince(dates[i - 1]) / 60) : ""
        print("\t\(iso.string(from: d))\(delta)")
    }
}

print("\n\(args.count) files, \(totalVersions) versions, \(filesWithNone) with none")
