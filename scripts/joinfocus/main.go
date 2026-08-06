// Command joinfocus checks whether timestamped-file evidence lands inside the
// focus intervals Biome recorded for the same application.
//
// This is the join the whole design rests on. Biome knows Motion held focus
// from 13:49 to 14:25 but not which composition was open; a Motion autosave
// knows dark-crystal was written at 14:01 but not for how long. Neither answers
// "how long went into dark-crystal" alone, and `source.Event.Contains` is the
// mechanical test that connects them — it requires the bundle identifier to
// match on both sides, which is why a manifest's "app" field is load-bearing
// rather than decoration.
//
// It exists to be re-run. Both sides are perishable: Motion prunes its autosave
// vault, Blender's .blend1 mtimes move to the day of the next save, and Biome
// eventually rolls its segments away. A day that joins cleanly today may have
// no evidence left next month, so the numbers below are an observation with a
// date on it rather than a fact about the past.
//
// A point landing OUTSIDE every interval is a finding, not noise. An autosave
// timer fires on wall-clock time whether or not the application is frontmost,
// so a miss usually means the save happened while the user had switched away
// for a moment — which is why this reports what held focus instead. A miss of
// hours would mean something else entirely: an epoch or timezone error in one
// of the two readers.
//
// Like the other tools here this is analysis rather than product behaviour, and
// is exempt from the project's red/green TDD rule. The assertions that must not
// regress live in internal/manifest/realdata_test.go.
//
// scripts/joinday answers a larger question — presence, focus and artifacts for
// one working day, side by side — and needs a knowledgeC snapshot to do it.
// This one needs nothing but the manifests and Biome, sweeps every day and
// every source at once, and exists to answer "do these manifests describe files
// that line up with recorded focus at all". If the two ever disagree about a
// day, one of them has a bug.
//
// Usage:
//
//	go run ./scripts/joinfocus                          # every day with evidence
//	go run ./scripts/joinfocus -day 2026-07-28          # one day
//	go run ./scripts/joinfocus -source blender-backups  # one source
//	go run ./scripts/joinfocus -manifests ""            # built-ins only
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timestamped"
)

const dayLayout = "2006-01-02"

func main() {
	day := flag.String("day", "", "report only this day, as 2006-01-02")
	only := flag.String("source", "", "report only this manifest source id")
	dir := flag.String("dir", "", "directory of Biome segments (default: the current user's)")
	manifests := flag.String("manifests", "", "user manifest directory (default: ~/.timetattle/manifests)")
	flag.Parse()

	if err := run(*day, *only, *dir, *manifests); err != nil {
		fmt.Fprintf(os.Stderr, "joinfocus: %v\n", err)
		os.Exit(1)
	}
}

func run(day, only, biomeDir, manifestDir string) error {
	ctx := context.Background()

	sources, err := manifest.LoadAll(manifestDir)
	if err != nil {
		// A broken manifest costs its own source and nothing else, so this is
		// worth saying out loud without stopping.
		fmt.Fprintf(os.Stderr, "joinfocus: %v\n", err)
	}

	if biomeDir == "" {
		if biomeDir, err = biome.DefaultAppUsageDir(); err != nil {
			return err
		}
	}
	focus, err := biome.NewAppUsage(biomeDir).Collect(ctx, time.Time{})
	if err != nil {
		return fmt.Errorf("read focus intervals: %w", err)
	}

	for _, spec := range sources {
		if only != "" && spec.ID != only {
			continue
		}
		if err := report(ctx, spec, focus, day); err != nil {
			fmt.Fprintf(os.Stderr, "joinfocus: %s: %v\n", spec.ID, err)
		}
	}
	return nil
}

