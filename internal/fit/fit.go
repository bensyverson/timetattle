// Package fit searches the clustering parameters against human labels, and is
// built so that it cannot report a confident answer it does not have.
//
// Three parameters decide how internal/sessions turns observations into
// sittings: the idle Gap that separates one sitting from the next, and the
// LeadIn and TailOut margins each observation is expanded by. Everything here
// exists because a naive search over those three would return a number, and the
// number would be wrong in three separate ways this package refuses to hide.
//
// # The gap is inert at or below lead-in plus tail-out, so a third of a naive search is degenerate
//
// Each observation is expanded by LeadIn before and TailOut after. Two
// observations closer together than LeadIn+TailOut therefore already overlap
// after expansion, and are merged before the Gap threshold is consulted at all.
// Below that floor the Gap decides nothing, every value in the band produces
// byte-identical sittings, and a search that includes the band will pick one of
// them and report an excellent fit for it.
//
// It was verified as a falsifiable prediction on the live corpus and held to the
// digit at two settings: at 5m/5m, gaps of 1m, 8m, 9m and 10m all give 2377
// sittings and 1181.8h, and 11m gives 2305 and 1182.4h
// (project/private/2026-08-01-gap-threshold-is-inert-below-the-margins.md).
//
// This package takes BOTH available treatments rather than choosing one,
// because they answer different questions. An inert Point is not a distinct
// model — it is the same model as Point.Canonical, the boundary point at
// Gap = LeadIn+TailOut — so inert points are excluded from candidacy for the
// minimum, and Result.InertSkipped says exactly which and how many, because a
// reader who is not told will assume the low gaps were searched and rejected.
// And Search.IncludeInert evaluates them anyway, checking each against its
// canonical representative, so the rule is CHECKED on whatever corpus is in
// front of it rather than assumed from a document; a mismatch is reported as
// Result.InertViolations rather than swallowed.
//
// # Flatness is a result, not a thing to minimise over
//
// Even among live points the objective is often flat, and for a reason that has
// nothing to do with the parameter being pinned down: if no silence in the
// corpus falls between 15m and 30m, every gap in that band produces the same
// answer. Result.Flat names each such band, Result.Best holds EVERY point
// attaining the minimum rather than the first one found, and
// Result.UniqueMinimum reports whether there was one at all.
//
// # A residual against near-circular labels is not evidence of quality
//
// The labels this was built for are a reviewer's confirmation of a derivation
// he was shown. His engaged set is a strict subset of that derivation, with
// exactly 0.000h of reviewer-says-work-machine-did-not, so the objective spans
// 1.4h out of 143h and its minimum sits at the value that was on the screen
// (project/private/2026-08-01-the-reviewed-days-cannot-fit-the-parameter.md). Reporting
// "15m, residual 1.257h" from that would be the failure mode, not the result.
//
// So a residual is never reported alone. Result carries the two no-skill nulls
// the objective is bounded by, the span it moves across the whole search, and —
// when the caller names the parameters the reviewer was actually shown as
// Search.Reference — whether those parameters win, which is the circularity the
// numbers cannot distinguish themselves from.
package fit

import (
	"fmt"
	"slices"
	"time"
)

// Point is one candidate parameter set: the three values a fit moves.
//
// It deliberately does not carry the presence and ignore lists. Those decide
// which observations exist rather than what is done with them, so a search that
// varied them would be comparing derivations drawn from different corpora and
// calling the difference a better fit. They travel separately, as the base
// Params handed to Run.
type Point struct {
	Gap, LeadIn, TailOut time.Duration
}

// Inert reports whether Gap decides nothing at this Point, which is so at or
// below LeadIn+TailOut. See the package doc: it is a property of the clustering
// rule and is checked against the clusterer by TestTheInertRuleHoldsInTheClusterer.
func (p Point) Inert() bool { return p.Gap <= p.LeadIn+p.TailOut }

// Canonical is the Point producing the identical derivation with the Gap moved
// to the top of its inert band, and is p itself when p is not inert.
//
// It is what makes "an inert point is not a distinct model" operational rather
// than rhetorical: every inert point collapses onto this one, and Search checks
// that it does.
func (p Point) Canonical() Point {
	if p.Inert() {
		p.Gap = p.LeadIn + p.TailOut
	}
	return p
}

