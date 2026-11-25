// Package filedays scans a directory tree and counts files by the local date
// of their last modification.
//
// # Why this exists apart from a count
//
// A file records only its LAST modification. Work done today on a file last
// touched on 11 July does not add evidence for 11 July — it MOVES it,
// deleting 11 July from the record and crediting today instead. Measured: a
// scan of the acorn repository found 1,773 files modified on 2026-07-11; the
// same scan hours later found 513. Both numbers were true when taken. Neither
// means anything without the moment it was taken alongside it, which is why
// Take exists and a bare count is not the API this package exports.
//
// This is worse than a source that prunes on a schedule, because a pruned
// window has a visible edge. Mtime decay has none: the count simply gets
// smaller and stays plausible, so nothing afterwards can tell "little work
// happened that day" apart from "those files were touched again". Absence of
// mtime evidence is not evidence of absence.
//
// See project/private/2026-07-28-source-corrections.md §4 for the full measurement,
// and docs/content/docs/sources.md for how this is described to a manifest
// author under `timestamp.from: "mtime"`.
//
// # Two ways this used to invent a count
//
// Both were measured on 2026-08-01, and both are worse than the silence this
// project has already shipped: they produced a plausible number rather than a
// suspicious zero.
//
// A root that IS a symlink to a directory was counted as one file and never
// descended into. filepath.WalkDir calls os.Lstat on its own root argument and
// does not follow a symlink there, so the callback saw the link itself, called
// it a file, and stopped: a tree of three files scanned as 3 by its real path
// and as 1 through a symlink to it, filed under the day the LINK was created.
// Scan resolves that one case now (see walkableRoot) and feeds the resolved
// form to filepath.WalkDir alone.
//
// Separately, and about entries met DURING a walk rather than about the root,
// every non-directory entry was counted. A symlink's mtime says when the
// pointer was made; a socket's says when a program started. Neither records
// work anybody did. Scan counts regular files only.
//
// The corollary is that a root which cannot be walked is now an error naming
// itself rather than a count. An empty directory still returns an empty map
// and no error — "nothing was written here" is a real, correct observation,
// and it must not look like "this root was never read".
package filedays

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultExcludes are directories that duplicate or generate content rather
// than holding work someone did, skipped whole rather than filtered
// afterwards: descending into twenty-one worktree copies of one tree is
// wasteful, and a path-substring filter would wrongly discard a directory
// that merely shares a prefix with an excluded name.
// `.venv` is the Python form of node_modules and is here for the same reason
// plus a measurement: on 2026-08-01 it accounted for 21.2% of the live store's
// attributed filesystem observations. The base names are matched WHOLE, which
// is why `env` and `.venv-old` survive an exclude of `.venv` — see
// TestDefaultExcludesSkipVirtualenvs, and scripts/spike/env in this repository,
// which a looser rule would delete. `.build` is Swift Package Manager's output,
// `.derived` is a directory Xcode is told to keep derived data in, and
// `__pycache__` and the three caches after it are Python's.
var DefaultExcludes = []string{
	".git",
	"worktrees",
	"node_modules",
	".venv",
	".build",
	".derived",
	"__pycache__",
	".mypy_cache",
	".pytest_cache",
	".ruff_cache",
	".Trash",
	"Autosave Vault",
}

// Scan counts files under root by the local date of their modification time.
//
// excludes names directories, by base name, to skip entirely. A nil or empty
// slice performs the unfiltered ("raw") scan; comparing it against a filtered
// one is how the inflation from copies (worktrees, node_modules) is measured
// rather than argued about.
//
// Only REGULAR files are counted. A symlink, a socket, a fifo or a device node
// has an mtime, but it records when the entry itself was made, not work
// somebody did — counting one invents evidence for a day nothing happened on.
// See the package doc's "Two ways this used to invent a count".
//
// An existing, readable, empty directory returns an empty map and no error: it
// is an ordinary, correct zero. Every way a root can fail to be walked —
// missing, a symlink whose target is gone, a symlink to a file, a plain file,
// a directory that refuses to be read — is an error naming the root instead,
// so nothing downstream can mistake it for a day on which no work happened.
func Scan(root string, excludes []string) (map[string]int, error) {
	walkRoot, err := walkableRoot(root)
	if err != nil {
		return nil, err
	}

	skip := make(map[string]bool, len(excludes))
	for _, e := range excludes {
		if e = strings.TrimSpace(e); e != "" {
			skip[e] = true
		}
	}

	days := make(map[string]int)
	err = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == walkRoot {
				// The root itself refusing to be read is not one subtree
				// lost; it is the entire scan producing a zero that looks
				// exactly like a quiet day.
				return fmt.Errorf("cannot read %s: %w", root, err)
			}
			// An unreadable subdirectory should cost that subtree, not the
			// whole scan.
			return nil //nolint:nilerr // deliberate: keep walking
		}
		if d.IsDir() {
			if path != walkRoot && skip[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		days[info.ModTime().Local().Format(time.DateOnly)]++
		return nil
	})
	if err != nil {
		// The caller's own root string, never the resolved one: it is the
		// path a roots file or a -filedays-roots flag actually names.
		return nil, fmt.Errorf("scan %s: %w", root, err)
	}
	return days, nil
}

