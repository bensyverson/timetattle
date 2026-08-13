package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/store"
)

// The knowledgeC snapshot now has a cadence of its own, so a harvest can finish
// having deliberately not preserved knowledgeC. That is a THIRD outcome for this
// capture and it has to be recorded as itself.
//
// The two it must not be confused with are both already here. A run that wrote a
// snapshot is files_new = 1. A run that read the source through and found its
// bytes already on disk is files_held = 1 — an observation ABOUT THE SOURCE,
// which is what capture_snapshot_test.go holds. A throttled run made neither
// observation: it did not read knowledgeC at all, because the schedule said not
// yet.
//
// The specific collapse being guarded against is 'absent'. A throttled capture
// has no error and preserved nothing, which is exactly the shape captureVerdict
// reads as absent — and absent means "there is no knowledgeC on this machine",
// which about a working laptop is simply false. internal/snapshot.Due and
// migration 0015 carry the rest of the argument.

func TestAThrottledCaptureIsSkippedAndNotAbsent(t *testing.T) {
	c := captureBegun(captureKindKnowledgeC, "/some/knowledgeC.db").captureThrottled()

	if got := c.captureVerdict(); got != store.CaptureSkipped {
		t.Errorf("a throttled knowledgeC capture reads as %q, want %q", got, store.CaptureSkipped)
	}
	if c.finishedAt.IsZero() {
		t.Error("a throttled capture left its span open; a capture row with no end cannot be written")
	}
}

// A throttled capture and a held one are the two readings closest together, and
// the pair this whole change exists to keep apart.
func TestAThrottledCaptureAndAHeldOneDoNotReadAlike(t *testing.T) {
	held := captureBegun(captureKindKnowledgeC, "/some/knowledgeC.db").
		capturedSnapshot(snapshot.Result{Path: "/snap/a.db.gz", Rows: 21939, Held: true}).
		captureEnded(true, nil)
	throttled := captureBegun(captureKindKnowledgeC, "/some/knowledgeC.db").captureThrottled()

	if held.captureVerdict() == throttled.captureVerdict() {
		t.Errorf("a held capture and a throttled one both read as %q; "+
			"\"knowledgeC has not changed\" and \"we have not looked\" are different facts",
			held.captureVerdict())
	}
	if throttled.filesNew != 0 || throttled.filesHeld != 0 || throttled.bytes != 0 {
		t.Errorf("a throttled capture reports %d new, %d held and %d bytes; it read nothing",
			throttled.filesNew, throttled.filesHeld, throttled.bytes)
	}
}

// The row has to survive the trip into the store, which is where the schema's
// own refusals live. A capture recorded in memory and rejected by SQLite would
// be a harvest that looks healthy and records nothing.
func TestAThrottledCaptureIsRecordedInTheStore(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "timetattle.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	out := &bytes.Buffer{}
	throttled := captureBegun(captureKindKnowledgeC, "/some/knowledgeC.db").captureThrottled()
	if err := captureRecord(ctx, st, out, []captureOutcome{throttled}); err != nil {
		t.Fatalf("captureRecord: %v\n%s", err, out)
	}

	got, err := st.Captures(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read back %d captures, want 1", len(got))
	}
	if got[0].Status != store.CaptureSkipped {
		t.Errorf("the recorded capture reads back as %q, want %q", got[0].Status, store.CaptureSkipped)
	}
	if got[0].Input != "/some/knowledgeC.db" {
		t.Errorf("the recorded capture names %q as what it did not read, want the knowledgeC path", got[0].Input)
	}
}

// captureKnowledgeC is where the throttle actually decides, so these tests drive
// it directly rather than through the CLI. now is a parameter for the same
// reason: a schedule cannot be tested against a clock nobody controls.

