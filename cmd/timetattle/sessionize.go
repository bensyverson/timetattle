package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// sessionize rebuilds derived_session from the events already in the store.
//
// It reads no source and captures nothing. Everything it needs is in the
// database, which is what makes it safe to run as often as an argument about
// the threshold requires: the derivation is thrown away and rebuilt each time,
// and `event` and `harvest_run` come out untouched.
func sessionize(args []string) error { return sessionizeTo(os.Stdout, args) }

// sessionizeTo is sessionize with the report redirected, so that what the
// command prints can be asserted on rather than merely not crashing.
func sessionizeTo(stdout io.Writer, args []string) error {
	return executeSubcommand(sessionizeCmd(stdout), args)
}

func sessionizeCmd(stdout io.Writer) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sessionize",
		Short: "Group events into sittings and store them",
		Long: `Groups the events already in the store into sittings, and replaces
derived_session with the result.

Nothing is captured and no source is read. The derivation is a pure function of
the events and the parameters, so it is rebuilt wholesale on every run: every
row of derived_session is deleted and rewritten inside one transaction, and the
event and harvest_run tables are not written to at all.

THE GAP THRESHOLD IS NOT A SETTLED VALUE. Total engaged time ranged from 530.4h
at two minutes to 1199.9h at two hours over the corpus the spike measured — a
2.3x spread — and nothing has yet fitted it against ground truth. The shipped
default of 15m is a placeholder in the middle of that range and should be read
as one. Use -sweep to see the spread on your own data before quoting a figure
from it.

Sittings are global rather than per-project. Engaged time is per-project and
may overlap, because concurrent work on two projects is billable to both;
presence is wall clock and is never double-counted. These spans are the second
kind, and per-project engaged time is an intersection against them.

Presence bounds sittings and never creates them. A span the backlight was
observed dark across splits a sitting and is subtracted from it; a span it was
observed lit across merges nothing; and a span it said NOTHING about does
neither, because an absence of evidence is not evidence of absence and reading
one as the other has deleted real work in this project three times.

-by-day and -by-week group the sittings into WORKING DAYS, which begin at 05:00
local rather than at midnight so that an evening running to 01:37 is still that
evening's work. A sitting that really does cross the boundary is split at it,
so no day is ever credited with more wall clock than elapsed inside it, and the
report reconciles the per-day figures against the ungrouped total on every run.
Days are 23 or 25 hours long across a daylight-saving transition and the report
says which; the zone comes from the zoneinfo database and never from a fixed
offset.

A day inside the observed span with no sittings on it reads "observed, no
work". A day nothing was recorded on does not appear at all, because a zero
there would be a claim nobody measured.

sessions.exclude names files that are machine activity rather than work, and it
is applied where the events are READ. Nothing is deleted from the store, the
rule reaches every observation already in it, and removing a pattern and
re-running puts the time back. SQLite -wal and -shm sidecars are excluded with
no configuration at all, because they cannot be anybody's work product; every
other pattern is yours to declare, and the list ships empty. What was dropped is
counted and itemised on every run.

Parameters come from ~/.timetattle/config.json, and any flag below overrides
the file for that one run without changing it. Every session row stores the
parameters it was derived under AND the corpus it was drawn from — the exclude
patterns that were applied and the -from/-to window that was read — so a stale
table is visible rather than silently mixed in with a fresh question. Editing
sessions.exclude and not re-running this command is therefore something a
reader can now see rather than something it has to be told.

-dry-run computes everything, writes nothing, and compares what it would have
written against what is already stored: whether the table is this same answer,
whether it is stale, and — separately from both — whether nothing has ever been
derived into this store at all.`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to read and write (default: ~/.timetattle/timetattle.db)")
	configPath := fs.String("config", "", "configuration to read (default: ~/.timetattle/config.json)")
	gap := fs.Duration("gap", 0, "idle gap that separates one sitting from the next (default: from the config file)")
	leadIn := fs.Duration("lead-in", 0, "how long before its first observation a sitting is assumed to have begun")
	tailOut := fs.Duration("tail-out", 0, "how long after its last observation a sitting is assumed to have run on")
	presence := fs.String("presence-sources", "",
		"comma-separated sources read as presence, which bounds sittings without creating them")
	ignore := fs.String("ignore-sources", "", "comma-separated sources to read and drop")
	exclude := fs.String("exclude", "", excludeFlagUsage)
	from := fs.String("from", "", "earliest instant to consider, as 2026-07-11 or 2026-07-11T09:00:00Z")
	to := fs.String("to", "", "latest instant to consider")
	sweep := fs.String("sweep", "",
		"comma-separated gaps to report side by side, writing nothing; e.g. 2m,15m,2h")
	dryRun := fs.Bool("dry-run", false,
		"compute and report, but leave the stored sessions alone; says whether they are stale")
	quiet := fs.Bool("quiet", false, "print nothing on success; for scheduled runs")
	byDay := fs.Bool("by-day", false, "report engaged time per working day")
	byWeek := fs.Bool("by-week", false, "report engaged time per week")
	dayStart := fs.String("day-start", "", "wall clock a working day begins at, as 05:00 (default: from the config file)")
	weekStart := fs.String("week-start", "", "weekday a week begins on, as monday (default: from the config file)")
	timezone := fs.String("timezone", "", "IANA zone the working day is local to, as America/Chicago")
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

		// The file is the parameter set; a flag replaces one value of it for this
		// run only. Nothing here writes the config back, so a swept threshold
		// cannot become the configured one by accident.
		p := sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		}
		if set["gap"] {
			p.Gap = *gap
		}
		if set["lead-in"] {
			p.LeadIn = *leadIn
		}
		if set["tail-out"] {
			p.TailOut = *tailOut
		}
		if set["presence-sources"] {
			p.PresenceSources = splitCommaList(*presence)
		}
		if set["ignore-sources"] {
			p.IgnoreSources = splitCommaList(*ignore)
		}
		if err := p.Validate(); err != nil {
			return err
		}
		excludes, err := sessionExcludesFrom(cfg, *exclude, set["exclude"])
		if err != nil {
			return err
		}

		// The calendar goes through the config types rather than being assembled
		// here, so that a flag and a file are parsed by the same code and cannot
		// disagree about what "05:00" or "monday" means.
		calCfg := cfg.Calendar
		if set["timezone"] {
			calCfg.Timezone = *timezone
		}
		if set["day-start"] {
			if err := calCfg.DayStart.UnmarshalJSON([]byte(strconv.Quote(*dayStart))); err != nil {
				return fmt.Errorf("-day-start: %w", err)
			}
		}
		if set["week-start"] {
			if err := calCfg.WeekStart.UnmarshalJSON([]byte(strconv.Quote(*weekStart))); err != nil {
				return fmt.Errorf("-week-start: %w", err)
			}
		}
		cal, err := calCfg.Sessions()
		if err != nil {
			return err
		}

		filter := store.Filter{}
		if filter.From, err = parseBoundary(*from, "from"); err != nil {
			return err
		}
		if filter.To, err = parseBoundary(*to, "to"); err != nil {
			return err
		}

		st, err := store.Open(*storePath)
		if err != nil {
			return err
		}
		defer st.Close()

		ctx := context.Background()
		// The canonical view, never the table. Summing over every observation
		// ever made double-counts each horizon that a later harvest completed,
		// which is the exact inflation Timetattle exists to catch.
		events, err := st.Events(ctx, filter)
		if err != nil {
			return err
		}
		// Machine activity is removed BEFORE the observed window is computed as
		// well as before the clustering, so a day whose only trace was a log
		// rotation reads as a day nothing was recorded on rather than as a day
		// observed to hold no work. Those are different claims and this is the one
		// place the difference can still be made.
		events, drop := sessions.Retain(events, excludes)

		out := stdout
		if *quiet {
			out = io.Discard
		}

		if *sweep != "" {
			reportExclusions(out, excludes, drop)
			return sweepGaps(*sweep, events, p, out)
		}

		res, err := sessions.Cluster(events, p)
		if err != nil {
			return err
		}
		reportSessions(out, *storePath, res, excludes, drop)

		// The observed window is the span the events themselves cover, and it is
		// what separates "no work on this day" from "nobody looked at this day".
		// It is computed from the events already read rather than from a second
		// query, so the two can never describe different corpora.
		if *byDay || *byWeek {
			reportByPeriod(out, cal, res, observedWindow(events), *byWeek)
		}

		// The corpus this run was drawn from: which observations it was allowed to
		// see, as against what it did with them. It is built from the excludes
		// actually applied above and the filter actually read with, rather than
		// from the config, so that a -exclude or a -from given for one run is
		// recorded as the thing that produced these rows.
		corpus := sittings.Corpus{
			Exclude: excludes.Patterns(),
			From:    filter.From,
			To:      filter.To,
		}

		if *dryRun {
			// A dry run leaves the table alone, so the useful thing it can say is
			// whether the table it left alone is still this answer. Both halves
			// count: a derivation at a different gap and a derivation drawn from a
			// different corpus are equally stale, and until the corpus was stored
			// the second kind could not be seen from here at all.
			//
			// window names an unbounded end as unbounded rather than printing the
			// zero time. It is a local rather than a helper beside plural and
			// listOrNone because it is wanted in exactly one place.
			window := func(c sittings.Corpus) string {
				from, to := "the beginning", "now"
				if !c.From.IsZero() {
					from = c.From.UTC().Format(store.TimeLayout)
				}
				if !c.To.IsZero() {
					to = c.To.UTC().Format(store.TimeLayout)
				}
				return from + ".." + to
			}
			stored, err := st.Sittings(ctx, time.Time{}, time.Time{})
			switch {
			case err != nil:
				fmt.Fprintf(out, "dry run: the stored sessions were left alone, and could not be read "+
					"to compare against: %v\n", err)
			case stored.ComputedAt.IsZero():
				fmt.Fprintln(out, "dry run: the stored sessions were left alone. Nothing has been derived "+
					"into this store at all, which is not the same as a store with no work in it.")
			case stored.Params.Equal(res.Params) && stored.Corpus.Equal(corpus):
				fmt.Fprintf(out, "dry run: the stored sessions were left alone. They hold %s "+
					"derived at %s under these same parameters and this same corpus.\n",
					plural(len(stored.Sittings), "sitting"), stored.ComputedAt.UTC().Format(store.TimeLayout))
			default:
				fmt.Fprintf(out, "dry run: the stored sessions were left alone, and they are STALE. "+
					"They hold %s derived at %s; this run would write %d.\n",
					plural(len(stored.Sittings), "sitting"), stored.ComputedAt.UTC().Format(store.TimeLayout),
					len(res.Sessions))
				if !stored.Params.Equal(res.Params) {
					fmt.Fprintf(out, "  parameters: stored gap %v, lead-in %v, tail-out %v; now %v, %v, %v\n",
						stored.Params.Gap, stored.Params.LeadIn, stored.Params.TailOut,
						res.Params.Gap, res.Params.LeadIn, res.Params.TailOut)
				}
				if !stored.Corpus.Equal(corpus) {
					fmt.Fprintf(out, "  excluded: stored %s; now %s\n",
						listOrNone(stored.Corpus.Canonical().Exclude), listOrNone(corpus.Canonical().Exclude))
					fmt.Fprintf(out, "  window:   stored %s; now %s\n", window(stored.Corpus), window(corpus))
				}
				fmt.Fprintln(out, "  re-run without -dry-run to make the stored derivation this one.")
			}
			return nil
		}

		// One call, through the handle already open. The store owns the
		// transaction, so derived_session comes out holding either this derivation
		// or the previous one and never a mixture — see store.ReplaceDerived.
		if err := st.ReplaceDerived(ctx, sessions.Replace(res, corpus, time.Now())); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: %s written\n", *storePath, plural(len(res.Sessions), "session"))
		return nil
	}
	return cmd
}

