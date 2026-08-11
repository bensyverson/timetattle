// Package adjustments is the human correction layer.
//
// Timetattle infers when work happened from traces it left. The inference is a
// model, it is wrong in ways no source can see — a warm-up period and a meeting
// were both found by a person looking at a reconstruction and saying "I was not
// working then" — and it will be refitted many times. This package is where the
// person's answer lives.
//
// # Corrections are an OVERLAY, applied over the machine's answer
//
// They are not an input to the clustering, and the difference is the whole
// design. If corrections fed into internal/sessions, the model's own output
// would already contain them, and the residual between what the model said and
// what the person said would shrink toward zero for reasons having nothing to
// do with the model improving. The error signal would be unmeasurable, in a
// direction that looks like success.
//
// So derived_session stays pure machine inference and this stays beside it. The
// difference between the two IS the model error, at any moment, for free — which
// is what task PkY6I fits the clustering parameters against. It also makes
// "recomputing sessions preserves every adjustment" true by construction rather
// than by discipline: sessionize and this package write different tables and
// neither reads the other's.
//
// # Six operations, five assertions
//
// Confirm, trim, extend, split, delete and reattribute are not six kinds of
// record. Each is an assertion about a wall-clock span:
//
//   - Engaged      — this span WAS work. Extend, and confirm.
//   - NotEngaged   — this span was NOT work. Trim, delete, and the honest form
//     of split, where there was a real gap.
//   - Boundary     — this instant separates two sittings. The other form of
//     split, for a seam with no gap; it changes no total.
//   - Attributed   — this span belongs to a named project.
//   - Unattributed — this span belongs to NO project, which is an assertion and
//     not the absence of one.
//
// Confirm and extend are the same assertion. What makes one a confirmation is
// that the model already agreed, which is recoverable by comparing against
// derived_session and is not a fact about the row.
//
// # Reviewed, unremembered, and never looked at
//
// A Review is the act of looking, recorded separately from what was found,
// because a day reviewed and not recalled and a day nobody has opened are
// different facts. Collapsing them is a bug this project has shipped twice in
// three separate tools; internal/sessions treats observed-absent,
// observed-present and nothing-recorded as three states for the same reason.
// Without Review the third state would be unrecordable, since a reviewed day
// that needed no corrections writes no adjustments at all.
//
// Unremembered says the machine's answer is UNCONFIRMED. It never deletes it.
//
// # The fold, and why it destroys nothing
//
// Adjustments are append-only. A correction to a correction is a new row, and
// the log is folded in recorded order: the later assertion wins for the
// instants it covers, and for no others. Asserting that lunch was not work does
// not undo the morning.
//
// The fold is not a supersession rule of the kind `event` needs. That one is an
// inference about which of two observations to believe, and an over-general
// version of it nearly began destroying records here once. This one reads rows
// and writes none.
package adjustments

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/timespan"
)

// Assertion is what an adjustment claims about its span.
//
// The values are the strings stored in the database and accepted by the CLI.
// One spelling, so that a value read out of a column can be compared with a
// constant without a translation table nobody would think to keep current.
type Assertion string

const (
	// Engaged asserts that work was happening across the span. It is both
	// "extend" and "confirm".
	Engaged Assertion = "engaged"
	// NotEngaged asserts that work was not happening across the span. It is
	// "trim", "delete", and the form of "split" that removes a real gap.
	NotEngaged Assertion = "not-engaged"
	// Boundary asserts that one sitting ended and another began at an instant,
	// with no gap between them. Start and End are equal.
	Boundary Assertion = "boundary"
	// Attributed asserts that the span belongs to Project.
	Attributed Assertion = "attributed"
	// Unattributed asserts that the span belongs to no project at all. This is
	// an observation; the absence of a row is not.
	Unattributed Assertion = "unattributed"
)

// Assertions are every assertion, in the order a help message should list them.
func Assertions() []Assertion {
	return []Assertion{Engaged, NotEngaged, Boundary, Attributed, Unattributed}
}

