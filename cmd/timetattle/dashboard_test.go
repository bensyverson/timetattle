package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// runDashboard is runMetrics's own shape, for the command's own tests.
func runDashboard(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := dashboardTo(&out, args); err != nil {
		t.Fatalf("dashboard %v: %v", args, err)
	}
	return out.String()
}

// emptyStore is a freshly migrated store with nothing written to it — enough
// for the skeleton's own tests, which are about SHAPE (scoping, ordering,
// flags) and not about any section's content.
func emptyStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	st.Close()
	return path
}

// CRITERION kbk companion: the seven placeholders appear, in the fixed order
// dashboard.go declares them in — gUoEy heatmap, freshness, stats, allocation,
// time-of-day, source attribution, filetypes — so a sibling task filling one
// in can rely on its position never moving under it.
//
// -detailed is passed explicitly: SOURCE FRESHNESS is gated behind it (Ben's
// dashboard feedback, 2026-08-07, item 3 — see
// TestDashboardOmitsSourceFreshnessByDefault below for the default-run half
// of that rule), and this test is about ORDER among every section dashboard
// can print, not about which ones a bare run happens to show.
func TestDashboardRendersSectionsInFixedOrder(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all", "-detailed")

	var positions []int
	var titles []string
	for _, s := range dashboardSections {
		i := strings.Index(out, s.Title)
		if i < 0 {
			t.Fatalf("the report never mentions the %q section at all:\n%s", s.Title, out)
		}
		positions = append(positions, i)
		titles = append(titles, s.Title)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] < positions[i-1] {
			t.Errorf("%q appears before %q, which is out of the declared order:\n%s",
				titles[i], titles[i-1], out)
		}
	}
}

// A run with nothing in the store still renders every section — a skeleton
// with no data is not a skeleton that refuses to run. -detailed is passed for
// the same reason as its sibling above: this test is about every section
// surviving an empty store, not about which ones a bare run shows.
func TestDashboardWithNoDataStillRendersEverySection(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all", "-detailed")
	for _, s := range dashboardSections {
		if !strings.Contains(out, s.Title) {
			t.Errorf("an empty store dropped the %q section entirely:\n%s", s.Title, out)
		}
	}
}

// CRITERION f4s. Freshness renders only under -detailed: the default run
// omits SOURCE FRESHNESS entirely, and every other section still appears —
// this gates ONE section, not the whole panel.
func TestDashboardOmitsSourceFreshnessByDefault(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all")

	for _, s := range dashboardSections {
		if s.Detailed {
			if strings.Contains(out, s.Title) {
				t.Errorf("a bare run (no -detailed) printed %q, which is supposed to be gated behind it:\n%s",
					s.Title, out)
			}
			continue
		}
		if !strings.Contains(out, s.Title) {
			t.Errorf("a bare run dropped %q, which is not gated behind -detailed at all:\n%s", s.Title, out)
		}
	}
}

// CRITERION f4s, the other half: -detailed brings SOURCE FRESHNESS back,
// alongside everything else.
func TestDashboardDetailedIncludesSourceFreshness(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all", "-detailed")
	for _, s := range dashboardSections {
		if !strings.Contains(out, s.Title) {
			t.Errorf("-detailed dropped %q:\n%s", s.Title, out)
		}
	}
}

// CRITERION JRQ. Run inside a registered project, dashboard reports only that
// project — the exact scoping metrics_scope_test.go proves for metrics,
// reusing its own fixture, because resolveWorkingScope is SHARED and not
// forked (see scope.go).
func TestDashboardInAProjectDirectoryReportsOnlyThatProject(t *testing.T) {
	storePath, indexPath, sondermarkDir, _, _, _ := seedScopeFixture(t)
	t.Chdir(sondermarkDir)

	out := runDashboard(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath)

	if !strings.Contains(out, `project "Site"`) {
		t.Errorf("running inside Sondermark's own directory did not scope to its project:\n%s", out)
	}
}

// CRITERION JRQ, the client-directory half.
func TestDashboardInAClientDirectoryReportsThatClientsProjects(t *testing.T) {
	storePath, indexPath, _, larkfieldParent, _, _ := seedScopeFixture(t)
	t.Chdir(larkfieldParent)

	out := runDashboard(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath)

	if !strings.Contains(out, `client "Larkfield"`) {
		t.Errorf("running inside the Larkfield parent did not scope to that client:\n%s", out)
	}
}

