package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/jobs"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/transcripts"
)

// The component stats panel: per-source totals for the scoped period, each
// with a per-day average.
//
// THIS FILE, AND dashboard_stats_test.go, ARE THE ONLY FILES THIS TASK EDITS.
// See the section list in dashboard.go for the contract every render function
// shares.
//
// # Two states, printed as a label plus a count and an average — nothing else
//
// Design principle 3 of project/2026-08-07-cli-overhaul-plan.md says "no data"
// is never "zero work". Applied to a table of counts, that is a distinction
// this panel draws in exactly one place — the value column of a row — and
// nowhere else:
//
//	0            OBSERVED ZERO. The window was observed and this source
//	             reported nothing in it. A measurement.
//	unrecorded   NOTHING WAS OBSERVED of this window, so a zero would be an
//	             assertion nobody made. The row carries the marker and no
//	             figure.
//
// Every row is label, count, average — a per-row commentary column that used
// to sit to the right of those three (a sentence explaining a figure, or
// standing in for one the store could not produce at all) is gone (job dGWPc,
// Ben's dashboard feedback items 5 and 6a). A reader compares a panel like
// this down a column, not across one row at a time, and a sentence attached
// to a single row is read exactly once and then never again — so the reasons
// that used to live there now live in docs/content/docs/dashboard.md, the
// published record for what this panel shows and does not, which the task
// that removed them (dGWPc) does not itself edit.
//
// # The rows dropped along with the column that explained them
//
// Three rows in the original stat list could not be answered honestly from
// what the store holds, and were printed as a third state, "not measured",
// with a sentence of reasoning in the now-removed column. With nowhere left
// to carry that sentence, the rows are omitted rather than printed empty —
// the same choice this file already makes for a source that was simply never
// wired up (see statCountRow). None are coming back the same way:
//
//   - DISTINCT JOBS AGENTS is not pending anything; Ben approved dropping it
//     for good. internal/jobs never SELECTs the actor column at all, by an
//     explicit decision recorded in its own package doc: agent work counts
//     fully, the actor titles are disposable, and source.Event has no
//     free-form payload internal/store would persist one in anyway. There is
//     no column to count, ever, under this schema.
//
//   - CLAUDE SESSIONS and TOKENS/COST are pending the source-schema work under
//     task 3jDIu, and return as real numeric rows when it lands. Today,
//     internal/transcripts emits one Point per transcript RECORD with
//     Identity.Document set to the record's uuid and Locator set to the
//     record's own cwd — never the session file's own name, which is the
//     session id — and reads only type, timestamp, cwd and uuid plus the
//     message content needed to tell a human turn from a tool result; no
//     usage figure is parsed and there is nowhere on the event to put one
//     (State is part of the store's event_identity key, so a per-record
//     number there would make every record its own identity).
//
// What IS recorded, and is therefore still a live row: internal/gitlog puts a
// diff-stat on every non-merge commit event's State (gitlog.Stat / ParseStat),
// so lines inserted and removed are real figures rather than an estimate.
//
// # Files created versus updated is one row, not two
//
// internal/fsobserve emits up to two Points per file — one for its birth and
// one for its mtime — and the two are identical in every stored field except
// the instant (same source, same Identity, same State, same Locator). A row
// read back cannot say which it was, and a file written once emits only one
// event because birth and mtime coincide. So this counts WRITES observed and
// distinct FILES touched, which are both answerable, rather than splitting a
// created/updated column the store cannot reconstruct.
func renderComponentStats(ctx context.Context, out io.Writer, scope metricsScope,
	period dashboardPeriod, cc colorCapability, data *dashboardData) error {
	cal, err := statsCalendar(data)
	if err != nil {
		return err
	}
	observed, total := observedDays(cal, period, data)
	tally := tallyStats(scope, data)
	reach := reachableSources(ctx, period, data)

	rows := statRows(scope, observed, total, tally, reach, data)
	writeStatRows(out, cc, rows)

	// The two things true of the whole table rather than of any one row, each
	// stated once. A finding repeated on nine rows is the prose the terseness
	// pass exists to keep out of terminal output.
	if observed == 0 {
		fmt.Fprintf(out, "  %s\n", "no day in this window was observed at all, so every row above is "+
			"unrecorded rather than zero")
	}
	if data.Log.during(period.From, period.To) == collectionLost {
		fmt.Fprintf(out, "  %s\n", "some sources lost part of what they read in this window; "+
			"counts are lower bounds")
	}
	return nil
}

