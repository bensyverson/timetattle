package dayview_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// The reductions the blind census is stated in terms of, on the untagged side
// of the realdata tag because BOTH halves of that census need them: the
// synthetic corpus in synthetic_test.go and the live one in
// blind_realdata_test.go, which `go test ./...` does not compile.
//
// The split matters. When these lived beside the live cases, tagging the live
// file out would have taken the portable half down with it — the same shape as
// the failure the tag exists to end, arriving from the opposite direction.

// gridPoints is a spread across the range the fitter actually searches, from
// the smallest gap that has ever been considered to the largest.
func gridPoints(base sittings.Params) []sittings.Params {
	out := make([]sittings.Params, 0, 3)
	for _, c := range []struct{ gap, margin time.Duration }{
		{2 * time.Minute, 0},
		{15 * time.Minute, 5 * time.Minute},
		{time.Hour, 30 * time.Minute},
	} {
		p := base
		p.Gap, p.LeadIn, p.TailOut = c.gap, c.margin, c.margin
		out = append(out, p)
	}
	return out
}

// seams are the stretches BETWEEN consecutive sittings, stripped of the margins
// that manufactured them and clipped to the ones wholly inside the day.
func seams(t *testing.T, events []source.Event, p sittings.Params, day dayview.Day) []timespan.Span {
	t.Helper()
	res, err := sessions.Cluster(events, p)
	if err != nil {
		t.Fatalf("Cluster at gap %v: %v", p.Gap, err)
	}
	var out []timespan.Span
	for i := 1; i < len(res.Sessions); i++ {
		seam := timespan.Span{
			Start: res.Sessions[i-1].End.Add(-p.TailOut),
			End:   res.Sessions[i].Start.Add(p.LeadIn),
		}
		// A seam at the day's edge is clipped by the window and says nothing
		// about where a boundary belongs.
		if !seam.Start.After(day.Start) || !seam.End.Before(day.End) {
			continue
		}
		out = append(out, seam)
	}
	return out
}

func insideAny(s timespan.Span, spans []timespan.Span) bool {
	for _, g := range spans {
		if !s.Start.Before(g.Start) && !s.End.After(g.End) {
			return true
		}
	}
	return false
}

// coveredBy reports whether every instant of s that has any duration lies in
// one of spans. Adjacent silences count as one, which is what lets a seam span
// a run of them separated only by instantaneous observations.
func coveredBy(s timespan.Span, spans []timespan.Span) bool {
	var left time.Duration
	for _, g := range timespan.Subtract([]timespan.Span{s}, timespan.Union(spans)) {
		left += g.Duration()
	}
	return left == 0
}

// touchesAny allows a second of slack, because a presence reading is recorded
// to the second and a Biome interval to the microsecond.
func touchesAny(at time.Time, instants []time.Time) bool {
	for _, i := range instants {
		if d := at.Sub(i); d < time.Second && d > -time.Second {
			return true
		}
	}
	return false
}
