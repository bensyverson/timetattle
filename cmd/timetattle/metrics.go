package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

// metricsCommand reports engaged time per project and presence overall.
//
// It reads and writes nothing. Metrics are a cheap aggregation over sittings
// rather than a third stored derivation, so there is no table to rebuild and no
// way for a figure to be stale relative to the events it came from — which is
// also why this opens the store read-only.
func metricsCommand(args []string) error { return metricsTo(os.Stdout, args) }

// metricsTo is metricsCommand with the report redirected, so that what the
// command prints can be asserted on rather than merely not crashing.
func metricsTo(stdout io.Writer, args []string) error {
	return executeSubcommand(metricsCmd(stdout), args)
}

func metricsCmd(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Report engaged time per project, and presence",
		Long: `Reports the two metrics a working day is judged by, from the events already in
the store. Nothing is captured, nothing is derived into a table, and the store
is opened read-only.

ENGAGED time is per-project and MAY OVERLAP, because concurrent work on two
projects is billable to both. It is the billing figure. PRESENCE is wall clock,
is never double-counted, and is the denominator for capacity: a half-time
retainer measured against anything else reads as satisfied at a fraction of
real attention.

Both normalisations are reported side by side. Under ABSOLUTE a minute two
projects shared counts in full for each, so the column sums to more than the
clock. Under ALLOCATION it is split between them, and the shares sum to exactly
the presence they came out of.

Work is reported at TWO LEVELS, client and project, because they are not the
same question and neither is redundant. A CLIENT'S HOURS ARE THE UNION OF ITS
PROJECTS' AND NEVER THEIR SUM: two of a client's projects worked in the same
minute bill that minute to each of them, and to the client once. Allocation
divides by client at the client level and by project at the project level, so a
client who keeps more repositories does not thereby consume more of the day.

-client and -project narrow WHICH ROWS ARE PRINTED and change no figure in them.
The whole report is computed either way, so PRESENCE stays the whole span's wall
clock and a share stays a share of a real day. Recomputing over one client's
evidence alone would shrink the denominator and drive every share toward 100%,
which would answer the opposite of the question a single-client report is asked.

A BARE DATE ON -to MEANS THE END OF THAT DAY. -to takes an instant and the span
is inclusive at both ends, so "-to 2026-08-03" used to mean midnight that
morning and silently dropped the whole of the day it named. An explicit time is
still that time: "-to 2026-08-03T09:00:00" is nine in the morning.

EVERY ENGAGED FIGURE IS MOSTLY INFERRED. A sitting is a cluster of observations
with a lead-in before it, a tail-out after it, and every silence shorter than
the gap threshold bridged; on the live corpus 77.2% of engaged time is that
margin rather than anything anybody observed. The observed and inferred halves
are printed separately for that reason, and a total quoted without them is not
a measurement.

Work attributed to a project but to NO CLIENT is reported as its own figure and
is folded into none of the three below. Those directories ARE registered; it is
the client that is unrecorded, which calls for a different action. Three things
produce it: nothing has ever asserted a client for that project, an assertion
exists but takes effect later than the work, or two directories claim the
project for different clients — the last is listed under CONTESTED PROJECTS.

A CLIENT ASSOCIATION IS A FACT ABOUT A MOMENT IN TIME. It lives in an
append-only log rather than on the event, so a project reassigned in June
reports the old client for May's work and the new one for July's. What that log
does NOT rewind is which directories were registered: event.project is a cache
the attribution pass overwrites on every harvest, with no history, so a
directory blessed today attributes its old evidence as though it always had
been. The unattributed figures below are therefore always AS OF NOW, whatever
the client mapping says about the past.

Unattributed presence is reported as three separate figures, because they mean
three different things. Evidence carrying a path that resolved to no registered
project MAY be recoverable: some of those directories can be registered, some
no longer exist and need "timetattle consolidate", and some are not project
directories at all — "timetattle orient" is what tells them apart, because this
command reads neither the registry nor the filesystem. Evidence carrying no path
at all (knowledgeC and Biome) can never be attributed this way however many
directories are blessed. And clock that no observation is near enough to
account for was bridged by the gap threshold, where nothing was recorded at all.

-by CUTS THE SPAN INTO A SERIES and prints it beneath the report, one row per
working day, week, month, or the whole span as one period. It is a RE-SLICE and
never a recomputation: the rows are cut out of the same partition every figure
above them is summed from, so they sum to the ungrouped total EXACTLY and the
report prints the difference on every run. Computing each day from its own
events instead would give a number that cannot be added up at all — a day
clustered alone gets its own lead-in and tail-out at every boundary, and a
silence bridged across midnight becomes two sittings. A sitting that really does
cross a boundary is SPLIT at it and counted once on each side.

Periods run from the working-day boundary, 05:00 local rather than midnight, so
an evening running to 01:37 is still that evening's work. Days are 23 or 25
hours long across a daylight-saving transition and the report says which.

EVERY PERIOD IN THE SPAN IS PRINTED, including the ones nothing fell in, and
each says what the store recorded about whether collection was RUNNING at the
time. A 0.0h period whose collection is "unrecorded" cannot be told apart from a
period in which nothing was gathered: capture outcomes have only been recorded
since migration 0007, and the harvest that failed silently for 44 hours in July
2026 predates it entirely.

-csv writes the series for another program instead of writing a report, one row
per period per grouping, in exact decimal seconds rather than the 0.1h the table
prints. Presence, the three unattributed reasons and clientless time each get
their own level, because none of them is a project; a cell that does not apply
at a level is EMPTY rather than zero, since an allocation of none and a question
that does not arise are different facts.

sessions.exclude names files that are machine activity rather than work, and it
is applied where the events are READ, so it reaches everything already in the
store and deletes nothing. SQLite -wal and -shm sidecars go with no
configuration at all; the configured list ships empty. What was dropped is
counted and itemised above the figures it changed.

Parameters come from ~/.timetattle/config.json, and any flag below overrides
the file for that one run without changing it. THE GAP THRESHOLD IS NOT A
SETTLED VALUE: total engaged time ranged 2.3x across the plausible range and
nothing has yet fitted it against ground truth. Use "timetattle sessionize
-sweep" to see the spread on your own data before quoting a figure.`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := fs.String("config", "", "configuration to read (default: ~/.timetattle/config.json)")
	gap := fs.Duration("gap", 0, "idle gap that separates one sitting from the next (default: from the config file)")
	leadIn := fs.Duration("lead-in", 0, "how long before its first observation a sitting is assumed to have begun")
	tailOut := fs.Duration("tail-out", 0, "how long after its last observation a sitting is assumed to have run on")
	presence := fs.String("presence-sources", "",
		"comma-separated sources read as presence, which bounds sittings without creating them")
	ignore := fs.String("ignore-sources", "", "comma-separated sources to read and drop")
	exclude := fs.String("exclude", "", excludeFlagUsage)
	from := fs.String("from", "", "earliest instant to consider, as 2026-07-11 or 2026-07-11T09:00:00Z")
	to := fs.String("to", "", "latest instant to consider; a bare date means the END of that day")
	client := fs.String("client", "", "report only this client, and only their projects")
	project := fs.String("project", "", "report only this project")
	by := fs.String("by", "", "cut the span into a series of periods: day, week, month or span")
	asCSV := fs.Bool("csv", false, "write the series as CSV instead of a report, for another program to read")
	dayStart := fs.String("day-start", "", "wall clock a working day begins at, as 05:00 (default: from the config file)")
	weekStart := fs.String("week-start", "", "weekday a week begins on, as monday (default: from the config file)")
	timezone := fs.String("timezone", "", "IANA zone the working day is local to, as America/Chicago")
	cmd.RunE = func(*cobra.Command, []string) error {
		set := flagsSet(fs)

		var err error
		if *storePath == "" {
			if *storePath, err = store.DefaultPath(); err != nil {
				return err
			}
		}
		if *configPath == "" {
			if *configPath, err = config.DefaultPath(); err != nil {
				return err
			}
		}
		cfg, err := config.Load(*configPath)
		if err != nil {
			return err
		}

		p := sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		}
		if set["gap"] {
			p.Gap = *gap
		}
		if set["lead-in"] {
			p.LeadIn = *leadIn
		}
		if set["tail-out"] {
			p.TailOut = *tailOut
		}
		if set["presence-sources"] {
			p.PresenceSources = splitCommaList(*presence)
		}
		if set["ignore-sources"] {
			p.IgnoreSources = splitCommaList(*ignore)
		}
		if err := p.Validate(); err != nil {
			return err
		}
		excludes, err := sessionExcludesFrom(cfg, *exclude, set["exclude"])
		if err != nil {
			return err
		}

		// -csv with no -by is the whole span as one period, which is a real answer
		// rather than a degenerate one: a program asking for a total wants the same
		// columns as one asking for a series.
		unit := unitSpan
		if set["by"] {
			if unit, err = parseSeriesUnit(*by); err != nil {
				return err
			}
		}
		// The calendar goes through the config types rather than being assembled
		// here, so that a flag and a file are parsed by the same code and cannot
		// disagree about what "05:00" or "monday" means. Same path sessionize uses.
		calCfg := cfg.Calendar
		if set["timezone"] {
			calCfg.Timezone = *timezone
		}
		if set["day-start"] {
			if err := calCfg.DayStart.UnmarshalJSON([]byte(strconv.Quote(*dayStart))); err != nil {
				return fmt.Errorf("-day-start: %w", err)
			}
		}
		if set["week-start"] {
			if err := calCfg.WeekStart.UnmarshalJSON([]byte(strconv.Quote(*weekStart))); err != nil {
				return fmt.Errorf("-week-start: %w", err)
			}
		}
		cal, err := calCfg.Sessions()
		if err != nil {
			return err
		}

		filter := store.Filter{}
		if filter.From, err = parseBoundary(*from, "from"); err != nil {
			return err
		}
		// A bare date on -to means the END of that day; see parseUpperBoundary for
		// why that differs from -from and why it is not a break with the shared
		// parser. -from wants the start of the day it names, which is what a bare
		// date already parses to.
		if filter.To, err = parseUpperBoundary(*to, "to"); err != nil {
			return err
		}

		// Read-only: this command has no business appearing in the store's own
		// history, and a check that CREATED or MIGRATED the database would report
		// on a schema no harvest had ever run against.
		st, err := store.OpenReadOnly(*storePath)
		if err != nil {
			return err
		}
		defer st.Close()

		events, err := st.Events(context.Background(), filter)
		if err != nil {
			return err
		}
		// Machine activity is removed before anything is clustered or attributed,
		// so a project whose only trace on a day was a log rotation does not
		// appear on it at all rather than appearing with an unexplained hour.
		events, drop := sessions.Retain(events, excludes)

		// THE CLIENT IS STAMPED HERE, not read from a column, because no column
		// holds it: the client-project log (migration 0012) is folded and each
		// event asks which client its project belonged to AT ITS OWN INSTANT. A
		// project reassigned in June therefore reports the old client for May's
		// work and the new one for July's, from one pass over ~38 rows.
		//
		// This is still the whole of what `metrics` reads — the store, and nothing
		// else. The log lives in the same database as the events, so the command's
		// designed property of consulting neither the registry nor the filesystem
		// is untouched.
		cm, err := st.ClientMap(context.Background(), time.Time{})
		if err != nil {
			return err
		}
		for i := range events {
			if events[i].Identity.Project == "" {
				continue
			}
			if client, ok := cm.At(events[i].Identity.Project, events[i].Anchor()); ok {
				events[i].Identity.Client = client
			}
		}

		report, err := metrics.Compute(events, p)
		if err != nil {
			return err
		}

		// What the store recorded about collection, read once and asked per period.
		// It is what stops a 0.0h row from being a confident nothing.
		log, err := readCollectionLog(context.Background(), st)
		if err != nil {
			return err
		}
		scope := metricsScope{Client: *client, Project: *project}
		rows, err := seriesOf(report, cal, unit, sessions.Window{From: filter.From, To: filter.To}, log)
		if err != nil {
			return err
		}

		// CSV REPLACES THE REPORT RATHER THAN JOINING IT. A data interchange format
		// with prose in it is neither, and every caveat the report carries is
		// carried in the CSV as a column instead — the collection state, whether a
		// period was only partly covered, and which level each figure belongs to.
		if *asCSV {
			return csvSeries(stdout, cal, unit, rows, scope, report.Clients)
		}

		// The report is laid out for the terminal it is going into, and clipped as a
		// backstop. columnsOf returns 0 for a pipe, which leaves both alone.
		cols := columnsOf(stdout)
		out := clipTo(stdout, cols)
		reported := reportMetrics(out, *storePath, report, cols, excludes, drop, scope, cm.Collisions())
		if set["by"] && reported {
			reportSeries(out, report, cal, unit, rows, log, scope, cols)
		}
		return nil
	}
	return cmd
}

