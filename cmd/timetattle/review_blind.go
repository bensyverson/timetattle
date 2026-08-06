package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// A REVIEW PASS WITH THE MODEL'S ANSWER OFF THE SCREEN.
//
// The ordinary pass draws the derived sittings and asks whether they are right,
// and that turns out to be unfittable. Over the fourteen presence-covered
// reviewed days the residual at the shipped 15m/5m/5m is EXACTLY 0.000h,
// contradicted is 0.000h at every point in the grid, and the reference
// parameters rank 1 of 22 live points; the fitter prints CIRCULAR and refuses
// the answer. A reviewer confirming blocks he was shown produces an engaged set
// that is a strict subset of the derivation, so the minimum sits at the
// reference by construction and no number of further days of the same kind can
// move it. project/private/2026-08-02-a-fitter-that-reports-an-interval.md.
//
// So this screen prints no block, no sitting, no margin and no parameter, and
// the question changes from "is this right?" to "when did you start and stop?".
// internal/dayview/blind.go carries the argument for why the SILENCE CENSUS is
// the minimum defensible presentation and why it does not smuggle the
// parameters back in. What is decided here is only how it reaches a terminal.
//
// # The timeline is an index, not the answer
//
// A cell grid still has to be drawn, and at sixty cells a working day is
// twenty-four minutes to the cell — coarser than, but the same order as, the
// 15m gap. A binary any-observation-lights-the-cell rendering therefore dilates
// each instant by roughly the parameter under test.
//
// Two things make it harmless. The reviewer answers about NAMED silences with
// their clock times printed beside them, so nothing is read off the grid; and
// the grid is drawn as a DENSITY ramp rather than a threshold, so a cell holding
// one stray observation and a cell holding four hundred do not render alike.
// A ramp has no cut point in it — it is monotone in a count — which is what
// keeps it from being a threshold wearing a picture's clothes.
//
// # Why the queue is still built from the derivation
//
// Which days are OFFERED comes from `derived_session`, because that is what the
// queue is built from and rebuilding it from raw observations would be a second
// clustering. That biases which days get reviewed and not what is said about
// them, and `-date` sidesteps it entirely. Aim this at the pre-July days: the
// regime holding 75.7% of all engaged time has two caveated reviewed days and
// zero job claims behind it.

// # A SILENCE HAS A DIRECTION, AND THE QUESTION HAS TO MATCH IT
//
// The first silence of a day runs from the working day's own boundary to the
// first thing anything saw. Asking "you stopped at?" there names a stop that
// never happened, at an instant the reviewer was asleep for: to record "I was
// asleep from 05:00 and started at 08:00" he had to answer 05:00 to a question
// about stopping, and answering it the way it READS would have asserted three
// hours of phantom overnight work into calibration ground truth. It was caught
// by his asking, not by anything here.
//
// That is the defect of 2df58b6, 4d62a32 and e475ccb once more — an option
// naming a conclusion instead of the thing being asked — and blindEdge already
// held the fact needed to word it correctly.
type blindWhere int

const (
	// blindInside is a silence with observations on both sides of it.
	blindInside blindWhere = iota
	// blindOpening is the day's lead edge: nothing had been seen yet.
	blindOpening
	// blindClosing is its tail: nothing was seen afterwards.
	blindClosing
)

// blindWhereIs says which of the three a silence is.
//
// The edges are not a decoration. They are where the LEAD-IN and the TAIL-OUT
// live, and they are the two quantities no interior silence says anything
// about, so they are the highest-value questions the pass asks.
func blindWhereIs(day dayview.Day, g timespan.Span, horizon time.Time) blindWhere {
	switch {
	case g.Start.Equal(day.Start):
		return blindOpening
	case !g.End.Before(horizon):
		return blindClosing
	}
	return blindInside
}

// blindKeysFor is the whole vocabulary at one silence, and there is deliberately
// no key that agrees with anything. Nothing was proposed, so there is nothing to
// confirm.
//
// t is the only key that changes with the direction, because it is the only one
// that asks for an instant. w and n are symmetric claims about the whole span
// and read the same way at either end; "stopped, all of it", which n used to
// say, did not — at an opening silence it named a stop as well.
func blindKeysFor(w blindWhere) []string {
	when := "[t] I stopped and came back inside it, and can say when"
	switch w {
	case blindOpening:
		when = "[t] I started inside it, and can say when"
	case blindClosing:
		when = "[t] I stopped inside it, and can say when"
	}
	return []string{"[w] worked through", "[n] not working for any of it", when,
		"[u] cannot remember", "[s] skip", "[q] quit"}
}

