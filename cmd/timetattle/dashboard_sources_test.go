package main

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/jobs"
	"github.com/bensyverson/timetattle/internal/knowledgec"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/storefixture"
	"github.com/bensyverson/timetattle/internal/transcripts"
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

// motionAutosaveID and blenderBackupsID (manifest_ingest_test.go, this same
// package) are the two built-in manifest source ids
// (internal/manifest/builtin/motion.json and blender.json's own "id" fields)
// that have no Go constant anywhere in the repo's non-test code to reference
// instead.
//
// The obvious alternative — calling manifest.Builtin() here and reading its
// []Source.ID back — was tried and reverted: internal/corpusgate's
// TestNoUntaggedTestReachesForTheMachine statically forbids any untagged test
// file from calling Builtin() unqualified, because (per its own Reaches
// table) it "resolves the shipped manifests, whose roots are real vaults on
// this machine and whose Spotlight discovery queries this machine's index."
// Fixing that would mean adding this file to corpusgate's exempt map in
// internal/corpusgate/suite_test.go, which job 57dP7 does not own (Files:
// cmd/timetattle/dashboard_sources.go and its test only) — so these two ids
// are literals (borrowed from the sibling test file that already declared
// them, rather than a second, possibly-diverging pair declared here) and
// this is the one part TestSourceFamilyMapsEveryKnownSourceID cannot
// re-derive from source: if either JSON file's "id" field is ever renamed,
// this list — not the completeness test — is what goes stale. Flagged for
// the integrator as a possible follow-up (a corpusgate exemption, reasoned
// the way internal/manifest/builtin_test.go's own entry already is).

// knownSourceIDs returns every source id this build's Go collectors can
// actually emit, plus the two built-in manifest ids above — read from the
// packages that define them (exported ID constants and methods) rather than
// duplicated here as a literal list, so this file's completeness test fails
// the moment a new collector ships without a family, instead of silently
// drifting the way scripts/confidential's name list did (AGENTS.md's "a tool
// that takes what to look for as an argument is only as good as the
// argument" rule).
func knownSourceIDs() []string {
	return []string{
		gitlog.CommitsID,
		gitlog.ReflogID,
		jobs.EventsID,
		jobs.ClaimsID,
		transcripts.ID,
		biome.NewDefaultAppUsage().ID(),
		knowledgec.NewBacklight("").ID(),
		knowledgec.NewLocked("").ID(),
		knowledgec.NewAppUsage("").ID(),
		knowledgec.NewMediaUsage("").ID(),
		fsobserve.ID,
		motionAutosaveID,
		blenderBackupsID,
	}
}

// CRITERION Zst, the completeness half: every source id the running binary's
// own collectors and built-in manifests can attach to an event must map to a
// family. An id reaching sourceFamily unmapped is exactly the "a new source
// silently falls out of the chart" failure AGENTS.md names as this project's
// own recurring sin — this test is what would have caught it before a real
// dashboard run did.
func TestSourceFamilyMapsEveryKnownSourceID(t *testing.T) {
	for _, id := range knownSourceIDs() {
		fam, ok := sourceFamily(id)
		if !ok {
			t.Errorf("source id %q has no family mapping", id)
			continue
		}
		if fam == "" {
			t.Errorf("source id %q mapped to an empty family", id)
		}
	}
}

// CRITERION Zst, the correctness half: completeness alone would pass a
// mapping that sent every id to the same family. Each known id must land in
// the SPECIFIC family job 57dP7's own description names for it.
func TestSourceFamilyMapsToTheStatedFamily(t *testing.T) {
	cases := map[string]family{
		gitlog.CommitsID:                  familyGit,
		gitlog.ReflogID:                   familyGit,
		jobs.EventsID:                     familyJobs,
		jobs.ClaimsID:                     familyJobs,
		transcripts.ID:                    familyClaude,
		biome.NewDefaultAppUsage().ID():   familyApps,
		knowledgec.NewBacklight("").ID():  familyApps,
		knowledgec.NewLocked("").ID():     familyApps,
		knowledgec.NewAppUsage("").ID():   familyApps,
		knowledgec.NewMediaUsage("").ID(): familyApps,
		fsobserve.ID:                      familyFiles,
		motionAutosaveID:                  familyMotion,
		blenderBackupsID:                  familyBlender,
	}
	for id, want := range cases {
		got, ok := sourceFamily(id)
		if !ok {
			t.Errorf("source id %q: expected family %s, got unmapped", id, want)
			continue
		}
		if got != want {
			t.Errorf("source id %q: family = %s, want %s", id, got, want)
		}
	}
}

// A source id no known collector or built-in manifest emits must not be
// silently absorbed into some default family — sourceFamily's ok return is
// what partitionSittingsByFamilies relies on to fail the render instead.
func TestSourceFamilyRejectsUnknownSourceID(t *testing.T) {
	if _, ok := sourceFamily("some-future-collector-nobody-mapped-yet"); ok {
		t.Errorf("an unrecognised source id was silently mapped to a family")
	}
}

