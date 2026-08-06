package sessions_test

import (
	"math/rand"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
)

// chicago is the zone every DST case here is expressed in. It is loaded rather
// than written as an offset, because the whole point is that the offset is not
// a constant: -6 until 2026-03-08T08:00Z and -5 after it.
func chicago(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("America/Chicago is not in this machine's zoneinfo: %v", err)
	}
	return loc
}

// calFor is the shipped default calendar in a named zone: days from 05:00,
// weeks from Monday.
func calFor(loc *time.Location) sessions.Calendar {
	return sessions.Calendar{
		Location:  loc,
		DayStart:  sessions.Clock{Hour: 5},
		WeekStart: time.Monday,
	}
}

// local builds an instant from a wall clock in loc, so a case reads as the
// wall clock somebody would have seen.
func local(loc *time.Location, y int, m time.Month, d, hh, mm int) time.Time {
	return time.Date(y, m, d, hh, mm, 0, 0, loc)
}

func day(y int, m time.Month, d int) sessions.Day {
	return sessions.Day{Year: y, Month: m, Date: d}
}

func session(start, end time.Time) sessions.Session {
	return sessions.Session{Start: start, End: end, Events: 1, Sources: []string{"biome-appusage"}}
}

// engagedOn is the engaged time a projection put on one working day, and
// whether that day appeared at all. The two are different answers and this
// returns both, because a day that is absent from the projection is a day
// nothing was recorded on, not a day with no work on it.
func engagedOn(slices []sessions.DaySlice, d sessions.Day) (time.Duration, bool) {
	for _, s := range slices {
		if s.Day == d {
			return s.Engaged, true
		}
	}
	return 0, false
}

// --- the working day, and the case that motivated it ---------------------

// The grounding case from task Tvfnf. In the live store this sitting runs
// 2026-07-11 21:33:56 to 2026-07-12 01:37:24 local, on 550 observations. Under
// a midnight boundary it is two days' work; under the 05:00 boundary it is one
// evening, which is what it was.
func TestLateNightSittingIsAttributedToTheDayItBeganOn(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	s := session(
		local(loc, 2026, time.July, 11, 21, 33),
		local(loc, 2026, time.July, 12, 1, 37),
	)
	slices := c.ByDay([]sessions.Session{s}, sessions.Window{})

	saturday := day(2026, time.July, 11)
	if got := c.DayOf(s.Start); got != saturday {
		t.Errorf("the sitting began on working day %v, want %v", got, saturday)
	}
	if got := c.DayOf(s.End); got != saturday {
		t.Errorf("the sitting ended on working day %v, want %v; 01:37 is still Saturday's evening", got, saturday)
	}
	engaged, ok := engagedOn(slices, saturday)
	if !ok {
		t.Fatalf("Saturday is absent from the projection; slices=%v", slices)
	}
	if want := s.Duration(); engaged != want {
		t.Errorf("Saturday holds %v of the sitting, want the whole %v", engaged, want)
	}
	if _, ok := engagedOn(slices, day(2026, time.July, 12)); ok {
		t.Errorf("Sunday appeared in the projection; the sitting ended before Sunday's working day began")
	}
}

// The rule for a sitting that really does cross the boundary: it is SPLIT, so
// that no working day is ever credited with wall clock that did not elapse
// inside it.
func TestSittingCrossingTheBoundaryIsSplitAtIt(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	s := session(
		local(loc, 2026, time.July, 20, 4, 0),
		local(loc, 2026, time.July, 20, 6, 0),
	)
	slices := c.ByDay([]sessions.Session{s}, sessions.Window{})

	before, ok := engagedOn(slices, day(2026, time.July, 19))
	if !ok {
		t.Fatalf("the day before the boundary is absent; slices=%v", slices)
	}
	after, ok := engagedOn(slices, day(2026, time.July, 20))
	if !ok {
		t.Fatalf("the day after the boundary is absent; slices=%v", slices)
	}
	if before != time.Hour {
		t.Errorf("04:00-05:00 landed as %v on the 19th, want 1h", before)
	}
	if after != time.Hour {
		t.Errorf("05:00-06:00 landed as %v on the 20th, want 1h", after)
	}
	if before+after != s.Duration() {
		t.Errorf("the split totals %v, want the sitting's own %v", before+after, s.Duration())
	}
}