// walkableRoot returns the path to hand filepath.WalkDir for root.
//
// filepath.WalkDir calls os.Lstat on its own root argument and does not follow
// a symlink there, so a root that IS a symlink to a directory used to arrive at
// the callback as a single non-directory entry: it was COUNTED, and the walk
// descended nowhere. Measured 2026-08-01 — a tree of 3 files scanned as 3 by
// its real path and as 1 through a symlink to it, dated to the day the link
// was made. That is the worst shape this defect takes anywhere in this
// codebase: a zero might be noticed, and a plausible 1 will not.
//
// The resolved form is used for reading and for nothing else. Nothing here
// produces a Locator — a filedays snapshot yields no events at all — but
// RootScan.Root plays the same part, since it is the only thing in a snapshot
// saying which configured root a day count belongs to, and it stays the
// caller's own string. On macOS /var resolves to /private/var, so recording
// what was walked would rewrite the root of any snapshot taken under a
// temporary directory and stop two observations of the same configured root
// from lining up. See TestTakeRecordsTheConfiguredRootNotTheResolvedOne.
func walkableRoot(root string) (string, error) {
	// os.Stat, unlike the os.Lstat filepath.WalkDir performs, follows a
	// symlinked root through to whatever it points at.
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("cannot scan %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cannot scan %s: not a directory", root)
	}

	link, err := os.Lstat(root)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		return root, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("cannot scan %s: it is a symlink that could not be resolved: %w", root, err)
	}
	return resolved, nil
}

// RootScan is one configured root's counts from a single observation.
type RootScan struct {
	// Root is the absolute directory that was scanned.
	Root string `json:"root"`
	// Raw is the unfiltered count, by day. Present alongside Filtered so the
	// inflation from copies is visible in the record itself rather than
	// requiring a second run to see it.
	Raw map[string]int `json:"raw"`
	// Filtered excludes the directories named in the snapshot's Excludes.
	Filtered map[string]int `json:"filtered"`
}

// Snapshot is one observation of every configured root, taken at one instant.
//
// It is the whole reason this package exists rather than being a thinner
// wrapper around Scan: a count with no ObservedAt is meaningless, because the
// same root scanned hours apart can legitimately report very different
// numbers for the same past day.
type Snapshot struct {
	// ObservedAt is when this scan was taken. This is the field that makes a
	// count mean something: two Snapshots for the same root, taken hours
	// apart, can disagree about the same past day, and only this says which
	// one is newer.
	ObservedAt time.Time `json:"observed_at"`
	// Excludes were applied to produce every root's Filtered map.
	Excludes []string `json:"excludes"`
	// Roots holds one entry per configured root.
	Roots []RootScan `json:"roots"`
}

// Result describes what one Take call wrote.
type Result struct {
	// Path is the snapshot file that was written.
	Path string
	// Snapshot is what it holds.
	Snapshot Snapshot
}

