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
	//
	// BOTH ARE STRUCTURALLY ZERO under DarknessPolicy SuppressTimerOnly, which
	// is what this build computes. Darkness no longer subtracts anything from a
	// cluster, so there is no input by which either can be non-zero — they are
	// not "measured and found to be nothing", which is the reading a bare zero
	// invites and the confusion this package exists to refuse. They are kept
	// rather than deleted because a stored derivation records the policy it was
	// computed under, so a row written under SuppressAllAndTrim still carries
	// real figures here and must stay readable.
	SplitByAbsence   int
	TrimmedByAbsence time.Duration
	// SuppressedByAbsence counts evidence observations dropped for being
	// anchored inside observed darkness. Under SuppressTimerOnly only
	// source.CauseTimer observations are, so this is a count of wall-clock
	// artifacts and not of work.
	SuppressedByAbsence int
	// UnevidencedPieces counts fragments left with no observation in them
	// after darkness cut a sitting up, and UnevidencedDropped is how much
	// time they covered.
	//
	// The case was found by the schema rather than by design: two dark spans
	// inside one cluster left a middle piece lying BETWEEN two observations
	// rather than containing either, so nothing evidenced it and nothing had
	// observed darkness in it, and it reached CHECK (events > 0) as a sitting
	// resting on nothing.
	//
	// ALSO STRUCTURALLY ZERO under SuppressTimerOnly, and for the same reason:
	// pieces were made by subtracting darkness, nothing subtracts darkness now,
	// so a cluster is one piece and that piece always holds the observations it
	// was built from. The invariant the case broke still holds and is still
	// tested (TestNoSittingRestsOnZeroObservations) — what has gone is the route
	// to breaking it, not the guarantee.
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
	// reached across it. It was the rule internal/sessions.Cluster implemented
	// from the backlight stream first being read as presence until 2026-08-14.
	//
	// Its cost on the live corpus at 2026-08-14, from one `timetattle
	// sessionize`: 24,704 observations suppressed and 48.6h trimmed across
	// 129 splits, out of 639.7h of observed darkness.
	//
	// RETIRED, and retained only so that a derivation stored under it still
	// reads as what it was. No binary computes it.
	SuppressAllAndTrim DarknessPolicy = "suppress-all-and-trim"

	// SuppressTimerOnly drops only observations a wall-clock timer wrote — a
	// Motion autosave and nothing else — and subtracts no time from any
	// sitting. An agent producing work behind a sleeping screen keeps its
	// hours under this rule and loses them under the one above.
	//
	// It is what internal/sessions.Cluster implements as of 2026-08-14, and on
	// the live corpus it leaves 1 observation suppressed and 0.0h trimmed across
	// 0 splits, against the same 639.7h of observed darkness. The one is the
	// Motion autosave the retired rule was written for.
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
// This constant went stale within hours of being written, exactly as its own
// warning predicted: it was authored alongside the run table while a second
// worker was narrowing what darkness may do, and the two landed in the same
// integration still disagreeing. Nothing caught it but a human reading both
// branches. Treat that as the measured failure rate of the coupling, not as a
// one-off — TestTheRecordedPolicyMatchesWhatClusterDoes exists because of it.
const CurrentDarknessPolicy = SuppressTimerOnly