// statsCalendar is the calendar the period was tiled by — cfg.Calendar's own,
// exactly as dashboardCmd built it — so a "day" in this panel's divisor is the
// same civil day the period's own edges were cut on. Deriving a second one
// here (midnight UTC, say, or a 24h step from period.From) would disagree with
// those edges across a daylight-saving transition and nowhere else, which is
// the worst possible place for two definitions of a day to differ.
func statsCalendar(data *dashboardData) (sessions.Calendar, error) {
	if data.Config == nil {
		return sessions.Calendar{}, fmt.Errorf("no configuration was loaded, so there is no calendar to " +
			"say where a working day begins, and a per-day average needs one")
	}
	return data.Config.Calendar.Sessions()
}

// observedDays counts the working days of the period the record says
// something about, and it is the divisor every per-day average here uses.
//
// # Why observed days and not the period's calendar days
//
// The default period is a trailing YEAR (dashboard.go's own -days default, for
// the heatmap's year grid). Dividing a count by 365 when the store only
// reaches back two months does not report a lower rate — it reports a rate
// that was never measured, and it understates by exactly the factor of how
// much of the window predates collection. The number a reader wants from this
// panel is "how much per day of the days we know about", and that is what the
// denominator has to be, or the figure means nothing.
//
// A day counts as observed if EITHER holds, and the union is the point:
//
//   - a capture or harvest run is recorded inside it (data.Log), which is
//     collection demonstrably running; or
//   - a retained observation anchors in it, which is collection demonstrably
//     having worked, whether or not a run was recorded.
//
// Neither alone is sufficient. A run with no events is a real observed-idle
// day and MUST be in the divisor, or idle days silently inflate every
// average. Evidence with no run is ordinary rather than exotic — harvest_run
// rows only exist from the day the store started keeping them, and a harvest
// on Thursday ingests Tuesday's evidence, which is why collectionLog
// documents itself as answering "was collection running" and never "is this
// period's evidence complete".
//
// A day that is neither is a day nothing was recorded about, and it is
// excluded rather than counted as a zero — the same distinction the rows
// themselves draw, applied to the denominator so the two cannot disagree.
func observedDays(cal sessions.Calendar, period dashboardPeriod, data *dashboardData) (observed, total int) {
	// The period is half-open [From, To); Days takes an inclusive span, so the
	// last instant that must be covered is the one before To.
	all := cal.Days(period.From, period.To.Add(-time.Nanosecond))
	if len(all) == 0 {
		return 0, 0
	}

	withEvidence := make(map[sessions.Day]bool, len(data.Events))
	for _, e := range data.Events {
		withEvidence[cal.DayOf(e.Anchor())] = true
	}
	for _, d := range all {
		start, end := cal.Bounds(d)
		if withEvidence[d] || data.Log.during(start, end) != collectionUnrecorded {
			observed++
		}
	}
	return observed, len(all)
}

// statTally is every count this panel draws, taken in one pass over the
// scoped events rather than one pass per row.
type statTally struct {
	commits    int
	insertions int
	deletions  int

	fileWrites int
	files      map[string]bool

	jobEvents int
	closed    map[string]bool

	records int
	prompts int
}

