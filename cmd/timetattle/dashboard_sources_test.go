package main

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/storefixture"
)

// See dashboard_heatmap_test.go's doc comment: this is the same one-line
// placeholder proof, for the source attribution stacked bar. The sibling task
// that fills renderSourceAttribution in owns this file and
// dashboard_sources.go only.
//
// Kept as a smoke test alongside the real ones below: an empty *dashboardData
// (zero-value Report, no sittings at all) must still write something rather
// than an empty section — the "no sittings" sentence, in this case.
func TestRenderSourceAttributionWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderSourceAttribution wrote nothing")
	}
}

// sourceHoursRE pulls every "12.3h" figure out of the legend, in order, so a
// test can sum exactly what a reader would read rather than re-deriving the
// figure from the segments the render function built internally — the
// "printed total equals the sum of printed segments" half of task UgTU2's
// reconciliation criterion.
var sourceHoursRE = regexp.MustCompile(`(\d+\.\d)h`)

func parsedLegendHours(t *testing.T, out string) time.Duration {
	t.Helper()
	matches := sourceHoursRE.FindAllStringSubmatch(out, -1)
	if len(matches) == 0 {
		t.Fatalf("no %%.1fh figures found in the legend:\n%s", out)
	}
	var total time.Duration
	for _, m := range matches {
		f, err := strconv.ParseFloat(m[1], 64)
		if err != nil {
			t.Fatalf("parse legend hours %q: %v", m[1], err)
		}
		total += time.Duration(f * float64(time.Hour))
	}
	return total
}

// mkSitting builds a sessions.Session with the given source combination
// (sorted, as sessions.Cluster always leaves it) and a Margin, so every case
// below also exercises the margin decision: margin is inferred time INSIDE a
// sitting, and dashboard_sources.go's own doc comment explains why it stays
// folded into that sitting's own segment rather than being split out on its
// own — matching how metrics.Report.Presence.Total and Project.Engaged.Total
// both already treat it, never stripping margin out of a bucket, only
// reporting it as a top-level percentage alongside one.
func mkSitting(start time.Time, dur, margin time.Duration, sources ...string) sessions.Session {
	return sessions.Session{
		Start:   start,
		End:     start.Add(dur),
		Events:  len(sources),
		Sources: sources,
		Margin:  margin,
	}
}

// CRITERION pl8: a single-source sitting and a multi-source (corroborated)
// sitting must be distinct, first-class, labeled segments — never collapsed
// into an undifferentiated "multiple" bucket.
func TestSourceAttributionDistinguishesSingleFromMultiSource(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, 2*time.Hour, 0, "biome-appusage"),
		mkSitting(t0.Add(3*time.Hour), 1*time.Hour, 0, "biome-appusage", "fs-observations"),
	}}
	report.Presence.Total = 3 * time.Hour

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	text := out.String()

	if !strings.Contains(text, "biome-appusage only") {
		t.Errorf("single-source segment is not labeled %q:\n%s", "biome-appusage only", text)
	}
	if !strings.Contains(text, "biome-appusage+fs-observations") {
		t.Errorf("multi-source segment is not labeled as the combination:\n%s", text)
	}
	if strings.Contains(text, "multiple") {
		t.Errorf("a source combination was collapsed into a generic \"multiple\" label:\n%s", text)
	}
}

// Segments order by hours descending, so the reader sees what dominates
// first.
func TestSourceAttributionOrdersSegmentsByHoursDescending(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, 1*time.Hour, 0, "fs-observations"),
		mkSitting(t0.Add(2*time.Hour), 5*time.Hour, 0, "biome-appusage"),
		mkSitting(t0.Add(8*time.Hour), 2*time.Hour, 0, "gitlog-commits"),
	}}
	report.Presence.Total = 8 * time.Hour

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	text := out.String()

	iBiome := strings.Index(text, "biome-appusage only")
	iGit := strings.Index(text, "gitlog-commits only")
	iFS := strings.Index(text, "fs-observations only")
	if iBiome < 0 || iGit < 0 || iFS < 0 {
		t.Fatalf("expected all three segment labels in the output:\n%s", text)
	}
	if !(iBiome < iGit && iGit < iFS) {
		t.Errorf("segments are not ordered 5h, 2h, 1h descending:\n%s", text)
	}
}

