// Command excludecheck measures what a sessions.exclude list would do to a
// store, before anybody puts it in a config file.
//
// It exists because "prove it by mutation" needs a mutation that is safe to
// run. `timetattle sessionize` WRITES derived_session, so measuring an
// experimental exclude list with it either destroys the model of record or
// requires copying a 682 MB database first — and the previous session published
// wrong numbers twice from a derived_session that was stale for exactly that
// reason. This reads the event table and nothing else, opens the store
// read-only, and writes nothing anywhere.
//
// # What it counts, and why these quantities
//
// A PROJECT-DAY is the unit the harm shows up in. One observation is enough to
// put a project row on a day's review screen, so a handful of WAL checkpoints
// fabricates a whole working day for a repository nobody opened. Counting
// observations alone would call that 0.1% of the corpus and miss the point
// entirely; counting project-days calls it seven fabricated days in one
// afternoon, which is what a reviewer actually met.
//
// THE SAFETY PROPERTY is the reason this tool exists rather than a SQL query.
// A project-day may only disappear if EVERY piece of evidence on it was
// excluded. A day carrying a commit, a transcript or a real file save must
// survive whatever else was dropped from it, and asserting that is not the same
// as measuring it. -verbose lists any day that fails, and the exit status is
// non-zero if any does.
//
// THE CENSUS is the constraint stated as a number. Of the fs-only project-days
// in the live store, a hand census on 2026-08-01 found 272 made entirely of
// machine artefacts, 245 mixed, and 81 made ENTIRELY OF ORDINARY FILES —
// MBCommon.h, a README.md, a design PDF — which are plausibly a real evening
// spent editing one document and must never be dropped by a rule. This tool
// reproduces that census with the SUFFIX classifier the original used, and then
// cross-tabulates it against what the exclude list actually removes. A path
// prefix such as /Users/you/git/Easley/reports/ can reach files the suffix
// census called ordinary — 2024-12-09.json looks like a document and is
// generated dashboard output — so the intersection is where a rule would start
// eating real work, and it is printed file by file rather than as a rate.
//
//	go run ./scripts/excludecheck -store ~/.timetattle/timetattle.db \
//	  -exclude '*.log.bz2,*.gz,*.log,/Users/you/git/Easley/reports/'
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/generated"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

// censusSuffixes is the classifier the 2026-08-01 hand census used, kept here
// verbatim so this tool's "entirely ordinary" population is the same population
// that census was talking about. It is deliberately NOT the exclude list under
// test: the whole point is to compare the two.
var censusSuffixes = []string{".log", ".log.bz2", ".gz", ".bz2", ".zip"}

// machineByCensus reports whether the census would have called this file a
// machine artefact — by suffix, plus the sidecars, and nothing else.
func machineByCensus(locator string) bool {
	base := strings.ToLower(filepath.Base(locator))
	if fsobserve.IsDatabaseSidecar(base) {
		return true
	}
	return slices.ContainsFunc(censusSuffixes, func(s string) bool {
		return strings.HasSuffix(base, s)
	})
}

type projectDay struct {
	project string
	day     string
}

