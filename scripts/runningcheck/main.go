// Command runningcheck calibrates the rule `timetattle status` uses to tell a
// harvest that is running right now from one that died partway.
//
// # The problem it measures
//
// harvest_run cannot distinguish the two. A harvest in flight and a harvest
// whose process died leave the identical row — a started_at and no finished_at —
// because the second one, by definition, was not there to write anything else.
// Until 2026-08-01 status resolved that by calling every such row "a harvest
// that died partway" and exiting non-zero, which is a false alarm at every live
// harvest, and a check that cries wolf is a check nobody reads.
//
// The rule that replaced it: a run is presumed dead once it has been marked
// running for longer than STATUS_SLACK times the slowest completed run its
// source has ever recorded, or STATUS_FLOOR, whichever is larger. A source with
// no completed run at all is never presumed dead, because nothing recorded can
// say how long its first read should take.
//
// # What this reports, and why each half is needed
//
// FALSE ALARMS is the half that matters most, and it is a replay rather than an
// opinion. For every run that DID complete, it re-derives the limit from the
// runs that came before it — the only evidence status would have had while that
// run was still in flight — and asks whether the run outlasted it. Every hit is
// a live harvest this rule would have reported as dead. On the live store on
// 2026-08-01, across 76 rows, there are none.
//
// STRANDED is the other half, and a rule with no hits there would be useless
// rather than safe. It lists every unfinished row, when the rule flips it from
// running to stranded, and how much of the -stale window that leaves.
//
// LIMITS is the per-source arithmetic both rest on, so that a figure quoted
// anywhere else can be reproduced.
//
//	go run ./scripts/runningcheck                  # the live store, read-only
//	go run ./scripts/runningcheck -store copy.db
//	go run ./scripts/runningcheck -slack 4         # what a tighter rule would cost
//
// Nothing is written. The store is opened read-only and is never created.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// These must match statusRunningSlack and statusRunningFloor in
// cmd/timetattle/status.go, which is package main and cannot be imported. They
// are restated rather than shared, and the -slack and -floor flags exist so that
// a candidate change can be costed here before it is made there.
const (
	defaultSlack = 10
	defaultFloor = 5 * time.Minute
)

