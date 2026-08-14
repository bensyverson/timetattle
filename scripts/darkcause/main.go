// Command darkcause counts evidence observations that fall inside OBSERVED
// DARKNESS, split by what wrote them.
//
// # Why this exists
//
// internal/sessions used to drop every evidence observation anchored inside a
// dark backlight reading, on the argument that a Motion autosave written with
// the screen off is evidence the application was running and none that a person
// was. That argument is about a WALL-CLOCK TIMER, and source.Cause is the field
// that says which observations are one. Nothing measured how much of the corpus
// the rule actually reached, or what wrote it, so the rule's cost and its
// benefit were both unknown. This measures both.
//
// The answer decides a policy, so it must be reproducible rather than recalled:
//
//	go run ./scripts/darkcause
//	go run ./scripts/darkcause -store /path/to/copy.db
//
// # What it reports, and why these quantities
//
// DARKNESS IS COMPUTED THE WAY internal/sessions COMPUTES IT, and not from the
// 'off' rows alone. Darkness is established only where nothing read the screen
// as lit: knowledgeC writes contradictory rows with identical endpoints, and a
// census built on raw 'off' rows would count observations inside spans the
// clustering never treats as dark. Contested time is reported separately for
// the same reason it is reported by the derivation.
//
// PER CAUSE is the split the policy turns on. An empty cause is printed as
// "(none)" and never folded into another bucket: source.Cause documents empty
// as "does not apply" rather than "not established", and collapsing those two
// is the bug this project has shipped in three separate tools.
//
// PER SOURCE sits beside it because a cause is a standing declaration about a
// source, so a surprising cause total should be explicable by a source total. A
// figure that cannot be traced back to a stream is a figure nobody can check.
//
// It opens the store READ-ONLY and writes nothing anywhere. It reads the event
// table and never derived_session, so a stale derivation cannot leak into a
// figure.
//
// It prints counts, source ids and cause names only — no locator, no document,
// no project. The output is meant to be quotable in a published document, and
// names are the thing three published leaks were made of.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"sort"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

func main() {
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := flag.String("config", "", "configuration naming the presence, ignored and excluded sources")
	flag.Parse()

	if err := run(*storePath, *configPath); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(storePath, configPath string) error {
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
	excludes, err := sessions.NewExcludes(cfg.Sessions.Exclude)
	if err != nil {
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
	// Retained, because the derivation this measures never sees an excluded
	// file. Censusing the raw table would count observations no clustering has
	// ever had the chance to suppress.
	//
	// The unretained count is reported too, and the difference stated, because
	// the two are separate populations and a reader given one figure cannot
	// tell which they were handed. An earlier hand measurement of this quantity
	// differed from the retained one by roughly a thousand observations for
	// exactly this reason.
	events, _ := sessions.Retain(all, excludes)

	presence := map[string]bool{}
	for _, id := range cfg.Sessions.PresenceSources {
		presence[id] = true
	}
	ignored := map[string]bool{}
	for _, id := range cfg.Sessions.IgnoreSources {
		ignored[id] = true
	}

	var lit, dark []timespan.Span
	for _, e := range events {
		if !presence[e.Source] || e.Shape == source.Unbounded {
			continue
		}
		switch e.State {
		case "on":
			lit = append(lit, timespan.Span{Start: e.Start, End: e.End})
		case "off":
			dark = append(dark, timespan.Span{Start: e.Start, End: e.End})
		}
	}
	litU, darkU := timespan.Union(lit), timespan.Union(dark)
	absent := timespan.Union(timespan.Subtract(darkU, litU))

	var (
		evidence     int
		inDark       int
		unretained   int
		byCause      = map[source.Cause]int{}
		bySource     = map[string]int{}
		totalCause   = map[source.Cause]int{}
		anchoredDark = func(e source.Event) bool { return timespan.Covers(absent, e.Anchor()) }
	)
	for _, e := range events {
		if presence[e.Source] || ignored[e.Source] {
			continue
		}
		evidence++
		totalCause[e.Cause]++
		if !anchoredDark(e) {
			continue
		}
		inDark++
		byCause[e.Cause]++
		bySource[e.Source]++
	}
	for _, e := range all {
		if presence[e.Source] || ignored[e.Source] {
			continue
		}
		if anchoredDark(e) {
			unretained++
		}
	}
	// The same census against the RAW union of 'off' rows, which is the figure a
	// hand query reaches for and is not the one the derivation acts on. The
	// difference is entirely observations sitting in contested time, where a
	// lit reading and a dark reading cover the same instants and darkness is
	// therefore not established. Both are printed because a census that quoted
	// only one would be indistinguishable from a census that quoted the other.
	var inRawOff int
	for _, e := range events {
		if presence[e.Source] || ignored[e.Source] {
			continue
		}
		if timespan.Covers(darkU, e.Anchor()) {
			inRawOff++
		}
	}

	fmt.Printf("store:                %s\n", storePath)
	fmt.Printf("evidence observations %d\n", evidence)
	fmt.Printf("observed darkness     %.2fh across %d spans\n", timespan.Total(absent).Hours(), len(absent))
	fmt.Printf("contested             %.2fh (established as neither, and trimmed by nothing)\n",
		timespan.Total(timespan.Intersect(darkU, litU)).Hours())
	fmt.Printf("anchored in darkness  %d retained, %d before excludes (difference %d)\n", inDark, unretained, unretained-inDark)
	fmt.Printf("anchored in raw 'off' %d — %d more, all of them in contested time,\n"+
		"                      which establishes no darkness and is trimmed by nothing\n\n",
		inRawOff, inRawOff-inDark)

	fmt.Println("by cause (of the observations anchored in darkness, and of all evidence):")
	for _, c := range causeOrder(byCause, totalCause) {
		fmt.Printf("  %-12s %8d in darkness   %9d total\n", name(c), byCause[c], totalCause[c])
	}

	fmt.Println("\nby source (observations anchored in darkness):")
	ids := make([]string, 0, len(bySource))
	for id := range bySource {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Printf("  %-22s %8d\n", id, bySource[id])
	}
	return nil
}

// name prints the empty cause as something a reader cannot mistake for a
// missing row. "does not apply" and "nobody said" are different claims and this
// output must not blur them.
func name(c source.Cause) string {
	if c == "" {
		return "(none)"
	}
	return string(c)
}

// causeOrder lists every cause either map mentions, so a cause with a total and
// no dark observations still prints a line. A bucket that vanishes because it
// scored zero reads as a bucket nobody measured.
func causeOrder(a, b map[source.Cause]int) []source.Cause {
	seen := map[source.Cause]bool{}
	var out []source.Cause
	for _, m := range []map[source.Cause]int{a, b} {
		for c := range m {
			if !seen[c] {
				seen[c] = true
				out = append(out, c)
			}
		}
	}
	slices.Sort(out)
	return out
}
