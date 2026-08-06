package store_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// harvest_run is where "is the evidence still arriving?" is answered, and until
// now the only thing that asked was `timetattle status`, with raw SQL, from
// outside this package. That made cmd/timetattle the sole holder of knowledge
// about these column names, so a migration could break a file no migration test
// covers.
//
// Runs is the read accessor those queries collapse into. It returns rows and
// not a verdict: what "the latest run" or "how many failures since it last
// worked" means belongs to the reader asking, and a store that decided it here
// would have to be changed by every later reader who disagreed.

// began starts, writes nothing to, and closes a run, returning it.
func began(t *testing.T, f fixture, id string, finish func(*store.Run) error) *store.Run {
	t.Helper()
	ctx := context.Background()
	run, err := f.BeginRun(ctx, store.RunSpec{SourceID: id, Input: "/tmp/" + id})
	if err != nil {
		t.Fatalf("BeginRun(%s): %v", id, err)
	}
	if finish != nil {
		if err := finish(run); err != nil {
			t.Fatalf("closing the run for %s: %v", id, err)
		}
	}
	return run
}

func ok(t *testing.T) func(*store.Run) error {
	t.Helper()
	return func(r *store.Run) error { return r.Finish(context.Background()) }
}

func failed(t *testing.T, because string) func(*store.Run) error {
	t.Helper()
	return func(r *store.Run) error { return r.Fail(context.Background(), errors.New(because)) }
}

func runs(t *testing.T, f fixture, filter store.Filter) []store.Run {
	t.Helper()
	got, err := f.Runs(context.Background(), filter)
	if err != nil {
		t.Fatalf("Runs(%+v): %v", filter, err)
	}
	return got
}

// TestRunsReturnsEveryRecordedRunInTheOrderTheyBegan states the ordering the
// two queries in status depended on: harvest_run ids are assigned in the order
// runs began, so the last row for a source is its most recent attempt even when
// two runs share a started_at to the microsecond.
func TestRunsReturnsEveryRecordedRunInTheOrderTheyBegan(t *testing.T) {
	f := newStore(t)
	began(t, f, "biome-appusage", ok(t))
	began(t, f, "kc-backlight", failed(t, "no Full Disk Access"))
	began(t, f, "biome-appusage", ok(t))

	got := runs(t, f, store.Filter{})
	if len(got) != 3 {
		t.Fatalf("Runs returned %d runs, want 3", len(got))
	}
	wantOrder := []string{"biome-appusage", "kc-backlight", "biome-appusage"}
	for i, want := range wantOrder {
		if got[i].SourceID != want {
			t.Errorf("run %d is %s, want %s; runs are not in the order they began", i, got[i].SourceID, want)
		}
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID {
			t.Errorf("run %d has id %d, not above the previous %d", i, got[i].ID, got[i-1].ID)
		}
	}
}

