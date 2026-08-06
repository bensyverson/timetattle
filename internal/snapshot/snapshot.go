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
	"database/sql"
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
}

// Take writes a compact, gzipped snapshot of src into dir.
//
// src is copied with its WAL and SHM sidecars before being read. Reading the
// database alone silently drops the most recent events, because an open
// writer keeps recent pages in the -wal file until a checkpoint folds them
// back — and those are exactly the events a harvest run exists to capture.
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

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Result{}, fmt.Errorf("create snapshot directory %s: %w", dir, err)
	}
	out, err := compress(compact, dir, now)
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

// compress gzips the compact database into dir under a timestamped name,
// never overwriting an existing snapshot. An overwritten snapshot is history
// destroyed, which is the exact failure this package exists to prevent.
func compress(compact, dir string, now time.Time) (string, error) {
	in, err := os.Open(compact)
	if err != nil {
		return "", fmt.Errorf("open compact database: %w", err)
	}
	defer in.Close()

	dst, out, err := createUnique(dir, now)
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

// createUnique opens a new snapshot file, adding a counter if the timestamped
// name is taken. O_EXCL makes this safe against a concurrent harvest run
// rather than merely unlikely to collide.
func createUnique(dir string, now time.Time) (string, *os.File, error) {
	base := filepath.Join(dir, "knowledgeC-"+now.Format("20060102T150405"))
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
