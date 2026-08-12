package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/filedays"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/store"
)

// `timetattle status` answers one question: is the evidence still arriving?
//
// It exists because the answer was unavailable for over a day. From 2026-07-30
// 12:01 the scheduled harvest failed on a lost Full Disk Access grant, three
// runs in a row, and the only trace was six lines in ~/.timetattle/harvest.err.
// launchd carried on. Nothing else changed. The store simply stopped
// advancing, which looks exactly like a quiet week.
//
// Everything needed to notice was already recorded: harvest_run holds one row
// per source per invocation, with status, error, events_seen and events_new.
// There was no way to ask for it.
//
// # Why a command rather than a flag on harvest
//
// The moment it is needed is the moment harvest cannot run. Hanging it off
// `harvest -status` would entangle a read-only check with the flags harvest
// resolves before it does anything — the knowledgeC path, the Biome root, the
// Motion vault root — any of which can be the thing that is broken. It would
// also make the check one typo away from starting a harvest. A separate,
// read-only command can be run from a prompt, from another launchd job, or at
// the top of a session, which is where it will actually get run.
//
// # What it refuses to do
//
// It does not create the store it is checking. A status check that creates an
// empty database would report a healthy store on a machine where the harvest
// has never once succeeded, which is the same class of mistake it exists to
// catch.
//
// It does not treat "never run" as an alarm. A source that has never run may
// simply not be on this machine — no Motion, no registered repositories — and
// a check that cries wolf is a check nobody reads, which is precisely how a
// day was lost. Never-run is reported plainly, on its own line, and left to
// the reader to judge; failure and staleness are what set the exit status.
//
// # Reading harvest_run from here
//
// It doesn't. The two queries this file used to hold reached past
// internal/store to the database with SQL of their own, which made this the
// only place outside that package knowing harvest_run's column names — so a
// migration would have broken a file no migration test covers. They are now
// store.OpenReadOnly, Store.Runs and Store.Schema, and the aggregation they did
// in SQL is done below in Go, where a reader can see what "the latest run" and
// "failures since it last succeeded" actually mean.
//
// Read-only is not incidental. A check that migrated the database it was
// checking would report a schema no harvest had ever run against, and a check
// that created one would report a healthy store on a machine where the harvest
// has never once succeeded. store.OpenReadOnly refuses both.
//
// # The second half of the report, and why it used to lie
//
// This command has two sections, and until 2026-08-01 they rested on entirely
// different footings. The source table above is fed by harvest_run. The capture
// section below COUNTED FILES IN A DIRECTORY, because the capture phase wrote
// no row of any kind.
//
// A capture that failed today therefore left yesterday's files exactly where
// they were, and this report printed a plausible count with no failure signal.
// It did: the 44-hour outage of 2026-07-30 shows up in harvest_run as five
// failed ingestion rows, and left nothing whatsoever from the capture side. The
// files were the only evidence, and files that stop changing look exactly like
// files that did not need to.
//
// It is also the worst phase to be blind in. An ingestion can be repeated at
// any hour from a snapshot already on disk; a capture is the one chance to hold
// knowledgeC and Biome before they prune themselves. So the phase that cannot
// be re-run was the one phase this command could not see.
//
// Store.Captures is the other half now. The file count stays — a snapshot that
// was written and an ingestion that succeeded are two different claims, and
// this is the one place both are visible — but every line now carries the last
// recorded capture beside it. See migration 0007 for why that is a table of its
// own rather than more harvest_run rows.

// statusOptions is everything a report reads. now is injected so that
// staleness can be tested against a fixed clock rather than a sleeping test.
type statusOptions struct {
	store     string
	snapshots string
	index     string
	// plist is the installed launchd plist the staleness bound is derived
	// from, and empty when none is to be consulted — which is a state of its
	// own and never a default bound. See status_schedule.go.
	plist string
	// stale is an explicit -stale, and zero when none was given. Zero rather
	// than a default of 24h: a bound nobody chose, reconciled against nothing,
	// is the whole defect this report was carrying.
	stale time.Duration
	now   time.Time
	// inputs asks for the per-input breakdown under the table.
	inputs bool
	// cols is how wide the terminal the report is going into is, and 0 when it
	// is going into a pipe — which means "shorten nothing". reportStatus fills
	// it in from the writer; nothing outside this file sets it.
	cols int
}

func status(args []string) error { return executeSubcommand(statusCmd(), args) }

func statusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the harvest is still working",
		Long: `Reports what each source and each capture last did, one line apiece, and
writes nothing. Exits non-zero on a failed or partial run, a stranded run, a
failed or partial capture, or a newest run or capture older than the staleness
bound; a harvest in progress, a source that has never run, and a capture nothing
ever recorded are reported without failing.

The staleness bound is derived from the installed launchd schedule, at one and a
half times its interval, and -stale overrides it. With no schedule installed and
no -stale, freshness is reported as unchecked rather than judged: a bound nobody
derived cannot call a store either fresh or stale.`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	dir := fs.String("dir", "", "snapshot directory to check (default: ~/.timetattle/snapshots)")
	indexPath := fs.String("index", "",
		"registry index of registered directories (default: ~/.timetattle/registered-directories.json)")
	plist := fs.String("schedule", "",
		"installed launchd plist to derive the staleness bound from "+
			"(default: ~/Library/LaunchAgents/"+harvestAgentLabel+".plist)")
	// No default. The bound is derived from the installed schedule, and a 24h
	// default reconciled against nothing is what made a whole skipped harvest
	// invisible on a 12h interval. See status_schedule.go.
	stale := fs.Duration("stale", 0,
		"how old the newest harvest may be before the schedule is called stale "+
			"(default: one and a half times the installed schedule's interval)")
	inputs := fs.Bool("inputs", false,
		"list every input each source read on its most recent harvest, one line each")
	cmd.RunE = func(*cobra.Command, []string) error {
		var err error
		if *storePath == "" {
			if *storePath, err = store.DefaultPath(); err != nil {
				return err
			}
		}
		if *plist == "" {
			// A home directory that cannot be resolved is reported as an
			// unreadable schedule below rather than aborting the report: the
			// source table is most of what a reader came for.
			*plist, _ = defaultSchedulePath()
		}
		if *dir == "" {
			if *dir, err = snapshot.DefaultDir(); err != nil {
				return err
			}
		}
		if *indexPath == "" {
			if *indexPath, err = registry.DefaultIndexPath(); err != nil {
				return err
			}
		}
		return reportStatus(os.Stdout, statusOptions{
			store:     *storePath,
			snapshots: *dir,
			index:     *indexPath,
			plist:     *plist,
			stale:     *stale,
			now:       time.Now(),
			inputs:    *inputs,
		})
	}
	return cmd
}

