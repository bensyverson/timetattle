package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
)

// The run row: what a derivation was given, and what it DID.
//
// Until migration 0016 the second half was computed on every derivation,
// printed by `timetattle sessionize`, and thrown away. Nothing stored could
// answer "how much did the darkness rule cost on the last derivation" — on
// 2026-08-14 that took two `metrics` runs with different flags. These tests are
// the Go half of the round trip; the constraints themselves are driven with raw
// SQL in internal/migrations.

// sitStats is a set of statistics with every field distinct, so that a field
// read back into the wrong one is visible rather than plausible.
func sitStats() sittings.Stats {
	return sittings.Stats{
		Evidence:            1079551,
		Presence:            2149,
		Ignored:             4088,
		Unbounded:           745,
		PresenceWithoutSpan: 11,
		MergedByExpansion:   17,
		SplitByAbsence:      129,
		TrimmedByAbsence:    48*time.Hour + 36*time.Minute,
		SuppressedByAbsence: 24704,
		UnevidencedPieces:   1,
		UnevidencedDropped:  3 * time.Second,
		Contested:           13*time.Hour + 18*time.Minute,
		ObservedAbsence:     639*time.Hour + 42*time.Minute,
	}
}

// seedDerivationWithStats writes a whole derivation — parameters, corpus,
// statistics — through the same path `timetattle sessionize` uses.
func seedDerivationWithStats(t *testing.T, f fixture, s sittings.Stats,
	computedAt time.Time, ss ...sittings.Sitting) {
	t.Helper()
	res := sessions.Result{Params: sitParams(), Sessions: ss, Stats: s}
	if err := f.ReplaceDerived(context.Background(),
		sessions.Replace(res, sittings.Corpus{}, computedAt)); err != nil {
		t.Fatalf("seed the derivation: %v", err)
	}
}

// The whole point of the migration: the statistics survive the derivation that
// computed them.
func TestTheStatisticsOfADerivationReadBackWithIt(t *testing.T) {
	f := newStore(t)
	computed := time.Date(2026, 8, 14, 13, 32, 33, 0, time.UTC)
	seedDerivationWithStats(t, f, sitStats(), computed, sitting(0, 60), sitting(120, 200))

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if got.Stats != sitStats() {
		t.Errorf("the statistics read back as %+v, want %+v", got.Stats, sitStats())
	}
}

// A windowed read still reports the statistics of the WHOLE derivation, for the
// same reason it reports the parameters of the whole derivation: they describe
// the run, not the window. Reporting a fraction of them for a Tuesday would be
// a number nobody could reconcile with anything.
func TestAWindowedReadStillReportsTheWholeDerivationsStatistics(t *testing.T) {
	f := newStore(t)
	seedDerivationWithStats(t, f, sitStats(), sitBase, sitting(0, 60))

	got, err := f.Sittings(context.Background(), sitAt(600), sitAt(700))
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(got.Sittings) != 0 {
		t.Fatalf("read %d sittings from a window with none in it", len(got.Sittings))
	}
	if got.Stats != sitStats() {
		t.Errorf("a window with no sittings in it reported %+v", got.Stats)
	}
}

// The policy is what a derivation LET observed darkness do to it. It is
// recorded because it is about to change, and a derivation from before the
// change must not read as one from after it.
func TestTheDarknessPolicyIsRecordedWithTheDerivation(t *testing.T) {
	f := newStore(t)
	seedDerivationWithStats(t, f, sitStats(), sitBase, sitting(0, 60))

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if got.DarknessPolicy != sittings.CurrentDarknessPolicy {
		t.Errorf("the derivation recorded the darkness policy as %q, want %q",
			got.DarknessPolicy, sittings.CurrentDarknessPolicy)
	}
	if got.DarknessPolicy == "" {
		t.Error("a derivation was stored with no darkness policy at all")
	}
}

