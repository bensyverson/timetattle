// Package dayview reconstructs one working day as something a person can be
// asked to confirm, and turns their answer into assertions.
//
// It holds no I/O and no rendering. cmd/timetattle's `review` draws what this
// assembles and prompts for the answer; internal/store records what this plans.
// Both halves are here because both are arguable, and an argument that can only
// be had against a terminal is one nobody has.
//
// # Why a whole day, and why one row per project
//
// The calibration loop this serves (task PkY6I) fits three time-boundary
// parameters — the idle gap, the lead-in and the tail-out — against what a
// person says really happened. Getting those labels is the bottleneck, so the
// fast path has to be one keystroke: an entire labelled day on one screen, and
// a reviewer who says yes.
//
// The layout is one ROW PER ACTIVE PROJECT against a shared time axis, which is
// one row on most days and several on a concurrent one. Measured on the live
// store on 2026-08-01: 589 days carry attributed evidence, 74.4% of them have
// exactly one project, the mean is 1.67 and the maximum is 9. Concurrency is
// real and rare, so it has to be representable without taxing the three
// quarters of days that have none. Rows also unify the two ways of drilling
// into a wrong day: the rows are the clients and the columns are the time, so
// client-by-client and block-by-block are two readings of one picture.
//
// # A project's row is its OWN clustering, intersected with the day's sittings
//
// This is internal/metrics' model B and it is here for the reason that package
// measured rather than argued: crediting a project the WHOLE of any sitting its
// evidence appears in summed to 24.01h across a 12.78h day. A row therefore
// shows where a project's own evidence clusters, inside the skeleton the global
// sittings lay down.
//
// On real evidence that produces slivers rather than bars, and the slivers are
// the finding. Every attributable source in this corpus emits INSTANTS, so two
// clients alternating inside one sitting come out as an interleaving rather
// than as an overlap — which is what alternating actually looks like, and is
// more informative than two solid bars would be.
//
// # Confirming a day is a CLOSED accounting of it
//
// This is the property everything downstream rests on. A confirmation does not
// only say where work WAS; it says, for every instant of the day, whether it
// was work. Blocks become 'engaged' assertions and every gap between them
// becomes 'not-engaged'.
//
// Without the second half a confirmation would be a lower bound, every
// parameter that lengthens a sitting would fit at least as well as every
// parameter that shortens one, and the gap threshold — the parameter with 2.3x
// of leverage over the headline figure — could not be fitted at all. With it, a
// single confirmed day pins roughly 2n boundaries exactly. That is what makes
// "confirm the whole day" a dense signal rather than a vague one, and it is why
// the wholesale path was worth choosing over triaging individual boundaries.
//
// The one thing that punches a hole in the closure is an unremembered block.
// Filling that hole with 'not-engaged' would turn "I cannot remember" into "I
// was not working", which is this project's cardinal bug wearing a new hat, so
// the hole is left open and a review row explains it.
//
// # Wholesale and drilled produce the SAME labels
//
// A day walked block by block and a day waved through in one keystroke emit
// identical assertions when the reviewer agreed with everything. That is
// deliberate, and it dissolves most of the sampling-bias worry that hung over
// this design: the fine-grained calibration labels are uniform over reviewed
// days rather than existing only for rejected ones.
//
// What still differs is how hard the reviewer looked, and that is recorded
// without a schema change and without parsing free text: a drilled day writes
// one review row PER BLOCK beside the day's own, because the reviewer really
// did examine each one. A day carrying only a day-spanning review was waved
// through. PkY6I can weight on that, and every row it reads is literally true.
//
// # A wholesale confirmation says nothing about attribution
//
// Only engagement. A quarter of days carry two or more projects, so a single
// 'attributed' assertion across a block would be false on them; and the
// parameters being fitted are time-boundary parameters, which attribution is
// not one of. Attribution is asserted only where a reviewer explicitly corrects
// it.
package dayview