// sourceStatus is one source's whole history, collapsed to what a reader needs.
//
// ran is the field the rest of this file turns on. A zero sourceStatus means
// "expected to run on this machine, and there is no row of any kind", which is
// not the same as a row saying nothing was found, and the two must never
// render alike.
type sourceStatus struct {
	id         string
	ran        bool
	startedAt  time.Time
	status     string
	failure    string
	seen       int
	added      int
	lastOK     time.Time
	everWorked bool
	// failedSince counts the runs that have failed since this source last
	// succeeded, which is the figure that turns "it is broken" into "it has
	// been broken for three runs and a day and a half". The incident this
	// command was written for was exactly three identical failures.
	failedSince int
	// inputs is how many things this source tried to read on that harvest and
	// okInputs how many it read through; the difference is what was lost.
	//
	// failedInputs is narrower than that difference on purpose: an input whose
	// run is still marked running was not read either, but it did not fail, and
	// a process that died partway is a different diagnosis from a source that
	// was refused. detail is each input, so that "which repository broke" is
	// answerable rather than merely counted.
	inputs       int
	okInputs     int
	failedInputs int
	detail       []statusInputStatus
	// runningSince is when the oldest of this source's unfinished runs began,
	// and the zero time when it has none. It is the age the verdict quotes,
	// which is what makes "running for 12s" and "running for 2h23m" two
	// different sentences about the same kind of row.
	runningSince time.Time
	// runLimit is how long one of this source's runs may sit at running before
	// it is presumed dead, and hasRunLimit says whether there was any completed
	// run to derive it from. False is not a limit of zero: it is a source
	// nothing recorded can yet judge, and it must never render as one that
	// passed a check.
	runLimit    time.Duration
	hasRunLimit bool
	// slowestRun is the measurement runLimit was derived from, kept so that the
	// verdict can say which of the two rules decided it.
	slowestRun time.Duration
}

// statusPartial is the status of a source that read some of its inputs and lost
// others.
//
// It is not a store.Status: nothing writes it to harvest_run, because no single
// run is partial. It is a property of a group of runs, and it exists so that a
// harvest which lost four repositories out of five can never print the word ok.
const statusPartial = "partial"

// statusStranded is a run that is still marked running, and has been for longer
// than anything of its source has ever taken to finish.
//
// It is not a store.Status either, and for a sharper reason than statusPartial:
// harvest_run cannot hold this. A harvest in flight and a harvest whose process
// died leave the identical row — a started_at and no finished_at — because the
// second one, by definition, was not there to write anything else. Nothing the
// writer can record distinguishes them, so the distinction has to be drawn by
// the reader, from the clock, and it is a presumption rather than an observation.
// The report says so in those words rather than asserting a death it did not see.
const statusStranded = "stranded"

// statusRunningSlack and statusRunningFloor are how long a run may sit at
// running before it is presumed dead: whichever is larger of the floor, and the
// slack multiplied by the longest completed run this source has ever recorded.
//
// # Why this is derived rather than a fixed number
//
// Until 2026-08-01 there was no tolerance at all. A row marked running in the
// latest invocation printed "a harvest that died partway" and exited non-zero —
// which is also exactly what a harvest running right now looks like, so a status
// check run from a prompt while the 12-hourly job happened to be mid-flight
// produced a failure that was not conservative but false. A check that cries
// wolf is a check nobody reads, and that is how 44 hours were lost once already.
//
// A fixed grace period would fix it today and break again later. claude-
// transcripts reads 723,905 events and dominates the harvest, and the corpus
// grows every day; a threshold tuned to a number measured this morning starts
// crying wolf again as soon as the runs outgrow it. Deriving it from the runs
// themselves means the tolerance grows exactly as fast as the work does, out of
// evidence rather than out of a guess.
//
// # Why the slack is ten, which is a measurement rather than a round number
//
// scripts/runningcheck replays the live store run by run: for every run that
// DID complete, it derives the limit from the runs before it — all this rule
// would have had while that run was in flight — and reports the ones that
// outlasted it. Each hit is a healthy harvest that would have been called dead.
// Against the 76 rows of 2026-08-01, with the floor taken out so that only the
// multiple is being tested:
//
//	go run ./scripts/runningcheck -floor 1s -slack N
//
//	slack 2  1 false alarm   run 20 took 1m15.75s, past the 41.92s implied by 20.96s
//	slack 3  1 false alarm   the same run, past 1m02.88s
//	slack 4  none
//	slack 10 none
//
// So four is where this history first comes clean, and four is not a margin: it
// is the exact edge of a five-sample record, and the next slow run resets it.
// The reason the edge sits there is the spread on IDENTICAL work — grouping
// completed runs by (source, input), claude-transcripts read ~722,000 events on
// each of five runs and took between 12.403s and 75.751s, 6.11x, with the work
// held constant; biome-appusage is 5.79x and motion-autosave 6.08x. The
// variation is the machine, not the workload, and the longest run recorded is
// one draw from it rather than a bound on the next.
//
// Ten is two and a half times the measured edge, applied to the maximum rather
// than the mean, which on claude-transcripts is another 2.1x again.
//
// # Why there is a floor as well, and what it is NOT
//
// Because ten times nothing is nothing. knowledgec-appusage's slowest completed
// run is 0.634s and ten times that is 6.34s, which a machine under memory
// pressure or a Spotlight storm clears without anything being wrong. The floor
// is what stops the fast sources — twelve of the thirteen — from being judged
// against a stopwatch.
//
// Five minutes is NOT validated by the replay above, and saying it were would be
// the kind of claim this project keeps catching itself in. runningcheck reports
// no false alarms at a 30-second floor either, because this store was recorded
// on a machine that was never wedged, and a store cannot measure a stall it
// never had. The floor is a margin against a variance that has not been sampled,
// chosen from the schedule instead: it is four times the longest whole harvest
// ever recorded here (76.17s, the invocation beginning 2026-08-01T13:05:14Z) and
// 1/144th of the 12-hour interval scripts/install-agent.sh schedules, so a death
// is still reported long before the next harvest would paper over it. On the one
// real stranded row in the store, harvest_run 28, the rule reports at
// 2026-08-01T13:19:51Z — 17h47m before the staleness bound would have said
// anything, and 23h47m before it while -stale still defaulted to 24h.
//
// # Why erring generous is cheap, and erring tight is not
//
// The two mistakes do not cost the same. Calling a live harvest dead is a lie
// that exits non-zero, and it discredits every other line in the report. Calling
// a dead harvest live only delays the news, and the delay is bounded twice over:
// the next scheduled harvest supersedes the stranded row within 12 hours, and
// the staleness check reports it within 18 whatever this rule decides — one and
// a half times the installed interval, derived in status_schedule.go. There is a
// backstop under being too slow and none under being too quick.
//
// No ceiling, deliberately. A tolerance that could only grow to some cap would
// start crying wolf again the moment the corpus outgrew the cap, which is the
// defect being fixed. It grows only because runs actually got slower, and a
// store where one run legitimately takes an hour should tolerate an hour.
const (
	statusRunningSlack = 10
	statusRunningFloor = 5 * time.Minute
)