// metricsScope is which rows the reader asked to see. It is NOT which events
// were computed over.
//
// THAT DISTINCTION IS THE WHOLE DESIGN OF THE FILTER, and getting it wrong
// would be invisible. The tempting implementation is to filter the EVENTS and
// recompute over what is left, which is wrong because PRESENCE is wall clock
// across every source at once: drop the other clients' evidence and the
// denominator shrinks, every share climbs toward 100%, and the one question
// somebody asks a single-client report — what share of a real day did this
// take — is answered with a number that cannot be anything but large.
//
// So the whole report is computed, exactly as it would be unfiltered, and the
// scope selects which rows are printed out of it. Every figure in a filtered
// row is byte for byte the figure in the unfiltered report; see
// TestFilteringSelectsRowsWithoutChangingAnyFigureInThem.
type metricsScope struct {
	Client  string
	Project string
}

// filtering reports whether the reader narrowed the report at all.
func (s metricsScope) filtering() bool { return s.Client != "" || s.Project != "" }

// showsClient reports whether a client's roll-up row belongs in the output.
func (s metricsScope) showsClient(c metrics.Client) bool {
	if s.Client != "" && c.Name != s.Client {
		return false
	}
	// A -project filter narrows the client table to the client that owns it,
	// rather than emptying it: a project's hours are still somebody's, and
	// printing the project without saying whose would answer half a question.
	if s.Project != "" && !slices.Contains(c.Projects, s.Project) {
		return false
	}
	return true
}

