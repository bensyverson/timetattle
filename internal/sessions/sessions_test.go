package sessions_test

import (
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// base is an arbitrary instant with round components, so an offset printed in
// a failure message reads as the offset it is.
var base = time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

func at(d time.Duration) time.Time { return base.Add(d) }

// params is the shape every test starts from: a 15-minute gap and five
// minutes either side, with knowledgec-backlit read as presence.
func params() sessions.Params {
	return sessions.Params{
		Gap:             15 * time.Minute,
		LeadIn:          5 * time.Minute,
		TailOut:         5 * time.Minute,
		PresenceSources: []string{"knowledgec-backlit"},
	}
}

// point is an observation carrying the EMPTY cause, which is not an absence of
// information here but the largest population in the corpus: a focus interval
// and a transcript record are not artifacts, so nothing wrote them in the sense
// Cause means, and 868,924 live claude-transcripts rows carry it. It is the arm
// darkness is not entitled to suppress, so most tests in this file reach for it.
//
// The source id is a stand-in and not a claim about fs-observations, which
// declares user-action for every event it emits; `saved` is that case.
func point(d time.Duration) source.Event {
	return source.NewPoint(at(d), source.Identity{Document: "a.motn"}, "fsobserve")
}

// autosave is an artifact written by a wall-clock timer nobody touches. Motion
// writes one every fifteen minutes whether or not a person is at the machine,
// and the 2026-07-11 17:53 one was written with the screen off during a break
// Ben independently recalls.
func autosave(d time.Duration) source.Event {
	e := source.NewPoint(at(d), source.Identity{Document: "a.motn"}, "motion-autosave")
	e.Cause = source.CauseTimer
	return e
}

// saved is an artifact written inside the application's own interaction loop,
// by something a person did: a Save, an export, a commit.
func saved(d time.Duration) source.Event {
	e := source.NewPoint(at(d), source.Identity{Document: "a.blend"}, "fs-observations")
	e.Cause = source.CauseUserAction
	return e
}

// recovered is an artifact written because something went wrong. Nothing in the
// live store emits one yet and source.CauseRecovery documents its evidentiary
// weight as UNMEASURED, which is the reason it must not be suppressed: dropping
// it would be drawing exactly the conclusion nobody has established.
func recovered(d time.Duration) source.Event {
	e := source.NewPoint(at(d), source.Identity{Document: "a.psd"}, "photoshop-autorecover")
	e.Cause = source.CauseRecovery
	return e
}

func interval(from, to time.Duration) source.Event {
	return source.NewInterval(at(from), at(to), source.Identity{App: "com.apple.motionapp"}, "biome-appusage")
}

func backlit(from, to time.Duration, state string) source.Event {
	return source.Event{
		Start: at(from), End: at(to),
		Shape:  source.Interval,
		State:  state,
		Source: "knowledgec-backlit",
	}
}

func cluster(t *testing.T, events []source.Event, p sessions.Params) sessions.Result {
	t.Helper()
	res, err := sessions.Cluster(events, p)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	return res
}

// spans renders sessions as offsets from base, for readable failures.
func spans(res sessions.Result) []string {
	out := make([]string, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		out = append(out, s.Start.Sub(base).String()+".."+s.End.Sub(base).String())
	}
	return out
}

func TestObservationsWithinTheGapAreOneSitting(t *testing.T) {
	res := cluster(t, []source.Event{point(0), point(10 * time.Minute)}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 20*time.Minute; got != want {
		t.Errorf("session lasted %v, want %v (10m observed, 5m either side)", got, want)
	}
}

func TestObservationsBeyondTheGapAreSeparateSittings(t *testing.T) {
	res := cluster(t, []source.Event{point(0), point(45 * time.Minute)}, params())

	if len(res.Sessions) != 2 {
		t.Fatalf("got %d sessions %v, want 2", len(res.Sessions), spans(res))
	}
}

// Criterion kRK.
func TestAnIsolatedObservationProducesANonZeroSession(t *testing.T) {
	// One Motion save, nothing else all day. Without a lead-in and a tail-out
	// it has no span and the day scores exactly zero, which is how sparse
	// creative work disappears.
	res := cluster(t, []source.Event{point(0)}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got := res.Sessions[0].Duration(); got <= 0 {
		t.Fatalf("an isolated observation scored %v", got)
	}
	if got, want := res.Sessions[0].Duration(), 10*time.Minute; got != want {
		t.Errorf("session lasted %v, want %v", got, want)
	}
	// All of it is inferred: nothing observed any of that span.
	if got, want := res.Sessions[0].Margin, 10*time.Minute; got != want {
		t.Errorf("margin %v, want %v — every second of an isolated point's session is inferred", got, want)
	}
}

// Criterion TXV.
func TestTheGapThresholdIsAParameter(t *testing.T) {
	events := []source.Event{point(0), point(10 * time.Minute), point(30 * time.Minute)}

	for _, tc := range []struct {
		gap  time.Duration
		want int
	}{
		{2 * time.Minute, 3},
		{15 * time.Minute, 2},
		{45 * time.Minute, 1},
	} {
		p := params()
		p.Gap = tc.gap
		// Margins narrow enough that the gap alone decides. With the
		// five-minute defaults a two-minute gap is unreachable, which is the
		// interaction the next test is about.
		p.LeadIn, p.TailOut = 30*time.Second, 30*time.Second
		res := cluster(t, events, p)
		if len(res.Sessions) != tc.want {
			t.Errorf("gap %v gave %d sessions %v, want %d", tc.gap, len(res.Sessions), spans(res), tc.want)
		}
	}
}

func TestMarginsWiderThanTheGapSwallowIt(t *testing.T) {
	// Found by a test that assumed otherwise, and worth keeping as a test
	// because it is the shape of a parameter that appears to be set and is
	// not. Three observations ten minutes apart at a two-minute gap are three
	// clusters, but a five-minute lead-in and tail-out expand each one by ten
	// minutes, so they all touch and come back as one sitting. The effective
	// threshold is Gap plus LeadIn plus TailOut, not Gap.
	events := []source.Event{point(0), point(10 * time.Minute), point(20 * time.Minute)}
	p := params()
	p.Gap = 2 * time.Minute

	res := cluster(t, events, p)

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if res.Stats.MergedByExpansion != 2 {
		t.Errorf("MergedByExpansion = %d, want 2; a gap overridden in silence is a parameter nobody can trust",
			res.Stats.MergedByExpansion)
	}
}

func TestTheGapIsMeasuredFromTheRunningEndNotTheStart(t *testing.T) {
	// A one-hour focus interval followed by a save five minutes after it
	// ended is one sitting. Measuring the gap from the interval's START would
	// make it 65 minutes and split a continuous hour of work in two.
	res := cluster(t, []source.Event{interval(0, time.Hour), point(65 * time.Minute)}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 75*time.Minute; got != want {
		t.Errorf("session lasted %v, want %v", got, want)
	}
}

func TestTheRunningEndOutlivesTheObservationsAppendedAfterIt(t *testing.T) {
	// Two observations cannot tell a running MAXIMUM end from the end of
	// whichever observation was appended last, because with two of them those
	// are the same instant. Three can, and the distinction is the whole of the
	// rule: a two-hour focus interval, a save ten minutes into it, and a save
	// ten minutes after it ended.
	//
	// The reach of the run is the interval's end throughout. Carrying the last
	// appended end instead would make the run reach only 0:10, put the final
	// save 1h50m past it, and cut a continuous two hours in half.
	events := []source.Event{
		interval(0, 2*time.Hour),
		point(10 * time.Minute),
		point(2*time.Hour + 10*time.Minute),
	}
	res := cluster(t, events, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 2*time.Hour+20*time.Minute; got != want {
		t.Errorf("session lasted %v, want %v", got, want)
	}
}

func TestOverlappingIntervalsDoNotDoubleCount(t *testing.T) {
	res := cluster(t, []source.Event{interval(0, 30*time.Minute), interval(10*time.Minute, 20*time.Minute)}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 40*time.Minute; got != want {
		t.Errorf("session lasted %v, want %v", got, want)
	}
	if got := res.Sessions[0].Margin; got != 10*time.Minute {
		t.Errorf("margin %v, want 10m — only the lead-in and tail-out were unobserved", got)
	}
}

func TestExpansionThatOverlapsMergesRatherThanDoubleCounting(t *testing.T) {
	// Two isolated saves eight minutes apart, with a five-minute gap
	// threshold, are two clusters. Expanded by five minutes either side their
	// spans overlap by two minutes, and emitting both would charge those two
	// minutes twice — to a metric whose whole purpose is that wall clock is
	// never double-counted.
	p := params()
	p.Gap = 5 * time.Minute
	res := cluster(t, []source.Event{point(0), point(8 * time.Minute)}, p)

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1 merged", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 18*time.Minute; got != want {
		t.Errorf("merged session lasted %v, want %v (not the 20m two separate ones would total)", got, want)
	}
	if res.Stats.MergedByExpansion != 1 {
		t.Errorf("MergedByExpansion = %d, want 1; a merge that is not reported is a merge nobody can audit",
			res.Stats.MergedByExpansion)
	}
}

func TestPresenceBoundsSessionsAndNeverCreatesThem(t *testing.T) {
	// Fourteen hours of lit screen and no evidence of work is not fourteen
	// hours of work. Presence is the denominator for capacity, computed
	// elsewhere; clustering it here would make an evening of reading
	// indistinguishable from an evening of building.
	res := cluster(t, []source.Event{backlit(0, 14*time.Hour, "on")}, params())

	if len(res.Sessions) != 0 {
		t.Fatalf("got %d sessions %v, want none: presence alone is not work", len(res.Sessions), spans(res))
	}
	if res.Stats.Presence != 1 {
		t.Errorf("Presence = %d, want 1", res.Stats.Presence)
	}
	if res.Stats.Evidence != 0 {
		t.Errorf("Evidence = %d, want 0", res.Stats.Evidence)
	}
}

func TestObservedAbsenceDoesNotCarveASitting(t *testing.T) {
	// The screen was observed off in the middle, and the sitting runs straight
	// through it. A dark backlight says the lid is shut, and a render, a
	// compile or an agent goes on producing work with the lid shut; the
	// stretch is not time nobody worked, it is time nobody was looking.
	//
	// This asserted the opposite until 2026-08-14. What changed is not the
	// arithmetic but what the reading is taken to MEAN, so the numbers here
	// move rather than the mechanism.
	events := []source.Event{
		point(0),
		point(10 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}
	res := cluster(t, events, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1: darkness does not carve non-timer evidence",
			len(res.Sessions), spans(res))
	}
	if got, want := res.Total(), 20*time.Minute; got != want {
		t.Errorf("total %v, want the full %v expanded span", got, want)
	}
	if res.Stats.TrimmedByAbsence != 0 {
		t.Errorf("TrimmedByAbsence = %v, want 0", res.Stats.TrimmedByAbsence)
	}
	if res.Stats.SplitByAbsence != 0 {
		t.Errorf("SplitByAbsence = %d, want 0", res.Stats.SplitByAbsence)
	}
	// The reading is still reported. Not acting on a measurement is not the
	// same as not having taken one, and a figure that rests on dark clock has
	// to remain recognisable as one.
	if got, want := res.Stats.ObservedAbsence, 2*time.Minute; got != want {
		t.Errorf("ObservedAbsence = %v, want %v; the reading must still be reported", got, want)
	}
}

func TestObservedAbsenceDoesNotCarveAUserActionSitting(t *testing.T) {
	// The same shape with the strongest artifact any source emits. A .blend1
	// written by Save is user-action, and there is no reading of a dark screen
	// under which a person pressing Save was not working.
	events := []source.Event{
		saved(0),
		saved(10 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}
	res := cluster(t, events, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Total(), 20*time.Minute; got != want {
		t.Errorf("total %v, want %v", got, want)
	}
}

func TestObservedAbsenceStillSuppressesTimerEvidence(t *testing.T) {
	// The sibling, and the whole of what darkness is still entitled to do. Two
	// autosaves either side of a dark stretch and a third written inside it:
	// the third asserts the application was running and nothing whatever about
	// whether the work was happening, so it is dropped and counted.
	events := []source.Event{
		autosave(0),
		autosave(3 * time.Minute),
		autosave(10 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}
	res := cluster(t, events, params())

	if res.Stats.SuppressedByAbsence != 1 {
		t.Errorf("SuppressedByAbsence = %d, want 1; the autosave written in the dark is the one "+
			"observation darkness still removes", res.Stats.SuppressedByAbsence)
	}
	// The two that were not in the dark are untouched, and the sitting they
	// make is not carved by the darkness between them.
	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Events, 2; got != want {
		t.Errorf("the sitting rests on %d observations, want %d", got, want)
	}
}

func TestNothingRecordedIsNotObservedAbsence(t *testing.T) {
	// The identical shape, with the backlight row removed. The sampler said
	// nothing, which is an absence of evidence and not evidence of absence —
	// collapsing the two deleted 1h27m of confirmed work on 2026-07-14 in an
	// earlier tool, and this project has now shipped that bug three times.
	events := []source.Event{point(0), point(10 * time.Minute)}
	res := cluster(t, events, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Total(), 20*time.Minute; got != want {
		t.Errorf("total %v, want %v; an unsampled gap must not be trimmed", got, want)
	}
	if res.Stats.TrimmedByAbsence != 0 {
		t.Errorf("TrimmedByAbsence = %v, want 0", res.Stats.TrimmedByAbsence)
	}
}

func TestContestedTimeIsReportedRatherThanTrimmed(t *testing.T) {
	// knowledgeC writes contradictory overlapping rows — at 2026-07-14
	// 09:01:12 both an isBacklit=1 and an isBacklit=0 row with identical
	// endpoints. Where the two readings disagree the darkness is not
	// established, so nothing is deleted, and the contested span is reported
	// so that a number resting on it can be recognised.
	events := []source.Event{
		point(0),
		point(10 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
		backlit(2*time.Minute, 4*time.Minute, "on"),
	}
	res := cluster(t, events, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1: a contested reading establishes no darkness", len(res.Sessions), spans(res))
	}
	if res.Stats.TrimmedByAbsence != 0 {
		t.Errorf("TrimmedByAbsence = %v, want 0", res.Stats.TrimmedByAbsence)
	}
	if got, want := res.Stats.Contested, 2*time.Minute; got != want {
		t.Errorf("Contested = %v, want %v", got, want)
	}
}

func TestATimerObservationMadeInObservedDarknessIsSuppressed(t *testing.T) {
	// A Motion autosave is written by a wall-clock timer that does not care
	// whether anyone is at the machine: the 2026-07-11 17:53 one was written
	// with the screen off during a break Ben independently recalls. It is
	// evidence the application was running and none at all that work was
	// happening, and a timer is the ONLY mechanism about which that is true.
	//
	// The fixture said `point` until 2026-08-14, which carries no cause, so
	// this test asserted the rule against every observation in the corpus while
	// its comment described one. It now encodes the timer it always described.
	events := []source.Event{
		autosave(3 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}
	res := cluster(t, events, params())

	if len(res.Sessions) != 0 {
		t.Fatalf("got %d sessions %v, want none", len(res.Sessions), spans(res))
	}
	if res.Stats.SuppressedByAbsence != 1 {
		t.Errorf("SuppressedByAbsence = %d, want 1; an observation dropped without a count is a number that vanished",
			res.Stats.SuppressedByAbsence)
	}
}

func TestANonTimerObservationMadeInObservedDarknessSurvives(t *testing.T) {
	// The identical shape, by a mechanism that is not a timer. The backlight
	// observed a LID, not a person: a video render with the screen deliberately
	// switched off is unambiguous work that leaves exactly this trace, and it
	// involves nothing anyone has to hold an opinion about.
	//
	// The empty cause survives, and that is a choice rather than an oversight.
	// It fails toward counting, which is the direction chosen deliberately, and
	// it is the largest population in the corpus.
	events := []source.Event{
		point(3 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}
	res := cluster(t, events, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1: darkness is not a reason to delete work", len(res.Sessions), spans(res))
	}
	if res.Stats.SuppressedByAbsence != 0 {
		t.Errorf("SuppressedByAbsence = %d, want 0", res.Stats.SuppressedByAbsence)
	}
	if got, want := res.Sessions[0].Duration(), 10*time.Minute; got != want {
		t.Errorf("session lasted %v, want the lead-in plus tail-out %v", got, want)
	}
}

func TestAUserActionObservationMadeInObservedDarknessSurvives(t *testing.T) {
	res := cluster(t, []source.Event{
		saved(3 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if res.Stats.SuppressedByAbsence != 0 {
		t.Errorf("SuppressedByAbsence = %d, want 0", res.Stats.SuppressedByAbsence)
	}
}

func TestARecoveryObservationMadeInObservedDarknessSurvives(t *testing.T) {
	// source.CauseRecovery ships with its evidentiary weight UNMEASURED and its
	// own instruction not to draw conclusions from it. Suppressing it would be
	// drawing one — that it is worth what a timer is worth — on no measurement
	// at all. The default arm is to count, and the exception has to be earned;
	// only the timer has earned it, and only against a measured case.
	res := cluster(t, []source.Event{
		recovered(3 * time.Minute),
		backlit(2*time.Minute, 4*time.Minute, "off"),
	}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if res.Stats.SuppressedByAbsence != 0 {
		t.Errorf("SuppressedByAbsence = %d, want 0", res.Stats.SuppressedByAbsence)
	}
}

func TestEverySurvivingObservationLandsInASitting(t *testing.T) {
	// The property that makes suppression and span-trimming ONE decision rather
	// than two, and the test that catches doing half of it.
	//
	// Narrowing the suppression rule while still subtracting darkness from the
	// cluster span keeps the observation and throws away the only time it could
	// have occupied: its cluster is carved down to nothing, no sitting is
	// emitted, and NOTHING COUNTS IT — not SuppressedByAbsence, which no longer
	// fires, and not UnevidencedPieces, which counts pieces rather than
	// observations. Work would disappear more quietly than it did before.
	//
	// So: every evidence observation is either suppressed and counted, or lands
	// in a sitting. There is no third outcome, and this asserts it over a
	// corpus dense with darkness rather than over one hand-built case.
	rng := rand.New(rand.NewSource(17))
	var events []source.Event
	for range 300 {
		start := time.Duration(rng.Intn(60*60*24)) * time.Second
		switch rng.Intn(3) {
		case 0:
			events = append(events, autosave(start))
		case 1:
			events = append(events, saved(start))
		default:
			events = append(events, point(start))
		}
	}
	for range 40 {
		start := time.Duration(rng.Intn(60*60*24)) * time.Second
		events = append(events, backlit(start, start+time.Duration(rng.Intn(1800))*time.Second, "off"))
	}
	res := cluster(t, events, params())

	if res.Stats.ObservedAbsence == 0 {
		t.Fatal("the corpus established no darkness at all, so this asserts nothing")
	}
	var placed int
	for _, s := range res.Sessions {
		placed += s.Events
	}
	// Short and long are different bugs. Short means an observation was kept and
	// the only time it could occupy was carved away, so it landed nowhere and
	// nothing counted it. Long means one observation was counted in two pieces
	// of a carved cluster, double-counting wall clock.
	if got := placed + res.Stats.SuppressedByAbsence; got != res.Stats.Evidence {
		t.Errorf("%d observations landed in a sitting and %d were suppressed, against %d of evidence "+
			"(%+d): observations are being %s", placed, res.Stats.SuppressedByAbsence,
			res.Stats.Evidence, got-res.Stats.Evidence,
			map[bool]string{true: "counted in more than one sitting", false: "stranded, counted by nothing"}[got > res.Stats.Evidence])
	}
	if res.Stats.SuppressedByAbsence == 0 {
		t.Error("no timer observation fell in darkness, so the suppressed arm of the sum is untested")
	}
}

func TestNoSittingRestsOnZeroObservations(t *testing.T) {
	// Found by the schema, on the live store, at 2026-07-09T23:37:36: a session
	// resting on zero observations, refused by CHECK (events > 0). Two dark
	// spans inside one cluster cut it into three, and the middle piece lay
	// BETWEEN two observations rather than containing either, so nothing
	// evidenced it and nothing had observed darkness there either.
	//
	// The exact corpus that produced it, kept because the invariant it broke —
	// a sitting must rest on at least one observation — is enforced by a
	// database constraint and must not depend on the route by which sittings
	// are built.
	//
	// It now comes out as ONE sitting rather than two plus a dropped middle.
	// Darkness no longer carves a cluster at all, so no piece exists to be left
	// unevidenced: the case is not handled, it is unreachable, and
	// Stats.UnevidencedPieces is structurally zero rather than measured zero.
	// That is asserted here so a future reader cannot mistake the one for the
	// other.
	p := params()
	p.Gap = 30 * time.Minute
	p.LeadIn, p.TailOut = 0, 0
	events := []source.Event{
		point(0),
		point(20 * time.Minute),
		backlit(5*time.Minute, 8*time.Minute, "off"),
		backlit(12*time.Minute, 15*time.Minute, "off"),
	}
	res := cluster(t, events, p)

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	for i, s := range res.Sessions {
		if s.Events == 0 {
			t.Errorf("session %d (%v..%v) rests on no observation", i, s.Start, s.End)
		}
	}
	if got, want := res.Total(), 20*time.Minute; got != want {
		t.Errorf("total %v, want %v", got, want)
	}
	if res.Stats.UnevidencedPieces != 0 {
		t.Errorf("UnevidencedPieces = %d, want 0: no cluster is cut, so no piece can be left over",
			res.Stats.UnevidencedPieces)
	}
	if res.Stats.UnevidencedDropped != 0 {
		t.Errorf("UnevidencedDropped = %v, want 0", res.Stats.UnevidencedDropped)
	}
}

func TestIgnoredSourcesContributeNothingAndAreCounted(t *testing.T) {
	// A 'locked' interval in the live store reaches 15h32m. Clustered as
	// evidence of work it manufactures a fifteen-hour sitting out of a
	// night's sleep.
	locked := source.Event{
		Start: at(0), End: at(15 * time.Hour),
		Shape: source.Interval, State: "locked", Source: "knowledgec-islocked",
	}
	p := params()
	p.IgnoreSources = []string{"knowledgec-islocked"}
	res := cluster(t, []source.Event{locked, point(time.Hour)}, p)

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 10*time.Minute; got != want {
		t.Errorf("session lasted %v, want %v", got, want)
	}
	if res.Stats.Ignored != 1 {
		t.Errorf("Ignored = %d, want 1", res.Stats.Ignored)
	}
}

func TestAnUnbounddedEventIsAnInstantAndNeverAnInventedSpan(t *testing.T) {
	// A bare stop is a partner record macOS never wrote. Its duration was
	// never observed, and interpolating one would have charged 5m09s of an
	// idle machine to Safari in the one case measured against knowledgeC.
	stop := source.NewOpenStart(at(time.Hour), source.UnknownSleep,
		source.Identity{App: "com.apple.Safari"}, "biome-appusage")
	res := cluster(t, []source.Event{stop}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions %v, want 1", len(res.Sessions), spans(res))
	}
	if got, want := res.Sessions[0].Duration(), 10*time.Minute; got != want {
		t.Errorf("session lasted %v, want the lead-in plus tail-out %v", got, want)
	}
	if res.Stats.Unbounded != 1 {
		t.Errorf("Unbounded = %d, want 1", res.Stats.Unbounded)
	}
}

func TestSessionsComeOutOrderedAndDisjoint(t *testing.T) {
	// The property Tvfnf and IFmcU both rest on: presence is wall clock and
	// is never double-counted, so two sessions may not claim one instant.
	rng := rand.New(rand.NewSource(7))
	var events []source.Event
	for range 400 {
		start := time.Duration(rng.Intn(60*60*24)) * time.Second
		if rng.Intn(2) == 0 {
			events = append(events, point(start))
			continue
		}
		events = append(events, interval(start, start+time.Duration(rng.Intn(3600))*time.Second))
	}
	res := cluster(t, events, params())

	if len(res.Sessions) == 0 {
		t.Fatal("400 random observations produced no sessions")
	}
	for i, s := range res.Sessions {
		if s.End.Before(s.Start) {
			t.Fatalf("session %d ends before it starts: %v..%v", i, s.Start, s.End)
		}
		if s.Events == 0 || len(s.Sources) == 0 {
			t.Fatalf("session %d (%v..%v) rests on no observation", i, s.Start, s.End)
		}
		if s.Margin > s.Duration() {
			t.Fatalf("session %d claims %v of margin across %v", i, s.Margin, s.Duration())
		}
		if i > 0 && s.Start.Before(res.Sessions[i-1].End) {
			t.Fatalf("session %d starts %v, before session %d ended at %v",
				i, s.Start, i-1, res.Sessions[i-1].End)
		}
	}
}

func TestClusteringIsDeterministicAndIndependentOfInputOrder(t *testing.T) {
	rng := rand.New(rand.NewSource(11))
	var events []source.Event
	for range 200 {
		start := time.Duration(rng.Intn(60*60*12)) * time.Second
		events = append(events, interval(start, start+time.Duration(rng.Intn(600))*time.Second))
	}
	first := cluster(t, events, params())

	shuffled := append([]source.Event(nil), events...)
	rng.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	second := cluster(t, shuffled, params())

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("clustering the same observations in a different order gave %d sessions then %d",
			len(first.Sessions), len(second.Sessions))
	}
}

func TestClusterDoesNotReorderTheCallersSlice(t *testing.T) {
	events := []source.Event{point(30 * time.Minute), point(0)}
	before := append([]source.Event(nil), events...)

	cluster(t, events, params())

	if !reflect.DeepEqual(events, before) {
		t.Error("Cluster sorted the caller's slice in place")
	}
}

func TestNoObservationsIsNoSessionsRatherThanAnError(t *testing.T) {
	res := cluster(t, nil, params())
	if len(res.Sessions) != 0 {
		t.Errorf("got %d sessions from no observations", len(res.Sessions))
	}
	if res.Total() != 0 {
		t.Errorf("total %v from no observations", res.Total())
	}
}

func TestSessionsNameTheSourcesThatEvidencedThem(t *testing.T) {
	res := cluster(t, []source.Event{point(0), interval(time.Minute, 2*time.Minute)}, params())

	if len(res.Sessions) != 1 {
		t.Fatalf("got %d sessions, want 1", len(res.Sessions))
	}
	if got, want := res.Sessions[0].Sources, []string{"biome-appusage", "fsobserve"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Sources = %v, want %v sorted", got, want)
	}
}

func TestParamsAreValidated(t *testing.T) {
	for _, tc := range []struct {
		name  string
		muton func(*sessions.Params)
	}{
		{"zero gap", func(p *sessions.Params) { p.Gap = 0 }},
		{"negative gap", func(p *sessions.Params) { p.Gap = -time.Minute }},
		{"negative lead-in", func(p *sessions.Params) { p.LeadIn = -time.Second }},
		{"negative tail-out", func(p *sessions.Params) { p.TailOut = -time.Second }},
		{"a source both presence and ignored", func(p *sessions.Params) {
			p.IgnoreSources = []string{"knowledgec-backlit"}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := params()
			tc.muton(&p)
			if _, err := sessions.Cluster([]source.Event{point(0)}, p); err == nil {
				t.Fatalf("Cluster accepted %s", tc.name)
			}
		})
	}
}

func TestTotalNeverExceedsTheWallClockItSpans(t *testing.T) {
	// The invariant that makes these sessions usable as a presence
	// denominator. Summing them must never exceed the span from the first
	// session's start to the last one's end.
	rng := rand.New(rand.NewSource(13))
	var events []source.Event
	for range 500 {
		start := time.Duration(rng.Intn(60*60*36)) * time.Second
		events = append(events, point(start))
	}
	res := cluster(t, events, params())

	span := res.Sessions[len(res.Sessions)-1].End.Sub(res.Sessions[0].Start)
	if res.Total() > span {
		t.Fatalf("sessions total %v across a %v span; wall clock has been double-counted", res.Total(), span)
	}
}

// A sitting with no margin is zero-length — a point observation is one, which
// this package's own span type says in as many words — and whether it survives
// must not depend on the backlight stream having recorded darkness somewhere
// else in the day.
//
// It did. subtract removed a zero-length span from its own output whenever
// there was anything at all to cut, so a lone observation clustered at no
// lead-in vanished with nothing counting it: not TrimmedByAbsence, which is
// zero across no time, and not UnevidencedPieces, which counts pieces that
// were emitted and then dropped. Silent, and in the direction of deleting real
// work.
func TestAPointSittingSurvivesDarknessRecordedElsewhereInTheDay(t *testing.T) {
	p := params()
	p.LeadIn, p.TailOut = 0, 0

	alone := cluster(t, []source.Event{point(0)}, p)
	if len(alone.Sessions) != 1 {
		t.Fatalf("a lone point observation produced %d sittings, want 1: %v", len(alone.Sessions), spans(alone))
	}

	withDark := cluster(t, []source.Event{point(0), backlit(5*time.Hour, 6*time.Hour, "off")}, p)
	if len(withDark.Sessions) != 1 {
		t.Fatalf("darkness five hours later left %d sittings, want the same 1: %v (trimmed %v, unevidenced %d)",
			len(withDark.Sessions), spans(withDark),
			withDark.Stats.TrimmedByAbsence, withDark.Stats.UnevidencedPieces)
	}
}
