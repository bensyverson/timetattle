//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

// package fsobserve_test, not fsobserve: config.Config depends on
// internal/sessions, which itself depends on internal/fsobserve, so
// resolving the reference-project pseudonym here through internal/config
// (task paDFg) would make fsobserve import itself indirectly if this file
// stayed inside package fsobserve. Every symbol this file touches was
// already exported, so the only cost is qualifying them.
package fsobserve_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/fsobserve"
)

// realProject is the machine-specific repository this test validates
// against. It is not portable — that is the point of the skip below — and it
// is the same repository project/*.md and docs/content/docs/sources.md
// already cite for the 2026-07-28 06:00–06:50 Motion session. "acorn" is the
// pseudonym; ResolveReferenceProject maps it to the real path through this
// machine's own ~/.timetattle/config.json rather than assuming a directory
// literally named "acorn" exists.
func realProject(t *testing.T) string {
	t.Helper()
	dir, err := config.ResolveReferenceProject("acorn")
	if err != nil {
		t.Skip(err.Error())
	}
	return dir
}

// TestRealFilesystemWalkRecoversMotionSession is criterion yYu: transcript
// inference scores 2026-07-28 06:00–06:50 as exactly zero Claude activity,
// yet a real Motion session happened in that window. This is what proves
// this collector makes it visible, against the real filesystem rather than a
// fixture built to already agree with the claim.
//
// The real files behind every assertion below (see
// /Users/you/git/acorn/scanline, read with `stat -f "%SB %Sm"`, local zone):
//
//	scanline2-face-granted.motn  born 06:00:14.913427472  mtime 06:50:34.742495669
//	scanline3-face-granted.motn  born 06:00:14.913427472  mtime 2026-07-29 13:17:56 (a day later — mtime alone would have lost this morning entirely)
//	chrome2.af                   born 06:41:36.799256773  mtime 06:41:36.984793537  (same second, one event)
//	big-x-2.png                  born 06:42:13.515838756  mtime 06:42:13.515962377  (same second, one event)
//	big-check-2.png              born 06:42:24.305116437  mtime 06:42:24.305286806  (same second, one event)
//	big-x-red-2.png              born 06:43:11.536758418  mtime 06:43:12.194606109  (crosses a second boundary, 658ms apart — two events)
//	big-check-green-2.png        born 06:48:09.154229300  mtime 06:48:09.205948420  (same second, one event)
//
// If this test starts failing, the most likely cause is that one of these
// files has been touched again since this was written — which would itself
// be the mtime-decay failure mode this package exists to work around, so
// re-derive the numbers with `stat -f "%SB %Sm" -t "%Y-%m-%d_%H:%M:%S"`
// rather than loosening the assertion.
func TestRealFilesystemWalkRecoversMotionSession(t *testing.T) {
	dir := realProject(t)

	events, err := fsobserve.New(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated real repository")
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v does not validate: %v", e, err)
		}
	}

	windowStart := time.Date(2026, 7, 28, 6, 0, 0, 0, time.Local)
	// The session's own last write, scanline2-face-granted's mtime, lands at
	// 06:50:34 — past the rounded "06:50" the rest of this project uses to
	// describe the session in prose. The window here is drawn a minute past
	// that real instant rather than at the rounded boundary.
	windowEnd := time.Date(2026, 7, 28, 6, 51, 0, 0, time.Local)

	t.Run("at least one observation falls inside the session window", func(t *testing.T) {
		var inWindow int
		for _, e := range events {
			if !e.Start.Before(windowStart) && e.Start.Before(windowEnd) {
				inWindow++
			}
		}
		if inWindow == 0 {
			t.Fatal("no filesystem observation falls inside 2026-07-28 06:00–06:51, " +
				"which means this collector fails the one validation it exists to pass: " +
				"recovering a Motion session that transcript-based inference scores as zero")
		}
		t.Logf("%d observations inside the session window", inWindow)
	})

	t.Run("no evidence leaks from a nested worktree copy", func(t *testing.T) {
		// acorn contains at least one agent worktree under .claude/worktrees
		// holding duplicate copies of these same scanline files. If the
		// exclusion in Collect is not working, the assertions below about
		// exact counts per real file would fail in the opposite direction —
		// too many, not too few.
		for _, e := range events {
			if strings.Contains(e.Locator, string(filepath.Separator)+"worktrees"+string(filepath.Separator)) {
				t.Errorf("event %+v was read from inside an excluded worktree directory", e)
			}
		}
	})

	byFile := func(name string) []time.Time {
		var out []time.Time
		want := filepath.Join(dir, "scanline", name)
		for _, e := range events {
			if e.Locator == want {
				out = append(out, e.Start)
			}
		}
		return out
	}

	t.Run("the session's own file spans birth to its final save, as two points", func(t *testing.T) {
		stamps := byFile("scanline2-face-granted.motn")
		if len(stamps) != 2 {
			t.Fatalf("scanline2-face-granted.motn produced %d events, want 2 (birth + mtime): %v", len(stamps), stamps)
		}
		first, second := stamps[0], stamps[1]
		if second.Before(first) {
			first, second = second, first
		}
		if got := first.Format("15:04:05"); got != "06:00:14" {
			t.Errorf("earlier event at %s, want 06:00:14 (the file's birth)", got)
		}
		if got := second.Format("15:04:05"); got != "06:50:34" {
			t.Errorf("later event at %s, want 06:50:34 (the file's final save)", got)
		}
	})

	t.Run("mtime decay would have lost this file's morning entirely", func(t *testing.T) {
		stamps := byFile("scanline3-face-granted.motn")
		var inMorningWindow, elsewhere int
		for _, s := range stamps {
			if !s.Before(windowStart) && s.Before(windowEnd) {
				inMorningWindow++
			} else {
				elsewhere++
			}
		}
		if inMorningWindow != 1 {
			t.Fatalf("scanline3-face-granted.motn has %d observations in the session window, want exactly 1 (the birth) — "+
				"its mtime moved to the next day, and a collector reading only mtime would show nothing here at all", inMorningWindow)
		}
		if elsewhere == 0 {
			t.Error("expected a second observation for this file outside the window (its decayed mtime); found none")
		}
	})

	t.Run("coincident birth and mtime produce one event, not two", func(t *testing.T) {
		for _, name := range []string{"chrome2.af", "big-x-2.png", "big-check-2.png", "big-check-green-2.png"} {
			if got := len(byFile(name)); got != 1 {
				t.Errorf("%s produced %d events, want 1 (birth and mtime fall in the same second)", name, got)
			}
		}
	})

	t.Run("a genuine 658ms gap across a second boundary still counts as two", func(t *testing.T) {
		stamps := byFile("big-x-red-2.png")
		if len(stamps) != 2 {
			t.Fatalf("big-x-red-2.png produced %d events, want 2 (its birth and mtime land in different Unix seconds): %v", len(stamps), stamps)
		}
	})

	t.Run("source documents and generated output are classified apart", func(t *testing.T) {
		compClass := make(map[string]fsobserve.Class)
		for _, e := range events {
			compClass[e.Locator] = fsobserve.Class(e.State)
		}
		motn := filepath.Join(dir, "scanline", "scanline2-face-granted.motn")
		png := filepath.Join(dir, "scanline", "big-x-2.png")
		if compClass[motn] != fsobserve.ClassSource {
			t.Errorf("scanline2-face-granted.motn classified as %q, want %q", compClass[motn], fsobserve.ClassSource)
		}
		if compClass[png] != fsobserve.ClassOutput {
			t.Errorf("big-x-2.png classified as %q, want %q", compClass[png], fsobserve.ClassOutput)
		}
	})

	t.Run("the Motion composition joins to its own application", func(t *testing.T) {
		motn := filepath.Join(dir, "scanline", "scanline2-face-granted.motn")
		for _, e := range events {
			if e.Locator == motn && e.Identity.App != "com.apple.motionapp" {
				t.Errorf("App = %q, want com.apple.motionapp", e.Identity.App)
			}
		}
	})
}
