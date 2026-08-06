package dayview_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// A day reviewed from its OBSERVATIONS, with the model's answer off the screen.
//
// The defect this answers is measured. Over the fourteen presence-covered
// reviewed days the residual at the shipped 15m/5m/5m is exactly 0.000h and
// contradicted is 0.000h at every point in the grid, because a reviewer
// confirming blocks he was SHOWN produces labels that are a strict subset of
// the derivation. See project/private/2026-08-02-a-fitter-that-reports-an-interval.md.

// blindDay assembles the reference day from evidence alone.
func blindDay(t *testing.T, events ...source.Event) dayview.Day {
	t.Helper()
	day, err := dayview.Assemble(cal, theDay, derived(), events, nil, nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return day
}

// A silence is the hole between one observation's reach and the next one's
// start. Points abut nothing, so a day of points is almost all silence.
func TestSilencesAreTheHolesBetweenObservations(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		evidence("acorn", "transcripts", 9, 1),
		evidence("acorn", "transcripts", 12, 0))

	got := day.Silences(at(29, 0), 0)
	want := []timespan.Span{
		span(5, 0, 9, 0),   // the day's start to the first observation
		span(9, 0, 9, 1),   // one minute
		span(9, 1, 12, 0),  // the long one
		span(12, 0, 29, 0), // the last observation to the horizon
	}
	if len(got) != len(want) {
		t.Fatalf("Silences = %v, want %v", got, want)
	}
	for i := range want {
		if !got[i].Start.Equal(want[i].Start) || !got[i].End.Equal(want[i].End) {
			t.Errorf("silence %d = %s..%s, want %s..%s", i,
				got[i].Start.Format(time.TimeOnly), got[i].End.Format(time.TimeOnly),
				want[i].Start.Format(time.TimeOnly), want[i].End.Format(time.TimeOnly))
		}
	}
}

// An interval swallowing later points does not manufacture a silence inside
// itself. The running reach is what the census is built from, not the previous
// row's own end.
func TestAnIntervalDoesNotManufactureASilenceInsideItself(t *testing.T) {
	long := evidence("acorn", "biome", 9, 0)
	long.End = at(11, 0)
	long.Shape = source.Interval
	day := blindDay(t, long, evidence("acorn", "transcripts", 10, 0))

	for _, s := range day.Silences(at(29, 0), 0) {
		if s.Start.Before(at(11, 0)) && s.End.After(at(9, 0)) {
			t.Errorf("a silence %s..%s was reported inside an observed interval 09:00..11:00",
				s.Start.Format(time.TimeOnly), s.End.Format(time.TimeOnly))
		}
	}
}

func TestASilenceFloorLeavesOutTheShortOnesAndNothingElse(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		evidence("acorn", "transcripts", 9, 1),
		evidence("acorn", "transcripts", 12, 0))

	got := day.Silences(at(29, 0), 2*time.Minute)
	for _, s := range got {
		if s.Duration() < 2*time.Minute {
			t.Errorf("a %v silence survived a 2m floor", s.Duration())
		}
	}
	if len(got) != 3 {
		t.Errorf("Silences at a 2m floor = %d, want 3 of the 4", len(got))
	}
}

// THE LOAD-BEARING PROPERTY. The census is what makes a blind pass defensible:
// it is a reduction of the raw evidence that carries no clustering parameter,
// so the same observations produce the same questions whatever is being fitted.
func TestTheCensusIsTheSameAtEveryParameterValue(t *testing.T) {
	events := []source.Event{
		evidence("acorn", "transcripts", 9, 0),
		evidence("acorn", "transcripts", 9, 20),
		evidence("acorn", "transcripts", 14, 0),
	}
	var first []timespan.Span
	for _, gap := range []time.Duration{2 * time.Minute, 15 * time.Minute, time.Hour} {
		for _, margin := range []time.Duration{0, 5 * time.Minute, 30 * time.Minute} {
			p := params
			p.Gap, p.LeadIn, p.TailOut = gap, margin, margin
			day, err := dayview.Assemble(cal, theDay,
				sittings.Derived{Params: p, ComputedAt: at(29, 0)}, events, nil, nil)
			if err != nil {
				t.Fatalf("Assemble at gap %v margin %v: %v", gap, margin, err)
			}
			got := day.Silences(at(29, 0), 0)
			if first == nil {
				first = got
				continue
			}
			if len(got) != len(first) {
				t.Fatalf("gap %v margin %v gives %d silences; gap %v gives %d — the census "+
					"moved with a parameter it must not depend on",
					gap, margin, len(got), 2*time.Minute, len(first))
			}
			for i := range got {
				if !got[i].Start.Equal(first[i].Start) || !got[i].End.Equal(first[i].End) {
					t.Fatalf("silence %d moved to %s..%s at gap %v margin %v", i,
						got[i].Start.Format(time.TimeOnly), got[i].End.Format(time.TimeOnly), gap, margin)
				}
			}
		}
	}
}

