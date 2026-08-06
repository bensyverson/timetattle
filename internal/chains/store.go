package chains

// Persistence for the derived chain tables (migration 0004).
//
// The contract, and it is narrow on purpose:
//
//   - Rebuild WIPES and re-INSERTS, inside a transaction internal/store opens
//     around it: it is a store.Derivation, so being all-or-nothing is a
//     property of the store rather than a discipline this file shares with
//     internal/sessions by coincidence. There is no
//     incremental update and there will not be one. A chain is a function of
//     every observation in the corpus at once — adding one file can merge two
//     families, split none, and move the first step of a lineage earlier — so
//     "update the chains that changed" is not a smaller problem than recomputing.
//
//   - It never writes to event or harvest_run. The derivation reads evidence
//     and produces a claim, and the evidence is append-only for reasons that
//     have nothing to do with this package. TestRebuildNeverTouchesTheEventStore
//     measures it rather than trusting the code review.
//
//   - Everything needed to interpret the output is written with the output. A
//     chain row and the parameters that produced it are inserted in the same
//     transaction and the run table holds exactly one row, so a half-recomputed
//     state is not reachable.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// Stored is a derivation read back out of the database, with the parameters
// that produced it.
//
// Load returns nil rather than a zero Stored where nothing has ever been
// derived, because a run that found no chains and a database that has never
// been asked are different facts and only one of them means "go and compute".
type Stored struct {
	// ComputedAt is when the derivation ran, not when the evidence was observed.
	ComputedAt time.Time
	// Options are the parameters that produced everything below.
	Options Options
	// Chains are in time order of their first step.
	Chains []Chain
	// Unchained are documents that were observed and formed no chain.
	Unchained []string

	EventsConsidered int
	Documents        int
	Chained          int
}

// Rebuild is the derivation that replaces the derived chain tables with r.
//
// It returns the write rather than performing it: the transaction is the
// store's, and st.ReplaceDerived(ctx, chains.Rebuild(r, at)) is the whole of a
// rebuild. Four tables are cleared and repopulated here, which is exactly the
// case where "all of it or none" must not depend on this file remembering to
// commit.
//
// computedAt is passed in rather than taken from the clock so that the write is
// reproducible and testable; the caller supplies time.Now.
func Rebuild(r Result, computedAt time.Time) store.Derivation {
	return func(ctx context.Context, tx *sql.Tx) error {
		o := r.Options.withDefaults()

		classes, err := json.Marshal(nonNil(o.Classes))
		if err != nil {
			return fmt.Errorf("encode the class list: %w", err)
		}
		extensions, err := json.Marshal(nonNil(o.Extensions))
		if err != nil {
			return fmt.Errorf("encode the extension list: %w", err)
		}

		// Deleted explicitly and in dependency order rather than left to ON
		// DELETE CASCADE. The cascade is real, but it only fires when
		// foreign_keys is on, and a caller who opened the database without that
		// pragma would otherwise leave every previous step behind while the
		// chains above them vanished.
		for _, table := range []string{
			"derived_chain_step",
			"derived_chain",
			"derived_chain_unchained",
			"derived_chain_run",
		} {
			if _, err := tx.ExecContext(ctx, `DELETE FROM `+table); err != nil {
				return fmt.Errorf("clear %s: %w", table, err)
			}
		}

		if _, err := tx.ExecContext(ctx, `INSERT INTO derived_chain_run
			(id, computed_at, source_id, classes, extensions, min_shared_prefix, coincidence_ms,
			 events_considered, documents, documents_chained)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			formatTime(computedAt), o.Source, string(classes), string(extensions),
			o.MinSharedPrefix, o.Coincidence.Milliseconds(),
			r.EventsConsidered, r.Documents, r.Chained); err != nil {
			return fmt.Errorf("record the derivation parameters: %w", err)
		}

		for _, c := range r.Chains {
			if len(c.Steps) < 2 {
				// The schema refuses this too. Failing here names the chain,
				// which a constraint violation does not.
				return fmt.Errorf("chain %s in %s has %d steps; a chain needs at least two",
					c.Root, c.Directory, len(c.Steps))
			}
			res, err := tx.ExecContext(ctx, `INSERT INTO derived_chain
				(run_id, directory, root, started_at, ended_at, step_count, document_count)
				VALUES (1, ?, ?, ?, ?, ?, ?)`,
				c.Directory, c.Root,
				formatTime(c.Steps[0].At), formatTime(c.Steps[len(c.Steps)-1].At),
				len(c.Steps), c.Documents())
			if err != nil {
				return fmt.Errorf("store the chain %s in %s: %w", c.Root, c.Directory, err)
			}
			chainID, err := res.LastInsertId()
			if err != nil {
				return fmt.Errorf("store the chain %s in %s: %w", c.Root, c.Directory, err)
			}

			for i, s := range c.Steps {
				documents, err := json.Marshal(nonNil(s.Documents))
				if err != nil {
					return fmt.Errorf("encode step %d of %s: %w", i, c.Root, err)
				}
				locators, err := json.Marshal(nonNil(s.Locators))
				if err != nil {
					return fmt.Errorf("encode step %d of %s: %w", i, c.Root, err)
				}
				// NULL on the first step, never zero. Zero would say two branch
				// points share an instant, which the collapse rule prevents.
				var gap any
				if i > 0 {
					gap = s.Gap.Milliseconds()
				}
				if _, err := tx.ExecContext(ctx, `INSERT INTO derived_chain_step
					(chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
					VALUES (?, ?, ?, ?, ?, ?, ?)`,
					chainID, i, formatTime(s.At), string(documents), string(locators),
					len(s.Documents), gap); err != nil {
					return fmt.Errorf("store step %d of %s in %s: %w", i, c.Root, c.Directory, err)
				}
			}
		}

		for _, d := range r.Unchained {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO derived_chain_unchained (run_id, document) VALUES (1, ?)`, d); err != nil {
				return fmt.Errorf("store the unchained document %s: %w", d, err)
			}
		}
		return nil
	}
}