// statusClock is what turns "this row says running" into "this run is alive" or
// "this run is dead". It is the only time-dependent judgement in the report, and
// it is a judgement rather than a reading.
type statusClock struct {
	now time.Time
	// slowest is the longest completed run each source has ever recorded. A
	// source that is ABSENT from this map has never completed a run, which is
	// not a duration of zero — it is the absence of any basis for judging one,
	// and the difference is the whole of statusClock.dead below.
	//
	// The raw measurement is kept rather than the limit derived from it, because
	// the report quotes both: which of the two rules bound the limit is the
	// difference between "past ten times its slowest run" and "past the floor",
	// and printing the first when the second is true would be arithmetic the
	// report did not do.
	slowest map[string]time.Duration
}

// limit is how long a run of this source may sit at running, and whether there
// is any basis for the question at all.
func (c statusClock) limit(source string) (time.Duration, bool) {
	d, ok := c.slowest[source]
	if !ok {
		return 0, false
	}
	return max(d*statusRunningSlack, statusRunningFloor), true
}

// statusSlowestRuns is the longest completed run each source has ever recorded,
// which is what the limit for a still-running run is derived from.
//
// Per source rather than per (source, input), and that is a decision rather than
// a convenience. Input identity is not stable: fs-observations records its input
// as the joined list of every registered root, motion-autosave records a glob,
// and knowledgeC records the snapshot filename, which carries a timestamp and is
// different every single harvest. Keyed per input, those sources would lose
// their precedent whenever the machine's configuration changed and quietly stop
// being judged at all — a silent disabling, which is worse than a loose bound.
// Pooling a source's inputs can only widen the limit, and wide is the safe
// direction here.
//
// Failed runs count as well as successful ones. A run that failed still measured
// how long the source took to return, and taking the maximum means including
// them can only widen the limit. On the live store it changes nothing: all five
// recorded failures are the Full Disk Access refusals of 2026-07-30, which
// returned in 0.000s to 0.002s.
func statusSlowestRuns(runs []store.Run) map[string]time.Duration {
	longest := map[string]time.Duration{}
	for _, r := range runs {
		// A run with no finished_at has not completed, and its elapsed time so
		// far is not a measurement of how long the source takes. Using it would
		// let a stranded run raise the very limit it is being judged against.
		if r.FinishedAt.IsZero() {
			continue
		}
		if prev, seen := longest[r.SourceID]; !seen || r.FinishedAt.Sub(r.StartedAt) > prev {
			longest[r.SourceID] = r.FinishedAt.Sub(r.StartedAt)
		}
	}
	return longest
}

// dead reports whether a run left at running has been there long enough to
// presume the process that wrote it is gone.
//
// A source with no completed run is never presumed dead, and that is the same
// rule as everywhere else in this file: absence of a row is not an observation.
// The first harvest on a machine reads the whole corpus and is the slowest run
// that will ever happen — nothing recorded bounds it, and inventing a bound
// would be a guess wearing the clothes of a measurement. It is reported by name
// and age instead, and staleness catches it within -stale if it never finishes.
func (c statusClock) dead(run store.Run) bool {
	limit, ok := c.limit(run.SourceID)
	return ok && c.now.Sub(run.StartedAt) > limit
}

// statusInputStatus is one thing a source read on one harvest — one repository,
// one snapshot, one glob — kept apart from the total so that a failure can be
// pointed at.
type statusInputStatus struct {
	input     string
	status    string
	failure   string
	seen      int
	added     int
	startedAt time.Time
}

func reportStatus(out io.Writer, opt statusOptions) error {
	// The two tables below are laid out for the terminal the report is going
	// into, and clipped as a backstop. columnsOf returns 0 for a pipe, which
	// leaves both alone: an agent reading this must get every column.
	opt.cols = columnsOf(out)
	out = clipTo(out, opt.cols)

	fmt.Fprintf(out, "store:     %s\n", opt.store)

	if _, err := os.Stat(opt.store); err != nil {
		fmt.Fprintf(out, "\nThere is no store at that path: a harvest has never completed one here.\n"+
			"Run \"timetattle harvest\" once, then \"./scripts/install-agent.sh\" to schedule it.\n")
		// No store means no capture rows, so every line below says so: the
		// files, if any, are there without anything having recorded putting
		// them there.
		reportCaptures(out, opt, nil)
		return fmt.Errorf("no store at %s", opt.store)
	}

	// Read-only, and never migrating: see the note at the top of this file.
	st, err := store.OpenReadOnly(opt.store)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	// The schema is the fact that diagnosed the incident this command was
	// written for: a store still on 0001 days after 0002 landed had not been
	// opened by a successful harvest since.
	if schema, err := st.Schema(ctx); err != nil {
		fmt.Fprintf(out, "schema:    unreadable (%v)\n", err)
	} else {
		fmt.Fprintf(out, "schema:    %s, applied %s\n", schema.Name, schema.AppliedAt.Format(store.TimeLayout))
	}

	// The bound freshness is judged against, and the schedule it came from,
	// named before any verdict rests on it. An unreadable schedule sets the
	// exit status for the same reason an unreadable capture_run table does: the
	// report could not take a reading it is supposed to take, and a verdict
	// printed over that would be an observation nobody made.
	bound := resolveStale(opt)
	writeSchedule(out, bound)
	scheduleErr := bound.failure()

	history, err := st.Runs(ctx, store.Filter{})
	if err != nil {
		return err
	}
	recorded := recordedStatus(history, opt.now)
	// A failure to work out what a harvest would attempt is reported here
	// rather than only in the exit status, because it silently shortens the
	// list of sources below: a source that could not be resolved would
	// otherwise be missing from the table altogether rather than shown as
	// never run, which is the one outcome this report may not produce.
	expected, expectErr := expectedSources(opt.index)
	if expectErr != nil {
		fmt.Fprintf(out, "warning:   the expected set of sources is incomplete (%v)\n", expectErr)
	}
	rows := merge(recorded, expected)

	newest := time.Time{}
	for _, r := range rows {
		if r.ran && r.startedAt.After(newest) {
			newest = r.startedAt
		}
	}
	if newest.IsZero() {
		fmt.Fprintf(out, "last run:  never\n")
	} else {
		fmt.Fprintf(out, "last run:  %s, %s ago\n", newest.Local().Format(time.DateTime), short(opt.now.Sub(newest)))
	}

	// The capture phase's own record, which harvest_run structurally cannot
	// hold: a capture produces files and bytes rather than events, and is the
	// one phase that cannot be re-run. See migration 0007.
	//
	// A failure to read it does NOT abandon the report. This command never
	// migrates the store it is checking — deliberately, see above — so a store
	// that has not been opened by a harvest since 0007 landed has no capture_run
	// table at all, and aborting here would break the whole check at exactly the
	// moment it is reached for. The source table below is unaffected and is most
	// of what a reader came for.
	//
	// It is reported rather than swallowed, and it sets the exit status. Without
	// that, every capture would print "no capture recorded" — which would be
	// this report claiming an observation it never made, over a table it could
	// not read.
	captures, captureErr := st.Captures(ctx, store.Filter{})
	if captureErr != nil {
		fmt.Fprintf(out, "warning:   the capture_run record could not be read (%v)\n", captureErr)
	}

	fmt.Fprintln(out)
	writeTable(out, rows, opt.now, opt.cols)
	if opt.inputs {
		statusWriteInputs(out, rows, opt.now)
	}
	fmt.Fprintln(out)
	states := reportCaptures(out, opt, captures)

	prior := errors.Join(expectErr, captureErr, scheduleErr)
	return errors.Join(prior, verdict(out, rows, states, newest, opt, bound, prior))
}