// A split sitting's margin cannot be apportioned. Margin is the lead-in, the
// tail-out and every bridged silence, and a Session carries only the total, so
// which side of a boundary any of it fell on is not recoverable from the row.
// Inventing a pro-rata share would be a number nobody measured.
func TestSplitSittingsMarginIsReportedUnapportioned(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	crossing := session(local(loc, 2026, time.July, 20, 4, 0), local(loc, 2026, time.July, 20, 6, 0))
	crossing.Margin = 30 * time.Minute
	whole := session(local(loc, 2026, time.July, 20, 9, 0), local(loc, 2026, time.July, 20, 10, 0))
	whole.Margin = 10 * time.Minute

	slices := c.ByDay([]sessions.Session{crossing, whole}, sessions.Window{})
	var margin, splitMargin time.Duration
	var split int
	for _, s := range slices {
		margin += s.Margin
		splitMargin += s.SplitMargin
		split += s.Split
	}
	if split != 2 {
		t.Errorf("%d pieces were marked as coming from a split sitting, want 2", split)
	}
	if margin != 10*time.Minute {
		t.Errorf("apportioned margin is %v, want only the whole sitting's 10m", margin)
	}
	if splitMargin != 30*time.Minute {
		t.Errorf("unapportioned margin is %v, want the split sitting's 30m", splitMargin)
	}
}

// --- daylight saving ------------------------------------------------------

// A WORKING day containing a spring-forward is 23 hours long.
//
// Note which day that is. The clock jumps at 02:00 on Sunday 2026-03-08, which
// is BEFORE 05:00, so the jump falls inside the working day named 2026-03-07 —
// the one that runs from Saturday 05:00 to Sunday 05:00. The calendar date of
// the transition and the working day it lands in are different answers, and
// the whole point of a boundary is that they can be.
func TestSpringForwardWorkingDayIsTwentyThreeHours(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	start, end := c.Bounds(day(2026, time.March, 7))
	if got := end.Sub(start); got != 23*time.Hour {
		t.Errorf("working day 2026-03-07 runs %v, want 23h; the clock went 02:00 -> 03:00 inside it", got)
	}
	// And the Sunday named 2026-03-08 is an ordinary 24 hours, because by
	// 05:00 the jump has already happened.
	start, end = c.Bounds(day(2026, time.March, 8))
	if got := end.Sub(start); got != 24*time.Hour {
		t.Errorf("working day 2026-03-08 runs %v, want 24h", got)
	}
}

// And the working day holding a fall-back is 25.
func TestFallBackWorkingDayIsTwentyFiveHours(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	start, end := c.Bounds(day(2026, time.October, 31))
	if got := end.Sub(start); got != 25*time.Hour {
		t.Errorf("working day 2026-10-31 runs %v, want 25h; the clock went 02:00 -> 01:00 inside it", got)
	}
	start, end = c.Bounds(day(2026, time.November, 1))
	if got := end.Sub(start); got != 24*time.Hour {
		t.Errorf("working day 2026-11-01 runs %v, want 24h", got)
	}
}

// Every other day is 24, including the ones either side of a transition. A
// boundary that leaked an hour into a neighbour would show up here.
func TestOrdinaryDaysNextToATransitionAreTwentyFourHours(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	for _, d := range []sessions.Day{
		day(2026, time.March, 5), day(2026, time.March, 6),
		day(2026, time.March, 9), day(2026, time.March, 10),
		day(2026, time.October, 29), day(2026, time.October, 30),
		day(2026, time.November, 2), day(2026, time.November, 3),
	} {
		start, end := c.Bounds(d)
		if got := end.Sub(start); got != 24*time.Hour {
			t.Errorf("%v runs %v, want 24h", d, got)
		}
	}
}

