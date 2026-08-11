package metrics_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// Constructed cases for the series. They sit beside realdata_test.go rather
// than in place of it, and the boundary-crossing case is the reason: the
// working-day boundary sits in the trough of the corpus on purpose, so ZERO of
// the live sittings cross it. A rule about crossings cannot be established by
// data that never exercises it, which is the failure this project has already
// shipped twice.

// edges builds the boundaries of consecutive whole days from the test day.
func edges(n int) []time.Time {
	out := make([]time.Time, 0, n+1)
	for i := 0; i <= n; i++ {
		out = append(out, at(24*i, 0))
	}
	return out
}

// A SERIES IS A RE-SLICE AND NEVER A RE-COMPUTATION, so what the buckets are
// summed from is the same partition every figure in the report is summed from.
// The consequence is that they conserve exactly rather than approximately, and
// that is what this asserts.
func TestASeriesSumsToTheReportItWasSlicedFrom(t *testing.T) {
	report := compute(t, []source.Event{
		point(at(9, 0), "acorn"),
		point(at(9, 10), "acorn"),
		point(at(9, 5), "birch"),
		point(at(30, 0), "acorn"),
		unlocatable(at(14, 0), at(15, 0)),
	})

	series, err := report.Series(edges(3))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}

	var presence, observed, unattributed time.Duration
	byProject := map[string]time.Duration{}
	for _, b := range series {
		presence += b.Presence.Total
		observed += b.Presence.Observed
		unattributed += b.Unattributed.Total
		for _, s := range b.Projects {
			byProject[s.Name] += s.Engaged.Total
		}
	}
	if presence != report.Presence.Total {
		t.Errorf("the buckets cover %v of presence, want %v", presence, report.Presence.Total)
	}
	if observed != report.Presence.Observed {
		t.Errorf("the buckets observed %v, want %v", observed, report.Presence.Observed)
	}
	if unattributed != report.Unattributed.Presence.Total {
		t.Errorf("the buckets leave %v unattributed, want %v", unattributed,
			report.Unattributed.Presence.Total)
	}
	for _, p := range report.Projects {
		if got := byProject[p.Name]; got != p.Engaged.Total {
			t.Errorf("%s is engaged %v across the buckets, want %v", p.Name, got, p.Engaged.Total)
		}
	}
}

// THE CASE THE LIVE CORPUS CANNOT MAKE. A sitting that crosses a bucket
// boundary is SPLIT at it, each side counted once, exactly as
// sessions.Calendar already splits a sitting across a working day. Counting it
// whole in both buckets would credit the pair with more clock than elapsed, and
// no reader could tell from the column.
func TestASittingCrossingABoundaryIsSplitAtItAndCountedOnceOnEachSide(t *testing.T) {
	report := compute(t, []source.Event{point(at(23, 55), "acorn"), point(at(24, 5), "acorn")})

	series, err := report.Series(edges(2))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("Series gave %d buckets, want 2", len(series))
	}
	// Lead-in 5m before 23:55 and tail-out 5m after 00:05, so the sitting runs
	// 23:50..00:10 and midnight cuts it in half.
	if want := 10 * time.Minute; series[0].Presence.Total != want {
		t.Errorf("the first bucket holds %v of presence, want %v", series[0].Presence.Total, want)
	}
	if want := 10 * time.Minute; series[1].Presence.Total != want {
		t.Errorf("the second bucket holds %v of presence, want %v", series[1].Presence.Total, want)
	}
	for i, b := range series {
		s, ok := b.Project("acorn")
		if !ok {
			t.Fatalf("bucket %d does not name acorn at all", i)
		}
		if want := 10 * time.Minute; s.Engaged.Total != want {
			t.Errorf("bucket %d credits acorn %v, want %v", i, s.Engaged.Total, want)
		}
	}
}

// ABSENCE OF A ROW IS NOT AN OBSERVATION. A bucket inside the span that nothing
// fell in is still a bucket, carrying a measured zero, because a caller that
// received nothing could not tell "no work" from "no bucket".
func TestABucketNothingFellInIsStillReported(t *testing.T) {
	report := compute(t, []source.Event{point(at(9, 0), "acorn"), point(at(56, 0), "acorn")})

	series, err := report.Series(edges(3))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	if len(series) != 3 {
		t.Fatalf("Series gave %d buckets, want 3", len(series))
	}
	if series[1].Presence.Total != 0 {
		t.Errorf("the middle bucket holds %v of presence, want none", series[1].Presence.Total)
	}
	if len(series[1].Projects) != 0 {
		t.Errorf("the middle bucket names %v, want no project at all", series[1].Projects)
	}
	if !series[1].Start.Equal(at(24, 0)) || !series[1].End.Equal(at(48, 0)) {
		t.Errorf("the middle bucket covers %v..%v, want %v..%v",
			series[1].Start, series[1].End, at(24, 0), at(48, 0))
	}
}