// tallyStats counts the scoped events.
//
// SCOPE SELECTS WHAT IS COUNTED, using metricsScope's own showsProject
// against the report's client roll-up — the same predicate metrics.go's report
// functions use, never a second filtering rule with its own drift. Note what
// this does NOT do: it does not recompute anything. metricsScope's own doc
// comment explains why the report is computed unfiltered and only the printed
// rows are narrowed, and the hours below obey that exactly. These counts are a
// different kind of figure — a count of observations, not a share of wall
// clock — so filtering the events IS the correct operation for them: no
// denominator shrinks, because a count has none.
func tallyStats(scope metricsScope, data *dashboardData) statTally {
	t := statTally{files: map[string]bool{}, closed: map[string]bool{}}
	for _, e := range data.Events {
		if !scope.showsProject(e.Identity.Project, data.Report.Clients) {
			continue
		}
		switch e.Source {
		case gitlog.CommitsID:
			t.commits++
			// ParseStat returns ok=false for a merge and for anything else
			// that is not a stat this package wrote. gitlog keeps "no diff was
			// computed" distinct from "a diff was computed and it was empty" —
			// an --allow-empty commit is the second, and is real — so a merge
			// is counted in "commits" above but never summed into the line
			// totals as a zero. (A per-row sentence explaining that used to
			// print here; the per-row commentary column it lived in is gone —
			// see this file's own doc comment.)
			if s, ok := gitlog.ParseStat(e.State); ok {
				t.insertions += s.Insertions
				t.deletions += s.Deletions
			}
		case fsobserve.ID:
			t.fileWrites++
			if e.Locator != "" {
				t.files[e.Locator] = true
			}
		case jobs.EventsID:
			t.jobEvents++
			if e.State == statsTaskDone && e.Identity.Document != "" {
				t.closed[e.Identity.Document] = true
			}
		case transcripts.ID:
			t.records++
			if e.State == transcripts.StateHuman {
				t.prompts++
			}
		}
	}
	return t
}

// statsTaskDone is the one job event type this panel interprets. internal/jobs
// deliberately whitelists no event type and carries whatever the database
// said, verbatim, because a reader that enumerated what it knew was wrong on
// the day it shipped — four live types nobody had listed. So the
// interpretation happens HERE, where it can be argued with, which is exactly
// what that package's doc asks a reporter to do. "done" and not the wider
// terminator set (done, released, canceled): a released or cancelled task was
// not closed, it was put down.
const statsTaskDone = "done"

// reachableSources is the per-source half of the unrecorded test: which
// sources the store has ever successfully been read through to an instant
// inside this window.
//
// data.Log answers the question STORE-WIDE — was anything collecting — and
// that is the gate the dashboard contract points at, correctly, because
// collection is a property of the store and not of a project. But it cannot
// say that ONE source was never wired up while the others harvested happily
// around it, and that source's zero is exactly as unmeasured as a zero in a
// window nothing ran in. Store.Freshness answers it per source, from the same
// harvest_run table, and it is careful about the same three states this panel
// is: a source ABSENT from the result has never been tried, one present with a
// zero LastHarvest was tried and never succeeded, and one present with a
// LastHarvest ran and found what it found.
//
// The result is nil when the store cannot be consulted — it is nil in a unit
// test, which constructs a dashboardData without one — and a nil map means
// this refinement is simply not applied, leaving the store-wide gate to
// decide. It never turns an unknown into a claim.
func reachableSources(ctx context.Context, period dashboardPeriod, data *dashboardData) map[string]bool {
	if data.Store == nil {
		return nil
	}
	fresh, err := data.Store.Freshness(ctx)
	if err != nil {
		// A freshness read that failed is not evidence about any source. The
		// panel falls back to the store-wide gate rather than reporting every
		// row as unrecorded on the strength of one failed query.
		return nil
	}
	reach := make(map[string]bool, len(fresh))
	for _, f := range fresh {
		reach[f.Source] = statsReachedWindow(f, period)
	}
	return reach
}