// CRITERION JRQ, the refusal half: exactly like metrics, an unregistered
// directory with nothing registered beneath it errors rather than silently
// reporting everything.
func TestDashboardInAnUnregisteredDirectoryErrors(t *testing.T) {
	storePath, indexPath, _, _, _, _ := seedScopeFixture(t)
	t.Chdir(t.TempDir())

	var out bytes.Buffer
	err := dashboardTo(&out, []string{"-store", storePath, "-config", noConfig(t), "-index", indexPath})
	if err == nil {
		t.Fatalf("dashboard in an unregistered directory returned no error, and printed:\n%s", out.String())
	}
	if !strings.Contains(err.Error(), "-all") {
		t.Errorf("the error %q does not name -all as a fix", err.Error())
	}
	if out.Len() != 0 {
		t.Errorf("dashboard wrote a report before failing: %q", out.String())
	}
}

// CRITERION JRQ, -all: ignores the working directory entirely, the same as
// metrics' own -all.
func TestDashboardAllIgnoresTheWorkingDirectory(t *testing.T) {
	storePath, indexPath, _, _, _, _ := seedScopeFixture(t)
	t.Chdir(t.TempDir())

	out := runDashboard(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath, "-all")

	if !strings.Contains(out, "every client and project") {
		t.Errorf("-all from an unregistered directory did not report the unscoped label:\n%s", out)
	}
}

// Explicit -client bypasses directory scoping, from a directory with no
// registration at all — the same rule metrics' own
// TestMetricsExplicitClientBypassesDirectoryScopingWhenUnregistered proves.
func TestDashboardExplicitClientBypassesDirectoryScopingWhenUnregistered(t *testing.T) {
	storePath, indexPath, _, _, _, _ := seedScopeFixture(t)
	t.Chdir(t.TempDir())

	out := runDashboard(t, "-store", storePath, "-config", noConfig(t), "-index", indexPath,
		"-client", "Larkfield")

	if !strings.Contains(out, `client "Larkfield"`) {
		t.Errorf("-client from an unregistered directory did not scope to Larkfield:\n%s", out)
	}
}

// CRITERION kbk companion, and -days's own default: a bare run with no -days
// asks for a full year, which is what the activity heatmap's year grid
// (design principle 5) needs to have anything to draw.
func TestDashboardDaysDefaultsToAYear(t *testing.T) {
	period := dashboardPeriodOf(defaultDashboardDays, mustCalendar(t), time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC))
	if period.Days != 365 {
		t.Errorf("the default period is %d days, want 365", period.Days)
	}
}

// -days N is a TRAILING window ending today: N calendar days, the last of
// which is the day `now` falls in.
func TestDashboardPeriodOfIsATrailingWindowEndingToday(t *testing.T) {
	cal := mustCalendar(t)
	now := time.Date(2026, 8, 7, 15, 0, 0, 0, time.UTC)
	period := dashboardPeriodOf(7, cal, now)

	wantTo := cal.DayOf(now)
	_, to := cal.Bounds(wantTo)
	if !period.To.Equal(to) {
		t.Errorf("period.To = %v, want %v (today's own closing boundary)", period.To, to)
	}

	wantFrom := wantTo.AddDays(-6)
	from, _ := cal.Bounds(wantFrom)
	if !period.From.Equal(from) {
		t.Errorf("period.From = %v, want %v (6 days before today)", period.From, from)
	}

	// Exactly 7 working days are covered: from the day AddDays(-6) begins to
	// the day AddDays(0) ends is 7 tiles of the calendar, no more and no
	// fewer.
	got := period.To.Sub(period.From)
	if days := got / (24 * time.Hour); days < 6 || days > 8 {
		t.Errorf("a 7-day period spans %v, which is not close to 7 days", got)
	}
}

// -days must be positive; 0 or negative describes no window at all and would
// otherwise silently produce an inverted or empty filter.
func TestDashboardDaysMustBePositive(t *testing.T) {
	for _, days := range []string{"0", "-3"} {
		var out bytes.Buffer
		err := dashboardTo(&out, []string{"-store", emptyStore(t), "-config", noConfig(t), "-all",
			"-days", days})
		if err == nil {
			t.Errorf("-days %s returned no error", days)
		}
	}
}

