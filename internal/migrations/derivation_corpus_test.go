package migrations_test

import (
	"database/sql"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// 0010 records the CORPUS a derivation was drawn from beside the parameters it
// was drawn with. The tests here drive the schema with raw SQL, because the
// question is what the database refuses rather than what Go happens to send it.

// dcRow writes one derivation with the corpus columns spelled by the caller and
// everything else at something the schema accepts.
//
// The corpus columns were on derived_session when 0010 added them and moved to
// derived_session_run in 0016, which put the whole derivation on one row rather
// than restating it on every sitting. The columns, their meanings and every
// guard on them are unchanged, so the tests below are unchanged too and only
// the table they are written into moved. dcClear goes with it.
func dcRow(d *sql.DB, exclude, from, to any) error {
	return drRun(d, map[string]any{
		"exclude_patterns": exclude,
		"corpus_from":      from,
		"corpus_to":        to,
	})
}

// dcClear empties the derivation so the next case can write its own. It clears
// the run row rather than the sittings: 0016 made the derivation the singleton,
// so a second dcRow against a table that already holds one is refused by
// `CHECK (id = 1)` rather than by the guard a test is asking about.
func dcClear(t *testing.T, d *sql.DB) {
	t.Helper()
	if _, err := d.Exec(`DELETE FROM derived_session_run`); err != nil {
		t.Fatalf("clear: %v", err)
	}
}

// The column exists and carries the list. Without it, a derivation built under
// Ben's four patterns is byte-identical to one built under none, and the two
// differ by 126.2h of presence over the live corpus.
func TestADerivationRecordsTheExcludeListItWasDrawnUnder(t *testing.T) {
	d := migrated(t)
	if err := dcRow(d, `["*.bz2","*.gz","*.log","/Users/you/git/Easley/reports/"]`, "", ""); err != nil {
		t.Fatalf("write a derivation with an exclude list: %v", err)
	}
	var stored string
	if err := d.QueryRow(`SELECT exclude_patterns FROM derived_session_run`).Scan(&stored); err != nil {
		t.Fatalf("read exclude_patterns back: %v", err)
	}
	if stored != `["*.bz2","*.gz","*.log","/Users/you/git/Easley/reports/"]` {
		t.Errorf("exclude_patterns read back as %q", stored)
	}
}

// "This derivation excluded nothing" and "nobody recorded what this derivation
// excluded" are different facts, and the column is NOT NULL so that the second
// one cannot be spelled at all. A derivation predating the column is not
// carried as a NULL: 0010 discards it, and the absence of rows is what says so.
func TestADerivationCannotBeWrittenWithoutSayingWhatItExcluded(t *testing.T) {
	d := migrated(t)
	if err := dcRow(d, nil, "", ""); err == nil {
		t.Error("a derivation inserted with a NULL exclude list; " +
			"\"excluded nothing\" would then be indistinguishable from \"nobody recorded it\"")
	}
	if err := dcRow(d, "[]", "", ""); err != nil {
		t.Errorf("a derivation that excluded nothing was refused: %v", err)
	}
	dcClear(t, d)
	// And the same fact from the other side: a derivation that names no corpus
	// at all is refused, rather than defaulting to one.
	_, err := d.Exec(`INSERT INTO derived_session_run
		(id, computed_at, gap_micros, lead_in_micros, tail_out_micros,
		 presence_sources, ignore_sources, darkness_policy,
		 evidence_observations, presence_observations, ignored_observations,
		 unbounded_observations, presence_without_span, merged_by_expansion,
		 split_by_absence, trimmed_by_absence_micros, suppressed_by_absence,
		 unevidenced_pieces, unevidenced_dropped_micros,
		 contested_micros, observed_absence_micros)
		VALUES (1, '2026-08-01T13:04:35.000000Z', 900000000, 0, 0, '', '', 'suppress-all-and-trim',
		        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`)
	if err == nil {
		t.Error("a derivation inserted without the corpus columns at all")
	}
}

// A value nobody can read back is not a record of what was excluded. The stored
// form is a JSON array; anything else is refused where it is written rather
// than becoming an unparseable row somebody meets months later.
func TestTheStoredExcludeListMustBeAJSONArray(t *testing.T) {
	d := migrated(t)
	for _, bad := range []string{"", "null", "*.log", `{"a":1}`, `["unterminated`} {
		if err := dcRow(d, bad, "", ""); err == nil {
			t.Errorf("exclude_patterns accepted %q", bad)
		}
	}
	for _, good := range []string{"[]", `["*.log"]`, `["/Users/you/Documents/Smith, John/"]`} {
		if err := dcRow(d, good, "", ""); err != nil {
			t.Errorf("exclude_patterns refused %q: %v", good, err)
		}
		dcClear(t, d)
	}
}

// From and To narrow the corpus exactly as the exclude list does. Empty is
// unbounded — a value, not an absence — and anything else has to be an instant
// in the one layout this database's TEXT ordering rests on.
func TestTheCorpusWindowIsEitherUnboundedOrAStoreTimestamp(t *testing.T) {
	d := migrated(t)
	const good = "2026-07-11T09:00:00.000000Z"
	const later = "2026-07-12T09:00:00.000000Z"

	for _, c := range []struct {
		name     string
		from, to any
	}{
		{"unbounded both ways", "", ""},
		{"an opening bound only", good, ""},
		{"a closing bound only", "", later},
		{"both bounds", good, later},
		{"an instant of a window", good, good},
	} {
		if err := dcRow(d, "[]", c.from, c.to); err != nil {
			t.Errorf("the corpus window refused %s: %v", c.name, err)
		}
		dcClear(t, d)
	}

	for _, c := range []struct {
		name     string
		from, to any
	}{
		{"a NULL opening bound", nil, ""},
		{"a NULL closing bound", "", nil},
		{"a date with no time", "2026-07-11", ""},
		{"an RFC3339 instant", "2026-07-11T09:00:00Z", ""},
		// The same width as the layout, and five hours out of UTC: the trap
		// 0009 measured, which a length check reports as success.
		{"a same-width local instant", "2026-08-01T08:04:35.0-05:00", ""},
		{"a window that ends before it begins", later, good},
	} {
		if err := dcRow(d, "[]", c.from, c.to); err == nil {
			t.Errorf("the corpus window accepted %s", c.name)
			dcClear(t, d)
		}
	}
}

// What happens to a derivation that predates the column, said out loud.
//
// It is DISCARDED rather than carried forward under an invented corpus. There
// is no honest value to write: the rows were computed under an exclude list
// nobody recorded, and defaulting them to "excluded nothing" would state as a
// fact the very thing this migration exists to stop being guessed. An empty
// table is the third state the whole design already has a name for — nothing
// has been derived — and `timetattle sessionize` rebuilds it in seconds.
func TestMigratingDiscardsADerivationWhoseCorpusNobodyRecorded(t *testing.T) {
	d := tsAt(t, 9)
	if err := tsSessionBefore0010(d, "2026-07-11T09:00:00.000000Z", "2026-07-11T10:00:00.000000Z", tsGood); err != nil {
		t.Fatalf("write a derivation at 0009: %v", err)
	}
	var before int
	if err := d.QueryRow(`SELECT COUNT(*) FROM derived_session`).Scan(&before); err != nil {
		t.Fatalf("count at 0009: %v", err)
	}
	if before != 1 {
		t.Fatalf("seeded %d rows at 0009, want 1", before)
	}

	if err := migrations.Apply(d); err != nil {
		t.Fatalf("apply 0010: %v", err)
	}

	var after int
	if err := d.QueryRow(`SELECT COUNT(*) FROM derived_session`).Scan(&after); err != nil {
		t.Fatalf("count after 0010: %v", err)
	}
	if after != 0 {
		t.Errorf("%d rows survived 0010; a derivation whose corpus nobody recorded would be "+
			"reported as having excluded nothing", after)
	}

	// The rebuild has to put back everything that hung off the table, or the
	// guard against a rebuild that inserted without deleting is gone.
	var index string
	if err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type='index' AND name='derived_session_span'`).
		Scan(&index); err != nil {
		t.Errorf("derived_session_span is missing after the rebuild: %v", err)
	}
	if err := dcRow(d, "[]", "", ""); err != nil {
		t.Fatalf("write a derivation after 0010: %v", err)
	}
	// The span index is on the sittings, which is where a duplicate now has to
	// be attempted. Two dcRows would test `CHECK (id = 1)` instead — 0016 made
	// the derivation a singleton, and a second one is refused before the index
	// is ever consulted.
	if err := drSitting(d, 1, drStart, drEnd); err != nil {
		t.Fatalf("write a sitting after 0010: %v", err)
	}
	if err := drSitting(d, 1, drStart, drEnd); err == nil {
		t.Error("a second session claiming the same span inserted; derived_session_span did not survive")
	}
}
