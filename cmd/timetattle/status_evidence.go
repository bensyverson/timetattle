package main

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// WHETHER EVIDENCE ARRIVED, which is the question the rest of this report could
// not ask.
//
// # The defect
//
// Every other check in status reads harvest_run, so every one of them measures
// whether a HARVEST RAN. A harvest that fires exactly on schedule and ingests
// nothing advances every one of those clocks while the store itself stands
// still. Measured on the live store on 2026-08-13 at 15:04Z: the newest harvest
// run was 1h59m old, the newest event was 12h12m old, and the report read
// "every source ran and succeeded within the last 6h00m, and every capture held"
// and exited zero.
//
//	sqlite3 ~/.timetattle/timetattle.db \
//	  "select max(started_at) from harvest_run;
//	   select max(coalesce(ended_at, started_at)) from event;"
//
// That is the same blindness as the 2026-08-12 incident by a different route:
// `dashboard` called the day unobserved and was right, `status` called the
// system green and was right by its own narrower definition, and nothing
// reconciled the two.
//
// # Why "no evidence" is NOT the fault
//
// It is very tempting to make the age of the newest event a fault and be done.
// It is wrong, and the store says so. 15:04Z is 10:04 local; the newest evidence
// was 21:52 local the previous evening. Asleep and not yet at the desk is a
// complete explanation, and a rule that called it a fault would fire every
// single morning — the same error as reading events_new = 0 as a fault, which on
// this store would have fired on the 09:00 and 13:00 runs of 2026-08-13, runs
// that read 1,017,812 and 1,017,255 events respectively and found nothing new.
// Those runs looked. They are observations of not-X, not absences.
//
// So the age of the newest evidence is REPORTED and never judged. What it does
// do is take the unqualified all-clear away: a reader is owed the figure, and a
// green line printed over a store that has not moved in twelve hours is the
// defect whatever the arithmetic behind it.
//
// # What IS a finding: silence somebody witnessed
//
// A source's silence is measured as its WITNESSED LAG — how far behind the
// newest evidence in the WHOLE store its own newest evidence sits. See
// internal/store/evidence.go. The frontier does not move while nobody is
// recording, so a night, a weekend and a fortnight's holiday cost every source
// exactly nothing. A source falls behind only while some OTHER source is
// recording, and that other source is a witness that the machine was alive and
// this one said nothing through it. That is an observation rather than an
// absence, which is the whole distinction this file exists to draw.
//
// THE GUARD IS NOT YET LOAD-BEARING ON THIS STORE, and saying otherwise would be
// the kind of claim this project keeps catching itself in. Sampled hourly across
// the collected era and judged both ways — against the frontier and against the
// wall clock — the two rules report identically at every slack of 2 or more, and
// differ only at slack 1, where the clock adds 12 source-hours (62 against 50),
// eleven of them knowledgec-islocked. The reason is that the longest stretch in
// which NO source recorded anything since collection began is 10h45m45s, ending
// 2026-08-09 12:37, and every bound in force is wider than that. What the guard
// protects against is the weekend away and the fortnight's holiday this
// fourteen-day record does not contain, and the case for it is structural rather
// than measured. TestSilenceIsReportedOnlyWhileSomethingElseIsRecording is the
// constructed pair that does measure it: removing the guard turns a 36h silence
// nobody witnessed into a report.
//
// It is compared against that source's OWN history, because the thirteen
// sources have nothing in common. Measured over each source's collected era on
// the live store, the longest witnessed lag ranges from 12.33h (biome-appusage,
// knowledgec-appusage, knowledgec-backlit) to 70.00h (job-claims) — a factor of
// nearly six, and a single global bound would be five times too tight at one end
// or six times too loose at the other.
//
//	go run ./scripts/quietperiod
//
// # What this still cannot tell apart, stated rather than papered over
//
// Whether a source was silent because it had nothing to say or because it has
// stopped working. Nothing in the store separates those, and the report says so
// in the same breath as the finding, exactly as the stale-schedule verdict says
// that harvest_run cannot tell a missed harvest from a machine that was not
// running. What the report claims is only what it saw: this source recorded
// nothing while others recorded, for longer than it ever has before.

