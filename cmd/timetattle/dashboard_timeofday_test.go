package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
)

// THE LOAD-BEARING PARTITIONING INVARIANT: a sitting spanning 09:30-11:15
// contributes 30m to hour 9, 60m to hour 10 and 15m to hour 11 — the exact
// example the task brief gives — and whatever the input, the hour buckets
// must sum to exactly the sitting's own duration. Written and run red before
// hourBuckets existed.
func TestHourBucketsPartitionsAcrossHourBoundariesExactly(t *testing.T) {
	start := time.Date(2026, 8, 3, 9, 30, 0, 0, time.UTC)
	end := time.Date(2026, 8, 3, 11, 15, 0, 0, time.UTC)

	got := hourBuckets(start, end, time.UTC)

	want := map[int]time.Duration{9: 30 * time.Minute, 10: 60 * time.Minute, 11: 15 * time.Minute}
	for h, d := range got {
		if w := want[h]; d != w {
			t.Errorf("hour %02d: got %v, want %v", h, d, w)
		}
	}

	var sum time.Duration
	for _, d := range got {
		sum += d
	}
	if want := end.Sub(start); sum != want {
		t.Errorf("bucket sum = %v, want the sitting's own duration %v", sum, want)
	}
}

// The invariant must hold for spans that do not line up on hour boundaries at
// either end, that cross midnight (so two calendar days but still just 24
// buckets), that sit entirely inside one hour, and that land exactly on a
// boundary.
func TestHourBucketsSumInvariantHoldsForEverySpanShape(t *testing.T) {
	day := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		start, end time.Time
	}{
		{"within one hour", day.Add(9*time.Hour + 5*time.Minute), day.Add(9*time.Hour + 40*time.Minute)},
		{"exact hour boundaries", day.Add(9 * time.Hour), day.Add(12 * time.Hour)},
		{"crosses midnight", day.Add(23*time.Hour + 30*time.Minute), day.Add(25*time.Hour + 15*time.Minute)},
		{"spans several days", day, day.Add(50*time.Hour + 7*time.Minute)},
		{"one second", day.Add(14*time.Hour + 59*time.Minute + 59*time.Second), day.Add(15 * time.Hour)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := hourBuckets(c.start, c.end, time.UTC)
			var sum time.Duration
			for _, d := range got {
				if d < 0 {
					t.Errorf("bucket went negative: %v", d)
				}
				sum += d
			}
			if want := c.end.Sub(c.start); sum != want {
				t.Errorf("bucket sum = %v, want %v", sum, want)
			}
		})
	}
}

// A degenerate span (End not after Start) contributes nothing and must not
// hang or panic.
func TestHourBucketsEmptyForNonPositiveSpan(t *testing.T) {
	at := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC)
	got := hourBuckets(at, at, time.UTC)
	for h, d := range got {
		if d != 0 {
			t.Errorf("hour %02d: got %v for a zero-length span, want 0", h, d)
		}
	}
}

// Bucketing is by LOCAL wall clock, not by the instant's UTC hour — the
// timezone finding this task's job note records. A fixed +05:30 zone turns a
// 09:00 UTC instant into 14:30 local, and the minute worked there must land
// in local hour 14, not UTC hour 9.
func TestHourBucketsUsesLocalZoneNotUTC(t *testing.T) {
	ist := time.FixedZone("TEST+0530", 5*3600+30*60)
	start := time.Date(2026, 8, 3, 9, 0, 0, 0, time.UTC) // 14:30 in ist
	end := time.Date(2026, 8, 3, 9, 20, 0, 0, time.UTC)  // 14:50 in ist

	got := hourBuckets(start, end, ist)
	if got[9] != 0 {
		t.Errorf("hour 9 (the UTC hour) got %v, want 0 — bucketing must not use UTC", got[9])
	}
	if got[14] != 20*time.Minute {
		t.Errorf("hour 14 (the local hour) got %v, want 20m", got[14])
	}
}

