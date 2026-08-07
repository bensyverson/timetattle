package migrations_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// 0013 adds the index the event read has always ordered by.
//
// Every read of the store goes out in anchor order — COALESCE(started_at,
// ended_at), id — because an event seen only as it stopped has no start to sort
// by, and sorting it at the zero time would file it decades before the work it
// records. No index in the schema could satisfy that ORDER BY, so SQLite built a
// temporary B-tree for it on every read, spilled it to disk when it outgrew the
// cache, and did all of that on a connection opened read-only.
//
// The tests here read the query plan rather than the results, because the
// property at issue is not what comes back but how it is produced. Nothing about
// a missing index is visible in a row.

// theEventRead is the query internal/store issues for a whole-corpus read, with
// no filter — the shape `timetattle metrics` uses. It is spelled out here rather
// than imported so that this package keeps testing the SCHEMA and not the Go
// that happens to query it; internal/store carries its own plan test, against
// the query text it actually builds, so that a change to that text cannot walk
// away from this index unnoticed.
const theEventRead = `SELECT source_id, shape, unknown, started_at, ended_at,
	app, document, state, locator, project, cause FROM canonical_event
	ORDER BY COALESCE(started_at, ended_at), id`

// queryPlan returns SQLite's plan for query as one string, so a test can ask
// whether a step appears in it at all.
func queryPlan(t *testing.T, d *sql.DB, query string) string {
	t.Helper()
	rows, err := d.Query("EXPLAIN QUERY PLAN " + query)
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN: %v", err)
	}
	defer rows.Close()

	columns, err := rows.Columns()
	if err != nil {
		t.Fatalf("plan columns: %v", err)
	}
	var out []string
	for rows.Next() {
		cells := make([]any, len(columns))
		for i := range cells {
			cells[i] = new(sql.NullString)
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatalf("scan a plan row: %v", err)
		}
		// The last column is the human-readable step; the ones before it are
		// the node's place in the tree, which nothing here asks about.
		out = append(out, cells[len(cells)-1].(*sql.NullString).String)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the plan: %v", err)
	}
	if len(out) == 0 {
		t.Fatalf("EXPLAIN QUERY PLAN returned no steps for %q", query)
	}
	return strings.Join(out, "\n")
}

// The before and the after in one test, because the after on its own would pass
// against a database that had never sorted in the first place. Version 12 is the
// schema this migration was written against: it is where the temp B-tree is, and
// watching it disappear is the only way the assertion means anything.
func TestTheAnchorIndexTakesTheSortOutOfTheEventRead(t *testing.T) {
	d := tsAt(t, 12)

	before := queryPlan(t, d, theEventRead)
	if !strings.Contains(before, "USE TEMP B-TREE FOR ORDER BY") {
		t.Fatalf("at version 12 the event read already avoids a sort:\n%s\n"+
			"this test's before-state is gone, so its after-state proves nothing", before)
	}

	if err := migrations.Apply(d); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after := queryPlan(t, d, theEventRead)
	if strings.Contains(after, "TEMP B-TREE") {
		t.Errorf("the event read still sorts through a temporary B-tree after 0013:\n%s", after)
	}
	if !strings.Contains(after, "event_anchor") {
		t.Errorf("the event read does not walk event_anchor after 0013:\n%s\n"+
			"an expression index only serves an ORDER BY whose text matches the indexed "+
			"expression, so this is what a mismatch looks like", after)
	}
}

// The table is read in the same order as the view, by
// EventsIncludingSuperseded, and one index serves both. Asserting only the view
// would leave the provenance read sorting a million rows with nothing to say so.
func TestTheAnchorIndexAlsoServesTheReadOfTheEventTable(t *testing.T) {
	d := migrated(t)
	const overTheTable = `SELECT source_id, shape, unknown, started_at, ended_at,
		app, document, state, locator, project, cause FROM event
		ORDER BY COALESCE(started_at, ended_at), id`

	plan := queryPlan(t, d, overTheTable)
	if strings.Contains(plan, "TEMP B-TREE") {
		t.Errorf("reading the event table sorts through a temporary B-tree:\n%s", plan)
	}
	if !strings.Contains(plan, "event_anchor") {
		t.Errorf("reading the event table does not walk event_anchor:\n%s", plan)
	}
}

// The index is NOT unique, and saying so is the point rather than an omission.
//
// Two observations may share an anchor — a knowledgeC point and a Biome interval
// that began at the same microsecond are two different facts about that instant —
// and event_identity is what keeps a re-harvest from storing either of them
// twice. An anchor index that refused the second one would be the most expensive
// kind of schema mistake this repository can make: discovered as a failing
// harvest, months of evidence later.
//
// WHAT THIS TEST DOES AND DOES NOT CATCH, established by mutation rather than
// asserted. Writing 0013 as `CREATE UNIQUE INDEX event_anchor ON event
// (COALESCE(started_at, ended_at))` fails it, on the second insert, with
// "UNIQUE constraint failed: index 'event_anchor'". Writing it as CREATE UNIQUE
// INDEX over `(COALESCE(started_at, ended_at), id)` — the shipped columns —
// does NOT fail it, and cannot: id is the primary key, so every key that
// includes it is distinct already and the UNIQUE has nothing left to refuse.
//
// That is worth knowing in both directions. It means naming id in the index
// disarms this whole class of mistake, which is a third argument for naming it
// alongside the two 0013 gives. It also means this test guards the plausible
// wrong design — an anchor index without id — and not the impossible one.
func TestTheAnchorIndexDoesNotRefuseTwoObservationsOfOneInstant(t *testing.T) {
	d := migrated(t)
	run := aRun(t, d)
	const at = "2026-07-14T09:15:00.000000Z"

	if err := insert(d, run, row{Source: "biome", Shape: "point", Started: at, Ended: at, App: "Editor"}); err != nil {
		t.Fatalf("write the first observation of %s: %v", at, err)
	}
	if err := insert(d, run, row{Source: "knowledgec", Shape: "point", Started: at, Ended: at, App: "Browser"}); err != nil {
		t.Fatalf("a second observation anchored at %s was refused: %v\n"+
			"event_anchor must not be unique; two sources observing one instant is ordinary", at, err)
	}

	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 2 {
		t.Errorf("the store holds %d events, want 2", n)
	}
}
