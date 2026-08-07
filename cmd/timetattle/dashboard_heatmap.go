package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
)

// The activity heatmap: a GitHub-style year grid of the hours worked per day.
//
// # It computes nothing; it re-draws what the series already cut
//
// The hours in a cell come from seriesOf(..., unitDay, ...) in series.go — the
// SAME per-day cut `metrics -by day` prints — and not from a second pass over
// the events. That is the point of the reuse rather than a convenience: a
// heatmap that re-derived its own sittings would be a second answer to "how
// many hours was that", and the property those figures carry (wall clock is
// never double-counted; see internal/sessions' package doc) is a property of
// the pipeline, not of the arithmetic here. Inheriting it is the only way to
// have it. The reconciliation is asserted in
// TestHeatmapHoursReconcileAgainstTheReportTheyWereCutFrom, which sums the
// grid and subtracts the report.
//
// # What one cell means, and what it does NOT
//
// Unscoped, a cell is the day's PRESENCE: the wall clock the sittings cover,
// counted once however many projects shared it. Scoped, it is that client's or
// project's engaged time, which is a union across the client's projects and
// never a sum of them. All three are figures no minute appears twice in.
// Summing every project's absolute engaged time — what the series table's own
// "absolute" column shows — is the invoice figure and double-counts concurrent
// work, so it is deliberately NOT what a cell draws: a day cannot be more than
// a day long.
//
// # An unobserved day is not an idle day
//
// This is design principle 3 in project/2026-08-07-cli-overhaul-plan.md, and
// it is the requirement this section exists to satisfy. A 0.0h cell can mean
// two entirely different things: that collection ran and found no work, or
// that nothing was being collected at all. The store separates them where a
// capture or a harvest left a row (collectionLog.during, series.go), and this
// section draws the two differently — a dim slate hatch for unobserved, the
// dark ground of the ramp for an observed zero, and distinct characters, '?'
// against '.', wherever there is no colour to say it with.
//
// EVIDENCE IS ITSELF AN OBSERVATION, which is the second half of the rule and
// the easier half to get wrong. capture_run and harvest_run hold rows only
// from the day the store began keeping them, so most of a real corpus has
// hours and no run row. A day carrying recorded presence is observed whatever
// the run log says — the hours ARE the observation — and the check below is
// therefore presence OR a logged run, never the run alone. Observedness is
// asked UNSCOPED for the same reason: under a -project filter, a day the other
// project worked is a day we know this one was idle, which is a finding, and
// collapsing it into "no data" would throw the finding away.
//
// A day whose collection is recorded as LOST (a failed or partial run) is
// drawn as observed here, and that is a deliberate limit rather than an
// oversight: the day still carries whatever was recovered, a later harvest
// commonly recovers what an earlier one dropped, and which sources are behind
// is the SOURCE FRESHNESS section's own subject. The heatmap answers "was
// anything known about this day", not "is what is known complete".
//
// # Why colour changes the LAYOUT and not just the paint
//
// dashcolor.go's rule is that a section paints unconditionally and never
// branches on capability, because Paint returns "" when there is none. That
// holds for every colour written here. The half-block does not: '▀' encodes
// two days in one character cell by giving the upper half the foreground
// colour and the lower half the background, which is what packs seven
// day-of-week rows into four terminal lines (design principle 5). With no
// colour the two halves are indistinguishable, so there is nothing to encode
// with and the grid falls back to one row per weekday and one character per
// day. A capture, a pipe and a test all land there, and that plain grid is
// what this section's tests assert against.

// defaultHeatmapAnchor is the hours a full working day is taken to be: where
// the ramp reaches green, and where the escalation past it begins.
//
// IT IS A DEFAULT AND NOT A CONSTANT OF THE PROBLEM. Eight hours is the
// convention, not a measurement of anybody's day, and the ramp takes its
// anchor as a parameter precisely so that a per-project allocation can supply
// a different one — a client contracted for two days a week has a different
// idea of a full day than this. No allocation configuration exists in this
// codebase yet (metrics.Share.Allocated is a concurrency division across
// active clients, not an hour budget; see internal/metrics' own doc comment on
// the type), so nothing can supply one today and every render uses this. When
// that configuration lands, it passes an anchor here and nothing else changes.
const defaultHeatmapAnchor = 8 * time.Hour

