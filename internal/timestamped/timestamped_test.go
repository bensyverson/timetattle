package timestamped_test

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timestamped"
)

// load writes a manifest and returns its sources, so the tests exercise the
// same path a real run takes rather than hand-building structs whose compiled
// patterns the loader is responsible for filling in.
func load(t *testing.T, body string) []manifest.Source {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sources.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	return m.Sources
}

// only returns the single source a fixture manifest declares.
func only(t *testing.T, body string) manifest.Source {
	t.Helper()
	sources := load(t, body)
	if len(sources) != 1 {
		t.Fatalf("manifest yielded %d sources, want 1", len(sources))
	}
	return sources[0]
}

// motionManifest describes the real Motion vault, rooted wherever the test
// put its fixtures. The directory names contain spaces because the real ones
// do — "Motion Projects/Autosave Vault" — and that has broken naive readers.
func motionManifest(root string) string {
	return fmt.Sprintf(`{
  "manifest_version": 1,
  "sources": [
    {
      "id": "motion-autosave",
      "type": "timestamped-files",
      "emits": "point",
      "app": "com.apple.motionapp",
      "perishable": true,
      "cause": "timer",
      "files": { "root": %q, "match": "*/*.motn" },
      "timestamp": { "from": "filename", "pattern": "_(\\d{4}-\\d{2}-\\d{2}_\\d{4})\\.motn$", "layout": "2006-01-02_1504" },
      "project": { "from": "path", "pattern": "Autosave Vault/([^/]+)/" }
    }
  ]
}`, root)
}

// blenderManifest reads the time from the filesystem, as .blend1 backups
// carry no timestamp in their names.
func blenderManifest(root, from string) string {
	return fmt.Sprintf(`{
  "manifest_version": 1,
  "sources": [
    {
      "id": "blender-backups",
      "type": "timestamped-files",
      "emits": "point",
      "app": "org.blenderfoundation.blender",
      "cause": "user-action",
      "files": { "root": %q, "match": "*.blend1" },
      "timestamp": { "from": %q }
    }
  ]
}`, root, from)
}

// vault builds a Motion-shaped tree and returns the "Autosave Vault"
// directory, which is what a manifest roots at.
func vault(t *testing.T, files map[string][]string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "Motion Projects", "Autosave Vault")
	for project, names := range files {
		dir := filepath.Join(root, project)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		for _, name := range names {
			if err := os.WriteFile(filepath.Join(dir, name), []byte("motn"), 0o644); err != nil {
				t.Fatalf("write fixture: %v", err)
			}
		}
	}
	return root
}

func collect(t *testing.T, r *timestamped.Reader, since time.Time) []source.Event {
	t.Helper()
	events, err := r.Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	return events
}

func newReader(t *testing.T, spec manifest.Source) *timestamped.Reader {
	t.Helper()
	r, err := timestamped.New(spec)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	return r
}

// capture redirects a reader's warnings so a test can assert that a tolerated
// problem was reported rather than swallowed.
func capture(r *timestamped.Reader) *bytes.Buffer {
	var buf bytes.Buffer
	r.Log = slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	return &buf
}

func TestReaderIdentifiesItself(t *testing.T) {
	r := newReader(t, only(t, motionManifest(t.TempDir())))
	if r.ID() != "motion-autosave" {
		t.Errorf("ID() = %q, want motion-autosave", r.ID())
	}
	if r.Kind() != source.KindTimestampedFiles {
		t.Errorf("Kind() = %q, want %q", r.Kind(), source.KindTimestampedFiles)
	}
	var _ source.Source = r
}

