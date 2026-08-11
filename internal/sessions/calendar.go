// Working days, and why a day is not a date.
//
// Everything in the store is UTC: internal/store.TimeLayout is a fixed-width
// UTC instant to the microsecond, and every endpoint of every sitting is one.
// A working day is the opposite kind of thing. It is LOCAL, it is a human
// convention, and it does not begin at midnight — on 2026-07-11 a sitting ran
// from 21:33 to 01:37 the following morning, and calling the last hour and a
// half of it Sunday's work would be a clerical fact rather than a true one.
//
// So a working day runs from a configured wall-clock time, 05:00 by default,
// and is NAMED by the civil date it begins on. The default is not arbitrary:
// across the live corpus the earliest morning sitting begins at 05:06:24
// local, and only two sittings in 381 start before 05:00, both at 01:39 on a
// Sunday that was plainly Saturday's evening. 05:00 is the deepest trough
// available. It is still a parameter, because a different person sleeps at a
// different time, and because 6m24s of clearance is not much.
//
// # A boundary is an instant, not a wall clock
//
// This is the whole of the daylight-saving problem. Bounds returns two
// INSTANTS, and consecutive days share an endpoint exactly, so working days
// TILE the timeline with no gap and no overlap. Every conservation property
// downstream falls out of that tiling and not out of any arithmetic on hours:
// summing presence per working day equals the ungrouped total because the
// days partition the instants, whether a given day is 23, 24, 25 or 24.5 hours
// long.
//
// A local day containing a spring-forward is 23 hours and one containing a
// fall-back is 25, and note WHICH day that is. America/Chicago jumps at 02:00,
// which is before 05:00, so the 23-hour working day of 2026 is the one named
// 2026-03-07 — Saturday 05:00 to Sunday 05:00 — and not the Sunday the
// transition is dated to. The calendar date of a transition and the working
// day that contains it are different answers.
//
// # A boundary landing in an hour that repeats, or never happens
//
// time.Date on an ambiguous or non-existent local time picks one silently and
// documents that the choice is not guaranteed. With a 05:00 boundary in
// America/Chicago nothing lands in a transition hour, so the trap would never
// fire on the shipped default and would fire the day somebody configured 02:00
// — which is the worst way for it to be found. The choice is therefore made
// here, explicitly:
//
//   - REPEATED (fall-back): the wall clock reads 01:30 twice on 2026-11-01,
//     once at -05 and once at -06. The boundary is the FIRST occurrence. The
//     day therefore begins as soon as the clock first reads it, and it is 25
//     hours long.
//
//   - SKIPPED (spring-forward): the wall clock never reads 02:30 on
//     2026-03-08. The boundary is the instant the clock jumped TO — the
//     transition itself — which is the earliest instant at which the local
//     time is past the nominal boundary. The day is 23 hours long.
//
// Both are defensible; what is not defensible is leaving it to a library's
// unspecified behaviour. Neither choice can break tiling, because each is a
// single well-defined instant and the next day's boundary is computed the same
// way.
//
// Fixed offsets are never used. The zone is loaded from the zoneinfo database
// and asked for the offset in force at each candidate instant, because the
// offset is a function of the instant and a constant would be wrong twice a
// year by construction.
//
// # A sitting that crosses a boundary is SPLIT
//
// The alternative — attributing a crossing sitting whole to the day it began
// on — is defensible and is rejected here for one reason: it can credit a
// working day with more wall clock than elapsed inside it. Sittings are the
// one metric whose defining property is that wall clock is never
// double-counted (see the package doc in sessions.go), and a 30-hour sitting
// booked entirely to a 24-hour Tuesday violates that in a way no reader could
// detect from the total. Splitting keeps the invariant presence(D) <= length(D)
// true by construction, and conservation holds either way.
//
// Because the boundary is chosen to sit in the trough, the split almost never
// fires: across the live corpus of 381 sittings, ZERO cross the 05:00
// boundary. At midnight two of them do. That is the boundary doing its work,
// and it is also why the split path is proved by construction rather than by
// the corpus.
//
// # Margin cannot be apportioned across a split
//
// A Session carries Margin as one number — its lead-in, its tail-out and every
// silence the gap threshold bridged — and not the spans those covered. Which
// side of a boundary any of it fell on is therefore not recoverable from the
// row. A pro-rata share would be arithmetic nobody measured, so a split
// sitting's margin is reported in SplitMargin and left unapportioned, beside
// the count of pieces it came from. That is the same three-state discipline
// the rest of this package runs on: observed, observed-not, and not
// established are three answers.
package sessions

