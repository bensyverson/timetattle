package migrations_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// 0016 moves the derivation off every session row onto one row of its own, and
// records what the derivation DID as well as what it was given.
//
// These tests drive the schema with raw SQL, because the question is what the
// database refuses rather than what Go happens to send it. The Go round trip is
// internal/store's.

// drRun writes the singleton run row with every column at something the schema
// accepts, and lets a caller override any of them.
//
// It is a map rather than a struct because most tests vary exactly one column
// and a struct would make every test restate twelve statistics it does not care
// about.
func drRun(d *sql.DB, override map[string]any) error {
	cols := map[string]any{
		"id":          1,
		"computed_at": "2026-08-14T13:04:35.000000Z",

		"gap_micros":       900000000,
		"lead_in_micros":   300000000,
		"tail_out_micros":  300000000,
		"presence_sources": "knowledgec-backlit",
		"ignore_sources":   "",
		"darkness_policy":  "suppress-all-and-trim",

		"exclude_patterns": "[]",
		"corpus_from":      "",
		"corpus_to":        "",

		"evidence_observations":      1079551,
		"presence_observations":      2149,
		"ignored_observations":       4088,
		"unbounded_observations":     745,
		"presence_without_span":      0,
		"merged_by_expansion":        0,
		"split_by_absence":           129,
		"suppressed_by_absence":      24704,
		"unevidenced_pieces":         1,
		"trimmed_by_absence_micros":  174960000000,
		"unevidenced_dropped_micros": 0,
		"contested_micros":           47880000000,
		"observed_absence_micros":    2302920000000,
	}
	for k, v := range override {
		if _, ok := cols[k]; !ok {
			return fmt.Errorf("drRun was given %q, which is not a column of derived_session_run", k)
		}
		cols[k] = v
	}

	var names, holes []string
	var args []any
	for name, value := range cols {
		names = append(names, name)
		holes = append(holes, "?")
		args = append(args, value)
	}
	_, err := d.Exec(`INSERT INTO derived_session_run (`+strings.Join(names, ", ")+
		`) VALUES (`+strings.Join(holes, ", ")+`)`, args...)
	return err
}

// drGrounded proves the schema accepts a LEGAL run row before a test starts
// asserting which illegal ones it refuses, and leaves the table empty.
//
// Without it every refusal test below passes on a database with no such table
// at all: "the insert failed" is satisfied by a missing table exactly as it is
// by a violated CHECK, and a test that cannot tell those apart is testing
// nothing. This project has shipped that confusion in three tools under a
// different name — an absence read as an observation.
func drGrounded(t *testing.T, d *sql.DB) {
	t.Helper()
	if err := drRun(d, nil); err != nil {
		t.Fatalf("the schema refused a legal run row, so nothing below distinguishes "+
			"a violated constraint from a broken fixture: %v", err)
	}
	if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
		t.Fatalf("clear: %v", err)
	}
}

// drSitting writes one sitting hanging off the run row.
func drSitting(d *sql.DB, runID any, started, ended string) error {
	_, err := d.Exec(`INSERT INTO derived_session
		(run_id, started_at, ended_at, events, sources, margin_micros)
		VALUES (?, ?, ?, 3, 'biome-appusage', 0)`, runID, started, ended)
	return err
}

const (
	drStart = "2026-07-11T09:00:00.000000Z"
	drEnd   = "2026-07-11T10:00:00.000000Z"
)

// The whole shape, in one test: one run row carries the parameters, the corpus
// and the statistics, and the sittings hang off it.
func TestOneRunRowCarriesTheDerivationForEverySitting(t *testing.T) {
	d := migrated(t)
	if err := drRun(d, nil); err != nil {
		t.Fatalf("write the run row: %v", err)
	}
	if err := drSitting(d, 1, drStart, drEnd); err != nil {
		t.Fatalf("write a sitting: %v", err)
	}

	var gap, suppressed int64
	var policy string
	if err := d.QueryRow(`SELECT r.gap_micros, r.suppressed_by_absence, r.darkness_policy
		FROM derived_session s JOIN derived_session_run r ON r.id = s.run_id`).
		Scan(&gap, &suppressed, &policy); err != nil {
		t.Fatalf("join a sitting to its derivation: %v", err)
	}
	if gap != 900000000 || suppressed != 24704 || policy != "suppress-all-and-trim" {
		t.Errorf("the derivation read back as gap %d, %d suppressed, policy %q", gap, suppressed, policy)
	}
}

