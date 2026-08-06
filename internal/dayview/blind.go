package dayview

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// A DAY REVIEWED FROM ITS OBSERVATIONS, WITH THE MODEL'S ANSWER OFF THE SCREEN.
//
// # The defect, measured rather than suspected
//
// Over the fourteen presence-covered reviewed days the fit residual at the
// shipped gap 15m, lead-in 5m, tail-out 5m is EXACTLY 0.000h. Contradicted is
// 0.000h at every point in the grid. The reference parameters rank 1 of 22 live
// points and the fitter prints CIRCULAR and refuses to report an answer.
// project/private/2026-08-02-a-fitter-that-reports-an-interval.md.
//
// The cause is structural. Confirm draws its labels from blocks the reviewer
// was SHOWN, so an agreeing reviewer's engaged set is a strict subset of the
// derivation: the objective cannot be improved by claiming less, and claiming
// more only adds contradiction, so the minimum sits at the reference by
// construction. More days of the same kind cannot help.
//
// # Every rendering of a day is a model, so this one is argued
//
// A day is 2,017 observations on 2026-08-01 and 81,984 on 2026-02-17. A person
// cannot read that, so something must reduce it, and every reduction is a
// choice. The choice made here is the SILENCE CENSUS, and it is defensible for
// one specific reason: it carries no clustering parameter.
//
// A silence is the hole between the running reach of everything seen so far and
// the next observation's start. Sort, subtract, report the holes. Nothing in
// that depends on the gap, the lead-in or the tail-out — TestTheCensusIsTheSame
// AtEveryParameterValue holds it to that — so the census is invariant under
// exactly the three quantities being fitted.
//
// # Where the census is the WHOLE space of boundaries, and where it is not
//
// The first version of this argument claimed that the clustering only ever
// places a boundary inside a silence, because a boundary inside an observed
// stretch would split evidence from itself. THAT IS FALSE, and the real-data
// case beside the constructed ones is what found it. On 2026-07-28 at
// 12:59:36Z a sitting ends three seconds BEFORE the biome-appusage interval
// running to 12:59:39.106956Z, because knowledgec-backlit recorded the screen
// going dark. Observed darkness cuts a sitting in the middle of an observed
// stretch.
//
// The corrected claim is narrower and is the one this presentation needs:
//
//   - In the POINTS-ONLY regime the census is complete. knowledgec-backlit —
//     the only stream this project reads as presence — has no row before
//     2026-07-02T02:47:40Z, so before then there is no darkness to cut
//     anything. Measured on 2026-03-21 across a 2m/15m/1h grid: 63 internal
//     boundaries, every one of them inside a silence.
//     TestOnAPointsOnlyDayEveryBoundaryFallsInsideASilence.
//
//   - In the PRESENCE-COVERED regime it is not. On 2026-07-28, 22 of 39
//     internal boundaries are placed by observed darkness rather than by
//     silence. TestOnAPresenceCoveredDayTheOnlyExceptionsAreObservedDarkness
//     bounds the exception to that one cause rather than leaving it open.
//
// This is exactly the right way round. The blind pass exists for the
// points-only regime — 175 days, 809.1h, 75.7% of all engaged time, two
// caveated reviewed days and zero job claims behind it — and that is the regime
// in which the census offers every boundary there is. A KNOWN GAP REMAINS for
// July onwards: a stretch the screen was dark through but Biome kept recording
// focus in is drawn as observed, is never put to the reviewer, and is therefore
// taken as work. Aim this at pre-July days until darkness is askable too.
//
// Measured with scripts/silencecensus against the live store on 2026-08-02,
// evidence sources only:
//
//	day           obs   >=2m  >=5m  >=15m   h<2m
//	2026-02-17  81984     45    22     14   4.31
//	2026-03-21  12832     50    24     12   6.26
//	2026-08-01  38772     24    18      2   1.22
//
// 81,984 observations reduce to 45 questions. That is the whole argument for
// the presentation, and it is why this is aimed at the pre-July days: they are
// the regime holding 75.7% of all engaged time and 809.1h over 175 days, with
// two caveated reviewed days and zero job claims behind it
// (project/private/2026-08-02-the-ground-truth-and-the-corpus-are-in-different-regimes.md).
//
// # Why the timeline is not what the reviewer answers from
//
// A cell timeline still has to be drawn, and at sixty cells a twelve-hour day
// is twelve minutes to the cell — the same order as the 15m gap being fitted.
// A cell that lights because ANY observation fell in it therefore dilates each
// instant by roughly the parameter in question, which is exactly the smuggling
// this task exists to avoid.
//
// It is harmless here because the timeline is an INDEX and not the answer. The
// reviewer answers about named silences with clock times attached, and the
// clock times are typed rather than picked off the grid. The picture says where
// to look; the census says what is there.
//
// # What is asserted, and what is deliberately left open
//
// A silence NOBODY WAS ASKED ABOUT gets no row at all. That is the difference
// between a display floor and a gap threshold: defaulting the short silences to
// "work continued" would be asserting a bridging rule at the floor, and the
// floor would then be a fitted parameter chosen by whoever set the default.
// The cost is stated rather than hidden — 4.31h of sub-2m silence on
// 2026-02-17, 6.26h on 2026-03-21 — and it costs statistical power, not
// correctness.
//
// The one thing assumed is that an instant SOME source observed was work unless
// the reviewer says otherwise. That is the tool's premise and it is not one of
// the three parameters, so holding it while the gap, the lead-in and the
// tail-out are all off the screen smuggles nothing. It is also sayable in the
// other direction: NotWork records an observed stretch the reviewer denies, and
// on 2026-08-01 that is what actually happened — four blocks carrying 2,017
// observations between them, rejected one at a time.

