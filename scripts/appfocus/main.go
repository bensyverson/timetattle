// Command appfocus reports what the Biome ScreenTime.AppUsage stream
// currently holds: how far back it reaches, where the time went, and how much
// of it could not be paired into intervals.
//
// It exists to be re-run. Biome rolls segments and eventually discards them,
// so retention depth drifts. The unpaired count is the number worth watching:
// those are focus sessions that began while macOS was not recording
// transitions — the machine woke with an application already frontmost — and
// they carry no duration. Roughly one stop in sixty was unpairable when this
// was written. A sharp rise would mean the stream had changed shape under us
// and the intervals below had quietly stopped describing the day.
//
// Like the probes in scripts/spike, this is an analysis tool rather than
// product behaviour and is exempt from the project's red/green TDD rule.
//
// Usage:
//
//	go run ./scripts/appfocus                 # the current user's stream
//	go run ./scripts/appfocus -dir path/to    # a copied stream
//	go run ./scripts/appfocus -since 168h     # only the last week
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/source"
)

func main() {
	dir := flag.String("dir", "", "directory of Biome segments (default: the current user's)")
	window := flag.Duration("since", 0, "report only events ending within this long ago")
	flag.Parse()

	if err := run(*dir, *window); err != nil {
		fmt.Fprintf(os.Stderr, "appfocus: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string, window time.Duration) error {
	if dir == "" {
		var err error
		if dir, err = biome.DefaultAppUsageDir(); err != nil {
			return err
		}
	}

	var since time.Time
	if window > 0 {
		since = time.Now().Add(-window)
	}

	events, err := biome.NewAppUsage(dir).Collect(context.Background(), since)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		fmt.Printf("%s holds no focus events\n", dir)
		return nil
	}

	type stat struct {
		app       string
		spans     int
		unpaired  int
		total     time.Duration
		longest   time.Duration
		firstSeen time.Time
	}
	byApp := map[string]*stat{}
	var unpaired int
	byReason := map[source.Unknown]int{}
	for _, e := range events {
		s := byApp[e.Identity.App]
		if s == nil {
			s = &stat{app: e.Identity.App, firstSeen: e.Anchor()}
			byApp[e.Identity.App] = s
		}
		if e.Shape == source.Unbounded {
			s.unpaired++
			unpaired++
			byReason[e.Unknown]++
			continue
		}
		s.spans++
		s.total += e.Duration()
		if e.Duration() > s.longest {
			s.longest = e.Duration()
		}
	}

	stats := make([]*stat, 0, len(byApp))
	for _, s := range byApp {
		stats = append(stats, s)
	}
	sort.Slice(stats, func(i, j int) bool { return stats[i].total > stats[j].total })

	first, last := span(events)
	fmt.Printf("%s\n%d events covering %s to %s (%s)\n",
		dir, len(events),
		first.Local().Format("2006-01-02 15:04"), last.Local().Format("2006-01-02 15:04"),
		last.Sub(first).Round(time.Hour))
	fmt.Printf("%d intervals with duration, %d observations without one (%.1f%%)\n",
		len(events)-unpaired, unpaired, 100*float64(unpaired)/float64(len(events)))
	// Horizon should be a handful — one per application frontmost when the
	// stream was read. Sleep is the number worth watching over time. Lost has
	// never been observed, and a nonzero count means macOS has started writing
	// a sequence this reader does not understand.
	fmt.Printf("  unpaired by reason: %d horizon (resolve next harvest), %d sleep (bound by presence), %d lost\n\n",
		byReason[source.UnknownHorizon], byReason[source.UnknownSleep], byReason[source.UnknownLost])

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "APPLICATION\tFOCUS TIME\tSESSIONS\tLONGEST\tUNPAIRED")
	var total time.Duration
	for _, s := range stats {
		total += s.total
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%d\n",
			s.app, s.total.Round(time.Minute), s.spans, s.longest.Round(time.Second), s.unpaired)
	}
	fmt.Fprintf(w, "\t%s\t%d\t\t%d\n", total.Round(time.Minute), len(events)-unpaired, unpaired)
	return w.Flush()
}

// span is the window the events cover. It reads the endpoint each event
// actually has: an unbounded event carries a zero time on one side, which
// would otherwise drag the window back to the year 1.
func span(events []source.Event) (time.Time, time.Time) {
	first, last := events[0].Anchor(), events[0].Anchor()
	for _, e := range events {
		if e.Anchor().Before(first) {
			first = e.Anchor()
		}
		if !e.End.IsZero() && e.End.After(last) {
			last = e.End
		}
		if e.Anchor().After(last) {
			last = e.Anchor()
		}
	}
	return first, last
}
