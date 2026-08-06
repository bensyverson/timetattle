package main

import (
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/jobs"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

// These tests cover the wiring of internal/jobs into harvest (task aoFma).
// The reader itself is tested next door; what is tested here is the plan:
// that every registered directory reaches the collector, that the two streams
// each get exactly one harvest_run row however many directories there are, and
// that a database nobody can read fails loudly rather than quietly.

// writeJobsFixture builds a .jobs.db inside dir carrying the schema the `job`
// tool writes, with one created/claimed/done trail on one task. It is spelled
// out here rather than shared with internal/jobs' own fixtures because a test
// in this package proving the wiring should not depend on another package's
// unexported test helpers.
func writeJobsFixture(t *testing.T, dir, task string, unixTimes ...int64) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, jobs.DBName))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, stmt := range []string{
		`CREATE TABLE tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, short_id TEXT UNIQUE NOT NULL,
			title TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE events (id INTEGER PRIMARY KEY AUTOINCREMENT, task_id INTEGER REFERENCES tasks(id),
			event_type TEXT NOT NULL, actor TEXT NOT NULL DEFAULT '', detail TEXT,
			created_at INTEGER NOT NULL)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO tasks (short_id) VALUES (?)`, task); err != nil {
		t.Fatal(err)
	}
	types := []string{"created", "claimed", "done"}
	for i, at := range unixTimes {
		if _, err := db.Exec(
			`INSERT INTO events (task_id, event_type, actor, created_at) VALUES (1, ?, 'claude', ?)`,
			types[i%len(types)], at); err != nil {
			t.Fatal(err)
		}
	}
}