// statusQuietSlack and statusQuietSample are the rule: a source is reported
// silent once its witnessed lag passes this multiple of the longest lag it has
// ever closed, and it is not judged at all until it has closed this many.
//
// # Why the multiple is four, which is a measurement
//
// scripts/quietperiod replays the live store prequentially, the same way
// scripts/runningcheck calibrates the stranded-run rule: at every instant of the
// collected era it re-derives the bound from only the lags that had closed by
// then — all the evidence status would have had if it had been run at that
// moment — and asks whether the source's lag exceeded it. The replay is weighted
// by TIME rather than by arrivals, because status is run when a person runs it
// and a long silence is over-represented in time exactly as it is
// under-represented in arrivals.
//
//	go run ./scripts/quietperiod -slack N -episodes
//
//	slack  1   37 reports over the fourteen-day record
//	slack  2   17
//	slack  3    8
//	slack  4    7
//	slack  6    2
//	slack 10    0
//
// Every one of those, at every slack, is a source setting a new record for its
// own quiet — the rule fires on an unprecedented silence and then never again at
// that length. At slack 4, FOUR of the seven are the single weekend of
// 2026-08-08 to 08-10, when the machine was on and recording system activity
// while no commit, no task claim and no watched file was touched:
// fs-observations 67.88h, gitlog-commits and gitlog-reflog 68.92h, job-claims
// 70.00h. THOSE ARE TRUE STATEMENTS AND NOT FALSE ALARMS — gitlog-commits really
// did record nothing while the store advanced 68.92h — and they are the reason
// the finding below is worded as a reading and refuses to name a cause.
//
// # What the multiple cannot buy, and it must be said
//
// There is no multiple at which this rule stops firing on innocent silences and
// still sees a stalled source, and there cannot be one. The maximum of a sample
// only ratchets upward, so the FIRST occurrence of any new length of quiet is
// unprecedented by construction, whether the cause is a broken collector or a
// long weekend. Ten reports nothing at all over this record and is also too
// loose to have seen the one real stall in it: knowledgec-appusage, silent
// 50.39h against its own longest of 12.33h, needs a multiple under 4.09.
//
// Four is the trade taken, and it is a trade rather than a threshold that came
// clean. It reports seven times over a fourteen-day record, and that rate falls
// as the record lengthens and each source's longest quiet converges — which
// makes this the one number here that should be re-measured against a longer
// store rather than left alone.
//
// # Why the sample floor is twenty
//
// Because the maximum of a handful of draws is not a statement about a
// distribution. With n closed lags the maximum sits at the n/(n+1) quantile of
// the source's own quiet in expectation; twenty puts it at the 95th percentile,
// which is where "longer than it has ever been quiet" starts to mean something.
//
// It is a guard rather than a tuning knob, and the measurement says so: on the
// live store the replay reports seven times at a sample floor of 5, of 20 and of
// 30 alike, because the four-hour floor above already removes every silence
// short enough for a thin sample to have produced. What it is really there for
// is the case this store does not contain — a source newly added, judged against
// the three quiet spells it has had so far.
//
//	go run ./scripts/quietperiod -min 5    # and -min 20, and -min 30
const (
	statusQuietSlack  = 4
	statusQuietSample = 20
)

// evidenceRow is one source's evidence freshness.
//
// judged is the field the rest of this turns on. False means nothing recorded
// can say what quiet looks like for this source, which is not the same fact as a
// source that is keeping up, and the two must never render alike.
type evidenceRow struct {
	id     string
	newest time.Time
	events int
	// lag is how far behind the store's frontier this source's newest evidence
	// sits, and is meaningless when judged is false.
	lag time.Duration
	// quiet is the longest witnessed lag this source has ever closed, spells is
	// how many closed lags that is the maximum of, and bound is the two of them
	// put through statusQuietSlack.
	quiet  time.Duration
	spells int
	bound  time.Duration
	judged bool
	silent bool
}

