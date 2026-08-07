package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/jobs"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/transcripts"
)

// The component stats panel, and the one distinction it exists to keep: a
// source that was never collecting must not be rendered with the same glyph
// as a source that collected and found nothing. Design principle 3 in
// project/2026-08-07-cli-overhaul-plan.md; the absence-of-a-row rule this
// project has already shipped as a bug in three separate tools.
//
// Every assertion here is on PLAIN text under colorNone, which is what a
// captured run produces (see dashboard_test.go's own note on why a buffer is
// never a terminal) — so a section is free to paint, and is never allowed to
// make painting load-bearing for what it says.

// statsDay is a fixed instant every fixture in this file is dated against, so
// no test depends on the day it is run.
var statsDay = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// statsPeriod is a short window, deliberately not the 365-day default: a
// seven-day period makes the per-day divisor small enough to check by hand.
func statsPeriod(t *testing.T) dashboardPeriod {
	t.Helper()
	return dashboardPeriodOf(7, mustCalendar(t), statsDay)
}

// statsData assembles the shared handle the way dashboardCmd does, minus the
// store: every figure this section prints comes from Events, Report and Log,
// and a section that needed a live database to be TESTED could not have its
// no-data rule exercised at all.
func statsData(events []source.Event, log collectionLog) *dashboardData {
	return &dashboardData{
		Config: config.Default(),
		Events: events,
		Log:    log,
	}
}

// collectedThroughout is a collection log with one clean run on every day of
// the period: collection demonstrably ran, so a zero in it is an OBSERVED
// zero and must render as 0.
func collectedThroughout(p dashboardPeriod) collectionLog {
	var log collectionLog
	for at := p.From.Add(time.Hour); at.Before(p.To); at = at.AddDate(0, 0, 1) {
		log.add(at, false)
	}
	return log
}

func renderStats(t *testing.T, scope metricsScope, p dashboardPeriod, data *dashboardData) string {
	t.Helper()
	var out bytes.Buffer
	if err := renderComponentStats(context.Background(), &out, scope, p, colorNone, data); err != nil {
		t.Fatalf("renderComponentStats: %v", err)
	}
	return out.String()
}

// lineWith returns the one rendered line whose label is label, so an
// assertion names the row it is about rather than grepping the whole panel.
func lineWith(t *testing.T, out, label string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label) {
			return line
		}
	}
	t.Fatalf("no row labelled %q in:\n%s", label, out)
	return ""
}

// CRITERION 5eZ, AND THE REASON THIS TASK EXISTS IN THIS FORM.
//
// Nothing was collected in the window and nothing was observed in it. Every
// counted row must say so. A single "0" anywhere in the panel is the bug:
// it asserts that a source ran and found nothing, which nobody measured.
func TestComponentStatsRendersUnrecordedRatherThanZeroWhenNothingWasCollected(t *testing.T) {
	p := statsPeriod(t)
	out := renderStats(t, metricsScope{}, p, statsData(nil, collectionLog{}))

	for _, label := range []string{"commits", "lines +", "file writes", "tasks closed", "claude records"} {
		row := lineWith(t, out, label)
		if !strings.Contains(row, "unrecorded") {
			t.Errorf("row %q reads %q, want it to say unrecorded", label, strings.TrimSpace(row))
		}
		if strings.Contains(row, " 0 ") || strings.HasSuffix(strings.TrimRight(row, " "), " 0") {
			t.Errorf("row %q reads %q — a zero here claims a source ran and found nothing",
				label, strings.TrimSpace(row))
		}
	}
}

// The other half of the same distinction, and the half that is easy to get
// wrong by rendering both as a dash: collection DID run across the window and
// observed nothing. That is a measurement, and it is 0.
func TestComponentStatsRendersZeroWhenCollectionRanAndObservedNothing(t *testing.T) {
	p := statsPeriod(t)
	out := renderStats(t, metricsScope{}, p, statsData(nil, collectedThroughout(p)))

	for _, label := range []string{"commits", "file writes", "tasks closed", "claude records"} {
		row := lineWith(t, out, label)
		if strings.Contains(row, "unrecorded") {
			t.Errorf("row %q reads %q — collection ran across this window, so this is an observed zero",
				label, strings.TrimSpace(row))
		}
		if !strings.Contains(row, "0") {
			t.Errorf("row %q reads %q, want an observed 0", label, strings.TrimSpace(row))
		}
	}
}