// blindSay is the sentence printed at the moment of choice saying which way the
// transition inside this silence runs — and, at an edge, what t cannot express.
//
// AT THE MOMENT OF CHOICE, not in a help text. A disclosure a reader has to
// remember is not a disclosure, which is the rule the sampled-day prompt was
// rewritten under.
func blindSay(w blindWhere) string {
	switch w {
	case blindOpening:
		return "This runs from the start of the working day to the first thing anything saw, " +
			"so the transition inside it goes one way: the question is when you STARTED, not " +
			"when you stopped. If you also worked and broke off inside it, t cannot say that — " +
			"press u, which asserts nothing."
	case blindClosing:
		return "Nothing was seen after this one, so the question is when you STOPPED. If you " +
			"also came back to work inside it, t cannot say that — press u, which asserts nothing."
	}
	return "Something was seen either side of this one, so t asks for both ends of the break " +
		"inside it."
}

// blindHelpFor is the long form of the keys, worded for the silence in hand.
func blindHelpFor(w blindWhere) string {
	when := `    t      you stopped and came back inside it, and can say when. This is the
           one answer that puts a boundary somewhere no observation is, which
           is the only thing that can measure the margins from outside.
`
	switch w {
	case blindOpening:
		when = `    t      you started inside it, and can say when. Everything before that
           instant is recorded as not work and everything after it as work.
           This is the day's lead edge and it is the highest-value answer in
           the pass; it is also the one that cannot say you broke off again.
`
	case blindClosing:
		when = `    t      you stopped inside it, and can say when. Everything before that
           instant is recorded as work and everything after it as not work.
           This is the day's tail and it is the highest-value answer in the
           pass; it is also the one that cannot say you came back.
`
	}
	return `
    w      you worked straight through this silence. Nothing observed you, and
           that is a fact about the instrumentation rather than about the work.
    n      you were not working for any of it.
` + when + `    u      you looked and cannot place it. NOTHING is asserted either way, and
           that is a better outcome than a guess: the two are the same row to
           everything downstream.
    s      skip. Nothing is recorded, which is what every silence too short to
           be asked about also gets.
    q      stop here. This day is not recorded.
`
}

// blindSightings is how many distinct things one source gets to name beside one
// silence. A budget on the LIST and not on the time it spans: the sightings are
// picked by nearness alone and no duration is chosen anywhere, which is what
// keeps a cue from being a bridging rule wearing a picture's clothes.
const blindSightings = 4

