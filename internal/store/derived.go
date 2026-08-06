package store

// Derived tables live in the same database as the evidence, and are written
// through here.
//
// A derivation is an inference: sittings clustered out of events, save-as
// chains reconstructed from filenames. It is recomputable from scratch at any
// moment, it is never a place to put a fact, and it is replaced wholesale
// rather than updated — adding one observation can merge two chains, split
// none, and move the first step of a lineage earlier, so "update the rows that
// changed" is not a smaller problem than recomputing.
//
// # Why this is a method on Store rather than a handle
//
// Both derivations used to open their own *sql.DB on the store file, with the
// DSN copied into two commands, because Store deliberately exposes no handle:
// handing out a *sql.DB would make "events are never edited" a convention
// rather than a property of the API. The cost was that "either entirely one
// derivation or entirely the previous one" became a discipline two files
// happened to share, each written by someone who could not see the other, and
// that nothing at all enforced one writer at a time.
//
// ReplaceDerived gives out a transaction instead of a handle. It is scoped to
// one call, it cannot outlive it, and it is the only way into these tables from
// outside this package — which is what makes atomicity a property of the store
// rather than something a third derivation would have to be told about.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Derivation writes one whole derivation into a transaction the store owns.
//
// It writes derived tables and nothing else. That much is still the
// derivation's own discipline — internal/chains and internal/sessions both
// measure it, by fingerprinting `event` and `harvest_run` around a rebuild
// rather than trusting the code review — but the transaction is not, and
// neither is the handle.
//
// A Derivation is expected to DELETE before it INSERTs. The store cannot do
// that part for it: which tables a derivation owns is exactly what the store
// does not know.
type Derivation func(context.Context, *sql.Tx) error

// ReplaceDerived runs one derivation against the store, all of it or none.
//
// The transaction commits when the derivation returns nil and rolls back on any
// other outcome, including a panic, so the tables come out holding either this
// derivation or the previous one and never a mixture of the two. A table half
// computed under one set of parameters and half under another is a number
// nobody could interpret and nothing would flag, which is why this is one call
// rather than a handle plus a convention.
//
// The error a derivation returns comes back unwrapped, so a caller can still
// match on its own sentinel.
func (s *Store) ReplaceDerived(ctx context.Context, replace Derivation) error {
	if replace == nil {
		return errors.New("ReplaceDerived was given no derivation to run; " +
			"an empty replacement would silently empty the tables it was meant to rebuild")
	}
	if s.readOnly {
		return fmt.Errorf("%s was opened read-only, so no derivation can be written to it", s.path)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replacing a derivation in %s: %w", s.path, err)
	}
	// This is the whole guarantee. It runs on an early return, on a panic
	// unwinding through here, and harmlessly after a successful commit, so
	// there is no path that leaves a half-written table behind.
	defer tx.Rollback()

	if err := replace(ctx, tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit a derivation to %s: %w", s.path, err)
	}
	return nil
}
