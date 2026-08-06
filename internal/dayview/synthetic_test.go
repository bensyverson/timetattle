package dayview_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/storefixture"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// The properties of realdata_test.go, taken over a store this test built.
//
// They sit BESIDE the live checks rather than instead of them, which is this
// project's rule. What each half can do is different, and neither is the other:
//
//   - The live day can show that the closure survives 400-odd real
//     observations across five sources at odd minutes, which no fixture can
//     manufacture.
//   - This half runs ON ANY MACHINE. The live half does not: when the store is
//     absent, or a migration lands before the next harvest, every one of those
//     tests SKIPS and the suite stays green. That fired twice on 2026-08-02 —
//     a pseudonym rename skipped eight tests, and migration 0011 against a
//     store at 0010 skipped several more — and on both days the properties
//     below were being asserted by nobody at all.
//
// The corpus is internal/storefixture. Its concurrent day carries three
// projects inside one lit stretch, which is the shape 2026-07-28 has and a
// single-project fixture cannot pose.

// syntheticDay assembles one working day out of the fixture store, the same way
// cmd/timetattle's review does: the derivation, the events with a margin either
// side so a project's own clustering is not truncated at the boundary, and
// whatever has been recorded over the day.
func syntheticDay(t *testing.T, f *storefixture.Fixture, day sessions.Day) (dayview.Day, []source.Event) {
	t.Helper()
	ctx := context.Background()
	start, end := f.Calendar.Bounds(day)

	derivation, err := f.Store.Sittings(ctx, start, end)
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if derivation.ComputedAt.IsZero() {
		t.Fatal("the fixture wrote no derivation")
	}
	pad := derivation.Params.Gap + derivation.Params.LeadIn + derivation.Params.TailOut
	events, err := f.Store.Events(ctx, store.Filter{From: start.Add(-pad), To: end.Add(pad)})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	reviews, err := f.Store.Reviews(ctx, start, end)
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	adjs, err := f.Store.Adjustments(ctx, start, end)
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	assembled, err := dayview.Assemble(f.Calendar, day, derivation, events, reviews, adjs)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return assembled, events
}

func TestASyntheticDayReconstructsAcrossEverySourceThatSawIt(t *testing.T) {
	f := storefixture.New(t)
	day, _ := syntheticDay(t, f, f.ConcurrentDay)
	if len(day.Blocks) == 0 {
		t.Fatal("the concurrent day has no blocks")
	}
	sources := map[string]bool{}
	for _, b := range day.Blocks {
		for _, s := range b.Sources {
			sources[s] = true
		}
	}
	if len(sources) < 2 {
		t.Errorf("the day rests on %d source, so it is not a reconstruction ACROSS sources", len(sources))
	}
}

func TestTheSyntheticConcurrentDayGetsMoreThanOneRow(t *testing.T) {
	f := storefixture.New(t)
	day, _ := syntheticDay(t, f, f.ConcurrentDay)
	if len(day.Rows) < 2 {
		t.Errorf("%d rows on the day built to be concurrent; the layout is not making it visible",
			len(day.Rows))
	}
}

// No row may claim more clock than the day's own sittings cover. A row is a
// reading of the blocks and never a second source of engaged time; the failure
// this guards is the one internal/metrics measured, where model A summed 24.01h
// across a 12.78h day.
func TestNoSyntheticRowClaimsMoreClockThanTheDayHas(t *testing.T) {
	f := storefixture.New(t)
	for _, d := range []sessions.Day{f.PlainDay, f.ConcurrentDay, f.DarknessDay, f.PointsOnlyDay, f.BetweenDay} {
		day, _ := syntheticDay(t, f, d)
		for _, r := range day.Rows {
			if got := timespan.Total(r.Active); got > day.Engaged() {
				t.Errorf("%s: %s claims %v of a day with %v of engaged time",
					d, r.Name(), got, day.Engaged())
			}
		}
	}
}