// drawBlind puts a day's OBSERVATIONS on one screen, and nothing else.
func (s *reviewSession) drawBlind(day dayview.Day) {
	loc := s.cal.Location
	now := time.Now().UTC()
	horizon, bound := day.Horizon(now)
	silences := s.silences(day, now)

	var observations int
	for _, r := range day.Rows {
		observations += r.Events
	}

	fmt.Fprintln(s.out)
	wrap(s.out, "", fmt.Sprintf("%s %s · %s · %s of %s or more, %s of them asked about",
		day.Day.Weekday().String()[:3], day.Day,
		plural(observations, "observation"), plural(len(s.census(day, now)), "silence"),
		compactDuration(s.floor), blindCount(len(silences))), s.cols)

	unlooked := day.Unlooked(now)
	fmt.Fprintf(s.out, "\n%s%s\n", strings.Repeat(" ", reviewLabelWidth+3), reviewAxis(s.width, day, loc))
	fmt.Fprintf(s.out, "%-*s   %s\n", reviewLabelWidth, "observed",
		blindCells(s.width, day, day.Observed, unlooked))
	for _, r := range day.Rows {
		fmt.Fprintf(s.out, "%s   %s\n", reviewLabel(r.Name()),
			blindCells(s.width, day, r.Observed, unlooked))
	}

	if len(silences) == 0 {
		fmt.Fprintln(s.out)
		wrap(s.out, "", fmt.Sprintf("nothing on this day was silent for as long as %v. There is "+
			"no boundary here anybody could be asked to place: every instant of it is either an "+
			"observation or within %v of one.", s.floor, s.floor), s.cols)
	}
	if len(silences) > 0 {
		fmt.Fprintln(s.out)
	}
	// THE DAY AS ONE CHRONOLOGY: silence, what was seen, silence. The runs and
	// the silences tile the day between them, so this loses nothing and repeats
	// nothing — which the first version did, drawing both flanks under every
	// silence and printing the same four applications twenty-four times.
	//
	// The whole CENSUS is walked and not only the silences this pass will ask
	// about. A silence left out of the budget that also vanished from the
	// listing would read as a stretch nothing was silent in.
	census := s.census(day, now)
	asked := map[time.Time]int{}
	for i, g := range silences {
		asked[g.Start] = i + 1
	}
	for i, g := range census {
		if i == 0 && g.Start.After(day.Start) {
			s.drawRun(day, timespan.Span{Start: day.Start, End: g.Start})
		}
		mark := "   ·"
		if n, ok := asked[g.Start]; ok {
			mark = fmt.Sprintf("  %2d", n)
		}
		fmt.Fprintf(s.out, "%s  %s..%s  %8s%s\n", mark,
			reviewClock(g.Start, s.cal), reviewClock(g.End, s.cal),
			reviewHours(g.Duration()), blindEdge(day, g, horizon))
		end := horizon
		if i+1 < len(census) {
			end = census[i+1].Start
		}
		s.drawRun(day, timespan.Span{Start: g.End, End: end})
	}
	if len(silences) < len(census) {
		wrap(s.out, "", fmt.Sprintf("· marks the %d not put to you. Nothing is recorded over "+
			"them either way.", len(census)-len(silences)), s.cols)
	}
	s.sayWhatIsHeldBack(day)

	reached := "The whole of this day has been looked at"
	if bound != "day" {
		reached = fmt.Sprintf("This day has been looked at as far as %s, and no further (the %s)",
			reviewClock(horizon, s.cal), bound)
	}
	fmt.Fprintln(s.out)
	wrap(s.out, "", fmt.Sprintf("%s. Silences shorter than %v, and any not put to you, are "+
		"asserted NOTHING — which is why the floor is a display budget and not a threshold: "+
		"defaulting them to 'work continued' would be asserting a bridging rule at %v.",
		reached, compactDuration(s.floor), compactDuration(s.floor)), s.cols)
	s.sayWhatIsBehind(day, len(unlooked) > 0)
	s.sayHowLongAgo(day, now)

	fmt.Fprint(s.out, "\n  █ many observations in the cell   ▄ some   ▁ one\n"+
		"  · nothing observed                ? nothing has looked here yet\n")
}

// sayHowLongAgo reports the day's distance from now, and says that a guess and
// a memory are the same row to everything downstream.
//
// # CRITERION KF1, DECIDED RATHER THAN SKIPPED: THE AGE, ALWAYS; A THRESHOLD,
// NEVER.
//
// The case for a warning is real. Ben's position is that same-week recall is
// real recall and three-week-old recall is not, the tool knows a day's age and
// the reviewer does not think about it, and confabulation is the worst outcome
// this pass can produce.
//
// The case against is equally real and pulls the other way. The corpus needs
// labels MOST exactly where memory is weakest: the regime holding 75.7% of all
// engaged time is pre-July, and a warning that discourages those days leaves it
// unlabelled for ever. Worse, a threshold would be a number nobody has measured,
// and a reviewer told he probably cannot remember will answer u on days he can.
//
// What settles it is that the number can be MEASURED LATER instead of guessed
// now. Every review row already carries the day it covers and the instant it was
// recorded, so the distance is stored for every label in the corpus — and
// nobody had looked. Measured on the live store on 2026-08-03: of eighteen
// reviewed days, sixteen were reviewed within 13.4 days, and the two remaining
// are 2026-02-17 at 164.3 days and 2026-03-21 at 132.4. Those two are precisely
// the days the points-only regime rests on. Whether recall decays is therefore a
// question the corpus can answer, not one this screen should pre-empt.
//
// So: the distance is printed on every day, never as a verdict and never as a
// gate. It prints on a fresh day too, for the reason sayWhatIsBehind prints on
// an unstale one — "this day was yesterday" is the reading that makes the
// absence of a warning mean something.
func (s *reviewSession) sayHowLongAgo(day dayview.Day, now time.Time) {
	fmt.Fprintln(s.out)
	wrap(s.out, "", fmt.Sprintf("%s %s was %s ago. Nothing here measures how far back recall "+
		"holds and no threshold acts on that number — the corpus needs labels most exactly "+
		"where memory is weakest. What IS recorded is the distance between a day and the "+
		"review of it, on every label, so whether recall decays can be measured from the "+
		"corpus later rather than argued now.",
		day.Day.Weekday().String()[:3], day.Day, plural(blindAge(day, now), "day")), s.cols)
	wrap(s.out, "", "If you are reconstructing rather than remembering, press u. It costs "+
		"nothing and asserts nothing, and that is the point: a guessed answer cannot be told "+
		"from a remembered one by anything downstream, so it would move what is being fitted "+
		"and carry false confidence into it.", s.cols)
}

