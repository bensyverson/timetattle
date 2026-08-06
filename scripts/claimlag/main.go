// Command claimlag measures how long an artifact lags the work that produced it.
//
// # The question
//
// Every parameter internal/sessions takes is an attempt to recover unobserved
// time around artifacts. lead-in says work began before its first trace,
// tail-out says it continued after the last, and the gap threshold says how
// much silence is still one sitting. All three are currently guesses, labelled
// provisional in internal/config for that reason, and 74.9% of all engaged time
// is margin they create.
//
// A job claim is the one thing in this corpus that says when work STARTED and
// STOPPED without being an artifact. `job claim` is typed by a person at the
// moment they begin; `job done` when they finish. So for any claim, the
// distance from the claim opening to the first file save or commit inside it is
// a DIRECT MEASUREMENT of what lead-in is trying to estimate, and the distance
// from the last artifact to the close is tail-out.
//
// That is what this tool reports, per source and in aggregate.
//
// # Why the evidence is deliberately not job's own
//
// job-events records planning actions as points, and they fire the instant a
// claim opens — `job claim` writes a `claimed` row, which is the same event.
// Measuring the lag from a claim to its own bookkeeping would report zero and
// mean nothing. Only NON-job sources count as artifacts here: transcripts,
// gitlog, fs-observations, and the manifest-driven Motion and Blender sources.
//
// Presence and ignored streams are excluded too. knowledgec-backlit observes
// the machine rather than the work, and carries no project, so it cannot be
// matched to a claim at all.
//
// # What a claim is NOT
//
// A claim is a claim, not a work unit, and this tool does not pretend
// otherwise:
//
//   - Claims terminated by claim_expired are UNBOUNDED in the store and are
//     skipped entirely. The expiry instant is written lazily by whatever
//     command next runs a sweep, so it is not when anything happened.
//   - Long claims span breaks. -max-claim drops claims longer than a
//     threshold, because a claim held over lunch reports a lead that is really
//     an absence. The default is deliberately tight and the flag exists so the
//     sensitivity can be seen rather than assumed.
//   - A claim with NO artifact inside it is counted and reported separately.
//     It is not a measurement failure: it is planning or reading work that
//     produced no file, and the count is itself a finding about how much work
//     the artifact sources cannot see at all.
//
// # What this does NOT do
//
// It does not fit anything. It reports distributions and leaves the choice of
// parameter to PkY6I, deliberately, because a median is not a fit and the two
// should not be confused in a document.
//
// # Usage
//
//	go run ./scripts/claimlag
//	go run ./scripts/claimlag -max-claim 90m -by-source
package main

import (
	"context"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// defaultArtifactSources are the sources that observe a trace work left behind,
// MINUS claude-transcripts, and the omission is the whole point.
//
// # The co-emission trap, measured
//
// Excluding job's own sources is not enough to make this measurement honest.
// 92% of claims in this corpus are made by agents, and an agent runs `job
// claim` as a TOOL CALL — which internal/transcripts records as a transcript
// event at the same instant. The claim and its "first artifact" are then two
// records of one keystroke.
//
// Including transcripts, 705 of 719 claims had one as their first artifact,
// median lead 0s and p95 1s, and the aggregate was 98.5% within a minute. That
// is not a finding that work leaves traces instantly; it is the tool measuring
// the distance from an event to itself. The same applies to the tail, since
// `job done` is also a tool call, and to the internal-silence distribution,
// which under transcripts measures an agent's turn cadence rather than a
// person's work rhythm.
//
// Pass -with-transcripts to reproduce the contaminated figure. It is left
// reachable deliberately, because a number this clean is exactly the kind that
// gets quoted, and the way to stop that is to show what it really measures.
var defaultArtifactSources = map[string]bool{
	"gitlog-commits":  true,
	"gitlog-reflog":   true,
	"fs-observations": true,
	"motion-autosave": true,
	"blender-backups": true,
}

func main() {
	dbPath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	maxClaim := flag.Duration("max-claim", 2*time.Hour, "skip claims longer than this, since a long claim spans breaks")
	bySource := flag.Bool("by-source", false, "break the lead distribution down by which source produced the first artifact")
	withTranscripts := flag.Bool("with-transcripts", false, "include claude-transcripts, which are CO-EMITTED with an agent's own job commands — see this file's doc comment")
	flag.Parse()

	if err := run(*dbPath, *maxClaim, *bySource, *withTranscripts); err != nil {
		fmt.Fprintf(os.Stderr, "claimlag: %v\n", err)
		os.Exit(1)
	}
}

func run(dbPath string, maxClaim time.Duration, bySource, withTranscripts bool) error {
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		dbPath = filepath.Join(home, ".timetattle", "timetattle.db")
	}

	st, err := store.OpenReadOnly(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		return err
	}

	sources := map[string]bool{}
	maps.Copy(sources, defaultArtifactSources)
	if withTranscripts {
		sources["claude-transcripts"] = true
	}

	claims, artifacts := split(events, sources)
	if len(claims) == 0 {
		return fmt.Errorf("no bounded job-claims intervals in %s — has a harvest run since internal/jobs landed?", dbPath)
	}
	// Sorting once lets each claim's window be found by binary search rather
	// than by scanning 800k events per claim.
	for p := range artifacts {
		sort.Slice(artifacts[p], func(i, j int) bool { return artifacts[p][i].Start.Before(artifacts[p][j].Start) })
	}

	report(dbPath, maxClaim, bySource, withTranscripts, claims, artifacts)
	return nil
}

