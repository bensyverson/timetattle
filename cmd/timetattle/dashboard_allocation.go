package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The allocation and effective-rate panel — hours against what was promised,
// and what an hour is actually earning.
//
// THIS SECTION PRINTS MONEY. Design principle 4 of
// project/2026-08-07-cli-overhaul-plan.md: a `dashboard` capture is the fourth
// surface (after `review`, `metrics` and `orient`) that re-imports a client
// name and a rate into anything it is pasted into. Nothing in this file, its
// test file or the documentation carries a real name or a real figure, and
// `go run ./scripts/confidential -names <names> -gate .` is the check.
//
// THIS FILE, AND dashboard_allocation_test.go, ARE THE ONLY FILES THIS TASK
// EDITS in cmd/. See the section list in dashboard.go for the contract every
// render function shares.
//
// # Where the money comes from, and where it does not
//
// From internal/config's `engagements` section, and nowhere else — see
// engagements.go for the two shapes an entry may declare. In particular NOT
// from metrics.Client.Allocated or metrics.Project.Allocated, which look like
// the field this section wants and are a different quantity entirely: an even
// division of concurrently-attributed presence across the clients active in an
// instant. That is a CAPACITY figure — how the day divided between the people
// being worked for — and has no relationship to a contracted hour budget. A
// section that rendered it as one would print a confident, meaningless bar.
//
// # The three states a figure here can be in
//
// The absence-of-a-row rule (AGENTS.md), which this project has shipped
// backwards in three separate tools, applies to money at least as hard as it
// applies to counts:
//
//	a figure     the window was observed and this is what was measured;
//	unrecorded   nothing in the window was collected, so hours against a
//	             budget would be an assertion nobody made;
//	omitted      no rate or contract exists to divide by. NEVER $0 — that
//	             would assert that an hour of this work earns nothing.
//
// The last of those is criterion sQI in so many words.
func renderAllocationRate(ctx context.Context, out io.Writer, scope metricsScope,
	period dashboardPeriod, cc colorCapability, data *dashboardData) error {
	entries := scopedEngagements(scope, data)
	if len(entries) == 0 {
		return allocationUnconfigured(out)
	}
	cal, err := data.Config.Calendar.Sessions()
	if err != nil {
		return err
	}
	// The same observed-day count every other section divides by — see
	// observedDays' own doc comment in dashboard_stats.go. Here it decides one
	// thing only: whether a zero against a budget is a measurement or an
	// absence.
	observed, _ := observedDays(cal, period, data)

	history, err := allocationHistoryFor(ctx, entries, cal, data)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Engagement.Shape() == config.EngagementContract {
			if err := writeContractEngagement(out, cc, cal, e, history, data); err != nil {
				return err
			}
			continue
		}
		if err := writeHourlyEngagement(out, cc, cal, period, observed, e); err != nil {
			return err
		}
	}
	return nil
}

// allocationUnconfigured is the line this section prints on the overwhelming
// majority of runs, and it is deliberately one quiet line rather than a panel
// of zeroes.
//
// Most projects in the registry are unbilled personal work. An engagement is
// the exception, not the rule, and a section that drew an empty progress bar
// and a $0.00 rate for every unpaid Saturday would be asserting a contract
// that does not exist — the same collapse of "nothing said" into "nothing
// owed" that the config refuses one level down by making every money field a
// pointer.
func allocationUnconfigured(out io.Writer) error {
	_, err := fmt.Fprint(out, "  no engagement is configured for this scope — the config's engagements "+
		"section is where an hour budget or a contract goes\n")
	return err
}

// scopedEngagement is one configured engagement that belongs in this report,
// paired with the hours the WINDOW credits it.
type scopedEngagement struct {
	// Name and Kind identify the entry — "client" or "project", which is also
	// which of the two config maps it came from.
	Name, Kind string
	Engagement config.Engagement
	// Hours is the scoped engaged total for the window, straight out of the
	// report every other section reads. Engaged.Total and not Allocated: the
	// package doc of metrics.Project calls it "the billing figure" in so many
	// words, and this is the one section that bills.
	Hours time.Duration
}

