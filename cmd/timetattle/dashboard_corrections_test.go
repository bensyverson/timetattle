package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The human correction layer, as the DASHBOARD reads it.
//
// `metrics` began laying recorded attribution assertions over its own figures
// on 2026-08-10; this command went on calling metrics.Compute, which is the
// uncorrected path. Nothing was visibly wrong, because no attribution
// assertion had ever been recorded anywhere — and that is exactly the state
// this file exists to end, since the first correction anybody records would
// have made the two commands disagree about the same span with nothing
// subtracting them.
//
// EVERY NAME BELOW IS INVENTED. "acorn" and "birch" are nobody's projects.

// correctionStore seeds two projects working the same sitting two days ago,
// and returns the store path and the instant that sitting's evidence begins.
//
// Both projects' FIRST observation is the same instant on purpose. The
// dashboard clamps its window to the first observation in scope (see
// clampPeriodToFirstObservation), so a fixture where one project started
// earlier than the other would give `dashboard -project acorn` and `dashboard
// -project birch` two different windows, and a test comparing either against
// `metrics` over the whole store would be comparing two spans rather than two
// readings of one.
//
// The arithmetic, all of it derivable from config.Default()'s 5m lead-in and
// 5m tail-out:
//
//	acorn   10:00–11:50  → 09:55–11:55, 2.0h engaged
//	birch   10:00–10:30  → 09:55–10:35, 0.7h engaged
//	presence            = 09:55–11:55, 2.0h, one sitting
//
// and 11:00–11:30 is half an hour only acorn is active for, which is what
// makes it assertable to birch with a figure moving at both ends.
func correctionStore(t *testing.T, now time.Time) (path string, sittingStart time.Time) {
	t.Helper()
	day := now.AddDate(0, 0, -2)
	sittingStart = time.Date(day.Year(), day.Month(), day.Day(), 10, 0, 0, 0, time.Local)

	path = filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	events := []source.Event{
		allocInterval(sittingStart, 110*time.Minute, "", "acorn"),
		allocInterval(sittingStart, 30*time.Minute, "", "birch"),
	}
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: gitlog.CommitsID, Input: "/synthetic-repo"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// allocInterval writes its locator as "/w/<project>", the same convention
	// allocStore resolves; without this pass every event reads back with an
	// empty project and every figure below is zero.
	if _, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		project, ok := strings.CutPrefix(locator, "/w/")
		if !ok {
			return store.Attributed{}, false, nil
		}
		return store.Attributed{Project: project}, true, nil
	}); err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	return path, sittingStart
}

// assertHalfAnHourWasBirchs records the correction through the command a
// person would use, rather than by writing the row directly: what is being
// pinned is that a correction somebody can actually make reaches this report.
func assertHalfAnHourWasBirchs(t *testing.T, path string, sittingStart time.Time) {
	t.Helper()
	from := sittingStart.Add(time.Hour)
	if err := adjustTo(&bytes.Buffer{}, []string{"-store", path,
		"-from", from.Format(time.RFC3339), "-to", from.Add(30 * time.Minute).Format(time.RFC3339),
		"-assert", "attributed", "-project", "birch", "-origin", "a-person"}); err != nil {
		t.Fatalf("adjust -assert attributed: %v", err)
	}
}

// dashboardHeaderHoursOf reads the scoped hours figure out of the header line
// — "project \"acorn\" - 1.5h attributed, …".
func dashboardHeaderHoursOf(t *testing.T, out string) string {
	t.Helper()
	m := regexp.MustCompile(`- (\d+\.\d)h attributed`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no scoped hours figure in the dashboard header:\n%s", out)
	}
	return m[1] + "h"
}

// CRITERION yzb. The two commands report the SAME figure for the same span on
// a store holding an attribution correction.
//
// This is an agreement test and not a plumbing test on purpose: it asserts
// nothing about which function the dashboard calls, only that a person reading
// `metrics` and a person reading `dashboard` are told the same thing about the
// same half hour. The uncorrected figure is asserted too, and it differs from
// both — without that, a dashboard still calling metrics.Compute could pass by
// agreeing with a report that had moved nothing.
func TestDashboardAndMetricsAgreeOnAFigureAPersonCorrected(t *testing.T) {
	path, sittingStart := correctionStore(t, time.Now())
	cfg := noConfig(t)

	for _, project := range []string{"acorn", "birch"} {
		before := metricsRow(t, runMetrics(t, "-store", path, "-config", cfg, "-project", project), project)
		dash := dashboardHeaderHoursOf(t,
			runDashboard(t, "-store", path, "-config", cfg, "-project", project, "-days", "7"))
		if dash != before {
			t.Fatalf("%s reads %s on the dashboard and %s in metrics BEFORE any correction "+
				"exists; this fixture cannot say anything about corrections until the two "+
				"agree without one", project, dash, before)
		}
	}

	assertHalfAnHourWasBirchs(t, path, sittingStart)

	for _, project := range []string{"acorn", "birch"} {
		corrected := metricsRow(t, runMetrics(t, "-store", path, "-config", cfg, "-project", project), project)
		uncorrected := metricsRow(t,
			runMetrics(t, "-store", path, "-config", cfg, "-project", project, "-uncorrected"), project)
		if corrected == uncorrected {
			t.Fatalf("%s reads %s corrected and uncorrected alike, so this test could pass "+
				"against a dashboard that reads no correction at all", project, corrected)
		}

		dash := dashboardHeaderHoursOf(t,
			runDashboard(t, "-store", path, "-config", cfg, "-project", project, "-days", "7"))
		if dash != corrected {
			t.Errorf("dashboard reports %s for %s and metrics reports %s over the same span, "+
				"with a recorded correction between them and nothing subtracting the two "+
				"figures (uncorrected: %s)", dash, project, corrected, uncorrected)
		}
	}
}