// The timestamp Motion writes into the name is local wall-clock time. Reading
// it as UTC would shift every creative session by the offset.
func TestFilenameTimestampsAreReadAsLocalTime(t *testing.T) {
	root := vault(t, map[string][]string{
		"scanline2-card-granted": {"scanline2-card-granted_2026-07-28_0632.motn"},
	})
	events := collect(t, newReader(t, only(t, motionManifest(root))), time.Time{})

	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	e := events[0]
	want := time.Date(2026, 7, 28, 6, 32, 0, 0, time.Local)
	if !e.Start.Equal(want) {
		t.Errorf("Start = %s, want %s", e.Start, want)
	}
	if e.Shape != source.Point {
		t.Errorf("Shape = %q, want %q", e.Shape, source.Point)
	}
	if !e.End.Equal(e.Start) {
		t.Errorf("End = %s, want it equal to Start for a point", e.End)
	}
	if e.Source != "motion-autosave" {
		t.Errorf("Source = %q", e.Source)
	}
	if e.Identity.App != "com.apple.motionapp" {
		t.Errorf("App = %q", e.Identity.App)
	}
	if e.Identity.Document != "scanline2-card-granted" {
		t.Errorf("Document = %q, want the filename without its timestamp or extension", e.Identity.Document)
	}
	if e.Identity.Project != "" {
		t.Errorf("Project = %q, want empty for resolution to fill", e.Identity.Project)
	}
}

// The vault directory corroborates the stripped filename, but is not a
// project: project resolution maps a composition to a client project later.
func TestPathNameCorroboratesDocumentAndDoesNotSetProject(t *testing.T) {
	root := vault(t, map[string][]string{
		"Big Client Job v2": {"Big Client Job v2_2026-07-28_0632.motn"},
	})
	r := newReader(t, only(t, motionManifest(root)))
	log := capture(r)
	events := collect(t, r, time.Time{})

	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	if got := events[0].Identity.Document; got != "Big Client Job v2" {
		t.Errorf("Document = %q, want %q", got, "Big Client Job v2")
	}
	if got := events[0].Identity.Project; got != "" {
		t.Errorf("Project = %q, want empty for resolution to fill", got)
	}
	if got := log.String(); strings.Contains(got, "disagrees") {
		t.Errorf("agreement logged as a disagreement: %s", got)
	}
}

func TestPathNameDisagreementIsWarnedRatherThanSilentlyCollapsed(t *testing.T) {
	root := vault(t, map[string][]string{
		"old-composition-name": {"renamed-composition_2026-07-28_0632.motn"},
	})
	r := newReader(t, only(t, motionManifest(root)))
	log := capture(r)
	events := collect(t, r, time.Time{})

	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	if got := events[0].Identity.Document; got != "renamed-composition" {
		t.Errorf("Document = %q, want filename-derived name", got)
	}
	if got := log.String(); !strings.Contains(got, "path-derived document disagrees") ||
		!strings.Contains(got, "old-composition-name") || !strings.Contains(got, "renamed-composition") {
		t.Errorf("warning = %q, want both conflicting names", got)
	}
}

