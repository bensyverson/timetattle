package main

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
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
// # Colour changes the PAINT, never the LAYOUT
//
// dashcolor.go's rule is that a section paints unconditionally and never
// branches on capability, because Paint returns "" when there is none. This
// section now follows it without exception: every capability draws the same
// rectangle, seven terminal lines — one per weekday — and one character per
// week, and colour changes what a cell's character says, never how many
// lines the grid takes.
//
// That was not always true. Design principle 5 in
// project/2026-08-07-cli-overhaul-plan.md specified '▀', the half-block,
// packing two weekday rows into one coloured line by giving the upper day the
// foreground colour and the lower day the background — four terminal lines
// instead of seven, with a colour-less terminal falling back to the plain
// seven-row grid because there was nothing to encode the pairing with. Ben's
// own read of that grid, from a screenshot, was that four dense lines are
// squinty; job dlIfA asked for the full seven lines in every capability, and
// the half-block path is deleted rather than kept as a fallback or an
// option. Principle 5 in project/2026-08-07-cli-overhaul-plan.md carries a
// marked correction-in-place recording that retirement, per this project's
// documentation practice.
//
// An observed day's cell is a small foreground-painted square
// (heatmapSquareGlyph, U+25A0) on the panel's own ground, rather than a
// background-painted space. job z47sR put both renderings, plus a spaced
// (one blank column between weeks) variant of each, behind a temporary
// -heatmap-style flag so Ben could compare them in his own terminal; his
// screenshot pick was square-spaced — the foreground glyph, with the spacer
// column — and job xLn3T hardcoded that one choice here, deleting the flag
// and the other three variants along with their tests. A background fill has
// no glyph to rasterize at all, which would have been the safer choice at
// density (some terminal fonts, bitmap ones especially, leave a hairline gap
// around block-drawing glyphs, read as a seam between adjacent cells); the
// spacer column this grid always draws now is what makes the foreground
// glyph's breathing room the point rather than the risk. The legend's ramp
// swatches (writeHeatmapSwatches) keep the older foreground '█' regardless —
// they are a handful of large, isolated samples where a seam was never at
// stake, and the legend's own rendering does not depend on the grid's cell
// style.

// defaultHeatmapAnchor is the hours a full working day is taken to be: where
// the ramp reaches green, and where the escalation past it begins.
//
// IT IS A DEFAULT AND NOT A CONSTANT OF THE PROBLEM. Eight hours is the
// convention, not a measurement of anybody's day, and the ramp takes its
// anchor as a parameter precisely so that a scoped engagement's own budget
// can supply a different one — a client contracted for two days a week has a
// different idea of a full day than this. heatmapAnchorFor (below) is what
// supplies one now, reusing the Allocation & Effective Rate section's own
// pro-ration (allocationWindowBudget, dashboard_allocation.go) rather than a
// second idea of what a budget means. This default is still what every
// render falls back to: when the scope's own engagement is a fixed contract
// rather than a recurring budget, when more than one engagement could apply
// and neither is uniquely the scope's own (heatmapEngagementFor's own doc
// comment), or — the overwhelming majority of runs, exactly as
// dashboard_allocation.go's allocationUnconfigured describes — when nothing
// is configured for the scope at all.
const defaultHeatmapAnchor = 8 * time.Hour

// The grid's fixed geometry. heatmapGutter is the width of the weekday label
// column, so the month labels above the grid line up over their own weeks.
const (
	heatmapIndent = "  "
	heatmapGutter = 6
)

