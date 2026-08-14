package sessions_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// derivedAt is the clock every persistence test hands Replace. It is a
// parameter rather than time.Now precisely so that "a second run writes
// identical rows" is a comparison with no exception in it.
var derivedAt = time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)

// open returns a migrated, empty store holding one harvest run and two events,
// so that "the derivation touched nothing it should not" is a claim about rows
// that actually exist.
//
// Two things come back. The store is what a rebuild goes through, because the
// transaction belongs to it; the raw handle is only ever read through, so that
// a bug in the writer cannot hide behind the reader that shares its code.
func open(t *testing.T) (*store.Store, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
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
	if _, err := db.Exec(`INSERT INTO harvest_run (id, source_id, started_at, input, status)
		VALUES (1, 'fsobserve', '2026-07-11T09:00:00.000000Z', '/tmp', 'ok')`); err != nil {
		t.Fatalf("seed a harvest run: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO event (first_seen_run, source_id, shape, started_at, ended_at, document)
		VALUES (1, 'fsobserve', 'point', '2026-07-11T09:00:00.000000Z', '2026-07-11T09:00:00.000000Z', 'a.motn'),
		       (1, 'fsobserve', 'point', '2026-07-11T09:10:00.000000Z', '2026-07-11T09:10:00.000000Z', 'b.motn')`); err != nil {
		t.Fatalf("seed events: %v", err)
	}
	return st, db
}

// replace rebuilds derived_session the way `timetattle sessionize` does: one
// call, through the store, which owns the transaction. Nothing in this package
// begins one any more.
func replace(st *store.Store, res sessions.Result, computedAt time.Time) error {
	return replaceFrom(st, res, sessions.Corpus{}, computedAt)
}

// replaceFrom is replace with the corpus named. The plain form passes the
// unbounded corpus — nothing excluded, no window — because the tests that use
// it are about the parameters and the rows, not about which observations the
// derivation was allowed to see.
func replaceFrom(st *store.Store, res sessions.Result, c sessions.Corpus, computedAt time.Time) error {
	return st.ReplaceDerived(context.Background(), sessions.Replace(res, c, computedAt))
}

// fingerprint is a checksum of a whole table, ordered, so "byte-identical"
// is a comparison rather than an assertion.
func fingerprint(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	return fingerprintExcept(t, db, table, "")
}

// fingerprintExcept checksums a whole table, optionally leaving one column
// out, so "byte-identical" and "identical but for the clock" are both
// comparisons rather than assertions.
//
// quote() renders every column including NULL unambiguously, which plain
// concatenation would not: 'a'||NULL is NULL, so two different tables would
// otherwise both fingerprint as nothing and the test would pass vacuously.
func fingerprintExcept(t *testing.T, db *sql.DB, table, omit string) string {
	t.Helper()
	cols, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("list the columns of %s: %v", table, err)
	}
	var keep []string
	for cols.Next() {
		var name string
		if err := cols.Scan(&name); err != nil {
			t.Fatalf("read a column name: %v", err)
		}
		if name != omit {
			keep = append(keep, "quote("+name+")")
		}
	}
	cols.Close()
	if len(keep) == 0 {
		t.Fatalf("%s has no columns left after omitting %q", table, omit)
	}
	var sum sql.NullString
	q := `SELECT group_concat(row, char(10)) FROM (SELECT ` +
		strings.Join(keep, `||','||`) + ` AS row FROM ` + table + ` ORDER BY rowid)`
	if err := db.QueryRow(q).Scan(&sum); err != nil {
		t.Fatalf("fingerprint %s: %v", table, err)
	}
	return sum.String
}

func rowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func sample(t *testing.T) sessions.Result {
	t.Helper()
	events := []source.Event{point(0), point(10 * time.Minute), point(3 * time.Hour)}
	res, err := sessions.Cluster(events, params())
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if len(res.Sessions) != 2 {
		t.Fatalf("the fixture produced %d sessions, want 2", len(res.Sessions))
	}
	return res
}

func TestReplaceWritesOneRowPerSession(t *testing.T) {
	st, db := open(t)
	res := sample(t)

	if err := replace(st, res, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got, want := rowCount(t, db, "derived_session"), len(res.Sessions); got != want {
		t.Errorf("derived_session holds %d rows, want %d", got, want)
	}
}

// Criterion Ybu, half of it: a recompute changes no event.
func TestRecomputingChangesNoEventAndNoHarvestRun(t *testing.T) {
	st, db := open(t)
	res := sample(t)

	beforeEvents := fingerprint(t, db, "event")
	beforeRuns := fingerprint(t, db, "harvest_run")
	if beforeEvents == "" || beforeRuns == "" {
		t.Fatal("the fixture has no rows to protect; the test would pass vacuously")
	}

	for range 3 {
		if err := replace(st, res, derivedAt); err != nil {
			t.Fatalf("Replace: %v", err)
		}
	}

	if got := fingerprint(t, db, "event"); got != beforeEvents {
		t.Error("recomputing sessions changed the event table")
	}
	if got := fingerprint(t, db, "harvest_run"); got != beforeRuns {
		t.Error("recomputing sessions changed the harvest_run table")
	}
}

// Criterion Ybu, the other half: recomputing from scratch is a no-op.
func TestASecondReplaceProducesByteIdenticalRows(t *testing.T) {
	st, db := open(t)
	res := sample(t)

	if err := replace(st, res, derivedAt); err != nil {
		t.Fatalf("first Replace: %v", err)
	}
	// Both tables. The derivation is on its own row since migration 0016, so a
	// fingerprint of the sittings alone would no longer notice a parameter or a
	// statistic that moved between two identical runs.
	first := fingerprint(t, db, "derived_session") + fingerprint(t, db, "derived_session_run")

	if err := replace(st, res, derivedAt); err != nil {
		t.Fatalf("second Replace: %v", err)
	}
	second := fingerprint(t, db, "derived_session") + fingerprint(t, db, "derived_session_run")
	if second != first {
		t.Errorf("a second recompute produced different rows:\n%s\n---\n%s", first, second)
	}
	if got, want := rowCount(t, db, "derived_session"), len(res.Sessions); got != want {
		t.Errorf("derived_session holds %d rows after two recomputes, want %d", got, want)
	}
}

func TestOnlyTheDerivationTimeMovesWhenTheClockDoes(t *testing.T) {
	// The honest form of the byte-identical claim. Everything a derivation says
	// about the work is reproducible; computed_at is a reading of the wall
	// clock and cannot be, so it is the one column allowed to differ and it
	// must be the ONLY one.
	//
	// computed_at lives on derived_session_run since migration 0016, so the
	// column is omitted there and the sittings are compared whole. Omitting a
	// column that no longer exists would have made this pass without comparing
	// anything the clock could have touched.
	st, db := open(t)
	res := sample(t)

	clocked := func() string {
		return fingerprint(t, db, "derived_session") +
			fingerprintExcept(t, db, "derived_session_run", "computed_at")
	}

	if err := replace(st, res, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	first := clocked()

	if err := replace(st, res, derivedAt.Add(72*time.Hour)); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if second := clocked(); second != first {
		t.Errorf("moving the clock changed something other than computed_at:\n%s\n---\n%s", first, second)
	}
	if fingerprint(t, db, "derived_session") == "" || fingerprint(t, db, "derived_session_run") == "" {
		t.Fatal("a table is empty; the comparison above was vacuous")
	}
	// And the column that IS allowed to move has moved, or the comparison above
	// proves only that nothing happened.
	var stored string
	if err := db.QueryRow(`SELECT computed_at FROM derived_session_run`).Scan(&stored); err != nil {
		t.Fatalf("read computed_at: %v", err)
	}
	if want := derivedAt.Add(72 * time.Hour).Format(store.TimeLayout); stored != want {
		t.Errorf("computed_at is %s after the clock moved, want %s", stored, want)
	}
}

func TestReplaceLeavesNoRowsFromTheParametersItSuperseded(t *testing.T) {
	// A table half computed at one gap and half at another is a number nobody
	// can interpret, and it would not look wrong from the outside. Since
	// migration 0016 the gap is on one row under `CHECK (id = 1)`, so a mixture
	// cannot be written — and what this measures is that a recompute REPLACED
	// the derivation rather than being refused by that CHECK and leaving the old
	// one standing, which is the failure the singleton makes possible.
	//
	// These two derivations produce IDENTICAL SPANS — at a two-minute gap the
	// three points cluster three ways and the five-minute margins then merge two
	// of them back, landing on the same two sittings a fifteen-minute gap
	// reaches directly. So the stored gap is the only thing that tells them
	// apart, which is a small demonstration of why it is stored at all.
	st, db := open(t)
	events := []source.Event{point(0), point(10 * time.Minute), point(3 * time.Hour)}

	tight := params()
	tight.Gap = 2 * time.Minute
	first, err := sessions.Cluster(events, tight)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if err := replace(st, first, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	// The tight derivation is really in the store, or the replacement below
	// would be superseding nothing.
	var before int64
	if err := db.QueryRow(`SELECT gap_micros FROM derived_session_run`).Scan(&before); err != nil {
		t.Fatalf("read the stored gap: %v", err)
	}
	if before != int64(tight.Gap/time.Microsecond) {
		t.Fatalf("the first derivation stored a gap of %d, want %d", before, tight.Gap/time.Microsecond)
	}

	if err := replace(st, sample(t), derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	var rows int
	var gap int64
	if err := db.QueryRow(`SELECT COUNT(*), COALESCE(MAX(gap_micros), -1) FROM derived_session_run`).
		Scan(&rows, &gap); err != nil {
		t.Fatalf("read the stored gaps: %v", err)
	}
	if rows != 1 {
		t.Fatalf("derived_session_run holds %d rows after a recompute, want 1", rows)
	}
	if want := int64(sample(t).Params.Gap / time.Microsecond); gap != want {
		t.Errorf("the stored gap is %d, want %d; the recompute did not supersede the tight one", gap, want)
	}
	// And the sittings hanging off it are the new derivation's.
	if got, want := rowCount(t, db, "derived_session"), len(sample(t).Sessions); got != want {
		t.Errorf("derived_session holds %d rows after the recompute, want %d", got, want)
	}
}

// The round trip, read back through the store rather than through a second
// handle on the file. What Replace wrote and what Store.Sittings returns are
// the same Params and the same Sitting — one type under two names rather than
// two types — so this is a comparison and not a translation.
//
// Store.Sittings' own behaviour is tested in internal/store, where the method
// lives: the window, a table holding two derivations, and the empty table this
// package used to test here as
// TestStoredOnAnEmptyTableIsNoSessionsRatherThanAnError — now
// TestSittingsOnATableNobodyHasDerivedSaysSoRatherThanReportingAnEmptyDay,
// which asks the harder question of the two. What is tested here is that this
// writer and that reader agree.
func TestTheParametersAreRecoverableFromTheStoredRows(t *testing.T) {
	st, _ := open(t)
	res := sample(t)

	if err := replace(st, res, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	stored, err := st.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if !stored.Params.Equal(res.Params) {
		t.Errorf("the parameters read back as %+v, want %+v", stored.Params, res.Params)
	}
	if !stored.ComputedAt.Equal(derivedAt) {
		t.Errorf("the derivation time read back as %v, want %v", stored.ComputedAt, derivedAt)
	}
	if len(stored.Sittings) != len(res.Sessions) {
		t.Fatalf("read back %d sittings, want %d", len(stored.Sittings), len(res.Sessions))
	}
	for i, s := range stored.Sittings {
		want := res.Sessions[i]
		if !s.Start.Equal(want.Start) || !s.End.Equal(want.End) {
			t.Errorf("sitting %d read back as %v..%v, want %v..%v", i, s.Start, s.End, want.Start, want.End)
		}
		if s.Events != want.Events || s.Margin != want.Margin {
			t.Errorf("sitting %d read back with %d events and %v margin, want %d and %v",
				i, s.Events, s.Margin, want.Events, want.Margin)
		}
	}
}

func TestTwoSessionsCannotClaimTheSameSpan(t *testing.T) {
	// The unique index is the guard against a rebuild that inserted without
	// deleting. Proved by writing the row twice directly rather than by
	// trusting Replace, since Replace is the thing being guarded.
	st, db := open(t)
	res := sample(t)
	if err := replace(st, res, derivedAt); err != nil {
		t.Fatalf("Replace: %v", err)
	}

	// A straight copy of an existing row, which is exactly the shape a rebuild
	// that inserted without deleting would produce. The column list is the
	// whole of derived_session since migration 0016 moved the derivation off
	// it, so a column added here without being added to the index's key would
	// show up as this test passing for the wrong reason.
	_, err := db.Exec(`INSERT INTO derived_session
		(run_id, started_at, ended_at, events, sources, margin_micros)
		SELECT run_id, started_at, ended_at, events, sources, margin_micros
		FROM derived_session LIMIT 1`)
	if err == nil {
		t.Fatal("a second session with an identical span inserted; the derived table would double-count wall clock")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Errorf("the copy was refused for some reason other than the span index: %v", err)
	}
}
