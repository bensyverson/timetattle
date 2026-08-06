//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -count=1 -tags realdata ./...
//
// -count=1 is not optional. Go's test cache keys on file mtime and size, and a
// Biome segment has changed content without changing either; a green tagged run
// without it is not evidence. docs/content/docs/testing.md says what each of
// the two runs buys.

package metrics_test

import (
	"testing"
	"time"
)

// The series against real evidence.
//
// THESE CANNOT ESTABLISH THE RULE AND ARE NOT MEANT TO. The boundary-crossing
// case is what a series gets wrong, and the working-day boundary is chosen to
// sit in the trough of this corpus precisely so that nothing crosses it: at the
// shipped 05:00 boundary, ZERO of the live sittings cross a day, so a mutant
// that counted a crossing segment whole in both periods produces byte-identical
// output to the correct one over the whole fortnight. The constructed case in
// series_test.go is what holds that rule; this is what establishes that the
// arithmetic survives a hundred thousand real segments, which a fixture cannot.
//
// Measured 2026-08-04, `go run ./cmd/timetattle metrics -from 2026-07-21T00:00:00Z
// -to 2026-08-04T00:00:00Z -by day`: the per-day rows reconcile against the
// ungrouped total with a difference of exactly 0s, and every day agrees with
// `timetattle sessionize -dry-run -by-day` over the same span to the second.
// See project/private/2026-08-04-a-series-that-is-a-re-slice.md.

// hourlyEdges cuts the report's own presence at every hour, which makes far
// more crossings than any calendar would and is the point: if the split is
// wrong, this is where it shows.
func hourlyEdges(t *testing.T, first, last time.Time) []time.Time {
	t.Helper()
	start := first.Truncate(time.Hour)
	var out []time.Time
	for at := start; !at.After(last); at = at.Add(time.Hour) {
		out = append(out, at)
	}
	return append(out, out[len(out)-1].Add(time.Hour))
}

// THE CONSERVATION PROPERTY, ON REAL SEGMENTS. Cut at every hour of the day,
// the periods sum to the report exactly — presence, and each project's absolute
// engaged time, and each client's.
func TestASeriesOverRealEvidenceSumsToTheReportExactly(t *testing.T) {
	r := concurrentDay(t)
	if len(r.Segments) == 0 {
		t.Skip("the live store holds no segments for 2026-07-28")
	}

	series, err := r.Series(hourlyEdges(t, r.Segments[0].Start, r.Segments[len(r.Segments)-1].End))
	if err != nil {
		t.Fatalf("Series over the live day: %v", err)
	}

	var presence time.Duration
	byProject := map[string]time.Duration{}
	byClient := map[string]time.Duration{}
	for _, p := range series {
		presence += p.Presence.Total
		for _, s := range p.Projects {
			byProject[s.Name] += s.Engaged.Total
		}
		for _, s := range p.Clients {
			byClient[s.Name] += s.Engaged.Total
		}
	}
	if presence != r.Presence.Total {
		t.Errorf("the hourly periods cover %v of presence against the report's %v, off by %v",
			presence, r.Presence.Total, presence-r.Presence.Total)
	}
	for _, p := range r.Projects {
		if got := byProject[p.Name]; got != p.Engaged.Total {
			t.Errorf("a project is engaged %v across the hours and %v across the day, off by %v",
				got, p.Engaged.Total, got-p.Engaged.Total)
		}
	}
	for _, c := range r.Clients {
		if got := byClient[c.Name]; got != c.Engaged.Total {
			t.Errorf("a client is engaged %v across the hours and %v across the day, off by %v",
				got, c.Engaged.Total, got-c.Engaged.Total)
		}
	}
}

// A PERIOD CAN NEVER HOLD MORE CLOCK THAN IT IS LONG. This is the property
// counting a crossing segment twice destroys, and it is asserted against real
// segments rather than only against the two-day fixture.
func TestNoRealPeriodHoldsMorePresenceThanItsOwnLength(t *testing.T) {
	r := concurrentDay(t)
	if len(r.Segments) == 0 {
		t.Skip("the live store holds no segments for 2026-07-28")
	}

	series, err := r.Series(hourlyEdges(t, r.Segments[0].Start, r.Segments[len(r.Segments)-1].End))
	if err != nil {
		t.Fatalf("Series over the live day: %v", err)
	}
	crossed := 0
	for _, p := range series {
		if p.Presence.Total > p.Length() {
			t.Errorf("the period %v..%v holds %v of presence across %v of clock",
				p.Start, p.End, p.Presence.Total, p.Length())
		}
		if p.Presence.Total > 0 && p.Presence.Total < p.Length() {
			crossed++
		}
	}
	// A day cut at every hour must produce partly-covered hours, or the cut
	// found nothing to cut and this test asserted nothing at all.
	if crossed == 0 {
		t.Error("no hour of the live day is partly covered, so the split was never exercised; " +
			"this test is not measuring what it claims to")
	}
}
