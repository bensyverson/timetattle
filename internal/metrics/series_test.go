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

// A BUCKET'S CLIENT CAN EXCEED THE SUM OF ITS PROJECTS INSIDE THAT BUCKET, AND
// THE WHOLE-SPAN REPORT IT WAS SLICED FROM NEED NOT.
//
// This file used to assert the opposite, as
// TestABucketsClientIsTheUnionOfItsProjectsAndNeverTheirSum. That claim is
// false, and it passed only because its fixture worked one project after the
// other rather than alternately — dormant rather than correct. A client's hours
// are that client's OWN evidence re-clustered, not a merge of its projects'
// spans, so a silence too wide for any one project to bridge is narrow enough to
// bridge once the client's evidence is taken together. The surplus is the cost
// of switching between two of one client's projects, which neither project's own
// evidence records. See project/2026-08-13-rollup-reclustering-resolved.md for
// the decision and internal/metrics/rollupcost_test.go for the same corpus one
// level up; on the live store the surplus is 19m13s of an unwindowed 617h40m54s,
// reproduced by `go run ./scripts/rollupunion`.
//
// A BUCKET IS FALSER THAN THE REPORT, not merely as false, and that is why this
// case slices where it does. What the removed assertion compared against is the
// projects' SUM, and the sum is inflated by every instant two of the client's
// projects run at once. Cutting that concurrency into a different bucket takes
// the inflation away, so the corpus below has a report comfortably INSIDE its
// projects' sum and a bucket of that same report outside it. A check that reads
// clean on a span therefore says nothing whatever about the buckets of it.
func TestABucketsClientCanExceedTheSumOfItsProjectsWhenTheyInterleave(t *testing.T) {
	// Alternating every 12 minutes puts each project's own observations 24 apart
	// — beyond the 15-minute gap, so neither project bridges its own silence —
	// while the client's are 12 apart throughout and bridge all of it. The pair
	// at 12:00 is one 10-minute sitting both projects share, counted ONCE for
	// the client and TWICE by the sum. It is the same corpus rollupunion_test.go
	// carries, and the same figures: client 56m, projects summing to 60m.
	report := compute(t, []source.Event{
		clientPoint(at(9, 0), "Northgate", "acorn"),
		clientPoint(at(9, 12), "Northgate", "birch"),
		clientPoint(at(9, 24), "Northgate", "acorn"),
		clientPoint(at(9, 36), "Northgate", "birch"),
		clientPoint(at(12, 0), "Northgate", "acorn"),
		clientPoint(at(12, 0), "Northgate", "birch"),
	})

	// The report is WITHIN its projects' sum, which is what the removed
	// assertion demanded. Asserted rather than assumed: if this corpus ever
	// stopped being one the old belief is comfortable with, the split below
	// would stop being the finding it is written to be.
	whole := clientOf(t, report, "Northgate")
	var wholeSummed time.Duration
	for _, name := range whole.Projects {
		wholeSummed += projectOf(t, report, name).Engaged.Total
	}
	if want := 56 * time.Minute; whole.Engaged.Total != want {
		t.Errorf("the report credits the client %v, want %v", whole.Engaged.Total, want)
	}
	if want := 60 * time.Minute; wholeSummed != want {
		t.Errorf("the report's projects sum to %v, want %v", wholeSummed, want)
	}
	if whole.Engaged.Total > wholeSummed {
		t.Fatalf("the report already exceeds its projects' sum by %v, so the buckets below cannot "+
			"show that a slice is falser than the span it came from",
			whole.Engaged.Total-wholeSummed)
	}

	// Cut at 11:00, between the alternation and the concurrent pair, so that one
	// bucket holds the interleaving with none of the overlap that was masking it.
	series, err := report.Series([]time.Time{at(0, 0), at(11, 0), at(24, 0)})
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	c, ok := series[0].Client("Northgate")
	if !ok {
		t.Fatal("the first bucket names no client at all")
	}
	var summed time.Duration
	for _, s := range series[0].Projects {
		summed += s.Engaged.Total
	}
	// 08:55 to 09:41 in one client sitting; each project holds two 10-minute
	// sittings of its own inside it.
	if want := 46 * time.Minute; c.Engaged.Total != want {
		t.Errorf("the first bucket credits the client %v, want %v", c.Engaged.Total, want)
	}
	if want := 40 * time.Minute; summed != want {
		t.Errorf("the first bucket's projects sum to %v, want %v", summed, want)
	}
	if got, want := c.Engaged.Total-summed, 6*time.Minute; got != want {
		t.Errorf("the first bucket's client exceeds its projects' sum by %v, want %v; that surplus "+
			"is the switching cost neither project can see, and a roll-up that merged the project "+
			"spans would read 0", got, want)
	}
}

