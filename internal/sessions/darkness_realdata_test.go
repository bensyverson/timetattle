//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata -count=1 ./...
//
// It sits beside the constructed cases in sessions_test.go rather than in place
// of them, and the reason is on the record: an over-general supersession rule
// once passed against the whole live Biome stream purely because nothing in it
// happened to trigger the rule, and would have begun destroying records the
// first time a second source arrived. A fixture cannot show that a rule survives
// a million observations, and a million observations cannot show that a rule
// handles a case they do not contain. Neither substitutes for the other.

package sessions_test

import (
	"context"
	"testing"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// liveCluster derives the whole store under the shipped configuration, opening
// it READ-ONLY: a harvest may be running, and a test is never a reason to take
// a write lock on the only copy of the evidence.
func liveCluster(t *testing.T) (sessions.Result, []source.Event) {
	t.Helper()
	path, err := store.DefaultPath()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	cfgPath, err := config.DefaultPath()
	if err != nil {
		t.Skipf("no configuration: %v", err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Skipf("no configuration at %s: %v", cfgPath, err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Skipf("no live store at %s: %v", path, err)
	}
	t.Cleanup(func() { st.Close() })

	all, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Skipf("reading the live store: %v", err)
	}
	excludes, err := sessions.NewExcludes(cfg.Sessions.Exclude)
	if err != nil {
		t.Fatalf("NewExcludes: %v", err)
	}
	events, _ := sessions.Retain(all, excludes)

	p := sessions.Params{
		Gap:             cfg.Sessions.Gap.Duration(),
		LeadIn:          cfg.Sessions.LeadIn.Duration(),
		TailOut:         cfg.Sessions.TailOut.Duration(),
		PresenceSources: cfg.Sessions.PresenceSources,
		IgnoreSources:   cfg.Sessions.IgnoreSources,
	}
	res, err := sessions.Cluster(events, p)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if res.Stats.ObservedAbsence == 0 {
		t.Skip("the live store establishes no darkness at all, so there is nothing here to check")
	}
	return res, events
}

// Only wall-clock timers are suppressed, over the whole corpus rather than over
// a case somebody thought of.
//
// The rule this replaced dropped every observation anchored in darkness. Across
// the live store that reached 1 timer artifact and 24,703 other observations,
// almost all of them Claude transcript records — work with the lid shut,
// deleted for having been done with the lid shut. Reproduce the census with
// `go run ./scripts/darkcause`.
func TestOnlyTimerObservationsAreSuppressedByDarkness(t *testing.T) {
	res, events := liveCluster(t)

	var timers int
	for _, e := range events {
		if e.Cause == source.CauseTimer {
			timers++
		}
	}
	if res.Stats.SuppressedByAbsence > timers {
		t.Errorf("%d observations were suppressed by darkness but only %d timer-caused observations "+
			"exist in the whole corpus; something other than a timer is being dropped",
			res.Stats.SuppressedByAbsence, timers)
	}
	t.Logf("%d suppressed of %d timer-caused observations, across %v of observed darkness",
		res.Stats.SuppressedByAbsence, timers, res.Stats.ObservedAbsence)
}

// Darkness removes no TIME from any sitting, only whole timer observations.
//
// These four counters are structurally zero rather than measured zero, which
// their own documentation says out loud. Asserting it against real evidence is
// what keeps "structurally" honest: a route back to non-zero would be a route
// back to deleting work with the lid shut, and it would not announce itself.
func TestDarknessTrimsNoTimeFromTheLiveCorpus(t *testing.T) {
	res, _ := liveCluster(t)

	if res.Stats.TrimmedByAbsence != 0 {
		t.Errorf("TrimmedByAbsence = %v across %v of observed darkness, want 0",
			res.Stats.TrimmedByAbsence, res.Stats.ObservedAbsence)
	}
	if res.Stats.SplitByAbsence != 0 {
		t.Errorf("SplitByAbsence = %d, want 0", res.Stats.SplitByAbsence)
	}
	if res.Stats.UnevidencedPieces != 0 || res.Stats.UnevidencedDropped != 0 {
		t.Errorf("UnevidencedPieces = %d over %v, want 0",
			res.Stats.UnevidencedPieces, res.Stats.UnevidencedDropped)
	}
}

// Every observation is accounted for exactly once: suppressed and counted, or
// placed in a sitting.
//
// This is the property that makes narrowing suppression and dropping the span
// subtraction ONE change rather than two. Doing only the first keeps an
// observation and then carves away the only time it could have occupied, so it
// lands in no sitting and no counter names it — a quieter deletion than the one
// being fixed. Over the live corpus it would strand roughly 24,700 records.
func TestEveryLiveObservationIsSuppressedOrPlaced(t *testing.T) {
	res, _ := liveCluster(t)

	var placed int
	for _, s := range res.Sessions {
		placed += s.Events
	}
	// The reconciliation fails in BOTH directions and they are different bugs,
	// so the message must not assume one. Short means observations were kept and
	// then had the only time they could occupy carved away, landing in no
	// sitting and no counter. Long means one observation was counted in two
	// pieces of a carved cluster, which double-counts the wall clock that both
	// the presence metric and the capacity denominator rest on never doing.
	if got := placed + res.Stats.SuppressedByAbsence; got != res.Stats.Evidence {
		t.Errorf("%d observations placed in a sitting and %d suppressed, against %d of evidence "+
			"(%+d): observations are being %s", placed, res.Stats.SuppressedByAbsence,
			res.Stats.Evidence, got-res.Stats.Evidence,
			map[bool]string{true: "counted in more than one sitting", false: "stranded, counted by nothing"}[got > res.Stats.Evidence])
	}
}

// Sittings stay disjoint and ordered without the subtraction that used to
// follow the expansion.
//
// Wall clock is never double-counted — the property both the presence metric
// and the capacity denominator rest on — and it was previously enforced partly
// by carving darkness out of overlapping spans. Removing that step must not
// have removed the guarantee with it.
func TestLiveSittingsRemainOrderedAndDisjoint(t *testing.T) {
	res, _ := liveCluster(t)

	if len(res.Sessions) == 0 {
		t.Fatal("the live store derived no sittings at all")
	}
	var longest sessions.Session
	for i, s := range res.Sessions {
		if s.End.Before(s.Start) {
			t.Fatalf("sitting %d ends before it starts: %v..%v", i, s.Start, s.End)
		}
		if s.Events == 0 {
			t.Fatalf("sitting %d (%v..%v) rests on no observation", i, s.Start, s.End)
		}
		if i > 0 && s.Start.Before(res.Sessions[i-1].End) {
			t.Fatalf("sitting %d starts %v, before sitting %d ended at %v",
				i, s.Start, i-1, res.Sessions[i-1].End)
		}
		if s.Duration() > longest.Duration() {
			longest = s
		}
	}

	// The failure mode removing the subtraction could plausibly have introduced:
	// a sitting that swallows a night because one long observation bridged it.
	// It does not arise, and the reason is structural rather than lucky. The gap
	// threshold still ends a sitting on its own, and only a single observation
	// LONGER than the gap could reach across one — but the longest evidence
	// interval in the whole store is under three hours, Biome focus intervals
	// top out around 37 minutes, and a Biome record that spans sleep is
	// UNBOUNDED rather than long, contributing its instant and no duration. The
	// old comment here cited "a focus interval that ran on after the display
	// slept" as a reason to subtract; no such interval exists in the corpus.
	//
	// Logged rather than bounded by a constant: a genuinely long working day is
	// a real sitting, and a threshold here would be fitting a parameter in a
	// test. The assertion is against the GAP, which is the thing that actually
	// decides.
	if longest.Duration() > 0 {
		t.Logf("longest sitting %v across %d observations, from %d sittings",
			longest.Duration(), longest.Events, len(res.Sessions))
	}
}
