package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/store"
)

// A derivation is a whole answer. The table it writes is either entirely this
// run's or entirely the previous run's, and a table half computed under one set
// of parameters and half under another is a number nobody could interpret and
// nothing would flag.
//
// Until now that was a discipline two files happened to share: internal/sessions
// and internal/chains each opened their own transaction, against their own
// second handle on the database, in code neither author could see. These tests
// state the property as one the STORE holds, so that a third derivation gets it
// without being told.

// insertSession writes one legal derived_session row, distinguished by the
// second it starts at. derived_session is used rather than a table invented for
// the test because a real table brings its real CHECKs, and one of the failures
// worth measuring is a rebuild the database itself refuses partway through.
func insertSession(ctx context.Context, tx *sql.Tx, second int) error {
	at := fmt.Sprintf("2026-07-11T09:%02d:00.000000Z", second)
	end := fmt.Sprintf("2026-07-11T09:%02d:30.000000Z", second)
	_, err := tx.ExecContext(ctx, `INSERT INTO derived_session
		(started_at, ended_at, events, sources, margin_micros, computed_at,
		 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources,
		 exclude_patterns, corpus_from, corpus_to)
		VALUES (?, ?, 1, 'test', 0, ?, 900000000, 0, 0, '', '', '[]', '', '')`, at, end, at)
	return err
}

// seedDerivation puts n sessions in the table through ReplaceDerived, which is
// also the "previous derivation" the rollback tests measure against.
func seedDerivation(t *testing.T, f fixture, n int) {
	t.Helper()
	err := f.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session`); err != nil {
			return err
		}
		for i := range n {
			if err := insertSession(ctx, tx, i); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("seed a derivation of %d sessions: %v", n, err)
	}
}

func (f fixture) sessionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.raw(t).QueryRow(`SELECT COUNT(*) FROM derived_session`).Scan(&n); err != nil {
		t.Fatalf("count derived_session: %v", err)
	}
	return n
}

// firstSessionStart is the cheapest way to tell one derivation from another
// with the same row count.
func (f fixture) firstSessionStart(t *testing.T) string {
	t.Helper()
	var at string
	if err := f.raw(t).QueryRow(`SELECT MIN(started_at) FROM derived_session`).Scan(&at); err != nil {
		t.Fatalf("read the first session: %v", err)
	}
	return at
}

func TestReplaceDerivedCommitsWhatTheDerivationWrote(t *testing.T) {
	f := newStore(t)
	seedDerivation(t, f, 3)
	if got := f.sessionCount(t); got != 3 {
		t.Fatalf("derived_session holds %d rows after a successful replace, want 3", got)
	}
}

// TestAFailedReplacementLeavesThePreviousDerivationIntact is the measurement
// behind "one call that cannot half-apply".
//
// The replacement deletes all five previous rows and inserts two of its own
// before failing. Without the store's transaction the table would be left
// holding those two — a derivation that never finished, indistinguishable from
// one that did. The number that matters is 5, not "no error".
func TestAFailedReplacementLeavesThePreviousDerivationIntact(t *testing.T) {
	f := newStore(t)
	seedDerivation(t, f, 5)

	boom := errors.New("the derivation ran out of evidence halfway")
	err := f.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session`); err != nil {
			return err
		}
		// Two rows of the new derivation, deliberately starting later than the
		// old ones so that a survivor would be visible by its timestamp as well
		// as by the count.
		for i := 30; i < 32; i++ {
			if err := insertSession(ctx, tx, i); err != nil {
				return err
			}
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ReplaceDerived = %v, want the derivation's own error returned unwrapped enough to match", err)
	}

	if got := f.sessionCount(t); got != 5 {
		t.Errorf("derived_session holds %d rows after a replacement that failed partway, want the previous 5; "+
			"the table half-applied", got)
	}
	if got := f.firstSessionStart(t); got != "2026-07-11T09:00:00.000000Z" {
		t.Errorf("the first session is %s, want the previous derivation's 09:00:00; "+
			"rows from the abandoned replacement survived", got)
	}
}

