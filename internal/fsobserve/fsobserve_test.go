package fsobserve

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
)

// write creates a file and returns its actual (OS-assigned) birth time,
// which cannot be set directly — only observed after the fact.
func write(t *testing.T, path string, contents string) time.Time {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	birth, ok := birthtime(info)
	if !ok {
		t.Skip("this platform cannot report a birth time")
	}
	return birth
}

func collect(t *testing.T, roots ...string) []source.Event {
	t.Helper()
	events, err := New(roots...).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return events
}

// eventsFor returns the events whose Locator is path, in the order Collect
// produced them.
func eventsFor(events []source.Event, path string) []source.Event {
	var out []source.Event
	for _, e := range events {
		if e.Locator == path {
			out = append(out, e)
		}
	}
	return out
}

// TestCollectEmitsSeparateEventsWhenBirthAndMtimeDiffer is the positive half
// of criterion 0X3: two genuinely different instants must survive as two
// observations, since the gap between them is exactly the fact this package
// exists to preserve without inventing a duration for it.
func TestCollectEmitsSeparateEventsWhenBirthAndMtimeDiffer(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dark-crystal-v2.motn")
	birth := write(t, path, "v1")

	later := birth.Add(2 * time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	events := eventsFor(collect(t, dir), path)
	if len(events) != 2 {
		t.Fatalf("got %d events for a file with a 2h birth-to-mtime gap, want 2: %+v", len(events), events)
	}

	sawBirth, sawMtime := false, false
	for _, e := range events {
		if e.Shape != source.Point {
			t.Errorf("event %+v is not a Point", e)
		}
		if !e.Start.Equal(e.End) {
			t.Errorf("event %+v is a Point but Start != End", e)
		}
		switch {
		case e.Start.Unix() == birth.Unix():
			sawBirth = true
		case e.Start.Unix() == later.Unix():
			sawMtime = true
		default:
			t.Errorf("event at %s matches neither the birth (%s) nor the set mtime (%s)", e.Start, birth, later)
		}
	}
	if !sawBirth || !sawMtime {
		t.Errorf("sawBirth=%v sawMtime=%v, want both", sawBirth, sawMtime)
	}
}

// TestCollectEmitsOneEventWhenBirthAndMtimeCoincide is the negative half of
// 0X3: a file written once and never touched again must not double-count as
// two identical observations.
func TestCollectEmitsOneEventWhenBirthAndMtimeCoincide(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "single-write.png")
	write(t, path, "only ever written once")

	events := eventsFor(collect(t, dir), path)
	if len(events) != 1 {
		t.Fatalf("got %d events for a file whose birth and mtime coincide, want 1: %+v", len(events), events)
	}
}

// TestCollectClassifiesSourceOutputAndOther is criterion P05: a compositor
// project file and a rendered frame must not read as the same kind of thing.
func TestCollectClassifiesSourceOutputAndOther(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		want Class
	}{
		{"comp.motn", ClassSource},
		{"scene.blend", ClassSource},
		{"backup.blend1", ClassSource},
		{"art.psd", ClassSource},
		{"frame-000001.png", ClassOutput},
		{"render.mov", ClassOutput},
		{"notes.txt", ClassOther},
	}
	for _, c := range cases {
		write(t, filepath.Join(dir, c.name), "x")
	}

	events := collect(t, dir)
	got := make(map[string]Class)
	for _, e := range events {
		got[filepath.Base(e.Locator)] = Class(e.State)
	}
	for _, c := range cases {
		if got[c.name] != c.want {
			t.Errorf("%s classified as %q, want %q", c.name, got[c.name], c.want)
		}
	}
}

// TestClassify checks the pure extension mapping directly, independent of a
// walk, so the classification table itself is exercised without needing a
// temp file per case.
func TestClassify(t *testing.T) {
	cases := map[string]Class{
		"a.motn":   ClassSource,
		"a.psd":    ClassSource,
		"a.psb":    ClassSource,
		"a.blend":  ClassSource,
		"a.blend1": ClassSource,
		"a.ai":     ClassSource,
		"a.aep":    ClassSource,
		"a.webp":   ClassOutput,
		"a.png":    ClassOutput,
		"a.jpg":    ClassOutput,
		"a.jpeg":   ClassOutput,
		"a.mov":    ClassOutput,
		"a.mp4":    ClassOutput,
		"a.gif":    ClassOutput,
		"a.exr":    ClassOutput,
		"a.tif":    ClassOutput,
		"a.txt":    ClassOther,
		"a":        ClassOther,
		"a.MOTN":   ClassSource, // extension matching is case-insensitive
	}
	for name, want := range cases {
		if got := Classify(name); got != want {
			t.Errorf("Classify(%q) = %q, want %q", name, got, want)
		}
	}
}