// THE THIRD STATE, which the restated columns could not express.
//
// With the parameters on every session row, a derivation that produced NO
// sittings wrote no rows at all — and read back exactly like a database nobody
// had ever sessionized. That is the collapse this project has shipped three
// times, and it was reachable from the command line: `timetattle sessionize
// -from <after the last event>` runs the model, finds nothing, and the store
// then says nothing has been derived.
//
// A run row exists whether or not it produced anything, so the two are now
// different rows and not the same absence.
func TestADerivationThatFoundNoSittingsIsNotAnUnderivedStore(t *testing.T) {
	f := newStore(t)
	computed := time.Date(2026, 8, 14, 13, 32, 33, 0, time.UTC)
	seedDerivationWithStats(t, f, sitStats(), computed)

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(got.Sittings) != 0 {
		t.Fatalf("read %d sittings out of a derivation that produced none", len(got.Sittings))
	}
	if got.ComputedAt.IsZero() {
		t.Error("a derivation that ran and found no sittings reported no derivation at all; " +
			"that is the answer an unsessionized store gives, and the two are different facts")
	}
	if !got.Params.Equal(sitParams()) {
		t.Errorf("a derivation that found no sittings reported its parameters as %+v", got.Params)
	}
	if got.Stats != sitStats() {
		t.Errorf("a derivation that found no sittings reported %+v; "+
			"it read 1,079,551 observations and the count is the evidence that it looked", got.Stats)
	}
}

// And the state that must still read as itself: a store nobody has derived into
// has no run row, and says so.
func TestAStoreNobodyHasDerivedIntoStillSaysSo(t *testing.T) {
	f := newStore(t)
	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings on an underived store: %v", err)
	}
	if !got.ComputedAt.IsZero() {
		t.Errorf("an underived store claimed a derivation at %v", got.ComputedAt)
	}
	if got.Stats != (sittings.Stats{}) {
		t.Errorf("an underived store reported statistics %+v", got.Stats)
	}
	if got.DarknessPolicy != "" {
		t.Errorf("an underived store named a darkness policy: %q", got.DarknessPolicy)
	}
}

// A rebuild REPLACES the derivation rather than adding one beside it. The
// singleton CHECK makes a second row impossible, so the failure this catches is
// a rebuild that inserted without deleting — which would have doubled every
// hour computed from the table.
func TestRecomputingLeavesExactlyOneDerivation(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	for i := range 3 {
		seedDerivationWithStats(t, f, sitStats(),
			sitBase.Add(time.Duration(i)*time.Hour), sitting(0, 60))
	}
	var runs int
	if err := f.raw(t).QueryRow(`SELECT COUNT(*) FROM derived_session_run`).Scan(&runs); err != nil {
		t.Fatalf("count the derivations: %v", err)
	}
	if runs != 1 {
		t.Errorf("derived_session_run holds %d rows after three rebuilds, want 1", runs)
	}
	got, err := f.Sittings(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if want := sitBase.Add(2 * time.Hour); !got.ComputedAt.Equal(want) {
		t.Errorf("the current derivation is dated %v, want the last one at %v", got.ComputedAt, want)
	}
}

// The durations are stored at the resolution every other duration in this
// database carries. Rounded to the second, a stored statistic would disagree
// with the same statistic recomputed, silently and for ever.
func TestTheDurationStatisticsKeepTheirMicroseconds(t *testing.T) {
	f := newStore(t)
	s := sitStats()
	s.TrimmedByAbsence = 3*time.Second + 1*time.Microsecond
	s.Contested = 7 * time.Microsecond
	s.UnevidencedDropped = 999 * time.Microsecond
	seedDerivationWithStats(t, f, s, sitBase, sitting(0, 60))

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if got.Stats.TrimmedByAbsence != s.TrimmedByAbsence ||
		got.Stats.Contested != s.Contested ||
		got.Stats.UnevidencedDropped != s.UnevidencedDropped {
		t.Errorf("the durations read back as %v, %v, %v; want %v, %v, %v",
			got.Stats.TrimmedByAbsence, got.Stats.Contested, got.Stats.UnevidencedDropped,
			s.TrimmedByAbsence, s.Contested, s.UnevidencedDropped)
	}
}

// A derivation whose statistics contradict each other is refused where it is
// written rather than becoming a row somebody reconciles against months later.
func TestAContradictoryDerivationIsRefusedByTheStore(t *testing.T) {
	f := newStore(t)
	s := sitStats()
	s.Unbounded = s.Evidence + 1
	res := sessions.Result{Params: sitParams(), Sessions: []sittings.Sitting{sitting(0, 60)}, Stats: s}
	err := f.ReplaceDerived(context.Background(),
		sessions.Replace(res, sittings.Corpus{}, sitBase))
	if err == nil {
		t.Error("a derivation claiming more unbounded observations than evidence was stored")
	}
	// And the store is left holding the previous answer, which here is none.
	got, gerr := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if gerr != nil {
		t.Fatalf("Sittings: %v", gerr)
	}
	if !got.ComputedAt.IsZero() {
		t.Error("the refused derivation was left in the store anyway")
	}
}
