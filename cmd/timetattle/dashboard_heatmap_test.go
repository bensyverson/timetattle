package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// The activity heatmap.
//
// EVERY TEST HERE PINS THE ZONE, for metrics_series_test.go's own reason: a
// working day is a LOCAL concept, and a fixture written against the
// developer's zone would put the same events on different days in Auckland.
//
// The load-bearing test in this file is
// TestHeatmapRendersAnUnobservedDayDistinctlyFromAnObservedIdleDay. It is
// design principle 3 in project/2026-08-07-cli-overhaul-plan.md — "no data" is
// not "zero work" — as an assertion rather than as a rule for somebody to
// remember, and it has shipped as a bug twice in three tools.

// heatmapNow is the instant every fixture's trailing window ends in, so that
// no test here depends on the day it is run.
var heatmapNow = time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)

// heatmapFixture assembles the dashboardData one section render needs, the
// same way dashboardCmd's own RunE assembles it: events, then
// metrics.Compute over them with the config's clustering parameters, then a
// collection log. Nothing is stubbed past the store, which the heatmap never
// reads.
type heatmapFixture struct {
	cfg    *config.Config
	cal    sessions.Calendar
	period dashboardPeriod
	events []source.Event
	data   *dashboardData
}

func newHeatmapFixture(t *testing.T, days int) *heatmapFixture {
	t.Helper()
	cfg := config.Default()
	cfg.Calendar.Timezone = "UTC"
	cal, err := cfg.Calendar.Sessions()
	if err != nil {
		t.Fatalf("build the fixture calendar: %v", err)
	}
	return &heatmapFixture{
		cfg:    cfg,
		cal:    cal,
		period: dashboardPeriodOf(days, cal, heatmapNow),
		data:   &dashboardData{Config: cfg},
	}
}

// days is every working day the period covers, in order. period.To is the
// EXCLUSIVE closing boundary — the instant tomorrow begins — so the last
// instant the period actually holds is the one before it.
func (f *heatmapFixture) days() []sessions.Day {
	return f.cal.Days(f.period.From, f.period.To.Add(-time.Nanosecond))
}

// worked records an interval of work on d, beginning after hours of the
// working day have elapsed and running for length.
func (f *heatmapFixture) worked(d sessions.Day, after, length time.Duration, client, project string) {
	start, _ := f.cal.Bounds(d)
	f.events = append(f.events, source.NewInterval(start.Add(after), start.Add(after+length),
		source.Identity{Client: client, Project: project}, "biome-appusage"))
}

// collected records that a harvest ran during d, which is what tells an idle
// day from a day nothing was gathering on at all.
func (f *heatmapFixture) collected(d sessions.Day) {
	start, _ := f.cal.Bounds(d)
	f.data.Log.add(start.Add(12*time.Hour), false)
}

// compute runs the same metrics.Compute the dashboard command runs.
func (f *heatmapFixture) compute(t *testing.T) {
	t.Helper()
	p := sessions.Params{
		Gap:             f.cfg.Sessions.Gap.Duration(),
		LeadIn:          f.cfg.Sessions.LeadIn.Duration(),
		TailOut:         f.cfg.Sessions.TailOut.Duration(),
		PresenceSources: f.cfg.Sessions.PresenceSources,
		IgnoreSources:   f.cfg.Sessions.IgnoreSources,
	}
	report, err := metrics.Compute(f.events, p)
	if err != nil {
		t.Fatalf("compute the fixture report: %v", err)
	}
	f.data.Params, f.data.Events, f.data.Report = p, f.events, report
}

func (f *heatmapFixture) render(t *testing.T, cc colorCapability, scope metricsScope) string {
	t.Helper()
	var out bytes.Buffer
	if err := renderActivityHeatmap(context.Background(), &out, scope, f.period, cc, f.data); err != nil {
		t.Fatalf("renderActivityHeatmap: %v", err)
	}
	return out.String()
}

func (f *heatmapFixture) grid(t *testing.T, scope metricsScope) heatmapGrid {
	t.Helper()
	g, err := heatmapGridOf(f.cal, f.period, scope, f.data)
	if err != nil {
		t.Fatalf("heatmapGridOf: %v", err)
	}
	return g
}

// gridLines is every line of the drawn grid, found by its own weekday gutter
// so the month label above it and the legend below it are excluded.
func gridLines(out string) []string {
	var lines []string
	for line := range strings.SplitSeq(out, "\n") {
		rest, ok := strings.CutPrefix(line, heatmapIndent)
		if !ok || len(rest) < 2 {
			continue
		}
		for d := time.Sunday; d <= time.Saturday; d++ {
			if strings.HasPrefix(rest, d.String()[:2]) {
				lines = append(lines, line)
				break
			}
		}
	}
	return lines
}