// scopedEngagements is every configured engagement the current scope shows, in
// a fixed order (clients then projects, each by name) so two runs of the same
// report never disagree about what comes first.
//
// SCOPE SELECTS WHAT PRINTS, through metricsScope's own showsClient and
// showsProject — the same predicates metrics.go's report functions use, never a
// second filtering rule with its own drift. Two consequences worth naming:
//
//   - Under `-all` (or an unscoped run) scope is the zero value, both
//     predicates admit everything, and every configured engagement prints.
//     That is the correct reading of "report every client and project": a
//     freelancer with three engagements wants all three.
//   - Under `-project X`, a CLIENT engagement prints only if the report's own
//     client-project roll-up credits X to that client. A client the record
//     does not link to X is not this scope's client, and showsClient already
//     says so given a row with no Projects in it.
func scopedEngagements(scope metricsScope, data *dashboardData) []scopedEngagement {
	if data == nil || data.Config == nil || data.Config.Engagements.IsEmpty() {
		return nil
	}
	var out []scopedEngagement
	for _, name := range slices.Sorted(maps.Keys(data.Config.Engagements.Clients)) {
		row, ok := data.Report.Client(name)
		if !ok {
			// A configured client the window has no row for still has to be
			// tested against the scope, so the zero row carries the name and
			// nothing else — no Projects, which is exactly what makes a
			// -project filter reject it.
			row = metrics.Client{Name: name}
		}
		if !scope.showsClient(row) {
			continue
		}
		out = append(out, scopedEngagement{
			Name: name, Kind: "client", Engagement: data.Config.Engagements.Clients[name],
			Hours: row.Engaged.Total,
		})
	}
	for _, name := range slices.Sorted(maps.Keys(data.Config.Engagements.Projects)) {
		if !scope.showsProject(name, data.Report.Clients) {
			continue
		}
		row, _ := data.Report.Project(name)
		out = append(out, scopedEngagement{
			Name: name, Kind: "project", Engagement: data.Config.Engagements.Projects[name],
			Hours: row.Engaged.Total,
		})
	}
	return out
}

// ─── the hourly shape: a recurring budget, pro-rated to the window ───────────

// writeHourlyEngagement prints shape (a): a nominal rate, and the window's
// hours against a budget pro-rated to the window.
func writeHourlyEngagement(out io.Writer, cc colorCapability, cal sessions.Calendar,
	period dashboardPeriod, observed int, e scopedEngagement) error {
	cur := e.Engagement.CurrencyLabel()
	if _, err := fmt.Fprintf(out, "  %s — %s per %s at %s/h\n", e.label(),
		allocationHours(*e.Engagement.BudgetedHours), e.Engagement.Period,
		allocationMoney(cur, *e.Engagement.Rate)); err != nil {
		return err
	}

	budget := allocationWindowBudget(cal, period, e.Engagement)
	worked := e.Hours.Hours()
	if worked == 0 && observed == 0 {
		// UNRECORDED, not zero. Nothing in this window was collected, so
		// "0.0h of 47.1h" would assert that the budget went unused when what
		// actually happened is that nobody was watching.
		_, err := fmt.Fprint(out, "    unrecorded — nothing in this window was collected, so there are "+
			"no hours to set against the budget\n")
		return err
	}
	_, err := fmt.Fprintf(out, "    %s %s of %s budgeted (%s), %s\n",
		allocationBar(cc, worked, budget), allocationHours(worked), allocationHours(budget),
		allocationPercent(worked, budget), allocationVerdict(cc, worked, budget))
	return err
}

