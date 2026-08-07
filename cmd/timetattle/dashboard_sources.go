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
	segments, total, err := partitionSittingsByFamilies(data.Report.Sessions)
	if err != nil {
		return err
	}
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

// family is a coarse grouping of the granular source ids sessions.Session's
// own Sources field carries — see sourceFamily below for the mapping and why
// this layer exists at all. It is a display-only taxonomy: nothing outside
// this file (sessions.Session.Sources, metrics.Report, the store) knows
// families exist, exactly as nothing outside this file previously knew about
// "combinations" — the same section-local narrowing dashboard_sources.go's
// own package doc already describes.
type family string

// The seven families every mapped source id rolls up to, named exactly as
// job 57dP7 (the task this file's family rollup belongs to) specifies them.
const (
	familyApps    family = "Apps"
	familyBlender family = "Blender"
	familyClaude  family = "Claude"
	familyFiles   family = "Files"
	familyGit     family = "Git"
	familyJobs    family = "Jobs"
	familyMotion  family = "Motion"
)

// sourceFamily maps one granular source id to the family its evidence rolls
// up to in this section's legend. ok is false for any id this table does not
// recognise.
//
// # Why an id ever finds nothing here
//
// A source id reaching this function with ok==false means a collector or
// built-in manifest this file's author had never heard of is now writing
// events — the exact "a new source silently falls out of the chart" failure
// AGENTS.md names as this repo's own recurring sin (three leaks, in three
// separate tools, from treating "not on the list" as "nothing to report").
// partitionSittingsByFamilies (below) therefore treats ok==false as fatal to
// the whole render rather than inventing an "Unmapped: <id>" catch-all
// family that would let the section keep drawing.
//
// That is a deliberate departure from the REMAINDER row's own philosophy —
// capSourceSegments folds excess combinations into a labeled row precisely
// so the cap is never silent, and an "Unmapped: <id>" family would look like
// the same move. It is not the same move: capSourceSegments' remainder is
// still an exact, reconciled account of hours the code understands. A
// segment for an id with no family is not — nobody has decided what that id
// even IS yet (which existing family it belongs to, or whether it needs a
// new one), so drawing it as if that were merely a cosmetic detail asserts a
// grouping decision nobody made. dashboard.go's own renderDashboard doc
// comment already settles the wider question this reduces to: "a section's
// error is fatal to the whole run rather than skipped over ... a section
// that failed partway through is not a section a reader can trust the rest
// of" — SOURCE ATTRIBUTION is not a special case of that rule, it is a plain
// instance of it. A CLI operator sees a named error identifying exactly
// which id to add a mapping for (and where); an agent or script consuming
// dashboard output sees a non-zero exit rather than a chart it could
// silently trust. See dashboard_sources_test.go's
// TestSourceAttributionFailsRenderOnAnUnmappedSource for the failure this
// guarantees, and TestSourceFamilyMapsEveryKnownSourceID for the completeness
// check every real collector and built-in manifest id is run through so this
// case is rare in practice.
//
// # Why prefix rules for three families and a literal table for the rest
//
// gitlog-, job- and knowledgec- each name a single collector that emits more
// than one stream under a shared namespace (internal/gitlog's CommitsID and
// ReflogID; internal/jobs' EventsID and ClaimsID; internal/knowledgec's
// four reader ids) — a prefix rule covers a whole collector's present AND
// future streams in one line, and is exactly as safe as the literal
// alternative since every one of those ids is already required to start with
// its collector's own namespace. The remaining five families each cover
// exactly one single-stream source (claude-transcripts, biome-appusage,
// fs-observations, motion-autosave, blender-backups), where a prefix rule
// would just be a more roundabout way of writing the same one-to-one entry a
// literal case already is.
func sourceFamily(id string) (family, bool) {
	switch {
	case strings.HasPrefix(id, "gitlog-"):
		return familyGit, true
	case strings.HasPrefix(id, "job-"):
		return familyJobs, true
	case strings.HasPrefix(id, "knowledgec-"):
		return familyApps, true
	}
	switch id {
	case "claude-transcripts":
		return familyClaude, true
	case "biome-appusage":
		return familyApps, true
	case "fs-observations":
		return familyFiles, true
	case "motion-autosave":
		return familyMotion, true
	// "vault-autosave" is NOT a real collector id — no built-in manifest or
	// Go package in this repo defines it (internal/manifest/builtin/motion.json
	// defines "motion-autosave"). internal/storefixture.VaultSource uses this
	// string as its stand-in for "autosaves written by a timer nobody
	// touches," the same concept motion-autosave names for real, and its own
	// package doc claims the corpus "carr[ies] the real IDs rather than
	// invented ones because the ID is not a name, it is a ROLE" — a claim
	// this one constant does not keep. Mapped here, to Motion, so this
	// section's storefixture-corpus reconciliation test (which this task
	// does not own the fixture for) does not fail on a mismatch that belongs
	// to internal/storefixture/storefixture.go, not to this mapping. Flagged
	// for the integrator: either storefixture.VaultSource should read
	// "motion-autosave" to match the real id, or this repo genuinely has two
	// distinct Motion-shaped ids and both belong here permanently.
	case "vault-autosave":
		return familyMotion, true
	case "blender-backups":
		return familyBlender, true
	default:
		return "", false
	}
}

