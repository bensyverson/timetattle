package snapshot

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// snapshotNames lists the finished snapshots in dir, so a test can assert what
// a run left behind rather than only what it returned.
func snapshotNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".db.gz") {
			out = append(out, e.Name())
		}
	}
	return out
}

// writer opens a second connection to the fixture so a test can change the
// source between two Takes. liveDB's own connection is pinned and returned
// only as a release func, so this is how a test writes to it.
func writer(t *testing.T, path string) (func(string, ...any), func()) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=wal_autocheckpoint(0)")
	if err != nil {
		t.Fatalf("open %s for writing: %v", path, err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin a write connection: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}
	return exec, func() { conn.Close(); db.Close() }
}

// TestTakeHoldsASnapshotWhoseContentsAreAlreadyPreserved is the whole point of
// the change: an unchanged source must not cost a second 1.5 MB file.
//
// Before this, knowledgeC was the one capture in this package with no such
// check — Biome segments and Motion vault files are both skipped on a digest
// match — so a harvest run over a quiet hour wrote a byte-for-byte repeat of
// the previous hour. Three such repeats sit in the live corpus today.
func TestTakeHoldsASnapshotWhoseContentsAreAlreadyPreserved(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snaps := filepath.Join(dir, "snapshots")

	first, err := Take(src, snaps, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}
	if first.Held {
		t.Fatal("the first Take into an empty directory reported the contents already held")
	}

	second, err := Take(src, snaps, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if !second.Held {
		t.Error("Take wrote a second snapshot of an unchanged source; Result.Held = false")
	}
	if second.Path != first.Path {
		t.Errorf("Take reported %s, want the snapshot already holding those bytes, %s",
			second.Path, first.Path)
	}
	if second.Bytes != 0 {
		t.Errorf("Result.Bytes = %d for a run that wrote nothing, want 0", second.Bytes)
	}
	// Rows is what the source held, not what was written, so it stays a real
	// count: a held capture that reported 0 rows would be indistinguishable
	// from a source that had gone empty.
	if second.Rows != first.Rows {
		t.Errorf("Result.Rows = %d on the held run, want the %d the source still holds",
			second.Rows, first.Rows)
	}
	if names := snapshotNames(t, snaps); len(names) != 1 {
		t.Errorf("the snapshot directory holds %d files (%v), want 1", len(names), names)
	}
}

// TestTakeWritesAgainWhenOneRowChanges is the other half, and it is the half
// that matters more: a dedup check that skips a changed source destroys
// evidence that cannot be recovered by any means.
func TestTakeWritesAgainWhenOneRowChanges(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snaps := filepath.Join(dir, "snapshots")

	first, err := Take(src, snaps, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}

	exec, done := writer(t, src)
	exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
		VALUES ('/display/isBacklit', NULL, 1, 9999, 9999)`)
	done()

	second, err := Take(src, snaps, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if second.Held {
		t.Fatal("Take skipped a source that had gained a row; that row is now unpreserved")
	}
	if second.Path == first.Path {
		t.Errorf("the changed source was written over %s rather than beside it", first.Path)
	}
	if second.Rows != first.Rows+1 {
		t.Errorf("Result.Rows = %d, want %d", second.Rows, first.Rows+1)
	}
	if names := snapshotNames(t, snaps); len(names) != 2 {
		t.Errorf("the snapshot directory holds %d files (%v), want 2", len(names), names)
	}
	// Both survive under names that say what is inside them, so the two
	// versions are told apart by their contents rather than only by when they
	// were taken. Biome and the Motion vault already store two versions of one
	// name this way.
	if a, b := digestInName(t, first.Path), digestInName(t, second.Path); a == b {
		t.Errorf("both snapshots are named for the digest %q, so the changed contents were not distinguished", a)
	}
}

// digestInName reads the digest out of a snapshot's name. It fails rather
// than returning the whole name, so a test cannot silently compare two
// timestamps and conclude something about contents.
func digestInName(t *testing.T, path string) string {
	t.Helper()
	name := strings.TrimSuffix(filepath.Base(path), ".db.gz")
	i := strings.LastIndexByte(name, '-')
	if i < 0 || len(name)-i-1 != snapshotDigestLength {
		t.Fatalf("snapshot %q is not named for its contents", filepath.Base(path))
	}
	return name[i+1:]
}

// TestTakeIsNotDefeatedByAChangedWALOverUnchangedRows is the caveat that sinks
// the obvious implementation.
//
// A knowledgeC snapshot is a SQLite database PLUS its -wal and -shm sidecars,
// and Take stages all three. Those staged bytes are not a function of the rows:
// a checkpoint folds the WAL back into the main file and truncates the sidecar,
// rewriting essentially every page of both while not changing a single ZOBJECT
// row, and a write to a table the snapshot deliberately discards
// (ZSTRUCTUREDMETADATA) appends WAL frames for rows that will never be
// preserved. A digest taken over the staged source therefore reports "changed"
// across both, and the dedup would skip nothing while looking like it worked.
//
// The digest is taken over the EXTRACTED database instead — the ZOBJECT rows
// this package actually keeps — so the extraction is what normalises the
// source, and this test is what pins that choice down.
func TestTakeIsNotDefeatedByAChangedWALOverUnchangedRows(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snaps := filepath.Join(dir, "snapshots")

	if _, err := Take(src, snaps, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	staged := stagedBytes(t, src)

	exec, done := writer(t, src)
	// Neither of these changes a ZOBJECT row. Both change the staged bytes.
	exec(`INSERT INTO ZSTRUCTUREDMETADATA VALUES (2, 'https://example.com/also-private', X'C0FFEE')`)
	exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
		VALUES ('/app/webUsage', 'com.apple.Safari', 1, 4242, 4242)`)
	exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	done()

	// Guard the guard. If the staged bytes happened not to change, this test
	// would pass without exercising anything.
	if after := stagedBytes(t, src); after == staged {
		t.Fatal("the staged source did not change, so this test proves nothing about a whole-source digest")
	}

	second, err := Take(src, snaps, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if !second.Held {
		t.Error("a checkpoint and a discarded-stream write defeated the dedup; " +
			"the digest is being taken over the staged source rather than the preserved rows")
	}
	if names := snapshotNames(t, snaps); len(names) != 1 {
		t.Errorf("the snapshot directory holds %d files (%v), want 1", len(names), names)
	}
}

// stagedBytes is what a whole-source digest would have seen: the database and
// its sidecars as stage copies them.
func stagedBytes(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	dst := filepath.Join(dir, "staged.db")
	if err := stage(src, dst); err != nil {
		t.Fatalf("stage %s: %v", src, err)
	}
	var b strings.Builder
	for _, suffix := range append([]string{""}, sidecarSuffixes...) {
		data, err := os.ReadFile(dst + suffix)
		if err != nil {
			continue
		}
		b.Write(data)
	}
	return b.String()
}

// TestTakeNamesASnapshotForItsContents keeps the state where biome.go and
// vault.go already keep it: in the file names. A separate manifest of what has
// been captured can disagree with the files, and the files are the thing that
// matters.
func TestTakeNamesASnapshotForItsContents(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snaps := filepath.Join(dir, "snapshots")

	first, err := Take(src, snaps, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}
	name := filepath.Base(first.Path)
	if !strings.HasPrefix(name, "knowledgeC-20260812T100000-") {
		t.Errorf("snapshot named %q, want the instant it was taken followed by a digest", name)
	}
	digest := strings.TrimSuffix(strings.TrimPrefix(name, "knowledgeC-20260812T100000-"), ".db.gz")
	if len(digest) != snapshotDigestLength {
		t.Errorf("digest %q is %d characters, want %d", digest, len(digest), snapshotDigestLength)
	}

	exec, done := writer(t, src)
	exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
		VALUES ('/display/isBacklit', NULL, 1, 1234, 1234)`)
	done()

	second, err := Take(src, snaps, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if strings.Contains(filepath.Base(second.Path), digest) {
		t.Errorf("a changed source was named for the previous contents' digest %q", digest)
	}
}

// TestTakeReportsAReadableSnapshotWhenItWroteNothing is what keeps ingestion
// working across the change. cmd/timetattle hands Result.Path straight to the
// four knowledgeC collectors, so a held run must return the file that holds
// those bytes and not an empty string, a staging path, or the path it would
// have written.
func TestTakeReportsAReadableSnapshotWhenItWroteNothing(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snaps := filepath.Join(dir, "snapshots")

	if _, err := Take(src, snaps, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("first Take: %v", err)
	}
	held, err := Take(src, snaps, time.Date(2026, 8, 12, 11, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if !held.Held {
		t.Fatal("the second Take over an unchanged source wrote a file, so this test is not exercising a held run")
	}
	db := openSnapshot(t, held.Path)
	if got := scalar(t, db, `SELECT COUNT(*) FROM ZOBJECT`); got != 40 {
		t.Errorf("the snapshot reported by a held run has %d rows, want 40", got)
	}
}

// TestTakeDoesNotHoldAgainstASnapshotItDidNotWrite guards the lookup itself.
// A file that merely looks like a snapshot must not be read as proof that
// contents are preserved, because being wrong here loses evidence permanently
// rather than costing a megabyte.
func TestTakeDoesNotHoldAgainstASnapshotItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snaps := filepath.Join(dir, "snapshots")

	if err := os.MkdirAll(snaps, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The old, digestless name. Every snapshot already on disk has this shape,
	// and none of them can be matched against without being decompressed.
	stale := filepath.Join(snaps, "knowledgeC-20260101T000000.db.gz")
	if err := os.WriteFile(stale, []byte("not a snapshot"), 0o600); err != nil {
		t.Fatalf("write %s: %v", stale, err)
	}

	res, err := Take(src, snaps, time.Date(2026, 8, 12, 10, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if res.Held {
		t.Fatal("Take treated an unrelated file as proof the contents were already preserved")
	}
	if res.Path == stale {
		t.Fatal("Take reported the unrelated file as the snapshot it preserved")
	}
	// And what it wrote is named for its own contents, so the NEXT run can
	// match against it without decompressing anything. The digestless file
	// beside it stays exactly where it is: every snapshot already on disk has
	// that shape, and none of them may be touched.
	digestInName(t, res.Path)
	if _, err := os.Stat(stale); err != nil {
		t.Errorf("the pre-existing file was disturbed: %v", err)
	}
}
