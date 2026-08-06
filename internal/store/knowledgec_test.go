package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/knowledgec"
	"github.com/bensyverson/timetattle/internal/store"
)

// The two behaviours knowledgeC collection has to have, proved against the store
// rather than argued for: re-reading an overlapping window inserts nothing, and a
// row that appears in a later snapshot than the work it describes is still
// captured. Both are properties of the pair, so neither can be tested inside
// internal/knowledgec alone.
//
// Each is written twice. This file holds the SYNTHETIC halves: deterministic,
// describing rows a real snapshot may not happen to hold today, and running on
// any machine. The real halves are in knowledgec_live_realdata_test.go, behind
// `//go:build realdata`, because they read whatever the last harvest on this
// particular machine wrote. Neither substitutes for the other, which is why the
// file was split rather than one side deleted.

// runFor begins a run naming the source being read. The shared fixture.run helper
// records a Biome harvest, and provenance that says the wrong thing is worse than
// no provenance at all.
func (f fixture) runFor(t *testing.T, sourceID, input string) *store.Run {
	t.Helper()
	run, err := f.BeginRun(context.Background(), store.RunSpec{SourceID: sourceID, Input: input})
	if err != nil {
		t.Fatalf("BeginRun for %s: %v", sourceID, err)
	}
	return run
}

// kcRow is one ZOBJECT row for a fixture snapshot.
//
// This duplicates the builder in internal/knowledgec's own tests, deliberately:
// a fixture exported from one test package to another is production API that
// exists only for tests, and the alternative — proving the store's dedup against
// a fixture the collector's own tests also depend on — would let one bug hide the
// other.
type kcRow struct {
	stream string
	start  time.Time
	end    time.Time
	value  int
}

