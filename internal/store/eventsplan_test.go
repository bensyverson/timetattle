package store_test

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// This file guards the ACCESS PATH of the event read, which is a third property
// alongside what it returns and what it allocates, and is invisible to tests of
// either.
//
// Reading the whole store returns its rows in anchor order —
// COALESCE(started_at, ended_at), id — and until migration 0013 no index could
// satisfy that ORDER BY, so SQLite materialised a temporary B-tree of a million
// rows on every read and spilled it to disk once it outgrew the page cache. The
// index that removes it is matched to the ORDER BY by TEXT: change how the
// expression is spelled and the sort comes back, with every correctness test
// still green and roughly a second added to every command that reads the store.
//
// The assertions here go through store.EventsQuery, the same function the read
// itself calls, so that this test cannot go on passing against a query the
// package no longer issues.

// planFor returns SQLite's plan for query as one string, read through a store
// that the automatic migration path has brought up to date — which is the only
// state a real store is ever in, since store.Open migrates on every open.
func planFor(t *testing.T, query string, args ...any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle", "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	t.Cleanup(func() { st.Close() })

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s read-only: %v", path, err)
	}
	defer db.Close()

	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("plan columns: %v", err)
	}
	var steps []string
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan a plan row: %v", err)
		}
		steps = append(steps, cells[len(cells)-1].(*sql.NullString).String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the plan: %v", err)
	}
	if len(steps) == 0 {
		t.Fatalf("EXPLAIN QUERY PLAN returned no steps for %q", query)
	}
	return strings.Join(steps, "\n")
}

// The unfiltered read is the one `timetattle metrics` issues, over the whole
// corpus, and it is where the sort cost the most.
func TestTheUnfilteredEventReadWalksTheAnchorIndex(t *testing.T) {
	for _, relation := range []string{"canonical_event", "event"} {
		plan := planFor(t, store.EventsQuery(relation, ""))
		if strings.Contains(plan, "TEMP B-TREE") {
			t.Errorf("the read of %s sorts through a temporary B-tree:\n%s", relation, plan)
		}
		if !strings.Contains(plan, "event_anchor") {
			t.Errorf("the read of %s does not walk event_anchor:\n%s\n"+
				"an expression index serves an ORDER BY only when the two spell the "+
				"expression identically; check eventsQuery against migration 0013",
				relation, plan)
		}
	}
}

// A filtered read still comes back in anchor order, and still must not sort to
// get there. This is the case a plan test over the unfiltered query alone would
// miss: SQLite may prefer an index that serves the WHERE and then sort the
// result, which is the right choice for a narrow filter and the wrong one for
// the windows Timetattle asks for — a day, a week, a quarter of a corpus.
func TestAFilteredEventReadStillComesBackWithoutASort(t *testing.T) {
	const clause = " WHERE COALESCE(ended_at, started_at) >= ? AND COALESCE(started_at, ended_at) <= ?"
	plan := planFor(t, store.EventsQuery("canonical_event", clause),
		"2026-07-01T00:00:00.000000Z", "2026-07-31T23:59:59.999999Z")
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("a window-filtered read sorts through a temporary B-tree:\n%s", plan)
	}
}