// statsReachedWindow reports whether f's source has been successfully read
// through to some instant inside the period. It is a WINDOW-level answer, not
// a per-day one: a source read through only the window's first day is
// reachable here, and the per-day picture is the activity heatmap's job, which
// is where day-by-day collection state belongs.
func statsReachedWindow(f store.Fresh, period dashboardPeriod) bool {
	return !f.LastHarvest.IsZero() && !f.LastHarvest.Before(period.From)
}

// statRow is one printed line. Exactly one of the value forms is used, which
// is what keeps the two states from ever collapsing into each other. There is
// no field for a note: every row is a label plus a count and an average, full
// stop — see this file's own doc comment for why the commentary column that
// used to sit here is gone.
type statRow struct {
	Label string
	// Value and Avg are the rendered figures, used when Marker is empty.
	Value, Avg string
	// Marker is "unrecorded" — a state rather than a figure.
	Marker string
	// Emphasis marks a row worth the eye. Color is decoration only: every
	// row reads identically under colorNone, which is what a captured run
	// and every test gets.
	Emphasis bool
}

// statUnrecorded is the one marker this panel prints now. A second marker,
// "not measured", existed here for a row whose reason lived in the per-row
// commentary column this task removes; with the column gone the three rows it
// explained are omitted entirely (see this file's own doc comment) rather
// than kept with no way to say why, so there is only ever one state to mark.
const statUnrecorded = "unrecorded"

// statRows lays the panel out. The order is hours, then git, then files, then
// jobs, then Claude — the coarsest figure first, and each source's rows
// adjacent so a reader comparing two of them is not scanning across the panel.
func statRows(scope metricsScope, observed, total int,
	t statTally, reach map[string]bool, data *dashboardData) []statRow {
	rows := []statRow{{
		Label: "observed days",
		Value: fmt.Sprintf("%d of %d", observed, total),
	}}
	rows = append(rows, statsHourRows(scope, observed, data)...)

	count := func(label string, n int, sourceID string) statRow {
		return statCountRow(label, n, observed, statsObserved(sourceID, observed, reach))
	}
	rows = append(rows,
		count("commits", t.commits, gitlog.CommitsID),
		count("lines +", t.insertions, gitlog.CommitsID),
		count("lines -", t.deletions, gitlog.CommitsID),
		count("file writes", t.fileWrites, fsobserve.ID),
		count("files touched", len(t.files), fsobserve.ID),
		count("job actions", t.jobEvents, jobs.EventsID),
		count("tasks closed", len(t.closed), jobs.EventsID),
		count("claude records", t.records, transcripts.ID),
		count("claude prompts", t.prompts, transcripts.ID),
	)
	return rows
}

// statsHourRows are the two duration rows, and both are REUSED from the report
// metrics already computed rather than re-derived from the events. Presence is
// wall clock across every source at once; a second summation here would be a
// second answer to the one question metrics exists to answer, and the two
// would drift.
func statsHourRows(scope metricsScope, observed int, data *dashboardData) []statRow {
	presence, known := statsPresence(scope, data)
	presenceRow := statRow{Label: "presence", Emphasis: true}
	switch {
	case !known && observed == 0:
		presenceRow.Marker = statUnrecorded
	default:
		presenceRow.Value = fmt.Sprintf("%.1fh", presence.Hours())
		presenceRow.Avg = statsAvgHours(presence, observed)
	}

	// The backlight is the literal screen-on reading, and internal/metrics
	// reports it store-wide for a reason no section can work around:
	// knowledgeC and Biome carry no path at all, so their evidence is
	// metrics.ReasonUnlocatable and can never be attributed to a project.
	// Printing the store-wide figure under a project filter would attribute
	// it; printing nothing would read as zero. This row used to say which of
	// the two it was doing, in the commentary column this task removes — see
	// this file's own doc comment, and job dGWPc's note, for the pushback:
	// unlike this panel's other removed notes, that one changed how the
	// figure should be read under a filtered scope, and nothing replaces it
	// here.
	b := data.Report.Backlight
	screen := statRow{Label: "screen-on"}
	switch {
	case b.Readings == 0:
		// Zero lit hours out of zero readings is not an observation that the
		// screen was off. internal/metrics keeps that third state itself, in
		// Backlight.Unwitnessed beside Lit, for the same reason.
		screen.Marker = statUnrecorded
	default:
		screen.Value = fmt.Sprintf("%.1fh", b.Lit.Hours())
		screen.Avg = statsAvgHours(b.Lit, observed)
	}
	return []statRow{presenceRow, screen}
}

