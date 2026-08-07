package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// BackfillPayloads writes the payload a stored observation always had onto the
// rows that were stored before their source emitted one.
//
// # This is the one write in this package that changes an observation
//
// Everything else here is append-only by design: an event is an observation,
// a later reading stands beside it or is dropped by dedup, and there is no DO
// UPDATE anywhere on the insert path. The single existing exception,
// event.project, is not an observation at all — it is a recomputable cache
// filled by a resolution pass, and 0001 says so in the column's own comment.
//
// A payload is different, and that is why this is a deliberate, one-shot,
// separately-invoked pass rather than a clause added to the insert. What it
// writes is not new evidence: it is the part of an observation the collector
// already read and had nowhere to put, because the column was only written from
// migration 0014 onwards. The rows it fills said nothing about sessions or
// tokens because nobody asked them, not because they were silent.
//
// Four properties make that safe to state, and each is pinned by a test in
// backfill_test.go rather than promised here:
//
//   - IT FILLS ONLY WHERE THERE IS NOTHING. Both statements below carry
//     `payload IS NULL`, so a payload already stored is never rewritten — the
//     first reading of a record stays the stored one, exactly as ON CONFLICT DO
//     NOTHING leaves it on the insert path.
//   - IT IS IDEMPOTENT. A second pass over the same evidence finds every row
//     already carrying a payload and writes nothing at all: same rows, same
//     bytes.
//   - IT INSERTS NOTHING. A record the store has never seen is counted and
//     skipped. Storing it here would give an event provenance no harvest run
//     ever had.
//   - ABSENCE STAYS ABSENCE. A row no offered record matches — its transcript
//     deleted, rewritten, or simply never re-read — keeps its NULL and is
//     counted as unmatched. NULL is "nothing was recorded"; a zero would be a
//     figure nobody observed.
//
// # Matching, and why it is the insert path's own identity
//
// A row is found by the seven columns of event_identity, spelled with the same
// COALESCE forms migration 0001 indexed them with. That index is UNIQUE, so a
// match is at most one row and the "two rows for one record" ambiguity cannot
// arise. The other direction — two offered records for one row — is possible in
// principle and is reported rather than silently resolved: the first offer wins,
// which is what the insert path does within a batch, and RepeatOffers says how
// often it happened.
//
// What the caller must hand over is the collector's OWN output, not a
// re-implementation of its rules. internal/transcripts resolves two readings of
// one record (Claude Code replays a conversation, uuid and timestamp intact,
// into a subagent's file) before the store ever sees them, and that resolution
// is precisely what makes a backfilled store equal a store harvested with
// payloads from the start.
//
// # The population is the table, not the canonical view
//
// canonical_event collapses an observation a later harvest completed, and the
// usage roll-up reads it. Filling the table therefore also fills rows no figure
// is computed from, and that is deliberate: a superseded observation is still an
// observation, and leaving half the store's transcript rows in a different state
// from the other half would be a distinction nobody could later explain.
func (s *Store) BackfillPayloads(ctx context.Context, offered []source.Event, opts BackfillOptions) (Backfill, error) {
	if opts.Source == "" {
		return Backfill{}, errors.New("a payload backfill must name the source whose rows it fills; " +
			"without it the pass would offer one collector's payloads to every row in the store")
	}
	if s.readOnly && !opts.DryRun {
		return Backfill{}, fmt.Errorf("%s was opened read-only, so no payload can be filled in it", s.path)
	}
	for i, e := range offered {
		if e.Source != opts.Source {
			return Backfill{}, fmt.Errorf("record %d of %d offered to a %s backfill was collected by %s; "+
				"a payload from one source written onto another's observation is a claim nothing downstream could question",
				i+1, len(offered), opts.Source, e.Source)
		}
	}

	out := Backfill{DryRun: opts.DryRun, Offered: len(offered)}

	// Counted BEFORE anything is written, because it is the denominator of the
	// row ledger: how many rows this pass could possibly be about.
	rows, err := s.countWithoutPayload(ctx, opts.Source)
	if err != nil {
		return Backfill{}, err
	}
	out.RowsWithoutAPayload = rows

	// What the first offer for each identity said. It exists to keep a repeat
	// offer from being reported as a row that "already carried a payload" —
	// which is what the second one would look like once the first had filled it,
	// and is the one way this report could quietly describe the wrong thing. It
	// holds string headers into the events the caller already has, not copies.
	firstOffer := make(map[eventIdentity]string, len(offered))

	for start := 0; start < len(offered); start += backfillBatch {
		end := min(start+backfillBatch, len(offered))
		if err := s.backfillBatch(ctx, offered[start:end], opts, &out, firstOffer); err != nil {
			// The partial report comes back with the error: the batches that
			// committed are done, and a caller that has to decide whether to
			// re-run needs to know how far it got.
			return out, err
		}
	}

	// By subtraction, and it is exact: every fillable and every
	// nothing-to-state record matched a DISTINCT row that carried no payload
	// (identity is unique and repeat offers are excluded above), so what is left
	// of the population is what nothing on disk spoke for.
	out.Unmatched = out.RowsWithoutAPayload - out.Fillable - out.NothingToState
	return out, nil
}

