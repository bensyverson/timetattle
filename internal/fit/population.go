package fit

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// A LABEL IS NOT WORTH THE SAME AS EVERY OTHER LABEL, and this file is what
// stops the fit pretending otherwise.
//
// `timetattle review` draws the derived sittings and asks whether they are
// right, so a reviewer who agrees produces an engaged set that is a strict
// SUBSET of the derivation: the objective cannot be improved by claiming less
// and claiming more only adds contradiction, so the minimum sits at the
// parameters that were on the screen by construction. Measured over the
// fourteen presence-covered reviewed days the residual at the shipped 15m/5m/5m
// is exactly 0.000h and contradicted is 0.000h at every point of the grid
// (project/private/2026-08-02-a-fitter-that-reports-an-interval.md).
//
// `review -blind` shows the reviewer where evidence was SEEN, with no block, no
// sitting and no margin on the screen. Those labels are formed independently of
// the parameters, so a fit against them measures the model.
//
// Pooling the two averages a population that is worth something with one that
// is worth almost nothing, in the direction that looks like success. Migration
// 0011 records which is which; everything here is the reader that acts on it.

// Population is one kind of label, and how much of it there is.
//
// It is reported for EVERY kind, present or not. A census that listed only what
// it found would report "confirming: 70" against the live store and leave the
// reader to infer the rest — and "nothing was recorded blind" is an observation,
// which a missing key cannot express. That collapse is a bug this project has
// shipped three times.
type Population struct {
	Shown  adjustments.Shown
	Labels int
	Days   int
	// Engaged and NotEngaged are what this population asserted, which is the
	// scale its own residual sits on.
	Engaged, NotEngaged time.Duration
}

// Asserted is all the time this population said anything at all about.
func (p Population) Asserted() time.Duration { return p.Engaged + p.NotEngaged }

// Empty reports that nothing was recorded for this population. It is a
// different statement from a residual of zero, and the whole reason this type
// exists is that the two look identical once a number has been printed.
func (p Population) Empty() bool { return p.Labels == 0 }

// Describe names a population in words a report can print, rather than in the
// column's vocabulary.
func Describe(s adjustments.Shown) string {
	if s == adjustments.Observations {
		return "blind"
	}
	return "confirming a derivation"
}

// Saw is the population this label belongs to, with the zero value read as a
// confirmation rather than as a missing value.
//
// That is the same argument migration 0011 makes for its NOT NULL DEFAULT, and
// it is honest for the same reason: `timetattle review` was the only writer and
// it had no other mode, so the value is KNOWN for every label recorded before a
// blind pass existed rather than merely unrecorded. Where a value were
// genuinely unknown this would have to say so instead.
func (l Label) Saw() adjustments.Shown {
	if l.Shown == "" {
		return adjustments.Derivation
	}
	return l.Shown
}

// Census is every population these labels could be split into, in a stable
// order, INCLUDING the ones holding nothing.
func (t Truth) Census() []Population {
	counts := map[adjustments.Shown]*Population{}
	days := map[adjustments.Shown]map[string]bool{}
	order := slices.Clone(adjustments.Everything())
	for _, s := range order {
		counts[s], days[s] = &Population{Shown: s}, map[string]bool{}
	}
	for _, l := range t.Labels {
		s := l.Saw()
		if counts[s] == nil {
			// A caller that invented a population is not silently dropped from
			// its own census; it is named, at the end, so the total reconciles.
			counts[s], days[s] = &Population{Shown: s}, map[string]bool{}
			order = append(order, s)
		}
		p := counts[s]
		p.Labels++
		days[s][l.Day] = true
		if l.Engaged {
			p.Engaged += l.Span.Duration()
		} else {
			p.NotEngaged += l.Span.Duration()
		}
	}
	out := make([]Population, 0, len(order))
	for _, s := range order {
		p := *counts[s]
		p.Days = len(days[s])
		out = append(out, p)
	}
	return out
}