import (
	"fmt"
	"time"

	"github.com/bensyverson/timetattle/internal/timespan"
)

// probeReach is how far either side of a nominal boundary the zone is asked
// for its offset. It only has to bracket a single transition; the zoneinfo
// database holds no two transitions within 36 hours of each other.
const probeReach = 36 * time.Hour

// Clock is a wall-clock time of day: no date, no zone, and therefore not an
// instant until a Calendar turns it into one.
type Clock struct {
	Hour, Minute int
}

// String renders the clock the way a config file writes it.
func (c Clock) String() string { return fmt.Sprintf("%02d:%02d", c.Hour, c.Minute) }

// Day is a working day, named by the civil date it BEGINS on. It is not a
// calendar date and the difference is the point: the working day 2026-07-11
// runs to 05:00 on the 12th.
type Day struct {
	Year  int
	Month time.Month
	Date  int
}

// String renders the day as the date it is named by.
func (d Day) String() string { return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Date) }

// AddDays moves n civil days, normalising through the standard library so that
// month and year ends need no arithmetic here.
func (d Day) AddDays(n int) Day {
	t := time.Date(d.Year, d.Month, d.Date+n, 12, 0, 0, 0, time.UTC)
	return Day{Year: t.Year(), Month: t.Month(), Date: t.Day()}
}

// Weekday is the day of the week the working day is named for. It is computed
// at noon UTC, which is a zone-free question about a civil date and cannot be
// moved by an offset.
func (d Day) Weekday() time.Weekday {
	return time.Date(d.Year, d.Month, d.Date, 12, 0, 0, 0, time.UTC).Weekday()
}

// before reports whether d names an earlier civil date than other.
func (d Day) before(other Day) bool {
	if d.Year != other.Year {
		return d.Year < other.Year
	}
	if d.Month != other.Month {
		return d.Month < other.Month
	}
	return d.Date < other.Date
}

// Calendar attributes instants to working days and weeks.
type Calendar struct {
	// Location is the zone the working day is a local concept in. It is a
	// loaded *time.Location and never a fixed offset, because the offset is a
	// function of the instant.
	Location *time.Location
	// DayStart is the wall clock a working day begins at.
	DayStart Clock
	// WeekStart is the weekday a week begins on, at DayStart.
	WeekStart time.Weekday
}

// Validate reports why a calendar cannot be used.
func (c Calendar) Validate() error {
	if c.Location == nil {
		return fmt.Errorf("the calendar has no location; load one with time.LoadLocation rather than " +
			"assuming an offset, which is wrong twice a year")
	}
	if c.DayStart.Hour < 0 || c.DayStart.Hour > 23 {
		return fmt.Errorf("the working day starts at hour %d; it must be between 0 and 23", c.DayStart.Hour)
	}
	if c.DayStart.Minute < 0 || c.DayStart.Minute > 59 {
		return fmt.Errorf("the working day starts at minute %d; it must be between 0 and 59", c.DayStart.Minute)
	}
	if c.WeekStart < time.Sunday || c.WeekStart > time.Saturday {
		return fmt.Errorf("the week starts on weekday %d; it must be between 0 (Sunday) and 6 (Saturday)",
			int(c.WeekStart))
	}
	return nil
}

// DayOf is the working day an instant belongs to.
//
// Bounds is the authority and this agrees with it by construction rather than
// by argument. The wall clock alone cannot decide: inside a repeated hour the
// local time RUNS BACKWARDS, so 01:00 CST on 2026-11-01 reads as earlier than
// a 01:30 boundary while being a full half hour later than it. Comparing wall
// clocks put that instant on the wrong day until a test walked every minute of
// both transition weekends at every boundary hour and half-hour.
//
// So the wall clock is used only as a first guess, and the answer is then
// confirmed against the day's real endpoints. One step of correction is the
// most that has ever been needed; the loop is bounded so that a zone doing
// something stranger produces a wrong answer rather than a hang.
func (c Calendar) DayOf(t time.Time) Day {
	lt := t.In(c.Location)
	d := Day{Year: lt.Year(), Month: lt.Month(), Date: lt.Day()}
	if lt.Hour() < c.DayStart.Hour ||
		(lt.Hour() == c.DayStart.Hour && lt.Minute() < c.DayStart.Minute) {
		d = d.AddDays(-1)
	}
	for range 4 {
		start, end := c.Bounds(d)
		switch {
		case t.Before(start):
			d = d.AddDays(-1)
		case !t.Before(end):
			d = d.AddDays(1)
		default:
			return d
		}
	}
	return d
}