// sweepGaps reports the same events clustered at several thresholds, and
// writes nothing.
//
// This is the calibration instrument rather than a convenience. A single total
// looks authoritative; four totals differing by a factor of two say plainly
// that the number is a function of a parameter nobody has fitted yet.
func sweepGaps(list string, events []source.Event, p sessions.Params, out io.Writer) error {
	var gaps []time.Duration
	for _, text := range splitCommaList(list) {
		d, err := time.ParseDuration(text)
		if err != nil {
			return fmt.Errorf("-sweep %q: %w", text, err)
		}
		gaps = append(gaps, d)
	}
	if len(gaps) == 0 {
		return fmt.Errorf("-sweep was given no thresholds")
	}
	slices.Sort(gaps)

	fmt.Fprintf(out, "lead-in %v, tail-out %v; nothing written\n\n", p.LeadIn, p.TailOut)
	fmt.Fprintf(out, "%-10s %10s %12s %10s %10s %10s\n", "gap", "sittings", "engaged", "median", "p95", "margin")
	var smallest, largest time.Duration
	for _, g := range gaps {
		at := p
		at.Gap = g
		res, err := sessions.Cluster(events, at)
		if err != nil {
			return err
		}
		total := res.Total()
		if smallest == 0 || total < smallest {
			smallest = total
		}
		if total > largest {
			largest = total
		}
		fmt.Fprintf(out, "%-10v %10d %11.1fh %10v %10v %9.1f%%\n",
			g, len(res.Sessions), total.Hours(),
			round(quantile(res, 0.5)), round(quantile(res, 0.95)), 100*marginShare(res))
	}
	if smallest > 0 {
		fmt.Fprintf(out, "\nspread %.2fx across the swept range\n", largest.Seconds()/smallest.Seconds())
	}
	return nil
}

