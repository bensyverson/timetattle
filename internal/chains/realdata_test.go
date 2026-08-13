//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package chains

import (
	"context"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
)

// These tests validate the model against the real filesystem rather than
// against fixtures built to agree with it. They sit BESIDE the constructed
// cases in chains_test.go and neither substitutes for the other: this project
// has already shipped an over-general rule that passed against an entire live
// stream purely because no real case happened to exercise it.
//
// Every figure asserted below was taken with:
//
//	stat -f '%SB | %Sm | %N' -t '%Y-%m-%d %H:%M:%S' /Users/you/git/acorn/blender/*lightbar*
//	stat -f '%SB | %Sm | %N' -t '%Y-%m-%d %H:%M:%S' /Users/you/git/acorn/scanline/*
//	stat -f '%SB | %Sm | %N' -t '%Y-%m-%d %H:%M:%S' /Users/you/git/acorn/local/*.motn /Users/you/git/acorn/local/*.psd
//
// If one starts failing, the likely cause is that a file was touched again or
// a new sibling was saved. Re-derive with the commands above rather than
// loosening an assertion — a changed number here is evidence, not noise.

// realProject is the machine-specific repository these tests validate
// against, the same one internal/fsobserve, project/*.md and
// docs/content/docs cite. "acorn" is the pseudonym these tests and the
// figures above are written in terms of; ResolveReferenceProject maps it to
// the real path through this machine's own ~/.timetattle/config.json (task
// paDFg) rather than assuming a directory literally named "acorn" exists.
func realProject(t *testing.T) string {
	t.Helper()
	dir, err := config.ResolveReferenceProject("acorn")
	if err != nil {
		t.Skip(err.Error())
	}
	return dir
}

// realEvents walks the real repository exactly as a harvest would.
func realEvents(t *testing.T) []source.Event {
	t.Helper()
	events, err := fsobserve.New(realProject(t)).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated real repository")
	}
	return events
}

// find returns the chain in dir with the given root, or fails.
func find(t *testing.T, r Result, dir, wantRoot string) Chain {
	t.Helper()
	for _, c := range r.Chains {
		if c.Directory == dir && c.Root == wantRoot {
			return c
		}
	}
	var got []string
	for _, c := range r.Chains {
		if c.Directory == dir {
			got = append(got, c.Root)
		}
	}
	t.Fatalf("no chain rooted %q in %s; roots there: %v", wantRoot, dir, got)
	return Chain{}
}

func local(y, m, d, hh, mm, ss int) time.Time {
	return time.Date(y, time.Month(m), d, hh, mm, ss, 0, time.Local)
}

// sec truncates to the second, which is the resolution `stat` prints and the
// resolution every published figure was taken at. The real birthtimes carry
// nanoseconds — lightbar-top-filmic-rect.blend1 is born at 13:33:27.501051599 —
// and comparing those against a figure read off a terminal would fail for a
// reason that has nothing to do with the model. Nothing is rounded away in the
// derivation itself; only the assertion meets the published number where it was
// measured.
func sec(t time.Time) time.Time { return t.Truncate(time.Second) }