// plainGlyph is the single cell character the colorNone grid drew for d.
//
// It asserts there is exactly one, which is only true because every fixture
// here uses a window shorter than a week: every weekday row then holds one
// day of the period and blanks for the rest of the columns, so finding one
// non-blank cell also proves the days OUTSIDE the period were left blank
// rather than being drawn as unobserved.
func (f *heatmapFixture) plainGlyph(t *testing.T, out string, d sessions.Day) rune {
	t.Helper()
	want := d.Weekday().String()[:2]
	for _, line := range gridLines(out) {
		rest := strings.TrimPrefix(line, heatmapIndent)
		if !strings.HasPrefix(rest, want) {
			continue
		}
		var drawn []rune
		for _, r := range rest[heatmapGutter:] {
			if r != ' ' {
				drawn = append(drawn, r)
			}
		}
		if len(drawn) != 1 {
			t.Fatalf("the %v row drew %d cells and the rest blank, want exactly one:\n%s",
				d.Weekday(), len(drawn), out)
		}
		return drawn[0]
	}
	t.Fatalf("no grid row for %v (%v):\n%s", d, d.Weekday(), out)
	return 0
}

// THE LOAD-BEARING TEST. A day nothing was collecting on renders differently
// from a day collection ran on and found no work. Both are 0.0h; only one of
// them is a measurement.
func TestHeatmapRendersAnUnobservedDayDistinctlyFromAnObservedIdleDay(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	days := f.days()
	idle, unobserved := days[2], days[4]
	f.collected(idle)
	f.compute(t)

	out := f.render(t, colorNone, metricsScope{})

	got, want := f.plainGlyph(t, out, unobserved), f.plainGlyph(t, out, idle)
	if got == want {
		t.Fatalf("a day nothing was collecting on (%v) and a day collection ran on and found nothing "+
			"(%v) both drew %q; a zero that was measured and a zero that was never looked for are "+
			"different findings:\n%s", unobserved, idle, string(got), out)
	}
	if got != heatmapPlainUnobserved {
		t.Errorf("the unobserved day drew %q, want %q", string(got), string(heatmapPlainUnobserved))
	}
	if want != heatmapPlainIdle {
		t.Errorf("the observed idle day drew %q, want %q", string(want), string(heatmapPlainIdle))
	}
}

// The same distinction, at the cell rather than at the glyph: the state is a
// value the grid carries, so nothing downstream has to re-derive it from a
// character.
func TestHeatmapCellsCarryTheUnobservedStateFromTheCollectionLog(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	days := f.days()
	f.collected(days[2])
	f.compute(t)

	g := f.grid(t, metricsScope{})
	if got := g.cell(days[2]).State; got != heatmapObserved {
		t.Errorf("the day a harvest ran on is %v, want %v", got, heatmapObserved)
	}
	if got := g.cell(days[4]).State; got != heatmapUnobserved {
		t.Errorf("the day nothing ran on is %v, want %v", got, heatmapUnobserved)
	}
}

// EVIDENCE IS ITSELF AN OBSERVATION. capture_run and harvest_run hold rows
// only from the day the store started keeping them, so most of a real corpus
// has hours and no run row; calling those days unobserved would black out the
// years this tool exists to draw, and would be false — the hours ARE the
// observation.
func TestHeatmapDoesNotCallADayWithRecordedWorkUnobserved(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	days := f.days()
	worked := days[3]
	f.worked(worked, 5*time.Hour, 3*time.Hour, "Alderwick", "Ledger")
	f.compute(t)

	g := f.grid(t, metricsScope{})
	if got := g.cell(worked).State; got != heatmapObserved {
		t.Fatalf("a day carrying %v of recorded work is %v; evidence is an observation whatever the "+
			"run log holds", g.cell(worked).Hours, got)
	}
	if g.cell(worked).Hours < 3*time.Hour {
		t.Errorf("the worked day carries %v, want at least the 3h interval", g.cell(worked).Hours)
	}
}

// OBSERVEDNESS IS UNSCOPED AND THE HOURS ARE SCOPED. Under a project filter, a
// day the OTHER project worked is a day we know this project was idle — a
// finding — and must not collapse into the same cell as a day nobody was
// collecting at all.
func TestHeatmapUnderAProjectScopeSeparatesIdleFromUnobserved(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	days := f.days()
	elsewhere, silent := days[1], days[5]
	f.worked(elsewhere, 5*time.Hour, 2*time.Hour, "Alderwick", "Ledger")
	f.compute(t)

	g := f.grid(t, metricsScope{Project: "Almanac"})
	if got := g.cell(elsewhere).Hours; got != 0 {
		t.Errorf("the scoped day carries %v of another project's work, want 0", got)
	}
	if got := g.cell(elsewhere).State; got != heatmapObserved {
		t.Errorf("a day another project was observed working is %v, want %v", got, heatmapObserved)
	}
	if got := g.cell(silent).State; got != heatmapUnobserved {
		t.Errorf("a day nothing at all was observed is %v, want %v", got, heatmapUnobserved)
	}
}

