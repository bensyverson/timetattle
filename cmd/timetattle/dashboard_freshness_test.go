package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// The placeholder test this file used to hold (renderSourceFreshness against
// an empty dashboardData{} with a nil Store) proved only that the stub wrote
// something. It cannot survive the real implementation: renderSourceFreshness
// now calls data.Store.Freshness(ctx), which panics on a nil *store.Store.
// The task brief that assigned this file said to replace both the
// placeholder call and the placeholder test, so this file replaces it with
// coverage of the real contract instead — see TestRenderSourceFreshnessReadsTheStore
// below for the equivalent "writes something, against a real store" proof.

// freshnessTestPeriod is a fixed week, so every case below reasons about the
// same boundary without repeating it.
func freshnessTestPeriod() dashboardPeriod {
	return dashboardPeriod{
		Days: 7,
		From: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		To:   time.Date(2026, 8, 8, 0, 0, 0, 0, time.UTC),
	}
}

// CRITERION NNa: a source harvested inside the period shows both an age
// ("2h ago" style) and the absolute instant, so a reader never subtracts by
// hand.
func TestFreshSourceShowsAgeAndAbsoluteTimestamp(t *testing.T) {
	period := freshnessTestPeriod()
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	f := store.Fresh{Source: "biome-appusage", LastHarvest: time.Date(2026, 8, 7, 10, 0, 0, 0, time.UTC)}

	line := freshnessLine(f, period, colorNone, now)

	if !strings.Contains(line, "biome-appusage") {
		t.Errorf("line %q does not name the source", line)
	}
	if !strings.Contains(line, "2h00m ago") {
		t.Errorf("line %q does not give the age as roughly 2h: %q", line, line)
	}
	wantAbs := f.LastHarvest.Local().Format("2006-01-02 15:04")
	if !strings.Contains(line, wantAbs) {
		t.Errorf("line %q does not carry the absolute last-harvest timestamp %q", line, wantAbs)
	}
}

// CRITERION EQI: a source with no harvest inside the selected period is
// flagged, and under colorNone the flag is a plain, unambiguous word — the
// spec asked for this to be provable without a color terminal.
func TestStaleSourceIsFlaggedInPlainTextUnderColorNone(t *testing.T) {
	period := freshnessTestPeriod()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	f := store.Fresh{Source: "gitlog-commits", LastHarvest: time.Date(2026, 7, 20, 9, 0, 0, 0, time.UTC)}

	line := freshnessLine(f, period, colorNone, now)

	if !strings.Contains(line, freshnessStaleTag) {
		t.Errorf("a source last harvested before the period started was not flagged stale: %q", line)
	}
	if strings.ContainsRune(line, 0x1b) {
		t.Errorf("colorNone still emitted an escape code: %q", line)
	}
}

// A source last harvested INSIDE the period is not flagged, however long ago
// that was in absolute terms — the flag is about the selected period, not
// about staleness in general.
func TestSourceHarvestedInsideThePeriodIsNotFlaggedStale(t *testing.T) {
	period := freshnessTestPeriod()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	f := store.Fresh{Source: "gitlog-commits", LastHarvest: period.From}

	line := freshnessLine(f, period, colorNone, now)

	if strings.Contains(line, freshnessStaleTag) {
		t.Errorf("a source last harvested exactly at the period's start was flagged stale: %q", line)
	}
}

// The load-bearing three-state distinction this section exists for: a
// source that has NEVER produced a successful harvest must not read the same
// as one that simply harvested before the window — "nothing was recorded"
// and "observed long ago" are different statements (AGENTS.md's own
// absence-of-a-row rule, restated here as the reason this section exists).
func TestNeverHarvestedSourceRendersDistinctlyFromStale(t *testing.T) {
	period := freshnessTestPeriod()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	never := store.Fresh{Source: "motion-autosave", Failing: true}
	stale := store.Fresh{Source: "gitlog-commits", LastHarvest: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}

	neverLine := freshnessLine(never, period, colorNone, now)
	staleLine := freshnessLine(stale, period, colorNone, now)

	if !strings.Contains(neverLine, freshnessNeverTag) {
		t.Errorf("a source with a zero LastHarvest was not flagged never-harvested: %q", neverLine)
	}
	if strings.Contains(neverLine, freshnessStaleTag) {
		t.Errorf("a never-harvested source was ALSO flagged stale, collapsing the two states: %q", neverLine)
	}
	if neverLine == staleLine {
		t.Errorf("never-harvested and stale rendered identically: %q", neverLine)
	}
}

