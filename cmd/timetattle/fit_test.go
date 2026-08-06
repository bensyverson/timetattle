package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/store"
)

// fitSeed builds a store holding one reviewed day: three point observations,
// and a wholesale confirm that tiles the whole day around the two blocks a 15m
// gap with 5m margins derives from them.
//
// It is deliberately the shape the live corpus has — the reviewer confirming a
// derivation he was shown — because the failure this command exists to prevent
// is a fitter reporting the shown parameters back as a discovery.
func fitSeed(t *testing.T) string {
	t.Helper()
	path := seedStore(t)

	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open the seeded store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	day := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	recorded := day.Add(30 * time.Hour)
	review, err := st.RecordReview(ctx, adjustments.Review{
		Start: day, End: day.Add(24 * time.Hour), Verdict: adjustments.Recalled,
		Origin: "test", RecordedAt: recorded,
	})
	if err != nil {
		t.Fatalf("record the day review: %v", err)
	}

	// The tiling: engaged over each derived block, not-engaged over every gap
	// between them, which is what a wholesale confirm writes.
	for _, a := range []struct {
		from, to time.Duration
		engaged  bool
	}{
		{0, 8*time.Hour + 55*time.Minute, false},
		{8*time.Hour + 55*time.Minute, 9*time.Hour + 7*time.Minute, true},
		{9*time.Hour + 7*time.Minute, 11*time.Hour + 55*time.Minute, false},
		{11*time.Hour + 55*time.Minute, 12*time.Hour + 5*time.Minute, true},
		{12*time.Hour + 5*time.Minute, 24 * time.Hour, false},
	} {
		assertion := adjustments.NotEngaged
		if a.engaged {
			assertion = adjustments.Engaged
		}
		if _, err := st.RecordAdjustment(ctx, adjustments.Adjustment{
			ReviewID: review, Start: day.Add(a.from), End: day.Add(a.to),
			Assertion: assertion, Origin: "test", RecordedAt: recorded,
		}); err != nil {
			t.Fatalf("record an assertion: %v", err)
		}
	}
	return path
}

// fitConfig pins the calendar to UTC so the reviewed day is the same day in
// every timezone a test might run in, and names the parameters the reviewer was
// shown.
func fitConfig(t *testing.T) string {
	t.Helper()
	return writeConfig(t, `{"sessions":{"gap":"15m","lead_in":"5m","tail_out":"5m"},
		"calendar":{"timezone":"UTC","day_start":"00:00","week_start":"monday"}}`)
}