// CRITERION i0A, THE LOAD-BEARING RECONCILIATION CHECK. The hour figures
// printed in the legend must sum EXACTLY to the total the metrics report
// states for this period (Report.Presence.Total) — never a second,
// unreconciled figure. Durations are chosen as exact tenths of an hour so
// %.1f formatting introduces no rounding residue and the check can be exact
// rather than fuzzy.
func TestSourceAttributionReconcilesExactlyWithReportPresenceTotal(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	sessionsIn := []sessions.Session{
		mkSitting(t0, 90*time.Minute, 10*time.Minute, "biome-appusage"),
		mkSitting(t0.Add(2*time.Hour), 36*time.Minute, 6*time.Minute, "biome-appusage", "fs-observations"),
		mkSitting(t0.Add(4*time.Hour), 3*time.Hour, 0, "gitlog-commits"),
	}
	var want time.Duration
	for _, s := range sessionsIn {
		want += s.Duration()
	}
	report := metrics.Report{Sessions: sessionsIn}
	report.Presence.Total = want

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}

	got := parsedLegendHours(t, out.String())
	if got != want {
		t.Errorf("printed legend hours sum to %v, want exactly %v (== Report.Presence.Total)", got, want)
	}
	if want != report.Presence.Total {
		t.Fatalf("test setup bug: want %v != Report.Presence.Total %v", want, report.Presence.Total)
	}
}

// The margin decision, locked as a regression test: a sitting's Margin
// (lead-in, tail-out, bridged silence — time inside the sitting no
// observation covered) is attributed to the sitting's OWN source
// combination, in full, exactly as metrics.Report.Presence.Total and
// Project.Engaged.Total already fold margin into their totals without
// stripping it into a separate bucket. A segment's printed hours must
// therefore equal the sitting's full Start..End duration, not just its
// observed portion.
func TestSourceAttributionAttributesMarginToItsOwnSittingsSegment(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	// A 2h sitting that is 1h45m margin and only 15m actually observed.
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, 2*time.Hour, 105*time.Minute, "biome-appusage"),
	}}
	report.Presence.Total = 2 * time.Hour

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	if !strings.Contains(out.String(), "2.0h") {
		t.Errorf("margin was not folded into the sitting's own segment; want the full 2.0h, got:\n%s", out.String())
	}
}

// No silent cap: with more distinct source combinations than the section
// displays individually, the excess folds into one explicit remainder
// segment that says how many combinations it aggregates, and the total
// still reconciles exactly including that remainder.
func TestSourceAttributionCapsWithAnExplicitRemainderSegment(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	var sessionsIn []sessions.Session
	var want time.Duration
	// 12 distinct single-source combinations, each a different exact tenth
	// of an hour so the legend's printed figures sum without rounding
	// residue, and each longer than the last so descending order also
	// determines which ones get folded (the shortest ones do).
	for i := range 12 {
		dur := time.Duration(i+1) * 6 * time.Minute // 0.1h, 0.2h, ... 1.2h
		s := mkSitting(t0.Add(time.Duration(i)*3*time.Hour), dur, 0, fmt.Sprintf("source-%02d", i))
		sessionsIn = append(sessionsIn, s)
		want += dur
	}
	report := metrics.Report{Sessions: sessionsIn}
	report.Presence.Total = want

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	text := out.String()

	if !strings.Contains(text, "more combination") {
		t.Errorf("12 distinct combinations did not produce an explicit remainder segment naming what it aggregates:\n%s", text)
	}
	// No source name was silently dropped: at least some individual segments
	// survive the cap and are named.
	if strings.Count(text, "source-") == 0 {
		t.Errorf("no individual source segments survived the cap at all:\n%s", text)
	}

	got := parsedLegendHours(t, text)
	if got != want {
		t.Errorf("capped legend hours sum to %v, want exactly %v (remainder included)", got, want)
	}
}

// colorNone gets distinct GLYPHS per segment rather than relying on color to
// tell segments apart — the brief's own requirement ("segment colors via cc
// with a colorNone fallback of per-segment glyphs"). Two segments rendered
// under colorNone must not use the same fill character, or a reader with no
// color capability cannot tell the bar's segments apart at all.
func TestSourceAttributionColorNoneUsesDistinctGlyphsPerSegment(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, 4*time.Hour, 0, "biome-appusage"),
		mkSitting(t0.Add(5*time.Hour), 3*time.Hour, 0, "fs-observations"),
		mkSitting(t0.Add(9*time.Hour), 2*time.Hour, 0, "gitlog-commits"),
	}}
	report.Presence.Total = 9 * time.Hour

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	if strings.ContainsRune(out.String(), 0x1b) {
		t.Errorf("colorNone render emitted an escape code:\n%q", out.String())
	}

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	// The bar is the first line; the legend swatch is the first rune of each
	// subsequent line's content after the leading indent.
	var glyphs []rune
	for _, l := range lines[1:] {
		trimmed := strings.TrimLeft(l, " ")
		if trimmed == "" {
			continue
		}
		glyphs = append(glyphs, []rune(trimmed)[0])
	}
	if len(glyphs) < 3 {
		t.Fatalf("expected at least 3 legend lines, got %d:\n%s", len(glyphs), out.String())
	}
	seen := map[rune]bool{}
	for _, g := range glyphs {
		if seen[g] {
			t.Errorf("glyph %q reused across segments under colorNone, so segments are not distinguishable:\n%s", g, out.String())
		}
		seen[g] = true
	}
}