// TestRunsReportsEverythingStatusReads is the column-by-column contract. Each
// of these was a column name spelled out in cmd/timetattle/status.go.
func TestRunsReportsEverythingStatusReads(t *testing.T) {
	f := newStore(t)
	run := began(t, f, "biome-appusage", nil)
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"))
	// The same event again: seen counts what the source returned, new counts
	// what survived dedup, and they are deliberately not equal.
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"))
	if err := run.Finish(context.Background()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got := runs(t, f, store.Filter{})
	if len(got) != 1 {
		t.Fatalf("Runs returned %d runs, want 1", len(got))
	}
	r := got[0]
	switch {
	case r.SourceID != "biome-appusage":
		t.Errorf("SourceID = %q", r.SourceID)
	case r.Input != "/tmp/biome-appusage":
		t.Errorf("Input = %q", r.Input)
	case r.Status != store.StatusOK:
		t.Errorf("Status = %q, want ok", r.Status)
	case r.EventsSeen != 2:
		t.Errorf("EventsSeen = %d, want 2", r.EventsSeen)
	case r.EventsNew != 1:
		t.Errorf("EventsNew = %d, want 1; the second write was a duplicate", r.EventsNew)
	case r.StartedAt.IsZero():
		t.Error("StartedAt is the zero time")
	case r.FinishedAt.IsZero():
		t.Error("FinishedAt is the zero time on a run that finished")
	case r.Error != "":
		t.Errorf("Error = %q on a run that succeeded", r.Error)
	}
}

// TestRunsCarriesTheReasonAFailedRunFailed. The incident status was written for
// was three identical failures whose only trace was six lines in a log file;
// the reason has to survive the read or the report cannot print it.
func TestRunsCarriesTheReasonAFailedRunFailed(t *testing.T) {
	f := newStore(t)
	began(t, f, "kc-backlight", failed(t, "knowledgeC.db: operation not permitted"))

	got := runs(t, f, store.Filter{})
	if len(got) != 1 {
		t.Fatalf("Runs returned %d runs, want 1", len(got))
	}
	if got[0].Status != store.StatusFailed {
		t.Errorf("Status = %q, want failed", got[0].Status)
	}
	if got[0].Error != "knowledgeC.db: operation not permitted" {
		t.Errorf("Error = %q, want the reason recorded", got[0].Error)
	}
}

// TestRunsLeavesARunningRunVisible. A run marked running is a harvest that died
// partway, and it is one of the two things status exits non-zero for. A reader
// that only ever saw closed runs could not report it.
func TestRunsLeavesARunningRunVisible(t *testing.T) {
	f := newStore(t)
	began(t, f, "biome-appusage", nil)

	got := runs(t, f, store.Filter{})
	if len(got) != 1 {
		t.Fatalf("Runs returned %d runs, want 1", len(got))
	}
	if got[0].Status != store.StatusRunning {
		t.Errorf("Status = %q, want running", got[0].Status)
	}
	if !got[0].FinishedAt.IsZero() {
		t.Errorf("FinishedAt = %s on a run that never finished; the zero time is what says it did not",
			stamp(got[0].FinishedAt))
	}
}

// TestRunsReportsNothingRatherThanZeroForASourceThatNeverRan. Absence of a row
// is not an observation: a source with no runs must come back as no rows, so
// that the caller can say "never run" rather than printing a zero that reads as
// a measurement.
func TestRunsReportsNothingRatherThanZeroForASourceThatNeverRan(t *testing.T) {
	f := newStore(t)
	began(t, f, "biome-appusage", ok(t))

	got := runs(t, f, store.Filter{Source: "motion-vault"})
	if len(got) != 0 {
		t.Fatalf("Runs for a source that never ran returned %d rows, want none", len(got))
	}
}

func TestRunsFiltersBySource(t *testing.T) {
	f := newStore(t)
	began(t, f, "biome-appusage", ok(t))
	began(t, f, "kc-backlight", ok(t))
	began(t, f, "biome-appusage", ok(t))

	got := runs(t, f, store.Filter{Source: "biome-appusage"})
	if len(got) != 2 {
		t.Fatalf("Runs for biome-appusage returned %d rows, want 2", len(got))
	}
	for _, r := range got {
		if r.SourceID != "biome-appusage" {
			t.Errorf("Runs returned a %s run under a biome-appusage filter", r.SourceID)
		}
	}
}

// TestRunsFiltersByWhenTheRunBegan. The span is over started_at, which is the
// only endpoint every run has: a run that is still going has no finished_at,
// and bounding on one would silently drop exactly the runs worth noticing.
func TestRunsFiltersByWhenTheRunBegan(t *testing.T) {
	f := newStore(t)
	began(t, f, "biome-appusage", nil)

	if got := runs(t, f, store.Filter{From: time.Now().Add(-time.Hour)}); len(got) != 1 {
		t.Errorf("Runs from an hour ago returned %d rows, want the run that just began", len(got))
	}
	if got := runs(t, f, store.Filter{From: time.Now().Add(time.Hour)}); len(got) != 0 {
		t.Errorf("Runs from an hour hence returned %d rows, want none", len(got))
	}
	if got := runs(t, f, store.Filter{To: time.Now().Add(-time.Hour)}); len(got) != 0 {
		t.Errorf("Runs to an hour ago returned %d rows, want none", len(got))
	}
	if got := runs(t, f, store.Filter{To: time.Now().Add(time.Hour)}); len(got) != 1 {
		t.Errorf("Runs to an hour hence returned %d rows, want the run that just began", len(got))
	}
}

// TestRunsOnAStoreNothingHasHarvested. Nothing recorded is not an error, and it
// is not a failure either; it is the state of every store on its first day.
func TestRunsOnAStoreNothingHasHarvested(t *testing.T) {
	f := newStore(t)
	got, err := f.Runs(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Runs on an unharvested store: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Runs on an unharvested store returned %d rows, want none", len(got))
	}
}
