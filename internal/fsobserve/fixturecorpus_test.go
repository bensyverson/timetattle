package fsobserve

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fsfixture"
	"github.com/bensyverson/timetattle/internal/source"
)

// testdata/realtree.txt is a synthetic corpus with the SHAPE of the real
// compositor morning realdata_test.go validates against, and none of its
// content. Every filename, directory and project name in it is invented; only
// the arithmetic of the timestamps is carried across, because the arithmetic is
// what this package is about.
//
// It exists because realdata_test.go asserts against one directory on one
// laptop. Task kDFeo assessed this package as not needing a corpus of its own,
// on the reasoning that its synthetic tests already drive birth and mtime
// through os.Chtimes on fresh temp files. That assessment was wrong in one
// specific way and it cost eight tests: os.Chtimes cannot set a BIRTH time, so
// every fixture file it makes is born now, and the cases that matter here —
// a birth and an mtime in the same second, an mtime that decayed to the next
// day while the birth stayed in the working morning — cannot be built with it
// at all. internal/fsfixture is what closes that, by setting st_birthtime
// directly.
//
// What this corpus reproduces, case for case:
//
//	beacon2-face-granted.motn   born 06:00:14.913427472, saved again 06:50:34.742495669
//	                            — two events spanning the session
//	beacon3-face-granted.motn   born 06:00:14.913427472, mtime a day later
//	                            — the whole morning survives only in the birthtime
//	panel2.af                   born and modified inside one second — one event
//	big-x-2.png                 the same, 124µs apart — one event
//	big-check-2.png             the same, 170µs apart — one event
//	big-x-red-2.png             657.8ms apart ACROSS a second boundary — two events
//	big-check-green-2.png       the same second, 51ms apart — one event
//
// plus a duplicate of the first file inside a nested worktree, which must not
// be counted twice, and five kinds of housekeeping file that must not be
// counted at all.
const fixtureTreeManifest = "testdata/realtree.txt"

// fixtureTreeDay is the synthetic morning the corpus sits in. It is
// deliberately NOT 2026-07-28, the real date realdata_test.go asserts against,
// so that a figure from this fixture can never be mistaken for a published
// measurement of anybody's actual working day.
func fixtureTreeDay(hh, mm, ss int, nsec int) time.Time {
	return time.Date(2026, 3, 4, hh, mm, ss, nsec, time.Local)
}

// fixtureTreeRoot materialises the corpus into a temporary directory.
func fixtureTreeRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	err := fsfixture.Materialize(root, fixtureTreeManifest)
	if errors.Is(err, fsfixture.ErrNoBirthtime) {
		t.Skipf("this platform cannot set a file's birth time, so a tree carrying one cannot be built here: %v", err)
	}
	if err != nil {
		t.Fatalf("Materialize %s: %v", fixtureTreeManifest, err)
	}
	return root
}