// report prints what the derivation found, separating what was observed from
// what was inferred and what was dropped.
func reportSessions(out io.Writer, path string, res sessions.Result,
	excludes sessions.Excludes, drop sessions.Drop) {
	p, s := res.Params, res.Stats
	fmt.Fprintf(out, "%s\n", path)
	fmt.Fprintf(out, "gap %v, lead-in %v, tail-out %v\n", p.Gap, p.LeadIn, p.TailOut)
	fmt.Fprintf(out, "presence: %s\n", listOrNone(p.PresenceSources))
	fmt.Fprintf(out, "ignored:  %s\n", listOrNone(p.IgnoreSources))
	reportExclusions(out, excludes, drop)
	if p.LeadIn+p.TailOut >= p.Gap {
		fmt.Fprintf(out, "WARNING: lead-in plus tail-out (%v) is at least the gap (%v), "+
			"so the effective threshold is %v and the gap is doing nothing on its own\n",
			p.LeadIn+p.TailOut, p.Gap, p.Gap+p.LeadIn+p.TailOut)
	}
	fmt.Fprintf(out, "%s read: %d evidence, %d presence, %d ignored\n",
		plural(s.Evidence+s.Presence+s.Ignored, "observation"), s.Evidence, s.Presence, s.Ignored)
	if s.Unbounded > 0 {
		fmt.Fprintf(out, "%s had an endpoint that was never observed, and counted as an instant\n",
			plural(s.Unbounded, "observation"))
	}
	fmt.Fprintf(out, "%s, %.1fh engaged, median %v, p95 %v\n",
		plural(len(res.Sessions), "sitting"), res.Total().Hours(),
		round(quantile(res, 0.5)), round(quantile(res, 0.95)))
	fmt.Fprintf(out, "%.1fh of that (%.1f%%) is margin: lead-in, tail-out and bridged silence\n",
		marginTotal(res).Hours(), 100*marginShare(res))
	if s.MergedByExpansion > 0 {
		fmt.Fprintf(out, "%d clusters merged because their margins overlapped\n", s.MergedByExpansion)
	}
	fmt.Fprintf(out, "observed absence: %.1fh recorded, %.1fh trimmed across %d splits, %d observations suppressed\n",
		s.ObservedAbsence.Hours(), s.TrimmedByAbsence.Hours(), s.SplitByAbsence, s.SuppressedByAbsence)
	fmt.Fprintf(out, "contested (read as lit and dark at once): %.1fh, trimmed from nothing\n", s.Contested.Hours())
	if s.UnevidencedPieces > 0 {
		fmt.Fprintf(out, "%d pieces left between two dark spans with no observation in them, %.1fh, dropped\n",
			s.UnevidencedPieces, s.UnevidencedDropped.Hours())
	}
	if s.PresenceWithoutSpan > 0 {
		fmt.Fprintf(out, "%s carried no usable interval and bounded nothing\n",
			plural(s.PresenceWithoutSpan, "presence observation"))
	}
}

