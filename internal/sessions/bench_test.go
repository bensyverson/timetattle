package sessions_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// runSizes are two lengths of ONE run, four times apart, so the ratio between
// the two results reads directly as the growth exponent: near 4x is linear,
// near 16x is quadratic. A single number cannot show scaling, which is why this
// benchmark is a pair and not a figure.
var runSizes = []int{2000, 8000}

// oneRun is n observations each a minute after the last, which under a
// fifteen-minute gap is a single unbroken run. Nothing else varies: same
// source, same instantaneous shape, no presence stream, so the only thing the
// two sizes change is how long the run being extended has grown.
//
// One long run is not a synthetic curiosity. It is what a working day looks
// like from a dense evidence source, and it is the shape that made a full
// `metrics` pass over the live store spend effectively all of its wall clock
// deciding whether the next observation joined the run it was already building.
func oneRun(n int) []source.Event {
	events := make([]source.Event, 0, n)
	for i := range n {
		events = append(events, point(time.Duration(i)*time.Minute))
	}
	return events
}

// BenchmarkClusterOneRun measures extending a single run. The assertion inside
// the loop is not decoration: a change that made the clustering cheap by
// breaking the run into pieces would otherwise benchmark beautifully and be
// wrong, so the benchmark refuses to report a number for input it did not
// cluster into one sitting.
func BenchmarkClusterOneRun(b *testing.B) {
	p := params()
	for _, n := range runSizes {
		events := oneRun(n)
		b.Run(fmt.Sprintf("observations=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				res, err := sessions.Cluster(events, p)
				if err != nil {
					b.Fatalf("Cluster: %v", err)
				}
				if len(res.Sessions) != 1 {
					b.Fatalf("expected one sitting from one run, got %d", len(res.Sessions))
				}
			}
		})
	}
}
