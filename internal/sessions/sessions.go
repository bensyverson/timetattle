// Package sessions groups observations into sittings.
//
// Almost every source Timetattle reads yields point observations: a commit, a
// file save, a Claude message. Presence is the only thing that directly
// observes an interval. Duration is therefore always inferred by clustering
// ACROSS sources and never interpolated WITHIN one — treating each file's
// birth-to-modification span as work claimed 70+ hours on a 13.9-hour day. This
// package is that clustering, and it is a pure function so that the inference
// can be re-run, re-fitted and argued with rather than being a property of a
// database.
//
// # The threshold is not a constant and must never become one
//
// Total presence ranges from 530.4h at a two-minute gap to 1199.9h at two
// hours — a 2.3x spread, measured in project/private/2026-07-28-spike-findings.md. The
// gap distribution is sharply bimodal, with 93.5% of gaps under ten seconds and
// only 0.22% falling between five minutes and two hours, and the intuition that
// a deep trough makes the threshold unimportant is wrong: a single 45-minute
// gap costs 45 minutes whether or not it has company, so rarity does not imply
// irrelevance. Fitting this value against ground truth is what the calibration
// phase exists for. Everything here takes it as a parameter, and the defaults
// in internal/config are labelled provisional because they are.
//
// # Three roles, and a source has exactly one
//
// Evidence is clustered. Presence bounds. Ignored sources do neither, and
// which is which is configuration, not code.
//
//   - EVIDENCE is anything that suggests work was being done. Sittings are
//     built out of it and nothing else.
//
//   - PRESENCE bounds sittings and never creates them. knowledgec-backlit is
//     the only stream this project treats as observing presence. It is
//     deliberately excluded from evidence: fourteen hours of lit screen with
//     no other trace is not fourteen hours of work, and clustering presence
//     would make an evening of reading indistinguishable from an evening of
//     building. It is also the reason engaged time and presence are two
//     metrics rather than one (task IFmcU), which a single clustered stream
//     could not express.
//
//   - IGNORED sources are read from the store and then dropped. A source lands
//     here when its MEANING is unestablished rather than when it is
//     uninteresting, and the cost of getting it wrong is measurable:
//     /device/isLocked's polarity is inferred from the field's name, and one
//     'locked' interval in the live store runs 15h32m, so clustering it as
//     evidence manufactures a fifteen-hour sitting out of a night's sleep.
//
// # Observed absent, observed present, and nothing recorded are three states
//
// This project has shipped the two-state version of this bug three times, in
// three separate tools, and it deletes real work every time: on 2026-07-14 the
// backlight sampler emitted nothing of either value between 12:35:20 and
// 14:02:24 while Biome recorded dense application switching throughout, and any
// query reading silence as darkness threw away 1h27m of confirmed work.
//
// So presence acts in one direction only:
//
//   - OBSERVED ABSENT — covered by an 'off' reading and by no 'on' reading —
//     suppresses an evidence observation anchored inside it whose Cause is
//     source.CauseTimer, and does NOTHING ELSE. It does not split a sitting, it
//     is not subtracted from one, and it suppresses nothing written by any
//     other mechanism.
//
//   - OBSERVED PRESENT never merges anything. A lit screen between two
//     observations three hours apart is not a reason to call them one sitting;
//     it is a reason the presence metric will be larger than the engaged one,
//     which is the entire point of keeping them separate.
//
//   - NOTHING RECORDED does neither. The gap threshold decides alone, exactly
//     as if the stream did not exist.
//
// Where the two readings contradict each other — knowledgeC writes both an
// isBacklit=1 and an isBacklit=0 row with identical endpoints at 2026-07-14
// 09:01:12, and 636 'on' rows sit inside longer 'off' rows in one snapshot —
// darkness is NOT established and nothing is suppressed. Stats.Contested
// reports how much time that covers, so a figure resting on contested readings
// can be recognised as one.
//
// # What a dark backlight actually observed, which is a lid
//
// The first state used to do much more than it does. Darkness split a sitting,
// was subtracted from it, and dropped any observation anchored inside it,
// arguing from a Motion autosave written at 2026-07-11 17:53 with the screen
// off during a break: evidence the application was running and none at all that
// a person was there.
//
// That argument is sound about the autosave and unsound as a rule, because it
// generalises from the one case where the mechanism is the whole explanation. A
// video render with the backlight deliberately switched off is unambiguous
// work, and it leaves exactly the same trace. So does a compile, a long export,
// and an agent running unattended. Reading a dark screen as an absence of work
// deletes every one of them.
//
// knowledgec-backlit observes a LID, and its name promises more than it can
// deliver. Lid state stands in for someone being at the machine, which stands
// in for work being done — a proxy for a proxy — and neither step holds in the
// direction that deletes. The three states above are still exactly right; the
// mistake was giving the first one a weight it cannot carry.
//
// Cause is what separates the two cases, and it was built for this. An artifact
// written by a WALL-CLOCK TIMER asserts that an application was running and
// nothing whatever about what was happening; joined with a dark reading there is
// no evidence of work in either direction, which is true of no other mechanism.
// So the timer arm stays, and everything else survives. That includes the empty
// cause — which means "not an artifact" rather than "nobody said", and which
// 868,924 live claude-transcripts rows carry — and source.CauseRecovery, whose
// evidentiary weight is documented as unmeasured and would be settled by
// default, in the direction of deleting work, if darkness were allowed to drop
// it.
//
// The rule that was replaced had, over the whole live store, reached 1 timer
// observation and 24,703 others, and cost roughly 35 hours across three
// projects in the two weeks the backlight had been collecting at all
// (`go run ./scripts/darkcause`, and `timetattle metrics --all` either side).
// It was protecting against a case that had happened once.
//
// # Engaged time is the headline, and the backlight is not in it
//
// This is the reason the change is confined to one arm rather than made
// configurable. ENGAGED TIME answers "how much did I work on this", and it
// counts work done with the screen off exactly as it counts work done with the
// screen on. A separate hands-on figure — how much of that time somebody was at
// the keyboard for — is a different question and reports a smaller number, and
// the backlight is the right instrument for THAT one. Nothing here computes it;
// the point is that darkness is entitled to affect it and not this.
//
// # Sittings are global, and attribution happens above them
//
// Clustering is over every evidence source at once, producing one disjoint,
// ordered run of wall-clock spans. It is deliberately not per-project.
//
// Engaged time is per-project and may overlap, because concurrent work on two
// projects is billable to both. Presence is wall clock and is never
// double-counted. A per-project clustering could produce the first and could
// never recover the second, since two projects' sittings would each claim the
// same hour. Global sittings give the wall-clock skeleton; per-project engaged
// time is an intersection against it, and Cluster is a pure function over a
// slice of events, so a caller wanting one project's sittings passes one
// project's events and gets them under the same parameters.
//
// # The one interaction to watch
//
// LeadIn plus TailOut at or above Gap makes Gap meaningless: every pair of
// clusters separated by less than LeadIn+TailOut merges after expansion, so the
// effective threshold becomes Gap minus the margins. Stats.MergedByExpansion
// counts those merges, and `timetattle sessionize` says so out loud, because
// the failure mode is a parameter that appears to be set and is not.
package sessions