// allocationWindowBudget pro-rates a recurring budget to the reporting window.
//
// # The rule, and the two rules it is not
//
// The window is measured in the CALENDAR's own working days — the same days
// every other section of this dashboard is tiled by, beginning at the
// configured day_start rather than at midnight — and each day contributes the
// FRACTION of itself the window actually covers, divided by the length of the
// period that day fell in. So a 30h-per-week budget over eleven whole days is
// 30 × 11/7 = 47.14h.
//
// Two other rules were considered and rejected, and both would have been
// wrong in a way that moves the over/under verdict rather than a decimal
// place:
//
//   - WEEKDAYS, MONDAY TO FRIDAY. sessions.Calendar has no concept of a
//     weekend and inventing one here would assert a working week this tool has
//     never observed — it exists precisely to measure evenings and weekends,
//     and its own corpus is full of both. A budget stated per week is per
//     week, not per five-sevenths of one.
//
//   - AN ELAPSED-DURATION RATIO (window hours ÷ 168). Simpler, and wrong twice
//     a year: a working day containing a daylight-saving transition is 23 or 25
//     hours long, and dividing raw durations would report that a spring week
//     held 6/7 of a day less budget than it did. Counting a fully-covered day
//     as exactly 1.0 whatever its length is the same choice sessions.Calendar
//     itself makes ("a day containing a transition is 23 or 25 hours long, and
//     that is the correct answer rather than an error to be corrected").
//
// The FRACTIONAL first day is what makes this safe under clamping. A clamped
// window opens at an INSTANT — the scope's first observation, see
// clampPeriodToFirstObservation — not at a day boundary, and counting that
// partial day whole would inflate the budget by up to a full day. On a short
// window that is easily enough to turn an over-budget week into an
// under-budget one.
//
// A MONTHLY budget divides by the real length of each day's own month, not by
// an averaged 30.44: February and January differ by 10%, which on a 160h
// monthly budget is sixteen hours. See config.BudgetPeriod.DaysIn.
func allocationWindowBudget(cal sessions.Calendar, period dashboardPeriod, e config.Engagement) float64 {
	if e.BudgetedHours == nil {
		return 0
	}
	share := 0.0
	// The period is half-open [From, To); Days takes an inclusive span, so the
	// last instant that must be covered is the one before To — the same
	// conversion observedDays makes for the same reason.
	for _, d := range cal.Days(period.From, period.To.Add(-time.Nanosecond)) {
		start, end := cal.Bounds(d)
		length := end.Sub(start)
		if length <= 0 {
			continue
		}
		covered := overlapOf(start, end, period.From, period.To)
		if covered <= 0 {
			continue
		}
		days := e.Period.DaysIn(d.Year, d.Month)
		if days <= 0 {
			continue
		}
		share += float64(covered) / float64(length) / days
	}
	return *e.BudgetedHours * share
}

// overlapOf is how much of [aStart, aEnd) also lies in [bStart, bEnd).
func overlapOf(aStart, aEnd, bStart, bEnd time.Time) time.Duration {
	if aStart.Before(bStart) {
		aStart = bStart
	}
	if aEnd.After(bEnd) {
		aEnd = bEnd
	}
	if !aStart.Before(aEnd) {
		return 0
	}
	return aEnd.Sub(aStart)
}

// ─── the contract shape: a burn-down over the contract's own life ────────────

// writeContractEngagement prints shape (b): a fixed total burning down against
// the hours it has actually taken, and the effective rate that falls as those
// hours accumulate.
//
// # All-time, and the window said beside it
//
// A CONTRACT DOES NOT RENEW WITH THE REPORTING WINDOW. Its 160 expected hours
// are consumed once, over the contract's whole life, so a burn-down cut to a
// trailing 7-day window would say 158h remained on a contract with 2h left —
// and would price the effective rate at whatever the last week happened to
// cost, which is not a rate anybody is paid. So the burn-down and the
// effective rate both read ALL-TIME hours. The window's own share is stated on
// the same line rather than dropped, because every other figure on this
// dashboard describes the window and a reader must never have to guess which
// span a number covers.
func writeContractEngagement(out io.Writer, cc colorCapability, cal sessions.Calendar,
	e scopedEngagement, history *allocationHistory, data *dashboardData) error {
	cur := e.Engagement.CurrencyLabel()
	header := fmt.Sprintf("  %s — fixed contract %s for %s expected", e.label(),
		allocationMoney(cur, *e.Engagement.ContractTotal), allocationHours(*e.Engagement.ExpectedHours))
	if !e.Engagement.Started.IsZero() {
		header += ", since " + e.Engagement.Started.String()
	}
	if _, err := fmt.Fprintln(out, header); err != nil {
		return err
	}

	toDate, err := history.hoursToDate(e, cal, data)
	if err != nil {
		return err
	}
	expected := *e.Engagement.ExpectedHours
	hours := toDate.Hours()
	if _, err := fmt.Fprintf(out, "    %s %s of %s expected (%s), %s\n",
		allocationBar(cc, hours, expected), allocationHours(hours), allocationHours(expected),
		allocationPercent(hours, expected), allocationRemaining(cc, hours, expected)); err != nil {
		return err
	}

	// CRITERION sQI. No hours means no denominator, and an omitted figure
	// rather than a zero one: $0.00/h would assert that this contract earns
	// nothing per hour, which is the one thing the record cannot say.
	if hours <= 0 {
		if _, err := fmt.Fprint(out, "    effective rate: none yet — no hours are recorded against this "+
			"contract, so there is nothing to divide by\n"); err != nil {
			return err
		}
	} else if _, err := fmt.Fprintf(out, "    effective rate %s/h over %s to date; %s of that in this window\n",
		allocationMoney(cur, *e.Engagement.ContractTotal/hours), allocationHours(hours),
		allocationHours(e.Hours.Hours())); err != nil {
		return err
	}

	return writeContractCollectionCaveat(out, cal, e, data)
}