// showsProject reports whether a project's row belongs in the output. clients
// is the report's roll-up, which is where the project-to-client mapping lives —
// metrics reads no registry, so there is nowhere else it could come from.
func (s metricsScope) showsProject(name string, clients []metrics.Client) bool {
	if s.Project != "" && name != s.Project {
		return false
	}
	if s.Client == "" {
		return true
	}
	for _, c := range clients {
		if c.Name == s.Client {
			return slices.Contains(c.Projects, name)
		}
	}
	return false
}

// describe names what was asked for, for the line that says nothing matched.
func (s metricsScope) describe() string {
	switch {
	case s.Client != "" && s.Project != "":
		return fmt.Sprintf("client %q and project %q", s.Client, s.Project)
	case s.Client != "":
		return fmt.Sprintf("client %q", s.Client)
	default:
		return fmt.Sprintf("project %q", s.Project)
	}
}

// reportMetrics prints both metrics, and never a total without what it rests on.
//
// It reports whether it printed a BODY — figures about work — or one of the two
// sentences that stand in for one: nothing was recorded in this span, or nothing
// in it carries the name the reader filtered for. A series printed after either
// of those would put a table of confident zeroes directly underneath the
// sentence that exists to refuse exactly that reading.
func reportMetrics(out io.Writer, path string, r metrics.Report, cols int,
	excludes sessions.Excludes, drop sessions.Drop, scope metricsScope,
	contested []clientmap.Collision) bool {
	fmt.Fprintf(out, "%s\n", path)
	fmt.Fprintf(out, "gap %v, lead-in %v, tail-out %v\n", r.Params.Gap, r.Params.LeadIn, r.Params.TailOut)
	fmt.Fprintf(out, "presence: %s\n", listOrNone(r.Params.PresenceSources))
	fmt.Fprintf(out, "ignored:  %s\n", listOrNone(r.Params.IgnoreSources))
	reportExclusions(out, excludes, drop)

	if r.Presence.Total == 0 {
		fmt.Fprint(out, "\nnothing was recorded in this span, which is not the same as nothing "+
			"having happened\n")
		return false
	}

	fmt.Fprintf(out, "\nPRESENCE %s across %s, median %v, p95 %v\n",
		metricsHours(r.Presence.Total), plural(r.Sittings.Count, "sitting"),
		round(r.Sittings.Median), round(r.Sittings.P95))
	fmt.Fprintf(out, "  %s observed, %s (%.1f%%) inferred by the clustering\n",
		metricsHours(r.Presence.Observed), metricsHours(r.Presence.Margin()), 100*r.Presence.MarginShare())
	fmt.Fprint(out, "  wall clock at the machine, counted once however many projects shared it\n")

	metricsBuckets(out, r.Sittings)
	if !metricsScopeMatched(out, r, cols, scope) {
		return false
	}
	metricsCollisions(out, cols, contested)
	metricsClients(out, r, cols, scope)
	metricsProjects(out, r, cols, scope)
	metricsUnattributed(out, r, cols)

	fmt.Fprintf(out, "\n%s read: %d evidence, %d presence, %d ignored\n",
		plural(r.Stats.Evidence+r.Stats.Presence+r.Stats.Ignored, "observation"),
		r.Stats.Evidence, r.Stats.Presence, r.Stats.Ignored)
	// Wrapped rather than clipped, because all four of these are figures and the
	// last of them sat fifteen columns past an eighty-column margin. A shortened
	// line here would delete a count, which is the one thing shortening may
	// never do.
	wrap(out, "", fmt.Sprintf("observed absence: %.1fh recorded, %.1fh trimmed across %d splits, "+
		"%d observations suppressed",
		r.Stats.ObservedAbsence.Hours(), r.Stats.TrimmedByAbsence.Hours(),
		r.Stats.SplitByAbsence, r.Stats.SuppressedByAbsence), cols)
	wrap(out, "", fmt.Sprintf("contested (read as lit and dark at once): %.1fh, trimmed from nothing",
		r.Stats.Contested.Hours()), cols)
	return true
}