// A count that is not zero can never be unrecorded, whatever the collection
// log says: the events are themselves proof the source was being read. This
// pins the gate to its actual job — deciding how a ZERO reads — rather than
// letting it suppress figures that exist.
func TestComponentStatsNeverCallsANonZeroCountUnrecorded(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(36 * time.Hour)
	events := []source.Event{statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 4, Deletions: 1})}

	out := renderStats(t, metricsScope{}, p, statsData(events, collectionLog{}))
	row := lineWith(t, out, "commits")
	if strings.Contains(row, "unrecorded") {
		t.Errorf("commits reads %q, but one commit was observed in the window", strings.TrimSpace(row))
	}
	if !strings.Contains(row, "1") {
		t.Errorf("commits reads %q, want the one observed commit", strings.TrimSpace(row))
	}
}

// CRITERION 8g7: a total AND a per-day average, on every counted row.
//
// The divisor is OBSERVED days, not the period's calendar days — see
// dashboard_stats.go's own comment for why. Here two days of the seven were
// observed, so three commits average 1.50/day and never 3/7 = 0.43.
func TestComponentStatsAveragesOverObservedDaysNotCalendarDays(t *testing.T) {
	p := statsPeriod(t)
	var log collectionLog
	log.add(p.From.Add(time.Hour), false)
	log.add(p.From.Add(25*time.Hour), false)

	day := p.From.Add(2 * time.Hour)
	events := []source.Event{
		statsCommit(day, gitlog.Stat{FilesChanged: 1, Insertions: 10, Deletions: 2}),
		statsCommit(day.Add(time.Minute), gitlog.Stat{FilesChanged: 1, Insertions: 20, Deletions: 4}),
		statsCommit(day.Add(24*time.Hour), gitlog.Stat{FilesChanged: 1, Insertions: 30, Deletions: 6}),
	}

	out := renderStats(t, metricsScope{}, p, statsData(events, log))
	if days := lineWith(t, out, "observed days"); !strings.Contains(days, "2 of 7") {
		t.Errorf("observed days reads %q, want 2 of 7", strings.TrimSpace(days))
	}
	if row := lineWith(t, out, "commits"); !strings.Contains(row, "1.50") {
		t.Errorf("commits reads %q, want 3 over 2 observed days = 1.50/day", strings.TrimSpace(row))
	}
}

// A day carrying an observation is an observed day even where no collection
// run was recorded inside it — a harvest on Thursday ingests Tuesday's
// evidence, which is exactly why collectionLog documents itself as "was
// collection running" and not "is this period's evidence complete".
func TestComponentStatsCountsADayWithEvidenceAsObservedWithoutARun(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(50 * time.Hour)
	events := []source.Event{statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 1, Deletions: 0})}

	out := renderStats(t, metricsScope{}, p, statsData(events, collectionLog{}))
	if days := lineWith(t, out, "observed days"); !strings.Contains(days, "1 of 7") {
		t.Errorf("observed days reads %q, want 1 of 7", strings.TrimSpace(days))
	}
}

// LOC is recorded — internal/gitlog carries a diff-stat on every non-merge
// commit event's State (see gitlog.Stat and ParseStat) — so it is a live row
// rather than an out-of-reach one. A MERGE commit carries no diff at all, and
// gitlog keeps that distinct from an observed empty diff on purpose; folding
// merges into the sum as zeroes would be the same collapse one level down.
func TestComponentStatsSumsInsertionsAndDeletionsAndExcludesMerges(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(3 * time.Hour)
	merge := statsCommit(at.Add(time.Minute), gitlog.Stat{})
	merge.State = gitlog.StateMerge
	events := []source.Event{
		statsCommit(at, gitlog.Stat{FilesChanged: 2, Insertions: 100, Deletions: 40}),
		merge,
		statsCommit(at.Add(2*time.Minute), gitlog.Stat{FilesChanged: 1, Insertions: 5, Deletions: 0}),
	}

	out := renderStats(t, metricsScope{}, p, statsData(events, collectedThroughout(p)))
	if row := lineWith(t, out, "commits"); !strings.Contains(row, "3") {
		t.Errorf("commits reads %q, want all 3 commits counted including the merge", strings.TrimSpace(row))
	}
	if row := lineWith(t, out, "lines +"); !strings.Contains(row, "105") {
		t.Errorf("lines + reads %q, want 105", strings.TrimSpace(row))
	}
	if row := lineWith(t, out, "lines -"); !strings.Contains(row, "40") {
		t.Errorf("lines - reads %q, want 40", strings.TrimSpace(row))
	}
	if !strings.Contains(out, "merge") {
		t.Errorf("nothing in the panel says a merge commit carried no diff:\n%s", out)
	}
}

