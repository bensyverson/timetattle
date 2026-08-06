package fit_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/source"
)

const (
	min1  = time.Minute
	min5  = 5 * time.Minute
	min10 = 10 * time.Minute
	min15 = 15 * time.Minute
)

func mins(ns ...int) []time.Duration {
	out := make([]time.Duration, 0, len(ns))
	for _, n := range ns {
		out = append(out, time.Duration(n)*time.Minute)
	}
	return out
}

func run(t *testing.T, s fit.Search, events []source.Event) fit.Result {
	t.Helper()
	res, err := s.Run(events)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

// THE FIRST THING THIS PACKAGE HAS TO GET RIGHT.
//
// A corpus with two clumps 58 minutes apart and nothing in between: every gap
// from 11m to 45m produces the identical two sittings, and the labels agree
// with all of them exactly. A fitter that reports one of those gaps as the
// answer has invented a distinction the data does not contain.
func TestSearchDoesNotNameABestFromAFlatBand(t *testing.T) {
	events := []source.Event{
		mark(0), mark(min1), mark(2 * min1),
		mark(60 * min1), mark(61 * min1), mark(62 * min1),
	}
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", -min5, 7*min1, true),
		label("2026-03-02", 7*min1, 55*min1, false),
		label("2026-03-02", 55*min1, 67*min1, true),
	}}
	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(11, 15, 20, 30, 45, 61), LeadIns: mins(5), TailOuts: mins(5)},
		Objective: truth,
	}, events)

	if res.Min.Total() != 0 {
		t.Fatalf("the minimum residual is %v, want 0: the labels were written to match exactly", res.Min.Total())
	}
	if got, want := len(res.Best), 5; got != want {
		t.Errorf("Best holds %d points (%v), want all %d that attain the minimum", got, res.Best, want)
	}
	if res.UniqueMinimum() {
		t.Error("UniqueMinimum is true over a five-point plateau")
	}

	var found bool
	for _, b := range res.Flat {
		if b.Identical && len(b.Points) == 5 {
			found = true
		}
	}
	if !found {
		t.Errorf("no five-point identical-model band was reported; Flat = %+v", res.Flat)
	}

	p := res.Profile(fit.GapAxis)
	if got, want := p.Interval.Low, 11*min1; got != want {
		t.Errorf("the gap interval starts at %v, want %v", got, want)
	}
	if got, want := p.Interval.High, 45*min1; got != want {
		t.Errorf("the gap interval ends at %v, want %v", got, want)
	}
}

// The failure this project has already had once: error against observed
// screen-on fell monotonically from 126.2h at a 5m gap to 116.9h at an hour,
// and the premise was wrong rather than the arithmetic. A fitter that cannot
// say "this objective has no minimum" will report the edge of its own search as
// an answer.
func TestSearchSaysThereIsNoMinimumWhenTheObjectiveOnlyFalls(t *testing.T) {
	// Separations of 10m, 15m and 20m, and a label that says the whole span was
	// work — so every widening of the gap can only agree more.
	events := []source.Event{mark(0), mark(10 * min1), mark(25 * min1), mark(45 * min1)}
	truth := fit.Truth{Labels: []fit.Label{label("2026-03-02", -min1, 46*min1, true)}}

	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(5, 12, 17, 22), LeadIns: mins(1), TailOuts: mins(1)},
		Objective: truth,
	}, events)

	p := res.Profile(fit.GapAxis)
	if p.Trend != fit.TrendFalling {
		t.Errorf("Trend = %q, want %q: the residual falls at every step and never turns", p.Trend, fit.TrendFalling)
	}
	if !p.ArgminAtEdge {
		t.Error("ArgminAtEdge is false, but the best gap is the largest one searched")
	}
	if got, want := p.Best, []time.Duration{39 * min1, 31 * min1, 18 * min1, 0}; !slices.Equal(got, want) {
		t.Errorf("the gap profile is %v, want %v", got, want)
	}
	if !p.Interval.HighBound.AtSearchEdge {
		t.Error("the interval's upper end does not say it is the edge of the search rather than a bound")
	}
}

// An interval is only worth reporting if each end names what holds it there.
func TestSearchNamesWhatBoundsEachEndOfAnInterval(t *testing.T) {
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

	p := res.Profile(fit.GapAxis)
	if p.Trend != fit.TrendMinimum {
		t.Errorf("Trend = %q, want %q", p.Trend, fit.TrendMinimum)
	}
	if got, want := p.Interval.Low, 12*min1; got != want {
		t.Errorf("interval low = %v, want %v", got, want)
	}
	if got, want := p.Interval.High, 20*min1; got != want {
		t.Errorf("interval high = %v, want %v", got, want)
	}
	if p.Interval.LowBound.AtSearchEdge || p.Interval.HighBound.AtSearchEdge {
		t.Fatalf("both ends are held by a value that was tried, not by the edge of the search: %+v", p.Interval)
	}
	if got, want := p.Interval.LowBound.Next, 5*min1; got != want {
		t.Errorf("the value just below the interval is %v, want %v", got, want)
	}
	if got, want := p.Interval.LowBound.Penalty, 8*min1; got != want {
		t.Errorf("the penalty for dropping to %v is %v, want %v", p.Interval.LowBound.Next, got, want)
	}
	if got, want := p.Interval.HighBound.Next, 35*min1; got != want {
		t.Errorf("the value just above the interval is %v, want %v", got, want)
	}
	if got, want := p.Interval.HighBound.Penalty, 28*min1; got != want {
		t.Errorf("the penalty for rising to %v is %v, want %v", p.Interval.HighBound.Next, got, want)
	}
}