// metricsBuckets prints the sitting length histogram.
//
// Sixteen twenty-minute blocks and two four-hour blocks are both presence hours
// and are obviously different kinds of time — ground-truth.md finding 1 — so
// the total is never printed without the shape it came in.
func metricsBuckets(out io.Writer, d metrics.Distribution) {
	fmt.Fprint(out, "\nsitting lengths\n")
	for _, b := range d.Buckets {
		if b.Count == 0 {
			continue
		}
		fmt.Fprintf(out, "  %-14s %12s %10s\n",
			metricsBand(b), plural(b.Count, "sitting"), metricsHours(b.Total))
	}
}

// metricsBand names a histogram band the way a person reads one.
func metricsBand(b metrics.Bucket) string {
	switch {
	case b.AtLeast == 0:
		return "under " + compactDuration(b.Below)
	case b.Below == 0:
		return compactDuration(b.AtLeast) + " and over"
	default:
		return compactDuration(b.AtLeast) + " to " + compactDuration(b.Below)
	}
}

// metricsScopeMatched reports whether a filter found anything, and says so when
// it did not.
//
// A FILTER MATCHING NOTHING IS THE EASIEST PLACE IN THE TOOL TO COLLAPSE THREE
// STATES INTO TWO. "this client was engaged for no time", "this client has no
// evidence in this span" and "there is no such client" would all print as a
// table of zeroes, and a reader could not tell which they were looking at. So
// the report says what was asked for and that nothing carried that name, in the
// same words the empty-span case already uses.
func metricsScopeMatched(out io.Writer, r metrics.Report, cols int, scope metricsScope) bool {
	if !scope.filtering() {
		return true
	}
	if slices.ContainsFunc(r.Clients, scope.showsClient) {
		return true
	}
	for _, p := range r.Projects {
		if scope.showsProject(p.Name, r.Clients) {
			return true
		}
	}
	fmt.Fprint(out, "\n")
	wrap(out, "", fmt.Sprintf("nothing in this span was attributed to %s. That is not the same as "+
		"nothing having happened, and it is not the same as a figure of zero: no evidence here "+
		"carries that name at all. `timetattle register -h` says how a directory is blessed, and "+
		"the unfiltered report lists every name that does appear.", scope.describe()), cols)
	return false
}