// TestCollectAppliesSinceToEachTimestampIndependently proves since is a
// per-observation filter, not a per-file one: a file can have one stamp a
// caller has already seen and one it has not, and only the new one should
// come back. Getting this wrong either re-reports old evidence forever or
// silently drops a later save on a file whose birth was already reported.
func TestCollectAppliesSinceToEachTimestampIndependently(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sprite.psd")
	birth := write(t, path, "v1")

	newMtime := birth.Add(1 * time.Hour)
	if err := os.Chtimes(path, newMtime, newMtime); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	since := birth.Add(30 * time.Minute)
	events, err := New(dir).Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	events = eventsFor(events, path)
	if len(events) != 1 {
		t.Fatalf("got %d events after since=%s, want exactly the mtime one: %+v", len(events), since, events)
	}
	if !events[0].Start.Equal(newMtime.Truncate(time.Second)) && events[0].Start.Unix() != newMtime.Unix() {
		t.Errorf("surviving event is at %s, want the mtime %s", events[0].Start, newMtime)
	}
}

// TestCollectSkipsExcludedDirectories mirrors the real, measured worktree
// inflation this project has already hit elsewhere (2026-07-09 read 6103 raw
// against 71 filtered files, 86x, from nested worktree copies) — a
// directory literally named "worktrees" must not contribute duplicate
// evidence of the same files.
func TestCollectSkipsExcludedDirectories(t *testing.T) {
	dir := t.TempDir()
	for _, sub := range []string{".git", "node_modules", "worktrees", ".Trash", "Autosave Vault"} {
		nested := filepath.Join(dir, sub, "buried.motn")
		if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		write(t, nested, "should never be seen")
	}
	real := filepath.Join(dir, "kept.motn")
	write(t, real, "real work")

	events := collect(t, dir)
	for _, e := range events {
		if e.Locator != real {
			t.Errorf("got an event from an excluded directory: %+v", e)
		}
	}
	if len(eventsFor(events, real)) == 0 {
		t.Error("the one file outside every excluded directory produced no events at all")
	}
}

// TestCollectSkipsDSStoreAndMarkerFiles checks that macOS's own housekeeping
// file and this project's own registry marker are not mistaken for evidence
// of creative work.
func TestCollectSkipsDSStoreAndMarkerFiles(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".DS_Store"), "junk")
	write(t, filepath.Join(dir, registry.MarkerFilename), "{}")
	real := filepath.Join(dir, "kept.png")
	write(t, real, "real work")

	events := collect(t, dir)
	if got := len(events); got != len(eventsFor(events, real)) {
		t.Errorf("got %d events total but only %d for the one real file; .DS_Store or the marker leaked through",
			got, len(eventsFor(events, real)))
	}
}

// TestCollectToleratesMissingRoot matches every other source in this
// codebase: a root that is not there describes an application nobody has, or
// a project nobody has registered yet on this machine, not a failure.
func TestCollectToleratesMissingRoot(t *testing.T) {
	events, err := New(filepath.Join(t.TempDir(), "does-not-exist")).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect on a missing root returned an error: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from a root that does not exist", len(events))
	}
}

// TestCollectSetsLocatorCauseAndIdentity checks the fields Collect is
// responsible for filling in on every event, beyond the timestamp itself.
func TestCollectSetsLocatorCauseAndIdentity(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "dark-crystal-v2.motn")
	write(t, path, "v1")

	events := eventsFor(collect(t, dir), path)
	if len(events) == 0 {
		t.Fatal("no events for the file")
	}
	for _, e := range events {
		if e.Locator != path {
			t.Errorf("Locator = %q, want %q", e.Locator, path)
		}
		if !filepath.IsAbs(e.Locator) {
			t.Errorf("Locator %q is not absolute", e.Locator)
		}
		if e.Cause != source.CauseUserAction {
			t.Errorf("Cause = %q, want %q", e.Cause, source.CauseUserAction)
		}
		if e.Identity.Document != "dark-crystal-v2" {
			t.Errorf("Document = %q, want %q", e.Identity.Document, "dark-crystal-v2")
		}
		if e.Identity.App != "com.apple.motionapp" {
			t.Errorf("App = %q, want %q", e.Identity.App, "com.apple.motionapp")
		}
		if e.Source != ID {
			t.Errorf("Source = %q, want %q", e.Source, ID)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("event %+v does not validate: %v", e, err)
		}
	}
}