// BackfillOptions says which rows a backfill is about, and whether it may write.
type BackfillOptions struct {
	// Source is the collector whose rows are filled. It is required: a pass
	// that defaulted to "all of them" would offer one collector's vocabulary
	// to every observation in the store.
	Source string
	// DryRun withholds the write and nothing else. The pass reads, matches and
	// classifies exactly as it otherwise would, so the counts it reports are a
	// prediction of the real run rather than an estimate of it — see
	// TestADryRunWritesNothingAndReportsWhatTheRealRunThenDoes. A dry run takes
	// no transaction and may be run through a store opened read-only, which is
	// how it should be run against a live store: then "it wrote nothing" is
	// structural rather than a flag somebody has to have read correctly.
	DryRun bool
}

// Backfill is what one pass did, in numbers, so that it can be questioned
// rather than believed. It follows store.Attribution and internal/sessions.Stats:
// every input lands in exactly one bucket, and the buckets sum to the input.
//
// TWO LEDGERS, because there are two populations and they answer different
// questions. The RECORD ledger is about the evidence on disk:
//
//	Offered = Matched + Unstored + RepeatOffers
//	Matched = Fillable + NothingToState + AlreadyCarrying
//
// The ROW ledger is about the store, and it is the one that says whether history
// is now covered:
//
//	RowsWithoutAPayload = Fillable + NothingToState + Unmatched
//
// A single "filled N rows" would collapse the distinctions both ledgers exist to
// keep — above all the difference between a row that was re-read and had nothing
// to state, and a row nothing re-read at all.
type Backfill struct {
	// DryRun records which kind of pass produced these numbers, so a capture of
	// a dry run cannot be mistaken for a record of a real one.
	DryRun bool

	// Offered is how many records the collector handed over: the input.
	Offered int
	// Matched is how many of them found their row.
	Matched int
	// Unstored is how many matched no row at all. These are ordinary on a live
	// machine — they are records written since the last harvest — and the pass
	// deliberately does NOT store them: a backfill fills rows, a harvest makes
	// them, and an event inserted here would carry provenance no run ever had.
	Unstored int
	// RepeatOffers is how many records repeated an identity already offered in
	// this pass, and DisagreeingRepeatOffers how many of those said something
	// different from the first. The first offer is the one used, which is what
	// the insert path does within a batch. internal/transcripts cannot produce
	// either — its replay resolution keys on (Document, Start), both inside
	// event_identity — so a non-zero figure here means the caller's own rules
	// changed, and it is reported rather than assumed away.
	RepeatOffers, DisagreeingRepeatOffers int

	// Fillable is how many matched rows carried no payload and had one to
	// receive; Filled is how many the pass actually wrote. They are equal after
	// a real pass and Filled is zero after a dry run, and that is the whole
	// difference between the two.
	Fillable, Filled int
	// NothingToState is how many matched rows carried no payload and whose
	// record had nothing to put there. They stay NULL, and they are NOT
	// unmatched: this row was re-read and said nothing, which is a different
	// fact from never having been read.
	NothingToState int
	// AlreadyCarrying is how many matched rows already had a payload and were
	// left exactly as they were. On a second pass this is every row, and it is
	// what tells "idempotent because everything is already done" apart from
	// "idempotent because nothing matched" — two states a Filled of zero
	// describes identically.
	AlreadyCarrying int

	// RowsWithoutAPayload is how many rows of this source carried none when the
	// pass began: the population, counted before anything was written.
	RowsWithoutAPayload int
	// Unmatched is how many of those no offered record spoke for. Their
	// transcript is gone from disk, or was rewritten, or was not under the root
	// this pass read. They keep their NULL.
	//
	// It cannot be split further, and that is a property of the store rather
	// than of this pass: an event records the working directory its record
	// named, never the file it was read from (internal/transcripts derives
	// nothing from a path), so no row can be traced to a file that is or is not
	// still there. "The transcript was deleted" and "the root was wrong" are
	// therefore one figure here, and a tool reporting them as two would be
	// inventing the difference.
	Unmatched int
}

