// Package fsfixture materialises a filesystem tree with exact birth and
// modification times, from a manifest checked into a package's testdata.
//
// # Why a manifest rather than a checked-in tree
//
// internal/fsobserve reads two timestamps per file, and the one that carries
// most of its value is st_birthtime — the instant APFS says the file was
// created. Git stores neither. A tree committed to the repository arrives on
// every machine born at checkout time and modified at checkout time, which is
// precisely the one shape that proves nothing: birth and mtime coincide, every
// file yields a single event, and every assertion about decay, about a 658ms
// gap across a second boundary, or about a file whose morning survives only in
// its birthtime, becomes untestable.
//
// So the fixture is the manifest, and the tree is built from it into a
// temporary directory at test time. What is committed is small, readable, and
// diffable; what is walked carries the timestamps the manifest names, to the
// nanosecond.
//
// # Why the times are verified rather than assumed
//
// macOS does not simply store what it is told. Setting a modification time
// EARLIER than a file's creation date lowers the creation date to match — which
// is exactly what backdating a file created a moment ago does — so the order of
// the two syscalls decides whether the birth time survives, and getting it
// wrong produces a tree that looks fine and silently disagrees with its own
// manifest. Build therefore reads every timestamp back and refuses to return a
// tree that does not match, because a fixture that quietly differs from its
// manifest would make the tests resting on it assert something nobody wrote
// down.
//
// # Away from macOS
//
// Only Darwin can set a creation timestamp at all, and internal/fsobserve
// deliberately reports no birth time anywhere else rather than substituting
// mtime. Build reports ErrNoBirthtime instead of building a tree that would
// silently be missing half of every file's evidence; callers skip on it, naming
// the missing kernel feature rather than concluding anything about the fixture.
package fsfixture

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrNoBirthtime reports that this platform cannot set a file's creation
// timestamp, so a tree built here could not carry the evidence the manifest
// describes. It is a missing precondition, not a verdict on the fixture.
var ErrNoBirthtime = errors.New("fsfixture: this platform cannot set a file's birth time")

// TimeLayout is how a manifest writes an instant. The fractional part is
// optional; it exists because whether two stamps fall in the same Unix second
// is the distinction internal/fsobserve turns one event into two over.
const TimeLayout = "2006-01-02 15:04:05.999999999"

// Entry is one file in a fixture tree.
type Entry struct {
	// Path is slash-separated and relative to the tree root.
	Path string
	// Birth is what st_birthtime will report.
	Birth time.Time
	// Mtime is what st_mtime will report. A manifest that omits it means the
	// file was written once and never touched again, and it equals Birth.
	Mtime time.Time
	// Content is what the file holds. Fixture corpora here care about
	// timestamps and names rather than bytes, so it is normally empty and the
	// file is created with zero length.
	Content string
}

