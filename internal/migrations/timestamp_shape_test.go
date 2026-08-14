package migrations_test

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// The shape guard on every TEXT timestamp in the schema, tested as one
// population rather than table by table.
//
// Every instant in this database is TEXT, and every ordering read over one — the
// CHECK (ended_at >= started_at) constraints, event_span, derived_session_span,
// adjustment_fold_order, and every range query above them — is a TEXT
// comparison. That is chronological only while every value carries the single
// layout internal/store.TimeLayout writes. 0003 knew this and guarded it with
// length(started_at) = 27, which is too weak: '2026-08-01T08:04:35.0-05:00' is
// exactly 27 characters and is five hours out of UTC.
//
// These tests are driven with raw SQL for the reason schema_test.go gives: Go
// refuses the same shapes, and Go can be bypassed by a script, by sqlite3 on the
// command line, or by a collector that writes its own INSERT. The constraints
// are what make the shape impossible rather than merely discouraged.

// tsGood is the layout internal/store writes, spelled out rather than imported:
// the schema sits below the store and must not depend on it.
const tsGood = "2026-08-01T13:04:35.000000Z"

// tsFloor and tsCeil bracket every value in adjBadTimestamps in TEXT order, so
// that a row carrying a bad value in one endpoint is refused by the SHAPE guard
// and never by an ordering CHECK that happened to fire first. Getting this wrong
// would make these tests pass for the wrong reason.
const (
	tsFloor = "2026-01-01T00:00:00.000000Z"
	tsCeil  = "2026-12-31T23:59:59.000000Z"
)

// tsColumn is one TEXT timestamp column and the two rows needed to interrogate
// it: one carrying a caller-chosen value, and — where the column is genuinely
// nullable — one where the instant was never observed at all.
type tsColumn struct {
	table, column string

	// write inserts one row whose column holds v and whose every other column
	// holds something the schema accepts.
	write func(t *testing.T, d *sql.DB, v any) error

	// writeAbsent inserts a row in which this column is NULL, and is nil where
	// the column is NOT NULL and no such row can exist. Both are facts about
	// the schema: event.started_at is NULL on an unbounded stop because the
	// endpoint was never observed, and derived_session.started_at cannot be,
	// because a derived span with no start is not a span.
	writeAbsent func(t *testing.T, d *sql.DB) error
}

// tsUnguarded names TEXT timestamp columns deliberately left without a shape
// CHECK, with the reason. It exists so that "not guarded" is a recorded decision
// rather than an omission nobody noticed — the completeness test below refuses a
// column that appears in neither list.
var tsUnguarded = map[string]string{
	"schema_migration.applied_at": "written by SQLite's own strftime in the runner's ledger DDL rather than " +
		"by any caller, created with CREATE TABLE IF NOT EXISTS outside the numbered sequence — so a CHECK " +
		"added there would reach new databases only and make a fresh store's schema differ from a migrated " +
		"one — and never ordered as text: Current() orders by version. Its format is covered by " +
		"TestTheMigrationLedgerRecordsTimeTheSameWayEverythingElseDoes.",
}

// tsRun inserts a harvest_run for events to hang off and returns its id. It is
// aRun under a name that will not collide, and it is separate because the
// timestamp cases need runs with statuses aRun does not write.
func tsRun(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	return aRun(t, d)
}

