// Command quietperiod calibrates the rule `timetattle status` uses to tell a
// source that has nothing to say from a source that has silently stopped.
//
// # The problem it measures
//
// status used to measure the age of the last HARVEST and never the age of the
// newest EVIDENCE. On 2026-08-13 at 15:04Z the newest harvest run was 1h59m old
// and the newest event was 12h12m old, and the report was green. But twelve
// hours of no evidence at ten in the morning is an ordinary night, and the same
// twelve hours in the middle of a working day is a collector that has died.
// Reading either as a fault by the clock fires every single morning.
//
// The rule that answers it: a source's silence is measured as its WITNESSED LAG
// — how far behind the newest evidence in the whole store its own newest
// evidence sits — so a night nobody records through costs every source nothing,
// and a source only falls behind while some OTHER source is recording. It is
// judged against QUIET_SLACK times the largest witnessed lag that source has
// ever closed inside its own collected era. A source with no closed lag at all
// is never judged, because nothing recorded can say what quiet looks like for
// it. See internal/store/evidence.go and cmd/timetattle/status_evidence.go.
//
// # What this reports, and why each half is needed
//
// FALSE ALARMS is the half that matters most, and it is a replay rather than an
// opinion — the same method scripts/runningcheck uses for the stranded-run rule.
// It walks a grid of instants across the collected era and, at each one,
// re-derives the bound from ONLY the lags that had closed before that instant —
// all the evidence status would have had if it had been run then — and asks
// whether the source's lag at that moment exceeded it. Every hit is a moment at
// which this rule would have reported a healthy, still-recording source as
// stopped.
//
// It is measured over a time grid rather than over arrivals, and that is
// deliberate. status is run at whatever moment a person runs it, not at the
// instant a source records, and long silences are over-represented in time
// exactly as they are under-represented in arrivals. Counting arrivals would
// flatter the rule.
//
// NOW is the other half, and a rule with no hits there would be useless rather
// than safe: what every source's lag is at this moment and what the rule makes
// of it.
//
//	go run ./scripts/quietperiod                 # the live store, read-only
//	go run ./scripts/quietperiod -slack 1        # what a bare maximum would cost
//	go run ./scripts/quietperiod -grid 1m        # a finer replay
//	go run ./scripts/quietperiod -store copy.db
//
// Nothing is written. The store is opened read-only and is never created.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// defaultSlack and defaultFloor must match statusQuietSlack and
// statusQuietSample in cmd/timetattle/status_evidence.go, which is package main
// and cannot be imported. They are restated rather than shared, and -slack and
// -min exist so that a candidate change can be costed here before it is made
// there.
const (
	defaultSlack = 4
	defaultFloor = 20
)

// defaultQuiet is the shortest silence that may be reported, and in the program
// it is NOT a constant: it is the installed launchd schedule's own interval,
// read from the plist, so that it follows the agent down to hourly with nothing
// edited. Four hours is what the schedule installed on 2026-08-13 gives -- six
// StartCalendarInterval slots at 00, 04, 08, 12, 16 and 20 -- and -quiet exists
// so that another cadence can be costed here.
//
// The reason there is a floor at all: evidence cannot arrive faster than the
// harvest brings it in, so a silence shorter than one interval is not evidence
// that a source has stopped. It may simply be that the next harvest has not run.
const defaultQuiet = 4 * time.Hour