// The glyphs. heatmapHatch is the dim hatch an unobserved day is drawn with —
// principle 3's own "dim hatch" in the one place a shape, not just a colour,
// can say it. heatmapFull is the legend's own ramp swatch, painted with the
// foreground colour it demonstrates; the grid's own observed-day cells use
// heatmapSquareGlyph instead — see this file's doc comment above for the
// choice and the spacer column that gives it room at the grid's density.
const (
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

// heatmapPitch is the fixed number of printed columns per week: the cell
// itself, plus one blank spacer column after every week but the last
// (writeHeatmapRows). writeHeatmapMonths and writeHeatmapLegend both scale
// by it too, so the grid's wider columns and its month labels and legend
// never drift apart from each other. It was a per-style field (heatmapStyle
// below, job z47sR) while filled/square/spaced/square-spaced were being
// compared; job xLn3T hardcoded Ben's square-spaced pick and this became a
// constant.
const heatmapPitch = 2

// heatmapSquareGlyph is the grid's foreground glyph — job z47sR's ask for
// "a smaller square" than the background fill the grid used before it, Ben's
// own screenshot feedback naming U+25FC as a starting point.
//
// U+25FC BLACK MEDIUM SQUARE was rejected, per the brief: it carries
// Unicode's Ambiguous East Asian Width (a CJK-locale terminal may double-
// width it) AND — the sharper hazard — it is one of the "squares" Unicode's
// own emoji data (emoji-data.txt) lists as Emoji=Yes. Some terminal font
// stacks substitute a colour emoji glyph for an emoji-capable codepoint even
// with no explicit U+FE0F variation selector present, purely because a
// colour glyph exists for it. That substitution is worse than a width
// mismatch alone: it silently doubles the glyph's width AND recolours it,
// defeating the ramp entirely. U+25A0 BLACK SQUARE and U+25AA BLACK SMALL
// SQUARE carry the SAME emoji promotion (both are in the same emoji-data.txt
// block as 25FC, just substituted less often in practice) and are rejected
// for the identical reason; U+25FE BLACK MEDIUM-SMALL SQUARE is the same
// family again.
//
// U+25A0 BLACK SQUARE is the pick. The first choice here was U+2B1D BLACK
// VERY SMALL SQUARE, for the same emoji-avoidance reason, and Ben's own
// terminal retired it in one look: "very small" is the literal truth of the
// glyph, and a cell that small defeats the purpose of a heatmap cell. U+25A0
// shares the property that matters — it was never swept into the emoji
// additions its 25xx-block siblings (U+25AA/25AB, U+25FB–25FE) received, so
// no font stack has a colour glyph to substitute it with — while rendering
// at a legible medium size in the monospace fonts that matter here. It
// carries the same Ambiguous East Asian Width every block/geometric
// character in this file already does (heatmapHatch and the legend swatch
// heatmapFull are both Ambiguous too, and that risk already ships
// unconditionally), so this is not a NEW hazard.
//
// NEVER APPEND A VARIATION SELECTOR (U+FE0F or U+FE0E) where this constant
// is written: that is the one thing that WOULD force emoji presentation on
// a codepoint that does not currently carry it.
//
// This is a single named constant precisely so it stays one-line swappable:
// Ben's own terminal, with his own font, is the final judge of what actually
// renders monospace there, and if U+25A0 turns out wrong the fix is
// changing this one rune, nothing that reads it.
const heatmapSquareGlyph = '■' // U+25A0 BLACK SQUARE

// heatmapIdleGlyphInk is the fixed colour an IDLE day's square draws in — a
// day heatmapColorOf would otherwise colour with heatmapGround, the ramp's
// own near-black "has not started yet" tone.
//
// heatmapGround was rejected for this glyph specifically. A background-fill
// rendering (heatmapDayCell's, before job xLn3T hardcoded the foreground
// glyph) paints the ramp's colour onto the CELL'S OWN BACKGROUND, where a
// near-black fill still reads as "the dark end of the ramp" against the
// lighter cells around it. heatmapSquareCell instead paints only a
// FOREGROUND glyph on whatever the terminal's own background already is — a
// colour this file never controls. A near-black glyph on a
// dark terminal theme is not dim, it is INVISIBLE, and an invisible idle
// cell then reads exactly like a day outside the period: the very
// collapse — a measured zero indistinguishable from nothing recorded — this
// file's package doc spends its longest section refusing to allow. Drawing
// NOTHING for an idle day (bare ground) was rejected for the identical
// reason: it is the same collapse by construction, not an accident of one
// bad theme.
//
// A fixed, theme-independent dim grey is drawn instead: neutral (no hue,
// unlike heatmapUnobservedInk's blue-grey slate, so idle and unobserved are
// never confusable by colour alone even before the hatch shape is
// considered) and dim enough to read as "measured, and there was nothing
// there" beside a ramp that only gets brighter from here — but never so
// dark that a plausible terminal background could swallow it.
var heatmapIdleGlyphInk = RGB{0x4a, 0x4a, 0x4a}

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
// anchor is a parameter and not a package constant so that a scoped
// engagement's own budget can move it; see heatmapAnchorFor for what supplies
// one today and defaultHeatmapAnchor for the fallback.
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
	return writeHeatmap(out, cc, g, heatmapAnchorFor(cal, scope, period, data))
}

