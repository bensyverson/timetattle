package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
)

// The sibling task that fills renderFiletypeBreakdown in owns this file and
// dashboard_filetypes.go only.

// mustFsEvent builds a minimal fs-observations event at a fixed instant,
// scoped and located the way fsobserve.Walker.observe actually produces one
// (see internal/fsobserve/fsobserve.go's own observe): a source.Point with
// Locator set to the absolute path and the identity's Client/Project
// stamped, exactly as dashboardCmd's own RunE stamps every event before a
// section ever sees it (dashboard.go's client-stamping loop).
func mustFsEvent(locator, client, project string, at time.Time) source.Event {
	e := source.NewPoint(at, source.Identity{
		Document: locator,
		Project:  project,
		Client:   client,
	}, fsobserve.ID)
	e.Locator = locator
	e.Cause = source.CauseUserAction
	return e
}

var fixtureAt = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

func TestCategorizeLocatorRules(t *testing.T) {
	cases := []struct {
		name string
		path string
		want string
	}{
		{"markdown doc", "/proj/docs/guide.md", ftDocumentation},
		{"restructured text", "/proj/README.rst", ftDocumentation},
		{"plain text", "/proj/notes.txt", ftDocumentation},
		{"go test file", "/proj/internal/foo/foo_test.go", ftTests},
		{"go non-test file with test in name but not suffix", "/proj/internal/testutil.go", ftCode},
		{"python test_ prefix", "/proj/tests/test_widgets.py", ftTests},
		{"python _test suffix", "/proj/tests/widgets_test.py", ftTests},
		{"python ordinary source", "/proj/widgets.py", ftCode},
		{"js jest spec", "/proj/src/widget.spec.ts", ftTests},
		{"js jest test", "/proj/src/widget.test.jsx", ftTests},
		{"markdown file that merely contains .test. is not a JS extension", "/proj/notes.test.md", ftDocumentation},
		{"ruby spec", "/proj/spec/widget_spec.rb", ftTests},
		{"ruby source", "/proj/lib/widget.rb", ftCode},
		{"go source", "/proj/main.go", ftCode},
		{"png image", "/proj/assets/logo.png", ftGraphics},
		{"photoshop doc", "/proj/art/frame.psd", ftGraphics},
		{"svg", "/proj/art/icon.svg", ftGraphics},
		{"json config", "/proj/config.json", ftConfig},
		{"yaml config", "/proj/.github/workflows/ci.yaml", ftConfig},
		{"toml config", "/proj/pyproject.toml", ftConfig},
		{"blender project file is not graphics or code", "/proj/scene.blend", ftOther},
		{"motion project file is not graphics or code", "/proj/comp.motn", ftOther},
		{"no extension", "/proj/Makefile", ftOther},
		{"unknown extension", "/proj/data.xyz123", ftOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := categorizeLocator(tc.path); got != tc.want {
				t.Errorf("categorizeLocator(%q) = %q, want %q", tc.path, got, tc.want)
			}
		})
	}
}

func TestFiletypeCountsOnlyCountsFsObservations(t *testing.T) {
	events := []source.Event{
		mustFsEvent("/proj/main.go", "acme", "widget", fixtureAt),
		// A different source entirely (not fs-observations) must not be
		// counted: this section reads file-touch activity from the one
		// source that actually carries a per-file Locator with an
		// extension, not from every event in the store.
		{Source: "biome-focus", Locator: "/proj/ignored.go", Identity: source.Identity{Client: "acme", Project: "widget"}},
	}
	counts, total := filetypeCounts(events, metricsScope{})
	if total != 1 {
		t.Fatalf("total = %d, want 1 (the non-fs-observations event must be excluded)", total)
	}
	if counts[ftCode] != 1 {
		t.Errorf("counts[ftCode] = %d, want 1", counts[ftCode])
	}
}