func writeKnowledgeC(t *testing.T, path string, rows ...kcRow) string {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE ZOBJECT (
		Z_PK INTEGER PRIMARY KEY,
		ZSTREAMNAME TEXT,
		ZVALUESTRING TEXT,
		ZVALUEINTEGER INTEGER,
		ZSTARTDATE REAL,
		ZENDDATE REAL)`); err != nil {
		t.Fatalf("create ZOBJECT: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUEINTEGER, ZSTARTDATE, ZENDDATE) VALUES (?, ?, ?, ?)`,
			r.stream, r.value,
			float64(r.start.Unix()-978307200), float64(r.end.Unix()-978307200)); err != nil {
			t.Fatalf("insert a %s row: %v", r.stream, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	return path
}

func lit(start, end time.Time) kcRow {
	return kcRow{stream: "/display/isBacklit", start: start, end: end, value: 1}
}

func dark(start, end time.Time) kcRow {
	return kcRow{stream: "/display/isBacklit", start: start, end: end, value: 0}
}

// TestReReadingAKnowledgeCSnapshotInsertsNothing is the acceptance test for the
// whole no-tailing design. The rows chosen are the ones that make it hard: a pair
// of exact duplicates, which must collapse to one row, and a pair of
// contradictory readings sharing endpoints, which must not.
func TestReReadingAKnowledgeCSnapshotInsertsNothing(t *testing.T) {
	base := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	path := writeKnowledgeC(t, filepath.Join(t.TempDir(), "kc.db"),
		lit(base, base.Add(30*time.Minute)),
		dark(base.Add(30*time.Minute), base.Add(time.Hour)),
		// Three identical rows, as 2026-07-10 15:22:32 carries in reality.
		dark(base.Add(time.Hour), base.Add(time.Hour)),
		dark(base.Add(time.Hour), base.Add(time.Hour)),
		dark(base.Add(time.Hour), base.Add(time.Hour)),
		// An on row and an off row with identical endpoints, as 2026-07-14
		// 09:01:12 carries in reality. These are two observations.
		lit(base.Add(2*time.Hour), base.Add(2*time.Hour)),
		dark(base.Add(2*time.Hour), base.Add(2*time.Hour)),
	)

	ctx := context.Background()
	src := knowledgec.NewBacklight(path)
	events, err := src.Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 7 {
		t.Fatalf("collected %d events, want all 7 rows; the collector must not tidy the source", len(events))
	}

	f := newStore(t)

	first := f.runFor(t, src.ID(), path)
	inserted := f.write(t, first, events...)
	rows := f.count(t)
	if err := first.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// events_new < events_seen on a fresh store is correct here rather than
	// tolerable: two of the three identical rows carry no information the first
	// does not.
	if inserted != 5 || rows != 5 {
		t.Fatalf("first harvest inserted %d events into %d rows, want 5 and 5: "+
			"three identical rows are one observation and the contradictory pair is two", inserted, rows)
	}

	second := f.runFor(t, src.ID(), path)
	again := f.write(t, second, events...)
	if again != 0 {
		t.Errorf("re-reading the same snapshot inserted %d rows, want 0", again)
	}
	if after := f.count(t); after != rows {
		t.Errorf("the store grew from %d rows to %d on a re-read", rows, after)
	}
	if err := second.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	run, err := f.LoadRun(ctx, second.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if run.EventsSeen != len(events) || run.EventsNew != 0 {
		t.Errorf("the second run recorded seen=%d new=%d, want seen=%d new=0",
			run.EventsSeen, run.EventsNew, len(events))
	}

	// knowledgeC emits no unbounded events, so nothing here is ever superseded
	// and the canonical read is the whole table.
	if canonical := len(f.all(t)); canonical != rows {
		t.Errorf("the canonical read returns %d of %d rows; knowledgeC has no horizons to supersede", canonical, rows)
	}
}

// TestALateArrivingKnowledgeCRowIsStillCaptured is the other half. knowledgeC
// writes rows well after the events they describe — /app/usage was measured 23
// hours behind /display/isBacklit — so a harvest that advanced a high-water mark
// to the newest event it had seen would step over everything that arrived
// afterwards, permanently and silently.
//
// Snapshot A is missing a row for an event that had already happened. Snapshot B,
// taken later, has it. The window handed to the second collection deliberately
// reaches back before the late row rather than starting where A ended.
func TestALateArrivingKnowledgeCRowIsStillCaptured(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	late := kcRow{stream: "/display/isBacklit", start: base.Add(-time.Hour), end: base.Add(-30 * time.Minute), value: 1}
	present := []kcRow{
		lit(base, base.Add(30*time.Minute)),
		dark(base.Add(30*time.Minute), base.Add(time.Hour)),
	}

	a := writeKnowledgeC(t, filepath.Join(dir, "a.db"), present...)
	b := writeKnowledgeC(t, filepath.Join(dir, "b.db"), append([]kcRow{late}, present...)...)

	ctx := context.Background()
	f := newStore(t)

	fromA, err := knowledgec.NewBacklight(a).Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("Collect from A: %v", err)
	}
	runA := f.runFor(t, "knowledgec-backlit", a)
	if n := f.write(t, runA, fromA...); n != len(fromA) {
		t.Fatalf("the first harvest stored %d of %d events", n, len(fromA))
	}
	if err := runA.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	rowsAfterA := f.count(t)

	// The guard that keeps this test from being vacuous: the late row ended
	// before everything the first harvest saw, so a watermark taken from A would
	// have excluded it.
	newest := fromA[0].Anchor()
	for _, e := range fromA {
		if e.Anchor().After(newest) {
			newest = e.Anchor()
		}
	}
	if !late.end.Before(newest) {
		t.Fatalf("the late row ends %s, which is not before the newest event from A at %s; "+
			"this fixture does not describe late arrival", late.end, newest)
	}

	// A generous overlapping window, which is the whole mechanism. It is not
	// derived from what the previous harvest saw.
	since := base.Add(-2 * time.Hour)
	fromB, err := knowledgec.NewBacklight(b).Collect(ctx, since)
	if err != nil {
		t.Fatalf("Collect from B: %v", err)
	}
	if len(fromB) != 3 {
		t.Fatalf("collected %d events from B, want 3", len(fromB))
	}
	runB := f.runFor(t, "knowledgec-backlit", b)
	newRows := f.write(t, runB, fromB...)
	if err := runB.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if newRows != 1 {
		t.Errorf("the second harvest inserted %d rows, want only the late one", newRows)
	}
	if got := f.count(t); got != rowsAfterA+1 {
		t.Errorf("the store holds %d rows, want %d; nothing from A may be duplicated", got, rowsAfterA+1)
	}

	var found bool
	for _, e := range f.all(t) {
		if e.Start.Equal(late.start) && e.End.Equal(late.end) && e.State == "on" {
			found = true
		}
	}
	if !found {
		t.Errorf("the late row %s–%s is not in the store", late.start, late.end)
	}
}