func fitRun(t *testing.T, args ...string) string {
	t.Helper()
	var out strings.Builder
	if err := fitCommandTo(&out, args); err != nil {
		t.Fatalf("fit %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

// THE POINT OF THE WHOLE COMMAND. Every parameter set from an 11m gap upward
// derives the same two blocks on this corpus, so every one of them scores zero,
// and a report that named one of them would have invented a distinction.
func TestFitReportsAPlateauRatherThanAPointEstimate(t *testing.T) {
	out := fitRun(t, "-store", fitSeed(t), "-config", fitConfig(t),
		"-gaps", "11m,15m,20m,45m", "-lead-ins", "5m", "-tail-outs", "5m")

	for _, want := range []string{
		"the minimum is a plateau",
		"gap: 11m..45m",
		"IDENTICAL",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not say %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "the fitted gap is") {
		t.Errorf("the report named a single fitted gap from a plateau:\n%s", out)
	}
}

// A search that quietly dropped a third of its grid reads exactly like one that
// covered it.
func TestFitSaysWhichPointsItSkippedForInertness(t *testing.T) {
	out := fitRun(t, "-store", fitSeed(t), "-config", fitConfig(t),
		"-gaps", "2m,5m,10m,15m,20m", "-lead-ins", "5m", "-tail-outs", "5m")

	if !strings.Contains(out, "3 of 5 points were not evaluated") {
		t.Errorf("the report does not say how many points were skipped:\n%s", out)
	}
	if !strings.Contains(out, "inert") {
		t.Errorf("the report does not say why they were skipped:\n%s", out)
	}
}

// The circularity guard, which is the one thing this command must not let a
// reader miss.
func TestFitWarnsWhenTheShownParametersAreAmongTheBest(t *testing.T) {
	out := fitRun(t, "-store", fitSeed(t), "-config", fitConfig(t),
		"-gaps", "11m,15m,20m", "-lead-ins", "5m", "-tail-outs", "5m")

	if !strings.Contains(out, "CIRCULAR") {
		t.Errorf("the report does not flag that the shown parameters won:\n%s", out)
	}
	if !strings.Contains(out, "gap 15m, lead-in 5m, tail-out 5m") {
		t.Errorf("the report does not name the reference parameters:\n%s", out)
	}
}

// A residual is a duration with no meaning until something says what scale it
// sits on.
func TestFitReportsTheNullsBesideTheResidual(t *testing.T) {
	out := fitRun(t, "-store", fitSeed(t), "-config", fitConfig(t),
		"-gaps", "11m,15m,20m", "-lead-ins", "5m", "-tail-outs", "5m")

	for _, want := range []string{"claiming everything", "claiming nothing", "the objective moved"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not carry %q:\n%s", want, out)
		}
	}
}

// Two evidence regimes, and pooling them is what hides that the days where the
// parameters decide something and the days that carry labels are almost
// disjoint sets.
func TestFitReportsPerDayCoverageAndRegime(t *testing.T) {
	out := fitRun(t, "-store", fitSeed(t), "-config", fitConfig(t),
		"-gaps", "11m,15m,20m", "-lead-ins", "5m", "-tail-outs", "5m")

	if !strings.Contains(out, "2026-07-11") {
		t.Errorf("the report does not name the reviewed day:\n%s", out)
	}
	if !strings.Contains(out, "point-only") {
		t.Errorf("the report does not name the regime a day of point observations sits in:\n%s", out)
	}
	if !strings.Contains(out, "coverage") {
		t.Errorf("the report does not say how much of the asserted work had an observed duration:\n%s", out)
	}
}

func TestFitDropsTheDaysItIsToldToAndSaysWhich(t *testing.T) {
	err := fitCommandTo(&strings.Builder{}, []string{
		"-store", fitSeed(t), "-config", fitConfig(t), "-skip-days", "2026-07-11",
		"-gaps", "11m,15m", "-lead-ins", "5m", "-tail-outs", "5m"})
	if err == nil {
		t.Fatal("fit accepted a run with every reviewed day skipped")
	}

	// A skip that matched nothing must still be reported. A run told to leave a
	// day out and silently leaving nothing out is a different corpus from the
	// one the reader thinks was searched.
	out := fitRun(t, "-store", fitSeed(t), "-config", fitConfig(t), "-skip-days", "2026-08-01",
		"-gaps", "11m,15m", "-lead-ins", "5m", "-tail-outs", "5m")
	if !strings.Contains(out, "2026-08-01") {
		t.Errorf("the report does not name the day it was told to skip:\n%s", out)
	}
	if !strings.Contains(out, "matched no reviewed day") {
		t.Errorf("the report does not say the skip matched nothing:\n%s", out)
	}
}

// A fit reads. It must not leave a derivation, a correction or a run behind.
func TestFitWritesNothing(t *testing.T) {
	path := fitSeed(t)
	before := query[int](t, path, "SELECT count(*) FROM adjustment")
	fitRun(t, "-store", path, "-config", fitConfig(t),
		"-gaps", "11m,15m", "-lead-ins", "5m", "-tail-outs", "5m")

	if got := query[int](t, path, "SELECT count(*) FROM derived_session"); got != 0 {
		t.Errorf("fit wrote %d session rows; it derives in memory and stores nothing", got)
	}
	if got := query[int](t, path, "SELECT count(*) FROM adjustment"); got != before {
		t.Errorf("the adjustment table moved from %d rows to %d", before, got)
	}
	if got := query[int](t, path, "SELECT count(*) FROM harvest_run"); got != 1 {
		t.Errorf("harvest_run holds %d rows, want the one the seed wrote", got)
	}
}

func TestFitRefusesAStoreWithNothingToFitAgainst(t *testing.T) {
	err := fitCommandTo(&strings.Builder{}, []string{
		"-store", seedStore(t), "-config", fitConfig(t),
		"-gaps", "11m,15m", "-lead-ins", "5m", "-tail-outs", "5m"})
	if err == nil {
		t.Fatal("fit accepted a store holding no reviews at all")
	}
	if !strings.Contains(err.Error(), "review") {
		t.Errorf("the complaint does not say what is missing: %v", err)
	}
}

// Narrowing is an optimisation, and an optimisation that changed the answer
// would be a silent corruption of it. The command can be run either way and the
// two must agree.
func TestFitNarrowsWithoutChangingTheAnswer(t *testing.T) {
	path, cfg := fitSeed(t), fitConfig(t)
	args := []string{"-store", path, "-config", cfg,
		"-gaps", "11m,15m,20m", "-lead-ins", "0,5m", "-tail-outs", "0,5m"}

	narrowed := fitRun(t, append(slicesClone(args), "-narrow")...)
	whole := fitRun(t, append(slicesClone(args), "-narrow=false")...)

	if got, want := fitResidualLine(t, narrowed), fitResidualLine(t, whole); got != want {
		t.Errorf("narrowed reports %q, the whole corpus reports %q", got, want)
	}
	if !strings.Contains(narrowed, "narrowed") {
		t.Errorf("the narrowed run does not say it narrowed anything:\n%s", narrowed)
	}
}

func slicesClone(s []string) []string { return append([]string(nil), s...) }

// fitResidualLine picks the line the two runs have to agree on, so a difference
// anywhere in the answer fails rather than a difference anywhere in the prose.
func fitResidualLine(t *testing.T, out string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "residual ") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no residual line in:\n%s", out)
	return ""
}