// The grid's fixed geometry. heatmapGutter is the width of the weekday label
// column, so the month labels above the grid line up over their own weeks.
const (
	heatmapIndent = "  "
	heatmapGutter = 6
)

// The glyphs. heatmapUpper carries two days at once (foreground the upper,
// background the lower); heatmapLower carries one when the day above it falls
// outside the reported period; heatmapHatch is the dim hatch a cell whose two
// days were BOTH unobserved is drawn with, which is principle 3's own "dim
// hatch" in the one place a glyph can still say it — a character cell covers
// two days, so where they disagree only colour can, and where they agree the
// shape can too.
const (
	heatmapUpper rune = '▀'
	heatmapLower rune = '▄'
	heatmapHatch rune = '░'
	heatmapFull  rune = '█'
)

// The plain-text alphabet, for a terminal with no colour at all. The buckets
// escalate through the anchor and past it, and the unobserved character is
// deliberately not on that scale: it is not a quantity.
const (
	heatmapPlainUnobserved = '?'
	heatmapPlainIdle       = '.'
	heatmapPlainOver       = '!'
)

// heatmapPlainRamp is the escalating characters, each with the fraction of the
// anchor it reaches up to. Anything past the last one draws heatmapPlainOver.
var heatmapPlainRamp = []struct {
	upTo  float64
	glyph rune
}{
	{1.0 / 3, '-'},
	{2.0 / 3, '+'},
	{1, '#'},
	{2, 'X'},
}

// The ramp's stops. Below the anchor it climbs from the near-black ground to
// green; ABOVE it, it keeps climbing — through yellow and red to white — so
// that a fourteen-hour day is unmistakable rather than being rewarded with a
// deeper green. That is Ben's specification and it is the whole editorial
// stance of the picture: overwork is visible, not celebrated.
var (
	heatmapGround = RGB{0x16, 0x1b, 0x22}
	heatmapGreen  = RGB{0x2e, 0xa0, 0x43}
	heatmapYellow = RGB{0xf5, 0xd0, 0x42}
	heatmapRed    = RGB{0xff, 0x3b, 0x30}
	heatmapWhite  = RGB{0xff, 0xff, 0xff}
)

// The unobserved cell's own two colours, off the ramp entirely. A slate
// blue-grey is both lighter and a different hue from heatmapGround, so an
// unobserved day and an observed zero cannot be mistaken for each other at any
// size — which is the requirement, not a preference.
var (
	heatmapUnobservedInk    = RGB{0x5b, 0x67, 0x82}
	heatmapUnobservedGround = RGB{0x1e, 0x24, 0x30}
)

// heatmapState is what is known about a day, which is three answers and not
// two: observed, observed-and-idle collapses into observed with zero hours,
// and NOTHING RECORDED is its own state. See this file's own doc above.
type heatmapState int

const (
	// heatmapOutside is a day the grid draws because its week is on screen,
	// but which the reported period does not cover. It is blank: the period
	// never asked about it, so the picture asserts nothing either way.
	heatmapOutside heatmapState = iota
	// heatmapUnobserved is a day inside the period that carries no recorded
	// presence and that no capture or harvest ran during. Its zero was never
	// measured.
	heatmapUnobserved
	// heatmapObserved is a day something is known about: recorded presence,
	// or a run that looked and is entitled to have found nothing.
	heatmapObserved
)

func (s heatmapState) String() string {
	switch s {
	case heatmapObserved:
		return "observed"
	case heatmapUnobserved:
		return "unobserved"
	default:
		return "outside the period"
	}
}

// heatmapCell is one day of the grid.
type heatmapCell struct {
	Day sessions.Day
	// Hours is the SCOPED figure the cell's colour is drawn from, and is zero
	// for every state but heatmapObserved.
	Hours time.Duration
	State heatmapState
}

