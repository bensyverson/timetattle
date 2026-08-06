package metrics_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// Constructed cases. They sit beside realdata_test.go rather than in place of
// it: real data can validate a wrong rule — an over-general supersession rule
// once passed against the whole live Biome stream purely because nothing
// happened to trigger it — and a fixture cannot establish that a rule survives
// 800,000 observations. Neither substitutes for the other.

var day = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)

func at(h, m int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// testParams are the shipped defaults, so that a figure here can be compared
// with one from `timetattle sessionize` without translating first.
func testParams() sessions.Params {
	return sessions.Params{
		Gap:             15 * time.Minute,
		LeadIn:          5 * time.Minute,
		TailOut:         5 * time.Minute,
		PresenceSources: []string{"knowledgec-backlit"},
	}
}

// point is an attributed instant: a Claude message, a commit, a file save.
// Every source that can be attributed to a project emits these and nothing
// else, which is why margin matters so much here.
func point(when time.Time, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Project: project}, "claude-transcripts")
	e.Locator = "/Users/ben/git/" + project
	return e
}

// unregistered is evidence carrying a path that resolved to no project.
func unregistered(when time.Time) source.Event {
	e := source.NewPoint(when, source.Identity{}, "claude-transcripts")
	e.Locator = "/Users/ben/git/somebody-elses-repo"
	return e
}

// unlocatable is evidence carrying no path at all — Biome and knowledgeC, which
// can never be attributed by the registry however many directories are blessed.
func unlocatable(start, end time.Time) source.Event {
	return source.NewInterval(start, end, source.Identity{App: "com.googlecode.iterm2"}, "biome-appusage")
}

// interval is attributed evidence that observes a DURATION rather than an
// instant. Nothing in the live store emits one today; it exists so the observed
// half of a Span is exercised by something.
func interval(start, end time.Time, project string) source.Event {
	e := source.NewInterval(start, end, source.Identity{Project: project}, "biome-appusage")
	e.Locator = "/Users/ben/git/" + project
	return e
}

func compute(t *testing.T, events []source.Event) metrics.Report {
	t.Helper()
	r, err := metrics.Compute(events, testParams())
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	return r
}

func projectOf(t *testing.T, r metrics.Report, name string) metrics.Project {
	t.Helper()
	p, ok := r.Project(name)
	if !ok {
		t.Fatalf("no project %q in the report; it holds %d", name, len(r.Projects))
	}
	return p
}

// Criterion 37i, the constructed half. Two projects worked concurrently: their
// engaged time overlaps and sums to more than the wall clock, while presence
// counts that wall clock exactly once.
func TestEngagedOverlapsAcrossProjectsWhilePresenceCountsTheClockOnce(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 3), "birch"),
	})

	// acorn spans 09:55–10:05 and birch 09:58–10:08, so one sitting runs
	// 09:55–10:08 and the two overlap across 09:58–10:05.
	if want := 13 * time.Minute; r.Presence.Total != want {
		t.Errorf("presence = %v, want %v", r.Presence.Total, want)
	}
	acorn, birch := projectOf(t, r, "acorn"), projectOf(t, r, "birch")
	if want := 10 * time.Minute; acorn.Engaged.Total != want || birch.Engaged.Total != want {
		t.Errorf("engaged = acorn %v, birch %v, want %v each", acorn.Engaged.Total, birch.Engaged.Total, want)
	}
	summed := acorn.Engaged.Total + birch.Engaged.Total
	if summed <= r.Presence.Total {
		t.Errorf("engaged summed to %v against %v of presence; concurrent work must bill to both projects "+
			"and therefore exceed the clock", summed, r.Presence.Total)
	}
}

