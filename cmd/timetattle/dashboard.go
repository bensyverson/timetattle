package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// dashboard is the human-facing sibling of metrics: same underlying data
// (dashboardData below is assembled the same way metrics.go assembles its
// own report — see its doc comment), free to use box drawing and 24-bit
// color where the terminal supports it. metrics stays the terse,
// stable, machine-facing surface; nothing here changes what it computes or
// prints. See design principle 2 in project/2026-08-07-cli-overhaul-plan.md.
//
// # The section list is the load-bearing design of this file
//
// dashboardSections, below, is a fixed, ordered list of (title, render
// function) pairs. Seven sibling tasks each fill in exactly one render
// function's body, in that section's OWN file — dashboard_heatmap.go,
// dashboard_freshness.go, dashboard_stats.go, dashboard_allocation.go,
// dashboard_timeofday.go, dashboard_sources.go, dashboard_filetypes.go — plus
// that file's own test file. THIS FILE IS NEVER EDITED TO FILL A SECTION IN:
// the function signature already exists, the slot in the order already
// exists, and a section that starts as a one-line placeholder and ends as a
// stacked bar chart is still called exactly the same way. That is what makes
// the seven tasks safe to run in parallel with disjoint file sets — the
// conflict this task exists to design away.
func dashboardCommand(args []string) error { return dashboardTo(os.Stdout, args) }

// dashboardTo is dashboardCommand with the report redirected, so tests can
// assert on it — the same seam metricsTo gives metricsCommand.
func dashboardTo(stdout io.Writer, args []string) error {
	return executeSubcommand(dashboardCmd(stdout), args)
}

// defaultDashboardDays is -days's default absent an override: a full trailing
// year, which is what the activity heatmap's GitHub-style year grid (design
// principle 5) needs to have anything to draw.
const defaultDashboardDays = 365