// The statistics are the point of the migration. Every one of sessions.Stats
// has a column, and a run row that omits any of them is refused — a count that
// was never measured must not be able to read as a count that was zero.
func TestEveryStatisticOfADerivationHasAColumnThatMustBeWritten(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	for _, col := range []string{
		"evidence_observations", "presence_observations", "ignored_observations",
		"unbounded_observations", "presence_without_span", "merged_by_expansion",
		"split_by_absence", "suppressed_by_absence", "unevidenced_pieces",
		"trimmed_by_absence_micros", "unevidenced_dropped_micros",
		"contested_micros", "observed_absence_micros",
	} {
		if err := drRun(d, map[string]any{col: nil}); err == nil {
			t.Errorf("a run row inserted with a NULL %s; "+
				"\"nobody measured it\" would then be indistinguishable from \"it was zero\"", col)
			if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
				t.Fatalf("clear: %v", err)
			}
		}
	}
}

// A negative count is not a small number; it is a derivation that has miscounted
// and would go on being read as a fact. derived_chain_run guards each of its
// three counters this way and this table has thirteen.
func TestNoStatisticOfADerivationMayBeNegative(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	for _, col := range []string{
		"evidence_observations", "presence_observations", "ignored_observations",
		"unbounded_observations", "presence_without_span", "merged_by_expansion",
		"split_by_absence", "suppressed_by_absence", "unevidenced_pieces",
		"trimmed_by_absence_micros", "unevidenced_dropped_micros",
		"contested_micros", "observed_absence_micros",
	} {
		if err := drRun(d, map[string]any{col: -1}); err == nil {
			t.Errorf("%s accepted -1", col)
			if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
				t.Fatalf("clear: %v", err)
			}
		}
	}
}

// The cross-column invariants. Each is a property of the clustering rather than
// a taste: an unbounded observation is an EVIDENCE observation, a presence
// reading with no span is a PRESENCE observation, a suppressed observation was
// evidence before it was suppressed, and trimmed time is a subset of the
// darkness it was trimmed out of — ObservedAbsence is the denominator
// TrimmedByAbsence is a fraction of, and sessions.Stats says so.
func TestTheStatisticsOfADerivationCannotContradictEachOther(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	for _, c := range []struct {
		name     string
		override map[string]any
	}{
		{"more unbounded observations than evidence",
			map[string]any{"evidence_observations": 3, "unbounded_observations": 4}},
		{"more spanless presence readings than presence readings",
			map[string]any{"presence_observations": 3, "presence_without_span": 4}},
		{"more evidence suppressed by darkness than there was evidence",
			map[string]any{"evidence_observations": 3, "suppressed_by_absence": 4}},
		{"more time trimmed than was ever observed dark",
			map[string]any{"observed_absence_micros": 3, "trimmed_by_absence_micros": 4}},
	} {
		if err := drRun(d, c.override); err == nil {
			t.Errorf("a run row inserted claiming %s", c.name)
			if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
				t.Fatalf("clear: %v", err)
			}
		}
	}
	// And the boundary cases, which are legal: every one of those may be equal.
	// A corpus in which every evidence observation was unbounded is a strange
	// corpus and not an impossible one, and a constraint that refused it would
	// be refusing an honest reading.
	if err := drRun(d, map[string]any{
		"evidence_observations": 4, "unbounded_observations": 4, "suppressed_by_absence": 4,
		"presence_observations": 2, "presence_without_span": 2,
		"observed_absence_micros": 3, "trimmed_by_absence_micros": 3,
	}); err != nil {
		t.Errorf("a run row in which each pair is equal was refused: %v", err)
	}
}

// Exactly one derivation is current. derived_chain_run makes that a CHECK
// rather than a convention, and the reason is stronger here: until this
// migration, `Store.Sittings` had to reconcile the parameters across 1557 rows
// on every read and refuse a table that disagreed with itself. A second run row
// cannot be inserted beside the first, so there is no mixture left to reconcile.
func TestOnlyOneDerivationCanBeCurrent(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	if err := drRun(d, nil); err != nil {
		t.Fatalf("write the run row: %v", err)
	}
	if err := drRun(d, map[string]any{"id": 2, "gap_micros": 7200000000}); err == nil {
		t.Error("a second derivation inserted beside the first; " +
			"half the sittings would then be measured against a different model")
	}
}

// A sitting separated from its parameters is a span with no meaning, which is
// the argument 0003 makes for restating them on every row. The reference is what
// keeps that true after the restating stops: with foreign keys on — which
// store.Open sets — a sitting cannot be written before the derivation that
// produced it exists.
func TestASittingCannotBeWrittenWithoutTheDerivationThatProducedIt(t *testing.T) {
	d := migrated(t)
	if err := drSitting(d, 1, drStart, drEnd); err == nil {
		t.Error("a sitting inserted with no run row to hang off; " +
			"it is a span with no parameters and nothing could interpret it")
	}
	if err := drSitting(d, nil, drStart, drEnd); err == nil {
		t.Error("a sitting inserted with a NULL run_id")
	}
	if err := drRun(d, nil); err != nil {
		t.Fatalf("write the run row: %v", err)
	}
	if err := drSitting(d, 2, drStart, drEnd); err == nil {
		t.Error("a sitting inserted pointing at a run that does not exist")
	}
	if err := drSitting(d, 1, drStart, drEnd); err != nil {
		t.Errorf("a sitting hanging off the run that produced it was refused: %v", err)
	}
}

