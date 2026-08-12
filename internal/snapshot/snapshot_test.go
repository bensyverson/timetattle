package snapshot

import (
	"compress/gzip"
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// liveDB builds a miniature knowledgeC: a WAL-mode database with a ZOBJECT
// event table and the ZSTRUCTUREDMETADATA table that holds the sensitive
// payloads we deliberately do not preserve.
//
// The returned connection stays open on purpose. SQLite folds the -wal file
// back into the main database when the last writer disconnects, so a closed
// fixture would make the WAL test pass for the wrong reason. The real
// knowledgeC is held open by a system daemon; this reproduces that.
func liveDB(t *testing.T, dir string) (path string, release func()) {
	t.Helper()
	path = filepath.Join(dir, "knowledgeC.db")

	// wal_autocheckpoint(0) stops SQLite folding the WAL back on its own, so
	// the rows provably remain in the sidecar for the duration of the test.
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(wal)&_pragma=wal_autocheckpoint(0)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		t.Fatalf("open live db: %v", err)
	}
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatalf("pin connection: %v", err)
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := conn.ExecContext(context.Background(), q, args...); err != nil {
			t.Fatalf("exec %q: %v", q, err)
		}
	}

	exec(`CREATE TABLE ZSTRUCTUREDMETADATA (
		Z_PK INTEGER PRIMARY KEY,
		Z_DKDIGITALHEALTHMETADATAKEY__WEBPAGEURL TEXT,
		Z_DKINTENTMETADATAKEY__SERIALIZEDINTERACTION BLOB)`)
	exec(`INSERT INTO ZSTRUCTUREDMETADATA VALUES (1, 'https://example.com/private', X'DEADBEEF')`)

	exec(`CREATE TABLE ZOBJECT (
		Z_PK INTEGER PRIMARY KEY,
		ZSTREAMNAME TEXT,
		ZVALUESTRING TEXT,
		ZSTRUCTUREDMETADATA INTEGER,
		ZSTARTDATE REAL,
		ZENDDATE REAL)`)

	// 40 rows we must keep, 60 rows of /app/webUsage we must drop.
	for i := range 20 {
		exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
			VALUES ('/display/isBacklit', NULL, 1, ?, ?)`, float64(i*100), float64(i*100+50))
	}
	for i := range 20 {
		exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
			VALUES ('/app/usage', 'com.apple.motionapp', 1, ?, ?)`, float64(i*100), float64(i*100+50))
	}
	for i := range 60 {
		exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
			VALUES ('/app/webUsage', 'com.apple.Safari', 1, ?, ?)`, float64(i*100), float64(i*100+50))
	}

	var mode string
	if err := conn.QueryRowContext(context.Background(), `PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal; the sidecar test would be meaningless", mode)
	}
	// Guard the guard: an empty -wal means the rows already reached the main
	// database and this fixture could not detect a dropped sidecar.
	if info, err := os.Stat(path + "-wal"); err != nil {
		t.Fatalf("stat -wal: %v", err)
	} else if info.Size() == 0 {
		t.Fatal("-wal is empty; rows are not in the sidecar, so a missing one would go undetected")
	}

	return path, func() { conn.Close(); db.Close() }
}

// openSnapshot decompresses a snapshot and opens it for reading.
func openSnapshot(t *testing.T, gzPath string) *sql.DB {
	t.Helper()
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("open snapshot: %v", err)
	}
	defer f.Close()

	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("snapshot %s is not gzip: %v", gzPath, err)
	}
	defer zr.Close()

	plain := filepath.Join(t.TempDir(), "restored.db")
	out, err := os.Create(plain)
	if err != nil {
		t.Fatalf("create restored db: %v", err)
	}
	if _, err := io.Copy(out, zr); err != nil {
		t.Fatalf("decompress: %v", err)
	}
	out.Close()

	db, err := sql.Open("sqlite", "file:"+plain+"?mode=ro")
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func scalar(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(q).Scan(&n); err != nil {
		t.Fatalf("query %q: %v", q, err)
	}
	return n
}

// TestTakePreservesWALData is the criterion the package exists for. Reading
// knowledgeC.db without its WAL sidecar silently drops the newest events —
// exactly the ones a harvester runs to capture.
func TestTakePreservesWALData(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()

	res, err := Take(src, filepath.Join(dir, "snapshots"), time.Date(2026, 7, 28, 17, 15, 30, 0, time.UTC))
	if err != nil {
		t.Fatalf("Take: %v", err)
	}

	db := openSnapshot(t, res.Path)
	if got := scalar(t, db, `SELECT COUNT(*) FROM ZOBJECT`); got != 40 {
		t.Errorf("snapshot has %d rows, want 40 — WAL data was lost", got)
	}
	if res.Rows != 40 {
		t.Errorf("Result.Rows = %d, want 40", res.Rows)
	}
}

