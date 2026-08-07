package main

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

// The day, week and month views, and the CSV a dashboard reads.
//
// # Why this is a flag on metrics rather than a `report` command
//
// The alternative was a second top-level command, and it was rejected. This
// binary's own usage text stakes out three questions and says none of them
// answers another's: orient says what to DO, status says whether collection is
// WORKING, metrics says what the NUMBERS are. A second numbers command would be
// the two-overlapping-systems failure this repo keeps recording in its own
// notes — two commands with two -from parsers, two exclude paths, two client
// folds, and two answers to "how many hours was that" for anybody to discover
// the difference between.
//
// The argument the other way is real and is what shaped the OUTPUT rather than
// the command. metrics prints a prose-annotated report whose caveats — that a
// client's hours are a union and never a sum, that engaged time is mostly
// margin, that the gap threshold is unsettled — do not survive being tabulated
// once per day. So the series does not replace that report: it is an extra
// SECTION beneath it, the caveats stay attached to the whole-span figures where
// they are true, and the section states its own reconciliation against them.
// -csv is the one exception, and it replaces the output entirely, because a
// data interchange format with prose in it is neither.
//
// # A period is FOLDED from days and never re-sliced
//
// Weeks and months are summed from the day periods rather than computed from
// their own edges. That is what makes the three units agree exactly instead of
// nearly: they are three foldings of one partition. It is also the shape
// sessionize already uses for its weeks, one level down, and the reason is the
// same — a week's own boundary arithmetic is a second place for an off-by-one
// to hide.

// seriesUnit is the period a span is cut into.
type seriesUnit string

const (
	// unitSpan is the whole requested span as one period. It is what -csv
	// alone emits, and it is a real unit rather than a null one: a dashboard
	// asking for a total wants the same columns as one asking for a series.
	unitSpan  seriesUnit = "span"
	unitDay   seriesUnit = "day"
	unitWeek  seriesUnit = "week"
	unitMonth seriesUnit = "month"
)

// noun is what one period of this unit is called in the report.
func (u seriesUnit) noun() string {
	if u == unitDay {
		return "working day"
	}
	return string(u)
}

func parseSeriesUnit(text string) (seriesUnit, error) {
	switch seriesUnit(text) {
	case unitSpan, unitDay, unitWeek, unitMonth:
		return seriesUnit(text), nil
	}
	return "", fmt.Errorf("-by %q is not a period this can cut a span into; write day, week, month "+
		"or span", text)
}

// seriesRow is one period of the report, with what the calendar calls it and
// what is true of it that its own figures cannot say.
type seriesRow struct {
	// Label is the period's name: the working day it begins on, the week's
	// first day, the month, or "span".
	Label string
	// Period is the metrics this row is made of.
	Period metrics.Period
	// Days is how many working days of this period the series actually holds,
	// and OfDays how many the CALENDAR says it has. A week the span only
	// reaches four days into is 4 of 7, and its total is not comparable with a
	// whole one's. The distinction is a fact about the calendar wearing no
	// disguise as a fact about the harvest.
	Days, OfDays int
	// Collection is what the store recorded about whether evidence was being
	// collected during this period. It is the difference between a zero that
	// means nobody worked and a zero that means nothing was gathered.
	Collection collectionState
}

// partial reports whether the span covered less of this period than the
// calendar says it holds.
func (r seriesRow) partial() bool { return r.Days < r.OfDays }

// collectionState is what the store knows about whether collection was running
// during a period.
//
// THE THIRD STATE IS THE POINT. capture_run arrived with migration 0007 and
// harvest_run has rows only from the day this store started keeping them, so
// for most of any real corpus the honest answer is that nothing was recorded
// either way. Rendering that as "ok" would assert something nobody measured,
// and rendering it as a failure would invent an outage.
type collectionState int

const (
	// collectionUnrecorded is no capture and no harvest run inside the period.
	// A zero on such a period cannot be told from a period nothing was
	// collected in.
	collectionUnrecorded collectionState = iota
	// collectionOK is at least one run inside the period, none of which
	// reported losing anything.
	collectionOK
	// collectionLost is at least one run inside the period that failed or
	// preserved only part of what it tried.
	collectionLost
)

func (c collectionState) String() string {
	switch c {
	case collectionOK:
		return "ok"
	case collectionLost:
		return "lost"
	default:
		return "unrecorded"
	}
}