// The hours come from the SAME per-day series metrics itself cuts (seriesOf,
// series.go), so they reconcile against the report exactly rather than nearly.
// A heatmap that re-derived its own sittings would be a second answer to "how
// many hours was that" for somebody to discover the difference between.
func TestHeatmapHoursReconcileAgainstTheReportTheyWereCutFrom(t *testing.T) {
	f := newHeatmapFixture(t, 14)
	days := f.days()
	f.worked(days[2], 5*time.Hour, 3*time.Hour, "Alderwick", "Ledger")
	f.worked(days[6], 6*time.Hour, 90*time.Minute, "Alderwick", "Almanac")
	f.worked(days[9], 4*time.Hour, 7*time.Hour, "Bellhouse", "Foundry")
	f.compute(t)

	var summed time.Duration
	for _, c := range f.grid(t, metricsScope{}).cells() {
		summed += c.Hours
	}
	if summed != f.data.Report.Presence.Total {
		t.Errorf("the grid sums to %v against the report's %v presence; difference %v — the cells are "+
			"cut from the report's own segments and a difference is a bug, not rounding",
			summed, f.data.Report.Presence.Total, summed-f.data.Report.Presence.Total)
	}
}

// WALL CLOCK IS NEVER DOUBLE-COUNTED. Two projects worked at once is one hour
// of the day, not two, so the unscoped cell is presence and never the sum of
// the projects' absolute engaged time.
func TestHeatmapDoesNotDoubleCountConcurrentProjects(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	day := f.days()[3]
	f.worked(day, 5*time.Hour, 2*time.Hour, "Alderwick", "Ledger")
	f.worked(day, 5*time.Hour, 2*time.Hour, "Alderwick", "Almanac")
	f.compute(t)

	got := f.grid(t, metricsScope{}).cell(day).Hours
	if got < 2*time.Hour {
		t.Fatalf("the shared 2h span drew %v; the cell is not carrying the work at all", got)
	}
	if got > 3*time.Hour {
		t.Errorf("two projects sharing one 2h span drew %v; the cell is counting the same clock twice", got)
	}
}

// The ramp's stops, exactly. 0 is the dark ground an idle day sits on, a full
// working day is green, and beyond it the ramp keeps climbing through yellow
// and red to white so that overwork is VISIBLE rather than rewarded with a
// deeper green (design principle 5).
func TestHeatmapRampClimbsFromDarkThroughGreenToWhite(t *testing.T) {
	const anchor = 8 * time.Hour
	stops := []struct {
		worked time.Duration
		want   RGB
	}{
		{0, heatmapGround},
		{anchor, heatmapGreen},
		{anchor * 5 / 3, heatmapYellow},
		{anchor * 7 / 3, heatmapRed},
		{anchor * 3, heatmapWhite},
	}
	// The stops are five DIFFERENT colours, asserted before they are asserted
	// to be hit: a palette whose entries coincide would satisfy every equality
	// below and draw one flat wash.
	for i, a := range stops {
		for _, b := range stops[i+1:] {
			if a.want == b.want {
				t.Fatalf("two ramp stops are the same colour %v; the ramp does not climb", a.want)
			}
		}
	}
	for _, c := range stops {
		if got := heatmapRamp(c.worked, anchor); got != c.want {
			t.Errorf("heatmapRamp(%v, %v) = %v, want %v", c.worked, anchor, got, c.want)
		}
	}
	// Green is green and white is white — the stops are named for what they
	// look like, and a ramp whose "green" were red would pass every equality
	// above.
	if !(heatmapGreen.G > heatmapGreen.R && heatmapGreen.G > heatmapGreen.B) {
		t.Errorf("heatmapGreen %v is not green-dominant", heatmapGreen)
	}
	if heatmapWhite != (RGB{0xff, 0xff, 0xff}) {
		t.Errorf("heatmapWhite is %v, want #ffffff", heatmapWhite)
	}
}

// Both ends are clamped: a negative duration cannot exist and a day past three
// anchors has nowhere brighter to go than white.
func TestHeatmapRampClampsAtBothEnds(t *testing.T) {
	const anchor = 8 * time.Hour
	if heatmapGround == heatmapWhite {
		t.Fatalf("the ramp's two ends are the same colour %v, so clamping to either is untestable",
			heatmapGround)
	}
	if got := heatmapRamp(-time.Hour, anchor); got != heatmapGround {
		t.Errorf("heatmapRamp below zero = %v, want the ground %v", got, heatmapGround)
	}
	if got := heatmapRamp(72*time.Hour, anchor); got != heatmapWhite {
		t.Errorf("heatmapRamp far past the ramp = %v, want white %v", got, heatmapWhite)
	}
}

// The ramp rises to green at ITS ANCHOR and not at a hard-coded eight hours.
// defaultHeatmapAnchor is today's only source of one; a per-project allocation
// config is what will supply another, and the ramp is already shaped to take
// it.
func TestHeatmapRampTakesTheAnchorAsAParameter(t *testing.T) {
	if got, want := heatmapRamp(4*time.Hour, 4*time.Hour), heatmapRamp(8*time.Hour, 8*time.Hour); got != want {
		t.Errorf("a full day against a 4h anchor is %v and against an 8h anchor is %v; the ramp is "+
			"anchored to a constant rather than to its parameter", got, want)
	}
	if got := heatmapRamp(8*time.Hour, 4*time.Hour); got == heatmapGreen {
		t.Errorf("twice a 4h anchor is still %v; the anchor is not moving the ramp", got)
	}
}