// classify is the pure boundary logic under freshnessLine, tested directly
// so the fresh/stale/never edges are pinned without formatting noise.
func TestClassifyBoundaries(t *testing.T) {
	period := freshnessTestPeriod()
	cases := []struct {
		name string
		last time.Time
		want freshnessState
	}{
		{"zero time is never", time.Time{}, freshnessNever},
		{"an instant before the period is stale", period.From.Add(-time.Second), freshnessStale},
		{"exactly at period.From is fresh", period.From, freshnessFresh},
		{"inside the period is fresh", period.From.Add(time.Hour), freshnessFresh},
		{"after period.To is still fresh: nothing suggests collection stopped",
			period.To.Add(24 * time.Hour), freshnessFresh},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := classify(store.Fresh{LastHarvest: c.last}, period)
			if got != c.want {
				t.Errorf("classify(%v) = %v, want %v", c.last, got, c.want)
			}
		})
	}
}

// Color capability paints the tag but never hides it: the plain word stays
// in the string whether or not an escape code wraps it, so a reader piping
// through `less -R`, or a test, never has to strip codes to find it — the
// same unconditional Paint/Reset rule dashboardRenderFunc's own contract
// states.
func TestStaleTagSurvivesUnderColor(t *testing.T) {
	period := freshnessTestPeriod()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	f := store.Fresh{Source: "gitlog-commits", LastHarvest: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)}

	line := freshnessLine(f, period, colorTruecolor, now)

	if !strings.Contains(line, freshnessStaleTag) {
		t.Errorf("colored line lost the plain STALE tag: %q", line)
	}
	if !strings.ContainsRune(line, 0x1b) {
		t.Errorf("colorTruecolor produced no escape code at all: %q", line)
	}
}

// CRITERION NNa, the whole-section shape: every source Freshness names gets
// exactly one line, under one header.
func TestWriteSourceFreshnessOneLinePerSource(t *testing.T) {
	period := freshnessTestPeriod()
	now := time.Date(2026, 8, 10, 0, 0, 0, 0, time.UTC)
	fresh := []store.Fresh{
		{Source: "biome-appusage", LastHarvest: period.From.Add(time.Hour)},
		{Source: "claude-transcripts", LastHarvest: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)},
		{Source: "motion-autosave"},
	}

	var out bytes.Buffer
	if err := writeSourceFreshness(&out, fresh, period, colorNone, now); err != nil {
		t.Fatalf("writeSourceFreshness: %v", err)
	}

	got := out.String()
	for _, f := range fresh {
		if !strings.Contains(got, f.Source) {
			t.Errorf("no line names %q:\n%s", f.Source, got)
		}
	}
	if lines := strings.Count(strings.TrimRight(got, "\n"), "\n") + 1; lines != len(fresh)+1 {
		t.Errorf("wrote %d lines, want a header plus one line per source (%d):\n%s", lines, len(fresh)+1, got)
	}
}

// A store nobody has ever harvested must say so, not print an empty section
// — silence and "nothing has ever landed here" are different facts, and this
// section exists to keep exactly that distinction visible.
func TestWriteSourceFreshnessWithNoSourcesSaysSo(t *testing.T) {
	var out bytes.Buffer
	if err := writeSourceFreshness(&out, nil, freshnessTestPeriod(), colorNone, time.Now()); err != nil {
		t.Fatalf("writeSourceFreshness: %v", err)
	}
	if out.Len() == 0 {
		t.Fatal("an empty Freshness result produced no output at all")
	}
}

// Integration: renderSourceFreshness actually reads data.Store.Freshness —
// not a fixture the section made up — so a source's real harvest history
// shows up. Reuses emptyStore from dashboard_test.go, the skeleton's own
// freshly-migrated fixture.
func TestRenderSourceFreshnessReadsTheStore(t *testing.T) {
	path := emptyStore(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the seeded store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "biome-appusage", Input: "/synthetic"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	// Wide enough to certainly contain "now", whichever instant
	// BeginRun/Finish actually stamped (they use time.Now() internally).
	period := dashboardPeriod{From: time.Now().Add(-time.Hour), To: time.Now().Add(time.Hour)}

	var out bytes.Buffer
	data := &dashboardData{Store: st}
	if err := renderSourceFreshness(ctx, &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderSourceFreshness: %v", err)
	}
	if !strings.Contains(out.String(), "biome-appusage") {
		t.Errorf("renderSourceFreshness did not report the run just recorded:\n%s", out.String())
	}
	if strings.Contains(out.String(), freshnessStaleTag) {
		t.Errorf("a harvest that just finished inside the period was flagged stale:\n%s", out.String())
	}
}

// A store with no runs at all still renders the section, saying so, rather
// than erroring or panicking — the same "no data still renders" rule
// TestDashboardWithNoDataStillRendersEverySection proves at the whole-report
// level in dashboard_test.go.
func TestRenderSourceFreshnessOnAnEmptyStoreSaysNothingHasHarvested(t *testing.T) {
	path := emptyStore(t)
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("reopen the seeded store: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	var out bytes.Buffer
	data := &dashboardData{Store: st}
	if err := renderSourceFreshness(ctx, &out, metricsScope{}, freshnessTestPeriod(), colorNone, data); err != nil {
		t.Fatalf("renderSourceFreshness: %v", err)
	}
	if out.Len() == 0 {
		t.Error("renderSourceFreshness wrote nothing for a store with no harvest history")
	}
}