// Take scans every root and writes the result, with its observation time, to
// a new file under dir.
//
// It returns (nil, nil) when roots is empty, the same tolerance TakeStreams
// applies to a Biome stream nobody has: nothing configured is the ordinary
// state for a machine where filedays capture has not been set up, not a
// failure, and it must not litter the snapshot directory with an empty file.
//
// A failing root does not lose the others: partial evidence preserved is
// better than none, which is the same partial-failure posture the harvest
// command applies to its sources.
func Take(roots []string, excludes []string, dir string, now time.Time) (*Result, error) {
	if len(roots) == 0 {
		return nil, nil
	}

	snap := Snapshot{
		ObservedAt: now,
		Excludes:   excludes,
		Roots:      make([]RootScan, 0, len(roots)),
	}
	var errs []error
	for _, root := range roots {
		raw, err := Scan(root, nil)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		filtered, err := Scan(root, excludes)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		snap.Roots = append(snap.Roots, RootScan{Root: root, Raw: raw, Filtered: filtered})
	}
	if len(snap.Roots) == 0 {
		return nil, errors.Join(errs...)
	}

	if err := os.MkdirAll(Dir(dir), 0o700); err != nil {
		return nil, errors.Join(append(errs, fmt.Errorf("create filedays snapshot directory %s: %w", Dir(dir), err))...)
	}
	path, err := writeSnapshot(Dir(dir), now, snap)
	if err != nil {
		return nil, errors.Join(append(errs, err)...)
	}
	return &Result{Path: path, Snapshot: snap}, errors.Join(errs...)
}

// Dir is where filedays snapshots live under a harvest's snapshot directory,
// mirroring how snapshot.BiomeDir sits beside the knowledgeC captures: one
// path under -dir is the whole of what a harvest has preserved.
func Dir(dir string) string { return filepath.Join(dir, "filedays") }

// writeSnapshot marshals snap and writes it under a name unique to this
// instant, adding a counter if the timestamped name is already taken —
// O_EXCL makes this safe against a concurrent harvest rather than merely
// unlikely to collide. A snapshot is never overwritten: the whole point of
// recording ObservedAt is that two observations of the same root can
// legitimately disagree, and overwriting one would destroy the earlier
// moment's evidence instead of merely superseding it.
func writeSnapshot(dir string, now time.Time, snap Snapshot) (string, error) {
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode filedays snapshot: %w", err)
	}

	base := filepath.Join(dir, "filedays-"+now.UTC().Format("20060102T150405"))
	for n := range 1000 {
		path := base + ".json"
		if n > 0 {
			path = base + "-" + strconv.Itoa(n) + ".json"
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			if _, err := f.Write(data); err != nil {
				f.Close()
				return "", fmt.Errorf("write filedays snapshot %s: %w", path, err)
			}
			return path, f.Close()
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("create filedays snapshot %s: %w", path, err)
		}
	}
	return "", fmt.Errorf("cannot find a free filedays snapshot name in %s", dir)
}

// ReadRootsFile reads which roots to scan from a plain text configuration
// file, one absolute or ~-prefixed path per line. Blank lines and lines
// starting with # are ignored.
//
// A plain text file rather than config.toml, deliberately: no TOML parser is
// in this module's dependencies, adding one for a list of paths is not
// justified, and this format needs nothing beyond the standard library.
//
// A missing file returns (nil, nil) rather than an error — the same
// tolerance a missing manifest directory gets — because "filedays capture has
// not been configured on this machine" is the ordinary state, not a failure.
func ReadRootsFile(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read filedays roots %s: %w", path, err)
	}

	var roots []string
	for line := range strings.SplitSeq(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		expanded, err := ExpandHome(line)
		if err != nil {
			return nil, fmt.Errorf("%s: expand %q: %w", path, line, err)
		}
		roots = append(roots, expanded)
	}
	return roots, nil
}

// ExpandHome resolves a leading ~ the way manifests do, so a roots file or a
// -filedays-roots flag is shareable rather than hardcoding whoever wrote it.
// It is intentionally its own small implementation rather than importing
// internal/manifest, which validates and compiles a great deal more than a
// bare path needs and is not otherwise a dependency of this package.
//
// Exported so cmd/timetattle can apply the same expansion to roots given on
// the command line, rather than the flag and the config file disagreeing
// about what ~ means.
func ExpandHome(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

// DefaultRootsFile is where a machine's filedays roots are configured, beside
// the manifests directory and config.toml.
func DefaultRootsFile() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".timetattle", "filedays-roots.txt"), nil
}

// sortedDays is a small helper for callers presenting a scan, kept here so
// the CLI and the harvester show days in the same order.
func sortedDays(counts map[string]int) []string {
	days := make([]string, 0, len(counts))
	for d := range counts {
		days = append(days, d)
	}
	sort.Strings(days)
	return days
}

// SortedDays returns the days a scan touched, oldest first.
func SortedDays(counts map[string]int) []string { return sortedDays(counts) }
