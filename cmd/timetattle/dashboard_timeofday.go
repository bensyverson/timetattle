package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
)

// hoursPerDay is the strip's width: one column per LOCAL clock hour, 00
// through 23.
const hoursPerDay = 24

// renderTimeOfDay draws a 24-column horizontal strip of when work typically
// happened across the period: one column per local clock hour, intensity
// showing that hour's share of the period's total sitting minutes. It reads
// data.Report.Sessions — the SAME clustering metrics.Compute already ran
// (dashboardData's own doc comment in dashboard.go) — and never re-clusters,
// so the wall-clock non-double-counting internal/sessions guarantees
// (package doc: "Sittings are global") is inherited rather than redone.
//
// # Timezone: local, deliberately
//
// The store keeps every instant in UTC (store.go's TimeLayout scans
// at.UTC()), and every other human-facing report in this package converts to
// local before showing a person a clock time: dashboard.go's own
// period.From.Local() two lines into renderDashboard, orient.go, status.go,
// filedays.go. "When do I typically work" is exactly that kind of question —
// a midnight-UTC sitting is a 5pm sitting to somebody in US/Pacific, and a
// strip bucketed in UTC would show that person's evening as somebody else's
// small hours. This section buckets by cal.Location, the CONFIGURED
// calendar's own zone (config.Calendar.Timezone, defaulting to the machine's
// own zone — see internal/config/calendar.go's Sessions method), rather than
// a second, independent call to time.Local, so the hour-of-day reading uses
// the same zone the period itself was tiled with.
//
// # Unobserved hours: what data.Log can and cannot say
//
// See observedDayCount's own doc comment for the full argument; in short,
// collectionLog only resolves to day granularity (a capture or harvest run's
// own timestamp, not a per-hour signal), so there is no way, with what
// data.Log exposes, to tell hour 3 of an observed day from hour 19 of the
// SAME day — every hour-of-day column is weighted by the same,
// once-computed observed-day count, not a per-column one. That is a
// disclosed simplification, not an oversight: the alternative (querying
// data.Log at hourly granularity) would answer a different and wrong
// question, since a single batch harvest run ingests a whole day's evidence
// at one instant and its timestamp says nothing about which hours of that
// day it covered. A column with real sitting minutes always renders from
// that evidence, whatever data.Log says (real minutes are not in question).
// A column with NO sitting minutes renders UNOBSERVED when no day in the
// period was collection-logged at all, and OBSERVED IDLE otherwise.
func renderTimeOfDay(_ context.Context, out io.Writer, _ metricsScope, period dashboardPeriod,
	cc colorCapability, data *dashboardData) error {
	cal, err := data.Config.Calendar.Sessions()
	if err != nil {
		return err
	}

	var minutes [hoursPerDay]float64
	var total float64
	for _, s := range data.Report.Sessions {
		start, end := s.Start, s.End
		if start.Before(period.From) {
			start = period.From
		}
		if end.After(period.To) {
			end = period.To
		}
		if !start.Before(end) {
			continue
		}
		for h, d := range hourBuckets(start, end, cal.Location) {
			m := d.Minutes()
			minutes[h] += m
			total += m
		}
	}

	// Render RELATIVE to the busiest hour rather than to the period's total,
	// so the strip's shape stays legible whatever the period's length — a
	// year's busiest hour rarely holds more than a small fraction of the
	// whole year's minutes, and scaling against the total alone would render
	// every column near-empty.
	maxShare := 0.0
	if total > 0 {
		for _, m := range minutes {
			if share := m / total; share > maxShare {
				maxShare = share
			}
		}
	}

	observedDays, totalDays := observedDayCount(period, cal, data.Log)
	anyObserved := observedDays > 0

	if _, err := fmt.Fprint(out, "  "); err != nil {
		return err
	}
	for h := range hoursPerDay {
		share := 0.0
		if total > 0 {
			share = minutes[h] / total
		}
		rel := 0.0
		if maxShare > 0 {
			rel = share / maxShare
		}
		if _, err := fmt.Fprint(out, cellGlyph(minutes[h] > 0, rel, anyObserved, cc)); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(out); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "  %s\n", hourLabels()); err != nil {
		return err
	}
	if totalDays > 0 && observedDays < totalDays {
		if _, err := fmt.Fprintf(out, "  hours weighted over %s (of %d in period)\n",
			plural(observedDays, "observed day"), totalDays); err != nil {
			return err
		}
	}
	return nil
}

// hourBuckets partitions [start, end) into the 24 local clock hours it
// touches, in loc's wall clock — bucket[h] is how much of the span fell in
// local hour h. THE INVARIANT THIS EXISTS TO PROVE: the buckets always sum
// to exactly end.Sub(start), whatever start and end are, and however many
// local calendar days the span crosses — see
// TestHourBucketsSumInvariantHoldsForEverySpanShape.
//
// Advancing by "duration remaining in the current wall-clock hour" rather
// than by constructing the next hour boundary with time.Date sidesteps a
// daylight-saving ambiguity (an hour that repeats, or one that never
// happens) ever producing a boundary that fails to be strictly after cur:
// the remaining duration is always positive, so cur always advances, and the
// loop always terminates.
func hourBuckets(start, end time.Time, loc *time.Location) [hoursPerDay]time.Duration {
	var buckets [hoursPerDay]time.Duration
	for cur := start; cur.Before(end); {
		lt := cur.In(loc)
		elapsedInHour := time.Duration(lt.Minute())*time.Minute +
			time.Duration(lt.Second())*time.Second +
			time.Duration(lt.Nanosecond())
		next := cur.Add(time.Hour - elapsedInHour)
		if next.After(end) {
			next = end
		}
		buckets[lt.Hour()] += next.Sub(cur)
		cur = next
	}
	return buckets
}

