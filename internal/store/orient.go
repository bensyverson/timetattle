package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// LocatorCounts is the census `timetattle orient` needs, computed in SQL
// instead of by reading every event into a source.Event: how many
// observations the store holds in total and the span they cover (stored
// observations of generated files are in none of these figures), and — among
// the observations no project has been attributed to — how many carry no
// locator at all against how many distinct locators appear and how many
// observations back each one.
//
// It answers exactly what orientCensusOf (cmd/timetattle/orient.go) used to
// compute by scanning Events() by hand, and nothing more: it does not resolve
// a locator against the registry, does not stat the filesystem, and does not
// decide what to do about any of it. Those all reach past this package and
// belong in the command that already owns the registry and os.Stat, not here
// — this package does not know what a project registry is.
type LocatorCounts struct {
	// Observations and the span below cover every event in the store,
	// attributed or not.
	Observations     int
	Earliest, Latest time.Time

	// NoPath is unattributed observations carrying no locator at all —
	// evidence from a source that observes the machine rather than a file, and
	// so can never be attributed however many directories are registered.
	NoPath int
	// ByLocator is unattributed observations that DO carry a locator, one
	// entry per distinct locator. It is empty rather than nil when there are
	// none, so a caller can range over it without a nil check.
	ByLocator map[string]int
}

// LocatorCounts computes the census above with three aggregate queries
// against canonical_event — the same view Events reads, so a horizon a later
// harvest completed is collapsed here exactly as it is there — rather than by
// reading and scanning every row.
func (s *Store) LocatorCounts(ctx context.Context) (LocatorCounts, error) {
	var out LocatorCounts
	out.ByLocator = map[string]int{}

	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM canonical_event WHERE `+notGenerated, notGeneratedArgs...,
	).Scan(&out.Observations); err != nil {
		return LocatorCounts{}, fmt.Errorf("count observations in %s: %w", s.path, err)
	}

	// The anchor is whichever endpoint was actually observed — source.Event's
	// own Anchor(), reproduced here as COALESCE(started_at, ended_at) because
	// the schema guarantees at least one of the two is set on every row (see
	// the shape CHECKs in 0009_timestamp_shape.sql). Using the same expression
	// for both MIN and MAX is deliberate: Anchor() is one instant per event,
	// not "whichever of start/end is earliest" and "whichever is latest"
	// independently.
	var earliest, latest sql.NullString
	if err := s.db.QueryRowContext(ctx,
		`SELECT MIN(COALESCE(started_at, ended_at)), MAX(COALESCE(started_at, ended_at))
			FROM canonical_event WHERE `+notGenerated, notGeneratedArgs...,
	).Scan(&earliest, &latest); err != nil {
		return LocatorCounts{}, fmt.Errorf("find the span of observations in %s: %w", s.path, err)
	}
	var err error
	if out.Earliest, err = parseNullTime(earliest); err != nil {
		return LocatorCounts{}, err
	}
	if out.Latest, err = parseNullTime(latest); err != nil {
		return LocatorCounts{}, err
	}

	// GROUP BY locator, restricted to events no project has been attributed
	// to. SQLite groups every NULL locator together — the one group this
	// query cannot produce is a locator of '', since the schema's own CHECK
	// (locator IS NULL OR locator LIKE '/%') refuses to store one, so there is
	// no empty string for NULL to be silently folded together with. The NULL
	// group is read out into NoPath explicitly, by column validity rather than
	// by comparing against "", so nothing here would notice if that changed.
	rows, err := s.db.QueryContext(ctx,
		`SELECT locator, COUNT(*) FROM canonical_event WHERE project IS NULL AND `+notGenerated+
			` GROUP BY locator`, notGeneratedArgs...)
	if err != nil {
		return LocatorCounts{}, fmt.Errorf("count unattributed locators in %s: %w", s.path, err)
	}
	defer rows.Close()

	for rows.Next() {
		var locator sql.NullString
		var n int
		if err := rows.Scan(&locator, &n); err != nil {
			return LocatorCounts{}, fmt.Errorf("read an unattributed locator count in %s: %w", s.path, err)
		}
		if !locator.Valid {
			out.NoPath = n
			continue
		}
		out.ByLocator[locator.String] = n
	}
	if err := rows.Err(); err != nil {
		return LocatorCounts{}, fmt.Errorf("count unattributed locators in %s: %w", s.path, err)
	}
	return out, nil
}