// Color capability paints each segment a distinct fixed-order color when the
// terminal can take it, so the same segment always gets the same slot's
// color across renders — never repainted by rank order changes elsewhere.
func TestSourceAttributionPaintsDistinctColorsUnderTruecolor(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, 4*time.Hour, 0, "biome-appusage"),
		mkSitting(t0.Add(5*time.Hour), 3*time.Hour, 0, "fs-observations"),
	}}
	report.Presence.Total = 7 * time.Hour

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorTruecolor, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	text := out.String()
	if !strings.ContainsRune(text, 0x1b) {
		t.Errorf("colorTruecolor render emitted no escape codes at all:\n%q", text)
	}
	// Two different foreground truecolor sequences (38;2;R;G;B) means two
	// different colors were painted, not one color reused for both segments.
	fgRE := regexp.MustCompile(`\x1b\[38;2;\d+;\d+;\d+m`)
	codes := fgRE.FindAllString(text, -1)
	unique := map[string]bool{}
	for _, c := range codes {
		unique[c] = true
	}
	if len(unique) < 2 {
		t.Errorf("expected at least 2 distinct truecolor codes for 2 segments, got %d in:\n%q", len(unique), text)
	}
}

// No sittings at all in the period is a "no data" state, not "zero work" —
// design principle 3. The section must say so rather than drawing an empty
// bar with no explanation, and never print figures that would read as a
// measured zero.
func TestSourceAttributionWithNoSittingsSaysSoRatherThanDrawingNothing(t *testing.T) {
	var out bytes.Buffer
	data := &dashboardData{Report: metrics.Report{}}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	if out.Len() == 0 {
		t.Fatalf("no sittings produced no output at all")
	}
	if strings.Contains(out.String(), "0.0h") || strings.Contains(out.String(), "100.0%") {
		t.Errorf("a no-data period was rendered as if it were a measured zero:\n%s", out.String())
	}
}

// The real-data-shaped half, alongside the constructed cases above (this
// project's own rule: constructed cases sit BESIDE a realistic corpus,
// neither substitutes for the other). internal/storefixture's corpus is run
// through the exact pipeline dashboardCmd itself uses — store.Events then
// metrics.Compute — rather than hand-built sessions.Session literals, and the
// same exact reconciliation must hold: the legend's printed hours sum to
// Report.Presence.Total to within one %.1fh display step.
func TestSourceAttributionReconcilesOverTheStorefixtureCorpus(t *testing.T) {
	f := storefixture.New(t)
	ctx := context.Background()
	events, err := f.Store.Events(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("read the fixture corpus back: %v", err)
	}
	report, err := metrics.Compute(events, f.Params)
	if err != nil {
		t.Fatalf("metrics.Compute over the fixture corpus: %v", err)
	}
	if report.Presence.Total == 0 {
		t.Fatalf("test setup bug: the fixture corpus produced no presence at all")
	}

	// THE EXACT HALF: partitionSittingsBySources' own total, computed in
	// full time.Duration precision with no display rounding anywhere, must
	// equal Report.Presence.Total to the nanosecond. This is what
	// renderSourceAttribution itself relies on and is the real reconciliation
	// guarantee; the parsed-text check below is a second, looser proof that
	// nothing was lost turning that exact figure into %.1fh legend rows.
	_, exactTotal := partitionSittingsBySources(report.Sessions)
	if exactTotal != report.Presence.Total {
		t.Fatalf("partitionSittingsBySources total %v != report.Presence.Total %v over the fixture corpus",
			exactTotal, report.Presence.Total)
	}

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(ctx, &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}

	// THE PARSED-TEXT HALF: summing the rendered %.1fh legend figures cannot
	// reproduce the exact total above, because each row's own printed value
	// independently rounds to the nearest tenth of an hour. The largest that
	// rounding can move any one row is half a display step (3 minutes); with
	// N rows on screen the worst-case accumulated drift is N*3 minutes, which
	// is the budget below rather than an arbitrary tolerance — this is not a
	// fudge factor, it is the exact bound display rounding admits. A real
	// reconciliation bug (a dropped or double-counted sitting) would move the
	// exact-total check above by a comparable amount to the bug itself, which
	// this fixture's own sittings put at up to 4.5h for a single dropped
	// segment — far outside any rounding budget regardless of row count.
	// (This budget was derived empirically once: the fixture's 5 legend rows
	// measured a genuine 4m48s of rounding drift against a naive 1-minute
	// guess, which is what motivated deriving the budget from row count
	// instead of guessing a constant.)
	rows := len(sourceHoursRE.FindAllString(out.String(), -1))
	budget := time.Duration(rows) * 3 * time.Minute
	got := parsedLegendHours(t, out.String())
	diff := got - report.Presence.Total
	if diff < 0 {
		diff = -diff
	}
	if diff > budget {
		t.Errorf("legend hours over the fixture corpus sum to %v, report.Presence.Total is %v, diff %v exceeds the %v display-rounding budget for %d rows",
			got, report.Presence.Total, diff, budget, rows)
	}
}
