package sittings

import "time"

// What a derivation DID, and under which rule it was allowed to do it.
//
// # Why this lives here rather than in internal/sessions
//
// It is computed there — Stats is filled in by internal/sessions.Cluster and
// nowhere else — but internal/sessions imports internal/store, so the store
// cannot import it back, and Store.Sittings has to be able to return these
// figures. Leaving the type in internal/sessions would mean a second definition
// of the same thirteen counts living in internal/store and drifting from this
// one from the day it was written. Params and Sitting moved here first, for the
// same reason and with the same consequence: internal/sessions aliases them, so
// a Stats read out of the store and a Stats just computed are one type under two
// names rather than two types needing a conversion nobody would keep correct.

// Stats is what the derivation did, in numbers, so that a result can be
// questioned without re-running it.
//
// Every count here separates something that was observed from something that
// was not. A figure that dropped observations without saying how many is a
// figure that cannot be reconciled with the store it came from.
//
// Until migration 0016 these were computed on every derivation, printed by
// `timetattle sessionize`, and discarded. Nothing stored could answer "how much
// did the darkness rule cost on the last derivation", and answering it took two
// `metrics` runs with different flags — which measures a different clustering
// rather than reading the one that produced the stored table.
type Stats struct {
	// Evidence is how many observations were clustered; Presence how many
	// were read as bounds; Ignored how many were dropped by configuration.
	// The three sum to the input.
	Evidence int
	Presence int
	Ignored  int
	// Unbounded counts evidence observations whose extent was never observed.
	// Each contributes its one endpoint as an instant and no duration.
	Unbounded int
	// PresenceWithoutSpan counts presence observations that carried no usable
	// interval — an unbounded reading, or a state that is neither 'on' nor
	// 'off'. They bound nothing, and saying so is not the same as saying the
	// stream was silent.
	PresenceWithoutSpan int
	// MergedByExpansion counts pairs of clusters joined because the lead-in
	// and tail-out overlapped. A large number here means the margins have
	// swallowed the gap threshold.
	MergedByExpansion int
	// SplitByAbsence counts sittings cut into more than one piece by an
	// observed dark span; TrimmedByAbsence is how much time that removed.
	SplitByAbsence   int
	TrimmedByAbsence time.Duration
	// SuppressedByAbsence counts evidence observations dropped for being
	// anchored inside observed darkness.
	SuppressedByAbsence int
	// UnevidencedPieces counts fragments left with no observation in them
	// after darkness cut a sitting up, and UnevidencedDropped is how much
	// time they covered.
	//
	// This is a third case, distinct from both of the above, and it was
	// found by the schema rather than by design: two dark spans inside one
	// cluster leave a middle piece that can lie BETWEEN two observations
	// rather than containing either. Nothing observed work there and nothing
	// observed darkness there. Emitting it would invent a sitting; counting
	// it as trimmed would claim a reading nobody took. So it is dropped, and
	// said out loud, which is the only treatment that is true.
	UnevidencedPieces  int
	UnevidencedDropped time.Duration
	// Contested is how much time the backlight stream claims as both lit and
	// dark. None of it is trimmed, because contradictory readings establish
	// no darkness; it is reported so a number resting on it is recognisable.
	Contested time.Duration
	// ObservedAbsence is the total span the backlight was read as dark,
	// contested time excluded. It is the denominator TrimmedByAbsence is a
	// fraction of.
	ObservedAbsence time.Duration
}

// DarknessPolicy names what a derivation let observed darkness DO to it.
//
// It is not a configurable parameter and is deliberately not a field of Params:
// nothing sets it, and a Params field that Cluster ignored would be the first
// one that did nothing. It is a property of the CODE that clustered, recorded so
// that two derivations either side of a change to that code do not read alike.
type DarknessPolicy string

const (
	// SuppressAllAndTrim drops every evidence observation anchored inside an
	// observed dark span, and subtracts the dark span from any sitting that
	// reached across it. It is the rule internal/sessions.Cluster has
	// implemented since the backlight stream was first read as presence.
	//
	// Its cost on the live corpus at 2026-08-14, from one `timetattle
	// sessionize`: 24,704 observations suppressed and 48.6h trimmed across
	// 129 splits, out of 639.7h of observed darkness.
	SuppressAllAndTrim DarknessPolicy = "suppress-all-and-trim"

	// SuppressTimerOnly drops only observations a wall-clock timer wrote — a
	// Motion autosave and nothing else — and subtracts no time from any
	// sitting. An agent producing work behind a sleeping screen keeps its
	// hours under this rule and loses them under the one above.
	//
	// It is named here before it is implemented, and that is the point of the
	// type: the stored value has to be able to distinguish the two the moment
	// the second one exists.
	SuppressTimerOnly DarknessPolicy = "suppress-timer-only"
)

// CurrentDarknessPolicy is the rule internal/sessions.Cluster implements in
// THIS build, and it is what every derivation written by this binary records.
//
// IF YOU CHANGE WHAT OBSERVED DARKNESS DOES TO A DERIVATION, CHANGE THIS WITH
// IT. There is no way for the schema or the compiler to notice that it has gone
// stale, and a stale value here is worse than none: every derivation would go on
// claiming a rule it was not computed under, and the stored series would look
// continuous across the one change it exists to make visible.
const CurrentDarknessPolicy = SuppressAllAndTrim
