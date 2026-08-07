package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// This file guards the ALLOCATION SHAPE of the event read, which is a
// different property from what it returns and is not covered by any test that
// only checks values.
//
// Why it needs guarding: reading the whole store is the single largest
// allocator in a `timetattle metrics` pass, because it is the one loop that
// runs once per row over a corpus of a million of them. A scan destination
// declared inside that loop has its address taken by rows.Scan, so it escapes
// and is heap-allocated on every iteration — ten objects per row, invisible in
// the returned value and invisible to every correctness test. The cost lands
// as garbage-collector time somewhere else entirely, which is exactly the kind
// of regression nobody attributes correctly after the fact.
//
// # The ceiling calibrates itself
//
// A hard number here would be a guard against one version of one SQL driver.
// The driver allocates per row on its own account — it decodes each TEXT
// column into a fresh Go string, and nothing this repository does can change
// that — so the test measures the driver's own per-row cost first, over the
// same table and the same eleven columns through database/sql directly, and
// then requires that reading through the store costs no more per row than
// that plus a small constant. A driver upgrade moves both sides together and
// the test keeps meaning the same thing.

// eventAllocRows is how many events the fixture store holds for this test. It
// is large enough that a per-row cost is not lost in the fixed cost of opening
// a connection and planning a query, and small enough to build in well under a
// second.
const eventAllocRows = 4000

// syntheticEventStore writes n point events, one a second apart, and returns a
// read-only handle on the result. Points rather than intervals so that every
// row exercises the same NULL-handling path, and one source so the read is a
// straight scan of the whole relation.
func syntheticEventStore(t *testing.T, n int) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle", "timetattle.db")
	w, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	ctx := context.Background()
	run, err := w.BeginRun(ctx, store.RunSpec{SourceID: "fsobserve", Input: "/synthetic/alloc"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	base := time.Date(2025, time.March, 3, 12, 0, 0, 0, time.UTC)
	events := make([]source.Event, 0, n)
	for i := range n {
		events = append(events, source.NewPoint(
			base.Add(time.Duration(i)*time.Second),
			source.Identity{App: "Editor", Document: fmt.Sprintf("note-%d.md", i)},
			"fsobserve",
		))
	}
	// Locator is set separately: it is provenance rather than identity, and
	// NewPoint does not take one.
	for i := range events {
		events[i].Locator = fmt.Sprintf("/synthetic/alloc/note-%d.md", i)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("Write %d events: %v", n, err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close the writer: %v", err)
	}

	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly %s: %v", path, err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

// allocsFor runs f once after a full collection and reports how many objects it
// allocated. AllocsPerRun is not used because a single call here costs tens of
// milliseconds and its default of five runs makes the test needlessly slow;
// what is being measured is a per-row count in the thousands, far above the
// noise a single run carries.
func allocsFor(t *testing.T, f func()) uint64 {
	t.Helper()
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)
	return after.Mallocs - before.Mallocs
}

// TestReadingEventsAllocatesNoMorePerRowThanTheDriverItself pins the property
// that the scan destinations are reused across rows rather than reallocated
// per row.
//
// It fails on the shape this repository shipped until 2026-08-07, where the
// ten destinations were declared inside the row loop: that costs ten heap
// objects a row on top of whatever the driver spends, roughly ten million of
// them on a store the size of a real one.
func TestReadingEventsAllocatesNoMorePerRowThanTheDriverItself(t *testing.T) {
	st, path := syntheticEventStore(t, eventAllocRows)
	ctx := context.Background()

	// The driver's own floor: the same eleven columns off the same relation,
	// through database/sql, scanned into destinations declared once. Whatever
	// this costs per row is not something the store's reader can avoid.
	raw, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s read-only: %v", path, err)
	}
	defer raw.Close()

	// The SAME query the store issues, taken from the store rather than copied,
	// so that the floor and the thing measured against it cannot come to be two
	// different reads. A copy that drifted would go on producing a number, and
	// the number would silently stop being a floor.
	columns := store.EventsQuery("canonical_event", "")

	var (
		floorRows int
		floorErr  error
	)
	floor := allocsFor(t, func() {
		var (
			sourceID, shape                                        string
			unknown, started, ended                                sql.NullString
			app, document, state, locator, projectColumn, causeCol sql.NullString
		)
		rows, err := raw.QueryContext(ctx, columns)
		if err != nil {
			floorErr = err
			return
		}
		defer rows.Close()
		for rows.Next() {
			if err := rows.Scan(&sourceID, &shape, &unknown, &started, &ended,
				&app, &document, &state, &locator, &projectColumn, &causeCol); err != nil {
				floorErr = err
				return
			}
			floorRows++
		}
		floorErr = rows.Err()
	})
	if floorErr != nil {
		t.Fatalf("read the driver floor: %v", floorErr)
	}
	if floorRows != eventAllocRows {
		t.Fatalf("the driver floor read %d rows, expected %d", floorRows, eventAllocRows)
	}

	var (
		got    []source.Event
		gotErr error
	)
	through := allocsFor(t, func() {
		got, gotErr = st.Events(ctx, store.Filter{})
	})
	if gotErr != nil {
		t.Fatalf("Events: %v", gotErr)
	}
	if len(got) != eventAllocRows {
		t.Fatalf("Events returned %d events, expected %d", len(got), eventAllocRows)
	}

	floorPerRow := float64(floor) / float64(eventAllocRows)
	gotPerRow := float64(through) / float64(eventAllocRows)

	// The headroom covers the reader's genuinely per-read allocations — the
	// returned slice's own growth, the query string, the argument slice —
	// amortised over the rows, plus ordinary run-to-run variation. It does not
	// cover a destination that escapes once a row, which costs ten.
	const headroomPerRow = 2.0
	if gotPerRow > floorPerRow+headroomPerRow {
		t.Errorf("reading %d events allocated %.1f objects a row against the driver's own %.1f; "+
			"no more than %.1f a row is the reader's to spend, and exceeding it means the scan "+
			"destinations are being reallocated inside the row loop rather than reused",
			eventAllocRows, gotPerRow, floorPerRow, floorPerRow+headroomPerRow)
	}
}