// An account is a CLOSED accounting of the silences it was asked about, and an
// OPEN one everywhere else. A silence nobody was asked about gets no row: that
// is the difference between this and a floor, which would be a gap threshold
// wearing a display's clothes.
func TestAnAccountAssertsNothingOverASilenceNobodyWasAsked(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		evidence("acorn", "transcripts", 9, 1),
		evidence("acorn", "transcripts", 12, 0))
	day.Reach = reach(at(29, 0), at(29, 0))

	plan, err := day.Account(dayview.Account{
		Worked: []timespan.Span{span(9, 1, 12, 0)},
		Origin: "ben", At: at(30, 0),
	})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	// INTERIOR overlap, not timespan.Overlaps, which is closed at the endpoints
	// on purpose so that a zero-length observation can touch a cell. A worked
	// silence beginning exactly where the unasked one ends shares that instant
	// and covers none of it.
	unasked := span(9, 0, 9, 1)
	for _, a := range plan.Assertions {
		both := timespan.Intersect([]timespan.Span{{Start: a.Start, End: a.End}}, []timespan.Span{unasked})
		for _, s := range both {
			if s.Duration() > 0 {
				t.Errorf("%s asserted over %s..%s, covering %v of a silence nobody was asked about",
					a.Assertion, a.Start.Format(time.TimeOnly), a.End.Format(time.TimeOnly), s.Duration())
			}
		}
	}
}

func TestAnAccountAssertsWhatTheReviewerSaidAndNothingFromTheBlocks(t *testing.T) {
	// A derivation that says the whole morning was one sitting, which this
	// answer contradicts. It must not reach the assertions at all.
	day, err := dayview.Assemble(cal, theDay,
		derived(sitting(8, 0, 13, 0, 3, "transcripts")),
		[]source.Event{
			evidence("acorn", "transcripts", 9, 0),
			evidence("acorn", "transcripts", 12, 0),
		}, nil, nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	day.Reach = reach(at(29, 0), at(29, 0))

	plan, err := day.Account(dayview.Account{
		Worked: []timespan.Span{span(9, 0, 10, 0)},
		Broke:  []timespan.Span{span(10, 0, 12, 0)},
		Origin: "ben", At: at(30, 0),
	})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	var engaged, notEngaged []timespan.Span
	for _, a := range plan.Assertions {
		switch a.Assertion {
		case adjustments.Engaged:
			engaged = append(engaged, timespan.Span{Start: a.Start, End: a.End})
		case adjustments.NotEngaged:
			notEngaged = append(notEngaged, timespan.Span{Start: a.Start, End: a.End})
		default:
			t.Errorf("an account produced a %s assertion", a.Assertion)
		}
	}
	if len(engaged) != 1 || !engaged[0].Start.Equal(at(9, 0)) || !engaged[0].End.Equal(at(10, 0)) {
		t.Errorf("engaged = %v, want 09:00..10:00 alone", engaged)
	}
	if len(notEngaged) != 1 || !notEngaged[0].Start.Equal(at(10, 0)) || !notEngaged[0].End.Equal(at(12, 0)) {
		t.Errorf("not-engaged = %v, want 10:00..12:00 alone", notEngaged)
	}
}

// Consecutive silences abut at the instant they share, so a reviewer who worked
// through several in a row states ONE stretch of work rather than a row per
// observation. On a points-only day this is the whole of how a work span comes
// to have any duration at all.
func TestAdjacentWorkedSilencesBecomeOneAssertion(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		evidence("acorn", "transcripts", 10, 0),
		evidence("acorn", "transcripts", 11, 0))
	day.Reach = reach(at(29, 0), at(29, 0))

	plan, err := day.Account(dayview.Account{
		Worked: []timespan.Span{span(9, 0, 10, 0), span(10, 0, 11, 0)},
		Origin: "ben", At: at(30, 0),
	})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if len(plan.Assertions) != 1 {
		t.Fatalf("%d assertions, want one 09:00..11:00: %v", len(plan.Assertions), plan.Assertions)
	}
	if !plan.Assertions[0].Start.Equal(at(9, 0)) || !plan.Assertions[0].End.Equal(at(11, 0)) {
		t.Errorf("engaged %s..%s, want 09:00..11:00",
			plan.Assertions[0].Start.Format(time.TimeOnly), plan.Assertions[0].End.Format(time.TimeOnly))
	}
}

// A silence the reviewer looked at and could not place asserts NOTHING, and
// must not fall through into either answer.
func TestAForgottenSilenceIsAssertedNeitherWay(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		evidence("acorn", "transcripts", 12, 0))
	day.Reach = reach(at(29, 0), at(29, 0))

	plan, err := day.Account(dayview.Account{
		Forgot: []timespan.Span{span(9, 0, 12, 0)},
		Origin: "ben", At: at(30, 0),
	})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if len(plan.Assertions) != 0 {
		t.Errorf("a forgotten silence produced %v", plan.Assertions)
	}
	if plan.Review.Verdict != adjustments.Recalled {
		t.Errorf("verdict = %q; the reviewer recalled the day and not one silence in it",
			plan.Review.Verdict)
	}
}