// The same agreement, read off the Component Stats row rather than the header,
// so the panel a reader actually looks at is pinned and not only the one line
// above it.
func TestDashboardComponentStatsCarryTheCorrectedFigure(t *testing.T) {
	path, sittingStart := correctionStore(t, time.Now())
	cfg := noConfig(t)
	assertHalfAnHourWasBirchs(t, path, sittingStart)

	out := runDashboard(t, "-store", path, "-config", cfg, "-project", "acorn", "-days", "7")
	row := lineWith(t, out, "attributed")
	want := metricsRow(t, runMetrics(t, "-store", path, "-config", cfg, "-project", "acorn"), "acorn")
	if !strings.Contains(row, want) {
		t.Errorf("the attributed row reads %q, and metrics reports %s for the same project "+
			"over the same span", strings.TrimSpace(row), want)
	}
}

// A DASHBOARD THAT ABSORBS A CORRECTION SILENTLY IS THE SAME FAILURE ONE
// LEVEL ON. `metrics` prints its CORRECTIONS section on every report,
// including when there is nothing to report, because a reader who has
// recorded a correction and sees nothing cannot tell "applied and agreed"
// from "never read". The dashboard says it in one line rather than a section,
// and it says it in both states.
func TestDashboardSaysWhetherAnyCorrectionWasApplied(t *testing.T) {
	path, sittingStart := correctionStore(t, time.Now())
	cfg := noConfig(t)

	quiet := runDashboard(t, "-store", path, "-config", cfg, "-project", "acorn", "-days", "7")
	if !strings.Contains(quiet, "none recorded") {
		t.Errorf("a dashboard over a store holding no correction does not say so, so a reader "+
			"cannot tell it from one that never looked:\n%s", quiet)
	}

	assertHalfAnHourWasBirchs(t, path, sittingStart)

	loud := runDashboard(t, "-store", path, "-config", cfg, "-project", "acorn", "-days", "7")
	flat := strings.Join(strings.Fields(loud), " ")
	if !strings.Contains(flat, "1 attribution assertion applied") {
		t.Errorf("the dashboard never says a correction was applied to the figures it prints:\n%s", loud)
	}
	if !strings.Contains(flat, "0.5h moved") {
		t.Errorf("the dashboard does not say how much clock the correction moved:\n%s", loud)
	}
}

// attributionsFrom is the assertion half of eventsFrom, and it exists for the
// same reason: the clamp moves the window's opening instant, and every figure
// printed under the header has to describe the window the header states.
//
// A piece wholly before the clamp cannot touch an instant inside it, and
// keeping it would report it as clock that "fell outside every sitting" —
// which would be a true sentence about a window the reader was never shown.
// A piece straddling the clamp is kept WHOLE, exactly as eventsFrom keeps a
// straddling event, because the part of it inside the window is real.
func TestAttributionsFromDropsOnlyThePiecesTheWindowNoLongerReaches(t *testing.T) {
	at := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	said := []metrics.Attribution{
		{Span: adjustments.Span{Start: at.Add(-3 * time.Hour), End: at.Add(-2 * time.Hour)}, Project: "acorn"},
		{Span: adjustments.Span{Start: at.Add(-time.Hour), End: at.Add(time.Hour)}, Project: "birch"},
		{Span: adjustments.Span{Start: at.Add(2 * time.Hour), End: at.Add(3 * time.Hour)}, Project: "acorn"},
	}

	got := attributionsFrom(said, at)
	if len(got) != 2 {
		t.Fatalf("%d pieces survived the clamp, want 2 — the straddling one and the later one:\n%v",
			len(got), got)
	}
	if !got[0].Start.Equal(said[1].Start) {
		t.Errorf("the straddling piece was cut to the window rather than kept whole: it starts "+
			"at %v, and it was recorded from %v", got[0].Start, said[1].Start)
	}
	if got[1].Project != "acorn" {
		t.Errorf("the piece after the clamp is %q, want acorn", got[1].Project)
	}
}

