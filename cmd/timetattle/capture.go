package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/store"
)

// The capture phase's vocabulary, shared by the subcommand that writes it and
// the one that reads it back.
//
// harvest runs the four captures; status reports what they did. Neither owns
// the identifiers, so under the naming rule in main.go's package doc they live
// in a file named for the concern rather than in whichever subcommand happened
// to need them first. Everything here is prefixed capture, because Go package
// scope spans this whole directory.
//
// # Why the outcome is recorded after the fact
//
// Nothing here runs until the store is open, and the store is opened AFTER all
// four captures. That ordering is the point rather than a convenience. Capture
// runs first in a harvest because it is the one phase that cannot be repeated —
// knowledgeC and Biome prune themselves, and evidence not preserved inside the
// window is gone — so a database that is locked, corrupt or slow to open must
// never be able to delay or prevent it. Recording afterwards, from a start and
// an end measured around each capture, keeps that independence total.
//
// The cost is that a harvest killed mid-capture records nothing at all, so the
// captures that had already succeeded go unremarked. That degrades to "no
// capture recorded", which status prints in those words and which reads as a
// problem — rather than to a row claiming to be live for ever, which is what
// harvest_run's nullable finished_at leaves behind and which the live store is
// carrying right now. See migration 0007.
const (
	captureKindKnowledgeC = "knowledgec"
	captureKindBiome      = "biome"
	captureKindVault      = "motion-vault"
	captureKindFiledays   = "filedays"
)

// captureOutcome is what one capture did, held in memory until the store is
// open.
type captureOutcome struct {
	kind       string
	input      string
	startedAt  time.Time
	finishedAt time.Time
	// preserved is whether the capture found the thing it was asked to preserve
	// and got hold of any of it. It is what separates absent from ok, and
	// partial from failed, and it is a different question from "did it write a
	// file": a Biome stream every one of whose segments was already held has
	// preserved its stream and written nothing.
	preserved bool
	// throttled is whether this capture was DECLINED rather than attempted,
	// because the source has a cadence of its own and it is not due yet. It is
	// separate from preserved and from err on purpose: a throttled capture has
	// neither, and the two fields together read as "absent", which about a
	// working laptop would be a machine reporting knowledgeC is not installed.
	throttled bool
	err       error
	filesNew  int
	filesHeld int
	bytes     int64
}

// captureBegun starts an outcome. The caller fills in the counts and calls
// captureEnded once the capture returns.
func captureBegun(kind, input string) captureOutcome {
	return captureOutcome{kind: kind, input: input, startedAt: time.Now()}
}

// capturedSnapshot fills in what a knowledgeC capture did with the files.
//
// It exists as its own method rather than two lines in harvest because a held
// capture and a written one differ only in which counter moves, and putting
// that decision where it can be tested is cheaper than discovering it wrong in
// a status report months later. Migration 0007 defines the two columns: a
// capture that found the contents already preserved wrote no file and no
// bytes, and files_held = 1 is what says it looked.
func (c captureOutcome) capturedSnapshot(res snapshot.Result) captureOutcome {
	if res.Held {
		c.filesHeld = 1
		return c
	}
	c.filesNew, c.bytes = 1, res.Bytes
	return c
}

// captureEnded closes the span and settles what happened.
func (c captureOutcome) captureEnded(preserved bool, err error) captureOutcome {
	c.finishedAt = time.Now()
	c.preserved = preserved
	c.err = err
	return c
}

// captureThrottled closes the span of a capture the harvest declined to make.
//
// Only knowledgeC has a cadence of its own, and it has one because the two
// halves of a harvest protect against different losses. The mtime sources decay
// continuously — a file records only its LAST modification, so evidence not
// scanned before the file is touched again is gone and no later run recovers it
// — which is why the harvest runs hourly. knowledgeC prunes by volume instead,
// so everything still inside its window comes back whole on the next harvest,
// while a snapshot of it is about 1.5 MB and the most expensive thing in the
// phase. internal/snapshot.Due holds the schedule and the measurement.
//
// The span is still closed and the row still written. That is the whole point:
// the throttle must be visible as a DECISION taken on time, not as a capture
// that quietly stopped happening. Biome and the Motion vault are never
// throttled — both are content-addressed already, so a run over an unchanged
// source writes nothing anyway — and neither is the filedays scan, which is the
// source frequency exists to protect.
func (c captureOutcome) captureThrottled() captureOutcome {
	c.finishedAt = time.Now()
	c.throttled = true
	return c
}

