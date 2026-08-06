package fit

import (
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// Narrowing is what Narrow dropped and what it kept.
//
// It exists because narrowing a corpus is exactly the kind of silent truncation
// this project has shipped before: scripts/sleepcheck clipped to a shared
// window, reported "largest gap 1h27m", and had excluded a 6h23m gap that ended
// 26 seconds before the window opened. A narrowing that did not say what it
// removed would read as a search over everything.
type Narrowing struct {
	Read, Kept, Dropped int
	// Evidence and Presence are how many of the kept events are of each kind.
	Evidence, Presence int
	// Ignored counts events dropped because the parameters ignore their source.
	// They affect no sitting, so removing them is free, and saying so is not.
	Ignored int
	// Reach is the threshold the closure was taken at: no chain of events
	// separated by less than this was cut.
	Reach time.Duration
	// Runs is how many maximal chains the evidence formed, and RunsKept how
	// many of them could reach a window.
	Runs, RunsKept int
}

// Narrow reduces a corpus to the events that can affect what is derived inside
// windows, at any point of a grid whose Reach is the third argument. It is
// exact rather than a heuristic.
//
// The whole live store holds 923,000 events and one clustering of it takes half
// a minute, so a two-hundred-point grid over the whole corpus is a two-hour
// wait. The labelled days are sixteen. Narrowing is what makes the search
// runnable, and it is only admissible if it changes no answer inside the
// windows, which is what the exactness is for and what
// TestNarrowDerivesTheSameSittingsInsideTheWindowAsTheWholeCorpus holds.
//
// # The closure rule
//
// Reach is the largest distance across which two observations can end up in one
// sitting under ANY point of the grid: the gap threshold merges at that
// distance, and so does the margin expansion, so it is the larger of the two
// (see Grid.Reach). Evidence is grouped into maximal chains no two consecutive
// members of which are further apart than Reach, and a chain is kept when its
// span, expanded by Reach, touches a window.
//
// A chain that fails that test cannot contribute to any sitting overlapping any
// window under any point of the grid, because every route by which it could is
// a step of at most Reach and every such step is already inside a chain. That
// is the whole argument, and it is why the runs are built at Reach rather than
// at each point's own gap: a closure taken at one point would be wrong at every
// other.
//
// # Presence, which subtracts and never adds
//
// A presence reading is kept where it overlaps a kept chain's reach, and only
// there. Darkness is subtracted pointwise, so a reading that does not overlap
// the region cannot change what is derived inside it — including a lit reading
// that contests a dark one somewhere else entirely, since the contest is
// resolved instant by instant.
//
// Anything whose extent cannot be read at all is KEPT. Being wrong in the
// direction of carrying too much costs a little time; being wrong in the other
// direction changes an answer and says nothing.
func Narrow(events []source.Event, windows []timespan.Span, reach time.Duration,
	p sittings.Params) ([]source.Event, Narrowing) {
	n := Narrowing{Read: len(events), Reach: reach}

	presence := make(map[string]bool, len(p.PresenceSources))
	for _, id := range p.PresenceSources {
		presence[id] = true
	}
	ignored := make(map[string]bool, len(p.IgnoreSources))
	for _, id := range p.IgnoreSources {
		ignored[id] = true
	}

	type placed struct {
		event source.Event
		span  timespan.Span
		known bool
	}
	var evidence, bounds []placed
	for _, e := range events {
		span, known := extent(e)
		switch {
		case ignored[e.Source]:
			n.Ignored++
		case presence[e.Source]:
			bounds = append(bounds, placed{e, span, known})
		default:
			evidence = append(evidence, placed{e, span, known})
		}
	}

	sort.SliceStable(evidence, func(i, j int) bool {
		return evidence[i].span.Start.Before(evidence[j].span.Start)
	})

	// Chains, built exactly the way Cluster builds clusters — against the
	// running maximum END rather than the previous start, because a long
	// interval early in a run can still be the latest end.
	type chain struct {
		span    timespan.Span
		members []placed
	}
	var chains []chain
	for _, x := range evidence {
		if !x.known {
			// Nothing can be said about where this reaches, so it travels with
			// whatever it is nearest and is never used to cut a chain.
			continue
		}
		if last := len(chains) - 1; last >= 0 && !x.span.Start.After(chains[last].span.End.Add(reach)) {
			chains[last].span.End = timespan.LaterOf(chains[last].span.End, x.span.End)
			chains[last].members = append(chains[last].members, x)
			continue
		}
		chains = append(chains, chain{span: x.span, members: []placed{x}})
	}
	n.Runs = len(chains)

	var kept []source.Event
	var region []timespan.Span
	for _, c := range chains {
		grown := timespan.Span{Start: c.span.Start.Add(-reach), End: c.span.End.Add(reach)}
		if !touches(grown, windows) {
			continue
		}
		n.RunsKept++
		region = append(region, grown)
		for _, m := range c.members {
			kept = append(kept, m.event)
			n.Evidence++
		}
	}
	region = timespan.Union(region)

	for _, x := range evidence {
		if !x.known {
			kept = append(kept, x.event)
			n.Evidence++
		}
	}
	for _, b := range bounds {
		if !b.known || touches(b.span, region) {
			kept = append(kept, b.event)
			n.Presence++
		}
	}

	n.Kept = len(kept)
	n.Dropped = n.Read - n.Kept
	return kept, n
}

// Reach is the closure distance a grid requires: the largest separation across
// which two observations can land in one sitting anywhere in it.
//
// Both merge rules are consulted. The gap threshold joins two clusters up to
// Gap apart; the margins join two up to LeadIn+TailOut apart after expansion,
// which is the whole of the inertness result. Taking only the first would cut a
// chain a grid with wide margins still joins.
func (g Grid) Reach() time.Duration {
	var gap, lead, tail time.Duration
	for _, d := range g.Gaps {
		gap = max(gap, d)
	}
	for _, d := range g.LeadIns {
		lead = max(lead, d)
	}
	for _, d := range g.TailOuts {
		tail = max(tail, d)
	}
	return max(gap, lead+tail)
}

// extent is the span an event covers, and whether that could be read at all.
//
// An unbounded event observed one endpoint and no duration, which is exactly
// how Cluster treats it: its anchor as an instant, and nothing else.
func extent(e source.Event) (timespan.Span, bool) {
	if e.Shape == source.Unbounded {
		at := e.Anchor()
		if at.IsZero() {
			return timespan.Span{}, false
		}
		return timespan.Span{Start: at, End: at}, true
	}
	if e.Start.IsZero() || e.End.IsZero() || e.End.Before(e.Start) {
		return timespan.Span{}, false
	}
	return timespan.Span{Start: e.Start, End: e.End}, true
}

func touches(s timespan.Span, spans []timespan.Span) bool {
	for _, w := range spans {
		if timespan.Overlaps(s, w) {
			return true
		}
	}
	return false
}
