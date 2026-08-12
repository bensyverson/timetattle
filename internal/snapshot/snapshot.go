// Package snapshot preserves the evidence macOS deletes about itself.
//
// Two stores are captured, for the same reason and by very different means.
//
// # knowledgeC
//
// knowledgeC is the only source that directly observes presence, and it
// prunes itself: /app/usage is capped near 10,000 rows giving under 10 days,
// and the remaining streams run on a 28-30 day window. Data not captured
// before it is pruned is gone permanently and cannot be recovered by any
// means. That is why the harvester exists and why it runs first. Take handles
// this one.
//
// A snapshot keeps the ZOBJECT event table for every stream except
// /app/webUsage, and nothing else. Measured against a real 37.4 MB database
// that is a 23x reduction, but size is the lesser reason. The discarded
// ZSTRUCTUREDMETADATA table holds web page URLs, serialized Siri
// interactions and contact identifiers; a work-time tracker needs none of
// it, and retaining it indefinitely would be a liability with no upside.
// It was also verified to carry no information for /app/usage, whose 10,000
// rows reference just three distinct metadata rows with a single usage type.
//
// # Biome
//
// Biome's ScreenTime.AppUsage stream carries application focus, which is the
// half of the picture knowledgeC cannot be trusted with: /app/usage stalled for
// a full day, writing its last row before the gap on 2026-07-27 at 14:28, none
// at all on 07-28, and 806 on 07-29 when it resumed on its own. An earlier note
// here said it did not resume; that was wrong, and the correction is recorded in
// project/private/2026-07-28-source-corrections.md §1. Biome has shown no such gap, so
// it remains the attribution path and /app/usage is a cross-check.
// Biome is perishable in a different way: macOS fills fixed 512 KB segments in
// place, rolls to a new one when full, and eventually discards old ones.
// TakeStreams handles this one, and its doc comment explains why nothing
// cheaper than the contents themselves can tell whether a segment has changed.
//
// The same privacy discipline applies. The restricted stream directory holds
// over 150 streams including Safari navigations, Mail harvesting and Siri
// history; the streams Timetattle keeps are named one by one in Streams, and
// nothing else is read.
//
// # Both
//
// This package deliberately does no interpretation. It is insurance, kept
// separate from ingestion so a bug in the collector cannot cost us history
// we already had in hand.
package snapshot