func TestFiletypeCountsScopesByClientAndProject(t *testing.T) {
	events := []source.Event{
		mustFsEvent("/a/main.go", "acme", "widget", fixtureAt),
		mustFsEvent("/b/main.go", "acme", "gadget", fixtureAt),
		mustFsEvent("/c/main.go", "beta", "sprocket", fixtureAt),
	}

	_, total := filetypeCounts(events, metricsScope{Client: "acme"})
	if total != 2 {
		t.Errorf("client scope acme: total = %d, want 2", total)
	}

	_, total = filetypeCounts(events, metricsScope{Client: "acme", Project: "widget"})
	if total != 1 {
		t.Errorf("client+project scope: total = %d, want 1", total)
	}

	_, total = filetypeCounts(events, metricsScope{})
	if total != 3 {
		t.Errorf("unscoped: total = %d, want 3", total)
	}
}

// TestFiletypeCountsAreTouchesNotDistinctFiles is the load-bearing
// touches-vs-distinct-files decision, made explicit as a test: the SAME file
// touched twice (its birth stamp and its mtime stamp landing as two separate
// source.Point events, exactly as fsobserve.Walker.observe emits up to one
// event per timestamp per file — see that function's own doc comment) must
// count as two touches, not be deduplicated to one file. See this file's own
// package doc for the reasoning against distinct-file counting.
func TestFiletypeCountsAreTouchesNotDistinctFiles(t *testing.T) {
	events := []source.Event{
		mustFsEvent("/proj/main.go", "acme", "widget", fixtureAt),
		mustFsEvent("/proj/main.go", "acme", "widget", fixtureAt.Add(time.Hour)),
	}
	_, total := filetypeCounts(events, metricsScope{})
	if total != 2 {
		t.Errorf("total = %d, want 2 touches for the same file touched twice", total)
	}
}

// TestFiletypeCountsReconcileWithTotal is the reconciliation criterion from
// the task itself: category counts must sum exactly to the total file-touch
// activity for the same scope and period. This exercises a mixed fixture
// spanning every category, including several that fall to "other", and
// checks the reconciliation arithmetically rather than by eyeballing a
// printed report.
func TestFiletypeCountsReconcileWithTotal(t *testing.T) {
	paths := []string{
		"/proj/README.md", "/proj/main.go", "/proj/main_test.go",
		"/proj/assets/logo.png", "/proj/config.yaml", "/proj/scene.blend",
		"/proj/comp.motn", "/proj/notes.rst", "/proj/lib/widget.rb",
		"/proj/spec/widget_spec.rb", "/proj/art/icon.svg", "/proj/data.bin",
	}
	events := make([]source.Event, 0, len(paths))
	for i, p := range paths {
		events = append(events, mustFsEvent(p, "acme", "widget", fixtureAt.Add(time.Duration(i)*time.Minute)))
	}

	counts, total := filetypeCounts(events, metricsScope{})
	if total != len(paths) {
		t.Fatalf("total = %d, want %d", total, len(paths))
	}
	sum := 0
	for _, cat := range append(append([]string{}, filetypeOrder...), ftOther) {
		sum += counts[cat]
	}
	if sum != total {
		t.Errorf("category counts sum to %d, want exactly total %d — every event must land in exactly one category", sum, total)
	}
}

func TestOrderedFiletypeCategoriesPutsOtherLastEvenWhenLargest(t *testing.T) {
	counts := map[string]int{
		ftOther:         1000, // deliberately the largest count of all
		ftDocumentation: 5,
		ftTests:         3,
		ftCode:          9,
		ftGraphics:      1,
		ftConfig:        2,
	}
	order := orderedFiletypeCategories(counts)
	if order[len(order)-1] != ftOther {
		t.Fatalf("orderedFiletypeCategories = %v, want other last regardless of its count", order)
	}
	// The remaining categories are otherwise strictly descending by count.
	for i := 1; i < len(order)-1; i++ {
		if counts[order[i-1]] < counts[order[i]] {
			t.Errorf("order %v is not descending by count before the trailing other bucket", order)
		}
	}
}