// writeContractCollectionCaveat says so when the store cannot see the whole of
// the contract it is reporting on.
//
// Collection began when somebody installed this tool; the contract began when
// somebody signed it, and there is no reason at all for those to be the same
// day. Hours worked before the first recorded collection run are not zero —
// they are unobserved, and the difference is the whole of the absence-of-a-row
// rule. Every figure above is therefore a bound rather than a measurement in
// that case, and it is named as one: hours to date are a LOWER bound, and
// since the effective rate divides a fixed total BY those hours, it is an
// UPPER bound. Saying which direction each is wrong in is the part a reader
// can actually act on.
func writeContractCollectionCaveat(out io.Writer, cal sessions.Calendar, e scopedEngagement,
	data *dashboardData) error {
	if e.Engagement.Started.IsZero() {
		return nil
	}
	started := e.Engagement.Started.In(cal.Location)
	begins := data.Log.begins
	if !begins.IsZero() && !begins.After(started) {
		return nil
	}
	when := "no collection run is recorded at all"
	if !begins.IsZero() {
		when = "collection began " + begins.Local().Format("2006-01-02")
	}
	_, err := fmt.Fprintf(out, "    %s, after this contract started on %s — hours to date are a lower "+
		"bound and the effective rate an upper one\n", when, e.Engagement.Started)
	return err
}

// allocationHistory is the ONE wider fetch every contract in scope shares.
//
// # What it costs, and when it costs anything at all
//
// A contract's hours reach outside the reporting window by definition, so they
// cannot come from data.Report — the only figure in this dashboard that
// cannot. dashboardData's own doc comment says what to do about that in so
// many words: "the one data layer is the store", and a section needing a query
// metrics never runs calls Store directly, exactly as the freshness and
// filetype sections do.
//
// The cost is one store query over the wider span plus one metrics.Compute
// over what it returns — the same two operations dashboardCmd already pays
// once. narrowDataToPeriod's own doc records what those cost on a real store:
// about three seconds to fetch a trailing year's 1,030,041 events and 1.58s to
// cluster them. Three things keep this from being paid twice on every run:
//
//   - NO CONTRACT IN SCOPE, NO QUERY. The overwhelmingly common case, since
//     most projects carry no engagement at all.
//   - A CONTRACT WHOSE LIFE THE WINDOW ALREADY COVERS costs no query either:
//     data.Events is already a superset, and eventsFrom narrows it in memory.
//   - SEVERAL CONTRACTS SHARE ONE FETCH. The query runs once, from the
//     earliest start any contract in scope names, and each contract's own
//     report is computed from that one slice. Only the clustering repeats, and
//     only per distinct start date.
//
// `started` is what bounds the fetch. A contract that names one is queried
// from that day; one that does not is queried from the beginning of the store,
// which is the honest reading of "all time" when nobody has said when the work
// began.
type allocationHistory struct {
	// events is the retained, client-stamped stream from `from` onward, or
	// data.Events when no wider fetch was needed.
	events []source.Event
	from   time.Time
	// reports memoises metrics.Compute per start instant.
	reports map[time.Time]metrics.Report
	params  sessions.Params
}

// allocationHistoryFor prepares the wider fetch, if any contract in scope
// needs one. It returns a usable history in every case, including the case
// where nothing needs one at all.
func allocationHistoryFor(ctx context.Context, entries []scopedEngagement, cal sessions.Calendar,
	data *dashboardData) (*allocationHistory, error) {
	h := &allocationHistory{
		events:  data.Events,
		from:    data.Filter.From,
		reports: map[time.Time]metrics.Report{data.Filter.From: data.Report},
		params:  data.Params,
	}

	earliest, need := allocationEarliestStart(entries, cal)
	if !need || !earliest.Before(data.Filter.From) {
		return h, nil
	}
	if data.Store == nil {
		return nil, fmt.Errorf("a fixed-contract engagement's hours reach back before this window, " +
			"and no store is open to read them from")
	}

	// The SAME filter shape dashboardCmd used, widened at the From end only —
	// To is carried over unchanged so the two queries cannot disagree about
	// where the window ends.
	events, err := data.Store.Events(ctx, store.Filter{From: earliest, To: data.Filter.To})
	if err != nil {
		return nil, err
	}
	// Retained and client-stamped exactly as dashboardCmd does it, or this
	// report would be computed over a different population from every other
	// figure on the page.
	events, _ = sessions.Retain(events, data.Excludes)
	for i := range events {
		if events[i].Identity.Project == "" {
			continue
		}
		if c, ok := data.ClientMap.At(events[i].Identity.Project, events[i].Anchor()); ok {
			events[i].Identity.Client = c
		}
	}
	h.events, h.from = events, earliest
	// The window's own cached report is dropped: it was computed over a
	// narrower slice, and keying it by an instant this history no longer
	// starts at would hand a contract the wrong population.
	h.reports = map[time.Time]metrics.Report{}
	return h, nil
}

