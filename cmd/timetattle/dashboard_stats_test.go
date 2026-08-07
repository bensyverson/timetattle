package main

import (
	"bytes"
	"context"
	"math"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/jobs"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/transcripts"
	"github.com/bensyverson/timetattle/internal/usage"
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
	// A prior version of this test also asserted the panel said "merge"
	// somewhere: the commits row carried a note explaining that a merge
	// commit's diff is not summed. That note lived in the per-row
	// commentary column this task removes entirely (job dGWPc); the merge
	// is still counted (asserted above — "commits" == 3) and still
	// excluded from the line sums (asserted above), which is the actual
	// behavior. Only the sentence explaining it is gone.
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

// CRITERION Txj, NARROWED BY TASK K2QMI. Of the three rows this panel used
// to print as "not measured" (a state that carried nothing but a reason, and
// had nowhere left to carry one once the commentary column that explained it
// was removed — job dGWPc, criterion 9BH), only "jobs agents" is dropped for
// good: Ben approved that one outright, and internal/jobs never SELECTs the
// actor column at all (see this file's own doc comment). "claude sessions"
// and the four token figures were the other two — pending the source-schema
// work under task 3jDIu (job GcgVp) at the time this test was first written,
// and back as real numeric rows now that K2QMI, this task, reads them out of
// Store.Usage. This test is altered rather than left passing on a stale
// premise: asserting their continued absence would pin exactly the omission
// this task exists to undo. See TestComponentStatsUsageRowsAreUnrecordedWithNoStore
// below for this file's replacement regression coverage of the one row still
// gone, and TestComponentStatsClaudeSessionsAndTokensRenderFromLiveUsage for
// the five rows that came back.
func TestComponentStatsOmitsJobsAgentsPermanently(t *testing.T) {
	p := statsPeriod(t)
	out := renderStats(t, metricsScope{}, p, statsData(nil, collectedThroughout(p)))

	if strings.Contains(out, "jobs agents") {
		t.Errorf("panel still prints %q, want it omitted for good: internal/jobs never SELECTs the actor "+
			"column, so there is no figure to render\n%s", "jobs agents", out)
	}
}

// The other half of criterion Txj's narrowing: the five rows Store.Usage now
// answers are never omitted, even with nothing to say — they print
// unrecorded, exactly like every other row in this panel with nothing
// behind it (see TestComponentStatsRendersUnrecordedRatherThanZeroWhenNothingWasCollected
// for the same rule applied to the older rows). data.Store is nil here, the
// same as every other test in this file that does not seed a live store —
// statsUsage's own nil guard renders every usage row unrecorded rather than
// panicking (dashboardCmd itself always opens a store; the guard is only
// ever exercised in a test).
func TestComponentStatsUsageRowsAreUnrecordedWithNoStore(t *testing.T) {
	p := statsPeriod(t)
	out := renderStats(t, metricsScope{}, p, statsData(nil, collectedThroughout(p)))

	for _, label := range []string{"claude sessions", "tokens out", "tokens in", "cache read", "cache written"} {
		row := lineWith(t, out, label)
		if !strings.Contains(row, "unrecorded") {
			t.Errorf("row %q reads %q, want unrecorded with no store to consult", label, strings.TrimSpace(row))
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
	// CRITERION dbI, the -all half: "attributed" is reserved for a scope and
	// must never appear on an unfiltered run.
	if strings.Contains(out, "attributed") {
		t.Errorf("an unscoped (-all) run prints \"attributed\" somewhere, want that label reserved for a "+
			"scope:\n%s", out)
	}
}

// A filtered scope takes the SCOPED figure out of the same report — a
// project's Engaged span — rather than the store-wide presence total, which
// would credit one project with every other project's hours.
//
// UPDATED BY job MAABc: this row was labeled "presence" under every scope,
// which Ben caught as a mislabel — the figure it prints under a scope is
// already Engaged.Total, not presence, and the row's own label now says so
// ("attributed"). The lineWith lookup below moved from "presence" to
// "attributed" for that reason; the figures asserted are unchanged.
func TestComponentStatsTakesTheScopedProjectsOwnHours(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{
		Presence: metrics.Span{Total: 100 * time.Hour},
		Projects: []metrics.Project{{Name: "alpha", Engaged: metrics.Span{Total: 3 * time.Hour}}},
		Clients:  []metrics.Client{{Name: "c1", Projects: []string{"alpha"}}},
	}

	out := renderStats(t, metricsScope{Project: "alpha"}, p, data)
	row := lineWith(t, out, "attributed")
	if !strings.Contains(row, "3.0") || strings.Contains(row, "100.0") {
		t.Errorf("attributed reads %q, want alpha's own 3h and not the store's 100h", strings.TrimSpace(row))
	}
}

// CRITERION dbI, the scoped half, red-first: under any scope the hours row
// reads "attributed", and the panel prints no bare "presence" row at all —
// that label is reserved for -all (see
// TestComponentStatsTakesPresenceHoursFromTheReport for the unscoped half,
// which already exercises "presence" and is unchanged by this task).
func TestComponentStatsLabelsTheHoursRowAttributedUnderAScope(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{
		Projects: []metrics.Project{{Name: "alpha", Engaged: metrics.Span{Total: 3 * time.Hour}}},
		Clients: []metrics.Client{{Name: "c1", Projects: []string{"alpha"},
			Engaged: metrics.Span{Total: 3 * time.Hour}}},
	}

	for _, scope := range []metricsScope{{Project: "alpha"}, {Client: "c1"}} {
		out := renderStats(t, scope, p, data)
		row := lineWith(t, out, "attributed")
		if !strings.Contains(row, "3.0") {
			t.Errorf("scope %+v: attributed reads %q, want alpha's/c1's own 3h", scope, strings.TrimSpace(row))
		}
		for line := range strings.SplitSeq(out, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "presence") {
				t.Errorf("scope %+v: still prints a bare \"presence\" row, want it reserved for -all: %q",
					scope, line)
			}
		}
	}
}

// The backlight is the literal screen-on reading, and internal/metrics
// reports it store-wide because knowledgeC and Biome carry no path and can
// never be attributed to a project (metrics.ReasonUnlocatable). Before job
// dGWPc the row said so with a note — "store-wide: the presence sources
// carry no path and attribute to no project" — printed in the per-row
// commentary column that task removed, and BwE29's pushback (c) flagged the
// gap this left: a reader who does not already know the backlight is
// store-wide misreads "3h" as alpha's own screen time under `-project
// alpha`. dGWPc considered folding "(store-wide)" into the row's Label and
// rejected it because statLabelWidth was a fixed 16 columns and pad()
// truncates rather than widens; this task (K2QMI) is the one that widens it
// (statLabelColumnWidth), so the row now carries the caveat in place — see
// statsScreenOnLabel.
//
// This test is ALTERED, not left passing on the old assertion: the row
// asserted here used to be pinned as identical to the unscoped row's label,
// which is exactly the gap BwE29 flagged and this task closes. Renamed to
// say what it now proves. The VALUE half of the old assertion — 6h
// regardless of scope, because the figure genuinely is store-wide — still
// holds and is still checked below.
func TestComponentStatsScreenOnCarriesAMachineWideQualifierUnderAFilteredScope(t *testing.T) {
	p := statsPeriod(t)
	data := statsData(nil, collectedThroughout(p))
	data.Report = metrics.Report{
		Backlight: metrics.Backlight{Readings: 12, Lit: 6 * time.Hour},
		Projects:  []metrics.Project{{Name: "alpha"}},
		Clients:   []metrics.Client{{Name: "c1", Projects: []string{"alpha"}}},
	}

	unscoped := lineWith(t, renderStats(t, metricsScope{}, p, data), "screen-on")
	if strings.Contains(unscoped, "machine-wide") {
		t.Errorf("the unscoped row reads %q, want the plain label: there is nothing to disambiguate from "+
			"under -all", strings.TrimSpace(unscoped))
	}

	for _, scope := range []metricsScope{{Project: "alpha"}, {Client: "c1"}} {
		out := renderStats(t, scope, p, data)
		row := lineWith(t, out, "screen-on")
		if !strings.Contains(row, "machine-wide") {
			t.Errorf("scope %+v: screen-on reads %q, want the machine-wide qualifier so a scoped reader "+
				"cannot mistake this for their own scope's figure", scope, strings.TrimSpace(row))
		}
		if !strings.Contains(row, "6.0") {
			t.Errorf("scope %+v: screen-on reads %q, want the backlight's own 6h regardless of scope",
				scope, strings.TrimSpace(row))
		}
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
//
// ALTERED by job yrAho, EXPLAINED RATHER THAN LEFT PASSING ON A STALE
// PREMISE: this test's own two figures (224002 and 1237.58/day) are both
// above yrAho's humanization threshold (1,000), so under yrAho's new
// default they no longer print as raw digits at all — they print "224.0k"
// and "1.2k/day", which is correct FOR the default and would make every
// assertion below fail for a reason that has nothing to do with column
// width. The bug this test exists to pin (kGFYV's live truncation and
// collision) is a property of rendering the RAW figure in a narrow column,
// which is now an -exact-only code path, so this test asks for -exact
// (data.Exact = true) to keep exercising exactly the scenario it always
// did. TestComponentStatsColumnWidthsFitBothHumanizedAndExactModes is this
// task's own sibling coverage of the humanized-default case.
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

	data := statsData(events, log)
	data.Exact = true
	out := renderStats(t, metricsScope{}, p, data)

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

// TestComponentStatsMergeNoteAgreesInNumber is REMOVED, not modified.
//
// It pinned "45 merge commits carried no diff and is summed" (live output,
// job kGFYV) getting its verb agreement right on both the singular and
// plural form. That sentence was the mergeNote printed in the per-row
// commentary column criterion 9BH removes entirely — commits are still
// counted including merges and merges are still excluded from the line
// sums (TestComponentStatsSumsInsertionsAndDeletionsAndExcludesMerges,
// above, still pins both numerically) — but there is no note left for a
// verb to agree or disagree in. A test asserting exact wording of a
// sentence that no longer exists would be pinning nothing.

// The trailing loss disclosure is the one sentence still printed below the
// table (renderComponentStats, not a per-row note), and it has to be
// conditional on data.Log actually recording a lost or partial run inside
// THIS window — printing it unconditionally would claim data loss on a
// totally healthy capture, the exact "no data means zero work" collapse
// this whole panel exists to avoid, one level up. The gate already existed
// in code (`if data.Log.during(...) == collectionLost`) before this task;
// this is the first test to pin it, in both directions.
func TestComponentStatsPrintsLossLineWhenAWindowRunLostSomething(t *testing.T) {
	p := statsPeriod(t)
	var log collectionLog
	log.add(p.From.Add(time.Hour), true) // lost
	out := renderStats(t, metricsScope{}, p, statsData(nil, log))
	if !strings.Contains(out, "lower bound") {
		t.Errorf("panel does not report a lower bound though a run inside the window lost part of what it read:\n%s", out)
	}
}

// The other half: a clean run in the window, and no run at all, must both
// print nothing about loss. Absence of loss is not itself printed — see this
// file's own doc comment on why "not measured" rows are omitted rather than
// asserted; the same discipline applies to a whole-table line as to a row.
func TestComponentStatsOmitsLossLineWhenNothingInTheWindowWasLost(t *testing.T) {
	p := statsPeriod(t)
	if out := renderStats(t, metricsScope{}, p, statsData(nil, collectedThroughout(p))); strings.Contains(out, "lower bound") {
		t.Errorf("panel claims a lower bound though every run in the window succeeded cleanly:\n%s", out)
	}
	if out := renderStats(t, metricsScope{}, p, statsData(nil, collectionLog{})); strings.Contains(out, "lower bound") {
		t.Errorf("panel claims a lower bound with no collection log recorded at all:\n%s", out)
	}
}

// Job K2QMI: claude sessions and the four token figures, read out of
// Store.Usage now that it exists (job GcgVp) and the window this panel
// describes is honestly clamped (job BwE29). Unlike every test above, these
// need a REAL store — Store.Usage is SQL, not something a fixture built from
// data.Events alone can exercise — so they use openUsageStore/
// harvestUsageEvents/attributeUsageProjects below, the same store.BeginRun /
// Write / Finish / ResolveProjects path internal/store's own usage_test.go
// and dashboard_freshness_test.go's TestRenderSourceFreshnessReadsTheStore
// already use for the same reason.

// The five rows, and the one thing they most need to prove: never summed.
// output (500+600=1100), input (3+4=7), cache read (1e8+2e8=3e8) and cache
// written (7+9=16) are four numbers of wildly different magnitude — cache
// read alone dwarfs every other figure here by six orders of magnitude,
// echoing GcgVp's own measurement that cache reads outweigh output tokens
// 223 to 1 on the live store — and a bug that folded them into one total
// would still print A number, just the wrong one. Asserting each figure
// individually is what catches that; asserting their sum would not.
//
// ALTERED by job yrAho, EXPLAINED RATHER THAN LEFT PASSING ON A STALE
// PREMISE: three of these four figures (1100, 100_000_000, 200_000_000) are
// above yrAho's humanization threshold and no longer print as raw digits by
// default. This test's own point — that four figures of wildly different
// magnitude are never folded into one total — needs the RAW digits to prove
// (a summing bug can still produce a wrong number that happens to round to
// a plausible-looking abbreviation at one decimal), so this asks for -exact
// to keep the precision the assertions below actually rely on.
func TestComponentStatsClaudeSessionsAndTokensRenderFromLiveUsage(t *testing.T) {
	p := statsPeriod(t)
	r1 := statsUsageEvent(t, p.From.Add(time.Hour), "u1", "sess-1", statsTokenFigures(3, 500, 100_000_000, 7))
	r2 := statsUsageEvent(t, p.From.Add(2*time.Hour), "u2", "sess-2", statsTokenFigures(4, 600, 200_000_000, 9))

	st := openUsageStore(t)
	harvestUsageEvents(t, st, []source.Event{r1, r2})

	data := statsData([]source.Event{r1, r2}, collectedThroughout(p))
	data.Store = st
	data.Filter = store.Filter{From: p.From, To: p.To}
	data.Exact = true

	out := renderStats(t, metricsScope{}, p, data)
	for _, c := range []struct{ label, want string }{
		{"claude sessions", "2"},
		{"tokens out", "1100"},
		{"tokens in", "7"},
		{"cache read", "300000000"},
		{"cache written", "16"},
	} {
		row := lineWith(t, out, c.label)
		if !strings.Contains(row, c.want) {
			t.Errorf("%s reads %q, want %s", c.label, strings.TrimSpace(row), c.want)
		}
		if strings.Contains(row, "unrecorded") {
			t.Errorf("%s reads %q, want a real figure — the store holds two payload-carrying records",
				c.label, strings.TrimSpace(row))
		}
	}
}

// CRITERION 8kU, THE WINDOW-BEFORE-ANY-PAYLOAD-EXISTS HALF. A transcript
// record stored before this source wrote payloads carries none for ever
// (event.payload NULL — TestARowStoredWithoutAPayloadReadsAsNothingRecorded
// in internal/store/usage_test.go pins the store side of this), and
// Store.Usage's own Observations answers "nothing was recorded", not zero.
// This is the older row, "claude records", right beside the new ones: the
// window WAS observed (claude records reads 1, an ordinary count) while the
// usage rows read unrecorded — the three-state distinction this whole panel
// exists to keep from collapsing, applied to the newest rows in it.
func TestComponentStatsUsageRowsAreUnrecordedBeforeAnyPayloadExists(t *testing.T) {
	p := statsPeriod(t)
	plain := statsTranscript(p.From.Add(time.Hour), transcripts.StateMachine)

	st := openUsageStore(t)
	harvestUsageEvents(t, st, []source.Event{plain})

	data := statsData([]source.Event{plain}, collectedThroughout(p))
	data.Store = st
	data.Filter = store.Filter{From: p.From, To: p.To}

	out := renderStats(t, metricsScope{}, p, data)
	if row := lineWith(t, out, "claude records"); !strings.Contains(row, "1") || strings.Contains(row, "unrecorded") {
		t.Fatalf("claude records reads %q, want the one observed record — this test is about a store "+
			"that DID observe something, just not a payload", strings.TrimSpace(row))
	}
	for _, label := range []string{"claude sessions", "tokens out", "tokens in", "cache read", "cache written"} {
		row := lineWith(t, out, label)
		if !strings.Contains(row, "unrecorded") {
			t.Errorf("%s reads %q, want unrecorded — the one stored record carries no payload at all",
				label, strings.TrimSpace(row))
		}
	}
}

// THE DIVISOR TREATMENT, THE HARD PART OF THIS TASK, RED-FIRST. Eight days
// of the ten-day window carry only git commits — ordinary evidence with no
// usage payload anywhere near it — and the last two carry the window's only
// payload-bearing records, 40 and 60 output tokens. "observed days" (the
// panel's GENERAL divisor) is honestly 10 of 10: every day in the window has
// SOME evidence. But dividing the 100-token total by 10 would spread a
// figure that only ever accrued on two days across eight it could not have
// touched — 10.00/day, which understates the true rate fivefold — so the
// usage rows must use their OWN divisor (the two payload-era days) and read
// 50.00/day instead. Before statsUsageRows/usageObservedDays existed, this
// test failed by not finding a "tokens out" row at all; the assertion
// against 10.00/day is the one a naive implementation sharing statRows' own
// `observed` would still fail after the row exists — proved by mutation
// during development (usageObservedDays swapped for the general `observed`
// count prints exactly 10.00/day; restored here).
func TestComponentStatsUsageAveragesOverPayloadEraDaysNotGeneralObservedDays(t *testing.T) {
	cal := mustCalendar(t)
	p := dashboardPeriodOf(10, cal, statsDay)

	var events []source.Event
	var log collectionLog
	for i := range 8 {
		start, _ := cal.Bounds(cal.DayOf(p.From).AddDays(i))
		at := start.Add(time.Hour)
		events = append(events, statsCommit(at, gitlog.Stat{Insertions: 1}))
		log.add(at, false)
	}
	day9, _ := cal.Bounds(cal.DayOf(p.From).AddDays(8))
	day10, _ := cal.Bounds(cal.DayOf(p.From).AddDays(9))
	r1 := statsUsageEvent(t, day9.Add(time.Hour), "u1", "sess-1", statsTokenFigures(0, 40, 0, 0))
	r2 := statsUsageEvent(t, day10.Add(time.Hour), "u2", "sess-1", statsTokenFigures(0, 60, 0, 0))
	events = append(events, r1, r2)

	st := openUsageStore(t)
	harvestUsageEvents(t, st, []source.Event{r1, r2})

	data := statsData(events, log)
	data.Store = st
	data.Filter = store.Filter{From: p.From, To: p.To}

	out := renderStats(t, metricsScope{}, p, data)

	if days := lineWith(t, out, "observed days"); !strings.Contains(days, "10 of 10") {
		t.Fatalf("observed days reads %q, want 10 of 10 — every day in this window carries SOME evidence, "+
			"which is what makes this a real test of the usage rows' own divisor rather than a window "+
			"that happens to have only two observed days anyway", strings.TrimSpace(days))
	}

	row := lineWith(t, out, "tokens out")
	if !strings.Contains(row, " 100 ") {
		t.Fatalf("tokens out reads %q, want the total 100", strings.TrimSpace(row))
	}
	if !strings.Contains(row, "50.00/day") {
		t.Errorf("tokens out reads %q, want 50.00/day (100 over the two payload-era days)", strings.TrimSpace(row))
	}
	if strings.Contains(row, "10.00/day") {
		t.Errorf("tokens out reads %q — averaged over the general 10-observed-day count instead of the "+
			"two payload-era days, diluting the true rate fivefold", strings.TrimSpace(row))
	}
}

// Store.UsageFilter has no Client field, so a -client scope is spelled here
// as one filter per project the client's own report entry lists, summed —
// the same overshoot store.Usage.Sessions already documents for one session
// touching two projects, extended to a client's basket of them (see
// statsUsageFilters' own comment). This also covers criterion OqS's "settled
// scope semantics" for -project directly, and item 4 (-all vs a scoped run,
// no special-casing): the four rows below are four calls into the exact same
// code path, differing only in which filters statsUsageFilters builds.
func TestComponentStatsUsageRowsRespectProjectAndClientScope(t *testing.T) {
	p := statsPeriod(t)
	quarry := statsUsageEvent(t, p.From.Add(time.Hour), "u1", "sess-quarry", statsTokenFigures(0, 50, 0, 0))
	quarry.Locator = "/work/quarry"
	quarry.Identity.Project = "quarry"
	lantern := statsUsageEvent(t, p.From.Add(2*time.Hour), "u2", "sess-lantern", statsTokenFigures(0, 70, 0, 0))
	lantern.Locator = "/work/lantern"
	lantern.Identity.Project = "lantern"

	st := openUsageStore(t)
	harvestUsageEvents(t, st, []source.Event{quarry, lantern})
	attributeUsageProjects(t, st, "c1", map[string]string{"/work/quarry": "quarry", "/work/lantern": "lantern"})

	data := statsData([]source.Event{quarry, lantern}, collectedThroughout(p))
	data.Store = st
	data.Filter = store.Filter{From: p.From, To: p.To}
	data.Report = metrics.Report{
		Projects: []metrics.Project{{Name: "quarry"}, {Name: "lantern"}},
		Clients:  []metrics.Client{{Name: "c1", Projects: []string{"quarry", "lantern"}}},
	}

	for _, c := range []struct {
		name          string
		scope         metricsScope
		sessions, out string
	}{
		{"quarry alone", metricsScope{Project: "quarry"}, "1", "50"},
		{"lantern alone", metricsScope{Project: "lantern"}, "1", "70"},
		{"client c1, summed across its projects", metricsScope{Client: "c1"}, "2", "120"},
		{"-all, the honest store-wide total", metricsScope{}, "2", "120"},
	} {
		out := renderStats(t, c.scope, p, data)
		sess := lineWith(t, out, "claude sessions")
		if !strings.Contains(sess, " "+c.sessions+" ") {
			t.Errorf("%s: claude sessions reads %q, want %s", c.name, strings.TrimSpace(sess), c.sessions)
		}
		tok := lineWith(t, out, "tokens out")
		if !strings.Contains(tok, " "+c.out+" ") {
			t.Errorf("%s: tokens out reads %q, want %s", c.name, strings.TrimSpace(tok), c.out)
		}
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

// statsUsageEvent is a transcript-shaped observation carrying an
// internal/usage payload — the fixture the tests above use for both the real
// store (via harvestUsageEvents) and data.Events, since a caller of
// renderComponentStats always sees the same event in both places (Events is
// exactly what a store query for Filter would return — dashboardData's own
// doc comment). Mirrors internal/store/usage_test.go's own spent() helper,
// which cannot be imported directly: that one lives in package store_test.
func statsUsageEvent(t *testing.T, at time.Time, uuid, session string, tok *usage.Tokens) source.Event {
	t.Helper()
	e := source.NewPoint(at, source.Identity{Document: uuid}, transcripts.ID)
	e.Locator = "/w/repo"
	e.State = transcripts.StateMachine
	payload, err := usage.Record{Session: session, Tokens: tok}.Encode()
	if err != nil {
		t.Fatalf("encode payload for session %q: %v", session, err)
	}
	e.Payload = payload
	return e
}

// statsTokenFigures builds a fully-stated Tokens object — every figure a
// real assistant turn states, never left nil, since a nil figure means "not
// stated" (internal/usage's own three-state design) and every test above
// wants a genuine observation.
func statsTokenFigures(input, output, cacheRead, cacheCreation int64) *usage.Tokens {
	return &usage.Tokens{
		Input:         usage.Stated(input),
		Output:        usage.Stated(output),
		CacheRead:     usage.Stated(cacheRead),
		CacheCreation: usage.Stated(cacheCreation),
	}
}

// openUsageStore is a freshly migrated store, kept open for the test's
// duration via t.Cleanup — the same lifecycle dashboard_freshness_test.go's
// TestRenderSourceFreshnessReadsTheStore gives its own store, needed here
// for the same reason: Store.Usage is SQL, and nothing short of a real store
// can prove what it returns.
func openUsageStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// harvestUsageEvents writes events through one harvest run, the way a real
// claude-transcripts harvest does — BeginRun/Write/Finish, the same sequence
// internal/store/usage_test.go's own harvest() helper uses (unexported
// there, in package store_test, so it cannot be shared directly).
func harvestUsageEvents(t *testing.T, st *store.Store, events []source.Event) {
	t.Helper()
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: transcripts.ID, Input: "/synthetic-transcripts"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

// attributeUsageProjects runs the resolution pass with a fixed
// locator-to-project map, the way a registry would — store.UsageFilter's
// Project field reads the store's own `project` column, which only
// ResolveProjects ever populates.
func attributeUsageProjects(t *testing.T, st *store.Store, client string, projects map[string]string) {
	t.Helper()
	_, err := st.ResolveProjects(context.Background(), func(locator string) (store.Attributed, bool, error) {
		project, ok := projects[locator]
		if !ok {
			return store.Attributed{}, false, nil
		}
		return store.Attributed{Client: client, Project: project}, true, nil
	})
	if err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
}

// Humanization: job yrAho. The threshold below was revised by Ben MID-TASK
// from the brief's original 10,000 down to 1,000 — see statHumanizeThreshold's
// own doc comment for the argument (1,000 is also exactly where the k suffix's
// own scale starts, so there is no gap between the largest unabbreviated
// figure and the smallest abbreviated one). Every fixture below uses 1,000.

// CRITERION eCg, the abbreviation half: at or above the threshold, one
// decimal digit and a k/M/B suffix, ROUNDED rather than truncated — 1259
// rounds to 1.3k, not the 1.2k a truncation would give — and the suffix
// keeps its trailing zero on a value that happens to round evenly (Ben's own
// phrasing: 46984830 is "47.0M", never "47M").
func TestStatHumanizeValueRoundsRatherThanTruncatesAtOrAboveTheThreshold(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{347, "347"},   // stays exact — well below the threshold
		{999, "999"},   // stays exact — one below the threshold
		{1000, "1.0k"}, // the threshold itself
		{1259, "1.3k"}, // ROUNDS, not truncates: 1.259 -> 1.3, never 1.2
		{2118, "2.1k"},
		{12345, "12.3k"},
		{128592, "128.6k"},  // the SPEC's own pinned example
		{46984830, "47.0M"}, // Ben's own example — the trailing .0 is required
		{12917913159, "12.9B"},
	}
	for _, c := range cases {
		if got := statHumanizeValue(c.n, false); got != c.want {
			t.Errorf("statHumanizeValue(%d, false) = %q, want %q", c.n, got, c.want)
		}
	}
}

// CRITERION eCg, the exact half: below the threshold, rendering is
// unchanged — a plain integer, exactly what statCountRow always printed.
func TestStatHumanizeValueStaysExactBelowTheThreshold(t *testing.T) {
	for _, n := range []int64{0, 1, 347, 999} {
		if got := statHumanizeValue(n, false); got != strconv.FormatInt(n, 10) {
			t.Errorf("statHumanizeValue(%d, false) = %q, want the plain integer", n, got)
		}
	}
}

// CRITERION Auy: -exact restores the full figure even where the count would
// otherwise be abbreviated.
func TestStatHumanizeValueExactFlagRestoresTheRawFigureAboveTheThreshold(t *testing.T) {
	for _, n := range []int64{1000, 128592, 46984830, 12917913159} {
		if got := statHumanizeValue(n, true); got != strconv.FormatInt(n, 10) {
			t.Errorf("statHumanizeValue(%d, true) = %q, want the untruncated raw figure %d", n, got, n)
		}
	}
}

// Rounding a value near a k/M/B boundary can carry it INTO the next tier —
// 999,951 divided by 1,000 is 999.951, which rounds to 1000.0 and would print
// the four-digit-plus-suffix collision "1000.0k" the threshold exists to
// avoid — so the tier itself has to be re-picked after rounding, not decided
// from the raw magnitude alone.
func TestStatHumanizeValueBumpsATierWhenRoundingCrossesTheBoundary(t *testing.T) {
	if got := statHumanizeValue(999_951, false); got != "1.0M" {
		t.Errorf("statHumanizeValue(999951, false) = %q, want 1.0M — rounding 999.951k crosses into M", got)
	}
	if got := statHumanizeValue(999_999_951, false); got != "1.0B" {
		t.Errorf("statHumanizeValue(999999951, false) = %q, want 1.0B — rounding crosses into B", got)
	}
}

// statHumanizeAvg is statHumanizeValue's twin for the per-day average column,
// keeping the "/day" suffix in both forms and judged on the average's OWN
// magnitude — see statHumanizeAvg's own doc comment for why that is
// independent of its row's count.
func TestStatHumanizeAvgAppliesTheSameRulesAsValue(t *testing.T) {
	cases := []struct {
		avg  float64
		want string
	}{
		{0.142857, "0.14/day"}, // below the threshold: today's two-decimal form, unchanged
		{999, "999.00/day"},
		{5591.83, "5.6k/day"}, // Ben's own live-capture example, "5.6k/day"
		{18370.285714, "18.4k/day"},
	}
	for _, c := range cases {
		if got := statHumanizeAvg(c.avg, false); got != c.want {
			t.Errorf("statHumanizeAvg(%v, false) = %q, want %q", c.avg, got, c.want)
		}
	}
	// -exact restores the two-decimal form even above the threshold.
	if got := statHumanizeAvg(5591.83, true); got != "5591.83/day" {
		t.Errorf("statHumanizeAvg(5591.83, true) = %q, want the raw two-decimal figure", got)
	}
}

// CRITERION qAX, THE LOAD-BEARING AGREEMENT TEST. A display transform is not
// exempt from this project's own "two figures nobody has subtracted" rule
// (AGENTS.md) — parsing a humanized figure back must land within half of one
// rounding step of the exact value it was derived from, for every tier
// (including the below-threshold case, where the tolerance is zero: nothing
// is rounded at all below the threshold).
func TestStatHumanizeAgreesWithTheExactValueWithinRounding(t *testing.T) {
	for _, n := range []int64{
		0, 1, 347, 999, 1000, 1259, 2118, 12345, 128592, 999_951,
		1_000_000, 1_234_567, 46_984_830, 999_999_951, 12_917_913_159,
	} {
		got := statHumanizeValue(n, false)
		parsed, scale := parseHumanized(t, got)
		exact := float64(n)
		tolerance := scale * 0.05
		if diff := math.Abs(parsed - exact); diff > tolerance {
			t.Errorf("statHumanizeValue(%d) = %q parses back to %v (off by %v), want within %v of %v",
				n, got, parsed, diff, tolerance, exact)
		}
	}
}

// parseHumanized parses a k/M/B-suffixed figure (or a plain integer, scale
// 1) back to its magnitude, so a caller can check how far rounding was
// allowed to move it from the exact value it renders.
func parseHumanized(t *testing.T, s string) (value, scale float64) {
	t.Helper()
	scale = 1
	numeric := s
	switch s[len(s)-1:] {
	case "k":
		scale, numeric = 1e3, s[:len(s)-1]
	case "M":
		scale, numeric = 1e6, s[:len(s)-1]
	case "B":
		scale, numeric = 1e9, s[:len(s)-1]
	}
	v, err := strconv.ParseFloat(numeric, 64)
	if err != nil {
		t.Fatalf("parse %q back to a number: %v", s, err)
	}
	return v * scale, scale
}

// CRITERION eCg, THROUGH THE FULL PANEL. A single commit's own diff-stat
// carries 128,592 insertions — the SPEC's own pinned example — over the
// seven-day statsPeriod, fully observed: the row must read "128.6k" and its
// average, 128592/7 = 18370.29, must read "18.4k/day". The commit COUNT
// itself (1) stays exact regardless, on the same row's own label — this is
// the row-vs-column distinction: each figure is judged on its own magnitude.
func TestComponentStatsHumanizesCountsAndAveragesAtOrAboveTheThresholdByDefault(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(time.Hour)
	events := []source.Event{statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 128592, Deletions: 0})}

	out := renderStats(t, metricsScope{}, p, statsData(events, collectedThroughout(p)))

	lines := lineWith(t, out, "lines +")
	if !strings.Contains(lines, "128.6k") {
		t.Errorf("lines + reads %q, want the abbreviated 128.6k", strings.TrimSpace(lines))
	}
	if !strings.Contains(lines, "18.4k/day") {
		t.Errorf("lines + reads %q, want the abbreviated average 18.4k/day", strings.TrimSpace(lines))
	}
	if strings.Contains(lines, "128592") {
		t.Errorf("lines + reads %q, still carries the raw figure though this is the default humanized run",
			strings.TrimSpace(lines))
	}

	// The commit count on the SAME row's own label stays exact: 1 is nowhere
	// near the threshold, and humanization is judged per figure, not per row.
	commits := lineWith(t, out, "commits")
	if !strings.Contains(commits, " 1 ") {
		t.Errorf("commits reads %q, want the exact count 1 — well below the threshold", strings.TrimSpace(commits))
	}
}

// CRITERION Auy, THROUGH THE FULL PANEL: -exact (data.Exact) restores every
// raw figure in Component Stats, undoing exactly what the test above pins.
func TestComponentStatsExactRestoresRawFiguresEverywhereInThePanel(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(time.Hour)
	events := []source.Event{statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 128592, Deletions: 0})}

	data := statsData(events, collectedThroughout(p))
	data.Exact = true
	out := renderStats(t, metricsScope{}, p, data)

	lines := lineWith(t, out, "lines +")
	if !strings.Contains(lines, "128592") {
		t.Errorf("lines + reads %q, want the untruncated raw count 128592 under -exact", strings.TrimSpace(lines))
	}
	if strings.Contains(lines, "128.6k") {
		t.Errorf("lines + reads %q, still abbreviated though -exact was requested", strings.TrimSpace(lines))
	}
	if !strings.Contains(lines, "18370.29/day") {
		t.Errorf("lines + reads %q, want the raw two-decimal average 18370.29/day under -exact",
			strings.TrimSpace(lines))
	}
}

// Small counts (below the threshold) are unaffected by -exact, since there is
// nothing for the flag to restore — statCountRow's own doc comment for the
// "commits" row already prints them exactly, in both modes.
func TestComponentStatsSmallCountsStayExactInBothModes(t *testing.T) {
	p := statsPeriod(t)
	at := p.From.Add(time.Hour)
	events := []source.Event{statsCommit(at, gitlog.Stat{FilesChanged: 1, Insertions: 3, Deletions: 1})}

	for _, exact := range []bool{false, true} {
		data := statsData(events, collectedThroughout(p))
		data.Exact = exact
		out := renderStats(t, metricsScope{}, p, data)
		if row := lineWith(t, out, "commits"); !strings.Contains(row, " 1 ") {
			t.Errorf("exact=%v: commits reads %q, want the exact count 1", exact, strings.TrimSpace(row))
		}
	}
}

// CRITERION qAX's column-width half: the value and average columns are
// content-sized (statColumnWidths/statLabelColumnWidth), and both rendering
// modes must fit their own live widths without truncating a figure or
// running two columns together with no separating space — the exact bug
// TestComponentStatsColumnsFitLiveWidthNumbersWithoutTruncationOrCollision
// already pins for the exact-only panel, reproduced here for BOTH modes with
// this task's own live capture (12,917,913,159 cache reads).
func TestComponentStatsColumnWidthsFitBothHumanizedAndExactModes(t *testing.T) {
	p := statsPeriod(t)
	r := statsUsageEvent(t, p.From.Add(time.Hour), "u1", "sess-1",
		statsTokenFigures(46_984_830, 46_984_830, 12_917_913_159, 46_984_830))

	st := openUsageStore(t)
	harvestUsageEvents(t, st, []source.Event{r})

	build := func(exact bool) string {
		data := statsData([]source.Event{r}, collectedThroughout(p))
		data.Store = st
		data.Filter = store.Filter{From: p.From, To: p.To}
		data.Exact = exact
		return renderStats(t, metricsScope{}, p, data)
	}

	humanized, exact := build(false), build(true)

	if row := lineWith(t, humanized, "cache read"); !strings.Contains(row, "12.9B") {
		t.Errorf("cache read (humanized) reads %q, want 12.9B", strings.TrimSpace(row))
	}
	if row := lineWith(t, exact, "cache read"); !strings.Contains(row, "12917913159") {
		t.Errorf("cache read (-exact) reads %q, want the untruncated 12917913159", strings.TrimSpace(row))
	}

	for _, out := range []string{humanized, exact} {
		for line := range strings.SplitSeq(out, "\n") {
			if line != strings.TrimRight(line, " ") {
				t.Errorf("line %q ends in trailing whitespace", line)
			}
		}
		// No two figures on the "cache read" row may run together with no
		// separating space — the collision bug the live-width test above
		// already pins for the exact panel; strings.Fields collapses any
		// padding, so a collision changes the field count for the label's
		// own two words plus its value and average.
		row := lineWith(t, out, "cache read")
		fields := strings.Fields(row)
		if len(fields) != 4 {
			t.Errorf("cache read row %q has %d whitespace-separated fields, want 4 (label's 2 words, value, "+
				"average) — fewer means two figures ran together", row, len(fields))
		}
	}
}
