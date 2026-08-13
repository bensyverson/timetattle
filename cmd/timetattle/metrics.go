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

	"github.com/bensyverson/timetattle/internal/adjustments"
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
func metricsCommand(args []string) error { return metricsWatching(os.Stdout, os.Stderr, args) }

// metricsTo is metricsCommand with the report redirected, so that what the
// command prints can be asserted on rather than merely not crashing.
//
// Its stderr is discarded, which also means the progress display is off: a
// discarded writer is not a terminal. A test that wants the commentary uses
// metricsWatching and hands it a writer that reports a width.
func metricsTo(stdout io.Writer, args []string) error {
	return metricsWatching(stdout, io.Discard, args)
}

// metricsWatching is metricsCommand with BOTH streams redirected.
//
// The two are separate because they carry different things and only one of them
// is a contract. Stdout is the report, and the primary reader of this CLI is an
// agent capturing it; stderr is commentary about a run that takes long enough
// for a person to wonder whether it has hung. Nothing may cross between them.
func metricsWatching(stdout, stderr io.Writer, args []string) error {
	return executeSubcommand(metricsCmd(stdout, stderr), args)
}

func metricsCmd(stdout, stderr io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "metrics",
		Short: "Report engaged time per project, and presence",
		Long: `Reports engaged time per client and per project, and presence overall, from
the events already in the store, which is opened read-only. ENGAGED time
overlaps between projects and is the billing figure; PRESENCE is wall clock,
never double-counted, and is the capacity denominator. Parameters come from
~/.timetattle/config.json and any flag below overrides it for one run.`,
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
	all := fs.Bool("all", false, "report every client and project, ignoring the working directory's scope")
	indexPath := fs.String("index", "",
		"registry index to resolve the working directory's scope against (default: ~/.timetattle/registered-directories.json)")
	by := fs.String("by", "", "cut the span into a series of periods: day, week, month or span")
	asCSV := fs.Bool("csv", false, "write the series as CSV instead of a report, for another program to read")
	dayStart := fs.String("day-start", "", "wall clock a working day begins at, as 05:00 (default: from the config file)")
	weekStart := fs.String("week-start", "", "weekday a week begins on, as monday (default: from the config file)")
	timezone := fs.String("timezone", "", "IANA zone the working day is local to, as America/Chicago")
	uncorrected := fs.Bool("uncorrected", false,
		"report the machine's answer alone, ignoring every attribution correction in the store")
	timings := fs.Bool("timings", false,
		"print how long each phase of the run took, to stderr, after the report")
	cmd.RunE = func(*cobra.Command, []string) error {
		set := flagsSet(fs)
		// Opened before anything is read, so the total the timings are shares of
		// is the whole command and not the part somebody remembered to wrap.
		watch := newMetricsWatch(stderr, *timings)
		// A run that fails part-way must not leave a half-drawn phase on the
		// screen above the error. finish is idempotent, so the explicit call
		// before the report is printed is not undone by this one.
		defer watch.finish()

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

		scope := metricsScope{Client: *client, Project: *project}
		// DEFAULT SCOPE IS THE WORKING DIRECTORY, resolved through the registry —
		// not applied when the reader already narrowed the report explicitly with
		// -client or -project, and not applied at all under -all. An unregistered,
		// unrelated directory refuses rather than silently reporting everything;
		// see resolveWorkingScope. Resolved here, before the store is even opened,
		// so that refusal costs nothing: it needs no event, and this command's
		// hot loop is the clustering below, not the registry.
		if !*all && !set["client"] && !set["project"] {
			ws, err := workingDirectoryScope(*indexPath)
			if err != nil {
				return err
			}
			scope.Client, scope.Project = ws.Client, ws.Project
		}

		// Read-only: this command has no business appearing in the store's own
		// history, and a check that CREATED or MIGRATED the database would report
		// on a schema no harvest had ever run against.
		st, err := store.OpenReadOnly(*storePath)
		if err != nil {
			return err
		}
		defer st.Close()

		watch.Begin(metricsPhaseRead, 0)
		events, err := st.Events(context.Background(), filter)
		watch.Done(metricsPhaseRead)
		if err != nil {
			return err
		}
		watch.count(metricsPhaseRead, len(events))
		// Machine activity is removed before anything is clustered or attributed,
		// so a project whose only trace on a day was a log rotation does not
		// appear on it at all rather than appearing with an unexplained hour.
		watch.Begin(metricsPhaseRetain, 0)
		events, drop := sessions.Retain(events, excludes)
		watch.Done(metricsPhaseRetain)
		watch.count(metricsPhaseRetain, len(events))

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
		watch.Begin(metricsPhaseStamp, 0)
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
		watch.Done(metricsPhaseStamp)

		// THE HUMAN CORRECTION LAYER, read here rather than merely stored.
		//
		// The store has held 'attributed' and 'unattributed' assertions since
		// migration 0008 and `timetattle review` has been able to produce one
		// from the block walk for as long; until this call existed nothing read
		// either, so a person who reattributed a span saw their correction
		// recorded faithfully, survive re-derivation exactly as promised, and
		// change no figure they could see. Silence is indistinguishable from
		// agreement, which is why the report now states the population below
		// even when it is empty.
		//
		// ONLY THE ATTRIBUTION HALF IS APPLIED. Engaged and not-engaged
		// assertions travel in the same log and move the capacity denominator;
		// internal/fit measures the clustering against exactly that difference,
		// so absorbing them here would shrink the model's own error signal for
		// reasons having nothing to do with the model. See metrics.Corrected.
		//
		// The window is the EVENT window, and the store's test is overlap
		// rather than containment: a correction beginning before it and running
		// into it describes time inside it. One recorded wholly outside cannot
		// touch an instant inside, because an assertion is keyed to wall clock
		// and nothing else.
		var said []metrics.Attribution
		if !*uncorrected {
			watch.Begin(metricsPhaseCorrections, 0)
			recorded, err := st.Adjustments(context.Background(), filter.From, filter.To)
			if err != nil {
				return err
			}
			said = adjustments.Fold(nil, recorded).Attributions()
			watch.Done(metricsPhaseCorrections)
		}

		report, err := metrics.Watched(events, p, said, watch)
		if err != nil {
			return err
		}

		watch.Begin(metricsPhaseFormat, 0)
		// What the store recorded about collection, read once and asked per period.
		// It is what stops a 0.0h row from being a confident nothing.
		log, err := readCollectionLog(context.Background(), st)
		if err != nil {
			return err
		}
		// filter.To is store.Filter's own INCLUSIVE closing instant for the
		// EVENT QUERY, which is a different question from where the DAY
		// SERIES stops: seriesOf's window.To is the EXCLUSIVE boundary that
		// convention, and a -to landing exactly on a working day's opening
		// instant (as a bare -to date already resolves one microsecond short
		// of, and as a trailing window computed from cal.Bounds does exactly)
		// must not pull that day's own zero-width touch into the series as a
		// row of its own. See seriesOf's doc comment in series.go.
		rows, err := seriesOf(report, cal, unit,
			sessions.Window{From: filter.From, To: filter.To}, log)
		if err != nil {
			return err
		}

		// CSV REPLACES THE REPORT RATHER THAN JOINING IT. A data interchange format
		// with prose in it is neither, and every caveat the report carries is
		// carried in the CSV as a column instead — the collection state, whether a
		// period was only partly covered, and which level each figure belongs to.
		// THE PROGRESS LINE IS ERASED BEFORE A SINGLE BYTE REACHES STDOUT. If the
		// two streams share a terminal, a half-drawn "segmenting" left standing
		// would sit above a finished report saying the run had not finished.
		watch.finish()

		if *asCSV {
			if err := csvSeries(stdout, cal, unit, rows, scope, report.Clients); err != nil {
				return err
			}
			watch.Done(metricsPhaseFormat)
			watch.report()
			return nil
		}

		// The report is laid out for the terminal it is going into, and clipped as a
		// backstop. columnsOf returns 0 for a pipe, which leaves both alone.
		cols := columnsOf(stdout)
		out := clipTo(stdout, cols)
		reported := reportMetrics(out, *storePath, report, cols, excludes, drop, scope,
			cm.Collisions(), *uncorrected)
		if set["by"] && reported {
			reportSeries(out, report, cal, unit, rows, log, scope, cols)
		}
		watch.Done(metricsPhaseFormat)
		watch.report()
		return nil
	}
	return cmd
}