// verdict says what is wrong in words, and returns the error that makes the
// exit status non-zero. Both, always: a check whose only output is an exit
// code is a check whose failure is as invisible as the one that started this.
// prior is what reportStatus already went wrong on before calling this — an
// incomplete expected-source list, an unreadable capture record. It is taken
// only so that the all-clear at the bottom cannot be printed over a problem
// that has already been reported higher up, which is the one line in this
// report a reader is entitled to trust absolutely.
func verdict(out io.Writer, rows []sourceStatus, states []captureState, newest time.Time,
	opt statusOptions, bound staleBound, prior error) error {
	var (
		failed   []string
		partial  []string
		inFlight []string
		stranded []string
		never    []string
		errs     []error
	)
	for _, r := range rows {
		switch {
		case !r.ran:
			never = append(never, r.id)
		case r.status == string(store.StatusFailed):
			failed = append(failed, r.id)
		case r.status == statusPartial:
			partial = append(partial, r.id)
		case r.status == string(store.StatusRunning):
			inFlight = append(inFlight, r.id)
		case r.status == statusStranded:
			stranded = append(stranded, r.id)
		}
	}

	fmt.Fprintln(out)
	if len(failed) > 0 {
		fmt.Fprintf(out, "%s failed on the most recent run:\n", plural(len(failed), "source"))
		for _, r := range rows {
			if r.status != string(store.StatusFailed) || !r.ran {
				continue
			}
			fmt.Fprintf(out, "  %s: %s\n    %s\n", r.id, failureSummary(r, opt.now), firstLine(r.failure))
			statusWriteLostInputs(out, r, opt.now)
		}
		errs = append(errs, fmt.Errorf("%s failed on the most recent run: %s",
			plural(len(failed), "source"), strings.Join(failed, ", ")))
	}
	// Partial is its own paragraph and its own error, and never folded into
	// either neighbour. A source that read four repositories of five has not
	// failed — the four are stored and are real — and it has not succeeded
	// either, and the whole reason this state exists is that the second half
	// used to be invisible.
	if len(partial) > 0 {
		fmt.Fprintf(out, "%s read some inputs and lost others on the most recent run:\n",
			plural(len(partial), "source"))
		for _, r := range rows {
			if r.status != statusPartial {
				continue
			}
			// Counted as what was not read rather than as what failed: an
			// input left marked running was not read either, and a paragraph
			// that said "0 of 4 inputs lost" under the word partial would be
			// the report arguing with itself.
			fmt.Fprintf(out, "  %s: %d of %s not read, %s\n",
				r.id, r.inputs-r.okInputs, plural(r.inputs, "input"), failureSummary(r, opt.now))
			statusWriteLostInputs(out, r, opt.now)
		}
		errs = append(errs, fmt.Errorf("%s only partly read on the most recent run: %s",
			plural(len(partial), "source"), strings.Join(partial, ", ")))
	}
	// A run that has outlasted everything of its source that ever finished. The
	// age and the limit are both printed, because this is the one line in the
	// report that states a conclusion nothing observed: it says a process is
	// gone, and a reader is owed the measurement it concluded that from.
	if len(stranded) > 0 {
		fmt.Fprintf(out, "%s left marked running far longer than anything of it that has finished, "+
			"which is a harvest that died partway:\n", plural(len(stranded), "source"))
		for _, r := range rows {
			if r.status != statusStranded {
				continue
			}
			// Which of the two rules decided it, said accurately. Quoting the
			// multiple over a source whose slowest run is a millisecond would
			// be the report inventing arithmetic to justify a conclusion.
			basis := fmt.Sprintf("%d times its slowest completed run", statusRunningSlack)
			if r.slowestRun*statusRunningSlack < statusRunningFloor {
				basis = fmt.Sprintf("the floor under a source whose slowest completed run is %s",
					short(r.slowestRun))
			}
			fmt.Fprintf(out, "  %s: running for %s, past the %s that is %s\n",
				r.id, short(opt.now.Sub(r.runningSince)), short(r.runLimit), basis)
			statusWriteLostInputs(out, r, opt.now)
		}
		errs = append(errs, fmt.Errorf("%s left marked running long past anything that has finished: %s",
			plural(len(stranded), "source"), strings.Join(stranded, ", ")))
	}
	// A harvest that is simply still going. Reported, and NOT an error: this is
	// the whole defect. It is also not silence — a source with no result yet is
	// a gap in the table, and a report that left it unexplained would send a
	// reader looking for a failure that is not there.
	if len(inFlight) > 0 {
		fmt.Fprintln(out, "a harvest is in progress, and this report is a snapshot of it:")
		for _, r := range rows {
			if r.status != string(store.StatusRunning) {
				continue
			}
			if !r.hasRunLimit {
				// The first run a source has ever had, which is also the slowest
				// it will ever be. Said plainly rather than resolved either way.
				fmt.Fprintf(out, "  %s: running for %s, and nothing of it has ever finished, "+
					"so nothing recorded here can say whether it is still alive\n",
					r.id, short(opt.now.Sub(r.runningSince)))
				continue
			}
			fmt.Fprintf(out, "  %s: running for %s, and it is not presumed dead until %s\n",
				r.id, short(opt.now.Sub(r.runningSince)), short(r.runLimit))
		}
	}
	if len(never) > 0 {
		// Not an error. Stated in full so that a source quietly dropping out of
		// the plan is at least visible to someone who knows it should be there.
		fmt.Fprintf(out, "%s expected on this machine and never run: %s\n",
			plural(len(never), "source"), strings.Join(never, ", "))
	}

	// The capture phase gets its own paragraphs, beside the ingestion sources
	// and never folded into them. A knowledgeC capture that failed and a
	// knowledgec-appusage ingestion that failed are different losses: the second
	// can be repeated from a snapshot on disk, and the first is a window that
	// has closed.
	errs = append(errs, statusCaptureVerdict(out, states, opt, bound)...)

	switch {
	case newest.IsZero():
		fmt.Fprintln(out, "no harvest has ever recorded a run in this store")
		errs = append(errs, errors.New("no harvest has ever recorded a run in this store"))
	case !bound.ok:
		// The third outcome, and the one this report may not skip in silence.
		// "Fresh", "stale" and "nothing here can say" are three different
		// findings, and printing either of the first two over a bound that was
		// never derived is the defect this check exists to catch, one level
		// down. See status_schedule.go.
		fmt.Fprintf(out, "the freshness of the newest run and of every capture was not checked, "+
			"because %s: the newest run is %s old and nothing here says whether that is late\n",
			bound.unchecked(), short(opt.now.Sub(newest)))
	case opt.now.Sub(newest) > bound.d:
		age := opt.now.Sub(newest)
		fmt.Fprintf(out, "the schedule is stale: the newest run of any source is %s old, past the %s allowed\n",
			short(age), short(bound.d))
		errs = append(errs, fmt.Errorf("stale: the newest harvest run is %s old", short(age)))
	}
	// The one line a reader is entitled to trust absolutely, which is why a
	// harvest still running changes its wording rather than being folded in.
	// "Every source ran and succeeded" over a source that is still reading would
	// be the false negative traded for the false positive above, and this file
	// is not permitted to make that trade in either direction.
	if len(errs) == 0 && prior == nil {
		switch {
		case !bound.ok:
			// Never the unqualified all-clear. Everything the report DID check
			// came back clean, and the check a reader most wants — is the
			// evidence still arriving — was not made at all.
			fmt.Fprintln(out, "nothing else is wrong: every source that ran succeeded and every capture "+
				"held, but that is not an all-clear on the schedule, which went unchecked")
		case len(inFlight) > 0:
			fmt.Fprintf(out, "nothing else is wrong: every source that has finished succeeded within "+
				"the last %s, and every capture held\n", short(bound.d))
		default:
			fmt.Fprintf(out, "every source ran and succeeded within the last %s, and every capture held\n", short(bound.d))
		}
	}
	return errors.Join(errs...)
}