// Only is the sub-population a reviewer formed while looking at one thing.
//
// IT REFUSES AN EMPTY POPULATION RATHER THAN RETURNING ONE. A residual over
// zero labels is 0.000h, which is indistinguishable from a perfect fit, and a
// caller handed an empty Truth would print that number in good faith. The
// complaint carries the whole census so that "nobody reviewed blind" can be
// told from "nobody reviewed at all" — those call for different actions and
// this project has collapsed them before.
func (t Truth) Only(shown adjustments.Shown) (Truth, error) {
	kept := t.only(shown)
	if len(kept) == 0 {
		return Truth{}, fmt.Errorf("no label in this ground truth was produced %s: %s. "+
			"A fit restricted to a population nobody recorded would score 0.000h over zero "+
			"labels, which reads as a perfect fit and is a statement about nothing",
			Describe(shown), censusPhrase(t.Census()))
	}
	return Truth{Labels: kept}, nil
}

func (t Truth) only(shown adjustments.Shown) []Label {
	var out []Label
	for _, l := range t.Labels {
		if l.Saw() == shown {
			out = append(out, l)
		}
	}
	return out
}

// ByShown is Score broken out by what the reviewer was looking at. See Census
// for why the two must never be summed without both being visible.
func (t Truth) ByShown(derived []sittings.Sitting) map[string]Residual {
	return t.groupBy(derived, func(l Label) string { return string(l.Saw()) })
}

// Contested is every day two populations both claim time on.
//
// POOLING THOSE IS NOT A RESIDUAL OVER A PARTITION. Each population tiles the
// day on its own, so together they cover it twice: the denominator
// double-counts, and where the two labellings disagree the pooled figure
// depends on nothing but their relative sizes. A caller must check this before
// pooling; cmd/timetattle refuses the run when it is non-empty.
//
// A day is the right granularity because the only producer of these labels is a
// whole-day review, whose assertions tile one day and nothing else. The overlap
// itself is measured on the spans rather than on the day names, so a producer
// that did something else is still caught.
func (t Truth) Contested() []string {
	spans := map[adjustments.Shown][]timespan.Span{}
	for _, l := range t.Labels {
		spans[l.Saw()] = append(spans[l.Saw()], l.Span)
	}
	present := slices.Sorted(maps.Keys(spans))
	var clashes []timespan.Span
	for i, a := range present {
		for _, b := range present[i+1:] {
			clashes = append(clashes,
				timespan.Intersect(timespan.Union(spans[a]), timespan.Union(spans[b]))...)
		}
	}
	if len(clashes) == 0 {
		return nil
	}
	clashes = timespan.Union(clashes)
	days := map[string]bool{}
	for _, l := range t.Labels {
		if timespan.Total(timespan.Intersect([]timespan.Span{l.Span}, clashes)) > 0 {
			days[l.Day] = true
		}
	}
	return slices.Sorted(maps.Keys(days))
}

// censusPhrase renders a census the way a COMPLAINT should: every population,
// with its count, including the zeroes. "0 blind" is the whole content of the
// message it appears in.
func censusPhrase(census []Population) string {
	parts := make([]string, 0, len(census))
	for _, p := range census {
		parts = append(parts, fmt.Sprintf("%d %s", p.Labels, Describe(p.Shown)))
	}
	return joinWithAnd(parts)
}

// presentPhrase renders a census the way a LABEL ON A FIGURE should: what the
// figure was computed over, and nothing about what it was not.
//
// The distinction from censusPhrase is not cosmetic. This one is printed by
// Name, which describes the objective actually scored — and a `-shown blind`
// run has legitimately been restricted, so listing "0 confirming a derivation"
// beside its residual would read as a claim that the store holds none. What the
// store holds is the census, printed in its own right by the report.
func presentPhrase(census []Population) string {
	parts := make([]string, 0, len(census))
	for _, p := range census {
		if p.Empty() {
			continue
		}
		parts = append(parts, fmt.Sprintf("%d %s", p.Labels, Describe(p.Shown)))
	}
	if len(parts) == 1 {
		// "all X" rather than a bare count, so a single-population objective
		// reads as the exclusion it is rather than as a total.
		return "all " + Describe(firstPresent(census))
	}
	return joinWithAnd(parts)
}

func firstPresent(census []Population) adjustments.Shown {
	for _, p := range census {
		if !p.Empty() {
			return p.Shown
		}
	}
	return adjustments.Derivation
}

func joinWithAnd(parts []string) string {
	switch len(parts) {
	case 0:
		return "nothing"
	case 1:
		return parts[0]
	default:
		return fmt.Sprintf("%s and %s",
			strings.Join(parts[:len(parts)-1], ", "), parts[len(parts)-1])
	}
}