// mustCalendar is the default config's calendar, built the same way
// dashboardCmd itself builds one — cfg.Calendar.Sessions() — so a test
// asserting on dashboardPeriodOf's arithmetic uses the same calendar the
// command would.
func mustCalendar(t *testing.T) sessions.Calendar {
	t.Helper()
	cfg := config.Default()
	cal, err := cfg.Calendar.Sessions()
	if err != nil {
		t.Fatalf("build the default calendar: %v", err)
	}
	return cal
}

// The color capability actually used is threaded from the real environment
// and stdout's own TTY-ness, and a captured buffer is never a terminal — so a
// test run under `go test` (stdout is a pipe) or any CI never emits an escape
// code, keeping every other test's string matching honest without a -no-color
// flag to remember.
func TestDashboardEmitsNoEscapeCodesWhenCaptured(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all")
	if strings.ContainsRune(out, 0x1b) {
		t.Errorf("a captured (non-terminal) run emitted an escape code:\n%q", out)
	}
}

// CRITERION RC9, the case half: every declared section title is Title Case —
// "Activity Heatmap", not "ACTIVITY HEATMAP" — asserted structurally (every
// significant word starts uppercase, and the title as a whole is not its own
// upper-case form) rather than against a hard-coded string list, so this
// keeps proving the RULE even if a section's own wording changes later.
// "of" and "&" are the two minor words this fixed list happens to contain
// and are deliberately excluded from the "starts uppercase" check.
func TestDashboardSectionTitlesAreTitleCase(t *testing.T) {
	minor := map[string]bool{"of": true, "&": true}
	for _, s := range dashboardSections {
		if s.Title == "" {
			t.Fatalf("a section has no title at all")
		}
		if s.Title == strings.ToUpper(s.Title) {
			t.Errorf("%q is rendered ALL CAPS, not Title Case", s.Title)
		}
		for w := range strings.FieldsSeq(s.Title) {
			if minor[w] {
				continue
			}
			r := []rune(w)[0]
			if !unicode.IsUpper(r) {
				t.Errorf("%q: word %q does not start with an uppercase letter", s.Title, w)
			}
		}
	}
}

// CRITERION RC9, the color half: dashboardHeading is the one place a section
// title is wrapped for bold, and it is exercised directly (not through the
// CLI) because detectColorCapability gates on TTY-ness before anything else —
// see TestDashboardEmitsNoEscapeCodesWhenCaptured's own note — so a captured
// run can never observe colorTruecolor or color256, and a unit test on the
// function actually doing the wrapping is the only way to prove the "bold
// under color" half at all.
func TestDashboardHeadingIsBoldUnderColor(t *testing.T) {
	for _, cc := range []colorCapability{color256, colorTruecolor} {
		got := dashboardHeading("Activity Heatmap", cc)
		want := "\x1b[1mActivity Heatmap\x1b[0m"
		if got != want {
			t.Errorf("dashboardHeading(%q, %s) = %q, want %q", "Activity Heatmap", cc, got, want)
		}
	}
}

// CRITERION RC9, the plain half: colorNone gets the bare title back, with no
// escape code at all — the same unconditional-write rule dashcolor.go's own
// Paint/Reset methods follow, reapplied here for bold.
func TestDashboardHeadingIsPlainUnderColorNone(t *testing.T) {
	got := dashboardHeading("Activity Heatmap", colorNone)
	if got != "Activity Heatmap" {
		t.Errorf("dashboardHeading(_, colorNone) = %q, want the bare title", got)
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Errorf("dashboardHeading(_, colorNone) contains an escape code: %q", got)
	}
}

// clampFixtureData builds the minimal dashboardData clampPeriodToFirstObservation
// reads — Events and Report.Clients — so the clamp arithmetic itself is
// tested with no store, no context and no real clock, exactly the seam
// dashboard_stats_test.go's own statsData gives its section.
func clampFixtureData(events []source.Event, clients []metrics.Client) *dashboardData {
	return &dashboardData{
		Events: events,
		Report: metrics.Report{Clients: clients},
	}
}

// clampFixturePeriod is a plain 10-day window ending on a fixed day, so every
// clamp test below reasons about the same, deterministic [From, To).
func clampFixturePeriod(t *testing.T) dashboardPeriod {
	t.Helper()
	return dashboardPeriodOf(10, mustCalendar(t), statsDayForClampTests)
}