// sourceSegment is one family combination's share of the period, or (when
// remainderCombos > 0) the synthetic remainder row capSourceSegments folds
// the lowest-ranked combinations into.
type sourceSegment struct {
	// families is the sorted, deduplicated set of families evidencing this
	// segment's sittings. Sorting is lexical, which is also alphabetical
	// order for every family name this file defines — the ordering the
	// legend line format requires — so no second sort is needed at display
	// time.
	families []string
	hours    time.Duration
	count    int // sittings contributing to this segment
	// remainderCombos is >0 only for the synthetic remainder row; it says how
	// MANY distinct combinations were folded in, so a cap is never silent —
	// AGENTS.md's rule against capping without saying what was aggregated.
	remainderCombos int
}

// name is the label this segment prints — single-family and multi-family
// combinations are both first-class and named for what they are, never
// collapsed into a generic "multiple" bucket (task UgTU2 criterion pl8,
// carried forward from source-level to family-level combinations by job
// 57dP7).
func (s sourceSegment) name() string {
	switch {
	case s.remainderCombos > 0:
		return fmt.Sprintf("%d more combinations (%s)", s.remainderCombos, plural(s.count, "sitting"))
	case len(s.families) == 0:
		// Unreached on data sessions.Cluster produced: a Session is only
		// emitted when at least one observation evidenced it (Events > 0),
		// and every such observation's source is appended to Sources before
		// the Session is appended to Result.Sessions — see sessions.go. Kept
		// so a hand-built or future fixture with a genuine gap fails loudly
		// rather than panicking on an empty join.
		return "(no evidence recorded)"
	default:
		return strings.Join(s.families, ", ")
	}
}

// partitionSittingsByFamilies groups sits by their family combination — the
// deduplicated set of families sourceFamily maps each sitting's own Sources
// to — and sums each group's full sitting duration (Start..End, margin
// included — see the margin doc comment above writeSourceLegend). total is
// the sum across every group, which by construction equals the sum of every
// sitting's own Duration(): this function partitions sits without dropping
// or double-counting any of them, so total always equals
// metrics.Report.Presence.Total for the same Sessions slice (Presence.Total
// is itself built by summing exactly these spans — see
// internal/metrics/metrics.go's Compute). This is the same reconciliation
// guarantee partitionSittingsBySources (the granular predecessor this
// function replaces) held; rolling up to families changes how many rows are
// drawn, never how many hours are accounted for.
//
// err is non-nil, and every return value is discarded by the caller, the
// moment any sitting carries a source id sourceFamily does not recognise —
// see sourceFamily's own doc comment for why an unmapped id is fatal here
// rather than folded into a catch-all family.
func partitionSittingsByFamilies(sits []sessions.Session) ([]sourceSegment, time.Duration, error) {
	byKey := map[string]*sourceSegment{}
	var order []string
	var total time.Duration
	for _, s := range sits {
		d := s.Duration()
		total += d

		famSet := map[family]bool{}
		for _, src := range s.Sources {
			fam, ok := sourceFamily(src)
			if !ok {
				return nil, 0, fmt.Errorf(
					"source id %q has no family mapping — add it to sourceFamily in dashboard_sources.go",
					src)
			}
			famSet[fam] = true
		}
		families := make([]string, 0, len(famSet))
		for fam := range famSet {
			families = append(families, string(fam))
		}
		sort.Strings(families)

		key := strings.Join(families, "\x00")
		seg, ok := byKey[key]
		if !ok {
			seg = &sourceSegment{families: families}
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
	return segments, total, nil
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

// writeSourceLegend prints one line per segment: its hours, its share of the
// period in parentheses, and the family combination name — hours first, per
// job 57dP7's own example line ("116.1h (25.3%): Apps, Claude, Git, Jobs"),
// which supersedes this section's earlier name-first, percent-last column
// layout. There is no fixed-width column for the family list any more (the
// old %-40s padding assumed a bounded set of literal source-id combinations;
// a family list's length varies with how many of the seven families a
// segment names), so alignment across lines is no longer guaranteed —
// legibility here comes from the swatch and the leading hours figure instead.
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
		if _, err := fmt.Fprintf(out, "  %s %s (%.1f%%): %s\n",
			swatch, metricsHours(s.hours), pct, s.name()); err != nil {
			return err
		}
	}
	return nil
}
