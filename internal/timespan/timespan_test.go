package timespan_test

import (
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/timespan"
)

var base = time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

func at(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }

func span(from, to int) timespan.Span { return timespan.Span{Start: at(from), End: at(to)} }

// render prints a run as minute offsets, so a failure reads as the arithmetic
// it is rather than as two columns of RFC3339.
func render(spans []timespan.Span) string {
	if len(spans) == 0 {
		return "(none)"
	}
	var out strings.Builder
	for i, s := range spans {
		if i > 0 {
			out.WriteString(" ")
		}
		out.WriteString(s.Start.Sub(base).String() + ".." + s.End.Sub(base).String())
	}
	return out.String()
}

func equal(got, want []timespan.Span) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			return false
		}
	}
	return true
}

// DECISION (a) and (b), enforced rather than commented.
//
// internal/sessions carried its own span with a third field, `source`, and its
// union rebuilt every element as {start, end} — deliberately dropping that
// third field on a merge, because a span merged out of two sources has no one
// source. internal/adjustments' copy had two fields and copied the whole
// struct, which is identical behaviour at two fields and silently different at
// three.
//
// The shared type carries the interval and nothing else, so the two forms are
// the same function and the question cannot be answered wrongly by accident.
// Provenance belongs to the caller's own type — see internal/sessions'
// observation, which pairs a Span with the source it came from and never asks
// this package to carry it.
//
// This test is what makes that a decision rather than a comment: a third field
// added here fails it, and whoever adds it has to say what Union should do
// with the field on a merge.
func TestASpanCarriesTheIntervalAndNothingElse(t *testing.T) {
	fields := reflect.TypeFor[timespan.Span]().NumField()
	if fields != 2 {
		t.Fatalf("Span has %d fields; it must carry the interval and nothing else, "+
			"because Union merges two spans into one and any third field would have to be "+
			"dropped, guessed, or taken from whichever contributor happened to sort first",
			fields)
	}
}

func TestUnionMergesOverlappingAndTouchingSpans(t *testing.T) {
	got := timespan.Union([]timespan.Span{span(30, 40), span(0, 10), span(10, 20), span(35, 50)})
	want := []timespan.Span{span(0, 20), span(30, 50)}
	if !equal(got, want) {
		t.Errorf("Union gave %s, want %s", render(got), render(want))
	}
}

// A point observation is a zero-length span, and the clustering has to keep it:
// dropping it would delete every save, commit and message that observed an
// instant rather than an interval. internal/metrics' interval arithmetic drops
// them on purpose and stays separate for exactly this reason, which its own
// file says in as many words.
func TestUnionKeepsAZeroLengthSpan(t *testing.T) {
	got := timespan.Union([]timespan.Span{span(30, 30), span(0, 10)})
	want := []timespan.Span{span(0, 10), span(30, 30)}
	if !equal(got, want) {
		t.Errorf("Union gave %s, want %s", render(got), render(want))
	}
}

func TestUnionLeavesItsArgumentInTheOrderItWasGiven(t *testing.T) {
	in := []timespan.Span{span(30, 40), span(0, 10)}
	timespan.Union(in)
	if !in[0].Start.Equal(at(30)) {
		t.Errorf("Union sorted its caller's slice: %s", render(in))
	}
}

func TestSubtractRemovesTheCutAndNothingElse(t *testing.T) {
	got := timespan.Subtract([]timespan.Span{span(0, 60)}, []timespan.Span{span(20, 30)})
	want := []timespan.Span{span(0, 20), span(30, 60)}
	if !equal(got, want) {
		t.Errorf("Subtract gave %s, want %s", render(got), render(want))
	}
}

// Half-open at both ends: a cut that ends exactly where a span begins removes
// nothing, and neither does one that begins exactly where it ends.
func TestSubtractTreatsBothRunsAsHalfOpen(t *testing.T) {
	got := timespan.Subtract([]timespan.Span{span(20, 30)}, []timespan.Span{span(0, 20), span(30, 40)})
	want := []timespan.Span{span(20, 30)}
	if !equal(got, want) {
		t.Errorf("Subtract gave %s, want %s", render(got), render(want))
	}
}