// Load reads a manifest.
//
// Each non-blank, non-comment line is a path, a birth time and an optional
// modification time, separated by '|' and surrounded by any amount of
// whitespace:
//
//	render/beacon.blend                  | 2026-03-04 09:19:26
//	studio/beacon.motn | 2026-03-04 06:00:14.913427472 | 2026-03-04 06:50:34.742495669
//
// Times are read in the machine's own zone rather than in UTC, because every
// assertion resting on them is about a wall clock — a working morning, a save
// at 09:19 — and a fixture that shifted with the reader's zone would make the
// same test assert different things in different places.
func Load(path string) ([]Entry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("fsfixture: open manifest: %w", err)
	}
	defer f.Close()

	var entries []Entry
	scan := bufio.NewScanner(f)
	for line := 1; scan.Scan(); line++ {
		text := strings.TrimSpace(scan.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		entry, err := parseLine(text)
		if err != nil {
			return nil, fmt.Errorf("fsfixture: %s line %d: %w", path, line, err)
		}
		entries = append(entries, entry)
	}
	if err := scan.Err(); err != nil {
		return nil, fmt.Errorf("fsfixture: read manifest %s: %w", path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("fsfixture: %s describes no files", path)
	}
	return entries, nil
}

func parseLine(text string) (Entry, error) {
	fields := strings.Split(text, "|")
	for i := range fields {
		fields[i] = strings.TrimSpace(fields[i])
	}
	if len(fields) < 2 || len(fields) > 3 {
		return Entry{}, fmt.Errorf("want 'path | birth' or 'path | birth | mtime', got %d field(s)", len(fields))
	}
	if fields[0] == "" {
		return Entry{}, errors.New("empty path")
	}
	birth, err := time.ParseInLocation(TimeLayout, fields[1], time.Local)
	if err != nil {
		return Entry{}, fmt.Errorf("birth time %q: %w", fields[1], err)
	}
	entry := Entry{Path: fields[0], Birth: birth, Mtime: birth}
	if len(fields) == 3 && fields[2] != "" {
		mtime, err := time.ParseInLocation(TimeLayout, fields[2], time.Local)
		if err != nil {
			return Entry{}, fmt.Errorf("modification time %q: %w", fields[2], err)
		}
		entry.Mtime = mtime
	}
	return entry, nil
}

// Materialize loads a manifest and builds it under root, which is normally a
// t.TempDir().
func Materialize(root, manifest string) error {
	entries, err := Load(manifest)
	if err != nil {
		return err
	}
	return Build(root, entries)
}

// Build creates every entry under root with the timestamps it names, and
// verifies each one afterwards.
//
// It reports ErrNoBirthtime, having created nothing, where the platform cannot
// set a creation timestamp.
func Build(root string, entries []Entry) error {
	if !CanSetBirthtime {
		return ErrNoBirthtime
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("fsfixture: resolve root %s: %w", root, err)
	}
	for _, e := range entries {
		path, err := resolveWithin(abs, e.Path)
		if err != nil {
			return err
		}
		if err := writeEntry(path, e); err != nil {
			return err
		}
	}
	return nil
}

// resolveWithin places a manifest path under root, refusing one that climbs
// out. A manifest is data, and a loader that will write wherever its data
// points is a loader that can be handed a path with a ".." in it.
func resolveWithin(root, rel string) (string, error) {
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("fsfixture: %s is an absolute path; manifest paths are relative to the tree root", rel)
	}
	path := filepath.Join(root, filepath.FromSlash(rel))
	if path != root && !strings.HasPrefix(path, root+string(filepath.Separator)) {
		return "", fmt.Errorf("fsfixture: %s leaves the tree root", rel)
	}
	return path, nil
}

func writeEntry(path string, e Entry) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("fsfixture: create directory for %s: %w", e.Path, err)
	}
	if err := os.WriteFile(path, []byte(e.Content), 0o644); err != nil {
		return fmt.Errorf("fsfixture: write %s: %w", e.Path, err)
	}

	mtime := e.Mtime
	if mtime.IsZero() {
		mtime = e.Birth
	}
	// The modification time goes first and the birth time second, and the
	// order is load-bearing: macOS lowers a file's creation date to the
	// modification time whenever the latter is set earlier, which every
	// backdated fixture file does. Setting the birth time afterwards is what
	// survives that; setting it first would silently be undone.
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		return fmt.Errorf("fsfixture: set times on %s: %w", e.Path, err)
	}
	if err := setBirthtime(path, e.Birth); err != nil {
		return fmt.Errorf("fsfixture: set birth time on %s: %w", e.Path, err)
	}
	return verify(path, e.Path, e.Birth, mtime)
}

// verify reads back what was just written. See the package doc: a tree that
// silently disagrees with its manifest would make every test resting on it
// assert something nobody wrote down.
func verify(path, name string, birth, mtime time.Time) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("fsfixture: stat %s: %w", name, err)
	}
	if got := info.ModTime(); !got.Equal(mtime) {
		return fmt.Errorf("fsfixture: %s has mtime %s, want %s; this filesystem did not store what it was given",
			name, got.Format(time.RFC3339Nano), mtime.Format(time.RFC3339Nano))
	}
	got, err := Birthtime(path)
	if err != nil {
		return fmt.Errorf("fsfixture: read birth time of %s: %w", name, err)
	}
	if !got.Equal(birth) {
		return fmt.Errorf("fsfixture: %s was born %s, want %s; this filesystem did not store what it was given",
			name, got.Format(time.RFC3339Nano), birth.Format(time.RFC3339Nano))
	}
	return nil
}