import (
	"slices"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// Params is the whole of the derivation, and Session is one sitting. Both are
// defined in internal/sittings rather than here.
//
// This package imports internal/store, so the store cannot import this one, and
// Store.Sittings has to be able to name what it returns. Leaving the types here
// would mean a second definition of a sitting living in internal/store, drifting
// from this one from the day it was written. They are the same types under two
// names, not two types: an alias, so a sitting read out of the store and a
// sitting just clustered are interchangeable without a conversion nobody would
// keep correct.
type (
	Params  = sittings.Params
	Session = sittings.Sitting
	// Corpus is which observations a derivation was drawn from, as against
	// Params, which is what it was drawn with. Cluster never sees it: Retain
	// has already applied it by the time the events arrive here, which is
	// exactly why it cannot be a field of Params.
	Corpus = sittings.Corpus
)

// Stats is what the derivation did, in numbers, so that a result can be
// questioned without re-running it.
//
// Every count here separates something that was observed from something that
// was not. A figure that dropped observations without saying how many is a
// figure that cannot be reconciled with the store it came from.
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
	// BOTH ARE STRUCTURALLY ZERO, and have been since 2026-08-14. Darkness no
	// longer subtracts anything from a cluster, so there is no route by which
	// either can be non-zero — they are not "measured and found to be nothing",
	// which is the reading a bare zero invites and the confusion this package
	// exists to refuse. They are retained rather than deleted because the store
	// persists the derivation's statistics and a field that vanishes from one
	// run to the next is worse than one that explains itself.
	SplitByAbsence   int
	TrimmedByAbsence time.Duration
	// SuppressedByAbsence counts evidence observations dropped for being
	// anchored inside observed darkness. Only source.CauseTimer observations
	// are, so this is a count of wall-clock artifacts and not of work.
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
	// ALSO STRUCTURALLY ZERO since 2026-08-14, and for the same reason: pieces
	// were made by subtracting darkness, nothing subtracts darkness now, so a
	// cluster is one piece and that piece always holds the observations it was
	// built from. The invariant the case broke still holds and is still tested
	// (TestNoSittingRestsOnZeroObservations) — what has gone is the route to
	// breaking it, not the guarantee.
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

// Result is a derivation and everything needed to interpret it.
type Result struct {
	// Params is what produced these sittings. It travels with them because a
	// session span separated from its parameters means nothing.
	Params   Params
	Sessions []Session
	Stats    Stats
}

// Total is how much wall clock the sittings cover. Sessions are disjoint, so
// this is a sum rather than a union.
func (r Result) Total() time.Duration {
	var total time.Duration
	for _, s := range r.Sessions {
		total += s.Duration()
	}
	return total
}

// observation is one evidence event reduced to what the clustering needs: the
// interval it covers and the source it came from.
//
// The source is carried HERE rather than on timespan.Span, which is the whole
// of decision (a) in that package: an interval merged out of two observations
// has no one source, so a shared Span that carried one would have to drop it,
// guess it, or take whichever contributor sorted first. Union never sees this
// type, and the sources a sitting names are collected from its members instead.
type observation struct {
	span   timespan.Span
	source string
	// cause is what wrote the artifact, carried through because darkness is
	// entitled to act on exactly one value of it. Deducing it here from source
	// would be the mistake internal/source's header names: inference branches on
	// Shape and Cause and stays ignorant of which reader produced the event.
	cause source.Cause
}

// Cluster groups events into sittings.
//
// It is pure: the same events under the same parameters give the same result,
// in the same order, whatever order they arrive in, and the caller's slice is
// not reordered.
func Cluster(events []source.Event, p Params) (Result, error) {
	if err := p.Validate(); err != nil {
		return Result{}, err
	}
	res := Result{Params: p}

	presence := make(map[string]bool, len(p.PresenceSources))
	for _, id := range p.PresenceSources {
		presence[id] = true
	}
	ignored := make(map[string]bool, len(p.IgnoreSources))
	for _, id := range p.IgnoreSources {
		ignored[id] = true
	}

	var (
		// Sized for the worst case — every event is evidence — rather than
		// grown. A slice grown by append allocates roughly five times the
		// bytes it ends up holding, since every regrowth abandons the array
		// before it, and over a corpus of a million events that was one of
		// this function's two largest allocating lines. Presence and ignored
		// events make this an overestimate; the unused tail costs one
		// allocation that is never touched, against the several hundred
		// megabytes of abandoned arrays it replaces.
		evidence  = make([]observation, 0, len(events))
		lit, dark []timespan.Span
	)
	for _, e := range events {
		switch {
		case ignored[e.Source]:
			res.Stats.Ignored++
		case presence[e.Source]:
			res.Stats.Presence++
			// An unbounded presence reading observed one edge and no
			// extent, so there is no span to bound anything with. A state
			// that is neither 'on' nor 'off' is a stream this package does
			// not understand, and guessing which it meant is how a polarity
			// mistake becomes a deleted day.
			if e.Shape == source.Unbounded {
				res.Stats.PresenceWithoutSpan++
				continue
			}
			switch e.State {
			case "on":
				lit = append(lit, timespan.Span{Start: e.Start, End: e.End})
			case "off":
				dark = append(dark, timespan.Span{Start: e.Start, End: e.End})
			default:
				res.Stats.PresenceWithoutSpan++
			}
		default:
			res.Stats.Evidence++
			if e.Shape == source.Unbounded {
				// The extent was never observed. Inventing one from the
				// surrounding records would have charged 5m09s of an idle
				// machine to Safari in the one case measured against
				// knowledgeC, so the observation contributes its instant
				// and nothing else.
				res.Stats.Unbounded++
				at := e.Anchor()
				evidence = append(evidence, observation{
					span:   timespan.Span{Start: at, End: at},
					source: e.Source,
					cause:  e.Cause,
				})
				continue
			}
			evidence = append(evidence, observation{
				span:   timespan.Span{Start: e.Start, End: e.End},
				source: e.Source,
				cause:  e.Cause,
			})
		}
	}

	litU, darkU := timespan.Union(lit), timespan.Union(dark)
	// Darkness is only established where nothing read it as lit. Where the two
	// contradict, neither wins and nothing is deleted.
	//
	// UNIONED, not merely subtracted. Subtract leaves a run that is sorted and
	// non-overlapping but can still TOUCH: a zero-length lit reading strictly
	// inside a dark span splits it into two pieces that meet at an instant,
	// and the live corpus has such readings. Touching is not a difference
	// anything downstream can see — the same instants are covered, the same
	// total elapses, and subtracting [a,b) then [b,c) removes what [a,c) does —
	// but it is the difference between a run Subtract and Covers can take
	// cheaply and one they cannot, and this run is handed to Subtract once per
	// cluster and to Covers once per observation. Merging the joins here, once,
	// is what makes both of those free.
	absent := timespan.Union(timespan.Subtract(darkU, litU))
	res.Stats.Contested = timespan.Total(timespan.Intersect(darkU, litU))
	res.Stats.ObservedAbsence = timespan.Total(absent)

	// FILTERED IN PLACE, over evidence's own array. This is the standard Go
	// filter, and it is safe here for one reason that must stay true: evidence
	// is dead the moment this loop ends. Nothing below reads it, and everything
	// that takes an observation out of kept — the clusters' members, the
	// sessions' sources — copies the value rather than holding the slice. The
	// write index never runs ahead of the read index, so an element is only
	// ever overwritten after it has been read. Reading evidence after this loop
	// would see kept's contents, so if a future change needs it, allocate a
	// second slice here rather than restoring the read.
	//
	// A dark reading suppresses an observation written by a WALL-CLOCK TIMER and
	// nothing else. See the header: the backlight observed a lid, and a lid says
	// nothing about whether work was being produced — but a timer's artifact
	// says nothing about it either, and that is a claim about the timer rather
	// than about the darkness. Where the two coincide there is no evidence of
	// work in either direction, and only there.
	//
	// Every other cause survives, including the empty one. Empty means "not an
	// artifact" rather than "nobody said" — a focus interval and a transcript
	// record are not written by anything a manifest can describe — so there is
	// no timer to argue from, and 868,924 live claude-transcripts rows carry it.
	// source.CauseRecovery survives for the opposite reason: its evidentiary
	// weight is documented as UNMEASURED, and suppressing it would be settling
	// that question by default, in the direction of deleting work.
	kept := evidence[:0]
	for _, s := range evidence {
		if s.cause == source.CauseTimer && timespan.Covers(absent, s.span.Start) {
			res.Stats.SuppressedByAbsence++
			continue
		}
		kept = append(kept, s)
	}

	sort.SliceStable(kept, func(i, j int) bool {
		if !kept[i].span.Start.Equal(kept[j].span.Start) {
			return kept[i].span.Start.Before(kept[j].span.Start)
		}
		if !kept[i].span.End.Equal(kept[j].span.End) {
			return kept[i].span.End.Before(kept[j].span.End)
		}
		return kept[i].source < kept[j].source
	})

	// A cluster is a run of observations no two of which are separated by more
	// than Gap. The comparison is against the running maximum END, not the
	// previous start: a one-hour focus interval followed by a save five minutes
	// later is one sitting, and measuring from the start would call it 65
	// minutes and cut a continuous hour in half.
	type run struct {
		members []observation
		// reach is the furthest END any member covers, carried forward rather
		// than recomputed from the members on each append. Rescanning the run
		// to find it is quadratic in the run's length, and a dense working day
		// is one long run, so the cost is not theoretical: clustering 8,000
		// observations into one run took 215ms by rescan and takes 1.9ms by
		// carrying the value (BenchmarkClusterOneRun), which moved a full
		// `metrics` pass over the live store from 77s to 18s.
		//
		// It is a MAXIMUM and not the last member's end. Members are sorted by
		// start, so a long interval early in a run can still be the latest
		// end, and it has to keep deciding the gap after shorter observations
		// have been appended behind it.
		reach time.Time
	}
	var clusters []run
	for _, s := range kept {
		if n := len(clusters); n > 0 && s.span.Start.Sub(clusters[n-1].reach) <= p.Gap {
			clusters[n-1].members = append(clusters[n-1].members, s)
			clusters[n-1].reach = timespan.LaterOf(clusters[n-1].reach, s.span.End)
			continue
		}
		clusters = append(clusters, run{members: []observation{s}, reach: s.span.End})
	}

	// Expand, then merge whatever the expansion made touch. Emitting both
	// halves of an overlap would charge the overlapping minutes twice, to the
	// one metric whose defining property is that wall clock is never
	// double-counted.
	type cluster struct {
		span    timespan.Span
		members []observation
	}
	var expanded []cluster
	for _, c := range clusters {
		next := cluster{
			span: timespan.Span{
				Start: c.members[0].span.Start.Add(-p.LeadIn),
				End:   c.reach.Add(p.TailOut),
			},
			members: c.members,
		}
		if n := len(expanded); n > 0 && !next.span.Start.After(expanded[n-1].span.End) {
			res.Stats.MergedByExpansion++
			expanded[n-1].span.End = timespan.LaterOf(expanded[n-1].span.End, next.span.End)
			expanded[n-1].members = append(expanded[n-1].members, next.members...)
			continue
		}
		expanded = append(expanded, next)
	}

	// One buffer for the clipped spans, reused across every cluster rather than
	// allocated per cluster. Union clones what it is given, so nothing outside
	// this loop keeps a reference to it and truncating it for the next cluster
	// cannot reach an answer already computed.
	var covered []timespan.Span
	for _, c := range expanded {
		// A CLUSTER IS EMITTED WHOLE. Darkness is not subtracted from it, and
		// that is the half of the rule which has to move together with the
		// suppression above rather than separately.
		//
		// Keeping a non-timer observation there while still carving darkness out
		// here would retain the evidence and throw away the only time it could
		// have occupied: its cluster shrinks to nothing, no sitting is emitted,
		// and NOTHING counts the loss — SuppressedByAbsence does not fire, and
		// UnevidencedPieces counts pieces rather than observations. That is a
		// quieter deletion than the one this change exists to stop, and it would
		// have stranded roughly 24,700 records on the live store.
		// TestEverySurvivingObservationLandsInASitting pins the property that
		// forbids it: every observation is suppressed and counted, or placed in
		// a sitting, with no third outcome.
		piece := c.span
		s := Session{Start: piece.Start, End: piece.End}
		covered = covered[:0]
		// Observations are attributed by their EXPANDED span, so that a stretch
		// made entirely of tail-out still names the observation it hangs off.
		for _, m := range c.members {
			grown := timespan.Span{
				Start: m.span.Start.Add(-p.LeadIn),
				End:   m.span.End.Add(p.TailOut),
			}
			if !timespan.Overlaps(grown, piece) {
				continue
			}
			s.Events++
			if !slices.Contains(s.Sources, m.source) {
				s.Sources = append(s.Sources, m.source)
			}
			if clipped, ok := timespan.Clip(m.span, piece); ok {
				covered = append(covered, clipped)
			}
		}
		// UNREACHABLE BY CONSTRUCTION, and kept because the invariant it guards
		// is enforced by a CHECK (events > 0) that a real run has already
		// tripped. Members are sorted by start and the cluster span runs from the
		// first member's start less the lead-in to the furthest end plus the
		// tail-out, so every grown member span lies inside it, and Overlaps
		// counts a zero-length span as touching. Nothing can fall out.
		//
		// It became unreachable when darkness stopped carving clusters: pieces
		// were what darkness cut, a piece could land between two observations
		// rather than containing either, and there are no pieces now. So these
		// two counters read STRUCTURALLY zero rather than measured zero — a
		// distinction their own documentation makes, because a bare zero here
		// invites exactly the reading this package exists to refuse.
		if s.Events == 0 {
			res.Stats.UnevidencedPieces++
			res.Stats.UnevidencedDropped += piece.Duration()
			continue
		}
		slices.Sort(s.Sources)
		s.Margin = piece.Duration() - timespan.Total(timespan.Union(covered))
		res.Sessions = append(res.Sessions, s)
	}
	return res, nil
}
