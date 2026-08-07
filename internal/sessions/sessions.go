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
// Total engaged time ranges from 530.4h at a two-minute gap to 1199.9h at two
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
//     splits a sitting and is subtracted from it, and an observation anchored
//     inside it is dropped. A Motion autosave is written by a wall-clock timer
//     that does not care whether anybody is there; the 2026-07-11 17:53 one was
//     written with the screen off during a break Ben independently recalls, and
//     it is evidence the application was running and none at all that a person
//     was.
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
// darkness is NOT established and nothing is trimmed. Stats.Contested reports
// how much time that covers, so a figure resting on contested readings can be
// recognised as one.
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
				evidence = append(evidence, observation{span: timespan.Span{Start: at, End: at}, source: e.Source})
				continue
			}
			evidence = append(evidence, observation{
				span:   timespan.Span{Start: e.Start, End: e.End},
				source: e.Source,
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
	kept := evidence[:0]
	for _, s := range evidence {
		if timespan.Covers(absent, s.span.Start) {
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

	// One buffer for the clipped spans, reused across every piece of every
	// cluster rather than allocated per piece. Union clones what it is given,
	// so nothing outside this loop keeps a reference to it and truncating it
	// for the next piece cannot reach an answer already computed.
	var covered []timespan.Span
	for _, c := range expanded {
		// Expansion can reach into a span the screen was observed dark
		// across, and so can a focus interval that ran on after the display
		// slept. Both are removed here rather than at clustering time,
		// because a sitting either side of a dark span is two sittings.
		pieces := timespan.Subtract([]timespan.Span{c.span}, absent)
		res.Stats.TrimmedByAbsence += c.span.Duration() - timespan.Total(pieces)
		if len(pieces) > 1 {
			res.Stats.SplitByAbsence++
		}
		// Observations are attributed to a piece by their EXPANDED span, the
		// same spans the pieces were carved out of, so that a piece made
		// entirely of tail-out still names the observation it hangs off.
		//
		// It does NOT follow that every piece has one. Two dark spans inside
		// one cluster leave a middle piece that can sit between two
		// observations rather than containing either; that piece is dropped
		// below rather than emitted as a sitting nothing evidenced.
		for _, piece := range pieces {
			s := Session{Start: piece.Start, End: piece.End}
			covered = covered[:0]
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
			if s.Events == 0 {
				res.Stats.UnevidencedPieces++
				res.Stats.UnevidencedDropped += piece.Duration()
				continue
			}
			slices.Sort(s.Sources)
			s.Margin = piece.Duration() - timespan.Total(timespan.Union(covered))
			res.Sessions = append(res.Sessions, s)
		}
	}
	return res, nil
}
