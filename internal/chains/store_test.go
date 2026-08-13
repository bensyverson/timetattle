package chains

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// testDB is a migrated store in a temporary directory, and a raw handle on the
// same file. foreign_keys is on because the derived tables cascade, and because
// a chain step whose chain does not exist has no meaning.
//
// The store is what a rebuild goes through — the transaction belongs to it —
// and the raw handle is only read through, so that a bug in the writer cannot
// hide behind the reader that shares its code.
func testDB(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open the store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_txlock=immediate")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return st, db
}

// rebuild replaces the derived chain tables the way `timetattle chains` does:
// one call, through the store, which owns the transaction.
func rebuild(st *store.Store, r Result, computedAt time.Time) error {
	return st.ReplaceDerived(context.Background(), Rebuild(r, computedAt))
}

func count(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// sampleResult is the lightbar chain plus one document that chains with
// nothing, so that both halves of the derivation are exercised.
func sampleResult(t *testing.T) Result {
	t.Helper()
	const blender = "/Users/you/git/acorn/blender"
	const scanline = "/Users/you/git/acorn/scanline"
	r := Reconstruct([]source.Event{
		src(day(9, 19, 26), blender+"/lightbar.blend"),
		src(day(9, 33, 38), blender+"/lightbar-top.blend"),
		src(day(9, 36, 26), blender+"/lightbar-top-filmic.blend"),
		src(day(10, 0, 0), blender+"/backup.blend"),
		src(day(16, 15, 22), scanline+"/scanline.motn"),
		src(day(16, 15, 22), scanline+"/scanline-native.motn"),
		src(day(17, 0, 0), scanline+"/scanline-card-granted.motn"),
	}, Options{})
	if len(r.Chains) != 2 || len(r.Unchained) != 1 {
		t.Fatalf("fixture is wrong: %d chains, %d unchained:\n%s", len(r.Chains), len(r.Unchained), describe(r))
	}
	return r
}

func computedAt() time.Time { return time.Date(2026, 7, 31, 20, 0, 0, 0, time.UTC) }

// The derivation has to survive a round trip, parameters included. A chain row
// without the parameters that produced it cannot be compared with anything.
func TestRebuildAndLoadRoundTrip(t *testing.T) {
	st, db := testDB(t)
	ctx := context.Background()
	want := sampleResult(t)

	if err := rebuild(st, want, computedAt()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	got, err := Load(ctx, db)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got == nil {
		t.Fatal("Load returned nothing after a Rebuild")
	}

	if !got.ComputedAt.Equal(computedAt()) {
		t.Errorf("ComputedAt = %s, want %s", got.ComputedAt, computedAt())
	}
	if got.Options.Source != want.Options.Source {
		t.Errorf("Source = %q, want %q", got.Options.Source, want.Options.Source)
	}
	if got.Options.MinSharedPrefix != want.Options.MinSharedPrefix {
		t.Errorf("MinSharedPrefix = %d, want %d", got.Options.MinSharedPrefix, want.Options.MinSharedPrefix)
	}
	if got.Options.Coincidence != want.Options.Coincidence {
		t.Errorf("Coincidence = %s, want %s", got.Options.Coincidence, want.Options.Coincidence)
	}
	if strings.Join(got.Options.Classes, ",") != strings.Join(want.Options.Classes, ",") {
		t.Errorf("Classes = %v, want %v", got.Options.Classes, want.Options.Classes)
	}
	if got.EventsConsidered != want.EventsConsidered || got.Documents != want.Documents || got.Chained != want.Chained {
		t.Errorf("counts = %d/%d/%d, want %d/%d/%d",
			got.EventsConsidered, got.Documents, got.Chained,
			want.EventsConsidered, want.Documents, want.Chained)
	}
	if strings.Join(got.Unchained, ",") != strings.Join(want.Unchained, ",") {
		t.Errorf("Unchained = %v, want %v", got.Unchained, want.Unchained)
	}

	if len(got.Chains) != len(want.Chains) {
		t.Fatalf("got %d chains, want %d", len(got.Chains), len(want.Chains))
	}
	for i := range want.Chains {
		w, g := want.Chains[i], got.Chains[i]
		if g.Directory != w.Directory || g.Root != w.Root {
			t.Errorf("chain %d = %s/%s, want %s/%s", i, g.Directory, g.Root, w.Directory, w.Root)
			continue
		}
		if len(g.Steps) != len(w.Steps) {
			t.Errorf("chain %d has %d steps, want %d", i, len(g.Steps), len(w.Steps))
			continue
		}
		for j := range w.Steps {
			ws, gs := w.Steps[j], g.Steps[j]
			if !gs.At.Equal(ws.At) {
				t.Errorf("chain %d step %d at %s, want %s", i, j, gs.At, ws.At)
			}
			if gs.Gap != ws.Gap {
				t.Errorf("chain %d step %d gap %s, want %s", i, j, gs.Gap, ws.Gap)
			}
			if strings.Join(gs.Documents, ",") != strings.Join(ws.Documents, ",") {
				t.Errorf("chain %d step %d documents %v, want %v", i, j, gs.Documents, ws.Documents)
			}
			if strings.Join(gs.Locators, ",") != strings.Join(ws.Locators, ",") {
				t.Errorf("chain %d step %d locators %v, want %v", i, j, gs.Locators, ws.Locators)
			}
		}
	}
}

// "Never computed" and "computed, and found nothing" are different facts. A
// bare empty list collapses them, and this project has shipped that bug twice.
func TestLoadDistinguishesNeverComputedFromComputedEmpty(t *testing.T) {
	st, db := testDB(t)
	ctx := context.Background()

	never, err := Load(ctx, db)
	if err != nil {
		t.Fatalf("Load on a fresh database: %v", err)
	}
	if never != nil {
		t.Errorf("Load = %+v on a database nothing has derived into, want nil", never)
	}

	if err := rebuild(st, Reconstruct(nil, Options{}), computedAt()); err != nil {
		t.Fatalf("Rebuild of an empty result: %v", err)
	}
	empty, err := Load(ctx, db)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if empty == nil {
		t.Fatal("Load = nil after an empty derivation; that is indistinguishable from never having run")
	}
	if len(empty.Chains) != 0 || empty.Documents != 0 {
		t.Errorf("got %+v, want a recorded run holding nothing", empty)
	}
}

// A rebuild replaces. Anything else and two generations of the model coexist,
// which is exactly what putting this in the event store would have caused.
func TestRebuildReplacesRatherThanAccumulates(t *testing.T) {
	st, db := testDB(t)
	r := sampleResult(t)

	for i := range 3 {
		if err := rebuild(st, r, computedAt()); err != nil {
			t.Fatalf("Rebuild %d: %v", i, err)
		}
	}
	if n := count(t, db, "derived_chain_run"); n != 1 {
		t.Errorf("derived_chain_run holds %d rows after three rebuilds, want 1", n)
	}
	if n := count(t, db, "derived_chain"); n != len(r.Chains) {
		t.Errorf("derived_chain holds %d rows, want %d", n, len(r.Chains))
	}
	steps := 0
	for _, c := range r.Chains {
		steps += len(c.Steps)
	}
	if n := count(t, db, "derived_chain_step"); n != steps {
		t.Errorf("derived_chain_step holds %d rows, want %d", n, steps)
	}
	if n := count(t, db, "derived_chain_unchained"); n != len(r.Unchained) {
		t.Errorf("derived_chain_unchained holds %d rows, want %d", n, len(r.Unchained))
	}

	// A narrower run must leave nothing of the wider one behind.
	narrow := Reconstruct([]source.Event{
		src(day(9, 19, 26), "/Users/you/git/acorn/blender/lightbar.blend"),
		src(day(9, 33, 38), "/Users/you/git/acorn/blender/lightbar-top.blend"),
	}, Options{})
	if err := rebuild(st, narrow, computedAt()); err != nil {
		t.Fatalf("narrow Rebuild: %v", err)
	}
	if n := count(t, db, "derived_chain"); n != 1 {
		t.Errorf("derived_chain holds %d rows after a narrower rebuild, want 1", n)
	}
	if n := count(t, db, "derived_chain_unchained"); n != 0 {
		t.Errorf("derived_chain_unchained holds %d rows, want 0", n)
	}
}

// The convention this table lives under: a derivation never writes to the
// evidence. Measured rather than asserted — a real event and a real run are put
// in first, and their row counts and contents must be untouched afterwards.
func TestRebuildNeverTouchesTheEventStore(t *testing.T) {
	st, db := testDB(t)

	if _, err := db.Exec(`INSERT INTO harvest_run (id, source_id, started_at, input, status)
		VALUES (1, 'fs-observations', '2026-07-31T00:00:00.000000Z', '/Users/you/git/acorn', 'ok')`); err != nil {
		t.Fatalf("seed harvest_run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO event (first_seen_run, source_id, shape, started_at, ended_at, document, state, locator, cause)
		VALUES (1, 'fs-observations', 'point', '2026-07-11T14:19:26.000000Z', '2026-07-11T14:19:26.000000Z',
		        'lightbar', 'source', '/Users/you/git/acorn/blender/lightbar.blend', 'user-action')`); err != nil {
		t.Fatalf("seed event: %v", err)
	}

	before := count(t, db, "event")
	beforeRuns := count(t, db, "harvest_run")
	var beforeDigest string
	if err := db.QueryRow(`SELECT group_concat(id || source_id || started_at || COALESCE(document,'')) FROM event`).
		Scan(&beforeDigest); err != nil {
		t.Fatalf("digest: %v", err)
	}

	if err := rebuild(st, sampleResult(t), computedAt()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	if got := count(t, db, "event"); got != before {
		t.Errorf("event holds %d rows after a rebuild, want %d", got, before)
	}
	if got := count(t, db, "harvest_run"); got != beforeRuns {
		t.Errorf("harvest_run holds %d rows after a rebuild, want %d", got, beforeRuns)
	}
	var afterDigest string
	if err := db.QueryRow(`SELECT group_concat(id || source_id || started_at || COALESCE(document,'')) FROM event`).
		Scan(&afterDigest); err != nil {
		t.Fatalf("digest: %v", err)
	}
	if afterDigest != beforeDigest {
		t.Errorf("event rows changed:\n before %s\n after  %s", beforeDigest, afterDigest)
	}
}

// The schema guards, proved by trying to violate them directly. Each of these
// is a row the Go code will not write; the point is that nothing else can
// write it either, because a derived table with no constraints is a cache that
// silently disagrees with its own model.
func TestSchemaRefusesRowsTheModelCannotProduce(t *testing.T) {
	st, db := testDB(t)
	if err := rebuild(st, sampleResult(t), computedAt()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}

	// A valid chain with no steps of its own, so that the step guards below are
	// exercised against the CHECK they name. Aimed at chain 1 instead, an
	// ordinal-0 row already exists and every attempt would be refused by the
	// unique index — the case would pass while the CHECK it claims to test sat
	// there doing nothing.
	if _, err := db.Exec(`INSERT INTO derived_chain
		(id, run_id, directory, root, started_at, ended_at, step_count, document_count)
		VALUES (999, 1, '/Users/you/git/acorn/spare', 'spare',
		        '2026-07-11T14:00:00.000000Z', '2026-07-11T15:00:00.000000Z', 2, 2)`); err != nil {
		t.Fatalf("seed the spare chain: %v", err)
	}

	cases := []struct {
		name string
		sql  string
	}{
		{
			// Without CHECK (id = 1) two parameter sets coexist and every chain
			// row becomes ambiguous about which model produced it.
			"a second derivation run",
			`INSERT INTO derived_chain_run (id, computed_at, source_id, classes, extensions,
			     min_shared_prefix, coincidence_ms, events_considered, documents, documents_chained)
			 VALUES (2, '2026-07-31T20:00:00.000000Z', 'fs-observations', '["source"]', '[]', 6, 1000, 0, 0, 0)`,
		},
		{
			// A one-step chain is a file. Admitting it makes the chain count
			// the document count and the whole measure vacuous.
			"a chain with one step",
			`INSERT INTO derived_chain (run_id, directory, root, started_at, ended_at, step_count, document_count)
			 VALUES (1, '/Users/you/git/acorn/blender', 'solo', '2026-07-11T14:00:00.000000Z',
			         '2026-07-11T15:00:00.000000Z', 1, 1)`,
		},
		{
			// Fewer documents than steps is impossible: every step names at
			// least one.
			"more steps than documents",
			`INSERT INTO derived_chain (run_id, directory, root, started_at, ended_at, step_count, document_count)
			 VALUES (1, '/Users/you/git/acorn/blender', 'thin', '2026-07-11T14:00:00.000000Z',
			         '2026-07-11T15:00:00.000000Z', 3, 2)`,
		},
		{
			// A chain that ends where it starts has no interval, so it is a
			// burst that the collapse rule failed to collapse.
			"a chain of zero width",
			`INSERT INTO derived_chain (run_id, directory, root, started_at, ended_at, step_count, document_count)
			 VALUES (1, '/Users/you/git/acorn/blender', 'flat', '2026-07-11T14:00:00.000000Z',
			         '2026-07-11T14:00:00.000000Z', 2, 2)`,
		},
		{
			"a relative directory",
			`INSERT INTO derived_chain (run_id, directory, root, started_at, ended_at, step_count, document_count)
			 VALUES (1, 'blender', 'rel', '2026-07-11T14:00:00.000000Z',
			         '2026-07-11T15:00:00.000000Z', 2, 2)`,
		},
		{
			// The same lineage twice in one directory means the clustering did
			// not converge.
			"a duplicate chain identity",
			`INSERT INTO derived_chain (run_id, directory, root, started_at, ended_at, step_count, document_count)
			 VALUES (1, '/Users/you/git/acorn/blender', 'lightbar', '2026-07-11T14:00:00.000000Z',
			         '2026-07-11T15:00:00.000000Z', 2, 2)`,
		},
		{
			// A first step measured from nothing.
			"a first step carrying a gap",
			`INSERT INTO derived_chain_step (chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
			 VALUES (999, 0, '2026-07-11T14:00:00.000000Z', '["x"]', '["/x"]', 1, 60000)`,
		},
		{
			// A later step with no gap reads as the start of a chain it is in
			// the middle of.
			"a later step with no gap",
			`INSERT INTO derived_chain_step (chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
			 VALUES (999, 1, '2026-07-11T14:00:00.000000Z', '["x"]', '["/x"]', 1, NULL)`,
		},
		{
			// Zero is not "no gap". It is two branch points at one instant,
			// which is what the collapse exists to prevent.
			"a step with a zero gap",
			`INSERT INTO derived_chain_step (chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
			 VALUES (999, 2, '2026-07-11T14:00:00.000000Z', '["x"]', '["/x"]', 1, 0)`,
		},
		{
			"a step naming no documents",
			`INSERT INTO derived_chain_step (chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
			 VALUES (999, 3, '2026-07-11T14:00:00.000000Z', '[]', '[]', 0, 60000)`,
		},
		{
			"a step belonging to no chain",
			`INSERT INTO derived_chain_step (chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
			 VALUES (4242, 0, '2026-07-11T14:00:00.000000Z', '["x"]', '["/x"]', 1, NULL)`,
		},
		{
			"a coincidence window of zero",
			`INSERT INTO derived_chain_run (id, computed_at, source_id, classes, extensions,
			     min_shared_prefix, coincidence_ms, events_considered, documents, documents_chained)
			 VALUES (1, '2026-07-31T20:00:00.000000Z', 'fs-observations', '["source"]', '[]', 6, 0, 0, 0, 0)`,
		},
		{
			"more documents chained than exist",
			`INSERT INTO derived_chain_run (id, computed_at, source_id, classes, extensions,
			     min_shared_prefix, coincidence_ms, events_considered, documents, documents_chained)
			 VALUES (1, '2026-07-31T20:00:00.000000Z', 'fs-observations', '["source"]', '[]', 6, 1000, 5, 3, 4)`,
		},
		{
			"an unchained document with a relative path",
			`INSERT INTO derived_chain_unchained (run_id, document) VALUES (1, 'blender/backup')`,
		},
	}
	for _, c := range cases {
		if _, err := db.Exec(c.sql); err == nil {
			t.Errorf("%s inserted successfully; the guard is not doing anything", c.name)
		}
	}
}

// The identity index has to hold across a rebuild too, not only within one.
func TestUnchainedDocumentsAreUniquePerRun(t *testing.T) {
	st, db := testDB(t)
	if err := rebuild(st, sampleResult(t), computedAt()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	var doc string
	if err := db.QueryRow(`SELECT document FROM derived_chain_unchained LIMIT 1`).Scan(&doc); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO derived_chain_unchained (run_id, document) VALUES (1, ?)`, doc); err == nil {
		t.Error("the same unchained document inserted twice for one run")
	}
}

// Times go in and come out in the store's own fixed-width UTC form, so text
// order is time order in this table exactly as it is in event.
func TestTimestampsAreStoredInTheStoreFormat(t *testing.T) {
	st, db := testDB(t)
	if err := rebuild(st, sampleResult(t), computedAt()); err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	rows, err := db.Query(`SELECT started_at, ended_at FROM derived_chain`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var start, end string
		if err := rows.Scan(&start, &end); err != nil {
			t.Fatal(err)
		}
		for _, ts := range []string{start, end} {
			if len(ts) != len("2006-01-02T15:04:05.000000Z") || !strings.HasSuffix(ts, "Z") {
				t.Errorf("timestamp %q is not the store's fixed-width UTC form", ts)
			}
		}
		if !(start < end) {
			t.Errorf("%q is not lexically before %q, so text order is not time order", start, end)
		}
	}
}