// TestAReplacementTheDatabaseRefusesLeavesThePreviousDerivationIntact is the
// same property when the failure comes from SQLite rather than from Go: the
// second insert of a duplicate span violates derived_session_span, which is the
// index that exists to catch an INSERT that ran without its DELETE.
func TestAReplacementTheDatabaseRefusesLeavesThePreviousDerivationIntact(t *testing.T) {
	f := newStore(t)
	seedDerivation(t, f, 4)

	err := f.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session`); err != nil {
			return err
		}
		if err := insertSession(ctx, tx, 40); err != nil {
			return err
		}
		// The same span twice. The unique index refuses it.
		return insertSession(ctx, tx, 40)
	})
	if err == nil {
		t.Fatal("ReplaceDerived = nil on a replacement the database refused")
	}
	if got := f.sessionCount(t); got != 4 {
		t.Errorf("derived_session holds %d rows after a constraint violation partway, want the previous 4", got)
	}
}

// TestAPanicDuringAReplacementLeavesThePreviousDerivationIntact covers the
// failure a returned error cannot: a derivation that dies rather than reports.
// An uncommitted transaction is rolled back when its connection is released,
// but only if nothing has already committed it, and a helper that committed
// before checking would leave a half-written table here and nowhere else.
func TestAPanicDuringAReplacementLeavesThePreviousDerivationIntact(t *testing.T) {
	f := newStore(t)
	seedDerivation(t, f, 2)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not reach the caller; ReplaceDerived swallowed it")
			}
		}()
		_ = f.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session`); err != nil {
				return err
			}
			if err := insertSession(ctx, tx, 50); err != nil {
				return err
			}
			panic("the derivation died partway")
		})
	}()

	if got := f.sessionCount(t); got != 2 {
		t.Errorf("derived_session holds %d rows after a derivation panicked partway, want the previous 2", got)
	}
}

// TestAFailedReplacementDoesNotWedgeTheStore is the other half of the
// guarantee, and it is the half a row count cannot see.
//
// A transaction that is neither committed nor rolled back holds its connection
// and, because the store opens write transactions with _txlock=immediate, the
// database's write lock with it. The rows would look perfectly correct — an
// abandoned transaction is invisible to every other connection — and the next
// harvest would simply fail to write, five seconds of busy_timeout later, for
// no reason anybody could see in the data.
func TestAFailedReplacementDoesNotWedgeTheStore(t *testing.T) {
	f := newStore(t)
	seedDerivation(t, f, 3)

	boom := errors.New("the derivation ran out of evidence halfway")
	_ = f.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session`); err != nil {
			return err
		}
		return boom
	})

	err := f.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session`); err != nil {
			return err
		}
		return insertSession(ctx, tx, 7)
	})
	if err != nil {
		t.Fatalf("the replacement after a failed one: %v; the failed transaction was never released", err)
	}
	if got := f.sessionCount(t); got != 1 {
		t.Errorf("derived_session holds %d rows after the recovering replacement, want 1", got)
	}
}

// TestReplaceDerivedRefusesAReadOnlyStore states the refusal in words rather
// than leaving SQLite to say "attempt to write a readonly database", which
// names neither the store nor the reason it was opened that way.
func TestReplaceDerivedRefusesAReadOnlyStore(t *testing.T) {
	f := newStore(t)
	seedDerivation(t, f, 1)

	ro, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", f.path, err)
	}
	defer ro.Close()

	err = ro.ReplaceDerived(context.Background(), func(ctx context.Context, tx *sql.Tx) error {
		return insertSession(ctx, tx, 60)
	})
	if err == nil {
		t.Fatal("ReplaceDerived = nil on a store opened read-only")
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("ReplaceDerived = %q, want it to say the store was opened read-only", err)
	}
	if got := f.sessionCount(t); got != 1 {
		t.Errorf("derived_session holds %d rows after a refused write, want 1", got)
	}
}