// TestRealLightbarChainIsBlenderAndReproducesThePublishedFigures is criterion
// Pdv, and it also settles a discrepancy in the record.
//
// project/private/2026-07-28-spike-findings.md names lightbar.psd on one line and a
// lightbar chain on the next, and the task calls the chain a Blender chain.
// Both are right, about two different files:
//
//	/Users/you/git/acorn/blender/lightbar.blend  born 2026-07-11 09:19:26, mtime identical
//	/Users/you/git/acorn/scanline/lightbar.psd   born 2026-07-11 09:55:54, mtime 23:41:06
//
// The chain is Blender. The .psd is the separate birth-to-mtime observation the
// same document cites to argue that gap is not a duration. They share a stem,
// sit in different directories, and are two unrelated pieces of work on one
// day — which is the strongest argument in the corpus for scoping clusters to a
// directory, because a model clustering on the name alone would have merged
// them.
//
// The published figures were taken over .blend alone, which is why this asserts
// against Extensions: [".blend"]. What the default produces instead is measured
// in TestRealLightbarChainWithBlenderBackups.
func TestRealLightbarChainIsBlenderAndReproducesThePublishedFigures(t *testing.T) {
	root := realProject(t)
	blender := filepath.Join(root, "blender")
	scanline := filepath.Join(root, "scanline")

	r := Reconstruct(realEvents(t), Options{Extensions: []string{".blend"}})
	c := find(t, r, blender, "lightbar")

	// The published table, to the second, from the birthtimes on disk.
	want := []struct {
		at  time.Time
		doc string
		gap time.Duration
	}{
		{local(2026, 7, 11, 9, 19, 26), "lightbar", 0},
		{local(2026, 7, 11, 9, 33, 38), "lightbar-top", 14*time.Minute + 12*time.Second},
		{local(2026, 7, 11, 9, 36, 26), "lightbar-top-filmic", 2*time.Minute + 48*time.Second},
		{local(2026, 7, 11, 13, 33, 43), "lightbar-top-filmic-rect-prefable", 237*time.Minute + 17*time.Second},
		{local(2026, 7, 11, 13, 40, 58), "lightbar-top-filmic-rect", 7*time.Minute + 15*time.Second},
		{local(2026, 7, 11, 16, 53, 50), "lightbar-top-filmic-fable", 192*time.Minute + 52*time.Second},
	}
	if len(c.Steps) < len(want) {
		t.Fatalf("got %d steps, want at least %d:\n%s", len(c.Steps), len(want), describe(r))
	}
	for i, w := range want {
		got := c.Steps[i]
		if !sec(got.At).Equal(w.at) {
			t.Errorf("step %d at %s, want %s", i, got.At.Format("2006-01-02 15:04:05.000"), w.at.Format("2006-01-02 15:04:05"))
		}
		if len(got.Documents) != 1 || got.Documents[0] != w.doc {
			t.Errorf("step %d documents = %v, want [%s]", i, got.Documents, w.doc)
		}
		if i > 0 {
			if gap := sec(got.At).Sub(sec(c.Steps[i-1].At)); gap != w.gap {
				t.Errorf("step %d gap = %s, want %s (exact, unrounded: %s)", i, gap, w.gap, got.Gap)
			}
		}
		if i == 0 && got.Gap != 0 {
			t.Errorf("first step carries a gap of %s, want none", got.Gap)
		}
	}
	t.Logf("published +14m/+3m/+237m/+7m/+193m reproduce as %s / %s / %s / %s / %s",
		sec(c.Steps[1].At).Sub(sec(c.Steps[0].At)), sec(c.Steps[2].At).Sub(sec(c.Steps[1].At)),
		sec(c.Steps[3].At).Sub(sec(c.Steps[2].At)), sec(c.Steps[4].At).Sub(sec(c.Steps[3].At)),
		sec(c.Steps[5].At).Sub(sec(c.Steps[4].At)))

	// The spike was written on 2026-07-28 and lightbar-top-filmic-2.blend was
	// born on 2026-07-29, so a seventh step now exists that the published table
	// could not have held. Recording it here keeps the two reconcilable.
	if len(c.Steps) > len(want) {
		for _, extra := range c.Steps[len(want):] {
			t.Logf("step after the published table: %s %v (+%s)",
				extra.At.Format("2006-01-02 15:04:05"), extra.Documents, extra.Gap)
			if extra.At.Before(local(2026, 7, 29, 0, 0, 0)) {
				t.Errorf("unexpected extra step on 2026-07-11 at %s: %v", extra.At, extra.Documents)
			}
		}
	}

	// And the discrepancy, resolved by measurement: the Photoshop lightbar is a
	// different chain in a different directory, and the model keeps them apart.
	// It has to be looked for in a run that admits .psd — the run above was
	// restricted to .blend to reproduce the published table.
	psd := find(t, Reconstruct(realEvents(t), Options{}), scanline, "lightbar")
	if psd.Directory == c.Directory {
		t.Fatal("the two lightbar chains are in the same directory; this test is not exercising the split")
	}
	for _, s := range psd.Steps {
		for _, l := range s.Locators {
			if filepath.Dir(l) != scanline {
				t.Errorf("Photoshop lightbar chain holds %s", l)
			}
		}
	}
	t.Logf("Blender lightbar: %d steps, %d documents, span %s, in %s",
		len(c.Steps), c.Documents(), c.Span(), c.Directory)
	t.Logf("Photoshop lightbar: %d steps, %d documents, span %s, in %s",
		len(psd.Steps), psd.Documents(), psd.Span(), psd.Directory)
}

