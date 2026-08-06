// Command locatorhistory measures what a report WOULD say if project
// attribution had a history — that is, if `metrics` could be time-travelled to
// an instant before a directory was registered and would then report that
// directory's evidence as unregistered rather than as attributed.
//
// # Why this exists
//
// Task g8vEQ asks whether locator -> project attribution needs an append-only
// log the way client -> project got one (internal/clientmap, migration 0012).
// The argument for building it rests on a quantity nobody had measured: how
// many hours actually change bucket when the registration history is honoured.
// This is that measurement, and it is deliberately a SIMULATION rather than a
// feature — it changes no schema and writes nothing.
//
// # What it does, and what it does not
//
// It reads the store read-only, reads the registry index for each marker's
// `registered` instant, and then computes the metrics report TWICE:
//
//	as-shipped   every event keeps the project the attribution pass wrote.
//	as-of T      an event's project is cleared when the marker naming that
//	             project was registered AFTER T, because at instant T nothing
//	             had asserted that the directory belonged to a project. Those
//	             events then fall into metrics' ReasonUnregistered bucket,
//	             which is exactly what criterion WA9 asks for.
//
// It does NOT model per-locator history. The live registry holds 38 markers
// with 38 distinct project names, so project -> registered-instant is a
// function and clearing by project is equivalent to clearing by locator; the
// program VERIFIES that and refuses rather than assuming it. A real locator log
// would have to be keyed finer, and the cost of that is the subject of
// BenchmarkFold in internal/clientmap rather than of this program.
//
// It also does not touch the CLIENT log. The client mapping already time-
// travels; this isolates the half that does not.
//
// # Use
//
//	go run ./scripts/locatorhistory -store /path/to/copy.db
//	go run ./scripts/locatorhistory -store /path/to/copy.db \
//	    -asof 2026-08-01T13:00:00Z,2026-08-01T14:00:00Z,2026-08-01T15:23:00Z
//	go run ./scripts/locatorhistory -store /path/to/copy.db -from 2026-07-21 -to 2026-08-04
//
// With no -asof it uses one instant one second before the EARLIEST registration
// in the registry, which is the worst case and the one a windowed run would
// hide.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "locatorhistory:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("locatorhistory", flag.ExitOnError)
	storePath := fs.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := fs.String("config", "", "configuration to read (default: ~/.timetattle/config.json)")
	indexPath := fs.String("index", "", "registry index to read (default: ~/.timetattle/registered-directories.json)")
	asOf := fs.String("asof", "", "comma-separated instants to time-travel to; default is one second before the earliest registration")
	from := fs.String("from", "", "earliest instant to consider")
	to := fs.String("to", "", "latest instant to consider")
	if err := fs.Parse(args); err != nil {
		return err
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
	if *indexPath == "" {
		if *indexPath, err = registry.DefaultIndexPath(); err != nil {
			return err
		}
	}

	// The registration instants. A marker that fails to parse is REPORTED, not
	// dropped: a directory in an unknown state is a third answer beside
	// registered and unregistered, and collapsing it would be this project's
	// standing bug.
	markers, listErr := registry.List(*indexPath)
	if listErr != nil {
		fmt.Fprintln(os.Stderr, "some markers could not be read:", listErr)
	}
	registered, err := registeredByProject(markers)
	if err != nil {
		return err
	}
	if len(registered) == 0 {
		return fmt.Errorf("no readable markers under %s: there is no registration history to travel through", *indexPath)
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
	if err := p.Validate(); err != nil {
		return err
	}

	instants, err := instantsToTravel(*asOf, registered)
	if err != nil {
		return err
	}

	filter := store.Filter{}
	if *from != "" {
		if filter.From, err = time.Parse(time.RFC3339, expand(*from, false)); err != nil {
			return fmt.Errorf("parse -from: %w", err)
		}
	}
	if *to != "" {
		if filter.To, err = time.Parse(time.RFC3339, expand(*to, true)); err != nil {
			return fmt.Errorf("parse -to: %w", err)
		}
	}

	st, err := store.OpenReadOnly(*storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	events, err := st.Events(context.Background(), filter)
	if err != nil {
		return err
	}
	excludes, err := sessions.NewExcludes(cfg.Sessions.Exclude)
	if err != nil {
		return err
	}
	events, _ = sessions.Retain(events, excludes)

	fmt.Printf("store            %s\n", *storePath)
	fmt.Printf("registry         %s, %d readable markers\n", *indexPath, len(markers))
	fmt.Printf("window           %s\n", describeWindow(filter))
	fmt.Printf("events read      %d\n", len(events))
	fmt.Printf("registrations    %s\n", describeRegistrations(registered))
	fmt.Println()

	base, err := metrics.Compute(events, p)
	if err != nil {
		return err
	}
	printRow("as shipped", base, 0, len(events))

	for _, at := range instants {
		travelled, cleared := clearAfter(events, registered, at)
		rep, err := metrics.Compute(travelled, p)
		if err != nil {
			return err
		}
		printRow("as of "+at.UTC().Format(time.RFC3339), rep, cleared, len(events))
	}
	fmt.Println()
	fmt.Println(`presence never moves: clearing a project reclassifies attributed clock as
unregistered and creates none. "cleared" counts EVENTS whose project was
removed, not hours; the hours are the change in the unregistered column.`)
	return nil
}

// registeredByProject maps each project to when its directory was blessed, and
// REFUSES a project two directories registered at different instants rather
// than picking one. That state is representable — the registry supports several
// directories sharing a project name — and this program's whole simplification
// is that it does not occur in the registry being measured. Assuming it instead
// of checking it would silently mis-date the evidence of whichever directory
// lost.
func registeredByProject(markers []*registry.Marker) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	holders := map[string][]string{}
	for _, m := range markers {
		if m.Project == "" {
			continue
		}
		holders[m.Project] = append(holders[m.Project], m.Path)
		if prev, seen := out[m.Project]; seen && !prev.Equal(m.Registered) {
			return nil, fmt.Errorf("project %q is registered by %d directories at different instants (%v); "+
				"this program models registration per project and cannot represent that",
				m.Project, len(holders[m.Project]), holders[m.Project])
		}
		out[m.Project] = m.Registered
	}
	return out, nil
}

// clearAfter returns a copy of events with the project removed from every event
// whose project's directory had not been registered by at.
//
// A project the registry has NOTHING to say about keeps its attribution. That
// is deliberate and it is the distinction this project keeps shipping wrong: a
// marker that could not be read, or a project whose directory has since been
// unregistered, is not observed-unregistered-at-T, it is unrecorded, and
// clearing it would state as observed something nothing observed. The count of
// such events is returned to stderr by the caller rather than folded in.
func clearAfter(events []source.Event, registered map[string]time.Time, at time.Time) ([]source.Event, int) {
	out := make([]source.Event, len(events))
	copy(out, events)
	cleared := 0
	unknown := map[string]int{}
	for i := range out {
		project := out[i].Identity.Project
		if project == "" {
			continue
		}
		when, known := registered[project]
		if !known {
			unknown[project]++
			continue
		}
		if when.After(at) {
			out[i].Identity.Project = ""
			out[i].Identity.Client = ""
			cleared++
		}
	}
	if len(unknown) > 0 {
		names := make([]string, 0, len(unknown))
		for n := range unknown {
			names = append(names, n)
		}
		sort.Strings(names)
		fmt.Fprintf(os.Stderr,
			"  note: %d projects carry events but no readable marker, so nothing is known about when they were registered; left attributed: %s\n",
			len(unknown), strings.Join(names, ", "))
	}
	return out, cleared
}

func printRow(label string, r metrics.Report, cleared, total int) {
	attributed := r.Presence.Total - r.Unattributed.Presence.Total
	fmt.Printf("%-34s presence %8s  attributed %8s  unregistered %8s  unlocatable %8s  bridged %7s  cleared %d/%d events\n",
		label, hours(r.Presence.Total), hours(attributed),
		hours(r.Unattributed.Unregistered), hours(r.Unattributed.Unlocatable),
		hours(r.Unattributed.Bridged), cleared, total)
}

func hours(d time.Duration) string { return fmt.Sprintf("%.1fh", d.Hours()) }

// instantsToTravel parses -asof, or picks the worst case when it is empty: one
// second before the EARLIEST registration, which is the instant at which the
// registry knew nothing at all. A convenient window or a convenient instant can
// hide the worst case silently, which scripts/sleepcheck already did once on
// this project.
func instantsToTravel(spec string, registered map[string]time.Time) ([]time.Time, error) {
	if strings.TrimSpace(spec) == "" {
		earliest := time.Time{}
		for _, t := range registered {
			if earliest.IsZero() || t.Before(earliest) {
				earliest = t
			}
		}
		return []time.Time{earliest.Add(-time.Second)}, nil
	}
	var out []time.Time
	for part := range strings.SplitSeq(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, expand(part, false))
		if err != nil {
			return nil, fmt.Errorf("parse -asof %q: %w", part, err)
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out, nil
}

// expand accepts a bare date as well as a full instant. A bare date on an upper
// bound means the END of that day, matching what `metrics -to` settled on.
func expand(s string, upper bool) string {
	if len(s) == 10 && strings.Count(s, "-") == 2 {
		if upper {
			return s + "T23:59:59Z"
		}
		return s + "T00:00:00Z"
	}
	if !strings.HasSuffix(s, "Z") && !strings.Contains(s[10:], "+") {
		return s + "Z"
	}
	return s
}

func describeWindow(f store.Filter) string {
	if f.From.IsZero() && f.To.IsZero() {
		return "the whole store, unwindowed"
	}
	return fmt.Sprintf("%v .. %v", f.From, f.To)
}

func describeRegistrations(registered map[string]time.Time) string {
	if len(registered) == 0 {
		return "none"
	}
	var earliest, latest time.Time
	for _, t := range registered {
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
		if t.After(latest) {
			latest = t
		}
	}
	return fmt.Sprintf("%d projects, earliest %s, latest %s, spread %v",
		len(registered), earliest.UTC().Format(time.RFC3339), latest.UTC().Format(time.RFC3339),
		latest.Sub(earliest).Round(time.Second))
}