// Days must TILE: one day's end is the next day's start, exactly, with no gap
// and no overlap. This is the property that makes conservation exact rather
// than approximate, and it is the one DST is most likely to break.
func TestDaysTileWithoutGapOrOverlapAcrossBothTransitions(t *testing.T) {
	loc := chicago(t)
	for hour := range 24 {
		c := sessions.Calendar{Location: loc, DayStart: sessions.Clock{Hour: hour}, WeekStart: time.Monday}
		for _, from := range []sessions.Day{day(2026, time.March, 1), day(2026, time.October, 25)} {
			d := from
			for range 14 {
				start, end := c.Bounds(d)
				// A degenerate day would satisfy the tiling check below
				// trivially, so the length is asserted first.
				if length := end.Sub(start); length < 23*time.Hour || length > 25*time.Hour {
					t.Fatalf("day start %02d:00: %v runs %v, which is not a day at all", hour, d, length)
				}
				next := d.AddDays(1)
				nextStart, _ := c.Bounds(next)
				if !end.Equal(nextStart) {
					t.Fatalf("day start %02d:00: %v ends %v but %v starts %v",
						hour, d, end.UTC(), next, nextStart.UTC())
				}
				d = next
			}
		}
	}
}

// A boundary landing in the hour that happens TWICE takes the first
// occurrence. Both are defensible; what is not defensible is Go's time.Date
// choosing one silently, so the choice is asserted.
func TestBoundaryInARepeatedHourTakesTheFirstOccurrence(t *testing.T) {
	loc := chicago(t)
	// 2026-11-01: 01:00-02:00 local happens twice, once at -5 and once at -6.
	c := sessions.Calendar{Location: loc, DayStart: sessions.Clock{Hour: 1}, WeekStart: time.Monday}
	start, end := c.Bounds(day(2026, time.November, 1))
	want := time.Date(2026, time.November, 1, 6, 0, 0, 0, time.UTC) // 01:00 CDT
	if !start.Equal(want) {
		t.Errorf("the 01:00 boundary is %v, want the first 01:00 at %v", start.UTC(), want)
	}
	if got := end.Sub(start); got != 25*time.Hour {
		t.Errorf("the day is %v, want 25h", got)
	}
}

// A boundary landing in an hour that does NOT EXIST is the instant the clock
// jumps to. 02:00 never happened on 2026-03-08; the day begins at 03:00 CDT,
// which is the same instant as 02:00 would have been.
func TestBoundaryInASkippedHourIsTheTransitionInstant(t *testing.T) {
	loc := chicago(t)
	c := sessions.Calendar{Location: loc, DayStart: sessions.Clock{Hour: 2}, WeekStart: time.Monday}
	start, end := c.Bounds(day(2026, time.March, 8))
	want := time.Date(2026, time.March, 8, 8, 0, 0, 0, time.UTC) // 03:00 CDT
	if !start.Equal(want) {
		t.Errorf("the 02:00 boundary is %v, want the transition instant %v", start.UTC(), want)
	}
	if got := end.Sub(start); got != 23*time.Hour {
		t.Errorf("the day is %v, want 23h", got)
	}
}

// A half-hour transition is not a special case in the zoneinfo database and
// must not be one here. Lord Howe Island shifts by 30 minutes.
func TestHalfHourTransitionZoneIsHandled(t *testing.T) {
	loc, err := time.LoadLocation("Australia/Lord_Howe")
	if err != nil {
		t.Skipf("Australia/Lord_Howe is not in this machine's zoneinfo: %v", err)
	}
	c := calFor(loc)
	// DST ends at 02:00 on the first Sunday in April (2026-04-05) and begins
	// at 02:00 on the first Sunday in October (2026-10-04), by 30 minutes each
	// way. Both are before 05:00, so both land in the working day before.
	start, end := c.Bounds(day(2026, time.April, 4))
	if got := end.Sub(start); got != 24*time.Hour+30*time.Minute {
		t.Errorf("working day 2026-04-04 runs %v, want 24h30m", got)
	}
	start, end = c.Bounds(day(2026, time.October, 3))
	if got := end.Sub(start); got != 23*time.Hour+30*time.Minute {
		t.Errorf("working day 2026-10-03 runs %v, want 23h30m", got)
	}
}

