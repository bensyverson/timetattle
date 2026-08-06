package fit

import (
	"fmt"
	"hash/fnv"
	"maps"
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
)

// Search is one fit: a grid, something to minimise, and the two honesty
// settings that decide what the result is allowed to claim.
type Search struct {
	Grid      Grid
	Objective Objective

	// Base carries the parameters the fit does NOT move — the presence and
	// ignore lists. They decide which observations exist, and varying them
	// would compare derivations drawn from different corpora and call the
	// difference a better fit.
	Base sittings.Params

	// Tolerance is how much worse than the minimum a point may score and still
	// be inside the reported interval. It is a duration rather than a
	// percentage because the objective is a duration.
	//
	// Zero means exact ties only. That is the right default here and not merely
	// the conservative one: these labels carry no error model, so any non-zero
	// tolerance is an assertion about measurement noise that nobody has
	// measured. A caller who sets one is making that assertion, and Result
	// carries it so a reader can see it was made.
	Tolerance time.Duration

	// IncludeInert evaluates the points where the gap is inert instead of
	// skipping them, and checks each against its canonical representative. It
	// costs one clustering per inert point and buys the inertness rule being
	// CHECKED on this corpus rather than assumed from a document.
	//
	// Inert points never become candidates for the minimum either way. This
	// decides whether the rule is verified or trusted, not whether it applies.
	IncludeInert bool

	// Reference is the parameter set the labels were produced under — what the
	// reviewer was actually shown. It is evaluated whether or not it is in the
	// grid, and if it wins, the result says so, because a fit that returns the
	// value that was on the screen has not measured anything.
	Reference *Point
}

// Cell is one evaluated point.
type Cell struct {
	Point    Point
	Inert    bool
	Residual Residual
	// Sittings and Engaged describe the derivation itself rather than its
	// agreement with the labels, so that a point scoring well can be seen to be
	// claiming a plausible amount of time rather than only agreeing.
	Sittings int
	Engaged  time.Duration
	// Model fingerprints the derived sittings. Two cells sharing it produced
	// the same answer, so no objective whatsoever could separate them.
	Model uint64
	// ByShown is this point's residual split by what the reviewer was looking
	// at when they produced each label.
	//
	// It is computed HERE, while the sittings are in hand, because they are
	// deliberately not memoised — see evaluate below — so a caller wanting the
	// split afterwards would have to re-derive the whole grid. That cost is the
	// only reason a fitter would be tempted to report the pooled figure alone,
	// and it is paid once here so the temptation does not arise.
	ByShown map[string]Residual
}

// Split is one population's own view of the whole search: where IT is
// minimised, over what range it moved, and every live point attaining its
// minimum.
//
// This is what makes "the populations were not pooled" a measurement rather
// than a claim in a comment. Two populations minimised at different points are
// two different answers to the same question, and the pooled minimum then
// reports whichever population is larger — a headcount, printed in the format
// of a fitted value.
type Split struct {
	Shown    string
	Min, Max Residual
	Best     []Point
}