import (
	"fmt"
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// Span is the shared half-open interval. This package adds no interval algebra
// of its own; internal/timespan is the one implementation.
type Span = timespan.Span

// State is what is known about a day, in FOUR answers rather than three.
//
// Unexamined and Unremembered are the pair this project has now collapsed three
// times in three separate tools. "I never looked" and "I looked and cannot
// remember" are different facts: the second says the machine's answer is
// unconfirmed, which is different from wrong and different from unexamined.
type State string

const (
	// Unexamined is no review row at all. Nobody has opened this day.
	Unexamined State = "unexamined"
	// Agreed is reviewed, recalled, and the corrections reproduce the
	// machine's answer exactly.
	Agreed State = "agreed"
	// Corrected is reviewed, recalled, and the corrections change it.
	Corrected State = "corrected"
	// Unremembered is reviewed and not recalled. The machine's answer stands,
	// marked unconfirmed, and nothing is asserted either way.
	Unremembered State = "unremembered"
)

// States are every state, in the order a summary should list them.
func States() []State { return []State{Unexamined, Agreed, Corrected, Unremembered} }

// Mode is how closely a day was looked at.
//
// It is not stored as a column. Drilled writes a review row per block, which
// is a true statement about what was examined and is what tells the two apart
// on the way back out.
type Mode string

const (
	// Wholesale is the fast path: the day confirmed as a whole, one keystroke.
	Wholesale Mode = "wholesale"
	// Drilled is the day walked block by block.
	Drilled Mode = "drilled"
)

// Block is one derived sitting as it falls inside one working day.
type Block struct {
	// Span is the part of the sitting inside the day, and Whole is the
	// sitting's own extent. They differ only across a day boundary.
	Span, Whole Span
	// Clipped says the sitting continues outside this day, so a reviewer can
	// tell a block that ended from one that was cut.
	Clipped bool
	// Events is how many observations the whole sitting rests on, and Sources
	// names where they came from. Neither is apportioned across a clip.
	Events  int
	Sources []string
	// Margin is the whole sitting's inferred part: lead-in, tail-out and every
	// bridged silence. Which side of a boundary any of it fell on is not
	// recoverable from the row, so it is reported whole and Day.Margin leaves
	// a clipped block's margin out rather than apportioning it pro rata —
	// the same refusal internal/sessions' calendar.go makes.
	Margin time.Duration
	// Projects are the projects whose evidence falls inside this block,
	// sorted. Empty means the block rests on evidence no project owns.
	Projects []string
}

// Duration is how much of the day this block covers.
func (b Block) Duration() time.Duration { return b.Span.Duration() }

// Strength is what a row's evidence is worth as a claim that somebody worked
// on that project that day, in THREE answers rather than two.
//
// It is not a filter and it never removes a row. After task 4LQHd excluded the
// machine-generated file activity, 88 of the 453 surviving project-days rest on
// nothing but file modification times, and every one of them may be a real
// evening spent editing one document — RapidPro's MBCommon.h, a cypress design
// PDF, the Woodcase note Ben judged by hand and kept. What was missing was not
// a rule but a MARK: those days and a day with a transcript underneath it
// printed identically.
type Strength string

const (
	// Unknown is nothing established. It is the zero value and it is what a
	// day assembled WITHOUT its evidence can say about any project — see
	// Skeleton, and Day.Evidence.
	Unknown Strength = ""
	// Weak is a row every one of whose observations is weak evidence on its
	// own: a file changed, and nothing recorded anybody changing it.
	Weak Strength = "weak"
	// Attested is a row at least one of whose observations records something
	// a person did. One is enough — a day of file saves with a single commit
	// in it is a day somebody was demonstrably there.
	Attested Strength = "attested"
)

// Row is one project's activity across the day: the shape a reviewer reads
// left to right.
type Row struct {
	// Project is the project as the registry resolved it. The empty name is
	// evidence belonging to NO registered project, which is a row rather than
	// a gap: a day whose evidence is all unattributed would otherwise render
	// as sittings nobody worked.
	Project string
	// Events is how many of this project's observations fall inside the day.
	Events int
	// Active is where this project's own evidence clusters, intersected with
	// the day's blocks. See the package doc for why it is not the whole of
	// every block the project appears in.
	Active []Span
	// Observed is the raw extent of this project's observations, unexpanded by
	// any lead-in or tail-out. Points are zero-length spans and stay that way:
	// this is what was seen, against Active's what was inferred.
	Observed []Span
	// Strength is what this row's evidence is worth, weighed over the
	// observations INSIDE the day. It is never Unknown on a row: a row exists
	// only because an observation does, so there is always something to weigh.
	Strength Strength
}

// Day is one working day, assembled.
type Day struct {
	// Day names the working day, and Start and End are the instants it covers.
	// They are 23, 24 or 25 hours apart across a daylight-saving transition.
	Day        sessions.Day
	Start, End time.Time

	// Params is the derivation the blocks came out of and ComputedAt is when
	// it ran. A zero ComputedAt is the third state: nothing has been derived
	// at all, which is NOT the same as a day the model found nothing in.
	Params     sittings.Params
	ComputedAt time.Time

	// Blocks are the day's sittings in time order, and Rows the projects
	// active in them, sorted by name with the unattributed row last.
	Blocks []Block
	Rows   []Row

	// Evidence says the day's observations were READ. False means Rows is
	// empty for want of reading rather than for want of evidence, and the two
	// are different facts: the review queue assembles days without their
	// evidence on purpose, because a listing costs a query per day. Reading
	// the first as the second is this project's cardinal bug.
	Evidence bool

	// Observed is where ANY evidence was seen in the day, whoever it belonged
	// to, unexpanded by any lead-in or tail-out. It is what a summary row of
	// the timeline draws, and it is the difference between the clock something
	// was recorded across and the clock the clustering inferred around it.
	Observed []Span

	// Observations are the raw events the day was assembled FROM, exactly as
	// the caller handed them over — margin either side included, nothing
	// reduced. Nil on a Skeleton, which is what Evidence already says.
	//
	// The rows above are a reduction BY PROJECT, and on 2026-07-17 — the day
	// whose review was abandoned — 573 of the day's 576 observations named no
	// project whatever, so every row read "(no project)" and told the reviewer
	// nothing. The application identifier, the document and the path are what
	// he actually recognises, and they survive only here. See Nearby.
	Observations []source.Event

	// Reach is how far the store has looked at this day: when the clustering
	// last ran, and when each source was last asked. A zero Reach is a caller
	// that did not read it, which is a third state and not a fresh one.
	//
	// It is a field rather than an argument because it must travel with the day
	// from the screen it was drawn on to the answer that is recorded about it.
	// Drawing a day as fully looked-at and then clipping the assertions would
	// be worse than either: the reviewer would answer one question and have a
	// different one recorded.
	Reach Reach

	// Reviews and Corrections are what has already been recorded over this
	// day, unfolded, in the order the store returned them.
	Reviews     []adjustments.Review
	Corrections []adjustments.Adjustment
}

// Horizon is the last instant of this day anybody may assert about, and which
// of the three things decided it. See reach.go.
//
// It never reaches past the day's own end: a horizon in tomorrow says the whole
// of this day has been looked at, which is the case for every day but today.
func (d Day) Horizon(now time.Time) (time.Time, string) {
	at, bound := d.Reach.Bounds(now)
	if !at.Before(d.End) {
		// Every hour of this day has been looked at, which is the case for
		// every day but today. The bound is the day's own end and naming the
		// harvest here would read as a warning about a day nothing is wrong
		// with.
		return d.End, "day"
	}
	return at, bound
}

// Unlooked is the part of the day past the horizon: the stretch where "nothing
// was recorded" means "nothing has been asked yet".
//
// It is what draws the FOURTH cell state. Three symbols cannot say this: an
// unharvested hour and an hour every source reported quiet render identically,
// and the reviewer then agrees that the first was empty. That is what happened
// on 2026-08-01 and it cost 1.4379h of false assertion.
func (d Day) Unlooked(now time.Time) []Span {
	at, _ := d.Horizon(now)
	if !at.Before(d.End) {
		return nil
	}
	if at.Before(d.Start) {
		at = d.Start
	}
	return []Span{{Start: at, End: d.End}}
}

// Derived reports whether the clustering has ever run. A day with no blocks and
// no derivation is a database nobody has sessionized; a day with no blocks and
// a derivation is a day the model found nothing in. Reading those as the same
// thing reports work as absent because nobody looked.
func (d Day) Derived() bool { return !d.ComputedAt.IsZero() }

// Length is how long the working day is. Not 24 hours across a transition.
func (d Day) Length() time.Duration { return d.End.Sub(d.Start) }

// Engaged is how much of the day the blocks cover.
func (d Day) Engaged() time.Duration {
	var total time.Duration
	for _, b := range d.Blocks {
		total += b.Duration()
	}
	return total
}

// Margin is how much of Engaged no observation covered, summed over the blocks
// whose margin belongs wholly to this day. A clipped block is left out and
// counted by ClippedBlocks instead, because its margin cannot be apportioned
// across the boundary from anything the row records.
func (d Day) Margin() time.Duration {
	var total time.Duration
	for _, b := range d.Blocks {
		if !b.Clipped {
			total += b.Margin
		}
	}
	return total
}

// ClippedBlocks is how many of the day's blocks continue outside it, and
// therefore how many margins Margin could not count.
func (d Day) ClippedBlocks() int {
	var n int
	for _, b := range d.Blocks {
		if b.Clipped {
			n++
		}
	}
	return n
}

// Spans is the day's blocks as bare intervals: the machine's answer, ready for
// the overlay to be applied over.
func (d Day) Spans() []Span {
	out := make([]Span, 0, len(d.Blocks))
	for _, b := range d.Blocks {
		out = append(out, b.Span)
	}
	return out
}

// State is which of the four things is known about this day.
//
// The review that speaks for a day is the latest one COVERING it. A drilled day
// writes a review row per block, and one of those must never decide the day's
// verdict; preferring the covering reviews is what guarantees that, rather than
// the order rows happen to be inserted in.
func (d Day) State() State {
	latest, found := d.speaker()
	if !found {
		return Unexamined
	}
	if latest.Verdict == adjustments.Unremembered {
		return Unremembered
	}
	if d.changed() {
		return Corrected
	}
	return Agreed
}

// Reviewed reports whether anybody has looked at this day at all, which is the
// question the review queue asks and is not the same as any verdict.
func (d Day) Reviewed() bool {
	_, found := d.speaker()
	return found
}

// ReviewedInFull reports whether the whole of this day has been examined, which
// is not the same question as Reviewed.
//
// A day confirmed while it was still happening is examined only as far as the
// moment of review; the hours after it carry no row at all, which is the fourth
// state. The review queue asks THIS question rather than Reviewed, because
// otherwise confirming today would take it off the queue for ever and its
// evening would never be looked at.
// It is deliberately NOT expressed as a verdict. Coverage and verdict are
// orthogonal — a partly examined day can equally have been agreed with or
// corrected — and folding "partial" into State would hide which.
func (d Day) ReviewedInFull() bool {
	for _, r := range d.Reviews {
		if !r.Start.After(d.Start) && !r.End.Before(d.End) {
			return true
		}
	}
	return false
}

// speaker is the review that pronounces on the day: the latest one covering it,
// or failing that the latest one touching it.
func (d Day) speaker() (adjustments.Review, bool) {
	var covering, touching []adjustments.Review
	for _, r := range d.Reviews {
		switch {
		case !r.Start.After(d.Start) && !r.End.Before(d.End):
			covering = append(covering, r)
		case r.End.After(d.Start) && r.Start.Before(d.End):
			touching = append(touching, r)
		}
	}
	if len(covering) == 0 {
		covering = touching
	}
	if len(covering) == 0 {
		return adjustments.Review{}, false
	}
	slices.SortStableFunc(covering, func(a, b adjustments.Review) int {
		if c := a.RecordedAt.Compare(b.RecordedAt); c != 0 {
			return c
		}
		return int(a.ID - b.ID)
	})
	return covering[len(covering)-1], true
}

// changed reports whether the corrections move the machine's answer for this
// day. It is how "recalled with corrections" and "recalled without" are told
// apart, and it is computed rather than stored: a flag would be a second
// spelling of a fact the rows already carry.
func (d Day) changed() bool {
	machine := d.Spans()
	corrected := adjustments.Fold(d.Reviews, d.Corrections).Engaged(machine)
	if len(corrected) != len(machine) {
		return true
	}
	for i := range machine {
		if !corrected[i].Start.Equal(machine[i].Start) || !corrected[i].End.Equal(machine[i].End) {
			return true
		}
	}
	return false
}

// Judgement is what a reviewer said about ONE block.
type Judgement struct {
	// Verdict is Recalled or Unremembered. An unremembered block asserts
	// nothing at all and leaves a hole in the day's accounting.
	Verdict adjustments.Verdict
	// Engaged says the block was work. False is "this was not work at all",
	// which removes the block.
	Engaged bool
	// Span is the corrected extent, which defaults to the block's own. It must
	// lie inside the working day: a correction reaching past the boundary
	// belongs to the day on the other side of it, and clipping it silently
	// would break the closure the day's accounting rests on.
	Span Span
	// Reattribute says the reviewer corrected who the block belonged to.
	// Project names them, and an empty Project with Reattribute set asserts
	// the block belonged to NOBODY — which is a thing a person can say, and is
	// not the same as saying nothing.
	Reattribute bool
	Project     string
}

// Answer is what a reviewer said about a day, whole.
//
// It is a struct rather than a parameter list because the interesting field is
// the one that is easiest to forget: Added is work the model MISSED, and
// without it a review can only ever agree that the machine over-claimed. A day
// the clustering found nothing in, that the reviewer spent working, is the
// sharpest signal there is about the lead-in and about evidence coverage, and
// it would be unrecordable through the blocks because there are none.
type Answer struct {
	// Mode is how closely the day was looked at.
	Mode Mode
	// Blocks is one judgement per derived block, in the blocks' own order, or
	// nil for the fast path that accepts every one of them.
	Blocks []Judgement
	// Added is work no block covers. Each span must lie inside the day.
	Added []Span
	// Origin is who or what is answering, At is when, and Note is why in their
	// own words. All three travel onto every row the answer writes.
	Origin string
	At     time.Time
	Note   string
}

// Plan is everything recording a verdict would write, and nothing else.
//
// It is a value rather than a call so that what a keystroke is about to do can
// be printed, tested and argued with before any of it reaches the store.
type Plan struct {
	// Review is the day's own review, and is recorded LAST so that the latest
	// review covering a day is always the one that pronounces on the day.
	Review adjustments.Review
	// BlockReviews are the per-block acts of looking, empty unless the day was
	// drilled. They are recorded BEFORE Review.
	BlockReviews []adjustments.Review
	// Assertions are the corrections, with ReviewID unset. Parent stamps them
	// once the day's review has an id.
	Assertions []adjustments.Adjustment
}

// Parent hangs every assertion off the review that produced it.
func (p *Plan) Parent(reviewID int64) {
	for i := range p.Assertions {
		p.Assertions[i].ReviewID = reviewID
	}
}

// Confirm turns an answer about a day into the assertions it implies.
//
// The result is a CLOSED accounting: every instant of the day is asserted to
// have been work or not to have been, except where a block could not be
// remembered. See the package doc for why the second half is what makes this
// worth fitting against.
func (d Day) Confirm(answer Answer) (Plan, error) {
	judgements := answer.Blocks
	switch {
	case answer.Mode != Wholesale && answer.Mode != Drilled:
		return Plan{}, fmt.Errorf("%q is not a way of confirming a day; write %s or %s",
			answer.Mode, Wholesale, Drilled)
	case answer.Mode == Wholesale && judgements != nil:
		return Plan{}, fmt.Errorf("a wholesale confirmation accepts the day as derived, so it takes no " +
			"per-block judgements; drill down to correct one")
	case judgements != nil && len(judgements) != len(d.Blocks):
		return Plan{}, fmt.Errorf("%d judgements against %d blocks; a drill-down has to account for "+
			"every block or the day is not a complete accounting", len(judgements), len(d.Blocks))
	}

	if judgements == nil {
		judgements = make([]Judgement, 0, len(d.Blocks))
		for _, b := range d.Blocks {
			judgements = append(judgements, Judgement{Verdict: adjustments.Recalled, Engaged: true, Span: b.Span})
		}
	}
	origin, at, note := answer.Origin, answer.At, answer.Note
	if !at.After(d.Start) {
		return Plan{}, fmt.Errorf("this confirmation is timed %s, and the working day does not "+
			"begin until %s; a day cannot be reviewed before it has started",
			clock(at), clock(d.Start))
	}

	// THE MOMENT OF REVIEW IS NOT THE ONLY THING THAT BOUNDS WHAT MAY BE
	// ASSERTED, and treating it as though it were cost 1.4379h of false
	// not-engaged on 2026-08-01. `now` below becomes the EARLIEST of the three
	// horizons — the review, the clustering, the harvest — so that every check
	// that follows is bounded by one instant and they cannot drift apart:
	// the future-work refusal, the block skip, the closure, and the extent the
	// review claims to have examined.
	//
	// RecordedAt stays answer.At throughout. When the recording happened is a
	// fact about the act and not about what was visible during it, and moving
	// it to the horizon would put the fold's ordering key hours in the past.
	now, bound := d.Horizon(at)
	if !now.After(d.Start) {
		return Plan{}, fmt.Errorf("nothing has looked at this day past %s (the %s), and it "+
			"begins at %s; there is nothing here anybody could confirm yet",
			clock(now), bound, clock(d.Start))
	}

	var (
		plan   Plan
		worked []Span
		holes  []Span
	)
	for _, s := range answer.Added {
		if s.Start.Before(d.Start) || s.End.After(d.End) {
			return Plan{}, fmt.Errorf("%s..%s was added as work, and it reaches outside the working "+
				"day %s..%s; record that part against the day it falls in",
				clock(s.Start), clock(s.End), clock(d.Start), clock(d.End))
		}
		// A person may report work the model missed; they may not report work
		// that has not happened. Refused rather than clipped, for the same
		// reason as the boundary above: clipping would silently record a
		// different assertion from the one that was made.
		if s.End.After(now) {
			return Plan{}, fmt.Errorf("%s..%s was added as work, and it reaches past %s, when this "+
				"review is being recorded; that stretch has not happened yet",
				clock(s.Start), clock(s.End), clock(now))
		}
		if !s.End.After(s.Start) {
			return Plan{}, fmt.Errorf("%s..%s was added as work and covers no time at all",
				clock(s.Start), clock(s.End))
		}
		worked = append(worked, s)
		plan.Assertions = append(plan.Assertions, d.assert(adjustments.Engaged, s, "", origin, at, note))
	}
	for i, j := range judgements {
		span := j.Span
		if span.Start.IsZero() && span.End.IsZero() {
			span = d.Blocks[i].Span
		}
		if span.Start.Before(d.Start) || span.End.After(d.End) {
			return Plan{}, fmt.Errorf("block %d was corrected to %s..%s, which reaches outside the working "+
				"day %s..%s; record that part against the day it falls in",
				i+1, clock(span.Start), clock(span.End), clock(d.Start), clock(d.End))
		}
		if !span.End.After(span.Start) {
			return Plan{}, fmt.Errorf("block %d was corrected to %s..%s, which covers no time at all",
				i+1, clock(span.Start), clock(span.End))
		}
		// A block lying WHOLLY after the moment of review is not examined and
		// is asserted nothing about — not engaged, not unremembered, and not a
		// hole. It falls into the unexamined remainder with the rest of the
		// evening, and the day comes back round the queue.
		//
		// In ordinary use there are none: a derivation is built from observed
		// events, so no block can begin in the future. That is exactly why this
		// is written down rather than relied on — it is a property of today's
		// data, not of the code, and the first version of this closure took the
		// examined extent from the LAST block of the day and so asserted over
		// every gap in an evening it had not seen. Caught by the real-day test
		// beside the constructed ones, which is what that pairing is for.
		if !span.Start.Before(now) {
			continue
		}

		switch j.Verdict {
		case adjustments.Unremembered:
			// Asserted neither way. The hole is what keeps "I cannot
			// remember" from being recorded as "I was not working".
			holes = append(holes, d.Blocks[i].Span)
		case adjustments.Recalled:
			if j.Engaged {
				worked = append(worked, span)
				plan.Assertions = append(plan.Assertions,
					d.assert(adjustments.Engaged, span, "", origin, at, note))
			}
			if j.Reattribute {
				assertion := adjustments.Attributed
				if j.Project == "" {
					assertion = adjustments.Unattributed
				}
				plan.Assertions = append(plan.Assertions,
					d.assert(assertion, span, j.Project, origin, at, note))
			}
		default:
			return Plan{}, fmt.Errorf("block %d carries the verdict %q; write %s or %s",
				i+1, j.Verdict, adjustments.Recalled, adjustments.Unremembered)
		}

		if answer.Mode == Drilled {
			plan.BlockReviews = append(plan.BlockReviews, adjustments.Review{
				Start: d.Blocks[i].Span.Start, End: d.Blocks[i].Span.End,
				Verdict: j.Verdict, Origin: origin, RecordedAt: at,
			})
		}
	}

	// HOW MUCH OF THE DAY WAS ACTUALLY LOOKED AT.
	//
	// Reviewing today is the ordinary case — recall is best on the day itself,
	// and the queue offers the most recent days first — so most of a reviewed
	// day is routinely still to come. The closure below would assert
	// not-engaged over all of it.
	//
	// It is not hypothetical. On the live store exactly one review row and one
	// adjustment reach past their own recorded_at, both on 2026-08-01, both by
	// 15h36m, and the resulting assertion contradicts 2.194h of sitting that
	// happened afterwards. The tables are append-only, so it is permanent.
	//
	// The unexamined remainder therefore gets NO ROW AT ALL, which is what
	// "nobody has looked yet" already means everywhere else here. Clipping
	// silently was the alternative and was rejected: it records a partial day
	// that a later reader cannot tell from a whole one, which is this project's
	// cardinal bug — collapsing "observed absent" into "nothing recorded".
	//
	// A BLOCK ALREADY UNDER WAY MAY REACH PAST THE MOMENT OF REVIEW, and that
	// is not the same defect. A sitting runs to its last observation PLUS the
	// tail-out, so a block drawn from an event at 12:59 ends at 13:04 under the
	// shipped 5m and a reviewer confirming at 13:00 is endorsing it. That
	// endorses an inference about work already observed; it does not assert
	// anything about unobserved hours, and refusing it would make same-day
	// review impossible on any day whose work reaches the present moment. So
	// the examined extent reaches to cover whatever was asserted, and the day's
	// own review says so rather than ending before the last thing it endorsed.
	//
	// Only spans that BEGAN before the moment of review can stretch it, which
	// the loop above has already guaranteed for blocks and the Added check for
	// hand-entered work. Blocks are disjoint, so at most one is under way.
	examined := now
	for _, s := range worked {
		if s.End.After(examined) {
			examined = s.End
		}
	}
	if examined.After(d.End) {
		examined = d.End
	}

	// The closure. Everything the reviewer did not claim as work, and did not
	// decline to remember, is asserted not to have been work — over the part of
	// the day they could see, and no further.
	rest := timespan.Subtract([]Span{{Start: d.Start, End: examined}}, timespan.Union(append(worked, holes...)))
	for _, s := range rest {
		if !s.End.After(s.Start) {
			continue
		}
		plan.Assertions = append(plan.Assertions, d.assert(adjustments.NotEngaged, s, "", origin, at, note))
	}

	plan.Review = adjustments.Review{
		Start: d.Start, End: examined, Verdict: adjustments.Recalled,
		Shown:  adjustments.Derivation,
		Origin: origin, RecordedAt: at, Note: note,
	}
	return plan, nil
}

// CannotRemember records that somebody looked at the day and could not recall
// it. It asserts nothing: the machine's answer stands, marked unconfirmed.
//
// This is the third state made recordable, and it is why reviews are a table of
// their own. A day confirmed with no corrections writes assertions; a day
// nobody could remember writes none, and without the review row it would be
// indistinguishable from a day nobody has opened.
// The span is clipped at the moment of review for the reason Confirm gives at
// length. It asserts nothing either way, so nothing here can contradict a
// later observation — but a review row is the record of what was LOOKED AT, and
// claiming to have looked at hours that have not happened is false whatever
// verdict follows it.
func (d Day) CannotRemember(answer Answer) Plan {
	end, _ := d.Horizon(answer.At)
	return Plan{Review: adjustments.Review{
		Start: d.Start, End: end, Verdict: adjustments.Unremembered,
		Shown:  adjustments.Derivation,
		Origin: answer.Origin, RecordedAt: answer.At, Note: answer.Note,
	}}
}

func (d Day) assert(a adjustments.Assertion, s Span, project, origin string,
	now time.Time, note string) adjustments.Adjustment {
	return adjustments.Adjustment{
		Start: s.Start, End: s.End, Assertion: a, Project: project,
		Origin: origin, RecordedAt: now, Note: note,
	}
}

// Assemble reconstructs one working day from the machine's answer, the
// evidence underneath it, and whatever a person has already said about it.
//
// events may cover more than the day and should: a project's own clustering is
// truncated at the edge of whatever window it is handed, so a caller reads a
// margin either side and lets this clip. reviews and adjustments are filtered
// to the ones touching the day.
func Assemble(cal sessions.Calendar, day sessions.Day, derived sittings.Derived,
	events []source.Event, reviews []adjustments.Review, adjs []adjustments.Adjustment) (Day, error) {
	return assemble(cal, day, derived, events, reviews, adjs, true)
}

// Skeleton reconstructs a day WITHOUT reading its evidence: the blocks, and
// whatever a person has already said about it.
//
// It exists so that "nobody read the observations" is a thing a Day can say.
// The review queue assembles every day in a window to report what is known
// about each, and reading a day's evidence costs a query per day; the days it
// draws are loaded as it reaches them. A skeleton has no rows, and a reader
// that took that for "no source that names a project saw any of this day" would
// be reporting absence as observation.
func Skeleton(cal sessions.Calendar, day sessions.Day, derived sittings.Derived,
	reviews []adjustments.Review, adjs []adjustments.Adjustment) (Day, error) {
	return assemble(cal, day, derived, nil, reviews, adjs, false)
}

func assemble(cal sessions.Calendar, day sessions.Day, derived sittings.Derived,
	events []source.Event, reviews []adjustments.Review, adjs []adjustments.Adjustment,
	read bool) (Day, error) {
	if err := cal.Validate(); err != nil {
		return Day{}, err
	}
	start, end := cal.Bounds(day)
	window := Span{Start: start, End: end}
	out := Day{
		Day: day, Start: start, End: end,
		Params: derived.Params, ComputedAt: derived.ComputedAt,
		Evidence: read, Observations: events,
	}

	for _, r := range reviews {
		if r.End.After(start) && r.Start.Before(end) {
			out.Reviews = append(out.Reviews, r)
		}
	}
	for _, a := range adjs {
		if a.End.After(start) && a.Start.Before(end) || (a.Start.Equal(a.End) && timespan.Covers([]Span{window}, a.Start)) {
			out.Corrections = append(out.Corrections, a)
		}
	}

	for _, s := range derived.Sittings {
		clipped, inside := timespan.Clip(s.Span(), window)
		if !inside || !clipped.End.After(clipped.Start) {
			continue
		}
		out.Blocks = append(out.Blocks, Block{
			Span: clipped, Whole: s.Span(),
			Clipped: !clipped.Start.Equal(s.Start) || !clipped.End.Equal(s.End),
			Events:  s.Events, Sources: slices.Clone(s.Sources), Margin: s.Margin,
		})
	}
	slices.SortStableFunc(out.Blocks, func(a, b Block) int { return a.Span.Start.Compare(b.Span.Start) })

	// A derivation that never ran carries a zero Params, which no clustering
	// will accept. There are no blocks to intersect against either, so there
	// is nothing a row could say.
	//
	// The test is on the PARAMETERS and not on Derived(), and the difference is
	// the blind pass. Observations are not the model's and do not wait on it
	// having run: a caller holding usable parameters and no derivation still
	// wants Observed and the rows, because that is the whole of what a review
	// from the evidence has to draw. Row.Active comes out empty there — it is
	// intersected against blocks and there are none — which is the correct
	// answer rather than a missing one.
	if out.Params.Validate() != nil {
		return out, nil
	}
	if err := out.rows(events, window); err != nil {
		return Day{}, err
	}
	return out, nil
}

// rows groups the day's evidence by project and works out where each one was
// active. See the package doc for why this is a re-clustering rather than an
// intersection against the blocks.
func (d *Day) rows(events []source.Event, window Span) error {
	names, byProject, bounds := weighAll(events, d.Params, window)

	blocks := d.Spans()
	var everything []Span
	for _, name := range names {
		w := byProject[name]
		// A project with evidence only outside the day appears because the
		// caller reads a margin either side. It is not a row of this day.
		if w.inside == 0 {
			continue
		}
		row := Row{Project: name, Events: w.inside, Strength: w.strength}
		raw := make([]Span, 0, len(w.events))
		for _, e := range w.events {
			if s := extent(e); !s.Start.IsZero() {
				raw = append(raw, s)
			}
		}
		row.Observed = reachInto(raw, window)
		everything = append(everything, row.Observed...)

		// The presence readings travel with the project's own evidence so that
		// its sittings are split and trimmed by observed darkness exactly as
		// the global ones were, which is what internal/metrics does and for
		// the same reason.
		sub := make([]source.Event, 0, len(w.events)+len(bounds))
		sub = append(sub, w.events...)
		sub = append(sub, bounds...)
		res, err := sessions.Cluster(sub, d.Params)
		if err != nil {
			return fmt.Errorf("cluster %s's own evidence: %w", rowName(name), err)
		}
		own := make([]Span, 0, len(res.Sessions))
		for _, s := range res.Sessions {
			own = append(own, s.Span())
		}
		row.Active = timespan.Intersect(own, blocks)
		d.Rows = append(d.Rows, row)

		for i := range d.Blocks {
			for _, s := range raw {
				if timespan.Overlaps(s, d.Blocks[i].Span) {
					d.Blocks[i].Projects = append(d.Blocks[i].Projects, name)
					break
				}
			}
		}
	}
	for i := range d.Blocks {
		slices.Sort(d.Blocks[i].Projects)
	}
	d.Observed = reachInto(everything, window)
	return nil
}

// Weigh reports what each project's evidence inside a working day is worth,
// WITHOUT clustering any of it.
//
// It is what the review queue asks. A listing of two hundred days cannot afford
// the per-project re-clustering a row costs, and it does not need it: strength
// is a property of the OBSERVATIONS, not of where the clustering put them. This
// and Assemble read the same rule through the same code, so the queue and the
// day screen cannot drift apart.
//
// A project absent from the result had no evidence inside the day. That is a
// different answer from Unknown, which is what a caller holding no events at
// all has — see Skeleton.
func Weigh(cal sessions.Calendar, day sessions.Day, p sittings.Params,
	events []source.Event) map[string]Strength {
	start, end := cal.Bounds(day)
	names, byProject, _ := weighAll(events, p, Span{Start: start, End: end})
	out := make(map[string]Strength, len(names))
	for _, name := range names {
		if w := byProject[name]; w.inside > 0 {
			out[name] = w.strength
		}
	}
	return out
}

// weighed is one project's evidence as it reaches a working day, before
// anything has been clustered.
type weighed struct {
	// events is every observation of this project the caller handed in,
	// INCLUDING the margin either side of the day, because a project's own
	// clustering is truncated at the edge of whatever window it is given.
	events []source.Event
	// inside is how many of them fall inside the day, and is what decides
	// whether this project is a row of it at all.
	inside int
	// strength is weighed over the ones inside the day and no others. An act
	// recorded at half past five the next working morning says nothing
	// whatever about the evening being judged.
	strength Strength
}

// weighAll groups evidence by project, counts what falls inside the day and
// weighs it, and hands back the presence readings separately.
//
// The names come back sorted with the unattributed row last: the empty name
// would otherwise sort first and put the row nobody owns above every row
// somebody does.
func weighAll(events []source.Event, p sittings.Params, window Span) (
	names []string, byProject map[string]*weighed, bounds []source.Event) {
	presence, ignored := roles(p)
	byProject = map[string]*weighed{}
	for _, e := range events {
		switch {
		case ignored[e.Source]:
			continue
		case presence[e.Source]:
			bounds = append(bounds, e)
			continue
		}
		w := byProject[e.Identity.Project]
		if w == nil {
			w = &weighed{}
			byProject[e.Identity.Project] = w
		}
		w.events = append(w.events, e)

		s := extent(e)
		if s.Start.IsZero() || !timespan.Overlaps(s, window) {
			continue
		}
		w.inside++
		// One recorded act carries the row. Weak is a statement about the
		// WHOLE of a row's evidence, so it can only be settled by finding
		// nothing, which is why it is the state this accumulates away from.
		if w.strength != Attested {
			w.strength = Weak
			if !source.WeakEvidence(e.Source) {
				w.strength = Attested
			}
		}
	}

	names = make([]string, 0, len(byProject))
	for name := range byProject {
		names = append(names, name)
	}
	slices.Sort(names)
	if len(names) > 0 && names[0] == "" {
		names = append(names[1:], "")
	}
	return names, byProject, bounds
}

// reachInto keeps the parts of a run of observations that fall inside a window,
// KEEPING the zero-length ones.
//
// timespan.Intersect cannot be used here and the reason is the whole corpus:
// every attributable source emits instants, an instant is a zero-length span,
// and an intersection of a point with anything has no length to return. Reading
// observations through Intersect would therefore report a day of solid work as
// having been observed nowhere at all.
func reachInto(spans []Span, window Span) []Span {
	var points, extents []Span
	for _, s := range spans {
		if s.Start.Equal(s.End) {
			if !s.Start.Before(window.Start) && s.Start.Before(window.End) {
				points = append(points, s)
			}
			continue
		}
		extents = append(extents, s)
	}
	out := timespan.Intersect(timespan.Union(extents), []Span{window})
	out = append(out, points...)
	slices.SortFunc(out, func(a, b Span) int { return a.Start.Compare(b.Start) })
	return out
}

// roles says which sources bound sittings and which are read and dropped.
// Everything else is evidence of work.
func roles(p sittings.Params) (presence, ignored map[string]bool) {
	presence, ignored = map[string]bool{}, map[string]bool{}
	for _, id := range p.PresenceSources {
		presence[id] = true
	}
	for _, id := range p.IgnoreSources {
		ignored[id] = true
	}
	return presence, ignored
}

// extent is an observation's raw span, unexpanded. An unbounded event observed
// one edge and no duration, so it is treated as the instant it was seen at
// rather than as a span reaching to the zero time decades ago.
func extent(e source.Event) Span {
	switch {
	case e.Start.IsZero():
		return Span{Start: e.End, End: e.End}
	case e.End.IsZero() || e.End.Before(e.Start):
		return Span{Start: e.Start, End: e.Start}
	}
	return Span{Start: e.Start, End: e.End}
}

// rowName is how a project is named in a sentence, saying "no project" out loud
// rather than printing an empty string a reader would take for a formatting
// bug.
func rowName(project string) string {
	if project == "" {
		return "(no project)"
	}
	return project
}

// Name is how this row should be labelled.
func (r Row) Name() string { return rowName(r.Project) }

// clock renders an instant for a complaint, in the zone the day was computed
// in, so a message about a boundary reads the way the day view drew it.
func clock(at time.Time) string { return at.Format("2006-01-02 15:04") }