// The discriminating case between the two models this could have been built on.
//
// Sittings are global wall-clock spans and hold whatever evidence falls in
// them. Crediting a project the WHOLE of any sitting its evidence appears
// anywhere inside would make an hour of acorn work bill in full to a client whose
// only trace in it is one commit at the far end. Engaged time is therefore the
// intersection of a sitting with the project's own evidence, re-clustered under
// the same parameters — see this package's doc for the measurement that settled
// it on real data.
func TestAProjectIsCreditedOnlyTheStretchItsOwnEvidenceReaches(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 12), "acorn"),
		point(at(10, 24), "acorn"),
		point(at(10, 36), "birch"),
	})

	// Every gap is 12m, under the 15m threshold, so this is ONE sitting
	// running 09:55–10:41.
	if want := 46 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v (one sitting)", r.Presence.Total, want)
	}
	if n := len(r.Sittings.Lengths()); n != 1 {
		t.Fatalf("%d sittings, want 1", n)
	}
	if want := 34 * time.Minute; projectOf(t, r, "acorn").Engaged.Total != want {
		t.Errorf("acorn engaged = %v, want %v (09:55–10:29)", projectOf(t, r, "acorn").Engaged.Total, want)
	}
	if want := 10 * time.Minute; projectOf(t, r, "birch").Engaged.Total != want {
		t.Errorf("birch engaged = %v, want %v (10:31–10:41); crediting it the whole 46m sitting "+
			"would bill 36 minutes of acorn work to birch",
			projectOf(t, r, "birch").Engaged.Total, want)
	}
}

// Criterion pRM. Under allocation a shared instant is divided rather than
// duplicated, so the shares must sum to EXACTLY the presence they were split
// from — asserted, because a rounding loss here would silently shrink every
// capacity figure downstream.
func TestAllocationSharesSumExactlyToTheAttributedPresence(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 3), "birch"),
	})

	var allocated time.Duration
	for _, p := range r.Projects {
		allocated += p.Allocated
	}
	want := r.Presence.Total - r.Unattributed.Presence.Total
	if allocated != want {
		t.Errorf("allocated shares sum to %v, want exactly %v (presence %v less %v unattributed)",
			allocated, want, r.Presence.Total, r.Unattributed.Presence.Total)
	}
	if allocated+r.Unattributed.Presence.Total != r.Presence.Total {
		t.Errorf("allocation plus unattributed = %v, want the presence it was split from, %v",
			allocated+r.Unattributed.Presence.Total, r.Presence.Total)
	}
}

// The same property where the arithmetic does not come out evenly.
//
// The tail-out is two nanoseconds longer than a round five minutes purely so
// that the sitting's length is NOT divisible by three. A round ten minutes
// divides exactly, which is how the first version of this test came to pass
// against an implementation that threw the remainder away — the mutation was
// applied and nothing failed. Whatever else this test does, it has to produce a
// remainder.
func TestAllocationHandsOutTheRemainderRatherThanLosingIt(t *testing.T) {
	p := testParams()
	p.TailOut += 2

	// One instant, three projects, so every project is active across the
	// whole of a single sitting.
	r, err := metrics.Compute([]source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 0), "birch"),
		point(at(10, 0), "timetattle"),
	}, p)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if len(r.Projects) != 3 {
		t.Fatalf("%d projects, want 3", len(r.Projects))
	}
	if r.Presence.Total%3 == 0 {
		t.Fatalf("presence is %v, which divides evenly by three; this test would prove nothing",
			r.Presence.Total)
	}

	var allocated time.Duration
	for _, p := range r.Projects {
		allocated += p.Allocated
	}
	if allocated != r.Presence.Total {
		t.Errorf("three shares sum to %v against %v; the remainder was lost", allocated, r.Presence.Total)
	}
	// Exact and also fair: nobody is handed the whole remainder.
	for _, p := range r.Projects {
		if d := p.Allocated - r.Presence.Total/3; d < 0 || d > 1 {
			t.Errorf("%s was allocated %v, %v off an even third; the remainder must go out one "+
				"nanosecond at a time", p.Name, p.Allocated, d)
		}
	}
	if r.Unattributed.Presence.Total != 0 {
		t.Errorf("unattributed = %v, want none: every instant here belongs to three projects",
			r.Unattributed.Presence.Total)
	}
}

// Absolute normalisation is the other half of pRM, and the two must differ:
// under absolute a shared minute counts in full for each project, so the totals
// exceed the clock, and under allocation they cannot.
func TestAbsoluteAndAllocationDisagreeExactlyWhereWorkIsShared(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 0), "birch"),
	})

	var absolute, allocated time.Duration
	for _, p := range r.Projects {
		absolute += p.Engaged.Total
		allocated += p.Allocated
	}
	if absolute != 2*r.Presence.Total {
		t.Errorf("absolute = %v, want twice the %v both projects were active across", absolute, r.Presence.Total)
	}
	if allocated != r.Presence.Total {
		t.Errorf("allocated = %v, want exactly the %v it was split from", allocated, r.Presence.Total)
	}
}