// The stages of a metrics run that internal/metrics cannot see, named in the
// same open vocabulary its own phases use so that one table covers the whole
// command. See internal/metrics/watch.go for why Phase is a bare string.
const (
	metricsPhaseRead        metrics.Phase = "reading events"
	metricsPhaseRetain      metrics.Phase = "dropping machine activity"
	metricsPhaseStamp       metrics.Phase = "stamping clients"
	metricsPhaseCorrections metrics.Phase = "reading corrections"
	metricsPhaseFormat      metrics.Phase = "formatting"
)

// metricsWatch says what a metrics run is doing while it does it, and what each
// part of it cost afterwards.
//
// # Why a run says anything at all
//
// An unwindowed run over a real store is not instant, and a silent command is
// indistinguishable from a hung one — which is exactly how this came to be
// written, from a run read as a hang. So each phase names itself on the way
// past, on a single line that rewrites itself and then erases itself, leaving
// the terminal as it found it.
//
// # Why it is a terminal-only display and a flag-only table
//
// The commentary is for a person watching a run, and there is no person on the
// other end of a pipe. Redrawing a line into a log file writes carriage returns
// nobody reads, and scripts/profile captures precisely that stream. So the live
// display is on only when stderr is a terminal, and the timings table only when
// it was asked for.
//
// NEITHER EVER TOUCHES STDOUT. The primary consumer of this CLI is an agent
// capturing the report, and a run's speed is not part of the report.
//
// # Why the timings ship rather than being re-instrumented each time
//
// Every figure this project has recorded about where a metrics run goes came
// from an agent temporarily editing internal/metrics and throwing the edit
// away, which is a measurement nobody else can reproduce and nobody can re-run
// after a change. One such figure — that the client roll-up was about two
// fifths of a run — stood for a week and was wrong by the time anybody looked,
// because four optimisations had landed underneath it. This is the instrument
// that replaces it: the same table, from the shipped binary, at any time.
type metricsWatch struct {
	out io.Writer
	// live says stderr is a terminal, so the phase line may be drawn.
	live bool
	// timings says the reader asked for the table.
	timings bool
	width   int

	started time.Time
	begun   map[metrics.Phase]time.Time
	took    map[metrics.Phase]time.Duration
	steps   map[metrics.Phase]int
	// order is the phases in the order they first began, which is the order the
	// table prints them: the run's own order is the one a reader is looking for
	// and it cannot go stale the way a hand-written list would.
	order []metrics.Phase
	drawn bool
}

