//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package store_test

import (
	"context"
	"database/sql"
	"os"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// The portable twin is TestSittingsAgreesWithTheSyntheticTableItReads in
// synthetic_test.go, and it is a genuine second reader — SQL through a second
// handle over a table internal/storefixture wrote. It is NOT the same fact. A
// fixture's table is written through the same code path this test would be
// checking; only the live one reconciles the reader against the rows
// `timetattle sessionize` actually produced on a real machine. Both exist.

// The live table, read twice and reconciled.
//
// Constructed rows sit BESIDE a real-data check; neither substitutes for the
// other. Every figure above comes from three sittings this file wrote itself,
// and a reader that agreed with its own fixture while disagreeing with the
// table sessionize actually produces would pass all of them.
//
// Nothing here asserts a count. The live corpus grows, and a test pinning 1,179
// sittings would be false by Tuesday. What it asserts is that two ways of
// measuring one table agree: the method, and SQL over the same file through a
// second read-only handle — which is how a bug in a reader is kept from hiding
// behind the reader that shares its code.
func TestSittingsAgreesWithTheLiveTableItReads(t *testing.T) {
	path, err := store.DefaultPath()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("cannot stat %s: %v", path, err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Skipf("cannot read %s: %v", path, err)
	}
	defer st.Close()

	// A read-only handle applies no migration, so a live store that has not
	// been opened for writing since 0010 landed does not carry the corpus
	// columns at all. That is a fact about the FILE and not about this reader:
	// the first command that opens it read-write migrates it, and the
	// derivation is rebuilt by the next `timetattle sessionize`. Saying which
	// of the two it is beats a failure naming a missing column.
	schema, err := st.Schema(context.Background())
	if err != nil {
		t.Skipf("%s has never been migrated: %v", path, err)
	}
	if schema.Version < 10 {
		t.Skipf("the live store is at migration %d and the corpus columns arrived in 0010; "+
			"run any timetattle command that opens it for writing", schema.Version)
	}

	got, err := st.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings over the live store: %v", err)
	}

	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s read-only: %v", path, err)
	}
	defer db.Close()

	var (
		rows       int
		hours      sql.NullFloat64
		derivation int
	)
	if err := db.QueryRow(`SELECT COUNT(*), SUM((julianday(ended_at)-julianday(started_at))*24)
		FROM derived_session`).Scan(&rows, &hours); err != nil {
		t.Fatalf("count derived_session: %v", err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM (SELECT DISTINCT computed_at, gap_micros,
		lead_in_micros, tail_out_micros, presence_sources, ignore_sources FROM derived_session)`).
		Scan(&derivation); err != nil {
		t.Fatalf("count the derivations: %v", err)
	}

	if rows == 0 {
		// Nothing derived. The method has to say exactly that rather than
		// report a day with no work in it, which is the whole point of
		// ComputedAt, and there is nothing left to reconcile.
		if !got.ComputedAt.IsZero() || len(got.Sittings) != 0 {
			t.Errorf("derived_session is empty but Sittings reported %d sittings derived at %v",
				len(got.Sittings), got.ComputedAt)
		}
		t.Skip("the live store holds no derivation to reconcile against")
	}
	if derivation != 1 {
		t.Skipf("the live derived_session holds %d derivations; Sittings refuses it, "+
			"and there is nothing to reconcile", derivation)
	}

	if len(got.Sittings) != rows {
		t.Errorf("Sittings read %d sittings; SQL counts %d rows in the same table",
			len(got.Sittings), rows)
	}
	// The same sum taken two ways. julianday resolves to about a millisecond
	// over these dates, so a real disagreement would be far larger than this.
	if diff := got.Total().Hours() - hours.Float64; diff > 0.001 || diff < -0.001 {
		t.Errorf("Sittings totals %.4fh and SQL over the same rows totals %.4fh, a difference of %.4fh",
			got.Total().Hours(), hours.Float64, diff)
	}
	if got.ComputedAt.IsZero() || got.Params.Gap <= 0 {
		t.Errorf("a table holding %d sittings reported no derivation: computed at %v, gap %v",
			rows, got.ComputedAt, got.Params.Gap)
	}

	// A window is a subset of the same answer, under the same derivation.
	first := got.Sittings[0]
	windowed, err := st.Sittings(context.Background(), first.Start, first.End)
	if err != nil {
		t.Fatalf("Sittings over one sitting's own span: %v", err)
	}
	if len(windowed.Sittings) == 0 {
		t.Error("a window covering the first sitting exactly read none of them")
	}
	if len(windowed.Sittings) > len(got.Sittings) {
		t.Errorf("a window read %d sittings, more than the %d in the whole table",
			len(windowed.Sittings), len(got.Sittings))
	}
	if !windowed.ComputedAt.Equal(got.ComputedAt) || !windowed.Params.Equal(got.Params) {
		t.Error("a windowed read reported a different derivation from an unbounded one")
	}
}
