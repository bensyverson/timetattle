// Package timespan is interval arithmetic over half-open wall-clock spans.
//
// It is the one implementation. internal/sessions and internal/adjustments each
// grew their own copy of union and subtract, written months apart by people who
// could not see each other's work, and the copies were NOT identical: three
// behavioural differences were measured between them, each of which a careless
// "extract the common one" would have decided by picking a file rather than by
// making a decision. The decisions are recorded below and each is held by a
// test rather than by this comment.
//
// # What is shared, and what is deliberately not
//
// internal/metrics keeps its own interval arithmetic and does NOT use this. The
// difference is real and is stated in that file: everything there measures how
// much CLOCK something covers, so a zero-length span covers none and is dropped
// on the way in. Here a zero-length span is ORDINARY — a commit, a save, a
// message is a point observation, and the clustering that has to keep it is the
// reason this package exists. One set of rules serving both meanings is how the
// zero-length case would end up decided by whichever caller was modified last.
//
// # Three decisions
//
//   - PROVENANCE IS THE CALLER'S. Span carries the interval and nothing else.
//     internal/sessions' copy had a third field naming the source, and its union
//     rebuilt each element without it, deliberately, because a span merged out of
//     two sources has no one source; internal/adjustments' copy copied the whole
//     struct, which is the same thing at two fields and silently different at
//     three. With two fields the question cannot be answered wrongly by accident,
//     and TestASpanCarriesTheIntervalAndNothingElse makes adding a third field
//     somebody's explicit decision rather than a quiet one.
//
//   - NOTHING RETURNED HERE ALIASES WHAT WAS PASSED IN. internal/sessions'
//     subtract returned the `from` slice itself when there was nothing to cut,
//     so the caller and the callee shared one backing array; the other copy
//     cloned. A shared helper cannot depend on each of its callers having cloned
//     first, and aliasing is invisible until something writes through the result
//     and edits an answer that was meant to be read.
//
//   - A ZERO-LENGTH SPAN IS A POINT, AND IT SURVIVES UNTIL SOMETHING COVERS IT.
//     Union keeps it. Subtract removes it only when a cut contains it, read
//     half-open, exactly as Covers reads it. internal/sessions' cursor form
//     dropped every zero-length span whenever there was anything at all to cut,
//     which silently deleted a lone point observation's sitting on any day the
//     backlight stream had recorded darkness anywhere — counted by nothing,
//     since no time was trimmed and no piece was emitted to be dropped.
package timespan

import (
	"slices"
	"sort"
	"time"
)

// Span is a half-open interval [Start, End).
//
// Zero-length spans are ordinary: a point observation is one, and so is an
// unbounded event's single endpoint.
type Span struct {
	Start, End time.Time
}

// Duration is how long the span runs. It is zero for a point.
func (s Span) Duration() time.Duration { return s.End.Sub(s.Start) }

// Union merges overlapping and touching spans into a disjoint, ordered run.
//
// The caller's slice is neither reordered nor written to.
func Union(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	sorted := slices.Clone(spans)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Start.Before(sorted[j].Start) })

	out := []Span{sorted[0]}
	for _, s := range sorted[1:] {
		last := &out[len(out)-1]
		if s.Start.After(last.End) {
			out = append(out, s)
			continue
		}
		last.End = LaterOf(last.End, s.End)
	}
	return out
}

// Subtract removes every instant of cut from each span in from, keeping from's
// order and never returning a slice that shares memory with it.
//
// Both runs are half-open, so a cut ending exactly where a span begins removes
// nothing. A point survives unless a cut contains it.
func Subtract(from, cut []Span) []Span {
	remaining := slices.Clone(from)
	if len(cut) == 0 {
		return remaining
	}
	for _, c := range Union(cut) {
		next := make([]Span, 0, len(remaining)+1)
		for _, s := range remaining {
			if !s.End.After(s.Start) {
				// A point. It is inside the cut or it is not; there is no
				// piece of it to keep.
				if !Covers([]Span{c}, s.Start) {
					next = append(next, s)
				}
				continue
			}
			// Disjoint, or touching only at an endpoint: nothing to remove.
			if !c.End.After(s.Start) || !s.End.After(c.Start) {
				next = append(next, s)
				continue
			}
			if s.Start.Before(c.Start) {
				next = append(next, Span{Start: s.Start, End: c.Start})
			}
			if c.End.Before(s.End) {
				next = append(next, Span{Start: c.End, End: s.End})
			}
		}
		remaining = next
	}
	return remaining
}

// Intersect is the time both runs claim, as a disjoint, ordered run.
//
// It compares every pair rather than walking both once, because it does not
// require its arguments to be disjoint or ordered and the cost of getting that
// precondition wrong is a silently short answer. internal/metrics has the
// linear form, for runs it has already unioned and for populations where the
// difference is a command against a wait.
func Intersect(a, b []Span) []Span {
	var out []Span
	for _, x := range a {
		for _, y := range b {
			start, end := LaterOf(x.Start, y.Start), EarlierOf(x.End, y.End)
			if end.After(start) {
				out = append(out, Span{Start: start, End: end})
			}
		}
	}
	return Union(out)
}

// Covers reports whether at falls inside one of the spans, half-open, so a
// zero-length span contains nothing — not even its own instant.
//
// spans MUST be sorted ascending by Start and pairwise non-overlapping — the
// shape Union and Subtract hand back, and the shape every call site in this
// repository passes: internal/sessions.Cluster queries Subtract's output
// directly, and the two other call sites each pass a single-element slice,
// which is sorted and non-overlapping with nothing to compare it against.
// This is a binary search, not a scan, and it trusts that ordering to decide
// which half of spans to discard — an unsorted or overlapping slice makes it
// return a wrong answer silently rather than a slow one. If a future caller
// cannot supply spans in this shape, sort and Union them first rather than
// weakening the precondition here.
func Covers(spans []Span, at time.Time) bool {
	// i is the index of the first span that starts after at, so the only
	// candidate that could cover at is the one just before it.
	i := sort.Search(len(spans), func(i int) bool {
		return spans[i].Start.After(at)
	})
	if i == 0 {
		return false
	}
	return spans[i-1].End.After(at)
}

// Overlaps reports whether two spans share any instant, counting a zero-length
// span as touching the span it sits inside or against.
func Overlaps(a, b Span) bool {
	return !a.Start.After(b.End) && !b.Start.After(a.End)
}

// Clip narrows s to the part of it inside within, reporting false when nothing
// of it is.
func Clip(s, within Span) (Span, bool) {
	start, end := LaterOf(s.Start, within.Start), EarlierOf(s.End, within.End)
	if end.Before(start) {
		return Span{}, false
	}
	return Span{Start: start, End: end}, true
}

// Total sums the lengths of a disjoint run. Overlapping spans would be counted
// twice, which is why every caller here unions first.
func Total(spans []Span) time.Duration {
	var sum time.Duration
	for _, s := range spans {
		sum += s.Duration()
	}
	return sum
}

// LaterOf and EarlierOf are the two-argument forms every caller of this package
// ends up writing for itself otherwise.
func LaterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func EarlierOf(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}