// fixtureTreeEvents walks the corpus exactly as a harvest would.
func fixtureTreeEvents(t *testing.T) (string, []source.Event) {
	t.Helper()
	root := fixtureTreeRoot(t)
	events, err := New(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return root, events
}

// TestFixtureTreeReproducesTheShapeOfARecoveredSession is the portable half of
// TestRealFilesystemWalkRecoversMotionSession. Neither substitutes for the
// other: that one proves this collector recovers a session on a real disk
// nobody arranged, this one proves the mechanism behind it works on a machine
// that has never seen that disk.
//
// Where the real test can only say "at least one observation falls in the
// window", because the disk may gain files, this one asserts the exact count.
// A fixture that cannot be tightened past its real-data sibling is not adding
// anything.
func TestFixtureTreeReproducesTheShapeOfARecoveredSession(t *testing.T) {
	root, events := fixtureTreeEvents(t)
	if len(events) == 0 {
		t.Fatal("got no events from a populated fixture tree")
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v does not validate: %v", e, err)
		}
	}

	windowStart := fixtureTreeDay(6, 0, 0, 0)
	windowEnd := fixtureTreeDay(6, 51, 0, 0)

	byFile := func(name string) []time.Time {
		var out []time.Time
		want := filepath.Join(root, "studio", name)
		for _, e := range events {
			if e.Locator == want {
				out = append(out, e.Start)
			}
		}
		return out
	}

	t.Run("the walk observes exactly the ten instants the manifest names", func(t *testing.T) {
		// 2 for the file saved twice, 2 for the one whose mtime decayed,
		// 2 for the one crossing a second boundary, 1 each for the four
		// written in a single second. Nothing from the nested worktree, and
		// nothing from any housekeeping file.
		if len(events) != 10 {
			for _, e := range events {
				t.Logf("  %s  %s  %s", e.Start.Format("2006-01-02 15:04:05.000000000"), e.State, e.Locator)
			}
			t.Fatalf("got %d events, want 10", len(events))
		}
	})

	t.Run("nine of the ten fall inside the session window", func(t *testing.T) {
		var inWindow int
		for _, e := range events {
			if !e.Start.Before(windowStart) && e.Start.Before(windowEnd) {
				inWindow++
			}
		}
		// Every instant but one: the decayed mtime, which landed the next day.
		if inWindow != 9 {
			t.Errorf("%d observations inside %s–%s, want 9",
				inWindow, windowStart.Format("15:04"), windowEnd.Format("15:04"))
		}
	})

	t.Run("no evidence leaks from a nested worktree copy", func(t *testing.T) {
		for _, e := range events {
			if strings.Contains(e.Locator, string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
				t.Errorf("event %+v was read from inside an excluded worktree directory", e)
			}
		}
	})

	t.Run("the session's own file spans birth to its final save, as two points", func(t *testing.T) {
		stamps := byFile("beacon2-face-granted.motn")
		if len(stamps) != 2 {
			t.Fatalf("beacon2-face-granted.motn produced %d events, want 2 (birth + mtime): %v", len(stamps), stamps)
		}
		first, second := stamps[0], stamps[1]
		if second.Before(first) {
			first, second = second, first
		}
		if want := fixtureTreeDay(6, 0, 14, 913427472); !first.Equal(want) {
			t.Errorf("earlier event at %s, want %s (the file's birth)",
				first.Format("15:04:05.000000000"), want.Format("15:04:05.000000000"))
		}
		if want := fixtureTreeDay(6, 50, 34, 742495669); !second.Equal(want) {
			t.Errorf("later event at %s, want %s (the file's final save)",
				second.Format("15:04:05.000000000"), want.Format("15:04:05.000000000"))
		}
	})

	t.Run("mtime decay would have lost this file's morning entirely", func(t *testing.T) {
		stamps := byFile("beacon3-face-granted.motn")
		var inMorningWindow, elsewhere int
		for _, s := range stamps {
			if !s.Before(windowStart) && s.Before(windowEnd) {
				inMorningWindow++
			} else {
				elsewhere++
			}
		}
		if inMorningWindow != 1 {
			t.Fatalf("beacon3-face-granted.motn has %d observations in the session window, want exactly 1 "+
				"(the birth) — its mtime moved to the next day, and a collector reading only mtime would "+
				"show nothing here at all", inMorningWindow)
		}
		if elsewhere != 1 {
			t.Errorf("%d observations for this file outside the window, want 1 (its decayed mtime)", elsewhere)
		}
	})

	t.Run("coincident birth and mtime produce one event, not two", func(t *testing.T) {
		for _, name := range []string{"panel2.af", "big-x-2.png", "big-check-2.png", "big-check-green-2.png"} {
			if got := len(byFile(name)); got != 1 {
				t.Errorf("%s produced %d events, want 1 (birth and mtime fall in the same second)", name, got)
			}
		}
	})

	t.Run("a genuine 657ms gap across a second boundary still counts as two", func(t *testing.T) {
		stamps := byFile("big-x-red-2.png")
		if len(stamps) != 2 {
			t.Fatalf("big-x-red-2.png produced %d events, want 2 (its birth and mtime land in "+
				"different Unix seconds): %v", len(stamps), stamps)
		}
		first, second := stamps[0], stamps[1]
		if second.Before(first) {
			first, second = second, first
		}
		// The property, stated so it cannot pass for the wrong reason: less
		// than a second apart, and yet in two different seconds. A collector
		// that compared elapsed time rather than the Unix second would call
		// this one event; a fixture that had rounded to whole seconds would
		// make the case untestable.
		if gap := second.Sub(first); gap >= time.Second {
			t.Errorf("the two stamps are %s apart, which is not the sub-second case this proves", gap)
		}
		if first.Unix() == second.Unix() {
			t.Errorf("both stamps land in Unix second %d; the boundary crossing was lost", first.Unix())
		}
	})

	t.Run("source documents and generated output are classified apart", func(t *testing.T) {
		class := make(map[string]Class, len(events))
		for _, e := range events {
			class[e.Locator] = Class(e.State)
		}
		for name, want := range map[string]Class{
			"beacon2-face-granted.motn": ClassSource,
			"big-x-2.png":               ClassOutput,
			"panel2.af":                 ClassOther,
		} {
			path := filepath.Join(root, "studio", name)
			if got := class[path]; got != want {
				t.Errorf("%s classified as %q, want %q", name, got, want)
			}
		}
	})

	t.Run("the compositor document joins to its own application", func(t *testing.T) {
		motn := filepath.Join(root, "studio", "beacon2-face-granted.motn")
		png := filepath.Join(root, "studio", "big-x-2.png")
		for _, e := range events {
			switch e.Locator {
			case motn:
				if e.Identity.App != "com.apple.motionapp" {
					t.Errorf("App = %q for the .motn, want com.apple.motionapp", e.Identity.App)
				}
			case png:
				// A rendered frame is not attributable to an application by
				// extension, and guessing one would corrupt the Biome join
				// silently rather than fail to make it.
				if e.Identity.App != "" {
					t.Errorf("App = %q for a .png, want empty", e.Identity.App)
				}
			}
		}
	})
}