// Verdict is what a reviewer could say about a span as a whole.
type Verdict string

const (
	// Recalled means the reviewer remembers the span. Corrections may hang off
	// the review; their absence means the machine's answer was agreed with.
	Recalled Verdict = "recalled"
	// Unremembered means the reviewer looked and could not recall. It is
	// informative, it is distinct from both confirmation and correction, and it
	// must never be coerced into either.
	Unremembered Verdict = "unremembered"
)

// Verdicts are every verdict, in the order a help message should list them.
func Verdicts() []Verdict { return []Verdict{Recalled, Unremembered} }

// Shown is what was IN FRONT OF the reviewer when they answered.
//
// It is not a detail of the interface. A label formed by agreeing with a
// derivation and a label formed by reading the observations are worth
// completely different amounts, and the difference has been measured: over the
// fourteen presence-covered reviewed days the fit residual at the shipped
// 15m/5m/5m is exactly 0.000h, contradicted is 0.000h at EVERY point in the
// grid, and the reference parameters rank 1 of 22 live points. A reviewer
// confirming blocks he was shown produces an engaged set that is a strict
// subset of the derivation, so the objective cannot be improved by claiming
// less and claiming more only adds contradiction. The minimum sits at the
// reference by construction, and no number of further days of the same kind can
// separate the model from the reviewer's agreement with it. See
// project/private/2026-08-02-a-fitter-that-reports-an-interval.md.
//
// A fitter that pools the two populations therefore reports a precision it has
// not got. This column is what lets it refuse to.
type Shown string

const (
	// Derivation is the ordinary review: the reviewer was shown the machine's
	// sittings and asked whether they were right.
	Derivation Shown = "derivation"
	// Observations is a blind pass: the reviewer was shown where evidence was
	// SEEN, with no block, no sitting and no margin on the screen, and was
	// asked when work started and stopped. The answer is formed independently
	// of the parameters, so a fit against it measures the model.
	Observations Shown = "observations"
)

// Everything is every value, in the order a help message should list them.
func Everything() []Shown { return []Shown{Derivation, Observations} }

// Span is a wall-clock interval: the machine's answer, or a piece of it.
//
// This is the only vocabulary this package shares with the rest of Timetattle.
// It deliberately does not name a session, a session id, or anything else that
// a re-derivation destroys — and it is now literally the shared interval type
// rather than a copy of it, so a caller holding sittings can hand them here
// without a conversion. internal/timespan carries no derivation vocabulary of
// its own, which is what keeps that import from making corrections an input to
// the model they are measured against.
type Span = timespan.Span

// Review is one act of looking at a span.
type Review struct {
	// ID is the row's identity in the store, and breaks ties in the fold when
	// two reviews were recorded in the same microsecond. Zero on a review that
	// has not been stored yet.
	ID int64

	Start, End time.Time
	Verdict    Verdict

	// Shown is what the reviewer was looking at. The zero value is Derivation,
	// which is what every review recorded before a blind pass existed and what
	// a caller who does not say means.
	Shown Shown

	// Origin is who or what recorded this, and RecordedAt is when — criterion
	// RLN. Neither describes the reviewed work; that is Start..End.
	Origin     string
	RecordedAt time.Time
	Note       string
}

// Saw is what this reviewer was shown, with the zero value read as the ordinary
// case rather than as a missing one. A review recorded before there was
// anything else to show was a review of the derivation.
func (r Review) Saw() Shown {
	if r.Shown == "" {
		return Derivation
	}
	return r.Shown
}

// Adjustment is one assertion about one span.
type Adjustment struct {
	ID int64

	// ReviewID is the review pass this came out of, or zero for a correction
	// recorded on its own. A correction may not hang off a review whose verdict
	// is Unremembered: the review says the span could not be recalled and the
	// correction says what happened in it, and one of them is false.
	ReviewID int64

	Start, End time.Time
	Assertion  Assertion

	// Project is set on Attributed and empty on every other assertion.
	Project string

	Origin     string
	RecordedAt time.Time
	Note       string
}

