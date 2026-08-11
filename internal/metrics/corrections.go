package metrics

import (
	"fmt"
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
)

// The human correction layer, read.
//
// internal/adjustments is where a person's answer is recorded and folded; this
// is the only place in the reporting stack that reads the attribution half of
// it. See Corrected for what an attribution assertion is allowed to move, and
// why the engagement half is deliberately left alone.

// Attribution is one resolved stretch of the correction log, re-exported so a
// caller can build a report without naming two packages for one idea. It is
// internal/adjustments' type unchanged, not a copy: a translation table between
// them is the sort of thing that stops being current without anything failing.
type Attribution = adjustments.Attribution

// Corrections is how much of a report is a person's word, and how much of what
// they said moved nothing.
//
// The four durations partition the clock the assertions cover, exactly, and
// Corrected refuses to return a report where they do not — a correction that
// evaporated between the log and the figure is the failure this whole layer
// exists to end, and it would otherwise arrive one level further in.
//
// The last two are the ones worth printing hardest. A reader who has recorded a
// correction and sees nothing move needs to be told WHICH of the two silences
// they are looking at: the machine already agreed, or the correction landed on
// clock no sitting covers and there was nothing there to move.
type Corrections struct {
	// Assertions is how many resolved pieces were applied — not how many rows
	// the log holds. Folding a correction to a correction leaves one piece
	// where two rows were recorded, which is the point of folding.
	Assertions int
	// Attributed is presence a person moved TO a named project, counted once
	// however many projects it was taken from.
	Attributed time.Duration
	// Unattributed is presence a person moved to NO project: "I looked, and
	// that hour was nobody's."
	Unattributed time.Duration
	// Agreed is asserted clock the machine had already answered the same way.
	// It moved no figure, and it is not nothing: it is the only evidence in the
	// system that somebody checked an attribution and found it right.
	Agreed time.Duration
	// Outside is asserted clock no sitting covers. An attribution assertion
	// says whose an hour was, never that there was one, so a correction over a
	// stretch the derivation does not reach moves nothing whatever. Recording
	// an engaged assertion is what makes the hour exist; this figure is how a
	// person finds out that is what they needed.
	Outside time.Duration
}

// Moved is the clock a report differs by because somebody corrected it.
func (c Corrections) Moved() time.Duration { return c.Attributed + c.Unattributed }

// Any reports whether anything was applied at all, which is a different
// question from whether anything moved.
func (c Corrections) Any() bool { return c.Assertions > 0 }

// account measures the assertions against what the machine said, BEFORE any of
// them is applied.
//
// active is the uncorrected per-project answer. The comparison has to be made
// here rather than by differencing two finished reports: a difference cannot
// tell "the machine already said this" from "the assertion moved clock in and
// an equal amount out", and those are the two states a reader most needs apart.
func account(said []Attribution, presence []span, active map[string][]span) (Corrections, error) {
	c := Corrections{Assertions: len(said)}

	all := make([]span, 0, 64)
	for _, run := range active {
		all = append(all, run...)
	}
	attributed := union(all)

	// only is the clock a project had TO ITSELF. An assertion naming a project
	// that was already active there alongside a second one is not agreement:
	// applying it takes the instant away from the second, which moves a figure.
	// Memoised, because the log names few projects and the subtraction walks
	// every other project's spans.
	sole := make(map[string][]span, 4)
	only := func(name string) []span {
		if run, ok := sole[name]; ok {
			return run
		}
		others := make([]span, 0, len(all))
		for other, run := range active {
			if other != name {
				others = append(others, run...)
			}
		}
		run := subtract(active[name], union(others))
		sole[name] = run
		return run
	}

	var asserted time.Duration
	for _, a := range said {
		piece := span{a.Start, a.End}
		asserted += piece.length()
		in := intersect([]span{piece}, presence)
		c.Outside += piece.length() - total(in)
		if a.Project == "" {
			// Already nobody's, by the machine's own answer.
			kept := subtract(in, attributed)
			c.Agreed += total(kept)
			c.Unattributed += total(in) - total(kept)
			continue
		}
		agreed := intersect(in, only(a.Project))
		c.Agreed += total(agreed)
		c.Attributed += total(in) - total(agreed)
	}

	if got := c.Attributed + c.Unattributed + c.Agreed + c.Outside; got != asserted {
		return Corrections{}, fmt.Errorf("%v of correction was recorded and %v of it is accounted "+
			"for; a person's assertion has gone missing between the log and the report, which is "+
			"the failure reading the log at all exists to end", asserted, got)
	}
	return c, nil
}

