package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// HOW FAR THE STORE HAS BEEN ASKED, PER SOURCE.
//
// `timetattle review` puts a day on the screen and asks a person to account for
// the whole of it, and every gap between blocks becomes a `not-engaged`
// assertion. So a stretch no source has been asked about is recorded as a
// stretch in which nothing happened. That is this project's cardinal bug —
// absence of a row read as an observation — and this is the read that lets a
// day view refuse it.
//
// It is deliberately NOT the whole horizon. The other half is the derivation,
// and on 2026-08-01 the derivation was the binding one by a factor of 248: the
// harvest was 23 minutes behind and the clustering was 1h45m behind. See
// internal/dayview/reach.go. Which of the two binds is a property of the day
// and cannot be decided here, so this reports rows and lets the caller take the
// minimum — the same doctrine Runs states for itself.

// Fresh is one source and the last instant it was successfully read through.
type Fresh struct {
	// Source is the source id, e.g. "biome-appusage".
	Source string
	// LastHarvest is when the latest SUCCESSFUL run of this source finished,
	// and is the instant up to which it has covered the world.
	//
	// The zero time means this source has never been read through — it has been
	// tried and has never succeeded. That is a third state and not a stale one:
	// treating it as a horizon at the zero time would pin every day in the
	// store to the year one, and hiding it would read as fresh.
	LastHarvest time.Time
	// Failing says the source's LATEST attempt failed, whether or not an
	// earlier one succeeded. A source can be both covered to lunchtime and
	// failing since, and a reader that could see only one of those would
	// either cry wolf or say nothing.
	Failing bool
	// Attempts is how many runs of this source the store holds, so a caller can
	// say "failing for the last four runs" rather than only "failing".
	Attempts int
}

// Freshness reports, for every source this store has ever tried to harvest, how
// far it has been asked.
//
// A source ABSENT from the result has never been tried. That is not the same as
// a source that was tried and failed, which is present with a zero LastHarvest,
// and it is not the same as a source that ran and found nothing, which is
// present with a LastHarvest and no Failing. Collapsing any pair of those is
// the bug this file exists to make impossible.
//
// The order is the source id, so a warning listing them is stable between runs
// and two invocations can be diffed.
func (s *Store) Freshness(ctx context.Context) ([]Fresh, error) {
	// finished_at IS NOT NULL as well as status = 'ok', and both are load
	// bearing. A run stranded at 'running' has no finished_at at all — the live
	// store carries one, claude-transcripts, started 2026-08-01T13:07:13Z and
	// dead ever since — and MAX over a NULL would silently return the earlier
	// row while a naive COALESCE would return the year one.
	//
	// The failing flag comes from the run with the greatest id rather than the
	// greatest started_at: ids are assigned as runs BEGIN, so two runs sharing
	// a started_at to the microsecond still order correctly. That is the same
	// rule Runs documents for its own ORDER BY.
	rows, err := s.db.QueryContext(ctx, `
		SELECT source_id,
		       MAX(CASE WHEN status = 'ok' AND finished_at IS NOT NULL THEN finished_at END),
		       (SELECT r.status FROM harvest_run r
		         WHERE r.source_id = harvest_run.source_id
		         ORDER BY r.id DESC LIMIT 1),
		       count(*)
		FROM harvest_run GROUP BY source_id ORDER BY source_id`)
	if err != nil {
		return nil, fmt.Errorf("read harvest freshness from %s: %w", s.path, err)
	}
	defer rows.Close()

	var out []Fresh
	for rows.Next() {
		var (
			f        Fresh
			last     sql.NullString
			latest   string
			attempts int
		)
		if err := rows.Scan(&f.Source, &last, &latest, &attempts); err != nil {
			return nil, fmt.Errorf("read a source's freshness from %s: %w", s.path, err)
		}
		f.Attempts = attempts
		// A run still 'running' is not a failure and is not coverage either. It
		// is counted as failing because that is what a reader must act on: a
		// source whose latest attempt never finished is not arriving.
		f.Failing = latest != string(StatusOK)
		if last.Valid {
			if f.LastHarvest, err = parseStoredTime(last.String); err != nil {
				return nil, err
			}
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