// Validate refuses the shapes the schema refuses, in words that name the
// problem, so a person typing a correction hears about it before SQLite does.
func (a Adjustment) Validate() error {
	if a.Start.IsZero() || a.End.IsZero() {
		return fmt.Errorf("an adjustment needs both ends of its span; it is keyed to wall clock and nothing else")
	}
	if a.End.Before(a.Start) {
		return fmt.Errorf("the span %s..%s ends before it starts", stamp(a.Start), stamp(a.End))
	}
	if !slices.Contains(Assertions(), a.Assertion) {
		return fmt.Errorf("%q is not an assertion; write one of %s", a.Assertion, joinAssertions())
	}
	if instant := a.End.Equal(a.Start); instant != (a.Assertion == Boundary) {
		if instant {
			return fmt.Errorf("a %s assertion across no time at all asserts nothing; "+
				"give it a span, or record a boundary if the point is that one sitting ended and another began",
				a.Assertion)
		}
		return fmt.Errorf("a boundary is one instant, not the span %s..%s; "+
			"record not-engaged if there was a gap between the two sittings", stamp(a.Start), stamp(a.End))
	}
	if named := a.Project != ""; named != (a.Assertion == Attributed) {
		if named {
			return fmt.Errorf("a %s assertion carries no project, so %q would be stored and never read; "+
				"record it as attributed, or drop the project", a.Assertion, a.Project)
		}
		return fmt.Errorf("an attributed assertion names no project; " +
			"record it as unattributed if the point is that the span belongs to none")
	}
	if a.Origin == "" {
		return fmt.Errorf("an adjustment must say who or what made it, so it can be traced back and re-examined")
	}
	if a.RecordedAt.IsZero() {
		return fmt.Errorf("an adjustment must say when it was recorded; the fold resolves conflicts by that time")
	}
	return nil
}

// Validate refuses the shapes the schema refuses.
func (r Review) Validate() error {
	if r.Start.IsZero() || r.End.IsZero() {
		return fmt.Errorf("a review needs both ends of the span it looked at")
	}
	if !r.End.After(r.Start) {
		return fmt.Errorf("the span %s..%s covers no time, so nothing was reviewed", stamp(r.Start), stamp(r.End))
	}
	if r.Verdict != Recalled && r.Verdict != Unremembered {
		return fmt.Errorf("%q is not a verdict; write %s or %s", r.Verdict, Recalled, Unremembered)
	}
	if r.Shown != "" && !slices.Contains(Everything(), r.Shown) {
		return fmt.Errorf("%q is not something a reviewer can have been shown; write %s or %s",
			r.Shown, Derivation, Observations)
	}
	if r.Origin == "" {
		return fmt.Errorf("a review must say who or what performed it")
	}
	if r.RecordedAt.IsZero() {
		return fmt.Errorf("a review must say when it was recorded")
	}
	return nil
}

// Overlay is a folded log: what the corrections say, resolved, with nothing
// destroyed on the way.
//
// It is a value rather than a query, so a caller folds once and asks many
// times — which is what the day review does, an hour at a time down a day.
type Overlay struct {
	engagement []piece[bool]
	attributed []piece[string]
	reviewed   []piece[Verdict]
	boundaries []time.Time
	unreadable []Adjustment
}

// piece is a span the fold has resolved to one value. Pieces of one kind are
// disjoint, which is what makes a lookup return at most one answer.
type piece[T any] struct {
	span  Span
	value T
}