func split(events []source.Event, sources map[string]bool) ([]source.Event, map[string][]source.Event) {
	var claims []source.Event
	artifacts := map[string][]source.Event{}

	for _, e := range events {
		switch {
		case e.Source == "job-claims":
			// Intervals only. An expired claim is Unbounded and its end was
			// never observed, so it can measure nothing.
			if e.Shape == source.Interval && !e.Start.IsZero() && !e.End.IsZero() {
				claims = append(claims, e)
			}
		case sources[e.Source] && e.Identity.Project != "":
			p := e.Identity.Project
			artifacts[p] = append(artifacts[p], e)
		}
	}
	return claims, artifacts
}

// window returns the artifacts of one project falling inside [from, to].
func window(in []source.Event, from, to time.Time) []source.Event {
	lo := sort.Search(len(in), func(i int) bool { return !in[i].Start.Before(from) })
	hi := sort.Search(len(in), func(i int) bool { return in[i].Start.After(to) })
	return in[lo:hi]
}

type sample struct {
	lead, tail, maxGap time.Duration
	firstSource        string
}

func report(dbPath string, maxClaim time.Duration, bySource, withTranscripts bool, claims []source.Event, artifacts map[string][]source.Event) {
	fmt.Printf("store: %s\n", dbPath)
	if withTranscripts {
		fmt.Println("WARNING: claude-transcripts included. An agent's `job claim` IS a tool call,\n" +
			"so the claim and its first transcript record are two records of one keystroke.\n" +
			"Every figure below is contaminated by that co-emission.")
	}
	fmt.Printf("claims: %d bounded intervals; skipping any longer than %s\n\n", len(claims), maxClaim)

	var samples []sample
	var tooLong, empty, noProject int

	for _, c := range claims {
		if c.Identity.Project == "" {
			noProject++
			continue
		}
		if c.End.Sub(c.Start) > maxClaim {
			tooLong++
			continue
		}
		in := window(artifacts[c.Identity.Project], c.Start, c.End)
		if len(in) == 0 {
			empty++
			continue
		}

		s := sample{
			lead:        in[0].Start.Sub(c.Start),
			tail:        c.End.Sub(in[len(in)-1].Start),
			firstSource: in[0].Source,
		}
		for i := 1; i < len(in); i++ {
			if g := in[i].Start.Sub(in[i-1].Start); g > s.maxGap {
				s.maxGap = g
			}
		}
		samples = append(samples, s)
	}

	fmt.Printf("%6d claims measured\n", len(samples))
	fmt.Printf("%6d skipped, longer than %s\n", tooLong, maxClaim)
	fmt.Printf("%6d skipped, no artifact of any kind inside the claim\n", empty)
	if noProject > 0 {
		fmt.Printf("%6d skipped, claim carries no project\n", noProject)
	}
	if len(samples) == 0 {
		fmt.Println("\nNothing measurable. Every claim was skipped.")
		return
	}
	fmt.Printf("\n%.1f%% of claims short enough to measure contained no artifact at all — work\n"+
		"the file and git sources cannot see, and a floor under how much any\n"+
		"artifact-only model must be missing.\n",
		100*float64(empty)/float64(empty+len(samples)))

	leads := make([]time.Duration, len(samples))
	tails := make([]time.Duration, len(samples))
	gaps := make([]time.Duration, len(samples))
	for i, s := range samples {
		leads[i], tails[i], gaps[i] = s.lead, s.tail, s.maxGap
	}

	fmt.Println("\nLEAD — claim opens, then the first artifact appears this much later.")
	fmt.Println("This is what lead-in estimates. A claim is typed when work begins.")
	printDist(leads)

	fmt.Println("\nTAIL — last artifact, then the claim closes this much later.")
	fmt.Println("This is what tail-out estimates.")
	printDist(tails)

	fmt.Println("\nLARGEST SILENCE INSIDE ONE CLAIM — a stretch of continuous declared work")
	fmt.Println("with no artifact in it. The gap threshold must exceed these or it splits")
	fmt.Println("a sitting the person considered unbroken.")
	printDist(gaps)

	if bySource {
		fmt.Println("\nLEAD, by the source that produced the first artifact")
		byFirst := map[string][]time.Duration{}
		for _, s := range samples {
			byFirst[s.firstSource] = append(byFirst[s.firstSource], s.lead)
		}
		names := make([]string, 0, len(byFirst))
		for n := range byFirst {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			fmt.Printf("  %-20s n=%-5d ", n, len(byFirst[n]))
			printInline(byFirst[n])
		}
	}
}

func printDist(d []time.Duration) {
	slices.Sort(d)
	fmt.Printf("  n=%d  min %s  p25 %s  median %s  p75 %s  p95 %s  max %s\n",
		len(d), rnd(d[0]), rnd(pct(d, 25)), rnd(pct(d, 50)), rnd(pct(d, 75)), rnd(pct(d, 95)), rnd(d[len(d)-1]))

	// The share under each configured default is the actionable number: it says
	// what fraction of real cases the current guess already covers.
	for _, ref := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute} {
		var n int
		for _, v := range d {
			if v <= ref {
				n++
			}
		}
		fmt.Printf("  %5.1f%% within %s\n", 100*float64(n)/float64(len(d)), ref)
	}
}

func printInline(d []time.Duration) {
	slices.Sort(d)
	fmt.Printf("median %s  p95 %s\n", rnd(pct(d, 50)), rnd(pct(d, 95)))
}

func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := (len(sorted) - 1) * p / 100
	return sorted[i]
}

// rnd trims sub-second noise, which is never meaningful at this scale and makes
// a table unreadable.
func rnd(d time.Duration) string {
	s := d.Round(time.Second).String()
	return strings.TrimSuffix(s, "0s0ms")
}