// DayOf and Bounds are two answers to the same question and must never
// disagree. This walks every minute of both transition weekends at every
// boundary hour and checks that the day an instant is attributed to is the day
// whose bounds contain it.
func TestDayOfAlwaysLandsInsideThatDaysBounds(t *testing.T) {
	loc := chicago(t)
	for hour := range 24 {
		for _, minute := range []int{0, 30} {
			c := sessions.Calendar{
				Location:  loc,
				DayStart:  sessions.Clock{Hour: hour, Minute: minute},
				WeekStart: time.Monday,
			}
			for _, from := range []time.Time{
				time.Date(2026, time.March, 7, 0, 0, 0, 0, time.UTC),
				time.Date(2026, time.October, 31, 0, 0, 0, 0, time.UTC),
			} {
				for t0 := from; t0.Before(from.Add(72 * time.Hour)); t0 = t0.Add(time.Minute) {
					d := c.DayOf(t0)
					start, end := c.Bounds(d)
					if t0.Before(start) || !t0.Before(end) {
						t.Fatalf("day start %02d:%02d: %v was attributed to %v, whose bounds are %v..%v",
							hour, minute, t0, d, start.UTC(), end.UTC())
					}
				}
			}
		}
	}
}

// The criterion in the task's own words: a transition creates no hours and
// destroys none. A sitting spanning the whole transition weekend must sum,
// across the days it is split over, to exactly its own length — on the
// 23-hour day and on the 25-hour one.
func TestTransitionsCreateAndDestroyNoEngagedTime(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	for _, s := range []sessions.Session{
		session(local(loc, 2026, time.March, 6, 9, 0), local(loc, 2026, time.March, 10, 9, 0)),
		session(local(loc, 2026, time.October, 30, 9, 0), local(loc, 2026, time.November, 3, 9, 0)),
	} {
		slices := c.ByDay([]sessions.Session{s}, sessions.Window{})
		var sum time.Duration
		for _, d := range slices {
			sum += d.Engaged
		}
		if sum != s.Duration() {
			t.Errorf("a sitting of %v summed to %v across %d working days; the difference is %v",
				s.Duration(), sum, len(slices), sum-s.Duration())
		}
	}
}

// The same property over arbitrary sittings at an arbitrary boundary, because
// the two constructed weekends above could both be right by luck.
func TestConservationHoldsOverRandomSittings(t *testing.T) {
	loc := chicago(t)
	rng := rand.New(rand.NewSource(7))
	origin := time.Date(2026, time.February, 1, 0, 0, 0, 0, time.UTC)

	for trial := range 200 {
		c := sessions.Calendar{
			Location:  loc,
			DayStart:  sessions.Clock{Hour: rng.Intn(24), Minute: 30 * rng.Intn(2)},
			WeekStart: time.Weekday(rng.Intn(7)),
		}
		var ss []sessions.Session
		var want time.Duration
		for range 40 {
			start := origin.Add(time.Duration(rng.Int63n(int64(300 * 24 * time.Hour))))
			s := session(start, start.Add(time.Duration(rng.Int63n(int64(30*time.Hour)))))
			ss = append(ss, s)
			want += s.Duration()
		}
		var got time.Duration
		for _, d := range c.ByDay(ss, sessions.Window{}) {
			got += d.Engaged
		}
		if got != want {
			t.Fatalf("trial %d (day start %v, week start %v): %v of sittings summed to %v, a difference of %v",
				trial, c.DayStart, c.WeekStart, want, got, got-want)
		}
	}
}

// No working day may be credited with more wall clock than elapsed inside it.
// This is the invariant that whole-session attribution cannot offer and is the
// reason a crossing sitting is split.
func TestNoDayHoldsMoreEngagedTimeThanItHasHours(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	// A 49-hour sitting, longer than any day, laid across the fall-back
	// weekend where the working days are 25, 24 and 24 hours.
	s := session(local(loc, 2026, time.October, 31, 12, 0), local(loc, 2026, time.November, 2, 12, 0))
	slices := c.ByDay([]sessions.Session{s}, sessions.Window{})
	// The sitting spans three working days, so an empty or short projection is
	// itself the failure rather than a vacuous pass.
	if len(slices) != 3 {
		t.Fatalf("the sitting was spread over %d working days, want 3", len(slices))
	}
	var sum time.Duration
	for _, d := range slices {
		start, end := c.Bounds(d.Day)
		if d.Engaged > end.Sub(start) {
			t.Errorf("%v holds %v of engaged time in a day %v long", d.Day, d.Engaged, end.Sub(start))
		}
		sum += d.Engaged
	}
	if sum != s.Duration() {
		t.Errorf("the three days hold %v between them, want the sitting's %v", sum, s.Duration())
	}
}

