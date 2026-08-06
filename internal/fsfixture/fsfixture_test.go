package fsfixture_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fsfixture"
)

// build materialises entries under a fresh temp directory, skipping the whole
// test where the platform cannot set a birth time at all. The skip names the
// missing precondition rather than a conclusion: this is not "the fixture is
// unnecessary here", it is "this kernel has no settable creation timestamp".
func build(t *testing.T, entries []fsfixture.Entry) string {
	t.Helper()
	root := t.TempDir()
	err := fsfixture.Build(root, entries)
	if errors.Is(err, fsfixture.ErrNoBirthtime) {
		t.Skipf("this platform cannot set a file's birth time: %v", err)
	}
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return root
}

func stamp(s string) time.Time {
	at, err := time.ParseInLocation("2006-01-02 15:04:05.999999999", s, time.Local)
	if err != nil {
		panic(err)
	}
	return at
}

// The whole package rests on this one property. A checked-in tree cannot carry
// a birthtime — git does not store one and a fresh checkout is born now — so a
// fixture corpus for internal/fsobserve is only possible if the two timestamps
// can be set independently and exactly.
func TestBuildSetsBirthAndModificationTimesExactly(t *testing.T) {
	birth := stamp("2026-03-04 06:00:14.913427472")
	mtime := stamp("2026-03-04 06:50:34.742495669")
	root := build(t, []fsfixture.Entry{
		{Path: "studio/beacon.motn", Birth: birth, Mtime: mtime},
	})

	path := filepath.Join(root, "studio", "beacon.motn")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.ModTime(); !got.Equal(mtime) {
		t.Errorf("mtime = %s, want %s", got.Format(time.RFC3339Nano), mtime.Format(time.RFC3339Nano))
	}
	got, err := fsfixture.Birthtime(path)
	if err != nil {
		t.Fatalf("Birthtime: %v", err)
	}
	if !got.Equal(birth) {
		t.Errorf("birth = %s, want %s", got.Format(time.RFC3339Nano), birth.Format(time.RFC3339Nano))
	}
}

// A birth and an mtime inside the same second, differing only in nanoseconds,
// is the case internal/fsobserve collapses to a single event. If the filesystem
// or the setter rounded to whole seconds, that test would pass for the wrong
// reason and its sibling — 658ms apart across a second boundary — would fail.
func TestBuildKeepsSubSecondPrecision(t *testing.T) {
	birth := stamp("2026-03-04 06:42:13.515838756")
	mtime := stamp("2026-03-04 06:42:13.515962377")
	root := build(t, []fsfixture.Entry{{Path: "a.png", Birth: birth, Mtime: mtime}})

	path := filepath.Join(root, "a.png")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got := info.ModTime().Nanosecond(); got != mtime.Nanosecond() {
		t.Errorf("mtime nanoseconds = %d, want %d", got, mtime.Nanosecond())
	}
	got, err := fsfixture.Birthtime(path)
	if err != nil {
		t.Fatalf("Birthtime: %v", err)
	}
	if got.Nanosecond() != birth.Nanosecond() {
		t.Errorf("birth nanoseconds = %d, want %d", got.Nanosecond(), birth.Nanosecond())
	}
}

// macOS lowers a file's creation date to the modification time when the
// modification time is set earlier than it — which is exactly what happens when
// a fixture file created today is backdated. The order Build uses has to
// survive that, and a file whose mtime is much later than its birth is the case
// where getting the order wrong is invisible.
func TestBuildSurvivesBackdatingAFileCreatedNow(t *testing.T) {
	birth := stamp("2026-03-04 06:00:14.913427472")
	mtime := stamp("2026-03-05 13:17:56.000000000")
	root := build(t, []fsfixture.Entry{{Path: "decayed.motn", Birth: birth, Mtime: mtime}})

	got, err := fsfixture.Birthtime(filepath.Join(root, "decayed.motn"))
	if err != nil {
		t.Fatalf("Birthtime: %v", err)
	}
	if !got.Equal(birth) {
		t.Errorf("birth = %s, want %s — the creation date was rewritten by the backdating",
			got.Format(time.RFC3339Nano), birth.Format(time.RFC3339Nano))
	}
}