// backfillBatch is how many records one transaction covers.
//
// One transaction for the whole pass would be atomic and would also mean that a
// pass interrupted at 90% has done nothing. Batching makes an interrupted pass
// leave rows filled and rows not filled and nothing in between, which is safe
// only because the pass is idempotent: running it again fills what is left and
// lands on exactly the store one uninterrupted pass would have produced
// (TestABackfillInterruptedPartwayIsCompletedByRunningItAgain). The figure is a
// balance between fsyncs and the size of the window a kill can land in, and
// nothing depends on its exact value.
const backfillBatch = 5000

// eventIdentity is the seven-column natural key of an event, as Go values.
// source_id is not in it because a pass is about one source and it is the same
// for every record.
type eventIdentity struct {
	shape, started, ended, app, document, state string
}

// identityMatch is the WHERE clause of both statements, and the reason they are
// two constants built from one.
//
// THE SPELLING IS THE ACCESS PATH. Migration 0001 creates event_identity on
// (source_id, shape, COALESCE(started_at, ”), COALESCE(ended_at, ”),
// COALESCE(app, ”), COALESCE(document, ”), COALESCE(state, ”)), and SQLite
// answers a query from an index on an expression only when the query spells the
// expression the same way the index does. Written the obvious way instead —
// `started_at = ? AND app = ?` — both statements return exactly the same rows
// and fall back to event_source, which on this machine's store means scanning
// within one source of 822,387 rows once per record offered. Nothing about that
// is visible in a result. TestTheBackfillFindsItsRowThroughTheIdentityIndexRatherThanAScan
// asserts the plan against these constants rather than against a copy, and the
// naive spelling was measured failing it before this one replaced it.
//
// The COALESCE is also what makes the match CORRECT rather than merely fast:
// the insert path stores the empty string as NULL, and `app = ”` matches no row
// where app is NULL.
const identityMatch = `source_id = ? AND shape = ?
	AND COALESCE(started_at, '') = ? AND COALESCE(ended_at, '') = ?
	AND COALESCE(app, '') = ? AND COALESCE(document, '') = ? AND COALESCE(state, '') = ?`

// matchIdentityQuery asks whether a record's observation is stored, and whether
// the row carries a payload. Those are two different facts and the query returns
// both: no row is "the store never saw this", and a row with a payload is "this
// is already recorded", and a pass that could not tell them apart would report
// its own idempotence as a store that had lost its evidence.
const matchIdentityQuery = `SELECT payload IS NULL FROM event WHERE ` + identityMatch

// fillPayloadQuery is the write, and `payload IS NULL` in it is a guard rather
// than a hint to the planner.
//
// IT IS THE SECOND OF TWO, and the pair was measured rather than assumed. The
// classification above skips a row that already carries a payload, so it is that
// branch which normally does the refusing; this clause is what makes the mistake
// LOUD if the branch is ever lost. Both were removed in turn against
// backfill_test.go:
//
//   - both gone: the second pass over three rows filled all 3 again, and a
//     disagreeing re-reading changed a stored record's output figure from 91 to
//     92 — silently, since the row count does not move.
//   - this clause alone kept: the UPDATE matches nothing, RowsAffected is 0, and
//     the pass aborts naming the observation and rolls its batch back.
//
// One guard alone would have been enough to be correct. Two are here because the
// failure the first one prevents is invisible and the failure the second one
// prevents is a stopped program, and on the only write in this project that
// changes an observation, that trade is worth a line of SQL.
//
// What is at stake is not hypothetical: 52 records in this machine's corpus have
// two readings that state different token figures (scripts/sessioncensus,
// 2026-08-07), so an overwriting pass would change their value on every run.
const fillPayloadQuery = `UPDATE event SET payload = ?
	WHERE payload IS NULL AND ` + identityMatch