// throttleSource writes a knowledgeC fixture at path, replacing whatever is
// there. Calling it twice with different rows is how a test makes the source
// CHANGE between two captures, which is what separates the throttle from the
// write-time content check: over an unchanged source both suppress the write,
// and only a changed source can tell them apart.
func throttleSource(t *testing.T, path string, extra int) string {
	t.Helper()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove %s: %v", path, err)
	}
	base := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	rows := []kcRow{
		flagRow("/display/isBacklit", base, base.Add(30*time.Minute), 1),
		appRow("/app/usage", base, base.Add(20*time.Minute), "com.apple.Safari"),
	}
	for i := range extra {
		when := base.Add(time.Duration(i+2) * time.Hour)
		rows = append(rows, appRow("/app/usage", when, when.Add(time.Minute), "com.apple.Terminal"))
	}
	return writeKnowledgeC(t, path, rows...)
}

// snapshotsIn counts the knowledgeC snapshots in a directory.
func snapshotsIn(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasPrefix(e.Name(), "knowledgeC-") {
			n++
		}
	}
	return n
}

func TestTheFirstKnowledgeCCaptureWritesASnapshot(t *testing.T) {
	work := t.TempDir()
	src := throttleSource(t, filepath.Join(work, "kc.db"), 0)
	dir := filepath.Join(work, "snapshots")

	kc := captureKnowledgeC(src, dir, time.Now(), 4*time.Hour)
	if kc.err != nil {
		t.Fatalf("captureKnowledgeC: %v", kc.err)
	}
	if got := kc.outcome.captureVerdict(); got != store.CaptureOK {
		t.Errorf("the first capture reads as %q, want %q", got, store.CaptureOK)
	}
	if kc.outcome.filesNew != 1 {
		t.Errorf("the first capture wrote %d files, want 1", kc.outcome.filesNew)
	}
	if _, err := os.Stat(kc.snapshot); err != nil {
		t.Errorf("the first capture named a snapshot that cannot be read: %v", err)
	}
}

// The measurement the throttle exists for, with the write-time content check
// ruled out: the source really does change between the two captures, so a run
// that read it would have written a second file.
func TestAKnowledgeCCaptureInsideTheIntervalIsThrottled(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "kc.db")
	src := throttleSource(t, path, 0)
	dir := filepath.Join(work, "snapshots")
	now := time.Now()

	first := captureKnowledgeC(src, dir, now, 4*time.Hour)
	if first.err != nil {
		t.Fatalf("first capture: %v", first.err)
	}

	throttleSource(t, path, 3)
	second := captureKnowledgeC(src, dir, now.Add(time.Hour), 4*time.Hour)
	if second.err != nil {
		t.Fatalf("second capture: %v", second.err)
	}

	if got := second.outcome.captureVerdict(); got != store.CaptureSkipped {
		t.Errorf("a capture one hour into a four-hour interval reads as %q, want %q", got, store.CaptureSkipped)
	}
	if n := snapshotsIn(t, dir); n != 1 {
		t.Errorf("%d snapshots on disk after a throttled second capture, want 1", n)
	}
	// The throttled run still has to name the snapshot the four knowledgeC
	// streams are ingested from. Without it they get no harvest_run row on three
	// runs in four, and a source with no row cannot be told from one that ran
	// and found nothing.
	if second.snapshot != first.snapshot {
		t.Errorf("a throttled capture named %q as the snapshot to read, want the held %q",
			second.snapshot, first.snapshot)
	}
}

// The control for the test above. Same changed source, no throttle: the second
// capture writes. Without this the first test would pass just as well if the
// write-time content check were doing the work and the throttle nothing.
func TestWithNoThrottleAChangedSourceWritesASecondSnapshot(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "kc.db")
	src := throttleSource(t, path, 0)
	dir := filepath.Join(work, "snapshots")
	now := time.Now()

	if kc := captureKnowledgeC(src, dir, now, 0); kc.err != nil {
		t.Fatalf("first capture: %v", kc.err)
	}
	throttleSource(t, path, 3)
	second := captureKnowledgeC(src, dir, now.Add(time.Second), 0)
	if second.err != nil {
		t.Fatalf("second capture: %v", second.err)
	}
	if got := second.outcome.captureVerdict(); got != store.CaptureOK {
		t.Errorf("an unthrottled capture reads as %q, want %q", got, store.CaptureOK)
	}
	if n := snapshotsIn(t, dir); n != 2 {
		t.Errorf("%d snapshots on disk with no throttle and a changed source, want 2", n)
	}
}

