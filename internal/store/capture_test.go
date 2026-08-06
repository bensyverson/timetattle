package store_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// capture_run answers a question harvest_run structurally cannot: did the phase
// that PRESERVES the perishable sources succeed?
//
// Ingestion can be re-run for ever — it reads snapshots already on disk — so a
// lost ingestion costs a re-run. Capture is the only chance to hold knowledgeC
// and Biome before they prune themselves, and until 0007 a failed capture wrote
// no row of any kind. `timetattle status` counted the files still sitting in
// the snapshot directory from the last successful run and printed a healthy
// number. That is how the outage of 2026-07-30 lasted 44 hours.
//
// The property these tests hold is the standing one in CLAUDE.md, in its
// three-way form. A capture that ran and captured nothing new, a capture that
// looked and found nothing to capture, and a capture that never ran are three
// different facts, and no two of them may be stored alike.

var captureStart = time.Date(2026, 8, 1, 13, 4, 35, 0, time.UTC)

// aCapture is the ordinary successful capture, as a value tests can vary.
func aCapture() store.Capture {
	return store.Capture{
		Kind:       "knowledgec",
		Input:      "/Users/x/Library/Application Support/Knowledge/knowledgeC.db",
		StartedAt:  captureStart,
		FinishedAt: captureStart.Add(6 * time.Second),
		Status:     store.CaptureOK,
		FilesNew:   1,
		Bytes:      4_100_000,
	}
}

func record(t *testing.T, f fixture, c store.Capture) int64 {
	t.Helper()
	id, err := f.RecordCapture(context.Background(), c)
	if err != nil {
		t.Fatalf("RecordCapture(%s, %s): %v", c.Kind, c.Status, err)
	}
	return id
}

func TestARecordedCaptureReadsBackWhole(t *testing.T) {
	f := newStore(t)
	want := aCapture()
	want.FilesHeld = 12
	record(t, f, want)

	got, err := f.Captures(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Captures returned %d rows, want 1", len(got))
	}
	c := got[0]
	if c.Kind != want.Kind || c.Input != want.Input || c.Status != want.Status {
		t.Errorf("read back %+v, want kind/input/status from %+v", c, want)
	}
	if !c.StartedAt.Equal(want.StartedAt) || !c.FinishedAt.Equal(want.FinishedAt) {
		t.Errorf("read back %s..%s, want %s..%s", c.StartedAt, c.FinishedAt, want.StartedAt, want.FinishedAt)
	}
	if c.FilesNew != 1 || c.FilesHeld != 12 || c.Bytes != 4_100_000 {
		t.Errorf("read back %d new, %d held, %d bytes; want 1, 12, 4100000", c.FilesNew, c.FilesHeld, c.Bytes)
	}
	if c.ID == 0 {
		t.Error("read back a capture with no id")
	}
}

// TestNothingNewIsNotNothingRecorded is criterion t38 at the storage layer. A
// Biome capture that found every segment already held writes files_new = 0, and
// that row must be readable as a capture that HAPPENED. The alternative — no
// row, because nothing was written — is the defect, and it is indistinguishable
// from a capture that never ran.
func TestNothingNewIsNotNothingRecorded(t *testing.T) {
	f := newStore(t)
	unchanged := aCapture()
	unchanged.Kind = "biome"
	unchanged.FilesNew, unchanged.FilesHeld, unchanged.Bytes = 0, 13, 0
	record(t, f, unchanged)

	got, err := f.Captures(context.Background(), store.Filter{Source: "biome"})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a capture that wrote no files left %d rows, want 1", len(got))
	}
	if got[0].Status != store.CaptureOK || got[0].FilesHeld != 13 {
		t.Errorf("read back %+v; want an ok capture holding 13 files", got[0])
	}

	// And the kind that never ran has no rows, which is the other fact.
	none, err := f.Captures(context.Background(), store.Filter{Source: "motion-vault"})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(none) != 0 {
		t.Errorf("a kind that never ran returned %d rows, want none", len(none))
	}
}