// allocationEarliestStart is the earliest instant any contract in scope needs
// history from, and whether any contract is in scope at all. A contract with
// no stated start needs the whole store, which is the zero instant.
func allocationEarliestStart(entries []scopedEngagement, cal sessions.Calendar) (time.Time, bool) {
	var earliest time.Time
	need, unbounded := false, false
	for _, e := range entries {
		if e.Engagement.Shape() != config.EngagementContract {
			continue
		}
		need = true
		if e.Engagement.Started.IsZero() {
			unbounded = true
			continue
		}
		at := e.Engagement.Started.In(cal.Location)
		if earliest.IsZero() || at.Before(earliest) {
			earliest = at
		}
	}
	if unbounded {
		return time.Time{}, need
	}
	return earliest, need
}

// hoursToDate is the engagement's all-time engaged hours: everything from its
// own start (or from the beginning of the store) up to the end of the window.
func (h *allocationHistory) hoursToDate(e scopedEngagement, cal sessions.Calendar,
	data *dashboardData) (time.Duration, error) {
	from := h.from
	if !e.Engagement.Started.IsZero() {
		if at := e.Engagement.Started.In(cal.Location); at.After(from) {
			from = at
		}
	}
	report, err := h.report(from)
	if err != nil {
		return 0, err
	}
	if e.Kind == "client" {
		row, _ := report.Client(e.Name)
		return row.Engaged.Total, nil
	}
	row, _ := report.Project(e.Name)
	return row.Engaged.Total, nil
}

// report is metrics.Compute over this history narrowed to from, memoised.
//
// eventsFrom is dashboard.go's own narrowing — the store's `COALESCE(ended_at,
// started_at) >= ?` predicate transcribed once — so a contract's population is
// what a store query starting at its own start date would have returned,
// rather than a second idea of what a window contains.
func (h *allocationHistory) report(from time.Time) (metrics.Report, error) {
	if r, ok := h.reports[from]; ok {
		return r, nil
	}
	events := h.events
	if from.After(h.from) {
		events = eventsFrom(events, from)
	}
	r, err := metrics.Compute(events, h.params)
	if err != nil {
		return metrics.Report{}, err
	}
	h.reports[from] = r
	return r, nil
}

// ─── rendering primitives ────────────────────────────────────────────────────

// allocationBarWidth is the bar's cell count. Thirty is wide enough that one
// cell is about three percent — fine enough for a glance to read a share — and
// narrow enough that the figures beside it still fit an eighty-column terminal.
const allocationBarWidth = 30

const (
	// allocationFilledGlyph and allocationEmptyGlyph are the bar itself.
	allocationFilledGlyph = "█"
	allocationEmptyGlyph  = "░"
	// allocationOverrunGlyph marks hours PAST the end of a full bar. It is a
	// different character rather than a different shade on purpose: under
	// colorNone — which is what every pipe, file and test capture gets — the
	// glyph is the whole of the signal, and a shade is not one.
	allocationOverrunGlyph = "+"
)

// allocationOverrunCells caps how far an overrun may run past the bar. Without
// a cap, two hours against a one-hour budget would draw thirty extra cells and
// wrap the line; the figure beside it is what states the magnitude precisely,
// and the mark's job is only to be impossible to miss.
const allocationOverrunCells = 6

var (
	// allocationUnderColor is calm: being inside a budget is not an event.
	allocationUnderColor = RGB{R: 90, G: 190, B: 130}
	// allocationOverColor is the warning half of design principle 5's ramp —
	// overwork is made visible rather than rewarded with a deeper green.
	allocationOverColor = RGB{R: 235, G: 100, B: 75}
	// allocationEmptyColor is the unfilled remainder: present, and quiet.
	allocationEmptyColor = RGB{R: 60, G: 60, B: 75}
)