func newMetricsWatch(stderr io.Writer, timings bool) *metricsWatch {
	cols := columnsOf(stderr)
	return &metricsWatch{
		out:     stderr,
		live:    cols > 0,
		timings: timings,
		width:   cols,
		started: time.Now(),
		begun:   map[metrics.Phase]time.Time{},
		took:    map[metrics.Phase]time.Duration{},
		steps:   map[metrics.Phase]int{},
	}
}

func (w *metricsWatch) Begin(p metrics.Phase, steps int) {
	if _, seen := w.begun[p]; !seen {
		w.order = append(w.order, p)
	}
	w.begun[p] = time.Now()
	w.steps[p] = steps
	w.draw(p, 0)
}

func (w *metricsWatch) Step(p metrics.Phase, done int) { w.draw(p, done) }

// count says how much a phase turned out to have worked through, for a phase
// that could not know before it ran. The store read is the case: how many rows
// come back is the answer, not the question, and it is the one number that
// makes a recorded timing comparable across a harvest.
func (w *metricsWatch) count(p metrics.Phase, n int) { w.steps[p] = n }

func (w *metricsWatch) Done(p metrics.Phase) {
	if at, ok := w.begun[p]; ok {
		// Accumulated rather than assigned: a phase entered twice is one phase
		// costing the sum of both, which is what a reader of the table wants.
		w.took[p] += time.Since(at)
		delete(w.begun, p)
	}
}

