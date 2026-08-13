package store_test

import (
	"context"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/store"
)

// The fifth capture outcome, in Go. Migration 0015 carries the argument for why
// a run that declined to preserve knowledgeC by POLICY is not ok, not absent,
// and not the content-addressed reading of nothing new (FilesNew 0 with
// FilesHeld 1). These tests hold the same three-way property the rest of this
// file does: "we read it and found nothing new", "we chose not to read it yet"
// and "no capture ran" are three facts, and no two of them may store alike.

// aSkippedCapture is what the harvest records when the snapshot throttle says
// not yet: the span and the subject of a real capture, and no counters, because
// nothing was read.
func aSkippedCapture() store.Capture {
	c := aCapture()
	c.Status = store.CaptureSkipped
	c.FilesNew, c.FilesHeld, c.Bytes = 0, 0, 0
	return c
}

func TestASkippedCaptureReadsBackAsItself(t *testing.T) {
	f := newStore(t)
	record(t, f, aSkippedCapture())

	got, err := f.Captures(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read back %d captures, want 1", len(got))
	}
	if got[0].Status != store.CaptureSkipped {
		t.Errorf("a throttled capture read back as %q, want %q", got[0].Status, store.CaptureSkipped)
	}
	if got[0].Error != "" {
		t.Errorf("a throttled capture read back carrying the error %q", got[0].Error)
	}
}

// The distinction the whole migration exists for, held as a comparison rather
// than as a comment: a run that read knowledgeC through and found its bytes
// already on disk, and a run that never read it, must not read back alike.
func TestAThrottledCaptureAndAHeldOneDoNotReadAlike(t *testing.T) {
	f := newStore(t)

	held := aCapture()
	held.FilesNew, held.FilesHeld, held.Bytes = 0, 1, 0
	record(t, f, held)
	record(t, f, aSkippedCapture())

	got, err := f.Captures(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read back %d captures, want 2", len(got))
	}
	if got[0].Status == got[1].Status {
		t.Errorf("a held capture and a throttled one both read back as %q; "+
			"\"knowledgeC has not changed\" and \"we have not looked\" are different facts", got[0].Status)
	}
}

// A skip lost nothing, so it has nothing to explain, and an error on the row
// would turn "the throttle said not yet" into "knowledgeC could not be read".
// RecordCapture refuses it in a sentence; migration 0015 refuses it in a CHECK.
// Both layers exist because Go can be bypassed and SQL cannot.
func TestRecordCaptureRefusesASkipCarryingAnError(t *testing.T) {
	f := newStore(t)
	c := aSkippedCapture()
	c.Error = "cannot read knowledgeC.db"
	if _, err := f.RecordCapture(context.Background(), c); err == nil {
		t.Error("RecordCapture accepted a skipped capture carrying an error")
	} else if !strings.Contains(err.Error(), "skipped") {
		t.Errorf("the refusal does not say which status it refused: %v", err)
	}
}

// A run that declined to read the source cannot have preserved anything from
// it. Without this the fifth status could be stamped over a row that really did
// write a snapshot, and every file and byte total would disagree with the
// directory.
func TestRecordCaptureRefusesASkipThatClaimsToHavePreservedSomething(t *testing.T) {
	f := newStore(t)
	for _, c := range []struct {
		what   string
		change func(*store.Capture)
	}{
		{"wrote a file", func(c *store.Capture) { c.FilesNew = 1 }},
		{"held a file", func(c *store.Capture) { c.FilesHeld = 1 }},
		{"produced bytes", func(c *store.Capture) { c.Bytes = 1_500_000 }},
	} {
		capture := aSkippedCapture()
		c.change(&capture)
		if _, err := f.RecordCapture(context.Background(), capture); err == nil {
			t.Errorf("RecordCapture accepted a skipped capture that %s", c.what)
		}
	}
}

// The refusal of an unknown status has to name every value that IS allowed, or
// the caller is told what is wrong without being told what is right. It went
// stale the moment a fifth value existed, which is exactly the kind of copy this
// project has watched drift.
func TestRecordCaptureNamesEveryStatusWhenItRefusesOne(t *testing.T) {
	f := newStore(t)
	c := aCapture()
	c.Status = "throttled"
	_, err := f.RecordCapture(context.Background(), c)
	if err == nil {
		t.Fatal("RecordCapture accepted the status \"throttled\"")
	}
	for _, want := range []store.CaptureStatus{
		store.CaptureOK, store.CapturePartial, store.CaptureFailed,
		store.CaptureAbsent, store.CaptureSkipped,
	} {
		if !strings.Contains(err.Error(), string(want)) {
			t.Errorf("the refusal does not mention %q, so a caller cannot tell what is allowed: %v", want, err)
		}
	}
}