// evidenceReport is the whole evidence side of the report.
type evidenceReport struct {
	// frontier is the newest evidence in the store, and the zero time when the
	// store holds none at all. Zero is the absence of a reading and never an
	// instant in the year one.
	frontier time.Time
	rows     []evidenceRow
	// floor is the shortest silence that may be reported however unprecedented
	// it is, and checked says whether there was one to derive.
	floor   time.Duration
	checked bool
	// unchecked is why the silence check was not made, in words, and empty when
	// it was.
	unchecked string
}

// evidenceFreshness works out what the store's own contents say.
//
// The floor is the installed schedule's interval, and it is derived rather than
// typed for the same reason the staleness bound is: it is the one figure about
// this machine's cadence that cannot go stale, and it follows the agent down to
// hourly with nothing here edited. The argument for having a floor at all is
// that evidence cannot arrive faster than the harvest brings it in, so a silence
// shorter than one interval is not evidence that a source has stopped — it may
// simply be that the next harvest has not run.
//
// With no schedule to derive one from, the silence check is NOT MADE and the
// report says so. That is b7TYN's rule for the staleness bound applied here: a
// bound reconciled against nothing produces a clean report indistinguishable
// from a correct one, which is the failure this whole file exists under.
func evidenceFreshness(ev []store.Evidence, bound staleBound) evidenceReport {
	rep := evidenceReport{rows: make([]evidenceRow, 0, len(ev))}
	if bound.sched.state == scheduleFound && bound.sched.interval > 0 {
		rep.floor, rep.checked = bound.sched.interval, true
	} else {
		rep.unchecked = bound.shortReason()
	}

	for _, e := range ev {
		if e.Newest.After(rep.frontier) {
			rep.frontier = e.Newest
		}
	}
	for _, e := range ev {
		r := evidenceRow{id: e.Source, newest: e.Newest, events: e.Events, spells: len(e.Lags)}
		quiet, ok := e.LongestLag()
		// Judged only where there is a source's own history to judge it by, and
		// only where a floor was derived to sit under that history.
		r.judged = ok && r.spells >= statusQuietSample && rep.checked && !e.Newest.IsZero()
		if !r.judged {
			rep.rows = append(rep.rows, r)
			continue
		}
		r.quiet = quiet
		r.lag = rep.frontier.Sub(e.Newest)
		r.bound = max(quiet*statusQuietSlack, rep.floor)
		r.silent = r.lag > r.bound
		rep.rows = append(rep.rows, r)
	}
	sort.Slice(rep.rows, func(i, j int) bool { return rep.rows[i].id < rep.rows[j].id })
	return rep
}

// silent is the sources that have stopped recording while the store went on.
func (rep evidenceReport) silent() []evidenceRow {
	var out []evidenceRow
	for _, r := range rep.rows {
		if r.silent {
			out = append(out, r)
		}
	}
	return out
}

// unjudged is the sources nothing recorded can say anything about — a source
// with no closed quiet spell of its own, or too few for its maximum to mean
// anything. It is reported and never alarmed about, the same tolerance a source
// that has never run gets.
//
// A source that has never stored an event at all is deliberately NOT here: the
// table above already says "never run" or shows what it did, and the sources
// section names it. Repeating it as an unjudged evidence row would be the report
// saying the same absence twice in two different vocabularies.
func (rep evidenceReport) unjudged() []string {
	var out []string
	for _, r := range rep.rows {
		if !r.judged && r.events > 0 {
			out = append(out, r.id)
		}
	}
	return out
}