// FirstStart is what the boundary is FITTED against: if a working day's first
// sitting habitually began at 04:30, a 05:00 boundary would be cutting real
// mornings off the day they belong to. It is the earliest sitting start lying
// inside the day, and a day that only holds the continuation of a sitting that
// began yesterday has none — which is a third answer and not a zero.
func TestFirstStartIsTheEarliestSittingToBeginInTheDay(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	morning := session(local(loc, 2026, time.July, 20, 8, 0), local(loc, 2026, time.July, 20, 9, 0))
	earlier := session(local(loc, 2026, time.July, 20, 6, 30), local(loc, 2026, time.July, 20, 7, 0))
	// Begins on the 19th and runs across the boundary into the 20th.
	crossing := session(local(loc, 2026, time.July, 20, 4, 0), local(loc, 2026, time.July, 20, 5, 30))

	slices := c.ByDay([]sessions.Session{morning, earlier, crossing}, sessions.Window{})
	var twentieth, nineteenth sessions.DaySlice
	for _, s := range slices {
		switch s.Day {
		case day(2026, time.July, 20):
			twentieth = s
		case day(2026, time.July, 19):
			nineteenth = s
		}
	}
	if want := local(loc, 2026, time.July, 20, 6, 30); !twentieth.FirstStart.Equal(want) {
		t.Errorf("the 20th's first start is %v, want %v", twentieth.FirstStart, want)
	}
	if want := local(loc, 2026, time.July, 20, 4, 0); !nineteenth.FirstStart.Equal(want) {
		t.Errorf("the 19th's first start is %v, want %v", nineteenth.FirstStart, want)
	}
}

func TestADayHoldingOnlyAContinuationHasNoFirstStart(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	crossing := session(local(loc, 2026, time.July, 20, 4, 0), local(loc, 2026, time.July, 20, 9, 0))

	for _, s := range c.ByDay([]sessions.Session{crossing}, sessions.Window{}) {
		if s.Day == day(2026, time.July, 20) && !s.FirstStart.IsZero() {
			t.Errorf("the 20th reports a first start of %v; no sitting began on it, and the "+
				"boundary is not a start", s.FirstStart)
		}
	}
}

// --- weeks ----------------------------------------------------------------

func TestWeekStartsOnTheConfiguredWeekday(t *testing.T) {
	loc := chicago(t)
	monday := calFor(loc)
	sunday := sessions.Calendar{Location: loc, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Sunday}

	// 2026-07-11 is a Saturday.
	saturday := day(2026, time.July, 11)
	if got, want := monday.WeekOf(saturday), day(2026, time.July, 6); got != want {
		t.Errorf("with weeks from Monday, %v is in the week of %v, want %v", saturday, got, want)
	}
	if got, want := sunday.WeekOf(saturday), day(2026, time.July, 5); got != want {
		t.Errorf("with weeks from Sunday, %v is in the week of %v, want %v", saturday, got, want)
	}
}

// A week begins at the boundary hour, not at midnight, or the first five hours
// of a week's Monday would belong to the week before it.
func TestWeekBeginsAtTheBoundaryHour(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	weekStart := c.WeekOf(day(2026, time.July, 11))
	start, _ := c.Bounds(weekStart)
	want := local(loc, 2026, time.July, 6, 5, 0)
	if !start.Equal(want) {
		t.Errorf("the week begins at %v, want %v", start, want)
	}
}

// --- three states ---------------------------------------------------------