// Fold resolves a log of reviews and adjustments into an Overlay.
//
// Order is taken from RecordedAt, with ID breaking a tie inside one microsecond,
// and NOT from the order of the slices: a caller reading the log back under a
// different ORDER BY has to get the same answer, or the overlay is a property of
// a query rather than of the data.
//
// Nothing is dropped silently. An adjustment carrying an assertion this package
// does not know is set aside and reported by Unreadable, rather than being
// skipped in a way that looks like a correction nobody ever made.
func Fold(reviews []Review, adjustments []Adjustment) Overlay {
	var o Overlay

	sortedReviews := slices.Clone(reviews)
	slices.SortStableFunc(sortedReviews, func(a, b Review) int { return order(a.RecordedAt, a.ID, b.RecordedAt, b.ID) })
	for _, r := range sortedReviews {
		o.reviewed = layer(o.reviewed, Span{Start: r.Start, End: r.End}, r.Verdict)
	}

	sorted := slices.Clone(adjustments)
	slices.SortStableFunc(sorted, func(a, b Adjustment) int { return order(a.RecordedAt, a.ID, b.RecordedAt, b.ID) })
	for _, a := range sorted {
		span := Span{Start: a.Start, End: a.End}
		switch a.Assertion {
		case Engaged:
			o.engagement = layer(o.engagement, span, true)
		case NotEngaged:
			o.engagement = layer(o.engagement, span, false)
		case Boundary:
			o.boundaries = append(o.boundaries, a.Start)
		case Attributed:
			o.attributed = layer(o.attributed, span, a.Project)
		case Unattributed:
			o.attributed = layer(o.attributed, span, "")
		default:
			o.unreadable = append(o.unreadable, a)
		}
	}
	return o
}

// Unreadable is every adjustment Fold could not interpret. It is empty for
// anything that came out of the store, whose schema refuses an unknown
// assertion; it is not empty for a log built in Go and never validated.
func (o Overlay) Unreadable() []Adjustment { return slices.Clone(o.unreadable) }

// Engaged applies the corrections to the machine's answer and returns the
// corrected one, leaving the argument untouched.
//
// Spans asserted engaged are added, spans asserted not-engaged are removed, and
// boundaries cut what is left. The order is not interchangeable: cutting first
// and then unioning would silently re-join every seam a boundary was recorded
// for.
//
// A boundary emits two spans that touch. That is the point of it — same wall
// clock, one more sitting — so the result is deliberately NOT re-merged, and a
// caller that unions it again will undo every split in it.
func (o Overlay) Engaged(machine []Span) []Span {
	var on, off []Span
	for _, p := range o.engagement {
		if p.value {
			on = append(on, p.span)
		} else {
			off = append(off, p.span)
		}
	}

	result := timespan.Subtract(timespan.Union(append(slices.Clone(machine), on...)), off)
	for _, b := range o.boundaries {
		result = cutAt(result, b)
	}
	return result
}

// EngagedAt reports what the corrections say about one instant, in three
// answers rather than two:
//
//	(true,  true)  — asserted to have been work
//	(false, true)  — asserted NOT to have been work
//	(false, false) — nobody has said, and the machine's answer stands
//
// This is what a day review renders an hour at a time, and it is the reason the
// fold keeps its resolved pieces DISJOINT rather than merely subtracting the
// not-engaged spans at the end. Engaged() would give the same totals either
// way — mutating the not-engaged layering breaks zero tests, measured — but
// with overlapping pieces this lookup would return whichever assertion happened
// to sort first, which is the class of bug that has no symptom until somebody
// quotes the number.
func (o Overlay) EngagedAt(at time.Time) (bool, bool) { return lookup(o.engagement, at) }

// Project reports what the corrections say the span containing at belongs to.
//
// Three answers, and they are three on purpose:
//
//	("a-project", true)  — asserted to belong to that project
//	("",          true)  — asserted to belong to NO project
//	("",          false) — nobody has said
//
// A caller that reads the second and third as the same thing turns 490,273
// never-examined events in the live store into assertions nobody made.
func (o Overlay) Project(at time.Time) (string, bool) { return lookup(o.attributed, at) }

// Attribution is one resolved stretch of the attribution log: a span, and who
// the corrections say it belonged to once the later assertions have won.
//
// An empty Project is the assertion that the span belonged to NO project. The
// third state — nobody has said — is the absence of a piece covering the
// instant, and a caller that reads a missing piece as an empty one has turned
// every unexamined hour in the corpus into a correction nobody made.
type Attribution struct {
	Span
	Project string
}