// heatmapGrid is the drawn rectangle: whole weeks, in columns, each holding
// the seven days of that week in the calendar's own week order.
//
// The rectangle is whole weeks and not just the period's days because the
// grid's shape is what makes it readable — a column is a week and a row is a
// weekday, and a ragged first column would break both. The days the period
// does not cover are heatmapOutside and are drawn blank, which is a fourth
// answer beside the three states: nothing was asked about them.
type heatmapGrid struct {
	// Rows is the weekday each of the seven rows draws, from the calendar's
	// own WeekStart — Monday under the shipped default, which is what makes
	// the grid and `metrics -by week` agree about where a week begins rather
	// than having two answers to that too.
	Rows [7]time.Weekday
	// Columns is one week each, oldest first.
	Columns [][7]heatmapCell
	// index is every IN-PERIOD day, for a lookup that does not care where in
	// the rectangle the day landed.
	index map[sessions.Day]heatmapCell
}

// cell is the day's cell, or a blank outside-the-period one if the grid does
// not cover it.
func (g heatmapGrid) cell(d sessions.Day) heatmapCell {
	if c, ok := g.index[d]; ok {
		return c
	}
	return heatmapCell{Day: d}
}

// cells is every day the period covers, in grid order. The outside-the-period
// days the rectangle is padded with are left out: they carry no figure and
// summing them would be summing days nobody asked about.
func (g heatmapGrid) cells() []heatmapCell {
	out := make([]heatmapCell, 0, len(g.index))
	for _, col := range g.Columns {
		for _, c := range col {
			if c.State != heatmapOutside {
				out = append(out, c)
			}
		}
	}
	return out
}

// heatmapGridOf lays the period out as whole weeks and fills each day from the
// per-day series metrics itself cuts.
func heatmapGridOf(cal sessions.Calendar, period dashboardPeriod, scope metricsScope,
	data *dashboardData) (heatmapGrid, error) {
	rows, err := seriesOf(data.Report, cal, unitDay, sessions.Window{From: period.From, To: period.To},
		data.Log)
	if err != nil {
		return heatmapGrid{}, err
	}

	// seriesOf's window.To is now the SAME half-open [From, To) convention
	// period itself carries (see series.go's own doc comment on seriesOf), so
	// a row belongs to the grid exactly when its own day's start instant falls
	// inside that window — no second, parallel call to cal.Days is needed just
	// to correct a boundary mismatch, only this filter for the widening
	// seriesOf still does on purpose (a sitting's lead-in or tail-out reaching
	// past the requested period, which the grid never draws).
	byDay := make(map[sessions.Day]metrics.Period, len(rows))
	for _, r := range rows {
		if r.Period.Start.Before(period.From) || !r.Period.Start.Before(period.To) {
			continue
		}
		byDay[cal.DayOf(r.Period.Start)] = r.Period
	}
	if len(byDay) == 0 {
		return heatmapGrid{}, nil
	}

	// period.To is the EXCLUSIVE closing boundary, so the last day the period
	// itself holds is the one the instant just before it falls in.
	firstDay, lastDay := cal.DayOf(period.From), cal.DayOf(period.To.Add(-time.Nanosecond))
	g := heatmapGrid{index: make(map[sessions.Day]heatmapCell, len(byDay))}
	first, last := cal.WeekOf(firstDay), cal.WeekOf(lastDay)
	for i := range g.Rows {
		g.Rows[i] = first.AddDays(i).Weekday()
	}
	for d := first; ; {
		var col [7]heatmapCell
		for i := range col {
			col[i] = heatmapCell{Day: d}
			if p, ok := byDay[d]; ok {
				col[i] = heatmapCellOf(cal, d, p, scope, data.Log)
				g.index[d] = col[i]
			}
			d = d.AddDays(1)
		}
		g.Columns = append(g.Columns, col)
		if col[0].Day == last {
			return g, nil
		}
	}
}