// The remaining live counts, one fixture each, so a miswired source id shows
// up as the row that stayed at zero rather than as a plausible total.
func TestComponentStatsCountsFilesJobsAndTranscripts(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(4 * time.Hour)
	events := []source.Event{
		statsFile(at, "/w/one.txt"),
		statsFile(at.Add(time.Minute), "/w/one.txt"),
		statsFile(at.Add(2*time.Minute), "/w/two.txt"),
		statsJobEvent(at, "abc", "done"),
		statsJobEvent(at.Add(time.Minute), "def", "note"),
		statsJobEvent(at.Add(2*time.Minute), "ghi", "done"),
		statsTranscript(at, transcripts.StateHuman),
		statsTranscript(at.Add(time.Minute), transcripts.StateMachine),
		statsTranscript(at.Add(2*time.Minute), transcripts.StateMachine),
	}

	out := renderStats(t, metricsScope{}, p, statsData(events, collectedThroughout(p)))
	for _, c := range []struct{ label, want string }{
		{"file writes", "3"},
		{"files touched", "2"},
		{"tasks closed", "2"},
		{"claude records", "3"},
		{"claude prompts", "1"},
	} {
		if row := lineWith(t, out, c.label); !strings.Contains(row, c.want) {
			t.Errorf("%s reads %q, want %s", c.label, strings.TrimSpace(row), c.want)
		}
	}
}

// A stat the data model does not record AT ALL is a third state again, and it
// is neither an observed zero nor an unrecorded window: nothing anywhere in
// the store could ever answer it. It is printed rather than omitted, because
// a row that silently vanished is the absence this project keeps misreading
// as an observation — and it is never printed as a number.
func TestComponentStatsPrintsUnreachableStatsAsUnmeasuredWithAReason(t *testing.T) {
	p := statsPeriod(t)
	out := renderStats(t, metricsScope{}, p, statsData(nil, collectedThroughout(p)))

	for _, label := range []string{"jobs agents", "claude sessions", "tokens, cost"} {
		row := lineWith(t, out, label)
		if !strings.Contains(row, "not measured") {
			t.Errorf("row %q reads %q, want it marked not measured", label, strings.TrimSpace(row))
		}
		if strings.ContainsAny(strings.TrimPrefix(strings.TrimSpace(row), label), "0123456789") {
			t.Errorf("row %q reads %q — a stat nothing records must carry no figure",
				label, strings.TrimSpace(row))
		}
		if len(strings.TrimSpace(row)) <= len(label)+len(" not measured")+4 {
			t.Errorf("row %q reads %q, want it to name WHY nothing records it",
				label, strings.TrimSpace(row))
		}
	}
}

// Scope selects what is counted, using metricsScope's own showsProject
// against the report's client roll-up — the same predicate metrics.go's
// report functions use, never a second filtering rule.
func TestComponentStatsCountsOnlyTheScopedProject(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(5 * time.Hour)
	mine := statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 7, Deletions: 1})
	mine.Identity.Project = "alpha"
	theirs := statsCommit(at.Add(time.Minute), gitlog.Stat{FilesChanged: 1, Insertions: 900, Deletions: 900})
	theirs.Identity.Project = "beta"

	data := statsData([]source.Event{mine, theirs}, collectedThroughout(p))
	data.Report = metrics.Report{
		Projects: []metrics.Project{{Name: "alpha"}, {Name: "beta"}},
		Clients:  []metrics.Client{{Name: "c1", Projects: []string{"alpha", "beta"}}},
	}

	out := renderStats(t, metricsScope{Project: "alpha"}, p, data)
	if row := lineWith(t, out, "commits"); !strings.Contains(row, "1") || strings.Contains(row, "2") {
		t.Errorf("commits reads %q, want only alpha's single commit", strings.TrimSpace(row))
	}
	if row := lineWith(t, out, "lines +"); !strings.Contains(row, "7") {
		t.Errorf("lines + reads %q, want alpha's 7 and not beta's 900", strings.TrimSpace(row))
	}
}

// Hours are REUSED from the report metrics already computed, never
// re-derived from Events: presence is wall clock across every source at once
// and a section that re-summed it would be a second answer to the one
// question metrics exists to answer.
func TestComponentStatsTakesPresenceHoursFromTheReport(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{Presence: metrics.Span{Total: 10 * time.Hour, Observed: 4 * time.Hour}}

	out := renderStats(t, metricsScope{}, p, data)
	if row := lineWith(t, out, "presence"); !strings.Contains(row, "10.0") {
		t.Errorf("presence reads %q, want the report's own 10h", strings.TrimSpace(row))
	}
}