// collectionLog is every capture and harvest run the store holds, so that each
// period can be asked what ran inside it.
//
// IT IS NOT A JUDGEMENT ABOUT WHETHER A PERIOD'S EVIDENCE IS COMPLETE, and the
// report says so. A harvest that runs on Thursday ingests evidence produced on
// Tuesday, so a period whose own runs all failed may still have had its
// evidence recovered by a later one. What this answers is the narrower question
// the store can actually answer: was collection running at the time.
type collectionLog struct {
	// at is the instant of every run, with whether it lost anything.
	at   []time.Time
	lost []bool
	// begins is the earliest run of either kind anywhere in the store, and is
	// the zero time when the store has never recorded one. Every period before
	// it is unrecorded for a reason that has nothing to do with that period.
	begins time.Time
}

// readCollectionLog gathers the capture and harvest runs. Both are read
// unbounded, because the earliest one anywhere is what tells a reader that a
// period's silence predates the record rather than describing it; there are
// hundreds of rows, not millions.
func readCollectionLog(ctx context.Context, st *store.Store) (collectionLog, error) {
	var log collectionLog
	captures, err := st.Captures(ctx, store.Filter{})
	if err != nil {
		return collectionLog{}, err
	}
	for _, c := range captures {
		log.add(c.StartedAt, c.Status == store.CaptureFailed || c.Status == store.CapturePartial)
	}
	runs, err := st.Runs(ctx, store.Filter{})
	if err != nil {
		return collectionLog{}, err
	}
	for _, r := range runs {
		log.add(r.StartedAt, r.Status == store.StatusFailed)
	}
	return log, nil
}

func (l *collectionLog) add(at time.Time, lost bool) {
	l.at = append(l.at, at)
	l.lost = append(l.lost, lost)
	if l.begins.IsZero() || at.Before(l.begins) {
		l.begins = at
	}
}

// during is what was recorded about [start, end).
func (l collectionLog) during(start, end time.Time) collectionState {
	state := collectionUnrecorded
	for i, at := range l.at {
		if at.Before(start) || !at.Before(end) {
			continue
		}
		if l.lost[i] {
			return collectionLost
		}
		state = collectionOK
	}
	return state
}

// seriesOf cuts a report into the periods a unit asks for.
//
// window.To IS THE EXCLUSIVE CLOSING INSTANT — the moment the window ends,
// never one still inside it — matching Calendar.Bounds' half-open day
// convention and dashboardPeriod's own documented "[From, To)" window (see
// dashboard.go). metrics.go's own filter.To is store.Filter's INCLUSIVE
// closing instant for the EVENT QUERY, a different question, and is handed in
// unconverted: for any instant not sitting exactly on a working day's own
// boundary the two readings agree on which day it falls in, and for the one
// case they would not — filter.To landing exactly on a day's opening instant,
// which is what a bare -to date already backs off of by a microsecond and
// what a trailing window built from Calendar.Bounds produces exactly — this
// convention is what keeps that instant from pulling in a day it only touches
// at its own first nanosecond. This one convention is also what let
// dashboard_heatmap.go's heatmapGridOf drop its own second, parallel day
// computation and trust this function's rows directly.
//
// The edges are always WORKING DAYS, whatever the unit, and weeks and months
// are folded from them. The day list is widened past the requested window to
// whatever the report's own presence reaches, because a sitting begins a
// lead-in before its first observation and runs a tail-out past its last: edges
// that stopped at the window would leave a segment outside the series, which
// Series refuses rather than silently dropping.
func seriesOf(r metrics.Report, cal sessions.Calendar, unit seriesUnit, window sessions.Window,
	log collectionLog) ([]seriesRow, error) {
	from, to := window.From, window.To
	if len(r.Segments) > 0 {
		if first := r.Segments[0].Start; from.IsZero() || first.Before(from) {
			from = first
		}
		// Segment.End is exclusive, the same convention to now carries, so a
		// segment reaching past the window replaces it directly.
		if last := r.Segments[len(r.Segments)-1].End; to.IsZero() || last.After(to) {
			to = last
		}
	}
	if from.IsZero() || to.IsZero() || !to.After(from) {
		return nil, nil
	}

	if unit == unitSpan {
		periods, err := r.Series([]time.Time{from, to})
		if err != nil {
			return nil, err
		}
		return []seriesRow{{
			Label: "span", Period: periods[0], Days: 1, OfDays: 1,
			Collection: log.during(periods[0].Start, periods[0].End),
		}}, nil
	}

	// cal.Days itself still takes an INCLUSIVE end — that is Calendar's own,
	// unrelated API and every other caller of it (ByDay, observedWindow) keeps
	// that convention — so the exclusive `to` this function works in is
	// converted at the one place it is spent on that call, rather than
	// threading the conversion through every caller.
	days := cal.Days(from, to.Add(-time.Nanosecond))
	if len(days) == 0 {
		return nil, nil
	}
	edges := make([]time.Time, 0, len(days)+1)
	for _, d := range days {
		start, _ := cal.Bounds(d)
		edges = append(edges, start)
	}
	_, last := cal.Bounds(days[len(days)-1])
	edges = append(edges, last)

	periods, err := r.Series(edges)
	if err != nil {
		return nil, err
	}
	rows := make([]seriesRow, 0, len(periods))
	for i, p := range periods {
		rows = append(rows, seriesRow{
			Label: days[i].String(), Period: p, Days: 1, OfDays: 1,
			Collection: log.during(p.Start, p.End),
		})
	}
	if unit == unitDay {
		return rows, nil
	}
	return foldSeries(cal, days, rows, unit), nil
}