func TestExplicitDocumentRuleOverridesFilenameDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "working-copy-8f3c.motn")
	if err := os.WriteFile(path, []byte("motn"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	spec := only(t, fmt.Sprintf(`{
  "manifest_version": 1,
  "sources": [{
    "id": "example", "type": "timestamped-files", "emits": "point", "cause": "timer",
    "files": {"root": %q, "match": "*.motn"},
    "timestamp": {"from": "mtime"},
    "document": {"from": "path", "pattern": "/([^/]+)-[0-9a-f]+\\.motn$"}
  }]
}`, root))
	events := collect(t, newReader(t, spec), time.Time{})
	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	if got := events[0].Identity.Document; got != "working-copy" {
		t.Errorf("Document = %q, want explicit rule result", got)
	}
}

func TestMtimeTimestamps(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "spaceship.blend1")
	if err := os.WriteFile(path, []byte("blend"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	want := time.Date(2026, 2, 10, 14, 5, 0, 0, time.Local)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatalf("set times: %v", err)
	}

	events := collect(t, newReader(t, only(t, blenderManifest(root, "mtime"))), time.Time{})
	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	if !events[0].Start.Equal(want) {
		t.Errorf("Start = %s, want %s", events[0].Start, want)
	}
	if events[0].Identity.Document != "spaceship" {
		t.Errorf("Document = %q, want spaceship", events[0].Identity.Document)
	}
	if events[0].Identity.Project != "" {
		t.Errorf("Project = %q, want empty when the manifest extracts none", events[0].Identity.Project)
	}
}

// Attribution's first pass matches a path against the directories a project
// has blessed, so the path this reader already holds has to survive into the
// event. Without it, file evidence — the only evidence that can be attributed
// directly — has to fall back to the name join like a shadow cache.
func TestEventsCarryThePathTheyWereReadFrom(t *testing.T) {
	root := vault(t, map[string][]string{
		"dark-crystal-v2": {"dark-crystal-v2_2026-07-28_1401.motn"},
	})
	events := collect(t, newReader(t, only(t, motionManifest(root))), time.Time{})

	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	want := filepath.Join(root, "dark-crystal-v2", "dark-crystal-v2_2026-07-28_1401.motn")
	if events[0].Locator != want {
		t.Errorf("Locator = %q, want %q", events[0].Locator, want)
	}
	if !filepath.IsAbs(events[0].Locator) {
		t.Errorf("Locator = %q is relative; it can never match a registered directory", events[0].Locator)
	}
}

// Discovery can hand back whatever the manifest's glob or Spotlight produced,
// and a locator that is not absolute silently attributes to nothing rather
// than failing. The reader resolves it once, here, so no later stage has to
// wonder.
func TestARelativeDiscoveryResultIsMadeAbsolute(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "spaceship.blend1")
	if err := os.WriteFile(path, []byte("blend"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	rel, err := filepath.Rel(cwd(t), root)
	if err != nil {
		t.Skipf("no relative path from the working directory to %s: %v", root, err)
	}

	events := collect(t, newReader(t, only(t, blenderManifest(rel, "mtime"))), time.Time{})
	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	if !filepath.IsAbs(events[0].Locator) {
		t.Errorf("Locator = %q, want an absolute path", events[0].Locator)
	}
	if err := events[0].Validate(); err != nil {
		t.Errorf("Validate() = %v; a relative locator is refused at the registry", err)
	}
}

func cwd(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return dir
}

// Birthtime is the point of the strategy: a file whose mtime says something
// else must still report when it was created.
//
// The mtime here is deliberately in the future rather than the past. macOS
// lowers a file's birth time to match a back-dated mtime, so a fixture built
// that way would prove nothing — both times would agree.
func TestBirthtimeTimestamps(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("birthtime is read from the darwin stat structure; this is %s", runtime.GOOS)
	}
	root := t.TempDir()
	path := filepath.Join(root, "spaceship.blend1")

	before := time.Now().Add(-2 * time.Second)
	if err := os.WriteFile(path, []byte("blend"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	after := time.Now().Add(2 * time.Second)

	later := time.Date(2028, 1, 1, 12, 0, 0, 0, time.Local)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("set times: %v", err)
	}

	events := collect(t, newReader(t, only(t, blenderManifest(root, "birthtime"))), time.Time{})
	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	got := events[0].Start
	if got.Equal(later) {
		t.Fatalf("Start = %s, which is the mtime; birthtime was not read", got)
	}
	if got.Before(before) || got.After(after) {
		t.Errorf("Start = %s, want the creation time between %s and %s", got, before, after)
	}
}

// since is how a repeated harvest avoids re-reporting files it already read.
func TestSinceExcludesFilesAlreadyReported(t *testing.T) {
	root := vault(t, map[string][]string{
		"scanline2-card-granted": {
			"scanline2-card-granted_2026-07-28_0615.motn",
			"scanline2-card-granted_2026-07-28_0632.motn",
		},
	})
	r := newReader(t, only(t, motionManifest(root)))

	first := collect(t, r, time.Time{})
	if len(first) != 2 {
		t.Fatalf("first harvest collected %d events, want 2: %+v", len(first), first)
	}

	newest := first[len(first)-1].Start
	again := collect(t, r, newest)
	if len(again) != 0 {
		t.Errorf("re-running with since=%s collected %d events, want none: %+v", newest, len(again), again)
	}

	partial := collect(t, r, first[0].Start)
	if len(partial) != 1 {
		t.Fatalf("collected %d events after the first save, want 1: %+v", len(partial), partial)
	}
	if !partial[0].Start.Equal(newest) {
		t.Errorf("Start = %s, want the later save %s", partial[0].Start, newest)
	}
}

// Events must arrive in a stable order or every consumer has to sort, and
// Spotlight in particular returns results in index order.
func TestEventsAreOrderedByTime(t *testing.T) {
	root := vault(t, map[string][]string{
		"b-project": {"b-project_2026-07-28_0615.motn"},
		"a-project": {"a-project_2026-07-28_0700.motn", "a-project_2026-07-28_0500.motn"},
	})
	events := collect(t, newReader(t, only(t, motionManifest(root))), time.Time{})
	if len(events) != 3 {
		t.Fatalf("collected %d events, want 3: %+v", len(events), events)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Start.Before(events[i-1].Start) {
			t.Fatalf("event %d starts %s, before its predecessor %s", i, events[i].Start, events[i-1].Start)
		}
	}
}

// An uninstalled application is ordinary. Most people have no Motion, and
// losing a whole harvest over that would be absurd.
func TestMissingRootIsAWarningNotAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "Motion Projects", "Autosave Vault")
	r := newReader(t, only(t, motionManifest(root)))
	warnings := capture(r)

	events, err := r.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect() = %v, want no error for a root that is simply absent", err)
	}
	if len(events) != 0 {
		t.Errorf("collected %d events from a missing root", len(events))
	}
	if !strings.Contains(warnings.String(), "Autosave Vault") {
		t.Errorf("warnings = %q, want the missing root named", warnings.String())
	}
}