// THE CLOSURE. Confirming a day wholesale must account for every instant of it,
// exactly once, to the microsecond — or the residual fitted against it is
// computed over clock nobody asserted.
func TestConfirmingASyntheticDayAccountsForEveryInstantOfIt(t *testing.T) {
	f := storefixture.New(t)
	for _, d := range []sessions.Day{f.PlainDay, f.ConcurrentDay, f.DarknessDay, f.PointsOnlyDay, f.BetweenDay} {
		day, _ := syntheticDay(t, f, d)
		plan, err := day.Confirm(dayview.Answer{
			Mode: dayview.Wholesale, Origin: "a-test", At: time.Date(2025, time.March, 10, 12, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("%s: Confirm: %v", d, err)
		}
		var all []timespan.Span
		for _, a := range plan.Assertions {
			if err := a.Validate(); err != nil {
				t.Errorf("%s: the plan produced an assertion the store would refuse: %v", d, err)
			}
			if a.Assertion == adjustments.Engaged || a.Assertion == adjustments.NotEngaged {
				all = append(all, timespan.Span{Start: a.Start, End: a.End})
			}
		}
		if got, want := timespan.Total(all), day.Length(); got != want {
			t.Errorf("%s: the assertions cover %v of a %v day, a shortfall of %v", d, got, want, want-got)
		}
		if got := timespan.Total(timespan.Union(all)); got != day.Length() {
			t.Errorf("%s: the assertions union to %v against %v of day, so two of them overlap",
				d, got, day.Length())
		}
	}
}

// Every row of an assembled day is weighed, and none comes back Unknown. A row
// exists only because an observation does, so a row reaching the screen
// unweighed would render as strong because nothing decided otherwise.
func TestEveryRowOfASyntheticDayIsWeighed(t *testing.T) {
	f := storefixture.New(t)
	day, _ := syntheticDay(t, f, f.ConcurrentDay)
	if len(day.Rows) == 0 {
		t.Fatal("no rows on the concurrent day; there is nothing to weigh")
	}
	if !day.Evidence {
		t.Error("a day assembled from its events says its evidence was not read")
	}
	for _, r := range day.Rows {
		if r.Strength == dayview.Unknown {
			t.Errorf("%s reached the day screen unweighed", r.Name())
		}
	}
}

// The queue and the day screen read ONE rule through one function. A listing
// that disagreed with the screen it leads to would send a reviewer looking for
// a mark that is not there.
func TestWeighAgreesWithASyntheticDaysRows(t *testing.T) {
	f := storefixture.New(t)
	day, events := syntheticDay(t, f, f.ConcurrentDay)
	weighed := dayview.Weigh(f.Calendar, day.Day, day.Params, events)
	if len(weighed) != len(day.Rows) {
		t.Fatalf("Weigh found %d projects and the day has %d rows", len(weighed), len(day.Rows))
	}
	for _, r := range day.Rows {
		if got := weighed[r.Project]; got != r.Strength {
			t.Errorf("%s: the queue would say %q and the day screen says %q", r.Name(), got, r.Strength)
		}
	}
}

// THE SAME CLOSURE, REVIEWED AS IF THE DAY WERE STILL HAPPENING. Everything
// before the moment of review is accounted for exactly, and nothing after it is
// asserted at all.
func TestConfirmingASyntheticDayStillRunningAssertsNothingAboutTheRestOfIt(t *testing.T) {
	f := storefixture.New(t)
	day, _ := syntheticDay(t, f, f.ConcurrentDay)
	// Inside the run of blocks rather than politely between two of them: the
	// concurrent day's second sitting runs 13:25..17:05 UTC and the day begins
	// at 05:00, so eight and a half hours in lands at 13:30.
	moment := day.Start.Add(8*time.Hour + 30*time.Minute)
	plan, err := day.Confirm(dayview.Answer{Mode: dayview.Wholesale, Origin: "a-test", At: moment})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	var all []timespan.Span
	for _, a := range plan.Assertions {
		if err := a.Validate(); err != nil {
			t.Errorf("the plan produced an assertion the store would refuse: %v", err)
		}
		if a.Assertion == adjustments.NotEngaged && a.End.After(moment) {
			t.Errorf("not-engaged is asserted over %s..%s, past the %s this review was recorded at",
				a.Start.Format("15:04"), a.End.Format("15:04"), moment.Format("15:04"))
		}
		if a.Assertion == adjustments.Engaged || a.Assertion == adjustments.NotEngaged {
			all = append(all, timespan.Span{Start: a.Start, End: a.End})
		}
	}

	examined := plan.Review.End
	if examined.Before(moment) {
		t.Fatalf("the review covers only to %s, before the %s it was recorded at",
			examined.Format("15:04"), moment.Format("15:04"))
	}
	if examined.Equal(day.End) {
		t.Fatal("the examined extent reached the end of the working day, so this test cut nothing")
	}
	if got, want := timespan.Total(all), examined.Sub(day.Start); got != want {
		t.Errorf("the assertions cover %v of the %v examined, a shortfall of %v", got, want, want-got)
	}
}

// A day of isolated saves and nothing else must not disappear. That regime
// holds 75.7% of engaged time in the real corpus, and it is exactly the one a
// hand-written interval fixture never poses.
func TestAPointsOnlySyntheticDayStillReachesTheScreen(t *testing.T) {
	f := storefixture.New(t)
	day, _ := syntheticDay(t, f, f.PointsOnlyDay)
	if len(day.Blocks) == 0 {
		t.Fatal("a day of saves produced no blocks at all")
	}
	if day.Engaged() <= 0 {
		t.Errorf("a day of saves scored %v", day.Engaged())
	}
	// Every minute of it is inferred: there is no duration-bearing evidence
	// on this day, so the margin is the whole of it.
	if day.Margin() != day.Engaged() {
		t.Errorf("a points-only day reports %v engaged of which %v is inferred; "+
			"with no interval evidence the two must be equal", day.Engaged(), day.Margin())
	}
}

// A day the backlight went dark in the middle of. The block ends where the
// observation was still running, which nothing but presence can do.
func TestObservedDarknessCutsABlockShortOnASyntheticDay(t *testing.T) {
	f := storefixture.New(t)
	day, events := syntheticDay(t, f, f.DarknessDay)
	if len(day.Blocks) == 0 {
		t.Fatal("the darkness day produced no blocks")
	}
	cut := false
	for _, b := range day.Blocks {
		for _, e := range events {
			if e.Source != storefixture.EvidenceSource || e.Shape != source.Interval {
				continue
			}
			if e.Start.Before(b.Span.End) && b.Span.End.Before(e.End) {
				cut = true
			}
		}
	}
	if !cut {
		t.Error("no block ends inside an interval that was still being observed; " +
			"the darkness did not reach the assembled day")
	}
}

// The blind pass's properties, over a corpus this test built.
//
// blind_realdata_test.go asserts the same two things about 2026-03-21 and
// 2026-07-28. What it cannot do is run anywhere: when the live store is absent
// it skips, and a whole presentation's soundness argument is then being checked
// by nobody. The fixture carries both regimes on purpose — a day with no
// presence reading at all, and a day the screen went dark in the middle of an
// observed stretch — so the argument is checked on every machine as well as on
// the one the days came from.

// syntheticObserved assembles a day from its EVIDENCE alone, reading no review
// and no correction, which is what these cases are about.
func syntheticObserved(t *testing.T, f *storefixture.Fixture, d sessions.Day) (dayview.Day, []source.Event) {
	t.Helper()
	ctx := context.Background()
	start, end := f.Calendar.Bounds(d)
	derivation, err := f.Store.Sittings(ctx, start, end)
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	pad := derivation.Params.Gap + derivation.Params.LeadIn + derivation.Params.TailOut
	events, err := f.Store.Events(ctx, store.Filter{From: start.Add(-pad), To: end.Add(pad)})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	built, err := dayview.Assemble(f.Calendar, d, derivation, events, nil, nil)
	if err != nil {
		t.Fatalf("Assemble %s: %v", d, err)
	}
	return built, events
}

// In the points-only regime there is no presence reading, so there is no
// darkness to cut anything: every boundary the clustering can place comes from
// a silence, and the parameter-free census is therefore the complete space of
// boundaries with none of them chosen.
func TestOnASyntheticPointsOnlyDayEveryBoundaryFallsInsideASilence(t *testing.T) {
	f := storefixture.New(t)
	day, events := syntheticObserved(t, f, f.PointsOnlyDay)
	for _, e := range events {
		if e.Source == storefixture.PresenceSource {
			t.Fatalf("%s carries a presence reading; this case's whole premise is that the "+
				"points-only regime has none", f.PointsOnlyDay)
		}
	}

	silences := day.Silences(day.End, 0)
	var checked int
	for _, p := range gridPoints(day.Params) {
		for _, seam := range seams(t, events, p, day) {
			checked++
			if !insideAny(seam, silences) {
				t.Errorf("at gap %v the clustering placed a boundary across %s..%s, which the "+
					"parameter-free census does not report as silent — so the census is NOT the "+
					"space of boundaries and the blind presentation is unsound for this regime",
					p.Gap, seam.Start.Format(time.RFC3339), seam.End.Format(time.RFC3339))
			}
		}
	}
	if checked == 0 {
		t.Fatalf("%s produced no internal boundary at any parameter value; this case checked nothing",
			f.PointsOnlyDay)
	}
}

// Where presence covers the day, a boundary can also be placed by observed
// DARKNESS, which sits wherever the screen went off and not in a silence. This
// does not assert that no such boundary exists — it asserts that every one of
// them is explained by a presence reading, so the exception stays bounded and
// named rather than open-ended.
func TestOnASyntheticPresenceCoveredDayTheOnlyExceptionsAreObservedDarkness(t *testing.T) {
	f := storefixture.New(t)
	for _, d := range []sessions.Day{f.DarknessDay, f.ConcurrentDay} {
		day, events := syntheticObserved(t, f, d)
		silences := day.Silences(day.End, 0)

		var edges []time.Time
		for _, e := range events {
			if e.Source != storefixture.PresenceSource {
				continue
			}
			for _, at := range []time.Time{e.Start, e.End} {
				if !at.IsZero() {
					edges = append(edges, at)
				}
			}
		}
		if len(edges) == 0 {
			t.Fatalf("%s carries no presence reading, so there is no exception to bound", d)
		}

		var checked int
		for _, p := range gridPoints(day.Params) {
			for _, seam := range seams(t, events, p, day) {
				checked++
				if coveredBy(seam, silences) {
					continue
				}
				if !touchesAny(seam.Start.Add(p.TailOut), edges) && !touchesAny(seam.End.Add(-p.LeadIn), edges) {
					t.Errorf("%s: at gap %v a boundary across %s..%s is neither silent nor at a "+
						"presence reading; the exception to the census is wider than observed darkness",
						d, p.Gap, seam.Start.Format(time.RFC3339), seam.End.Format(time.RFC3339))
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s produced no internal boundary at any parameter value", d)
		}
	}
}

// The census does not move with the parameters. It is what makes the blind
// screen parameter-free rather than merely parameter-quiet.
func TestTheSyntheticCensusIsTheSameAtEveryParameterValue(t *testing.T) {
	f := storefixture.New(t)
	day, events := syntheticObserved(t, f, f.ConcurrentDay)

	var first []timespan.Span
	for _, gap := range []time.Duration{2 * time.Minute, 15 * time.Minute, time.Hour} {
		p := day.Params
		p.Gap, p.LeadIn, p.TailOut = gap, gap/3, gap/3
		built, err := dayview.Assemble(f.Calendar, day.Day,
			sittings.Derived{Params: p, ComputedAt: day.End}, events, nil, nil)
		if err != nil {
			t.Fatalf("Assemble at gap %v: %v", gap, err)
		}
		got := built.Silences(built.End, 2*time.Minute)
		if first == nil {
			first = got
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("gap %v gives %d silences of 2m or more, want the %d every other "+
				"parameter value gives", gap, len(got), len(first))
		}
		for i := range got {
			if !got[i].Start.Equal(first[i].Start) || !got[i].End.Equal(first[i].End) {
				t.Fatalf("silence %d moved to %s..%s at gap %v", i,
					got[i].Start.Format(time.RFC3339), got[i].End.Format(time.RFC3339), gap)
			}
		}
	}
	if len(first) == 0 {
		t.Fatal("the concurrent day reports no silence of 2m or more; this case compared nothing")
	}
}

// The census and the observations tile the day exactly. A census that lost time
// would hide stretches nobody could ever be asked about.
func TestTheSyntheticCensusAndTheObservationsAccountForTheWholeDay(t *testing.T) {
	f := storefixture.New(t)
	for _, d := range []sessions.Day{f.PlainDay, f.ConcurrentDay, f.DarknessDay, f.PointsOnlyDay, f.BetweenDay} {
		day, _ := syntheticObserved(t, f, d)
		covered := timespan.Union(append(day.Silences(day.End, 0), day.Observed...))
		left := timespan.Subtract([]timespan.Span{{Start: day.Start, End: day.End}}, covered)
		var lost time.Duration
		for _, s := range left {
			lost += s.Duration()
		}
		if lost > 0 {
			t.Errorf("on %s, %v of the working day is neither an observation nor a silence; "+
				"a reviewer could never be asked about it and nothing would say so", d, lost)
		}
	}
}