// THE INVARIANT A BUCKET DOES CARRY, and it is the one the whole-span report
// carries: A CLIENT OWNING EXACTLY ONE PROJECT NEVER EXCEEDS THAT PROJECT,
// INSIDE EVERY BUCKET AS WELL AS ACROSS THE SPAN. The client is that project's
// own evidence clustered again under the same parameters, so its spans sit
// inside the project's and every segment the client is active in is a segment
// the project is active in too.
//
// A BUCKET EDGE CANNOT DIVIDE THE TWO LEVELS DIFFERENTLY, which is the thing
// worth checking here and the reason this case straddles one. A series is a
// re-slice and never a re-computation: a bucket is summed from Report.Segments,
// and each segment carries BOTH its projects and its clients, so the clip an
// edge makes — the overlap of one segment with one bucket — is a single duration
// booked to both levels in the same call. There is no second cursor to fall out
// of step with the first, and a boundary therefore splits a client and its
// project by construction identically. The second bucket below is that fact
// measured: 10m against 10m, both sides of one cut sitting.
//
// IT IS AN INEQUALITY AND NEVER AN EQUALITY, for the reason Compute's own check
// is one. A project is stamped with its client per observation from a log with
// effective dates, so a directory registered partway through its life has
// earlier evidence that is the project's and no client's — the report calls it
// Clientless. The first bucket below is that shape, at 10m against 25m, and an
// equality would refuse it. See
// TestAOneProjectClientHoldsLessThanItsProjectWhenTheClientResolvedLate.
//
// checkSeries ASSERTS THIS TOO, and the neighbouring refusal test is what pins
// that it can fire at all. The two are not redundant: this one measures the
// figures a correct slice produces, and that one measures what an incorrect one
// is met with.
func TestABucketNeverCreditsAOneProjectClientMoreThanThatProject(t *testing.T) {
	report := compute(t, []source.Event{
		// Before the registration: the project resolves and no client does.
		point(at(9, 0), "acorn"),
		point(at(9, 5), "acorn"),
		// After it: the same project, stamped with its client, in a sitting that
		// runs 23:50..00:10 and is cut in half by the bucket edge at midnight.
		clientPoint(at(23, 55), "Northgate", "acorn"),
		clientPoint(at(24, 5), "Northgate", "acorn"),
	})
	c := clientOf(t, report, "Northgate")
	if len(c.Projects) != 1 {
		t.Fatalf("the client owns %v, and this case is only about a client with exactly one project",
			c.Projects)
	}

	series, err := report.Series(edges(2))
	if err != nil {
		t.Fatalf("Series: %v", err)
	}
	var strictlyLess, cut int
	for i, b := range series {
		client, held := b.Client("Northgate")
		if !held {
			continue
		}
		project, ok := b.Project("acorn")
		if !ok {
			t.Fatalf("bucket %d credits the client %v and names its only project not at all",
				i, client.Engaged.Total)
		}
		switch {
		case client.Engaged.Total > project.Engaged.Total:
			t.Errorf("bucket %d credits the client %v against the %v held by its only project; the "+
				"two are the same evidence clustered twice and this cannot happen",
				i, client.Engaged.Total, project.Engaged.Total)
		case client.Engaged.Total < project.Engaged.Total:
			strictlyLess++
		default:
			cut++
		}
	}
	// The two shapes the case exists for, pinned by figure rather than left to
	// whichever the corpus happened to produce. Bucket 0 holds 08:55..09:10
	// before the registration and 23:50..00:00 after it, so the project has 25m
	// and the client 10m of it; bucket 1 holds the other half of the cut
	// sitting, where the edge gave both levels the same 10m.
	if want := 15 * time.Minute; series[0].Clientless != want {
		t.Errorf("the first bucket holds %v the project owns and no client does, want %v; the clock "+
			"a one-project client does not hold has to land somewhere that says what it is",
			series[0].Clientless, want)
	}
	for i, want := range []time.Duration{10 * time.Minute, 10 * time.Minute} {
		client, _ := series[i].Client("Northgate")
		if client.Engaged.Total != want {
			t.Errorf("bucket %d credits the client %v, want %v", i, client.Engaged.Total, want)
		}
	}
	for i, want := range []time.Duration{25 * time.Minute, 10 * time.Minute} {
		project, _ := series[i].Project("acorn")
		if project.Engaged.Total != want {
			t.Errorf("bucket %d credits the project %v, want %v", i, project.Engaged.Total, want)
		}
	}
	if strictlyLess == 0 {
		t.Error("no bucket holds a client with LESS than its only project, so this corpus never " +
			"exercised the direction that makes the relation an inequality")
	}
	if cut == 0 {
		t.Error("no bucket splits the client and its project equally across an edge, so the " +
			"boundary this case exists to cut was never cut")
	}
}

