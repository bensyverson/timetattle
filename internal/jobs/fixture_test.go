package jobs

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	_ "modernc.org/sqlite"
)

// The fixtures here build miniature .jobs.db databases carrying the schema the
// `job` tool actually writes, rather than committing a copy of a real one. A
// real database is a record of what somebody worked on and when, which does not
// belong in this repository; and building them is the only way to describe rows
// the nine live databases happen not to contain today — a claim re-claimed with
// no terminator between, an event type nobody has invented yet, a database at a
// different migration.
//
// The schema is copied verbatim from the live databases, checked 2026-08-01
// across all nine under ~/git. Two of them (Hayes, Jobs) declare task_id NOT
// NULL and the other seven leave it nullable; the nullable form is used here
// because it is the weaker constraint and therefore the one a reader has to
// survive.

// row is one events row. created_at is an ordinary instant; the fixture
// converts it to the unix seconds the schema stores, which is also the only
// place this test package encodes that epoch in the same direction the reader
// decodes it.
type row struct {
	task   string
	typ    string
	actor  string
	detail string
	at     time.Time
}

// ev is shorthand so a test body reads as a sequence of things somebody did.
func ev(task, typ string, at time.Time) row {
	return row{task: task, typ: typ, actor: "claude", at: at}
}

// evActor is ev with the actor named, for the one test that proves the actor
// changes nothing.
func evActor(task, typ, actor string, at time.Time) row {
	return row{task: task, typ: typ, actor: actor, at: at}
}

// evDetail is ev carrying the JSON payload the real tool writes. Two rows from
// one command differ only here, which is what makes them collapse.
func evDetail(task, typ, detail string, at time.Time) row {
	return row{task: task, typ: typ, actor: "claude", detail: detail, at: at}
}

// writeJobsDB builds a .jobs.db inside dir and returns dir, so a caller can
// hand the DIRECTORY straight to a collector the way the registry does.
func writeJobsDB(t *testing.T, dir string, rows ...row) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, DBName))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer db.Close()

	schema := []string{
		`CREATE TABLE tasks (
			id               INTEGER PRIMARY KEY AUTOINCREMENT,
			short_id         TEXT UNIQUE NOT NULL,
			parent_id        INTEGER REFERENCES tasks(id) ON DELETE CASCADE,
			title            TEXT NOT NULL,
			description      TEXT NOT NULL DEFAULT '',
			status           TEXT NOT NULL DEFAULT 'available',
			sort_order       INTEGER NOT NULL DEFAULT 0,
			claimed_by       TEXT,
			claim_expires_at INTEGER,
			completion_note  TEXT,
			created_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			updated_at       INTEGER NOT NULL DEFAULT (strftime('%s','now')),
			deleted_at       INTEGER
		)`,
		`CREATE TABLE events (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id     INTEGER REFERENCES tasks(id),
			event_type  TEXT NOT NULL,
			actor       TEXT NOT NULL DEFAULT '',
			detail      TEXT,
			created_at  INTEGER NOT NULL DEFAULT (strftime('%s','now'))
		)`,
	}
	for _, stmt := range schema {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("create fixture schema: %v", err)
		}
	}

	// Tasks are inserted in first-mention order, so a test names them by their
	// short id alone and never has to know an integer key.
	ids := make(map[string]int64)
	for _, r := range rows {
		if r.task == "" || ids[r.task] != 0 {
			continue
		}
		res, err := db.Exec(`INSERT INTO tasks (short_id, title) VALUES (?, ?)`, r.task, "task "+r.task)
		if err != nil {
			t.Fatalf("insert task %s: %v", r.task, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			t.Fatalf("task id for %s: %v", r.task, err)
		}
		ids[r.task] = id
	}

	// Insertion order is the order the rows are given, which is what makes the
	// rowid a tie-break a test can rely on when two rows share a second.
	for i, r := range rows {
		var task any
		if r.task != "" {
			task = ids[r.task]
		}
		var detail any
		if r.detail != "" {
			detail = r.detail
		}
		if _, err := db.Exec(
			`INSERT INTO events (task_id, event_type, actor, detail, created_at) VALUES (?, ?, ?, ?, ?)`,
			task, r.typ, r.actor, detail, r.at.Unix(),
		); err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}
	return dir
}

// at is a readable instant builder. Everything in these tests happens on one
// afternoon, so only the time of day varies.
func at(hour, min, sec int) time.Time {
	return time.Date(2026, 8, 1, hour, min, sec, 0, time.UTC)
}

// collect runs a source and fails the test on any error, since most tests are
// about what came back rather than about failure.
func collect(t *testing.T, s source.Source, since time.Time) []source.Event {
	t.Helper()
	events, err := s.Collect(t.Context(), since)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return events
}