// observedDayCount uses the SAME day-level collectionLog.during check the
// activity heatmap's own cells use (dashboardData.Log's doc comment in
// dashboard.go) — a day with any logged capture or harvest run counts as
// observed, whether or not it lost anything; a day with no run at all does
// not.
func TestObservedDayCountUsesDayGranularity(t *testing.T) {
	cal := mustCalendar(t)
	day0 := cal.DayOf(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	from, _ := cal.Bounds(day0)
	to, _ := cal.Bounds(day0.AddDays(3))
	period := dashboardPeriod{Days: 3, From: from, To: to}

	// A run lands inside day0 and inside day0+2, but nothing is ever logged
	// for day0+1 — it must not count.
	loggedDay0Start, _ := cal.Bounds(day0)
	loggedDay2Start, _ := cal.Bounds(day0.AddDays(2))
	log := collectionLog{
		at:   []time.Time{loggedDay0Start.Add(time.Hour), loggedDay2Start.Add(time.Hour)},
		lost: []bool{false, false},
	}

	observed, total := observedDayCount(period, cal, log)
	if total != 3 {
		t.Fatalf("total = %d, want 3", total)
	}
	if observed != 2 {
		t.Errorf("observed = %d, want 2 (day0 and day0+2, not day0+1)", observed)
	}
}

// An empty log (nothing ever recorded a capture or harvest run) leaves every
// day unobserved — the state a real historical corpus is mostly in, per
// series.go's own doc comment on collectionUnrecorded.
func TestObservedDayCountZeroWhenLogIsEmpty(t *testing.T) {
	cal := mustCalendar(t)
	day0 := cal.DayOf(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	from, _ := cal.Bounds(day0)
	to, _ := cal.Bounds(day0.AddDays(5))
	period := dashboardPeriod{Days: 5, From: from, To: to}

	observed, total := observedDayCount(period, cal, collectionLog{})
	if total != 5 {
		t.Fatalf("total = %d, want 5", total)
	}
	if observed != 0 {
		t.Errorf("observed = %d, want 0", observed)
	}
}

// mustSection builds a minimal but real dashboardData: a config (for its
// calendar), a Report carrying only the Sessions this section reads, and a
// collectionLog. Everything else a section might reach for is left zero,
// which is fine — renderTimeOfDay touches only Config, Report.Sessions and
// Log.
func mustSection(t *testing.T, sessionsIn []sessions.Session, log collectionLog) *dashboardData {
	t.Helper()
	return &dashboardData{
		Config: config.Default(),
		Report: metrics.Report{Sessions: sessionsIn},
		Log:    log,
	}
}

// CRITERION hsq. With no collection ever logged for the period, an hour with
// no sitting minutes must render as UNOBSERVED, distinctly from an hour that
// really was watched and found idle — and it must do so even though hour 9
// (which DOES have real sitting minutes) is fully legible regardless of what
// the log says, since real evidence is never in question.
func TestRenderTimeOfDayMarksUnobservedHoursDistinctlyFromIdleHours(t *testing.T) {
	cal := mustCalendar(t)
	day0 := cal.DayOf(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	from, _ := cal.Bounds(day0)
	to, _ := cal.Bounds(day0.AddDays(2))
	period := dashboardPeriod{Days: 2, From: from, To: to}

	// CIVIL hour 09:00-10:00, not "9 hours after the working day's own
	// 05:00 boundary" — hourBuckets partitions by civil wall clock, which is
	// what a reader means by "hour 9", and using the working-day boundary
	// instead was a fixture bug this test caught on its first run: the
	// default calendar's DayStart is 05:00, so from.Add(9*time.Hour) is
	// 14:00 local, not 09:00.
	sit := sessions.Session{
		Start: civilAt(cal, day0, 9, 0),
		End:   civilAt(cal, day0, 10, 0),
	}

	// Case A: nothing was ever logged. Hour 9 has real minutes and must still
	// render from the ramp; hour 3 has none and must render UNOBSERVED.
	unrecorded := mustSection(t, []sessions.Session{sit}, collectionLog{})
	var outA bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &outA, metricsScope{}, period, colorNone, unrecorded); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	stripA := stripLine(t, outA.String())
	if stripA[3] != '?' {
		t.Errorf("case A hour 3 (no minutes, nothing ever logged) = %q, want the unobserved glyph '?':\n%s",
			stripA[3], outA.String())
	}
	if stripA[9] == '?' {
		t.Errorf("case A hour 9 (real sitting minutes) rendered as unobserved despite real evidence:\n%s", outA.String())
	}

	// Case B: every day in the period is logged. Hour 3 still has no
	// minutes, but now it is OBSERVED IDLE, not unobserved — a different
	// glyph.
	loggedDay0, _ := cal.Bounds(day0)
	loggedDay1, _ := cal.Bounds(day0.AddDays(1))
	logged := collectionLog{
		at:   []time.Time{loggedDay0.Add(time.Hour), loggedDay1.Add(time.Hour)},
		lost: []bool{false, false},
	}
	recorded := mustSection(t, []sessions.Session{sit}, logged)
	var outB bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &outB, metricsScope{}, period, colorNone, recorded); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	stripB := stripLine(t, outB.String())
	if stripB[3] == '?' {
		t.Errorf("case B hour 3 (no minutes, but every day logged) still rendered unobserved:\n%s", outB.String())
	}
}

// The busiest hour renders at the top of the ASCII ramp and a genuinely idle,
// observed hour renders at the bottom — so the strip's shape is legible
// regardless of how many days the period covers (criterion 4sE).
func TestRenderTimeOfDayNormalizesToTheBusiestHour(t *testing.T) {
	cal := mustCalendar(t)
	day0 := cal.DayOf(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	from, _ := cal.Bounds(day0)
	to, _ := cal.Bounds(day0.AddDays(1))
	period := dashboardPeriod{Days: 1, From: from, To: to}

	sit := sessions.Session{Start: civilAt(cal, day0, 9, 0), End: civilAt(cal, day0, 10, 0)}
	logStart, _ := cal.Bounds(day0)
	log := collectionLog{at: []time.Time{logStart.Add(time.Hour)}, lost: []bool{false}}

	data := mustSection(t, []sessions.Session{sit}, log)
	var out bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	strip := stripLine(t, out.String())
	if strip[9] != '@' {
		t.Errorf("the only worked hour (9) = %q, want the top of the ramp '@':\n%s", strip[9], out.String())
	}
	if strip[14] != ' ' {
		t.Errorf("an untouched, observed hour (14) = %q, want the bottom of the ramp ' ':\n%s", strip[14], out.String())
	}
}

// The label line marks 00/06/12/18 at the columns those hours occupy.
func TestRenderTimeOfDayLabelsMarkQuarterDayHours(t *testing.T) {
	data := mustSection(t, nil, collectionLog{})
	period := dashboardPeriodOf(1, mustCalendar(t), time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC))
	var out bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("expected at least a strip line and a label line, got %d lines:\n%s", len(lines), out.String())
	}
	labels := lines[1]
	for _, want := range []struct {
		at  int
		txt string
	}{{0, "00"}, {6, "06"}, {12, "12"}, {18, "18"}} {
		got := strings.TrimPrefix(labels, "  ")
		if len(got) < want.at+2 || got[want.at:want.at+2] != want.txt {
			t.Errorf("label line %q does not have %q at column %d", labels, want.txt, want.at)
		}
	}
}