// foldSeries sums day rows into the weeks or months they belong to.
//
// SUMMING IS EXACT HERE AND WOULD NOT BE IF THE PERIODS WERE RECOMPUTED. The
// day periods are disjoint cuts of one segmentation, so a week is their sum to
// the nanosecond — including a project's ABSOLUTE engaged time, which overlaps
// between projects and never between days. Recomputing a week from its events
// would give a different and unsummable number; see internal/metrics/series.go.
func foldSeries(cal sessions.Calendar, days []sessions.Day, rows []seriesRow,
	unit seriesUnit) []seriesRow {
	var out []seriesRow
	index := map[string]int{}
	for i, row := range rows {
		label, start, end, of := periodOf(cal, days[i], unit)
		at, ok := index[label]
		if !ok {
			out = append(out, seriesRow{Label: label, OfDays: of,
				Period: metrics.Period{Start: start, End: end}})
			at = len(out) - 1
			index[label] = at
		}
		out[at].Days++
		out[at].Collection = worseOf(out[at].Collection, row.Collection)
		addPeriod(&out[at].Period, row.Period)
	}
	return out
}

// periodOf names the week or month a working day belongs to, and says how long
// the calendar makes it.
//
// A WEEK IS 7 DAYS AND A MONTH IS ITS OWN LENGTH WHATEVER ARRIVED. Counting
// only the days the corpus held would report a span beginning on a Thursday as
// a four-day week, which is a fact about the harvest wearing the clothes of a
// fact about the calendar.
func periodOf(cal sessions.Calendar, d sessions.Day, unit seriesUnit) (string, time.Time, time.Time, int) {
	if unit == unitWeek {
		first := cal.WeekOf(d)
		start, _ := cal.Bounds(first)
		_, end := cal.Bounds(first.AddDays(6))
		return "week of " + first.String(), start, end, 7
	}
	first := sessions.Day{Year: d.Year, Month: d.Month, Date: 1}
	// The last day of the month, through the standard library so that February
	// and a leap year need no arithmetic here. Noon, so no zone can move it.
	end := time.Date(d.Year, d.Month, 1, 12, 0, 0, 0, time.UTC).AddDate(0, 1, -1)
	last := sessions.Day{Year: end.Year(), Month: end.Month(), Date: end.Day()}
	from, _ := cal.Bounds(first)
	_, to := cal.Bounds(last)
	return fmt.Sprintf("%04d-%02d", d.Year, int(d.Month)), from, to, last.Date
}

// worseOf keeps the more serious of two collection states, so a week holding
// one failed day does not read as healthy.
func worseOf(a, b collectionState) collectionState {
	if a == collectionLost || b == collectionLost {
		return collectionLost
	}
	if a == collectionOK || b == collectionOK {
		return collectionOK
	}
	return collectionUnrecorded
}

// addPeriod sums one period into another, merging the shares by name.
func addPeriod(dst *metrics.Period, src metrics.Period) {
	// Start and End are deliberately untouched: a folded period's endpoints
	// come from the CALENDAR, not from the days that happened to arrive, so
	// that a month the span reaches four days into still reports the month's
	// own length rather than four days'.
	dst.Presence.Total += src.Presence.Total
	dst.Presence.Observed += src.Presence.Observed
	dst.Clientless += src.Clientless
	dst.Unattributed.Total += src.Unattributed.Total
	dst.Unattributed.Observed += src.Unattributed.Observed
	dst.Unregistered += src.Unregistered
	dst.Unlocatable += src.Unlocatable
	dst.Bridged += src.Bridged
	dst.Projects = addShares(dst.Projects, src.Projects)
	dst.Clients = addShares(dst.Clients, src.Clients)
}