// blindAge is how many whole days ago the working day on the screen began.
func blindAge(day dayview.Day, now time.Time) int {
	if now.Before(day.Start) {
		return 0
	}
	return int(now.Sub(day.Start) / (24 * time.Hour))
}

// sayWhatIsHeldBack names the streams whose readings are NOT drawn beside the
// silences, and says why.
//
// A stream that simply vanishes from a screen reads as one that recorded
// nothing, and that collapse is this project's cardinal bug. See
// dayview.Nearby for the argument: these readings fall INSIDE the silences
// rather than beside them, so drawing them would answer the question from a
// stream a script can read unaided, and the answer would be filed as a person's
// recollection.
func (s *reviewSession) sayWhatIsHeldBack(day dayview.Day) {
	n, sources := day.Unshown()
	if n == 0 {
		return
	}
	wrap(s.out, "", fmt.Sprintf("%s from %s are not drawn above. They fall INSIDE the silences "+
		"rather than beside them, and a reading that answers the question is a different thing "+
		"from one that cues it — nobody is needed to read it, and an answer copied off it would "+
		"be filed as something you remembered.",
		plural(n, "reading"), strings.Join(sources, ", ")), s.cols)
}

// drawNearby puts the RAW OBSERVATIONS FLANKING one silence on the screen.
//
// This is the answer to "this interface is not at all intuitive". A reviewer
// asked what he was doing between 08:03 and 08:26 three weeks ago, given a row
// of block characters and nothing else, does not answer — he abandons the pass,
// which is what happened. dayview.Nearby carries the argument for why this does
// not smuggle the model back onto the screen, and the property it rests on is
// held to by TestNearbyDoesNotMoveWhenTheParametersDo.
//
// The silence itself is EMPTY by construction, so both lines are labelled for
// the span they really describe. A cue must never be mistakable for an answer.
func (s *reviewSession) drawNearby(day dayview.Day, before, after timespan.Span) {
	near, later := day.Nearby(before, after, blindSightings)
	s.drawSide("  ", "before it", near,
		"nothing had been seen on this working day yet")
	s.drawSide("  ", "after it", later,
		"nothing was seen after it on this working day")
}

// drawRun draws one observed stretch of the day's chronology.
//
// A run is usually ONE INSTANT WIDE, and that is not a defect in the rendering.
// Consecutive silences abut at the observation between them, so on 2026-07-17
// the stretch separating the 08:09..08:26 silence from the 08:26..08:36 one is
// the single point at 08:26. Saying so is the honest shape of a points-only day,
// and a cue that looks thin because it is thin is worth more than one padded out
// by reaching past the next silence.
func (s *reviewSession) drawRun(day dayview.Day, run timespan.Span) {
	if run.End.Before(run.Start) {
		return
	}
	traces := day.Sightings(run, blindSightings, false)
	if len(traces) == 0 {
		return
	}
	clock := reviewClock(run.Start, s.cal)
	if run.End.After(run.Start) {
		clock += ".." + reviewClock(run.End, s.cal)
	}
	s.drawSide("      ", clock, traces, "")
}

