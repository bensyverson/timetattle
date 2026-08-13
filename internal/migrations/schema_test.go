package migrations_test

import (
	"database/sql"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// The tests in this file drive the schema with raw SQL rather than through
// internal/store, deliberately. source.Event.Validate refuses the same shapes
// in Go, but Go can be bypassed — by a future collector writing its own insert,
// by a script, by sqlite3 on the command line — and the constraints are what
// make the shapes impossible rather than merely discouraged.

// migrated returns a database with the schema applied.
func migrated(t *testing.T) *sql.DB {
	t.Helper()
	db := open(t)
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	return db
}

// aRun inserts a harvest_run for events to hang off, since first_seen_run is
// NOT NULL and references it.
func aRun(t *testing.T, db *sql.DB) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status)
		VALUES ('test-source', '2026-07-14T09:00:00.000000Z', '/tmp/stream', 'running')`)
	if err != nil {
		t.Fatalf("insert harvest_run: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

// row is one event's worth of column values. Every field is any so that a test
// can write NULL where Go would insist on a string.
type row struct {
	Source   any
	Shape    any
	Unknown  any
	Started  any
	Ended    any
	App      any
	Document any
	State    any
	Locator  any
	Cause    any
}

func insert(db *sql.DB, run int64, r row) error {
	source := r.Source
	if source == nil {
		source = "test-source"
	}
	_, err := db.Exec(`INSERT INTO event
		(first_seen_run, source_id, shape, unknown, started_at, ended_at, app, document, state, locator, cause)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run, source, r.Shape, r.Unknown, r.Started, r.Ended, r.App, r.Document, r.State, r.Locator, r.Cause)
	return err
}

func events(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

const (
	at0901 = "2026-07-14T09:01:12.000000Z"
	at0902 = "2026-07-14T09:02:00.000000Z"
)

// Every timestamp in this database is fixed-width UTC text, so that ordering it
// as text orders it in time. The migration ledger is written by SQLite rather
// than by Go and so is the one place that could quietly disagree.
func TestTheMigrationLedgerRecordsTimeTheSameWayEverythingElseDoes(t *testing.T) {
	db := migrated(t)

	var appliedAt string
	if err := db.QueryRow(`SELECT applied_at FROM schema_migration WHERE version = 1`).Scan(&appliedAt); err != nil {
		t.Fatalf("read applied_at: %v", err)
	}
	// The same layout as internal/store.TimeLayout, spelled out rather than
	// imported: the schema is below the store and must not depend on it.
	if _, err := time.Parse("2006-01-02T15:04:05.000000Z", appliedAt); err != nil {
		t.Errorf("applied_at is %q, which is not the store's timestamp format: %v", appliedAt, err)
	}
}

func TestTheDatabaseRefusesAnIntervalMissingAnEndpoint(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "interval", Started: at0901}); err == nil {
		t.Error("an interval with no end was accepted; only an unbounded event may lack one")
	}
	if err := insert(db, run, row{Shape: "interval", Ended: at0902}); err == nil {
		t.Error("an interval with no start was accepted")
	}
}

func TestTheDatabaseRefusesAnIntervalThatEndsBeforeItStarts(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "interval", Started: at0902, Ended: at0901}); err == nil {
		t.Error("an interval ending before it starts was accepted")
	}
}

func TestTheDatabaseRefusesAPointWhoseEndsDiffer(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "point", Started: at0901, Ended: at0902}); err == nil {
		t.Error("a point with two different endpoints was accepted; a point is an instant")
	}
	if err := insert(db, run, row{Shape: "point", Started: at0901}); err == nil {
		t.Error("a point with no end was accepted")
	}
}

func TestTheDatabaseRefusesAMalformedUnboundedEvent(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "unbounded", Unknown: "sleep", Started: at0901, Ended: at0902}); err == nil {
		t.Error("an unbounded event with both endpoints was accepted; it is an interval")
	}
	if err := insert(db, run, row{Shape: "unbounded", Unknown: "sleep"}); err == nil {
		t.Error("an unbounded event with neither endpoint was accepted; nothing was observed")
	}
	if err := insert(db, run, row{Shape: "unbounded", Ended: at0901}); err == nil {
		t.Error("an unbounded event was accepted without saying why its endpoint is missing")
	}
	if err := insert(db, run, row{Shape: "unbounded", Unknown: "guesswork", Ended: at0901}); err == nil {
		t.Error("an unbounded event was accepted with a reason that is not one of the three")
	}
}

func TestTheDatabaseRefusesAReasonOnABoundedEvent(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "point", Unknown: "sleep", Started: at0901, Ended: at0901}); err == nil {
		t.Error("a point claiming a missing endpoint was accepted; it has both")
	}
}

func TestTheDatabaseRefusesAnUnknownShape(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "instant", Started: at0901, Ended: at0901}); err == nil {
		t.Error("shape \"instant\" was accepted; inference branches on shape and must know all of them")
	}
}