func addShares(dst, src []metrics.Share) []metrics.Share {
	for _, s := range src {
		i := slices.IndexFunc(dst, func(d metrics.Share) bool { return d.Name == s.Name })
		if i < 0 {
			dst = append(dst, s)
			continue
		}
		dst[i].Engaged.Total += s.Engaged.Total
		dst[i].Engaged.Observed += s.Engaged.Observed
		dst[i].Allocated += s.Allocated
		dst[i].Corroborated += s.Corroborated
		dst[i].Attested += s.Attested
		dst[i].Weak += s.Weak
		dst[i].Unaccounted += s.Unaccounted
	}
	slices.SortFunc(dst, func(a, b metrics.Share) int { return strings.Compare(a.Name, b.Name) })
	return dst
}

// shareIn is the figures a row shows for the scope the reader asked for: one
// project's, one client's, or every project's summed.
//
// Summing every project's ABSOLUTE engaged time across a period double-counts a
// minute two projects shared, exactly as the unfiltered table's "sum" row does,
// and for the same reason: it is the invoice figure and concurrent work bills
// to both. The reconciliation below is stated against PRESENCE, which does not.
func shareIn(p metrics.Period, scope metricsScope) (absolute, allocated time.Duration) {
	switch {
	case scope.Project != "":
		s, _ := p.Project(scope.Project)
		return s.Engaged.Total, s.Allocated
	case scope.Client != "":
		s, _ := p.Client(scope.Client)
		return s.Engaged.Total, s.Allocated
	}
	for _, s := range p.Projects {
		absolute += s.Engaged.Total
		allocated += s.Allocated
	}
	return absolute, allocated
}

// reportSeries prints the periods beneath the whole-span report, and reconciles
// them against it.
func reportSeries(out io.Writer, r metrics.Report, cal sessions.Calendar, unit seriesUnit,
	rows []seriesRow, log collectionLog, scope metricsScope, cols int) {
	if len(rows) == 0 {
		fmt.Fprintf(out, "\nno %ss to report: nothing was recorded in this span, which is not the "+
			"same as nothing having happened\n", unit.noun())
		return
	}

	fmt.Fprintf(out, "\nBY %s, days from %v in %s, weeks from %v\n",
		strings.ToUpper(unit.noun()), cal.DayStart, zoneName(cal, rows[0].Period.Start),
		strings.ToLower(cal.WeekStart.String()))

	var presence, absolute, allocated, unattributed time.Duration
	var unrecorded, partial int
	body := make([][]string, 0, len(rows)+1)
	for _, row := range rows {
		a, alloc := shareIn(row.Period, scope)
		presence += row.Period.Presence.Total
		absolute += a
		allocated += alloc
		unattributed += row.Period.Unattributed.Total
		if row.Collection == collectionUnrecorded {
			unrecorded++
		}
		if row.partial() {
			partial++
		}
		note := row.Collection.String()
		if row.partial() {
			note += fmt.Sprintf(", %d of %d days", row.Days, row.OfDays)
		}
		body = append(body, []string{row.Label,
			fmt.Sprintf("%.0fh", row.Period.Length().Hours()),
			metricsHours(row.Period.Presence.Total), metricsHours(a), metricsHours(alloc),
			metricsHours(row.Period.Unattributed.Total), note})
	}
	body = append(body, []string{"sum", "", metricsHours(presence), metricsHours(absolute),
		metricsHours(allocated), metricsHours(unattributed), ""})

	writeColumns(out, table{
		indent:  "  ",
		gutter:  " ",
		head:    []string{unit.noun(), "length", "presence", "absolute", "allocated", "unattributed", "collection"},
		body:    body,
		natural: []int{14, 7, 10, 10, 11, 13, 22},
		right:   []bool{false, true, true, true, true, true, false},
		floor:   10,
		cols:    cols,
	})

	// THE RECONCILIATION. The periods are cut out of the same segments the
	// ungrouped figures are summed from, so the difference is zero by
	// construction; printing it is what makes that falsifiable rather than
	// asserted, and a non-zero here is a bug and never a rounding artefact.
	fmt.Fprintf(out, "\n%v across %s, against an ungrouped %v; difference %v\n",
		metricsHours(presence), plural(len(rows), unit.noun()),
		metricsHours(r.Presence.Total), presence-r.Presence.Total)
	if presence != r.Presence.Total {
		fmt.Fprintf(out, "WARNING: the per-period figures do not sum to the total; the period "+
			"boundary is creating or destroying time\n")
	}

	// PRESENCE is the column that sums; ABSOLUTE overlaps between projects, so a
	// period two projects worked in at once shows more absolute than presence.
	// That is not an error, and the count is what says how much of the table it
	// affects — see docs/content/docs/metrics.md for why the two differ.
	fmt.Fprintf(out, "%s show more absolute than presence, which is concurrent work\n",
		plural(overCounted(rows, scope), "period"))

	// plural() appends its s to the noun it is given, so it must be handed the
	// noun alone. This was handing it unit.noun()+" is", which rendered two
	// partial weeks as "2 week iss": the count was right and the sentence
	// carrying it was not one. The assertion above matches on "partly covered"
	// and never looked as far as the words before it.
	if partial > 0 {
		fmt.Fprintf(out, "%s only partly covered by the span asked for; the days column says "+
			"how much of each is here\n", plural(partial, unit.noun()))
	}
	reportCollection(out, unit, rows, log, unrecorded, cols)
}