// Attributions is the fold read as a list rather than as a point query.
//
// Project(at) is what a day review needs, an hour at a time. A report needs
// this shape instead: the pieces are disjoint and in time order, so a
// segmentation can be cut at their edges before any figure is summed over it.
// Both are the same resolution of the same log — this returns exactly the
// pieces Project() looks its answer up in — so the two readings cannot drift.
//
// The pieces are copied out. The overlay is folded once and asked many times,
// and a caller that sorted or rewrote what it got back would be editing every
// later reader's answer.
func (o Overlay) Attributions() []Attribution {
	out := make([]Attribution, 0, len(o.attributed))
	for _, p := range o.attributed {
		out = append(out, Attribution{Span: p.span, Project: p.value})
	}
	slices.SortStableFunc(out, func(a, b Attribution) int { return a.Start.Compare(b.Start) })
	return out
}

// Review reports whether a span containing at has been looked at, and what the
// reviewer could say about it.
//
//	(Recalled,     true)  — looked at, and remembered
//	(Unremembered, true)  — looked at, and could not be remembered
//	("",           false) — never looked at
//
// The second is not a correction and not a confirmation. It says the machine's
// answer for that span is unconfirmed, which is different from wrong and
// different from unexamined, and it never removes the machine's answer.
func (o Overlay) Review(at time.Time) (Verdict, bool) { return lookup(o.reviewed, at) }

// layer puts a new assertion over the pieces already resolved: it cuts the new
// span out of every earlier piece and appends itself. That is the whole of
// "the later assertion wins, for the instants it covers and for no others".
func layer[T any](pieces []piece[T], s Span, v T) []piece[T] {
	out := make([]piece[T], 0, len(pieces)+2)
	for _, p := range pieces {
		for _, rest := range timespan.Subtract([]Span{p.span}, []Span{s}) {
			out = append(out, piece[T]{span: rest, value: p.value})
		}
	}
	return append(out, piece[T]{span: s, value: v})
}

// lookup finds the piece covering an instant. Pieces are disjoint, so there is
// at most one, and the zero value with false is "nobody has said".
func lookup[T comparable](pieces []piece[T], at time.Time) (T, bool) {
	var zero T
	for _, p := range pieces {
		if !at.Before(p.span.Start) && at.Before(p.span.End) {
			return p.value, true
		}
	}
	return zero, false
}

// order sorts by when a judgment was recorded, then by id.
//
// The id is not decoration. Two assertions recorded in the same microsecond
// would otherwise resolve by whatever order a query returned them in, and the
// answer would differ between two readers of the same database with nothing to
// flag it.
func order(aAt time.Time, aID int64, bAt time.Time, bID int64) int {
	if aAt.Before(bAt) {
		return -1
	}
	if aAt.After(bAt) {
		return 1
	}
	switch {
	case aID < bID:
		return -1
	case aID > bID:
		return 1
	}
	return 0
}

// cutAt splits any span strictly containing at into two that touch there.
//
// Strictly, because a cut on an endpoint would emit a zero-length span — the
// shape the schema refuses to store, and one that must not be manufactured on
// the way out either.
func cutAt(spans []Span, at time.Time) []Span {
	out := make([]Span, 0, len(spans)+1)
	for _, s := range spans {
		if at.After(s.Start) && at.Before(s.End) {
			out = append(out, Span{Start: s.Start, End: at}, Span{Start: at, End: s.End})
			continue
		}
		out = append(out, s)
	}
	return out
}

func joinAssertions() string {
	var out strings.Builder
	for i, a := range Assertions() {
		if i > 0 {
			out.WriteString(", ")
		}
		out.WriteString(string(a))
	}
	return out.String()
}

// stamp renders an instant for an error message, in the form the store writes,
// so a complaint about a span is comparable with what is in the column.
func stamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05.000000Z") }
