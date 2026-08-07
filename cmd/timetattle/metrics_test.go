package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// seedTwoProjects returns a store holding two projects' evidence three minutes
// apart, so one sitting holds both and their engaged time overlaps. The two
// directories are really registered, so the attribution pass has something to
// resolve rather than the test hand-writing a project name into a column that
// nothing writes at collect time.
//
// EACH PROJECT'S CLIENT IS NAMED DIFFERENTLY FROM THE PROJECT. This fixture
// used to give both the same string, which was invisible while nothing read a
// client and is the degenerate shape once something does: a client named for
// its project cannot tell a client row from a project row, in the report or in
// a test that greps one. Two names per directory, always.
func seedTwoProjects(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	base := time.Date(2026, 7, 28, 15, 0, 0, 0, time.UTC)
	var events []source.Event
	owners := []struct{ client, project string }{
		{"Sondermark", "Holiday Campaign"},
		{"Larkfield", "Recruiting Site"},
	}
	for i, o := range owners {
		project := o.project
		dir := t.TempDir()
		if _, err := registry.Register(registry.Registration{
			Dir: dir, Client: o.client, Project: project,
		}); err != nil {
			t.Fatalf("Register %s: %v", project, err)
		}
		e := source.NewPoint(base.Add(time.Duration(i)*3*time.Minute),
			source.Identity{Document: "notes.md"}, "claude-transcripts")
		// A DIRECTORY locator, which is what internal/transcripts and
		// internal/gitlog actually emit.
		e.Locator = dir
		events = append(events, e)
	}
	// One observation from a source that carries no path at all, so the
	// unattributed breakdown has both kinds to separate.
	events = append(events, source.NewInterval(base.Add(2*time.Hour), base.Add(2*time.Hour+20*time.Minute),
		source.Identity{App: "com.googlecode.iterm2"}, "biome-appusage"))

	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	// No registered markers: this fixture attributes by climbing to real
	// markers on disk, and declares no former paths.
	if err := resolveProjects(ctx, st, nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	return path
}

func runMetrics(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := metricsTo(&out, args); err != nil {
		t.Fatalf("metrics %v: %v", args, err)
	}
	return out.String()
}

// The command reports both normalisations side by side, so a reader does not
// have to know which one a single column would have been.
func TestMetricsReportsBothNormalisations(t *testing.T) {
	out := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))

	for _, want := range []string{"absolute", "allocated", "ENGAGED", "PRESENCE", "Holiday Campaign", "Recruiting Site"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention %q:\n%s", want, out)
		}
	}
}

// The point of the whole command. An engaged total that is mostly margin is an
// inference wearing the clothes of a measurement, so the report can never print
// one without saying how much of it was observed.
func TestMetricsNeverPrintsAnEngagedTotalWithoutItsMargin(t *testing.T) {
	out := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))

	if !strings.Contains(out, "inferred by the clustering") {
		t.Errorf("the report does not say how much of presence was inferred:\n%s", out)
	}
	if !strings.Contains(out, "observed") {
		t.Errorf("the report does not name the observed share at all:\n%s", out)
	}
	// Every attributable source in this corpus emits instants, so nothing here
	// observes a duration and the report has to say so rather than letting the
	// zero column pass as a rounding artefact.
	if !strings.Contains(out, "NONE of the engaged time above was observed as a duration") {
		t.Errorf("the report does not warn that no engaged time was observed:\n%s", out)
	}
}

// Criterion 86I at the command. The two kinds of unattributed presence are
// printed as separate figures with separate advice, because one is fixed by
// registering a directory and the other can never be.
func TestMetricsSeparatesTheTwoKindsOfUnattributedPresence(t *testing.T) {
	out := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))

	for _, want := range []string{
		"UNATTRIBUTED PRESENCE",
		"a path resolving to no registered project",
		"a source carrying no path at all",
		"silence the gap threshold bridged",
		"registering can never reach these",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not distinguish %q:\n%s", want, out)
		}
	}
}