// String renders a point the way the reports do.
func (p Point) String() string {
	return fmt.Sprintf("gap %s, lead-in %s, tail-out %s",
		Compact(p.Gap), Compact(p.LeadIn), Compact(p.TailOut))
}

// Compact writes a whole number of minutes or hours without the zero tail
// time.Duration prints: 5m rather than 5m0s.
//
// It is here rather than in the CLI because a Point's String has to read the
// same in a project/ document as it does on a terminal, and this package is
// what both of those are quoting.
func Compact(d time.Duration) string {
	switch {
	case d == 0:
		return "0"
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%dm", d/time.Minute)
	default:
		return d.String()
	}
}

// Grid is the search space, one list of candidate values per parameter.
//
// A grid rather than a descent, deliberately. The objective here is a step
// function with flat plateaus everywhere — it changes only when a threshold
// crosses an actual silence in the corpus — so a gradient method has nothing to
// follow and a local search would stop on the first plateau and call it a
// minimum. An exhaustive grid is also the only thing that can honestly report
// where the flat regions ARE, which is most of what this package is for.
type Grid struct {
	Gaps, LeadIns, TailOuts []time.Duration
}

// Points is every combination in the grid, deduplicated and in a stable order.
func (g Grid) Points() []Point {
	gaps, leads, tails := sortedUnique(g.Gaps), sortedUnique(g.LeadIns), sortedUnique(g.TailOuts)
	out := make([]Point, 0, len(gaps)*len(leads)*len(tails))
	for _, gap := range gaps {
		for _, lead := range leads {
			for _, tail := range tails {
				out = append(out, Point{Gap: gap, LeadIn: lead, TailOut: tail})
			}
		}
	}
	return out
}

// Live and inert are the two halves of Points, split by the inertness rule.
// They are returned together so that a caller reporting one is holding the
// count of the other, which is what stops a narrowed search reading as a
// complete one.
func (g Grid) Split() (live, inert []Point) {
	for _, p := range g.Points() {
		if p.Inert() {
			inert = append(inert, p)
			continue
		}
		live = append(live, p)
	}
	return live, inert
}

// Validate refuses a grid that cannot be searched, naming what is missing.
func (g Grid) Validate() error {
	switch {
	case len(g.Gaps) == 0:
		return fmt.Errorf("the grid names no gaps to search")
	case len(g.LeadIns) == 0:
		return fmt.Errorf("the grid names no lead-ins to search")
	case len(g.TailOuts) == 0:
		return fmt.Errorf("the grid names no tail-outs to search")
	}
	for _, d := range slices.Concat(g.Gaps, g.LeadIns, g.TailOuts) {
		if d < 0 {
			return fmt.Errorf("the grid holds a negative duration %v", d)
		}
	}
	if live, _ := g.Split(); len(live) == 0 {
		return fmt.Errorf("every point in this grid has a gap at or below its lead-in plus tail-out, "+
			"so the gap decides nothing anywhere in it; search a gap above %s",
			Compact(slices.Min(g.LeadIns)+slices.Min(g.TailOuts)))
	}
	return nil
}

// Axis names one of the three parameters, so that a per-parameter result can
// say which parameter it is about without three copies of every type.
type Axis string

const (
	GapAxis     Axis = "gap"
	LeadInAxis  Axis = "lead-in"
	TailOutAxis Axis = "tail-out"
)

// Axes is the three axes in the order a report should list them.
func Axes() []Axis { return []Axis{GapAxis, LeadInAxis, TailOutAxis} }

// Of reads the axis's value out of a point.
func (a Axis) Of(p Point) time.Duration {
	switch a {
	case LeadInAxis:
		return p.LeadIn
	case TailOutAxis:
		return p.TailOut
	default:
		return p.Gap
	}
}

func sortedUnique(ds []time.Duration) []time.Duration {
	out := slices.Clone(ds)
	slices.Sort(out)
	return slices.Compact(out)
}