// statsDayForClampTests is this file's own fixed instant, deliberately
// distinct from dashboard_stats_test.go's statsDay so this file never depends
// on a symbol only a sibling test file happens to define.
var statsDayForClampTests = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// CRITERION vGU. No in-scope event at all — an empty store, or a scope whose
// data all falls outside the window (defensively; the real query already
// bounds Events to the window) — leaves the period exactly as -days computed.
// There is no observation to clamp to, and the honest answer is the window as
// requested, not a guess.
func TestClampLeavesPeriodUntouchedWithNoInScopeEvents(t *testing.T) {
	period := clampFixturePeriod(t)
	data := clampFixtureData(nil, nil)

	got := clampPeriodToFirstObservation(period, metricsScope{}, data)
	if !got.From.Equal(period.From) || got.Clamped {
		t.Errorf("clampPeriodToFirstObservation with no events = %+v, want the period unchanged", got)
	}
}

// CRITERION vGU, the narrowing half: a scope whose first observation is two
// days after the window's own start reports from that later instant.
func TestClampNarrowsToTheFirstInScopeObservation(t *testing.T) {
	period := clampFixturePeriod(t)
	firstObserved := period.From.Add(48 * time.Hour)

	events := []source.Event{
		source.NewPoint(firstObserved, source.Identity{Project: "Site"}, "claude-transcripts"),
		source.NewPoint(firstObserved.Add(time.Hour), source.Identity{Project: "Site"}, "claude-transcripts"),
	}
	clients := []metrics.Client{{Name: "Sondermark", Projects: []string{"Site"}}}

	got := clampPeriodToFirstObservation(period, metricsScope{}, clampFixtureData(events, clients))
	if !got.From.Equal(firstObserved) {
		t.Errorf("clamped From = %v, want the first observation %v", got.From, firstObserved)
	}
	if !got.Clamped {
		t.Error("clamping to a later first observation left Clamped false")
	}
	if !got.To.Equal(period.To) {
		t.Errorf("clamping moved To (%v), which -days never asked it to", got.To)
	}
	if got.Days != period.Days {
		t.Errorf("clamping changed Days (%d), which is what -days asked for and must survive narrowing", got.Days)
	}
}

// CRITERION vGU, the never-widen half: an event earlier than the window's own
// start (impossible from the real store query, which already bounds Events
// to the window, but asserted defensively since this function's own contract
// promises it) must never push From earlier than -days computed.
func TestClampNeverWidensPastWhatDaysAsked(t *testing.T) {
	period := clampFixturePeriod(t)
	tooEarly := period.From.Add(-48 * time.Hour)

	events := []source.Event{source.NewPoint(tooEarly, source.Identity{}, "claude-transcripts")}

	got := clampPeriodToFirstObservation(period, metricsScope{}, clampFixtureData(events, nil))
	if !got.From.Equal(period.From) {
		t.Errorf("clamped From = %v, want the original %v — narrowing must never widen", got.From, period.From)
	}
}

// A scoped run only clamps to what its OWN scope observed, exactly as every
// other section filters by scope.showsProject rather than a second rule —
// see dashboard_stats.go's tallyStats for the precedent. An event that
// belongs to a project outside scope must not pull the clamp earlier than
// the in-scope project's own first observation.
func TestClampOnlyConsidersEventsInScope(t *testing.T) {
	period := clampFixturePeriod(t)
	inScopeFirst := period.From.Add(72 * time.Hour)
	outOfScopeFirst := period.From.Add(24 * time.Hour) // earlier, but a different project

	events := []source.Event{
		source.NewPoint(outOfScopeFirst, source.Identity{Project: "Backend"}, "claude-transcripts"),
		source.NewPoint(inScopeFirst, source.Identity{Project: "Frontend"}, "claude-transcripts"),
	}
	clients := []metrics.Client{{Name: "Larkfield", Projects: []string{"Frontend", "Backend"}}}
	scope := metricsScope{Project: "Frontend"}

	got := clampPeriodToFirstObservation(period, scope, clampFixtureData(events, clients))
	if !got.From.Equal(inScopeFirst) {
		t.Errorf("clamped From = %v, want the in-scope project's own first observation %v",
			got.From, inScopeFirst)
	}
}