// metrics cannot tell an unregistered directory from one that no longer exists,
// because it never consults the registry or the filesystem — it only knows that
// event.project is NULL. Calling the whole of that figure ACTIONABLE was
// measurably false on 2026-08-01: of the 1,743 observations behind it, not one
// named a directory `timetattle register` could have been pointed at. 1,311
// carried the locator "/", and the rest named paths that no longer exist. The
// line now says which question it cannot answer, and names the command that
// can, rather than asserting an action that fails every time it is followed.
func TestMetricsDoesNotClaimUnattributedPathsAreAllRegisterable(t *testing.T) {
	out := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))

	if strings.Contains(out, "ACTIONABLE") {
		t.Errorf("metrics still calls every unattributed path actionable, which it cannot "+
			"know: it reads no registry and stats no directory:\n%s", out)
	}
	if !strings.Contains(out, "timetattle orient") {
		t.Errorf("metrics does not name the command that separates the registerable from "+
			"the stranded:\n%s", out)
	}
}

// Criterion kHo at the command: the distribution is printed alongside the
// total, not instead of it and not only on request.
func TestMetricsPrintsTheSittingDistributionAlongsideTheTotal(t *testing.T) {
	out := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))

	if !strings.Contains(out, "sitting lengths") {
		t.Errorf("the report has no length distribution:\n%s", out)
	}
	if !strings.Contains(out, "median") || !strings.Contains(out, "p95") {
		t.Errorf("the report has no quantiles:\n%s", out)
	}
}

// A span nothing was recorded in must read as "nothing was recorded", never as
// a confident set of zeroes. That is the same three-state discipline the rest
// of the codebase runs on, at the one place a user meets it.
func TestMetricsSaysNothingWasRecordedRatherThanReportingZero(t *testing.T) {
	out := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"),
		"-from", "2020-01-01", "-to", "2020-01-02")

	if !strings.Contains(out, "nothing was recorded") {
		t.Errorf("a span with no observations does not say so:\n%s", out)
	}
	if strings.Contains(out, "ENGAGED") {
		t.Errorf("a span with no observations still printed an engaged section:\n%s", out)
	}
}

// The command must not write to the store it reads. Metrics are recomputed on
// demand precisely so there is no third derivation to keep in step, and opening
// the store read-only is what makes that a property rather than an intention.
func TestMetricsDoesNotWriteToTheStore(t *testing.T) {
	path := seedTwoProjects(t)
	before := query[int](t, path, "SELECT COUNT(*) FROM event")
	sessionsBefore := query[int](t, path, "SELECT COUNT(*) FROM derived_session")

	runMetrics(t, "-store", path, "-config", filepath.Join(t.TempDir(), "none.json"))

	if after := query[int](t, path, "SELECT COUNT(*) FROM event"); after != before {
		t.Errorf("event went from %d rows to %d", before, after)
	}
	if after := query[int](t, path, "SELECT COUNT(*) FROM derived_session"); after != sessionsBefore {
		t.Errorf("derived_session went from %d rows to %d; metrics derive nothing into a table",
			sessionsBefore, after)
	}
}

// -from and -to go through the shared parser, so a bare date means the same
// thing here as it does to sessionize and chains. Two independently written
// parsers accepting different layouts is a bug this package has already had.
func TestMetricsBoundariesUseTheSharedParser(t *testing.T) {
	path := seedTwoProjects(t)
	var out bytes.Buffer
	err := metricsTo(&out, []string{"-store", path, "-config", filepath.Join(t.TempDir(), "none.json"),
		"-from", "the twelfth"})
	if err == nil {
		t.Fatal("-from with an unparseable date returned no error")
	}
	if !strings.Contains(err.Error(), "-from") {
		t.Errorf("the error does not name the flag that was wrong: %v", err)
	}
}

// FITTING THE TERMINAL. The per-project table was 83 columns and the "observed
// absence" line was 95, so both wrapped in an eighty-column terminal.
//
// What may be lost and what may not: the SITTINGS COUNT sits at the right-hand
// edge of every project row, and clipping a line at eighty would have removed
// it. Never a number. The project NAME is the elastic column, because a
// shortened name is still a name.
func TestTheMetricsTableFitsAnEightyColumnTerminal(t *testing.T) {
	var out bytes.Buffer
	if err := metricsTo(atWidth{&out, 80}, []string{"-store", seedTwoProjects(t),
		"-config", filepath.Join(t.TempDir(), "none.json")}); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	got := out.String()
	t.Logf("report:\n%s", got)
	fits(t, 80, "metrics", got)

	// Every project row still ends in its sittings count, rather than in the
	// ellipsis that would mean the count had been clipped off.
	var rows int
	for line := range strings.SplitSeq(got, "\n") {
		if !strings.Contains(line, "Holiday") && !strings.Contains(line, "Recruiting") {
			continue
		}
		rows++
		if strings.HasSuffix(strings.TrimRight(line, " "), "…") {
			t.Errorf("a project row was clipped, so it lost its trailing count: %q", line)
		}
	}
	if rows != 2 {
		t.Fatalf("%d project rows survived at eighty columns, want 2:\n%s", rows, got)
	}
}