// A relative locator never prefix-matches a registered directory, so the work
// attributes to nothing and disappears — the failure this project exists to
// catch. The empty string is refused for the same reason, which is why the
// store writes NULL, rather than an empty string, for evidence that observes
// no file at all.
func TestTheDatabaseRefusesALocatorThatIsNotAnAbsolutePath(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "point", Started: at0901, Ended: at0901, Locator: "Movies/comp.motn"}); err == nil {
		t.Error("a relative locator was accepted")
	}
	if err := insert(db, run, row{Shape: "point", Started: at0901, Ended: at0901, Locator: ""}); err == nil {
		t.Error("an empty locator was accepted; absent evidence of a file must be NULL")
	}
}

// A zero-length observation satisfies BOTH the point and the interval
// constraints, and nothing in the schema can tell which was meant. That is why
// shape must reflect what the source claims to have observed and never the
// measured duration: knowledgeC reports a start and an end, so its rows are
// intervals even when the two are equal, and a collector that chose by duration
// would store the same observation under both shapes — where dedup cannot help,
// because shape is part of the natural key.
func TestAZeroLengthObservationIsLegalUnderEitherShape(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "interval", Started: at0901, Ended: at0901, State: "on"}); err != nil {
		t.Errorf("a zero-length interval was refused: %v", err)
	}
	if err := insert(db, run, row{Shape: "point", Started: at0901, Ended: at0901, State: "on"}); err != nil {
		t.Errorf("a zero-length point was refused: %v", err)
	}
	if got := events(t, db); got != 2 {
		t.Errorf("the two rows collapsed to %d; shape is part of the natural key and must separate them", got)
	}
}

// SQLite treats NULLs as DISTINCT in a unique index, so without the COALESCE in
// event_identity two identical bare stops would both insert and every harvest
// would duplicate all 382 of them. This test fails if the COALESCE is dropped.
func TestIdenticalBareStopsCollideOnTheNaturalKey(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	stop := row{Shape: "unbounded", Unknown: "sleep", Ended: at0901, App: "com.apple.Safari"}
	if err := insert(db, run, stop); err != nil {
		t.Fatalf("first bare stop: %v", err)
	}
	if err := insert(db, run, stop); err == nil {
		t.Error("two identical bare stops both inserted; the natural key must collapse them")
	}
}

// knowledgeC writes contradictory overlapping rows: on 2026-07-14 at 09:01:12
// both an isBacklit=1 and an isBacklit=0 row exist with identical endpoints.
// They are two observations, and state is in the key so that both survive.
func TestTwoObservationsDifferingOnlyInStateBothStore(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	for _, state := range []string{"on", "off"} {
		if err := insert(db, run, row{Shape: "interval", Started: at0901, Ended: at0902, State: state}); err != nil {
			t.Fatalf("insert state=%s: %v", state, err)
		}
	}
	if got := events(t, db); got != 2 {
		t.Errorf("got %d rows, want 2; contradictory presence rows must both be kept", got)
	}
}

// Locator is provenance rather than identity: a segment replayed from a
// snapshot rather than the live stream is the same observation.
func TestTwoObservationsDifferingOnlyInLocatorCollide(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	base := row{Shape: "point", Started: at0901, Ended: at0901, Document: "dark-crystal-v2"}
	live := base
	live.Locator = "/Users/you/Movies/dark-crystal-v2.motn"
	replay := base
	replay.Locator = "/Users/you/.timetattle/snapshots/dark-crystal-v2.motn"

	if err := insert(db, run, live); err != nil {
		t.Fatalf("first locator: %v", err)
	}
	if err := insert(db, run, replay); err == nil {
		t.Error("the same observation read from two paths inserted twice; locator is not identity")
	}
}

// The cause of an artifact is what decides whether it is evidence of a person or
// only of a running application, so a value nothing recognises must not be
// storable. Both arms matter: the constraint has to admit what the sources
// actually emit and refuse what they do not.
func TestTheDatabaseRefusesACauseNothingRecognises(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	for _, cause := range []string{"user-action", "timer", "recovery"} {
		r := row{Shape: "point", Started: at0901, Ended: at0901, Document: cause, Cause: cause}
		if err := insert(db, run, r); err != nil {
			t.Errorf("cause %q was refused: %v", cause, err)
		}
	}
	// NULL is how everything that is not an artifact reports itself: a focus
	// interval and a presence row are caused by nothing a manifest describes.
	if err := insert(db, run, row{Shape: "interval", Started: at0901, Ended: at0902, App: "com.apple.Safari"}); err != nil {
		t.Errorf("an event with no cause was refused: %v", err)
	}
	if err := insert(db, run, row{Shape: "point", Started: at0902, Ended: at0902, Cause: "human"}); err == nil {
		t.Error("cause 'human' inserted; inference switches on this value and would fall through its default arm")
	}
}

// Cause is fixed for a source rather than read per observation, so source_id
// already carries it and the key would only get wider. It is also a declaration
// that can be CORRECTED: a manifest author who fixes a mis-stated cause must not
// thereby turn every file they have already harvested into a second observation
// of itself.
func TestTwoObservationsDifferingOnlyInCauseCollide(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	base := row{Shape: "point", Started: at0901, Ended: at0901, Document: "dark-crystal-v2"}
	declared := base
	declared.Cause = "timer"
	corrected := base
	corrected.Cause = "user-action"

	if err := insert(db, run, declared); err != nil {
		t.Fatalf("first cause: %v", err)
	}
	if err := insert(db, run, corrected); err == nil {
		t.Error("one artifact stored twice because its declared cause changed; cause is not identity")
	}
}