// TestTakeExcludesWebUsage: 29% of real rows are /app/webUsage carrying only
// two distinct values, which /app/usage already reports better.
func TestTakeExcludesWebUsage(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()

	res, err := Take(src, filepath.Join(dir, "snapshots"), time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	db := openSnapshot(t, res.Path)

	if got := scalar(t, db, `SELECT COUNT(*) FROM ZOBJECT WHERE ZSTREAMNAME='/app/webUsage'`); got != 0 {
		t.Errorf("snapshot kept %d /app/webUsage rows, want 0", got)
	}
	// The streams that matter must survive intact.
	if got := scalar(t, db, `SELECT COUNT(*) FROM ZOBJECT WHERE ZSTREAMNAME='/display/isBacklit'`); got != 20 {
		t.Errorf("/display/isBacklit rows = %d, want 20", got)
	}
	if got := scalar(t, db, `SELECT COUNT(*) FROM ZOBJECT WHERE ZSTREAMNAME='/app/usage'`); got != 20 {
		t.Errorf("/app/usage rows = %d, want 20", got)
	}
}

// TestTakeDropsSensitiveMetadata guards a privacy property, not a size one.
// ZSTRUCTUREDMETADATA holds web page URLs, serialized Siri interactions and
// contact identifiers, none of which time tracking needs.
func TestTakeDropsSensitiveMetadata(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()

	res, err := Take(src, filepath.Join(dir, "snapshots"), time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	db := openSnapshot(t, res.Path)

	if got := scalar(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='ZSTRUCTUREDMETADATA'`); got != 0 {
		t.Error("snapshot retained ZSTRUCTUREDMETADATA, which holds URLs, Siri blobs and contact identifiers")
	}

	// Belt and braces: the URL must not appear anywhere in the bytes.
	raw, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read snapshot: %v", err)
	}
	zr, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	plain, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress: %v", err)
	}
	if strings.Contains(string(plain), "example.com/private") {
		t.Error("snapshot bytes still contain a web page URL from ZSTRUCTUREDMETADATA")
	}
}

// TestTakeIsSubstantiallySmallerThanSource keeps the size win from silently
// regressing if the extraction is ever widened.
func TestTakeIsSubstantiallySmallerThanSource(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()

	res, err := Take(src, filepath.Join(dir, "snapshots"), time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	info, err := os.Stat(res.Path)
	if err != nil {
		t.Fatalf("stat snapshot: %v", err)
	}
	if info.Size() != res.Bytes {
		t.Errorf("Result.Bytes = %d, actual file is %d", res.Bytes, info.Size())
	}
	if !strings.HasSuffix(res.Path, ".db.gz") {
		t.Errorf("snapshot %s should be a gzipped database", res.Path)
	}
}

// TestTakeDoesNotClobberEarlierSnapshots guards the insurance property. A
// snapshot overwritten is history destroyed — the exact failure this package
// exists to prevent.
//
// # Why this test's SETUP changed, and its assertion did not
//
// It used to take two snapshots of an UNCHANGED source at the same instant and
// require two files. That is no longer the behaviour: Take now skips a write
// whose contents are already on disk, so the second call reports the first
// call's file and writes nothing. The old expectation was a statement about
// name collisions, and it happened to be measurable only through a case that
// content-addressing removes.
//
// The property being guarded is unchanged and is the one that matters — two
// snapshots landing in the same second must never occupy one file — so the
// source is changed between the calls, which is the only way two snapshots can
// still collide on a name. That the OTHER case now writes one file instead of
// two is asserted directly, and separately, in
// TestTakeHoldsASnapshotWhoseContentsAreAlreadyPreserved.
func TestTakeDoesNotClobberEarlierSnapshots(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snapDir := filepath.Join(dir, "snapshots")

	at := time.Date(2026, 7, 28, 17, 15, 30, 0, time.UTC) // same instant twice
	first, err := Take(src, snapDir, at)
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}

	exec, done := writer(t, src)
	exec(`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZSTRUCTUREDMETADATA, ZSTARTDATE, ZENDDATE)
		VALUES ('/display/isBacklit', NULL, 1, 7777, 7777)`)
	done()

	second, err := Take(src, snapDir, at)
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}

	if first.Path == second.Path {
		t.Fatalf("both snapshots wrote to %s; the first was destroyed", first.Path)
	}
	for _, p := range []string{first.Path, second.Path} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("snapshot %s does not exist: %v", p, err)
		}
	}
	// The first file must still hold what it held, not the second's contents
	// under the first's name.
	if got := scalar(t, openSnapshot(t, first.Path), `SELECT COUNT(*) FROM ZOBJECT`); got != first.Rows {
		t.Errorf("the earlier snapshot now holds %d rows, want the %d it captured", got, first.Rows)
	}
}

// TestTakeDoesNotModifySource: knowledgeC belongs to macOS. Timetattle reads
// it and must never write to it.
func TestTakeDoesNotModifySource(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()

	before, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	if _, err := Take(src, filepath.Join(dir, "snapshots"), time.Now()); err != nil {
		t.Fatalf("Take: %v", err)
	}
	after, err := os.Stat(src)
	if err != nil {
		t.Fatalf("stat source: %v", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("source database changed: size %d->%d, mtime %v->%v",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
}

// TestTakeLeavesNoTemporaryFiles: Take stages a raw copy to read the WAL and
// must clean it up, or a 37 MB temp file accumulates on every harvest run.
func TestTakeLeavesNoTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()
	snapDir := filepath.Join(dir, "snapshots")

	res, err := Take(src, snapDir, time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	entries, err := os.ReadDir(snapDir)
	if err != nil {
		t.Fatalf("read snapshot dir: %v", err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("snapshot dir holds %v, want only %s", names, filepath.Base(res.Path))
	}
}

// TestTakeUnreadableSourceIsActionable: macOS reports a missing Full Disk
// Access grant as a plain permission error, and an operator who sees only
// "permission denied" has no idea what to do about it.
func TestTakeUnreadableSourceIsActionable(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "knowledgeC.db")
	if err := os.WriteFile(src, []byte("not really a database"), 0o000); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if f, err := os.Open(src); err == nil { // running as root defeats mode 0o000
		f.Close()
		t.Skip("source readable despite mode 0o000; test is meaningless as this user")
	}

	_, err := Take(src, filepath.Join(dir, "snapshots"), time.Now())
	if err == nil {
		t.Fatal("Take succeeded on an unreadable source; a silent failure loses data without saying so")
	}
	if msg := err.Error(); !strings.Contains(msg, "Full Disk Access") {
		t.Errorf("error %q does not mention Full Disk Access, so it is not actionable", msg)
	} else if !strings.Contains(msg, src) {
		t.Errorf("error %q does not name the unreadable path %s", msg, src)
	}
}

// TestTakeMissingSourceIsNotSilent guards a bug in the spike prototype: it
// skipped unstattable files with `continue`, so a fully denied read produced
// no files, no error, and a returned path that did not exist.
func TestTakeMissingSourceIsNotSilent(t *testing.T) {
	dir := t.TempDir()
	absent := filepath.Join(dir, "absent.db")
	snapDir := filepath.Join(dir, "snapshots")

	res, err := Take(absent, snapDir, time.Now())
	if err == nil {
		t.Fatal("Take reported success for a source that does not exist")
	}
	if !strings.Contains(err.Error(), absent) {
		t.Errorf("error %q does not name the missing path %s", err, absent)
	}
	if res.Path != "" {
		t.Errorf("Result.Path = %q on failure, want empty", res.Path)
	}
	if entries, err := os.ReadDir(snapDir); err == nil && len(entries) > 0 {
		t.Errorf("failed Take left %d file(s) behind in %s", len(entries), snapDir)
	}
}

// TestTakeCreatesSnapshotDirectory keeps the launchd agent working on a
// machine where nothing has run yet.
func TestTakeCreatesSnapshotDirectory(t *testing.T) {
	dir := t.TempDir()
	src, release := liveDB(t, dir)
	defer release()

	res, err := Take(src, filepath.Join(dir, "does", "not", "exist"), time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if _, err := os.Stat(res.Path); err != nil {
		t.Errorf("snapshot not written into a created directory: %v", err)
	}
}

func TestDefaultPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	src, err := DefaultSourcePath()
	if err != nil {
		t.Fatalf("DefaultSourcePath: %v", err)
	}
	if want := filepath.Join(home, "Library/Application Support/Knowledge/knowledgeC.db"); src != want {
		t.Errorf("DefaultSourcePath = %q, want %q", src, want)
	}
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if want := filepath.Join(home, ".timetattle", "snapshots"); dir != want {
		t.Errorf("DefaultDir = %q, want %q", dir, want)
	}
}