// The absence figures are four numbers in one sentence, and the last of them
// was fifteen columns past the margin. A clipped line would have deleted a
// count; the sentence wraps instead.
func TestTheAbsenceFiguresWrapRatherThanLoseACount(t *testing.T) {
	var out bytes.Buffer
	if err := metricsTo(atWidth{&out, 80}, []string{"-store", seedTwoProjects(t),
		"-config", filepath.Join(t.TempDir(), "none.json")}); err != nil {
		t.Fatalf("metrics: %v", err)
	}
	got := out.String()
	// Whitespace-insensitive, because the sentence is allowed to break across
	// two lines — what it is not allowed to do is lose its last figure.
	if flat := strings.Join(strings.Fields(got), " "); !strings.Contains(flat, "observations suppressed") {
		t.Errorf("the tail of the absence sentence was truncated away:\n%s", got)
	}
	fits(t, 80, "metrics", got)
}

func TestMetricsIsNeverShortenedIntoAPipe(t *testing.T) {
	got := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))
	if strings.Contains(got, "…") {
		t.Errorf("piped output was truncated:\n%s", got)
	}
}

// TASK KrfMh. A PROJECT'S THIN TIME IS A COLUMN, NOT A FOOTNOTE.
//
// `Easley` read 135.2h absolute with 0.0h corroborated and nothing acted on it,
// because the reader had to know that a zero in the corroborated column meant
// something. The weak column says it beside the total instead.
func TestTheWeakColumnStandsBesideEveryProjectsAbsoluteTotal(t *testing.T) {
	// reviewSeed builds a store whose every observation is a file modification
	// time, which is exactly the shape the 88 surviving project-days have.
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)
	got := runMetrics(t, "-store", path, "-config", cfg)
	t.Logf("report:\n%s", got)

	var row string
	for line := range strings.SplitSeq(got, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "seedwork") {
			row = strings.TrimSpace(line)
		}
	}
	if row == "" {
		t.Fatalf("no row for seedwork at all:\n%s", got)
	}
	fields := strings.Fields(row)
	if len(fields) < 3 {
		t.Fatalf("the project row is %q, which has no weak column", row)
	}
	// project, absolute, weak: every hour of a store made of mtimes is weak.
	if fields[1] != fields[2] {
		t.Errorf("seedwork reads %s absolute and %s weak; every observation behind it is a "+
			"file's modification time", fields[1], fields[2])
	}
	if !strings.Contains(got, "weak") {
		t.Errorf("the table has no weak heading:\n%s", got)
	}
}

// THREE ANSWERS AND NOT TWO. Attested, weak, and accounted for by NOTHING must
// each be printed with a number: folding the third into either neighbour is the
// distinction this project has shipped wrongly three times.
func TestMetricsNamesAllThreeThingsEngagedTimeCanRestOn(t *testing.T) {
	got := runMetrics(t, "-store", seedTwoProjects(t), "-config", filepath.Join(t.TempDir(), "none.json"))
	flat := strings.Join(strings.Fields(got), " ")
	// The three are named as three labelled figures rather than in a paragraph.
	// What this test guards is that none of them is folded into a neighbour —
	// unaccounted clock is not weak evidence, it is no evidence — and that
	// survives the wording, so it is the labels that are asserted on. Why the
	// three are different questions is docs/content/docs/metrics.md.
	for _, phrase := range []string{"attested", "weak (a file's modification time only)",
		"unaccounted (bridged silence)"} {
		if !strings.Contains(flat, phrase) {
			t.Errorf("the report never says %q:\n%s", phrase, got)
		}
	}
}
