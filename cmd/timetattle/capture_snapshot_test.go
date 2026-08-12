package main

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/snapshot"
)

// The knowledgeC capture used to be recorded as "one file per run, always",
// because it was the one capture in internal/snapshot that never found
// anything already held. It now skips a write whose contents are on disk,
// which makes it the same shape as the Biome and Motion vault captures, and
// migration 0007 already reserved the columns for exactly this: files_new is
// what the run WROTE and files_held is what it found already preserved.
//
// Getting this wrong does not lose evidence, but it loses the ability to see
// it: a held capture recorded as files_new = 1 would inflate every count
// status and metrics report, and a held capture recorded as nothing at all
// would read as a harvest that never reached knowledgeC. That last collapse —
// "observed not-X" into "nothing was recorded" — is the one this repository
// has now shipped three times.

func TestARecordedKnowledgeCCaptureCountsWhatItWrote(t *testing.T) {
	c := captureBegun(captureKindKnowledgeC, "/some/knowledgeC.db").
		capturedSnapshot(snapshot.Result{Path: "/snap/a.db.gz", Rows: 21939, Bytes: 1495874})

	if c.filesNew != 1 {
		t.Errorf("filesNew = %d for a run that wrote a snapshot, want 1", c.filesNew)
	}
	if c.filesHeld != 0 {
		t.Errorf("filesHeld = %d for a run that wrote a snapshot, want 0", c.filesHeld)
	}
	if c.bytes != 1495874 {
		t.Errorf("bytes = %d, want the 1495874 the run wrote", c.bytes)
	}
}

func TestARecordedKnowledgeCCaptureCountsWhatItAlreadyHeld(t *testing.T) {
	c := captureBegun(captureKindKnowledgeC, "/some/knowledgeC.db").
		capturedSnapshot(snapshot.Result{Path: "/snap/a.db.gz", Rows: 21939, Held: true})

	if c.filesNew != 0 {
		t.Errorf("filesNew = %d for a run that wrote nothing, want 0", c.filesNew)
	}
	if c.filesHeld != 1 {
		t.Errorf("filesHeld = %d for a run that found the contents already preserved, want 1", c.filesHeld)
	}
	if c.bytes != 0 {
		t.Errorf("bytes = %d for a run that wrote nothing, want 0", c.bytes)
	}
	// A capture that preserved knowledgeC by finding it already preserved is
	// still a capture that ran and succeeded. Recording it as absent would say
	// there was no knowledgeC on this machine.
	if got := c.captureEnded(true, nil).captureVerdict(); got != "ok" {
		t.Errorf("a held knowledgeC capture reads as %q, want ok", got)
	}
}