// TestAbsentIsAnObservation is the third state. A harvest that looked at the
// Motion vault root and found no Motion installed has observed something. Left
// unrecorded it reads exactly like a harvest that died before reaching the
// vault, and the two call for opposite actions.
func TestAbsentIsAnObservation(t *testing.T) {
	f := newStore(t)
	absent := aCapture()
	absent.Kind = "motion-vault"
	absent.Input = "/Users/x/Movies/Motion Projects/Autosave Vault"
	absent.Status = store.CaptureAbsent
	absent.FilesNew, absent.Bytes = 0, 0
	record(t, f, absent)

	got, err := f.Captures(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 1 || got[0].Status != store.CaptureAbsent {
		t.Fatalf("read back %+v, want one absent capture", got)
	}
	if got[0].Error != "" {
		t.Errorf("an absent capture carries the error %q; nothing-there is not a failure", got[0].Error)
	}
}

func TestCapturesComeBackOldestFirst(t *testing.T) {
	f := newStore(t)
	for i := range 4 {
		c := aCapture()
		c.StartedAt = captureStart.Add(time.Duration(i) * time.Hour)
		c.FinishedAt = c.StartedAt.Add(time.Second)
		record(t, f, c)
	}
	got, err := f.Captures(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("Captures returned %d rows, want 4", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].StartedAt.Before(got[i-1].StartedAt) {
			t.Fatalf("row %d started before row %d; Captures must return the order they ran, "+
				"which is what makes the LAST row of a kind its most recent attempt", i, i-1)
		}
	}
}

func TestCapturesBoundOnStartedAt(t *testing.T) {
	f := newStore(t)
	old := aCapture()
	old.StartedAt = captureStart.Add(-48 * time.Hour)
	old.FinishedAt = old.StartedAt.Add(time.Second)
	record(t, f, old)
	record(t, f, aCapture())

	got, err := f.Captures(context.Background(), store.Filter{From: captureStart.Add(-time.Hour)})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 1 || !got[0].StartedAt.Equal(captureStart) {
		t.Errorf("From returned %d rows starting %v, want only the recent one", len(got), got)
	}
}

// TestRecordCaptureRefusesADishonestRow is the Go half of the constraints
// 0007 enforces in SQL. Both exist: the CHECKs stop a script or a future
// collector writing a shape nobody can read back, and these stop a caller
// getting a confusing SQLite error instead of a sentence.
func TestRecordCaptureRefusesADishonestRow(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	for _, c := range []struct {
		what    string
		capture func(store.Capture) store.Capture
		says    string
	}{
		{"no kind", func(c store.Capture) store.Capture { c.Kind = ""; return c }, "kind"},
		{"no input", func(c store.Capture) store.Capture { c.Input = ""; return c }, "read"},
		{"no start", func(c store.Capture) store.Capture { c.StartedAt = time.Time{}; return c }, "start"},
		{"no end", func(c store.Capture) store.Capture { c.FinishedAt = time.Time{}; return c }, "end"},
		{"an end before its start", func(c store.Capture) store.Capture {
			c.FinishedAt = c.StartedAt.Add(-time.Second)
			return c
		}, "before"},
		{"an unknown status", func(c store.Capture) store.Capture { c.Status = "running"; return c }, "running"},
		{"a failure with no reason", func(c store.Capture) store.Capture {
			c.Status = store.CaptureFailed
			return c
		}, "reason"},
		{"a partial with no reason", func(c store.Capture) store.Capture {
			c.Status = store.CapturePartial
			return c
		}, "reason"},
		{"a success carrying an error", func(c store.Capture) store.Capture {
			c.Error = "cannot read /x"
			return c
		}, "succeeded"},
		{"an absent carrying an error", func(c store.Capture) store.Capture {
			c.Status = store.CaptureAbsent
			c.Error = "cannot read /x"
			return c
		}, "succeeded"},
	} {
		_, err := f.RecordCapture(ctx, c.capture(aCapture()))
		if err == nil {
			t.Errorf("RecordCapture accepted %s", c.what)
			continue
		}
		if !strings.Contains(err.Error(), c.says) {
			t.Errorf("RecordCapture refused %s with %q, which does not mention %q", c.what, err, c.says)
		}
	}
}

// TestRecordCaptureTouchesNoOtherTable is the separation 0007 argues for, held
// as a property rather than as a comment: capture rows may not reach into the
// provenance chain events hang off.
func TestRecordCaptureTouchesNoOtherTable(t *testing.T) {
	f := newStore(t)
	record(t, f, aCapture())

	runs, err := f.Runs(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("recording a capture wrote %d harvest_run rows; it must write none", len(runs))
	}
	events, err := f.EventsIncludingSuperseded(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("EventsIncludingSuperseded: %v", err)
	}
	if len(events) != 0 {
		t.Errorf("recording a capture wrote %d events; it must write none", len(events))
	}
}

// TestCapturesAreReadableThroughAReadOnlyStore is what `timetattle status`
// needs: the check that exists because a harvest was broken may not itself
// write to, create or migrate the database it is checking.
func TestCapturesAreReadableThroughAReadOnlyStore(t *testing.T) {
	f := newStore(t)
	record(t, f, aCapture())

	ro, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()
	got, err := ro.Captures(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Captures through a read-only store: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("read %d captures through a read-only store, want 1", len(got))
	}
}