// heatmapAnchorFor derives the ramp's anchor from the scoped engagement's own
// pro-rated daily budget, reusing exactly the machinery the Allocation &
// Effective Rate section (dashboard_allocation.go) already built for its own
// budget bar rather than a second idea of what pro-rating means:
// allocationWindowBudget pro-rates a recurring hourly budget across the
// window, and observedDays' own total (dashboard_stats.go) is the same
// real-calendar-day count every other per-day average on this dashboard
// divides by — "the period's real days" the task that added this function
// asked for, in exactly those words.
//
// It falls back to defaultHeatmapAnchor whenever no single engagement can be
// attributed to what the heatmap's cells actually measure — see
// heatmapEngagementFor's own doc comment for the multiple-engagement and
// unscoped cases this deliberately refuses to guess at — or when the
// engagement in scope declares a fixed contract rather than a recurring
// budget: a contract has no per-period shape to pro-rate, so "beyond a full
// day" has nothing of its own to be measured against.
func heatmapAnchorFor(cal sessions.Calendar, scope metricsScope, period dashboardPeriod,
	data *dashboardData) time.Duration {
	e, ok := heatmapEngagementFor(scope, data)
	if !ok || e.Shape() != config.EngagementHourly {
		return defaultHeatmapAnchor
	}
	budget := allocationWindowBudget(cal, period, e)
	_, days := observedDays(cal, period, data)
	if budget <= 0 || days <= 0 {
		return defaultHeatmapAnchor
	}
	return time.Duration(budget / float64(days) * float64(time.Hour))
}