// Silences are the stretches of this day in which no source observed anything,
// in time order, longest-first selection left to the caller.
//
// until bounds the last one, and is the horizon: past it, silence is the
// store's rather than the world's, so a question about it is unanswerable
// rather than merely hard. floor drops the short ones and may be zero.
//
// The running REACH is what the holes are measured from, not the previous
// observation's own end. An interval spanning several later points would
// otherwise appear to contain silences inside itself.
func (d Day) Silences(until time.Time, floor time.Duration) []Span {
	if until.After(d.End) {
		until = d.End
	}
	if !until.After(d.Start) {
		return nil
	}

	seen := slices.Clone(d.Observed)
	slices.SortFunc(seen, func(a, b Span) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return a.End.Compare(b.End)
	})

	var (
		out   []Span
		reach = d.Start
	)
	keep := func(from, to time.Time) {
		if to.After(until) {
			to = until
		}
		if to.Sub(from) >= floor && to.After(from) {
			out = append(out, Span{Start: from, End: to})
		}
	}
	for _, s := range seen {
		if !s.Start.Before(until) {
			break
		}
		if s.Start.After(reach) {
			keep(reach, s.Start)
		}
		if s.End.After(reach) {
			reach = s.End
		}
	}
	if reach.Before(until) {
		keep(reach, until)
	}
	return out
}

// Sighting is one thing a source saw, and the instant it saw it.
type Sighting struct {
	// What the source recorded, verbatim: an application identifier, a
	// document under its project, a project, or a reading. Empty when the
	// source records none of those, which is a true statement about that
	// source and not a formatting failure.
	What string
	// At is the instant of the sighting NEAREST the silence: the end of the
	// last observation before it, or the start of the first one after it.
	At time.Time
}

// Trace is one source's raw evidence beside one silence.
type Trace struct {
	// Source is the source ID, unprettified. It is the same vocabulary the
	// freshness line already uses on this screen.
	Source string
	// Seen are the distinct things this source recorded, NEAREST THE SILENCE
	// FIRST, at most as many as the caller asked for.
	Seen []Sighting
	// More is how many distinct things were left out. A truncated list nobody
	// mentions is read as a whole one, which this project has shipped twice.
	More int
}