// Bounds is the half-open span of instants a working day covers.
//
// Consecutive days share an endpoint exactly, so days tile the timeline. A day
// containing a transition is 23 or 25 hours long, and that is the correct
// answer rather than an error to be corrected.
func (c Calendar) Bounds(d Day) (time.Time, time.Time) {
	return c.boundary(d), c.boundary(d.AddDays(1))
}

// boundary is the instant a working day begins.
//
// It works by candidate and check rather than by trusting time.Date on a local
// time that may be ambiguous or may not exist. The nominal wall clock is
// combined with each offset the zone has in force around it; a candidate is
// valid when reading it back in the zone gives the wall clock that was asked
// for.
func (c Calendar) boundary(d Day) time.Time {
	// The wall clock treated as if it were UTC. Subtracting an offset from it
	// gives the instant at which the zone reads that wall clock.
	wall := time.Date(d.Year, d.Month, d.Date, c.DayStart.Hour, c.DayStart.Minute, 0, 0, time.UTC)

	var candidates []time.Time
	seen := map[int]bool{}
	for _, probe := range []time.Time{wall.Add(-probeReach), wall.Add(probeReach)} {
		_, offset := probe.In(c.Location).Zone()
		if seen[offset] {
			continue
		}
		seen[offset] = true
		candidates = append(candidates, wall.Add(-time.Duration(offset)*time.Second))
	}
	if len(candidates) == 2 && candidates[1].Before(candidates[0]) {
		candidates[0], candidates[1] = candidates[1], candidates[0]
	}

	for _, at := range candidates {
		lt := at.In(c.Location)
		if lt.Year() == d.Year && lt.Month() == d.Month && lt.Day() == d.Date &&
			lt.Hour() == c.DayStart.Hour && lt.Minute() == c.DayStart.Minute {
			// The earliest valid candidate. When the hour happens twice, this
			// is the first occurrence, which is the documented choice.
			return at
		}
	}
	// No candidate reads back: the clock jumped over this wall time. The day
	// begins at the instant it jumped to, which is the later candidate — the
	// earliest instant whose local time is past the nominal boundary.
	return candidates[len(candidates)-1]
}

// WeekOf is the working day the week containing d begins on.
func (c Calendar) WeekOf(d Day) Day {
	back := (int(d.Weekday()) - int(c.WeekStart) + 7) % 7
	return d.AddDays(-back)
}

// Days is every working day the span [from, to] touches, in order and with no
// day left out — including the days nothing happened on, which is the only way
// a caller can tell "no work" from "no observations".
func (c Calendar) Days(from, to time.Time) []Day {
	if to.Before(from) {
		return nil
	}
	var out []Day
	for d, last := c.DayOf(from), c.DayOf(to); !last.before(d); d = d.AddDays(1) {
		out = append(out, d)
	}
	return out
}

// Window is a span of instants something is known about. Its zero value means
// nothing is claimed either way, and ByDay then reports only the days sittings
// fall on.
type Window struct{ From, To time.Time }

func (w Window) empty() bool { return w.From.IsZero() && w.To.IsZero() }

// DaySlice is one working day's share of the sittings.
type DaySlice struct {
	// Day is the working day, and Start and End are the instants it covers.
	// The two endpoints are 23, 24 or 25 hours apart across a transition, and
	// a report quoting a daily rate should divide by End.Sub(Start) rather
	// than by 24 hours.
	Day        Day
	Start, End time.Time

	// Presence is how much sitting time elapsed inside this day — the same
	// wall-clock, never-double-counted quantity internal/metrics and `metrics`
	// call presence, because ByDay slices global sittings (see sessions.go's
	// package doc) and never a per-project intersection of them. A sitting
	// crossing the boundary contributes only the part that fell here.
	Presence time.Duration
	// Sessions counts the sittings contributing, whole or in part; Split
	// counts how many of those contributed only part of themselves.
	Sessions int
	Split    int

	// Margin is the inferred share — lead-in, tail-out and bridged silence —
	// of the sittings that landed here WHOLE.
	Margin time.Duration
	// SplitMargin is the margin of the sittings that were split, deliberately
	// not apportioned between the days they were split across. A Session
	// records the total and not the spans it covered, so which side of a
	// boundary any of it fell on is not recoverable, and a pro-rata share
	// would be a number nobody measured.
	//
	// It is booked once, against the day the sitting began on, so that the
	// column sums across a projection to the margin that exists. That location
	// is bookkeeping and not an attribution.
	SplitMargin time.Duration

	// FirstStart is the earliest sitting to BEGIN inside this day, and the
	// zero time when none did. That distinction is what the boundary is
	// fitted against: a day whose work habitually began at 04:30 would be a
	// day the 05:00 boundary was cutting in half, whereas a day holding only
	// the tail of last night's sitting has no morning at all and must not be
	// averaged in as though it started at the boundary.
	FirstStart time.Time

	// Observed says the day overlaps the window observations were made in. A
	// day with Observed true and Presence zero was looked at and held no work.
	// A day missing from the projection entirely is a day nothing was recorded
	// on. Those are three answers and this project has shipped the two-answer
	// version of them three times.
	Observed bool
}

