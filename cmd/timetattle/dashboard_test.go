package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/sessions"
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
func TestDashboardRendersSectionsInFixedOrder(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all")

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
// with no data is not a skeleton that refuses to run.
func TestDashboardWithNoDataStillRendersEverySection(t *testing.T) {
	out := runDashboard(t, "-store", emptyStore(t), "-config", noConfig(t), "-all")
	for _, s := range dashboardSections {
		if !strings.Contains(out, s.Title) {
			t.Errorf("an empty store dropped the %q section entirely:\n%s", s.Title, out)
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

func init() {
	// Guard against NO_COLOR or COLORTERM leaking in from whatever shell runs
	// `go test`, which would make TestDashboardEmitsNoEscapeCodesWhenCaptured
	// pass or fail for a reason that has nothing to do with the code under
	// test. detectColorCapability already gates on TTY-ness first and a
	// bytes.Buffer is never a TTY, so this is redundant belt-and-braces
	// rather than load-bearing — cheap enough to keep.
	os.Unsetenv("NO_COLOR")
}
