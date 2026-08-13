// Command weakcensus counts what the review screen's weak mark will actually
// say about a store, and lists the project-days it will say it about.
//
// It exists because task KrfMh's population was quoted from
// scripts/excludecheck, which measures a DIFFERENT quantity: "this project-day
// has fs-observations and nothing else". The mark asks "does any observation of
// this project-day record something a person DID", which is a whitelist over
// sources rather than a test against one source, and which also weighs the row
// nobody owns. The two figures must be reconciled rather than assumed equal,
// and this reproduces both from the same pass so the difference can be
// subtracted rather than argued about.
//
// It calls dayview.Weigh, which is the same code the day screen and the review
// queue call. A census that reimplemented the rule would be measuring a second
// rule that happens to agree today.
//
// It opens the store READ-ONLY and writes nothing. It does not read
// derived_session and does not sessionize: the mark is a property of the
// evidence, not of where the clustering put it, so a stale derivation cannot
// make this answer wrong. That also means the population here is over every day
// carrying attributable evidence, which is LARGER than the review queue's,
// because the queue only offers days the stored derivation gave a sitting.
//
//	go run ./scripts/weakcensus -store ~/.timetattle/timetattle.db \
//	  -exclude '*.bz2,*.gz,*.log,/Users/you/git/Easley/reports/'
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := flag.String("config", "", "configuration to read the calendar and exclude list from")
	exclude := flag.String("exclude", "", "comma-separated exclude patterns, overriding the config")
	verbose := flag.Bool("verbose", false, "list every weak project-day with the sources behind it")
	flag.Parse()

	if err := run(*storePath, *configPath, *exclude, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// weakDay is one project's evidence on one working day, with enough of the
// sources kept to say WHY it was weighed as it was.
type weakDay struct {
	day     string
	project string
	sources map[string]int
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
	params := sessions.Params{
		Gap:             cfg.Sessions.Gap.Duration(),
		LeadIn:          cfg.Sessions.LeadIn.Duration(),
		TailOut:         cfg.Sessions.TailOut.Duration(),
		PresenceSources: cfg.Sessions.PresenceSources,
		IgnoreSources:   cfg.Sessions.IgnoreSources,
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
	kept, _ := sessions.Retain(events, excludes)

	// Grouped by working day, because that is the unit Weigh takes. A day's
	// evidence is weighed on its own; nothing from the day before can attest a
	// row here, which is the property TestAnActOutsideTheDayDoesNotAttestARow
	// pins in internal/dayview.
	byDay := map[sessions.Day][]source.Event{}
	for _, e := range kept {
		d := cal.DayOf(e.Anchor())
		byDay[d] = append(byDay[d], e)
	}
	// The sources behind each project-day, for the listing and for the
	// reconciliation against excludecheck.
	sourcesOf := map[[2]string]map[string]int{}
	for d, evs := range byDay {
		for _, e := range evs {
			key := [2]string{d.String(), e.Identity.Project}
			if sourcesOf[key] == nil {
				sourcesOf[key] = map[string]int{}
			}
			sourcesOf[key][e.Source]++
		}
	}

	var (
		total, weak, attested  int
		namedWeak, unownedWeak int
		fsOnlyWeak, otherWeak  int
		namedFSOnly            int
		unknown                int
		weakDays               []weakDay
		daysWithAnyWeak        = map[string]bool{}
		weakProjects           = map[string]int{}
	)
	for d, evs := range byDay {
		for project, strength := range dayview.Weigh(cal, d, params, evs) {
			total++
			key := [2]string{d.String(), project}
			srcs := sourcesOf[key]
			switch strength {
			case dayview.Attested:
				attested++
				continue
			case dayview.Unknown:
				// Weigh never returns this, and saying so with a number is
				// cheaper than trusting the comment that says so.
				unknown++
				continue
			}
			weak++
			daysWithAnyWeak[d.String()] = true
			if project == "" {
				unownedWeak++
			} else {
				namedWeak++
				weakProjects[project]++
			}
			if len(srcs) == 1 && srcs[fsobserve.ID] > 0 {
				fsOnlyWeak++
				if project != "" {
					namedFSOnly++
				}
			} else {
				otherWeak++
			}
			weakDays = append(weakDays, weakDay{day: d.String(), project: project, sources: srcs})
		}
	}
	fmt.Printf("%s\n", storePath)
	fmt.Printf("exclude: %s\n\n", listOrNone(patterns))

	fmt.Printf("SOURCES THAT RECORD AN ACT\n  %s\n", strings.Join(source.Attesting(), ", "))
	fmt.Printf("  everything else is weak evidence on its own, including any source nobody has weighed\n\n")

	fmt.Printf("PROJECT-DAYS, over every day carrying attributable evidence\n")
	fmt.Printf("  %d in all: %d attested, %d weak", total, attested, weak)
	if unknown > 0 {
		fmt.Printf(", %d NOT WEIGHED — which Weigh must never return", unknown)
	}
	fmt.Printf("\n")
	fmt.Printf("  weak rows sit on %d distinct working days\n", len(daysWithAnyWeak))
	fmt.Printf("  of the %d weak: %d name a project, %d are the row nobody owns\n",
		weak, namedWeak, unownedWeak)

	fmt.Printf("\nRECONCILIATION with scripts/excludecheck, which counts project-days whose\n")
	fmt.Printf("ONLY SOURCE is fs-observations and skips the row nobody owns\n")
	fmt.Printf("  %d weak rows rest on fs-observations alone, %d on some other non-attesting source\n",
		fsOnlyWeak, otherWeak)
	fmt.Printf("  %d weak rows both name a project AND rest on fs-observations alone: that is the\n",
		namedFSOnly)
	fmt.Printf("  figure excludecheck reports, and the two must agree exactly\n")
	if namedFSOnly != namedWeak {
		fmt.Printf("  %d NAMED weak rows rest on something else as well, and excludecheck cannot\n",
			namedWeak-namedFSOnly)
		fmt.Printf("  see them: this rule asks whether anything recorded an ACT, not whether one\n")
		fmt.Printf("  particular source was the only one present\n")
	}

	if len(weakProjects) > 0 {
		fmt.Printf("\nPER PROJECT\n")
		names := make([]string, 0, len(weakProjects))
		for name := range weakProjects {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Printf("  %-24s %4d weak\n", name, weakProjects[name])
		}
	}

	if verbose {
		fmt.Printf("\nEVERY WEAK PROJECT-DAY. Nothing here is dropped by anything; this is what\n")
		fmt.Printf("the review screen will mark and ask a person about.\n")
		sort.Slice(weakDays, func(i, j int) bool {
			if weakDays[i].day != weakDays[j].day {
				return weakDays[i].day > weakDays[j].day
			}
			return weakDays[i].project < weakDays[j].project
		})
		for _, w := range weakDays {
			name := w.project
			if name == "" {
				name = "(unattributed)"
			}
			fmt.Printf("  %s  %-24s %s\n", w.day, name, census(w.sources))
		}
	}
	return nil
}

// census renders the sources behind a project-day the way a person reading a
// list of a hundred of them needs: sorted, with counts.
func census(srcs map[string]int) string {
	names := make([]string, 0, len(srcs))
	for name := range srcs {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%s×%d", name, srcs[name]))
	}
	return strings.Join(parts, " ")
}

func splitList(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func listOrNone(patterns []string) string {
	if len(patterns) == 0 {
		return "(none)"
	}
	return strings.Join(patterns, ", ")
}