// The plain-text glyphs escalate, and the unobserved glyph is not one of them.
// This is the colorNone half of "renders correctly in every capability": a
// capture, a pipe and a test all land here, and a reader of one has to be able
// to tell the buckets apart without a single escape code.
func TestHeatmapPlainGlyphsEscalateAndExcludeTheUnobservedOne(t *testing.T) {
	const anchor = 8 * time.Hour
	seen := map[rune]time.Duration{heatmapPlainUnobserved: -1}
	var last rune
	for _, worked := range []time.Duration{
		0, anchor / 5, anchor / 2, anchor * 9 / 10, anchor * 3 / 2, anchor * 5 / 2,
	} {
		got := heatmapPlainGlyph(heatmapCell{State: heatmapObserved, Hours: worked}, anchor)
		if at, ok := seen[got]; ok {
			t.Fatalf("%v and %v both draw %q; the plain ramp cannot be read back", at, worked, string(got))
		}
		seen[got] = worked
		last = got
	}
	if got := heatmapPlainGlyph(heatmapCell{State: heatmapUnobserved}, anchor); got != heatmapPlainUnobserved {
		t.Errorf("an unobserved cell draws %q, want %q", string(got), string(heatmapPlainUnobserved))
	}
	if got := heatmapPlainGlyph(heatmapCell{State: heatmapOutside}, anchor); got != ' ' {
		t.Errorf("a day outside the period draws %q, want a blank", string(got))
	}
	if last == heatmapPlainIdle {
		t.Errorf("the busiest bucket draws the idle glyph %q", string(last))
	}
}

// The plain glyphs are anchored too, for the ramp's own reason.
func TestHeatmapPlainGlyphsTakeTheAnchorAsAParameter(t *testing.T) {
	half := heatmapCell{State: heatmapObserved, Hours: 4 * time.Hour}
	got := heatmapPlainGlyph(half, 8*time.Hour)
	if want := heatmapPlainGlyph(heatmapCell{State: heatmapObserved, Hours: 2 * time.Hour},
		4*time.Hour); got != want {
		t.Errorf("half an 8h day draws %q and half a 4h day draws %q", string(got), string(want))
	}
	// Against a 4h anchor the same four hours are a WHOLE day, and must not
	// draw the same glyph — otherwise the equality above is satisfied by a
	// function that ignores its anchor entirely.
	if whole := heatmapPlainGlyph(half, 4*time.Hour); whole == got {
		t.Errorf("4h draws %q against both an 8h and a 4h anchor; the glyph ignores its anchor",
			string(got))
	}
}

// CRITERION OHt. Every capability draws the grid as seven lines, one per
// weekday, one cell per week — the half-block that packed two weekday rows
// into one coloured line (four lines instead of seven) is deleted, not kept
// as an option. This is Ben's own screenshot feedback on job dlIfA: the
// four-line packed grid was squinty, and he asked for the full seven lines
// in every capability, not only the plain-text fallback that already drew
// them.
func TestHeatmapDrawsSevenLinesInEveryCapability(t *testing.T) {
	f := newHeatmapFixture(t, 30)
	f.worked(f.days()[10], 5*time.Hour, 6*time.Hour, "Alderwick", "Ledger")
	f.compute(t)

	for _, cc := range []colorCapability{colorNone, color256, colorTruecolor} {
		out := f.render(t, cc, metricsScope{})
		if lines := gridLines(out); len(lines) != 7 {
			t.Errorf("%v drew %d grid lines, want one per weekday:\n%s", cc, len(lines), out)
		}
		if strings.ContainsRune(out, '▀') || strings.ContainsRune(out, '▄') {
			t.Errorf("%v drew a half-block; the seven-line grid has no use for one:\n%s", cc, out)
		}
	}

	plain := f.render(t, colorNone, metricsScope{})
	if strings.ContainsRune(plain, 0x1b) {
		t.Errorf("colorNone emitted an escape code:\n%q", plain)
	}

	for _, c := range []struct {
		cc     colorCapability
		fg, bg string
	}{
		{colorTruecolor, "\x1b[38;2;", "\x1b[48;2;"},
		{color256, "\x1b[38;5;", "\x1b[48;5;"},
	} {
		out := f.render(t, c.cc, metricsScope{})
		for _, want := range []string{c.fg, c.bg} {
			if !strings.Contains(out, want) {
				t.Errorf("%v output has no %q in it:\n%s", c.cc, want, out)
			}
		}
	}
}