// TestCollectLeavesAppEmptyForUnrecognisedExtensions checks that a bundle
// identifier is only ever asserted where this codebase already has an
// independently confirmed one (internal/manifest/builtin). Guessing one for
// an extension nobody has verified would silently corrupt the Biome join
// this field exists for.
func TestCollectLeavesAppEmptyForUnrecognisedExtensions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.psd")
	write(t, path, "x")

	events := eventsFor(collect(t, dir), path)
	if len(events) == 0 {
		t.Fatal("no events for the file")
	}
	for _, e := range events {
		if e.Identity.App != "" {
			t.Errorf("App = %q for a .psd, want empty: no confirmed bundle id exists in this codebase for it", e.Identity.App)
		}
	}
}

// TestIDAndKind is a minimal sanity check on the two fixed identifiers this
// source reports.
func TestIDAndKind(t *testing.T) {
	w := New()
	if w.ID() != ID || w.ID() == "" {
		t.Errorf("ID() = %q", w.ID())
	}
	if w.Kind() == "" {
		t.Error("Kind() is empty")
	}
}

// TestCollectVisitsAFileOnceWhenItsParentAndItAreBothRegisteredRoots is
// criterion o2s (task MPsdu): a repo and a sub-folder inside it can both
// carry a marker — Register has no rule against it — and registry.List
// hands both paths to fsobserve.New as separate roots. Without
// normalisation, WalkDir descends into the nested root twice: once because
// it is reachable from the outer root, and once because it is a root in its
// own right. A single-write file (birth == mtime) makes the miscount
// unambiguous: it must contribute exactly one event, not two.
func TestCollectVisitsAFileOnceWhenItsParentAndItAreBothRegisteredRoots(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "nested")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(child, "buried.motn")
	write(t, path, "only ever written once")

	events := eventsFor(collect(t, parent, child), path)
	if len(events) != 1 {
		t.Fatalf("got %d events for a file under both a registered parent and a registered nested child, want 1: %+v",
			len(events), events)
	}
}

// TestCollectVisitsAFileOnceRegardlessOfRootOrder checks the same case with
// the nested root listed FIRST — registry.List sorts by path, so a caller
// cannot rely on the parent always arriving before its descendant.
func TestCollectVisitsAFileOnceRegardlessOfRootOrder(t *testing.T) {
	parent := t.TempDir()
	child := filepath.Join(parent, "sub", "nested")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(child, "buried.motn")
	write(t, path, "only ever written once")

	events := eventsFor(collect(t, child, parent), path)
	if len(events) != 1 {
		t.Fatalf("got %d events with the nested root listed before its parent, want 1: %+v", len(events), events)
	}
}

// TestCollectVisitsAFileOnceWhenTheSameRootIsPassedTwice covers the
// degenerate case of the nesting bug: two registrations that resolve to the
// exact same directory (e.g. one with a trailing slash quirk from how it was
// typed at register time), rather than a genuine parent/child pair.
func TestCollectVisitsAFileOnceWhenTheSameRootIsPassedTwice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "buried.motn")
	write(t, path, "only ever written once")

	events := eventsFor(collect(t, dir, dir), path)
	if len(events) != 1 {
		t.Fatalf("got %d events when the identical root was passed twice, want 1: %+v", len(events), events)
	}
}