// heatmapCellOf is one day's figure and one day's state.
func heatmapCellOf(cal sessions.Calendar, d sessions.Day, p metrics.Period, scope metricsScope,
	log collectionLog) heatmapCell {
	c := heatmapCell{Day: d, Hours: heatmapHours(p, scope), State: heatmapUnobserved}
	start, end := cal.Bounds(d)
	// PRESENCE FIRST, and unscoped. A day carrying recorded work is observed
	// whatever the run log holds — most of a real corpus predates capture_run
	// and harvest_run entirely — and a day another project worked is a day
	// this one is known to have been idle. Only a day with neither is a day
	// nothing was ever established about. See this file's doc comment.
	if p.Presence.Total > 0 || log.during(start, end) != collectionUnrecorded {
		c.State = heatmapObserved
	}
	return c
}

// heatmapHours is what one cell draws: a figure no minute appears twice in,
// for whichever scope was asked for.
//
// Unfiltered it is PRESENCE and deliberately not the sum of the projects'
// absolute engaged time. That sum is the invoice figure — concurrent work
// bills to both clients — and a day two projects shared would draw as sixteen
// hours of a twenty-four hour day when eight elapsed. Filtered it is
// shareIn's own answer, which is the series table's, so a cell and a row of
// `metrics -by day` never disagree.
func heatmapHours(p metrics.Period, scope metricsScope) time.Duration {
	if scope.filtering() {
		absolute, _ := shareIn(p, scope)
		return absolute
	}
	return p.Presence.Total
}

// heatmapRamp is the colour for a day's hours, anchored at a full working day.
//
// Below the anchor it runs the dark ground to green. Above it, it keeps going
// — green to yellow at five thirds of the anchor, to red at seven thirds, to
// white at three — so that with the shipped eight-hour anchor a 24-hour day is
// white and everything past it stays there. Both ends clamp.
//
// anchor is a parameter and not a package constant so that a per-project
// allocation can move it; see defaultHeatmapAnchor for why nothing can supply
// one yet.
func heatmapRamp(worked, anchor time.Duration) RGB {
	if anchor <= 0 {
		anchor = defaultHeatmapAnchor
	}
	if worked <= 0 {
		return heatmapGround
	}
	stops := []struct {
		at float64
		c  RGB
	}{
		{0, heatmapGround},
		{1, heatmapGreen},
		{5.0 / 3, heatmapYellow},
		{7.0 / 3, heatmapRed},
		{3, heatmapWhite},
	}
	r := float64(worked) / float64(anchor)
	for i := 1; i < len(stops); i++ {
		if r > stops[i].at {
			continue
		}
		// The equality is checked before the interpolation rather than left to
		// it: a stop reached exactly must be the stop's own colour, and
		// arriving there through a float division and a rounded blend is a
		// place for it to be off by one in a channel.
		if r == stops[i].at {
			return stops[i].c
		}
		return blendRGB(stops[i-1].c, stops[i].c,
			(r-stops[i-1].at)/(stops[i].at-stops[i-1].at))
	}
	return heatmapWhite
}

// blendRGB is t of the way from a to b, per channel.
func blendRGB(a, b RGB, t float64) RGB {
	mix := func(x, y uint8) uint8 {
		return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t))
	}
	return RGB{mix(a.R, b.R), mix(a.G, b.G), mix(a.B, b.B)}
}

// heatmapColorOf is the colour one day is drawn in, which is the ramp for an
// observed day and the off-ramp slate for one nothing is known about.
func heatmapColorOf(c heatmapCell, anchor time.Duration) RGB {
	if c.State == heatmapUnobserved {
		return heatmapUnobservedInk
	}
	return heatmapRamp(c.Hours, anchor)
}

