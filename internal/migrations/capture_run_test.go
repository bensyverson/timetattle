package migrations_test

import (
	"database/sql"
	"testing"
)

// capture_run is the table 0007 adds, and these tests drive it with raw SQL for
// the same reason the rest of this package's schema tests do: internal/store
// refuses the same shapes in Go, and Go can be bypassed by a script, by a future
// collector writing its own insert, or by sqlite3 on the command line. The
// constraints are what make a dishonest row impossible rather than merely
// discouraged.
//
// Every CHECK below is written against a NOT NULL column or carries its own IS
// NOT NULL guard, because a SQLite CHECK PASSES when it evaluates to NULL. This
// schema was bitten by exactly that in 0001.

// insertCapture writes one capture_run row. Every field is any so that a test
// can write NULL where Go would insist on a string.
func insertCapture(db *sql.DB, kind, input, started, finished, status any, failure any,
	filesNew, filesHeld, bytes any) error {
	_, err := db.Exec(`INSERT INTO capture_run
		(kind, input, started_at, finished_at, status, error, files_new, files_held, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		kind, input, started, finished, status, failure, filesNew, filesHeld, bytes)
	return err
}

const (
	captureStart = "2026-08-01T13:04:35.000000Z"
	captureEnd   = "2026-08-01T13:04:41.000000Z"
)

// okCapture is the ordinary row: a capture that ran and read its source through.
func okCapture(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := insertCapture(db, "knowledgec", "/Users/you/knowledgeC.db",
		captureStart, captureEnd, "ok", nil, 1, 0, 4_100_000); err != nil {
		t.Fatalf("an ordinary successful capture was refused: %v", err)
	}
}

func TestCaptureRunAcceptsTheFourOutcomes(t *testing.T) {
	db := migrated(t)
	okCapture(t, db)
	if err := insertCapture(db, "biome", "/Users/you/Library/Biome",
		captureStart, captureEnd, "partial", "cannot read stream: operation not permitted", 3, 2, 900); err != nil {
		t.Errorf("a partial capture was refused: %v", err)
	}
	if err := insertCapture(db, "motion-vault", "/Users/you/Autosave Vault",
		captureStart, captureEnd, "failed", "cannot read vault: operation not permitted", 0, 0, 0); err != nil {
		t.Errorf("a failed capture was refused: %v", err)
	}
	// absent is the row this whole table exists for. A harvest that looked at
	// the vault root and found no Motion installed has OBSERVED something, and
	// that observation must be storable, or it is indistinguishable from a
	// harvest that never got that far.
	if err := insertCapture(db, "filedays", "(nothing configured)",
		captureStart, captureEnd, "absent", nil, 0, 0, 0); err != nil {
		t.Errorf("an absent capture was refused: %v", err)
	}
}

func TestCaptureRunRefusesAnUnknownStatus(t *testing.T) {
	db := migrated(t)
	// 'running' in particular: harvest_run has it and capture_run must not.
	// A capture row is written after the capture ended, from times measured
	// around it, so there is no instant at which a live capture row exists —
	// and a status that could be left at 'running' for ever is precisely the
	// dishonesty this table was added to remove rather than to copy.
	if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
		"running", nil, 0, 0, 0); err == nil {
		t.Error("capture_run accepted status 'running'; a capture row is written after the fact and can never be live")
	}
	if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
		"", nil, 0, 0, 0); err == nil {
		t.Error("capture_run accepted an empty status")
	}
}

func TestCaptureRunRefusesAFailureWithNoReason(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
		"failed", nil, 0, 0, 0); err == nil {
		t.Error("capture_run accepted a failed capture with no error; an unexplained loss reads as a source with nothing to report")
	}
	if err := insertCapture(db, "biome", "/x", captureStart, captureEnd,
		"partial", nil, 1, 0, 0); err == nil {
		t.Error("capture_run accepted a partial capture with no error; what was lost would be unrecoverable")
	}
}

func TestCaptureRunRefusesASuccessCarryingAnError(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
		"ok", "cannot read /x", 1, 0, 0); err == nil {
		t.Error("capture_run accepted an ok capture carrying an error; the row argues with itself")
	}
	if err := insertCapture(db, "filedays", "/x", captureStart, captureEnd,
		"absent", "cannot read /x", 0, 0, 0); err == nil {
		t.Error("capture_run accepted an absent capture carrying an error; nothing-there and could-not-look are different facts")
	}
}

// TestCaptureRunRefusesARowWithNoEnd is the difference from harvest_run that
// matters most. harvest_run.finished_at is nullable, and the live store has
// carried row 28 — claude-transcripts, started 2026-08-01T13:07:13Z — with a
// NULL finished_at ever since the process died under it. A capture row is
// written from times measured around a capture that has already returned, so
// there is no honest way for one to lack an end.
func TestCaptureRunRefusesARowWithNoEnd(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "knowledgec", "/x", captureStart, nil,
		"ok", nil, 1, 0, 0); err == nil {
		t.Error("capture_run accepted a row with no finished_at; a capture that never ended cannot have been recorded")
	}
	if err := insertCapture(db, "knowledgec", "/x", nil, captureEnd,
		"ok", nil, 1, 0, 0); err == nil {
		t.Error("capture_run accepted a row with no started_at")
	}
}

func TestCaptureRunRefusesAnEndBeforeItsStart(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "knowledgec", "/x", captureEnd, captureStart,
		"ok", nil, 1, 0, 0); err == nil {
		t.Error("capture_run accepted a capture that finished before it started")
	}
}

// TestCaptureRunRefusesTimestampsThatAreNotTheStoreLayout guards the
// precondition CHECK (finished_at >= started_at) silently depends on.
//
// That comparison is a TEXT comparison, and TEXT comparison is chronological
// only while every value has the same shape. A caller writing RFC3339 produces
// values that satisfy the ordering CHECK and sort wrongly anyway:
// '2026-08-01T14:00:00Z' compares GREATER than '2026-08-01T14:00:00.500000Z',
// because 'Z' (0x5A) sorts above '.' (0x2E) — so the earlier instant sorts
// after the later one, and every read that orders on these columns is wrong
// near a second boundary.
//
// 0003 learned this and guards it at line 87. This table did not, and the
// invariant lived in a column comment instead — which is the failure mode the
// 2026-07-31 handoff already recorded once: an invariant "written in the code
// as a comment and false".
func TestCaptureRunRefusesTimestampsThatAreNotTheStoreLayout(t *testing.T) {
	db := migrated(t)
	for _, c := range []struct {
		what              string
		started, finished string
	}{
		// RFC3339 to the second: 20 characters where the layout writes 27.
		{"RFC3339 seconds", "2026-08-01T13:04:35Z", "2026-08-01T13:04:41Z"},
		// Right length, wrong instant. 27 characters, but the zone is -05:00
		// and the fraction has been truncated to one digit to make room. A
		// length check alone admits this, and the value is five hours out.
		{"27 characters but not UTC", "2026-08-01T08:04:35.0-05:00", "2026-08-01T08:04:41.0-05:00"},
		// Millisecond precision, a plausible thing for another tool to write.
		{"milliseconds", "2026-08-01T13:04:35.000Z", "2026-08-01T13:04:41.000Z"},
	} {
		if err := insertCapture(db, "knowledgec", "/x", c.started, c.finished,
			"ok", nil, 1, 0, 0); err == nil {
			t.Errorf("capture_run accepted %s timestamps (%q); "+
				"the ordering CHECK is a TEXT comparison and is only chronological at one fixed shape",
				c.what, c.started)
		}
	}
}

// TestMixedTimestampShapesSortWrongly is the measurement the guard above rests
// on, kept as an executable fact rather than as a claim in a comment.
//
// It asserts a property of SQLite's TEXT comparison and not of this schema, so
// it deliberately does not touch capture_run — once the guard is in place, the
// rows that would demonstrate the breakage can no longer be inserted, and a
// test that could only be written by removing the guard is not a test. What it
// pins down is WHY the guard has to exist: 'Z' is 0x5A and '.' is 0x2E, so a
// timestamp written to the second sorts ABOVE a sub-second timestamp inside the
// same second. The earlier instant compares as the later one.
func TestMixedTimestampShapesSortWrongly(t *testing.T) {
	db := open(t)
	const (
		rfc3339 = "2026-08-01T14:00:00Z"        // 14:00:00.000000
		layout  = "2026-08-01T14:00:00.500000Z" // half a second LATER
	)
	var rfcSortsFirst bool
	if err := db.QueryRow(`SELECT ? < ?`, rfc3339, layout).Scan(&rfcSortsFirst); err != nil {
		t.Fatalf("compare: %v", err)
	}
	if rfcSortsFirst {
		t.Fatalf("%q sorts before %q, so mixing shapes would be harmless and the "+
			"length and shape CHECKs on capture_run are guarding nothing", rfc3339, layout)
	}
	t.Logf("%q sorts AFTER %q although it is half a second earlier; "+
		"this is what a mixed-shape column does to every ORDER BY and every range read",
		rfc3339, layout)
}

func TestCaptureRunRefusesNegativeCounts(t *testing.T) {
	db := migrated(t)
	for _, c := range []struct {
		what                        string
		filesNew, filesHeld, nbytes any
	}{
		{"files_new", -1, 0, 0},
		{"files_held", 0, -1, 0},
		{"bytes", 0, 0, -1},
	} {
		if err := insertCapture(db, "knowledgec", "/x", captureStart, captureEnd,
			"ok", nil, c.filesNew, c.filesHeld, c.nbytes); err == nil {
			t.Errorf("capture_run accepted a negative %s", c.what)
		}
	}
}

func TestCaptureRunRefusesAnUnnamedKindOrInput(t *testing.T) {
	db := migrated(t)
	if err := insertCapture(db, "", "/x", captureStart, captureEnd, "ok", nil, 1, 0, 0); err == nil {
		t.Error("capture_run accepted a row naming no kind")
	}
	if err := insertCapture(db, "knowledgec", "", captureStart, captureEnd, "ok", nil, 1, 0, 0); err == nil {
		t.Error("capture_run accepted a row naming nothing it read")
	}
}

// TestCaptureRunWritesNothingToEventOrHarvestRun holds the separation the
// migration argues for: capture rows live in their own table and the ingestion
// provenance chain is untouched by them.
func TestCaptureRunWritesNothingToEventOrHarvestRun(t *testing.T) {
	db := migrated(t)
	okCapture(t, db)
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM harvest_run`).Scan(&n); err != nil {
		t.Fatalf("count harvest_run: %v", err)
	}
	if n != 0 {
		t.Errorf("recording a capture wrote %d harvest_run rows; it must write none", n)
	}
	if got := events(t, db); got != 0 {
		t.Errorf("recording a capture wrote %d events; it must write none", got)
	}
}
