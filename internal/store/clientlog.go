package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
)

// ClientAssertions reads the whole client-project log, oldest first.
//
// The whole of it, always. It is one row per registration rather than per
// event — 38 on the live machine against ~973,000 events — so there is nothing
// to page and no filter worth offering: the fold needs every row to answer
// "what did this say before that correction", and a windowed read would
// silently produce a different mapping. See internal/clientmap.
//
// An empty log reads as an empty slice and no error. A machine where nobody has
// registered anything has not been observed to have no clients; it has not been
// observed at all, and the caller is the one that must not collapse those.
func (s *Store) ClientAssertions(ctx context.Context) ([]clientmap.Assertion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, project, client, source,
		effective_from, recorded_at FROM client_project ORDER BY recorded_at, id`)
	if err != nil {
		return nil, fmt.Errorf("read the client-project log from %s: %w", s.path, err)
	}
	defer rows.Close()

	var out []clientmap.Assertion
	for rows.Next() {
		var (
			a                       clientmap.Assertion
			effectiveFrom, recorded sql.NullString
		)
		if err := rows.Scan(&a.ID, &a.Project, &a.Client, &a.Source,
			&effectiveFrom, &recorded); err != nil {
			return nil, fmt.Errorf("read a client-project assertion from %s: %w", s.path, err)
		}
		// NULL means "from the beginning of time" and comes back as the zero
		// time, which is the one value that cannot be mistaken for a real
		// instant. parseNullTime already makes exactly that distinction for an
		// event endpoint that was never observed.
		if a.EffectiveFrom, err = parseNullTime(effectiveFrom); err != nil {
			return nil, err
		}
		if a.RecordedAt, err = parseNullTime(recorded); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AppendClientAssertion records one claim that a project belongs to a client.
//
// APPEND, NEVER UPDATE. Changing an assertion means recording another one; the
// triggers in 0012 refuse anything else, so this is a property of the database
// rather than of this function. The earlier row stays readable, which is what
// makes a past report reproducible and what tells a correction (superseding a
// claim) from a first registration (filling a hole).
//
// The caller supplies both timestamps inside the Assertion, and they mean
// different things: EffectiveFrom is when the association took effect, and the
// ZERO TIME means from the beginning of time — a correction, or a first
// registration, both of which must reach backwards over evidence that already
// exists. RecordedAt is when it was written down and never decides which
// instants the assertion covers. Collapsing them would turn every correction
// into a reassignment dated today.
func (s *Store) AppendClientAssertion(ctx context.Context, a clientmap.Assertion, origin string) error {
	if s.readOnly {
		return fmt.Errorf("append a client-project assertion: %s was opened read-only", s.path)
	}
	if a.RecordedAt.IsZero() {
		// Unlike EffectiveFrom, this one has no meaningful absent case: an
		// assertion nobody dated cannot be ordered against another, and the
		// fold's whole ordering rests on it.
		return fmt.Errorf("append a client-project assertion for %q: it has no recorded_at, so "+
			"nothing can order it against another assertion", a.Project)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO client_project
		(project, client, source, effective_from, recorded_at, origin)
		VALUES (?, ?, ?, ?, ?, ?)`,
		a.Project, a.Client, a.Source, nullableTime(a.EffectiveFrom), formatTime(a.RecordedAt), origin)
	if err != nil {
		return fmt.Errorf("append a client-project assertion for %q: %w", a.Project, err)
	}
	return nil
}

// ClientMap reads the log and folds it, which is what almost every caller
// actually wants.
//
// asOf restricts the fold to assertions recorded by that instant, so a report
// can be reproduced as it read at a past moment; the zero time means everything.
//
// WHAT THIS DOES NOT REWIND, said here because a reader will assume it does.
// It rewinds the CLIENT mapping and nothing else. event.project is a
// recomputable cache with no history — the attribution pass overwrites it on
// every harvest — so folding as of a past instant does NOT restore which
// directories were registered then. A project registered last week still
// carries its events today when asked about last month. The honest reading of a
// time-travelled report is therefore "what we now know about that work, under
// the client mapping as it stood then", and the unattributed figures do not
// travel at all.
func (s *Store) ClientMap(ctx context.Context, asOf time.Time) (clientmap.Map, error) {
	log, err := s.ClientAssertions(ctx)
	if err != nil {
		return clientmap.Map{}, err
	}
	return clientmap.FoldAsOf(log, asOf), nil
}
