//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package dayview_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// These run against the live store and skip cleanly when it is not there,
// following internal/metrics/realdata_test.go.
//
// They sit beside the constructed cases rather than instead of them, which is
// this project's rule and has been earned twice: an over-general supersession
// rule once passed against the entire live Biome stream purely because no real
// interval happened to exercise it. A fixture cannot establish that the closure
// survives a real day of 400-odd observations across five sources, and a real
// day cannot be made to contain an unremembered block on demand.
//
// The window is the working day 2026-07-28, which
// project/private/2026-07-31-concurrent-project-validation.md documents as the one
// genuinely concurrent day in the corpus. What is asserted are PROPERTIES and
// not figures: a fixed number would break on the next harvest, and the
// properties are what must hold whatever the store holds.

var (
	realOnce   sync.Once
	realDay    dayview.Day
	realEvents []source.Event
	realWhy    string
)

// liveEvents is the evidence liveDay was assembled from, margin and all: what a
// caller weighing the same day without assembling it would hold.
func liveEvents(t *testing.T) []source.Event {
	t.Helper()
	liveDay(t)
	return realEvents
}

func liveDay(t *testing.T) dayview.Day {
	t.Helper()
	realOnce.Do(func() {
		path, err := store.DefaultPath()
		if err != nil {
			realWhy = "no home directory: " + err.Error()
			return
		}
		if _, err := os.Stat(path); err != nil {
			realWhy = "cannot stat " + path + ": " + err.Error()
			return
		}
		st, err := store.OpenReadOnly(path)
		if err != nil {
			realWhy = "cannot read " + path + ": " + err.Error()
			return
		}
		defer st.Close()

		cfg := config.Default()
		liveCal, err := cfg.Calendar.Sessions()
		if err != nil {
			realWhy = "the default calendar will not load: " + err.Error()
			return
		}
		day := sessions.Day{Year: 2026, Month: time.July, Date: 28}
		start, end := liveCal.Bounds(day)

		ctx := context.Background()
		derivation, err := st.Sittings(ctx, start, end)
		if err != nil {
			realWhy = "cannot read the sittings: " + err.Error()
			return
		}
		if derivation.ComputedAt.IsZero() {
			// Sittings was asked for 2026-07-28 alone, so this observes that
			// window and nothing wider. "Nothing has been sessionized in the
			// live store" was a claim about the whole store drawn from one
			// day's read -- the clipped-window mistake this project has
			// logged twice.
			realWhy = "the live store holds no derivation covering " + start.Format(time.DateOnly)
			return
		}
		// A margin either side, so a project's own clustering is not truncated
		// at the day boundary. The same pad cmd/timetattle's review uses.
		pad := derivation.Params.Gap + derivation.Params.LeadIn + derivation.Params.TailOut
		events, err := st.Events(ctx, store.Filter{From: start.Add(-pad), To: end.Add(pad)})
		if err != nil {
			realWhy = "cannot read the events: " + err.Error()
			return
		}
		realEvents = events
		reviews, err := st.Reviews(ctx, start, end)
		if err != nil {
			realWhy = "cannot read the reviews: " + err.Error()
			return
		}
		adjs, err := st.Adjustments(ctx, start, end)
		if err != nil {
			realWhy = "cannot read the corrections: " + err.Error()
			return
		}
		realDay, err = dayview.Assemble(liveCal, day, derivation, events, reviews, adjs)
		if err != nil {
			realWhy = "cannot assemble the day: " + err.Error()
		}
	})
	if realWhy != "" {
		t.Skip(realWhy)
	}
	return realDay
}

