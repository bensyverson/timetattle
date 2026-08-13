// Command rollupunion measures how much of a client's hours are held by the
// CLIENT ALONE — clock no project of that client's holds — and how close the
// roll-up's own closing guard is to refusing the whole report.
//
// # Why this exists
//
// internal/metrics used to say two things about what a client's hours are, and
// they were not the same thing:
//
//   - THE IMPLEMENTATION re-clusters the client's own evidence under the same
//     parameters, exactly as a project's hours are derived one level down.
//   - THE DOC COMMENT ON metrics.Client, and the guard that closed Compute, said
//     the figure was the UNION of the client's projects' spans, and the guard
//     refused any report in which a client exceeded its projects' SUM.
//
// Clustering does not distribute over a union. Where a client's projects
// interleave, a silence too wide to bridge inside any one project is narrow
// enough to bridge once that client's evidence is taken together, so the client
// holds clock no project of its own holds. This tool measured how much: 19m13s
// of an unwindowed 617h40m54s, and one calendar day in the live store that
// produced no report whatever.
//
// THE QUESTION WAS SETTLED ON 2026-08-13 IN FAVOUR OF THE RE-CLUSTERING —
// project/2026-08-13-rollup-reclustering-resolved.md — because the bridged
// stretch is the cost of switching between two of one client's projects, which
// neither project's own evidence records. The doc comment and the guard were
// what changed; no reported figure did. This tool stays, because the quantity it
// measures is now a documented property of the report rather than a suspicion,
// and nothing else measures it:
//
//	HELD BY THE CLIENT ALONE  the clock a client holds that no project of its
//	                          own holds. It is what a merge-the-project-spans
//	                          roll-up would take away, and the same clock is
//	                          also printed under UNATTRIBUTED PRESENCE.
//	MARGIN                    the client against the SUM of its projects. A
//	                          NEGATIVE margin is now an ordinary reading and no
//	                          longer refuses anything; it is kept because it is
//	                          how far past the old belief a window has gone.
//
// What CAN still refuse is narrower and is a defect rather than a working
// pattern: a client owning exactly ONE project credited more than that project,
// which is the same evidence clustered twice. The sweep counts those, and there
// are none.
//
// # The populations, which are not interchangeable
//
// A windowed run and an unwindowed one are DIFFERENT POPULATIONS, and the guard
// is not equally likely on them: narrowing the window strips away overlap the
// sum was inflated by, so a client that passes over the whole store can still
// be refused over one day of it. Both shipped surfaces narrow — `metrics` takes
// -from/-to, and `dashboard` recomputes over a suffix of history for its period
// and again per engagement — so the sweeps below are not hypothetical.
//
//	-sweep none     the window given, or the whole store. The default.
//	-sweep day      one closed window per calendar day, the `metrics -from D -to D` shape.
//	-sweep suffix   one window per calendar day, running from that day to the end
//	                of the store: the shape `dashboard` and its allocation
//	                section actually compute.
//
// Every table names its population above itself.
//
// # What it prints, and what it deliberately does not
//
// Clients are numbered rather than named. The store holds REAL client names and
// a capture from this tool is meant to be quotable in a published document; the
// numbering is by sorted name and stable within a run, so two rows can be
// compared without either being identified. -names prints the real names for
// reading on this machine only, and says so in the output.
//
//	go run ./scripts/rollupunion
//	go run ./scripts/rollupunion -sweep day
//	go run ./scripts/rollupunion -from 2026-07-01 -to 2026-07-31 -names
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := flag.String("config", "", "configuration to read parameters and excludes from")
	from := flag.String("from", "", "start of the window, YYYY-MM-DD (default: the whole store)")
	to := flag.String("to", "", "end of the window, YYYY-MM-DD, inclusive")
	sweep := flag.String("sweep", "none", "none, day, or suffix — see the package comment for what each population is")
	names := flag.Bool("names", false, "print real client names; the output then names real clients and must not be pasted into a published document")
	uncorrected := flag.Bool("uncorrected", false, "ignore the recorded attribution corrections, as `metrics -uncorrected` does")
	flag.Parse()

	if err := run(*storePath, *configPath, *from, *to, *sweep, *names, *uncorrected); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(storePath, configPath, from, to, sweep string, names, uncorrected bool) error {
	var err error
	if storePath == "" {
		if storePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	if configPath == "" {
		if configPath, err = config.DefaultPath(); err != nil {
			return err
		}
	}
	cfg, err := config.Load(configPath)
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
	excludes, err := sessions.NewExcludes(cfg.Sessions.Exclude)
	if err != nil {
		return err
	}
	cal, err := cfg.Calendar.Sessions()
	if err != nil {
		return err
	}

	filter := store.Filter{}
	if from != "" {
		if filter.From, err = time.ParseInLocation("2006-01-02", from, time.Local); err != nil {
			return fmt.Errorf("-from: %w", err)
		}
	}
	if to != "" {
		day, err := time.ParseInLocation("2006-01-02", to, time.Local)
		if err != nil {
			return fmt.Errorf("-to: %w", err)
		}
		// The same inclusive closing instant `metrics -to` resolves a bare date
		// to: the end of the day named, not its opening instant.
		filter.To = day.AddDate(0, 0, 1).Add(-time.Microsecond)
	}

	st, err := store.OpenReadOnly(storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	fmt.Printf("store:  %s\n", storePath)
	fmt.Printf("params: gap %v, lead-in %v, tail-out %v\n", p.Gap, p.LeadIn, p.TailOut)
	fmt.Printf("corrections: %s\n", correctionMode(uncorrected))
	if names {
		fmt.Printf("NAMES ARE REAL: this capture names real clients and must not be pasted into a published document\n")
	}
	fmt.Println()

	base, err := reportOver(context.Background(), st, filter, p, excludes, uncorrected)
	if err != nil {
		return err
	}
	fmt.Printf("population: %s\n", population(filter))
	fmt.Printf("events: %d read, %d after exclusions\n\n", base.read, base.kept)
	printWindow(base, names)

	switch sweep {
	case "none":
		return nil
	case "day", "suffix":
	default:
		return fmt.Errorf("-sweep %q is none, day or suffix", sweep)
	}

	if len(base.report.Segments) == 0 {
		return fmt.Errorf("the window holds no segments, so there is nothing to sweep")
	}
	first := base.report.Segments[0].Start
	last := base.report.Segments[len(base.report.Segments)-1].End
	days := cal.Days(first, last)
	fmt.Printf("\nsweep: %s, %d calendar days from %s to %s\n", sweep, len(days),
		days[0], days[len(days)-1])
	fmt.Printf("EACH WINDOW IS ITS OWN POPULATION and is computed from its own events, exactly\n" +
		"as a shipped run over that window would be.\n\n")
	// TWO MARGIN COLUMNS, because one of them is not a measurement. A client
	// with a single project has margin EXACTLY ZERO by construction — its union
	// and its sum are the same one span — so a minimum taken across all clients
	// reads 0s on almost every window and says nothing about how close anything
	// came to refusing. The multi-project column is the one with information in
	// it, and a dash there means no client in that window had a second project
	// to interleave with: nothing was measured, rather than nothing was found.
	fmt.Printf("  %-12s  %7s  %13s  %11s  %5s  %14s  %14s\n",
		"window", "clients", "alone", "largest", "runs", "margin all", "margin multi")
	var (
		refused     int
		withExcess  int
		multiSeen   int
		worst       = time.Duration(1<<63 - 1)
		worstMulti  = time.Duration(1<<63 - 1)
		mostAlone   time.Duration
		neverPassed = true
	)
	for _, d := range days {
		start, end := cal.Bounds(d)
		w := store.Filter{From: start, To: end.Add(-time.Microsecond)}
		if sweep == "suffix" {
			w.To = filter.To
		}
		if !filter.From.IsZero() && w.From.Before(filter.From) {
			w.From = filter.From
		}
		got, err := reportOver(context.Background(), st, w, p, excludes, uncorrected)
		if err != nil {
			credited, held, ok := parseRefusal(err)
			if !ok {
				return fmt.Errorf("%s: %w", d, err)
			}
			// A refusal now comes only from a ONE-project client, so it says
			// nothing about the multi-project margin and must not be counted
			// into it: that column would otherwise report a window as having
			// exercised the interleaving when it did no such thing.
			refused++
			worst = min(worst, held-credited)
			fmt.Printf("  %-12s  %7s  %13s  %11s  %5s  %14v  %14s  REFUSED: a client credited %v against the %v held by its only project\n",
				d.String(), "-", "-", "-", "-", held-credited, "-", credited, held)
			continue
		}
		rows := audit(got.report)
		var alone time.Duration
		var runs, clients, multi int
		margin, marginMulti := time.Duration(1<<63-1), time.Duration(1<<63-1)
		for _, row := range rows {
			clients++
			alone += row.Excess
			runs += row.ExcessRuns
			margin = min(margin, row.Margin)
			if row.Projects > 1 {
				multi++
				marginMulti = min(marginMulti, row.Margin)
			}
		}
		if clients == 0 {
			margin = 0
		}
		if alone > 0 {
			withExcess++
		}
		neverPassed = false
		worst = min(worst, margin)
		mostAlone = max(mostAlone, alone)
		shown := "-"
		if multi > 0 {
			multiSeen++
			worstMulti = min(worstMulti, marginMulti)
			shown = marginMulti.String()
		}
		fmt.Printf("  %-12s  %7d  %13v  %11v  %5d  %14v  %14s\n", d.String(), clients, alone,
			longestRun(rows), runs, margin, shown)
	}
	fmt.Printf("\n%d of %d windows refused; %d held clock a client alone held; "+
		"most held alone in one window %v\n", refused, len(days), withExcess, mostAlone)
	if neverPassed {
		fmt.Println("no window produced a report at all, so no margin was measured")
		return nil
	}
	fmt.Printf("worst margin over all clients %v\n", worst)
	if multiSeen == 0 {
		fmt.Println("NO window held a client with more than one project, so the margin that can " +
			"go negative was never exercised: this sweep measured nothing about the guard")
		return nil
	}
	fmt.Printf("worst margin over the %d windows holding a client with more than one project: %v\n",
		multiSeen, worstMulti)
	return nil
}

func longestRun(rows []rollup) time.Duration {
	var out time.Duration
	for _, r := range rows {
		out = max(out, r.LargestExcess)
	}
	return out
}

// window is one computed report and the counts behind it.
type window struct {
	report     metrics.Report
	read, kept int
}

// reportOver is the shipped read path — the same store call, the same exclusion
// pass, the same client stamping and the same correction fold `metrics` and
// `dashboard` run before they call into internal/metrics.
//
// It is deliberately not a re-implementation of any of them: every step here is
// the same exported call the commands make, in the same order, so a figure this
// tool prints is a figure those commands would have computed.
func reportOver(ctx context.Context, st *store.Store, f store.Filter, p sessions.Params,
	excludes sessions.Excludes, uncorrected bool) (window, error) {
	events, err := st.Events(ctx, f)
	if err != nil {
		return window{}, err
	}
	read := len(events)
	events, _ = sessions.Retain(events, excludes)

	cm, err := st.ClientMap(ctx, time.Time{})
	if err != nil {
		return window{}, err
	}
	for i := range events {
		if events[i].Identity.Project == "" {
			continue
		}
		if c, ok := cm.At(events[i].Identity.Project, events[i].Anchor()); ok {
			events[i].Identity.Client = c
		}
	}

	var said []metrics.Attribution
	if !uncorrected {
		recorded, err := st.Adjustments(ctx, f.From, f.To)
		if err != nil {
			return window{}, err
		}
		said = adjustments.Fold(nil, recorded).Attributions()
	}
	r, err := metrics.Corrected(events, p, said)
	if err != nil {
		return window{}, err
	}
	return window{report: r, read: read, kept: len(events)}, nil
}

func printWindow(w window, names bool) {
	rows := audit(w.report)
	if len(rows) == 0 {
		fmt.Println("no client was attributed anything in this window, which is not the same")
		fmt.Println("statement as every client holding zero: nothing was recorded to roll up.")
		return
	}
	fmt.Printf("  %-14s %8s %10s %10s %10s %5s %9s %10s %10s %10s\n",
		"client", "projects", "engaged", "with proj", "alone", "runs", "largest",
		"unattr too", "summed", "margin")
	for i, r := range rows {
		label := fmt.Sprintf("client %d", i+1)
		if names {
			label = r.Client
		}
		fmt.Printf("  %-14s %8d %10v %10v %10v %5d %9v %10v %10v %10v\n", label, r.Projects,
			round(r.Engaged), round(r.Union), round(r.Excess), r.ExcessRuns,
			round(r.LargestExcess), round(r.AloneAndUnattributed), round(r.Summed),
			round(r.Margin))
		if r.Mismatch != 0 {
			fmt.Printf("  ^ THIS TOOL RECOMPUTED %v MORE THAN THE REPORT'S OWN FIGURE; the two "+
				"disagree and nothing above is trustworthy\n", r.Mismatch)
		}
		if r.Orphan != 0 {
			fmt.Printf("  ^ %v is held by this client's projects and NOT by the client, which "+
				"re-clustering cannot produce and is a defect\n", r.Orphan)
		}
	}
	fmt.Println()
	fmt.Println("alone = clock the client holds that no project of its own holds: a silence")
	fmt.Println("        bridged only once the client's evidence is taken together. It is what")
	fmt.Println("        a merge-the-project-spans roll-up would remove, and it falsifies the")
	fmt.Println("        doc comment on metrics.Client wherever it is not zero.")
	fmt.Println("unattr too = the part of that clock no project of ANY client covers, which the")
	fmt.Println("        same report ALSO prints under UNATTRIBUTED PRESENCE as bridged silence.")
	fmt.Println("margin = summed - engaged. The shipped guard REFUSES THE WHOLE REPORT when it")
	fmt.Println("        is negative, so this is how much headroom the store has left.")
}

func round(d time.Duration) time.Duration { return d.Round(time.Second) }

func correctionMode(uncorrected bool) string {
	if uncorrected {
		return "not applied (-uncorrected)"
	}
	return "the recorded attribution assertions are applied, as `metrics` applies them"
}

func population(f store.Filter) string {
	switch {
	case f.From.IsZero() && f.To.IsZero():
		return "unwindowed — the whole store"
	case f.To.IsZero():
		return fmt.Sprintf("windowed — from %s to the end of the store", f.From.Format("2006-01-02"))
	case f.From.IsZero():
		return fmt.Sprintf("windowed — from the start of the store to %s", f.To.Format("2006-01-02"))
	}
	return fmt.Sprintf("windowed — %s to %s", f.From.Format("2006-01-02"), f.To.Format("2006-01-02"))
}

// refusal is the roll-up guard's own error text. Matching the WHOLE sentence
// rather than a fragment is what keeps another of Compute's checks — several of
// which also name a client and a duration — from being counted as a refusal
// this tool exists to count.
// It is the check as it stands since 2026-08-13. The one it replaced compared
// EVERY client against the SUM of its projects and refused the interleaving this
// tool measures; that invariant was false, and a window that refuses now is a
// defect in the clustering rather than an ordinary day.
var refusal = regexp.MustCompile(`^client .* is credited (.*) against the (.*) held by its only ` +
	`project .*; a client with one project is that project's own evidence clustered again and ` +
	`can never exceed it$`)

// parseRefusal recovers the two durations the roll-up guard refused over.
//
// It is needed because a refusal destroys the report: Compute returns the zero
// value, so the error text is the only surviving record of how far apart the
// two levels were. The second return says whether this was the roll-up guard at
// all, and a false there means the window failed for some OTHER reason and has
// measured nothing about the roll-up — the sweep then aborts with that error
// rather than counting a refusal it did not understand, which is also what
// happens if this pattern and internal/metrics ever drift apart.
func parseRefusal(err error) (credited, held time.Duration, ok bool) {
	if err == nil {
		return 0, 0, false
	}
	m := refusal.FindStringSubmatch(err.Error())
	if m == nil {
		return 0, 0, false
	}
	if credited, err = time.ParseDuration(m[1]); err != nil {
		return 0, 0, false
	}
	if held, err = time.ParseDuration(m[2]); err != nil {
		return 0, 0, false
	}
	return credited, held, true
}