// Criterion 86I. Unattributed presence is not one bucket, and collapsing it
// into one is this project's cardinal bug. A directory nobody registered is
// actionable; a source that carries no path at all can never be attributed by
// the registry however many directories get blessed; and a stretch nothing
// observed at all is neither.
func TestUnattributedPresenceSeparatesWhyItIsUnattributed(t *testing.T) {
	r := compute(t, []source.Event{
		unregistered(at(10, 0)),
		unlocatable(at(11, 0), at(11, 20)),
	})

	u := r.Unattributed
	if u.Presence.Total != r.Presence.Total {
		t.Fatalf("unattributed %v of %v presence; nothing here is attributable", u.Presence.Total, r.Presence.Total)
	}
	if want := 10 * time.Minute; u.Unregistered != want {
		t.Errorf("unregistered = %v, want %v (09:55–10:05, a path resolving to no project)", u.Unregistered, want)
	}
	if want := 30 * time.Minute; u.Unlocatable != want {
		t.Errorf("unlocatable = %v, want %v (10:55–11:25, a source carrying no path)", u.Unlocatable, want)
	}
	if u.Unregistered+u.Unlocatable+u.Bridged != u.Presence.Total {
		t.Errorf("the reasons sum to %v, want the whole %v of unattributed presence",
			u.Unregistered+u.Unlocatable+u.Bridged, u.Presence.Total)
	}
	if u.UnregisteredEvents != 1 || u.UnlocatableEvents != 1 {
		t.Errorf("counted %d unregistered and %d unlocatable observations, want 1 each",
			u.UnregisteredEvents, u.UnlocatableEvents)
	}
}

// The third reason has to be reachable, or it is a state nothing can enter and
// the distinction is decorative. A silence longer than the lead-in and tail-out
// together, but shorter than the gap, is bridged into one sitting and nothing
// observed the middle of it.
func TestSilenceTheGapThresholdBridgedIsItsOwnReason(t *testing.T) {
	r := compute(t, []source.Event{
		unregistered(at(10, 0)),
		unregistered(at(10, 14)),
	})

	// 09:55–10:19, one sitting. The tail-out reaches 10:05 and the next
	// lead-in reaches back to 10:09, so 10:05–10:09 is bridged silence.
	if want := 24 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v", r.Presence.Total, want)
	}
	if want := 4 * time.Minute; r.Unattributed.Bridged != want {
		t.Errorf("bridged = %v, want %v; nothing observed 10:05–10:09 and nothing is near enough "+
			"to be responsible for it", r.Unattributed.Bridged, want)
	}
}

// Criterion kHo. Sixteen twenty-minute blocks and two four-hour blocks are both
// presence hours and are obviously different kinds of time, so a total that
// does not carry its distribution is not enough to act on.
func TestSittingLengthDistributionSeparatesManyShortBlocksFromFewLongOnes(t *testing.T) {
	var short, long []source.Event
	for i := range 16 {
		// Ten minutes apart is inside the gap, so a run of them is one
		// sitting; an hour apart is not.
		base := at(6+i, 0)
		short = append(short, point(base, "acorn"), point(base.Add(10*time.Minute), "acorn"))
	}
	for i := range 2 {
		base := at(6+i*6, 0)
		for m := 0; m <= 230; m += 10 {
			long = append(long, point(base.Add(time.Duration(m)*time.Minute), "birch"))
		}
	}

	fragmented := compute(t, short)
	sustained := compute(t, long)

	if got := len(fragmented.Sittings.Lengths()); got != 16 {
		t.Fatalf("%d fragmented sittings, want 16", got)
	}
	if got := len(sustained.Sittings.Lengths()); got != 2 {
		t.Fatalf("%d sustained sittings, want 2", got)
	}
	if fragmented.Sittings.Median >= sustained.Sittings.Median {
		t.Errorf("median: fragmented %v, sustained %v; the distribution must tell them apart",
			fragmented.Sittings.Median, sustained.Sittings.Median)
	}
	// The buckets are what makes the shape legible without reading 16 rows.
	var shortBucket, longBucket int
	for _, b := range fragmented.Sittings.Buckets {
		if b.AtLeast < time.Hour {
			shortBucket += b.Count
		}
	}
	for _, b := range sustained.Sittings.Buckets {
		if b.AtLeast >= time.Hour {
			longBucket += b.Count
		}
	}
	if shortBucket != 16 || longBucket != 2 {
		t.Errorf("buckets put %d fragmented sittings under an hour and %d sustained ones at or over it, "+
			"want 16 and 2", shortBucket, longBucket)
	}
}