// The two colored capabilities draw the SAME picture and differ only in how
// each color is spelled — 24-bit exactly, or the nearest cell of the 256-color
// cube. A 256-color terminal is not a degraded layout.
func TestHeatmapDrawsTheSameGridInTruecolorAnd256Color(t *testing.T) {
	f := newHeatmapFixture(t, 30)
	f.worked(f.days()[10], 5*time.Hour, 6*time.Hour, "Alderwick", "Ledger")
	f.compute(t)

	strip := func(s string) string {
		var b strings.Builder
		for i := 0; i < len(s); i++ {
			if s[i] != 0x1b {
				b.WriteByte(s[i])
				continue
			}
			for i < len(s) && s[i] != 'm' {
				i++
			}
		}
		return b.String()
	}
	got, want := strip(f.render(t, color256, metricsScope{})), strip(f.render(t, colorTruecolor, metricsScope{}))
	// A grid was actually drawn before the two are compared: two placeholders
	// are also identical, and identical nothings would satisfy the comparison
	// below without either capability having drawn a cell. The fixture leaves
	// every day but one unobserved, so the hatch glyph is what proves it.
	if lines := gridLines(want); len(lines) != 7 || !strings.ContainsRune(want, heatmapHatch) {
		t.Fatalf("truecolor drew %d grid lines and no hatch glyph; there is no grid to compare:\n%s",
			len(lines), want)
	}
	if got != want {
		t.Errorf("256-color and truecolor draw different grids:\n%s\n---\n%s", got, want)
	}
}

// A colored cell for an unobserved day is drawn as a dim hatch rather than as
// a solid fill, which is principle 3's own "dim hatch" in the one place a
// glyph, not just a color, can say it.
func TestHeatmapDrawsUnobservedCellsAsAHatch(t *testing.T) {
	f := newHeatmapFixture(t, 30)
	f.compute(t)

	// The GRID lines only. The legend carries a hatch swatch of its own, and
	// matching anywhere in the output would pass on the legend alone while the
	// grid drew none.
	out := f.render(t, colorTruecolor, metricsScope{})
	var hatched int
	for _, line := range gridLines(out) {
		hatched += strings.Count(line, string(heatmapHatch))
	}
	if hatched == 0 {
		t.Errorf("a period nothing was collected in drew no hatch in the grid at all:\n%s", out)
	}
}

// The month labels name the months the period runs through, so a column is
// locatable in the year without counting weeks.
func TestHeatmapLabelsTheMonthsThePeriodCovers(t *testing.T) {
	f := newHeatmapFixture(t, 90)
	f.compute(t)

	out := f.render(t, colorNone, metricsScope{})
	head, _, ok := strings.Cut(out, "\n")
	if !ok {
		t.Fatalf("the heatmap wrote no month line:\n%s", out)
	}
	for _, month := range []string{"May", "Jun", "Jul", "Aug"} {
		if !strings.Contains(head, month) {
			t.Errorf("the month line %q does not name %s", head, month)
		}
	}
}

// The legend states the ramp's own anchor rather than a hard-coded eight
// hours, so a reader is never told the wrong thing about the picture.
func TestHeatmapLegendNamesTheAnchorInForce(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	f.compute(t)

	out := f.render(t, colorNone, metricsScope{})
	if !strings.Contains(out, "8h") {
		t.Errorf("the legend never names the %v anchor:\n%s", defaultHeatmapAnchor, out)
	}
	if !strings.Contains(out, "unobserved") {
		t.Errorf("the legend never names the unobserved cell:\n%s", out)
	}
}

// TEMPORARY CELL STYLES — job z47sR. Ben's screenshot feedback on the
// filled, background-painted grid was that a 30-day window reads compact and
// hard to tease apart. These tests exercise the four -heatmap-style variants
// (filled, square, spaced, square-spaced) this task adds, and are deleted
// along with the mechanism itself once Ben picks a winner and a follow-up
// hardcodes it — see heatmapStyle's own doc comment in dashboard_heatmap.go.

// heatmapStrip removes SGR escape sequences, the same way
// TestHeatmapDrawsTheSameGridInTruecolorAnd256Color's own local strip does,
// pulled out here so every style test below can reuse it.
func heatmapStrip(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != 0x1b {
			b.WriteByte(s[i])
			continue
		}
		for i < len(s) && s[i] != 'm' {
			i++
		}
	}
	return b.String()
}

// syntheticHeatmapGrid builds weeks columns of seven OBSERVED days each, all
// carrying hours — no calendar, no store, no collection log, just enough of
// a heatmapGrid to exercise writeHeatmap's own layout (spacer columns, month
// labels, glyph vs. fill). Every cell being observed is what makes the
// character-exact assertions below exact: nothing in these grids is ever
// blank because it fell outside the period.
func syntheticHeatmapGrid(weeks int, hours time.Duration) heatmapGrid {
	g := heatmapGrid{Rows: [7]time.Weekday{
		time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday, time.Saturday, time.Sunday,
	}}
	for range weeks {
		var col [7]heatmapCell
		for i := range col {
			col[i] = heatmapCell{State: heatmapObserved, Hours: hours}
		}
		g.Columns = append(g.Columns, col)
	}
	return g
}