// draw rewrites the single progress line. A phase that announced how many units
// it has counts itself, because "client roll-up" alone cannot tell a slow
// client from a stuck one.
func (w *metricsWatch) draw(p metrics.Phase, done int) {
	if !w.live {
		return
	}
	line := string(p)
	if total := w.steps[p]; total > 0 {
		line = fmt.Sprintf("%s %d of %d", p, done, total)
	}
	line = clip(line, w.width-1)
	// Padded to the previous line's width so that a shorter phase name does not
	// leave the tail of a longer one behind it.
	fmt.Fprintf(w.out, "\r%-*s", w.width-1, line)
	w.drawn = true
}

// finish erases the progress line, and is safe to call more than once. It has
// to run before anything reaches stdout, and again on the way out of a failed
// run, so idempotence is the property rather than a convenience.
func (w *metricsWatch) finish() {
	if !w.drawn {
		return
	}
	fmt.Fprintf(w.out, "\r%-*s\r", w.width-1, "")
	w.drawn = false
}

// report prints the per-phase table, if it was asked for.
//
// A TABLE THAT DOES NOT ADD UP IS WORSE THAN NO TABLE, so whatever the phases
// failed to account for — opening the store, reading the config, resolving the
// working directory's scope, and the process's own startup — is printed as its
// own row rather than left as a difference a reader has to notice and subtract.
func (w *metricsWatch) report() {
	if !w.timings {
		return
	}
	total := time.Since(w.started)

	rows := make([][2]string, 0, len(w.order)+2)
	var accounted time.Duration
	for _, p := range w.order {
		took := w.took[p]
		accounted += took
		name := string(p)
		if n := w.steps[p]; n > 0 {
			name = fmt.Sprintf("%s (%d)", p, n)
		}
		rows = append(rows, [2]string{name, metricsTiming(took, total)})
	}
	rows = append(rows,
		[2]string{"elsewhere", metricsTiming(total-accounted, total)},
		[2]string{"total", metricsTiming(total, total)})

	var widest int
	for _, r := range rows {
		widest = max(widest, len(r[0]))
	}
	fmt.Fprintf(w.out, "\ntimings for this run, by phase; a count in brackets is what the phase "+
		"worked through:\n")
	for _, r := range rows {
		fmt.Fprintf(w.out, "  %-*s  %s\n", widest, r[0], r[1])
	}
}