// Lead and tail are asymmetric in the measured data — median lead 4m44s against
// a median tail of 38s, a factor of 7.5 — and internal/config sets them equal.
// A fitter that could only move them together could not express the answer the
// evidence points at.
func TestSearchMovesLeadInAndTailOutIndependently(t *testing.T) {
	// Three marks two hours apart, and labels saying work ran from ten minutes
	// before each to one minute after it. The right answer is lead-in 10m,
	// tail-out 1m, and no symmetric pair can reach it.
	events := []source.Event{mark(0), mark(2 * time.Hour), mark(4 * time.Hour)}
	var labels []fit.Label
	cursor := -30 * min1
	for _, atOffset := range []time.Duration{0, 2 * time.Hour, 4 * time.Hour} {
		labels = append(labels,
			label("2026-03-02", cursor, atOffset-min10, false),
			label("2026-03-02", atOffset-min10, atOffset+min1, true))
		cursor = atOffset + min1
	}
	labels = append(labels, label("2026-03-02", cursor, 4*time.Hour+30*min1, false))

	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(30), LeadIns: mins(0, 5, 10, 15), TailOuts: mins(0, 1, 5, 10)},
		Objective: fit.Truth{Labels: labels},
	}, events)

	if !res.UniqueMinimum() {
		t.Fatalf("expected one best point, got %v", res.Best)
	}
	best := res.Best[0]
	if best.LeadIn != min10 || best.TailOut != min1 {
		t.Errorf("best = %v, want lead-in 10m and tail-out 1m", best)
	}
	if res.Min.Total() != 0 {
		t.Errorf("the best residual is %v, want 0", res.Min.Total())
	}
	if best.LeadIn == best.TailOut {
		t.Error("the fit returned a symmetric pair, which this corpus was built to exclude")
	}
}

// A search that quietly dropped a third of its grid reads exactly like one that
// covered it.
func TestSearchSaysWhichPointsItSkippedAndWhy(t *testing.T) {
	events := []source.Event{mark(0), mark(30 * min1)}
	truth := fit.Truth{Labels: []fit.Label{label("2026-03-02", -min10, 40*min1, true)}}
	s := fit.Search{
		Grid:      fit.Grid{Gaps: mins(2, 5, 10, 15, 20), LeadIns: mins(5), TailOuts: mins(5)},
		Objective: truth,
	}

	res := run(t, s, events)
	if got, want := len(res.InertSkipped), 3; got != want {
		t.Errorf("InertSkipped holds %d points, want %d (gaps 2m, 5m and 10m against a 10m floor)", got, want)
	}
	if got, want := len(res.Cells), 2; got != want {
		t.Errorf("%d cells were evaluated, want %d", got, want)
	}
	if got := len(res.InertSkipped) + len(res.Cells); got != len(s.Grid.Points()) {
		t.Errorf("skipped plus evaluated is %d, against a grid of %d: some points are unaccounted for",
			got, len(s.Grid.Points()))
	}
	if res.InertVerified != 0 {
		t.Errorf("InertVerified = %d without IncludeInert; nothing was checked", res.InertVerified)
	}
	for _, p := range res.Best {
		if p.Inert() {
			t.Errorf("an inert point %v was named as best", p)
		}
	}
}

// With IncludeInert the rule is checked on whatever corpus is in front of it,
// rather than trusted from a document written about a different one.
func TestSearchVerifiesTheInertRuleWhenAskedTo(t *testing.T) {
	events := []source.Event{mark(0), mark(3 * min1), mark(11 * min1), mark(30 * min1)}
	truth := fit.Truth{Labels: []fit.Label{label("2026-03-02", -min10, 40*min1, true)}}

	res := run(t, fit.Search{
		Grid:         fit.Grid{Gaps: mins(2, 5, 10, 15, 20), LeadIns: mins(5), TailOuts: mins(5)},
		Objective:    truth,
		IncludeInert: true,
	}, events)

	if got, want := len(res.InertSkipped), 0; got != want {
		t.Errorf("InertSkipped holds %d points, want %d: IncludeInert evaluates them", got, want)
	}
	if got, want := res.InertVerified, 3; got != want {
		t.Errorf("InertVerified = %d, want %d", got, want)
	}
	if len(res.InertViolations) != 0 {
		t.Errorf("the inertness rule was violated at %v, which would falsify it on this corpus",
			res.InertViolations)
	}
	for _, p := range res.Best {
		if p.Inert() {
			t.Errorf("an inert point %v was named as best even though it was evaluated", p)
		}
	}
}

