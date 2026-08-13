// Command sittingcheck measures what an exclude rule does to SITTINGS and to
// ENGAGED TIME, which is the half scripts/excludecheck does not answer.
//
// excludecheck counts PROJECT-DAYS: whether a rule makes a repository appear on
// a day nobody opened it. That is the right unit for a rule whose harm is a
// fabricated day, and it is the wrong unit for a rule whose harm would be a
// fabricated HOUR. A `pip install` writes thousands of files in one burst, and a
// burst can create a sitting, or extend one it merely touches the edge of,
// without ever being the only evidence on its day. Nothing measured that, so
// this does.
//
// It opens the store READ-ONLY, reads the event table, and writes nothing
// anywhere. `timetattle sessionize` writes derived_session and must not be used
// for an experiment; nothing here reads that table either, so a stale derivation
// cannot leak into a figure.
//
// # What it reports, and why these quantities
//
// A BASELINE is derived from the exclude list already in force, and a TEST from
// that list plus the patterns under -add. Everything printed is the difference,
// to the second, because the difference is the claim being made.
//
//   - SITTINGS WHOLLY BUILT ON THE ADDED FILES is the fabrication case: a
//     sitting every one of whose evidence observations the added patterns
//     remove. It disappears entirely, and its whole duration was invented.
//   - SITTINGS MIXED WITH OTHER EVIDENCE is the subtler one: the added files sat
//     inside or at the edge of a real sitting and may have stretched it. The
//     sitting survives, no project-day changes, and engaged time can still have
//     been overstated.
//   - PER-PROJECT ENGAGED prints absolute and allocated before and after,
//     because a rule can move a project's hours without moving the wall clock.
//
// Evidence is assigned to a sitting by its ANCHOR falling inside the sitting's
// span. Any evidence that lands in no sitting at all is counted and printed
// rather than dropped: an observation the clustering suppressed is a fact, and
// silently omitting it would let this tool report a clean answer it had not
// actually measured.
//
//	go run ./scripts/sittingcheck -store /path/to/copy.db \
//	  -add '/Users/you/git/assistant/.venv/'
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
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := flag.String("config", "", "configuration to read the baseline exclude list from")
	baseline := flag.String("exclude", "", "comma-separated baseline exclude patterns, overriding the config")
	add := flag.String("add", "", "comma-separated patterns to ADD to the baseline; the difference is what is reported")
	verbose := flag.Bool("verbose", false, "list every affected sitting rather than only the totals")
	flag.Parse()

	if err := run(*storePath, *configPath, *baseline, *add, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(storePath, configPath, baseline, add string, verbose bool) error {
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
	basePatterns := cfg.Sessions.Exclude
	if baseline != "" {
		basePatterns = splitList(baseline)
	}
	addPatterns := splitList(add)
	if len(addPatterns) == 0 {
		return fmt.Errorf("-add names no pattern, so there is no difference to measure")
	}

	baseExcludes, err := sessions.NewExcludes(basePatterns)
	if err != nil {
		return err
	}
	testExcludes, err := sessions.NewExcludes(append(append([]string{}, basePatterns...), addPatterns...))
	if err != nil {
		return err
	}
	// The added patterns on their own, so that "which files does the ADDITION
	// reach" is answered by the addition and not by the baseline underneath it.
	addedOnly, err := sessions.NewExcludes(addPatterns)
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

	st, err := store.OpenReadOnly(storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	all, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		return err
	}

	baseEvents, baseDrop := sessions.Retain(all, baseExcludes)
	testEvents, testDrop := sessions.Retain(all, testExcludes)

	before, err := metrics.Compute(baseEvents, p)
	if err != nil {
		return err
	}
	after, err := metrics.Compute(testEvents, p)
	if err != nil {
		return err
	}

	fmt.Printf("%s\n", storePath)
	fmt.Printf("gap %v, lead-in %v, tail-out %v\n", p.Gap, p.LeadIn, p.TailOut)
	fmt.Printf("baseline exclude: %s\n", listOrNone(basePatterns))
	fmt.Printf("added:            %s\n\n", listOrNone(addPatterns))

	fmt.Printf("OBSERVATIONS\n")
	fmt.Printf("  %d read, %d dropped by the baseline, %d dropped with the addition (%+d)\n",
		len(all), baseDrop.Total, testDrop.Total, testDrop.Total-baseDrop.Total)

	reached := 0
	for _, e := range baseEvents {
		if e.Locator != "" && addedOnly.Match(e.Locator) {
			reached++
		}
	}
	fmt.Printf("  %d of the %d baseline events are reached by the added patterns\n", reached, len(baseEvents))

	fmt.Printf("\nPRESENCE\n")
	fmt.Printf("  %d sittings before, %d after (%+d)\n",
		before.Sittings.Count, after.Sittings.Count, after.Sittings.Count-before.Sittings.Count)
	fmt.Printf("  %s before, %s after, %s removed\n",
		exact(before.Presence.Total), exact(after.Presence.Total),
		exact(before.Presence.Total-after.Presence.Total))
	fmt.Printf("  observed %s before, %s after\n",
		exact(before.Presence.Observed), exact(after.Presence.Observed))

	// The fabrication question, asked of the BASELINE derivation: is there a
	// sitting every one of whose evidence observations the addition removes?
	evidence := evidenceRoles(p)
	type sittingFacts struct {
		start, end     time.Time
		events         int
		reachedByAdded int
		projects       map[string]bool
	}
	facts := make([]*sittingFacts, len(before.Sessions))
	for i, s := range before.Sessions {
		facts[i] = &sittingFacts{start: s.Start, end: s.End, projects: map[string]bool{}}
	}
	var unassigned, reachedUnassigned, reachedAssigned int
	for _, e := range baseEvents {
		if !evidence(e.Source) {
			continue
		}
		hit := e.Locator != "" && addedOnly.Match(e.Locator)
		i := sittingAt(before.Sessions, e.Anchor())
		if i < 0 {
			// Suppressed by observed darkness, or otherwise clustered into
			// nothing. Reported rather than ignored: this is the difference
			// between "no sitting held it" and "nobody looked".
			unassigned++
			if hit {
				reachedUnassigned++
			}
			continue
		}
		f := facts[i]
		f.events++
		if e.Identity.Project != "" {
			f.projects[e.Identity.Project] = true
		}
		if hit {
			f.reachedByAdded++
			reachedAssigned++
		}
	}

	var (
		wholly, partly            []*sittingFacts
		whollyTotal, partlyTotal  time.Duration
		whollyLongest             time.Duration
		whollyLongestAt           time.Time
		partlyLongest             time.Duration
		partlyLongestAt           time.Time
		whollyLongestEventsInside int
	)
	for _, f := range facts {
		d := f.end.Sub(f.start)
		switch {
		case f.reachedByAdded == 0:
			continue
		case f.reachedByAdded == f.events:
			wholly = append(wholly, f)
			whollyTotal += d
			if d > whollyLongest {
				whollyLongest, whollyLongestAt, whollyLongestEventsInside = d, f.start, f.events
			}
		default:
			partly = append(partly, f)
			partlyTotal += d
			if d > partlyLongest {
				partlyLongest, partlyLongestAt = d, f.start
			}
		}
	}

	fmt.Printf("\nSITTINGS THE ADDED FILES APPEAR IN\n")
	fmt.Printf("  %d of %d sittings hold at least one\n", len(wholly)+len(partly), len(before.Sessions))
	fmt.Printf("  WHOLLY built on them: %d sittings, %s of wall clock — this is what the addition\n"+
		"                        deletes outright\n", len(wholly), exact(whollyTotal))
	if len(wholly) > 0 {
		fmt.Printf("                        longest %v starting %s, on %d observations\n",
			whollyLongest.Round(time.Second), whollyLongestAt.Local().Format("2006-01-02 15:04"),
			whollyLongestEventsInside)
	}
	fmt.Printf("  mixed with other evidence: %d sittings, %s, which survive and can only shorten\n",
		len(partly), exact(partlyTotal))
	if len(partly) > 0 {
		fmt.Printf("                        longest %v starting %s\n",
			partlyLongest.Round(time.Second), partlyLongestAt.Local().Format("2006-01-02 15:04"))
	}
	// Every reached observation is accounted for, in a sitting or out of one.
	// A tool that reported "only 3 sittings hold them" without saying where the
	// other 27,000 went would be asserting the answer rather than measuring it.
	fmt.Printf("  ACCOUNTING of the %d reached observations: %d fell inside a sitting, %d in none\n",
		reachedAssigned+reachedUnassigned, reachedAssigned, reachedUnassigned)
	if unassigned > 0 {
		fmt.Printf("  %d evidence observations in total landed in no sitting at all; internal/sessions\n"+
			"  reports %d suppressed by observed darkness and %d unbounded, which is what that is\n",
			unassigned, before.Stats.SuppressedByAbsence, before.Stats.Unbounded)
	}
	if verbose {
		for _, f := range wholly {
			fmt.Printf("    WHOLLY %s .. %s  %v  %d events  %s\n",
				f.start.Local().Format("2006-01-02 15:04"), f.end.Local().Format("15:04"),
				f.end.Sub(f.start).Round(time.Second), f.events, strings.Join(sortedKeys(f.projects), ", "))
		}
		for _, f := range partly {
			fmt.Printf("    mixed  %s .. %s  %v  %d of %d events  %s\n",
				f.start.Local().Format("2006-01-02 15:04"), f.end.Local().Format("15:04"),
				f.end.Sub(f.start).Round(time.Second), f.reachedByAdded, f.events,
				strings.Join(sortedKeys(f.projects), ", "))
		}
	}

	fmt.Printf("\nPER-PROJECT ENGAGED, only where the addition moved something\n")
	names := map[string]bool{}
	for _, pr := range before.Projects {
		names[pr.Name] = true
	}
	for _, pr := range after.Projects {
		names[pr.Name] = true
	}
	moved := 0
	for _, n := range sortedKeys(names) {
		b, okB := before.Project(n)
		a, okA := after.Project(n)
		if !okB && !okA {
			continue
		}
		if b.Engaged.Total == a.Engaged.Total && b.Allocated == a.Allocated &&
			b.Sittings.Count == a.Sittings.Count {
			continue
		}
		if moved == 0 {
			fmt.Printf("  %-18s %22s %22s %22s %22s %12s\n",
				"project", "abs before", "abs after", "alloc before", "alloc after", "sittings")
		}
		moved++
		fmt.Printf("  %-18s %22s %22s %22s %22s %5d->%-5d\n", n,
			exact(b.Engaged.Total), exact(a.Engaged.Total),
			exact(b.Allocated), exact(a.Allocated),
			b.Sittings.Count, a.Sittings.Count)
	}
	if moved == 0 {
		fmt.Printf("  nothing moved: every project's absolute engaged, allocated engaged and sitting\n" +
			"  count is identical to the microsecond before and after\n")
	}
	return nil
}

// sittingAt returns the index of the sitting containing at, or -1. Sittings are
// disjoint and ordered, so this is a binary search rather than a scan; over a
// million events the scan is the whole runtime.
func sittingAt(ss []sessions.Session, at time.Time) int {
	lo, hi := 0, len(ss)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		switch {
		case at.Before(ss[mid].Start):
			hi = mid - 1
		case at.After(ss[mid].End):
			lo = mid + 1
		default:
			return mid
		}
	}
	return -1
}

// evidenceRoles returns the test internal/sessions applies: a source is
// evidence unless the parameters name it as presence or as ignored.
func evidenceRoles(p sessions.Params) func(string) bool {
	other := map[string]bool{}
	for _, id := range p.PresenceSources {
		other[id] = true
	}
	for _, id := range p.IgnoreSources {
		other[id] = true
	}
	return func(id string) bool { return !other[id] }
}

// exact prints a duration without rounding it away. A tenth of an hour is six
// minutes, and a "0.0h" has hidden a real change in this project before.
func exact(d time.Duration) string {
	return fmt.Sprintf("%.4fh/%v", d.Hours(), d.Round(time.Second))
}

func splitList(s string) []string {
	var out []string
	for p := range strings.SplitSeq(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func listOrNone(list []string) string {
	if len(list) == 0 {
		return "none"
	}
	return strings.Join(list, ", ")
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