// overCounted is how many periods show more absolute engaged time than
// presence, which is concurrent work and not an error.
func overCounted(rows []seriesRow, scope metricsScope) int {
	var n int
	for _, row := range rows {
		if a, _ := shareIn(row.Period, scope); a > row.Period.Presence.Total {
			n++
		}
	}
	return n
}

// reportCollection says what the collection column can and cannot settle.
//
// A ZERO IS AMBIGUOUS AND THE REPORT REFUSES TO PRETEND OTHERWISE. It can mean
// nobody worked, or it can mean nothing was gathered — the live store's own
// harvest failed silently for 44 hours in July 2026 when a reinstall dropped
// its Full Disk Access grant — and the store can separate them only where a
// capture or a harvest left a row. Where it left none, this says so by name
// rather than printing a confident nothing.
func reportCollection(out io.Writer, unit seriesUnit, rows []seriesRow, log collectionLog,
	unrecorded int, cols int) {
	if unrecorded == 0 {
		return
	}
	begins := "and this store has never recorded a capture or a harvest at all"
	if !log.begins.IsZero() {
		begins = fmt.Sprintf("and the earliest run of either kind anywhere in this store is %v",
			log.begins.Format(time.RFC3339))
	}
	fmt.Fprint(out, "\n")
	wrap(out, "  ", fmt.Sprintf("COLLECTION IS UNRECORDED FOR %s. Neither a capture nor a harvest "+
		"left a row inside them, %s, so a 0.0h there cannot be told apart from a period in which "+
		"nothing was collected.", plural(unrecorded, "of these "+unit.noun()), begins), cols)
}