// The corpus carries one of every kind of file this package refuses, so that
// "nothing was collected from here" is an assertion rather than an accident of
// what happened to be lying around on somebody's disk. Each of these is worth
// a measured figure in the package doc — 27,645 events from one .venv, 86
// SQLite sidecars putting seven unrelated repositories on a review screen — and
// none of them was reachable on a machine without that disk until now.
func TestFixtureTreeCollectsNothingFromHousekeeping(t *testing.T) {
	root, events := fixtureTreeEvents(t)
	for _, e := range events {
		rel, err := filepath.Rel(root, e.Locator)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", root, e.Locator, err)
		}
		switch {
		case strings.HasSuffix(rel, ".DS_Store"):
			t.Errorf("%s was collected; macOS's own metadata cache is not evidence", rel)
		case IsDatabaseSidecar(filepath.Base(rel)):
			t.Errorf("%s was collected; a SQLite sidecar cannot be a work product", rel)
		case strings.HasPrefix(rel, ".git"+string(filepath.Separator)),
			strings.HasPrefix(rel, ".venv"+string(filepath.Separator)),
			strings.HasPrefix(rel, "node_modules"+string(filepath.Separator)):
			t.Errorf("%s was collected from an excluded directory", rel)
		}
	}
}

// A fixture is only worth anything if it fails when the thing it describes
// changes, so the manifest itself is checked against what was built. This is
// the guard against a corpus that silently stops carrying a case — a line
// deleted, a timestamp edited into agreement with a rule it was written to
// refuse.
func TestFixtureTreeManifestAndTreeAgree(t *testing.T) {
	entries, err := fsfixture.Load(fixtureTreeManifest)
	if err != nil {
		t.Fatalf("Load %s: %v", fixtureTreeManifest, err)
	}
	if len(entries) != 13 {
		t.Errorf("the manifest describes %d files, want 13 (7 observed, 1 duplicated in a worktree, "+
			"5 kinds of housekeeping)", len(entries))
	}

	var sameSecond, differentSecond, decayed int
	for _, e := range entries {
		switch {
		case e.Birth.Unix() == e.Mtime.Unix():
			sameSecond++
		case e.Mtime.Sub(e.Birth) > 12*time.Hour:
			decayed++
		default:
			differentSecond++
		}
	}
	if sameSecond == 0 {
		t.Error("no file in the corpus has its birth and mtime inside one second, so the " +
			"one-event-not-two rule is not exercised")
	}
	if differentSecond == 0 {
		t.Error("no file in the corpus has two stamps in different seconds, so the " +
			"two-events rule is not exercised")
	}
	if decayed == 0 {
		t.Error("no file in the corpus has an mtime that decayed past its birth by more than " +
			"half a day, so the decay case realdata_test.go exists for is not exercised")
	}
}
