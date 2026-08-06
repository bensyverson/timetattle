package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/store"
)

// THE CLIENT-PROJECT LOG AT THE STORE BOUNDARY.
//
// The pure fold is internal/clientmap and is tested there. What these pin is
// what the store adds to it: that a row round-trips with BOTH of its timestamps
// intact, that "from the beginning of time" survives as NULL rather than as a
// sentinel instant, and that the log is genuinely append-only rather than
// append-only by convention.
//
// Names here are invented. The live client names are a confidentiality matter
// (task 5nCl4) and none of them appears in this repository.

func assertion(project, client, source string, from time.Time) clientmap.Assertion {
	return clientmap.Assertion{
		Project: project, Client: client, Source: source,
		EffectiveFrom: from, RecordedAt: nine,
	}
}

// The round trip. Both timestamps matter and they mean different things, so a
// store that collapsed them — or that wrote recorded_at into effective_from —
// would turn every correction into a reassignment dated today, silently.
func TestAClientAssertionRoundTripsWithBothOfItsTimestamps(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	moved := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	if err := f.AppendClientAssertion(ctx, assertion("loom", "atelier", "/git/loom", moved), "a-person"); err != nil {
		t.Fatalf("AppendClientAssertion: %v", err)
	}

	got, err := f.ClientAssertions(ctx)
	if err != nil {
		t.Fatalf("ClientAssertions: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("read back %d assertions, want 1", len(got))
	}
	a := got[0]
	if a.Project != "loom" || a.Client != "atelier" || a.Source != "/git/loom" {
		t.Errorf("assertion round-tripped as %+v", a)
	}
	if !a.EffectiveFrom.Equal(moved) {
		t.Errorf("effective_from is %v, want %v; a reassignment that loses its date becomes a "+
			"retroactive correction and rewrites work that was done for somebody else",
			a.EffectiveFrom, moved)
	}
	if !a.RecordedAt.Equal(nine) {
		t.Errorf("recorded_at is %v, want %v", a.RecordedAt, nine)
	}
	if a.ID == 0 {
		t.Error("the assertion has no id; the fold tiebreaks on it and would stop being " +
			"deterministic for two assertions recorded in the same microsecond")
	}
}

// "From the beginning of time" is NULL in the column and the zero time in Go.
// A sentinel date would be a value somebody can compare against and get a true
// answer from by accident; the zero time cannot be mistaken for a real instant.
func TestACorrectionStoresNoEffectiveDateRatherThanASentinelOne(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	if err := f.AppendClientAssertion(ctx, assertion("loom", "atelier", "/git/loom", time.Time{}), "a-person"); err != nil {
		t.Fatalf("AppendClientAssertion: %v", err)
	}

	var nulls int
	if err := f.raw(t).QueryRow(
		`SELECT COUNT(*) FROM client_project WHERE effective_from IS NULL`).Scan(&nulls); err != nil {
		t.Fatalf("read effective_from: %v", err)
	}
	if nulls != 1 {
		t.Errorf("%d rows have a NULL effective_from, want 1; a correction covers all of time "+
			"and storing an early date instead makes it a reassignment nobody dated", nulls)
	}

	got, err := f.ClientAssertions(ctx)
	if err != nil {
		t.Fatalf("ClientAssertions: %v", err)
	}
	if !got[0].EffectiveFrom.IsZero() {
		t.Errorf("effective_from read back as %v, want the zero time", got[0].EffectiveFrom)
	}
}

// APPEND-ONLY IS A PROPERTY OF THE DATABASE, NOT A DISCIPLINE. 0008 makes the
// same guarantee with the same mechanism, and the reason is the same: a change
// of mind is itself a fact, and an UPDATE records only the latest state while
// silently forgetting that anybody thought otherwise.
func TestTheClientProjectLogRefusesUpdatesAndDeletes(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	if err := f.AppendClientAssertion(ctx, assertion("loom", "atelier", "/git/loom", time.Time{}), "a-person"); err != nil {
		t.Fatalf("AppendClientAssertion: %v", err)
	}
	db := f.raw(t)

	if _, err := db.Exec(`UPDATE client_project SET client = 'bindery'`); err == nil {
		t.Error("an assertion was updated in place; the answer it used to give is now unrecoverable")
	}
	if _, err := db.Exec(`DELETE FROM client_project`); err == nil {
		t.Error("an assertion was deleted; a period that WAS attributed now looks as though it never was")
	}
}

// The whole point of the log, at the store boundary: two assertions, and the
// answer depends on which instant is asked about.
func TestTheStoredLogFoldsToDifferentAnswersAtDifferentInstants(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	june := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	july := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	first := assertion("loom", "atelier", "/git/loom", time.Time{})
	first.RecordedAt = june
	second := assertion("loom", "bindery", "/git/loom", july)
	second.RecordedAt = july
	for _, a := range []clientmap.Assertion{first, second} {
		if err := f.AppendClientAssertion(ctx, a, "a-person"); err != nil {
			t.Fatalf("AppendClientAssertion: %v", err)
		}
	}

	log, err := f.ClientAssertions(ctx)
	if err != nil {
		t.Fatalf("ClientAssertions: %v", err)
	}
	m := clientmap.Fold(log)
	if got, _ := m.At("loom", june); got != "atelier" {
		t.Errorf("June folds to %q, want atelier", got)
	}
	if got, _ := m.At("loom", july); got != "bindery" {
		t.Errorf("July folds to %q, want bindery", got)
	}
}

// A store with no log at all is not a store whose projects have no client — it
// is a store nobody has asserted anything in. Absence of a row is not an
// observation, and an empty slice with no error is how that is said.
func TestAnEmptyLogReadsAsEmptyRatherThanAsAnError(t *testing.T) {
	f := newStore(t)

	got, err := f.ClientAssertions(context.Background())
	if err != nil {
		t.Fatalf("ClientAssertions on a fresh store: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("a fresh store reports %d assertions, want 0", len(got))
	}
	if len(clientmap.Fold(got).Collisions()) != 0 {
		t.Error("an empty log reported a collision")
	}
}

// A read-only store must refuse to append, in words that name the store and why
// — not by SQLite's "attempt to write a readonly database".
func TestAReadOnlyStoreRefusesToAppendAnAssertion(t *testing.T) {
	f := newStore(t)
	ro, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if err := ro.AppendClientAssertion(context.Background(),
		assertion("loom", "atelier", "/git/loom", time.Time{}), "a-person"); err == nil {
		t.Error("a read-only store appended an assertion")
	}
}