// observedWindow is the span the observations themselves cover.
//
// It is the denominator for "was this day looked at". A working day inside it
// with no sittings on it was observed and held no work; a working day outside
// it was never looked at, and the two must never print the same. Every event
// counts, including the ignored ones: a row from a source this derivation
// drops is still proof that something was recorded at that moment.
func observedWindow(events []source.Event) sessions.Window {
	var w sessions.Window
	for _, e := range events {
		for _, at := range []time.Time{e.Start, e.End} {
			if at.IsZero() {
				continue
			}
			if w.From.IsZero() || at.Before(w.From) {
				w.From = at
			}
			if w.To.IsZero() || at.After(w.To) {
				w.To = at
			}
		}
	}
	return w
}

// reportByPeriod prints engaged time per working day, or per week.
//
// It states the reconciliation rather than leaving it to be checked: the
// per-period figures must sum to the ungrouped total exactly, on a 23-hour day
// as much as on a 24-hour one, and a report that did not say so would be a
// report nobody could falsify.
func reportByPeriod(out io.Writer, cal sessions.Calendar, res sessions.Result, observed sessions.Window, weekly bool) {
	slices := cal.ByDay(res.Sessions, observed)
	if len(slices) == 0 {
		fmt.Fprintf(out, "\nno working days to report: nothing was recorded, which is not the same "+
			"as nothing having happened\n")
		return
	}

	unit := "working day"
	if weekly {
		unit = "week"
	}
	fmt.Fprintf(out, "\nengaged time by %s, days from %v in %s, weeks from %v\n",
		unit, cal.DayStart, zoneName(cal, slices[0].Start), strings.ToLower(cal.WeekStart.String()))

	rows := slices
	var daysPresent []int
	if weekly {
		rows, daysPresent = foldIntoWeeks(cal, slices)
	}

	fmt.Fprintf(out, "%-12s %6s %10s %8s %9s %6s\n",
		unit, "length", "engaged", "began", "sittings", "split")
	var sum time.Duration
	var unobserved, partial int
	for i, r := range rows {
		sum += r.Engaged
		// A week the projection holds fewer than seven days of is a week the
		// corpus only partly covers. Saying so is the difference between "52h
		// that week" and "52h across the four days of it we have".
		if daysPresent != nil && daysPresent[i] < 7 {
			partial++
		}
		// "began" is the earliest sitting to START here, and a dash means none
		// did. On a day holding only the tail of last night's sitting that is
		// the true answer, and a boundary time there would be an invention.
		began := "-"
		if !r.FirstStart.IsZero() {
			began = r.FirstStart.In(cal.Location).Format("15:04:05")
		}
		switch {
		case r.Engaged == 0 && r.Observed:
			fmt.Fprintf(out, "%-12v %5.0fh %10s %8s %9d %6d   observed, no work\n",
				r.Day, r.Length().Hours(), "-", began, r.Sessions, r.Split)
		case r.Engaged == 0:
			unobserved++
			fmt.Fprintf(out, "%-12v %5.0fh %10s %8s %9d %6d   nothing recorded\n",
				r.Day, r.Length().Hours(), "-", began, r.Sessions, r.Split)
		default:
			note := ""
			if !r.Observed {
				note = "   outside the observed span"
			}
			if daysPresent != nil && daysPresent[i] < 7 {
				note += fmt.Sprintf("   partial: %d of 7 days recorded", daysPresent[i])
			}
			fmt.Fprintf(out, "%-12v %5.0fh %10v %8s %9d %6d%s\n",
				r.Day, r.Length().Hours(), round(r.Engaged), began, r.Sessions, r.Split, note)
		}
	}

	// The reconciliation. Days tile the timeline, so this difference is zero
	// by construction; printing it is what makes that checkable rather than
	// asserted, and a non-zero here is a bug and not a rounding artefact.
	total := res.Total()
	fmt.Fprintf(out, "\n%v across %d %ss, against an ungrouped %v; difference %v\n",
		round(sum), len(rows), unit, round(total), sum-total)
	if sum != total {
		fmt.Fprintf(out, "WARNING: the per-%s figures do not sum to the total; "+
			"the working-day boundary is creating or destroying time\n", unit)
	}
	if unobserved > 0 {
		fmt.Fprintf(out, "%d %ss are listed as holding no work only because a sitting reached into them; "+
			"nothing was recorded there\n", unobserved, unit)
	}
	if partial > 0 {
		fmt.Fprintf(out, "%d %ss are only partly covered by the corpus; their totals are not "+
			"comparable with a whole one\n", partial, unit)
	}
	if split := countSplit(rows); split > 0 {
		fmt.Fprintf(out, "%s crossed a boundary and were split across two %ss. "+
			"Their margin is reported unapportioned, because a sitting records how much of it was "+
			"inferred and not where that fell\n", plural(split, "sitting"), unit)
	}
}