// Result is a completed search and everything a reader needs to refuse to
// over-read it.
type Result struct {
	Grid      Grid
	Objective string
	Tolerance time.Duration

	// Cells is every GRID point that was evaluated, live and inert, in grid
	// order, and Live how many of them were candidates for the minimum. A rank
	// quoted against the first when it was computed against the second is a
	// figure nobody can reconcile.
	//
	// Two things may be clustered without appearing here, and both are reported
	// in their own right instead: a Reference outside the grid, which is
	// Result.Reference, and the canonical representative an inert point is
	// verified against when it is not itself a grid point.
	Cells []Cell
	Live  int
	// InertSkipped is every point NOT evaluated because its gap is inert, and
	// it is a field rather than a log line because a search that quietly
	// dropped a third of its grid reads exactly like one that covered it.
	InertSkipped []Point
	// InertVerified counts inert points evaluated and found to produce the
	// identical derivation as their canonical representative; InertViolations
	// names any that did not, which would falsify the rule on this corpus.
	//
	// One point in each inert band IS its own canonical representative, so it
	// verifies trivially. The count says how many were checked, not how many
	// checks were non-trivial.
	InertVerified   int
	InertViolations []Point

	// Best is EVERY live point attaining the minimum, in grid order. More than
	// one means the search cannot name a best, and Result says so rather than
	// taking the first.
	Best []Point
	Min  Residual
	Max  Residual

	// Profiles is the profile of the objective along each axis: the best score
	// achievable at each value of that parameter, with the other two free.
	Profiles []Profile

	// Flat is every set of two or more live points that produced the IDENTICAL
	// derivation. These are regions the data cannot distinguish at all, as
	// against regions this one objective happens not to.
	Flat []Band

	// NullAllEngaged and NullNoneEngaged bound the objective from above: what a
	// model claiming everything, and one claiming nothing, would score.
	NullAllEngaged, NullNoneEngaged Residual

	// Reference is the reference point's cell, and ReferenceRank is how many
	// live points scored strictly better than it, plus one. Circular is true
	// when nothing in the grid beat it by more than Tolerance.
	Reference     *Cell
	ReferenceRank int
	Circular      bool

	// ByDay, ByRegime and ByShown are the first best point's residual broken
	// out. They are here rather than left to the caller because the pooled
	// figure is the one that misleads, and a Result that made the breakdown
	// optional would make the misleading form the easy one.
	ByDay    map[string]Residual
	ByRegime map[string]Residual
	ByShown  map[string]Residual

	// Splits is every population's own minimum across the WHOLE live grid,
	// which is a stronger statement than ByShown at one point: it says where
	// each population would have sent the fit if it had been fitted alone.
	Splits []Split
}

// Pooled reports whether more than one population contributed to this search.
// A search over one population is not a pool and must not describe itself as
// one.
func (r Result) Pooled() bool { return len(r.Splits) > 1 }

// CommonBest is every live point that is best for EVERY population.
//
// An empty result over a pooled search is the finding: no parameter set
// satisfies both populations, so Result.Best is the larger population's answer
// carried by weight rather than an answer the labels agree on. Over a single
// population it is simply that population's minimum.
func (r Result) CommonBest() []Point {
	if len(r.Splits) == 0 {
		return nil
	}
	out := slices.Clone(r.Splits[0].Best)
	for _, s := range r.Splits[1:] {
		out = slices.DeleteFunc(out, func(p Point) bool { return !slices.Contains(s.Best, p) })
	}
	return out
}

// Split finds one population's view of the search.
func (r Result) Split(shown string) (Split, bool) {
	for _, s := range r.Splits {
		if s.Shown == shown {
			return s, true
		}
	}
	return Split{}, false
}

// Profile is the objective along one axis, with the other two parameters free
// to take their best value at each point.
//
// This is a profile rather than a slice through the minimum, deliberately: a
// slice would report how the gap behaves at ONE lead-in and tail-out, which is
// a statement about that pair rather than about the gap.
type Profile struct {
	Axis   Axis
	Values []time.Duration
	// Best is the lowest objective total achievable at each value.
	Best []time.Duration
	// Interval is the run of values around the best one that stay within
	// Tolerance of the overall minimum.
	Interval Interval
	// AcceptedElsewhere holds any value outside that run which also scores
	// within Tolerance. A non-empty list means the objective is multi-modal
	// along this axis and the interval is not the whole story.
	AcceptedElsewhere []time.Duration
	// Trend says whether the objective has a minimum along this axis at all.
	Trend Trend
	// ArgminAtEdge is true when the best value is the first or last searched,
	// which means the search may simply have stopped too early.
	ArgminAtEdge bool
}

// Interval is a fitted range for one parameter, with each end saying what holds
// it there.
type Interval struct {
	Low, High           time.Duration
	LowBound, HighBound Bound
}

// Bound is one end of an Interval and the evidence for it.
//
// AtSearchEdge is the case a report must never render as a bound: it means the
// search stopped, not that the data objected. Next and Penalty are the real
// thing — the adjacent value that was tried and how much worse it scored.
type Bound struct {
	AtSearchEdge bool
	Next         time.Duration
	Penalty      time.Duration
}

// Trend says what shape the objective has along an axis.
type Trend string

