package store

// Persistence for the human correction layer.
//
// These are the only tables in this database a person writes directly, and the
// only ones that are neither an observation nor a derivation. Two properties
// follow, and both are enforced by migration 0008 rather than by this file:
//
//   - Append-only. There is no UpdateAdjustment and no DeleteAdjustment, and
//     there is no way to add one from outside this package, because triggers in
//     the schema refuse the UPDATE and the DELETE outright. A correction to a
//     correction is a new row; internal/adjustments.Fold takes the later of the
//     two and both survive.
//
//   - Keyed to wall clock and to nothing else. Nothing here references
//     derived_session, so `timetattle sessionize` — which DELETEs and reinserts
//     every session row on every run — cannot touch a correction. That is
//     criterion ZHM, held by construction rather than by care.
//
// This is deliberately not ReplaceDerived. A derivation is a whole answer and
// is replaced wholesale; a correction is one fact appended to a log, and giving
// it the wholesale-replacement seam would put "DELETE FROM adjustment" one typo
// away from being written.

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
)

// RecordAdjustment appends one human correction and returns its id.
//
// It never updates and never deletes. Recording a second correction over a span
// that already has one is the supported way to change your mind: the fold reads
// the later of the two and the earlier stays visible, which is what makes
// "each adjustment records its origin and time" mean something over history
// rather than only over the latest state.
func (s *Store) RecordAdjustment(ctx context.Context, a adjustments.Adjustment) (int64, error) {
	if err := a.Validate(); err != nil {
		return 0, fmt.Errorf("refusing to record this adjustment: %w", err)
	}
	if s.readOnly {
		return 0, fmt.Errorf("%s was opened read-only, so no correction can be recorded to it", s.path)
	}

	res, err := s.db.ExecContext(ctx, `INSERT INTO adjustment
		(review_id, started_at, ended_at, assertion, project, origin, recorded_at, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		nullableID(a.ReviewID), formatTime(a.Start), formatTime(a.End), string(a.Assertion),
		nullable(a.Project), a.Origin, formatTime(a.RecordedAt), nullable(a.Note))
	if err != nil {
		return 0, fmt.Errorf("record an adjustment over %s..%s in %s: %w",
			formatTime(a.Start), formatTime(a.End), s.path, err)
	}
	return res.LastInsertId()
}

// RecordReview appends one act of looking at a span, and returns its id.
//
// A review is recorded whether or not it produced any correction, because that
// is the only way a reviewed span and an unexamined one can be told apart. A
// review whose verdict is Unremembered says the machine's answer for that span
// is unconfirmed; it is not a correction, it is not a confirmation, and it
// removes nothing.
func (s *Store) RecordReview(ctx context.Context, r adjustments.Review) (int64, error) {
	if err := r.Validate(); err != nil {
		return 0, fmt.Errorf("refusing to record this review: %w", err)
	}
	if s.readOnly {
		return 0, fmt.Errorf("%s was opened read-only, so no review can be recorded to it", s.path)
	}

	res, err := s.db.ExecContext(ctx, `INSERT INTO adjustment_review
		(started_at, ended_at, verdict, shown, origin, recorded_at, note)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		formatTime(r.Start), formatTime(r.End), string(r.Verdict), string(r.Saw()),
		r.Origin, formatTime(r.RecordedAt), nullable(r.Note))
	if err != nil {
		return 0, fmt.Errorf("record a review of %s..%s in %s: %w",
			formatTime(r.Start), formatTime(r.End), s.path, err)
	}
	return res.LastInsertId()
}