// zoneName renders the calendar's zone usefully.
//
// An unconfigured zone is time.Local, whose String is the literal "Local" —
// which tells a reader nothing, and a per-day figure is meaningless without
// knowing which day it is a day of. The abbreviation and offset in force at
// the first day are appended so that a report pasted into project/ carries the
// zone it was computed in.
func zoneName(cal sessions.Calendar, at time.Time) string {
	abbrev, offset := at.In(cal.Location).Zone()
	sign, mins := "+", offset/60
	if mins < 0 {
		sign, mins = "-", -mins
	}
	return fmt.Sprintf("%v (%s, %s%02d:%02d at the first day)", cal.Location, abbrev, sign, mins/60, mins%60)
}

// foldIntoWeeks sums day slices into the weeks they belong to, and reports how
// many days of each week the projection actually held.
//
// A week's length is a property of the CALENDAR and not of how much data
// arrived: it is 167 or 169 hours across a transition rather than always 168.
// Summing the lengths of the days present would instead report a corpus that
// began on a Thursday as a 96-hour week, which is a fact about the harvest
// wearing the clothes of a fact about the calendar. The count of days present
// is returned separately so a partial week can say that it is one.
func foldIntoWeeks(cal sessions.Calendar, days []sessions.DaySlice) ([]sessions.DaySlice, []int) {
	var out []sessions.DaySlice
	var present []int
	index := map[sessions.Day]int{}
	for _, d := range days {
		week := cal.WeekOf(d.Day)
		i, ok := index[week]
		if !ok {
			start, _ := cal.Bounds(week)
			_, end := cal.Bounds(week.AddDays(6))
			out = append(out, sessions.DaySlice{Day: week, Start: start, End: end})
			present = append(present, 0)
			i = len(out) - 1
			index[week] = i
		}
		present[i]++
		out[i].Engaged += d.Engaged
		out[i].Sessions += d.Sessions
		out[i].Split += d.Split
		out[i].Margin += d.Margin
		out[i].SplitMargin += d.SplitMargin
		out[i].Observed = out[i].Observed || d.Observed
		// A week's first start is the earliest of its days', and a week of
		// nothing but continuations has none at all.
		if !d.FirstStart.IsZero() && (out[i].FirstStart.IsZero() || d.FirstStart.Before(out[i].FirstStart)) {
			out[i].FirstStart = d.FirstStart
		}
	}
	return out, present
}

func countSplit(rows []sessions.DaySlice) int {
	var n int
	for _, r := range rows {
		n += r.Split
	}
	// A split sitting is counted once per piece it was cut into, so the pieces
	// come in pairs at minimum; reporting sittings rather than pieces is what
	// a reader means by "how many were split".
	return n / 2
}

// quantile is the qth session length, nearest-rank, so p95 of twenty sessions
// is a session that exists rather than an interpolation between two.
func quantile(res sessions.Result, q float64) time.Duration {
	if len(res.Sessions) == 0 {
		return 0
	}
	lengths := make([]time.Duration, 0, len(res.Sessions))
	for _, s := range res.Sessions {
		lengths = append(lengths, s.Duration())
	}
	slices.Sort(lengths)
	rank := int(q * float64(len(lengths)))
	if rank >= len(lengths) {
		rank = len(lengths) - 1
	}
	return lengths[rank]
}

func marginTotal(res sessions.Result) time.Duration {
	var sum time.Duration
	for _, s := range res.Sessions {
		sum += s.Margin
	}
	return sum
}

func marginShare(res sessions.Result) float64 {
	total := res.Total()
	if total == 0 {
		return 0
	}
	return marginTotal(res).Seconds() / total.Seconds()
}