// What the default model — every source extension, so Blender's own .blend1
// backups included — makes of the same directory.
//
// internal/fsobserve classifies .blend1 as source deliberately, and it changes
// the answer: two names first appear as a backup, earlier than the .blend that
// carries them, so two intervals shrink and one adjacent pair swaps order. Both
// readings are honest readings of the same disk and the difference is recorded
// rather than resolved by preference.
func TestRealLightbarChainWithBlenderBackups(t *testing.T) {
	blender := filepath.Join(realProject(t), "blender")
	r := Reconstruct(realEvents(t), Options{})
	c := find(t, r, blender, "lightbar")

	for i, s := range c.Steps {
		t.Logf("step %d  %s  %v  (+%s)  %v",
			i, s.At.Format("2006-01-02 15:04:05"), s.Documents, s.Gap, s.Locators)
	}

	byName := map[string]time.Time{}
	for _, s := range c.Steps {
		for _, d := range s.Documents {
			byName[d] = sec(s.At)
		}
	}
	// lightbar-top-filmic-rect.blend1 is born 13:33:27, before the .blend at
	// 13:40:58, so the document first appears at the backup.
	if got, want := byName["lightbar-top-filmic-rect"], local(2026, 7, 11, 13, 33, 27); !got.Equal(want) {
		t.Errorf("lightbar-top-filmic-rect first appears at %s, want %s (its .blend1)", got, want)
	}
	// lightbar-top-filmic-fable.blend1 is born 15:41:02, before the .blend at
	// 16:53:50.
	if got, want := byName["lightbar-top-filmic-fable"], local(2026, 7, 11, 15, 41, 2); !got.Equal(want) {
		t.Errorf("lightbar-top-filmic-fable first appears at %s, want %s (its .blend1)", got, want)
	}
	// Which reorders the published table: rect now precedes rect-prefable.
	if !byName["lightbar-top-filmic-rect"].Before(byName["lightbar-top-filmic-rect-prefable"]) {
		t.Error("expected the .blend1 to move rect ahead of rect-prefable")
	}

	// A .blend and its .blend1 are one document, so the step count must be
	// below the file count.
	files := 0
	for _, s := range c.Steps {
		files += len(s.Locators)
	}
	if files <= c.Documents() {
		t.Errorf("%d files across %d documents; the .blend/.blend1 merge did not happen", files, c.Documents())
	}
	t.Logf("default model: %d steps, %d documents, %d files", len(c.Steps), c.Documents(), files)
}

// TestRealDarkCrystalIsNotSplit is criterion gqe, against real files. These
// exist on this machine right now — this is an observation of not-split, not an
// absence of evidence:
//
//	/Users/you/git/acorn/local/dark-crystal.motn       born 2026-07-10 12:57:23
//	/Users/you/git/acorn/local/dark-crystal-v2.motn    born 2026-07-10 12:57:23
//	/Users/you/git/acorn/local/dark-crystal.psd        born 2026-07-10 14:09:54
//	/Users/you/git/acorn/local/dark-crystal-apex.motn  born 2026-07-10 14:46:10
func TestRealDarkCrystalIsNotSplit(t *testing.T) {
	dir := filepath.Join(realProject(t), "local")
	r := Reconstruct(realEvents(t), Options{})

	for _, c := range r.Chains {
		if c.Root == "dark" {
			t.Fatalf("a chain rooted %q exists in %s: the name was split at its dash", c.Root, c.Directory)
		}
	}

	c := find(t, r, dir, "dark-crystal")
	got := names(c)
	sort.Strings(got)
	want := []string{"dark-crystal", "dark-crystal-apex", "dark-crystal-v2"}
	if len(got) != len(want) {
		t.Fatalf("chain names %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chain names %v, want %v", got, want)
			break
		}
	}

	// dark-crystal.motn and dark-crystal.psd are one document under two
	// extensions, so the chain holds more files than names.
	files := 0
	for _, s := range c.Steps {
		files += len(s.Locators)
	}
	if files < 4 {
		t.Errorf("%d files behind %d documents, want at least 4 (.motn and .psd both present)", files, len(got))
	}
	for i, s := range c.Steps {
		t.Logf("step %d  %s  %v  (+%s)", i, s.At.Format("2006-01-02 15:04:05"), s.Documents, s.Gap)
	}
}