// writeEvidence is the header line saying how old the newest evidence is.
//
// It sits beside the schedule and the staleness bound because it is the same
// kind of fact: the figure every verdict below rests on, named before any of
// them. Its absence is precisely what let a store that had not moved in twelve
// hours read as healthy — there was no line a reader could have looked at.
func writeEvidence(out io.Writer, rep evidenceReport, now time.Time) {
	if rep.frontier.IsZero() {
		fmt.Fprintf(out, "evidence:  none: no source has ever stored an event in this store\n")
		return
	}
	fmt.Fprintf(out, "evidence:  newest %s, %s ago\n",
		rep.frontier.Local().Format(time.DateTime), short(now.Sub(rep.frontier)))
	// AND IT IS NOT JUDGED, said in the same breath. Nothing in the store
	// distinguishes a machine that was idle from a collection that has stalled:
	// both leave exactly no rows, and this project has shipped that particular
	// collapse three times in three separate tools.
	fmt.Fprintln(out, "           whether the machine was idle through that or the collection has "+
		"stopped is not in this record")
}

// evidenceVerdict names the sources that have gone silent and the ones nothing
// can judge, and returns nothing.
//
// NOTHING, deliberately — this raises no error and does not touch the exit
// status. status's documented contract is that a thing OBSERVED to be wrong
// fails and a thing NEVER RECORDED is reported, and "recorded nothing while
// others recorded" is on the wrong side of that line: it is a true reading whose
// cause the report explicitly declines to name. Making it non-zero would be
// asserting a fault the store cannot support, and would put the exit status of a
// command nothing yet gates on behind a rule that reports about once a day on a
// fourteen-day record. What it DOES do is take the unqualified all-clear away,
// which is the half that actually mattered on 2026-08-13. If this should exit
// non-zero that is a deliberate change to the contract and is Ben's to make; the
// test TestASourceSilentWhileOthersRecordIsReported pins the decision so that
// changing it cannot happen by accident.
func evidenceVerdict(out io.Writer, rep evidenceReport, now time.Time) {
	if !rep.checked {
		fmt.Fprintf(out, "which sources have gone silent was not checked, because %s: nothing here "+
			"says how long a source may say nothing before it has stopped\n", rep.unchecked)
		return
	}

	if silent := rep.silent(); len(silent) > 0 {
		fmt.Fprintf(out, "%s recorded nothing while the store went on advancing:\n",
			plural(len(silent), "source"))
		for _, r := range silent {
			// The measurement the conclusion rests on, in full: how long it has
			// been, how far the store moved while it said nothing, and what it
			// is being compared against. A line that said only "silent" would be
			// a conclusion with its evidence removed.
			fmt.Fprintf(out, "  %s: nothing since %s ago, while the store advanced %s past it — "+
				"%.1f times the %s it has ever been quiet for, across %s\n",
				r.id, short(now.Sub(r.newest)), short(r.lag),
				float64(r.lag)/float64(r.quiet), short(r.quiet), plural(r.spells, "recorded quiet spell"))
			fmt.Fprintln(out, "    whether it has nothing to say or has stopped working is not in this record")
		}
	}

	if unjudged := rep.unjudged(); len(unjudged) > 0 {
		// Not an alarm, and not an all-clear either. blender-backups and
		// motion-autosave on the live store are both this: every event either of
		// them holds is a file modification time from before this machine ever
		// ran a harvest, so there is no closed quiet spell to judge them by.
		fmt.Fprintf(out, "%s recorded too little since collection began, so nothing here says what "+
			"quiet looks like for them: %s\n",
			plural(len(unjudged), "source"), strings.Join(unjudged, ", "))
	}
}

// evidenceAllClear is what the bottom line says about the store's contents once
// everything else has come back clean.
//
// It is never nothing. The whole defect was an all-clear that mentioned only
// harvests, so the age of the newest evidence is welded onto the one line a
// reader is entitled to trust absolutely, and a source that has gone silent
// replaces that line rather than qualifying it.
func evidenceAllClear(rep evidenceReport, now time.Time) string {
	if rep.frontier.IsZero() {
		return ", and no source has ever stored an event in it"
	}
	return fmt.Sprintf(", and the newest evidence in the store is %s old", short(now.Sub(rep.frontier)))
}