// tsChain inserts the one derived_chain_run row and one chain hanging off it,
// returning the chain's id, so that a step has a parent to reference.
func tsChain(t *testing.T, d *sql.DB) int64 {
	t.Helper()
	if _, err := d.Exec(`INSERT INTO derived_chain_run
		(id, computed_at, source_id, classes, extensions, min_shared_prefix, coincidence_ms,
		 events_considered, documents, documents_chained)
		VALUES (1, ?, 'filedays', '[]', '[]', 4, 2000, 0, 0, 0)`, tsGood); err != nil {
		t.Fatalf("insert derived_chain_run: %v", err)
	}
	res, err := d.Exec(`INSERT INTO derived_chain
		(run_id, directory, root, started_at, ended_at, step_count, document_count)
		VALUES (1, '/Users/you/blender', 'animation_final', ?, ?, 2, 2)`, tsFloor, tsCeil)
	if err != nil {
		t.Fatalf("insert derived_chain: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// tsColumns is the audit. Every TEXT timestamp column in the schema appears here
// or in tsUnguarded, and the completeness assertion in
// TestEveryTextTimestampColumnRefusesASameWidthNonUTCValue is what keeps that
// true as migrations are added.
var tsColumns = []tsColumn{
	{
		table: "harvest_run", column: "started_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			_, err := d.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status)
				VALUES ('s', ?, '/tmp/stream', 'running')`, v)
			return err
		},
	},
	{
		table: "harvest_run", column: "finished_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			_, err := d.Exec(`INSERT INTO harvest_run (source_id, started_at, finished_at, input, status)
				VALUES ('s', ?, ?, '/tmp/stream', 'ok')`, tsFloor, v)
			return err
		},
		// A run in progress has not finished. The live store carries one right
		// now — row 28, claude-transcripts, stranded when the process died.
		writeAbsent: func(t *testing.T, d *sql.DB) error {
			_, err := d.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status)
				VALUES ('s', ?, '/tmp/stream', 'running')`, tsGood)
			return err
		},
	},
	{
		table: "harvest_run", column: "since",
		write: func(t *testing.T, d *sql.DB, v any) error {
			_, err := d.Exec(`INSERT INTO harvest_run (source_id, started_at, input, since, status)
				VALUES ('s', ?, '/tmp/stream', ?, 'running')`, tsGood, v)
			return err
		},
		// NULL since means the whole of it was asked for. All 293 rows in the
		// live store are NULL here (scripts/timestamp-shape-audit, snapshot
		// 2026-08-01T11:40:36-0500), so this arm is the only evidence there is
		// about what the column holds.
		writeAbsent: func(t *testing.T, d *sql.DB) error {
			_, err := d.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status)
				VALUES ('s', ?, '/tmp/stream', 'running')`, tsGood)
			return err
		},
	},
	{
		table: "event", column: "started_at",
		// A horizon: an unpaired start, seen frontmost with no resignation yet.
		write: func(t *testing.T, d *sql.DB, v any) error {
			return insert(d, tsRun(t, d), row{Shape: "unbounded", Unknown: "horizon", Started: v})
		},
		// A sleep stop: an unpaired end, where macOS wrote no start and no later
		// harvest will supply one. started_at is NULL because the instant was
		// never observed, which is the one thing a NULL here may mean.
		writeAbsent: func(t *testing.T, d *sql.DB) error {
			return insert(d, tsRun(t, d), row{Shape: "unbounded", Unknown: "sleep", Ended: tsGood})
		},
	},
	{
		table: "event", column: "ended_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return insert(d, tsRun(t, d), row{Shape: "unbounded", Unknown: "sleep", Ended: v})
		},
		writeAbsent: func(t *testing.T, d *sql.DB) error {
			return insert(d, tsRun(t, d), row{Shape: "unbounded", Unknown: "horizon", Started: tsGood})
		},
	},
	{
		table: "derived_session", column: "started_at",
		write: func(t *testing.T, d *sql.DB, v any) error { return tsSession(d, v, tsCeil) },
	},
	{
		table: "derived_session", column: "ended_at",
		write: func(t *testing.T, d *sql.DB, v any) error { return tsSession(d, tsFloor, v) },
	},
	// computed_at and the corpus bounds moved to derived_session_run in 0016,
	// which is where the derivation now lives; they are the same three columns
	// under a new table name. See tsSessionRun.
	{
		table: "derived_session_run", column: "computed_at",
		write: func(t *testing.T, d *sql.DB, v any) error { return tsSessionRun(d, v, "", "") },
	},
	// The corpus bounds, added by 0010. They are NOT NULL and their absent
	// state is the empty string rather than a NULL: "unbounded" is a value
	// here, saying that nothing was excluded by time, and it is a string no
	// instant can be confused with. That is why there is no writeAbsent — the
	// row a writeAbsent would insert cannot exist, and the empty case is
	// exercised in derivation_corpus_test.go where it belongs.
	{
		table: "derived_session_run", column: "corpus_from",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return tsSessionRun(d, tsGood, v, "")
		},
	},
	{
		table: "derived_session_run", column: "corpus_to",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return tsSessionRun(d, tsGood, "", v)
		},
	},
	{
		table: "derived_chain_run", column: "computed_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			_, err := d.Exec(`INSERT INTO derived_chain_run
				(id, computed_at, source_id, classes, extensions, min_shared_prefix, coincidence_ms,
				 events_considered, documents, documents_chained)
				VALUES (1, ?, 'filedays', '[]', '[]', 4, 2000, 0, 0, 0)`, v)
			return err
		},
	},
	{
		table: "derived_chain", column: "started_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			tsChain(t, d)
			_, err := d.Exec(`INSERT INTO derived_chain
				(run_id, directory, root, started_at, ended_at, step_count, document_count)
				VALUES (1, '/Users/you/blender', 'other_root', ?, ?, 2, 2)`, v, tsCeil)
			return err
		},
	},
	{
		table: "derived_chain", column: "ended_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			tsChain(t, d)
			_, err := d.Exec(`INSERT INTO derived_chain
				(run_id, directory, root, started_at, ended_at, step_count, document_count)
				VALUES (1, '/Users/you/blender', 'other_root', ?, ?, 2, 2)`, tsFloor, v)
			return err
		},
	},
	{
		table: "derived_chain_step", column: "observed_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			_, err := d.Exec(`INSERT INTO derived_chain_step
				(chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
				VALUES (?, 0, ?, '[]', '[]', 1, NULL)`, tsChain(t, d), v)
			return err
		},
	},
	{
		table: "capture_run", column: "started_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return insertCapture(d, "knowledgec", "/x", v, tsCeil, "ok", nil, 1, 0, 0)
		},
	},
	{
		table: "capture_run", column: "finished_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return insertCapture(d, "knowledgec", "/x", tsFloor, v, "ok", nil, 1, 0, 0)
		},
	},
	{
		table: "adjustment_review", column: "started_at",
		write: func(t *testing.T, d *sql.DB, v any) error { return adjReviewInsert(d, v, tsCeil, tsGood) },
	},
	{
		table: "adjustment_review", column: "ended_at",
		write: func(t *testing.T, d *sql.DB, v any) error { return adjReviewInsert(d, tsFloor, v, tsGood) },
	},
	{
		table: "adjustment_review", column: "recorded_at",
		write: func(t *testing.T, d *sql.DB, v any) error { return adjReviewInsert(d, tsFloor, tsCeil, v) },
	},
	{
		table: "adjustment", column: "started_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return adjInsert(d, adjRow{Started: v, Ended: tsCeil})
		},
	},
	{
		table: "adjustment", column: "ended_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return adjInsert(d, adjRow{Started: tsFloor, Ended: v})
		},
	},
	{
		table: "adjustment", column: "recorded_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return adjInsert(d, adjRow{Started: tsFloor, Ended: tsCeil, RecordedAt: v})
		},
	},
	{
		// 0012's log. The fold orders on recorded_at, so a mis-sorting here
		// silently reverses which of two assertions about one project won.
		table: "client_project", column: "recorded_at",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return cpInsert(d, nil, v)
		},
	},
	{
		// effective_from is compared against EVENT timestamps rather than
		// against other rows here, which is what decides whether a reassignment
		// covers a given instant. A same-width non-UTC value there moves a
		// client boundary by hours without failing anything.
		table: "client_project", column: "effective_from",
		write: func(t *testing.T, d *sql.DB, v any) error {
			return cpInsert(d, v, tsGood)
		},
		// NULL is the ordinary case and means "from the beginning of time": a
		// correction, or a first registration reaching back over evidence that
		// already exists. It is not an absent value that nobody got round to
		// filling in, which is why it carries its own IS NULL arm in the CHECK
		// rather than the column being NOT NULL.
		writeAbsent: func(t *testing.T, d *sql.DB) error {
			return cpInsert(d, nil, tsGood)
		},
	},
}

// cpInsert writes one client_project assertion with the two timestamps spelled
// by the caller and every other column at something the schema accepts.
func cpInsert(d *sql.DB, effectiveFrom, recordedAt any) error {
	_, err := d.Exec(`INSERT INTO client_project
		(project, client, source, effective_from, recorded_at, origin)
		VALUES ('loom', 'atelier', '/git/loom', ?, ?, 'a-person')`, effectiveFrom, recordedAt)
	return err
}

// tsSession writes one derived_session with its two timestamps spelled by the
// caller, and the derivation it hangs off.
//
// The run row comes first because 0016 made it have to: run_id is NOT NULL and
// references derived_session_run, so a sitting cannot exist before the
// derivation that produced it. Its own timestamps are written at values the
// schema accepts, because this helper's subject is the sitting's endpoints;
// derived_session_run.computed_at and the corpus bounds have their own entries
// in tsColumns above and go through tsSessionRun.
func tsSession(d *sql.DB, started, ended any) error {
	if err := tsSessionRun(d, tsGood, "", ""); err != nil {
		return err
	}
	_, err := d.Exec(`INSERT INTO derived_session
		(run_id, started_at, ended_at, events, sources, margin_micros)
		VALUES (1, ?, ?, 3, 'biome', 0)`, started, ended)
	return err
}

// tsSessionRun writes the one derivation row with its three timestamps spelled
// by the caller and every other column — thirteen statistics included — at
// something the schema accepts.
func tsSessionRun(d *sql.DB, computed, from, to any) error {
	_, err := d.Exec(`INSERT INTO derived_session_run
		(id, computed_at, gap_micros, lead_in_micros, tail_out_micros,
		 presence_sources, ignore_sources, darkness_policy,
		 exclude_patterns, corpus_from, corpus_to,
		 evidence_observations, presence_observations, ignored_observations,
		 unbounded_observations, presence_without_span, merged_by_expansion,
		 split_by_absence, trimmed_by_absence_micros, suppressed_by_absence,
		 unevidenced_pieces, unevidenced_dropped_micros,
		 contested_micros, observed_absence_micros)
		VALUES (1, ?, 900000000, 0, 0, '', '', 'suppress-all-and-trim', '[]', ?, ?,
		        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`, computed, from, to)
	return err
}

// tsSessionBefore0010 writes a derived_session into a schema that has not yet
// grown the corpus columns. It exists for the two tests that seed rows in front
// of a migration and watch what it does to them, and it is separate rather than
// conditional so that neither helper quietly writes the wrong shape.
func tsSessionBefore0010(d *sql.DB, started, ended, computed any) error {
	_, err := d.Exec(`INSERT INTO derived_session
		(started_at, ended_at, events, sources, margin_micros, computed_at,
		 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources)
		VALUES (?, ?, 3, 'biome', 0, ?, 900000000, 0, 0, '', '')`, started, ended, computed)
	return err
}

// tsIrregular names the TEXT columns that carry an instant WITHOUT following the
// _at naming convention that discovery keys on. There is one, and it is the
// reason this list exists rather than a pure pattern match: the first draft of
// this audit — and of scripts/timestamp-shape-audit beside it — discovered by
// the pattern alone and therefore silently reported nothing whatever about
// harvest_run.since, which is the exact failure the audit is supposed to catch.
//
// It is checked against the schema in tsDiscovered, so it cannot rot into a name
// that no longer exists.
//
// corpus_from and corpus_to joined it with 0010, on derived_session, and moved
// to derived_session_run with the rest of the derivation in 0016. They carry
// the window the events were read over, they are ordered against the same
// timestamps as everything else, and neither is named _at because neither is a
// moment something happened — they are the bounds of a population. Without this
// list the discovery query would pass over both in silence, which is the exact
// failure harvest_run.since is here to remember.
var tsIrregular = []string{
	"harvest_run.since",
	"derived_session_run.corpus_from",
	"derived_session_run.corpus_to",
}

// tsDiscovered reads every TEXT column that carries an instant out of the live
// schema, so that the audit cannot go stale: a migration adding a timestamp
// column makes the completeness assertion fail until somebody decides, in
// writing, whether it is guarded.
func tsDiscovered(t *testing.T, d *sql.DB) []string {
	t.Helper()
	rows, err := d.Query(`SELECT m.name, c.name FROM sqlite_master AS m
		JOIN pragma_table_info(m.name) AS c
		WHERE m.type = 'table' AND c.type = 'TEXT' AND c.name LIKE '%\_at' ESCAPE '\'
		ORDER BY m.name, c.name`)
	if err != nil {
		t.Fatalf("discover timestamp columns: %v", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var table, column string
		if err := rows.Scan(&table, &column); err != nil {
			t.Fatalf("scan timestamp column: %v", err)
		}
		out = append(out, table+"."+column)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("discover timestamp columns: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("discovered no TEXT timestamp columns at all; the query is wrong and this audit measures nothing")
	}

	for _, name := range tsIrregular {
		table, column, _ := strings.Cut(name, ".")
		var found int
		if err := d.QueryRow(`SELECT count(*) FROM sqlite_master AS m
			JOIN pragma_table_info(m.name) AS c
			WHERE m.type = 'table' AND m.name = ? AND c.name = ? AND c.type = 'TEXT'`,
			table, column).Scan(&found); err != nil {
			t.Fatalf("look for the irregular timestamp column %s: %v", name, err)
		}
		if found != 1 {
			t.Errorf("tsIrregular names %s, which is not a TEXT column in this schema; "+
				"the list has gone stale and is hiding rather than covering a column", name)
			continue
		}
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// TestEveryTextTimestampColumnRefusesASameWidthNonUTCValue is criteria 6O5 and
// Oad together, and they are one test on purpose: an audit that lists the
// columns without exercising them proves nothing, and exercising a list nobody
// checked against the schema proves nothing either.
//
// The value that matters most in adjBadTimestamps is
// '2026-08-01T08:04:35.0-05:00'. It is exactly 27 characters — the same width
// the store writes — and it is five hours out of UTC, so the length() guard
// 0003 shipped reports success while the ordering it exists to protect is wrong
// by five hours.
func TestEveryTextTimestampColumnRefusesASameWidthNonUTCValue(t *testing.T) {
	covered := map[string]bool{}
	for _, c := range tsColumns {
		covered[c.table+"."+c.column] = true
	}
	for name := range tsUnguarded {
		covered[name] = true
	}
	for _, found := range tsDiscovered(t, db(t)) {
		if !covered[found] {
			t.Errorf("%s is a TEXT timestamp column that this audit does not cover; "+
				"add it to tsColumns, or to tsUnguarded with the reason it is left alone", found)
		}
	}

	for _, c := range tsColumns {
		name := c.table + "." + c.column
		if _, ok := tsUnguarded[name]; ok {
			t.Errorf("%s is in both tsColumns and tsUnguarded; one of them is a mistake", name)
			continue
		}

		// Both arms. A constraint that refuses the layout the store writes
		// guards nothing anybody would notice, and one that accepts everything
		// guards nothing at all.
		if err := c.write(t, db(t), tsGood); err != nil {
			t.Errorf("%s refused %q, which is exactly what internal/store.TimeLayout writes: %v",
				name, tsGood, err)
		}
		for _, bad := range adjBadTimestamps {
			if err := c.write(t, db(t), bad.text); err == nil {
				t.Errorf("%s accepted %q (%s); every read that orders on this column is a TEXT "+
					"comparison and is chronological only at one fixed shape", name, bad.text, bad.why)
			}
		}
	}
}

// TestANullableTimestampStillRecordsThatNothingWasObserved is the other half of
// criterion 78Z, and it is the arm that a tightened guard breaks.
//
// event.started_at, event.ended_at, harvest_run.finished_at and
// harvest_run.since are NULL for a reason in every case — an endpoint that was
// never observed, a run still in flight, a collection with no lower bound — and
// a guard that refused those would turn "nothing was recorded" into an error,
// which is the collapse this project has now shipped twice in three tools.
func TestANullableTimestampStillRecordsThatNothingWasObserved(t *testing.T) {
	for _, c := range tsColumns {
		if c.writeAbsent == nil {
			continue
		}
		if err := c.writeAbsent(t, db(t)); err != nil {
			t.Errorf("%s.%s refused a row in which the instant was never observed: %v",
				c.table, c.column, err)
		}
	}
}

// TestABareGlobOnANullableColumnAdmitsNull pins the SQLite rule the IS NULL
// guards are written against, as an executable fact rather than a claim in a
// comment — the same reason TestMixedTimestampShapesSortWrongly exists.
//
// It asserts a property of SQLite and not of this schema, and it is worth being
// precise about what it shows. A CHECK PASSES when it evaluates to NULL, so a
// bare GLOB on a nullable column admits NULL. On these columns NULL is legal, so
// the bare form and the `x IS NULL OR x GLOB …` form happen to behave the same —
// which is exactly why the guard is spelled out. A reader must not have to
// work out whether the NULL case was intended or overlooked, and the next
// migration that folds two of these into one conjunction must not have to
// rediscover the rule. Migration 0002 makes the same argument for event.cause.
func TestABareGlobOnANullableColumnAdmitsNull(t *testing.T) {
	d := open(t)
	if _, err := d.Exec(`CREATE TABLE bare (at TEXT CHECK (at GLOB '[0-9][0-9][0-9][0-9]-*Z'))`); err != nil {
		t.Fatalf("create the demonstration table: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO bare (at) VALUES (NULL)`); err != nil {
		t.Fatalf("a bare GLOB refused NULL, so a CHECK no longer passes when it evaluates to NULL "+
			"and every IS NULL guard in this schema is arguing against a rule that changed: %v", err)
	}
	if _, err := d.Exec(`INSERT INTO bare (at) VALUES ('nonsense')`); err == nil {
		t.Fatal("the demonstration CHECK accepted 'nonsense', so it is not enforcing anything and shows nothing")
	}

	var isNull bool
	if err := d.QueryRow(`SELECT (NULL GLOB 'x') IS NULL`).Scan(&isNull); err != nil {
		t.Fatalf("evaluate a GLOB against NULL: %v", err)
	}
	if !isNull {
		t.Error("NULL GLOB 'x' is not NULL; the premise the IS NULL guards rest on has changed")
	}
}

// TestTheShapeGuardRebuildKeepsEveryRowAndEveryReader is the cost of the only
// mechanism SQLite offers. There is no ALTER TABLE ADD CONSTRAINT, so a CHECK
// added to a table that already exists means rebuilding it: create, copy, drop,
// rename, and put back every index and every view that hung off it. On `event`
// that is 914,521 irreplaceable observations in the live store, and the risk is
// not the copy — it is silently failing to restore something.
//
// So this drives the real migration over a database at 0008 holding rows of
// every shape, and checks the rows, the ids, the foreign key, the dedup index
// and the canonical view on the other side.
func TestTheShapeGuardRebuildKeepsEveryRowAndEveryReader(t *testing.T) {
	d := tsAt(t, 8)

	run := aRun(t, d)
	// One of each shape, including both unbounded forms, so a rebuild that
	// dropped a CHECK or a column shows up as a row that changed rather than as
	// a row that vanished.
	for _, r := range []row{
		{Shape: "interval", Started: at0901, Ended: at0902, App: "com.apple.motionapp"},
		{Shape: "point", Started: at0901, Ended: at0901, Document: "dark-crystal-v2", Cause: "timer"},
		// This horizon shares its start and its app with the interval above, so
		// canonical_event supersedes it. That is the only way the assertion
		// below actually exercises the view rather than counting rows.
		{Shape: "unbounded", Unknown: "horizon", Started: at0901, App: "com.apple.motionapp"},
		{Shape: "unbounded", Unknown: "sleep", Ended: at0902, App: "com.apple.Safari"},
	} {
		if err := insert(d, run, r); err != nil {
			t.Fatalf("insert %v at 0008: %v", r, err)
		}
	}
	if err := tsSessionBefore0010(d, at0901, at0902, tsGood); err != nil {
		t.Fatalf("insert derived_session at 0008: %v", err)
	}

	before := tsFingerprint(t, d)

	// 0009 and no further. This test's subject is what the shape-guard rebuild
	// did to rows that were already there, and 0010 discards every
	// derived_session row on purpose — a derivation whose corpus nobody
	// recorded has no honest value to carry forward, and that is asserted in
	// derivation_corpus_test.go rather than smuggled in here as data loss.
	if err := tsApplyUpTo(t, d, 9); err != nil {
		t.Fatalf("Apply the shape guard migration: %v", err)
	}

	if after := tsFingerprint(t, d); after != before {
		t.Errorf("the rebuild changed the data.\nbefore: %s\nafter:  %s", before, after)
	}

	// The guard is the point of the migration; without this the test above
	// passes on a migration that did nothing.
	if err := insert(d, run, row{Shape: "unbounded", Unknown: "horizon",
		Started: "2026-08-01T08:04:35.0-05:00", App: "com.apple.Finder"}); err == nil {
		t.Error("event.started_at still accepts a 27-character non-UTC value after the migration")
	}

	// Everything that hung off the rebuilt tables has to still hang off them.
	if err := insert(d, 999, row{Shape: "point", Started: at0902, Ended: at0902}); err == nil {
		t.Error("an event referencing a nonexistent run was accepted; the foreign key did not survive the rebuild")
	}
	if err := insert(d, run, row{Shape: "unbounded", Unknown: "sleep", Ended: at0902, App: "com.apple.Safari"}); err == nil {
		t.Error("a duplicate bare stop inserted; event_identity and its COALESCEs did not survive the rebuild")
	}
	var canonical int
	if err := d.QueryRow(`SELECT COUNT(*) FROM canonical_event`).Scan(&canonical); err != nil {
		t.Fatalf("read canonical_event after the rebuild: %v", err)
	}
	// Four rows, of which the horizon is superseded by the interval that shares
	// its start, app and (absent) document and state.
	if canonical != 3 {
		t.Errorf("canonical_event holds %d rows, want 3; the supersession view did not survive the rebuild", canonical)
	}
	for _, index := range []string{"event_identity", "event_span", "event_source", "event_project",
		"event_locator", "harvest_run_source", "derived_session_span"} {
		var name string
		err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&name)
		if err != nil {
			t.Errorf("index %s is missing after the rebuild: %v", index, err)
		}
	}
}

// TestARowThatIsAlreadyMisshapenStopsTheMigration is the property that decided
// the mechanism. A rebuild validates the rows that are ALREADY there, because
// every one of them passes through the new CHECK on the way into the new table.
// A trigger would have been instant and would have guarded only the future,
// leaving a store that says it is guarded and is not.
//
// It also has to fail SAFE: the migration runs in one transaction, so a store
// holding a bad row must come out untouched and still at 0008, rather than half
// rebuilt.
func TestARowThatIsAlreadyMisshapenStopsTheMigration(t *testing.T) {
	d := tsAt(t, 8)
	run := aRun(t, d)

	const bad = "2026-08-01T08:04:35.0-05:00"
	if err := insert(d, run, row{Shape: "unbounded", Unknown: "horizon", Started: bad}); err != nil {
		t.Fatalf("0008 refused the 27-character non-UTC value, so there is nothing to migrate over: %v", err)
	}

	if err := migrations.Apply(d); err == nil {
		t.Error("the migration ran over a row it could not have written; " +
			"a rebuild that admits the population it is guarding guards nothing")
	}

	var version int
	if err := d.QueryRow(`SELECT MAX(version) FROM schema_migration`).Scan(&version); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if version != 8 {
		t.Errorf("the ledger reads %d after a failed migration, want 8; it must roll back whole", version)
	}
	var kept string
	if err := d.QueryRow(`SELECT started_at FROM event`).Scan(&kept); err != nil {
		t.Fatalf("read the event back: %v", err)
	}
	if kept != bad {
		t.Errorf("the event reads %q after the failed migration, want %q; nothing may be rewritten", kept, bad)
	}
}

// tsAt returns a database migrated to exactly version v and no further, so a
// test can put rows in front of a migration and watch what it does to them.
func tsAt(t *testing.T, v int) *sql.DB {
	t.Helper()
	d := open(t)
	if err := tsApplyUpTo(t, d, v); err != nil {
		t.Fatalf("apply migrations up to %d: %v", v, err)
	}
	return d
}

// tsApplyUpTo runs every migration through version v against d, and no later
// one, so a test can watch ONE migration meet the rows in front of it.
//
// It refuses a version at or above the newest, because a test that asked for
// "everything" through this path would be asserting against a schema that no
// longer changes and would go on passing after the next migration landed.
func tsApplyUpTo(t *testing.T, d *sql.DB, v int) error {
	t.Helper()
	all, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}
	var upTo []migrations.Script
	for _, s := range all {
		if s.Version <= v {
			upTo = append(upTo, s)
		}
	}
	if len(upTo) == len(all) {
		t.Fatalf("there is no migration above version %d, so this test is asserting against a schema "+
			"that no longer changes", v)
	}
	return migrations.ApplyScripts(d, upTo)
}

// tsFingerprint renders every row of the tables a rebuild touches, in a stable
// order and including the ids, so that "the data came through unchanged" is a
// comparison rather than a hope. ids are in it deliberately: event.first_seen_run
// points at harvest_run.id, and a rebuild that renumbered either would silently
// re-attribute every observation.
func tsFingerprint(t *testing.T, d *sql.DB) string {
	t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, source_id, started_at, finished_at, input, since, status, error, events_seen, events_new
		 FROM harvest_run ORDER BY id`,
		`SELECT id, first_seen_run, source_id, shape, unknown, started_at, ended_at, app, document,
		        state, locator, project, payload, cause FROM event ORDER BY id`,
		`SELECT id, started_at, ended_at, events, sources, margin_micros, computed_at, gap_micros,
		        lead_in_micros, tail_out_micros, presence_sources, ignore_sources
		 FROM derived_session ORDER BY id`,
	} {
		rows, err := d.Query(q)
		if err != nil {
			t.Fatalf("fingerprint: %v", err)
		}
		cols, err := rows.Columns()
		if err != nil {
			t.Fatalf("fingerprint columns: %v", err)
		}
		for rows.Next() {
			cells := make([]any, len(cols))
			into := make([]any, len(cols))
			for i := range cells {
				into[i] = &cells[i]
			}
			if err := rows.Scan(into...); err != nil {
				t.Fatalf("fingerprint scan: %v", err)
			}
			var parts []string
			for i, c := range cells {
				parts = append(parts, fmt.Sprintf("%s=%v", cols[i], c))
			}
			sort.Strings(parts)
			fmt.Fprintf(&b, "%s\n", strings.Join(parts, " "))
		}
		rows.Close()
	}
	return b.String()
}
