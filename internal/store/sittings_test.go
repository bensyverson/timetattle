package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/store"
)

// Reading sittings back out, with the parameters they were derived under.
//
// A span separated from its parameters means nothing — "3h12m engaged" is a
// different claim at a two-minute gap than at two hours, and the spread across
// that range is 2.3x on the live corpus. The read therefore carries the
// derivation, and it distinguishes three states rather than two: sittings in
// the window, no sittings in the window, and nothing derived at all.

var sitBase = time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

func sitAt(m int) time.Time { return sitBase.Add(time.Duration(m) * time.Minute) }

func sitParams() sittings.Params {
	return sittings.Params{
		Gap: 15 * time.Minute, LeadIn: 5 * time.Minute, TailOut: 5 * time.Minute,
		PresenceSources: []string{"knowledgec-backlit"},
	}
}

// seedSittings writes a derivation through the same path `timetattle
// sessionize` uses, so what these tests read back is what the command stores
// rather than what a test's own INSERT happened to spell.
// The corpus these seed under is the unbounded one — nothing excluded, no
// window — because their subject is the parameters. seedSittingsFrom in
// corpus_test.go is the same thing with the corpus named.
func seedSittings(t *testing.T, f fixture, p sittings.Params, computedAt time.Time, ss ...sittings.Sitting) {
	t.Helper()
	seedSittingsFrom(t, f, p, sittings.Corpus{}, computedAt, ss...)
}

func sitting(from, to int) sittings.Sitting {
	return sittings.Sitting{
		Start: sitAt(from), End: sitAt(to), Events: 2,
		Sources: []string{"biome-appusage"}, Margin: time.Minute,
	}
}

func TestSittingsReadBackWithTheParametersTheyWereDerivedUnder(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	computed := time.Date(2026, 8, 1, 13, 32, 33, 0, time.UTC)
	seedSittings(t, f, sitParams(), computed, sitting(0, 60), sitting(120, 200))

	got, err := f.Sittings(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(got.Sittings) != 2 {
		t.Fatalf("read %d sittings, want 2", len(got.Sittings))
	}
	if !got.ComputedAt.Equal(computed) {
		t.Errorf("ComputedAt is %v, want %v", got.ComputedAt, computed)
	}
	if !got.Params.Equal(sitParams()) {
		t.Errorf("read the parameters as %+v, want %+v", got.Params, sitParams())
	}
	first := got.Sittings[0]
	if !first.Start.Equal(sitAt(0)) || !first.End.Equal(sitAt(60)) {
		t.Errorf("the first sitting is %v..%v, want +0m..+60m", first.Start, first.End)
	}
	if first.Events != 2 || first.Margin != time.Minute || strings.Join(first.Sources, ",") != "biome-appusage" {
		t.Errorf("the first sitting came back as %+v; events, sources and margin are what make it falsifiable", first)
	}
	if got.Total() != 140*time.Minute {
		t.Errorf("Total is %v, want 2h20m", got.Total())
	}
}

// The three states, and the one this project keeps collapsing. A table nobody
// has derived into is not a day with no work in it.
func TestSittingsOnATableNobodyHasDerivedSaysSoRatherThanReportingAnEmptyDay(t *testing.T) {
	f := newStore(t)
	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings on an empty table: %v", err)
	}
	if len(got.Sittings) != 0 {
		t.Errorf("read %d sittings out of an empty table", len(got.Sittings))
	}
	if !got.ComputedAt.IsZero() {
		t.Errorf("an empty table reported a derivation time of %v", got.ComputedAt)
	}
	if got.Params.Gap != 0 {
		t.Errorf("an empty table reported a gap of %v", got.Params.Gap)
	}
}

// And the distinction that only a windowed read can express: a window with no
// sittings in it still has to name the derivation it was read under, or a
// caller cannot tell "you have not sessionized" from "you did not work on
// Sunday".
func TestAWindowWithNoSittingsStillNamesTheDerivation(t *testing.T) {
	f := newStore(t)
	computed := time.Date(2026, 8, 1, 13, 32, 33, 0, time.UTC)
	seedSittings(t, f, sitParams(), computed, sitting(0, 60))

	got, err := f.Sittings(context.Background(), sitAt(600), sitAt(700))
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(got.Sittings) != 0 {
		t.Fatalf("read %d sittings from a window with none in it", len(got.Sittings))
	}
	if got.ComputedAt.IsZero() || !got.Params.Equal(sitParams()) {
		t.Errorf("an empty window reported no derivation (%v, %+v); "+
			"that is the answer an unsessionized database gives, and the two are different facts",
			got.ComputedAt, got.Params)
	}
}

// A mixture cannot come out of Replace, which rebuilds the table wholesale. It
// can come out of a hand-written INSERT or a rebuild interrupted in a way
// SQLite is not meant to allow, and it reads as a perfectly ordinary set of
// sittings — half of them measured against one model and half against another.
func TestSittingsRefusesATableHoldingTwoDerivations(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	seedSittings(t, f, sitParams(), time.Now().UTC(), sitting(0, 60))

	err := f.ReplaceDerived(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO derived_session
			(started_at, ended_at, events, sources, margin_micros, computed_at,
			 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources,
			 exclude_patterns, corpus_from, corpus_to)
			VALUES (?, ?, 1, 'biome-appusage', 0, ?, 7200000000, 300000000, 300000000, 'knowledgec-backlit', '',
			        '[]', '', '')`,
			sitAt(300).Format(store.TimeLayout), sitAt(360).Format(store.TimeLayout),
			sitBase.Format(store.TimeLayout))
		return err
	})
	if err != nil {
		t.Fatalf("write a second derivation's row: %v", err)
	}

	if _, err := f.Sittings(ctx, time.Time{}, time.Time{}); err == nil {
		t.Fatal("Sittings read a table holding two derivations without complaint; " +
			"half these sittings are measured against a different model")
	} else if !strings.Contains(err.Error(), "sessionize") {
		t.Errorf("the complaint does not name the way out: %v", err)
	}
}

// Overlap and not containment, the same rule Adjustments and the event query
// use: a sitting that began before the window and ended inside it describes
// time during the window.
func TestSittingsWindowByOverlapRatherThanContainment(t *testing.T) {
	f := newStore(t)
	seedSittings(t, f, sitParams(), sitBase,
		sitting(0, 60), sitting(90, 150), sitting(300, 360))

	got, err := f.Sittings(context.Background(), sitAt(120), sitAt(130))
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(got.Sittings) != 1 || !got.Sittings[0].Start.Equal(sitAt(90)) {
		t.Errorf("a window inside one sitting read %d sittings, want the one it falls inside", len(got.Sittings))
	}
}

func TestSittingsComeBackInOrder(t *testing.T) {
	f := newStore(t)
	seedSittings(t, f, sitParams(), sitBase, sitting(300, 360), sitting(0, 60), sitting(90, 150))

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	for i := 1; i < len(got.Sittings); i++ {
		if got.Sittings[i].Start.Before(got.Sittings[i-1].Start) {
			t.Fatalf("sitting %d starts before the one before it: %v", i, got.Sittings)
		}
	}
}
