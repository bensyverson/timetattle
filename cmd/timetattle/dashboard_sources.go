package main

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
)

// renderSourceAttribution draws a horizontal stacked bar of estimated hours
// by evidencing source combination, with explicit segments for multi-source
// corroboration — the source attribution task
// (project/2026-08-07-cli-overhaul-plan.md). data.Report.Sessions' own
// Sources field already carries the per-sitting source list this is built
// from; segment totals must reconcile numerically with the metrics figures
// for the same scope and period, per that task's own criteria.
//
// THIS FILE, AND dashboard_sources_test.go, ARE THE ONLY FILES THAT TASK
// EDITS. See the section list in dashboard.go for the contract every render
// function shares.
//
// # Why scope does not filter what is drawn here — read before "fixing" this
//
// dashboardRenderFunc's contract says a section filters by scope using
// metricsScope's own showsClient/showsProject against data.Report's Clients
// and Projects rows. Sittings carry no such thing: sessions.Session (an
// alias for sittings.Sitting) has Start, End, Events, Sources and Margin and
// nothing that names a project, because sittings are deliberately GLOBAL —
// see internal/sessions' own package doc, "Sittings are global, and
// attribution happens above them". A per-project cluster could produce
// engaged time but could never recover presence, since two projects'
// sittings would each claim the same hour.
//
// This is not a gap this file introduces: metrics.go's own reportMetrics
// prints "PRESENCE %s across %s sittings" from r.Presence.Total
// UNCONDITIONALLY, before scope is consulted at all, and a -client or
// -project run still shows the same figure — only the Clients/Projects
// tables below it are narrowed. Source attribution is a presence-shaped
// question ("what evidenced the wall clock"), not an engaged-time-shaped one
// ("whose hour was this"), so it follows the same precedent: this section
// draws the whole period's sittings regardless of scope, exactly as the
// PRESENCE line already does. scope is accepted (the shared signature
// requires it) and deliberately unused.
func renderSourceAttribution(_ context.Context, out io.Writer, _ metricsScope, _ dashboardPeriod,
	cc colorCapability, data *dashboardData) error {
	segments, total := partitionSittingsBySources(data.Report.Sessions)
	if total == 0 {
		// A NO-DATA state, not a measured zero — design principle 3 in the
		// overhaul plan. Printing "0.0h" or an empty bar here would read as
		// "no work happened" when the true fact is narrower: no sittings
		// exist in this period at all, whatever the reason.
		_, err := fmt.Fprint(out, "  no sittings in this period\n")
		return err
	}
	segments = capSourceSegments(segments, maxSourceSegments)

	if err := writeSourceBar(out, cc, segments, total); err != nil {
		return err
	}
	return writeSourceLegend(out, cc, segments, total)
}

// maxSourceSegments is the most individual combinations drawn before the
// rest fold into one explicit remainder row — including that row, so at
// most maxSourceSegments-1 real combinations are ever named individually.
// Fixed at the categorical palette's own size (sourceSwatches below) so
// every displayed combination gets its own color/glyph slot with nothing
// left to wrap or reuse — see the palette's own doc comment for why reuse is
// the thing being avoided.
const maxSourceSegments = 8

// sourceSegment is one evidencing source combination's share of the period,
// or (when remainderCombos > 0) the synthetic remainder row capSourceSegments
// folds the lowest-ranked combinations into.
type sourceSegment struct {
	// sources is the sorted, deduplicated combination — sessions.Session's
	// own Sources field is already sorted, and combos are compared by this
	// slice's contents rather than a pre-joined string so a segment never
	// depends on the separator chosen for display.
	sources []string
	hours   time.Duration
	count   int // sittings contributing to this segment
	// remainderCombos is >0 only for the synthetic remainder row; it says how
	// MANY distinct combinations were folded in, so a cap is never silent —
	// AGENTS.md's rule against capping without saying what was aggregated.
	remainderCombos int
}

// name is the label this segment prints — single-source and multi-source
// combinations are both first-class and named for what they are, never
// collapsed into a generic "multiple" bucket (task UgTU2 criterion pl8).
func (s sourceSegment) name() string {
	switch {
	case s.remainderCombos > 0:
		return fmt.Sprintf("%d more combinations (%s)", s.remainderCombos, plural(s.count, "sitting"))
	case len(s.sources) == 0:
		// Unreached on data sessions.Cluster produced: a Session is only
		// emitted when at least one observation evidenced it (Events > 0),
		// and every such observation's source is appended to Sources before
		// the Session is appended to Result.Sessions — see sessions.go. Kept
		// so a hand-built or future fixture with a genuine gap fails loudly
		// rather than panicking on an empty join.
		return "(no evidence recorded)"
	case len(s.sources) == 1:
		return s.sources[0] + " only"
	default:
		return strings.Join(s.sources, "+")
	}
}