const (
	// TrendMinimum is the only shape that supports an answer: the objective
	// falls and then rises.
	TrendMinimum Trend = "minimum"
	// TrendFalling and TrendRising mean the objective never turned around
	// inside the searched range, so the best value is the edge of the search
	// and is not a fitted value at all. This has happened on this project
	// already: error against observed screen-on fell monotonically from 126.2h
	// at a 5m gap to 116.9h at 1h, and the premise was wrong rather than the
	// arithmetic.
	TrendFalling Trend = "falling"
	TrendRising  Trend = "rising"
	// TrendFlat means the axis changed nothing at all across the whole search.
	TrendFlat Trend = "flat"
)

// Band is a set of points a search could not tell apart.
type Band struct {
	// Identical is true when the points produced the same sittings, so no
	// objective whatever could separate them. A band that is merely score-tied
	// is a weaker claim and is not reported here.
	Identical bool
	Points    []Point
	Score     time.Duration
}

// Run evaluates the grid and assembles the result. It clusters once per
// evaluated point and does nothing else expensive.
func (s Search) Run(events []source.Event) (Result, error) {
	if s.Objective == nil {
		return Result{}, fmt.Errorf("a search needs something to minimise")
	}
	if err := s.Grid.Validate(); err != nil {
		return Result{}, err
	}
	if err := s.Objective.Validate(); err != nil {
		return Result{}, fmt.Errorf("this objective cannot be scored against: %w", err)
	}

	res := Result{Grid: s.Grid, Objective: s.Objective.Name(), Tolerance: s.Tolerance}
	res.NullAllEngaged, res.NullNoneEngaged = s.Objective.Nulls()

	// Every clustering is memoised on its point, so the canonical
	// representative an inert point is checked against, and a reference point
	// that happens to be in the grid, cost nothing extra.
	//
	// The SITTINGS are deliberately not memoised. A grid of a few hundred points
	// over the live corpus derives a few hundred thousand of them, each carrying
	// its own list of source names, and holding all of that to look at one point
	// again at the end would cost hundreds of megabytes for one lookup. The best
	// point is re-clustered below instead.
	// The split is taken per cell rather than only at the winner, because the
	// question "would this population have chosen a different point?" cannot be
	// answered from the winner alone.
	splitter, splits := s.Objective.(interface {
		ByShown([]sittings.Sitting) map[string]Residual
	})

	seen := map[Point]Cell{}
	derive := func(p Point) ([]sittings.Sitting, error) {
		at := s.Base
		at.Gap, at.LeadIn, at.TailOut = p.Gap, p.LeadIn, p.TailOut
		out, err := sessions.Cluster(events, at)
		if err != nil {
			return nil, fmt.Errorf("%v: %w", p, err)
		}
		return out.Sessions, nil
	}
	evaluate := func(p Point) (Cell, error) {
		if c, ok := seen[p]; ok {
			return c, nil
		}
		derived, err := derive(p)
		if err != nil {
			return Cell{}, err
		}
		var engaged time.Duration
		for _, x := range derived {
			engaged += x.Duration()
		}
		c := Cell{
			Point:    p,
			Inert:    p.Inert(),
			Residual: s.Objective.Score(derived),
			Sittings: len(derived),
			Engaged:  engaged,
			Model:    fingerprint(derived),
		}
		if splits {
			c.ByShown = splitter.ByShown(derived)
		}
		seen[p] = c
		return c, nil
	}

	for _, p := range s.Grid.Points() {
		if p.Inert() && !s.IncludeInert {
			res.InertSkipped = append(res.InertSkipped, p)
			continue
		}
		c, err := evaluate(p)
		if err != nil {
			return Result{}, err
		}
		res.Cells = append(res.Cells, c)

		// The inertness rule, checked rather than trusted: an inert point must
		// derive exactly what the top of its band derives.
		if p.Inert() {
			canonical, err := evaluate(p.Canonical())
			if err != nil {
				return Result{}, err
			}
			if canonical.Model == c.Model {
				res.InertVerified++
			} else {
				res.InertViolations = append(res.InertViolations, p)
			}
		}
	}

	live := make([]Cell, 0, len(res.Cells))
	for _, c := range res.Cells {
		if !c.Inert {
			live = append(live, c)
		}
	}
	res.Live = len(live)
	if len(live) == 0 {
		return Result{}, fmt.Errorf("no live point was evaluated, so there is nothing to minimise")
	}

	res.Min, res.Max = live[0].Residual, live[0].Residual
	for _, c := range live[1:] {
		if c.Residual.Total() < res.Min.Total() {
			res.Min = c.Residual
		}
		if c.Residual.Total() > res.Max.Total() {
			res.Max = c.Residual
		}
	}
	for _, c := range live {
		if c.Residual.Total() == res.Min.Total() {
			res.Best = append(res.Best, c.Point)
		}
	}

	res.Splits = splitsOf(live)
	res.Flat = identicalBands(live)
	for _, a := range Axes() {
		res.Profiles = append(res.Profiles, profile(live, a, res.Min.Total(), s.Tolerance))
	}

	if s.Reference != nil {
		c, err := evaluate(*s.Reference)
		if err != nil {
			return Result{}, err
		}
		res.Reference = &c
		res.ReferenceRank = 1
		for _, other := range live {
			if other.Residual.Total() < c.Residual.Total() {
				res.ReferenceRank++
			}
		}
		res.Circular = c.Residual.Total() <= res.Min.Total()+s.Tolerance
	}

	// The breakdowns come from the first best point. Where Best holds several
	// points sharing one Model the choice is immaterial; where it holds several
	// with different models it is not, and UniqueMinimum is what a caller has to
	// check before quoting these.
	if b, ok := s.Objective.(interface {
		ByDay([]sittings.Sitting) map[string]Residual
		ByRegime([]sittings.Sitting) map[string]Residual
	}); ok {
		best, err := derive(res.Best[0])
		if err != nil {
			return Result{}, err
		}
		res.ByDay, res.ByRegime = b.ByDay(best), b.ByRegime(best)
	}
	for _, c := range live {
		if c.Point == res.Best[0] {
			res.ByShown = c.ByShown
			break
		}
	}
	return res, nil
}