// statusWriteLostInputs names the inputs a source lost, which is the difference
// between "gitlog is broken" and "gitlog could not read one repository of five".
//
// A source with a single input has already said all of this in the line above,
// and repeating its one path there would push the sources that read several
// down the page for no information.
func statusWriteLostInputs(out io.Writer, r sourceStatus, now time.Time) {
	if r.inputs < 2 {
		return
	}
	for _, d := range r.detail {
		if d.status == string(store.StatusOK) {
			continue
		}
		reason := firstLine(d.failure)
		if reason == "" {
			// Three ways to have no error text, and they are three different
			// facts. "Left marked running" would be a lie about the first of
			// them: nobody left it anywhere, it is being read right now.
			switch d.status {
			case string(store.StatusRunning):
				reason = fmt.Sprintf("still being read, %s in", short(now.Sub(d.startedAt)))
			case statusStranded:
				reason = fmt.Sprintf("left marked running %s ago and never finished", short(now.Sub(d.startedAt)))
			default:
				reason = "left marked " + d.status
			}
		}
		fmt.Fprintf(out, "    %s: %s\n", d.input, reason)
	}
}

// failureSummary says how long this has been going on.
//
// The two branches are different diagnoses, not two phrasings of one. A source
// that has never once succeeded is a wiring or configuration problem and has
// cost nothing yet. A source that succeeded this morning and has failed three
// times since is a permission that was revoked today, and every hour of that
// is evidence draining out of a window that does not refill.
func failureSummary(r sourceStatus, now time.Time) string {
	if !r.everWorked {
		return fmt.Sprintf("%s, and it has never once succeeded", plural(r.failedSince, "failure"))
	}
	return fmt.Sprintf("%s since it last succeeded, %s ago",
		plural(r.failedSince, "failure"), short(now.Sub(r.lastOK)))
}

// writeTable renders one line per source, in name order.
//
// Name order rather than worst-first, deliberately: the table is meant to be
// compared against the last time it was read, and a table that reorders itself
// according to what is broken cannot be. The problems are called out by name
// underneath instead.
func writeTable(out io.Writer, rows []sourceStatus, now time.Time, cols int) {
	// inputs is a column rather than a footnote because it is what makes the
	// totals mean something: 932 events across 5 inputs and 932 across 1 are
	// different claims, and the second is what this table used to imply about a
	// source that had read five repositories.
	head := []string{"source", "last run", "status", "inputs", "seen", "new", "last ok"}
	body := make([][]string, 0, len(rows))
	for _, r := range rows {
		if !r.ran {
			// The whole point of the row: nothing was recorded, said in those
			// words, with every measured column blank rather than zero. A zero
			// here would read as a measurement that came back empty.
			body = append(body, []string{r.id, "never run", "-", "-", "-", "-", "never"})
			continue
		}
		lastOK := "never"
		if r.everWorked {
			lastOK = short(now.Sub(r.lastOK)) + " ago"
		}
		// How many were read out of how many were tried, and only when those
		// differ: "5" is a source that read everything it planned to, and "4/5"
		// is one that did not. Reading it as a fraction of what was attempted
		// is the whole point, so it is never the count of failures.
		count := fmt.Sprintf("%d", r.inputs)
		if r.okInputs != r.inputs {
			count = fmt.Sprintf("%d/%d", r.okInputs, r.inputs)
		}
		body = append(body, []string{r.id, short(now.Sub(r.startedAt)) + " ago", r.status,
			count, fmt.Sprintf("%d", r.seen), fmt.Sprintf("%d", r.added), lastOK})
	}
	writeColumns(out, table{
		head:    head,
		body:    body,
		natural: []int{colContent, 12, 8, 6, 9, 9, 12},
		right:   []bool{false, false, false, true, true, true, false},
		floor:   statusNameFloor,
		cols:    cols,
	})
}

// statusNameFloor is how short a source name may get before the table would
// stop telling one source from another. The four knowledgeC streams share an
// eleven-character prefix, so anything below this collapses them into rows a
// reader cannot tell apart — which is worse than a line that wrapped.
const statusNameFloor = 16

// statusWriteInputs is the per-input breakdown, behind -inputs.
//
// It is not in the default report because status is read by scanning it, and a
// machine with five registered repositories would otherwise spend eleven of its
// lines on gitlog. It is not omitted altogether because "which repository
// broke" is the only question worth asking once something is broken, and the
// answer is in these rows and nowhere else.
func statusWriteInputs(out io.Writer, rows []sourceStatus, now time.Time) {
	fmt.Fprintf(out, "\nwhat each source read on its most recent harvest:\n")
	for _, r := range rows {
		if !r.ran {
			fmt.Fprintf(out, "  %s: never run\n", r.id)
			continue
		}
		fmt.Fprintf(out, "  %s: %s, %s ago\n", r.id, plural(r.inputs, "input"), short(now.Sub(r.startedAt)))
		for _, d := range r.detail {
			fmt.Fprintf(out, "    %-8s  %9d  %9d  %s\n", d.status, d.seen, d.added, d.input)
			if d.failure != "" {
				fmt.Fprintf(out, "      %s\n", firstLine(d.failure))
			}
		}
	}
}