// heatmapStyleOf resolves every name the flag accepts to the Pitch/Glyph
// pair job z47sR's brief specifies, and anything else — including the empty
// string a fixture that never sets HeatmapStyle carries — falls back to
// filled, today's shipped behaviour.
func TestHeatmapStyleOfResolvesEveryNamedStyle(t *testing.T) {
	cases := []struct {
		name  string
		pitch int
		glyph bool
	}{
		{heatmapStyleFilled, 1, false},
		{heatmapStyleSquare, 1, true},
		{heatmapStyleSpaced, 2, false},
		{heatmapStyleSquareSpaced, 2, true},
	}
	for _, c := range cases {
		if got := heatmapStyleOf(c.name); got.Pitch != c.pitch || got.Glyph != c.glyph {
			t.Errorf("heatmapStyleOf(%q) = %+v, want Pitch %d Glyph %v", c.name, got, c.pitch, c.glyph)
		}
	}
}

func TestHeatmapStyleOfFallsBackToFilledForEmptyOrUnknownNames(t *testing.T) {
	filled := heatmapStyles[heatmapStyleFilled]
	for _, name := range []string{"", "bogus", "Filled", "SQUARE"} {
		if got := heatmapStyleOf(name); got != filled {
			t.Errorf("heatmapStyleOf(%q) = %+v, want the filled default %+v", name, got, filled)
		}
	}
}

// CRITERION gDM, the row-count half. All four styles still draw exactly
// seven lines, one per weekday, in every color capability — this task's
// brief is explicit that the styles are rendering-only and never change how
// many lines the grid takes, only what a cell (and its neighbours) look
// like.
func TestHeatmapAllFourStylesRenderSevenRows(t *testing.T) {
	f := newHeatmapFixture(t, 30)
	f.worked(f.days()[10], 5*time.Hour, 6*time.Hour, "Alderwick", "Ledger")
	f.compute(t)

	for _, name := range []string{
		heatmapStyleFilled, heatmapStyleSquare, heatmapStyleSpaced, heatmapStyleSquareSpaced,
	} {
		f.data.HeatmapStyle = name
		for _, cc := range []colorCapability{colorNone, color256, colorTruecolor} {
			out := f.render(t, cc, metricsScope{})
			if lines := gridLines(out); len(lines) != 7 {
				t.Errorf("style %q under %v drew %d grid lines, want 7:\n%s", name, cc, len(lines), out)
			}
		}
	}
}

// The spaced styles insert exactly one blank column BETWEEN weeks, and none
// after the last one — "a blank column between weeks", not a trailing
// margin. Plain text makes the interior spacer exact: 2h against the
// default 8h anchor is comfortably in the first plain bucket ('-'), so every
// week draws the same non-blank character and only the spacer stays blank.
func TestHeatmapSpacedStyleInsertsOneBlankColumnBetweenWeeksPlainText(t *testing.T) {
	g := syntheticHeatmapGrid(3, 2*time.Hour)
	var b strings.Builder
	if err := writeHeatmap(&b, colorNone, g, defaultHeatmapAnchor, heatmapStyles[heatmapStyleSpaced]); err != nil {
		t.Fatalf("writeHeatmap: %v", err)
	}
	for _, line := range gridLines(b.String()) {
		rest := strings.TrimPrefix(line, heatmapIndent)[heatmapGutter:]
		if rest != "- - -" {
			t.Fatalf("spaced row is %q, want %q (three weeks, one interior spacer each, none trailing)",
				rest, "- - -")
		}
	}
}

// The same layout, in color: a spacer column paints nothing (only the cells
// themselves call PaintBG), and once the escapes are stripped every column —
// cell or spacer — is a bare space, since the default (non-Glyph) fill IS a
// painted space. writeHeatmapRows alone is exercised here, not the full
// writeHeatmap: the legend (writeHeatmapLegend) unconditionally paints one
// background swatch of its own for its "unobserved" key, which would throw
// off a raw count of every "\x1b[48;2;" in the section's full output.
func TestHeatmapSpacedStyleInsertsOneBlankColumnBetweenWeeksInColor(t *testing.T) {
	g := syntheticHeatmapGrid(3, 2*time.Hour)
	var b strings.Builder
	writeHeatmapRows(&b, colorTruecolor, g, defaultHeatmapAnchor, heatmapStyles[heatmapStyleSpaced])
	out := b.String()
	if n := strings.Count(out, "\x1b[48;2;"); n != 21 {
		t.Errorf("spaced truecolor output painted %d background cells, want 21 (3 weeks x 7 rows); the "+
			"spacer columns must paint nothing", n)
	}
	stripped := heatmapStrip(out)
	for _, line := range gridLines(stripped) {
		rest := strings.TrimPrefix(line, heatmapIndent)[heatmapGutter:]
		if rest != "     " {
			t.Fatalf("stripped spaced row is %q (%d chars), want 5 blank characters (three painted cells "+
				"plus two interior spacers)", rest, len(rest))
		}
	}
}