// report prints the join for one source.
func report(ctx context.Context, spec manifest.Source, focus []source.Event, day string) error {
	reader, err := timestamped.New(spec)
	if err != nil {
		return err
	}
	points, err := reader.Collect(ctx, time.Time{})
	if err != nil {
		return err
	}

	fmt.Printf("\n%s (%s)\n", spec.ID, spec.App)
	if len(points) == 0 {
		fmt.Println("  no files found")
		return nil
	}

	// Intervals for this app only. Unpaired focus records carry no duration —
	// they are a machine resuming with something already frontmost — and a
	// zero-width interval cannot contain anything, so they are set aside here
	// rather than counted as a failed join.
	var intervals []source.Event
	for _, e := range focus {
		if e.Identity.App == spec.App && e.Shape == source.Interval && e.Duration() > 0 {
			intervals = append(intervals, e)
		}
	}

	byDay := make(map[string][]source.Event)
	for _, p := range points {
		byDay[local(p.Start).Format(dayLayout)] = append(byDay[local(p.Start).Format(dayLayout)], p)
	}
	days := make([]string, 0, len(byDay))
	for d := range byDay {
		days = append(days, d)
	}
	sort.Strings(days)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "  DAY\tPOINTS\tINTERVALS\tINSIDE\tOUTSIDE")
	var detail []string
	for _, d := range days {
		if day != "" && d != day {
			continue
		}
		onThisDay := sameDay(intervals, d)
		// Biome reaches back a few weeks, so older evidence has nothing to
		// join against. That is missing focus history, not a failed join, and
		// reporting it as "outside" would make the manifest look wrong.
		if len(onThisDay) == 0 {
			fmt.Fprintf(w, "  %s\t%d\t0\t-\t-\tno focus history for this app\n", d, len(byDay[d]))
			continue
		}
		var inside, outside int
		for _, p := range byDay[d] {
			hit, iv := containing(p, onThisDay)
			if hit {
				inside++
				detail = append(detail, fmt.Sprintf("  inside   %s %s  %-26s  focus %s-%s",
					d, local(p.Start).Format("15:04:05"), name(p),
					local(iv.Start).Format("15:04:05"), local(iv.End).Format("15:04:05")))
				continue
			}
			outside++
			gap, near := nearest(p, onThisDay)
			detail = append(detail, fmt.Sprintf("  outside  %s %s  %-26s  %s from %s-%s, %s held focus",
				d, local(p.Start).Format("15:04:05"), name(p), gap.Round(time.Second),
				local(near.Start).Format("15:04:05"), local(near.End).Format("15:04:05"),
				frontmost(focus, p.Start)))
		}
		fmt.Fprintf(w, "  %s\t%d\t%d\t%d\t%d\n", d, len(byDay[d]), len(onThisDay), inside, outside)
	}
	w.Flush()

	if len(detail) > 0 {
		fmt.Println()
		fmt.Println(strings.Join(detail, "\n"))
	}
	return nil
}

// containing returns the interval a point falls inside, if any.
func containing(p source.Event, intervals []source.Event) (bool, source.Event) {
	for _, iv := range intervals {
		if iv.Contains(p) {
			return true, iv
		}
	}
	return false, source.Event{}
}

// nearest returns how far a point sits from the closest interval, and which.
func nearest(p source.Event, intervals []source.Event) (time.Duration, source.Event) {
	var (
		best   time.Duration
		closer source.Event
	)
	for i, iv := range intervals {
		var d time.Duration
		switch {
		case p.Start.Before(iv.Start):
			d = iv.Start.Sub(p.Start)
		case p.Start.After(iv.End):
			d = p.Start.Sub(iv.End)
		}
		if i == 0 || d < best {
			best, closer = d, iv
		}
	}
	return best, closer
}

// frontmost says which application held focus at an instant, which is what
// explains most misses: the autosave timer does not wait for the application
// to be frontmost.
func frontmost(focus []source.Event, at time.Time) string {
	for _, e := range focus {
		if e.Shape != source.Interval || e.Duration() == 0 {
			continue
		}
		if !at.Before(e.Start) && !at.After(e.End) {
			return e.Identity.App
		}
	}
	return "nothing recorded"
}

// sameDay returns the intervals observed on a given local day. It buckets on
// the endpoint each event actually has, because an unbounded event's missing
// side is the zero time and would bucket into the year 1.
func sameDay(events []source.Event, day string) []source.Event {
	var out []source.Event
	for _, e := range events {
		if local(e.Anchor()).Format(dayLayout) == day {
			out = append(out, e)
		}
	}
	return out
}

// name is what an event is about, preferring the grouping over the file.
func name(e source.Event) string {
	if e.Identity.Project != "" {
		return e.Identity.Project
	}
	return e.Identity.Document
}

// local puts an instant in the machine's zone. Biome timestamps arrive in UTC
// and filename timestamps are parsed as local, so printing either as it comes
// would put two different zones in adjacent columns.
func local(t time.Time) time.Time { return t.In(time.Local) }