// heatmapPlainGlyph is one day as a single character, for a terminal with no
// colour. The buckets are coarse because characters are; the distinction the
// section exists for — nothing recorded against a measured zero — is not a
// bucket and gets its own character.
func heatmapPlainGlyph(c heatmapCell, anchor time.Duration) rune {
	switch c.State {
	case heatmapOutside:
		return ' '
	case heatmapUnobserved:
		return heatmapPlainUnobserved
	}
	if anchor <= 0 {
		anchor = defaultHeatmapAnchor
	}
	if c.Hours <= 0 {
		return heatmapPlainIdle
	}
	r := float64(c.Hours) / float64(anchor)
	for _, b := range heatmapPlainRamp {
		if r <= b.upTo {
			return b.glyph
		}
	}
	return heatmapPlainOver
}

// renderActivityHeatmap draws the section: month labels, the grid, a legend.
// No prose — design principle 1, and the docs carry what the picture means
// (docs/content/docs/dashboard.md).
func renderActivityHeatmap(_ context.Context, out io.Writer, scope metricsScope,
	period dashboardPeriod, cc colorCapability, data *dashboardData) error {
	cal, err := data.Config.Calendar.Sessions()
	if err != nil {
		return err
	}
	g, err := heatmapGridOf(cal, period, scope, data)
	if err != nil {
		return err
	}
	return writeHeatmap(out, cc, g, defaultHeatmapAnchor)
}

// writeHeatmap draws a built grid. It is separate from the render function
// above so that the drawing can be exercised against a constructed grid
// without a store, a config or a report anywhere near it.
func writeHeatmap(out io.Writer, cc colorCapability, g heatmapGrid, anchor time.Duration) error {
	var b strings.Builder
	if len(g.Columns) == 0 {
		fmt.Fprintf(&b, "%sno days in the period\n", heatmapIndent)
		_, err := io.WriteString(out, b.String())
		return err
	}
	writeHeatmapMonths(&b, g)
	if cc == colorNone {
		writeHeatmapPlainRows(&b, g, anchor)
	} else {
		writeHeatmapBlockRows(&b, cc, g, anchor)
	}
	writeHeatmapLegend(&b, cc, anchor)
	_, err := io.WriteString(out, b.String())
	return err
}

// writeHeatmapMonths labels the column each month's first drawn week begins
// at.
//
// A label is three characters over a one-character column, so it runs across
// the two weeks after it and the next label waits until there is room. The
// last label is allowed to overhang the end of the grid rather than being
// dropped: there is nothing to the right of the grid for it to collide with,
// and a picture that silently stops naming months near its own right-hand edge
// is exactly the kind of quiet omission the rest of this codebase spends its
// comments refusing.
func writeHeatmapMonths(b *strings.Builder, g heatmapGrid) {
	labels := make([]rune, len(g.Columns)+3)
	for i := range labels {
		labels[i] = ' '
	}
	prev, next := time.Month(0), 0
	for c, col := range g.Columns {
		m := col[0].Day.Month
		if m == prev {
			continue
		}
		prev = m
		if c < next {
			continue
		}
		copy(labels[c:], []rune(m.String()[:3]))
		next = c + 4
	}
	fmt.Fprintf(b, "%s%s%s\n", heatmapIndent, strings.Repeat(" ", heatmapGutter),
		strings.TrimRight(string(labels), " "))
}

// writeHeatmapBlockRows draws the grid in four lines, two weekdays to a line.
func writeHeatmapBlockRows(b *strings.Builder, cc colorCapability, g heatmapGrid, anchor time.Duration) {
	for r := 0; r < len(g.Rows); r += 2 {
		label := g.Rows[r].String()[:2]
		if r+1 < len(g.Rows) {
			label += " " + g.Rows[r+1].String()[:2]
		}
		fmt.Fprintf(b, "%s%-*s", heatmapIndent, heatmapGutter, label)
		for _, col := range g.Columns {
			// The seventh row has no partner, so its lower half is a day that
			// does not exist and is drawn as outside the period — the same
			// blank the padding days on either end of the grid get.
			lower := heatmapCell{}
			if r+1 < len(g.Rows) {
				lower = col[r+1]
			}
			b.WriteString(heatmapBlock(cc, col[r], lower, anchor))
		}
		b.WriteByte('\n')
	}
}