import (
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ExcludedStreams are dropped when a snapshot is written.
//
// /app/webUsage is 29% of all rows in a real database and carries only two
// distinct values, com.apple.Safari and com.apple.mobilesafari. /app/usage
// already reports Safari focus with real intervals, so this stream tells us
// nothing we do not have.
var ExcludedStreams = []string{"/app/webUsage"}

// sidecarSuffixes are SQLite's write-ahead log and shared-memory files.
var sidecarSuffixes = []string{"-wal", "-shm"}

// Result describes one preserved snapshot.
type Result struct {
	// Path is the gzipped database that was written.
	Path string
	// Rows is the number of ZOBJECT rows preserved.
	Rows int
	// Bytes is the compressed size on disk.
	Bytes int64
	// Held reports that this run wrote nothing because a snapshot already on
	// disk holds these exact contents. Path then names that snapshot.
	Held bool
}

// snapshotDigestLength is how much of the SHA-256 of a snapshot's contents
// goes in its file name: 32 hex characters, 128 bits.
//
// It is its own constant rather than a reuse of digestLength or
// vaultDigestLength for the reason vault.go already gives — the three capture
// mechanisms are allowed to change independently — and the margin is chosen on
// the same grounds as Biome's. A collision here would mean skipping a capture
// of knowledgeC, which prunes itself and cannot be recovered by any means, so
// the margin is deliberately absurd rather than merely sufficient.
const snapshotDigestLength = 32

// Take writes a compact, gzipped snapshot of src into dir, unless a snapshot
// already in dir holds the identical contents.
//
// src is copied with its WAL and SHM sidecars before being read. Reading the
// database alone silently drops the most recent events, because an open
// writer keeps recent pages in the -wal file until a checkpoint folds them
// back — and those are exactly the events a harvest run exists to capture.
//
// # Why a run over an unchanged source writes nothing
//
// Every snapshot used to be written afresh under a new timestamped name, with
// no check of any kind, and that was this package's one asymmetry: Biome
// segments and Motion vault files were already skipped on a digest match while
// knowledgeC was not. Measured 2026-08-12 with `go run ./scripts/snapchurn`,
// three of the 38 snapshots in the live corpus (4.8 MB, 8.2%) are byte-for-byte
// repeats of a file sitting next to them, written by runs eleven seconds and
// three minutes after the one they duplicate.
//
// That cost is what capped the harvest cadence. At 1.5 MB a snapshot, an hourly
// schedule projects to about 13 GB a year, and the cadence argument — mtime
// evidence MOVES rather than accumulates, so a harvest that comes too late
// recovers nothing — was being decided on disk usage.
//
// What is skipped is a WRITE, never a capture. The source is read, staged and
// extracted in full every time; the only thing that does not happen is storing
// a second copy of bytes already on disk. Nothing existing is deleted, moved or
// rewritten.
//
// # What "identical" means here, and why it is not the staged bytes
//
// A knowledgeC snapshot is a SQLite database PLUS its -wal and -shm sidecars,
// and those three files are not a function of the rows inside them. A
// checkpoint folds the WAL back into the main database and truncates the
// sidecar, rewriting essentially every page while changing no row at all; a
// write to ZSTRUCTUREDMETADATA or to /app/webUsage — both of which a snapshot
// deliberately discards — appends WAL frames for data that is never preserved.
// A digest over the staged source therefore reports "changed" almost every
// time, and a dedup built on one would skip nothing while reporting success.
//
// The digest is taken over the EXTRACTED database instead: the ZOBJECT rows
// this package actually keeps, in the file it actually writes. Extraction is
// what normalises the source, so two runs over a differently-checkpointed
// database with the same rows produce the same digest.
//
// That this holds in practice is not an inference. The three duplicate pairs
// in the live corpus were produced by separate runs, each staging the live
// knowledgeC afresh into its own temporary directory while macOS held the
// database open, and their compressed outputs are byte-identical — 35 distinct
// digests over 38 files, whether measured on the compressed or the decompressed
// form. See TestTakeIsNotDefeatedByAChangedWALOverUnchangedRows for the
// constructed case that sits beside that observation.
//
// # Where the record of "already held" lives
//
// In the file names, exactly as biome.go argues: a snapshot is named for the
// instant it was taken AND the digest of its contents, so the question "do we
// already hold these bytes" is answered by reading the directory. A separate
// manifest could disagree with the files, and the files are the thing that
// matters.
//
// The consequence for snapshots written before this existed is deliberate.
// Their names carry no digest, so nothing can be matched against them without
// decompressing every one on every run, and they are therefore never matched.
// The first run after this lands writes a snapshot; every run after that can
// hold against it. Rewriting the old names to add a digest was considered and
// rejected: they are the perishable evidence this package exists to protect,
// and no cleanup is worth touching them for.
func Take(src, dir string, now time.Time) (Result, error) {
	// Fail before creating anything, so an unreadable source leaves no
	// half-written snapshot for a later run to mistake for real history.
	if err := checkReadable(src); err != nil {
		return Result{}, err
	}

	// Stage outside dir: the snapshot directory must end up holding only
	// finished snapshots.
	staging, err := os.MkdirTemp("", "timetattle-snapshot-")
	if err != nil {
		return Result{}, fmt.Errorf("create staging directory: %w", err)
	}
	defer os.RemoveAll(staging)

	staged := filepath.Join(staging, "knowledgeC.db")
	if err := stage(src, staged); err != nil {
		return Result{}, err
	}

	compact := filepath.Join(staging, "compact.db")
	rows, err := extract(staged, compact)
	if err != nil {
		return Result{}, err
	}

	digest, err := contentDigest(compact)
	if err != nil {
		return Result{}, err
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create snapshot directory %s: %w", dir, err)
	}

	// Rows is the source's count either way. A held run that reported zero
	// rows would be indistinguishable from a source that had gone empty, and
	// those two call for opposite reactions.
	held, err := heldSnapshot(dir, digest)
	if err != nil {
		return Result{}, err
	}
	if held != "" {
		return Result{Path: held, Rows: rows, Held: true}, nil
	}

	out, err := compress(compact, dir, now, digest)
	if err != nil {
		return Result{}, err
	}

	info, err := os.Stat(out)
	if err != nil {
		return Result{}, fmt.Errorf("stat snapshot %s: %w", out, err)
	}
	return Result{Path: out, Rows: rows, Bytes: info.Size()}, nil
}

// checkReadable turns macOS's opaque permission errors into something an
// operator can act on. A denied read of knowledgeC almost always means Full
// Disk Access was never granted to the binary doing the reading.
func checkReadable(src string) error {
	f, err := os.Open(src)
	if err == nil {
		f.Close()
		return nil
	}
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot read %s: %w\n\n"+
			"This is almost always a missing Full Disk Access grant. Open\n"+
			"System Settings > Privacy & Security > Full Disk Access, add the\n"+
			"program running Timetattle (your terminal, or the timetattle\n"+
			"binary itself when run from launchd), then restart it.", src, err)
	}
	return fmt.Errorf("cannot read %s: %w", src, err)
}