// statsPresence is the scoped hours figure, taken out of the same report
// metrics itself would print. A filtered scope takes the SCOPED row —
// a project's or a client's Engaged span — and never the store-wide presence
// total, which would credit one project with every other project's hours.
//
// A roll-up is a union and never a sum (see metrics.Client's own doc), which
// is why a client filter reads the client's own row rather than adding its
// projects' rows together.
//
// known is false when the scope names something the report has no row for at
// all. That is not the same as zero hours, and the caller decides how to
// render it against what the window says was observed.
func statsPresence(scope metricsScope, data *dashboardData) (time.Duration, bool) {
	r := data.Report
	switch {
	case scope.Project != "":
		p, ok := r.Project(scope.Project)
		return p.Engaged.Total, ok
	case scope.Client != "":
		c, ok := r.Client(scope.Client)
		return c.Engaged.Total, ok
	default:
		return r.Presence.Total, r.Presence.Total > 0
	}
}

func statsAvgHours(d time.Duration, observed int) string {
	if observed == 0 {
		return ""
	}
	return fmt.Sprintf("%.2fh/day", d.Hours()/float64(observed))
}

// statsObserved reports whether a zero from this source would be a
// MEASUREMENT. Both gates have to pass: the window must have been observed at
// all, and — where the store could be asked — this particular source must
// have been read through into it.
func statsObserved(sourceID string, observed int, reach map[string]bool) bool {
	if observed == 0 {
		return false
	}
	if reach == nil {
		return true
	}
	return reach[sourceID]
}

// statCountRow renders one count, and the gate it applies is narrow on
// purpose: it only ever decides how a ZERO reads. A count that is not zero is
// printed whatever the collection log says, because the events are themselves
// proof the source was being read — suppressing a figure that exists would be
// the mirror image of the bug this panel is built around.
//
// An unrecorded row carries the marker and nothing else. It used to add a
// sentence in the one case a reader could not otherwise diagnose — the window
// was observed and this particular source was not — but that sentence lived
// in the per-row commentary column this task removes (see this file's own
// doc comment); the marker alone is now what every unrecorded row says, same
// as when nothing in the window at all was observed. renderComponentStats
// still states the whole-window case once, beneath the table.
func statCountRow(label string, n, observed int, sourceObserved bool) statRow {
	if n == 0 && !sourceObserved {
		return statRow{Label: label, Marker: statUnrecorded}
	}
	row := statRow{Label: label, Value: fmt.Sprintf("%d", n)}
	if observed > 0 {
		row.Avg = fmt.Sprintf("%.2f/day", float64(n)/float64(observed))
	}
	return row
}

// Column widths. The label column is fixed, because the label set is fixed —
// every run of this panel prints the same rows in the same order. The value
// and average columns are floors, not fixed widths: format.go's own package
// doc states the rule this panel violated on a live store ("A count clipped
// to two digits is a wrong figure rather than a short one, so every table
// below shrinks its label column and its padding before any figure is at
// risk") — a fixed 9/11 truncated "181 of 365" to "181 of 3…", the divisor's
// own value. statColumnWidths measures the actual rows below and widens past
// the floor rather than ever clipping a number; the floors just keep an
// ordinary, small-number run looking exactly as it always has.
const (
	statLabelWidth    = 16
	statMinValueWidth = 9
	statMinAvgWidth   = 11
)

