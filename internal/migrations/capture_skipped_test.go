package migrations_test

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// 0015 adds a FIFTH capture outcome, and these tests are about the one property
// that makes it worth a migration: a run that declined to preserve knowledgeC
// by POLICY must be storable as itself.
//
// It is not 'ok' — nothing was preserved on this run. It is not the
// content-addressed reading of nothing new either, which is files_new = 0 with
// files_held = 1 and means the run read the source through and found the bytes
// already on disk. And it is emphatically not 'absent', which means the source
// is not on this machine at all. Recording it as any of those three would
// collapse "we chose not to look yet" into "we looked", which is the standing
// lesson in CLAUDE.md and a bug this project has shipped three times.

// applyRest runs every migration a database has not had yet, which is how a
// test watches ONE migration meet rows that are already there.
func applyRest(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
}

func TestCaptureRunAcceptsAPolicySkip(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "knowledgec", "/Users/you/knowledgeC.db",
		captureStart, captureEnd, "skipped", nil, 0, 0, 0); err != nil {
		t.Errorf("capture_run refused a policy skip: %v", err)
	}
}

// A skip lost nothing, so it has nothing to explain. An error on this row would
// turn "the throttle said not yet" into "knowledgeC could not be read", which
// are opposite readings and call for opposite actions.
func TestCaptureRunRefusesASkipCarryingAnError(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
		"skipped", "cannot read /x", 0, 0, 0); err == nil {
		t.Error("capture_run accepted a skipped capture carrying an error; the row argues with itself")
	}
}

// A skipped capture did not read the source, so it cannot have written a file,
// held one, or produced a byte. Without this the fifth status could be stamped
// over a row that really did preserve something, and every file and byte total
// in the store would quietly disagree with the directory.
func TestCaptureRunRefusesASkipThatClaimsToHavePreservedSomething(t *testing.T) {
	db := migrated(t)
	for _, c := range []struct {
		what                       string
		filesNew, filesHeld, bytes int
	}{
		{"wrote a file", 1, 0, 0},
		{"held a file", 0, 1, 0},
		{"produced bytes", 0, 0, 1_500_000},
	} {
		if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
			"skipped", nil, c.filesNew, c.filesHeld, c.bytes); err == nil {
			t.Errorf("capture_run accepted a skipped capture that %s", c.what)
		}
	}
}

// The fifth status is added by rebuilding the table, because SQLite has no way
// to widen a CHECK in place. A rebuild is the moment a constraint gets dropped
// by accident, so the four outcomes 0007 argued for are re-asserted here rather
// than assumed to have survived.
func TestCaptureRunKeepsItsOtherConstraintsAcrossTheRebuild(t *testing.T) {
	db := migrated(t)
	okCapture(t, db)

	for _, c := range []struct {
		what              string
		status, failure   any
		started, finished any
		filesNew          any
	}{
		{"status 'running'", "running", nil, captureStart, captureEnd, 0},
		{"an empty status", "", nil, captureStart, captureEnd, 0},
		{"a failure with no reason", "failed", nil, captureStart, captureEnd, 0},
		{"an ok row carrying an error", "ok", "cannot read /x", captureStart, captureEnd, 1},
		{"a row with no end", "ok", nil, captureStart, nil, 1},
		{"an end before its start", "ok", nil, captureEnd, captureStart, 1},
		{"a timestamp that is not the store layout", "ok", nil, "2026-08-01T13:04:35Z", captureEnd, 1},
		{"a negative count", "ok", nil, captureStart, captureEnd, -1},
	} {
		if err := insertCapture(db, "knowledgec", "/x", c.started, c.finished,
			c.status, c.failure, c.filesNew, 0, 0); err == nil {
			t.Errorf("after the rebuild capture_run accepted %s", c.what)
		}
	}
}

// The rebuild carries the rows across, ids and all. A capture_run row is the
// only record that a perishable source was preserved on a given day, so losing
// one to a schema change would destroy exactly the history 0007 exists to keep.
func TestTheRebuildCarriesEveryRecordedCaptureAcross(t *testing.T) {
	db := atVersion(t, 14)

	rows := [][]any{
		{"knowledgec", "/Users/you/knowledgeC.db", "ok", nil, 1, 0, int64(1_500_000)},
		{"biome", "/Users/you/Library/Biome", "ok", nil, 0, 13, int64(0)},
		{"motion-vault", "/Users/you/Autosave Vault", "absent", nil, 0, 0, int64(0)},
		{"filedays", "/Users/you/git", "failed", "cannot read /Users/you/git", 0, 0, int64(0)},
	}
	for _, r := range rows {
		if err := insertCapture(db, r[0], r[1], captureStart, captureEnd, r[2], r[3], r[4], r[5], r[6]); err != nil {
			t.Fatalf("insert a %s capture before the rebuild: %v", r[0], err)
		}
	}

	before := captureFingerprint(t, db)
	applyRest(t, db)
	if after := captureFingerprint(t, db); after != before {
		t.Errorf("the rebuild changed capture_run:\nbefore\n%s\nafter\n%s", before, after)
	}
}

// atVersion is a database with every migration through v applied and no later
// one. It refuses a version at or above the newest, so a test written this way
// cannot silently start asserting against a schema that no longer changes.
func atVersion(t *testing.T, v int) *sql.DB {
	t.Helper()
	db := open(t)
	all, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}
	var upTo []migrations.Script
	for _, s := range all {
		if s.Version <= v {
			upTo = append(upTo, s)
		}
	}
	if len(upTo) == len(all) {
		t.Fatalf("there is no migration above version %d, so this test is asserting against a schema "+
			"that no longer changes", v)
	}
	if err := migrations.ApplyScripts(db, upTo); err != nil {
		t.Fatalf("apply migrations up to %d: %v", v, err)
	}
	return db
}

// captureFingerprint renders every capture_run row, ids included, so that "the
// rows came through unchanged" is a comparison rather than a hope.
func captureFingerprint(t *testing.T, db *sql.DB) string {
	t.Helper()
	rows, err := db.Query(`SELECT id, kind, input, started_at, finished_at, status,
		COALESCE(error, '<null>'), files_new, files_held, bytes FROM capture_run ORDER BY id`)
	if err != nil {
		t.Fatalf("read capture_run: %v", err)
	}
	defer rows.Close()

	var out strings.Builder
	for rows.Next() {
		var (
			id                                              int64
			kind, input, started, finished, status, failure string
			filesNew, filesHeld                             int
			bytes                                           int64
		)
		if err := rows.Scan(&id, &kind, &input, &started, &finished, &status, &failure,
			&filesNew, &filesHeld, &bytes); err != nil {
			t.Fatalf("scan capture_run: %v", err)
		}
		out.WriteString(fmt.Sprintf("%d|%s|%s|%s|%s|%s|%s|%d|%d|%d\n",
			id, kind, input, started, finished, status, failure, filesNew, filesHeld, bytes))
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read capture_run: %v", err)
	}
	return out.String()
}
