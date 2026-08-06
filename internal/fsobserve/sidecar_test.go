package fsobserve

import (
	"path/filepath"
	"testing"
)

// TestIsDatabaseSidecarNamesOnlyWhatCannotBeADeliverable pins the one rule
// this package is allowed to hardcode.
//
// A -wal or a -shm cannot exist without the database file it belongs to and
// carries no content of its own, so it is never anybody's work product on any
// machine — the same category as the .DS_Store this package already skips. A
// .db and a .log are NOT in that category: either can be somebody's real
// deliverable, and excluding them is configuration rather than code.
func TestIsDatabaseSidecarNamesOnlyWhatCannotBeADeliverable(t *testing.T) {
	sidecars := []string{
		".jobs.db-wal",
		".jobs.db-shm",
		"timetattle.db-wal",
		"timetattle.db-shm",
	}
	for _, name := range sidecars {
		if !IsDatabaseSidecar(name) {
			t.Errorf("%q is a SQLite sidecar and was not recognised as one", name)
		}
	}

	// Every one of these is a plausible real file somebody made on purpose.
	// -journal is deliberately absent from the rule for exactly this reason:
	// "2026-08-01-journal" is a diary entry, not a rollback journal.
	work := []string{
		".jobs.db",
		"timetattle.db",
		"access.log",
		"access.log.bz2",
		"2026-08-01-journal",
		"walrus",
		"shm",
		"MBCommon.h",
		"README.md",
	}
	for _, name := range work {
		if IsDatabaseSidecar(name) {
			t.Errorf("%q was treated as a SQLite sidecar; it is a file somebody could have made", name)
		}
	}
}

// TestCollectSkipsDatabaseSidecars is the collect-time half of the rule.
//
// Measured on the live store 2026-08-01: 86 of 130,361 fs-observations were
// .jobs.db-wal or .jobs.db-shm, and on 2026-08-01 alone seven unrelated
// repositories each showed a whole working day made of nothing but a WAL
// checkpoint, every stamp inside the same second. Volume is the wrong measure —
// one observation is enough to put a project row on a day's review screen.
func TestCollectSkipsDatabaseSidecars(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, ".jobs.db-wal"), "wal")
	write(t, filepath.Join(dir, ".jobs.db-shm"), "shm")
	real := filepath.Join(dir, ".jobs.db")
	write(t, real, "a database is a deliverable")

	events := collect(t, dir)
	if got := len(events); got != len(eventsFor(events, real)) {
		t.Errorf("got %d events total but only %d for the database itself; a sidecar leaked through",
			got, len(eventsFor(events, real)))
	}
	if len(eventsFor(events, real)) == 0 {
		t.Error("the database file itself produced no events; the rule reached past the sidecars")
	}
}