func TestARealDayReconstructsAcrossEverySourceThatSawIt(t *testing.T) {
	day := liveDay(t)
	if len(day.Blocks) == 0 {
		t.Fatal("the working day 2026-07-28 has no blocks; either the store has moved on or " +
			"the day is being windowed wrong")
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
	t.Logf("2026-07-28: %d blocks, %v engaged, %v inferred, %d rows, %d sources",
		len(day.Blocks), day.Engaged().Round(time.Minute), day.Margin().Round(time.Minute),
		len(day.Rows), len(sources))
}

// The documented concurrent day has to come out as more than one row, or the
// layout chosen to make concurrency visible is not making it visible.
func TestTheConcurrentDayGetsMoreThanOneRow(t *testing.T) {
	day := liveDay(t)
	if len(day.Rows) < 2 {
		t.Errorf("%d rows on the day documented as concurrent", len(day.Rows))
	}
	for _, r := range day.Rows {
		t.Logf("  %-16s %4d obs  active %v", r.Name(), r.Events,
			timespan.Total(r.Active).Round(time.Minute))
	}
}

// No row may claim more clock than the day's own sittings cover. A row is a
// reading of the blocks and never a second source of engaged time, and the
// failure this guards is the one internal/metrics measured: model A summed
// 24.01h across a 12.78h day.
func TestNoRowClaimsMoreClockThanTheDayHas(t *testing.T) {
	day := liveDay(t)
	for _, r := range day.Rows {
		if got := timespan.Total(r.Active); got > day.Engaged() {
			t.Errorf("%s claims %v of a day with %v of engaged time", r.Name(), got, day.Engaged())
		}
	}
}

// THE CLOSURE, ON A REAL DAY. Constructed blocks are tidy; a real day has nine
// of them at odd minutes, and the property has to hold to the microsecond or
// the residual PkY6I fits against is computed over clock nobody asserted.
func TestConfirmingARealDayAccountsForEveryInstantOfIt(t *testing.T) {
	day := liveDay(t)
	plan, err := day.Confirm(dayview.Answer{
		Mode: dayview.Wholesale, Origin: "a-test", At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	var all []timespan.Span
	for _, a := range plan.Assertions {
		if err := a.Validate(); err != nil {
			t.Errorf("the plan produced an assertion the store would refuse: %v", err)
		}
		if a.Assertion == adjustments.Engaged || a.Assertion == adjustments.NotEngaged {
			all = append(all, timespan.Span{Start: a.Start, End: a.End})
		}
	}
	if got, want := timespan.Total(all), day.Length(); got != want {
		t.Errorf("the assertions cover %v of a %v day, a shortfall of %v", got, want, want-got)
	}
	if got := timespan.Total(timespan.Union(all)); got != day.Length() {
		t.Errorf("the assertions union to %v against %v of day, so two of them overlap",
			got, day.Length())
	}
	t.Logf("confirming 2026-07-28 wholesale writes 1 review and %d assertions, "+
		"pinning %d boundaries", len(plan.Assertions), 2*len(day.Blocks))
}

// TASK KrfMh ON REAL DATA. Every row of a real day is weighed, and none of them
// comes back Unknown.
//
// Unknown is a real state and it belongs to a day nobody read the evidence for
// — see Skeleton. A row exists only because an observation does, so a row that
// reached the screen unweighed would be a row rendering as strong because
// nothing decided otherwise, which is the exact shape of this project's
// cardinal bug.
func TestEveryRowOfARealDayIsWeighed(t *testing.T) {
	day := liveDay(t)
	if len(day.Rows) == 0 {
		t.Fatal("no rows on 2026-07-28; there is nothing to weigh")
	}
	if !day.Evidence {
		t.Error("a day assembled from its events says its evidence was not read")
	}
	for _, r := range day.Rows {
		if r.Strength == dayview.Unknown {
			t.Errorf("%s reached the day screen unweighed, so it renders as though nothing "+
				"were wrong with it", r.Name())
		}
		t.Logf("  %-16s %-8s %4d obs", r.Name(), r.Strength, r.Events)
	}
}

// The queue and the day screen read ONE rule through one function, and this is
// the real day where that is checked. A listing that disagreed with the screen
// it leads to would send a reviewer looking for a mark that is not there.
func TestWeighAgreesWithARealDaysRows(t *testing.T) {
	day := liveDay(t)
	cfg := config.Default()
	liveCal, err := cfg.Calendar.Sessions()
	if err != nil {
		t.Fatalf("the default calendar will not load: %v", err)
	}
	weighed := dayview.Weigh(liveCal, day.Day, day.Params, liveEvents(t))
	if len(weighed) != len(day.Rows) {
		t.Fatalf("Weigh found %d projects on 2026-07-28 and the day has %d rows",
			len(weighed), len(day.Rows))
	}
	for _, r := range day.Rows {
		if got := weighed[r.Project]; got != r.Strength {
			t.Errorf("%s: the queue would say %q and the day screen says %q",
				r.Name(), got, r.Strength)
		}
	}
}

// THE SAME CLOSURE, ON A REAL DAY, REVIEWED AS IF IT WERE STILL HAPPENING.
//
// The constructed cases in dayview_test.go can make a half-finished day happen
// on demand; only a real day can show that the closure still tiles exactly when
// the cut lands in the middle of real blocks drawn from real evidence, rather
// than between the tidy hours a fixture chooses. Neither substitutes for the
// other, which is why both are here.
func TestConfirmingARealDayStillRunningAssertsNothingAboutTheRestOfIt(t *testing.T) {
	day := liveDay(t)
	// Eight hours in, which lands inside the working day and, on this day,
	// inside the run of blocks rather than politely between two of them.
	moment := day.Start.Add(8 * time.Hour)
	plan, err := day.Confirm(dayview.Answer{
		Mode: dayview.Wholesale, Origin: "a-test", At: moment,
	})
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

	// The examined extent: the moment of review, or the end of the last block
	// endorsed if one of them was still running. Everything before it is
	// accounted for exactly, and nothing after it is asserted at all.
	examined := plan.Review.End
	if examined.Before(moment) {
		t.Fatalf("the review covers only to %s, before the %s it was recorded at",
			examined.Format("15:04"), moment.Format("15:04"))
	}
	if examined.Equal(day.End) {
		t.Fatal("the examined extent reached the end of the working day, so this test cut " +
			"nothing and is proving nothing; move the moment earlier")
	}
	if got, want := timespan.Total(all), examined.Sub(day.Start); got != want {
		t.Errorf("the assertions cover %v of the %v examined, a shortfall of %v", got, want, want-got)
	}
	if got := timespan.Total(timespan.Union(all)); got != examined.Sub(day.Start) {
		t.Errorf("the assertions union to %v against %v examined, so two of them overlap",
			got, examined.Sub(day.Start))
	}
	t.Logf("2026-07-28 confirmed as at %s writes %d assertions over the %v examined, "+
		"and leaves %v of the day unexamined rather than asserted empty",
		moment.Format("15:04"), len(plan.Assertions), examined.Sub(day.Start), day.End.Sub(examined))
}