// A filtered scope takes the SCOPED figure out of the same report — a
// project's Engaged span — rather than the store-wide presence total, which
// would credit one project with every other project's hours.
func TestComponentStatsTakesTheScopedProjectsOwnHours(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{
		Presence: metrics.Span{Total: 100 * time.Hour},
		Projects: []metrics.Project{{Name: "alpha", Engaged: metrics.Span{Total: 3 * time.Hour}}},
		Clients:  []metrics.Client{{Name: "c1", Projects: []string{"alpha"}}},
	}

	out := renderStats(t, metricsScope{Project: "alpha"}, p, data)
	row := lineWith(t, out, "presence")
	if !strings.Contains(row, "3.0") || strings.Contains(row, "100.0") {
		t.Errorf("presence reads %q, want alpha's own 3h and not the store's 100h", strings.TrimSpace(row))
	}
}

// The backlight is the literal screen-on reading, and internal/metrics
// reports it store-wide because knowledgeC and Biome carry no path and can
// never be attributed to a project (metrics.ReasonUnlocatable). Printing a
// store-wide figure under a project filter would attribute it; printing
// nothing would read as zero. So the row says which it is.
func TestComponentStatsSaysScreenOnIsStoreWideUnderAFilteredScope(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{
		Backlight: metrics.Backlight{Readings: 12, Lit: 6 * time.Hour},
		Projects:  []metrics.Project{{Name: "alpha"}},
		Clients:   []metrics.Client{{Name: "c1", Projects: []string{"alpha"}}},
	}

	out := renderStats(t, metricsScope{Project: "alpha"}, p, data)
	row := lineWith(t, out, "screen-on")
	if !strings.Contains(row, "store-wide") {
		t.Errorf("screen-on reads %q, want it to say the figure is store-wide", strings.TrimSpace(row))
	}
}

// No backlight reading at all is the third state internal/metrics already
// keeps for it (Backlight.Readings, and Unwitnessed beside Lit). Zero lit
// hours out of zero readings is not an observation that the screen was off.
func TestComponentStatsCallsScreenOnUnrecordedWithNoReadings(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{Backlight: metrics.Backlight{Readings: 0}}

	out := renderStats(t, metricsScope{}, p, data)
	if row := lineWith(t, out, "screen-on"); !strings.Contains(row, "unrecorded") {
		t.Errorf("screen-on reads %q, want unrecorded — no reading was ever taken", strings.TrimSpace(row))
	}
}

// Color is emphasis and never meaning: the panel must carry no escape
// sequence under colorNone, which is what every captured run and every test
// gets. See dashcolor.go's own note on why a pipe has no color.
func TestComponentStatsWritesNoEscapeSequencesUnderColorNone(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(6 * time.Hour)
	events := []source.Event{
		statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 2, Deletions: 1}),
		statsTranscript(at, transcripts.StateHuman),
	}
	out := renderStats(t, metricsScope{}, p, statsData(events, collectedThroughout(p)))
	// Asserted against a panel that actually rendered its rows, so this is a
	// real guard rather than a sentence that is vacuously true of anything
	// short — a test that is green before the code exists is testing nothing.
	if row := lineWith(t, out, "commits"); !strings.Contains(row, "1") {
		t.Fatalf("commits reads %q, so this test would pass on an empty panel", strings.TrimSpace(row))
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("colorNone output carries an escape sequence:\n%q", out)
	}
}

// Column padding puts the next column in the same place on every row, so it
// has to be written — but a line that ENDS in it carries trailing whitespace a
// reader cannot see and a diff of two captured runs can. Caught by eye on the
// first rendered fixture, pinned here.
func TestComponentStatsLeavesNoTrailingWhitespace(t *testing.T) {
	p := statsPeriod(t)
	// Both renderings: a full table, and the all-unrecorded one, whose rows
	// carry no note at all and are therefore the pure padding case.
	for _, data := range []*dashboardData{
		statsData([]source.Event{statsCommit(p.From.Add(time.Hour), gitlog.Stat{Insertions: 3})},
			collectedThroughout(p)),
		statsData(nil, collectionLog{}),
	} {
		for line := range strings.SplitSeq(renderStats(t, metricsScope{}, p, data), "\n") {
			if line != strings.TrimRight(line, " ") {
				t.Errorf("line %q ends in whitespace", line)
			}
		}
	}
}