// The circularity guard. If the parameters the reviewer was shown win, the fit
// has not measured anything, and the result has to say so where a reader will
// see it rather than leaving it to a footnote in a document.
func TestSearchReportsCircularityWhenTheShownParametersWin(t *testing.T) {
	events := []source.Event{mark(0), mark(10 * min1), mark(40 * min1)}
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", -min1, 11*min1, true),
		label("2026-03-02", 11*min1, 39*min1, false),
		label("2026-03-02", 39*min1, 41*min1, true),
	}}
	grid := fit.Grid{Gaps: mins(5, 12, 20, 35), LeadIns: mins(1), TailOuts: mins(1)}

	shown := fit.Point{Gap: 12 * min1, LeadIn: min1, TailOut: min1}
	res := run(t, fit.Search{Grid: grid, Objective: truth, Reference: &shown}, events)
	if !res.Circular {
		t.Error("Circular is false although the reference point is one of the best")
	}
	if res.Reference == nil {
		t.Fatal("the reference point was not evaluated")
	}
	if got, want := res.ReferenceRank, 1; got != want {
		t.Errorf("ReferenceRank = %d, want %d", got, want)
	}

	// A reference nobody would call circular: the worst point in the grid, and
	// one that is not even in it.
	elsewhere := fit.Point{Gap: 90 * min1, LeadIn: min1, TailOut: min1}
	res = run(t, fit.Search{Grid: grid, Objective: truth, Reference: &elsewhere}, events)
	if res.Circular {
		t.Error("Circular is true for a reference point the grid beats outright")
	}
	if res.Reference == nil {
		t.Fatal("a reference point outside the grid was not evaluated")
	}
	if res.ReferenceRank <= 1 {
		t.Errorf("ReferenceRank = %d for a reference every live point beats", res.ReferenceRank)
	}
}

// A residual is a duration with no meaning until something says what scale it
// is on. Against labels that agree with the derivation almost everywhere, the
// objective barely moves, and that — not the residual — is the number that says
// the fit learnt nothing.
func TestResultReportsTheScaleTheResidualSitsOn(t *testing.T) {
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

	if got, want := res.Span(), 28*min1; got != want {
		t.Errorf("Span = %v, want %v: the objective ran from 0 to 28m across the live grid", got, want)
	}
	if got, want := res.NullNoneEngaged.Total(), 14*min1; got != want {
		t.Errorf("the none-engaged null = %v, want the whole engaged label at %v", got, want)
	}
	if got, want := res.NullAllEngaged.Total(), 28*min1; got != want {
		t.Errorf("the all-engaged null = %v, want the whole not-engaged label at %v", got, want)
	}
	if got := res.Discrimination(); got != 1 {
		t.Errorf("Discrimination = %v, want 1: the search covers the whole of the available error here", got)
	}
}

func TestRunRefusesAGridItCannotSearch(t *testing.T) {
	s := fit.Search{
		Grid:      fit.Grid{Gaps: mins(2), LeadIns: mins(5), TailOuts: mins(5)},
		Objective: fit.Truth{Labels: []fit.Label{label("2026-03-02", 0, min10, true)}},
	}
	if _, err := s.Run([]source.Event{mark(0)}); err == nil {
		t.Fatal("Run accepted a grid every point of which is inert")
	}
}

func TestRunRefusesLabelsItCannotScoreAgainst(t *testing.T) {
	s := fit.Search{
		Grid:      fit.Grid{Gaps: mins(15), LeadIns: mins(1), TailOuts: mins(1)},
		Objective: fit.Truth{},
	}
	if _, err := s.Run([]source.Event{mark(0)}); err == nil {
		t.Fatal("Run accepted an objective with no labels in it")
	}
}

// The derivation itself travels with the score. A point that agrees with the
// labels while claiming an implausible amount of time is a different thing from
// one that agrees and claims a plausible amount, and a cell holding only a
// residual could not tell them apart.
func TestCellsCarryTheDerivationAsWellAsTheScore(t *testing.T) {
	events := []source.Event{mark(0), mark(40 * min1)}
	res := run(t, fit.Search{
		Grid:      fit.Grid{Gaps: mins(15), LeadIns: mins(1), TailOuts: mins(1)},
		Objective: fit.Truth{Labels: []fit.Label{label("2026-03-02", -min10, 50*min1, true)}},
	}, events)

	if len(res.Cells) != 1 {
		t.Fatalf("got %d cells, want 1", len(res.Cells))
	}
	c := res.Cells[0]
	if got, want := c.Sittings, 2; got != want {
		t.Errorf("Sittings = %d, want %d", got, want)
	}
	if got, want := c.Engaged, 4*min1; got != want {
		t.Errorf("Engaged = %v, want %v", got, want)
	}
	if c.Model == 0 {
		t.Error("Model is zero, so two cells could not be compared for having produced the same answer")
	}
}
