package main

import (
	"context"
	"fmt"
	"io"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// fitCommand searches the three clustering parameters against the human labels
// already in the store, and writes nothing.
//
// It is deliberately not `sessionize -fit`. sessionize REPLACES the stored
// derivation and this replaces nothing: it clusters a few hundred times in
// memory, scores each against the corrections, and prints what it found and —
// at greater length — what it did not find. Sharing an entry point with the
// command that writes derived_session would put a two-hundred-clustering search
// one typo away from overwriting the answer it was measuring.
//
// Every helper in this file is prefixed fit. cmd/timetattle is one Go package
// and two agents have already collided at package scope here; see the naming
// rule in main.go.
func fitCommand(args []string) error { return fitCommandTo(os.Stdout, args) }

func fitCommandTo(stdout io.Writer, args []string) error {
	return executeSubcommand(fitCmd(stdout), args)
}

func fitCmd(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "fit",
		Short: "Search the clustering parameters against the corrections",
		Long: `Searches the gap, lead-in and tail-out against the corrections already in the
store, and writes nothing at all.

WHAT THIS COMMAND IS FOR IS REFUSING TO OVERSTATE ITS ANSWER. Three things make
a naive search of these parameters return a confident number that means nothing,
and each is reported rather than smoothed over.

THE GAP IS INERT at or below lead-in plus tail-out, because each observation is
expanded by the margins before the gap is consulted. Every gap in that band
derives byte-identical sittings, so those points are not candidates and the
report says how many were set aside. -include-inert evaluates them anyway and
checks the rule against your corpus instead of trusting it.

THE OBJECTIVE IS FLAT over wide bands even above that floor, because it only
moves when a threshold crosses a silence that actually occurred. Where the best
score is attained by more than one point the report says so and gives the whole
plateau; it never picks one.

THE LABELS MAY AGREE WITH THE DERIVATION THEY CAME FROM. A reviewer confirming
blocks he was shown produces labels that are close to a restatement of the
model, and a fit against them returns the parameters that were on the screen
with an excellent residual. Name those parameters with -reference — by default
the configured ones — and the report says outright when they win.

A BLIND LABEL AND A CONFIRMING ONE ARE NOT WORTH THE SAME, so this does not
average them. Every run prints the census of both populations, present or
absent, and a pooled run prints where each population would have sent the fit on
its own. -shown fits one alone. Two populations that label the SAME time are
refused rather than pooled: each tiles the day by itself, so pooling counts that
day twice in the denominator and the result is not a residual over a partition.

Every residual is printed beside what a model claiming everything and a model
claiming nothing would score, and beside how far the objective moved across the
whole search. A small residual against labels the objective barely discriminates
between is not evidence of a good fit and the report says which it has.

Days are reported separately by EVIDENCE REGIME. Every interval-shaped source in
this store begins in July 2026; before that a sitting's whole extent is
manufactured by the margins, so a day of point observations cannot discriminate
between parameter values however many labels it carries. Pooling the two hides
that, so this does not pool them.

THIS IS SLOW AND IT IS MEANT TO BE. The default grid is 288 parameter sets, each
of which re-derives every sitting on the labelled days from scratch, and on a
corpus of a million observations that is tens of minutes. Narrow it with -gaps,
-lead-ins and -tail-outs while you are exploring; run the whole thing when you
mean to publish a figure.`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	configPath := fs.String("config", "", "configuration to read (default: ~/.timetattle/config.json)")
	gaps := fs.String("gaps", "11m,12m,15m,20m,25m,30m,45m,1h", "comma-separated gap thresholds to search")
	leadIns := fs.String("lead-ins", "0,1m,2m,5m,10m,15m", "comma-separated lead-ins to search")
	tailOuts := fs.String("tail-outs", "0,30s,1m,2m,5m,10m", "comma-separated tail-outs to search")
	tolerance := fs.Duration("tolerance", 0,
		"how much worse than the best a point may score and still be inside the reported interval")
	includeInert := fs.Bool("include-inert", false,
		"evaluate the points whose gap is inert too, and check each against the top of its band")
	reference := fs.String("reference", "",
		"the parameters the reviewer was shown, as gap/lead-in/tail-out; \"none\" to omit the circularity check "+
			"(default: the configured parameters)")
	population := fs.String("shown", "all",
		"which labels to fit against: \"all\", \"derivation\" (a reviewer confirming blocks he was "+
			"shown) or \"observations\", spelt \"blind\", (a reviewer shown where evidence was seen)")
	skipDays := fs.String("skip-days", "2026-08-01", "comma-separated reviewed days to leave out")
	minReview := fs.Duration("min-review", 20*time.Hour,
		"how long a review has to be to count as a whole working day")
	coverageFloor := fs.Float64("coverage-floor", 0.01,
		"a day whose asserted work is covered by duration-bearing observations below this share "+
			"is reported as point-only")
	narrow := fs.Bool("narrow", true,
		"read only the events that can affect the labelled days; -narrow=false searches the whole corpus")
	exclude := fs.String("exclude", "", excludeFlagUsage)
	cmd.RunE = func(*cobra.Command, []string) error {
		set := flagsSet(fs)

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
		cal, err := cfg.Calendar.Sessions()
		if err != nil {
			return err
		}
		excludes, err := sessionExcludesFrom(cfg, *exclude, set["exclude"])
		if err != nil {
			return err
		}
		want, err := fitShown(*population)
		if err != nil {
			return err
		}

		grid := fit.Grid{}
		if grid.Gaps, err = fitDurations(*gaps, "gaps"); err != nil {
			return err
		}
		if grid.LeadIns, err = fitDurations(*leadIns, "lead-ins"); err != nil {
			return err
		}
		if grid.TailOuts, err = fitDurations(*tailOuts, "tail-outs"); err != nil {
			return err
		}
		if err := grid.Validate(); err != nil {
			return err
		}

		base := sittings.Params{
			Gap: cfg.Sessions.Gap.Duration(), LeadIn: cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources, IgnoreSources: cfg.Sessions.IgnoreSources,
		}
		shown := &fit.Point{Gap: base.Gap, LeadIn: base.LeadIn, TailOut: base.TailOut}
		switch {
		case *reference == "none":
			shown = nil
		case *reference != "":
			if shown, err = fitPoint(*reference); err != nil {
				return err
			}
		}

		// The store is READ. Opening read-only is what makes "writes nothing" a
		// property of the handle rather than of every branch below being right.
		st, err := store.OpenReadOnly(*storePath)
		if err != nil {
			return err
		}
		defer st.Close()
		ctx := context.Background()

		truth, ground, err := fitTruth(ctx, st, cal, *minReview, splitCommaList(*skipDays), want)
		if err != nil {
			return err
		}

		events, err := st.Events(ctx, store.Filter{})
		if err != nil {
			return err
		}
		events, drop := sessions.Retain(events, excludes)

		// Coverage is measured on the WHOLE corpus, before narrowing, because it is
		// a statement about what was observed rather than about what this run read.
		coverage := truth.Coverage(events, base)
		for i := range truth.Labels {
			truth.Labels[i].Regime = fitRegime(coverage[truth.Labels[i].Day], *coverageFloor)
		}

		var narrowing fit.Narrowing
		if *narrow {
			events, narrowing = fit.Narrow(events, truth.Span(), grid.Reach(), base)
		}

		res, err := fit.Search{
			Grid: grid, Objective: truth, Base: base,
			Tolerance: *tolerance, IncludeInert: *includeInert, Reference: shown,
		}.Run(events)
		if err != nil {
			return err
		}

		// The width is read from the ORIGINAL writer. clipTo wraps it, and a wrapped
		// writer is not a terminal, so asking the wrapper would report zero and every
		// table below would lay itself out for a pipe on a narrow screen.
		width := columnsOf(stdout)
		out := clipTo(stdout, width)
		fitReport(out, *storePath, res, ground, coverage, *coverageFloor, truth.Regimes(), base, excludes, drop, narrowing, *narrow, width)
		return nil
	}
	return cmd
}

// fitGround is what the ground truth turned out to be, kept beside the Truth
// itself so the report can say what was left out as well as what was used.
type fitGround struct {
	Days []string
	// Skipped is every day -skip-days matched, and Unmatched every day it named
	// that no review holds. A skip that matched nothing has to be reported too:
	// a run told to leave a day out and silently leaving nothing out is a
	// different corpus from the one the reader thinks was searched.
	Skipped    []string
	Unmatched  []string
	Reviews    int
	Assertions int
	// Engaged and NotEngaged are what the reviewer asserted, per day.
	Engaged, NotEngaged map[string]time.Duration

	// Census is every population of labels this run could have drawn on,
	// INCLUDING the ones holding nothing, taken before -shown narrowed it.
	//
	// It is the answer to a question the residual cannot answer: a fit
	// restricted to a population nobody recorded scores 0.000h over zero
	// labels, which is indistinguishable from a perfect fit. The counts are what
	// tell "nobody reviewed blind" apart from "nobody reviewed".
	Census []fit.Population
	// Only is the population -shown selected, and nil for a pooled run.
	Only *adjustments.Shown
}

// Fitted is the census entries this run actually scored against.
func (g fitGround) Fitted() []fit.Population {
	if g.Only == nil {
		return g.Census
	}
	for _, p := range g.Census {
		if p.Shown == *g.Only {
			return []fit.Population{p}
		}
	}
	return nil
}

// fitShown reads -shown. It returns nil for a pooled run, which is the one
// value that is a decision rather than a selection and so is spelt out.
func fitShown(text string) (*adjustments.Shown, error) {
	switch text {
	case "all", "":
		return nil, nil
	case "blind", "observations":
		s := adjustments.Observations
		return &s, nil
	case "confirming", "derivation":
		s := adjustments.Derivation
		return &s, nil
	}
	return nil, fmt.Errorf("-shown %q names no population; write all, derivation or blind", text)
}

// fitTruth reads the corrections out of the store and turns them into labels.
//
// A DAY-LEVEL review is one whose span reaches minReview, which is how a
// wholesale confirm of a working day is told apart from a confirmation of one
// block inside it. Only assertions parented to a day-level review are used:
// those tile their whole day, so the comparison is two-directional — a span the
// reviewer called work and the model does not claim, and a span he called not
// work and the model does. Assertions hanging off a block review would give the
// first direction only, and a fitter that could only be punished for claiming
// too little would always prefer to claim more.
// WHAT THE REVIEWER WAS LOOKING AT travels with every label, because a
// confirmation of a derivation and a blind reading of the observations are
// worth different amounts and a fitter that averaged them would report a
// precision it has not got. want narrows to one population, and nil pools —
// which is refused outright where the two populations label the same time.
func fitTruth(ctx context.Context, st *store.Store, cal sessions.Calendar,
	minReview time.Duration, skip []string, want *adjustments.Shown) (fit.Truth, fitGround, error) {
	var zero fit.Truth
	reviews, err := st.Reviews(ctx, time.Time{}, time.Time{})
	if err != nil {
		return zero, fitGround{}, err
	}
	adjs, err := st.Adjustments(ctx, time.Time{}, time.Time{})
	if err != nil {
		return zero, fitGround{}, err
	}

	dayOf, sawOf := map[int64]string{}, map[int64]adjustments.Shown{}
	for _, r := range reviews {
		if r.Verdict != adjustments.Recalled || r.End.Sub(r.Start) < minReview {
			continue
		}
		dayOf[r.ID], sawOf[r.ID] = cal.DayOf(r.Start).String(), r.Saw()
	}
	if len(dayOf) == 0 {
		return zero, fitGround{}, fmt.Errorf("%s holds no whole-day review at all "+
			"(no recalled review spanning %v or more), so there is nothing to fit against; "+
			"run `timetattle review` first", st.Path(), minReview)
	}

	ground := fitGround{
		Reviews:    len(dayOf),
		Engaged:    map[string]time.Duration{},
		NotEngaged: map[string]time.Duration{},
		Only:       want,
	}
	skipped := map[string]bool{}
	for _, d := range skip {
		skipped[d] = true
	}

	truth := fit.Truth{}
	dropped := map[string]bool{}
	for _, a := range adjs {
		day, ok := dayOf[a.ReviewID]
		if !ok {
			continue
		}
		if skipped[day] {
			dropped[day] = true
			continue
		}
		// Engagement is the only assertion a clustering can be scored against.
		// An attribution says which project an hour belongs to, which is a
		// question these parameters do not answer and must not be fitted to.
		if a.Assertion != adjustments.Engaged && a.Assertion != adjustments.NotEngaged {
			continue
		}
		truth.Labels = append(truth.Labels, fit.Label{
			Day:     day,
			Shown:   sawOf[a.ReviewID],
			Span:    timespan.Span{Start: a.Start, End: a.End},
			Engaged: a.Assertion == adjustments.Engaged,
		})
	}
	ground.Skipped = slices.Sorted(maps.Keys(dropped))
	for _, d := range skip {
		if !dropped[d] {
			ground.Unmatched = append(ground.Unmatched, d)
		}
	}
	slices.Sort(ground.Unmatched)

	if err := truth.Validate(); err != nil {
		return zero, ground, fmt.Errorf("the corrections in %s cannot be fitted against: %w", st.Path(), err)
	}

	// THE CENSUS IS TAKEN BEFORE THE NARROWING, so a run that fitted nothing
	// can still say what it declined to fit and what was never there at all.
	ground.Census = truth.Census()
	switch {
	case want != nil:
		only, err := truth.Only(*want)
		if err != nil {
			return zero, ground, fmt.Errorf("-shown %s cannot be fitted against %s: %w",
				fit.Describe(*want), st.Path(), err)
		}
		truth = only
	default:
		// POOLING TWO LABELLINGS OF ONE DAY IS NOT A RESIDUAL OVER A PARTITION.
		// Each population tiles that day by itself, so together they cover it
		// twice: the denominator double-counts, and where the two disagree the
		// pooled figure is decided by nothing but their relative sizes. This is
		// the one case the command refuses rather than reports, because there is
		// no wording that would make the number mean something.
		if both := truth.Contested(); len(both) > 0 {
			return zero, ground, fmt.Errorf("the labels in %s cannot be pooled: %s labelled both "+
				"blind and confirming a derivation, and each labelling tiles the whole day, so a "+
				"pooled residual would count that time twice in its own denominator. Fit one "+
				"population at a time with -shown derivation or -shown blind",
				st.Path(), plural(len(both), "day")+" ("+strings.Join(both, ", ")+")")
		}
	}

	used := map[string]bool{}
	for _, l := range truth.Labels {
		used[l.Day] = true
		ground.Assertions++
		if l.Engaged {
			ground.Engaged[l.Day] += l.Span.Duration()
			continue
		}
		ground.NotEngaged[l.Day] += l.Span.Duration()
	}
	ground.Days = slices.Sorted(maps.Keys(used))
	return truth, ground, nil
}

// fitRegime names which evidence regime a day is in, from how much of the time
// the reviewer called work any duration-bearing observation actually covered.
//
// A date cut would be the obvious alternative and is a proxy for this. Coverage
// measures the quantity itself and needs no boundary to be agreed on, so a day
// that happens to hold an interval source early, or to hold none late, lands
// where it belongs rather than where its date says.
func fitRegime(coverage, floor float64) string {
	if coverage < floor {
		return "point-only"
	}
	return "presence-covered"
}

func fitDurations(list, name string) ([]time.Duration, error) {
	var out []time.Duration
	for _, text := range splitCommaList(list) {
		if text == "0" {
			out = append(out, 0)
			continue
		}
		d, err := time.ParseDuration(text)
		if err != nil {
			return nil, fmt.Errorf("-%s %q: %w", name, text, err)
		}
		out = append(out, d)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-%s names nothing to search", name)
	}
	return out, nil
}

// fitPoint reads a parameter set written as gap/lead-in/tail-out.
func fitPoint(text string) (*fit.Point, error) {
	parts := strings.Split(text, "/")
	if len(parts) != 3 {
		return nil, fmt.Errorf("-reference %q is not gap/lead-in/tail-out; write 15m/5m/5m", text)
	}
	var p fit.Point
	for i, into := range []*time.Duration{&p.Gap, &p.LeadIn, &p.TailOut} {
		if parts[i] == "0" {
			continue
		}
		d, err := time.ParseDuration(parts[i])
		if err != nil {
			return nil, fmt.Errorf("-reference %q: %w", text, err)
		}
		*into = d
	}
	return &p, nil
}

// fitReport prints what the search found and, at greater length, what it did
// not.
func fitReport(out io.Writer, path string, res fit.Result, ground fitGround,
	coverage map[string]float64, floor float64, regimeDays map[string]int, base sittings.Params,
	excludes sessions.Excludes, drop sessions.Drop, narrowing fit.Narrowing, narrowed bool, width int) {

	fmt.Fprintln(out, path)
	fmt.Fprintf(out, "objective: %s\n", res.Objective)
	fmt.Fprintf(out, "presence:  %s\n", listOrNone(base.PresenceSources))
	fmt.Fprintf(out, "ignored:   %s\n", listOrNone(base.IgnoreSources))
	reportExclusions(out, excludes, drop)
	if narrowed {
		fmt.Fprintf(out, "narrowed to the labelled days: %s kept of %d read, %d dropped, "+
			"closure taken at %s across %d chains of which %d could reach a label\n",
			plural(narrowing.Kept, "observation"), narrowing.Read, narrowing.Dropped,
			fit.Compact(narrowing.Reach), narrowing.Runs, narrowing.RunsKept)
	} else {
		fmt.Fprintln(out, "the whole corpus was searched; nothing was narrowed away")
	}

	fitReportGround(out, ground, coverage, floor, width)
	fitReportPopulations(out, ground, width)
	fitReportSearch(out, res, width)
	fitReportMinimum(out, res, width)
	fitReportProfiles(out, res, width)
	fitReportScale(out, res, width)
	fitReportBreakdown(out, res, regimeDays, width)
	fitReportSplits(out, res, width)
	fitReportCircularity(out, res, width)
}

// fitReportPopulations is the census, and it is printed on every run whether or
// not there is anything interesting in it.
//
// EVERY POPULATION APPEARS, INCLUDING AN EMPTY ONE. There are no blind labels
// in the live store at all, and a report that listed only what it found would
// print "confirming a derivation: 70" and leave a reader to infer that the
// other population is empty rather than merely uninteresting. "Nothing was
// recorded blind" is an observation and has to be printed as one.
func fitReportPopulations(out io.Writer, ground fitGround, width int) {
	fmt.Fprintf(out, "\nWHAT THE REVIEWER WAS SHOWN\n")

	var total time.Duration
	for _, p := range ground.Census {
		total += p.Asserted()
	}
	rows := make([][]string, 0, len(ground.Census))
	for _, p := range ground.Census {
		share := "n/a"
		if total > 0 {
			share = fmt.Sprintf("%.1f%%", 100*p.Asserted().Seconds()/total.Seconds())
		}
		// "yes" against a population holding nothing would read as "these were
		// fitted", which is the collapse this whole section exists to prevent.
		fitted := "yes"
		switch {
		case p.Empty():
			fitted = "none recorded"
		case ground.Only != nil && *ground.Only != p.Shown:
			fitted = "no (-shown)"
		}
		rows = append(rows, []string{fit.Describe(p.Shown),
			fmt.Sprintf("%d", p.Labels), fmt.Sprintf("%d", p.Days),
			fmt.Sprintf("%.3fh", p.Engaged.Hours()),
			fmt.Sprintf("%.3fh", p.NotEngaged.Hours()),
			share, fitted,
		})
	}
	writeColumns(out, table{
		head:    []string{"population", "assertions", "days", "engaged", "not-engaged", "of the labelled time", "fitted"},
		body:    rows,
		natural: []int{24, 11, 6, 10, 12, 21, 12},
		right:   []bool{false, true, true, true, true, true, false},
		floor:   10, cols: width,
	})

	fitted := ground.Fitted()
	if len(fitted) == 1 && ground.Only != nil {
		wrap(out, "", fmt.Sprintf("This run fitted the %s labels ALONE. Every figure below is about "+
			"that population and about no other, which is what -shown is for.",
			fit.Describe(*ground.Only)), width)
		return
	}

	var present, blind int
	for _, p := range ground.Census {
		if p.Empty() {
			continue
		}
		present++
		if p.Shown == adjustments.Observations {
			blind = p.Labels
		}
	}
	if blind == 0 {
		wrap(out, "", "no label in this fit was produced blind. Every assertion below is a reviewer "+
			"agreeing or disagreeing with blocks the model drew for him, so his engaged set is close "+
			"to a restatement of the derivation and this search cannot separate the model from his "+
			"agreement with it. That is a fact about the ground truth and no amount of further days "+
			"of the same kind changes it; run `timetattle review -blind` to produce labels that can.",
			width)
		return
	}
	if present > 1 {
		wrap(out, "", "TWO POPULATIONS ARE POOLED BELOW and they are not worth the same. A blind "+
			"label is a statement about the world; a confirming label is an endorsement of the "+
			"parameters that drew it. The shares above are the weights the pooled residual gives "+
			"them, and the section headed EACH POPULATION ON ITS OWN says where each would have "+
			"sent the fit by itself.", width)
	}
}

// fitReportSplits is where each population would have sent the fit if it had
// been the only one.
//
// A pooled minimum that both populations also attain is a real agreement. A
// pooled minimum only the larger one attains is a HEADCOUNT printed in the
// format of a fitted value, and nothing in a pooled residual distinguishes the
// two. This section is the distinguishing.
func fitReportSplits(out io.Writer, res fit.Result, width int) {
	if !res.Pooled() {
		return
	}
	fmt.Fprintf(out, "\nEACH POPULATION ON ITS OWN\n")
	rows := make([][]string, 0, len(res.Splits))
	for _, s := range res.Splits {
		attained := s.Best[0].String()
		if len(s.Best) > 1 {
			attained = fmt.Sprintf("%s, and %d more", s.Best[0], len(s.Best)-1)
		}
		rows = append(rows, []string{fit.Describe(adjustments.Shown(s.Shown)),
			fmt.Sprintf("%.3fh", s.Min.Total().Hours()),
			fmt.Sprintf("%.1fh", s.Min.Scale().Hours()),
			fmt.Sprintf("%.3fh", (s.Max.Total() - s.Min.Total()).Hours()),
			attained,
		})
	}
	writeColumns(out, table{
		head:    []string{"population", "best residual", "labelled", "moved", "attained at"},
		body:    rows,
		natural: []int{24, 14, 10, 9, 34},
		right:   []bool{false, true, true, true, false},
		floor:   10, cols: width,
	})

	if common := res.CommonBest(); len(common) > 0 {
		wrap(out, "", fmt.Sprintf("%s is best for EVERY population, so the pooled minimum above is "+
			"an agreement between them rather than an artefact of their sizes: %v",
			plural(len(common), "point"), common[0]), width)
		return
	}
	wrap(out, "", "NO POINT IS BEST FOR BOTH POPULATIONS. The pooled minimum reported above is "+
		"therefore the larger population's answer carried by weight, and not something the labels "+
		"agree on. Read the two rows above instead of the pooled figure, and prefer the blind row: "+
		"it is the only one formed without the parameters on the screen.", width)
}

func fitReportGround(out io.Writer, ground fitGround, coverage map[string]float64, floor float64, width int) {
	fmt.Fprintf(out, "\nGROUND TRUTH\n")
	fmt.Fprintf(out, "%s over %s, from %s\n",
		plural(ground.Assertions, "assertion"), plural(len(ground.Days), "reviewed day"),
		plural(ground.Reviews, "whole-day review"))
	if len(ground.Skipped) > 0 {
		wrap(out, "", fmt.Sprintf("Left out by -skip-days: %s. A day left out is not a day that "+
			"disagreed; it is a day nothing was asked of.", strings.Join(ground.Skipped, ", ")), width)
	}
	if len(ground.Unmatched) > 0 {
		wrap(out, "", fmt.Sprintf("-skip-days also named %s, which matched no reviewed day in this "+
			"store. Nothing was left out on their account.", strings.Join(ground.Unmatched, ", ")), width)
	}

	rows := make([][]string, 0, len(ground.Days))
	for _, d := range ground.Days {
		engaged, notEngaged := ground.Engaged[d], ground.NotEngaged[d]
		rows = append(rows, []string{d,
			fmt.Sprintf("%.1f%%", 100*coverage[d]),
			fitRegime(coverage[d], floor),
			fmt.Sprintf("%.3fh", engaged.Hours()),
			fmt.Sprintf("%.3fh", notEngaged.Hours()),
		})
	}
	writeColumns(out, table{
		head:    []string{"day", "coverage", "regime", "engaged", "not-engaged"},
		body:    rows,
		natural: []int{12, 9, 17, 10, 12},
		right:   []bool{false, true, false, true, true},
		floor:   10, cols: width,
	})

	counts := map[string]int{}
	for _, d := range ground.Days {
		counts[fitRegime(coverage[d], floor)]++
	}
	if n := counts["point-only"]; n > 0 {
		fmt.Fprintf(out, "point-only days: %d of %d\n", n, len(ground.Days))
		wrap(out, "", "On a point-only day no observation with a duration covers the work asserted, "+
			"so the whole derived extent is manufactured by lead-in, tail-out and the gap. That is the "+
			"regime the parameters matter most in and the one the ground truth covers least, and a "+
			"pooled figure over both regimes is dominated by days that cannot discriminate at all.",
			width)
	}
}

func fitReportSearch(out io.Writer, res fit.Result, width int) {
	total := len(res.Cells) + len(res.InertSkipped)
	fmt.Fprintf(out, "\nTHE SEARCH\n")
	fmt.Fprintf(out, "gaps:      %s\n", fitList(res.Grid.Gaps))
	fmt.Fprintf(out, "lead-ins:  %s\n", fitList(res.Grid.LeadIns))
	fmt.Fprintf(out, "tail-outs: %s\n", fitList(res.Grid.TailOuts))
	if len(res.InertSkipped) > 0 {
		wrap(out, "", fmt.Sprintf("%d of %d points were not evaluated: their gap is inert, at or below "+
			"lead-in plus tail-out, so each derives exactly what the top of its band derives and is not a "+
			"distinct model. Run with -include-inert to evaluate them anyway and check that on this corpus.",
			len(res.InertSkipped), total), width)
	}
	if res.InertVerified > 0 {
		fmt.Fprintf(out, "%d inert points were evaluated and every one derived exactly what "+
			"the top of its band derived\n", res.InertVerified)
	}
	if len(res.InertViolations) > 0 {
		wrap(out, "", fmt.Sprintf("WARNING: %s did NOT derive what the top of its band derived, "+
			"which falsifies the inertness rule on this corpus and means the points set aside "+
			"elsewhere in this report were set aside wrongly: %s",
			plural(len(res.InertViolations), "inert point"), fitPoints(res.InertViolations)), width)
	}
	fmt.Fprintf(out, "%s evaluated\n", plural(len(res.Cells), "point"))
}

func fitReportMinimum(out io.Writer, res fit.Result, width int) {
	fmt.Fprintf(out, "\nTHE MINIMUM\n")
	fmt.Fprintf(out, "residual %.3fh of %.1fh labelled (%.2f%%): %.3fh orphaned, %.3fh contradicted\n",
		res.Min.Total().Hours(), res.Min.Scale().Hours(), 100*res.Min.Share(),
		res.Min.Orphaned.Hours(), res.Min.Contradicted.Hours())
	wrap(out, "  ", "orphaned is time the reviewer called work and the model does not claim; "+
		"contradicted is time he called not-work and the model claims.", width)

	if res.UniqueMinimum() {
		fmt.Fprintf(out, "attained by one point: %v\n", res.Best[0])
	} else {
		wrap(out, "", fmt.Sprintf("the minimum is a plateau, attained by %s. "+
			"This report does not name one of them, because the data does not.",
			plural(len(res.Best), "point")), width)
		for i, p := range res.Best {
			if i == fitListed {
				fmt.Fprintf(out, "  ... and %d more, not listed; the interval below covers them all\n",
					len(res.Best)-fitListed)
				break
			}
			fmt.Fprintf(out, "  %v\n", p)
		}
	}

	for _, b := range res.Flat {
		if !b.Identical {
			continue
		}
		wrap(out, "", fmt.Sprintf("%s derived an IDENTICAL set of sittings, scoring %.3fh: %s. "+
			"No objective whatever could separate these; they are one model under several names.",
			plural(len(b.Points), "point"), b.Score.Hours(), fitPoints(b.Points)), width)
	}
}

func fitReportProfiles(out io.Writer, res fit.Result, width int) {
	fmt.Fprintf(out, "\nEACH PARAMETER, WITH THE OTHER TWO FREE\n")
	for _, p := range res.Profiles {
		rows := make([][]string, 0, len(p.Values))
		for i, v := range p.Values {
			rows = append(rows, []string{fit.Compact(v), fmt.Sprintf("%.3fh", p.Best[i].Hours())})
		}
		fmt.Fprintf(out, "\n%s: %s\n", p.Axis, fitIntervalLine(p))
		writeColumns(out, table{
			indent: "  ", head: []string{string(p.Axis), "best residual"},
			body: rows, natural: []int{10, 14}, right: []bool{false, true}, floor: 6,
			cols: width,
		})
		switch p.Trend {
		case fit.TrendFalling, fit.TrendRising:
			fmt.Fprintf(out, "  NO MINIMUM: the residual is still %s at the edge of the searched "+
				"range, so the best value here is where the search stopped and is not a fitted one.\n", p.Trend)
		case fit.TrendFlat:
			fmt.Fprintf(out, "  FLAT: this parameter changed nothing at all across the whole search.\n")
		}
		if note := fitAxisCaveat(p.Axis); note != "" {
			wrap(out, "  ", note, width)
		}
		if len(p.AcceptedElsewhere) > 0 {
			fmt.Fprintf(out, "  also within tolerance, outside that run: %s — the objective is "+
				"multi-modal along this axis and the interval is not the whole of it\n",
				fitList(p.AcceptedElsewhere))
		}
	}
}

// fitIntervalLine renders an interval with the evidence at each end, and says
// plainly when an end is the edge of the search rather than a bound.
func fitIntervalLine(p fit.Profile) string {
	span := fmt.Sprintf("%s..%s", fit.Compact(p.Interval.Low), fit.Compact(p.Interval.High))
	if p.Interval.Low == p.Interval.High {
		span = fit.Compact(p.Interval.Low)
	}
	low := "unbounded below (the search stopped there)"
	if !p.Interval.LowBound.AtSearchEdge {
		low = fmt.Sprintf("below it %s costs %.3fh",
			fit.Compact(p.Interval.LowBound.Next), p.Interval.LowBound.Penalty.Hours())
	}
	high := "unbounded above (the search stopped there)"
	if !p.Interval.HighBound.AtSearchEdge {
		high = fmt.Sprintf("above it %s costs %.3fh",
			fit.Compact(p.Interval.HighBound.Next), p.Interval.HighBound.Penalty.Hours())
	}
	return fmt.Sprintf("%s; %s; %s", span, low, high)
}

// fitAxisCaveat is what a fitted value for one parameter cannot mean, measured
// elsewhere and stated here because this report is where somebody will read the
// number and act on it.
//
// The figures come from project/private/2026-08-02-which-claims-are-ground-truth.md,
// reproduced with `go run ./scripts/claimset`.
func fitAxisCaveat(a fit.Axis) string {
	switch a {
	case fit.LeadInAxis:
		return "A single lead-in is an average over a mix that is not stationary. Measured against " +
			"235 declared work units, the lead is 4m37s over all artifact sources and 7m57s against " +
			"commits alone — a commit is written later in a stretch of work than a file save, so the " +
			"right lead-in depends on which stream a day is made of, and the mix changes month to month."
	case fit.TailOutAxis:
		return "The tail is the most robust figure in the corpus and does not move under any cut: 38s " +
			"over all artifact sources, 41s against commits alone, 39s post-July, 24s pre-July. An " +
			"interval here that excludes those values is a disagreement worth explaining."
	default:
		return "Two independent methods bound the gap at roughly 10-20 minutes: 96.4% of declared-" +
			"continuous work units fit inside 15m, with the largest silence inside one at 18m, which " +
			"bounds it from BELOW and cannot bound it from above at all; and screen-on fraction by " +
			"gap length falls off through the 10-20m band. Neither is a fit, and the middle of the " +
			"second is 62 observations of noise."
	}
}

func fitReportScale(out io.Writer, res fit.Result, width int) {
	fmt.Fprintf(out, "\nWHAT THE RESIDUAL IS WORTH\n")
	fmt.Fprintf(out, "a model claiming everything scores %.3fh; a model claiming nothing scores %.3fh\n",
		res.NullAllEngaged.Total().Hours(), res.NullNoneEngaged.Total().Hours())
	fmt.Fprintf(out, "the objective moved %.3fh across the whole live search, %.1f%% of the larger null\n",
		res.Span().Hours(), 100*res.Discrimination())
	if res.Discrimination() < 0.05 {
		wrap(out, "", fmt.Sprintf("READ THIS BEFORE QUOTING THE RESIDUAL. The objective separates the "+
			"candidates by %.3fh, which is %.1f%% of the error a model with no skill at all would make. "+
			"A small residual here is a statement about how much the labels and the derivation already "+
			"agree, and not about how good the parameters are. An interval from this search is worth "+
			"reporting; a point estimate from it is not.",
			res.Span().Hours(), 100*res.Discrimination()), width)
	}
}

func fitReportBreakdown(out io.Writer, res fit.Result, regimeDays map[string]int, width int) {
	if len(res.ByRegime) > 0 {
		fmt.Fprintf(out, "\nAT %v\n", res.Best[0])
		if !res.UniqueMinimum() {
			wrap(out, "", fmt.Sprintf("which is the FIRST of %d tied points, chosen by grid order and "+
				"nothing else. Where the tie is between points that derived identical sittings this is "+
				"immaterial; where it is not, the two tables below are one member of a plateau rather "+
				"than the answer.", len(res.Best)), width)
		}
		if len(res.ByShown) > 1 {
			fmt.Fprintf(out, "\nby what the reviewer was shown\n")
			shown := map[string]fit.Residual{}
			for k, r := range res.ByShown {
				shown[fit.Describe(adjustments.Shown(k))] = r
			}
			fitResidualTable(out, shown, "population", width)
		}
		fmt.Fprintf(out, "\nby evidence regime\n")
		fitResidualTable(out, res.ByRegime, "regime", width)
		for _, regime := range slices.Sorted(maps.Keys(res.ByRegime)) {
			if n := regimeDays[regime]; n < 3 {
				wrap(out, "", fmt.Sprintf("The %s regime is represented by %s. That is too few to fit "+
					"anything from: the interval above rests on the other regime, and any figure quoted "+
					"for this one is a statement about %s and not about the parameters.",
					regime, plural(n, "day"), plural(n, "day")), width)
			}
		}
	}
	if len(res.ByDay) > 0 {
		fmt.Fprintf(out, "\nby day\n")
		fitResidualTable(out, res.ByDay, "day", width)
	}
}

func fitResidualTable(out io.Writer, byKey map[string]fit.Residual, label string, width int) {
	keys := slices.Sorted(maps.Keys(byKey))
	rows := make([][]string, 0, len(keys))
	for _, k := range keys {
		r := byKey[k]
		rows = append(rows, []string{k,
			fmt.Sprintf("%.3fh", r.Total().Hours()),
			fmt.Sprintf("%.3fh", r.Orphaned.Hours()),
			fmt.Sprintf("%.3fh", r.Contradicted.Hours()),
			fmt.Sprintf("%.1fh", r.Scale().Hours()),
		})
	}
	writeColumns(out, table{
		head:    []string{label, "residual", "orphaned", "contradicted", "labelled"},
		body:    rows,
		natural: []int{18, 10, 10, 13, 10},
		right:   []bool{false, true, true, true, true},
		floor:   10, cols: width,
	})
}

func fitReportCircularity(out io.Writer, res fit.Result, width int) {
	if res.Reference == nil {
		return
	}
	fmt.Fprintf(out, "\nTHE PARAMETERS THE REVIEWER WAS SHOWN\n")
	fmt.Fprintf(out, "%v: residual %.3fh, ranked %d of %s\n",
		res.Reference.Point, res.Reference.Residual.Total().Hours(),
		res.ReferenceRank, plural(res.Live, "live point"))
	if res.Circular {
		wrap(out, "", "CIRCULAR: no point in this grid beat the parameters the labels were produced "+
			"under. That is the expected outcome when a reviewer confirms blocks he was shown, and it "+
			"means this search has measured the reviewer's agreement with the model rather than the "+
			"model. Do not adopt this answer as a fitted one.", width)
		return
	}
	wrap(out, "", "The search beat the shown parameters, which is the only circumstance in which a "+
		"residual from these labels carries information about the model rather than about the review.",
		width)
}

func fitList(ds []time.Duration) string {
	out := make([]string, 0, len(ds))
	for _, d := range ds {
		out = append(out, fit.Compact(d))
	}
	return strings.Join(out, ", ")
}

// fitListed is how many parameter sets a list in this report spells out before
// it starts counting instead.
//
// A plateau on the live grid runs to a hundred points and a hundred lines is not
// a report. What is not negotiable is that the count of what was left out is
// printed with it: a list that trails off silently reads as a complete one, and
// this project has shipped that mistake three times in three separate tools.
const fitListed = 12

func fitPoints(ps []fit.Point) string {
	out := make([]string, 0, min(len(ps), fitListed))
	for _, p := range ps[:min(len(ps), fitListed)] {
		out = append(out, p.String())
	}
	joined := strings.Join(out, "; ")
	if len(ps) > fitListed {
		joined += fmt.Sprintf("; and %d more, not listed", len(ps)-fitListed)
	}
	return joined
}