// rowQuerier is whatever the batch runs against: the database directly for a dry
// run, which takes no transaction, or the transaction of a real one.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// backfillBatchRun classifies one batch of records and, unless this is a dry
// run, fills the rows that can be filled — both inside one transaction, so the
// answer a record was classified by is the answer its row is written under.
func (s *Store) backfillBatch(ctx context.Context, batch []source.Event, opts BackfillOptions,
	out *Backfill, firstOffer map[eventIdentity]string) error {

	var q rowQuerier = s.db
	if !opts.DryRun {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin filling payloads in %s: %w", s.path, err)
		}
		// Runs on every early return, on a panic unwinding through here, and
		// harmlessly after a successful commit: there is no path that leaves a
		// half-applied batch behind.
		defer tx.Rollback()
		q = tx
	}

	for _, e := range batch {
		key := identityOf(e)
		if before, repeated := firstOffer[key]; repeated {
			out.RepeatOffers++
			if before != e.Payload {
				out.DisagreeingRepeatOffers++
			}
			continue
		}
		firstOffer[key] = e.Payload

		args := identityArgs(opts.Source, e)
		var empty bool
		switch err := q.QueryRowContext(ctx, matchIdentityQuery, args...).Scan(&empty); {
		case errors.Is(err, sql.ErrNoRows):
			out.Unstored++
			continue
		case err != nil:
			return fmt.Errorf("look for the row of the %s observation at %s in %s: %w",
				e.Source, formatTime(e.Anchor()), s.path, err)
		}

		out.Matched++
		switch {
		case !empty:
			out.AlreadyCarrying++
			continue
		case e.Payload == "":
			out.NothingToState++
			continue
		}
		out.Fillable++
		if opts.DryRun {
			continue
		}

		res, err := q.ExecContext(ctx, fillPayloadQuery, append([]any{e.Payload}, args...)...)
		if err != nil {
			return fmt.Errorf("fill the payload of the %s observation at %s in %s: %w",
				e.Source, formatTime(e.Anchor()), s.path, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("count what was filled: %w", err)
		}
		if affected != 1 {
			// The probe found exactly one row carrying no payload, and this
			// statement is the same WHERE plus a condition that was just true.
			// Zero here means the row changed between the two inside one
			// transaction, which the store cannot do to itself.
			return fmt.Errorf("filling the payload of the %s observation at %s changed %d rows rather than 1; "+
				"the row it matched moved underneath the pass", e.Source, formatTime(e.Anchor()), affected)
		}
		out.Filled += int(affected)
	}

	if tx, ok := q.(*sql.Tx); ok {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %d filled payloads to %s: %w", out.Filled, s.path, err)
		}
	}
	return nil
}

// countWithoutPayload is the population: rows of this source that carry no
// payload at all.
//
// It counts the TABLE rather than canonical_event, because that is what the
// pass writes to. See BackfillPayloads.
func (s *Store) countWithoutPayload(ctx context.Context, sourceID string) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM event WHERE source_id = ? AND payload IS NULL`, sourceID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count the %s rows in %s carrying no payload: %w", sourceID, s.path, err)
	}
	return n, nil
}

func identityOf(e source.Event) eventIdentity {
	return eventIdentity{
		shape:    string(e.Shape),
		started:  identityTime(e.Start),
		ended:    identityTime(e.End),
		app:      e.Identity.App,
		document: e.Identity.Document,
		state:    e.State,
	}
}

func identityArgs(sourceID string, e source.Event) []any {
	id := identityOf(e)
	return []any{sourceID, id.shape, id.started, id.ended, id.app, id.document, id.state}
}

// identityTime renders an endpoint as the identity index sees it. The empty
// string rather than NULL, and rather than a formatted zero time: the insert
// path writes NULL for an endpoint that was never observed, and the index
// COALESCEs that to ” precisely so two spellings of "there is no endpoint here"
// cannot both insert.
func identityTime(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return formatTime(at)
}