// Every duration this package reports carries how much of it anything actually
// observed, because on the live corpus 77.2% of engaged time is margin — the
// lead-in, the tail-out and every silence the threshold bridged. A caller that
// can reach a bare number cannot tell an invoice from a guess.
func TestASpanReportsTheObservedAndTheInferredSeparately(t *testing.T) {
	r := compute(t, []source.Event{interval(at(10, 0), at(10, 30), "acorn")})

	// 09:55–10:35, of which 10:00–10:30 was observed.
	if want := 40 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v", r.Presence.Total, want)
	}
	if want := 30 * time.Minute; r.Presence.Observed != want {
		t.Errorf("observed presence = %v, want %v", r.Presence.Observed, want)
	}
	if want := 10 * time.Minute; r.Presence.Margin() != want {
		t.Errorf("margin = %v, want %v", r.Presence.Margin(), want)
	}
	acorn := projectOf(t, r, "acorn")
	if want := 30 * time.Minute; acorn.Engaged.Observed != want {
		t.Errorf("acorn observed = %v, want %v", acorn.Engaged.Observed, want)
	}
}

// An instant is observed to be a project's only by that project's own evidence.
// A Biome focus interval proves the machine was in use and says nothing about
// for whom, so it corroborates without attributing — three states again, and
// the difference between "acorn was observed working" and "somebody was".
func TestEvidenceThatNamesNoProjectCorroboratesWithoutAttributing(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		unlocatable(at(9, 56), at(10, 4)),
	})

	acorn := projectOf(t, r, "acorn")
	if acorn.Engaged.Observed != 0 {
		t.Errorf("acorn observed = %v, want none: its own evidence is an instant and observes no duration",
			acorn.Engaged.Observed)
	}
	if want := 8 * time.Minute; acorn.Corroborated != want {
		t.Errorf("acorn corroborated = %v, want %v; a focus interval shows the machine was in use "+
			"across 09:56–10:04 without saying whose work it was", acorn.Corroborated, want)
	}
}

// The segments are the partition every total is computed from. If they do not
// tile presence exactly, some total is counting an instant twice or not at all,
// and no other assertion in this file would notice.
func TestSegmentsTilePresenceExactlyAndInOrder(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 3), "birch"),
		unlocatable(at(11, 0), at(11, 20)),
		unregistered(at(13, 0)),
	})

	var covered time.Duration
	for i, s := range r.Segments {
		if !s.End.After(s.Start) {
			t.Fatalf("segment %d is %v..%v and covers nothing", i, s.Start, s.End)
		}
		if i > 0 && s.Start.Before(r.Segments[i-1].End) {
			t.Fatalf("segment %d starts at %v, before segment %d ended at %v", i, s.Start, i-1, r.Segments[i-1].End)
		}
		covered += s.Duration()
	}
	if covered != r.Presence.Total {
		t.Errorf("segments cover %v against %v of presence", covered, r.Presence.Total)
	}
}

// Engaged time overlaps ACROSS projects and never exceeds the clock WITHIN one.
// A project's own hours cannot be more than the wall clock it was worked in,
// however its evidence is arranged.
func TestNoProjectIsCreditedMoreThanThePresenceItSitsIn(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 3), "birch"),
		point(at(10, 6), "acorn"),
	})
	for _, p := range r.Projects {
		if p.Engaged.Total > r.Presence.Total {
			t.Errorf("%s engaged %v against %v of presence", p.Name, p.Engaged.Total, r.Presence.Total)
		}
		if p.Allocated > p.Engaged.Total {
			t.Errorf("%s allocated %v, more than the %v it was engaged", p.Name, p.Allocated, p.Engaged.Total)
		}
	}
}