// Nearby is the RAW EVIDENCE FLANKING a silence: for each source, the distinct
// things it recorded nearest the silence on each side, with the instant of each.
//
// # Why this is not the circularity the blind pass exists to remove
//
// A reviewer asked what he was doing between 08:03 and 08:26 three weeks ago,
// handed a row of block characters and nothing else, cannot answer — and does
// not. The first real session was abandoned for exactly that reason.
//
// What may be shown is bounded by one question: does the display MOVE when the
// parameters move? The model's derived answer does, by definition, and a
// reviewer agreeing with a shape he was shown restates the derivation — that is
// the measured defect, residual exactly 0.000h over fourteen days, and it is why
// this pass exists. Nothing here moves. The silence itself comes from the
// census, which is invariant under the gap, the lead-in and the tail-out; the
// sightings are a summary of raw observations selected only by their position
// relative to that silence. TestNearbyDoesNotMoveWhenTheParametersDo holds it to
// that at 2m, 15m and 1h, and it is the property to break first if this is ever
// suspected.
//
// The subtle case is worth stating rather than dismissing. Seeing "git in
// scubapants at 08:02" may lead a reviewer to reason "so I probably started
// about 08:00", and 08:00 is a boundary the lead-in also tries to place. That is
// INFERENCE FROM AN OBSERVATION and not leakage of a model boundary, and the
// difference is decidable: to restate the model he would have to know the
// lead-in is five minutes, which is precisely what this screen withholds. He is
// equally free to answer 07:30 — half an hour of reading nothing observed — or
// 08:02, and that freedom is the whole quantity the fit measures. Contrast the
// confirming pass, where the number is on the screen and agreement makes the
// label a subset of the derivation by construction.
//
// # What is deliberately NOT here, and it is not the model
//
// A silence is the hole in EVIDENCE OF WORK. Presence and ignored streams never
// reach Observed, so they never bound a silence — and they therefore fall INSIDE
// one. On 2026-07-17 the 2h24m silence at 11:53 holds six knowledgec-backlit
// rows, five of them reading "on". Those are raw observations, and drawing them
// would not be circular. It would be worse. "The screen was lit from 11:55 to
// 12:40" ANSWERS the question, from a stream a script can read unaided, and the
// answer would then be recorded as a person's recollection: a label no human was
// needed for, carrying a provenance it does not have. The reviewer's whole value
// is the part no source recorded.
//
// So Nearby reports the evidence-of-work streams only, and Unshown names what it
// held back — because a stream that simply vanishes from a screen reads as one
// that recorded nothing, and this project has collapsed that distinction twice
// in three separate tools.
// # THE FLANKS ARE BOUNDED BY THE NEIGHBOURING SILENCES, and that bound is not
// cosmetic
//
// The first version of this reached for the nearest sighting of each subject
// however far away it was, and rendered against 2026-07-17 it cued the silence
// at 08:26..08:36 with "com.apple.finder 10:08" — evidence from the far side of
// four other silences, sitting under a heading that says "after it". A cue from
// beyond the next silence is a statement about a different question.
//
// So each flank is the OBSERVED RUN adjacent to the silence, and the runs are
// the complement of the census: silence, run, silence, run, tiling the day.
// Nothing new is introduced by that — the census already carries it — and the
// caller supplies the two spans, because the caller is what holds the census.
func (d Day) Nearby(before, after Span, atMost int) (nearer, later []Trace) {
	return d.Sightings(before, atMost, true), d.Sightings(after, atMost, false)
}

// Sightings is what each source recorded inside one span: the distinct subjects,
// with the instant of each, at most atMost of them per source.
//
// latest picks which end of the span the budget is spent at, and it matters on a
// long run. Beside a silence at 14:16 the useful cue is what was on the screen at
// 14:15, not what the morning opened with; on the other side of the same silence
// it is the reverse. The instant reported is the one NEAREST that end, so a
// subject seen throughout a run is dated by its closest appearance.
//
// A CLOCK BUDGET IS NEVER CHOSEN, only a list length. A window measured in
// minutes would be a duration this screen picked, sitting next to a question
// about a duration the model picked, and telling them apart afterwards would be
// nobody's idea of a good time.
func (d Day) Sightings(within Span, atMost int, latest bool) []Trace {
	if atMost <= 0 {
		atMost = 1
	}
	presence, ignored := roles(d.Params)
	found := map[string]map[string]time.Time{}
	for _, e := range d.Observations {
		if presence[e.Source] || ignored[e.Source] {
			continue
		}
		s := extent(e)
		// Clipped to the working day as well as to the span. A caller reads a
		// margin either side so a project's own clustering is not truncated at
		// the edge, and that margin's evidence belongs to another day.
		if s.Start.IsZero() || s.Start.Before(d.Start) || !s.Start.Before(d.End) {
			continue
		}
		at, ok := touch(s, within, latest)
		if !ok {
			continue
		}
		m := found[e.Source]
		if m == nil {
			m = map[string]time.Time{}
			found[e.Source] = m
		}
		what := Subject(e)
		held, seen := m[what]
		if !seen || (latest && at.After(held)) || (!latest && at.Before(held)) {
			m[what] = at
		}
	}
	return traces(found, atMost, latest)
}

// touch reports where an observation meets a span, and at which end of it.
//
// A zero-length span is a single instant an observation has to cover, which is
// the ordinary case: consecutive silences ABUT at the point between them, so the
// run separating them is one instant wide and holds the whole of the evidence
// that ends one silence and begins the next.
func touch(s, within Span, latest bool) (time.Time, bool) {
	switch {
	case within.End.Before(within.Start):
		return time.Time{}, false
	case s.End.Before(within.Start) || s.Start.After(within.End):
		return time.Time{}, false
	}
	if latest {
		if s.End.After(within.End) {
			return within.End, true
		}
		return s.End, true
	}
	if s.Start.Before(within.Start) {
		return within.Start, true
	}
	return s.Start, true
}

