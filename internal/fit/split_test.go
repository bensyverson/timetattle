package fit_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/source"
)

// splitCorpus is two days of two marks twenty minutes apart, labelled twice
// over: a heavy CONFIRMING population that agrees with the one-sitting answer,
// and a light BLIND population that says the twenty minutes in the middle were
// not work.
//
// The two disagree about the gap on purpose, and they disagree by construction
// rather than by accident: the confirming labels are a restatement of the 25m
// derivation, which is exactly what `timetattle review` produces, and the blind
// ones are a statement about where the observations are, which is exactly what
// `review -blind` produces.
func splitCorpus() ([]source.Event, fit.Truth) {
	const day = 24 * time.Hour
	events := []source.Event{mark(0), mark(20 * min1), mark(day), mark(day + 20*min1)}

	truth := fit.Truth{Labels: []fit.Label{
		// Two days of confirming the merged answer.
		label("2026-03-02", -min1, 21*min1, true),
		label("2026-03-02", 21*min1, 121*min1, false),
		label("2026-03-03", day-min1, day+21*min1, true),
		label("2026-03-03", day+21*min1, day+121*min1, false),
		// One day of blind labels over the first day's observations.
		blind("2026-03-02", -min1, min1, true),
		blind("2026-03-02", min1, 19*min1, false),
		blind("2026-03-02", 19*min1, 21*min1, true),
	}}
	return events, truth
}

// THE DEFECT, MEASURED RATHER THAN ASSERTED.
//
// The confirming labels are minimised at a 25m gap and the blind ones at 5m.
// Those are different answers to the same question, and a pooled search returns
// the confirming one because the confirming population is larger. A Result that
// carried only the pooled minimum could not show that, and a reader would take
// "gap 25m" for a fitted value when it is a headcount.
func TestSearchReportsEachPopulationsOwnMinimum(t *testing.T) {
	events, truth := splitCorpus()
	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(5, 25), LeadIns: mins(1), TailOuts: mins(1)},
		Objective: truth,
	}, events)

	if got, want := len(res.Splits), 2; got != want {
		t.Fatalf("the result holds %d populations, want %d: %+v", got, want, res.Splits)
	}

	confirming, ok := res.Split(string(adjustments.Derivation))
	if !ok {
		t.Fatalf("no split for the confirming population: %+v", res.Splits)
	}
	observed, ok := res.Split(string(adjustments.Observations))
	if !ok {
		t.Fatalf("no split for the blind population: %+v", res.Splits)
	}

	for _, s := range res.Splits {
		t.Logf("%-24s best %v at %v, labelled %v, moved %v",
			fit.Describe(adjustments.Shown(s.Shown)), s.Min.Total(), s.Best, s.Min.Scale(),
			s.Max.Total()-s.Min.Total())
	}
	t.Logf("pooled                   best %v at %v", res.Min.Total(), res.Best)

	if got, want := confirming.Best, []fit.Point{{Gap: 25 * min1, LeadIn: min1, TailOut: min1}}; !slices.Equal(got, want) {
		t.Errorf("the confirming population is minimised at %v, want %v", got, want)
	}
	if got, want := observed.Best, []fit.Point{{Gap: 5 * min1, LeadIn: min1, TailOut: min1}}; !slices.Equal(got, want) {
		t.Errorf("the blind population is minimised at %v, want %v", got, want)
	}
	if got, want := confirming.Min.Total(), time.Duration(0); got != want {
		t.Errorf("the confirming minimum is %v, want %v", got, want)
	}
	if got, want := observed.Min.Total(), time.Duration(0); got != want {
		t.Errorf("the blind minimum is %v, want %v", got, want)
	}

	// The pooled answer follows the larger population, which is the whole point.
	if got, want := res.Best, []fit.Point{{Gap: 25 * min1, LeadIn: min1, TailOut: min1}}; !slices.Equal(got, want) {
		t.Errorf("the pooled search settled on %v, want %v", got, want)
	}
	if got := res.CommonBest(); len(got) != 0 {
		t.Errorf("CommonBest() = %v, want none: no point is best for both populations, "+
			"so the pooled minimum is a weighting and not an agreement", got)
	}
	if !res.Pooled() {
		t.Error("Pooled() is false for a search run over two populations")
	}
}

// A cell has to carry the split, not just the total. Recomputing it afterwards
// would mean re-deriving the sittings at every point, which this package
// deliberately does not keep.
func TestEveryCellCarriesItsResidualSplitByPopulation(t *testing.T) {
	events, truth := splitCorpus()
	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(5, 25), LeadIns: mins(1), TailOuts: mins(1)},
		Objective: truth,
	}, events)

	for _, c := range res.Cells {
		if len(c.ByShown) != 2 {
			t.Fatalf("%v carries %d populations, want 2: %+v", c.Point, len(c.ByShown), c.ByShown)
		}
		var sum fit.Residual
		for _, r := range c.ByShown {
			sum = sum.Add(r)
		}
		if sum != c.Residual {
			t.Errorf("%v: the split sums to %+v against a pooled %+v", c.Point, sum, c.Residual)
		}
	}
}

// One population is not a pool, and a search over it must not describe itself
// as one. CommonBest is then simply the answer.
func TestASinglePopulationIsNotReportedAsPooled(t *testing.T) {
	events := []source.Event{mark(0), mark(10 * min1), mark(40 * min1)}
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", -min1, 11*min1, true),
		label("2026-03-02", 11*min1, 39*min1, false),
		label("2026-03-02", 39*min1, 41*min1, true),
	}}
	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(5, 12, 20, 35), LeadIns: mins(1), TailOuts: mins(1)},
		Objective: truth,
	}, events)

	if res.Pooled() {
		t.Errorf("Pooled() is true over one population: %+v", res.Splits)
	}
	if got, want := len(res.Splits), 1; got != want {
		t.Fatalf("the result holds %d populations, want %d", got, want)
	}
	if got, want := res.CommonBest(), res.Best; !slices.Equal(got, want) {
		t.Errorf("CommonBest() = %v over one population, want the search's own best %v", got, want)
	}
}
