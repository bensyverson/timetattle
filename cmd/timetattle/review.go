package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// review presents whole working days and records what a person says about them.
//
// It is the calibration loop's front door. 75.7% of the engaged time in the
// live store is margin that unfitted parameters invented, nothing downstream is
// believable until those parameters are fitted, and this is where the ground
// truth to fit them against comes from. Friction here is therefore not a
// cosmetic problem: a review pass that feels like a chore does not happen, and
// nothing gets fitted.
//
// So the fast path is ONE KEYSTROKE PER DAY. An entire labelled day goes on one
// screen and the reviewer presses Enter. Detail costs something only on the
// days that are actually wrong, which is what makes a wholesale confirmation
// usable without making it vague — see internal/dayview for why confirming a
// day is a complete accounting of it rather than a shrug.
//
// Every helper in this file is prefixed review, for the reason main.go's
// package doc gives.
func review(args []string) error { return reviewTo(os.Stdout, os.Stdin, args) }

// reviewTo is review with both ends redirected, so that a whole session can be
// scripted in a test and asserted on rather than merely not crashing.
func reviewTo(stdout io.Writer, stdin io.Reader, args []string) error {
	fs := flag.NewFlagSet("review", flag.ExitOnError)
	storePath := fs.String("store", "", "event store to read and record into (default: ~/.timetattle/timetattle.db)")
	configPath := fs.String("config", "", "configuration to read (default: ~/.timetattle/config.json)")
	date := fs.String("date", "", "one working day, as 2026-07-28; without it, the queue is walked")
	from := fs.String("from", "", "earliest working day to offer, as 2026-07-01")
	to := fs.String("to", "", "latest working day to offer")
	limit := fs.Int("limit", 20, "how many days to offer in one pass; 0 for all of them")
	all := fs.Bool("all", false, "include days that have already been reviewed")
	oldest := fs.Bool("oldest", false, "offer the oldest days first (default: the most recent, where recall is)")
	list := fs.Bool("list", false, "report the queue and what is known about each day, and write nothing")
	print := fs.Bool("print", false, "draw one day and stop, asking nothing and writing nothing")
	confirm := fs.Bool("confirm", false, "record that -date is right as derived, without prompting")
	unremembered := fs.Bool("unremembered", false, "record that -date was looked at and could not be recalled")
	origin := fs.String("origin", "", "who or what is reviewing (default: $USER)")
	note := fs.String("note", "", "why, in your own words; kept verbatim and never parsed")
	drillEvery := fs.Int("drill-every", 5,
		"force a block-by-block walk on one day in N, whatever the answer would have been; 0 to never")
	width := fs.Int("width", 60,
		"how many cells wide the day's timeline is drawn (default: fill the terminal)")
	blind := fs.Bool("blind", false,
		"review from the OBSERVATIONS, with no block, sitting or parameter on the screen")
	floor := fs.Duration("silence", 2*time.Minute,
		"the shortest silence a blind pass will ask about; shorter ones are asserted nothing")
	ask := fs.Int("ask", 40,
		"how many silences a blind pass will put to you, longest first; 0 for all of them")
	// PrintDefaults, not merely the prose: every other subcommand ends its
	// usage this way, and without it the "Flags:" heading below prints an
	// empty section. That is worse than no heading at all — review carries
	// seventeen flags, including the -oldest that reverses the queue and the
	// -limit that bounds it, and a reader who sees an empty list concludes
	// there are none rather than that they are hidden.
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, reviewUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	// The day view is laid out for the terminal it is going into, and clipped
	// as a backstop. columnsOf returns 0 for a pipe, which leaves both alone:
	// -print and -list are read by callers that are not people.
	cols := columnsOf(stdout)
	stdout = clipTo(stdout, cols)

	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if !set["width"] {
		*width = reviewTimeline(cols)
	}

	switch {
	case *confirm && *unremembered:
		return errors.New("a day was both confirmed and not remembered; one of those is false")
	case (*confirm || *unremembered) && *date == "":
		return errors.New("-confirm and -unremembered record a verdict on one day, so they need -date")
	case *list && (*confirm || *unremembered):
		return errors.New("-list reports and writes nothing; drop it, or drop the verdict")
	case *width < 12:
		return fmt.Errorf("a timeline %d cells wide cannot show a working day", *width)
	// -blind and -confirm are opposite claims about what was in front of the
	// reviewer, and the whole value of a blind pass is that the claim is true.
	case *blind && (*confirm || *unremembered):
		return errors.New("-blind reviews a day from its observations and -confirm records the " +
			"derivation as right; they cannot both describe the same review")
	case *blind && *list:
		return errors.New("-list reports the queue and asks nothing, so there is nothing for " +
			"-blind to hide from anybody")
	case *floor < 0:
		return fmt.Errorf("-silence is %v; a silence cannot be shorter than no time at all", *floor)
	}

	var err error
	if *storePath == "" {
		if *storePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	if *configPath == "" {
		if *configPath, err = config.DefaultPath(); err != nil {
			return err
		}
	}
	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	// The day view draws a project row for every project with ANY evidence in
	// the day, so machine activity has to be removed before it is assembled
	// rather than after somebody has read it.
	excludes, err := sessionExcludesFrom(cfg, "", false)
	if err != nil {
		return err
	}
	cal, err := cfg.Calendar.Sessions()
	if err != nil {
		return err
	}

	who := *origin
	if who == "" {
		who = os.Getenv("USER")
	}

	// Read-only wherever nothing can be written, so that drawing a day cannot
	// appear in the store's own history and cannot create a database.
	reads := *list || *print
	open := store.Open
	if reads {
		open = store.OpenReadOnly
	}
	st, err := open(*storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	window, err := reviewWindow(cal, *date, *from, *to)
	if err != nil {
		return err
	}

	derivation, err := st.Sittings(ctx, window.Start, window.End)
	if err != nil {
		return err
	}
	// Three states, and this is the one a day view would otherwise turn into
	// "you did no work". A window with no sittings and a database nobody has
	// ever sessionized are different facts.
	if !derivation.ComputedAt.IsZero() && len(derivation.Sittings) == 0 && *date == "" {
		fmt.Fprintf(stdout, "%s\nthe clustering has run and found no sittings in this window. "+
			"That is a fact about the window, not about the work.\n", *storePath)
		return nil
	}
	if derivation.ComputedAt.IsZero() {
		fmt.Fprintf(stdout, "%s\nnothing has been derived from this store, so there is no day to "+
			"confirm — which is not the same as a store with no work in it.\n"+
			"\nNEXT  timetattle sessionize\n", *storePath)
		return nil
	}

	reviews, err := st.Reviews(ctx, window.Start, window.End)
	if err != nil {
		return err
	}
	corrections, err := st.Adjustments(ctx, window.Start, window.End)
	if err != nil {
		return err
	}

	queue, err := reviewQueue(cal, derivation, reviews, corrections, window)
	if err != nil {
		return err
	}
	reach, err := reviewReach(ctx, st, derivation)
	if err != nil {
		return err
	}
	sess := &reviewSession{
		out: stdout, in: bufio.NewReader(stdin), store: st, cal: cal,
		derivation: derivation, reach: reach, excludes: excludes, origin: who, note: *note,
		width: *width, cols: cols, drillEvery: *drillEvery,
		blind: *blind, floor: *floor, askAtMost: *ask,
	}
	fmt.Fprintf(stdout, "%s\n", *storePath)
	reviewSayIfTheConfigMoved(stdout, derivation, cfg)
	if patterns := excludes.Patterns(); len(patterns) > 0 {
		wrap(stdout, "", fmt.Sprintf("NOTE: sessions.exclude is removing %s from the evidence "+
			"every day below is drawn from. The blocks come from the stored derivation and the "+
			"rows from the events; re-run `timetattle sessionize` if the two disagree.",
			strings.Join(patterns, ", ")), cols)
	}

	if *list {
		return reviewList(ctx, sess, queue, *all, *limit, *oldest, cols)
	}

	offered := reviewOffered(queue, *all || *date != "", *limit, *oldest)
	if len(offered) == 0 {
		fmt.Fprint(stdout, "every day carrying sittings in this window has been looked at. "+
			"Add -all to go over one again.\n")
		return nil
	}

	switch {
	case *print:
		day, err := sess.load(ctx, offered[0])
		if err != nil {
			return err
		}
		if *blind {
			sess.drawBlind(day)
			return nil
		}
		sess.draw(day, true)
		return nil
	case *confirm, *unremembered:
		day, err := sess.load(ctx, offered[0])
		if err != nil {
			return err
		}
		answer := dayview.Answer{Mode: dayview.Wholesale, Origin: who, At: time.Now().UTC(), Note: *note}
		if *unremembered {
			return sess.record(ctx, day, day.CannotRemember(answer))
		}
		plan, err := day.Confirm(answer)
		if err != nil {
			return err
		}
		return sess.record(ctx, day, plan)
	}
	if who == "" {
		return errors.New("-origin is required here, because $USER is unset; " +
			"a review that names nobody cannot be traced back and re-examined")
	}
	return sess.walk(ctx, offered)
}

// reviewWindow is the span of instants the queue is drawn from.
func reviewWindow(cal sessions.Calendar, date, from, to string) (timespan.Span, error) {
	if date != "" {
		if from != "" || to != "" {
			return timespan.Span{}, errors.New("-date names one working day, so -from and -to have nothing to bound")
		}
		day, err := reviewParseDay(date)
		if err != nil {
			return timespan.Span{}, err
		}
		start, end := cal.Bounds(day)
		return timespan.Span{Start: start, End: end}, nil
	}
	var out timespan.Span
	start, err := parseBoundary(from, "from")
	if err != nil {
		return timespan.Span{}, err
	}
	end, err := parseBoundary(to, "to")
	if err != nil {
		return timespan.Span{}, err
	}
	if !start.IsZero() {
		out.Start, _ = cal.Bounds(cal.DayOf(start))
	}
	if !end.IsZero() {
		_, out.End = cal.Bounds(cal.DayOf(end))
	}
	return out, nil
}

// reviewParseDay reads a working day, which is named by the civil date it
// begins on and is not an instant.
func reviewParseDay(text string) (sessions.Day, error) {
	at, err := time.Parse("2006-01-02", text)
	if err != nil {
		return sessions.Day{}, fmt.Errorf("-date %q is not a working day; write 2026-07-28. "+
			"A working day is named by the date it BEGINS on and runs to the next day's "+
			"start time, so an evening past midnight belongs to the day before it", text)
	}
	return sessions.Day{Year: at.Year(), Month: at.Month(), Date: at.Day()}, nil
}

// reviewQueue is every working day carrying sittings, assembled far enough to
// say what is known about it.
//
// The events are deliberately NOT read here. A listing needs the blocks and the
// review state and nothing else, and reading a day's evidence costs a query per
// day; the interactive walk loads each day's rows as it reaches it.
func reviewQueue(cal sessions.Calendar, derivation sittings.Derived,
	reviews []adjustments.Review, corrections []adjustments.Adjustment,
	window timespan.Span) ([]dayview.Day, error) {
	seen := map[sessions.Day]bool{}
	var days []sessions.Day
	for _, s := range derivation.Sittings {
		// Both ends, because a sitting crossing a boundary belongs to two
		// working days. The end is stepped back an instant so a sitting ending
		// exactly at 05:00 does not conjure a day it covers none of.
		for _, d := range []sessions.Day{cal.DayOf(s.Start), cal.DayOf(s.End.Add(-time.Nanosecond))} {
			if !seen[d] {
				seen[d] = true
				days = append(days, d)
			}
		}
	}

	out := make([]dayview.Day, 0, len(days))
	for _, d := range days {
		start, end := cal.Bounds(d)
		if !window.Start.IsZero() && !end.After(window.Start) {
			continue
		}
		if !window.End.IsZero() && !start.Before(window.End) {
			continue
		}
		day, err := dayview.Skeleton(cal, d, derivation, reviews, corrections)
		if err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	slices.SortFunc(out, func(a, b dayview.Day) int { return b.Start.Compare(a.Start) })
	return out, nil
}

// reviewOffered narrows the queue to what this pass will ask about.
//
// The question is ReviewedInFull rather than Reviewed, and the difference is a
// day confirmed while it was still happening. Such a day is examined only as
// far as the moment of review and its evening carries no row at all, so asking
// whether anybody has looked AT ALL would take today off the queue for ever and
// the evening would never be offered. See dayview.Confirm for why the remainder
// is left unexamined rather than clipped or asserted empty.
func reviewOffered(queue []dayview.Day, all bool, limit int, oldest bool) []dayview.Day {
	out := make([]dayview.Day, 0, len(queue))
	for _, d := range queue {
		if all || !d.ReviewedInFull() {
			out = append(out, d)
		}
	}
	if oldest {
		slices.Reverse(out)
	}
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// reviewList reports the queue and how much of it is in each of the four
// states, which is the whole of criterion 7d1 made visible.
//
// The days it OFFERS are weighed as well. The queue itself deliberately reads
// no evidence — it spans every day in the window, and a day of a listing is not
// worth a query — so the counts above are over days whose evidence nobody read,
// and the weak marks are only ever on lines this pass would actually put in
// front of somebody. Weighing does not cluster anything; see dayview.Weigh.
func reviewList(ctx context.Context, s *reviewSession, queue []dayview.Day,
	all bool, limit int, oldest bool, cols int) error {
	counts := map[dayview.State]int{}
	for _, d := range queue {
		counts[d.State()]++
	}

	offered := reviewOffered(queue, all, limit, oldest)
	order := "most recent first"
	if oldest {
		order = "oldest first"
	}
	weighed, err := s.weighEach(ctx, offered)
	if err != nil {
		return err
	}
	fmt.Fprintf(s.out, "%s of %s carrying sittings, %s\n\n",
		plural(len(offered), "day"), plural(len(queue), "day"), order)
	var thin int
	for _, d := range offered {
		// Three answers on one line. A day with no weak row says nothing, a
		// day with one says how many, and a day nobody could weigh says THAT
		// rather than passing for the first.
		//
		// The third is unreachable from here today: every day in the queue came
		// out of the stored derivation, and review stops before this when there
		// is no derivation at all. It is written because the alternative reads
		// as "weighed, and nothing was weak", and weighEach's contract is
		// tested directly rather than through this line.
		mark := ""
		switch weighed := weighed[d.Day]; {
		case weighed == nil:
			mark = "  (not weighed)"
		case len(weighed) > 0:
			var weak, rows int
			for _, st := range weighed {
				rows++
				if st == dayview.Weak {
					weak++
				}
			}
			if weak > 0 {
				thin++
				mark = fmt.Sprintf("  ! %d of %s weak", weak, plural(rows, "row"))
			}
		}
		fmt.Fprintf(s.out, "  %s  %s  %11s  %8s  %s%s\n", d.Day, d.Day.Weekday().String()[:3],
			plural(len(d.Blocks), "sitting"), reviewHours(d.Engaged()), reviewSayCoverage(d), mark)
	}

	// Wrapped rather than clipped: this sentence is five counts, the last of
	// them at the far end, and a shortened line would delete one.
	fmt.Fprintln(s.out)
	wrap(s.out, "", fmt.Sprintf("%s carry sittings: %d agreed, %d corrected, %d unremembered, "+
		"%d never examined.", plural(len(queue), "day"), counts[dayview.Agreed],
		counts[dayview.Corrected], counts[dayview.Unremembered], counts[dayview.Unexamined]), cols)
	wrap(s.out, "", "Never examined and looked-at-and-not-remembered are counted apart, because "+
		"they are different facts: the second says the machine's answer is unconfirmed.", cols)
	if thin > 0 {
		wrap(s.out, "", fmt.Sprintf("! marks %s where some project's whole trace is file "+
			"modification times: a file changed and nothing recorded anybody changing it. It is a "+
			"mark and not a filter — a real evening spent editing one document looks exactly like "+
			"this, which is why nothing drops it.", plural(thin, "day")), cols)
	}
	return nil
}

// weighEach says what each project's evidence is worth on each of these days,
// reading their observations and clustering none of them.
//
// A day ABSENT from the result, or present with a nil map, is the third state:
// it carries no derivation to read roles out of, so nothing was weighed — which
// is not the same as having weighed it and found nothing weak. A day that was
// weighed and held nothing is present with an empty map, and the two must not
// be read as one.
//
// ONE QUERY FOR THE WHOLE PASS, not one per day. Two hundred per-day reads of
// the live store take 31.9s against 5.7s for a single span read, and the output
// is byte-identical: the days are bucketed here by the same calendar
// dayview.Weigh would bucket them by. Measured with `timetattle review -list
// -all -limit 0` against a copy of the live store on 2026-08-01.
func (s *reviewSession) weighEach(ctx context.Context, days []dayview.Day) (
	map[sessions.Day]map[string]dayview.Strength, error) {
	out := make(map[sessions.Day]map[string]dayview.Strength, len(days))
	var from, to time.Time
	for _, d := range days {
		if !d.Derived() {
			continue
		}
		out[d.Day] = nil
		if from.IsZero() || d.Start.Before(from) {
			from = d.Start
		}
		if to.IsZero() || d.End.After(to) {
			to = d.End
		}
	}
	if from.IsZero() {
		return out, nil
	}
	events, err := s.store.Events(ctx, store.Filter{From: from, To: to})
	if err != nil {
		return nil, err
	}
	events, _ = sessions.Retain(events, s.excludes)

	byDay := map[sessions.Day][]source.Event{}
	for _, e := range events {
		day := s.cal.DayOf(e.Anchor())
		if _, offered := out[day]; offered {
			byDay[day] = append(byDay[day], e)
		}
	}
	for day := range out {
		out[day] = dayview.Weigh(s.cal, day, s.derivation.Params, byDay[day])
	}
	return out, nil
}

// reviewSayCoverage renders a day's state and says when only part of the day
// was examined.
//
// Coverage is reported BESIDE the verdict rather than as a fifth state, because
// the two are orthogonal: a day looked at as far as lunchtime can equally have
// been agreed with or corrected, and folding them into one word would hide
// which. Without this, a day still sitting in the queue prints as plainly
// "agreed" and a reader cannot tell why it keeps being offered.
func reviewSayCoverage(d dayview.Day) string {
	say := reviewSay(d.State())
	if d.Reviewed() && !d.ReviewedInFull() {
		return say + ", part of the day"
	}
	return say
}

// reviewSay renders a state the way a listing should read.
func reviewSay(s dayview.State) string {
	switch s {
	case dayview.Unexamined:
		return "never examined"
	case dayview.Agreed:
		return "agreed"
	case dayview.Corrected:
		return "corrected"
	case dayview.Unremembered:
		return "unremembered"
	}
	return string(s)
}

// reviewSayIfTheConfigMoved warns when the stored derivation was computed under
// parameters the config file no longer holds.
//
// The reviewer is judging the model of record, which is what is IN the table,
// and the day view draws that. A config edited since the last sessionize would
// otherwise make the drawn day disagree with what `timetattle metrics` reports
// for the same date, with nothing to say why.
func reviewSayIfTheConfigMoved(out io.Writer, derivation sittings.Derived, cfg *config.Config) {
	current := sittings.Params{
		Gap:             cfg.Sessions.Gap.Duration(),
		LeadIn:          cfg.Sessions.LeadIn.Duration(),
		TailOut:         cfg.Sessions.TailOut.Duration(),
		PresenceSources: cfg.Sessions.PresenceSources,
		IgnoreSources:   cfg.Sessions.IgnoreSources,
	}
	if derivation.Params.Equal(current) {
		return
	}
	fmt.Fprintf(out, "NOTE: these days were derived at a gap of %v and the config now says %v. "+
		"You are\nconfirming what is in the table; re-run `timetattle sessionize` to review the "+
		"current\nparameters instead.\n", compactDuration(derivation.Params.Gap),
		compactDuration(current.Gap))
}

// reviewSession is one pass over some days: what it draws on, what it reads
// answers from, and what it records them into.
type reviewSession struct {
	out        io.Writer
	in         *bufio.Reader
	store      *store.Store
	cal        sessions.Calendar
	derivation sittings.Derived
	// excludes is what sessions.exclude and the SQLite sidecar rule remove
	// from the evidence a day is drawn from. Without it, a WAL checkpoint is
	// enough to put a project row on this screen: on 2026-08-01 seven
	// unrelated repositories each showed a whole working day made of one, all
	// stamped inside the same second, which is how this was found.
	excludes sessions.Excludes
	// reach is how far the store has looked at the world: when the clustering
	// last ran, and when each source was last asked. It travels with every day
	// this session draws, because the answer recorded about a day must be
	// bounded by the same horizon the day was DRAWN under. Drawing a day as
	// fully looked-at and clipping the answer afterwards would ask one question
	// and record a different one.
	reach  dayview.Reach
	origin string
	note   string
	// blind reviews from the observations with the model's answer off the
	// screen; floor is the shortest silence it will ask about and ask is how
	// many it will put, longest first.
	blind     bool
	floor     time.Duration
	askAtMost int
	// width is how many cells the timeline is drawn in, and cols how wide the
	// whole terminal is — 0 when the output is a pipe, which means "shorten
	// nothing". They are separate because -width pins the first without saying
	// anything about the second.
	width      int
	cols       int
	drillEvery int
	done       bool // the input ran out, which is how a scripted pass ends
}

// load reads one day's evidence and assembles it with its rows.
//
// The events are read with a MARGIN either side of the day. A project's own
// clustering is truncated at the edge of whatever window it is handed, so
// reading exactly the day would make every row's first and last span shorter
// than the model says they are, by up to the lead-in and tail-out.
func (s *reviewSession) load(ctx context.Context, day dayview.Day) (dayview.Day, error) {
	pad := s.derivation.Params.Gap + s.derivation.Params.LeadIn + s.derivation.Params.TailOut
	events, err := s.store.Events(ctx, store.Filter{From: day.Start.Add(-pad), To: day.End.Add(pad)})
	if err != nil {
		return dayview.Day{}, err
	}
	events, _ = sessions.Retain(events, s.excludes)
	reviews, err := s.store.Reviews(ctx, day.Start, day.End)
	if err != nil {
		return dayview.Day{}, err
	}
	corrections, err := s.store.Adjustments(ctx, day.Start, day.End)
	if err != nil {
		return dayview.Day{}, err
	}
	built, err := dayview.Assemble(s.cal, day.Day, s.derivation, events, reviews, corrections)
	if err != nil {
		return dayview.Day{}, err
	}
	built.Reach = s.reach
	// A BLIND PASS IS NOT BOUNDED BY THE CLUSTERING, and this is the one place
	// the two tasks genuinely interact. The ordinary pass draws blocks, so a
	// stretch the clustering has not reached is a stretch the reviewer was
	// shown as empty and must not account for. A blind pass draws OBSERVATIONS,
	// which are as fresh as the harvest and owe the derivation nothing —
	// clipping there would throw away hours of real evidence a person can
	// perfectly well speak about, in the name of a horizon that does not apply
	// to what they were looking at.
	if s.blind {
		built.Reach.Derived = time.Time{}
	}
	return built, nil
}

// reviewReach reads how far the store has looked at the world.
//
// TWO HORIZONS AND NOT ONE, and which of them binds is a property of the day
// rather than a fact about the design. The standing explanation for the
// 2026-08-01 defect was harvest lag; it was not. Harvests ran through that
// afternoon, the last finishing 23 minutes before the review, and every one of
// the 1,989 observations under the disputed stretch was already in `event`. The
// DERIVATION was 1h45m behind, and 1.4379h of false not-engaged followed from
// that alone. See project/private/2026-08-02-what-the-reviewer-could-not-have-seen.md.
//
// A source that has never been read through contributes no horizon: it comes
// back with a zero LastHarvest and is left out of the minimum rather than
// pinning every day in the store to the year one. It is still named in Behind,
// so a reviewer hears about it.
func reviewReach(ctx context.Context, st *store.Store, derivation sittings.Derived) (dayview.Reach, error) {
	fresh, err := st.Freshness(ctx)
	if err != nil {
		return dayview.Reach{}, err
	}
	reach := dayview.Reach{Derived: derivation.ComputedAt}
	for _, f := range fresh {
		if f.LastHarvest.IsZero() {
			continue
		}
		reach.Behind = append(reach.Behind, dayview.Lag{Source: f.Source, LastHarvest: f.LastHarvest})
		if reach.Harvested.IsZero() || f.LastHarvest.Before(reach.Harvested) {
			reach.Harvested = f.LastHarvest
		}
	}
	return reach, nil
}

// walk offers each day in turn and records what it is told.
func (s *reviewSession) walk(ctx context.Context, days []dayview.Day) error {
	var reviewed int
	for i, queued := range days {
		day, err := s.load(ctx, queued)
		if err != nil {
			return err
		}
		// Which screen is drawn and which question is asked are ONE choice. A
		// blind draw followed by the confirming prompt would ask a reviewer to
		// agree with something they were never shown, and a drawn day followed
		// by the blind prompt would ask them to place boundaries the tool had
		// already placed. They are picked together here so neither can drift.
		question := s.ask
		if s.blind {
			s.drawBlind(day)
			question = s.askBlind
		} else {
			s.draw(day, i == 0)
		}

		plan, ok, err := question(day)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		if plan == nil {
			continue // skipped: no row at all, which is not a verdict
		}
		if err := s.record(ctx, day, *plan); err != nil {
			return err
		}
		reviewed++
	}
	fmt.Fprintf(s.out, "\n%s recorded.\n", plural(reviewed, "day"))
	return nil
}

// record writes a plan, block reviews first and the day's own review last.
//
// The order is the design. internal/dayview decides a day's state from the
// latest review COVERING it, and recording the day's own last means that a
// second reader ordering by id alone reaches the same answer.
func (s *reviewSession) record(ctx context.Context, day dayview.Day, plan dayview.Plan) error {
	for _, r := range plan.BlockReviews {
		if _, err := s.store.RecordReview(ctx, r); err != nil {
			return err
		}
	}
	id, err := s.store.RecordReview(ctx, plan.Review)
	if err != nil {
		return err
	}
	plan.Parent(id)
	for _, a := range plan.Assertions {
		if _, err := s.store.RecordAdjustment(ctx, a); err != nil {
			return err
		}
	}
	fmt.Fprintf(s.out, "  recorded: %s %s, %s\n", day.Day, reviewSayVerdict(plan),
		plural(len(plan.Assertions), "assertion"))
	return nil
}

func reviewSayVerdict(plan dayview.Plan) string {
	if plan.Review.Verdict == adjustments.Unremembered {
		return "looked at and not remembered"
	}
	// A blind pass confirms NOTHING, because nothing was proposed to it. Saying
	// "confirmed" there would put the one word this whole mode exists to avoid
	// on the line that records what happened.
	if plan.Review.Saw() == adjustments.Observations {
		return "answered from its observations"
	}
	if len(plan.BlockReviews) > 0 {
		return "walked block by block"
	}
	return "confirmed"
}

// ask puts one day to the reviewer and turns the answer into a plan.
//
// A nil plan with ok is a skip, which records NOTHING. That is the fourth
// state, and it must stay reachable: a day nobody has opened and a day somebody
// opened and could not remember are different facts, and pressing s has to
// leave the first one alone rather than manufacture the second.
func (s *reviewSession) ask(day dayview.Day) (*dayview.Plan, bool, error) {
	forced := s.forced(day)
	keys := reviewDayKeys
	if forced {
		keys = reviewSampledKeys
		fmt.Fprint(s.out, "\n")
		wrap(s.out, "", "This day was sampled for a full walk, so that the days examined "+
			"closely are not only the days somebody already suspected.", s.cols)
	}
	prompt := reviewPrompt(keys, "", s.cols)
	var added []timespan.Span

	for {
		answer := dayview.Answer{
			Mode: dayview.Wholesale, Origin: s.origin, At: time.Now().UTC(), Note: s.note,
			Added: added,
		}
		key, ok := s.read(prompt)
		if !ok {
			return nil, false, nil
		}
		switch key {
		case "", "y":
			if forced {
				blocks, ok, err := s.walkBlocks(day)
				if err != nil || !ok {
					return nil, ok, err
				}
				answer.Mode, answer.Blocks = dayview.Drilled, blocks
			}
			plan, err := day.Confirm(answer)
			return &plan, true, err
		case "n":
			blocks, ok, err := s.walkBlocks(day)
			if err != nil || !ok {
				return nil, ok, err
			}
			answer.Mode, answer.Blocks = dayview.Drilled, blocks
			plan, err := day.Confirm(answer)
			return &plan, true, err
		case "u":
			plan := day.CannotRemember(answer)
			return &plan, true, nil
		case "s":
			return nil, true, nil
		case "a":
			span, ok, err := s.askSpan(day)
			if err != nil {
				fmt.Fprintf(s.out, "  %v\n", err)
				continue
			}
			if !ok {
				return nil, false, nil
			}
			added = append(added, span)
			fmt.Fprintf(s.out, "  added %s..%s as work\n",
				reviewClock(span.Start, s.cal), reviewClock(span.End, s.cal))
		case "q":
			return nil, false, nil
		default:
			fmt.Fprint(s.out, reviewDayHelp)
		}
	}
}

// forced says whether this day is sampled for a full walk regardless of the
// answer.
//
// The choice is a hash of the day's own name rather than its position in the
// queue, so it is stable across passes and independent of the order days happen
// to be offered in. A queue-position rule would sample the same slots every
// time and correlate with whatever ordering the queue used.
func (s *reviewSession) forced(day dayview.Day) bool {
	if s.drillEvery <= 0 || len(day.Blocks) == 0 {
		return false
	}
	var h uint32 = 2166136261
	for _, b := range []byte(day.Day.String()) {
		h = (h ^ uint32(b)) * 16777619
	}
	return int(h%uint32(s.drillEvery)) == 0
}

// walkBlocks puts each block to the reviewer in time order.
//
// Time order rather than client order, because the parameters being fitted —
// the gap, the lead-in and the tail-out — are all time-boundary parameters,
// and a block's project is an attribute of the block rather than a second pass
// over the day. The rows above the timeline are where a reader looks
// client-by-client; this is where they correct.
func (s *reviewSession) walkBlocks(day dayview.Day) ([]dayview.Judgement, bool, error) {
	out := make([]dayview.Judgement, 0, len(day.Blocks))
	for i, b := range day.Blocks {
		j := dayview.Judgement{Verdict: adjustments.Recalled, Engaged: true, Span: b.Span}
		for settled := false; !settled; {
			fmt.Fprintf(s.out, "\n  block %d of %d  %s..%s  %s  %s\n", i+1, len(day.Blocks),
				reviewClock(j.Span.Start, s.cal), reviewClock(j.Span.End, s.cal),
				reviewHours(j.Span.Duration()), reviewNamed(b.Projects))
			key, ok := s.read(reviewPrompt(reviewBlockKeys, "  ", s.cols))
			if !ok {
				return nil, false, nil
			}
			switch key {
			case "", "y":
				settled = true
			case "n":
				j.Engaged, settled = false, true
			case "u":
				j.Verdict, settled = adjustments.Unremembered, true
			case "q":
				return nil, false, nil
			case "s", "e":
				at, ok, err := s.askInstant(day, map[string]string{
					"s": "  it really started at? ", "e": "  it really ended at? ",
				}[key])
				if err != nil {
					fmt.Fprintf(s.out, "  %v\n", err)
					continue
				}
				if !ok {
					return nil, false, nil
				}
				if key == "s" {
					j.Span.Start = at
				} else {
					j.Span.End = at
				}
			case "p":
				name, ok := s.read("  it belonged to? (a project, or blank for none) ")
				if !ok {
					return nil, false, nil
				}
				j.Reattribute, j.Project = true, name
			default:
				fmt.Fprint(s.out, reviewBlockHelp)
			}
		}
		out = append(out, j)
	}
	return out, true, nil
}

// askSpan reads both ends of a stretch of work the model missed.
func (s *reviewSession) askSpan(day dayview.Day) (timespan.Span, bool, error) {
	start, ok, err := s.askInstant(day, "  work the model missed started at? ")
	if err != nil || !ok {
		return timespan.Span{}, ok, err
	}
	end, ok, err := s.askInstant(day, "  and ended at? ")
	if err != nil || !ok {
		return timespan.Span{}, ok, err
	}
	if !end.After(start) {
		return timespan.Span{}, true, errors.New("that span ends before it starts")
	}
	return timespan.Span{Start: start, End: end}, true, nil
}

// askInstant reads a wall clock and resolves it inside the day on the screen.
func (s *reviewSession) askInstant(day dayview.Day, prompt string) (time.Time, bool, error) {
	text, ok := s.read(prompt)
	if !ok {
		return time.Time{}, false, nil
	}
	at, err := reviewInstant(text, day, s.cal)
	return at, true, err
}

// reviewInstant reads a time of day inside one working day.
//
// A bare 09:30 is the ordinary case and has to be one word, because this is the
// only place a review costs more than a keystroke. It is resolved to the FIRST
// instant at or after the day begins whose wall clock reads that, which is what
// makes 02:00 on a day starting at 05:00 mean two in the morning at the END of
// that working day rather than a time twenty-seven hours earlier.
func reviewInstant(text string, day dayview.Day, cal sessions.Calendar) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return time.Time{}, errors.New("that is not a time; write 09:30, or a full instant")
	}
	if clock, err := time.Parse("15:04", text); err == nil {
		local := day.Start.In(cal.Location)
		for _, date := range []time.Time{local, local.AddDate(0, 0, 1)} {
			at := time.Date(date.Year(), date.Month(), date.Day(),
				clock.Hour(), clock.Minute(), 0, 0, cal.Location)
			if !at.Before(day.Start) && at.Before(day.End) {
				return at, nil
			}
		}
		return time.Time{}, fmt.Errorf("%s does not fall inside the working day %s", text, day.Day)
	}
	at, err := parseBoundary(text, "instant")
	if err != nil {
		return time.Time{}, fmt.Errorf("%q is not a time; write 09:30, or 2026-07-28T09:30:00Z", text)
	}
	return at, nil
}

// read prints a prompt and takes one line, lower-cased and trimmed.
//
// The empty line is the fast path's whole vocabulary. Enter confirms, which is
// what makes twenty days twenty keystrokes without this command having to put
// the terminal into raw mode — a platform-specific dependency, untestable, and
// worth about one keystroke a day against the several seconds of READING a day
// costs.
func (s *reviewSession) read(prompt string) (string, bool) {
	if s.done {
		return "", false
	}
	fmt.Fprint(s.out, prompt)
	line, err := s.in.ReadString('\n')
	if err != nil && line == "" {
		s.done = true
		return "", false
	}
	return strings.ToLower(strings.TrimSpace(line)), true
}

// THE PROMPT SAYS WHAT THE KEY WILL DO, AND IT SAYS IT AT THE MOMENT OF CHOICE.
//
// On a sampled day Enter WALKS THE BLOCKS. The prompt used to read "[Enter]
// right   [n] wrong, walk the blocks" on those days too, which is false twice
// over: Enter does not record the day as right, and the two keys it offers as
// opposites run the same code. The notice printed above the loop said so, once,
// and then scrolled away — a disclosure a reader has to remember is not a
// disclosure at the moment they act.
//
// So a sampled day has its own prompt, naming what Enter does and dropping the
// [n] it does not contrast with. n keeps working; it is simply no longer
// advertised as the other half of a choice that does not exist.
var (
	reviewDayKeys = []string{"[Enter] right", "[n] wrong, walk the blocks",
		"[u] cannot remember", "[a] add missed work", "[s] skip", "[q] quit"}

	reviewSampledKeys = []string{"[Enter] walk the blocks", "[u] cannot remember",
		"[a] add missed work", "[s] skip", "[q] quit"}

	reviewBlockKeys = []string{"[Enter] right", "[n] not work at all", "[s] start",
		"[e] end", "[p] project", "[u] cannot remember", "[q] quit"}
)

// reviewPrompt packs a prompt's keys into as few lines as the terminal holds.
//
// Clipping a prompt is not an option: the keys are the whole content of the
// line, and one clipped at eighty columns hides [s] and [q] altogether. It
// wraps instead, which costs a line only on a terminal too narrow to hold them
// all — and the old day prompt was 112 columns, so at eighty it was wrapping
// anyway, just not where anybody chose.
func reviewPrompt(keys []string, indent string, cols int) string {
	if cols <= 0 {
		cols = 80
	}
	// One column short of the terminal, and with room kept for the cursor. A
	// prompt is the one thing this command writes WITHOUT a trailing newline,
	// and clipTo holds the last rune of a full-width line back until it knows
	// whether anything follows it — so a prompt that filled the line exactly
	// would sit on the screen a character short until the reviewer typed. It is
	// also the line that must not be clipped at all: the keys are its whole
	// content, and one shortened at eighty columns hides [s] and [q].
	const cursor = "  > "
	room := cols - 1 - len([]rune(cursor))

	var out strings.Builder
	out.WriteString("\n")
	line := indent
	for _, key := range keys {
		switch {
		case line == indent:
			line += key
		case len([]rune(line))+3+len([]rune(key)) <= room:
			line += "   " + key
		default:
			out.WriteString(line + "\n")
			line = indent + key
		}
	}
	out.WriteString(line + cursor)
	return out.String()
}

const reviewDayHelp = `
  Enter  the day is right as drawn. Every block is recorded as work and every
         gap between them as not work, so the whole day is accounted for.
         On a day sampled for a full walk, Enter walks the blocks instead, and
         the prompt says so.
  n      walk the blocks one at a time and correct the ones that are wrong.
  u      you looked and cannot remember. Nothing is asserted either way and the
         machine's answer stands, marked unconfirmed. This is not a skip.
  a      add a stretch of work nothing observed, then answer for the day.
  s      skip. NOTHING is recorded, and the day stays never-examined — which is
         a different fact from having looked and not remembered.
  q      stop here. The day on the screen is not recorded.
`

const reviewBlockHelp = `
    Enter  this block is right.
    n      this was not work at all; the block goes away.
    s / e  the block really started or ended at some other time.
    p      it belonged to a different project, or to none.
    u      you cannot remember this block. It is asserted neither way, and the
           rest of the day is still accounted for around it.
    q      abandon this day without recording it.
`

// draw puts a whole working day on one screen.
func (s *reviewSession) draw(day dayview.Day, legend bool) {
	loc := s.cal.Location
	// The header is six figures in one line and was 82 columns on a real day.
	// Single spaces around its separators rather than the two it used to have,
	// which is where those two columns came from; and wrapped rather than
	// clipped, because the state at the end of it — agreed, corrected,
	// unremembered, never examined — is the thing this whole command exists to
	// set, and it is the first thing a clip would delete.
	fmt.Fprintln(s.out)
	wrap(s.out, "", fmt.Sprintf("%s %s · %s, %s · %s (%.0f%%) inferred · %s",
		day.Day.Weekday().String()[:3], day.Day, plural(len(day.Blocks), "sitting"),
		reviewHours(day.Engaged()), reviewHours(day.Margin()),
		100*reviewShare(day.Margin(), day.Engaged()), reviewSay(day.State())), s.cols)
	fmt.Fprintf(s.out, "gap %s  lead-in %s  tail-out %s  ·  derived %s\n",
		compactDuration(day.Params.Gap), compactDuration(day.Params.LeadIn),
		compactDuration(day.Params.TailOut),
		day.ComputedAt.In(loc).Format("2006-01-02 15:04"))
	if day.ClippedBlocks() > 0 {
		wrap(s.out, "", fmt.Sprintf("%s cross the day boundary; their inferred share is not "+
			"apportioned and is left out above.", plural(day.ClippedBlocks(), "sitting")), s.cols)
	}

	// The mark sits in a column of its own between the label and the timeline,
	// so that a weak row is legible without reading the label twice and without
	// the timeline losing its left edge. It costs the timeline two cells, which
	// reviewTimeline already keeps back.
	unlooked := day.Unlooked(time.Now().UTC())
	fmt.Fprintf(s.out, "\n%s%s\n", strings.Repeat(" ", reviewLabelWidth+3), reviewAxis(s.width, day, loc))
	fmt.Fprintf(s.out, "%-*s   %s\n", reviewLabelWidth, "sittings",
		reviewCells(s.width, day, day.Observed, day.Spans(), unlooked))
	var thin bool
	for _, r := range day.Rows {
		// THREE ANSWERS, and the third is unreachable on purpose rather than by
		// accident. A row exists only because an observation does, so there is
		// always something to weigh and Unknown should never arrive here —
		// internal/dayview's TestEveryRowOfARealDayIsWeighed fails if it does.
		// It is drawn anyway because the alternative is a blank, and a blank
		// reads as attested: a row that was never weighed would render as one
		// nothing is wrong with.
		mark := " "
		switch r.Strength {
		case dayview.Weak:
			mark, thin = "!", true
		case dayview.Unknown:
			mark = "?"
		}
		fmt.Fprintf(s.out, "%s %s %s\n", reviewLabel(r.Name()), mark,
			reviewCells(s.width, day, r.Observed, r.Active, unlooked))
	}
	// Three answers, and the middle one is the one this project keeps losing.
	// A day whose evidence nobody read has no rows AND no finding; saying "no
	// source that names a project saw any of this day" about it would be
	// reporting an absence as an observation.
	if len(day.Rows) == 0 && len(day.Blocks) > 0 {
		if day.Evidence {
			wrap(s.out, "", "no source that names a project saw any of this day; the sittings above "+
				"rest on evidence nothing can attribute", s.cols)
		} else {
			wrap(s.out, "", "this day's observations were not read, so there is nothing to say "+
				"about which projects it belonged to — which is not the same as nothing having "+
				"seen them", s.cols)
		}
	}
	if thin {
		fmt.Fprintln(s.out)
		wrap(s.out, "", "! every observation behind that row is a file's modification time. A file "+
			"changed and nothing recorded anybody changing it; a log-rotation cron writes those "+
			"too. It may well be a real evening spent editing one document — that is what this is "+
			"asking you.", s.cols)
	}

	if len(day.Blocks) == 0 {
		fmt.Fprintln(s.out)
		wrap(s.out, "", "the clustering found no sitting on this day. It ran; it found nothing. "+
			"If you worked, press a and say when.", s.cols)
	}
	s.drawBlocks(day)

	if len(day.Corrections) > 0 || day.Reviewed() {
		fmt.Fprintf(s.out, "already recorded here: %s and %s\n",
			plural(len(day.Reviews), "review"), plural(len(day.Corrections), "correction"))
	}
	s.sayWhatIsBehind(day, len(unlooked) > 0)
	if legend {
		// Not wrapped: the keys are separated by three spaces, which is what
		// tells them apart at a glance, and wrap would collapse them into one
		// run-on line. Two lines by hand rather than one clipped at eighty.
		fmt.Fprint(s.out, "\n  █ observed in the cell   ░ inferred by the clustering\n"+
			"  · nothing recorded       ? nothing has looked here yet\n")
	}
}

// sayWhatIsBehind reports how far anything has looked at this day, and names
// which of the three horizons is the binding one.
//
// It says WHICH because the three call for different actions — re-run
// sessionize, fix a failing source, come back tomorrow — and a warning that
// cannot tell them apart is one nobody can act on. It prints on every day
// rather than only on a stale one, because "the clustering last ran four
// minutes ago" is the reading that makes the absence of a warning meaningful.
func (s *reviewSession) sayWhatIsBehind(day dayview.Day, clipped bool) {
	now := time.Now().UTC()
	// The DAY's reach and not the session's. A blind pass drops the clustering
	// horizon because it draws no clustering, and reading the session's copy
	// here would print a warning about a model that is not on the screen.
	if said := day.Reach.Says(now); said != "" {
		fmt.Fprintln(s.out)
		wrap(s.out, "", said, s.cols)
	}
	if !clipped {
		return
	}
	horizon, bound := day.Horizon(now)
	wrap(s.out, "", fmt.Sprintf("NOTHING HAS LOOKED AT THIS DAY PAST %s (the %s), so the ? cells "+
		"are unasked rather than empty and nothing will be recorded over them. Confirming this "+
		"day accounts for it only as far as that.", reviewClock(horizon, s.cal), bound), s.cols)
}

// drawBlocks lists the day's sittings, one line each.
//
// The line is five measurements and two lists, and on the live corpus it ran to
// 126 columns. What gives when there is not room for all of it is decided here
// rather than left to clipTo, because only this function knows which of its
// columns is a figure: the clock, the length and the observation count never
// shorten, the project name shortens to fit, and the SOURCE LIST is the part
// that gives — and it counts what it dropped rather than trailing off.
//
// A block that crosses the day boundary says so ahead of its sources. That is a
// fact about the block; a source list is a convenience, and if only one of them
// fits it is not the fact that goes.
func (s *reviewSession) drawBlocks(day dayview.Day) {
	shown := min(len(day.Blocks), reviewMaxBlocks)
	// The widths this line has always used, which is what a pipe keeps. With a
	// terminal every fixed column shrinks to its own content and the gutters go
	// to one space, because all of that is lossless and the source list — the
	// only part with no natural width — wants every column of it.
	idx, hours, obs, project, gutter := 2, 7, 9, reviewProjectWidth, "  "
	if s.cols > 0 {
		gutter = " "
		idx, hours, obs, project = 1, 1, 1, 1
		for _, b := range day.Blocks[:shown] {
			idx = max(idx, len(fmt.Sprintf("%d", shown)))
			hours = max(hours, len([]rune(reviewHours(b.Duration()))))
			obs = max(obs, len([]rune(reviewObs(b.Events))))
			project = min(reviewProjectWidth,
				max(project, len([]rune(reviewNamed(b.Projects)))))
		}
	}

	for i, b := range day.Blocks {
		if i == reviewMaxBlocks {
			fmt.Fprintf(s.out, "  … and %d more, not shown\n", len(day.Blocks)-reviewMaxBlocks)
			break
		}
		clipped := ""
		if b.Clipped {
			clipped = gutter + "(continues outside this day)"
		}
		head := "  " + fmt.Sprintf("%*d", idx, i+1) + gutter +
			reviewClock(b.Span.Start, s.cal) + ".." + reviewClock(b.Span.End, s.cal) + gutter +
			fmt.Sprintf("%*s", hours, reviewHours(b.Duration())) + gutter +
			fmt.Sprintf("%*s", obs, reviewObs(b.Events)) + gutter +
			pad(reviewNamed(b.Projects), project) + gutter
		budget := 0
		if s.cols > 0 {
			budget = s.cols - len([]rune(head)) - len([]rune(clipped))
		}
		fmt.Fprintf(s.out, "%s%s%s\n", head, reviewSourcesWithin(b.Sources, budget), clipped)
	}
}

// reviewProjectWidth is how much room a block's project list gets before the
// terminal is consulted, and the most it ever gets.
const reviewProjectWidth = 22

// reviewTimeline is how many cells wide to draw the day when -width was not
// given: as many as the terminal has room for beside the row labels and the
// column the weak mark sits in.
//
// A pipe has no width and gets sixty, which is what this command has always
// drawn. Piped output may not depend on a window nobody is looking at.
func reviewTimeline(cols int) int {
	if cols <= 0 {
		return 60
	}
	return max(reviewMinTimeline, cols-reviewLabelWidth-3)
}

// reviewMinTimeline is the narrowest timeline -width will accept, and the floor
// a very narrow terminal is held to rather than drawn a day in four cells.
const reviewMinTimeline = 12

// reviewLabelWidth is how much room a row's name gets. Wide enough for the
// project names in the live registry and narrow enough to leave a day readable
// in eighty columns.
const reviewLabelWidth = 14

// reviewMaxBlocks is how many blocks are listed before the list says it is
// truncated. It says so out loud: a clipped listing nobody mentions is a
// failure mode this project has logged twice.
const reviewMaxBlocks = 16

// reviewLabel fits a row's name into the label column, so that the timeline
// underneath every row starts in the same place. pad, in format.go, is what
// reviewFit used to be here: it counts RUNES rather than bytes, because a
// project name is arbitrary text and slicing one mid-rune produces a column
// that is the right length in bytes and the wrong length on the screen.
func reviewLabel(name string) string { return pad(name, reviewLabelWidth) }

// reviewAxis is the hour ruler over a working day.
func reviewAxis(width int, day dayview.Day, loc *time.Location) string {
	line := []byte(strings.Repeat(" ", width+4))
	length := day.Length()
	for at := day.Start; at.Before(day.End); at = at.Add(2 * time.Hour) {
		pos := int(float64(at.Sub(day.Start)) / float64(length) * float64(width))
		if pos+2 <= width {
			copy(line[pos:], at.In(loc).Format("15"))
		}
	}
	return string(line[:width])
}

// reviewCells draws one row of the timeline.
//
// Three characters, and the middle one is the point of the whole exercise: a
// cell inside a sitting that nothing was observed in is INFERRED, and 75.7% of
// the engaged time in the live store is that. Drawing it the same as observed
// clock would hide the thing the reviewer is being asked to judge.
//
// A cell reads as observed if ANY observation falls in it, rather than if most
// of it was covered. Every attributable source in this corpus emits instants,
// which cover no duration at all, so a majority rule would render a day of
// solid work as entirely inferred.
// FOUR CHARACTERS, AND THE FOURTH IS THE ONE THIS PROJECT KEEPS LOSING. An hour
// every source reported quiet and an hour no source has been asked about are
// different facts, and drawing them the same is what let a reviewer agree that
// an empty morning was empty when it was the store that was empty. On
// 2026-08-01 that cost 1.4379h of assertion the derivation now contradicts.
func reviewCells(width int, day dayview.Day, observed, active, unlooked []timespan.Span) string {
	cells := make([]rune, width)
	length := day.Length()
	for i := range cells {
		cell := timespan.Span{
			Start: day.Start.Add(time.Duration(int64(length) * int64(i) / int64(width))),
			End:   day.Start.Add(time.Duration(int64(length) * int64(i+1) / int64(width))),
		}
		switch {
		case reviewTouches(observed, cell):
			cells[i] = '█'
		case reviewTouches(active, cell):
			cells[i] = '░'
		// Checked AFTER the two positives on purpose. A cell that straddles the
		// horizon holds real evidence on one side of it, and drawing it as
		// unlooked-at would hide an observation — which is the same error in the
		// opposite direction. The horizon is reported to the minute beside the
		// timeline; the cell says only that SOMETHING was seen here.
		case reviewTouches(unlooked, cell):
			cells[i] = '?'
		default:
			cells[i] = '·'
		}
	}
	return string(cells)
}

// reviewTouches reports whether any span reaches into a cell, counting a
// zero-length observation as touching the cell it falls in.
func reviewTouches(spans []timespan.Span, cell timespan.Span) bool {
	for _, s := range spans {
		if s.Start.Equal(s.End) {
			if !s.Start.Before(cell.Start) && s.Start.Before(cell.End) {
				return true
			}
			continue
		}
		if timespan.Overlaps(s, cell) && s.End.After(cell.Start) && s.Start.Before(cell.End) {
			return true
		}
	}
	return false
}

// reviewClock renders an instant in the zone the working day is local to.
func reviewClock(at time.Time, cal sessions.Calendar) string {
	return at.In(cal.Location).Format("15:04")
}

// reviewHours renders a duration the way a person reads one: 3h18m, or 42m.
func reviewHours(d time.Duration) string {
	d = d.Round(time.Minute)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// reviewObs counts observations. "obs" is already a plural, so plural() would
// make it "2 obss".
func reviewObs(n int) string { return fmt.Sprintf("%d obs", n) }

// reviewNamed lists the projects a block belongs to, leaving out the empty name
// that means "evidence nobody owns".
//
// Not because it is uninteresting — it is a row of the timeline above, which is
// where it is legible. It is left out HERE because it is nearly constant:
// biome-appusage carries no path at all, so on the live corpus almost every
// block holds some unattributed evidence, and printing that in every row costs
// the column the space that tells one day from another. A block with no named
// project says so, and does not print an empty column a reader would take for
// missing data.
func reviewNamed(projects []string) string {
	named := make([]string, 0, len(projects))
	for _, p := range projects {
		if p != "" {
			named = append(named, p)
		}
	}
	if len(named) == 0 {
		return "(nothing named)"
	}
	return strings.Join(named, ", ")
}

// reviewSome lists at most n items and SAYS how many it left out.
//
// A truncated list nobody mentions is a failure mode this project has logged
// twice: six of thirteen sources were once shown as though they were all of
// them. The blocks on a real day carry up to eight sources each and the
// untruncated line runs past two hundred columns.
func reviewSome(items []string, n int) string {
	if len(items) == 0 {
		return "(none)"
	}
	if len(items) <= n {
		return strings.Join(items, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(items[:n], ", "), len(items)-n)
}

// reviewSourcesWithin names as many sources as budget columns hold, and says
// how many it left out.
//
// It chooses n for reviewSome rather than clipping its result, so the count of
// what was dropped survives however narrow the terminal is. Where not even one
// name fits, a bare count of the sources is still a true statement; a line
// trailing off into an ellipsis is not, and this project has shipped that
// three times.
//
// A budget of zero is a pipe: at most three and the rest counted, which is what
// this line has always shown.
func reviewSourcesWithin(items []string, budget int) string {
	if len(items) == 0 {
		return "(none)"
	}
	if budget <= 0 {
		return reviewSome(items, 3)
	}
	for n := len(items); n > 0; n-- {
		if text := reviewSome(items, n); len([]rune(text)) <= budget {
			return text
		}
	}
	return clip(plural(len(items), "source"), budget)
}

func reviewShare(part, whole time.Duration) float64 {
	if whole == 0 {
		return 0
	}
	return part.Seconds() / whole.Seconds()
}

const reviewUsage = `Usage: timetattle review [flags]

Puts a whole working day on one screen and asks whether it is right.

Nothing downstream is believable until the clustering parameters are fitted, and
this is where the ground truth to fit them against comes from. 75.7% of the
engaged time in the live store is margin that unfitted parameters invented.

THE FAST PATH IS ONE KEYSTROKE. Press Enter and the day is confirmed. Detail
costs something only on the days that are actually wrong: press n and the day is
walked block by block, and each block can be trimmed, extended, deleted or
reattributed without leaving the keyboard.

CONFIRMING A DAY IS A COMPLETE ACCOUNTING OF IT, not a shrug. Every block is
recorded as work AND every gap between them as not work, so the day says
something about all twenty-four hours. That second half is what a gap threshold
can be fitted against; without it every parameter that lengthens a sitting would
fit at least as well as every parameter that shortens one.

FOUR STATES, AND THEY ARE FOUR ON PURPOSE. A day can be reviewed and agreed
with, reviewed and corrected, reviewed and NOT REMEMBERED, or never opened at
all. Pressing u records the third; pressing s records nothing whatever and
leaves the fourth. "I never looked" and "I looked and cannot remember" are
different facts, and this tool has collapsed that distinction three times.

A CONFIRMATION OF THE MODEL'S ANSWER CANNOT FIT THE MODEL. Over the fourteen
presence-covered reviewed days the fit residual at the shipped 15m/5m/5m is
exactly 0.000h and "timetattle fit" prints CIRCULAR, because a reviewer
confirming blocks he was SHOWN produces a strict subset of the derivation. So
-blind draws the OBSERVATIONS and no part of the model — no block, no sitting,
no margin, no parameter — and asks when work started and stopped, one silence at
a time. Aim it at pre-July days, where the parameters actually decide something.

THREE HORIZONS BOUND WHAT MAY BE ASSERTED, not one: the moment of review, the
last time the clustering ran, and the earliest source anything has been asked
of. The day view draws the stretch past the earliest of them as ? rather than ·
and says which one is binding. "Nothing has looked here yet" and "every source
reported quiet" are different facts, and confusing them cost 1.4379h of false
not-engaged on 2026-08-01.

CORRECTIONS ARE AN OVERLAY. Nothing here touches an event and nothing here
touches derived_session, which "timetattle sessionize" rebuilds wholesale on
every run. The difference between the machine's answer and yours is the model
error, and that difference is the point.

Non-interactive, for a caller that is not sitting at a terminal:

  timetattle review -list                      what is left to review, and in
                                               which of the four states
  timetattle review -date 2026-07-28 -print    draw one day and stop
  timetattle review -date 2026-07-28 -confirm  record it as right, no prompting
  timetattle review -date 2026-07-28 -unremembered

Flags:
`
