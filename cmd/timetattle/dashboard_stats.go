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
// # Three states, and this panel prints all three
//
// Design principle 3 of project/2026-08-07-cli-overhaul-plan.md says "no data"
// is never "zero work". Applied to a table of counts that splits into three
// renderings, not two, and the third is the one a reader would otherwise
// supply for themselves out of a blank:
//
//	0            OBSERVED ZERO. The window was observed and this source
//	             reported nothing in it. A measurement.
//	unrecorded   NOTHING WAS OBSERVED of this window, so a zero would be an
//	             assertion nobody made. The row says which window, or which
//	             source, was never read.
//	not measured NOTHING ANYWHERE IN THE STORE could answer this, at any
//	             scope or period, because no source records the quantity. It
//	             is a fact about the data model and never about the period.
//
// The last is printed rather than omitted, and that is deliberate: a row that
// silently vanished is precisely the absence this project keeps misreading as
// an observation. Each one names WHY, so a reader can tell a gap that a
// harvest would fill from one that only new collection code ever could.
//
// # What is not reachable, and the evidence for each
//
// The stat list this panel was specified from is a wishlist, and three of its
// entries cannot be answered honestly from what the store holds. Each is
// printed as "not measured" with its reason, and none is approximated:
//
//   - DISTINCT JOBS AGENTS. internal/jobs never SELECTs the actor column at
//     all, by an explicit decision recorded in its own package doc: agent
//     work counts fully, the actor titles are disposable, and source.Event
//     has no free-form payload internal/store would persist it in anyway.
//     There is no column to count.
//
//   - CLAUDE SESSIONS. internal/transcripts emits one Point per transcript
//     RECORD, with Identity.Document set to the record's uuid and Locator set
//     to the record's own cwd — deliberately not the walked path, so the
//     session file's name (which is the session id) never reaches the store.
//     Counting distinct sittings from internal/sessions instead would answer
//     a different question with the same word: a timetattle sitting is
//     clustered across every source at once and is not a Claude session.
//
//   - TOKENS AND COST. The transcript record struct (internal/transcripts,
//     event.go) parses four fields — type, timestamp, cwd, uuid — plus the
//     message content it needs to tell a human turn from a tool result.
//     Usage figures are never read, and there is nowhere to put them: the
//     only scalar an event carries is State, which is part of the store's
//     event_identity key, so a per-record number there would make every
//     record its own identity.
//
// What IS recorded, and is therefore live: internal/gitlog puts a diff-stat
// on every non-merge commit event's State (gitlog.Stat / ParseStat), so lines
// inserted and removed are real figures rather than an estimate.
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
		fmt.Fprintf(out, "  %s\n", "a collection run in this window lost or partly lost what it read, "+
			"so every count above is a lower bound")
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
	merges     int
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
			// is counted separately and never summed in as a zero.
			if s, ok := gitlog.ParseStat(e.State); ok {
				t.insertions += s.Insertions
				t.deletions += s.Deletions
			} else {
				t.merges++
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
// is what keeps the three states from ever collapsing into each other.
type statRow struct {
	Label string
	// Value and Avg are the rendered figures, used when Marker is empty.
	Value, Avg string
	// Marker is "unrecorded" or "not measured" — a state rather than a
	// figure.
	Marker string
	// Note is the reason, or a qualification on a figure that is real.
	Note string
	// Emphasis marks a row worth the eye. Color is decoration only: every
	// row reads identically under colorNone, which is what a captured run
	// and every test gets.
	Emphasis bool
}

// Markers. Two words, each meaning one thing, and neither ever standing in
// for the other. See this file's own doc comment for the three-state table.
const (
	statUnrecorded  = "unrecorded"
	statNotMeasured = "not measured"
)

// statRows lays the panel out. The order is hours, then git, then files, then
// jobs, then Claude — the coarsest figure first, and each source's rows
// adjacent so a reader comparing two of them is not scanning across the panel.
func statRows(scope metricsScope, observed, total int,
	t statTally, reach map[string]bool, data *dashboardData) []statRow {
	rows := []statRow{{
		Label: "observed days",
		Value: fmt.Sprintf("%d of %d", observed, total),
		Note:  "the divisor for every average below",
	}}
	rows = append(rows, statsHourRows(scope, observed, data)...)

	count := func(label string, n int, sourceID, note string) statRow {
		return statCountRow(label, n, observed, statsObserved(sourceID, observed, reach), note)
	}
	mergeNote := ""
	if t.merges > 0 {
		mergeNote = fmt.Sprintf("%s carried no diff and is summed into neither line count",
			plural(t.merges, "merge commit"))
	}
	rows = append(rows,
		count("commits", t.commits, gitlog.CommitsID, mergeNote),
		count("lines +", t.insertions, gitlog.CommitsID, ""),
		count("lines -", t.deletions, gitlog.CommitsID, ""),
		count("file writes", t.fileWrites, fsobserve.ID, "a birth and an mtime are two writes of one file"),
		count("files touched", len(t.files), fsobserve.ID, ""),
		count("job actions", t.jobEvents, jobs.EventsID, "planning and coordination are work and are counted"),
		count("tasks closed", len(t.closed), jobs.EventsID, ""),
		count("claude records", t.records, transcripts.ID, ""),
		count("claude prompts", t.prompts, transcripts.ID, "records a person typed"),
	)

	// The three the data model does not record. Printed, never omitted, and
	// never as a figure — see this file's doc comment for the evidence behind
	// each reason.
	rows = append(rows,
		statRow{Label: "jobs agents", Marker: statNotMeasured,
			Note: "internal/jobs never reads the actor column, and an event has nowhere to carry it"},
		statRow{Label: "claude sessions", Marker: statNotMeasured,
			Note: "a transcript event carries the record's uuid and cwd, never the session's own id"},
		statRow{Label: "tokens, cost", Marker: statNotMeasured,
			Note: "a transcript record is read for type, timestamp, cwd and uuid; no usage figure is parsed"},
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
		presenceRow.Note = "no day in this window was observed"
	default:
		presenceRow.Value = fmt.Sprintf("%.1fh", presence.Hours())
		presenceRow.Avg = statsAvgHours(presence, observed)
	}

	// The backlight is the literal screen-on reading, and internal/metrics
	// reports it store-wide for a reason no section can work around:
	// knowledgeC and Biome carry no path at all, so their evidence is
	// metrics.ReasonUnlocatable and can never be attributed to a project.
	// Printing the store-wide figure under a project filter would attribute
	// it; printing nothing would read as zero. So the row prints the figure
	// and says what it is a figure ABOUT.
	b := data.Report.Backlight
	screen := statRow{Label: "screen-on"}
	switch {
	case b.Readings == 0:
		// Zero lit hours out of zero readings is not an observation that the
		// screen was off. internal/metrics keeps that third state itself, in
		// Backlight.Unwitnessed beside Lit, for the same reason.
		screen.Marker = statUnrecorded
		screen.Note = "no backlight reading covers this window"
	default:
		screen.Value = fmt.Sprintf("%.1fh", b.Lit.Hours())
		screen.Avg = statsAvgHours(b.Lit, observed)
		screen.Note = "store-wide: the presence sources carry no path and attribute to no project"
		if !scope.filtering() {
			screen.Note = "the backlight's own lit hours, wherever they fall"
		}
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
// An unrecorded row carries the marker and NOT a sentence, except in the one
// case a reader could not otherwise diagnose: the window was observed and this
// particular source was not. Where nothing at all was observed every row would
// carry the same sentence, and nine copies of one finding is the prose this
// command's terseness pass exists to keep out — renderComponentStats states it
// once, beneath the table.
func statCountRow(label string, n, observed int, sourceObserved bool, note string) statRow {
	if n == 0 && !sourceObserved {
		row := statRow{Label: label, Marker: statUnrecorded}
		if observed > 0 {
			row.Note = "this source has never been harvested through this window"
		}
		return row
	}
	row := statRow{Label: label, Value: fmt.Sprintf("%d", n), Note: note}
	if observed > 0 {
		row.Avg = fmt.Sprintf("%.2f/day", float64(n)/float64(observed))
	}
	return row
}

// Column widths. Fixed rather than measured, because the labels and the
// magnitudes here are both known: a panel whose columns moved between two runs
// of the same command could not be diffed.
const (
	statLabelWidth = 16
	statValueWidth = 9
	statAvgWidth   = 11
)

// statMarkerRGB paints a state rather than a number, and only ever as
// emphasis: every marker is a WORD, so the panel says exactly the same thing
// with the color stripped. That is the rule dashcolor.go states — a capture is
// not a terminal — applied to the one part of this panel where a reader might
// otherwise be tempted to encode meaning in a hue.
var (
	statMarkerRGB = RGB{R: 0xb0, G: 0x80, B: 0x20}
	statValueRGB  = RGB{R: 0x60, G: 0xc0, B: 0xa0}
)

func writeStatRows(out io.Writer, cc colorCapability, rows []statRow) {
	for _, r := range rows {
		fmt.Fprintf(out, "  %s", pad(r.Label, statLabelWidth))
		if r.Marker != "" {
			fmt.Fprintf(out, "%s%s%s", cc.Paint(statMarkerRGB),
				pad(r.Marker, statValueWidth+statAvgWidth), cc.Reset())
		} else {
			value := padLeft(r.Value, statValueWidth)
			if r.Emphasis {
				value = cc.Paint(statValueRGB) + value + cc.Reset()
			}
			fmt.Fprintf(out, "%s%s", value, padLeft(r.Avg, statAvgWidth))
		}
		if r.Note != "" {
			fmt.Fprintf(out, "  %s", r.Note)
		}
		fmt.Fprintln(out)
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