func main() {
	path := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	slack := flag.Int("slack", defaultSlack, "multiple of a source's slowest completed run")
	floor := flag.Duration("floor", defaultFloor, "shortest limit any source may be given")
	stale := flag.Duration("stale", 24*time.Hour, "the -stale window status backstops with")
	flag.Parse()

	if err := run(*path, *slack, *floor, *stale); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, slack int, floor, stale time.Duration) error {
	var err error
	if path == "" {
		if path, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer st.Close()

	runs, err := st.Runs(context.Background(), store.Filter{})
	if err != nil {
		return err
	}
	fmt.Printf("store:  %s\n", path)
	fmt.Printf("rule:   %dx the slowest completed run of the source, floored at %s\n", slack, floor)
	fmt.Printf("rows:   %d\n\n", len(runs))

	limits(runs, slack, floor)
	falseAlarms(runs, slack, floor)
	stranded(runs, slack, floor, stale)
	return nil
}

// limit is the rule itself, kept in one place so the three reports below cannot
// drift from each other.
func limit(slowest time.Duration, slack int, floor time.Duration) time.Duration {
	return max(slowest*time.Duration(slack), floor)
}

func limits(runs []store.Run, slack int, floor time.Duration) {
	slowest, n := map[string]time.Duration{}, map[string]int{}
	// Spread is deliberately NOT slowest/fastest over the source. That figure is
	// meaningless here: gitlog-commits reads five repositories that differ 400x
	// in size, and a failed run returns in microseconds, so a source-wide ratio
	// measures the workload rather than the variation the limit has to absorb.
	//
	// It is computed per (source, input) over successful runs — the same work,
	// done again — and the source's figure is the widest of those. That is the
	// number quoted in cmd/timetattle/status.go, and it is why the slack is 10.
	type key struct{ source, input string }
	fast, slow, reads := map[key]time.Duration{}, map[key]time.Duration{}, map[key]int{}
	for _, r := range runs {
		if r.FinishedAt.IsZero() {
			continue
		}
		d := r.FinishedAt.Sub(r.StartedAt)
		if prev, seen := slowest[r.SourceID]; !seen || d > prev {
			slowest[r.SourceID] = d
		}
		n[r.SourceID]++
		if r.Status != store.StatusOK {
			continue
		}
		k := key{r.SourceID, r.Input}
		reads[k]++
		if prev, seen := slow[k]; !seen || d > prev {
			slow[k] = d
		}
		if prev, seen := fast[k]; !seen || d < prev {
			fast[k] = d
		}
	}
	// repeats counts inputs this source read more than once. Without it a source
	// whose input is different every harvest — knowledgeC records the snapshot
	// filename, which carries a timestamp — would report a spread of 1.00x, and
	// "no input was ever read twice" would print as "the same work never varied".
	repeats := map[string]int{}
	for k := range slow {
		if reads[k] > 1 {
			repeats[k.source]++
		}
	}
	spread := map[string]float64{}
	for k, s := range slow {
		if reads[k] < 2 {
			continue
		}
		if f := fast[k]; f > 0 && s.Seconds()/f.Seconds() > spread[k.source] {
			spread[k.source] = s.Seconds() / f.Seconds()
		}
	}

	fmt.Println("LIMITS  what each source is judged against, from its own completed runs")
	fmt.Println("        spread is the widest slowest/fastest over repeat reads of ONE input, which is")
	fmt.Println("        the same work done again — the variation the limit actually has to absorb")
	fmt.Printf("  %-24s %5s  %9s  %7s  %14s  %s\n",
		"source", "runs", "slowest", "spread", "limit", "bound by")
	for _, id := range sorted(slowest) {
		bound := "the floor"
		if slowest[id]*time.Duration(slack) >= floor {
			bound = fmt.Sprintf("%dx slowest", slack)
		}
		// No input read twice is not a spread of one. It is no reading at all,
		// and it prints as one rather than as a reassuring number.
		shown := "       -"
		if repeats[id] > 0 {
			shown = fmt.Sprintf("%6.2fx", spread[id])
		}
		fmt.Printf("  %-24s %5d  %8.3fs  %8s  %14s  %s\n",
			id, n[id], slowest[id].Seconds(), shown,
			limit(slowest[id], slack, floor), bound)
	}

	var unjudged []string
	seen := map[string]bool{}
	for _, r := range runs {
		if _, ok := slowest[r.SourceID]; !ok && !seen[r.SourceID] {
			seen[r.SourceID] = true
			unjudged = append(unjudged, r.SourceID)
		}
	}
	if len(unjudged) > 0 {
		sort.Strings(unjudged)
		fmt.Printf("  never completed a run, so never presumed dead: %v\n", unjudged)
	}
	fmt.Println()
}

// falseAlarms replays history run by run. For each completed run it derives the
// limit from the runs BEFORE it — which is all status would have had while that
// run was still in flight — and reports the ones that outlasted it.
//
// A run appearing here is a harvest that was working normally and would have
// been reported dead. That is the defect this rule exists to end, so the figure
// that matters is zero.
func falseAlarms(runs []store.Run, slack int, floor time.Duration) {
	fmt.Println("FALSE ALARMS  completed runs this rule would have called dead while they were running")
	slowest := map[string]time.Duration{}
	hits := 0
	for _, r := range runs {
		if r.FinishedAt.IsZero() {
			continue
		}
		took := r.FinishedAt.Sub(r.StartedAt)
		if prior, ok := slowest[r.SourceID]; ok {
			if l := limit(prior, slack, floor); took > l {
				hits++
				fmt.Printf("  run %d  %-24s took %s, past the %s derived from its %s of prior evidence\n",
					r.ID, r.SourceID, took, l, prior)
			}
		}
		// The prefix grows only after the run has been judged, so a run is never
		// measured against a limit its own duration helped set.
		if took > slowest[r.SourceID] {
			slowest[r.SourceID] = took
		}
	}
	if hits == 0 {
		fmt.Println("  none: every completed run finished inside the limit its source's earlier runs implied")
	}
	fmt.Println()
}

func stranded(runs []store.Run, slack int, floor, stale time.Duration) {
	slowest := map[string]time.Duration{}
	for _, r := range runs {
		if r.FinishedAt.IsZero() {
			continue
		}
		if d := r.FinishedAt.Sub(r.StartedAt); d > slowest[r.SourceID] {
			slowest[r.SourceID] = d
		}
	}

	fmt.Println("STRANDED  rows with no finished_at, and when the rule turns on them")
	found := false
	for _, r := range runs {
		if !r.FinishedAt.IsZero() {
			continue
		}
		found = true
		prior, ok := slowest[r.SourceID]
		if !ok {
			fmt.Printf("  run %d  %-24s began %s and %s has never completed a run;\n"+
				"          never presumed dead, and -stale reports it at %s\n",
				r.ID, r.SourceID, r.StartedAt.Format(store.TimeLayout), r.SourceID,
				r.StartedAt.Add(stale).Format(store.TimeLayout))
			continue
		}
		l := limit(prior, slack, floor)
		fmt.Printf("  run %d  %-24s began %s, limit %s, stranded from %s\n"+
			"          which is %s before -stale would have said anything\n",
			r.ID, r.SourceID, r.StartedAt.Format(store.TimeLayout), l,
			r.StartedAt.Add(l).Format(store.TimeLayout), stale-l)
	}
	if !found {
		fmt.Println("  none: every run in this store finished")
	}
}

func sorted[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