// The canonical view names its columns, so a column added to the table does not
// reach it on its own — and Events reads the view. A cause that stopped at the
// table would be invisible to every ordinary read.
func TestTheCanonicalViewCarriesTheCause(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	if err := insert(db, run, row{Shape: "point", Started: at0901, Ended: at0901,
		Document: "dark-crystal-v2", Cause: "timer"}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var cause string
	if err := db.QueryRow(`SELECT cause FROM canonical_event`).Scan(&cause); err != nil {
		t.Fatalf("read cause from canonical_event: %v", err)
	}
	if cause != "timer" {
		t.Errorf("canonical_event reports cause %q, want \"timer\"", cause)
	}
}

func TestAnEventMustReferenceARealHarvestRun(t *testing.T) {
	db := migrated(t)

	if err := insert(db, 999, row{Shape: "point", Started: at0901, Ended: at0901}); err == nil {
		t.Error("an event referencing a nonexistent run was accepted; provenance is not optional")
	}
}

func TestTheDatabaseRefusesARunThatCannotBeAccountedFor(t *testing.T) {
	db := migrated(t)

	if _, err := db.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status)
		VALUES ('s', '2026-07-14T09:00:00.000000Z', '/tmp', 'aborted')`); err == nil {
		t.Error("status \"aborted\" was accepted; a run is running, ok or failed")
	}
	if _, err := db.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status)
		VALUES ('s', '2026-07-14T09:00:00.000000Z', '/tmp', 'failed')`); err == nil {
		t.Error("a failed run was accepted without saying why; the loss would be invisible")
	}
}

// The canonical view is part of the schema rather than a convenience in
// internal/store, so that a script, a report or a future server reads collapsed
// observations by default too. Anything that has to remember to collapse
// eventually forgets, and the failure mode is double-counted hours.
func TestTheCanonicalViewCollapsesACompletedHorizonAndNothingElse(t *testing.T) {
	db := migrated(t)
	run := aRun(t, db)

	canonical := func() int {
		t.Helper()
		var n int
		if err := db.QueryRow(`SELECT COUNT(*) FROM canonical_event`).Scan(&n); err != nil {
			t.Fatalf("read canonical_event: %v", err)
		}
		return n
	}

	// A horizon and the interval that completes it: one observation, twice.
	horizon := row{Shape: "unbounded", Unknown: "horizon", Started: at0901, App: "com.apple.motionapp"}
	if err := insert(db, run, horizon); err != nil {
		t.Fatalf("insert the horizon: %v", err)
	}
	if got := canonical(); got != 1 {
		t.Fatalf("an uncompleted horizon is not in the canonical view (%d rows)", got)
	}
	if err := insert(db, run, row{Shape: "interval", Started: at0901, Ended: at0902, App: "com.apple.motionapp"}); err != nil {
		t.Fatalf("insert the completing interval: %v", err)
	}
	if got := canonical(); got != 1 {
		t.Errorf("the canonical view holds %d rows, want 1: the completing interval alone", got)
	}
	var shape string
	if err := db.QueryRow(`SELECT shape FROM canonical_event`).Scan(&shape); err != nil {
		t.Fatalf("read the surviving row: %v", err)
	}
	if shape != "interval" {
		t.Errorf("the surviving row is a %s; the more complete observation must win", shape)
	}
	if got := events(t, db); got != 2 {
		t.Errorf("the table holds %d rows, want 2; supersession is a derivation and never a mutation", got)
	}

	// A bare stop is never superseded, not even by an interval ending at the
	// instant it was seen. Nothing will ever complete it, and 6.42h of five
	// confirmed days hangs off these.
	stop := row{Shape: "unbounded", Unknown: "sleep", Ended: at0902, App: "com.apple.Safari"}
	if err := insert(db, run, stop); err != nil {
		t.Fatalf("insert the bare stop: %v", err)
	}
	if err := insert(db, run, row{Shape: "interval", Started: at0901, Ended: at0902, App: "com.apple.Safari"}); err != nil {
		t.Fatalf("insert the interval beside it: %v", err)
	}
	if got := canonical(); got != 3 {
		t.Errorf("the canonical view holds %d rows, want 3; the bare stop must survive", got)
	}
}

// TestTheRegistryRefusesAPathPrefixMatchingCannotUse used to exercise the
// CHECK constraints on the "registry" table 0001 shipped. Migration 0006
// (task o9W0x) drops that table outright: it predates the marker-file
// design actually built (internal/registry writes a marker file into the
// directory itself, indexed at ~/.timetattle/registered-directories.json),
// and nothing in this codebase ever read or wrote the SQL table. There is
// nothing left in the schema for this test to exercise, so it is removed
// rather than kept disabled — see TestApplyDropsTheVestigialRegistryTable
// in migrations_test.go for the regression coverage on the drop itself.
