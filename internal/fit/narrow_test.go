package fit_test

import (
	"slices"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

func window(from, to time.Duration) []timespan.Span {
	return []timespan.Span{{Start: at(from), End: at(to)}}
}

// Reach is the largest separation across which two observations can end up in
// one sitting anywhere in the grid — which is the gap threshold OR the two
// margins, whichever is larger, because both merge.
func TestGridReachIsTheLargestDistanceThatCanMerge(t *testing.T) {
	byGap := fit.Grid{Gaps: mins(15, 45), LeadIns: mins(5, 20), TailOuts: mins(5, 10)}
	if got, want := byGap.Reach(), 45*min1; got != want {
		t.Errorf("Reach = %v, want %v: the largest gap is larger than the largest margin pair", got, want)
	}
	byMargin := fit.Grid{Gaps: mins(15), LeadIns: mins(20), TailOuts: mins(20)}
	if got, want := byMargin.Reach(), 40*min1; got != want {
		t.Errorf("Reach = %v, want %v: the margins merge further than the gap does", got, want)
	}
}

func TestNarrowKeepsEveryChainThatCanReachTheWindow(t *testing.T) {
	events := []source.Event{
		mark(-2 * time.Hour), // its own chain, far outside
		mark(-22 * min1),     // chains to the next one, 12m away
		mark(-10 * min1),
		mark(30 * min1), // inside
	}
	kept, n := fit.Narrow(events, window(0, time.Hour), min15, params(min15, min5, min5))

	if got, want := n.Read, 4; got != want {
		t.Errorf("Read = %d, want %d", got, want)
	}
	if got, want := n.Kept, 3; got != want {
		t.Errorf("Kept = %d, want %d: the chain reaching back to -22m must survive whole", got, want)
	}
	if got, want := n.Dropped, 1; got != want {
		t.Errorf("Dropped = %d, want %d", got, want)
	}
	for _, e := range kept {
		if e.Start.Equal(at(-2 * time.Hour)) {
			t.Error("the chain two hours away was kept; it cannot reach the window at any point of the grid")
		}
	}
	if !slices.ContainsFunc(kept, func(e source.Event) bool { return e.Start.Equal(at(-22 * min1)) }) {
		t.Error("the far end of the chain that reaches the window was dropped")
	}
}

// THE PROPERTY THE WHOLE NARROWING RESTS ON. Anything else is an optimisation
// that quietly changes the answer.
func TestNarrowDerivesTheSameSittingsInsideTheWindowAsTheWholeCorpus(t *testing.T) {
	var events []source.Event
	// A day inside the window, made of two clumps and a straggler, plus
	// activity a day either side that must not be allowed to influence it and
	// must not be needed to reproduce it.
	for _, off := range []time.Duration{
		-30 * time.Hour, -29 * time.Hour, -20 * min1, -8 * min1,
		5 * min1, 6 * min1, 40 * min1, 3 * time.Hour, 3*time.Hour + 2*min1,
		26 * time.Hour, 26*time.Hour + min1,
	} {
		events = append(events, mark(off))
	}
	events = append(events, lit(-time.Hour, 5*time.Hour), dark(time.Hour, 2*time.Hour),
		lit(-40*time.Hour, -30*time.Hour))

	w := window(0, 8*time.Hour)
	grid := fit.Grid{Gaps: mins(12, 20, 45), LeadIns: mins(0, 5, 10), TailOuts: mins(0, 1, 5)}
	kept, n := fit.Narrow(events, w, grid.Reach(), params(min15, min5, min5))
	if n.Kept == n.Read {
		t.Fatalf("nothing was narrowed away (%d of %d kept), so this test proves nothing", n.Kept, n.Read)
	}

	for _, p := range grid.Points() {
		pr := params(p.Gap, p.LeadIn, p.TailOut)
		full := clipSpans(cluster(t, events, pr), w)
		short := clipSpans(cluster(t, kept, pr), w)
		if !slices.Equal(full, short) {
			t.Errorf("%v: narrowed corpus derives %v inside the window, whole corpus derives %v",
				p, short, full)
		}
	}
}

// Presence subtracts and never adds, so a narrowing that dropped a dark reading
// overlapping a kept sitting would silently restore time the backlight said was
// not there.
func TestNarrowKeepsPresenceThatTrimsAKeptSitting(t *testing.T) {
	events := []source.Event{mark(min5), mark(90 * min1), dark(20*min1, 70*min1), dark(30*time.Hour, 31*time.Hour)}
	w := window(0, 2*time.Hour)
	kept, n := fit.Narrow(events, w, 30*min1, params(30*min1, min15, min15))

	if got, want := n.Presence, 1; got != want {
		t.Errorf("kept %d presence readings, want %d: the far one bounds nothing inside the window", got, want)
	}
	p := params(30*min1, min15, min15)
	if got, want := clipSpans(cluster(t, kept, p), w), clipSpans(cluster(t, events, p), w); !slices.Equal(got, want) {
		t.Errorf("narrowed derives %v, whole corpus derives %v", got, want)
	}
}

func TestNarrowCountsWhatItDroppedAndWhy(t *testing.T) {
	events := []source.Event{
		mark(min5),
		lit(0, time.Hour),
		{Start: at(min5), End: at(min5), Shape: source.Point, Source: "test-ignored"},
		mark(40 * time.Hour),
	}
	p := params(min15, min5, min5)
	p.IgnoreSources = []string{"test-ignored"}

	_, n := fit.Narrow(events, window(0, time.Hour), min15, p)
	if got, want := n.Read, 4; got != want {
		t.Errorf("Read = %d, want %d", got, want)
	}
	if got, want := n.Ignored, 1; got != want {
		t.Errorf("Ignored = %d, want %d", got, want)
	}
	if got, want := n.Evidence, 1; got != want {
		t.Errorf("Evidence = %d, want %d", got, want)
	}
	if got, want := n.Presence, 1; got != want {
		t.Errorf("Presence = %d, want %d", got, want)
	}
	if got, want := n.Dropped, 2; got != want {
		t.Errorf("Dropped = %d, want %d: one ignored and one out of reach", got, want)
	}
	if n.Read != n.Kept+n.Dropped {
		t.Errorf("%d read is not %d kept plus %d dropped", n.Read, n.Kept, n.Dropped)
	}
	if got, want := n.Reach, min15; got != want {
		t.Errorf("Reach = %v, want %v", got, want)
	}
}

// clipSpans renders a derivation clipped to the windows, which is the only part
// of it a narrowing promises anything about.
func clipSpans(derived []sittings.Sitting, windows []timespan.Span) []string {
	var spans []timespan.Span
	for _, s := range derived {
		for _, w := range windows {
			if c, ok := timespan.Clip(s.Span(), w); ok && c.Duration() > 0 {
				spans = append(spans, c)
			}
		}
	}
	out := make([]string, 0, len(spans))
	for _, s := range timespan.Union(spans) {
		out = append(out, "["+fit.Compact(s.Start.Sub(origin))+","+fit.Compact(s.End.Sub(origin))+"]")
	}
	return out
}