// allocationBar draws actual against budget: a filled proportion, an unfilled
// remainder, and — past the closing bracket — an overrun mark when actual
// exceeds budget.
//
// CRITERION Ab5 LIVES HERE. Under and over are distinguished three ways over,
// so that no single channel is load-bearing: the bar's fill color, the overrun
// glyph past its end (visible with no color at all), and the verdict word
// beside it.
func allocationBar(cc colorCapability, actual, budget float64) string {
	frac := 0.0
	if budget > 0 {
		frac = actual / budget
	}
	filled := 0
	if frac > 0 {
		filled = int(min(frac, 1)*float64(allocationBarWidth) + 0.5)
	}
	// A non-zero share never rounds away to nothing: a bar that reads empty
	// where hours exist is the same collapse this whole panel is built to
	// avoid, one pixel down.
	if filled == 0 && actual > 0 {
		filled = 1
	}
	filled = min(filled, allocationBarWidth)

	color := allocationUnderColor
	if frac > 1 {
		color = allocationOverColor
	}
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(cc.Paint(color))
	b.WriteString(strings.Repeat(allocationFilledGlyph, filled))
	b.WriteString(cc.Reset())
	b.WriteString(cc.Paint(allocationEmptyColor))
	b.WriteString(strings.Repeat(allocationEmptyGlyph, allocationBarWidth-filled))
	b.WriteString(cc.Reset())
	b.WriteString("]")

	if frac > 1 {
		over := int((frac-1)*float64(allocationBarWidth) + 0.5)
		over = max(1, min(over, allocationOverrunCells))
		b.WriteString(cc.Paint(allocationOverColor))
		b.WriteString(strings.Repeat(allocationOverrunGlyph, over))
		b.WriteString(cc.Reset())
	}
	return b.String()
}

// allocationVerdict is the hourly shape's under/over sentence. The over case
// is capitalised because it is the one a reader must not skim past.
func allocationVerdict(cc colorCapability, actual, budget float64) string {
	if actual > budget {
		return cc.Paint(allocationOverColor) + "OVER by " + allocationHours(actual-budget) + cc.Reset()
	}
	return cc.Paint(allocationUnderColor) + "under by " + allocationHours(budget-actual) + cc.Reset()
}

// allocationRemaining is the contract shape's equivalent: what is left of the
// expected hours, or how far past them the work has gone.
func allocationRemaining(cc colorCapability, hours, expected float64) string {
	if hours > expected {
		return cc.Paint(allocationOverColor) + "OVER by " + allocationHours(hours-expected) + cc.Reset()
	}
	return allocationHours(expected-hours) + " remaining"
}

// allocationPercent is a share as a whole number. It is only ever a gloss on
// two figures already printed beside it, so a percentage of an absent budget
// says so rather than dividing by zero.
func allocationPercent(actual, budget float64) string {
	if budget <= 0 {
		return "no budget"
	}
	return fmt.Sprintf("%.0f%%", 100*actual/budget)
}

// allocationHours formats hours through metrics' own hours formatter, so a
// figure here and a figure in `metrics` are spelled the same way.
func allocationHours(h float64) string {
	return metricsHours(time.Duration(h * float64(time.Hour)))
}

// allocationMoney is an amount with its currency label in front, grouped in
// thousands. The label is whatever the config wrote and is empty when nothing
// was stated — Timetattle converts no currencies and asserts none, so an
// amount with no label prints bare rather than acquiring a dollar sign nobody
// asked for.
func allocationMoney(currency string, v float64) string {
	return currency + groupThousands(fmt.Sprintf("%.2f", v))
}

// groupThousands inserts separators into the integer part of a fixed-point
// decimal. A contract total is the largest number this dashboard prints and
// the one a misread digit costs the most on; 24000.00 and 240000.00 are one
// glance apart without grouping and never with it.
func groupThousands(s string) string {
	whole, frac, _ := strings.Cut(s, ".")
	sign := ""
	if strings.HasPrefix(whole, "-") {
		sign, whole = "-", whole[1:]
	}
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	if frac == "" {
		return sign + b.String()
	}
	return sign + b.String() + "." + frac
}

// label names the engagement and which namespace it came from, so a client and
// a project sharing a name are never confused for one another.
func (e scopedEngagement) label() string { return e.Name + " (" + e.Kind + ")" }