// A working day nobody worked on and a working day nobody recorded are
// different answers. Collapsing them is a bug this project has shipped three
// times, and a projection that reports only the days with sittings on them
// cannot tell them apart.
func TestObservedDayWithNoSittingsIsNotAnUnobservedDay(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	// Observations run across three working days; sittings fall on the first
	// and the third.
	observed := sessions.Window{
		From: local(loc, 2026, time.July, 6, 9, 0),
		To:   local(loc, 2026, time.July, 8, 18, 0),
	}
	ss := []sessions.Session{
		session(local(loc, 2026, time.July, 6, 9, 0), local(loc, 2026, time.July, 6, 10, 0)),
		session(local(loc, 2026, time.July, 8, 9, 0), local(loc, 2026, time.July, 8, 10, 0)),
	}
	slices := c.ByDay(ss, observed)

	middle, ok := engagedOn(slices, day(2026, time.July, 7))
	if !ok {
		t.Fatalf("the middle day is absent from the projection, so a day with no work reads the same "+
			"as a day nobody harvested; slices=%v", slices)
	}
	if middle != 0 {
		t.Errorf("the middle day holds %v of engaged time, want none", middle)
	}
	for _, s := range slices {
		if !s.Observed {
			t.Errorf("%v is inside the observed window but is not marked observed", s.Day)
		}
	}
	// A day outside the window was never looked at, and must not appear at all.
	if _, ok := engagedOn(slices, day(2026, time.July, 9)); ok {
		t.Errorf("2026-07-09 appeared; nothing was recorded on it and a zero there would be a claim")
	}
}

// A sitting outside the observed window still appears, and says plainly that
// it was not covered by the window.
func TestSittingOutsideTheObservedWindowIsStillReported(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	observed := sessions.Window{
		From: local(loc, 2026, time.July, 6, 9, 0),
		To:   local(loc, 2026, time.July, 6, 18, 0),
	}
	ss := []sessions.Session{session(local(loc, 2026, time.July, 20, 9, 0), local(loc, 2026, time.July, 20, 10, 0))}
	slices := c.ByDay(ss, observed)
	if got, ok := engagedOn(slices, day(2026, time.July, 20)); !ok || got != time.Hour {
		t.Errorf("the sitting on 2026-07-20 is %v (present=%v), want 1h", got, ok)
	}
}

// --- validation -----------------------------------------------------------

func TestCalendarRefusesUnusableValues(t *testing.T) {
	loc := chicago(t)
	for name, c := range map[string]sessions.Calendar{
		"no location":    {DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday},
		"hour too large": {Location: loc, DayStart: sessions.Clock{Hour: 24}, WeekStart: time.Monday},
		"hour negative":  {Location: loc, DayStart: sessions.Clock{Hour: -1}, WeekStart: time.Monday},
		"minute too big": {Location: loc, DayStart: sessions.Clock{Hour: 5, Minute: 60}, WeekStart: time.Monday},
		"weekday":        {Location: loc, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Weekday(9)},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate accepted it", name)
		}
	}
	if err := calFor(loc).Validate(); err != nil {
		t.Errorf("the default calendar was refused: %v", err)
	}
}

func TestDayAndClockRenderReadably(t *testing.T) {
	if got := day(2026, time.July, 11).String(); got != "2026-07-11" {
		t.Errorf("Day.String is %q, want %q", got, "2026-07-11")
	}
	if got := (sessions.Clock{Hour: 5}).String(); got != "05:00" {
		t.Errorf("Clock.String is %q, want %q", got, "05:00")
	}
	if got := (sessions.Clock{Hour: 5, Minute: 30}).String(); got != "05:30" {
		t.Errorf("Clock.String is %q, want %q", got, "05:30")
	}
	if got := day(2026, time.July, 11).Weekday(); got != time.Saturday {
		t.Errorf("2026-07-11 is a %v, want Saturday", got)
	}
	if got := day(2026, time.July, 31).AddDays(1); got != day(2026, time.August, 1) {
		t.Errorf("the day after 2026-07-31 is %v, want 2026-08-01", got)
	}
}

// Days enumerates every working day a span touches, including the ones with
// nothing on them, and does it across a transition without dropping or
// duplicating one.
func TestDaysEnumeratesEveryWorkingDayInASpan(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	from := local(loc, 2026, time.March, 6, 9, 0)
	to := local(loc, 2026, time.March, 10, 9, 0)
	got := c.Days(from, to)
	want := []sessions.Day{
		day(2026, time.March, 6), day(2026, time.March, 7), day(2026, time.March, 8),
		day(2026, time.March, 9), day(2026, time.March, 10),
	}
	if len(got) != len(want) {
		t.Fatalf("Days returned %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Days returned %v, want %v", got, want)
		}
	}
}