// metricsClients prints the roll-up one level above the projects.
//
// IT IS A UNION AND NOT A SUM, which is the whole reason it is computed in
// internal/metrics rather than added up here. Absolute engaged time overlaps by
// design — that is what makes it the invoice figure — so adding three of one
// client's project rows together credits them three times for a minute worked
// once. In the live registry every client is 1:1 with its project except one,
// which spans 33, so the error would be invisible everywhere except in the one
// row anybody wanted this for.
func metricsClients(out io.Writer, r metrics.Report, cols int, scope metricsScope) {
	rows := make([][]string, 0, len(r.Clients))
	for _, c := range r.Clients {
		if !scope.showsClient(c) {
			continue
		}
		share := 0.0
		if r.Presence.Total > 0 {
			share = 100 * c.Allocated.Seconds() / r.Presence.Total.Seconds()
		}
		rows = append(rows, []string{c.Name,
			metricsHours(c.Engaged.Total), metricsHours(c.Weak),
			metricsHours(c.Engaged.Observed), metricsHours(c.Corroborated),
			metricsHours(c.Allocated), fmt.Sprintf("%.1f%%", share),
			fmt.Sprintf("%d", len(c.Projects))})
	}
	if len(rows) == 0 {
		return
	}

	fmt.Fprint(out, "\nENGAGED, per client\n")
	writeColumns(out, table{
		indent: "  ",
		gutter: " ",
		head: []string{"client", "absolute", "weak", "observed", "corroborated", "allocated",
			"share", "projects"},
		body:    rows,
		natural: []int{14, 10, 8, 10, 13, 11, 8, 9},
		right:   []bool{false, true, true, true, true, true, true, true},
		floor:   10,
		cols:    cols,
	})
	fmt.Fprint(out, "\n")
	wrap(out, "  ", "A client's absolute hours are the UNION of its projects' spans and are never "+
		"their sum: two of a client's projects worked in the same minute bill that minute to each "+
		"of them and to the client ONCE. The projects column counts how many were rolled up, and "+
		"the rows below are those projects.", cols)
	// A -project filter leaves this row showing the client's WHOLE roll-up
	// while the table beneath it shows one project, and the two figures then
	// differ for a reason a reader has to be told rather than work out. Saying
	// which projects it covers is what stops the client row being read as the
	// filtered project's client-level total.
	if scope.Project != "" {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", fmt.Sprintf("The client row above is that client's WHOLE roll-up, across "+
			"every project of theirs in this span, not the share belonging to %q. It is printed so "+
			"the project below can be seen against the client it belongs to; the two figures differ "+
			"whenever the client has more than one project.", scope.Project), cols)
	}

	// THE FOURTH FIGURE, kept apart from the three unattributed reasons below
	// on purpose. Presence a PROJECT owns and no CLIENT does is not
	// unattributed presence: the directory IS registered and it is the client
	// that is unrecorded, which calls for a different action entirely.
	if r.Clientless > 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", fmt.Sprintf("%s is attributed to a project but to NO CLIENT, and is "+
			"counted in no row above. That is not unattributed presence — those directories are "+
			"registered — and it is not a client with zero hours. Three different things produce "+
			"it: nothing has ever asserted a client for that project, or an assertion exists but "+
			"takes effect later than the work, or two directories claim the project for different "+
			"clients. `timetattle register` records the first two; the third is listed above if "+
			"it occurred.", metricsHours(r.Clientless)), cols)
	}
}