// knowledgeCCapture is what the knowledgeC capture did, and which snapshot the
// rest of the harvest should read.
type knowledgeCCapture struct {
	outcome captureOutcome
	// snapshot is the file the four knowledgeC streams are ingested from: the
	// one this run wrote, the one it found already holding those bytes, or —
	// when the throttle declined — the one an earlier run left. It is empty only
	// when the capture failed, and then nothing is read at all.
	snapshot string
	// rows is what the source held, and is reported on a taken capture only. A
	// throttled run did not count anything, and a zero here would otherwise be
	// indistinguishable from a source that had gone empty.
	rows int
	err  error
}

// captureKnowledgeC preserves knowledgeC unless its own schedule says not yet.
//
// # Why this is a function rather than six lines in harvest
//
// Because it is the one place in the harvest that makes a scheduling DECISION,
// and a decision that cannot be tested against a clock the test controls is a
// decision nobody has checked. now is a parameter for exactly that reason.
//
// # The three outcomes, and why the third is not either of the other two
//
//	took       a snapshot was written. files_new = 1.
//	held       the source was read through, extracted and digested, and a
//	           snapshot already on disk holds those exact bytes. files_held = 1.
//	           This is an observation ABOUT THE SOURCE: knowledgeC has not
//	           changed.
//	throttled  the source was not read at all, because its own cadence says the
//	           next snapshot is not due. This is a decision about the SCHEDULE,
//	           taken before anything was opened. It says nothing whatever about
//	           what knowledgeC holds.
//
// The last two are the pair that would be easiest to collapse and the pair that
// must not be. "knowledgeC has not changed in four hours" and "we have not
// looked in four hours" are different facts, and a status report that printed
// them alike would be reporting a healthy source it had stopped watching.
// store.CaptureSkipped and migration 0015 are what let the third be written
// down.
//
// # Why a throttled run still names a snapshot
//
// So the four knowledgeC streams keep getting a harvest_run row on every
// harvest. Ingestion is idempotent — the store deduplicates by content, and
// re-reading a snapshot adds nothing — so reading the held one costs a parse and
// buys the property that a source which reported nothing new and a source that
// was never read do not render alike. It is also honest in the strict sense
// ingest.go asks for: the run really did read the file it records as its input.
//
// The failure case is deliberately different. lostSnapshotIngestions does NOT
// reach for the snapshot an earlier run left, because a capture that FAILED
// would then report ok for every stream and the day it lost would be invisible.
// A throttle is not a loss, so the two get opposite treatment.
func captureKnowledgeC(src, dir string, now time.Time, every time.Duration) knowledgeCCapture {
	c := captureBegun(captureKindKnowledgeC, src)

	// A snapshot directory that cannot be read fails the capture; it never
	// throttles it. Read as "not due", a permissions problem would stop
	// preserving the one source no later run can recover, for ever, while
	// reporting a healthy throttled harvest. snapshot.Due takes the same
	// position and says why.
	due, err := snapshot.Due(dir, now, every)
	if err != nil {
		return knowledgeCCapture{outcome: c.captureEnded(false, err), err: err}
	}
	if !due.Take {
		// A throttled run still OPENS knowledgeC, even though it will read
		// nothing out of it. macOS pins Full Disk Access to a binary's code
		// signature and revokes it silently on rebuild, and that revocation is
		// invisible until something opens the database. Skipping the open with
		// the snapshot would hold the news for a whole interval, ingest the
		// snapshot already on disk, and print a clean harvest the whole time —
		// which is the outage of 2026-07-30 arriving by a new route. One
		// os.Open closes that, and a source that cannot be opened is a failed
		// capture rather than a throttled one.
		if err := snapshot.Readable(src); err != nil {
			return knowledgeCCapture{outcome: c.captureEnded(false, err), err: err}
		}
		return knowledgeCCapture{outcome: c.captureThrottled(), snapshot: due.Newest}
	}

	res, err := snapshot.Take(src, dir, now)
	if err != nil {
		return knowledgeCCapture{outcome: c.captureEnded(false, err), err: err}
	}
	return knowledgeCCapture{
		outcome:  c.capturedSnapshot(res).captureEnded(true, nil),
		snapshot: res.Path,
		rows:     res.Rows,
	}
}