// partitionSittingsBySources groups sits by their exact Sources combination
// and sums each group's full sitting duration (Start..End, margin included —
// see the margin doc comment above writeSourceLegend). total is the sum
// across every group, which by construction equals the sum of every sitting's
// own Duration(): this function partitions sits without dropping or
// double-counting any of them, so total always equals
// metrics.Report.Presence.Total for the same Sessions slice (Presence.Total
// is itself built by summing exactly these spans — see
// internal/metrics/metrics.go's Compute).
func partitionSittingsBySources(sits []sessions.Session) ([]sourceSegment, time.Duration) {
	byKey := map[string]*sourceSegment{}
	var order []string
	var total time.Duration
	for _, s := range sits {
		d := s.Duration()
		total += d
		key := strings.Join(s.Sources, "\x00")
		seg, ok := byKey[key]
		if !ok {
			seg = &sourceSegment{sources: append([]string(nil), s.Sources...)}
			byKey[key] = seg
			order = append(order, key)
		}
		seg.hours += d
		seg.count++
	}
	segments := make([]sourceSegment, 0, len(order))
	for _, key := range order {
		segments = append(segments, *byKey[key])
	}
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].hours != segments[j].hours {
			return segments[i].hours > segments[j].hours
		}
		return segments[i].name() < segments[j].name() // deterministic tie-break
	})
	return segments, total
}

// capSourceSegments keeps the top max-1 segments by hours and folds every
// remaining one into a single explicit remainder segment appended last, so
// the displayed row count never exceeds max. It never drops a segment's
// hours: the remainder's own hours are the exact sum of what it folded, so
// the total across every returned segment (including the remainder) is
// unchanged from the input — the cap changes how many ROWS are drawn, never
// how many HOURS are accounted for.
func capSourceSegments(segments []sourceSegment, max int) []sourceSegment {
	if max < 2 || len(segments) <= max {
		return segments
	}
	kept := append([]sourceSegment(nil), segments[:max-1]...)
	var remainder sourceSegment
	for _, s := range segments[max-1:] {
		remainder.hours += s.hours
		remainder.count += s.count
		remainder.remainderCombos++
	}
	return append(kept, remainder)
}

// sourceBarWidth is the stacked bar's total width in cells. Fixed rather
// than derived from columnsOf(out): dashboardRenderFunc's own doc comment
// says a section lays itself out for cc's color capability and not for a
// terminal's column count, exactly as this file's sibling sections do.
const sourceBarWidth = 50

// sourceSwatch pairs one categorical slot's paint color with the glyph that
// stands in for it when cc is colorNone — so a reader with no color
// capability can still tell segments apart by shape rather than losing that
// information entirely (the brief's own requirement).
type sourceSwatch struct {
	rgb   RGB
	glyph rune
}

// sourceSwatches is the FIXED, ordered categorical palette this file paints
// segments with — the dataviz skill's validated default 8-hue categorical
// theme (references/palette.md), used here in its documented fixed order
// rather than cycled: "assign categorical hues in fixed order, never
// cycled" is the skill's own non-negotiable, and it matters concretely here
// because a stacked bar's adjacent-pair readability is exactly what that
// palette's ordering was chosen to hold (worst adjacent CVD ΔE 9.1
// light/8.4 dark, both above the ≥8 target). The glyphs are eight visually
// distinct Unicode block/pattern characters so colorNone keeps the same
// distinguishability property by shape instead of hue.
//
// Timetattle has no established brand palette elsewhere in the codebase
// (checked before choosing this one), so this is the skill's own reference
// instance rather than a swapped-in brand palette — the right default until
// one exists.
var sourceSwatches = []sourceSwatch{
	{RGB{0x2a, 0x78, 0xd6}, '█'}, // slot 1 blue
	{RGB{0xeb, 0x68, 0x34}, '▓'}, // slot 2 orange
	{RGB{0x1b, 0xaf, 0x7a}, '▒'}, // slot 3 aqua
	{RGB{0xed, 0xa1, 0x00}, '░'}, // slot 4 yellow
	{RGB{0xe8, 0x7b, 0xa4}, '▚'}, // slot 5 magenta
	{RGB{0x00, 0x83, 0x00}, '▞'}, // slot 6 green
	{RGB{0x4a, 0x3a, 0xa7}, '▤'}, // slot 7 violet
	{RGB{0xe3, 0x49, 0x48}, '▥'}, // slot 8 red
}