// drawSide draws one flank: a row per source, nearest the silence first.
func (s *reviewSession) drawSide(indent, label string, traces []dayview.Trace, none string) {
	head := fmt.Sprintf("%s%-12s", indent, label)
	hang := strings.Repeat(" ", len([]rune(head)))
	if len(traces) == 0 {
		fmt.Fprintf(s.out, "%s %s\n", head, none)
		return
	}
	var name int
	for _, tr := range traces {
		name = max(name, len([]rune(tr.Source)))
	}
	for i, tr := range traces {
		lead := head
		if i > 0 {
			lead = hang
		}
		units := make([]string, 0, len(tr.Seen)+1)
		for _, seen := range tr.Seen {
			units = append(units, reviewClock(seen.At, s.cal)+" "+blindWhat(seen.What))
		}
		// SAID rather than trailed off. A truncated list nobody mentions is
		// read as a whole one, which this project has shipped three times.
		if tr.More > 0 {
			units = append(units, fmt.Sprintf("(and %d more)", tr.More))
		}
		lead += " " + pad(tr.Source, name)
		for _, line := range blindPack(lead, hang+" "+strings.Repeat(" ", name), units, s.cols) {
			fmt.Fprintln(s.out, line)
		}
	}
}

// blindWhat renders one sighting's subject, saying out loud when a source
// records none rather than printing a blank a reader would take for a bug.
func blindWhat(what string) string {
	if what == "" {
		return "(nothing named)"
	}
	return what
}

// blindPack lays sightings out in as few lines as the terminal holds, breaking
// only BETWEEN sightings.
//
// wrap is no use here: it breaks on whitespace, and every sighting is a clock
// and a subject that have to stay on one line together — "09:01" on one row and
// "com.apple.Safari" on the next is two readings of nothing.
func blindPack(lead, hang string, units []string, cols int) []string {
	if cols <= 0 {
		cols = 80
	}
	var out []string
	line := lead
	for _, unit := range units {
		switch {
		case line == lead:
			line += "  " + unit
		case len([]rune(line))+3+len([]rune(unit)) <= cols:
			line += " · " + unit
		default:
			out, line = append(out, line), hang+"  "+unit
		}
	}
	return append(out, line)
}

// census is every silence of the day at the floor, before any budget is
// applied. It is what the header counts, so that a budget can say what it left
// out rather than making the day look quieter than it was.
func (s *reviewSession) census(day dayview.Day, now time.Time) []timespan.Span {
	horizon, _ := day.Horizon(now)
	return day.Silences(horizon, s.floor)
}

// silences is what this pass will actually put to the reviewer: the longest
// `ask` of them, RETURNED IN TIME ORDER.
//
// Selected longest-first and asked forwards, and both halves are deliberate.
// Selection by length is what spends a fixed question budget where the leverage
// is; asking in time order is what lets a person walk their own day rather than
// jump about in it. Ordering the questions by length would also leak a ranking
// the reviewer would start to read as the tool's opinion.
func (s *reviewSession) silences(day dayview.Day, now time.Time) []timespan.Span {
	all := s.census(day, now)
	if s.askAtMost <= 0 || len(all) <= s.askAtMost {
		return all
	}
	longest := slices.Clone(all)
	slices.SortStableFunc(longest, func(a, b timespan.Span) int {
		return int(b.Duration() - a.Duration())
	})
	longest = longest[:s.askAtMost]
	slices.SortFunc(longest, func(a, b timespan.Span) int { return a.Start.Compare(b.Start) })
	return longest
}

// blindFlanks are the observed stretches immediately before and after a
// silence, bounded by the SILENCES either side of it.
//
// The bound is the whole of what keeps a cue about a different question off the
// screen. Rendered against 2026-07-17 without it, the silence at 08:26..08:36
// was cued with com.apple.finder at 10:08 — four silences away, under a heading
// reading "after it".
//
// The CENSUS bounds it and not the asked subset. A silence dropped by the
// question budget is still a silence, and a run allowed to swallow one would
// present evidence from the far side of a stretch nobody can account for.
func blindFlanks(census []timespan.Span, g timespan.Span, day dayview.Day,
	horizon time.Time) (before, after timespan.Span) {
	before = timespan.Span{Start: day.Start, End: g.Start}
	after = timespan.Span{Start: g.End, End: horizon}
	for i, c := range census {
		if !c.Start.Equal(g.Start) {
			continue
		}
		if i > 0 {
			before.Start = census[i-1].End
		}
		if i+1 < len(census) {
			after.End = census[i+1].Start
		}
		break
	}
	return before, after
}

