package main

import (
	"compress/gzip"
	"database/sql"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// stampPattern matches the instant snapshot.Take puts in a file name. Anything
// after it — a collision counter, an extension — is not part of the instant.
var stampPattern = regexp.MustCompile(`\d{8}T\d{6}`)

// captureTime reads the instant out of a snapshot file name.
//
// snapshot.Take stamps the name in LOCAL time, so it is parsed as local. Every
// retention figure this tool prints is a capture time minus a row time, and the
// row times are absolute, so getting this zone wrong would bias the whole
// series by the UTC offset — several hours against a deadline measured in days.
func captureTime(name string) (time.Time, error) {
	stamp := stampPattern.FindString(filepath.Base(name))
	if stamp == "" {
		return time.Time{}, fmt.Errorf("no instant in the name %q", filepath.Base(name))
	}
	return time.ParseInLocation("20060102T150405", stamp, time.Local)
}

// readSnapshot opens one snapshot — plain .db or gzipped .db.gz — and reports
// the state of each requested stream inside it.
//
// It returns an error rather than a set of empty observations when the file
// cannot be read. The caller records that as PresenceUnreadable, which is a
// third thing from "the stream has no rows": a snapshot that would not open
// observed nothing at all, and must not close a gap in the series.
func readSnapshot(path string, streams []string) ([]Observation, error) {
	captured, err := captureTime(path)
	if err != nil {
		return nil, err
	}

	dbPath := path
	if strings.HasSuffix(path, ".gz") {
		plain, cleanup, err := decompress(path)
		if err != nil {
			return nil, fmt.Errorf("decompress %s: %w", filepath.Base(path), err)
		}
		defer cleanup()
		dbPath = plain
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", filepath.Base(path), err)
	}
	defer db.Close()

	obs := make([]Observation, 0, len(streams))
	for _, stream := range streams {
		o, err := readStream(db, stream)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		o.Snapshot = filepath.Base(path)
		o.Captured = captured
		o.Stream = stream
		obs = append(obs, o)
	}
	return obs, nil
}

// streamNames lists every stream a snapshot names. It is how -all builds its
// list from the corpus itself rather than from a constant in this file, so a
// stream macOS adds or renames appears without anyone editing the tool.
func streamNames(path string) ([]string, error) {
	dbPath := path
	if strings.HasSuffix(path, ".gz") {
		plain, cleanup, err := decompress(path)
		if err != nil {
			return nil, err
		}
		defer cleanup()
		dbPath = plain
	}
	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT DISTINCT ZSTREAMNAME FROM ZOBJECT WHERE ZSTREAMNAME IS NOT NULL`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// readStream scans one stream's start dates rather than asking SQL to
// aggregate them, because the same pass has to produce the count, the span and
// the per-day histogram, and because an undated row has to be counted rather
// than filtered away.
func readStream(db *sql.DB, stream string) (Observation, error) {
	rows, err := db.Query(`SELECT ZSTARTDATE FROM ZOBJECT WHERE ZSTREAMNAME = ?`, stream)
	if err != nil {
		return Observation{}, fmt.Errorf("read %s: %w", stream, err)
	}
	defer rows.Close()

	o := Observation{Presence: PresenceNoRows, Days: map[string]int{}}
	for rows.Next() {
		var start sql.NullFloat64
		if err := rows.Scan(&start); err != nil {
			return Observation{}, fmt.Errorf("scan a %s row: %w", stream, err)
		}
		o.Rows++
		o.Presence = PresenceRows
		if !start.Valid {
			o.Undated++
			continue
		}
		t := time.Unix(int64(start.Float64)+coreDataEpoch, 0).UTC()
		if o.Oldest.IsZero() || t.Before(o.Oldest) {
			o.Oldest = t
		}
		if t.After(o.Newest) {
			o.Newest = t
		}
		o.Days[t.Format("2006-01-02")]++
		o.Starts = append(o.Starts, t)
	}
	if err := rows.Err(); err != nil {
		return Observation{}, fmt.Errorf("read %s: %w", stream, err)
	}
	return o, nil
}

func decompress(path string) (string, func(), error) {
	in, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer in.Close()

	zr, err := gzip.NewReader(in)
	if err != nil {
		return "", nil, err
	}
	defer zr.Close()

	out, err := os.CreateTemp("", "streamseries-*.db")
	if err != nil {
		return "", nil, err
	}
	if _, err := io.Copy(out, zr); err != nil {
		out.Close()
		os.Remove(out.Name())
		return "", nil, err
	}
	if err := out.Close(); err != nil {
		os.Remove(out.Name())
		return "", nil, err
	}
	return out.Name(), func() { os.Remove(out.Name()) }, nil
}
