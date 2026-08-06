package fsobserve

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// This file is the constructed half of task Ows1j's measurement — see
// project/private/2026-08-01-locator-dedup.md for the real-data half it sits beside,
// and for why both are needed rather than either alone.
//
// Ows1j was raised on the premise that one physical file registered twice —
// once by its real path, once through a symlink — writes TWO rows for one
// save, inflating the evidence count a sitting's strength is read off. Within
// a single Collect that cannot happen: normalizedRoots drops the duplicate
// root, which
// TestCollectVisitsAFileOnceWhenARootIsRegisteredBothDirectlyAndAsASymlink
// already pins. ACROSS RUNS nothing dedupes the roots, because the two runs
// never see each other.
//
// These tests measure what actually happens across runs, and the answer is
// not two rows. event_identity (0001_initial.sql) keys on (source_id, shape,
// started_at, ended_at, app, document, state) and deliberately excludes
// locator, so the two observations are the SAME identity: the second run's
// insert hits ON CONFLICT DO NOTHING and adds nothing at all. The guard the
// task asked for is already structural. What is not obvious, and is the
// reason these tests exist, is that it is structural for a reason that has
// nothing to do with symlinks and could be removed by a future change to the
// index without anything else in this package noticing.
//
// The mutation that proves they are load-bearing rather than decorative: add
// `locator` to the event_identity index in 0001_initial.sql and
// TestOneFileHarvestedByTwoRegisteredPathsAcrossRunsYieldsOneRow fails with
// two rows for one save — the exact inflation Ows1j was raised about.
// Recorded with its output in §6 of the document above.

// storeForTest opens a real store — real migrations, real schema, no
// hand-copied DDL — in a temporary directory.
func storeForTest(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "timetattle.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// harvest writes one batch of events as one complete harvest run, the way
// cmd/timetattle's ingest does, and returns how many rows were new.
func harvest(t *testing.T, st *store.Store, input string, events []source.Event) int {
	t.Helper()
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: ID, Input: input, Since: time.Time{}})
	if err != nil {
		t.Fatalf("begin run for %s: %v", input, err)
	}
	n, err := run.Write(ctx, events)
	if err != nil {
		t.Fatalf("write %d events from %s: %v", len(events), input, err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish run for %s: %v", input, err)
	}
	return n
}

// storedEvents reads the table rather than the canonical view, because this
// is a question about what was WRITTEN, not about what a reader collapses.
func storedEvents(t *testing.T, st *store.Store) []source.Event {
	t.Helper()
	events, err := st.EventsIncludingSuperseded(context.Background(), store.Filter{Source: ID})
	if err != nil {
		t.Fatalf("read events back: %v", err)
	}
	return events
}

// TestOneFileHarvestedByTwoRegisteredPathsAcrossRunsYieldsOneRow is task
// Ows1j's criterion scL, measured rather than assumed.
//
// Two harvests, the first with the project registered by its real path and
// the second with it registered through a symlinked alias — the across-runs
// case normalizedRoots cannot reach, because the two runs never see each
// other. The two Collects genuinely disagree about the locator (that is the
// invariant TAKiT proved by mutation, asserted here rather than worked
// around) and the store still holds exactly one row afterwards.
func TestOneFileHarvestedByTwoRegisteredPathsAcrossRunsYieldsOneRow(t *testing.T) {
	real, link, file := symlinkedRoot(t)

	viaReal := collect(t, real)
	viaLink := collect(t, link)
	if len(viaReal) == 0 || len(viaLink) == 0 {
		t.Fatalf("the fixture produced no events (%d by the real path, %d through the alias)", len(viaReal), len(viaLink))
	}
	if len(viaReal) != len(viaLink) {
		t.Fatalf("got %d events by the real path and %d through the alias; the two runs must observe the same file the same number of times",
			len(viaReal), len(viaLink))
	}
	// The premise. If these ever agree, this test is measuring nothing and
	// the question it belongs to has gone away.
	if viaReal[0].Locator == viaLink[0].Locator {
		t.Fatalf("both runs reported Locator %q; the two registrations are supposed to differ, which is what makes the dedup question real",
			viaReal[0].Locator)
	}

	st := storeForTest(t)
	first := harvest(t, st, real, viaReal)
	if first != len(viaReal) {
		t.Fatalf("the first harvest wrote %d rows for %d events into an empty store, want %d",
			first, len(viaReal), len(viaReal))
	}

	second := harvest(t, st, link, viaLink)
	if second != 0 {
		t.Errorf("the second harvest wrote %d new rows for the same file reached through an alias, want 0", second)
	}

	rows := storedEvents(t, st)
	if len(rows) != len(viaReal) {
		t.Fatalf("the store holds %d rows for a file harvested twice through two paths, want %d: %+v",
			len(rows), len(viaReal), rows)
	}
	want := filepath.Join(real, file)
	for _, e := range rows {
		if e.Locator != want {
			t.Errorf("surviving Locator = %q, want %q — the row that was written first keeps its locator",
				e.Locator, want)
		}
	}
}

// TestBothRegisteredPathsResolveToTheSameProject is why the surviving row may
// keep whichever locator it likes without anything downstream going wrong,
// and it is the half of this question Ls0XC could not settle because no
// directory had been registered on this machine yet.
//
// Attribution does not compare a locator against a list of registered path
// STRINGS. internal/registry.Resolve climbs the filesystem from the locator's
// directory reading a marker FILE, and os.ReadFile follows symlinks, so an
// alias and its target reach the same marker on disk and therefore the same
// project. A locator spelled through a symlink is not a misattribution risk;
// a locator naming a path that has since STOPPED EXISTING is, and that is a
// different failure needing a different fix.
func TestBothRegisteredPathsResolveToTheSameProject(t *testing.T) {
	real, link, file := symlinkedRoot(t)

	if _, err := registry.Register(registry.Registration{
		Dir: real, Client: "acme", Project: "widget",
	}); err != nil {
		t.Fatalf("register %s: %v", real, err)
	}

	for _, dir := range []string{real, link} {
		locator := filepath.Join(dir, file)
		m, ok, err := registry.Resolve(filepath.Dir(locator))
		if err != nil {
			t.Fatalf("resolve %s: %v", locator, err)
		}
		if !ok {
			t.Fatalf("%s resolved to no project, but the marker is in the directory it names", locator)
		}
		if m.Client != "acme" || m.Project != "widget" {
			t.Errorf("%s resolved to %s/%s, want acme/widget", locator, m.Client, m.Project)
		}
	}
}
