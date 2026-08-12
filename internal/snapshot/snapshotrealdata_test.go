//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package snapshot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// frozenKnowledgeC copies this machine's real knowledgeC database and its
// sidecars into the test's own directory and returns the copy.
//
// The copy is what makes these tests measurements rather than coin flips. The
// real database is written continuously by a system daemon, so two Takes
// straight off it can differ for reasons that have nothing to do with the
// dedup, and a test that failed on that would be reporting the weather. What
// is frozen is nonetheless real: the actual rows, the actual sidecar state
// macOS left behind, and the actual page layout — none of which a constructed
// fixture reproduces.
//
// Nothing here writes to the real database, and nothing here writes into
// ~/.timetattle. Every file these tests produce lives under t.TempDir().
func frozenKnowledgeC(t *testing.T) string {
	t.Helper()
	src, err := DefaultSourcePath()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if err := checkReadable(src); err != nil {
		t.Skipf("cannot read the real knowledgeC: %v", err)
	}

	dst := filepath.Join(t.TempDir(), "knowledgeC.db")
	if err := stage(src, dst); err != nil {
		t.Skipf("cannot stage the real knowledgeC: %v", err)
	}
	return dst
}

// TestRealKnowledgeCTakenTwiceWritesOneFile is the real-data half of the dedup
// criterion, and the half that says the size of the prize.
//
// The constructed fixture in dedup_test.go holds 40 rows in six columns. This
// runs against whatever this machine's knowledgeC actually holds — around
// 22,000 rows across sixteen streams, in the 37-column table Core Data
// defines, compressing to about 1.5 MB — which is the population the storage
// argument was ever about.
func TestRealKnowledgeCTakenTwiceWritesOneFile(t *testing.T) {
	src := frozenKnowledgeC(t)
	dir := filepath.Join(t.TempDir(), "snapshots")

	first, err := Take(src, dir, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}
	if first.Held {
		t.Fatal("the first Take into an empty directory reported the contents already held")
	}
	second, err := Take(src, dir, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if !second.Held {
		t.Fatalf("a second Take of a frozen real knowledgeC wrote %d bytes to %s; "+
			"the extraction is not a function of the rows alone",
			second.Bytes, second.Path)
	}
	if names := snapshotNames(t, dir); len(names) != 1 {
		t.Fatalf("the snapshot directory holds %d files (%v), want 1", len(names), names)
	}
	t.Logf("MUTATION MEASUREMENT: %d rows, %d bytes written on the first run and %d on the second. "+
		"Without the digest check the second run writes %d bytes again, every run, for ever.",
		first.Rows, first.Bytes, second.Bytes, first.Bytes)
}

// TestRealKnowledgeCIsHeldAcrossAWALCheckpoint is the caveat measured against
// real bytes rather than a fixture.
//
// A checkpoint folds the write-ahead log back into the main database and
// truncates the sidecar. On the real database that rewrites a large fraction of
// a multi-megabyte file and empties a sidecar macOS had left populated, while
// changing not one ZOBJECT row. Any dedup keyed on the staged source — the
// database plus its -wal and -shm, which is what Take copies — reports
// "changed" here and skips nothing.
//
// The three duplicate pairs in the live snapshot corpus are the passive version
// of this observation: separate harvest runs, each staging the live knowledgeC
// afresh while macOS held it open, produced byte-identical compressed output
// (measured 2026-08-12, `go run ./scripts/snapchurn`: 35 distinct digests over
// 38 files). This is the active version, which does not have to wait for the
// machine to be idle to prove the same thing.
func TestRealKnowledgeCIsHeldAcrossAWALCheckpoint(t *testing.T) {
	src := frozenKnowledgeC(t)
	dir := filepath.Join(t.TempDir(), "snapshots")

	first, err := Take(src, dir, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}

	before := stagedSize(t, src)
	checkpoint(t, src)
	after := stagedSize(t, src)
	// Guard the guard: a source whose staged bytes did not change proves
	// nothing about a digest taken over them.
	if before == after {
		t.Skipf("the checkpoint did not change the staged size (%d bytes); "+
			"this copy carried no WAL, so there is nothing here to defeat", before)
	}

	second, err := Take(src, dir, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if !second.Held {
		t.Fatalf("a checkpoint of the real database defeated the dedup: %d rows before, %d after, "+
			"and a second %d-byte snapshot was written", first.Rows, second.Rows, second.Bytes)
	}
	t.Logf("staged source went from %d to %d bytes across the checkpoint and the %d preserved rows were unchanged",
		before, after, second.Rows)
}

// stagedSize is the total size of the database and its sidecars, which is what
// a whole-source digest would have been taken over.
func stagedSize(t *testing.T, src string) int64 {
	t.Helper()
	var total int64
	for _, suffix := range append([]string{""}, sidecarSuffixes...) {
		if info, err := os.Stat(src + suffix); err == nil {
			total += info.Size()
		}
	}
	return total
}

// checkpoint folds the copy's write-ahead log back into it. The copy, never
// the real database: knowledgeC belongs to macOS.
func checkpoint(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open the frozen copy: %v", err)
	}
	defer db.Close()
	if _, err := db.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		t.Fatalf("checkpoint the frozen copy: %v", err)
	}
}