// TestRealScanlineSimultaneousBirthsAreOneAction is criterion QN2.
//
// The record says six scanline files share a 16:15 timestamp. Seven do, all in
// /Users/you/git/acorn/scanline, all born 2026-07-11 16:15:22: scanline-native,
// scanline-card-granted, scanline-card-denied, scanline-face-granted,
// scanline-face-denied, scanline2-face-denied, scanline3-face-denied.
func TestRealScanlineSimultaneousBirthsAreOneAction(t *testing.T) {
	dir := filepath.Join(realProject(t), "scanline")
	r := Reconstruct(realEvents(t), Options{})
	c := find(t, r, dir, "scanline")

	at := local(2026, 7, 11, 16, 15, 22)
	var burst *Step
	for i := range c.Steps {
		if sec(c.Steps[i].At).Equal(at) {
			burst = &c.Steps[i]
		}
		if i > 0 && c.Steps[i].Gap <= 0 {
			t.Errorf("step %d has gap %s; collapsing a burst must leave no zero intervals", i, c.Steps[i].Gap)
		}
	}
	if burst == nil {
		t.Fatalf("no step at %s; steps are:\n%s", at, describe(r))
	}
	if len(burst.Documents) != 7 {
		t.Errorf("the 16:15:22 burst names %d documents, want 7: %v", len(burst.Documents), burst.Documents)
	}

	// One action, not seven. The whole chain must have far fewer steps than
	// documents, which is the property the criterion is really about.
	if len(c.Steps) >= c.Documents() {
		t.Errorf("%d steps for %d documents; simultaneous births were not collapsed", len(c.Steps), c.Documents())
	}
	for i, s := range c.Steps {
		t.Logf("step %d  %s  %d document(s)  (+%s)  %v",
			i, s.At.Format("2006-01-02 15:04:05"), len(s.Documents), s.Gap, s.Documents)
	}
}

// The whole-corpus shape. A model that chains everything is as useless as one
// that chains nothing, so this reports the distribution rather than asserting a
// single number, and fails only on the two degenerate outcomes.
func TestRealCorpusDistribution(t *testing.T) {
	events := realEvents(t)
	r := Reconstruct(events, Options{})

	t.Logf("events walked: %d; source-class events considered: %d", len(events), r.EventsConsidered)
	t.Logf("documents: %d; in a chain: %d; in no chain: %d", r.Documents, r.Chained, len(r.Unchained))
	t.Logf("chains: %d", len(r.Chains))

	hist := map[int]int{}
	steps := 0
	for _, c := range r.Chains {
		hist[len(c.Steps)]++
		steps += len(c.Steps)
	}
	lengths := make([]int, 0, len(hist))
	for n := range hist {
		lengths = append(lengths, n)
	}
	sort.Ints(lengths)
	for _, n := range lengths {
		t.Logf("  chains of %d steps: %d", n, hist[n])
	}
	for _, c := range r.Chains {
		t.Logf("  %s  root=%s  %d steps  %d documents  span %s  from %s",
			c.Directory, c.Root, len(c.Steps), c.Documents(), c.Span(),
			c.Steps[0].At.Format("2006-01-02 15:04"))
	}

	if len(r.Chains) == 0 {
		t.Fatal("no chains recovered from a corpus known to hold several")
	}
	if r.Chained == r.Documents {
		t.Errorf("every one of %d documents landed in a chain; a model that chains everything measures nothing",
			r.Documents)
	}
	if r.Chained == 0 {
		t.Fatal("no document landed in a chain")
	}
	// Accounting has to close: every document is either chained or named as
	// unchained, with nothing falling off the edge.
	if r.Chained+len(r.Unchained) != r.Documents {
		t.Errorf("%d chained + %d unchained != %d documents", r.Chained, len(r.Unchained), r.Documents)
	}
	// Steps must be strictly fewer than documents across the corpus, or
	// simultaneous births are not being collapsed anywhere.
	if steps >= r.Chained {
		t.Errorf("%d steps for %d chained documents; nothing collapsed", steps, r.Chained)
	}
}

// The published figures were taken over one project. Reconstruct is given
// events, not roots, so the corpus is whatever the caller read from the store —
// and the registry is what decides that. At the time of writing nothing is
// registered on this machine and the store holds no fs-observations events at
// all, so this records the reachable population rather than pretending a
// registry-wide figure exists.
func TestRealRegistryPopulation(t *testing.T) {
	root := realProject(t)
	events := realEvents(t)

	byDir := map[string]int{}
	for _, e := range events {
		if e.State == string(fsobserve.ClassSource) {
			byDir[filepath.Dir(e.Locator)]++
		}
	}
	dirs := make([]string, 0, len(byDir))
	for d := range byDir {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)
	t.Logf("%s: %d directories holding source documents", root, len(dirs))
	for _, d := range dirs {
		t.Logf("  %s: %d source events", d, byDir[d])
	}
	if len(dirs) == 0 {
		t.Fatal("no directory in the real project holds a source document")
	}
}