// THE UNMAPPED-SOURCE DECISION, locked as a regression test. sourceFamily's
// own doc comment argues for failing the whole render over inventing an
// "Unmapped: <id>" catch-all family, on the strength of dashboard.go's own
// renderDashboard precedent ("a section's error is fatal to the whole run
// rather than skipped over"). This is what proves that decision actually
// holds: renderSourceAttribution must return a non-nil error, naming the
// offending id, rather than drawing a chart with a silently-invented
// category in it.
func TestSourceAttributionFailsRenderOnAnUnmappedSource(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, time.Hour, 0, "some-future-collector-nobody-mapped-yet"),
	}}
	report.Presence.Total = time.Hour

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data)
	if err == nil {
		t.Fatalf("expected an error for an unmapped source id, got nil (wrote: %q)", out.String())
	}
	if !strings.Contains(err.Error(), "some-future-collector-nobody-mapped-yet") {
		t.Errorf("error does not name the unmapped source id: %v", err)
	}
}

// CRITERION Zst: combinations are FAMILY-level, and a single-family sitting
// and a multi-family (corroborated across families) sitting must be
// distinct, first-class, labeled segments — never collapsed into an
// undifferentiated "multiple" bucket. biome-appusage and fs-observations map
// to different families (Apps and Files), so this also exercises the rollup
// itself: two sources that used to print their own literal ids now print
// two family names.
func TestSourceAttributionDistinguishesSingleFromMultiFamily(t *testing.T) {
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

	// Anchored on "): " and a line end so the single-family "Apps" check
	// cannot accidentally match as a substring of the "Apps, Files" combo
	// line below it.
	if !strings.Contains(text, "): Apps\n") {
		t.Errorf("single-family segment is not labeled %q:\n%s", "Apps", text)
	}
	if !strings.Contains(text, "): Apps, Files\n") {
		t.Errorf("multi-family segment is not labeled as the alphabetical combination %q:\n%s", "Apps, Files", text)
	}
	if strings.Contains(text, "multiple") {
		t.Errorf("a family combination was collapsed into a generic \"multiple\" label:\n%s", text)
	}
	// The granular source ids themselves must no longer appear anywhere in
	// the legend — this section now speaks families, not source ids.
	if strings.Contains(text, "biome-appusage") || strings.Contains(text, "fs-observations") {
		t.Errorf("a granular source id leaked into the family-rolled-up legend:\n%s", text)
	}
}

// Segments order by hours descending, so the reader sees what dominates
// first — unchanged by the rollup to families.
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

	iApps := strings.Index(text, "): Apps\n")
	iGit := strings.Index(text, "): Git\n")
	iFiles := strings.Index(text, "): Files\n")
	if iApps < 0 || iGit < 0 || iFiles < 0 {
		t.Fatalf("expected all three family segment labels in the output:\n%s", text)
	}
	if !(iApps < iGit && iGit < iFiles) {
		t.Errorf("segments are not ordered 5h Apps, 2h Git, 1h Files descending:\n%s", text)
	}
}

// CRITERION 0xP, THE LOAD-BEARING RECONCILIATION CHECK. The hour figures
// printed in the legend must sum EXACTLY to the total the metrics report
// states for this period (Report.Presence.Total) — never a second,
// unreconciled figure. Durations are chosen as exact tenths of an hour so
// %.1f formatting introduces no rounding residue and the check can be exact
// rather than fuzzy. Unchanged by the rollup: this test's assertions are
// identical to the pre-family version, because the reconciliation guarantee
// does not depend on what a segment is keyed by.
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
// observation covered) is attributed to the sitting's OWN family
// combination, in full, exactly as metrics.Report.Presence.Total and
// Project.Engaged.Total already fold margin into their totals without
// stripping it into a separate bucket. A segment's printed hours must
// therefore equal the sitting's full Start..End duration, not just its
// observed portion. Unchanged by the rollup.
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

