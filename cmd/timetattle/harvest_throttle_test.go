package main

import (
	"context"
	"testing"

	"github.com/bensyverson/timetattle/internal/store"
)

// The end-to-end shape of the throttle, through the CLI, because that is where
// the flag and the ingestion plan are wired together and neither can be checked
// from captureKnowledgeC alone.
//
// The property that matters most here is the one that is easiest to lose: the
// four knowledgeC streams must keep getting a harvest_run row on EVERY harvest,
// including the three runs in four that take no snapshot. Without it, a source
// with no row would be indistinguishable from a source that ran and found
// nothing — which is the standing lesson in CLAUDE.md — and `timetattle status`
// would call every knowledgeC stream stale against a bound derived from an
// hourly schedule while the collector was working perfectly.

// lastKnowledgeCCapture is the status of the most recent recorded knowledgeC
// capture, which is what says whether a given harvest was throttled.
func lastKnowledgeCCapture(t *testing.T, storePath string) store.CaptureStatus {
	t.Helper()
	st, err := store.Open(storePath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	captures, err := st.Captures(context.Background(), store.Filter{Source: captureKindKnowledgeC})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(captures) == 0 {
		t.Fatal("no knowledgeC capture was recorded at all")
	}
	return captures[len(captures)-1].Status
}

func TestAThrottledHarvestStillRecordsEveryKnowledgeCStream(t *testing.T) {
	p := newPartialHarvest(t)
	src := populatedSnapshot(t)

	// Two harvests back to back, which is what an hourly schedule looks like
	// against a four-hour snapshot interval: the second is inside the interval
	// and takes nothing.
	if err := p.run(t, src); err != nil {
		t.Logf("the first harvest reported: %v", err)
	}
	first := harvestRuns(t, p.storePath)
	if err := p.run(t, src); err != nil {
		t.Logf("the second harvest reported: %v", err)
	}
	second := harvestRuns(t, p.storePath)[len(first):]

	// The second harvest must genuinely have been throttled, or the rest of this
	// test is about an ordinary harvest and proves nothing. The snapshot count is
	// deliberately NOT the check here: this fixture's source does not change
	// between the two runs, so the write-time content check would hold the second
	// write anyway and a count of 1 would be satisfied either way.
	// TestAKnowledgeCCaptureInsideTheIntervalIsThrottled is where a CHANGED
	// source separates the two mechanisms.
	if got := lastKnowledgeCCapture(t, p.storePath); got != store.CaptureSkipped {
		t.Fatalf("the second harvest's knowledgeC capture is %q, want %q; nothing here was throttled",
			got, store.CaptureSkipped)
	}

	recorded := map[string]recordedRun{}
	for _, r := range second {
		recorded[r.sourceID] = r
	}
	for _, id := range []string{kcBacklight, kcLocked, kcAppUsage, kcMediaUsage} {
		r, ok := recorded[id]
		if !ok {
			t.Errorf("the throttled harvest recorded no run for %s; a source with no row cannot be "+
				"told from one that ran and found nothing", id)
			continue
		}
		if r.status != string(store.StatusOK) {
			t.Errorf("%s: status %q on a throttled harvest, want %q (it read the snapshot already held)",
				id, r.status, store.StatusOK)
		}
		// Re-reading the held snapshot adds nothing, because the store
		// deduplicates by content. That zero is a measurement and not an
		// absence: seen is non-zero beside it.
		if r.seen == 0 {
			t.Errorf("%s: seen=0 on a throttled harvest; it did not read the held snapshot at all", id)
		}
		if r.added != 0 {
			t.Errorf("%s: added=%d re-reading a snapshot already ingested, want 0", id, r.added)
		}
	}
}

// The capture_run side of the same two harvests. The two rows must not read
// alike: one preserved knowledgeC and one declined to, and a report that printed
// them the same would be reporting a source it had stopped watching.
func TestTwoHarvestsRecordACaptureAndThenASkip(t *testing.T) {
	p := newPartialHarvest(t)
	src := populatedSnapshot(t)
	for range 2 {
		if err := p.run(t, src); err != nil {
			t.Logf("harvest reported: %v", err)
		}
	}

	st, err := store.Open(p.storePath)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()

	captures, err := st.Captures(context.Background(), store.Filter{Source: captureKindKnowledgeC})
	if err != nil {
		t.Fatalf("Captures: %v", err)
	}
	if len(captures) != 2 {
		t.Fatalf("%d knowledgeC captures recorded across two harvests, want 2", len(captures))
	}
	if captures[0].Status != store.CaptureOK || captures[0].FilesNew != 1 {
		t.Errorf("the first capture is %q with %d files written, want ok with 1",
			captures[0].Status, captures[0].FilesNew)
	}
	if captures[1].Status != store.CaptureSkipped {
		t.Errorf("the second capture is %q, want %q; it took no snapshot because the interval had not passed",
			captures[1].Status, store.CaptureSkipped)
	}
	if captures[1].FilesHeld != 0 {
		t.Errorf("the throttled capture reports %d files held; it read nothing, and \"knowledgeC has not "+
			"changed\" is a different fact from \"we have not looked\"", captures[1].FilesHeld)
	}
	if captures[1].FinishedAt.Before(captures[1].StartedAt) {
		t.Error("the throttled capture recorded an end before its start")
	}
}