// stage copies the database and its sidecars. The spike prototype skipped
// unstattable files with `continue`, so a fully denied read produced no
// files, no error, and a path that did not exist.
func stage(src, dst string) error {
	if err := copyFile(src, dst); err != nil {
		return fmt.Errorf("copy %s: %w", src, err)
	}
	for _, suffix := range sidecarSuffixes {
		// A checkpointed database legitimately has no sidecars.
		if _, err := os.Stat(src + suffix); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := copyFile(src+suffix, dst+suffix); err != nil {
			return fmt.Errorf("copy sidecar %s: %w (without it the newest events are lost)", src+suffix, err)
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// extract copies the wanted ZOBJECT rows from the staged database into a
// fresh one, preserving the original column definitions but none of the
// indexes, auxiliary tables or free pages.
func extract(staged, dst string) (int, error) {
	// The staged copy is ours, so opening it read-write is safe and lets
	// SQLite recover the WAL we deliberately brought along.
	src, err := sql.Open("sqlite", "file:"+staged)
	if err != nil {
		return 0, fmt.Errorf("open staged database: %w", err)
	}
	defer src.Close()

	var schema string
	err = src.QueryRow(`SELECT sql FROM sqlite_master WHERE type='table' AND name='ZOBJECT'`).Scan(&schema)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, fmt.Errorf("%s has no ZOBJECT table; is this a knowledgeC database?", staged)
	}
	if err != nil {
		return 0, fmt.Errorf("read ZOBJECT schema: %w", err)
	}
	src.Close()

	out, err := sql.Open("sqlite", "file:"+dst)
	if err != nil {
		return 0, fmt.Errorf("create compact database: %w", err)
	}
	defer out.Close()

	if _, err := out.Exec(`ATTACH DATABASE ? AS src`, staged); err != nil {
		return 0, fmt.Errorf("attach staged database: %w", err)
	}
	if _, err := out.Exec(schema); err != nil {
		return 0, fmt.Errorf("recreate ZOBJECT: %w", err)
	}

	// A NULL stream name must not be silently dropped: `x NOT IN (...)` is
	// NULL, not true, so the IS NULL arm is load-bearing rather than defensive.
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(ExcludedStreams)), ",")
	args := make([]any, len(ExcludedStreams))
	for i, s := range ExcludedStreams {
		args[i] = s
	}
	query := fmt.Sprintf(
		`INSERT INTO main.ZOBJECT SELECT * FROM src.ZOBJECT
		 WHERE ZSTREAMNAME IS NULL OR ZSTREAMNAME NOT IN (%s)`, placeholders)
	if _, err := out.Exec(query, args...); err != nil {
		return 0, fmt.Errorf("copy ZOBJECT rows: %w", err)
	}
	if _, err := out.Exec(`DETACH DATABASE src`); err != nil {
		return 0, fmt.Errorf("detach staged database: %w", err)
	}

	var rows int
	if err := out.QueryRow(`SELECT COUNT(*) FROM ZOBJECT`).Scan(&rows); err != nil {
		return 0, fmt.Errorf("count preserved rows: %w", err)
	}
	return rows, out.Close()
}