// TestCollectVisitsAFileOnceWhenARootIsReachedThroughAnIntermediateSymlink
// covers the route to the same duplication that nesting is not: two
// registered directories that are not one inside the other on paper (their
// path strings share no prefix relationship), but resolve to the same
// physical directory once an intermediate symlink on the path is followed
// — the same shape as macOS's own /tmp being a symlink to /private/tmp.
//
// The symlink is placed one level up from the walked root, deliberately: an
// intermediate symlink earlier in the path is resolved by ordinary OS path
// lookup before filepath.WalkDir ever sees it, which is the shape a real
// duplicate-path registration takes. A root that is ITSELF a symlink is a
// separate mechanism — filepath.WalkDir Lstats its own root argument — and
// used to collect zero files silently; that is task TAKiT, covered by
// TestCollectWalksARootThatIsItselfASymlink and the tests beside it.
func TestCollectVisitsAFileOnceWhenARootIsReachedThroughAnIntermediateSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "actual")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	loop := filepath.Join(base, "circular")
	if err := os.Symlink(base, loop); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	aliasRoot := filepath.Join(loop, "actual")

	write(t, filepath.Join(real, "buried.motn"), "only ever written once")

	// Filtering by Locator would not catch this case: the two roots produce
	// two DIFFERENT locator strings for the same physical file (one through
	// "actual", one through "circular/actual"), so the count is taken over
	// every event Collect returned rather than events matching one
	// particular path.
	events := collect(t, real, aliasRoot)
	if len(events) != 1 {
		t.Fatalf("got %d events for a root and the same directory reached through an intermediate symlink, want 1: %+v",
			len(events), events)
	}
}

// symlinkedRoot builds a directory holding one single-write file and returns
// the real directory alongside a sibling symlink pointing at it. The symlink
// is the ROOT itself, which is what makes every test using it distinct from
// TestCollectVisitsAFileOnceWhenARootIsReachedThroughAnIntermediateSymlink:
// there the symlink sits one level ABOVE the walked root and the OS resolves
// it during ordinary path lookup, so filepath.WalkDir never has to deal with
// it. Here filepath.WalkDir is handed the link itself, which it os.Lstats
// rather than os.Stats.
func symlinkedRoot(t *testing.T) (real, link, file string) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "actual")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	file = "buried.motn"
	write(t, filepath.Join(real, file), "only ever written once")

	link = filepath.Join(base, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	return real, link, file
}

// warnings returns a Walker over roots plus the buffer its warnings land in,
// so a test can assert on what was REPORTED and not only on what was
// returned. Zero events with a warning and zero events without one are
// different outcomes, and this package's whole failure mode is confusing
// them.
func warnings(t *testing.T, roots ...string) (*Walker, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	w := New(roots...)
	w.Log = slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	return w, buf
}

// TestCollectWalksARootThatIsItselfASymlink is criterion elw (task TAKiT).
// filepath.WalkDir calls os.Lstat on its own root argument and never follows
// a symlink there, so a registered root that IS a symlink to a directory
// arrives at the walk function as a non-directory entry: the walk never
// descends and returns nothing, with no error and no warning. Measured
// against a real registered directory on 2026-07-31: 19 observations by its
// real path, 0 through a symlink to it.
//
// A project registered through an alias or a symlinked checkout would
// therefore harvest nothing from this package for ever, and every downstream
// report would read "no filesystem work happened here" rather than "this
// root was never walked".
func TestCollectWalksARootThatIsItselfASymlink(t *testing.T) {
	real, link, _ := symlinkedRoot(t)

	viaReal := collect(t, real)
	if len(viaReal) == 0 {
		t.Fatal("the real directory produced no events; the fixture is wrong, not the walk")
	}
	viaLink := collect(t, link)
	if len(viaLink) != len(viaReal) {
		t.Fatalf("got %d events through a symlinked root, want the %d the real path yields: %+v",
			len(viaLink), len(viaReal), viaLink)
	}
}

// TestCollectReportsTheRegisteredPathWhenTheRootIsASymlink pins the half of
// the fix that is easy to lose: the walk may have to run over the symlink's
// resolved target, because that is the only thing filepath.WalkDir will
// descend into, but no caller may ever see the resolved form. On macOS /var
// — and therefore os.TempDir(), and therefore this very fixture — is itself
// a symlink to /private/var, so a fix that walks the fully resolved path and
// reports what it walked rewrites the Locator of every event this package
// produces. Locator must stay the path the caller actually registered.
func TestCollectReportsTheRegisteredPathWhenTheRootIsASymlink(t *testing.T) {
	_, link, file := symlinkedRoot(t)

	events := collect(t, link)
	if len(events) == 0 {
		t.Fatal("no events through the symlinked root")
	}
	want := filepath.Join(link, file)
	for _, e := range events {
		if e.Locator != want {
			t.Errorf("Locator = %q, want the registered path %q", e.Locator, want)
		}
	}
}

