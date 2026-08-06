package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

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

// captureEnded closes the span and settles what happened.
func (c captureOutcome) captureEnded(preserved bool, err error) captureOutcome {
	c.finishedAt = time.Now()
	c.preserved = preserved
	c.err = err
	return c
}

// captureVerdict is the four-way reading of one capture, and the reason the
// third and fourth values exist at all.
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