func dashboardCmd(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Render a human dashboard of engaged time and presence",
		Long: `The human-facing sibling of metrics: the same underlying data, laid out with
box drawing and color where the terminal supports it, in a fixed trailing
window rather than metrics' -from/-to. Scoped to the working directory by
default, exactly like metrics — see resolveWorkingScope in scope.go, which
both commands share.`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := fs.String("config", "", "configuration to read (default: ~/.timetattle/config.json)")
	days := fs.Int("days", defaultDashboardDays, "trailing days to report, ending today")
	client := fs.String("client", "", "report only this client, and only their projects")
	project := fs.String("project", "", "report only this project")
	all := fs.Bool("all", false, "report every client and project, ignoring the working directory's scope")
	indexPath := fs.String("index", "",
		"registry index to resolve the working directory's scope against (default: ~/.timetattle/registered-directories.json)")
	detailed := fs.Bool("detailed", false,
		"include sections too dense for every run, such as SOURCE FRESHNESS (omitted by default)")
	cmd.RunE = func(*cobra.Command, []string) error {
		set := flagsSet(fs)
		if *days < 1 {
			return fmt.Errorf("-days %d: must be at least 1, since a window of zero or fewer days "+
				"describes no report at all", *days)
		}

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

		// Clustering parameters come from the config file alone, with none of
		// metrics' -gap/-lead-in/-tail-out/-presence-sources/-ignore-sources
		// overrides. dashboard is a human's view of what is already
		// configured, not a second place to re-tune the clustering — a
		// reader who wants a different gap threshold has metrics -gap and
		// `timetattle fit` for that, and giving dashboard its own copy of
		// six flags nobody asked for would be exactly the terseness pass's
		// prose-in-the-wrong-place mistake turned into flags instead of
		// sentences.
		p := sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		}
		if err := p.Validate(); err != nil {
			return err
		}
		excludes, err := sessionExcludesFrom(cfg, "", false)
		if err != nil {
			return err
		}
		cal, err := cfg.Calendar.Sessions()
		if err != nil {
			return err
		}
		period := dashboardPeriodOf(*days, cal, time.Now())

		scope := metricsScope{Client: *client, Project: *project}
		// DEFAULT SCOPE IS THE WORKING DIRECTORY, resolved through the SAME
		// helper metrics uses — see scope.go's own doc comment for the full
		// rule (nearest marker wins, an unregistered directory refuses
		// rather than silently reporting everything). Resolved before the
		// store is even opened, exactly as metrics.go does it, so the
		// refusal costs nothing.
		if !*all && !set["client"] && !set["project"] {
			ws, err := workingDirectoryScope(*indexPath)
			if err != nil {
				return err
			}
			scope.Client, scope.Project = ws.Client, ws.Project
		}

		// Read-only, for the reason metricsCommand's own doc comment gives:
		// a dashboard is an aggregation over what harvest already stored,
		// never a rebuild of anything.
		st, err := store.OpenReadOnly(*storePath)
		if err != nil {
			return err
		}
		defer st.Close()

		ctx := context.Background()
		filter := store.Filter{From: period.From, To: period.To}
		events, err := st.Events(ctx, filter)
		if err != nil {
			return err
		}
		events, drop := sessions.Retain(events, excludes)

		// THE CLIENT IS STAMPED HERE, exactly as metrics.go stamps it — see
		// its own comment for why this is still the whole of what is read
		// (the store, and nothing else) and why a column cannot hold it.
		cm, err := st.ClientMap(ctx, time.Time{})
		if err != nil {
			return err
		}
		for i := range events {
			if events[i].Identity.Project == "" {
				continue
			}
			if c, ok := cm.At(events[i].Identity.Project, events[i].Anchor()); ok {
				events[i].Identity.Client = c
			}
		}

		report, err := metrics.Compute(events, p)
		if err != nil {
			return err
		}
		log, err := readCollectionLog(ctx, st)
		if err != nil {
			return err
		}

		data := &dashboardData{
			Store:     st,
			Config:    cfg,
			Params:    p,
			Filter:    filter,
			Excludes:  excludes,
			Drop:      drop,
			Events:    events,
			Report:    report,
			ClientMap: cm,
			Log:       log,
		}
		// CLAMPING HAPPENS ONCE, HERE, BEFORE ANY SECTION RENDERS — see
		// clampPeriodToFirstObservation's own doc comment and dashboardPeriod's
		// for why this needs no query beyond the one already run above.
		period = clampPeriodToFirstObservation(period, scope, data)
		cc := detectColorCapability(stdout, os.LookupEnv)
		return renderDashboard(ctx, stdout, scope, period, cc, data, *detailed)
	}
	return cmd
}

// dashboardData is the read-only handle every section renders from — the
// same primitives metrics.go itself assembles for one report, not a second
// data layer for the dashboard alone. Every field here is something
// metrics.go already reads or computes; see dashboardCmd's RunE above, which
// builds this the same way.
//
// A section that needs a query metrics never runs at all — source freshness
// (store.Freshness), or filetype breakdown, which needs whatever
// internal/filedays already exposes — calls Store directly, exactly the way
// metrics.go itself reaches past its own Events() call to st.ClientMap,
// st.Captures and st.Runs. That is "do not invent a new data layer" in
// practice: the one data layer is the store, and this struct is a cache of
// what the aggregate report already cost to compute, not a wall between a
// section and the store underneath it.
type dashboardData struct {
	// Store is open READ-ONLY for the lifetime of the render; a section may
	// query it further but must never write through it.
	Store  *store.Store
	Config *config.Config
	// Params and Excludes are what Events was clustered and filtered with,
	// exactly as metrics.Report.Params carries them for the same reason: a
	// figure quoted apart from the parameters that produced it means
	// nothing.
	Params   sessions.Params
	Excludes sessions.Excludes
	// Filter is the store query the period resolved to — From/To, half-open.
	Filter store.Filter
	// Drop is what Excludes removed before anything downstream saw it.
	Drop sessions.Drop
	// Events is every retained, client-stamped observation in the period —
	// the exact slice metrics.Compute below was run over. A section wanting
	// per-source, per-kind detail metrics.Report does not carry (a commit
	// count, a LOC delta, a file touched) reads this rather than re-querying
	// the store, since it is already exactly what the aggregate below rests
	// on.
	Events []source.Event
	// Report is metrics.Compute(Events, Params): the SAME aggregate
	// metrics itself would print for this store and this period, unfiltered
	// by scope — see metricsScope's own doc comment for why the whole report
	// is always computed and only the PRINTED rows are ever narrowed. A
	// section that filters by scope must use metricsScope's own
	// showsClient/showsProject, not recompute anything, for exactly the
	// reason metrics.go's own comment there gives.
	Report metrics.Report
	// ClientMap is the client-project log Report's own client stamping
	// already consulted, exposed so a section can ask the same question
	// (clientmap.Map.At) about an instant Report itself does not carry, and
	// Collisions() for the same contested-project reporting metrics gives.
	ClientMap clientmap.Map
	// Log is every capture and harvest run this store holds, unbounded — see
	// collectionLog's own doc comment in series.go. Log.during(start, end)
	// is how a section tells an UNRECORDED period (nothing was being
	// collected) from an OK one where collection ran and simply found
	// nothing, which is design principle 3 in the CLI overhaul plan stated
	// as code rather than as a rule for someone to remember.
	Log collectionLog
}