// observedDayCount is how many of the period's calendar days data.Log says
// collection was running during, by cal's own day tiling — the SAME
// day-level collectionLog.during check the activity heatmap's own cells use
// (dashboardData.Log's doc comment in dashboard.go). A day counts as
// observed whether or not any run inside it lost data; collectionLost still
// means collection was running, which is the only question asked here.
func observedDayCount(period dashboardPeriod, cal sessions.Calendar, log collectionLog) (observed, total int) {
	if !period.From.Before(period.To) {
		return 0, 0
	}
	// The end is exclusive and a day's bounds are half-open (series.go's
	// seriesOf does the identical thing for the identical reason), so the
	// last instant that must be covered is the one before period.To.
	for _, d := range cal.Days(period.From, period.To.Add(-time.Nanosecond)) {
		total++
		start, end := cal.Bounds(d)
		if log.during(start, end) != collectionUnrecorded {
			observed++
		}
	}
	return observed, total
}

// cellGlyph is one column of the strip. worked is whether this hour has any
// real sitting minutes at all (real evidence always wins, whatever data.Log
// says); rel is this hour's share of the period's total minutes, relative to
// the busiest hour (0 for none, 1 for the busiest); anyObserved is whether
// any day in the period was collection-logged.
func cellGlyph(worked bool, rel float64, anyObserved bool, cc colorCapability) string {
	if !worked && !anyObserved {
		return unobservedGlyph(cc)
	}
	return intensityGlyph(rel, cc)
}

// asciiRamp is the colorNone intensity scale, lightest to heaviest. Index 0
// is a bare space — an observed, genuinely idle hour renders as an absence
// of mark, distinct from the unobserved glyph, which is always present.
const asciiRamp = " .:-=+*#%@"

// intensityGlyph paints one ramp step of rel (0-1, already relative to the
// busiest hour): the ASCII ramp under colorNone, a solid block shaded along
// a fixed low-to-high ramp under 256-color or truecolor.
func intensityGlyph(rel float64, cc colorCapability) string {
	if cc == colorNone {
		return string(asciiRamp[rampIndex(rel)])
	}
	return cc.Paint(rampColor(rel)) + "█" + cc.Reset()
}

// rampIndex maps rel (0-1) onto asciiRamp's steps, rounding to the nearest
// rather than flooring, so a share sitting just above a step's midpoint
// reads as that step and not the one below it.
func rampIndex(rel float64) int {
	if rel < 0 {
		rel = 0
	}
	if rel > 1 {
		rel = 1
	}
	n := len(asciiRamp) - 1
	idx := min(int(rel*float64(n)+0.5), n)
	return idx
}

// rampLow and rampHigh are the color-capable ramp's endpoints: a dim slate at
// rel=0 to a vivid amber at rel=1.
var (
	rampLow  = RGB{R: 35, G: 35, B: 55}
	rampHigh = RGB{R: 255, G: 195, B: 60}
)

// rampColor linearly interpolates between rampLow and rampHigh by rel (0-1).
func rampColor(rel float64) RGB {
	if rel < 0 {
		rel = 0
	}
	if rel > 1 {
		rel = 1
	}
	lerp := func(a, b uint8) uint8 {
		return uint8(float64(a) + rel*(float64(b)-float64(a)))
	}
	return RGB{R: lerp(rampLow.R, rampHigh.R), G: lerp(rampLow.G, rampHigh.G), B: lerp(rampLow.B, rampHigh.B)}
}

// unobservedGlyphRune is the one character that means "data.Log says nothing
// was ever collecting here" — always '?', in every color capability, so the
// glyph itself (not merely its color) is what marks it, and the distinction
// survives a colorNone capture same as an escape-coded one.
const unobservedGlyphRune = '?'

// unobservedGlyphColor is a neutral, unalarming gray — this is a data gap,
// not a warning.
var unobservedGlyphColor = RGB{R: 90, G: 90, B: 90}

func unobservedGlyph(cc colorCapability) string {
	if cc == colorNone {
		return string(unobservedGlyphRune)
	}
	return cc.Paint(unobservedGlyphColor) + string(unobservedGlyphRune) + cc.Reset()
}

// hourLabels is the 24-wide legend beneath the strip, marking every sixth
// hour so a reader can find any column without counting from the edge.
func hourLabels() string {
	labels := []byte("                        ") // hoursPerDay spaces
	for _, mark := range []struct {
		at   int
		text string
	}{{0, "00"}, {6, "06"}, {12, "12"}, {18, "18"}} {
		copy(labels[mark.at:], mark.text)
	}
	return string(labels)
}