// metricsCollisions names every project two directories claim for different
// clients, before any figure that depends on it is printed.
//
// A COLLISION IS REPORTED, NEVER RESOLVED. Two directories disagreeing is not a
// correction — neither changed its mind, and there is no later-is-truer
// ordering between them — so picking the later one is last-writer-wins and
// produces an entirely plausible wrong number. The hours land in the clientless
// figure instead, and this says why, naming the directories so somebody can go
// and fix one. It is the same choice store.Attribution makes in reporting an
// unreadable locator rather than calling it unattributed.
func metricsCollisions(out io.Writer, cols int, cs []clientmap.Collision) {
	if len(cs) == 0 {
		return
	}
	fmt.Fprint(out, "\nCONTESTED PROJECTS\n")
	for _, c := range cs {
		wrap(out, "  ", fmt.Sprintf("%s is claimed by %s. Its hours are credited to no client "+
			"at all rather than to whichever registration happened to be written last. The "+
			"directories claiming it are: %s. Re-register one of them, or give it a project "+
			"name of its own.",
			c.Project, strings.Join(c.Clients, " and "), strings.Join(c.Sources, ", ")), cols)
	}
}

// metricsProjects prints per-project engaged time under both normalisations.
func metricsProjects(out io.Writer, r metrics.Report, cols int, scope metricsScope) {
	shown := make([]metrics.Project, 0, len(r.Projects))
	for _, p := range r.Projects {
		if scope.showsProject(p.Name, r.Clients) {
			shown = append(shown, p)
		}
	}
	if len(shown) == 0 {
		fmt.Fprint(out, "\nENGAGED: no project could be attributed any of this span. That is a fact "+
			"about the registry, not about the work — see the unattributed breakdown below\n")
		return
	}

	var absolute, allocated, corroborated, observed time.Duration
	var attested, weak, unaccounted time.Duration
	rows := make([][]string, 0, len(shown)+1)
	for _, p := range shown {
		absolute += p.Engaged.Total
		allocated += p.Allocated
		corroborated += p.Corroborated
		observed += p.Engaged.Observed
		attested += p.Attested
		weak += p.Weak
		unaccounted += p.Unaccounted
		share := 0.0
		if r.Presence.Total > 0 {
			share = 100 * p.Allocated.Seconds() / r.Presence.Total.Seconds()
		}
		rows = append(rows, []string{p.Name,
			metricsHours(p.Engaged.Total), metricsHours(p.Weak),
			metricsHours(p.Engaged.Observed),
			metricsHours(p.Corroborated), metricsHours(p.Allocated),
			fmt.Sprintf("%.1f%%", share), fmt.Sprintf("%d", p.Sittings.Count)})
	}
	// Every column here is summed, including observed. It was hardcoded to zero
	// when this was written, which was true of the corpus at that moment and is
	// exactly the kind of constant that stops being true without anything
	// failing: internal/jobs landed hours later emitting claim INTERVALS, and an
	// interval does observe a duration, so the honest figure became non-zero the
	// first time a job database was harvested. A printed zero would have gone on
	// asserting that nothing was observed.
	//
	// THE LABEL CHANGES WHEN THE REPORT IS FILTERED, and that is not cosmetic.
	// "sum" over a selection of the rows is a partial total, and the sentence
	// below it — that ALLOCATED sums to exactly the attributed presence — is
	// true of every project and false of some of them. A filtered report that
	// went on asserting it would be stating something it had just stopped
	// measuring.
	label := "sum"
	if scope.filtering() {
		label = "shown"
	}
	rows = append(rows, []string{label, metricsHours(absolute), metricsHours(weak),
		metricsHours(observed), metricsHours(corroborated), metricsHours(allocated)})

	fmt.Fprint(out, "\nENGAGED, per project\n")
	metricsWriteProjects(out, rows, cols)

	fmt.Fprint(out, "\n")
	if scope.filtering() {
		wrap(out, "  ", fmt.Sprintf("FILTERED to %s. Every figure above is exactly what it is in "+
			"the unfiltered report — the filter selects rows and recomputes nothing — so PRESENCE "+
			"above is still the whole span's wall clock and the share column is still a share of "+
			"it. That is deliberate: recomputing over one client's evidence alone would shrink the "+
			"denominator and drive every share toward 100%%. The \"shown\" row totals only the "+
			"rows printed.", scope.describe()), cols)
		fmt.Fprint(out, "\n")
	}
	wrap(out, "  ", "ABSOLUTE bills a shared minute in full to every project active in it, "+
		"so the column sums to more than the clock on any day two projects ran at once. "+
		"ALLOCATED splits it between them, and sums to exactly the attributed presence.", cols)
	if absolute > r.Presence.Total {
		wrap(out, "  ", fmt.Sprintf("Here absolute is %s against %s of presence — %.2fx — which "+
			"is concurrent work billing to both clients and is correct for an invoice and wrong "+
			"for capacity.", metricsHours(absolute), metricsHours(r.Presence.Total),
			absolute.Seconds()/r.Presence.Total.Seconds()), cols)
	}

	// WHAT THE HOURS REST ON, in three figures rather than two. The weak column
	// above is the middle one; it is printed beside every project's total
	// because a client whose whole trace is file modification times cannot
	// otherwise be told from one with a transcript underneath it, and the pair
	// printed identically until task KrfMh. The third figure is the one this
	// project keeps losing: clock nothing accounts for is not weak evidence,
	// it is no evidence, and folding it into either neighbour is the bug.
	fmt.Fprint(out, "\n")
	wrap(out, "  ", fmt.Sprintf("Of the %s absolute: %s is accounted for by an observation "+
		"RECORDING AN ACT — a message, a commit, a claim held; %s by nothing but a file's "+
		"modification time, which a cron writes as readily as a person does; and %s by nothing "+
		"at all, being silence the gap threshold bridged inside a project's own cluster. Weak "+
		"time is not a deduction from the total and it is not the opposite of corroborated: a "+
		"focus interval says somebody was at the machine and says nothing about which client, "+
		"so a minute can be both.", metricsHours(absolute), metricsHours(attested),
		metricsHours(weak), metricsHours(unaccounted)), cols)

	// The warning the brief for this command exists for. An engaged total that
	// is mostly margin is an inference wearing the clothes of a measurement,
	// and the difference between a defensible invoice and a fabricated one.
	if observed == 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", "WARNING: NONE of the engaged time above was observed as a duration. "+
			"Every source that can be attributed to a project — Claude transcripts, git, file "+
			"saves — records INSTANTS, and an instant observes no duration at all. Every hour "+
			"in the absolute column was inferred by the clustering from the gap, lead-in and "+
			"tail-out above. The corroborated column is the nearest thing to evidence there is: "+
			"clock some source observed the machine in use across, without saying whose work it "+
			"was.", cols)
	}
}