// blindEdge names a silence that is one of the day's two ends rather than a
// hole between two observations. blindWhereIs decides; this renders.
func blindEdge(day dayview.Day, g timespan.Span, horizon time.Time) string {
	switch blindWhereIs(day, g, horizon) {
	case blindOpening:
		return "  before anything was seen"
	case blindClosing:
		return "  after the last thing seen"
	}
	return ""
}

func blindCount(n int) string {
	if n == 0 {
		return "none"
	}
	return fmt.Sprintf("%d", n)
}

// blindCells draws a row of the observation timeline as a DENSITY ramp.
//
// A ramp and not a threshold. `reviewCells` answers "did anything fall in this
// cell", which at twenty-four minutes to the cell dilates a single instant into
// something that reads like half an hour of work — the same order as the
// parameter being fitted. A ramp is monotone in the count and has no cut point
// in it, so it adds no rule; it only stops one stray observation from looking
// like a busy hour.
//
// The levels are absolute counts and not a share of the day's own maximum. A
// normalised ramp would make two days incomparable and would make a quiet day
// render as a busy one, which is exactly the reading a reviewer must not be
// given.
func blindCells(width int, day dayview.Day, observed, unlooked []timespan.Span) string {
	cells := make([]rune, width)
	length := day.Length()
	for i := range cells {
		cell := timespan.Span{
			Start: day.Start.Add(time.Duration(int64(length) * int64(i) / int64(width))),
			End:   day.Start.Add(time.Duration(int64(length) * int64(i+1) / int64(width))),
		}
		var seen int
		for _, o := range observed {
			if reviewTouches([]timespan.Span{o}, cell) {
				seen++
			}
		}
		switch {
		case seen >= 10:
			cells[i] = '█'
		case seen >= 2:
			cells[i] = '▄'
		case seen == 1:
			cells[i] = '▁'
		case reviewTouches(unlooked, cell):
			cells[i] = '?'
		default:
			cells[i] = '·'
		}
	}
	return string(cells)
}

// askBlind walks the day's silences and turns the answers into an account.
func (s *reviewSession) askBlind(day dayview.Day) (*dayview.Plan, bool, error) {
	now := time.Now().UTC()
	// THE DAY'S HORIZON AND NOT `now`, which is what this used to pass. On any
	// day but today `now` is past the day's own end, so `!g.End.Before(now)`
	// was false for every silence and the closing one went unnamed AT THE
	// PROMPT while drawBlind — which computes the horizon properly — named it
	// on the screen above. The two disagreed, and the wording of the question
	// now depends on which it is.
	horizon, _ := day.Horizon(now)
	census := s.census(day, now)
	silences := s.silences(day, now)
	account := dayview.Account{Origin: s.origin, At: now, Note: s.note}

	for i, g := range silences {
		where := blindWhereIs(day, g, horizon)
		before, after := blindFlanks(census, g, day, horizon)
		for settled := false; !settled; {
			fmt.Fprintf(s.out, "\n  silence %d of %d  %s..%s  %s%s\n", i+1, len(silences),
				reviewClock(g.Start, s.cal), reviewClock(g.End, s.cal),
				reviewHours(g.Duration()), blindEdge(day, g, horizon))
			s.drawNearby(day, before, after)
			wrap(s.out, "  ", blindSay(where), s.cols)
			key, ok := s.read(reviewPrompt(blindKeysFor(where), "  ", s.cols))
			if !ok {
				return nil, false, nil
			}
			switch key {
			case "w":
				account.Worked, settled = append(account.Worked, g), true
			case "n":
				account.Broke, settled = append(account.Broke, g), true
			case "u":
				account.Forgot, settled = append(account.Forgot, g), true
			case "s":
				settled = true
			case "q":
				return nil, false, nil
			case "t":
				worked, broke, ok, err := s.askWithin(day, g, where)
				if err != nil {
					fmt.Fprintf(s.out, "  %v\n", err)
					continue
				}
				if !ok {
					return nil, false, nil
				}
				account.Worked = append(account.Worked, worked...)
				account.Broke = append(account.Broke, broke...)
				settled = true
			default:
				fmt.Fprint(s.out, blindHelpFor(where))
			}
		}
	}

	plan, err := day.Account(account)
	return &plan, true, err
}