// Load reads the current derivation back, or nil where nothing has derived into
// this database yet.
func Load(ctx context.Context, db *sql.DB) (*Stored, error) {
	var (
		s                       Stored
		computedAt              string
		classes, extensions     string
		coincidenceMilliseconds int64
	)
	err := db.QueryRowContext(ctx, `SELECT computed_at, source_id, classes, extensions,
		min_shared_prefix, coincidence_ms, events_considered, documents, documents_chained
		FROM derived_chain_run WHERE id = 1`).
		Scan(&computedAt, &s.Options.Source, &classes, &extensions,
			&s.Options.MinSharedPrefix, &coincidenceMilliseconds,
			&s.EventsConsidered, &s.Documents, &s.Chained)
	if errors.Is(err, sql.ErrNoRows) {
		// Nothing has been derived. Not an empty derivation — see Stored.
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the derivation parameters: %w", err)
	}
	if s.ComputedAt, err = parseTime(computedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(classes), &s.Options.Classes); err != nil {
		return nil, fmt.Errorf("decode the class list %q: %w", classes, err)
	}
	if err := json.Unmarshal([]byte(extensions), &s.Options.Extensions); err != nil {
		return nil, fmt.Errorf("decode the extension list %q: %w", extensions, err)
	}
	s.Options.Coincidence = time.Duration(coincidenceMilliseconds) * time.Millisecond

	chains, err := loadChains(ctx, db)
	if err != nil {
		return nil, err
	}
	s.Chains = chains

	rows, err := db.QueryContext(ctx,
		`SELECT document FROM derived_chain_unchained WHERE run_id = 1 ORDER BY document`)
	if err != nil {
		return nil, fmt.Errorf("read the unchained documents: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, fmt.Errorf("read an unchained document: %w", err)
		}
		s.Unchained = append(s.Unchained, d)
	}
	return &s, rows.Err()
}

// loadChains reads the chains and their steps.
//
// Two queries rather than one join, because a join would repeat every chain
// column once per step and the assembly would have to detect the boundary — and
// the ordering that makes that safe is exactly the thing a future index change
// could quietly take away.
func loadChains(ctx context.Context, db *sql.DB) ([]Chain, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT id, directory, root FROM derived_chain WHERE run_id = 1
		 ORDER BY started_at, directory, root`)
	if err != nil {
		return nil, fmt.Errorf("read the chains: %w", err)
	}
	defer rows.Close()

	var (
		out []Chain
		ids []int64
	)
	for rows.Next() {
		var (
			id int64
			c  Chain
		)
		if err := rows.Scan(&id, &c.Directory, &c.Root); err != nil {
			return nil, fmt.Errorf("read a chain: %w", err)
		}
		out = append(out, c)
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the chains: %w", err)
	}

	for i, id := range ids {
		steps, err := loadSteps(ctx, db, id)
		if err != nil {
			return nil, fmt.Errorf("read the steps of %s in %s: %w", out[i].Root, out[i].Directory, err)
		}
		out[i].Steps = steps
	}
	return out, nil
}

func loadSteps(ctx context.Context, db *sql.DB, chainID int64) ([]Step, error) {
	rows, err := db.QueryContext(ctx,
		`SELECT observed_at, documents, locators, gap_ms FROM derived_chain_step
		 WHERE chain_id = ? ORDER BY ordinal`, chainID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Step
	for rows.Next() {
		var (
			s                   Step
			observedAt          string
			documents, locators string
			gap                 sql.NullInt64
		)
		if err := rows.Scan(&observedAt, &documents, &locators, &gap); err != nil {
			return nil, err
		}
		if s.At, err = parseTime(observedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(documents), &s.Documents); err != nil {
			return nil, fmt.Errorf("decode the documents %q: %w", documents, err)
		}
		if err := json.Unmarshal([]byte(locators), &s.Locators); err != nil {
			return nil, fmt.Errorf("decode the locators %q: %w", locators, err)
		}
		if gap.Valid {
			s.Gap = time.Duration(gap.Int64) * time.Millisecond
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// formatTime writes an instant the way every other timestamp in this database
// is written: fixed-width UTC, so that text order is time order. store.TimeLayout
// is the single definition — see internal/store for why time.RFC3339Nano is wrong.
func formatTime(at time.Time) string {
	return at.UTC().Format(store.TimeLayout)
}

func parseTime(text string) (time.Time, error) {
	at, err := time.Parse(store.TimeLayout, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q is not in the store's format: %w", text, err)
	}
	// Local, because every consumer of a chain is asking about somebody's
	// working day and the store deliberately normalises to UTC on the way in.
	return at.Local(), nil
}

// nonNil keeps a nil slice out of JSON, where it would encode as null and read
// back as "not recorded" rather than as the empty list it actually is.
func nonNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}