// THE SAME NARROWING, THROUGH THE COMMAND. A correction recorded against a
// stretch the clamp then puts outside the reported window must not be counted
// against that window at all: reporting it as clock that "fell outside every
// sitting" would send a reader to record an `engaged` assertion over a span
// this report never covered, and `metrics` over the window the header states
// would never have mentioned it.
func TestACorrectionBeforeTheClampIsNotReportedAgainstTheWindow(t *testing.T) {
	path, sittingStart := correctionStore(t, time.Now())
	cfg := noConfig(t)

	// Three days before the sitting, and so three days before the window this
	// scope's first observation clamps it to — but well inside the seven days
	// -days asked for.
	old := sittingStart.AddDate(0, 0, -3)
	if err := adjustTo(&bytes.Buffer{}, []string{"-store", path,
		"-from", old.Format(time.RFC3339), "-to", old.Add(time.Hour).Format(time.RFC3339),
		"-assert", "attributed", "-project", "birch", "-origin", "a-person"}); err != nil {
		t.Fatalf("adjust -assert attributed: %v", err)
	}

	out := runDashboard(t, "-store", path, "-config", cfg, "-project", "acorn", "-days", "7")
	if !strings.Contains(out, "none recorded in this window") {
		t.Errorf("a correction three days before the window the header states is reported "+
			"against it:\n%s", firstLines(out, 3))
	}
	if strings.Contains(out, "outside every sitting") {
		t.Errorf("the dashboard tells a reader their correction reached no sitting, when it "+
			"reached no part of this window at all:\n%s", firstLines(out, 3))
	}
}

// firstLines keeps a failure message to the header block, which is where every
// assertion in this file that quotes output is looking.
func firstLines(out string, n int) string {
	lines := strings.SplitN(out, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}

// A CONTRACT'S HOURS-TO-DATE REACH OUTSIDE THE WINDOW, and they are the one
// figure on this page computed from its own store query rather than from
// data.Report — see allocationHistory. So they need the correction layer
// applied over their own wider span, or the same dashboard would print
// corrected hours in its header and uncorrected hours against the money.
func TestAllocationHoursToDateApplyTheCorrectionOverTheWiderSpan(t *testing.T) {
	p := allocPeriod(t, 7)
	inside := allocInterval(p.From.Add(2*time.Hour), 110*time.Minute, "", "atlas")      // 2.0h
	before := allocInterval(p.From.Add(-60*24*time.Hour), 350*time.Minute, "", "atlas") // 6.0h

	started := contractEngagement()
	started.Started = config.Date{Year: 2026, Month: time.January, Day: 15}
	cfg := allocConfig(nil, map[string]config.Engagement{"atlas": started})

	data := allocData(t, cfg, p, []source.Event{inside})
	st := allocStore(t, []source.Event{inside, before})
	data.Store = st

	// An hour of the older sitting was somebody else's work. It is outside the
	// reporting window and inside the contract's life, which is precisely the
	// span hours-to-date is summed over.
	ctx := context.Background()
	from := before.Start.Add(time.Hour)
	if _, err := st.RecordAdjustment(ctx, adjustments.Adjustment{
		Start:      from,
		End:        from.Add(time.Hour),
		Assertion:  adjustments.Attributed,
		Project:    "bluebird",
		Origin:     "a-person",
		RecordedAt: time.Now(),
	}); err != nil {
		t.Fatalf("record the correction: %v", err)
	}

	out := renderAllocation(t, metricsScope{Project: "atlas"}, p, colorNone, data)
	if !strings.Contains(out, "7.0h of 160.0h expected") {
		t.Errorf("hours to date still read 8.0h with an hour of them asserted to another "+
			"project; the burn-down is uncorrected while the rest of the page is not:\n%s", out)
	}
}

// The reconciliation the house rule asks for, stated as a test rather than as
// a claim: dashboard's own two readings of one project — the header and the
// contract burn-down — and metrics' report of the same span all carry the same
// correction, so no two of them differ by an unsubtracted half hour.
func TestNoTwoReadingsOfACorrectedProjectDisagree(t *testing.T) {
	path, sittingStart := correctionStore(t, time.Now())
	cfg := noConfig(t)
	assertHalfAnHourWasBirchs(t, path, sittingStart)

	dash := runDashboard(t, "-store", path, "-config", cfg, "-project", "acorn", "-days", "7")
	header := dashboardHeaderHoursOf(t, dash)
	row := strings.TrimSpace(lineWith(t, dash, "attributed"))
	met := metricsRow(t, runMetrics(t, "-store", path, "-config", cfg, "-project", "acorn"), "acorn")

	if header != met || !strings.Contains(row, met) {
		t.Errorf("three readings of one corrected project: header %s, stats row %q, metrics %s",
			header, row, met)
	}
	if header != fmt.Sprintf("%.1fh", 1.5) {
		t.Errorf("acorn reads %s; 2.0h of engaged clock less the half hour asserted to birch "+
			"is 1.5h", header)
	}
}