// And the other direction, which is what makes the pair replaceable as one
// thing: clearing the derivation clears the sittings it produced. Without the
// cascade, `sessionize` could leave 1557 sittings pointing at parameters that
// had been deleted out from under them.
func TestClearingTheDerivationClearsTheSittingsItProduced(t *testing.T) {
	d := migrated(t)
	if err := drRun(d, nil); err != nil {
		t.Fatalf("write the run row: %v", err)
	}
	for i := range 3 {
		start := fmt.Sprintf("2026-07-11T%02d:00:00.000000Z", 9+i)
		end := fmt.Sprintf("2026-07-11T%02d:30:00.000000Z", 9+i)
		if err := drSitting(d, 1, start, end); err != nil {
			t.Fatalf("write a sitting: %v", err)
		}
	}
	if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
		t.Fatalf("clear the derivation: %v", err)
	}
	var left int
	if err := d.QueryRow(`SELECT COUNT(*) FROM derived_session`).Scan(&left); err != nil {
		t.Fatalf("count the sittings: %v", err)
	}
	if left != 0 {
		t.Errorf("%d sittings survived the derivation being cleared, pointing at parameters "+
			"that no longer exist", left)
	}
}

// The darkness policy is what a derivation let observed darkness DO to it, and
// it has to be recorded because it is about to change: rows derived before and
// after must not read alike. It is NOT NULL and non-empty for the same reason
// every other column here is — a policy nobody recorded is not a policy.
//
// The value set is deliberately NOT pinned by a CHECK. SQLite cannot widen one
// without rebuilding the table, and this column exists precisely to hold values
// that do not exist yet.
func TestADerivationMustNameTheDarknessPolicyItRanUnder(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	if err := drRun(d, map[string]any{"darkness_policy": nil}); err == nil {
		t.Error("a run row inserted with no darkness policy")
	}
	if err := drRun(d, map[string]any{"darkness_policy": ""}); err == nil {
		t.Error("a run row inserted with an empty darkness policy; " +
			"the empty string is not the name of a rule")
	}
	// A policy nobody has written yet has to be storable, or the column
	// records nothing the day the rule changes.
	if err := drRun(d, map[string]any{"darkness_policy": "suppress-timer-only"}); err != nil {
		t.Errorf("a policy this build does not implement was refused: %v", err)
	}
}

// The timestamp guard 0009 put on every instant in this database. computed_at
// is the one instant on the run row, and the corpus bounds are the two that may
// be unbounded — empty is a VALUE and anything else has to be an instant in the
// one layout the TEXT ordering rests on.
func TestTheRunRowsInstantsCarryTheOneStoredLayout(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	bad := []any{
		nil,
		"2026-08-14",
		"2026-08-14T13:04:35Z",
		// The same width as the layout and five hours out of UTC: the trap
		// 0009 measured, which a length check reports as success.
		"2026-08-01T08:04:35.0-05:00",
	}
	for _, v := range bad {
		if err := drRun(d, map[string]any{"computed_at": v}); err == nil {
			t.Errorf("computed_at accepted %v", v)
			if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
				t.Fatalf("clear: %v", err)
			}
		}
		for _, col := range []string{"corpus_from", "corpus_to"} {
			if err := drRun(d, map[string]any{col: v}); err == nil {
				t.Errorf("%s accepted %v", col, v)
				if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
					t.Fatalf("clear: %v", err)
				}
			}
		}
	}
	if err := drRun(d, map[string]any{
		"corpus_from": "2026-07-12T09:00:00.000000Z",
		"corpus_to":   "2026-07-11T09:00:00.000000Z",
	}); err == nil {
		t.Error("a corpus window that ends before it begins was accepted; no observation can be in it")
	}
}

// The exclude list moved with the rest of the corpus and keeps 0010's guard: a
// value nobody can read back as a list is not a record of what was excluded.
func TestTheRunRowsExcludeListMustStillBeAJSONArray(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	for _, bad := range []any{nil, "", "null", "*.log", `{"a":1}`, `["unterminated`} {
		if err := drRun(d, map[string]any{"exclude_patterns": bad}); err == nil {
			t.Errorf("exclude_patterns accepted %v", bad)
			if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
				t.Fatalf("clear: %v", err)
			}
		}
	}
	// A path may hold a comma, which is why this is JSON and not the
	// comma-joined form the source columns use.
	if err := drRun(d, map[string]any{
		"exclude_patterns": `["/Users/you/Documents/Smith, John/"]`,
	}); err != nil {
		t.Errorf("exclude_patterns refused a pattern holding a comma: %v", err)
	}
}