func TestRenderFiletypeBreakdownReportsExplicitNoDataNeverAnAllZeroBar(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, fixtureAt)
	data := &dashboardData{Events: nil} // no observations at all

	if err := renderFiletypeBreakdown(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}
	got := out.String()
	if !strings.Contains(strings.ToLower(got), "no data") {
		t.Errorf("expected an explicit no-data line, got %q", got)
	}
	// None of the category glyphs may appear repeated as a bar segment: a
	// no-data period must never render as an all-zero stacked bar.
	for _, style := range filetypeStyles {
		if strings.Count(got, string(style.Glyph)) > 1 {
			t.Errorf("output %q looks like it drew a bar for a no-data period", got)
		}
	}
}

func TestRenderFiletypeBreakdownDistinguishesUnrecordedFromObservedZero(t *testing.T) {
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, fixtureAt)

	var unrecordedOut bytes.Buffer
	unrecorded := &dashboardData{Events: nil, Log: collectionLog{}}
	if err := renderFiletypeBreakdown(context.Background(), &unrecordedOut, metricsScope{}, period, colorNone, unrecorded); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}

	var observedOut bytes.Buffer
	observed := &dashboardData{Events: nil, Log: collectionLog{
		at:   []time.Time{period.From.Add(time.Hour)},
		lost: []bool{false},
	}}
	if err := renderFiletypeBreakdown(context.Background(), &observedOut, metricsScope{}, period, colorNone, observed); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}

	if unrecordedOut.String() == observedOut.String() {
		t.Errorf("collection unrecorded and collection-ran-but-empty must not render identically: got %q for both",
			unrecordedOut.String())
	}
}

func TestRenderFiletypeBreakdownPrintsEveryCategoryIncludingOther(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, fixtureAt)
	data := &dashboardData{Events: []source.Event{
		mustFsEvent("/proj/main.go", "acme", "widget", fixtureAt),
		mustFsEvent("/proj/scene.blend", "acme", "widget", fixtureAt),
	}}

	if err := renderFiletypeBreakdown(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}
	got := out.String()
	for _, name := range filetypeDisplayNames {
		if !strings.Contains(got, name) {
			t.Errorf("output missing category %q — a category with a zero or nonzero count must never be silently omitted, output was:\n%s", name, got)
		}
	}
}

func TestRenderFiletypeBreakdownColorNoneHasNoEscapeCodesAndUsesDeterministicGlyphs(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, fixtureAt)
	data := &dashboardData{Events: []source.Event{
		mustFsEvent("/proj/main.go", "acme", "widget", fixtureAt),
		mustFsEvent("/proj/README.md", "acme", "widget", fixtureAt),
	}}

	if err := renderFiletypeBreakdown(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}
	got := out.String()
	if strings.Contains(got, "\x1b[") {
		t.Errorf("colorNone output must carry no ANSI escapes, got %q", got)
	}
	if !strings.ContainsRune(got, filetypeStyles[ftCode].Glyph) {
		t.Errorf("expected the deterministic code glyph %q in colorNone output %q", filetypeStyles[ftCode].Glyph, got)
	}
	if !strings.ContainsRune(got, filetypeStyles[ftDocumentation].Glyph) {
		t.Errorf("expected the deterministic documentation glyph %q in colorNone output %q", filetypeStyles[ftDocumentation].Glyph, got)
	}
}

// TestRenderFiletypeBreakdownWritesSomething is the original one-line
// placeholder proof, kept as a smoke test now that the section is real.
func TestRenderFiletypeBreakdownWritesSomething(t *testing.T) {
	var out bytes.Buffer
	cal := mustCalendar(t)
	period := dashboardPeriodOf(defaultDashboardDays, cal, time.Now())
	data := &dashboardData{}

	if err := renderFiletypeBreakdown(context.Background(), &out, metricsScope{}, period, colorNone, data); err != nil {
		t.Fatalf("renderFiletypeBreakdown: %v", err)
	}
	if out.Len() == 0 {
		t.Errorf("renderFiletypeBreakdown wrote nothing")
	}
}