// Subject is what one observation records, in the order a person recognises it.
//
// Verbatim, and no prettifier. "com.apple.Safari" is the value the source
// reported; turning it into "Safari" is a registry's job, and a mapping invented
// here would be one more thing on this screen that nothing else agrees with.
func Subject(e source.Event) string {
	switch {
	case e.Identity.App != "":
		return e.Identity.App
	case e.Identity.Document != "" && e.Identity.Project != "":
		return e.Identity.Project + "/" + e.Identity.Document
	case e.Identity.Document != "":
		return e.Identity.Document
	case e.Identity.Project != "":
		return e.Identity.Project
	case e.State != "":
		return e.State
	}
	return ""
}

// Unshown counts the observations Nearby will never draw, and names the sources
// they came from.
//
// It exists so that the holding-back can be SAID. See Nearby for why presence
// and ignored streams are held back, which is not because they are derived.
func (d Day) Unshown() (int, []string) {
	presence, ignored := roles(d.Params)
	held := map[string]int{}
	var total int
	for _, e := range d.Observations {
		if !presence[e.Source] && !ignored[e.Source] {
			continue
		}
		s := extent(e)
		if s.Start.IsZero() || s.Start.Before(d.Start) || !s.Start.Before(d.End) {
			continue
		}
		held[e.Source]++
		total++
	}
	return total, slices.Sorted(maps.Keys(held))
}

// traces turns the gathered sightings into rows, nearest the silence first.
//
// EVERY source that saw something gets a row. Only the sightings WITHIN a row
// are budgeted, and the budget says what it dropped.
func traces(bySource map[string]map[string]time.Time, atMost int, latest bool) []Trace {
	backwards := latest
	out := make([]Trace, 0, len(bySource))
	for src, whats := range bySource {
		tr := Trace{Source: src}
		for what, at := range whats {
			tr.Seen = append(tr.Seen, Sighting{What: what, At: at})
		}
		slices.SortFunc(tr.Seen, func(a, b Sighting) int {
			if c := a.At.Compare(b.At); c != 0 {
				if backwards {
					return -c
				}
				return c
			}
			return strings.Compare(a.What, b.What)
		})
		if len(tr.Seen) > atMost {
			tr.More, tr.Seen = len(tr.Seen)-atMost, tr.Seen[:atMost]
		}
		out = append(out, tr)
	}
	slices.SortFunc(out, func(a, b Trace) int {
		if c := a.Seen[0].At.Compare(b.Seen[0].At); c != 0 {
			if backwards {
				return -c
			}
			return c
		}
		return strings.Compare(a.Source, b.Source)
	})
	return out
}

// Account is what a reviewer said about a day they were shown only the
// OBSERVATIONS of.
//
// It is four lists rather than a verdict because the question is no longer "is
// this right?". Nothing was proposed, so there is nothing to agree with; the
// question is when work started and stopped, and these are the answers.
type Account struct {
	// Worked are silences the reviewer says work continued through, and Broke
	// are the stretches they say it did not. Broke need not be a whole silence:
	// "I stopped at 14:20 and came back at 15:05" inside a silence running
	// 14:06..15:40 is what actually measures the tail-out and the lead-in, and
	// it is the one answer the shown-the-derivation pass can never produce.
	Worked, Broke []Span
	// Forgot are silences the reviewer looked at and could not place. They
	// assert NOTHING, and the hole they leave is the point: filling it either
	// way would turn "I cannot remember" into an answer.
	Forgot []Span
	// NotWork are OBSERVED stretches the reviewer says were not work at all.
	NotWork []Span

	// Origin is who or what is answering, At is when, and Note is why in their
	// own words.
	Origin string
	At     time.Time
	Note   string
}

