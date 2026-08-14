// Package sittings is what a sitting IS, and the parameters one was derived
// under. It is a vocabulary and holds no algorithm.
//
// internal/sessions is how sittings are DERIVED. That package cannot hold these
// types, because it imports internal/store and the store has to be able to
// return them: `Store.Sittings` reading derived_session would otherwise have to
// declare its own Sitting, its own Params and its own Derived, and there would
// be two definitions of the same row diverging from the day they were written.
// This is the same shape internal/adjustments already has — a leaf package the
// store imports for the vocabulary of a table it holds — and it exists for the
// same reason.
//
// # A span separated from its parameters means nothing
//
// Total engaged time over the live corpus ranges from 530.4h at a two-minute
// gap to 1199.9h at two hours, a 2.3x spread measured in
// project/private/2026-07-28-spike-findings.md. "3h12m on Tuesday" is therefore not a
// fact on its own, and a reviewer judging the model's output is judging a
// derivation rather than a number. So Derived carries Params and ComputedAt
// beside the sittings, and every path that reads them keeps them together.
package sittings

import (
	"fmt"
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/timespan"
)

// Params is the whole of the derivation. Two runs with equal Params over equal
// events produce equal output, which is what makes a stored sitting auditable
// rather than merely plausible.
type Params struct {
	// Gap is how long a silence must be to separate one sitting from the next.
	Gap time.Duration
	// LeadIn is how long before its first observation a sitting is assumed to
	// have begun; TailOut how long after its last it is assumed to have run on.
	//
	// Both must be positive for an isolated observation to score anything at
	// all: a single Motion save with no neighbours has no span, and a day of
	// creative work otherwise disappears (task J04gF, criterion kRK).
	LeadIn  time.Duration
	TailOut time.Duration
	// PresenceSources name the sources that bound sittings without creating
	// them. Their events are read for 'on' and 'off' states and are never
	// clustered.
	PresenceSources []string
	// IgnoreSources name sources read from the store and then dropped.
	IgnoreSources []string
}

// Validate reports why a set of parameters cannot be used.
func (p Params) Validate() error {
	if p.Gap <= 0 {
		return fmt.Errorf("the gap is %v; it must be positive, or every observation becomes its own sitting", p.Gap)
	}
	if p.LeadIn < 0 {
		return fmt.Errorf("the lead-in is %v; it cannot be negative", p.LeadIn)
	}
	if p.TailOut < 0 {
		return fmt.Errorf("the tail-out is %v; it cannot be negative", p.TailOut)
	}
	for _, id := range p.PresenceSources {
		if slices.Contains(p.IgnoreSources, id) {
			return fmt.Errorf("source %q is named as presence and as ignored; it cannot both bound sittings and be unread", id)
		}
	}
	return nil
}

// Equal reports whether two derivations are the same one.
//
// Every field counts. A rebuild that changed only the presence sources changes
// the answer as much as one that changed the gap — reading /device/isLocked as
// evidence manufactures a 15h32m sitting out of a night's sleep — so this is
// what a reader uses to refuse a table holding two derivations.
func (p Params) Equal(q Params) bool {
	return p.Gap == q.Gap && p.LeadIn == q.LeadIn && p.TailOut == q.TailOut &&
		slices.Equal(p.PresenceSources, q.PresenceSources) &&
		slices.Equal(p.IgnoreSources, q.IgnoreSources)
}

// Sitting is one sitting: a span of wall clock somebody is inferred to have
// been working across.
type Sitting struct {
	// Start and End include the lead-in and tail-out.
	Start, End time.Time
	// Events is how many evidence observations this sitting rests on.
	Events int
	// Sources names the evidence sources involved, sorted and deduplicated.
	Sources []string
	// Margin is how much of Start..End no observation covered — the lead-in,
	// the tail-out, and every bridged silence. It is the inferred part of an
	// inferred span, kept separate so a report can say which it is quoting.
	Margin time.Duration
}

// Duration is how long the sitting lasted.
func (s Sitting) Duration() time.Duration { return s.End.Sub(s.Start) }

// Span is the interval the sitting covers, for the arithmetic in
// internal/timespan — intersecting a project's sittings with the global ones,
// or a day's with a review window.
func (s Sitting) Span() timespan.Span { return timespan.Span{Start: s.Start, End: s.End} }

// Derived is a run of sittings and the derivation that produced them.
//
// A zero ComputedAt is the third state: nothing has been derived at all. It is
// NOT "no sittings in the window asked about", and a caller that reads the two
// as the same thing reports an empty day for a database nobody has ever
// sessionized. Distinguishing observed, observed-absent and never-recorded is a
// discipline this project has broken three times in three separate tools, each
// time in the direction of throwing real work away.
type Derived struct {
	// Params is what produced these sittings, Corpus is which observations
	// they were produced FROM, and ComputedAt is when. All three travel with
	// them because a sitting separated from any of the three is a span with no
	// meaning: the same events at a two-minute gap and at two hours differ by
	// 2.3x, and the same gap over the corpus with and without the configured
	// exclude list differs by 126.2h.
	Params     Params
	Corpus     Corpus
	ComputedAt time.Time
	Sittings   []Sitting

	// Stats is what the derivation DID, and DarknessPolicy is the rule it was
	// allowed to do it under. Both describe the whole run and not the window
	// asked about, so a windowed read reports the same figures a total one
	// does; a fraction of them for a Tuesday would be a number nobody could
	// reconcile with anything.
	//
	// Both are zero when ComputedAt is, and mean nothing has been derived
	// rather than a derivation that suppressed nothing. Stats is a struct of
	// counts, so its zero value is a legal reading as well as an absent one —
	// which is exactly why ComputedAt and not Stats is what says which.
	Stats          Stats
	DarknessPolicy DarknessPolicy
}

// Total is how much wall clock the sittings cover. Sittings are disjoint by
// construction, so this is a sum rather than a union.
func (d Derived) Total() time.Duration {
	var total time.Duration
	for _, s := range d.Sittings {
		total += s.Duration()
	}
	return total
}