func TestAKnowledgeCCaptureIsNotThrottledOnceTheIntervalHasPassed(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "kc.db")
	src := throttleSource(t, path, 0)
	dir := filepath.Join(work, "snapshots")
	now := time.Now()

	if kc := captureKnowledgeC(src, dir, now, 4*time.Hour); kc.err != nil {
		t.Fatalf("first capture: %v", kc.err)
	}
	throttleSource(t, path, 3)
	second := captureKnowledgeC(src, dir, now.Add(4*time.Hour), 4*time.Hour)
	if second.err != nil {
		t.Fatalf("second capture: %v", second.err)
	}
	if got := second.outcome.captureVerdict(); got != store.CaptureOK {
		t.Errorf("a capture four hours after the last reads as %q, want %q", got, store.CaptureOK)
	}
	if n := snapshotsIn(t, dir); n != 2 {
		t.Errorf("%d snapshots on disk once the interval had passed, want 2", n)
	}
}

// TestAThrottledCaptureStillNoticesAnUnreadableSource is the property the
// throttle nearly destroyed, and it was the existing
// TestAFailedCaptureDoesNotIngestAnEarlierSnapshot that caught it.
//
// A run that declines to take a snapshot must still open knowledgeC. macOS pins
// Full Disk Access to a binary's code signature and silently revokes it on
// rebuild, and that revocation is invisible except when something tries to read
// the database. A throttle that skipped the read as well would hold the news for
// a whole interval, ingest the held snapshot, and report a clean harvest the
// whole time — which is the 44-hour outage of 2026-07-30 in miniature, arriving
// by a new route. The check costs one os.Open and nothing else: nothing is
// staged, extracted or written.
func TestAThrottledCaptureStillNoticesAnUnreadableSource(t *testing.T) {
	work := t.TempDir()
	path := filepath.Join(work, "kc.db")
	src := throttleSource(t, path, 0)
	dir := filepath.Join(work, "snapshots")
	now := time.Now()

	if kc := captureKnowledgeC(src, dir, now, 4*time.Hour); kc.err != nil {
		t.Fatalf("first capture: %v", kc.err)
	}
	if err := os.Chmod(src, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(src, 0o600) })

	kc := captureKnowledgeC(src, dir, now.Add(time.Hour), 4*time.Hour)
	if kc.err == nil {
		t.Fatal("a throttled capture reported nothing wrong with a knowledgeC it cannot open")
	}
	if got := kc.outcome.captureVerdict(); got != store.CaptureFailed {
		t.Errorf("a throttled capture over an unreadable source reads as %q, want %q", got, store.CaptureFailed)
	}
	if kc.snapshot != "" {
		t.Errorf("a capture that could not open its source named %q as a snapshot to ingest; "+
			"re-reading the last one would report ok for every stream and hide the outage", kc.snapshot)
	}
}

// A snapshot directory that cannot be read must fail the capture, never
// throttle it. Read as "not due", a permissions problem would stop preserving
// the one source no later run can recover, for ever, and report a healthy
// throttled harvest while doing it.
func TestAnUnreadableSnapshotDirectoryFailsTheCaptureRatherThanThrottlingIt(t *testing.T) {
	work := t.TempDir()
	src := throttleSource(t, filepath.Join(work, "kc.db"), 0)
	dir := filepath.Join(work, "snapshots")

	if kc := captureKnowledgeC(src, dir, time.Now(), 4*time.Hour); kc.err != nil {
		t.Fatalf("first capture: %v", kc.err)
	}
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	kc := captureKnowledgeC(src, dir, time.Now().Add(time.Hour), 4*time.Hour)
	if kc.err == nil {
		t.Fatal("an unreadable snapshot directory produced no error")
	}
	if got := kc.outcome.captureVerdict(); got != store.CaptureFailed {
		t.Errorf("an unreadable snapshot directory reads as %q, want %q", got, store.CaptureFailed)
	}
	if kc.snapshot != "" {
		t.Errorf("a failed capture named %q as a snapshot to ingest", kc.snapshot)
	}
}