// metricsTiming is one cell of the table: how long, and what share of the run.
func metricsTiming(d, total time.Duration) string {
	var share float64
	if total > 0 {
		share = 100 * d.Seconds() / total.Seconds()
	}
	return fmt.Sprintf("%8.3fs  %5.1f%%", d.Seconds(), share)
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
	contested []clientmap.Collision, uncorrected bool) bool {
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

	metricsBuckets(out, r.Sittings)
	if !metricsScopeMatched(out, r, cols, scope) {
		return false
	}
	metricsCollisions(out, cols, contested)
	metricsClients(out, r, cols, scope)
	metricsProjects(out, r, cols, scope)
	metricsUnattributed(out, r, cols)
	metricsCorrections(out, r, cols, uncorrected)

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
	wrap(out, "", fmt.Sprintf("nothing in this span was attributed to %s, which is not a figure of "+
		"zero: no evidence here carries that name at all. The unfiltered report lists every name "+
		"that does appear.", scope.describe()), cols)
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
	// A -project filter leaves this row showing the client's WHOLE roll-up
	// while the table beneath it shows one project, and the two figures then
	// differ for a reason a reader has to be told rather than work out. Saying
	// which projects it covers is what stops the client row being read as the
	// filtered project's client-level total.
	if scope.Project != "" {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", fmt.Sprintf("the client row is that client's WHOLE roll-up, not the share "+
			"belonging to %q", scope.Project), cols)
	}

	// THE FOURTH FIGURE, kept apart from the three unattributed reasons below
	// on purpose. Presence a PROJECT owns and no CLIENT does is not
	// unattributed presence: the directory IS registered and it is the client
	// that is unrecorded, which calls for a different action entirely — so the
	// figure is stated on its own rather than folded into either neighbour.
	if r.Clientless > 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", fmt.Sprintf("%s is attributed to a project but to NO CLIENT, and is "+
			"counted in no row above. `timetattle register` is what records a client.",
			metricsHours(r.Clientless)), cols)
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
		wrap(out, "  ", fmt.Sprintf("%s is claimed by %s, so its hours are credited to no client "+
			"at all. Claimed by: %s. Re-register one of them, or give it a project name of its own.",
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
			"about the registry, not about the work — see UNATTRIBUTED PRESENCE below\n")
		return
	}

	var absolute, allocated, corroborated, observed time.Duration
	var attested, weak, unaccounted, asserted time.Duration
	rows := make([][]string, 0, len(shown)+1)
	for _, p := range shown {
		absolute += p.Engaged.Total
		allocated += p.Allocated
		corroborated += p.Corroborated
		observed += p.Engaged.Observed
		attested += p.Attested
		weak += p.Weak
		unaccounted += p.Unaccounted
		asserted += p.Asserted
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
		// The label on the totals row changes to "shown" under a filter, and a
		// reader who has not noticed would read a partial total as the whole.
		wrap(out, "  ", fmt.Sprintf("FILTERED to %s: rows are selected and nothing is recomputed, "+
			"so the \"shown\" row totals only the rows printed.", scope.describe()), cols)
		fmt.Fprint(out, "\n")
	}
	if absolute > r.Presence.Total {
		wrap(out, "  ", fmt.Sprintf("absolute %s against %s of presence, %.2fx: concurrent work, "+
			"billed to each project", metricsHours(absolute), metricsHours(r.Presence.Total),
			absolute.Seconds()/r.Presence.Total.Seconds()), cols)
	}

	// WHAT THE HOURS REST ON, in three figures rather than two. The weak column
	// above is the middle one; it is printed beside every project's total
	// because a client whose whole trace is file modification times cannot
	// otherwise be told from one with a transcript underneath it, and the pair
	// printed identically until task KrfMh. The third figure is the one this
	// project keeps losing: clock nothing accounts for is not weak evidence,
	// it is no evidence, and folding it into either neighbour is the bug. So all
	// three are printed, and the sentence they are printed in is one line.
	// The unaccounted remainder has TWO sources once anybody has corrected a
	// report, and they are opposite kinds of ignorance: silence nothing was
	// recorded in, and a stretch somebody looked at and told us whose it was.
	// One sentence naming both is what stops a person's word being read as
	// bridged silence — the same collapse the three figures above already
	// avoid, arriving through the correction layer instead of through a source.
	remainder := "%s unaccounted (bridged silence)"
	if asserted > 0 {
		remainder = "%s unaccounted, of which " + metricsHours(asserted) +
			" is a person's assertion and the rest bridged silence"
	}
	wrap(out, "  ", fmt.Sprintf("of %s absolute: %s attested, %s weak (a file's modification "+
		"time only), "+remainder, metricsHours(absolute),
		metricsHours(attested), metricsHours(weak), metricsHours(unaccounted)), cols)

	// The warning the brief for this command exists for. An engaged total that
	// is mostly margin is an inference wearing the clothes of a measurement,
	// and the difference between a defensible invoice and a fabricated one.
	if observed == 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", "WARNING: NONE of the engaged time above was observed as a duration. "+
			"Every hour in the absolute column was inferred by the clustering.", cols)
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
		"a path resolving to no registered project, across %s. `timetattle orient` says which "+
			"of them can be registered.", plural(u.UnregisteredEvents, "observation")))
	metricsWhy(out, cols, metricsHours(u.Unlocatable), fmt.Sprintf(
		"a source carrying no path at all, across %s. knowledgeC and Biome; registering can "+
			"never reach these.", plural(u.UnlocatableEvents, "observation")))
	// The fourth reason, printed only once somebody has recorded one. It is the
	// only one of the four that is not a failure to attribute: the other three
	// say the machine could not place the hour, and this says a person placed
	// it — with nobody. An always-printed 0.0h would read as a bucket something
	// was expected to fall into.
	if u.Asserted > 0 {
		metricsWhy(out, cols, metricsHours(u.Asserted),
			"a person having looked and said it belonged to no project. That is an observation, "+
				"not a gap in the registry, and no amount of registering will change it.")
	}
	metricsWhy(out, cols, metricsHours(u.Bridged),
		"silence the gap threshold bridged. Nothing was recorded here, which is not the same "+
			"as nothing having happened.")
}

