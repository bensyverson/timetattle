package metrics

import (
	"fmt"
	"slices"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// THE ACTIVITY MIX: which KIND of evidence stream observed the work.
//
// Raised because the report could say a week held 40 hours and could not say
// how much of that was Claude, how much was git and how much was a file
// changing on disk. Every figure above this one is about WHOSE an hour was;
// this one is about WHAT KIND OF WORK it was, and the two are different
// questions asked of the same clock.
//
// # It is not a breakdown, and columns that summed to the total would be false
//
// Sources overlap in time. A commit made in the middle of a Claude Code session
// is observed by gitlog AND by claude-transcripts, and there is no principled
// way to award that minute to one of them: both saw it. Engaged time therefore
// has NO partition by source, and the same arithmetic the client roll-up
// already lives under applies one axis over — a family's figure is a UNION, the
// percentages overlap, and the rows never sum to presence.
//
// So each family carries two figures rather than one:
//
//	Engaged   the presence this family's own evidence clusters to. It says the
//	          family PARTICIPATED in that clock, alongside whatever else did.
//	Solely    the part of it no other family reaches: what would drop out of
//	          this section if the stream were not there.
//
// The Solely figures ARE disjoint, and Unreached is what neither of them
// covers, so the mix does close — see ActivityMix.
//
// # A family's clock is its own evidence re-clustered, exactly as a client's is
//
// The alternative is crediting a family the whole of any sitting its evidence
// appears anywhere inside, and this package has already measured that model
// wrong one level down: applied to projects it summed to 24.01h across a 12.78h
// day. Applied to families it would credit Git the whole of a four-hour
// afternoon on the strength of one commit at the end of it. reduce is therefore
// handed the family buckets exactly as it is handed the project and client
// ones, and there is one implementation of the rule for all three.
//
// NOTE that cmd/timetattle's dashboard partitions SITTINGS by family
// combination, which is the rejected model applied to a different question, and
// legitimately so: it asks what evidenced a sitting, treating the sitting as
// atomic. Its per-family figure can never be SMALLER than this one — a family's
// own cluster cannot reach outside the global sittings, since the global
// clustering sees strictly more events at the same gap, and it contributes
// nothing to a sitting holding none of its evidence. The two are not the same
// measurement and must not be subtracted from each other.
//
// # What is in the mix and what is not
//
// EVIDENCE sources only. A presence source bounds sittings and creates none, and
// an ignored source is read and dropped, so neither can be responsible for any
// engaged time — a row for them would be a figure about work the stream cannot
// make. The backlight's own four figures are reported separately, which is where
// a reader asks what the screen was doing.
//
// Evidence that no project could be attributed IS in the mix, and that is
// deliberate. Biome and knowledgeC carry no path and appear in no project row
// and no client row anywhere in the report; the mix is the one place they are
// counted, and leaving them out would answer "what kind of work was this" using
// only the streams that happen to resolve to a directory.

// Family is one kind of evidence stream's share of the clock.
type Family struct {
	// Name is the family, from internal/source's one mapping — or
	// Unclassified for observations from an id nobody has classified.
	Name string
	// Sources names the source ids that actually contributed to this row IN
	// THIS RUN, sorted, and is the mapping made visible to a reader.
	//
	// It is derived from the corpus rather than printed from the table,
	// because a grouping a reader cannot check is a verdict whose list nobody
	// has seen — the standing rule about tools fed a list. Read from the
	// table it would name streams this window never held; read from the
	// corpus it names exactly what produced the figures beside it, and a
	// newly shipped collector shows up in the report the first time it
	// harvests.
	Sources []string
	// Events is how many observations this rests on.
	Events int
	// Engaged is the presence this family's own evidence clusters to: the
	// clock it PARTICIPATED in, which overlaps every other family's. Observed
	// is the part an observation actually spanned, which is zero for a family
	// of instants and real for one carrying intervals — a held job claim, a
	// focus interval.
	Engaged Span
	// Solely is the part of Engaged.Total no OTHER family reaches.
	//
	// It is what would drop out of this section without the stream, which is
	// not quite the same as what would drop out of the REPORT: presence is
	// clustered from every source at once, so removing one would also move the
	// sittings the whole report is an intersection against. That is a
	// different question and this figure does not answer it.
	Solely time.Duration
}

// Unclassified is the row carrying observations from a source id no family has
// been decided for.
//
// It is not a family and is named so it cannot be mistaken for one. A manifest
// may declare any id, so the mapping is a list, and a list goes stale silently —
// in the direction that makes a report look complete. Dropping those
// observations would be the "observed not-X versus nothing was recorded"
// collapse this package exists to refuse; guessing a family for them would
// assert a grouping nobody made. Naming the ids does neither, and puts the
// decision in front of the one person who can take it.
const Unclassified = "unclassified"

// ActivityMix is what kinds of evidence observed the work, and how much of the
// clock none of them reaches.
type ActivityMix struct {
	// Families, sorted by name. THEY OVERLAP AND NEVER SUM.
	Families []Family
	// Unreached is presence no family's own evidence clusters to.
	//
	// It is the third state and it is a real one: silence the GLOBAL gap
	// threshold bridged between two different streams, which neither stream
	// bridges by itself. Ten minutes of Claude, a commit twelve minutes later
	// and more Claude twelve minutes after that is one sitting, and no single
	// family's own clustering covers the whole of it. Awarding that clock to a
	// family would credit a stream with time it never reached; omitting it
	// would make the rows look like a partition of presence.
	//
	// Every family's Solely plus this is exactly presence, and Compute checks
	// it before anybody reads it.
	Unreached time.Duration
}

// Family finds one family's row by name.
func (m ActivityMix) Family(name string) (Family, bool) {
	for _, f := range m.Families {
		if f.Name == name {
			return f, true
		}
	}
	return Family{}, false
}

// familyOf is the family a source's observations are counted under, with
// Unclassified standing in for an id nobody has classified.
func familyOf(sourceID string) string {
	if f, ok := source.FamilyOf(sourceID); ok {
		return string(f)
	}
	return Unclassified
}

// mixOf builds the activity mix from the family level and the sittings it was
// intersected against.
//
// # This section checks itself, because its own rows cannot be added
//
// The participation figures OVERLAP and must never be summed, which leaves the
// mix with no arithmetic a reader could catch it out by — and a column of
// plausible overlapping hours nobody can subtract is exactly the shape of
// wrongness this project keeps shipping. Two things here are disjoint, and both
// are asserted before anybody sees a figure:
//
//  1. What one family holds ALONE, computed twice by different code: by
//     subtracting the other families from it, and by a sweep counting how many
//     families cover each instant. Pairwise subtraction and a depth count are
//     independent computations of the same quantity.
//  2. Clock held by exactly one family, clock two or more reached, and clock
//     none reaches must TILE presence. Nothing else in the report would notice
//     a family credited a stretch another one also reached.
func mixOf(l level, byFamily map[string][]source.Event, ids map[string]map[string]bool,
	presence []span) (ActivityMix, error) {
	m := ActivityMix{Families: make([]Family, 0, len(l.names))}

	runs := make([][]span, 0, len(l.names))
	var covered []span
	for _, name := range l.names {
		runs = append(runs, l.active[name])
		covered = append(covered, l.active[name]...)
	}
	covered = union(covered)
	m.Unreached = total(subtract(presence, covered))

	var alone time.Duration
	for _, name := range l.names {
		var others []span
		for _, other := range l.names {
			if other != name {
				others = append(others, l.active[other]...)
			}
		}
		others = union(others)

		mine := total(l.active[name])
		only := total(subtract(l.active[name], others))
		shared := total(intersect(l.active[name], others))
		if only+shared != mine {
			return ActivityMix{}, fmt.Errorf("the %s family holds %v alone and %v shared with the "+
				"others, coming to %v against the %v it participated in; subtracting the other "+
				"families and intersecting them are two computations of the same split and they "+
				"disagree", name, only, shared, only+shared, mine)
		}
		alone += only

		sources := make([]string, 0, len(ids[name]))
		for id := range ids[name] {
			sources = append(sources, id)
		}
		slices.Sort(sources)

		m.Families = append(m.Families, Family{
			Name:    name,
			Sources: sources,
			Events:  len(byFamily[name]),
			Engaged: Span{Total: mine, Observed: total(l.evidenced[name])},
			Solely:  only,
		})
	}

	once, twiceOrMore := depths(runs)
	if once != alone {
		return ActivityMix{}, fmt.Errorf("the families hold %v of presence alone by subtraction and "+
			"%v by counting how many of them cover each instant, off by %v; a family has been "+
			"credited clock another family also reaches", alone, once, alone-once)
	}
	if got := once + twiceOrMore + m.Unreached; got != total(presence) {
		return ActivityMix{}, fmt.Errorf("the activity mix accounts for %v held by one family, %v "+
			"held by more than one and %v reached by none, coming to %v against %v of presence, "+
			"off by %v; the rows overlap and never sum, but these three must tile the clock",
			once, twiceOrMore, m.Unreached, got, total(presence), got-total(presence))
	}
	return m, nil
}

// depths sweeps a set of disjoint, ordered runs and reports how much clock
// exactly one of them covers, and how much two or more do.
//
// It is deliberately a different algorithm from the subtraction above rather
// than a tidier way of writing it. Two implementations of one quantity that
// agree are a check; one implementation called twice is a comment.
func depths(runs [][]span) (once, twiceOrMore time.Duration) {
	type edge struct {
		at   time.Time
		step int
	}
	var edges []edge
	for _, run := range runs {
		for _, s := range run {
			if s.end.After(s.start) {
				edges = append(edges, edge{s.start, +1}, edge{s.end, -1})
			}
		}
	}
	if len(edges) == 0 {
		return 0, 0
	}
	// Closing edges are applied before opening ones at the same instant, so two
	// runs that merely touch are never read as overlapping for zero clock.
	sort.Slice(edges, func(i, j int) bool {
		if !edges[i].at.Equal(edges[j].at) {
			return edges[i].at.Before(edges[j].at)
		}
		return edges[i].step < edges[j].step
	})

	depth := 0
	at := edges[0].at
	for _, e := range edges {
		if d := e.at.Sub(at); d > 0 {
			switch {
			case depth == 1:
				once += d
			case depth > 1:
				twiceOrMore += d
			}
			at = e.at
		}
		depth += e.step
	}
	return once, twiceOrMore
}
