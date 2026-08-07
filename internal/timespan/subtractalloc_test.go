package timespan_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/timespan"
)

// This file guards Subtract's ALLOCATION SHAPE, which is a different property
// from what it returns and is not covered by any test that only checks spans.
//
// Subtract is called once per project and once per client on every metrics
// pass, over a run of observed darkness that on a real corpus is thousands of
// spans long. Until 2026-08-07 it allocated a whole new run for every cut it
// applied, so the number of allocations was the number of cuts — the single
// largest allocating line in the clustering pipeline, and one no correctness
// test could see.
//
// # Why the assertion is a pair and not a number
//
// A fixed ceiling would have to be re-tuned every time the growth policy of
// append changes, and it would say nothing about the property that actually
// matters. What matters is SCALING: allocations must not grow in proportion to
// the cuts. Two sizes four times apart make the growth exponent readable
// directly, which is the convention BenchmarkClusterOneRun and BenchmarkCovers
// already use in this repository.

// cutSizes are two counts of cuts, four times apart. A shape that allocates per
// cut grows by 4x between them; a shape that reuses its buffers does not.
var cutSizes = []int{2000, 8000}

// combToSubtract is one long span cut by n disjoint one-minute holes, spaced
// two minutes apart, so every cut lands strictly inside the run and splits it.
// That is the worst case for the per-cut shape and the ordinary case for
// clustering, where observed darkness is a long run of short holes.
func combToSubtract(n int) (from, cut []timespan.Span) {
	base := time.Date(2025, time.March, 3, 9, 0, 0, 0, time.UTC)
	from = []timespan.Span{{Start: base, End: base.Add(time.Duration(2*n+2) * time.Minute)}}
	cut = make([]timespan.Span, 0, n)
	for i := range n {
		start := base.Add(time.Duration(2*i+1) * time.Minute)
		cut = append(cut, timespan.Span{Start: start, End: start.Add(time.Minute)})
	}
	return from, cut
}

func TestSubtractAllocationsDoNotScaleWithTheNumberOfCuts(t *testing.T) {
	allocs := make([]float64, len(cutSizes))
	for i, n := range cutSizes {
		from, cut := combToSubtract(n)
		// Run once first, outside the measurement, so that nothing the first
		// call warms is counted against it.
		if got := len(timespan.Subtract(from, cut)); got != n+1 {
			t.Fatalf("%d cuts through one span left %d pieces, expected %d", n, got, n+1)
		}
		allocs[i] = testing.AllocsPerRun(3, func() {
			timespan.Subtract(from, cut)
		})
	}

	growth := allocs[1] / allocs[0]
	t.Logf("%d cuts: %.0f allocations; %d cuts: %.0f allocations; growth %.2fx for 4x the cuts",
		cutSizes[0], allocs[0], cutSizes[1], allocs[1], growth)

	// 4x is the per-cut shape this replaced: one fresh run allocated for every
	// cut applied. Reusing two buffers leaves only the buffers' own growth,
	// which is logarithmic in the size of the result. The threshold sits well
	// below 4 and well above 1 so that neither ordinary append growth nor a
	// change of growth policy can fail it, while a return to allocating per
	// cut cannot pass it.
	const linear = 4.0
	if growth >= linear/2 {
		t.Errorf("allocations grew %.2fx for 4x the cuts (%.0f to %.0f); anything approaching %.1fx "+
			"means Subtract is allocating a new run per cut rather than reusing its buffers",
			growth, allocs[0], allocs[1], linear)
	}
}