// The parameter guards 0003 wrote, carried onto the row that now holds them.
// A gap of zero makes every observation its own sitting; a negative margin is
// not a margin.
func TestTheRunRowKeepsTheParameterGuards(t *testing.T) {
	d := migrated(t)
	drGrounded(t, d)
	for _, c := range []struct {
		name     string
		override map[string]any
	}{
		{"a gap of zero", map[string]any{"gap_micros": 0}},
		{"a negative gap", map[string]any{"gap_micros": -1}},
		{"a negative lead-in", map[string]any{"lead_in_micros": -1}},
		{"a negative tail-out", map[string]any{"tail_out_micros": -1}},
		{"a NULL presence list", map[string]any{"presence_sources": nil}},
		{"a NULL ignore list", map[string]any{"ignore_sources": nil}},
	} {
		if err := drRun(d, c.override); err == nil {
			t.Errorf("the run row accepted %s", c.name)
			if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
				t.Fatalf("clear: %v", err)
			}
		}
	}
	// Naming no presence source and no ignored source is a fact about the
	// derivation, and the empty string is how it is spelled.
	if err := drRun(d, map[string]any{"presence_sources": "", "ignore_sources": ""}); err != nil {
		t.Errorf("a derivation that read no source as presence was refused: %v", err)
	}
}

// What happens to the derivation already in the table, said out loud.
//
// It is DISCARDED rather than carried forward, for the reason 0010 discarded
// its own predecessor: there is no honest value to write into the thirteen
// statistic columns for a row that already exists. Those derivations recorded
// what they were GIVEN and never what they DID, so defaulting the counts to
// zero would state as a fact the very thing this migration exists to record —
// and it would do it in the one place a reader is entitled to trust. The live
// table's real figures are 24,704 observations suppressed and 48.6h trimmed;
// zero is not a rounding of those.
//
// An empty pair of tables is the third state the design already has a name for:
// nothing has been derived, which internal/store reports as such. The cost of
// the whole discard is one `timetattle sessionize`.
func TestMigratingDiscardsADerivationWhoseStatisticsNobodyRecorded(t *testing.T) {
	d := tsAt(t, 15)
	if _, err := d.Exec(`INSERT INTO derived_session
		(started_at, ended_at, events, sources, margin_micros, computed_at,
		 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources,
		 exclude_patterns, corpus_from, corpus_to)
		VALUES (?, ?, 3, 'biome-appusage', 0, '2026-08-01T13:32:33.000000Z',
		        900000000, 300000000, 300000000, 'knowledgec-backlit', '', '[]', '', '')`,
		drStart, drEnd); err != nil {
		t.Fatalf("write a derivation at 0015: %v", err)
	}

	if err := migrations.Apply(d); err != nil {
		t.Fatalf("apply 0016: %v", err)
	}

	for _, table := range []string{"derived_session", "derived_session_run"} {
		var n int
		if err := d.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
			t.Fatalf("count %s after 0016: %v", table, err)
		}
		if n != 0 {
			t.Errorf("%d rows survived 0016 in %s; a derivation whose statistics nobody "+
				"recorded would be reported as having suppressed nothing", n, table)
		}
	}

	// The rebuild has to put back everything that hung off the table, or the
	// guard against a rebuild that inserted without deleting is gone.
	var index string
	if err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='derived_session_span'`).
		Scan(&index); err != nil {
		t.Errorf("derived_session_span is missing after the rebuild: %v", err)
	}
	if err := drRun(d, nil); err != nil {
		t.Fatalf("write a derivation after 0016: %v", err)
	}
	if err := drSitting(d, 1, drStart, drEnd); err != nil {
		t.Fatalf("write a sitting after 0016: %v", err)
	}
	if err := drSitting(d, 1, drStart, drEnd); err == nil {
		t.Error("a second sitting claiming the same span inserted; derived_session_span did not survive")
	}
}

// The columns that moved are GONE from derived_session rather than left behind
// as a second copy nobody updates. Two places holding the gap is how a table
// comes to disagree with itself.
func TestTheParametersNoLongerSitOnEverySittingRow(t *testing.T) {
	d := migrated(t)
	for _, col := range []string{
		"computed_at", "gap_micros", "lead_in_micros", "tail_out_micros",
		"presence_sources", "ignore_sources", "exclude_patterns", "corpus_from", "corpus_to",
	} {
		if _, err := d.Query(`SELECT ` + col + ` FROM derived_session`); err == nil {
			t.Errorf("derived_session still has a %s column; it is now on the run row, "+
				"and two copies of one parameter is how a table comes to disagree with itself", col)
		}
	}
}