// CRITERION vGU, the -all half: an unscoped run clamps to the earliest event
// among everything already fetched — the store-wide earliest WITHIN the
// window already asked for — with no special-case branch, because
// scope.showsProject admits every event when Client and Project are both
// blank.
func TestClampUnderAllUsesTheStoreWideEarliest(t *testing.T) {
	period := clampFixturePeriod(t)
	earliest := period.From.Add(24 * time.Hour)
	later := period.From.Add(72 * time.Hour)

	events := []source.Event{
		source.NewPoint(later, source.Identity{Project: "Backend"}, "claude-transcripts"),
		source.NewPoint(earliest, source.Identity{Project: "Frontend"}, "claude-transcripts"),
	}
	clients := []metrics.Client{{Name: "Larkfield", Projects: []string{"Frontend", "Backend"}}}

	got := clampPeriodToFirstObservation(period, metricsScope{}, clampFixtureData(events, clients))
	if !got.From.Equal(earliest) {
		t.Errorf("clamped From = %v, want the store-wide earliest %v", got.From, earliest)
	}
}

// CRITERION vGU, stated in the header: the clamped range is not silently
// applied — a reader is told what window was actually used. This runs the
// full CLI path (real store, real "now") rather than calling
// clampPeriodToFirstObservation directly, to prove dashboardCmd actually
// wires the clamp in before rendering, not merely that the function is
// correct in isolation.
func TestDashboardHeaderStatesTheClampedRangeWhenScopeStartsLate(t *testing.T) {
	now := time.Now()
	firstObserved := now.Add(-3 * 24 * time.Hour)
	storePath := seedSingleEventStore(t, firstObserved)

	out := runDashboard(t, "-store", storePath, "-config", noConfig(t), "-all", "-days", "10")

	cal := mustCalendar(t)
	wantFrom, _ := cal.Bounds(cal.DayOf(firstObserved))
	wantDate := wantFrom.Local().Format("2006-01-02")

	header := strings.SplitN(out, "\n", 3)[1]
	if !strings.Contains(header, wantDate) {
		t.Errorf("header %q does not state the clamped start %s", header, wantDate)
	}
	if !strings.Contains(header, "clamped") {
		t.Errorf("header %q does not say the window was clamped at all", header)
	}
}

// CRITERION vGU's own justification: clamping the period is what makes the
// COMPONENT STATS "observed days" divisor honest, automatically, with no
// change to dashboard_stats.go — the divisor is derived from the period a
// section receives, and dashboard.go now narrows that period before any
// section ever sees it.
func TestDashboardClampNarrowsTheObservedDaysDivisor(t *testing.T) {
	now := time.Now()
	firstObserved := now.Add(-3 * 24 * time.Hour)
	storePath := seedSingleEventStore(t, firstObserved)

	out := runDashboard(t, "-store", storePath, "-config", noConfig(t), "-all", "-days", "10")

	m := regexp.MustCompile(`observed days\s+(\d+) of (\d+)`).FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("no \"observed days\" row found:\n%s", out)
	}
	total, err := strconv.Atoi(m[2])
	if err != nil {
		t.Fatalf("parse the observed-days total %q: %v", m[2], err)
	}
	if total >= 10 {
		t.Errorf("observed-days divisor is %d, not narrowed below the requested 10-day window by the clamp:\n%s",
			total, out)
	}
}

// seedSingleEventStore opens a fresh store and writes one event anchored at
// anchor, with no project or client — enough for an -all run, which needs no
// registry at all. Mirrors metrics_scope_test.go's own seedScopeFixture, cut
// down to what the clamp tests need: a single, precisely dated observation.
func seedSingleEventStore(t *testing.T, anchor time.Time) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	e := source.NewPoint(anchor, source.Identity{Document: "notes.md"}, "claude-transcripts")
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "claude-transcripts", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	if _, err := run.Write(ctx, []source.Event{e}); err != nil {
		t.Fatalf("write the seed event: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	return path
}

func init() {
	// Guard against NO_COLOR or COLORTERM leaking in from whatever shell runs
	// `go test`, which would make TestDashboardEmitsNoEscapeCodesWhenCaptured
	// pass or fail for a reason that has nothing to do with the code under
	// test. detectColorCapability already gates on TTY-ness first and a
	// bytes.Buffer is never a TTY, so this is redundant belt-and-braces
	// rather than load-bearing — cheap enough to keep.
	os.Unsetenv("NO_COLOR")
}
