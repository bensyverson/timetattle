package timespan_test

import (
	"fmt"
	"testing"

	"github.com/bensyverson/timetattle/internal/timespan"
)

// coverSizes are two counts of disjoint spans, four times apart, so the ratio
// between the two results reads directly as the growth exponent: near 4x is
// linear, near log-growth (about 1.2x here) is logarithmic. A single number
// cannot show scaling, which is why this benchmark is a pair and not a figure,
// matching BenchmarkClusterOneRun in internal/sessions/bench_test.go.
var coverSizes = []int{2000, 8000}

// disjointSpans returns n one-minute spans separated by one-minute gaps,
// sorted ascending — the shape Union and Subtract hand back, and the shape
// internal/sessions.Cluster queries once per evidence observation against the
// absent-span set.
func disjointSpans(n int) []timespan.Span {
	spans := make([]timespan.Span, n)
	for i := range n {
		spans[i] = span(i*2, i*2+1)
	}
	return spans
}

// BenchmarkCovers queries a point after every span in the run. A linear scan
// cannot short-circuit on a miss — it walks the whole slice before returning
// false — so a query that is never covered is the scan's worst case and the
// case that dominates internal/sessions.Cluster's real workload, where most
// evidence observations do NOT land in observed darkness.
func BenchmarkCovers(b *testing.B) {
	for _, n := range coverSizes {
		spans := disjointSpans(n)
		notCovered := at(n * 2) // one minute past the last span's start, in its gap
		b.Run(fmt.Sprintf("spans=%d/miss", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if timespan.Covers(spans, notCovered) {
					b.Fatal("expected the query point to fall in the trailing gap, not a span")
				}
			}
		})
	}
}