// A stretch the screen was observed dark across is not presence, and metrics
// must inherit that rather than re-deriving it. The clustering already splits
// and trims; this asserts the trim survives into the totals.
func TestObservedDarknessIsNotCountedAsPresence(t *testing.T) {
	dark := source.NewInterval(at(10, 6), at(10, 10), source.Identity{}, "knowledgec-backlit")
	dark.State = "off"

	// 10:00 and 10:12 are twelve minutes apart, inside the gap, so without the
	// dark reading they are one sitting running 09:55–10:17.
	lit := compute(t, []source.Event{point(at(10, 0), "acorn"), point(at(10, 12), "acorn")})
	trimmed := compute(t, []source.Event{point(at(10, 0), "acorn"), point(at(10, 12), "acorn"), dark})

	if want := 22 * time.Minute; lit.Presence.Total != want {
		t.Fatalf("presence without the dark reading = %v, want %v", lit.Presence.Total, want)
	}
	if !(trimmed.Presence.Total < lit.Presence.Total) {
		t.Errorf("presence with an observed dark span is %v against %v without it; the trim was lost",
			trimmed.Presence.Total, lit.Presence.Total)
	}
	if want := 18 * time.Minute; trimmed.Presence.Total != want {
		t.Errorf("presence = %v, want %v (09:55–10:06 and 10:10–10:17)", trimmed.Presence.Total, want)
	}
	if acorn := projectOf(t, trimmed, "acorn"); acorn.Engaged.Total != trimmed.Presence.Total {
		t.Errorf("acorn engaged %v against %v of presence; a project's own clustering must be trimmed "+
			"by the same darkness", acorn.Engaged.Total, trimmed.Presence.Total)
	}
}

// Presence is NOT screen-on time, and the difference is not academic: on the
// live corpus the backlight is silent about 74.8% of the clock the sittings
// cover, because knowledgeC prunes itself in 9–28 days while the transcript
// corpus reaches back much further. A capacity denominator that quietly rested
// on 1,623 backlight readings would be resting on a quarter of the data, so how
// much of presence the backlight actually witnessed is reported rather than
// assumed.
func TestHowMuchOfPresenceTheBacklightWitnessedIsReported(t *testing.T) {
	lit := source.NewInterval(at(9, 50), at(10, 0), source.Identity{}, "knowledgec-backlit")
	lit.State = "on"

	r := compute(t, []source.Event{point(at(10, 0), "acorn"), lit})

	// The sitting is 09:55–10:05 and the lit reading is 09:50–10:00.
	if want := 10 * time.Minute; r.Presence.Total != want {
		t.Fatalf("presence = %v, want %v; a lit reading must not merge or extend anything",
			r.Presence.Total, want)
	}
	b := r.Backlight
	if want := 10 * time.Minute; b.Lit != want {
		t.Errorf("lit = %v, want %v", b.Lit, want)
	}
	if want := 5 * time.Minute; b.Corroborated != want {
		t.Errorf("corroborated = %v, want %v (09:55–10:00)", b.Corroborated, want)
	}
	if want := 5 * time.Minute; b.LitOutsideASitting != want {
		t.Errorf("lit outside a sitting = %v, want %v (09:50–09:55: the screen was on and "+
			"nothing was observed happening)", b.LitOutsideASitting, want)
	}
	if want := 5 * time.Minute; b.Unwitnessed != want {
		t.Errorf("unwitnessed = %v, want %v (10:00–10:05: the backlight said neither on nor off, "+
			"which is not the same as saying off)", b.Unwitnessed, want)
	}
	if b.Readings != 1 {
		t.Errorf("readings = %d, want 1", b.Readings)
	}
}