// Adjustments reads corrections back, in the order the fold resolves them.
//
// from and to bound the span and either may be the zero time to mean unbounded,
// the same convention Filter uses. The test is OVERLAP rather than containment:
// a correction that began before the window and runs into it describes time
// inside the window, and dropping it would silently restore work a person had
// removed.
//
// The order is (recorded_at, id) — the fold order, so that a caller who folds
// what this returns without re-sorting gets the right answer, and one who does
// re-sort gets the same one. internal/adjustments.Fold sorts either way.
func (s *Store) Adjustments(ctx context.Context, from, to time.Time) ([]adjustments.Adjustment, error) {
	where, args := spanWhere(from, to)
	rows, err := s.db.QueryContext(ctx, `SELECT id, review_id, started_at, ended_at,
		assertion, project, origin, recorded_at, note FROM adjustment`+where+
		` ORDER BY recorded_at, id`, args...)
	if err != nil {
		return nil, fmt.Errorf("read adjustments from %s: %w", s.path, err)
	}
	defer rows.Close()

	var out []adjustments.Adjustment
	for rows.Next() {
		var (
			a                        adjustments.Adjustment
			review                   sql.NullInt64
			started, ended, recorded string
			assertion                string
			project, note            sql.NullString
		)
		if err := rows.Scan(&a.ID, &review, &started, &ended, &assertion,
			&project, &a.Origin, &recorded, &note); err != nil {
			return nil, fmt.Errorf("read an adjustment from %s: %w", s.path, err)
		}
		a.ReviewID = review.Int64
		a.Assertion = adjustments.Assertion(assertion)
		a.Project = project.String
		a.Note = note.String
		if a.Start, err = parseStoredTime(started); err != nil {
			return nil, err
		}
		if a.End, err = parseStoredTime(ended); err != nil {
			return nil, err
		}
		if a.RecordedAt, err = parseStoredTime(recorded); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Reviews reads back which spans have been looked at, and what the reviewer
// could say about each.
//
// A span this returns nothing for has not been reviewed. That is the third
// state, and it is why the reviews are a table of their own: a reviewed day
// that needed no corrections writes no adjustments, so the adjustment table
// alone cannot tell a confirmed day from an unopened one.
func (s *Store) Reviews(ctx context.Context, from, to time.Time) ([]adjustments.Review, error) {
	where, args := spanWhere(from, to)
	rows, err := s.db.QueryContext(ctx, `SELECT id, started_at, ended_at, verdict, shown,
		origin, recorded_at, note FROM adjustment_review`+where+
		` ORDER BY recorded_at, id`, args...)
	if err != nil {
		// A store that has not run 0011 has no `shown` column, and a read-only
		// handle applies no migration. Without this the failure is a bare
		// driver error — "SQL logic error: no such column: shown (1)" — naming
		// neither the store nor the migration nor the way out, which is exactly
		// what internal/fit's real-data suite reported when 0011 landed against
		// a live store sitting at 0010.
		//
		// The same shape, and for the same reason, as the 0010 guard in
		// sittings.go: the version is looked up only on the failure path, so the
		// ordinary read pays nothing for it.
		if schema, serr := s.Schema(ctx); serr == nil && schema.Version < 11 {
			return nil, fmt.Errorf(
				"%s is at migration %d, and what a reviewer was SHOWN — the derivation, or "+
					"the observations — arrived in 0011. Its reviews cannot be told apart "+
					"without it, and a fit that pools the two reports a precision it has not "+
					"got; open the store for writing once to migrate it",
				s.path, schema.Version)
		}
		return nil, fmt.Errorf("read reviews from %s: %w", s.path, err)
	}
	defer rows.Close()

	var out []adjustments.Review
	for rows.Next() {
		var (
			r                        adjustments.Review
			started, ended, recorded string
			verdict, shown           string
			note                     sql.NullString
		)
		if err := rows.Scan(&r.ID, &started, &ended, &verdict, &shown,
			&r.Origin, &recorded, &note); err != nil {
			return nil, fmt.Errorf("read a review from %s: %w", s.path, err)
		}
		r.Verdict = adjustments.Verdict(verdict)
		r.Shown = adjustments.Shown(shown)
		r.Note = note.String
		if r.Start, err = parseStoredTime(started); err != nil {
			return nil, err
		}
		if r.End, err = parseStoredTime(ended); err != nil {
			return nil, err
		}
		if r.RecordedAt, err = parseStoredTime(recorded); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// spanWhere bounds a query by overlap with from..to, either of which may be the
// zero time to mean unbounded.
//
// Overlap and not containment, for the reason Filter gives: a span that began
// before the window and ended inside it describes time during the window. Both
// endpoints are NOT NULL in these tables, so this needs none of the COALESCE
// the event query carries.
func spanWhere(from, to time.Time) (string, []any) {
	var (
		where []string
		args  []any
	)
	if !from.IsZero() {
		where = append(where, `ended_at >= ?`)
		args = append(args, formatTime(from))
	}
	if !to.IsZero() {
		where = append(where, `started_at <= ?`)
		args = append(args, formatTime(to))
	}
	if len(where) == 0 {
		return "", nil
	}
	var clause strings.Builder
	clause.WriteString(" WHERE " + where[0])
	for _, w := range where[1:] {
		clause.WriteString(" AND " + w)
	}
	return clause.String(), args
}

// nullableID keeps a zero id out of the database. Zero is "no review", and only
// NULL says that; a literal 0 would be a foreign key to a row that cannot exist.
func nullableID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// parseStoredTime reads a column that is NOT NULL, so it needs none of the
// sql.NullString handling parseNullTime carries.
func parseStoredTime(text string) (time.Time, error) {
	at, err := time.Parse(TimeLayout, text)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q is not in the store's format: %w", text, err)
	}
	return at, nil
}