// contentDigest is the SHA-256 of the extracted database, truncated to
// snapshotDigestLength hex characters.
//
// It is taken over the UNCOMPRESSED file on purpose. The compressed form is a
// function of the contents plus whatever the compressor does, so digesting it
// would make the dedup depend on gzip staying byte-stable across Go versions
// and levels — and the failure mode of that dependency is silent: every
// snapshot would simply start looking new again, and nothing would report an
// error. The two forms agree on the live corpus today (35 distinct digests
// over 38 files, measured either way, `go run ./scripts/snapchurn`); digesting
// the contents means it does not matter if they ever stop agreeing.
func contentDigest(compact string) (string, error) {
	f, err := os.Open(compact)
	if err != nil {
		return "", fmt.Errorf("open the extracted database to digest it: %w", err)
	}
	defer f.Close()

	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("digest the extracted database: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil))[:snapshotDigestLength], nil
}

// heldSnapshot returns the snapshot in dir already holding digest's contents,
// or "" if there is none.
//
// The earliest such snapshot is returned rather than the newest, so the answer
// is the same on every run and a reader following the reported path is always
// sent to the same file.
//
// An unreadable directory is an error rather than "nothing held". Those two
// differ by exactly the thing that matters: if a directory that cannot be read
// were treated as empty, a machine with a permissions problem would quietly
// write a fresh 1.5 MB snapshot every hour and look like it was working. The
// opposite mistake is the one that is safe — an unmatched digest costs a
// duplicate file, a wrongly matched one costs evidence — so nothing here
// guesses.
func heldSnapshot(dir, digest string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read the snapshot directory %s to see what is already held: %w", dir, err)
	}
	// os.ReadDir sorts by name, and a name begins with its timestamp, so the
	// first match is the earliest.
	suffix := "-" + digest + ".db.gz"
	for _, e := range entries {
		name := e.Name()
		if !e.IsDir() && strings.HasPrefix(name, snapshotPrefix) && strings.HasSuffix(name, suffix) {
			return filepath.Join(dir, name), nil
		}
	}
	return "", nil
}

// snapshotPrefix is how a knowledgeC snapshot's name begins.
// internal/knowledgec keeps its own copy for reading the directory back.
const snapshotPrefix = "knowledgeC-"

// compress gzips the compact database into dir under a name carrying both the
// instant it was taken and the digest of what is inside it, never overwriting
// an existing snapshot. An overwritten snapshot is history destroyed, which is
// the exact failure this package exists to prevent.
func compress(compact, dir string, now time.Time, digest string) (string, error) {
	in, err := os.Open(compact)
	if err != nil {
		return "", fmt.Errorf("open compact database: %w", err)
	}
	defer in.Close()

	dst, out, err := createUnique(dir, now, digest)
	if err != nil {
		return "", err
	}

	zw, _ := gzip.NewWriterLevel(out, gzip.BestCompression)
	if _, err := io.Copy(zw, in); err != nil {
		zw.Close()
		out.Close()
		os.Remove(dst)
		return "", fmt.Errorf("compress snapshot: %w", err)
	}
	if err := zw.Close(); err != nil {
		out.Close()
		os.Remove(dst)
		return "", fmt.Errorf("finish compressing snapshot: %w", err)
	}
	if err := out.Close(); err != nil {
		os.Remove(dst)
		return "", fmt.Errorf("write snapshot %s: %w", dst, err)
	}
	return dst, nil
}

// createUnique opens a new snapshot file, adding a counter if the name is
// taken. O_EXCL makes this safe against a concurrent harvest run rather than
// merely unlikely to collide.
//
// The counter survives the move to content-addressed names, and is reachable
// only by two runs landing in the same second with DIFFERENT contents — the
// same second with the same contents is answered by heldSnapshot before
// anything gets here. It is kept rather than replaced by treating the
// collision as "already held", because a run losing the O_EXCL race may be
// looking at a file another process is still writing, and a duplicate file is
// a cost while a half-written one reported as preserved is a loss.
func createUnique(dir string, now time.Time, digest string) (string, *os.File, error) {
	base := filepath.Join(dir, snapshotPrefix+now.Format("20060102T150405")+"-"+digest)
	for n := range 1000 {
		path := base + ".db.gz"
		if n > 0 {
			path = fmt.Sprintf("%s-%d.db.gz", base, n)
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return path, f, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("create snapshot %s: %w", path, err)
		}
	}
	return "", nil, fmt.Errorf("cannot find a free snapshot name in %s", dir)
}

// DefaultSourcePath is where macOS keeps the user's knowledgeC database.
func DefaultSourcePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, "Library/Application Support/Knowledge/knowledgeC.db"), nil
}

// DefaultDir is where Timetattle preserves snapshots.
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".timetattle", "snapshots"), nil
}