// statColumnWidths measures the value and average columns from the rows
// actually being printed. A row's Marker (statUnrecorded) is excluded from
// the measurement — it is rendered across both columns plus their separator,
// per writeStatRows, and the floors already leave that combined span room for
// the word.
func statColumnWidths(rows []statRow) (value, avg int) {
	value, avg = statMinValueWidth, statMinAvgWidth
	for _, r := range rows {
		if r.Marker != "" {
			continue
		}
		if w := len([]rune(r.Value)); w > value {
			value = w
		}
		if w := len([]rune(r.Avg)); w > avg {
			avg = w
		}
	}
	return value, avg
}

// statMarkerRGB paints a state rather than a number, and only ever as
// emphasis: every marker is a WORD, so the panel says exactly the same thing
// with the color stripped. That is the rule dashcolor.go states — a capture is
// not a terminal — applied to the one part of this panel where a reader might
// otherwise be tempted to encode meaning in a hue.
var (
	statMarkerRGB = RGB{R: 0xb0, G: 0x80, B: 0x20}
	statValueRGB  = RGB{R: 0x60, G: 0xc0, B: 0xa0}
)

// writeStatRows builds each line in a buffer so the column padding can be
// trimmed back off. The padding has to be WRITTEN — it is what puts the next
// column in the same place on every row — but a line ENDING in it leaves
// trailing whitespace in every captured run, which a reader cannot see and a
// diff of two captures can, so it is applied and then cut. Every row ends in
// the average column now that no per-row note can follow it (that column
// used to sit here — see this file's own doc comment for why it is gone),
// but a row whose Avg is empty (no per-day figure applies) still pads out to
// the column's width, so the trim still has work to do on every run.
//
// Trimming the built line rather than tracking whether anything was painted
// is what keeps that correct under color: a painted value ends in an escape
// sequence, not a space, so a rule that stopped at the last non-space rune
// would have to know it had painted. This does not.
//
// The value and average columns are separated by an explicit space rather
// than relying on padLeft's own leading padding to provide the gap: a live
// run showed "lines + 2240021237.58/day", the count 224002 run directly into
// the average 1237.58/day, because the average happened to fill its column
// exactly and padLeft adds no padding when there is nothing left to add. Two
// adjacent right-aligned columns are only ever guaranteed a gap when one of
// them writes it.
func writeStatRows(out io.Writer, cc colorCapability, rows []statRow) {
	valueWidth, avgWidth := statColumnWidths(rows)
	for _, r := range rows {
		var line strings.Builder
		fmt.Fprintf(&line, "  %s", pad(r.Label, statLabelWidth))
		if r.Marker != "" {
			fmt.Fprintf(&line, "%s%s%s", cc.Paint(statMarkerRGB),
				pad(r.Marker, valueWidth+1+avgWidth), cc.Reset())
		} else {
			value := padLeft(r.Value, valueWidth)
			if r.Emphasis {
				value = cc.Paint(statValueRGB) + value + cc.Reset()
			}
			fmt.Fprintf(&line, "%s %s", value, padLeft(r.Avg, avgWidth))
		}
		fmt.Fprintln(out, strings.TrimRight(line.String(), " "))
	}
}

// padLeft is pad's right-aligning twin, for the two numeric columns: a total
// and a rate are read by comparing them down the column, and a ragged left
// edge on a column of numbers defeats exactly that. It is local to this file
// rather than promoted next to pad in format.go — the rule that package's own
// doc states is that a helper moves on its SECOND caller, and this is the
// first.
func padLeft(text string, width int) string {
	text = clip(text, width)
	n := width - len([]rune(text))
	if n <= 0 {
		return text
	}
	return strings.Repeat(" ", n) + text
}