// metricsWriteProjects lays the per-project table out for the room there is.
//
// EVERY COLUMN BUT THE FIRST IS A FIGURE, and the sittings count sits at the
// right-hand edge: a line clipped at an eighty-column margin would have deleted
// it, and a count clipped to two digits is a wrong number rather than a short
// one. The project name is the only thing here allowed to lose a character,
// because a shortened name is still a name. See writeColumns for the order.
//
// One space between columns rather than two, which is what this table has
// always used and what a pipe therefore keeps getting.
func metricsWriteProjects(out io.Writer, rows [][]string, cols int) {
	writeColumns(out, table{
		indent: "  ",
		gutter: " ",
		head: []string{"project", "absolute", "weak", "observed", "corroborated", "allocated",
			"share", "sittings"},
		body:    rows,
		natural: []int{14, 10, 8, 10, 13, 11, 8, 9},
		right:   []bool{false, true, true, true, true, true, true, true},
		floor:   10,
		cols:    cols,
	})
}

// metricsUnattributed prints presence no project could be attributed, split by
// why, because the three reasons call for three different responses.
func metricsUnattributed(out io.Writer, r metrics.Report, cols int) {
	u := r.Unattributed
	share := 0.0
	if r.Presence.Total > 0 {
		share = 100 * u.Presence.Total.Seconds() / r.Presence.Total.Seconds()
	}
	fmt.Fprintf(out, "\nUNATTRIBUTED PRESENCE %s (%.1f%% of presence)\n",
		metricsHours(u.Presence.Total), share)
	// Deliberately no longer labelled ACTIONABLE. This command reads events and
	// nothing else: it never consults the registry and never stats a directory,
	// so "project IS NULL" is all it knows, and that is true of a directory
	// nobody has registered, a directory that no longer exists, and the locator
	// "/". Measured on the live store on 2026-08-01, after the attribution pass
	// had caught up: of the 1,743 observations behind this figure, NONE named a
	// directory register could have been pointed at — 1,311 were "/" and the
	// remainder were paths that are gone. Asserting an action here that fails
	// every time it is followed is the actionable-versus-unregisterable collapse
	// one level up from the one the three figures below already avoid.
	metricsWhy(out, cols, metricsHours(u.Unregistered), fmt.Sprintf(
		"a path resolving to no registered project, across %s. Some of those directories can "+
			"be registered and some no longer exist; telling them apart needs the registry and "+
			"the filesystem, which this command reads neither of. `timetattle orient` separates "+
			"them.", plural(u.UnregisteredEvents, "observation")))
	metricsWhy(out, cols, metricsHours(u.Unlocatable), fmt.Sprintf(
		"a source carrying no path at all, across %s. knowledgeC and Biome; registering can "+
			"never reach these.", plural(u.UnlocatableEvents, "observation")))
	metricsWhy(out, cols, metricsHours(u.Bridged),
		"silence the gap threshold bridged. Nothing was recorded here, which is not the same "+
			"as nothing having happened.")
}

// metricsWhy prints one reason presence went unattributed: the hours in a
// right-aligned column, and the explanation hanging beside them.
//
// The explanation is wrapped rather than clipped because it carries the
// observation count that makes the hours mean something, and that count is at
// the far end of the first sentence.
func metricsWhy(out io.Writer, cols int, hours, why string) {
	const gutter = 14 // "  " + a ten-column figure + "  "
	var block strings.Builder
	wrap(&block, strings.Repeat(" ", gutter), why, cols)
	text := block.String()
	fmt.Fprint(out, "  "+strings.Repeat(" ", max(0, 10-len([]rune(hours))))+hours+"  "+
		strings.TrimPrefix(text, strings.Repeat(" ", gutter)))
}

// metricsHours renders a duration the way every other figure in project/ is
// quoted, so two documents can be compared without converting first.
func metricsHours(d time.Duration) string { return fmt.Sprintf("%.1fh", d.Hours()) }