// heatmapEngagementFor is the one configured engagement, if any, whose own
// hours are EXACTLY what the heatmap's cells draw under this scope.
//
// # Why this mirrors shareIn instead of reading scopedEngagements
//
// heatmapHours (above) draws shareIn's own figure: one project's engaged
// total when scope.Project is set, one client's UNION across its projects
// when only scope.Client is, and presence — nobody's budget — otherwise.
// scopedEngagements (dashboard_allocation.go) answers a different question,
// "every engagement this scope's row list should print", and can legitimately
// return several at once: a client scope prints the client's own engagement
// AND every one of that client's projects that carries its own. Handing the
// ramp one of several possible budgets — or summing them — would be a
// confident, wrong number the moment a client's projects do not all share one
// budget, which is exactly the kind of collapse this dashboard's other
// sections spend their own doc comments refusing. So this looks up exactly
// the one entry stated at the SAME granularity shareIn already committed to:
//
//   - scope.Project set (shareIn's Project branch always wins when both
//     Client and Project are set — see shareIn's own switch) looks up that
//     project's own engagement, never the client's, even when the client
//     also carries one: the client's budget covers the client's whole
//     portfolio, and applying it to one project's slice of it would be a lie
//     in either direction.
//   - scope.Client set with no Project looks up the client's own engagement,
//     which is the only shape a budget can take that matches a UNION across
//     that client's projects.
//   - Neither set (-all, or an unscoped run) returns false: presence spans
//     every client at once, and no single engagement's budget describes it.
//
// # The other open question — a period that does not cover the whole window
//
// The brief that created this function asked what happens when an
// engagement's period does not cover the whole heatmap window, and the
// answer chosen is that a day outside any budgeted period falls back to the
// default anchor rather than asserting a budget for a day it was never
// stated to cover. That case CANNOT ARISE TODAY: an hourly entry carries no
// start date at all — config.Engagement's validateHourly refuses one,
// Started is contract-only — so a configured weekly or monthly budget is, by
// construction, in force for the engagement's entire life, and the single
// anchor this function returns already covers every day of any window. If a
// future config change lets an hourly engagement state when it began (an
// open question dashboard_allocation.go's own pushback for mDu9N already
// raises for a different reason), this function would need to turn into a
// PER-DAY anchor rather than one figure for the whole grid, and heatmapCellOf
// would need to carry it — see heatmapGridOf and writeHeatmap, both of which
// take a single anchor today. Building that now, with no config field to
// drive it, would be exactly the untested guess this task's own "never lies"
// instruction warns against.
func heatmapEngagementFor(scope metricsScope, data *dashboardData) (config.Engagement, bool) {
	if data == nil || data.Config == nil {
		return config.Engagement{}, false
	}
	switch {
	case scope.Project != "":
		return data.Config.Engagements.Project(scope.Project)
	case scope.Client != "":
		return data.Config.Engagements.Client(scope.Client)
	default:
		return config.Engagement{}, false
	}
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
	writeHeatmapRows(&b, cc, g, anchor)
	// ONE BLANK LINE between the grid and the legend — Ben's finishing touch
	// on job xLn3T, so the legend reads as a caption beneath the picture
	// rather than an eighth row of it.
	b.WriteByte('\n')
	writeHeatmapLegend(&b, cc, anchor)
	_, err := io.WriteString(out, b.String())
	return err
}

// writeHeatmapMonths labels the column each month's first drawn week begins
// at.
//
// A label is three characters over a one-column-wide week, so it runs across
// the two weeks after it and the next label waits until there is room. The
// last label is allowed to overhang the end of the grid rather than being
// dropped: there is nothing to the right of the grid for it to collide with,
// and a picture that silently stops naming months near its own right-hand edge
// is exactly the kind of quiet omission the rest of this codebase spends its
// comments refusing.
//
// A week's own printed column is c*heatmapPitch, not c: writeHeatmapRows
// below draws heatmapPitch columns per week (the cell, plus a blank spacer),
// and a label computed in WEEK-index space rather than printed-column space
// would drift right under every week after the first.
func writeHeatmapMonths(b *strings.Builder, g heatmapGrid) {
	width := len(g.Columns) * heatmapPitch
	labels := make([]rune, width+3)
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
		pos := c * heatmapPitch
		if pos < next {
			continue
		}
		copy(labels[pos:], []rune(m.String()[:3]))
		next = pos + 4
	}
	fmt.Fprintf(b, "%s%s%s\n", heatmapIndent, strings.Repeat(" ", heatmapGutter),
		strings.TrimRight(string(labels), " "))
}

// writeHeatmapRows draws the grid: one line per weekday, one cell per week,
// the same seven lines in every capability. Colour and glyph shape change
// what a cell draws — see this file's doc comment for why the LAYOUT itself
// no longer varies with colour. heatmapPitch adds one blank spacer column
// after every week but the last — "a blank column BETWEEN weeks", not a
// trailing margin — under colorNone exactly as under colour, since a reader
// of a piped or captured plain-text grid must see the same spacing a
// terminal does.
func writeHeatmapRows(b *strings.Builder, cc colorCapability, g heatmapGrid, anchor time.Duration) {
	for r, weekday := range g.Rows {
		fmt.Fprintf(b, "%s%-*s", heatmapIndent, heatmapGutter, weekday.String()[:3])
		for i, col := range g.Columns {
			if cc == colorNone {
				b.WriteRune(heatmapPlainGlyph(col[r], anchor))
			} else {
				b.WriteString(heatmapDayCell(cc, col[r], anchor))
			}
			if i < len(g.Columns)-1 {
				b.WriteString(strings.Repeat(" ", heatmapPitch-1))
			}
		}
		b.WriteByte('\n')
	}
}