// csvSeries writes the series as one row per period per grouping.
//
// # Why long rather than wide
//
// A wide row — one column per project — cannot say what a level means. Presence
// is not a project, unattributed presence is not a project, and a client is a
// different population from the projects beneath it. A wide table forces every
// one of those into a project-shaped hole and then needs a zero wherever a
// project was not active, which is exactly the "absence of a row is not an
// observation" collapse: a consumer cannot tell a project that was idle from a
// project that does not exist. One row per grouping says which question each
// figure answers, in a column.
//
// The period endpoints are written in the CALENDAR'S OWN ZONE rather than in
// UTC. A working day is a local concept and its label is a local date, so a UTC
// endpoint printed beside it reads as naming a different day — one instant
// wearing two dates for anybody who does not convert first.
//
// # Why the numbers are seconds and are exact
//
// The table quotes 0.1h, which is six minutes of resolution and is fine to read
// and useless to re-aggregate. These are decimal seconds rendered from the
// integer nanoseconds the report is made of, by integer arithmetic rather than
// through a float, so summing every row of a period reproduces the period and
// summing every period reproduces the span — to the nanosecond, which is the
// precision at which the report's own conservation checks hold.
//
// # Why some cells are EMPTY rather than zero
//
// Presence is not allocated to anybody, and a 0 in that column would be an
// allocation of none rather than a question that does not apply. The same
// distinction the rest of this codebase spends its comments on, in a cell.
// # Why a filter selects rows here too, and changes no figure
//
// -client and -project narrow which GROUPING rows are written, exactly as they
// narrow which rows the table prints, and the presence rows are untouched by
// either. Presence is the whole span's wall clock and belongs to no project, so
// dropping it under a project filter would leave an export whose shares had no
// denominator. Nothing is recomputed: every figure here is byte for byte the
// figure in the unfiltered export.
func csvSeries(out io.Writer, cal sessions.Calendar, unit seriesUnit, rows []seriesRow,
	scope metricsScope, clients []metrics.Client) error {
	w := csv.NewWriter(out)
	if err := w.Write([]string{"period", "start", "end", "unit", "days", "of_days", "covered",
		"collection", "level", "name", "seconds", "observed_seconds", "allocated_seconds",
		"corroborated_seconds", "attested_seconds", "weak_seconds", "unaccounted_seconds"}); err != nil {
		return err
	}
	for _, row := range rows {
		covered := "whole"
		if row.partial() {
			covered = "partial"
		}
		prefix := []string{row.Label,
			row.Period.Start.In(cal.Location).Format(time.RFC3339),
			row.Period.End.In(cal.Location).Format(time.RFC3339),
			string(unit), fmt.Sprint(row.Days), fmt.Sprint(row.OfDays), covered,
			row.Collection.String()}

		emit := func(level, name string, cells ...string) error {
			return w.Write(append(append([]string{}, prefix...), append([]string{level, name}, cells...)...))
		}
		// The skeleton every period carries, whether or not anything fell in
		// it. A period with no rows at all would be a period nobody sliced.
		if err := emit("presence", "", csvSeconds(row.Period.Presence.Total),
			csvSeconds(row.Period.Presence.Observed), "", "", "", "", ""); err != nil {
			return err
		}
		if err := emit("clientless", "", csvSeconds(row.Period.Clientless),
			"", "", "", "", "", ""); err != nil {
			return err
		}
		for _, u := range []struct {
			name string
			d    time.Duration
		}{
			{"unregistered", row.Period.Unregistered},
			{"unlocatable", row.Period.Unlocatable},
			{"bridged", row.Period.Bridged},
		} {
			// The three reasons and no total row: the total is their sum
			// exactly, and a fourth row carrying it would double-count for any
			// consumer that added the column up.
			if err := emit("unattributed", u.name, csvSeconds(u.d), "", "", "", "", "", ""); err != nil {
				return err
			}
		}
		for _, level := range []struct {
			name   string
			shares []metrics.Share
			shows  func(string) bool
		}{
			{"client", row.Period.Clients, func(n string) bool {
				return scope.showsClient(clientNamed(clients, n))
			}},
			{"project", row.Period.Projects, func(n string) bool {
				return scope.showsProject(n, clients)
			}},
		} {
			for _, s := range level.shares {
				if !level.shows(s.Name) {
					continue
				}
				if err := emit(level.name, s.Name, csvSeconds(s.Engaged.Total),
					csvSeconds(s.Engaged.Observed), csvSeconds(s.Allocated),
					csvSeconds(s.Corroborated), csvSeconds(s.Attested), csvSeconds(s.Weak),
					csvSeconds(s.Unaccounted)); err != nil {
					return err
				}
			}
		}
	}
	w.Flush()
	return w.Error()
}

// clientNamed finds the report's roll-up for a client, so that a scope test
// needing the client's PROJECT LIST — which is what a -project filter narrows
// the client table by — has it. A period's Share carries only a name.
func clientNamed(clients []metrics.Client, name string) metrics.Client {
	for _, c := range clients {
		if c.Name == name {
			return c
		}
	}
	return metrics.Client{Name: name}
}

// csvSeconds renders a duration as exact decimal seconds.
//
// Built from the integer nanoseconds rather than from Duration.Seconds(),
// because a float64 cannot hold every nanosecond count a long span reaches and
// the whole reason this column exists is that it can be added back up.
func csvSeconds(d time.Duration) string {
	ns := d.Nanoseconds()
	sign := ""
	if ns < 0 {
		sign, ns = "-", -ns
	}
	if ns%int64(time.Second) == 0 {
		return fmt.Sprintf("%s%d", sign, ns/int64(time.Second))
	}
	text := fmt.Sprintf("%s%d.%09d", sign, ns/int64(time.Second), ns%int64(time.Second))
	return strings.TrimRight(text, "0")
}