// captureKind is one of the four things the capture phase preserves as files.
type captureKind struct {
	// kind is the identifier harvest records against, and label is how it is
	// printed. They are separate so that the stored value can stay stable while
	// the report is reworded.
	kind  string
	label string
	dir   string
	// topLevelOnly is for the knowledgeC snapshots, which sit directly in the
	// snapshot directory alongside the subdirectories the other three use.
	topLevelOnly bool
}

// captureState is one kind's whole story: what is on disk, and what the last
// capture actually recorded doing.
//
// Both halves are needed and neither substitutes for the other. The files are
// what was preserved; the row is whether preserving them still works. A capture
// that failed today leaves yesterday's files in place, so the first half alone
// reads as healthy — which it did, for 44 hours.
type captureState struct {
	captureKind
	// files and newest describe the directory. err is why it could not be read.
	files  int
	newest time.Time
	err    error
	// recorded is the field the rest of this turns on. False means no capture
	// of this kind has ever been recorded, which is not the same fact as a
	// capture that ran and preserved nothing new, and the two must never render
	// alike.
	recorded bool
	last     store.Capture
}

// reportCaptures says what is preserved and whether preserving it still works.
//
// It used to count files in a directory and nothing else, because the capture
// phase wrote no row of any kind — the defect task 4tuVv exists for. The count
// stays: a snapshot that was written and an ingestion that succeeded are two
// different claims, and this is the one place both can be seen at once. What is
// new is the second half of every line, which is the only part that can tell a
// stale directory from a fresh one.
//
// captures is every recorded capture, oldest first, so the last row of a kind is
// its most recent attempt. It is empty when there is no store to read, and the
// report then says so rather than implying the files arrived by themselves.
func reportCaptures(out io.Writer, opt statusOptions, captures []store.Capture) []captureState {
	latest := map[string]store.Capture{}
	for _, c := range captures {
		latest[c.Kind] = c
	}

	kinds := []captureKind{
		{captureKindKnowledgeC, "knowledgeC", opt.snapshots, true},
		{captureKindBiome, "Biome", snapshot.BiomeDir(opt.snapshots), false},
		{captureKindVault, "Motion vault", snapshot.VaultDir(opt.snapshots), false},
		{captureKindFiledays, "filedays", filedays.Dir(opt.snapshots), false},
	}

	states := make([]captureState, 0, len(kinds))
	for _, k := range kinds {
		s := captureState{captureKind: k}
		s.files, s.newest, s.err = countCaptures(k.dir, k.topLevelOnly)
		if errors.Is(s.err, os.ErrNotExist) {
			// A directory that was never created is the ordinary state of a
			// capture that has nothing to preserve, not an unreadable one.
			s.err = nil
		}
		s.last, s.recorded = latest[k.kind]
		states = append(states, s)
	}

	fmt.Fprintf(out, "preserved in %s:\n", opt.snapshots)
	body := make([][]string, 0, len(states))
	for _, s := range states {
		body = append(body, []string{s.label, statusOnDisk(s), statusNewest(s, opt.now),
			statusCaptureAge(s, opt.now), statusCaptureOutcome(s)})
	}
	writeColumns(out, table{
		indent:  "  ",
		head:    []string{"what", "on disk", "newest", "last capture", "outcome"},
		body:    body,
		natural: []int{colContent, 12, 10, 12, colFlexible},
		right:   make([]bool, 5),
		floor:   len("Motion vault"),
		cols:    opt.cols,
	})
	return states
}

// statusOnDisk is the file count, with "none" rather than "0 files" so that an
// empty directory does not read as a measurement that came back zero when it is
// really the absence of one.
func statusOnDisk(s captureState) string {
	switch {
	case s.err != nil:
		return "unreadable"
	case s.files == 0:
		return "none"
	default:
		return plural(s.files, "file")
	}
}

func statusNewest(s captureState, now time.Time) string {
	if s.err != nil || s.files == 0 {
		return "-"
	}
	return short(now.Sub(s.newest)) + " ago"
}

// statusCaptureAge is when the capture last ran, which is the column that
// carries criterion t38. A directory whose files have not changed in two days
// looks identical whether the capture has been running happily over an
// unchanged source or has not run at all, and this is the difference.
func statusCaptureAge(s captureState, now time.Time) string {
	if !s.recorded {
		return "-"
	}
	return short(now.Sub(s.last.StartedAt)) + " ago"
}

// statusCaptureOutcome is what the last capture did, in words.
//
// The four cases below are four different facts and none of them may be printed
// as another. "Nothing new" is a healthy reading from a content-addressed
// capture over an unchanged source. "Nothing to capture" is a machine with no
// Motion installed. "No capture recorded" is the absence of a reading
// altogether. And "failed" is the one that was invisible.
func statusCaptureOutcome(s captureState) string {
	if !s.recorded {
		return "no capture recorded"
	}
	switch s.last.Status {
	case store.CaptureAbsent:
		return "nothing to capture on this machine"
	case store.CaptureFailed:
		return "failed"
	case store.CapturePartial:
		return fmt.Sprintf("partial, %s preserved and some lost", plural(s.last.FilesNew, "file"))
	}
	held := ""
	if s.last.FilesHeld > 0 {
		held = fmt.Sprintf(", %d already held", s.last.FilesHeld)
	}
	if s.last.FilesNew == 0 {
		return "ok, nothing new" + held
	}
	return fmt.Sprintf("ok, %d new%s", s.last.FilesNew, held)
}

