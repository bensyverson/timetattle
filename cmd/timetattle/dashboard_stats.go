package main

import (
	"context"
	"fmt"
	"io"
	"math"
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
//   - CLAUDE SESSIONS and the four token figures were pending the
//     source-schema work under task 3jDIu (job GcgVp) and are live rows now
//     that it has landed: internal/usage's payload rides outside
//     event_identity (see that package's own doc, and migration 0014), so
//     the trap that used to block a per-record figure — State participating
//     in identity — no longer applies. See statsUsageRows below for the five
//     rows this reads out of Store.Usage, never summed into one "tokens"
//     total (cache reads outweigh output tokens 223 to 1 on the corpus
//     GcgVp measured — scripts/sessioncensus, 2026-08-07).
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

	rows, err := statRows(ctx, scope, cal, observed, total, tally, reach, data)
	if err != nil {
		return err
	}
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
//
// The Claude group now ends in the five rows statsUsageRows reads out of
// Store.Usage rather than data.Events — the one query in this whole panel
// that reaches the store directly, for the reason reachableSources already
// does: Store.Usage's own Observations/Records-vs-Total pairs already ARE the
// three-state answer this file's own doc comment asks every row to give, and
// re-deriving it from data.Events would be a second, possibly disagreeing
// computation of the one thing Store.Usage exists to compute (the same
// argument statsHourRows' own comment makes for reusing Report rather than
// re-summing presence).
func statRows(ctx context.Context, scope metricsScope, cal sessions.Calendar, observed, total int,
	t statTally, reach map[string]bool, data *dashboardData) ([]statRow, error) {
	rows := []statRow{{
		Label: "observed days",
		Value: fmt.Sprintf("%d of %d", observed, total),
	}}
	rows = append(rows, statsHourRows(scope, observed, data)...)

	count := func(label string, n int, sourceID string) statRow {
		return statCountRow(label, n, observed, statsObserved(sourceID, observed, reach), data.Exact)
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

	usageRows, err := statsUsageRows(ctx, scope, cal, data)
	if err != nil {
		return nil, err
	}
	return append(rows, usageRows...), nil
}

// dashboardAttributedLabel is the word both the Component Stats hours row
// (statsHourRows below) and the dashboard header (dashboard.go's
// dashboardHeaderHours) use for the identical statsPresence figure —
// "attributed" under any scope, "presence" only when nothing narrows the
// report (-all, or an unfiltered run).
//
// Job MAABc: this row printed "presence" under every scope, which Ben caught
// as a mislabel — under a scope the figure is already a project's or a
// client's Engaged.Total (statsPresence's own doc comment below), not the
// store-wide presence total "presence" names everywhere else in this panel
// and in metrics.Report. "Attributed" is Ben's own word for it (see
// docs/content/docs/dashboard.md).
func dashboardAttributedLabel(scope metricsScope) string {
	if scope.filtering() {
		return "attributed"
	}
	return "presence"
}

// statsHourRows are the two duration rows, and both are REUSED from the report
// metrics already computed rather than re-derived from the events. Presence is
// wall clock across every source at once; a second summation here would be a
// second answer to the one question metrics exists to answer, and the two
// would drift.
func statsHourRows(scope metricsScope, observed int, data *dashboardData) []statRow {
	presence, known := statsPresence(scope, data)
	presenceRow := statRow{Label: dashboardAttributedLabel(scope), Emphasis: true}
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
	// the two it was doing, in the commentary column dGWPc removed — job
	// BwE29's pushback (c) flagged that the sentence's replacement, folding
	// the caveat into the LABEL, was still missing and assigned it here; see
	// statsScreenOnLabel below for where it now lives.
	b := data.Report.Backlight
	screen := statRow{Label: statsScreenOnLabel(scope)}
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

// statsScreenOnLabel folds job BwE29's pushback (c) into the row itself: the
// backlight figure is store-wide always, so a reader under -project or
// -client who sees a bare "screen-on" beside their own scope's other rows —
// commits, files, presence, all genuinely theirs — has no reason in the row
// to suspect this one is not. scope.filtering() covers both kinds of
// narrowing identically, because the figure is unattributable to either one
// identically (metrics.ReasonUnlocatable names no project AND no client).
//
// Under -all or an unfiltered scope there is nothing to disambiguate FROM —
// the figure is store-wide and so is the reader's own view — so the plain
// label stays, exactly as it always has (TestComponentStatsCallsScreenOnUnrecordedWithNoReadings
// and every other unscoped test in this file's own suite still reads
// "screen-on" verbatim).
func statsScreenOnLabel(scope metricsScope) string {
	if scope.filtering() {
		return "screen-on, machine-wide"
	}
	return "screen-on"
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
func statCountRow(label string, n, observed int, sourceObserved bool, exact bool) statRow {
	if n == 0 && !sourceObserved {
		return statRow{Label: label, Marker: statUnrecorded}
	}
	row := statRow{Label: label, Value: statHumanizeValue(int64(n), exact)}
	if observed > 0 {
		row.Avg = statHumanizeAvg(float64(n)/float64(observed), exact)
	}
	return row
}

// statHumanizeThreshold is the smallest count or per-day rate this panel
// abbreviates. Ben's original request (job yrAho) named 10,000; he revised it
// mid-task to 1,000, and this is that revised value.
//
// 1,000 is also exactly where the k suffix's own scale starts, so
// "humanize" and "abbreviate" collapse into the same decision rather than
// two thresholds that could drift apart — there is no band of four-digit
// numbers (1,000-9,999) printed in full before abbreviation kicks in, the
// gap a 10,000 threshold would have left. A reader never sees "9999" beside
// "10.0k" on adjacent runs of the same panel as the underlying count crosses
// a boundary that has nothing to do with where the suffix itself starts.
const statHumanizeThreshold = 1000

// statHumanizeValue renders n as today's plain integer when exact is
// requested or n is below the threshold — unchanged from before this task —
// and otherwise abbreviates it via humanizeMagnitude: one decimal digit and
// a k/M/B suffix, ROUNDED rather than truncated (128592 -> "128.6k", the
// SPEC's own pinned example; never "128.5k", which is what truncating the
// third significant digit would give).
func statHumanizeValue(n int64, exact bool) string {
	if exact || n < statHumanizeThreshold {
		return fmt.Sprintf("%d", n)
	}
	return humanizeMagnitude(float64(n))
}

// statHumanizeAvg is statHumanizeValue's twin for the per-day average
// column, keeping today's two-decimal "/day" form below the threshold (or
// under -exact) and switching to humanizeMagnitude's form, still suffixed
// "/day", at or above it.
//
// Judged on the average's OWN magnitude, independently of its row's count —
// a huge total spread over many observed days can average out to a small
// per-day rate that stays exact right beside an abbreviated total, and
// that is correct: the two columns answer different questions and one
// crossing the threshold says nothing about the other. The reverse can
// never happen (an average abbreviated while its own row's count is not),
// since a per-day average can never exceed the total it is divided from —
// see statsAvgHours' sibling row for the one place in this panel a bigger
// number could otherwise sit next to a smaller one's abbreviation.
func statHumanizeAvg(avg float64, exact bool) string {
	if exact || avg < statHumanizeThreshold {
		return fmt.Sprintf("%.2f/day", avg)
	}
	return humanizeMagnitude(avg) + "/day"
}

// humanizeTiers is ascending by scale (k, M, B) — see humanizeMagnitude for
// why the order matters (the tier is stepped UP from the smallest one that
// applies, to catch a rounding bump across a boundary).
var humanizeTiers = []struct {
	scale  float64
	suffix string
}{
	{1e3, "k"},
	{1e6, "M"},
	{1e9, "B"},
}

// humanizeMagnitude renders x with exactly one decimal digit and a k/M/B
// suffix, ROUNDED to that decimal — never truncated — and ALWAYS keeping the
// trailing decimal even when it is zero: Ben's own phrasing for the revision
// was "always one trailing decimal", so 46,984,830 renders "47.0M", never
// "47M" — dropping the decimal on a value that happens to round evenly would
// make the same panel print one figure with a decimal and its neighbor
// without, for a reason no reader could see in the number itself.
//
// Rounding can carry a value INTO the next tier: 999,951 divided by 1,000 is
// 999.951, which rounds to 1000.0 and would print "1000.0k" — the
// four-digit-plus-suffix collision the whole threshold exists to avoid. This
// re-checks the rounded value after each tier and steps up rather than
// trusting the raw magnitude to pick the final tier in advance.
func humanizeMagnitude(x float64) string {
	tier := -1
	for i, t := range humanizeTiers {
		if math.Abs(x) >= t.scale {
			tier = i
		}
	}
	if tier == -1 {
		return fmt.Sprintf("%.1f", round1(x))
	}
	for {
		t := humanizeTiers[tier]
		scaled := round1(x / t.scale)
		if math.Abs(scaled) < 1000 || tier == len(humanizeTiers)-1 {
			return fmt.Sprintf("%.1f%s", scaled, t.suffix)
		}
		tier++
	}
}

// round1 rounds to one decimal place, half away from zero — math.Round's own
// rule — which is what makes this ROUND rather than TRUNCATE:
// 128592.0/1000 = 128.592, and round1 returns 128.6, never 128.5.
func round1(x float64) float64 {
	return math.Round(x*10) / 10
}

// statsUsageSourceID names the one source this panel's usage rows read.
// Store.Usage's own Source filter defaults to every source that ever writes a
// payload, which today is one source anyway (internal/usage's own doc), but
// naming it here is how this panel says "Claude" rather than "whatever
// happens to carry a payload" if a second source ever starts writing one.
const statsUsageSourceID = transcripts.ID

// statsUsageRows are the five rows Store.Usage answers: how many Claude
// Code sessions touched the scope, and the four token figures, kept apart —
// see internal/usage's own doc and store.Usage's own doc for why a summed
// "tokens" total would be the cheapest figure (cache reads) wearing a
// general name, 223 times over on the corpus GcgVp measured.
//
// Grouped at the end of the Claude rows (claude records, claude prompts)
// rather than interleaved, for the same adjacency reason every other group
// in statRows already keeps its source's rows together.
//
// Each row is gated by Store.Usage's OWN three-state signal — Observations
// for sessions, a figure's own Records for a token row — never by
// reachableSources/data.Log, which answers a different question (was a
// harvest run recorded) than the one these rows need (did any record ever
// carry a payload). The two can disagree in exactly the direction that
// matters: a store with years of transcript harvests but only days of
// payloads reads as "reached" by reachableSources and "unrecorded" by
// Store.Usage, and Store.Usage's answer is the true one for a figure that did
// not exist before migration 0014.
func statsUsageRows(ctx context.Context, scope metricsScope, cal sessions.Calendar, data *dashboardData) ([]statRow, error) {
	u, err := statsUsage(ctx, scope, data)
	if err != nil {
		return nil, err
	}
	days := usageObservedDays(cal, scope, data)

	return []statRow{
		usageCountRow("claude sessions", int64(u.Sessions), days, u.Observations > 0, data.Exact),
		usageCountRow("tokens out", u.Output.Total, days, u.Output.Records > 0, data.Exact),
		usageCountRow("tokens in", u.Input.Total, days, u.Input.Records > 0, data.Exact),
		usageCountRow("cache read", u.CacheRead.Total, days, u.CacheRead.Records > 0, data.Exact),
		usageCountRow("cache written", u.CacheCreation.Total, days, u.CacheCreation.Records > 0, data.Exact),
	}, nil
}

// statsUsage asks the store, scoped exactly the way the rest of this panel
// is: showsProject's own three cases (a project, a client, or neither),
// spelled in terms of the one thing store.UsageFilter can select (one
// project at a time) rather than a second scoping rule.
//
// data.Store == nil renders every usage row unrecorded rather than panicking
// — the same guard reachableSources applies, and it is real in this file's
// own tests (statsData builds a dashboardData with no store at all) even
// though dashboardCmd always opens one.
func statsUsage(ctx context.Context, scope metricsScope, data *dashboardData) (store.Usage, error) {
	if data.Store == nil {
		return store.Usage{}, nil
	}
	var out store.Usage
	for _, f := range statsUsageFilters(scope, data) {
		u, err := data.Store.Usage(ctx, f)
		if err != nil {
			return store.Usage{}, err
		}
		out.Observations += u.Observations
		out.Sessions += u.Sessions
		out.Input = addTokens(out.Input, u.Input)
		out.Output = addTokens(out.Output, u.Output)
		out.CacheRead = addTokens(out.CacheRead, u.CacheRead)
		out.CacheCreation = addTokens(out.CacheCreation, u.CacheCreation)
	}
	return out, nil
}

// statsUsageFilters is one store.UsageFilter per project the scope names, or
// one filter naming no project at all (the store's own "every project AND
// the events attributed to none" total) when the scope is -project or
// unfiltered.
//
// A -client scope has no counterpart field on store.UsageFilter, so it is
// spelled here as one filter per project data.Report already lists under
// that client and the results are ADDED — Observations, Sessions and every
// Tokens pair. That is the same overshoot store.Usage.Sessions already
// documents for a session touching two projects, one level up: a session
// touching two of one client's projects was always going to be counted
// twice by that rule, and a client scope choosing to hide the second
// counting would need a rule for which project owns the session that
// nothing here has evidence for — the same argument store.Usage's own doc
// makes for not partitioning by project, extended to a client's basket of
// projects.
func statsUsageFilters(scope metricsScope, data *dashboardData) []store.UsageFilter {
	base := store.UsageFilter{From: data.Filter.From, To: data.Filter.To, Source: statsUsageSourceID}
	switch {
	case scope.Project != "":
		base.Project = scope.Project
		return []store.UsageFilter{base}
	case scope.Client != "":
		var filters []store.UsageFilter
		for _, c := range data.Report.Clients {
			if c.Name != scope.Client {
				continue
			}
			for _, p := range c.Projects {
				f := base
				f.Project = p
				filters = append(filters, f)
			}
		}
		return filters
	default:
		return []store.UsageFilter{base}
	}
}

// addTokens sums two roll-ups of one figure, Records and Total both — the
// only sum this file ever performs across the four token figures, and it
// never mixes figures: see statsUsageFilters for the one thing being summed
// here, one client's several projects, versus never adding output to cache
// reads.
func addTokens(a, b store.Tokens) store.Tokens {
	return store.Tokens{Records: a.Records + b.Records, Total: a.Total + b.Total}
}

// usageObservedDays is the divisor every usage row's per-day average uses,
// and it is deliberately NOT observed (statRows' own general divisor above).
//
// Payload-writing began at a fixed instant per store — the first harvest
// after migration 0014 landed — and a day before that instant can hold
// perfectly ordinary transcript activity (data.Events carries the records;
// observedDays counts the day) while holding zero usage evidence at all. A
// figure that only ever accrued on the days after that instant, divided by
// the WIDER count of every day the window was observed at all, understates
// the true rate by exactly the share of the window that predates payloads —
// silently, since the row still prints a plausible-looking number. This
// counts only the days a scoped, payload-carrying Claude record actually
// anchors in, from data.Events (already the clamped, client-stamped slice
// every other row in this file reads), so the average is never diluted by a
// day that could not have contributed to it.
//
// A payload-carrying event is not the same test as "is a transcripts.ID
// event": e.Payload == "" is exactly the pre-payload-era row
// TestARowStoredWithoutAPayloadReadsAsNothingRecorded pins at the store
// layer — the column is NULL, encode.Encode's own empty-string case — so
// filtering on it here asks the identical question statsUsage's Observations
// answers, just per day instead of summed over the whole window.
func usageObservedDays(cal sessions.Calendar, scope metricsScope, data *dashboardData) int {
	withPayload := make(map[sessions.Day]bool)
	for _, e := range data.Events {
		if e.Source != statsUsageSourceID || e.Payload == "" {
			continue
		}
		if !scope.showsProject(e.Identity.Project, data.Report.Clients) {
			continue
		}
		withPayload[cal.DayOf(e.Anchor())] = true
	}
	return len(withPayload)
}

// usageCountRow renders one usage-derived figure — claude sessions, or one
// token total — against days, its OWN divisor (see usageObservedDays), never
// the panel's general observed count statCountRow's other callers use.
//
// recorded is Store.Usage's own signal for this figure (Observations for
// sessions, one Tokens pair's own Records for a token row) rather than
// statsObserved/reachableSources: those answer whether a HARVEST ran, and
// this file's own doc comment above explains why that is the wrong question
// for a figure that Store.Usage can already answer directly and more
// narrowly.
func usageCountRow(label string, n int64, days int, recorded bool, exact bool) statRow {
	if !recorded {
		return statRow{Label: label, Marker: statUnrecorded}
	}
	row := statRow{Label: label, Value: statHumanizeValue(n, exact)}
	if days > 0 {
		row.Avg = statHumanizeAvg(float64(n)/float64(days), exact)
	}
	return row
}

// Column widths. All three columns are floors, not fixed widths:
// format.go's own package doc states the rule this panel violated on a live
// store ("A count clipped to two digits is a wrong figure rather than a
// short one, so every table below shrinks its label column and its padding
// before any figure is at risk") — a fixed 9/11 truncated "181 of 365" to
// "181 of 3…", the divisor's own value. statColumnWidths and
// statLabelColumnWidth measure the actual rows below and widen past the
// floor rather than ever clipping a number or a word; the floors just keep
// an ordinary, small-number run looking exactly as it always has.
//
// statLabelWidth stopped being a fixed width the moment a row's label could
// vary at all — statsScreenOnLabel prints "screen-on, machine-wide" under a
// filtered scope, 8 columns past this floor, and pad() truncates rather than
// widens (job dGWPc's own note on why the caveat could not simply be
// appended to the label until this floor became a floor). Every other label
// in this panel is still fixed per run, so an unfiltered run's label column
// is still exactly 16 wide, unchanged.
const (
	statLabelWidth    = 16
	statMinValueWidth = 9
	statMinAvgWidth   = 11
)

// statLabelColumnWidth is statColumnWidths' twin for the label column: the
// floor unless some row's label is longer, per this file's own note above.
func statLabelColumnWidth(rows []statRow) int {
	width := statLabelWidth
	for _, r := range rows {
		if w := len([]rune(r.Label)); w > width {
			width = w
		}
	}
	return width
}

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
	labelWidth := statLabelColumnWidth(rows)
	valueWidth, avgWidth := statColumnWidths(rows)
	for _, r := range rows {
		var line strings.Builder
		fmt.Fprintf(&line, "  %s", pad(r.Label, labelWidth))
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