func main() {
	path := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	slack := flag.Int("slack", defaultSlack, "multiple of a source's largest closed witnessed lag")
	floor := flag.Int("min", defaultFloor, "closed lags a source needs before it is judged at all")
	grid := flag.Duration("grid", 10*time.Minute, "how finely the replay samples the collected era")
	quiet := flag.Duration("quiet", defaultQuiet,
		"shortest silence that may be reported; the program derives this from the installed schedule")
	episodes := flag.Bool("episodes", false, "list every silence that would have alarmed, and where in the source's history it fell")
	flag.Parse()

	if err := run(*path, *slack, *floor, *quiet, *grid, *episodes); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(path string, slack, floor int, quiet, grid time.Duration, episodes bool) error {
	if path == "" {
		var err error
		if path, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	ev, err := st.Evidence(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("store  %s\nslack  %d\nmin    %d closed lags\nquiet  %s floor\ngrid   %s\n\n",
		path, slack, floor, quiet, grid)

	frontier := time.Time{}
	for _, e := range ev {
		if e.Newest.After(frontier) {
			frontier = e.Newest
		}
	}

	fmt.Printf("NOW — the frontier is %s\n\n", frontier.UTC().Format(time.RFC3339))
	fmt.Printf("  %-24s %8s %8s %10s %10s   %s\n",
		"source", "events", "lags", "longest", "lag now", "verdict")
	for _, e := range ev {
		longest, ok := e.LongestLag()
		if !ok || len(e.Lags) < floor {
			why := "no closed lag inside its collected era: not judged"
			if ok {
				why = fmt.Sprintf("only %d closed lags, under the %d needed: not judged", len(e.Lags), floor)
			}
			fmt.Printf("  %-24s %8d %8d %10s %10s   %s\n", e.Source, e.Events, len(e.Lags), "-", "-", why)
			continue
		}
		lag := frontier.Sub(e.Newest)
		verdict := "keeping up"
		if lag > max(longest*time.Duration(slack), quiet) {
			verdict = fmt.Sprintf("SILENT, %.2fx its own longest", float64(lag)/float64(longest))
		}
		fmt.Printf("  %-24s %8d %8d %10s %10s   %s\n", e.Source, e.Events, len(e.Lags),
			round(longest), round(lag), verdict)
	}

	fmt.Printf("\nFALSE ALARMS — replayed across the collected era, weighted by time on a %s grid\n\n", grid)
	fmt.Printf("  %-24s %10s %10s %10s   %s\n", "source", "sampled", "alarms", "share", "worst overshoot")
	for _, e := range ev {
		r := replay(e, slack, floor, quiet, grid)
		if r.sampled == 0 {
			fmt.Printf("  %-24s %10s %10s %10s   %s\n", e.Source, "-", "-", "-",
				"nothing to replay")
			continue
		}
		worst := "-"
		if r.worst > 0 {
			worst = fmt.Sprintf("%.2fx the bound then in force", r.worst)
		}
		fmt.Printf("  %-24s %10d %10d %9.2f%%   %s\n", e.Source, r.sampled, r.alarms,
			100*float64(r.alarms)/float64(r.sampled), worst)
		if !episodes {
			continue
		}
		for _, ep := range r.episodes {
			fmt.Printf("      lag %-9s past a bound of %-9s at closed lag %d of %d (%.0f%% in)\n",
				round(ep.lag), round(ep.bound), ep.at, ep.of, 100*float64(ep.at)/float64(ep.of))
		}
	}
	return nil
}

// result is one source's replay.
type result struct {
	sampled int
	alarms  int
	// worst is the largest ratio of a lag to the bound that was in force while
	// it was open.
	worst float64
	// episodes is one entry per silence that would have alarmed, so that "2% of
	// the era" can be read as what it actually is. An alarm share alone cannot
	// tell a rule that misfires steadily from one that misfires once, on a new
	// record, and never again — and those are different rules.
	episodes []episode
}

// episode is one silence the rule would have reported, with where in the
// source's own history it fell.
type episode struct {
	// at is the index of the closed lag in the source's series, and of is how
	// many it has, so a reader can see whether the alarms cluster in the early
	// record (a bound still growing) or are spread through it (a bound that is
	// simply wrong).
	at, of int
	lag    time.Duration
	bound  time.Duration
}

// replay asks, at every grid instant of the collected era, what the rule would
// have said with only the evidence that existed by then.
//
// The walk is over the lag SERIES rather than over instants, and the two are the
// same measurement. Between a source's k-th and (k+1)-th events its open lag
// grows from zero to Lags[k] while the bound sits still at the slack times the
// maximum of the first k lags — so the instants at which the rule would have
// fired are exactly the tail of that window past the bound, and their number is
// (Lags[k] - bound) / grid. Summing those against Lags[k] / grid time-weights the
// replay, which is the point: status is run when a person runs it, and a long
// silence is over-represented in time exactly as it is under-represented in
// arrivals. Counting arrivals instead would flatter the rule.
//
// A source with no closed lag is not replayed at all, because the rule does not
// judge it either.
func replay(e store.Evidence, slack, floor int, quiet, grid time.Duration) result {
	var (
		r       result
		longest time.Duration
	)
	for i, lag := range e.Lags {
		if i < floor {
			longest = max(longest, lag)
			continue
		}
		bound := max(longest*time.Duration(slack), quiet)
		r.sampled += int(lag / grid)
		if lag > bound {
			r.alarms += int(lag/grid) - int(bound/grid)
			if ratio := float64(lag) / float64(bound); ratio > r.worst {
				r.worst = ratio
			}
			r.episodes = append(r.episodes, episode{at: i, of: len(e.Lags), lag: lag, bound: bound})
		}
		longest = max(longest, lag)
	}
	return r
}

func round(d time.Duration) string {
	if d >= time.Hour {
		return fmt.Sprintf("%.2fh", d.Hours())
	}
	return d.Round(time.Second).String()
}