// The case that actually decides the order of the two syscalls: a file whose
// modification time is EARLIER than its birth. internal/chains documents this
// as a real shape — "a file restored with its mtime preserved can invert that"
// — and it is the only one where getting the order wrong is visible, because
// macOS lowers the creation date to a modification time set behind it.
//
// Measured by mutation: swapping the two calls in writeEntry so the birth time
// is set first turns this test red with the birth reported as 2026-03-04
// 06:12:00, the mtime, instead of 2026-03-05 13:00:00. Every other test in
// this file stays green, because every other entry has mtime >= birth.
func TestBuildKeepsABirthTimeLaterThanItsModificationTime(t *testing.T) {
	birth := stamp("2026-03-05 13:00:00")
	mtime := stamp("2026-03-04 06:12:00")
	root := build(t, []fsfixture.Entry{{Path: "restored.psd", Birth: birth, Mtime: mtime}})

	path := filepath.Join(root, "restored.psd")
	got, err := fsfixture.Birthtime(path)
	if err != nil {
		t.Fatalf("Birthtime: %v", err)
	}
	if !got.Equal(birth) {
		t.Errorf("birth = %s, want %s — setting the mtime behind the creation date dragged it back",
			got.Format(time.RFC3339Nano), birth.Format(time.RFC3339Nano))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if !info.ModTime().Equal(mtime) {
		t.Errorf("mtime = %s, want %s", info.ModTime().Format(time.RFC3339Nano), mtime.Format(time.RFC3339Nano))
	}
}

func TestBuildCreatesParentDirectories(t *testing.T) {
	at := stamp("2026-03-04 06:00:00")
	root := build(t, []fsfixture.Entry{
		{Path: ".claude/worktrees/copy/studio/beacon.motn", Birth: at, Mtime: at},
	})
	if _, err := os.Stat(filepath.Join(root, ".claude", "worktrees", "copy", "studio", "beacon.motn")); err != nil {
		t.Errorf("nested entry not created: %v", err)
	}
}

// A manifest is data, and data that can name ../../.ssh/id_rsa is a fixture
// loader that writes outside the directory it was given.
func TestBuildRefusesAPathThatLeavesTheRoot(t *testing.T) {
	at := stamp("2026-03-04 06:00:00")
	err := fsfixture.Build(t.TempDir(), []fsfixture.Entry{{Path: "../escape.motn", Birth: at, Mtime: at}})
	if err == nil {
		t.Fatal("Build accepted a path that climbs out of the root")
	}
	if errors.Is(err, fsfixture.ErrNoBirthtime) {
		t.Skipf("this platform cannot set a file's birth time: %v", err)
	}
	if !strings.Contains(err.Error(), "escape.motn") {
		t.Errorf("error %q does not name the offending path", err)
	}
}

func TestLoadParsesEntriesAndIgnoresCommentsAndBlankLines(t *testing.T) {
	path := writeManifest(t, `
# a comment

studio/beacon.motn | 2026-03-04 06:00:14.913427472 | 2026-03-04 06:50:34.742495669
studio/tile.af    | 2026-03-04 06:41:36.799256773 | 2026-03-04 06:41:36.984793537
`)
	got, err := fsfixture.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d entries, want 2: %+v", len(got), got)
	}
	if got[0].Path != "studio/beacon.motn" {
		t.Errorf("entry 0 path = %q", got[0].Path)
	}
	if !got[0].Birth.Equal(stamp("2026-03-04 06:00:14.913427472")) {
		t.Errorf("entry 0 birth = %s", got[0].Birth)
	}
	if !got[1].Mtime.Equal(stamp("2026-03-04 06:41:36.984793537")) {
		t.Errorf("entry 1 mtime = %s", got[1].Mtime)
	}
}

// Most files in a real save-as corpus were written once: birth and mtime are
// the same syscall. Repeating the stamp in every such line is noise a manifest
// reader has to compare by eye, so an omitted mtime means "never touched again".
func TestLoadDefaultsTheModificationTimeToTheBirthTime(t *testing.T) {
	path := writeManifest(t, "render/beacon.blend | 2026-03-04 09:19:26\n")
	got, err := fsfixture.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d entries, want 1", len(got))
	}
	if !got[0].Mtime.Equal(got[0].Birth) {
		t.Errorf("mtime %s does not default to the birth %s", got[0].Mtime, got[0].Birth)
	}
}

// A manifest that is wrong has to say WHERE, or a 40-line fixture becomes a
// bisect.
func TestLoadNamesTheLineItCouldNotParse(t *testing.T) {
	path := writeManifest(t, "ok/a.blend | 2026-03-04 09:19:26\nbroken/b.blend | not-a-time\n")
	_, err := fsfixture.Load(path)
	if err == nil {
		t.Fatal("Load accepted a manifest with an unparseable timestamp")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Errorf("error %q does not name line 2", err)
	}
}

func TestLoadRejectsALineWithNoTimestamp(t *testing.T) {
	path := writeManifest(t, "render/beacon.blend\n")
	if _, err := fsfixture.Load(path); err == nil {
		t.Fatal("Load accepted a line carrying no timestamp at all")
	}
}

func writeManifest(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tree.txt")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
