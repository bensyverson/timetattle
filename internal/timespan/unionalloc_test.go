package timespan_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/timespan"
)

// This file guards what Union costs when it is handed a run that is ALREADY a
// union — sorted, and with no two spans touching or overlapping.
//
// That is not a corner case, it is the common one. Subtract unions its cut
// argument on every call, and the cut it is given is almost always something a
// previous Union or Subtract produced: internal/sessions.Cluster subtracts the
// same run of observed darkness once for every cluster it has built, and that
// run came out of Subtract, which came out of Union. Re-deriving a union that
// already holds means a sort and a rebuilt output run, thousands of times per
// metrics pass, for a result identical to the input.
//
// The property asserted is that the already-unioned case COPIES and does
// nothing else. It is stated as an allocation count rather than a time because
// a count is stable across machines and a time is not.

// disjointRun is n one-minute spans two minutes apart: sorted, and with a gap
// between every pair, so it is exactly what Union would have produced from it.
func disjointRun(n int) []timespan.Span {
	base := time.Date(2025, time.March, 3, 9, 0, 0, 0, time.UTC)
	out := make([]timespan.Span, 0, n)
	for i := range n {
		start := base.Add(time.Duration(2*i) * time.Minute)
		out = append(out, timespan.Span{Start: start, End: start.Add(time.Minute)})
	}
	return out
}

func TestUnionOfARunThatIsAlreadyAUnionOnlyCopiesIt(t *testing.T) {
	const n = 2000
	run := disjointRun(n)

	// The answer must not change, whatever the cost of reaching it.
	got := timespan.Union(run)
	if len(got) != n {
		t.Fatalf("Union merged a run with a gap between every pair: %d spans in, %d out", n, len(got))
	}
	for i := range got {
		if !got[i].Start.Equal(run[i].Start) || !got[i].End.Equal(run[i].End) {
			t.Fatalf("span %d came back as %v..%v, expected %v..%v",
				i, got[i].Start, got[i].End, run[i].Start, run[i].End)
		}
	}
	if &got[0] == &run[0] {
		t.Fatal("Union returned the caller's own array; nothing this package returns may alias what it was passed")
	}

	allocs := testing.AllocsPerRun(5, func() {
		timespan.Union(run)
	})
	t.Logf("Union over %d already-disjoint spans: %.0f allocations", n, allocs)

	// One for the copy. The headroom is a single allocation, not a
	// proportion, because there is nothing else here that should scale: a
	// sort's closure and a rebuilt output run are precisely what this case
	// must not pay for.
	const ceiling = 2.0
	if allocs > ceiling {
		t.Errorf("Union allocated %.0f times over a run that was already a union; the copy is the only "+
			"allocation this case has any use for, and anything beyond %.0f means it is sorting and "+
			"rebuilding a run identical to the one it was given", allocs, ceiling)
	}
}