// Length is how long the working day itself ran.
func (d DaySlice) Length() time.Duration { return d.End.Sub(d.Start) }

// noteStart records a sitting's start if it is the earliest seen. The zero
// time means no sitting has begun here, and is never a candidate.
func (d *DaySlice) noteStart(at time.Time) {
	if d.FirstStart.IsZero() || at.Before(d.FirstStart) {
		d.FirstStart = at
	}
}

// ByDay splits sittings across the working days they fall in.
//
// Days tile the timeline, so the returned Presence durations sum to exactly the
// sum of the sittings' own durations, on 23- and 25-hour days alike. Sittings
// are assumed disjoint, which Cluster guarantees.
//
// Every working day inside observed appears, even with no sittings on it. Days
// outside it appear only if a sitting falls on them, and say so through
// Observed.
func (c Calendar) ByDay(ss []Session, observed Window) []DaySlice {
	slices := map[Day]*DaySlice{}
	ensure := func(d Day) *DaySlice {
		if s, ok := slices[d]; ok {
			return s
		}
		start, end := c.Bounds(d)
		s := &DaySlice{Day: d, Start: start, End: end}
		slices[d] = s
		return s
	}

	if !observed.empty() {
		for _, d := range c.Days(observed.From, observed.To) {
			ensure(d).Observed = true
		}
	}

	for _, s := range ss {
		if !s.End.After(s.Start) {
			// A zero-length sitting still happened somewhere and is counted
			// there; it just contributes no time.
			d := ensure(c.DayOf(s.Start))
			d.Sessions++
			d.Margin += s.Margin
			d.noteStart(s.Start)
			continue
		}
		first, last := c.DayOf(s.Start), c.DayOf(s.End.Add(-time.Nanosecond))
		split := first != last
		// Bounded by the ordering of the days rather than by reaching `last`
		// exactly. The two are the same while DayOf is monotonic, which it is
		// because days tile; but an equality break turns any future violation
		// of that into a hang, and a wrong number is a far better failure than
		// a command that never returns.
		for d := first; !last.before(d); d = d.AddDays(1) {
			slice := ensure(d)
			// Only the day the sitting BEGAN on gets a start. The pieces after
			// a boundary are continuations, and recording the boundary as
			// their start would report a morning that nobody had.
			if d == first {
				slice.noteStart(s.Start)
			}
			from, to := timespan.LaterOf(s.Start, slice.Start), timespan.EarlierOf(s.End, slice.End)
			if to.After(from) {
				slice.Presence += to.Sub(from)
			}
			slice.Sessions++
			if split {
				slice.Split++
				// Charged once, to the day the sitting began on, so that the
				// column sums to the margin that actually exists instead of
				// counting it once per day the sitting touched. Where it is
				// booked is bookkeeping; it is emphatically not a claim that
				// the margin fell on that day, which is the thing a Session
				// does not record.
				if d == first {
					slice.SplitMargin += s.Margin
				}
			} else {
				slice.Margin += s.Margin
			}
		}
	}

	out := make([]DaySlice, 0, len(slices))
	for _, s := range slices {
		out = append(out, *s)
	}
	sortDaySlices(out)
	return out
}

func sortDaySlices(out []DaySlice) {
	// Insertion sort: a projection holds one entry per day of a corpus, so
	// this is a few hundred elements at most and the ordering is trivially
	// auditable.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].Day.before(out[j-1].Day); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
}