// A fully-observed period (every day logged) needs no weighting caveat; a
// partially-observed one states its weighting basis in one trailing line.
func TestRenderTimeOfDayStatesWeightingOnlyWhenPartial(t *testing.T) {
	cal := mustCalendar(t)
	day0 := cal.DayOf(time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC))
	from, _ := cal.Bounds(day0)
	to, _ := cal.Bounds(day0.AddDays(2))
	period := dashboardPeriod{Days: 2, From: from, To: to}

	fullyLoggedStart0, _ := cal.Bounds(day0)
	fullyLoggedStart1, _ := cal.Bounds(day0.AddDays(1))
	full := mustSection(t, nil, collectionLog{
		at:   []time.Time{fullyLoggedStart0.Add(time.Hour), fullyLoggedStart1.Add(time.Hour)},
		lost: []bool{false, false},
	})
	var outFull bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &outFull, metricsScope{}, period, colorNone, full); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	if strings.Contains(outFull.String(), "weighted") {
		t.Errorf("a fully-observed period printed a weighting caveat it does not need:\n%s", outFull.String())
	}

	partial := mustSection(t, nil, collectionLog{
		at:   []time.Time{fullyLoggedStart0.Add(time.Hour)},
		lost: []bool{false},
	})
	var outPartial bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &outPartial, metricsScope{}, period, colorNone, partial); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	if !strings.Contains(outPartial.String(), "1 observed day") || !strings.Contains(outPartial.String(), "of 2 in period") {
		t.Errorf("a partially-observed period did not state its weighting basis:\n%s", outPartial.String())
	}
}

// colorNone output carries no escape codes, ever — the same rule
// TestDashboardEmitsNoEscapeCodesWhenCaptured proves for the whole report.
func TestRenderTimeOfDayColorNoneEmitsNoEscapeCodes(t *testing.T) {
	data := mustSection(t, []sessions.Session{{
		Start: time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC),
	}}, collectionLog{})
	period := dashboardPeriodOf(1, mustCalendar(t), time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC))
	var out bytes.Buffer
	if err := renderTimeOfDay(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderTimeOfDay: %v", err)
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Errorf("colorNone output contained an escape code:\n%q", out.String())
	}
}

// civilAt builds the instant of the given CIVIL clock hour and minute on day
// d, in cal's own zone — distinct from cal.Bounds(d), which is the working
// day's boundary (DayStart, 05:00 by default) and not civil midnight.
// hourBuckets partitions by civil wall clock, so a test constructing "hour
// 9" must mean 09:00 local, not nine hours past the working day's start.
func civilAt(cal sessions.Calendar, d sessions.Day, hour, minute int) time.Time {
	return time.Date(d.Year, d.Month, d.Date, hour, minute, 0, 0, cal.Location)
}

// stripLine extracts the 24-wide hour strip (the first line, minus its
// two-space indent) from a rendered section, failing loudly if it is not
// exactly hoursPerDay runes wide.
func stripLine(t *testing.T, rendered string) []rune {
	t.Helper()
	lines := strings.Split(rendered, "\n")
	if len(lines) == 0 {
		t.Fatalf("rendered output has no lines at all")
	}
	strip := []rune(strings.TrimPrefix(lines[0], "  "))
	if len(strip) != hoursPerDay {
		t.Fatalf("strip line %q is %d runes wide, want %d", lines[0], len(strip), hoursPerDay)
	}
	return strip
}