// TestCollectVisitsAFileOnceWhenARootIsRegisteredBothDirectlyAndAsASymlink
// is the negative guard on the fix: now that a symlinked root really does
// get walked, a caller registering both the symlink and its target must
// still see each file once. The symlink is listed FIRST deliberately —
// normalizedRoots keeps the first of two roots that canonicalise the same,
// so this ordering is the one where the surviving root is the symlink.
func TestCollectVisitsAFileOnceWhenARootIsRegisteredBothDirectlyAndAsASymlink(t *testing.T) {
	real, link, _ := symlinkedRoot(t)

	events := collect(t, link, real)
	if len(events) != 1 {
		t.Fatalf("got %d events for one single-write file registered both as a symlink and by its real path, want 1: %+v",
			len(events), events)
	}
}

// TestCollectReportsARootItCouldNotWalk is criterion dzJ. Every way a root
// can yield nothing BECAUSE IT COULD NOT BE READ has to say so; see
// TestCollectDoesNotReportAnEmptyRoot for the other side of the same line.
func TestCollectReportsARootItCouldNotWalk(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func(t *testing.T, base string) string
	}{
		{
			name: "a path that is not there at all",
			root: func(t *testing.T, base string) string {
				return filepath.Join(base, "never-registered")
			},
		},
		{
			name: "a symlink whose target has been deleted",
			root: func(t *testing.T, base string) string {
				link := filepath.Join(base, "dangling")
				if err := os.Symlink(filepath.Join(base, "gone"), link); err != nil {
					t.Skipf("symlinks not supported here: %v", err)
				}
				return link
			},
		},
		{
			name: "a symlink to a file rather than a directory",
			root: func(t *testing.T, base string) string {
				file := filepath.Join(base, "not-a-directory.motn")
				write(t, file, "x")
				link := filepath.Join(base, "alias")
				if err := os.Symlink(file, link); err != nil {
					t.Skipf("symlinks not supported here: %v", err)
				}
				return link
			},
		},
		{
			name: "a plain file registered as a root",
			root: func(t *testing.T, base string) string {
				file := filepath.Join(base, "not-a-directory.motn")
				write(t, file, "x")
				return file
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, logged := warnings(t, tc.root(t, t.TempDir()))
			events, err := w.Collect(context.Background(), time.Time{})
			if err != nil {
				t.Fatalf("Collect returned an error rather than tolerating and reporting: %v", err)
			}
			if len(events) != 0 {
				t.Errorf("got %d events from a root that cannot be walked as a directory: %+v", len(events), events)
			}
			if logged.Len() == 0 {
				t.Error("a root that could not be walked collected nothing and said nothing; " +
					"'zero because unwalkable' is indistinguishable from 'zero because empty'")
			}
		})
	}
}

// TestCollectDoesNotReportAnEmptyRoot is the other side of criterion dzJ,
// and the constraint that stops the lazy fix of warning whenever a root
// yields no events. A registered project where nothing has been saved yet is
// an ordinary, correct zero and must stay silent, or the warning that means
// "this root was never read" becomes noise nobody looks at.
func TestCollectDoesNotReportAnEmptyRoot(t *testing.T) {
	w, logged := warnings(t, t.TempDir())
	events, err := w.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from an empty directory", len(events))
	}
	if logged.Len() != 0 {
		t.Errorf("an empty but perfectly readable root was reported as a problem: %s", logged.String())
	}
}

// TestCollectStillWalksTwoGenuinelySeparateRoots is the negative case
// guarding against an overzealous fix: two roots that share no ancestry at
// all must both still be walked, and a file existing under only one of them
// must not be lost.
func TestCollectStillWalksTwoGenuinelySeparateRoots(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	pathA := filepath.Join(a, "only-in-a.motn")
	pathB := filepath.Join(b, "only-in-b.motn")
	write(t, pathA, "a")
	write(t, pathB, "b")

	events := collect(t, a, b)
	if len(eventsFor(events, pathA)) != 1 {
		t.Errorf("got %d events for the file under root a, want 1", len(eventsFor(events, pathA)))
	}
	if len(eventsFor(events, pathB)) != 1 {
		t.Errorf("got %d events for the file under root b, want 1", len(eventsFor(events, pathB)))
	}
}