// A file caught by the glob but not by the timestamp pattern is a manifest
// mismatch, not a reason to lose the files that did match.
func TestFileWithoutATimestampIsSkipped(t *testing.T) {
	root := vault(t, map[string][]string{
		"scanline2-card-granted": {
			"scanline2-card-granted.motn",
			"scanline2-card-granted_2026-07-28_0632.motn",
		},
	})
	r := newReader(t, only(t, motionManifest(root)))
	warnings := capture(r)

	events, err := r.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect() = %v, want the unmatched file tolerated", err)
	}
	if len(events) != 1 {
		t.Fatalf("collected %d events, want only the timestamped file: %+v", len(events), events)
	}
	if !strings.Contains(warnings.String(), "scanline2-card-granted.motn") {
		t.Errorf("warnings = %q, want the skipped file named", warnings.String())
	}
}

// fakeMdfind puts an executable named mdfind at the front of PATH, so the
// Spotlight path can be exercised without depending on the machine's index.
func fakeMdfind(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mdfind")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatalf("write fake mdfind: %v", err)
	}
	t.Setenv("PATH", dir)
}

func spotlightManifest(root string) string {
	return fmt.Sprintf(`{
  "manifest_version": 1,
  "sources": [
    {
      "id": "blender-backups",
      "type": "timestamped-files",
      "emits": "point",
      "app": "org.blenderfoundation.blender",
      "cause": "user-action",
      "discovery": "spotlight",
      "files": { "root": %q, "query": "kMDItemFSName == '*.blend1'" },
      "timestamp": { "from": "mtime" }
    }
  ]
}`, root)
}

// Spotlight exists because recursive globs are refused: .blend1 backups are
// scattered across a volume and the index is the bounded way to find them.
func TestSpotlightDiscoveryReadsMdfindOutput(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "spaceship interior.blend1")
	if err := os.WriteFile(path, []byte("blend"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	want := time.Date(2026, 7, 12, 9, 30, 0, 0, time.Local)
	if err := os.Chtimes(path, want, want); err != nil {
		t.Fatalf("set times: %v", err)
	}
	fakeMdfind(t, fmt.Sprintf("printf '%%s\\0' %q", path))

	events := collect(t, newReader(t, only(t, spotlightManifest(root))), time.Time{})
	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1: %+v", len(events), events)
	}
	if !events[0].Start.Equal(want) {
		t.Errorf("Start = %s, want %s", events[0].Start, want)
	}
	if events[0].Identity.Document != "spaceship interior" {
		t.Errorf("Document = %q, want the name with its spaces intact", events[0].Identity.Document)
	}
}

// mdfind is macOS-only and can be missing. That is the same situation as an
// uninstalled app: report it and collect nothing.
func TestSpotlightWithoutMdfindIsAWarningNotAnError(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	r := newReader(t, only(t, spotlightManifest(t.TempDir())))
	warnings := capture(r)

	events, err := r.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect() = %v, want no error when mdfind is absent", err)
	}
	if len(events) != 0 {
		t.Errorf("collected %d events without mdfind", len(events))
	}
	if !strings.Contains(warnings.String(), "mdfind") {
		t.Errorf("warnings = %q, want mdfind named", warnings.String())
	}
}