// Account turns what a reviewer said about a day's observations into the
// assertions it implies, WITHOUT reading a single block.
//
// That is the whole property. d.Blocks is never consulted here, so the labels
// this produces are formed independently of the parameters and a fit against
// them measures the model rather than the reviewer's agreement with it.
func (d Day) Account(a Account) (Plan, error) {
	if a.Origin == "" {
		return Plan{}, fmt.Errorf("this account names nobody, so it could not be traced back " +
			"and re-examined")
	}
	if a.At.IsZero() {
		return Plan{}, fmt.Errorf("this account says nothing about when it was recorded; " +
			"the fold resolves conflicts by that time")
	}
	horizon, bound := d.Horizon(a.At)
	if !horizon.After(d.Start) {
		return Plan{}, fmt.Errorf("nothing has looked at this day past %s (the %s), and it "+
			"begins at %s; there is nothing here to be asked about yet",
			clock(horizon), bound, clock(d.Start))
	}

	named := map[string][]Span{
		"worked through": a.Worked, "stopped in": a.Broke,
		"could not place": a.Forgot, "was not work": a.NotWork,
	}
	for what, spans := range named {
		for _, s := range spans {
			switch {
			case !s.End.After(s.Start):
				return Plan{}, fmt.Errorf("%s..%s was %s and covers no time at all",
					clock(s.Start), clock(s.End), what)
			case s.Start.Before(d.Start) || s.End.After(d.End):
				return Plan{}, fmt.Errorf("%s..%s was %s, and it reaches outside the working "+
					"day %s..%s; record that part against the day it falls in",
					clock(s.Start), clock(s.End), what, clock(d.Start), clock(d.End))
			case s.End.After(horizon):
				// Refused rather than clipped, for the reason Confirm gives at
				// the same boundary: clipping records a different assertion from
				// the one that was made, and a later reader cannot tell.
				return Plan{}, fmt.Errorf("%s..%s was %s, and it reaches past %s, which is as far "+
					"as anything has looked at this day (the %s); nothing there could have been "+
					"put to you", clock(s.Start), clock(s.End), what, clock(horizon), bound)
			}
		}
	}

	// An answer that says both things about one instant is a mistake worth
	// refusing rather than resolving. Whichever way a resolution went it would
	// record something nobody said.
	for _, clash := range []struct {
		a, b       []Span
		one, other string
	}{
		{a.Worked, a.Broke, "worked through", "stopped in"},
		{a.Worked, a.NotWork, "worked through", "was not work"},
		{a.Broke, a.Forgot, "stopped in", "could not place"},
		{a.Worked, a.Forgot, "worked through", "could not place"},
	} {
		if both := timespan.Intersect(timespan.Union(clash.a), timespan.Union(clash.b)); len(both) > 0 {
			return Plan{}, fmt.Errorf("%s..%s is recorded as both %s and %s; one of those is false",
				clock(both[0].Start), clock(both[0].End), clash.one, clash.other)
		}
	}

	// WHERE WORK WAS. The observed extents carry the premise — an instant some
	// source saw was work — and the worked silences carry the reviewer's answer.
	// They are unioned rather than listed because consecutive silences ABUT at
	// the observation instant between them, so a reviewer who worked through
	// several in a row states one stretch of work and not one per point. On a
	// points-only day that is the whole of how an engaged span comes to have
	// any duration at all: every observation covers none.
	observed := timespan.Union(extents(d.Observed))
	worked := timespan.Union(append(slices.Clone(a.Worked), observed...))
	denied := timespan.Union(append(slices.Clone(a.Broke), a.NotWork...))
	unknown := timespan.Union(a.Forgot)

	inside := []Span{{Start: d.Start, End: horizon}}
	engaged := timespan.Intersect(timespan.Subtract(worked, timespan.Union(append(slices.Clone(denied), unknown...))), inside)
	notEngaged := timespan.Intersect(timespan.Subtract(denied, unknown), inside)

	var plan Plan
	for _, s := range engaged {
		plan.Assertions = append(plan.Assertions,
			d.assert(adjustments.Engaged, s, "", a.Origin, a.At, a.Note))
	}
	for _, s := range notEngaged {
		plan.Assertions = append(plan.Assertions,
			d.assert(adjustments.NotEngaged, s, "", a.Origin, a.At, a.Note))
	}
	slices.SortStableFunc(plan.Assertions, func(x, y adjustments.Adjustment) int {
		return x.Start.Compare(y.Start)
	})

	plan.Review = adjustments.Review{
		Start: d.Start, End: horizon, Verdict: adjustments.Recalled,
		Shown: adjustments.Observations, Origin: a.Origin, RecordedAt: a.At, Note: a.Note,
	}
	return plan, nil
}

// extents drops the zero-length observations, which is every observation on a
// points-only day.
//
// A point is a true reading and is what the census is built FROM; it is simply
// not an assertion, because a span covering no time asserts nothing and the
// schema refuses it. Dropping them here is not losing them: they are the ends
// of the silences the reviewer answered about.
func extents(spans []Span) []Span {
	out := make([]Span, 0, len(spans))
	for _, s := range spans {
		if s.End.After(s.Start) {
			out = append(out, s)
		}
	}
	return out
}