// TestJobIngestionsCoverEveryRegisteredDirectoryInTwoRuns is the shape
// decision made visible. Three registered directories produce TWO ingestions,
// not six: one per stream, each reading every root. gitlogIngestions returns
// one per repository, and `timetattle status` consequently reports one
// repository's numbers as though they were the source's (task uO70D).
func TestJobIngestionsCoverEveryRegisteredDirectoryInTwoRuns(t *testing.T) {
	base := t.TempDir()
	var markers []*registry.Marker
	for _, name := range []string{"one", "two", "three"} {
		dir := filepath.Join(base, name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		markers = append(markers, marker(t, dir, "Client", name))
	}

	plan := jobIngestions(markers)
	if len(plan) != 2 {
		t.Fatalf("jobIngestions over 3 directories = %d ingestions, want 2", len(plan))
	}
	ids := []string{plan[0].source.ID(), plan[1].source.ID()}
	sort.Strings(ids)
	if ids[0] != jobs.ClaimsID || ids[1] != jobs.EventsID {
		t.Errorf("source IDs = %v, want %v and %v", ids, jobs.ClaimsID, jobs.EventsID)
	}
	for _, ing := range plan {
		if ing.input == "" {
			t.Errorf("%s: input is empty; store.BeginRun refuses one", ing.source.ID())
		}
		for _, m := range markers {
			if !strings.Contains(ing.input, m.Path) {
				t.Errorf("%s: input %q does not name registered directory %s",
					ing.source.ID(), ing.input, m.Path)
			}
		}
	}
}

// TestJobIngestionsWithAnEmptyRegistryPlanNothing. store.BeginRun refuses a
// run with an empty input, so a plan entry naming no directories at all would
// fail the harvest rather than record an empty one.
func TestJobIngestionsWithAnEmptyRegistryPlanNothing(t *testing.T) {
	if got := jobIngestions(nil); got != nil {
		t.Errorf("jobIngestions(nil) = %+v, want nil", got)
	}
}

// TestJobEventsFromEveryRegisteredDirectoryReachTheStore is criterion ufP.
//
// Two registered directories with task databases, one without, and every
// observation from both databases has to arrive under one run per stream — the
// counts on the runs matching the rows in the store, so that a number can be
// reconciled rather than merely reported.
func TestJobEventsFromEveryRegisteredDirectoryReachTheStore(t *testing.T) {
	base := t.TempDir()
	withDB := filepath.Join(base, "with-db")
	alsoWithDB := filepath.Join(base, "also-with-db")
	withoutDB := filepath.Join(base, "without-db")
	writeJobsFixture(t, withDB, "aaaaa", 1785257292, 1785257400, 1785258000)
	writeJobsFixture(t, alsoWithDB, "bbbbb", 1785260000, 1785260100)
	if err := os.MkdirAll(withoutDB, 0o755); err != nil {
		t.Fatal(err)
	}

	markers := []*registry.Marker{
		marker(t, withDB, "Client", "One"),
		marker(t, alsoWithDB, "Client", "Two"),
		marker(t, withoutDB, "Client", "Three"),
	}
	st, storePath := newStore(t)
	if err := ingest(context.Background(), st, jobIngestions(markers), io.Discard); err != nil {
		t.Fatalf("ingest: %v", err)
	}

	runs := bySource(t, harvestRuns(t, storePath))
	if len(runs) != 2 {
		t.Fatalf("recorded %d runs, want 2 (one per stream)", len(runs))
	}
	events, ok := runs[jobs.EventsID]
	if !ok {
		t.Fatalf("no harvest_run row for %s", jobs.EventsID)
	}
	if events.status != string(store.StatusOK) {
		t.Fatalf("%s: status %q, error %q", jobs.EventsID, events.status, events.failure.String)
	}
	// Three rows in one database and two in the other, none of them written by
	// the same command at the same instant, so all five are observations.
	if events.seen != 5 || events.added != 5 {
		t.Errorf("%s saw %d and added %d events, want 5 and 5", jobs.EventsID, events.seen, events.added)
	}
	claims, ok := runs[jobs.ClaimsID]
	if !ok {
		t.Fatalf("no harvest_run row for %s", jobs.ClaimsID)
	}
	// One claimed→done pair in the first database; the second database's claim
	// is still open and produces no span.
	if claims.seen != 1 {
		t.Errorf("%s saw %d spans, want 1", jobs.ClaimsID, claims.seen)
	}
	if got := eventCount(t, storePath); got != 6 {
		t.Errorf("the store holds %d rows, want 6", got)
	}

	// The directory with no database is not a failure, and it is also not
	// something either run was allowed to fail over.
	for id, r := range runs {
		if r.status != string(store.StatusOK) {
			t.Errorf("%s: a registered directory with no %s failed the run: %q",
				id, jobs.DBName, r.failure.String)
		}
	}
}

// TestJobIngestionRecordsALossWhenADatabaseCannotBeRead is criterion kLP at the
// wiring level. The registered directory in the previous test HAS no database
// and succeeds; this one has one that cannot be parsed, and the difference has
// to be visible in the store rather than in a log nobody reads.
func TestJobIngestionRecordsALossWhenADatabaseCannotBeRead(t *testing.T) {
	base := t.TempDir()
	good := filepath.Join(base, "good")
	broken := filepath.Join(base, "broken")
	writeJobsFixture(t, good, "aaaaa", 1785257292)
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, jobs.DBName), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	markers := []*registry.Marker{
		marker(t, good, "Client", "Good"),
		marker(t, broken, "Client", "Broken"),
	}
	st, storePath := newStore(t)
	err := ingest(context.Background(), st, jobIngestions(markers), io.Discard)
	if err == nil {
		t.Fatal("ingest reported success over an unreadable database")
	}

	runs := bySource(t, harvestRuns(t, storePath))
	for _, id := range []string{jobs.EventsID, jobs.ClaimsID} {
		r, ok := runs[id]
		if !ok {
			t.Fatalf("no harvest_run row for %s; a source with no row cannot be told "+
				"apart from one that ran and found nothing", id)
		}
		if r.status == string(store.StatusOK) {
			t.Errorf("%s: status ok over an unreadable database", id)
		}
		if !strings.Contains(r.failure.String, broken) {
			t.Errorf("%s: failure %q does not name the database that could not be read",
				id, r.failure.String)
		}
	}
	if got := eventCount(t, storePath); got != 0 {
		t.Errorf("the store holds %d rows; a partial harvest was written as a whole one", got)
	}
}