// statusCaptureVerdict names what is wrong with the capture phase, and returns
// the errors that make the exit status non-zero.
//
// A capture that has never been recorded is reported and not alarmed about, the
// same tolerance a source that has never run gets: captures come and go with
// what is installed, and a check that cries wolf is a check nobody reads, which
// is exactly how a day was lost.
func statusCaptureVerdict(out io.Writer, states []captureState, opt statusOptions, bound staleBound) []error {
	var errs []error
	for _, s := range states {
		if !s.recorded {
			continue
		}
		switch s.last.Status {
		case store.CaptureFailed:
			// What is on disk is named in the same breath as the failure,
			// because those files are the trap: they are from before this run
			// and they are exactly what made the old report look healthy.
			// Nothing on disk is a different sentence, not a "0 files" one.
			held := "and nothing of it is preserved on disk"
			if s.files > 0 {
				held = fmt.Sprintf("and the %s on disk are from before that", plural(s.files, "file"))
			}
			fmt.Fprintf(out, "the %s capture failed %s ago, %s:\n  %s\n",
				s.label, short(opt.now.Sub(s.last.StartedAt)), held, firstLine(s.last.Error))
			errs = append(errs, fmt.Errorf("the %s capture failed on the most recent run", s.label))
		case store.CapturePartial:
			fmt.Fprintf(out, "the %s capture preserved some of what it tried and lost the rest:\n  %s\n",
				s.label, firstLine(s.last.Error))
			errs = append(errs, fmt.Errorf("the %s capture only partly succeeded on the most recent run", s.label))
		}
	}

	// Staleness per capture, and not only for the harvest as a whole. The two
	// come apart in exactly the case this task is about: ingestion carries on
	// reading snapshots already on disk, writing fresh harvest_run rows, while
	// the capture that feeds it has silently stopped. The source table looks
	// perfect throughout.
	//
	// A bound that was never derived stops this loop rather than defaulting it.
	// The one sentence in the verdict below says so once, for the captures and
	// the sources together, so that a reader is never left to infer from
	// silence that a capture was judged fresh.
	for _, s := range states {
		if !s.recorded || !bound.ok {
			continue
		}
		if age := opt.now.Sub(s.last.StartedAt); age > bound.d {
			fmt.Fprintf(out, "the %s capture is stale: it last ran %s ago, past the %s allowed\n",
				s.label, short(age), short(bound.d))
			errs = append(errs, fmt.Errorf("stale: the %s capture last ran %s ago", s.label, short(age)))
		}
	}

	var unrecorded []string
	for _, s := range states {
		if !s.recorded {
			unrecorded = append(unrecorded, s.label)
		}
	}
	if len(unrecorded) > 0 {
		fmt.Fprintf(out, "nothing has recorded a capture of: %s\n", strings.Join(unrecorded, ", "))
	}
	return errs
}

// countCaptures counts files under dir and reports the newest modification
// time among them.
//
// topLevelOnly is for the knowledgeC snapshots, which sit directly in the
// snapshot directory alongside the subdirectories the other three captures
// use; counting recursively there would count everything four times.
func countCaptures(dir string, topLevelOnly bool) (int, time.Time, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, time.Time{}, err
	}
	var (
		n      int
		newest time.Time
	)
	for _, e := range entries {
		if e.IsDir() {
			if topLevelOnly {
				continue
			}
			sub, subNewest, subErr := countCaptures(filepath.Join(dir, e.Name()), false)
			if subErr != nil {
				continue
			}
			n += sub
			if subNewest.After(newest) {
				newest = subNewest
			}
			continue
		}
		if topLevelOnly && !strings.HasSuffix(e.Name(), ".db.gz") {
			continue
		}
		info, infoErr := e.Info()
		if infoErr != nil {
			continue
		}
		n++
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	return n, newest, nil
}

// expectedSources is what a harvest run on this machine, right now, would try
// to read.
//
// It is built from the same functions harvest builds its plan from rather than
// from a list restated here, so a source that is added to one cannot go
// missing from the other — which would make it permanently invisible in
// exactly the way this report exists to prevent.
func expectedSources(index string) ([]string, error) {
	var (
		ids  []string
		errs []error
	)
	add := func(plan []ingestion) {
		for _, p := range plan {
			ids = append(ids, p.source.ID())
		}
	}

	registered, err := registeredIngestions()
	errs = append(errs, err)
	add(registered)
	// The path is never opened: only the four identifiers are wanted, and
	// naming a file here that a harvest did not write would be a lie waiting
	// to be read as provenance.
	add(snapshotIngestions("(not read)"))

	markers, err := registry.List(index)
	errs = append(errs, err)
	if fs := fsobserveIngestion(markers); fs != nil {
		ids = append(ids, fs.source.ID())
	}
	add(gitlogIngestions(markers))

	manifests, err := manifestIngestions()
	errs = append(errs, err)
	add(manifests)

	return ids, errors.Join(errs...)
}

// merge puts the recorded history and the expected plan together, so that a
// source in the plan with no history gets a row saying it has never run and a
// source with history nobody plans any more still shows what it last did.
func merge(recorded map[string]sourceStatus, expected []string) []sourceStatus {
	all := make(map[string]sourceStatus, len(recorded)+len(expected))
	maps.Copy(all, recorded)
	for _, id := range expected {
		if _, ok := all[id]; !ok {
			all[id] = sourceStatus{id: id}
		}
	}
	out := make([]sourceStatus, 0, len(all))
	for _, r := range all {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].id < out[j].id })
	return out
}

// recordedStatus collapses every recorded run into what a reader needs: what
// each source did on its most recent harvest, and how it has fared across all
// of them.
//
// The store returns rows rather than a verdict, and this is the verdict. It is
// in Go rather than in SQL deliberately — the two queries it replaces expressed
// "failures since it last succeeded" as a correlated subquery with a COALESCE
// whose purpose took a paragraph to explain, and the count it produces is one
// of the numbers a person acts on.
//
// # Why a source is not one row
//
// A source that runs once per repository writes one harvest_run row per
// repository. On 2026-08-01 gitlog-commits wrote five, totalling 932 seen and
// 930 new — 930 being exactly the store's gitlog-commits event count — and this
// function took the last row and reported 81 seen and 79 new, understating the
// source elevenfold.
//
// The undercount was the harmless half. Nothing aggregated status across the
// group either, so four repositories could have been lost and the fifth's ok
// would have been printed as the source's result, with the summary line below
// still saying every source succeeded. That is the hole task NkvFg closed one
// level up — best-effort per source, with the partial failure made visible —
// reopened one level down.
//
// The fix is here rather than in the collectors. fs-observations avoids the
// problem by walking every root in one call and writing one row, and gitlog
// could be reshaped the same way, but a report that is only honest about
// sources shaped a particular way is a trap for whoever writes the next
// collector. This is generic over source_id; nothing below knows what gitlog is.
//
// runs must be in the order they began, which is what Store.Runs guarantees.
//
// now is taken because one of the answers depends on it. Whether a run still
// marked running is in flight or stranded is not in the rows — the two cases
// write the identical row — so it is decided here, against the clock the report
// was asked for, and never against time.Now() reached for from inside.
func recordedStatus(runs []store.Run, now time.Time) map[string]sourceStatus {
	// Derived from the whole history rather than from one invocation: five
	// completed runs is already a thin sample, and narrowing it further would
	// make the limit jump about with whatever the last harvest happened to do.
	clock := statusClock{now: now, slowest: statusSlowestRuns(runs)}
	out := map[string]sourceStatus{}
	for _, harvest := range statusInvocations(runs) {
		for _, r := range statusHarvest(harvest, clock) {
			// The history so far, carried across from this source's earlier
			// harvests: a source failing since yesterday and a source that has
			// never once worked read identically without it, and they are
			// different diagnoses.
			was := out[r.id]
			r.lastOK, r.everWorked, r.failedSince = was.lastOK, was.everWorked, was.failedSince
			switch r.status {
			case string(store.StatusOK):
				r.lastOK = r.startedAt
				r.everWorked = true
				r.failedSince = 0
			case string(store.StatusFailed), statusPartial:
				// Counted per harvest and not per row. Three failed harvests of
				// a five-repository source are three failures, not fifteen, and
				// "three failures since it last succeeded" is the sentence that
				// described the incident this command was written for.
				r.failedSince++
			}
			out[r.id] = r
		}
	}
	return out
}

