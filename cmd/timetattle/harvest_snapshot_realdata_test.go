//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package main

import (
	"context"
	"io"
	"testing"

	"github.com/bensyverson/timetattle/internal/knowledgec"
	"github.com/bensyverson/timetattle/internal/snapshot"
)

// liveSnapshotDir is where THIS machine's harvests were written.
//
// It exists because this package's TestMain points HOME at an empty temp
// directory for the whole binary, deliberately, so that no test can write into
// ~/.timetattle. That protection had a cost nobody had noticed: it made
// snapshot.DefaultDir() resolve inside the temp home, so the one test here that
// reads a real harvest skipped on EVERY run, on Ben's machine included, and had
// done since TestMain landed. Measured 2026-08-02 with
// `go test ./cmd/timetattle/ -run TestIngestingTheNewestRealSnapshotTwiceAddsNothing -v`,
// which reported SKIP with a real store sitting on the disk. A tagged test that
// can never run is the same fail-open the tag exists to end, so it is fixed
// here rather than carried across.
//
// The real HOME is restored for the length of this test only, through t.Setenv,
// which testing puts back afterwards — so the resolver stays the single source
// of the path (a second hardcoded ~/.timetattle/snapshots would drift) while
// every other test in the binary keeps the empty home TestMain gave it.
func liveSnapshotDir(t *testing.T) string {
	t.Helper()
	if realHome == "" {
		t.Skip("the suite started with no HOME, so there is no machine to read")
	}
	t.Setenv("HOME", realHome)
	dir, err := snapshot.DefaultDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	return dir
}

// The same claim TestIngestingASnapshotTwiceAddsNothing makes against a fixture,
// restated against whatever the last harvest really wrote. A fixture cannot say
// whether it holds over tens of thousands of rows a machine produced, including
// the ones it repeats and contradicts.
func TestIngestingTheNewestRealSnapshotTwiceAddsNothing(t *testing.T) {
	dir := liveSnapshotDir(t)
	path, err := knowledgec.NewestSnapshot(dir)
	if err != nil {
		t.Skipf("NewestSnapshot(%s): %v", dir, err)
	}

	st, storePath := newStore(t)
	ctx := context.Background()

	if err := ingest(ctx, st, snapshotIngestions(path), io.Discard); err != nil {
		t.Fatalf("first ingestion of %s: %v", path, err)
	}
	rows := eventCount(t, storePath)
	if rows == 0 {
		t.Skipf("%s holds no rows in any of the four streams", path)
	}
	t.Logf("%s: %d rows in the store after one ingestion", path, rows)

	if err := ingest(ctx, st, snapshotIngestions(path), io.Discard); err != nil {
		t.Fatalf("second ingestion of %s: %v", path, err)
	}
	if after := eventCount(t, storePath); after != rows {
		t.Errorf("the store grew from %d rows to %d on a re-ingestion of %s", rows, after, path)
	}
	for _, r := range harvestRuns(t, storePath)[4:] {
		if r.added != 0 {
			t.Errorf("%s: re-ingesting %s added %d rows, want 0", r.sourceID, path, r.added)
		}
	}
}