// report writes one line about what the knowledgeC capture did, in the three
// shapes it can take. The throttled line names the snapshot that is being read
// instead, so that a reader of ~/.timetattle/harvest.log can see the decision
// rather than an unexplained gap where a capture used to be.
func (k knowledgeCCapture) report(out io.Writer) {
	switch {
	case k.err != nil:
		fmt.Fprintf(out, "knowledgeC: capture lost (%v)\n", k.err)
	case k.outcome.throttled:
		fmt.Fprintf(out, "%s\nnot due yet, reading the snapshot already held\n", k.snapshot)
	case k.outcome.filesHeld > 0:
		fmt.Fprintf(out, "%s\n%d rows, already held\n", k.snapshot, k.rows)
	default:
		fmt.Fprintf(out, "%s\n%d rows, %.1f MB\n", k.snapshot, k.rows, float64(k.outcome.bytes)/(1<<20))
	}
}

// captureVerdict is the five-way reading of one capture, and the reason the
// last three values exist at all.
//
// Throttled is tested FIRST, and the order is load-bearing. A capture the
// harvest declined has no error and preserved nothing, which is precisely the
// shape the absent arm below reads — so a verdict that checked absent first
// would record "there is no knowledgeC on this machine" three runs in four.
//
// An error with something preserved is partial, not failed: the Biome capture
// takes one stream at a time and the filedays scan one root at a time, and both
// are best-effort, so losing one must not report the others as lost too.
//
// No error and nothing preserved is absent, not ok and not failed. A machine
// with no Motion installed has no vault, and a machine with no filedays roots
// configured has nothing to scan. Recording either as a success would claim a
// preservation that never happened; recording it as a failure would cry wolf at
// an ordinary machine. Recording nothing — which is what happened before — makes
// it indistinguishable from a harvest that died before it got this far.
func (c captureOutcome) captureVerdict() store.CaptureStatus {
	switch {
	case c.throttled:
		return store.CaptureSkipped
	case c.err != nil && c.preserved:
		return store.CapturePartial
	case c.err != nil:
		return store.CaptureFailed
	case !c.preserved:
		return store.CaptureAbsent
	default:
		return store.CaptureOK
	}
}

// captureRecord writes every outcome to the store.
//
// One failed write does not abandon the rest, for the same reason the captures
// themselves do not: these are four independent facts, and losing the record of
// three because the fourth could not be written would maximise the loss.
//
// A failure to record is reported and returned rather than swallowed. It is
// exactly the class of silence this task exists to remove, and a capture whose
// outcome could not be stored is a capture status will report as never having
// happened.
func captureRecord(ctx context.Context, st *store.Store, out io.Writer, outcomes []captureOutcome) error {
	var errs []error
	for _, o := range outcomes {
		status := o.captureVerdict()
		c := store.Capture{
			Kind:       o.kind,
			Input:      o.input,
			StartedAt:  o.startedAt,
			FinishedAt: o.finishedAt,
			Status:     status,
			FilesNew:   o.filesNew,
			FilesHeld:  o.filesHeld,
			Bytes:      o.bytes,
		}
		// Only the two statuses that lost something carry a reason; 0007
		// refuses an ok or absent row holding one, because a record that both
		// succeeded and failed cannot be acted on.
		if status == store.CaptureFailed || status == store.CapturePartial {
			c.Error = o.err.Error()
		}
		if _, err := st.RecordCapture(ctx, c); err != nil {
			errs = append(errs, fmt.Errorf("record what the %s capture did: %w", o.kind, err))
			fmt.Fprintf(out, "%s: capture outcome not recorded (%v)\n", o.kind, err)
		}
	}
	return errors.Join(errs...)
}