// heatmapDayCell is one coloured day's character cell: the dim slate hatch
// for a day nothing was ever established about (see heatmapIdleGlyphInk's
// own doc comment for why unobserved keeps its own treatment rather than
// drawing the square glyph too), a blank for a day outside the reported
// period, or — for an observed day — heatmapSquareCell's foreground glyph.
func heatmapDayCell(cc colorCapability, c heatmapCell, anchor time.Duration) string {
	switch c.State {
	case heatmapOutside:
		return " "
	case heatmapUnobserved:
		return cc.Paint(heatmapUnobservedInk) + cc.PaintBG(heatmapUnobservedGround) +
			string(heatmapHatch) + cc.Reset()
	default:
		return heatmapSquareCell(cc, c, anchor)
	}
}

// heatmapSquareCell is one OBSERVED day: heatmapSquareGlyph, painted the
// ramp's own colour, with no PaintBG call at all — that absence is exactly
// what gives the cell the breathing room Ben's feedback asked for, since the
// glyph sits on the panel's own ground rather than on a painted rectangle.
// An idle day (Hours <= 0) is the one exception: see
// heatmapIdleGlyphInk's own doc comment for why it draws a fixed dim grey
// rather than heatmapColorOf's own near-black ramp-ground colour.
func heatmapSquareCell(cc colorCapability, c heatmapCell, anchor time.Duration) string {
	ink := heatmapColorOf(c, anchor)
	if c.Hours <= 0 {
		ink = heatmapIdleGlyphInk
	}
	return cc.Paint(ink) + string(heatmapSquareGlyph) + cc.Reset()
}

// writeHeatmapLegend states the scale, including the anchor in force — never a
// hard-coded eight hours, so that a moved anchor can never leave the legend
// asserting the wrong thing about the picture beside it. Its ramp swatches
// (writeHeatmapSwatches) draw ONE SWATCH PER HOUR the span covers —
// heatmapLegendSwatchCount — decoupled from the grid's own column pitch
// (job xLn3T): with the shipped 8h anchor that is 8 swatches across 0h-8h
// and 16 across 8h-24h, Ben's own ask, rather than the doubled 12-and-24 an
// earlier draft drew by scaling the swatch count with heatmapPitch instead
// of with the hours themselves.
func writeHeatmapLegend(b *strings.Builder, cc colorCapability, anchor time.Duration) {
	if cc == colorNone {
		fmt.Fprintf(b, "%s%c unobserved  %c idle  %s to %s  %c %c beyond\n", heatmapIndent,
			heatmapPlainUnobserved, heatmapPlainIdle, heatmapPlainRampKey(), heatmapHoursLabel(anchor),
			heatmapPlainRamp[len(heatmapPlainRamp)-1].glyph, heatmapPlainOver)
		return
	}
	fmt.Fprintf(b, "%s0h ", heatmapIndent)
	writeHeatmapSwatches(b, cc, heatmapLegendSwatchCount(anchor), 0, anchor, anchor)
	fmt.Fprintf(b, " %s ", heatmapHoursLabel(anchor))
	writeHeatmapSwatches(b, cc, heatmapLegendSwatchCount(2*anchor), anchor, 2*anchor, anchor)
	fmt.Fprintf(b, " %s+   %s%s%c%s unobserved\n", heatmapHoursLabel(3*anchor),
		cc.Paint(heatmapUnobservedInk), cc.PaintBG(heatmapUnobservedGround), heatmapHatch, cc.Reset())
}

// heatmapLegendSwatchCount is one ramp swatch per hour span covers, so the
// legend's own width is a function of the anchor alone and never of the
// grid's column pitch. A span shorter than an hour still draws one swatch —
// there is nothing to show with zero.
func heatmapLegendSwatchCount(span time.Duration) int {
	if n := int(span / time.Hour); n > 0 {
		return n
	}
	return 1
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