// metricsCorrections says what the human correction layer did to the figures
// above, INCLUDING when it did nothing.
//
// It prints on every report, and the empty case is the whole reason. A reader
// who has recorded a correction and sees no section cannot tell whether it was
// applied and agreed, applied and landed on clock no sitting covers, or never
// read at all — and this command spent its whole life until now in the third
// state without saying so. A clean report is indistinguishable from a correct
// one unless the report names the population it checked.
func metricsCorrections(out io.Writer, r metrics.Report, cols int, uncorrected bool) {
	c := r.Corrections
	fmt.Fprint(out, "\n")
	if uncorrected {
		wrap(out, "", "CORRECTIONS were not read: -uncorrected reports the machine's answer alone. "+
			"Drop the flag to lay any recorded attribution corrections over it.", cols)
		return
	}
	if !c.Any() {
		wrap(out, "", "CORRECTIONS: none recorded over this span, so every figure above is the "+
			"machine's own answer. `timetattle review` records one against a block; "+
			"`timetattle adjust -assert attributed` records one directly.", cols)
		return
	}

	fmt.Fprintf(out, "CORRECTIONS %s applied\n", plural(c.Assertions, "attribution assertion"))
	if c.Attributed > 0 {
		metricsWhy(out, cols, metricsHours(c.Attributed),
			"moved to a project somebody named. Those hours are that project's on a person's "+
				"word and nothing else's, and they are counted in its asserted column.")
	}
	if c.Unattributed > 0 {
		metricsWhy(out, cols, metricsHours(c.Unattributed),
			"moved to NO project, by somebody who looked. It is in unattributed presence above, "+
				"under its own reason rather than under an unregistered directory.")
	}
	if c.Agreed > 0 {
		metricsWhy(out, cols, metricsHours(c.Agreed),
			"agreed with what the machine had already said, and moved nothing. That is not the "+
				"same as nobody having looked.")
	}
	// THE ONE A READER WILL OTHERWISE MISREAD AS THE TOOL IGNORING THEM. An
	// attribution assertion says whose an hour was, never that there was one,
	// so one laid across a stretch the clustering does not reach moves nothing
	// whatever — and the fix is a different assertion, which is worth saying
	// here rather than leaving somebody to re-record the same correction.
	if c.Outside > 0 {
		metricsWhy(out, cols, metricsHours(c.Outside),
			"fell outside every sitting and moved nothing. An attribution says whose an hour "+
				"was, never that there was one; `timetattle adjust -assert engaged` is what "+
				"makes the hour exist.")
	}
	if c.Moved() == 0 {
		fmt.Fprint(out, "\n")
		wrap(out, "  ", "NOTHING ABOVE MOVED. Every assertion either agreed with the machine or "+
			"reached clock no sitting covers.", cols)
	}
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