// correct lays the resolved assertions over one level of the machine's answer
// and reports, per name, the clock each one GAINED by them.
//
// target says who a piece belongs to at this level: the project itself lower
// down, the project's client higher up. One implementation for both tiers, for
// the reason level itself exists — the alternative is a roll-up corrected under
// subtly different rules from the rows beneath it.
//
// EVERY ASSERTED INSTANT IS TAKEN FROM EVERYONE IT WAS NOT ASSERTED TO. That is
// what makes the assertion single-valued, which is what the fold already
// resolved it to be, and it is why an assertion naming one of two concurrent
// projects genuinely changes the answer rather than merely confirming half of
// it. A piece whose target is empty — belonging to nobody, or to a project this
// level cannot place — is taken from everyone and given to no one.
//
// Evidence never moves. A project handed an hour by a person is not thereby
// observed for that hour, so evidenced, attested and weak are clipped to what
// survives rather than extended, and the gained clock lands in the unaccounted
// remainder where Corrected then checks that it stayed.
func (l *level) correct(said []Attribution, presence []span,
	target func(Attribution) string) map[string][]span {

	to := make(map[string][]span, 4)
	var every []span
	for _, a := range said {
		in := intersect([]span{{a.Start, a.End}}, presence)
		if len(in) == 0 {
			continue
		}
		every = append(every, in...)
		if name := target(a); name != "" {
			to[name] = append(to[name], in...)
		}
	}
	every = union(every)

	// A name nobody in the corpus mentions is a row this report did not have.
	// Creating it is the honest answer: it carries zero observations and every
	// hour in it is a person's word, both of which the row says out loud.
	// Refusing to create it would discard the correction in silence.
	for name := range to {
		if _, known := l.active[name]; !known {
			l.names = append(l.names, name)
			l.active[name] = nil
		}
	}
	slices.Sort(l.names)

	gained := make(map[string][]span, len(to))
	for _, name := range l.names {
		mine := union(to[name])
		before := l.active[name]
		after := subtract(union(append(slices.Clone(before), mine...)), subtract(every, mine))
		if len(mine) > 0 {
			gained[name] = subtract(after, before)
		}
		l.active[name] = after
		l.evidenced[name] = intersect(l.evidenced[name], after)
		l.attested[name] = intersect(l.attested[name], after)
		l.weak[name] = intersect(l.weak[name], after)
	}
	return gained
}

// assertedToNobody is the clock somebody said belonged to no project at all,
// clipped to presence. It is what gives that stretch its own reason instead of
// letting it fall back on one of the machine's three.
func assertedToNobody(said []Attribution, presence []span) []span {
	var out []span
	for _, a := range said {
		if a.Project != "" {
			continue
		}
		out = append(out, intersect([]span{{a.Start, a.End}}, presence)...)
	}
	return union(out)
}

// ownersOf inverts the client-to-projects mapping the report has already built
// from its own events, and names the projects the inversion cannot settle.
//
// A project two clients claim is CONTESTED and is deliberately left without an
// owner rather than resolved. There is no later-is-truer ordering between two
// directories that disagree — internal/clientmap makes the same choice, and
// `metrics` prints the collision instead of picking — so an assertion naming
// such a project moves its hours to the project and out of every client, into
// the clientless figure that exists to say exactly that.
func ownersOf(clientOwns map[string]map[string]bool) (owner map[string]string, contested map[string]bool) {
	owner = make(map[string]string, len(clientOwns))
	contested = map[string]bool{}
	for client, projects := range clientOwns {
		for project := range projects {
			if held, seen := owner[project]; seen && held != client {
				contested[project] = true
				continue
			}
			owner[project] = client
		}
	}
	return owner, contested
}