// THE SAME RELATION, ASSERTED BY THE CODE RATHER THAN ONLY BY THE TEST ABOVE.
//
// A DESYNC BETWEEN THE TWO LEVELS IS INVISIBLE TO EVERY OTHER CHECK HERE, which
// is why this one is worth its cost, and that was established by mutation rather
// than believed. Booking a segment's client share into the NEXT bucket while its
// project share stays put — one cursor a bucket ahead of the other, which is the
// failure checkSeries' own comment names — conserves each client's total across
// the series exactly, so the report-against-buckets equality reads clean. Under
// that mutation, with the check taken out, the whole cold suite passed except
// the two cases above — which named a bucket crediting a one-project client
// 20m0s against the 10m0s its only project held — while
// `timetattle metrics -all -by day -csv` over the live store exited 0 and put 297
// client rows on the wrong day without a word.
//
// COMPUTE CANNOT PRODUCE THE REPORT BELOW, and that is the point of writing it
// by hand. A client's events are exactly its projects' events that resolved to
// it, so a one-project client's evidence is a subset of that project's; the same
// clustering and the same presence then put its spans inside the project's, and
// the correction layer preserves it (an assertion naming the project moves the
// same span at both levels, and one naming a contested project moves the project
// and no client, which is the safe direction). So no segment Compute emits can
// carry a one-project client without its project. This report carries one
// anyway, to prove the refusal is reachable at all: a guard that no input can
// trip is indistinguishable from a comment.
func TestSeriesRefusesABucketCreditingAOneProjectClientMoreThanItsOnlyProject(t *testing.T) {
	// Ten minutes the client is active and its project is not, followed by ten
	// the project is active and the client is not. Every other figure closes, so
	// the containment check is the only thing that can object.
	r := metrics.Report{
		Presence:   metrics.Span{Total: 20 * time.Minute},
		Clientless: 10 * time.Minute,
		Unattributed: metrics.Unattributed{
			Presence: metrics.Span{Total: 10 * time.Minute},
			Bridged:  10 * time.Minute,
		},
		Projects: []metrics.Project{{
			Name:      "acorn",
			Engaged:   metrics.Span{Total: 10 * time.Minute},
			Allocated: 10 * time.Minute,
		}},
		Clients: []metrics.Client{{
			Name:      "Northgate",
			Projects:  []string{"acorn"},
			Engaged:   metrics.Span{Total: 10 * time.Minute},
			Allocated: 10 * time.Minute,
		}},
		Segments: []metrics.Segment{
			{Start: at(0, 0), End: at(0, 10), Clients: []string{"Northgate"}},
			{Start: at(0, 10), End: at(0, 20), Projects: []string{"acorn"}},
		},
	}

	if _, err := r.Series([]time.Time{at(0, 0), at(0, 10), at(0, 20)}); err == nil {
		t.Fatal("Series returned a bucket crediting a client 10m against the 0s its only project " +
			"holds there; the two are the same evidence clustered twice, so it must refuse")
	}

	// AND IT REFUSES ONLY THAT. The same report sliced whole is the shape a
	// one-project client legitimately takes — the client inside its project
	// across the span — and must still produce a series, or the check is
	// refusing the ordinary case rather than the impossible one.
	r.Segments[0].Projects = []string{"acorn"}
	r.Projects[0].Engaged.Total = 20 * time.Minute
	r.Projects[0].Allocated = 20 * time.Minute
	r.Clientless = 10 * time.Minute
	r.Unattributed = metrics.Unattributed{}
	if _, err := r.Series([]time.Time{at(0, 0), at(0, 10), at(0, 20)}); err != nil {
		t.Fatalf("Series refused a client sitting INSIDE its only project in every bucket: %v", err)
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