// dashboardPeriod is the reporting window: Days trailing WORKING days ending
// today, tiled by the same Calendar metrics' own -by day series uses (see
// series.go), so a day boundary here and a day boundary in `metrics -by day`
// never disagree about where one day ends and the next begins.
//
// # From narrows to the scope's first observation — never earlier than -days
//
// -days alone describes a REQUEST (trailing N days ending today) and knows
// nothing about when the scope itself began. Run in a young project, the
// dashboard's year-long default drew months of unobserved cells before the
// project existed at all — Ben's dashboard feedback, 2026-08-07, item 2.
// dashboardCmd's RunE calls clampPeriodToFirstObservation, below, once,
// before any section renders, and every section (and the header) sees only
// the result — see that function's own doc comment for exactly what "first
// observation" means and for the -all case.
//
// An earlier draft of this file argued against carrying ANY observation
// floor, on the grounds that a correctly SCOPED one (as opposed to the whole
// store's earliest, which belongs to whichever project happened to start
// collecting first — store.LocatorCounts, already used by `timetattle
// orient`) would need a new project-filtered MIN(occurred_at) query with no
// equivalent in metrics.go: precisely the "bolted-on store query" the
// original brief said to avoid. That objection dissolves once the floor is
// bounded to the window -days already asked for, rather than the whole
// store's history: the events dashboardCmd already fetched for [From, To)
// are exactly the evidence needed to find the earliest IN-SCOPE anchor
// inside that window, with no query beyond the one already run. What
// remains true, and is still why there is no field here naming an UNBOUNDED
// earliest observation, is dashboardData.Log: collection is a property of
// the STORE and not of any one project (a capture or harvest run does not
// name a project), so collectionLog needs no per-project version at all.
// Log.during(start, end) tells UNRECORDED (nothing was being collected) from
// OK (collection ran, found nothing) for any period a section asks about,
// which is design principle 3's own question and is a different one from
// "when did this scope's own data begin" — the question From's narrowing
// answers. Sections needing the per-day answer — the heatmap most of all —
// call data.Log.during(dayStart, dayEnd) directly, unaffected by clamping.
type dashboardPeriod struct {
	// Days is what -days asked for. Clamping never changes it — a reader
	// comparing what was asked against what was used needs both, and the
	// header prints both for exactly that reason.
	Days int
	// From and To are the resolved, half-open instant window: [From, To).
	// From may be later than -days alone would have computed; see Clamped.
	From, To time.Time
	// Clamped is true when From was narrowed below the raw -days window
	// because the scope's first in-scope observation fell later than that
	// window's own start. false means From is exactly what -days computed,
	// unchanged.
	Clamped bool
}

