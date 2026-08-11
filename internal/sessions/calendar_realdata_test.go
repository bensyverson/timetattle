package sessions_test

import (
	"encoding/csv"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

// The 381 sittings the live store held on 2026-07-31, derived at gap 15m,
// lead-in 5m, tail-out 5m, presence knowledgec-backlit, ignoring
// knowledgec-islocked and knowledgec-mediausage. Reproduce with
// `timetattle sessionize -dry-run`; the fixture is timestamps and counts only.
//
// It is here because the store's own span holds NO daylight-saving transition
// — America/Chicago changed on 2026-03-08 and changes again on 2026-11-01,
// while the corpus runs 2026-07-05 to 2026-07-31 local. So real data can
// establish conservation on ordinary days and cannot, by itself, establish it
// on a 23- or 25-hour one. Translating these real spans onto a real transition
// is how the second half gets tested against real shapes rather than against
// four hand-written intervals; the translation is stated wherever a figure
// rests on it.
func realSessions(t *testing.T) []sessions.Session {
	t.Helper()
	f, err := os.Open("testdata/realsessions.csv")
	if err != nil {
		t.Fatalf("open the real session fixture: %v", err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read the real session fixture: %v", err)
	}
	out := make([]sessions.Session, 0, len(rows))
	for i, row := range rows {
		if len(row) != 4 {
			t.Fatalf("row %d has %d fields, want 4", i+1, len(row))
		}
		start, err := time.Parse(store.TimeLayout, row[0])
		if err != nil {
			t.Fatalf("row %d start: %v", i+1, err)
		}
		end, err := time.Parse(store.TimeLayout, row[1])
		if err != nil {
			t.Fatalf("row %d end: %v", i+1, err)
		}
		events, err := strconv.Atoi(row[2])
		if err != nil {
			t.Fatalf("row %d events: %v", i+1, err)
		}
		marginUS, err := strconv.ParseInt(row[3], 10, 64)
		if err != nil {
			t.Fatalf("row %d margin: %v", i+1, err)
		}
		out = append(out, sessions.Session{
			Start: start, End: end, Events: events,
			Sources: []string{"biome-appusage"},
			Margin:  time.Duration(marginUS) * time.Microsecond,
		})
	}
	return out
}

func totalOf(ss []sessions.Session) time.Duration {
	var sum time.Duration
	for _, s := range ss {
		sum += s.Duration()
	}
	return sum
}

// The corpus totals 248.7h over 381 sittings. If this fails the fixture has
// been regenerated under different parameters and every figure below moved.
func TestRealFixtureIsTheCorpusTheFiguresWereMeasuredOn(t *testing.T) {
	ss := realSessions(t)
	if len(ss) != 381 {
		t.Fatalf("the fixture holds %d sittings, want 381", len(ss))
	}
	if got := totalOf(ss).Hours(); got < 248.6 || got > 248.8 {
		t.Fatalf("the fixture totals %.4fh, want 248.7h", got)
	}
}

// Conservation over the real corpus, on every working day, at every boundary
// hour. The sum of presence per working day must equal the ungrouped
// total EXACTLY — not to the second, exactly — or the boundary is inventing or
// destroying time somewhere in the middle of real data.
func TestRealCorpusConservesPresenceAtEveryBoundaryHour(t *testing.T) {
	loc := chicago(t)
	ss := realSessions(t)
	want := totalOf(ss)

	for hour := range 24 {
		c := sessions.Calendar{Location: loc, DayStart: sessions.Clock{Hour: hour}, WeekStart: time.Monday}
		var got time.Duration
		for _, d := range c.ByDay(ss, sessions.Window{}) {
			got += d.Presence
		}
		if got != want {
			t.Errorf("day start %02d:00: per-day presence sums to %v against an ungrouped %v; difference %v",
				hour, got, want, got-want)
		}
	}
}

// The same real spans, rigidly translated so that they straddle a real
// transition.
//
// The shift is a whole number of 24-hour periods in INSTANT space, so every
// sitting keeps its length to the microsecond. Its local wall clock does not
// survive the transition unchanged — on the far side of it every sitting reads
// an hour earlier or later — and that is the effect under test rather than a
// flaw in the method: the question is whether regrouping those instants by
// working day still totals what it did before.
//
// This is the real-data half of criterion QCM, and it is a TRANSLATED corpus,
// not an observed one. Nobody in this store worked through a DST transition:
// America/Chicago changed on 2026-03-08 and changes again on 2026-11-01, and
// the corpus runs 2026-07-05 to 2026-07-31 local. No figure here should be
// read as if somebody had.
func TestTranslatedRealCorpusConservesPresenceAcrossBothTransitions(t *testing.T) {
	loc := chicago(t)
	ss := realSessions(t)

	for _, tc := range []struct {
		name  string
		shift time.Duration
		day   sessions.Day
		hours time.Duration
	}{
		// The corpus covers 27 local days from 2026-07-05. Back 133 days puts
		// it over 2026-02-22..03-20, with the 23-hour working day 2026-03-07
		// well inside it rather than at an edge.
		{"spring forward", -133 * 24 * time.Hour, sessions.Day{Year: 2026, Month: time.March, Date: 7}, 23 * time.Hour},
		// Forward 105 days puts it over 2026-10-18..11-13, containing the
		// 25-hour working day 2026-10-31.
		{"fall back", 105 * 24 * time.Hour, sessions.Day{Year: 2026, Month: time.October, Date: 31}, 25 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			moved := make([]sessions.Session, len(ss))
			for i, s := range ss {
				moved[i] = s
				moved[i].Start = s.Start.Add(tc.shift)
				moved[i].End = s.End.Add(tc.shift)
			}
			want := totalOf(moved)
			if want != totalOf(ss) {
				t.Fatalf("translating changed the corpus total from %v to %v", totalOf(ss), want)
			}

			c := calFor(loc)
			slices := c.ByDay(moved, sessions.Window{})
			var got time.Duration
			var sawTransitionDay bool
			for _, d := range slices {
				got += d.Presence
				start, end := c.Bounds(d.Day)
				if d.Day == tc.day {
					sawTransitionDay = true
					if end.Sub(start) != tc.hours {
						t.Errorf("%v runs %v, want %v", d.Day, end.Sub(start), tc.hours)
					}
				}
				if d.Presence > end.Sub(start) {
					t.Errorf("%v holds %v of presence in a %v day", d.Day, d.Presence, end.Sub(start))
				}
			}
			if !sawTransitionDay {
				t.Errorf("the %v transition day is absent from the projection; the shift did not land on it", tc.day)
			}
			if got != want {
				t.Errorf("across the transition, per-day presence sums to %v against %v; difference %v",
					got, want, got-want)
			}
		})
	}
}

// The grounding case, against the real fixture rather than a constructed one:
// the sitting that ran to 01:37 belongs to the Saturday.
func TestRealCorpusPutsTheLateSittingOnTheSaturday(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)
	saturday := sessions.Day{Year: 2026, Month: time.July, Date: 11}

	var found *sessions.Session
	for i, s := range realSessions(t) {
		lt := s.End.In(loc)
		if lt.Year() == 2026 && lt.Month() == time.July && lt.Day() == 12 && lt.Hour() == 1 && lt.Minute() == 37 {
			found = &realSessions(t)[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("no sitting in the fixture ends at 01:37 local on 2026-07-12")
	}
	if got := c.DayOf(found.Start); got != saturday {
		t.Errorf("the sitting starting %v is on working day %v, want %v", found.Start.In(loc), got, saturday)
	}
	if got := c.DayOf(found.End); got != saturday {
		t.Errorf("the sitting ending %v is on working day %v, want %v", found.End.In(loc), got, saturday)
	}
	// And it is not split: the whole of it lands on the one day.
	slices := c.ByDay([]sessions.Session{*found}, sessions.Window{})
	if len(slices) != 1 {
		t.Fatalf("the sitting was spread over %d working days, want 1", len(slices))
	}
	if slices[0].Presence != found.Duration() {
		t.Errorf("Saturday holds %v of it, want all %v", slices[0].Presence, found.Duration())
	}
	if slices[0].Split != 0 {
		t.Errorf("the sitting was marked split; it crosses no 05:00 boundary")
	}
}

// The default boundary is 05:00 because no morning in the ground-truth set
// begins before it. This checks that against the corpus rather than against
// the claim: the earliest local start of any sitting that is NOT a late-night
// continuation of the day before.
//
// It also records the margin, which is small: the earliest morning start in
// this corpus is 05:06:24 local, six minutes and twenty-four seconds after the
// boundary — and that start already includes a five-minute lead-in, so the
// first OBSERVATION was at 05:11:24. A lead-in above 6m24s would push a real
// morning across the boundary and into the day before.
func TestNoMorningSittingBeginsBeforeTheDefaultBoundary(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	earliest := 24 * time.Hour
	var earliestAt time.Time
	var lateNight int
	for _, s := range realSessions(t) {
		lt := s.Start.In(loc)
		since := time.Duration(lt.Hour())*time.Hour + time.Duration(lt.Minute())*time.Minute +
			time.Duration(lt.Second())*time.Second
		if since < 5*time.Hour {
			// A start before 05:00 belongs to the previous working day. It is
			// not a morning and is counted rather than compared.
			lateNight++
			continue
		}
		if since < earliest {
			earliest, earliestAt = since, s.Start
		}
	}
	if lateNight != 2 {
		t.Errorf("%d sittings start before 05:00 local, want the 2 on 2026-07-12", lateNight)
	}
	if earliest < 5*time.Hour {
		t.Fatalf("a morning sitting begins at %v local, before the 05:00 boundary", earliest)
	}
	if earliest > 5*time.Hour+7*time.Minute {
		t.Errorf("the earliest morning start is %v (%v); the corpus measured 05:06:24 and this "+
			"fixture no longer matches it", earliest, earliestAt.In(loc))
	}
	// And the two late-night sittings really do land on the day before: both
	// start at 01:39 local on Sunday 2026-07-12 and belong to Saturday.
	saturday := sessions.Day{Year: 2026, Month: time.July, Date: 11}
	var checked int
	for _, s := range realSessions(t) {
		lt := s.Start.In(loc)
		if lt.Hour() >= 5 {
			continue
		}
		checked++
		if got := c.DayOf(s.Start); got != saturday {
			t.Errorf("the sitting at %v was attributed to working day %v, want %v", lt, got, saturday)
		}
	}
	if checked != 2 {
		t.Errorf("checked %d late-night sittings, want 2", checked)
	}
}

// The boundary fitted against the corpus, day by day, through the same
// FirstStart a report prints. The earliest of them is 05:06:24 local on
// 2026-07-10, which is the whole evidence for 05:00 rather than 06:00.
func TestTheEarliestWorkingDayStartAcrossTheCorpusIsJustAfterFive(t *testing.T) {
	loc := chicago(t)
	c := calFor(loc)

	earliest := 24 * time.Hour
	var earliestDay sessions.Day
	var withoutAStart int
	for _, d := range c.ByDay(realSessions(t), sessions.Window{}) {
		if d.FirstStart.IsZero() {
			// No sitting BEGAN on this day. That is not a start of zero.
			withoutAStart++
			continue
		}
		start, _ := c.Bounds(d.Day)
		if since := d.FirstStart.Sub(start); since < earliest {
			earliest, earliestDay = since, d.Day
		}
	}
	if earliest < 0 {
		t.Fatalf("%v begins %v before its own boundary, which is impossible", earliestDay, -earliest)
	}
	// 2026-07-05 is the corpus's first day and opens at 19:50 local because
	// harvesting began mid-evening, so it is not the earliest.
	if want := 6*time.Minute + 24*time.Second; earliest != want {
		t.Errorf("the earliest sitting of any working day begins %v after the boundary (on %v), want %v "+
			"— 05:06:24 local", earliest, earliestDay, want)
	}
	if withoutAStart != 0 {
		t.Errorf("%d working days hold only a continuation; none was expected, since no sitting "+
			"crosses the 05:00 boundary in this corpus", withoutAStart)
	}
}

// The mutation the brief asks for, as a test rather than as a one-off run:
// moving the boundary to midnight must change which working day some real
// sittings fall on. If it changes nothing, the boundary is untested against
// this corpus and every figure resting on it is unearned.
func TestMovingTheBoundaryToMidnightChangesRealAttributions(t *testing.T) {
	loc := chicago(t)
	ss := realSessions(t)
	five := calFor(loc)
	midnight := sessions.Calendar{Location: loc, DayStart: sessions.Clock{}, WeekStart: time.Monday}

	var reassigned, splitAtMidnight int
	var movedTime time.Duration
	for _, s := range ss {
		if five.DayOf(s.Start) != midnight.DayOf(s.Start) {
			reassigned++
		}
		fiveSlices := five.ByDay([]sessions.Session{s}, sessions.Window{})
		midSlices := midnight.ByDay([]sessions.Session{s}, sessions.Window{})
		if len(midSlices) > len(fiveSlices) {
			splitAtMidnight++
			// The time that changed day is everything after the midnight cut.
			for _, d := range midSlices[1:] {
				movedTime += d.Presence
			}
		}
	}
	if reassigned+splitAtMidnight == 0 {
		t.Fatal("moving the boundary from 05:00 to midnight changed nothing on the real corpus; " +
			"the working-day boundary is untested against this data")
	}
	if reassigned != 2 {
		t.Errorf("%d sittings changed working day outright, want 2 (the pair at 01:39 on 2026-07-12)", reassigned)
	}
	if splitAtMidnight != 2 {
		t.Errorf("%d sittings were split by the midnight boundary, want 2 "+
			"(2026-07-11 21:33 and 2026-07-18 23:51)", splitAtMidnight)
	}
	// 1h37m24s from the sitting that ran to 01:37:24 on 2026-07-12, and 47m20s
	// from the one that ran to 00:47:20 on 2026-07-19.
	if want := 2*time.Hour + 24*time.Minute + 44*time.Second; movedTime != want {
		t.Errorf("the midnight boundary moved %v of presence to the following day, want %v",
			movedTime, want)
	}
	// Conservation survives the mutation: a different boundary regroups the
	// same total and never a different one.
	var atFive, atMidnight time.Duration
	for _, d := range five.ByDay(ss, sessions.Window{}) {
		atFive += d.Presence
	}
	for _, d := range midnight.ByDay(ss, sessions.Window{}) {
		atMidnight += d.Presence
	}
	if atFive != atMidnight || atFive != totalOf(ss) {
		t.Errorf("totals differ by boundary: 05:00 gives %v, midnight %v, ungrouped %v",
			atFive, atMidnight, totalOf(ss))
	}
}