// heatmapBlock is one character cell, carrying up to two days.
func heatmapBlock(cc colorCapability, upper, lower heatmapCell, anchor time.Duration) string {
	switch {
	case upper.State == heatmapOutside && lower.State == heatmapOutside:
		return " "
	case upper.State == heatmapUnobserved && lower.State == heatmapUnobserved:
		return cc.Paint(heatmapUnobservedInk) + cc.PaintBG(heatmapUnobservedGround) +
			string(heatmapHatch) + cc.Reset()
	case lower.State == heatmapOutside:
		return cc.Paint(heatmapColorOf(upper, anchor)) + string(heatmapUpper) + cc.Reset()
	case upper.State == heatmapOutside:
		return cc.Paint(heatmapColorOf(lower, anchor)) + string(heatmapLower) + cc.Reset()
	default:
		return cc.Paint(heatmapColorOf(upper, anchor)) + cc.PaintBG(heatmapColorOf(lower, anchor)) +
			string(heatmapUpper) + cc.Reset()
	}
}

// writeHeatmapPlainRows draws the grid one weekday to a line, which is what a
// terminal with no colour can carry: see this file's doc comment on why the
// layout and not merely the paint changes.
func writeHeatmapPlainRows(b *strings.Builder, g heatmapGrid, anchor time.Duration) {
	for r, weekday := range g.Rows {
		fmt.Fprintf(b, "%s%-*s", heatmapIndent, heatmapGutter, weekday.String()[:3])
		for _, col := range g.Columns {
			b.WriteRune(heatmapPlainGlyph(col[r], anchor))
		}
		b.WriteByte('\n')
	}
}

// writeHeatmapLegend states the scale, including the anchor in force — never a
// hard-coded eight hours, so that a moved anchor can never leave the legend
// asserting the wrong thing about the picture beside it.
func writeHeatmapLegend(b *strings.Builder, cc colorCapability, anchor time.Duration) {
	if cc == colorNone {
		fmt.Fprintf(b, "%s%c unobserved  %c idle  %s to %s  %c %c beyond\n", heatmapIndent,
			heatmapPlainUnobserved, heatmapPlainIdle, heatmapPlainRampKey(), heatmapHoursLabel(anchor),
			heatmapPlainRamp[len(heatmapPlainRamp)-1].glyph, heatmapPlainOver)
		return
	}
	fmt.Fprintf(b, "%s0h ", heatmapIndent)
	writeHeatmapSwatches(b, cc, 6, 0, anchor, anchor)
	fmt.Fprintf(b, " %s ", heatmapHoursLabel(anchor))
	writeHeatmapSwatches(b, cc, 12, anchor, 2*anchor, anchor)
	fmt.Fprintf(b, " %s+   %s%s%c%s unobserved\n", heatmapHoursLabel(3*anchor),
		cc.Paint(heatmapUnobservedInk), cc.PaintBG(heatmapUnobservedGround), heatmapHatch, cc.Reset())
}

// writeHeatmapSwatches draws n blocks sampling the ramp across [from, from+span),
// each at the middle of the slice it stands for.
func writeHeatmapSwatches(b *strings.Builder, cc colorCapability, n int, from, span, anchor time.Duration) {
	for i := range n {
		at := from + time.Duration((float64(i)+0.5)/float64(n)*float64(span))
		b.WriteString(cc.Paint(heatmapRamp(at, anchor)) + string(heatmapFull) + cc.Reset())
	}
}

// heatmapPlainRampKey is the escalating characters, spaced, for the plain
// legend.
func heatmapPlainRampKey() string {
	var parts []string
	for _, b := range heatmapPlainRamp[:len(heatmapPlainRamp)-1] {
		parts = append(parts, string(b.glyph))
	}
	return strings.Join(parts, " ")
}

// heatmapHoursLabel writes a duration the way a legend tick should read: whole
// hours where it is whole, one decimal where it is not.
func heatmapHoursLabel(d time.Duration) string {
	if d%time.Hour == 0 {
		return fmt.Sprintf("%dh", int(d/time.Hour))
	}
	return fmt.Sprintf("%.1fh", d.Hours())
}