// Column widths were fixed at 9 and 11 for the Value and Avg columns, sized
// for fixture magnitudes rather than measured from real ones. A live run
// (job kGFYV's own evidence) produced "observed days 181 of 3…" — the
// divisor's OWN value truncated, which format.go's package doc calls out by
// name as the one thing a report must never shorten — and "lines +
// 2240021237.58/day", where 224002 and 1237.58/day (both real widths) ran
// together with no separating space because the average happened to fill its
// column exactly. This fixture reproduces both real figures.
func TestComponentStatsColumnsFitLiveWidthNumbersWithoutTruncationOrCollision(t *testing.T) {
	cal := mustCalendar(t)
	p := dashboardPeriodOf(365, cal, statsDay)

	// 181 of the 365 days carry a logged run — the rest predate collection,
	// mirroring a store whose harvest_run table reaches back a fraction of a
	// trailing-year window.
	var log collectionLog
	for i := range 181 {
		start, _ := cal.Bounds(cal.DayOf(p.From).AddDays(i))
		log.add(start.Add(time.Hour), false)
	}

	// 224002 insertions over 181 observed days is 1237.58/day, both live
	// widths: a six-digit count and a four-digit-plus-decimal average.
	events := []source.Event{statsCommit(p.From.Add(time.Minute), gitlog.Stat{Insertions: 224002})}

	out := renderStats(t, metricsScope{}, p, statsData(events, log))

	days := lineWith(t, out, "observed days")
	if !strings.Contains(days, "181 of 365") {
		t.Errorf("observed days reads %q, want the untruncated divisor 181 of 365", strings.TrimSpace(days))
	}

	lines := lineWith(t, out, "lines +")
	if !strings.Contains(lines, "224002") {
		t.Errorf("lines + reads %q, want the untruncated count 224002", strings.TrimSpace(lines))
	}
	if !strings.Contains(lines, "1237.58/day") {
		t.Errorf("lines + reads %q, want the untruncated average 1237.58/day", strings.TrimSpace(lines))
	}
	if strings.Contains(lines, "2240021237.58") {
		t.Errorf("lines + reads %q — the count and the average ran together with no separating space",
			strings.TrimSpace(lines))
	}
}

// "45 merge commits carried no diff and is summed" was live output (job
// kGFYV) — the noun pluralised correctly but the verb did not, the same class
// of bug the terseness pass's "2 week iss" regression already caught once.
// Both the singular and plural forms are pinned so neither can regress alone.
func TestComponentStatsMergeNoteAgreesInNumber(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(time.Hour)

	one := statsCommit(at, gitlog.Stat{})
	one.State = gitlog.StateMerge
	outOne := renderStats(t, metricsScope{}, p, statsData([]source.Event{one}, collectedThroughout(p)))
	if row := lineWith(t, outOne, "commits"); !strings.Contains(row, "1 merge commit carried no diff and is summed") {
		t.Errorf("commits reads %q, want singular agreement (1 merge commit ... is summed)",
			strings.TrimSpace(row))
	}

	twoA := statsCommit(at, gitlog.Stat{})
	twoA.State = gitlog.StateMerge
	twoB := statsCommit(at.Add(time.Minute), gitlog.Stat{})
	twoB.State = gitlog.StateMerge
	outTwo := renderStats(t, metricsScope{}, p, statsData([]source.Event{twoA, twoB}, collectedThroughout(p)))
	if row := lineWith(t, outTwo, "commits"); !strings.Contains(row, "2 merge commits carried no diff and are summed") {
		t.Errorf("commits reads %q, want plural agreement (2 merge commits ... are summed)",
			strings.TrimSpace(row))
	}
}

// Fixtures. Each mirrors the event its own source package actually writes —
// see internal/gitlog, internal/fsobserve, internal/jobs and
// internal/transcripts — so a change to any of those shapes breaks this
// section's test rather than silently zeroing a row.

func statsCommit(at time.Time, s gitlog.Stat) source.Event {
	e := source.NewPoint(at, source.Identity{}, gitlog.CommitsID)
	e.Locator = "/w/repo"
	e.Cause = source.CauseUserAction
	e.State = s.String()
	return e
}

func statsFile(at time.Time, path string) source.Event {
	e := source.NewPoint(at, source.Identity{Document: path}, fsobserve.ID)
	e.Locator = path
	e.Cause = source.CauseUserAction
	e.State = string(fsobserve.ClassOther)
	return e
}

func statsJobEvent(at time.Time, task, typ string) source.Event {
	e := source.NewPoint(at, source.Identity{Document: task}, jobs.EventsID)
	e.Locator = "/w/repo"
	e.State = typ
	return e
}

func statsTranscript(at time.Time, state string) source.Event {
	e := source.NewPoint(at, source.Identity{Document: state + at.String()}, transcripts.ID)
	e.Locator = "/w/repo"
	e.State = state
	return e
}