// With no presence source configured at all, every instant of presence is
// unwitnessed — and that must read as "nothing was recorded", not as a
// backlight that observed the screen dark.
func TestWithNoPresenceSourceEveryInstantIsUnwitnessed(t *testing.T) {
	p := testParams()
	p.PresenceSources = nil
	r, err := metrics.Compute([]source.Event{point(at(10, 0), "acorn")}, p)
	if err != nil {
		t.Fatalf("Compute: %v", err)
	}
	if r.Backlight.Unwitnessed != r.Presence.Total {
		t.Errorf("unwitnessed = %v against %v of presence; with nothing observing presence "+
			"the whole of it is unwitnessed", r.Backlight.Unwitnessed, r.Presence.Total)
	}
	if r.Backlight.Lit != 0 || r.Backlight.Readings != 0 {
		t.Errorf("backlight = %+v, want nothing observed either way", r.Backlight)
	}
}

// Nothing recorded is not zero. A report over no events at all must say so
// rather than reporting a confident set of zeroes.
func TestNoObservationsProducesNoPresenceAndNoProjects(t *testing.T) {
	r := compute(t, nil)
	if r.Presence.Total != 0 || len(r.Projects) != 0 || len(r.Segments) != 0 {
		t.Errorf("report over no events = %+v, want empty", r)
	}
	if r.Sittings.Count != 0 {
		t.Errorf("sittings = %d, want 0", r.Sittings.Count)
	}
}

// Params travel with the numbers, for the same reason a stored session carries
// the parameters it was derived under: an engaged figure separated from its gap
// threshold means nothing, and the threshold moves the total by 2.3x.
func TestTheReportCarriesTheParametersItWasDerivedUnder(t *testing.T) {
	r := compute(t, []source.Event{point(at(10, 0), "acorn")})
	if r.Params.Gap != testParams().Gap || r.Params.LeadIn != testParams().LeadIn {
		t.Errorf("params = %+v, want %+v", r.Params, testParams())
	}
}

// Every figure in a report is a sum over the same segments, so two identities
// hold by construction: the allocation plus the unattributed presence is the
// presence it was split from, and the three reasons are the whole of the
// unattributed presence. Compute checks both before returning, because the way
// they would break — a boundary nobody cut at, so a segment straddles a span
// and is counted as outside it — produces a plausible report rather than a
// crash, and nothing downstream would ever notice a project a few minutes
// short.
func TestReconcilesItsOwnTotalsBeforeReturning(t *testing.T) {
	r := compute(t, []source.Event{
		point(at(10, 0), "acorn"),
		point(at(10, 3), "birch"),
		unregistered(at(13, 0)),
		unlocatable(at(15, 0), at(15, 20)),
	})

	var allocated time.Duration
	for _, p := range r.Projects {
		allocated += p.Allocated
	}
	if allocated+r.Unattributed.Presence.Total != r.Presence.Total {
		t.Errorf("allocated %v plus unattributed %v is not the %v of presence they came from",
			allocated, r.Unattributed.Presence.Total, r.Presence.Total)
	}
	u := r.Unattributed
	if u.Unregistered+u.Unlocatable+u.Bridged != u.Presence.Total {
		t.Errorf("the reasons sum to %v against %v of unattributed presence",
			u.Unregistered+u.Unlocatable+u.Bridged, u.Presence.Total)
	}
	// Observed and the reasons are different clocks and the first is the
	// smaller: an instant is RESPONSIBLE for the lead-in and tail-out around
	// it and OBSERVES neither. An earlier version of this file asserted they
	// were equal, and it was the assertion that was wrong.
	if u.Presence.Observed > u.Unregistered+u.Unlocatable {
		t.Errorf("observed unattributed %v exceeds the %v something was responsible for; "+
			"an observation cannot span more than it accounts for",
			u.Presence.Observed, u.Unregistered+u.Unlocatable)
	}
	// Bridged is the state where nothing was recorded, so nothing can have
	// observed it. If any bridged segment reads as observed, the two coverages
	// have been crossed.
	for _, s := range r.Segments {
		if s.Unattributed == metrics.ReasonBridged && s.Observed {
			t.Errorf("segment %v..%v is bridged silence and also reads as observed", s.Start, s.End)
		}
	}
}

func TestUnusableParametersAreRefused(t *testing.T) {
	if _, err := metrics.Compute(nil, sessions.Params{Gap: 0}); err == nil {
		t.Error("Compute with a zero gap returned no error; every observation would be its own sitting")
	}
}