// askWithin reads the instants inside a silence that the transition really ran
// at, ASKING IN THE DIRECTION THE SILENCE RUNS.
//
// THIS IS THE ANSWER THE CONFIRMING PASS CAN NEVER PRODUCE. A reviewer agreeing
// with a shape he was shown endorses a boundary the model placed; a reviewer
// saying "I stopped at 14:20" places one where no observation is, which is the
// only thing that can measure a margin from outside the model.
//
// An interior silence takes two instants: work, break, work. AN EDGE TAKES ONE,
// and that is the fix for the defect this file's blindWhere block records. The
// mechanics were always exact — answering 05:00 and 08:00 to the two-instant
// form records the right thing — but the WORDING asked a man to name a stop at
// an instant he was asleep for, and the reading that comes naturally from it
// asserts phantom overnight work.
//
// What an edge therefore cannot express is work, then a break, then work again,
// inside the opening or closing silence. That is a real loss and it is chosen:
// preferring the loss of a rare true answer to the availability of a common
// false one is the same trade this pass makes everywhere else, and blindSay says
// so at the moment of choice rather than in a manual.
func (s *reviewSession) askWithin(day dayview.Day, g timespan.Span, w blindWhere) (
	worked, broke []timespan.Span, ok bool, err error) {
	inside := fmt.Sprintf("(inside %s..%s)", reviewClock(g.Start, s.cal), reviewClock(g.End, s.cal))

	// The edges first, because they are the reason the pass exists: they are
	// where the day's lead edge and tail live, and no interior silence says
	// anything about either.
	if w != blindInside {
		prompt, first := "  you started at? "+inside+" ", false
		if w == blindClosing {
			prompt, first = "  you stopped at? "+inside+" ", true
		}
		at, ok, err := s.askInstant(day, prompt)
		if err != nil || !ok {
			return nil, nil, ok, err
		}
		if at.Before(g.Start) || at.After(g.End) {
			return nil, nil, true, fmt.Errorf("%s is outside the silence %s..%s; something was "+
				"observed there, so it cannot be said about without being said about that too",
				reviewClock(at, s.cal), reviewClock(g.Start, s.cal), reviewClock(g.End, s.cal))
		}
		// One instant cuts the silence in two, and BOTH halves are asserted.
		// Naming when work started says nothing at all unless the hours before
		// it are recorded as not work, which is the half that pins the edge.
		early, late := timespan.Span{Start: g.Start, End: at}, timespan.Span{Start: at, End: g.End}
		if first {
			return keepReal(early), keepReal(late), true, nil
		}
		return keepReal(late), keepReal(early), true, nil
	}

	stopped, ok, err := s.askInstant(day, "  you stopped at? "+inside+" ")
	if err != nil || !ok {
		return nil, nil, ok, err
	}
	back, ok, err := s.askInstant(day, "  and started again at? ")
	if err != nil || !ok {
		return nil, nil, ok, err
	}
	switch {
	case !back.After(stopped):
		return nil, nil, true, errors.New("that break ends before it starts")
	case stopped.Before(g.Start) || back.After(g.End):
		return nil, nil, true, fmt.Errorf("%s..%s reaches outside the silence %s..%s; something "+
			"was observed there, so a break cannot cover it without being said about that too",
			reviewClock(stopped, s.cal), reviewClock(back, s.cal),
			reviewClock(g.Start, s.cal), reviewClock(g.End, s.cal))
	}
	// The remainder of the silence either side of the break is work, and saying
	// so is the whole point: it is what pins the boundary rather than leaving it
	// to be worked out. A zero-length remainder is dropped, not asserted.
	worked = append(keepReal(timespan.Span{Start: g.Start, End: stopped}),
		keepReal(timespan.Span{Start: back, End: g.End})...)
	return worked, []timespan.Span{{Start: stopped, End: back}}, true, nil
}

// keepReal drops a span covering no time at all. A zero-length remainder is not
// an assertion — the schema refuses one — and manufacturing it would record
// something nobody said.
func keepReal(s timespan.Span) []timespan.Span {
	if !s.End.After(s.Start) {
		return nil
	}
	return []timespan.Span{s}
}