// splitsOf is each population's own minimum over the live grid.
//
// A population with no label at any point simply has no key here, and that is
// why Result must never be the only thing a report reads: a population that was
// never recorded is invisible to the search and visible only in the census. The
// two are printed together, deliberately.
func splitsOf(live []Cell) []Split {
	seen := map[string]bool{}
	for _, c := range live {
		for k := range c.ByShown {
			seen[k] = true
		}
	}
	out := make([]Split, 0, len(seen))
	for _, k := range slices.Sorted(maps.Keys(seen)) {
		s, first := Split{Shown: k}, true
		for _, c := range live {
			r, ok := c.ByShown[k]
			if !ok {
				continue
			}
			if first {
				s.Min, s.Max, first = r, r, false
			}
			if r.Total() < s.Min.Total() {
				s.Min = r
			}
			if r.Total() > s.Max.Total() {
				s.Max = r
			}
		}
		for _, c := range live {
			if r, ok := c.ByShown[k]; ok && r.Total() == s.Min.Total() {
				s.Best = append(s.Best, c.Point)
			}
		}
		out = append(out, s)
	}
	return out
}

// UniqueMinimum reports whether exactly one live point attained the minimum. A
// caller printing a single fitted value without checking this is printing an
// arbitrary member of a plateau.
func (r Result) UniqueMinimum() bool { return len(r.Best) == 1 }

// Span is how far the objective moved across the whole live search. It is the
// scale a residual has to be read against: a fit whose objective barely moves
// has not discriminated between its candidates, however small the residual is.
func (r Result) Span() time.Duration { return r.Max.Total() - r.Min.Total() }

// Discrimination is Span as a fraction of the larger no-skill null. It is the
// share of the available error the parameters explain, and on near-circular
// labels it is close to zero.
func (r Result) Discrimination() float64 {
	null := max(r.NullAllEngaged.Total(), r.NullNoneEngaged.Total())
	if null == 0 {
		return 0
	}
	return r.Span().Seconds() / null.Seconds()
}

// Profile finds the profile for an axis.
func (r Result) Profile(a Axis) Profile {
	for _, p := range r.Profiles {
		if p.Axis == a {
			return p
		}
	}
	return Profile{Axis: a}
}