// The square styles draw a foreground glyph instead of a background fill —
// the whole point of job z47sR's ask, breathing room around each cell rather
// than a flush-painted rectangle. Grid rows only, for the same reason the
// color test above isolates them: the legend paints one background swatch
// of its own regardless of style.
func TestHeatmapSquareStyleDrawsAForegroundGlyphInsteadOfABackgroundFill(t *testing.T) {
	g := syntheticHeatmapGrid(1, 4*time.Hour)
	var b strings.Builder
	writeHeatmapRows(&b, colorTruecolor, g, defaultHeatmapAnchor, heatmapStyles[heatmapStyleSquare])
	out := b.String()
	if strings.Contains(out, "\x1b[48;2;") {
		t.Errorf("square style painted a background fill; every observed day here must draw a foreground "+
			"glyph only:\n%s", out)
	}
	if got := strings.Count(out, string(heatmapSquareGlyph)); got != 7 {
		t.Errorf("square style drew the square glyph %d times, want 7 (one per weekday row)", got)
	}
	if strings.ContainsRune(out, 0xFE0F) {
		t.Errorf("square style output carries a variation selector (U+FE0F), which risks forcing an " +
			"emoji-presentation substitution for the glyph")
	}
}

// CRITERION k4j, the square-style half. An idle day (observed, zero hours)
// must stay visually distinct from BOTH an unobserved day (still the hatch,
// unchanged by style) and a worked day, under a Glyph style. See
// heatmapIdleGlyphInk's own doc comment in dashboard_heatmap.go for why a
// fixed dim grey was chosen over drawing the ramp's own near-black ground
// colour or drawing nothing at all.
func TestHeatmapSquareStyleDistinguishesIdleFromUnobservedAndFromWorked(t *testing.T) {
	idleCell := heatmapCell{State: heatmapObserved, Hours: 0}
	unobservedCell := heatmapCell{State: heatmapUnobserved}
	workedCell := heatmapCell{State: heatmapObserved, Hours: 4 * time.Hour}

	style := heatmapStyles[heatmapStyleSquare]
	idle := heatmapDayCell(colorTruecolor, idleCell, defaultHeatmapAnchor, style)
	unobserved := heatmapDayCell(colorTruecolor, unobservedCell, defaultHeatmapAnchor, style)
	worked := heatmapDayCell(colorTruecolor, workedCell, defaultHeatmapAnchor, style)

	if idle == unobserved {
		t.Fatalf("an idle day and an unobserved day drew identically under the square style: %q", idle)
	}
	if !strings.Contains(unobserved, string(heatmapHatch)) {
		t.Errorf("the unobserved cell dropped its hatch glyph under the square style: %q", unobserved)
	}
	if strings.Contains(idle, string(heatmapHatch)) {
		t.Errorf("the idle cell drew the unobserved hatch glyph: %q", idle)
	}
	if !strings.Contains(idle, string(heatmapSquareGlyph)) {
		t.Errorf("the idle cell drew no square glyph at all under a Glyph style: %q", idle)
	}
	if idle == worked {
		t.Errorf("an idle day and a 4h worked day painted identically under the square style: %q", idle)
	}
}

// heatmapIdleGlyphInk itself must actually be a different colour from both
// the ramp's own ground (or a dark terminal theme would make an idle square
// invisible) and the unobserved ink (or idle and unobserved would read as
// the same finding at a glance even with the hatch shape as a second cue).
func TestHeatmapIdleGlyphInkIsDistinctFromUnobservedInkAndFromTheRampGround(t *testing.T) {
	if heatmapIdleGlyphInk == heatmapUnobservedInk {
		t.Errorf("the idle glyph ink matches the unobserved ink %v", heatmapUnobservedInk)
	}
	if heatmapIdleGlyphInk == heatmapGround {
		t.Errorf("the idle glyph ink matches the ramp's own ground %v; on a dark terminal theme an idle "+
			"square would be invisible", heatmapGround)
	}
}

// Month labels track the style's own pitch: a spaced grid's weeks are twice
// as wide on screen, so the label naming week index 6 must land at printed
// column 12 (6 * Pitch 2), not printed column 6, or the label would drift
// out from under the week it names. Week index 6 — not 2 — is deliberate:
// writeHeatmapMonths refuses to place a label within 4 printed columns of
// the previous one (a 3-character label over a 1-column week needs the
// room), and at Pitch 1 two months only 2 weeks apart do not have it — this
// asserts the PITCH SCALING, not that rule, so the fixture gives both
// styles enough room to place the label at all.
func TestHeatmapMonthLabelsTrackTheStylesColumnPitch(t *testing.T) {
	const septemberWeek = 6
	var g heatmapGrid
	for i := range septemberWeek + 1 {
		var col [7]heatmapCell
		m := time.August
		if i == septemberWeek {
			m = time.September
		}
		col[0].Day = sessions.Day{Year: 2026, Month: m, Date: 1}
		g.Columns = append(g.Columns, col)
	}
	prefix := heatmapIndent + strings.Repeat(" ", heatmapGutter)
	for _, style := range []heatmapStyle{heatmapStyles[heatmapStyleFilled], heatmapStyles[heatmapStyleSpaced]} {
		var b strings.Builder
		writeHeatmapMonths(&b, g, style)
		body := strings.TrimSuffix(strings.TrimPrefix(b.String(), prefix), "\n")
		want := septemberWeek * style.Pitch
		if got := strings.Index(body, "Sep"); got != want {
			t.Errorf("Pitch %d: Sep landed at column %d, want %d (week index %d * pitch)",
				style.Pitch, got, want, septemberWeek)
		}
	}
}

