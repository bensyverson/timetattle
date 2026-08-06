package knowledgec

import (
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// The fixtures here build miniature knowledgeC snapshots the same way
// internal/snapshot does: a SQLite database holding a ZOBJECT table, optionally
// gzipped. Building them rather than committing a copy of a real snapshot keeps
// a personal record of when a machine was in use out of the repository, and lets
// a test describe rows a real database happens not to contain today —
// contradictory readings, a value that is neither 0 nor 1, a stream that stops
// writing for a day.

// row is one ZOBJECT row. Times are ordinary instants; the fixture converts them
// to Core Data seconds on the way in, which is also the only place this test
// package encodes that epoch in the same direction the reader decodes it.
type row struct {
	stream string
	start  time.Time
	end    time.Time
	// value is the reading. A nil value writes SQL NULL, which is how a row
	// that says nothing about the backlight is described.
	value any
	// text is ZVALUESTRING, the bundle identifier on the application streams.
	text string
}

// backlit, locked, used and played are shorthand for the four streams, so a test
// body reads as a sequence of observations rather than of column values.
func backlit(start, end time.Time, on int) row {
	return row{stream: streamBacklight, start: start, end: end, value: on}
}

func locked(start, end time.Time, isLocked int) row {
	return row{stream: streamLocked, start: start, end: end, value: isLocked}
}

func used(start, end time.Time, bundle string) row {
	return row{stream: streamAppUsage, start: start, end: end, text: bundle}
}

func played(start, end time.Time, bundle string) row {
	return row{stream: streamMediaUsage, start: start, end: end, text: bundle}
}

// writeSnapshot builds a plain SQLite snapshot and returns its path.
func writeSnapshot(t *testing.T, path string, rows ...row) string {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("create fixture %s: %v", path, err)
	}
	defer db.Close()

	// The column set a snapshot preserves. ZSTARTDATE and ZENDDATE are REAL
	// because Core Data writes doubles, and reading them as integers is one of
	// the two ways to be three decades wrong about a timestamp.
	if _, err := db.Exec(`CREATE TABLE ZOBJECT (
		Z_PK INTEGER PRIMARY KEY,
		ZSTREAMNAME TEXT,
		ZVALUESTRING TEXT,
		ZVALUEINTEGER INTEGER,
		ZSTARTDATE REAL,
		ZENDDATE REAL)`); err != nil {
		t.Fatalf("create ZOBJECT: %v", err)
	}

	for _, r := range rows {
		var text any
		if r.text != "" {
			text = r.text
		}
		if _, err := db.Exec(
			`INSERT INTO ZOBJECT (ZSTREAMNAME, ZVALUESTRING, ZVALUEINTEGER, ZSTARTDATE, ZENDDATE)
			 VALUES (?, ?, ?, ?, ?)`,
			r.stream, text, r.value, appleSeconds(r.start), appleSeconds(r.end)); err != nil {
			t.Fatalf("insert a %s row: %v", r.stream, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close fixture %s: %v", path, err)
	}
	return path
}

// writeGzSnapshot builds the same thing gzipped, which is the only form a real
// harvest ever writes.
func writeGzSnapshot(t *testing.T, path string, rows ...row) string {
	t.Helper()

	plain := writeSnapshot(t, filepath.Join(t.TempDir(), "plain.db"), rows...)
	in, err := os.Open(plain)
	if err != nil {
		t.Fatalf("open %s: %v", plain, err)
	}
	defer in.Close()

	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := gzip.NewWriter(out)
	if _, err := io.Copy(zw, in); err != nil {
		t.Fatalf("compress fixture: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("finish compressing fixture: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// appleSeconds is the fixture's half of the epoch conversion, written out
// independently of the reader's so that a sign error in one is not cancelled by
// the same error in the other.
func appleSeconds(at time.Time) float64 {
	return float64(at.Unix() - 978307200)
}

// at is a wall-clock instant on the day the presence traps were measured,
// in local time because that is what the reader returns.
func at(hour, minute, second int) time.Time {
	return time.Date(2026, 7, 14, hour, minute, second, 0, time.Local)
}