// identicalBands groups live points by what they derived. A group of two or
// more is a region no objective could separate, which is a stronger and more
// useful statement than a tie in this one score.
func identicalBands(live []Cell) []Band {
	order := []uint64{}
	byModel := map[uint64][]Cell{}
	for _, c := range live {
		if _, seen := byModel[c.Model]; !seen {
			order = append(order, c.Model)
		}
		byModel[c.Model] = append(byModel[c.Model], c)
	}
	var out []Band
	for _, m := range order {
		group := byModel[m]
		if len(group) < 2 {
			continue
		}
		b := Band{Identical: true, Score: group[0].Residual.Total()}
		for _, c := range group {
			b.Points = append(b.Points, c.Point)
		}
		out = append(out, b)
	}
	return out
}

// profile is the objective along one axis with the other two free.
func profile(live []Cell, a Axis, minimum, tolerance time.Duration) Profile {
	best := map[time.Duration]time.Duration{}
	for _, c := range live {
		v, total := a.Of(c.Point), c.Residual.Total()
		if seen, ok := best[v]; !ok || total < seen {
			best[v] = total
		}
	}
	p := Profile{Axis: a}
	for v := range best {
		p.Values = append(p.Values, v)
	}
	slices.Sort(p.Values)
	for _, v := range p.Values {
		p.Best = append(p.Best, best[v])
	}

	argmin := 0
	for i, b := range p.Best {
		if b < p.Best[argmin] {
			argmin = i
		}
	}
	p.ArgminAtEdge = argmin == 0 || argmin == len(p.Values)-1

	accept := func(i int) bool { return p.Best[i] <= minimum+tolerance }
	low, high := argmin, argmin
	for low > 0 && accept(low-1) {
		low--
	}
	for high < len(p.Values)-1 && accept(high+1) {
		high++
	}
	p.Interval = Interval{Low: p.Values[low], High: p.Values[high]}
	if low == 0 {
		p.Interval.LowBound.AtSearchEdge = true
	} else {
		p.Interval.LowBound = Bound{Next: p.Values[low-1], Penalty: p.Best[low-1] - minimum}
	}
	if high == len(p.Values)-1 {
		p.Interval.HighBound.AtSearchEdge = true
	} else {
		p.Interval.HighBound = Bound{Next: p.Values[high+1], Penalty: p.Best[high+1] - minimum}
	}
	for i, v := range p.Values {
		if (i < low || i > high) && accept(i) {
			p.AcceptedElsewhere = append(p.AcceptedElsewhere, v)
		}
	}

	p.Trend = trendOf(p.Best)
	return p
}

// trendOf reads the shape of a profile. The two monotone answers are the ones
// that matter: they mean the search stopped before the objective turned around,
// so its best value is an edge rather than a fitted one.
func trendOf(best []time.Duration) Trend {
	flat, falling, rising := true, true, true
	for i := 1; i < len(best); i++ {
		switch {
		case best[i] < best[i-1]:
			flat, rising = false, false
		case best[i] > best[i-1]:
			flat, falling = false, false
		}
	}
	switch {
	case flat:
		return TrendFlat
	case falling:
		return TrendFalling
	case rising:
		return TrendRising
	default:
		return TrendMinimum
	}
}

// fingerprint identifies a derivation by its spans.
//
// The endpoints and nothing else: two derivations covering the same wall clock
// are the same answer for every purpose this package has, whatever they name as
// their sources or count as their events.
func fingerprint(derived []sittings.Sitting) uint64 {
	h := fnv.New64a()
	var buf [16]byte
	for _, s := range derived {
		putUint64(buf[0:8], uint64(s.Start.UnixNano()))
		putUint64(buf[8:16], uint64(s.End.UnixNano()))
		_, _ = h.Write(buf[:])
	}
	// An empty derivation is a real answer and must not fingerprint as the zero
	// value, which is what a caller checking Model != 0 would read as "never
	// computed".
	if len(derived) == 0 {
		_, _ = h.Write([]byte("no sittings"))
	}
	return h.Sum64()
}

func putUint64(b []byte, v uint64) {
	for i := range b {
		b[i] = byte(v >> (8 * (7 - i)))
	}
}