// The legend's ramp swatches double in count under a spaced style, so the
// legend's own width tracks the grid's doubled pitch rather than looking
// squeezed beside a grid that just got twice as wide.
func TestHeatmapLegendSwatchCountDoublesUnderSpacedStyles(t *testing.T) {
	countSwatches := func(style heatmapStyle) int {
		var b strings.Builder
		writeHeatmapLegend(&b, colorTruecolor, defaultHeatmapAnchor, style)
		return strings.Count(b.String(), string(heatmapFull))
	}
	filled := countSwatches(heatmapStyles[heatmapStyleFilled])
	spaced := countSwatches(heatmapStyles[heatmapStyleSpaced])
	if filled == 0 {
		t.Fatalf("the filled legend drew no ramp swatches at all")
	}
	if spaced != filled*2 {
		t.Errorf("spaced legend drew %d swatches against filled's %d, want exactly double (Pitch 2)",
			spaced, filled)
	}
}

// colorNone: spacing still applies (a reader piping the plain output should
// still see the spaced layout), but a Glyph style has nothing of its own to
// draw in plain text and falls back to exactly the same single character per
// cell filled already draws — DETERMINISTIC, per this task's brief, rather
// than an arbitrary third glyph nobody asked for.
func TestHeatmapColorNoneAppliesSpacingButFallsBackToThePlainGlyphForGlyphStyles(t *testing.T) {
	g := syntheticHeatmapGrid(2, 2*time.Hour)
	var filled, square strings.Builder
	writeHeatmapRows(&filled, colorNone, g, defaultHeatmapAnchor, heatmapStyles[heatmapStyleFilled])
	writeHeatmapRows(&square, colorNone, g, defaultHeatmapAnchor, heatmapStyles[heatmapStyleSquare])
	if filled.String() != square.String() {
		t.Errorf("colorNone drew differently under filled and square (both Pitch 1); a Glyph style has "+
			"nothing to fall back FROM in plain text:\nfilled: %q\nsquare: %q", filled.String(), square.String())
	}

	var spaced strings.Builder
	writeHeatmapRows(&spaced, colorNone, g, defaultHeatmapAnchor, heatmapStyles[heatmapStyleSpaced])
	if spaced.String() == filled.String() {
		t.Errorf("colorNone drew identically under filled and spaced; spacing must still apply without color")
	}
}

// THE FULL SEAM. dashboardData.HeatmapStyle — the field dashboard.go's own
// -heatmap-style flag sets, exactly the way -exact sets data.Exact — must
// actually be read by renderActivityHeatmap. Every day in this fixture is
// marked collected, so the GRID carries no unobserved (hatch) cells at all
// and "no background fill in the grid" is unambiguous evidence of the
// square style rather than an accident of which days happened to be
// unobserved. Only the grid lines are checked — gridLines(out), not out
// itself — because the legend paints one background swatch of its own for
// its "unobserved" key regardless of style, which this task's brief leaves
// unchanged.
func TestHeatmapRenderActivityHeatmapHonorsDataHeatmapStyle(t *testing.T) {
	f := newHeatmapFixture(t, 7)
	for _, d := range f.days() {
		f.collected(d)
	}
	f.worked(f.days()[3], 5*time.Hour, 6*time.Hour, "Alderwick", "Ledger")
	f.compute(t)

	f.data.HeatmapStyle = heatmapStyleSquareSpaced
	out := f.render(t, colorTruecolor, metricsScope{})
	grid := strings.Join(gridLines(out), "\n")
	if strings.Contains(grid, "\x1b[48;2;") {
		t.Errorf("data.HeatmapStyle=%q still painted a background fill in the grid:\n%s",
			heatmapStyleSquareSpaced, out)
	}
	if !strings.Contains(grid, string(heatmapSquareGlyph)) {
		t.Errorf("data.HeatmapStyle=%q drew no square glyph in the grid:\n%s", heatmapStyleSquareSpaced, out)
	}

	f.data.HeatmapStyle = ""
	bare := f.render(t, colorTruecolor, metricsScope{})
	if !strings.Contains(strings.Join(gridLines(bare), "\n"), "\x1b[48;2;") {
		t.Errorf("an unset data.HeatmapStyle drew no background fill in the grid at all; the default must "+
			"stay filled:\n%s", bare)
	}
}