// DECISION (c). The shared Subtract never hands back the slice it was given.
//
// internal/sessions' copy returned `from` itself when there was nothing to cut,
// so the caller and the callee shared one backing array. internal/adjustments'
// cloned. Aliasing is invisible until somebody writes through the result, and
// then it edits an answer that was meant to be read — which is what
// internal/adjustments' TestFoldCopiesTheMachineAnswerRatherThanEditingIt
// exists to prevent for Overlay.Engaged.
//
// A shared helper cannot rely on every caller having cloned first, so the copy
// is here rather than at the call sites.
func TestSubtractNeverAliasesTheSliceItWasGiven(t *testing.T) {
	for _, tc := range []struct {
		name string
		cut  []timespan.Span
	}{
		{"nothing to cut", nil},
		{"a cut that removes nothing", []timespan.Span{span(90, 100)}},
	} {
		from := []timespan.Span{span(0, 60)}
		out := timespan.Subtract(from, tc.cut)
		if len(out) != 1 {
			t.Fatalf("%s: Subtract gave %s, want the span back", tc.name, render(out))
		}
		out[0].End = at(1)
		if !from[0].End.Equal(at(60)) {
			t.Errorf("%s: writing to the result edited the caller's span: %s", tc.name, render(from))
		}
	}
}

// A zero-length span is a point, and it survives a subtraction unless the cut
// contains it — read half-open, the same way Covers reads it. Otherwise whether
// a point observation survived would depend on whether anything else in the day
// happened to need cutting.
func TestSubtractKeepsAPointOutsideTheCutAndDropsOneInsideIt(t *testing.T) {
	points := []timespan.Span{span(10, 10), span(20, 20), span(30, 30), span(40, 40)}
	got := timespan.Subtract(points, []timespan.Span{span(20, 30)})
	want := []timespan.Span{span(10, 10), span(30, 30), span(40, 40)}
	if !equal(got, want) {
		t.Errorf("Subtract gave %s, want %s: a point at the cut's start is inside it, "+
			"one at its end is not, and one nowhere near it is untouched", render(got), render(want))
	}
}

// TestSubtractOfUnionedSpansStaysSortedAndDisjoint is what makes Covers'
// binary search legal at its one hot call site: internal/sessions.Cluster
// calls Covers against timespan.Subtract(darkU, litU), where both arguments
// are Union's output. Subtract does not sort or merge its `from` argument —
// it only ever removes or splits each of its elements in place — so this
// property depends on darkU already being sorted and disjoint before Subtract
// sees it, which is exactly what Union guarantees and what Cluster passes.
// This test pins that chain with randomised input rather than one hand-picked
// case, because a search that trusts an unsorted or overlapping slice returns
// a wrong answer silently, and a single example cannot rule that out.
func TestSubtractOfUnionedSpansStaysSortedAndDisjoint(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	randomSpans := func(n int) []timespan.Span {
		out := make([]timespan.Span, n)
		for i := range out {
			start := rng.Intn(200)
			out[i] = span(start, start+rng.Intn(20))
		}
		return out
	}
	for trial := range 200 {
		from := timespan.Union(randomSpans(1 + rng.Intn(15)))
		cut := timespan.Union(randomSpans(1 + rng.Intn(15)))
		got := timespan.Subtract(from, cut)
		for i := 1; i < len(got); i++ {
			if got[i-1].Start.After(got[i].Start) {
				t.Fatalf("trial %d: Subtract's output is not sorted by Start: %s", trial, render(got))
			}
			if got[i-1].End.After(got[i].Start) {
				t.Fatalf("trial %d: Subtract's output overlaps: %s", trial, render(got))
			}
		}
	}
}

func TestIntersectIsTheTimeBothRunsClaim(t *testing.T) {
	got := timespan.Intersect(
		[]timespan.Span{span(0, 30), span(40, 60)},
		[]timespan.Span{span(20, 50)})
	want := []timespan.Span{span(20, 30), span(40, 50)}
	if !equal(got, want) {
		t.Errorf("Intersect gave %s, want %s", render(got), render(want))
	}
}