// remainderSwatch is the neutral, muted color+glyph for the synthetic
// remainder row — the same "muted ink" token the palette reserves for
// axis/label text, deliberately outside the eight categorical slots so a
// remainder bucket never reads as a ninth named combination.
var remainderSwatch = sourceSwatch{RGB{0x89, 0x87, 0x81}, '·'}

// swatchFor returns segments[i]'s paint+glyph. i indexes directly into
// sourceSwatches with no wraparound: capSourceSegments guarantees at most
// maxSourceSegments segments, of which at most maxSourceSegments-1 are real
// (the last is the remainder when present), and maxSourceSegments equals
// len(sourceSwatches), so i never runs past the palette's own length for a
// real segment.
func swatchFor(i int, s sourceSegment) sourceSwatch {
	if s.remainderCombos > 0 {
		return remainderSwatch
	}
	return sourceSwatches[i]
}

// allocateSourceCells splits width cells among segments in proportion to
// their hours, using largest-remainder rounding so the cells sum to EXACTLY
// width — the same reconciliation discipline as the legend's hours, applied
// to the bar's own pixels: a bar that is short by a cell because of
// independent per-segment rounding is a small instance of the "two figures
// nobody subtracted" failure mode.
func allocateSourceCells(segments []sourceSegment, total time.Duration, width int) []int {
	cells := make([]int, len(segments))
	if total <= 0 || width <= 0 {
		return cells
	}
	type remainder struct {
		index int
		frac  float64
	}
	fracs := make([]remainder, len(segments))
	assigned := 0
	for i, s := range segments {
		share := float64(s.hours) / float64(total) * float64(width)
		whole := int(share)
		cells[i] = whole
		fracs[i] = remainder{index: i, frac: share - float64(whole)}
		assigned += whole
	}
	sort.SliceStable(fracs, func(i, j int) bool { return fracs[i].frac > fracs[j].frac })
	for i := 0; i < width-assigned && i < len(fracs); i++ {
		cells[fracs[i].index]++
	}
	return cells
}

// writeSourceBar draws the one-line stacked bar.
func writeSourceBar(out io.Writer, cc colorCapability, segments []sourceSegment, total time.Duration) error {
	cells := allocateSourceCells(segments, total, sourceBarWidth)
	var bar strings.Builder
	bar.WriteString("  ")
	for i, s := range segments {
		if cells[i] == 0 {
			continue
		}
		sw := swatchFor(i, s)
		bar.WriteString(cc.Paint(sw.rgb))
		bar.WriteString(strings.Repeat(string(sw.glyph), cells[i]))
		bar.WriteString(cc.Reset())
	}
	bar.WriteByte('\n')
	_, err := fmt.Fprint(out, bar.String())
	return err
}

// writeSourceLegend prints one line per segment: the combination name, its
// hours, and its share of the period.
//
// # The margin decision
//
// A sitting's Margin (lead-in, tail-out, bridged silence — the part of
// Start..End no observation covers) is attributed to the sitting's OWN
// source combination in full: a segment's hours are the sitting's whole
// Duration(), not Duration()-Margin. This matches how metrics.go's own
// figures already treat margin — Report.Presence.Total and
// Project.Engaged.Total both sum whole sitting/piece durations with margin
// folded in, and margin is reported only as a top-level percentage
// alongside a total ("77.2% ... inferred by the clustering" in metrics.go's
// package doc), never split out of a bucket it belongs to. There is no other
// sensible attribution for a sitting's margin: it is the connective tissue
// of THAT cluster specifically (its own lead-in, tail-out, and the silences
// its own gap threshold bridged), not evidence of any other sitting's
// sources, so splitting it across neighbouring segments would invent an
// apportionment nobody measured — the same refusal
// internal/sessions/calendar.go makes for a sitting's margin split across a
// day boundary (SplitMargin, deliberately unapportioned).
func writeSourceLegend(out io.Writer, cc colorCapability, segments []sourceSegment, total time.Duration) error {
	for i, s := range segments {
		sw := swatchFor(i, s)
		swatch := cc.Paint(sw.rgb) + string(sw.glyph) + string(sw.glyph) + cc.Reset()
		pct := 100 * float64(s.hours) / float64(total)
		if _, err := fmt.Fprintf(out, "  %s %-40s %8s %6.1f%%\n",
			swatch, s.name(), metricsHours(s.hours), pct); err != nil {
			return err
		}
	}
	return nil
}
