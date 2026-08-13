package metrics

import (
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// One project measured against sittings that were derived somewhere else.
//
// # Why this exists at all, and it is a cost argument rather than a modelling one
//
// Compute clusters the WHOLE corpus to establish presence, and that clustering
// is what a report costs: over the fourteen days to 2026-08-04 the live store
// returns 212,094 events in 0.9s and `timetattle metrics` takes 14.9s, so
// better than nine tenths of the wall clock is the clustering and not the read.
// That is the right trade for a report somebody asked for. It is the wrong
// trade for a headline printed by the BARE command, which a person also types
// to find out what the commands are, and which therefore may not cost fifteen
// seconds.
//
// The stored derivation is the way out. `timetattle sessionize` has already
// clustered the corpus and written derived_session, and reading it back costs
// 13ms. So a caller with the global sittings in hand can measure one project
// against them without re-establishing presence.
//
// # It is NOT a second way of computing engaged time
//
// That would be the two-overlapping-systems failure this repository keeps
// recording, and the two figures would diverge the first time either changed.
// This calls the same reduce Compute calls, with a map of one name, and
// intersects the result with the sittings it is given exactly as Compute
// intersects with the ones it derived. Handed Compute's own sittings it returns
// Compute's own answer, to the nanosecond, which
// TestAgainstGivesTheSameFigureComputeDoes asserts rather than assumes.
//
// # What it deliberately cannot tell you
//
// There is no allocation here and there could not be. An allocated share is a
// division of an instant between the groups active in it, so it is a fact about
// every project at once; asking for one project's allocation in isolation is
// asking a question with a missing operand. A caller wanting it has to compute
// the whole report, which is the expensive thing this exists to avoid — and
// that is the honest answer rather than a plausible number.

// Windowed is what one project accounts for inside a window, measured against
// sittings derived elsewhere.
type Windowed struct {
	// Project is the project's share. Allocated is always zero and means
	// NOTHING WAS COMPUTED rather than a share of none: allocation divides an
	// instant between every group active in it, and this reduced one group.
	Project Share
	// Sittings is the length distribution of the project's own active spans.
	Sittings Distribution
	// Presence is the clock the sittings given cover, counted once. It is the
	// denominator the project's share is a share of.
	Presence time.Duration
	// Events is how many of this project's observations the figure rests on.
	// Zero engaged hours across zero observations and zero engaged hours across
	// two hundred are different facts, and a headline that printed only the
	// hours could not tell a quiet fortnight from a collection outage.
	Events int
}

// Against measures one project against the sittings it is handed.
//
// The sittings are the global ones — read from derived_session, typically — and
// are never widened by this project's own evidence. A project's engaged time is
// the INTERSECTION of its own re-clustered evidence with the presence somebody
// else established; adding to that presence out of one project's evidence would
// let a single client's activity manufacture the denominator it is measured
// against.
func Against(name string, events []source.Event, presence []timespan.Span,
	p sessions.Params) (Windowed, error) {
	if err := p.Validate(); err != nil {
		return Windowed{}, err
	}
	global := make([]span, 0, len(presence))
	for _, s := range presence {
		global = append(global, span{s.Start, s.End})
	}
	global = union(global)

	roles := rolesOf(p)
	var (
		mine        []source.Event
		bounds      []source.Event
		observedRaw []span
	)
	for _, e := range events {
		switch roles[e.Source] {
		case roleIgnored:
			continue
		case rolePresence:
			bounds = append(bounds, e)
			continue
		}
		// Every evidence source counts toward what CORROBORATES the hours,
		// whether or not it says whose they are: a Biome focus interval is the
		// case, and it is the whole of the corroboration on this corpus.
		observedRaw = append(observedRaw, rawSpan(e))
		if e.Identity.Project == name {
			mine = append(mine, e)
		}
	}

	// One name through the same reduction every group in a report goes through.
	// Unwatched: this is one group over a window, not the pass whose cost a
	// progress display exists to explain.
	level, err := reduce(map[string][]source.Event{name: mine}, bounds, global, p, watch{}, PhaseProjects)
	if err != nil {
		return Windowed{}, err
	}
	active := level.active[name]
	out := Windowed{
		Presence: total(global),
		Events:   len(mine),
		Sittings: distribute(active),
		Project: Share{
			Name:         name,
			Engaged:      Span{Total: total(active), Observed: total(level.evidenced[name])},
			Corroborated: total(intersect(union(observedRaw), active)),
			Attested:     total(level.attested[name]),
			Weak:         total(level.weak[name]),
		},
	}
	// The remainder, computed rather than accumulated, so that what nothing
	// accounts for cannot be anything but what the other two leave behind.
	out.Project.Unaccounted = out.Project.Engaged.Total - out.Project.Attested - out.Project.Weak
	return out, nil
}
