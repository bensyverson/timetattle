package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/storefixture"

	_ "modernc.org/sqlite"
)

// The reconciliations of sittings_test.go and realdata_test.go, over a store
// this test built rather than over one particular machine's.
//
// TestSittingsAgreesWithTheLiveTableItReads is the same shape as the first case
// here and stays exactly as it is: it reads the table `timetattle sessionize`
// actually wrote on this machine, which is a different fact from reading one a
// test wrote through the same writer, and neither substitutes for the other.
// What it CANNOT do is run anywhere. It skipped for the whole of 2026-08-02
// because migration 0011 landed against a live store still at 0010, and while
// it skipped nothing at all was reconciling the reader against SQL.

// The method and SQL over the same file, through a second handle. A bug in a
// reader is otherwise free to hide behind the reader that shares its code.
func TestSittingsAgreesWithTheSyntheticTableItReads(t *testing.T) {
	f := storefixture.New(t)

	got, err := f.Store.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(got.Sittings) == 0 {
		t.Fatal("the fixture derived nothing; there is nothing to reconcile")
	}

	db, err := sql.Open("sqlite", "file:"+f.Path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s read-only: %v", f.Path, err)
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

	if derivation != 1 {
		t.Fatalf("derived_session holds %d derivations; the fixture wrote one", derivation)
	}
	if len(got.Sittings) != rows {
		t.Errorf("Sittings read %d sittings; SQL counts %d rows in the same table",
			len(got.Sittings), rows)
	}
	// julianday resolves to about a millisecond over these dates, so a real
	// disagreement would be far larger than this.
	if diff := got.Total().Hours() - hours.Float64; diff > 0.001 || diff < -0.001 {
		t.Errorf("Sittings totals %.4fh and SQL over the same rows totals %.4fh, a difference of %.4fh",
			got.Total().Hours(), hours.Float64, diff)
	}
	if got.ComputedAt.IsZero() || got.Params.Gap <= 0 {
		t.Errorf("a table holding %d sittings reported no derivation: computed at %v, gap %v",
			rows, got.ComputedAt, got.Params.Gap)
	}
}

// A window is a subset of the same answer, under the same derivation.
func TestAWindowedSyntheticReadIsASubsetOfTheWholeTable(t *testing.T) {
	f := storefixture.New(t)
	ctx := context.Background()
	all, err := f.Store.Sittings(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(all.Sittings) == 0 {
		t.Fatal("nothing derived")
	}
	first := all.Sittings[0]
	windowed, err := f.Store.Sittings(ctx, first.Start, first.End)
	if err != nil {
		t.Fatalf("Sittings over one sitting's own span: %v", err)
	}
	if len(windowed.Sittings) == 0 {
		t.Error("a window covering the first sitting exactly read none of them")
	}
	if len(windowed.Sittings) > len(all.Sittings) {
		t.Errorf("a window read %d sittings, more than the %d in the whole table",
			len(windowed.Sittings), len(all.Sittings))
	}
	if !windowed.ComputedAt.Equal(all.ComputedAt) || !windowed.Params.Equal(all.Params) {
		t.Error("a windowed read reported a different derivation from an unbounded one")
	}
	if !windowed.Corpus.Equal(all.Corpus) {
		t.Error("a windowed read reported a different corpus from an unbounded one")
	}
}

// A second harvest of an unchanged window adds NOTHING. Dedup is by content
// because collectors re-read, and the property that makes a re-read free is
// that the row count does not move.
func TestReharvestingTheSyntheticCorpusAddsNothing(t *testing.T) {
	f := storefixture.New(t)
	ctx := context.Background()

	before, err := f.Store.EventsIncludingSuperseded(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("EventsIncludingSuperseded: %v", err)
	}

	run, err := f.Store.BeginRun(ctx, store.RunSpec{SourceID: "biome-appusage", Input: "/synthetic/again"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	var again []source.Event
	for _, e := range storefixture.Events() {
		if e.Source == "biome-appusage" {
			again = append(again, e)
		}
	}
	n, err := run.Write(ctx, again)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if n != 0 {
		t.Errorf("re-reading %d unchanged observations stored %d of them again", len(again), n)
	}

	after, err := f.Store.EventsIncludingSuperseded(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("EventsIncludingSuperseded: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("the table went from %d rows to %d across a re-read that saw nothing new",
			len(before), len(after))
	}
}

// Attribution is re-runnable and idempotent. project is a recomputable cache
// and never a stored fact, which is what makes registering a directory after
// the events exist a repair rather than a re-harvest.
func TestReattributingTheSyntheticCorpusChangesNothing(t *testing.T) {
	f := storefixture.New(t)
	ctx := context.Background()

	att, err := f.Store.ResolveProjects(ctx, storefixture.Resolve)
	if err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	if att.Updated != 0 {
		t.Errorf("a second attribution pass over an unchanged store moved %d events", att.Updated)
	}
	if att.Skipped != 0 {
		t.Errorf("%d locators were skipped by a resolver that cannot fail", att.Skipped)
	}
	if att.Locators == 0 {
		t.Error("the pass saw no locators at all, so it proved nothing")
	}
}