// dashboardPeriodOf resolves days trailing working days ending on the day now
// falls in, using cal to find both boundaries — the same DayOf/Bounds pair
// series.go's own day-by-day cutting uses, so "today" and "N days ago" mean
// the same civil day here as they do in a `metrics -by day` report run at the
// same instant.
func dashboardPeriodOf(days int, cal sessions.Calendar, now time.Time) dashboardPeriod {
	today := cal.DayOf(now)
	_, to := cal.Bounds(today)
	from, _ := cal.Bounds(today.AddDays(-(days - 1)))
	return dashboardPeriod{Days: days, From: from, To: to}
}

// clampPeriodToFirstObservation narrows period.From to the earliest in-scope
// event's Anchor, when that instant falls later than period.From already
// is — dashboardPeriod's own doc comment names why this field exists at
// all; this is what computes it.
//
// # Derived from data already assembled, never a new store query
//
// data.Events is exactly what dashboardCmd already queried for
// [period.From, period.To) — see dashboardData's own doc comment on
// reaching for what the pipeline already computed rather than adding a
// query of its own. Because every event here already falls inside the
// window, the earliest in-scope anchor found can only ever be AT OR AFTER
// period.From, so assigning it straight to the returned period's From can
// only narrow the window, never widen it past what -days asked for — no
// separate floor-clamp is needed on top of this.
//
// # Scope selects "in scope" exactly the way every section already does
//
// scope.showsProject(e.Identity.Project, data.Report.Clients) is the same
// predicate dashboard_stats.go's tallyStats uses to decide which events
// count for a scoped run. Under -all, scope is the zero value and
// showsProject admits every event unconditionally, so the earliest anchor
// found is the earliest of every event dashboardCmd already fetched: the
// store-wide earliest WITHIN the window already asked for. That is the
// brief's "-all uses the store-wide earliest," reached with no special case
// of its own — showsProject already degrades to "everything" when scope is
// empty.
//
// No in-scope event at all — an empty store, or (defensively; the real
// query already bounds Events to the window) a scope whose data all falls
// outside it — leaves period untouched: there is no observation to clamp
// to, and the honest answer is the window as requested, not a guess.
func clampPeriodToFirstObservation(period dashboardPeriod, scope metricsScope, data *dashboardData) dashboardPeriod {
	var earliest time.Time
	for _, e := range data.Events {
		if !scope.showsProject(e.Identity.Project, data.Report.Clients) {
			continue
		}
		a := e.Anchor()
		if a.IsZero() {
			continue
		}
		if earliest.IsZero() || a.Before(earliest) {
			earliest = a
		}
	}
	if earliest.IsZero() || !earliest.After(period.From) {
		return period
	}
	period.From = earliest
	period.Clamped = true
	return period
}

// dashboardRenderFunc is the one signature every section's render function
// has, whether it is still today's one-line placeholder or the finished
// panel a sibling task replaces it with.
//
//   - ctx is threaded through for a section's own store queries (e.g.
//     Store.Freshness), the same context dashboardCmd itself used to build
//     Report.
//   - out is where the section writes; nothing here decides the terminal's
//     width the way metrics' clipTo does — a dashboard section lays itself
//     out for cc's color capability, not for a column count, and is free to
//     wrap or truncate however its own content calls for.
//   - scope is metricsScope, reused rather than forked: a section filtering
//     by client or project uses scope.showsClient/showsProject exactly as
//     metrics.go's own report functions do, against data.Report — never a
//     second scope type with its own filtering rules to keep in sync.
//   - period is the resolved window above.
//   - cc is the detected color capability; a section paints through
//     cc.Paint/PaintBG/Reset and never checks isatty or an environment
//     variable itself.
//   - data is the shared handle above.
type dashboardRenderFunc func(ctx context.Context, out io.Writer, scope metricsScope,
	period dashboardPeriod, cc colorCapability, data *dashboardData) error

// dashboardSection pairs one panel's title with its render function.
type dashboardSection struct {
	Title  string
	Render dashboardRenderFunc
	// Detailed marks a section that only renders under -detailed. Today,
	// just SOURCE FRESHNESS (Ben's dashboard feedback, 2026-08-07, item 3):
	// default output omits it entirely rather than printing it on every run.
	Detailed bool
}