// No silent cap: with more distinct FAMILY combinations than the section
// displays individually, the excess folds into one explicit remainder
// segment that says how many combinations it aggregates, and the total
// still reconciles exactly including that remainder.
//
// The 12 distinct combinations here are built from REAL, family-mapped
// source ids rather than the pre-rollup version's synthetic "source-NN"
// literals: those would now be unmapped ids and renderSourceAttribution
// would (correctly, per TestSourceAttributionFailsRenderOnAnUnmappedSource)
// fail the render instead of reaching the cap at all. The 7 families give 7
// singleton combinations plus 5 two-family combinations chosen to all be
// distinct sets, for 12 total — comfortably over maxSourceSegments.
func TestSourceAttributionCapsWithAnExplicitRemainderSegment(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	combos := [][]string{
		{"gitlog-commits"},                       // Git
		{"job-events"},                           // Jobs
		{"claude-transcripts"},                   // Claude
		{"biome-appusage"},                       // Apps
		{"fs-observations"},                      // Files
		{"motion-autosave"},                      // Motion
		{"blender-backups"},                      // Blender
		{"gitlog-commits", "job-events"},         // Git+Jobs
		{"gitlog-commits", "claude-transcripts"}, // Git+Claude
		{"job-events", "claude-transcripts"},     // Jobs+Claude
		{"biome-appusage", "fs-observations"},    // Apps+Files
		{"biome-appusage", "motion-autosave"},    // Apps+Motion
	}
	var sessionsIn []sessions.Session
	var want time.Duration
	// Each a different exact tenth of an hour, ascending, so the legend's
	// printed figures sum without rounding residue and descending render
	// order also determines which ones get folded (the shortest ones do).
	for i, sources := range combos {
		dur := time.Duration(i+1) * 6 * time.Minute // 0.1h, 0.2h, ... 1.2h
		s := mkSitting(t0.Add(time.Duration(i)*3*time.Hour), dur, 0, sources...)
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
		t.Errorf("12 distinct family combinations did not produce an explicit remainder segment naming what it aggregates:\n%s", text)
	}
	// No family combination was silently dropped: the largest combination
	// (Apps+Motion, the last and longest in the list) survives the cap and
	// is named individually.
	if !strings.Contains(text, "): Apps, Motion\n") {
		t.Errorf("the largest family combination did not survive the cap:\n%s", text)
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
// color capability cannot tell the bar's segments apart at all. Unchanged by
// the rollup.
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
// Unchanged by the rollup.
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
// measured zero. Unchanged by the rollup.
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

// CRITERION jLu: the legend line format is hours, percent in parens, colon,
// alphabetical family names — "116.1h (25.3%): Apps, Claude, Git, Jobs", per
// Ben's own example in job 57dP7's description.
func TestSourceAttributionLegendLineFormatIsHoursPercentColonFamilies(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 9, 0, 0, 0, time.UTC)
	report := metrics.Report{Sessions: []sessions.Session{
		mkSitting(t0, time.Duration(116.1*float64(time.Hour)), 0,
			"job-events", "gitlog-commits", "claude-transcripts", "biome-appusage"),
	}}
	report.Presence.Total = report.Sessions[0].Duration()

	var out bytes.Buffer
	data := &dashboardData{Report: report}
	if err := renderSourceAttribution(context.Background(), &out, metricsScope{}, dashboardPeriod{}, colorNone, data); err != nil {
		t.Fatalf("renderSourceAttribution: %v", err)
	}
	text := out.String()

	lineRE := regexp.MustCompile(`116\.1h \(\d+\.\d%\): Apps, Claude, Git, Jobs`)
	if !lineRE.MatchString(text) {
		t.Errorf("legend line does not match the required \"<hours>h (<percent>%%): <families>\" format:\n%s", text)
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

	// THE EXACT HALF: partitionSittingsByFamilies' own total, computed in
	// full time.Duration precision with no display rounding anywhere, must
	// equal Report.Presence.Total to the nanosecond. This is what
	// renderSourceAttribution itself relies on and is the real reconciliation
	// guarantee; the parsed-text check below is a second, looser proof that
	// nothing was lost turning that exact figure into %.1fh legend rows.
	segs, exactTotal, err := partitionSittingsByFamilies(report.Sessions)
	if err != nil {
		t.Fatalf("partitionSittingsByFamilies over the fixture corpus: %v", err)
	}

	// The collapse job 57dP7 asks to be measured: how many distinct
	// combinations exist at the granular source level over this fixture,
	// versus at the family level. Reproduce with:
	//   go test ./cmd/timetattle/... -run TestSourceAttributionReconcilesOverTheStorefixtureCorpus -v
	// storefixture's own package doc says its corpus is "small and
	// deliberate" (internal/storefixture/storefixture.go), not a replica of
	// any real store's source diversity, so this number is NOT the 76 job
	// 57dP7's description cites from the live store — that figure can only
	// be checked against the real store, by whoever integrates this section
	// (the task's own "note the live count is for the integrator"). What
	// this DOES demonstrate, on data nobody hand-tuned for the assertion, is
	// that the rollup can only ever shrink the combination count, never grow
	// it: every family combination here is the image of at least one source
	// combination under sourceFamily.
	sourceCombos := map[string]bool{}
	for _, s := range report.Sessions {
		key := strings.Join(s.Sources, "\x00")
		sourceCombos[key] = true
	}
	t.Logf("fixture corpus: %d distinct source-level combinations, %d distinct family-level combinations",
		len(sourceCombos), len(segs))
	if len(segs) > len(sourceCombos) {
		t.Errorf("family rollup produced MORE distinct combinations (%d) than the source-level partition (%d) — "+
			"the rollup must never increase the combination count", len(segs), len(sourceCombos))
	}

	if exactTotal != report.Presence.Total {
		t.Fatalf("partitionSittingsByFamilies total %v != report.Presence.Total %v over the fixture corpus",
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