// statusInvocationGap is how long a pause between two consecutive runs means
// they came out of different harvests.
//
// harvest_run records no invocation of its own, so the boundary is inferred,
// and this is the one fact in the table that infers it. It is not a guess about
// scheduling: the ingestion loop begins the next run immediately after
// finishing the previous one, with a Fprintf between them and nothing else, so
// runs of one harvest are chained end to end by construction. Measured on the
// live store on 2026-08-01, across 55 rows and 7 invocations: the largest gap
// within an invocation is 0.0014s and the smallest gap between two invocations
// is 17.1s, four orders of magnitude apart. A second sits between them with
// room on both sides.
//
// Erring either way is visible rather than silent. Splitting one harvest in two
// under-reports a total, which is the bug being fixed; merging two harvests
// double-counts one, which is why the collapse in statusHarvest is by input.
const statusInvocationGap = time.Second

// statusInvocations partitions runs, in the order they began, into the harvests
// that wrote them.
func statusInvocations(runs []store.Run) [][]store.Run {
	var out [][]store.Run
	for i, run := range runs {
		if i == 0 || statusNewHarvest(runs[i-1], run) {
			out = append(out, nil)
		}
		out[len(out)-1] = append(out[len(out)-1], run)
	}
	return out
}

// statusNewHarvest says whether next began a harvest of its own.
//
// A run with no finished_at ends its harvest whatever follows it: that is a
// process that died partway, and the next run is necessarily a later attempt.
func statusNewHarvest(prev, next store.Run) bool {
	if prev.FinishedAt.IsZero() {
		return true
	}
	return next.StartedAt.Sub(prev.FinishedAt) > statusInvocationGap
}

// statusHarvest collapses the runs of one harvest into one row per source,
// keeping each input separately underneath.
//
// A source that read the same input twice in one harvest keeps only the later
// read — the registry does not stop a directory being registered twice, and the
// second read of a repository sees the same commits as the first and adds
// nothing, so summing both would report evidence that was never there and a
// total that cannot be reconciled against the event table.
func statusHarvest(runs []store.Run, clock statusClock) []sourceStatus {
	var order []string
	inputs := map[string][]string{}
	latest := map[string]map[string]store.Run{}
	for _, run := range runs {
		if _, seen := latest[run.SourceID]; !seen {
			latest[run.SourceID] = map[string]store.Run{}
			order = append(order, run.SourceID)
		}
		if _, seen := latest[run.SourceID][run.Input]; !seen {
			inputs[run.SourceID] = append(inputs[run.SourceID], run.Input)
		}
		latest[run.SourceID][run.Input] = run
	}

	out := make([]sourceStatus, 0, len(order))
	for _, id := range order {
		out = append(out, statusAcross(id, inputs[id], latest[id], clock))
	}
	return out
}

// statusAcross totals one source's inputs within one harvest.
func statusAcross(id string, inputs []string, runs map[string]store.Run, clock statusClock) sourceStatus {
	r := sourceStatus{id: id, ran: true, inputs: len(inputs)}
	r.slowestRun = clock.slowest[id]
	r.runLimit, r.hasRunLimit = clock.limit(id)
	// inFlight and stranded are both rows that say running, split by the clock.
	// They are counted apart because they are different diagnoses: one is work
	// that has not finished, the other is work nobody is doing.
	var inFlight, stranded int
	for _, in := range inputs {
		run := runs[in]
		r.seen += run.EventsSeen
		r.added += run.EventsNew
		if run.StartedAt.After(r.startedAt) {
			r.startedAt = run.StartedAt
		}
		state := string(run.Status)
		switch run.Status {
		case store.StatusOK:
			r.okInputs++
		case store.StatusFailed:
			r.failedInputs++
		case store.StatusRunning:
			// The oldest unfinished run, not the newest: a source with two runs
			// in flight is as stuck as its most stuck one, and quoting the
			// younger age would understate exactly the case worth noticing.
			if r.runningSince.IsZero() || run.StartedAt.Before(r.runningSince) {
				r.runningSince = run.StartedAt
			}
			if clock.dead(run) {
				stranded++
				state = statusStranded
			} else {
				inFlight++
			}
		}
		if run.Status != store.StatusOK && r.failure == "" {
			r.failure = run.Error
		}
		r.detail = append(r.detail, statusInputStatus{
			input:     in,
			status:    state,
			failure:   run.Error,
			seen:      run.EventsSeen,
			added:     run.EventsNew,
			startedAt: run.StartedAt,
		})
	}

	// ok is reachable only when every input was read. That is the whole
	// property: a harvest that lost four repositories of five may not print the
	// same word as one that lost none, and there is no arrangement of the rows
	// that makes it.
	//
	// The in-flight branch sits second, and only when nothing failed. A source
	// still reading its fourth repository has not lost the fourth, so it may not
	// print partial — that was the same false alarm wearing the other label, and
	// it fired on every live harvest of a multi-repository machine. But a
	// repository that was refused has been refused whatever the process is doing
	// now, so a failure anywhere in the group still decides the word. Being
	// still busy is not an excuse the report accepts on behalf of a lost input.
	switch {
	case r.okInputs == r.inputs:
		r.status = string(store.StatusOK)
	case r.failedInputs == 0 && inFlight > 0:
		r.status = string(store.StatusRunning)
	case r.okInputs > 0:
		r.status = statusPartial
	case r.failedInputs > 0:
		r.status = string(store.StatusFailed)
	default:
		// Nothing read, nothing failed, nothing in flight: every input of this
		// source is a run that stopped being written to.
		r.status = statusStranded
	}

	// Input order, so that the detail can be compared against the last time it
	// was read rather than reordering itself with what is broken.
	sort.Slice(r.detail, func(i, j int) bool { return r.detail[i].input < r.detail[j].input })
	return r
}

// firstLine trims a multi-line error to its first line for the summary. The
// knowledgeC permission error is six lines, five of them advice about System
// Settings, and the summary is a list.
func firstLine(s string) string {
	if before, _, ok := strings.Cut(s, "\n"); ok {
		return strings.TrimSpace(before)
	}
	return strings.TrimSpace(s)
}