// dashboardSections is the fixed, ordered list this command renders — see
// this file's own package doc above for why it is the one list a sibling
// task filling a section in never has to touch, let alone reorder.
//
// Titles are Title Case, not ALL CAPS — Ben's dashboard feedback, 2026-08-07,
// item 4 — and renderDashboard below is what bolds them under color; a
// section's own file never decides how its title is painted, only what it
// is.
var dashboardSections = []dashboardSection{
	{Title: "Activity Heatmap", Render: renderActivityHeatmap},
	{Title: "Source Freshness", Render: renderSourceFreshness, Detailed: true},
	{Title: "Component Stats", Render: renderComponentStats},
	{Title: "Allocation & Effective Rate", Render: renderAllocationRate},
	{Title: "Time of Day", Render: renderTimeOfDay},
	{Title: "Source Attribution", Render: renderSourceAttribution},
	{Title: "Filetype Breakdown", Render: renderFiletypeBreakdown},
}

// renderDashboard prints the header every section shares and then each
// section in dashboardSections' own order, skipping any marked Detailed
// unless detailed is true.
//
// A section's error is fatal to the whole run rather than skipped over: a
// section that failed partway through is not a section a reader can trust
// the rest of, and "the sixth panel silently vanished" is exactly the kind
// of absence this project's own rules warn against reading as a zero.
func renderDashboard(ctx context.Context, out io.Writer, scope metricsScope, period dashboardPeriod,
	cc colorCapability, data *dashboardData, detailed bool) error {
	fmt.Fprintf(out, "%s\n", data.Store.Path())
	fmt.Fprintf(out, "%s\n", dashboardHeaderLine(scope, period))

	for _, s := range dashboardSections {
		if s.Detailed && !detailed {
			continue
		}
		fmt.Fprintf(out, "\n%s\n", dashboardHeading(s.Title, cc))
		if err := s.Render(ctx, out, scope, period, cc, data); err != nil {
			return fmt.Errorf("%s: %w", s.Title, err)
		}
	}
	return nil
}

// dashboardHeaderLine states the scope and the window actually used — the
// clamped From/To, never the raw -days computation alone — so a reader is
// never guessing which window the numbers below cover. period.Days (what
// -days asked for) is printed either way, so a clamped run still says what
// was requested alongside what was used.
func dashboardHeaderLine(scope metricsScope, period dashboardPeriod) string {
	from, to := period.From.Local().Format("2006-01-02"), period.To.Local().Format("2006-01-02")
	if !period.Clamped {
		return fmt.Sprintf("%s, %s to %s (%d days)", dashboardScopeLabel(scope), from, to, period.Days)
	}
	return fmt.Sprintf("%s, %s to %s (%d days requested, clamped to the first observation in scope)",
		dashboardScopeLabel(scope), from, to, period.Days)
}

// dashboardHeading bolds title (ANSI SGR 1) under any color capability and
// leaves it plain otherwise — Ben's dashboard feedback, 2026-08-07, item 4.
// The escape code is written directly here rather than through a new method
// on colorCapability: dashcolor.go is a file this task does not touch, bold
// is a heading-only concern with exactly this one caller, and cc.Reset()
// already closes it (SGR 0 clears bold along with any color Paint set).
func dashboardHeading(title string, cc colorCapability) string {
	if cc == colorNone {
		return title
	}
	return "\x1b[1m" + title + cc.Reset()
}

// dashboardScopeLabel names what was asked for, reusing metricsScope's own
// describe() for the filtered cases so the two commands say a filtered scope
// in exactly the same words.
func dashboardScopeLabel(scope metricsScope) string {
	if scope.filtering() {
		return scope.describe()
	}
	return "every client and project"
}

// dashboardPlaceholder is what an unimplemented section renders, under the
// title renderDashboard already printed for it. It lives here, not in any
// one section's file, because it is generic: no section file needs to know
// this helper exists, only that its render function must write something for
// now. A sibling task filling a section in simply stops calling it —
// dashboard.go needs no edit either way.
func dashboardPlaceholder(out io.Writer) error {
	_, err := fmt.Fprint(out, "  not yet implemented — see project/2026-08-07-cli-overhaul-plan.md\n")
	return err
}