// ALLOCATION'S DEFINING PROPERTY, ASSERTED PER BUCKET. The shares of one bucket
// sum to exactly the presence THAT BUCKET was split from, to the nanosecond. A
// series that divided the whole span and then apportioned the result would fail
// this by an amount too small to see.
func TestEachBucketsSharesSumToItsOwnPresence(t *testing.T) {
	report := compute(t, []source.Event{
		point(at(9, 0), "acorn"),
		point(at(9, 1), "birch"),
		point(at(9, 2), "cypress"),
		point(at(30, 0), "acorn"),
		point(at(30, 1), "birch"),
	})

	series, err := report.Series(edges(2))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	for i, b := range series {
		var allocated time.Duration
		for _, s := range b.Projects {
			allocated += s.Allocated
		}
		if got := allocated + b.Unattributed.Total; got != b.Presence.Total {
			t.Errorf("bucket %d allocated %v plus %v unattributed = %v, want its %v of presence",
				i, allocated, b.Unattributed.Total, got, b.Presence.Total)
		}
	}
}

// A series whose edges do not reach the segments would drop clock silently,
// which is a report that is quietly short of an hour and reads as complete. It
// is refused instead, naming what fell outside.
func TestSeriesRefusesEdgesThatDoNotCoverTheReport(t *testing.T) {
	report := compute(t, []source.Event{point(at(9, 0), "acorn"), point(at(30, 0), "acorn")})

	if _, err := report.Series([]time.Time{at(0, 0), at(24, 0)}); err == nil {
		t.Fatal("Series accepted edges that stop before the last segment; it must refuse them")
	}
}

// Two edges at the same instant make a bucket covering no clock, and an
// out-of-order pair makes one covering negative clock. Both are caller bugs
// that would produce a plausible series, so both are refused.
func TestSeriesRefusesEdgesThatDoNotIncrease(t *testing.T) {
	report := compute(t, []source.Event{point(at(9, 0), "acorn")})

	for _, e := range [][]time.Time{
		{at(0, 0)},
		{at(0, 0), at(12, 0), at(12, 0), at(24, 0)},
		{at(0, 0), at(24, 0), at(12, 0), at(48, 0)},
	} {
		if _, err := report.Series(e); err == nil {
			t.Errorf("Series accepted the edges %v; it must refuse them", e)
		}
	}
}

// The client roll-up is sliced by the same cut as the projects, and a bucket's
// client hours are the union of its projects' inside that bucket rather than
// their sum — the same property the whole-span report asserts, one slice down.
func TestABucketsClientIsTheUnionOfItsProjectsAndNeverTheirSum(t *testing.T) {
	report := compute(t, []source.Event{
		clientPoint(at(9, 0), "Northgate", "acorn"),
		clientPoint(at(9, 1), "Northgate", "birch"),
		clientPoint(at(30, 0), "Northgate", "acorn"),
	})

	series, err := report.Series(edges(2))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	for i, b := range series {
		c, ok := b.Client("Northgate")
		if !ok {
			t.Fatalf("bucket %d names no client at all", i)
		}
		var summed time.Duration
		for _, s := range b.Projects {
			summed += s.Engaged.Total
		}
		if c.Engaged.Total > summed {
			t.Errorf("bucket %d credits the client %v across projects summing to %v; a roll-up is a "+
				"union and can never exceed the sum", i, c.Engaged.Total, summed)
		}
		if c.Engaged.Total == 0 {
			t.Errorf("bucket %d credits the client nothing, and both its projects were active", i)
		}
	}
}

// A SLICE MUST NOT LOSE A REASON THE REPORT KEEPS. The whole-span report gives
// presence a person said belonged to nobody its own bucket, distinct from
// silence the gap threshold bridged; a series that folded the two would report
// a person's own accounting as clock nothing was recorded in — the exact
// collapse this project has shipped in three separate tools, arriving through
// the re-slice rather than through a source.
func TestASeriesKeepsAssertedUnattributedApartFromBridgedSilence(t *testing.T) {
	report := corrected(t, oneSitting("acorn"),
		[]metrics.Attribution{said(10, 0, 10, 30, "")})

	series, err := report.Series(edges(1))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	var asserted, bridged, unattributed time.Duration
	for _, b := range series {
		asserted += b.Asserted
		bridged += b.Bridged
		unattributed += b.Unattributed.Total
	}
	if asserted != report.Unattributed.Asserted {
		t.Errorf("the buckets carry %v asserted against %v in the report", asserted,
			report.Unattributed.Asserted)
	}
	if asserted == 0 {
		t.Fatal("the fixture asserted nothing; the test measures nothing")
	}
	if bridged != report.Unattributed.Bridged {
		t.Errorf("the buckets carry %v of bridged silence against %v in the report; a person's "+
			"assertion has been folded into clock nobody recorded", bridged,
			report.Unattributed.Bridged)
	}
	if got := asserted + bridged; got > unattributed {
		t.Errorf("the reasons come to %v of %v unattributed; one stretch is counted twice",
			got, unattributed)
	}
}