func main() {
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := flag.String("config", "", "configuration to read the exclude list from")
	exclude := flag.String("exclude", "", "comma-separated exclude patterns to test, overriding the config")
	verbose := flag.Bool("verbose", false, "list every affected project-day rather than only the totals")
	flag.Parse()

	if err := run(*storePath, *configPath, *exclude, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(storePath, configPath, exclude string, verbose bool) error {
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
	patterns := cfg.Sessions.Exclude
	if exclude != "" {
		patterns = splitList(exclude)
	}
	excludes, err := sessions.NewExcludes(patterns)
	if err != nil {
		return err
	}
	cal, err := cfg.Calendar.Sessions()
	if err != nil {
		return err
	}

	st, err := store.OpenReadOnly(storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		return err
	}

	// Only attributed, located evidence can make or lose a project-day.
	// Presence sources carry no path and no project and are never at issue
	// here; counting them would put the same enormous number on both sides of
	// every comparison.
	type dayFacts struct {
		total, fs, fsKept, other int
		machine, ordinary        int
		locators                 map[string]int
	}
	days := map[projectDay]*dayFacts{}
	var totalFS, droppedTotal, droppedSidecars int
	byPattern := map[string]int{}
	for _, p := range patterns {
		byPattern[p] = 0
	}
	droppedPerProject := map[string]int{}

	for _, e := range events {
		// Not part of the population: no report counts it, whatever the
		// configured patterns say.
		if e.Identity.Project == "" || generated.Is(e) {
			continue
		}
		key := projectDay{project: e.Identity.Project, day: cal.DayOf(e.Anchor()).String()}
		d := days[key]
		if d == nil {
			d = &dayFacts{locators: map[string]int{}}
			days[key] = d
		}
		d.total++
		if e.Source != fsobserve.ID {
			d.other++
			continue
		}
		d.fs++
		totalFS++
		d.locators[e.Locator]++
		if machineByCensus(e.Locator) {
			d.machine++
		} else {
			d.ordinary++
		}
		if !excludes.Match(e.Locator) {
			d.fsKept++
			continue
		}
		droppedTotal++
		droppedPerProject[e.Identity.Project]++
		if fsobserve.IsDatabaseSidecar(filepath.Base(e.Locator)) {
			droppedSidecars++
			continue
		}
		for _, p := range patterns {
			one, _ := sessions.NewExcludes([]string{p})
			if one.Match(e.Locator) {
				byPattern[p]++
				break
			}
		}
	}

	fmt.Printf("%s\n", storePath)
	fmt.Printf("exclude: %s\n\n", listOrNone(patterns))

	fmt.Printf("OBSERVATIONS\n")
	fmt.Printf("  %d attributed fs-observations\n", totalFS)
	fmt.Printf("  %d excluded (%.1f%%): %d SQLite sidecars", droppedTotal,
		100*float64(droppedTotal)/float64(max(totalFS, 1)), droppedSidecars)
	for _, p := range patterns {
		fmt.Printf(", %d by %q", byPattern[p], p)
	}
	fmt.Printf("\n")
	for _, p := range patterns {
		if byPattern[p] == 0 {
			fmt.Printf("  NOTE: %q matched nothing\n", p)
		}
	}

	// A project-day survives if anything at all is left on it: a non-fs source,
	// or an fs-observation the rules kept.
	var (
		before, after, lost   int
		lostWithOther         []projectDay
		fsOnlyBefore          int
		fsOnlyAfter           int
		censusMachine         int
		censusMixed           int
		censusOrdinary        int
		ordinaryLost          []projectDay
		lostPerProject        = map[string]int{}
		beforePerProject      = map[string]int{}
		afterPerProject       = map[string]int{}
		survivingFSOnlySample []projectDay
	)
	keys := make([]projectDay, 0, len(days))
	for k := range days {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].project != keys[j].project {
			return keys[i].project < keys[j].project
		}
		return keys[i].day < keys[j].day
	})

	for _, k := range keys {
		d := days[k]
		before++
		beforePerProject[k.project]++
		fsOnly := d.other == 0 && d.fs > 0
		if fsOnly {
			fsOnlyBefore++
			switch {
			case d.ordinary == 0:
				censusMachine++
			case d.machine == 0:
				censusOrdinary++
			default:
				censusMixed++
			}
		}
		survives := d.other > 0 || d.fsKept > 0
		if survives {
			after++
			afterPerProject[k.project]++
			if d.other == 0 {
				fsOnlyAfter++
				survivingFSOnlySample = append(survivingFSOnlySample, k)
			}
			continue
		}
		lost++
		lostPerProject[k.project]++
		if d.other > 0 {
			// The safety property failing. Kept as a list rather than a
			// count: one such day is worth more than the rate.
			lostWithOther = append(lostWithOther, k)
		}
		if fsOnly && d.machine == 0 {
			ordinaryLost = append(ordinaryLost, k)
		}
	}

	fmt.Printf("\nPROJECT-DAYS\n")
	fmt.Printf("  %d before, %d after, %d lost\n", before, after, lost)
	fmt.Printf("  fs-observations as the ONLY evidence: %d of %d before, %d of %d after\n",
		fsOnlyBefore, before, fsOnlyAfter, after)

	fmt.Printf("\nSAFETY: a project-day may only go if EVERY piece of evidence on it went\n")
	if len(lostWithOther) == 0 {
		fmt.Printf("  holds: 0 of the %d lost project-days carried a transcript, a commit or any "+
			"other non-filesystem evidence\n", lost)
	} else {
		fmt.Printf("  FAILS: %d lost project-days carried other evidence\n", len(lostWithOther))
		for _, k := range lostWithOther {
			fmt.Printf("    %s %s\n", k.day, k.project)
		}
	}

	fmt.Printf("\nCENSUS of the fs-only project-days, by the 2026-08-01 suffix classifier\n")
	fmt.Printf("  %d entirely machine artefacts, %d mixed, %d entirely ORDINARY files\n",
		censusMachine, censusMixed, censusOrdinary)
	if len(ordinaryLost) == 0 {
		fmt.Printf("  0 of the entirely-ordinary days were removed, which is the required answer\n")
	} else {
		fmt.Printf("  %d entirely-ordinary days were removed. Each is listed with its files so a "+
			"person can judge it; the rule reached past the suffixes the census knew about:\n",
			len(ordinaryLost))
		for _, k := range ordinaryLost {
			fmt.Printf("    %s %s\n", k.day, k.project)
			for _, l := range sortedKeys(days[k].locators) {
				fmt.Printf("      %s\n", l)
			}
		}
	}

	fmt.Printf("\nPER PROJECT\n")
	fmt.Printf("  %-22s %8s %8s %8s %8s\n", "project", "days", "lost", "left", "obs gone")
	names := sortedKeys(beforePerProject)
	sort.Slice(names, func(i, j int) bool {
		if lostPerProject[names[i]] != lostPerProject[names[j]] {
			return lostPerProject[names[i]] > lostPerProject[names[j]]
		}
		return names[i] < names[j]
	})
	for _, n := range names {
		if lostPerProject[n] == 0 && droppedPerProject[n] == 0 {
			continue
		}
		fmt.Printf("  %-22s %8d %8d %8d %8d\n", n, beforePerProject[n], lostPerProject[n],
			afterPerProject[n], droppedPerProject[n])
	}

	if verbose {
		fmt.Printf("\nSURVIVING fs-only project-days (%d), which are the ones a human still has "+
			"to judge\n", len(survivingFSOnlySample))
		for _, k := range survivingFSOnlySample {
			fmt.Printf("  %s %-18s", k.day, k.project)
			var kept []string
			for _, l := range sortedKeys(days[k].locators) {
				if !excludes.Match(l) {
					kept = append(kept, filepath.Base(l))
				}
			}
			fmt.Printf(" %s\n", strings.Join(kept, ", "))
		}
	}

	if len(lostWithOther) > 0 {
		return fmt.Errorf("the safety property does not hold; see above")
	}
	return nil
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