// THE ROW THAT TELLS THE TWO POPULATIONS APART. A label formed against the
// observations and a label formed by agreeing with the derivation are worth
// completely different amounts to a fit, and a fitter that cannot separate them
// will pool them and report a precision it has not got.
func TestAnAccountRecordsThatTheReviewerSawTheObservations(t *testing.T) {
	day := blindDay(t, evidence("acorn", "transcripts", 9, 0))
	day.Reach = reach(at(29, 0), at(29, 0))
	plan, err := day.Account(dayview.Account{Origin: "ben", At: at(30, 0)})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if plan.Review.Shown != adjustments.Observations {
		t.Errorf("Shown = %q, want %q", plan.Review.Shown, adjustments.Observations)
	}
}

func TestAConfirmationRecordsThatTheReviewerSawTheDerivation(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	plan, err := day.Confirm(dayview.Answer{Mode: dayview.Wholesale, Origin: "ben", At: at(30, 0)})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if plan.Review.Shown != adjustments.Derivation {
		t.Errorf("Shown = %q, want %q", plan.Review.Shown, adjustments.Derivation)
	}
}

// An account may not reach past the horizon, for exactly the reason a
// confirmation may not: beyond it nothing has looked, so a not-engaged row
// there records the store's silence as the world's.
func TestAnAccountIsRefusedPastTheHorizon(t *testing.T) {
	day := blindDay(t, evidence("acorn", "transcripts", 9, 0))
	day.Reach = reach(at(16, 39), at(17, 55))

	_, err := day.Account(dayview.Account{
		Broke:  []timespan.Span{span(17, 0, 18, 0)},
		Origin: "ben", At: at(18, 30),
	})
	if err == nil {
		t.Fatal("an account reaching past the horizon was accepted")
	}
}

func TestAnAccountRefusesOverlappingAnswers(t *testing.T) {
	day := blindDay(t, evidence("acorn", "transcripts", 9, 0))
	day.Reach = reach(at(29, 0), at(29, 0))
	_, err := day.Account(dayview.Account{
		Worked: []timespan.Span{span(9, 0, 12, 0)},
		Broke:  []timespan.Span{span(11, 0, 13, 0)},
		Origin: "ben", At: at(30, 0),
	})
	if err == nil {
		t.Fatal("an account claiming to have both worked and stopped over 11:00..12:00 was accepted")
	}
}

// An OBSERVED stretch the reviewer says was not work. It has to be sayable —
// this project's own rule is that observed-not-X must be recordable — and on
// 2026-08-01 it is what actually happened: Ben rejected four blocks carrying
// 2,017 observations between them.
func TestAnObservedStretchCanBeDeniedAsWork(t *testing.T) {
	seen := evidence("acorn", "biome", 9, 0)
	seen.End, seen.Shape = at(11, 0), source.Interval
	day := blindDay(t, seen)
	day.Reach = reach(at(29, 0), at(29, 0))

	plan, err := day.Account(dayview.Account{
		NotWork: []timespan.Span{span(9, 0, 11, 0)},
		Origin:  "ben", At: at(30, 0),
	})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if len(plan.Assertions) != 1 || plan.Assertions[0].Assertion != adjustments.NotEngaged {
		t.Fatalf("assertions = %v, want one not-engaged over 09:00..11:00", plan.Assertions)
	}
}

// An observed stretch nobody denied is work. That assumption is the tool's
// premise and NOT one of the parameters being fitted, which is what makes it
// safe to hold while the gap, the lead-in and the tail-out are all off the
// screen.
func TestAnObservedStretchNobodyDeniedIsWork(t *testing.T) {
	seen := evidence("acorn", "biome", 9, 0)
	seen.End, seen.Shape = at(11, 0), source.Interval
	day := blindDay(t, seen)
	day.Reach = reach(at(29, 0), at(29, 0))

	plan, err := day.Account(dayview.Account{Origin: "ben", At: at(30, 0)})
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if len(plan.Assertions) != 1 || plan.Assertions[0].Assertion != adjustments.Engaged {
		t.Fatalf("assertions = %v, want one engaged over 09:00..11:00", plan.Assertions)
	}
}

// Assemble reads a day WITHOUT a derivation for a blind pass, so the rows and
// the observations have to survive a zero Params. Skeleton's guard returns
// early on an underived day and this is the case that must not.
func TestADayIsObservableWithoutADerivation(t *testing.T) {
	day, err := dayview.Assemble(cal, theDay,
		sittings.Derived{Params: params, ComputedAt: time.Time{}},
		[]source.Event{evidence("acorn", "transcripts", 9, 0)}, nil, nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if len(day.Observed) == 0 {
		t.Error("a day with no derivation reported no observations; the evidence is not " +
			"the model's and does not depend on it having run")
	}
}

var _ = sessions.Day{}