func TestIntersectIsEmptyWhenTheRunsOnlyTouch(t *testing.T) {
	got := timespan.Intersect([]timespan.Span{span(0, 30)}, []timespan.Span{span(30, 60)})
	if len(got) != 0 {
		t.Errorf("Intersect gave %s, want nothing", render(got))
	}
}

func TestCoversIsHalfOpen(t *testing.T) {
	run := []timespan.Span{span(10, 20)}
	for _, tc := range []struct {
		minute int
		want   bool
	}{{9, false}, {10, true}, {19, true}, {20, false}} {
		if got := timespan.Covers(run, at(tc.minute)); got != tc.want {
			t.Errorf("Covers(%s, +%dm) = %v, want %v", render(run), tc.minute, got, tc.want)
		}
	}
}

func TestAZeroLengthSpanCoversNothingAtAll(t *testing.T) {
	if timespan.Covers([]timespan.Span{span(10, 10)}, at(10)) {
		t.Error("a zero-length span was read as covering its own instant; it covers no time")
	}
}

// TestCoversFindsEachSpanInASortedDisjointRun exercises a run of more than one
// span, which the single-span cases above cannot: a search that only ever
// looks at spans[0] would still pass TestCoversIsHalfOpen. Every gap, every
// span, and the far side of the last span are queried so a search that stops
// at the wrong half of the slice has somewhere to be caught.
func TestCoversFindsEachSpanInASortedDisjointRun(t *testing.T) {
	run := []timespan.Span{span(0, 10), span(20, 30), span(40, 50)}
	for _, tc := range []struct {
		minute int
		want   bool
	}{
		{-1, false},  // before every span
		{0, true},    // start of the first span
		{5, true},    // inside the first span
		{9, true},    // last covered instant of the first span
		{10, false},  // end of the first span, half-open
		{15, false},  // the gap between the first and second spans
		{20, true},   // start of the second span
		{29, true},   // last covered instant of the second span
		{30, false},  // end of the second span
		{35, false},  // the gap between the second and third spans
		{40, true},   // start of the last span
		{49, true},   // last covered instant of the last span
		{50, false},  // end of the last span
		{100, false}, // well past every span
	} {
		if got := timespan.Covers(run, at(tc.minute)); got != tc.want {
			t.Errorf("Covers(%s, +%dm) = %v, want %v", render(run), tc.minute, got, tc.want)
		}
	}
}

func TestOverlapsCountsAPointTouchingASpan(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b timespan.Span
		want bool
	}{
		{"a point inside", span(15, 15), span(10, 20), true},
		{"a point on the end", span(20, 20), span(10, 20), true},
		{"a point past the end", span(21, 21), span(10, 20), false},
		{"two spans meeting", span(10, 20), span(20, 30), true},
		{"two spans apart", span(10, 20), span(21, 30), false},
	} {
		if got := timespan.Overlaps(tc.a, tc.b); got != tc.want {
			t.Errorf("Overlaps(%s) with %s: %v, want %v", tc.name, render([]timespan.Span{tc.a, tc.b}), got, tc.want)
		}
	}
}

func TestClipNarrowsToThePartInside(t *testing.T) {
	got, ok := timespan.Clip(span(0, 60), span(20, 30))
	if !ok || !equal([]timespan.Span{got}, []timespan.Span{span(20, 30)}) {
		t.Errorf("Clip gave %s (ok=%v), want +20m..+30m", render([]timespan.Span{got}), ok)
	}
	if _, ok := timespan.Clip(span(0, 10), span(20, 30)); ok {
		t.Error("Clip reported an overlap between two disjoint spans")
	}
}

func TestTotalSumsARun(t *testing.T) {
	got := timespan.Total([]timespan.Span{span(0, 10), span(30, 35), span(40, 40)})
	if want := 15 * time.Minute; got != want {
		t.Errorf("Total = %v, want %v", got, want)
	}
}

func TestDurationIsHowLongASpanRuns(t *testing.T) {
	if got, want := span(0, 90).Duration(), 90*time.Minute; got != want {
		t.Errorf("Duration = %v, want %v", got, want)
	}
}