// A query mdfind rejects is a manifest bug, and silently reporting nothing
// would leave it undiagnosed forever.
func TestSpotlightFailureIsReported(t *testing.T) {
	fakeMdfind(t, "echo 'invalid query' >&2; exit 2")
	r := newReader(t, only(t, spotlightManifest(t.TempDir())))

	_, err := r.Collect(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("Collect() = nil, want an error when mdfind fails")
	}
	if !strings.Contains(err.Error(), "mdfind") {
		t.Errorf("error = %q, want mdfind named", err)
	}
	if !strings.Contains(err.Error(), "invalid query") {
		t.Errorf("error = %q, want mdfind's own complaint included", err)
	}
}

// A file has one timestamp, so there is no honest way to give it a duration.
// Refuse at construction rather than inventing one.
func TestIntervalEmitsIsRefused(t *testing.T) {
	spec := only(t, motionManifest(t.TempDir()))
	spec.Emits = source.Interval

	if _, err := timestamped.New(spec); err == nil {
		t.Fatal("New() = nil error, want interval refused")
	}
}

// What caused a file is a property of the application that wrote it, not of the
// file, so it comes from the manifest and has to reach every event the reader
// emits. Nothing downstream can recover it afterwards: the mtime of a .blend1
// looks exactly like the mtime of an autosave, and for a perishable source the
// file itself may be gone.
func TestEventsCarryWhatCausedTheFile(t *testing.T) {
	root := vault(t, map[string][]string{
		"dark-crystal": {"dark-crystal_2026-07-28_0632.motn", "dark-crystal_2026-07-28_0645.motn"},
	})
	events := collect(t, newReader(t, only(t, motionManifest(root))), time.Time{})
	if len(events) != 2 {
		t.Fatalf("collected %d events, want 2", len(events))
	}
	for _, e := range events {
		if e.Cause != source.CauseTimer {
			t.Errorf("event for %q reports cause %q, want %q; Motion autosaves on a timer",
				e.Identity.Document, e.Cause, source.CauseTimer)
		}
	}

	// The same reader, the same shape, a different cause — read from the
	// manifest and not from anything about the file.
	backups := t.TempDir()
	if err := os.WriteFile(filepath.Join(backups, "scene.blend1"), []byte("blend"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	saves := collect(t, newReader(t, only(t, blenderManifest(backups, "mtime"))), time.Time{})
	if len(saves) != 1 {
		t.Fatalf("collected %d events, want 1", len(saves))
	}
	if saves[0].Cause != source.CauseUserAction {
		t.Errorf("event for %q reports cause %q, want %q; a .blend1 is written by an explicit save",
			saves[0].Identity.Document, saves[0].Cause, source.CauseUserAction)
	}
}

// A spec built by hand rather than loaded is the way an undeclared cause could
// reach the store, and it would do it silently: the events are otherwise
// perfectly good, and the evidence they came from may be pruned before anybody
// notices they assert nothing about presence.
func TestASpecThatDoesNotSayWhatCausedItsFilesIsRefused(t *testing.T) {
	spec := only(t, motionManifest(t.TempDir()))
	spec.Cause = ""

	if _, err := timestamped.New(spec); err == nil {
		t.Fatal("New() = nil error, want a spec with no cause refused")
	}
}

// A tilde is expanded for glob roots at load, but not for Spotlight ones, so
// an unexpanded root would search a directory literally named "~".
func TestUnexpandedSpotlightRootIsRefused(t *testing.T) {
	spec := only(t, spotlightManifest(t.TempDir()))
	spec.Files.Root = "~/Documents"

	if _, err := timestamped.New(spec); err == nil {
		t.Fatal("New() = nil error, want an unexpanded root refused")
	}
}

func TestCancelledContextStops(t *testing.T) {
	root := vault(t, map[string][]string{
		"scanline2-card-granted": {"scanline2-card-granted_2026-07-28_0632.motn"},
	})
	r := newReader(t, only(t, motionManifest(root)))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Collect(ctx, time.Time{}); err == nil {
		t.Fatal("Collect() = nil error, want the cancelled context reported")
	}
}
