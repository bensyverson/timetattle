package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

// WHETHER EVIDENCE ARRIVED, which is not the question Freshness answers.
//
// Freshness reads harvest_run and reports when each source was last read
// through. That is whether a HARVEST RAN. A harvest that fires exactly on
// schedule and ingests nothing advances that reading while the store itself
// does not move at all, and on 2026-08-13 at 15:04Z the live store showed
// exactly that: the newest harvest run was 1h59m old and the newest event was
// 12h12m old, and every check in the program was green.
//
//	sqlite3 ~/.timetattle/timetattle.db \
//	  "select max(started_at) from harvest_run;
//	   select max(coalesce(ended_at, started_at)) from event;"
//
// # The hard part, which is the whole of this file
//
// A source with nothing new to say and a source that has silently stopped write
// the same nothing. Twelve hours of no evidence at ten in the morning is an
// ordinary night; twelve hours of no evidence in the middle of a working day is
// a collector that has died. Nothing in the store separates those two by the
// clock, and a rule that tried would fire every single morning — the same error
// as reading events_new = 0 as a fault, which on this store would have fired on
// the 09:00 and 13:00 runs of 2026-08-13, both of which read 1,017,812 and
// 1,017,255 events respectively and found nothing new. Those runs looked. They
// are observations of not-X, not absences.
//
// # Witnessed lag: silence measured against the store's own frontier
//
// So silence here is never measured against the wall clock. A source's WITNESSED
// LAG is how far behind the newest evidence in the WHOLE store its own newest
// evidence sits. The frontier does not move while nobody is recording, so a
// night, a weekend and a fortnight's holiday cost every source exactly nothing.
// A source only falls behind while some OTHER source is recording — which is
// evidence that the machine was alive and this source was silent through it, and
// that is an observation rather than an absence.
//
// It is deliberately not derived from harvest_run at all. A harvest that runs
// and ingests nothing is the case being caught, so a measure that moved when a
// harvest ran would be blind to it by construction.
//
// # Why the lags are counted only inside each source's collected era
//
// Because most of this store's event timeline is not a record of collection at
// all. fs-observations records file modification times reaching back to 2013 and
// gitlog records commit dates from 2023, so measured over the whole event table
// their largest witnessed lags are 5.4 years and 1.7 years. A bound derived from
// those could never fire, and the report would be clean and indistinguishable
// from a correct one, which is the failure this project has shipped repeatedly.
//
// The era is each source's OWN first harvest_run row. Before that instant an
// event's timestamp is archaeology; after it, an event arriving means collection
// worked. That boundary is read out of the record rather than typed in, so it
// cannot go stale. Restricted to it, every live source's largest witnessed lag
// lands between 12.33h and 70.00h.
//
// # What this still cannot answer
//
// How old the frontier itself is allowed to be. When EVERY source is silent
// there is no witness, the whole construction above says nothing, and no arrangement
// of these rows can tell an idle machine from a stalled collector. The caller is
// given the reading and must say so in words rather than judge it. See
// cmd/timetattle/status_evidence.go, which does exactly that and does not pretend
// otherwise.

// Evidence is one source's evidence timeline, collapsed to what a freshness
// check needs.
type Evidence struct {
	// Source is the source id, e.g. "biome-appusage".
	Source string
	// Newest is the newest event anchor this source holds, over the WHOLE
	// store, including events backfilled from long before collection began.
	//
	// The zero time means this source has never stored an event. That is a
	// third state and not an instant: a source that has run thirty times and
	// stored nothing is present here with a zero Newest, because reporting it
	// by leaving it out is the collapse this package exists to refuse.
	Newest time.Time
	// Events is how many events the source holds over the whole store.
	Events int
	// CollectedFrom is this source's first recorded harvest run, and the
	// instant its collected era begins. The zero time means no harvest_run row
	// names this source, which is a store that holds its events and no record
	// of reading them.
	CollectedFrom time.Time
	// Lags is every witnessed lag this source has CLOSED inside its collected
	// era, in the order they closed: for each pair of consecutive events of
	// this source, how far the store's frontier had moved past the earlier one
	// by the time the later one arrived.
	//
	// Closed, and never the lag running right now. The one now open is the
	// thing being judged, and folding it into the evidence it is judged against
	// would make every silence its own precedent.
	//
	// Empty is the absence of a basis for judging this source, not a source
	// that has never once fallen behind. blender-backups and motion-autosave on
	// the live store are both empty: every event either of them holds predates
	// its own first harvest.
	Lags []time.Duration
}

// LongestLag is the largest witnessed lag this source has ever closed, and
// whether there is any closed lag to take it from.
//
// False is not a duration of zero. It is a source nothing recorded can yet
// judge, and it must never render as one that passed a check — the same rule
// statusClock.limit follows for a source that has never completed a run.
func (e Evidence) LongestLag() (time.Duration, bool) {
	var longest time.Duration
	for _, lag := range e.Lags {
		longest = max(longest, lag)
	}
	return longest, len(e.Lags) > 0
}

// Evidence reports, for every source this store has ever run or stored an event
// for, what evidence it holds and how far behind the store's own frontier it has
// been in the past.
//
// The order is the source id, so two invocations can be diffed.
func (s *Store) Evidence(ctx context.Context) ([]Evidence, error) {
	by := map[string]*Evidence{}
	at := func(id string) *Evidence {
		e, ok := by[id]
		if !ok {
			e = &Evidence{Source: id}
			by[id] = e
		}
		return e
	}

	// The collected era per source, and the enumeration of every source that
	// has ever been read. A source with runs and no events belongs in the
	// result; leaving it out would report "nothing was recorded" as "nothing
	// there is to record".
	eras, err := s.db.QueryContext(ctx,
		`SELECT source_id, min(started_at) FROM harvest_run GROUP BY source_id`)
	if err != nil {
		return nil, fmt.Errorf("read the collected era of each source from %s: %w", s.path, err)
	}
	if err := scanRows(eras, func(rows *sql.Rows) error {
		var (
			id    string
			first string
		)
		if err := rows.Scan(&id, &first); err != nil {
			return err
		}
		e := at(id)
		e.CollectedFrom, err = parseStoredTime(first)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the collected era of each source from %s: %w", s.path, err)
	}

	// The newest evidence and the total, over the whole store rather than over
	// the era: an event backfilled from 2021 is still the newest thing
	// blender-backups holds, and saying it has none would be a lie about the
	// store's contents.
	//
	// COALESCE(ended_at, started_at) is the anchor, spelled the way
	// eventsQuery and migration 0013's index spell it. An interval that ENDED
	// at 11:30 is evidence up to 11:30, and reading started_at alone would
	// understate every interval source — biome-appusage, the source that most
	// often holds this store's frontier, is exactly that shape.
	newest, err := s.db.QueryContext(ctx,
		`SELECT source_id, max(COALESCE(ended_at, started_at)), count(*)
		 FROM canonical_event GROUP BY source_id`)
	if err != nil {
		return nil, fmt.Errorf("read the newest evidence of each source from %s: %w", s.path, err)
	}
	if err := scanRows(newest, func(rows *sql.Rows) error {
		var (
			id     string
			anchor string
			n      int
		)
		if err := rows.Scan(&id, &anchor, &n); err != nil {
			return err
		}
		e := at(id)
		e.Events = n
		e.Newest, err = parseStoredTime(anchor)
		return err
	}); err != nil {
		return nil, fmt.Errorf("read the newest evidence of each source from %s: %w", s.path, err)
	}

	if err := s.witnessedLags(ctx, by); err != nil {
		return nil, err
	}

	out := make([]Evidence, 0, len(by))
	for _, e := range by {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Source < out[j].Source })
	return out, nil
}

// witnessedLags walks the collected era in anchor order and fills in every lag
// each source has closed.
//
// ONE PASS, IN GO, over one ordered stream. The alternative is a pair of window
// functions in SQL, which computes the same thing and hides what "the frontier
// at that moment" means inside a LAG() nobody can test in isolation. This is the
// same trade recordedStatus in cmd/timetattle/status.go states for itself: the
// rows come out of the database and the meaning is assembled where a reader can
// see it.
//
// The stream is ordered by the anchor, so the frontier just before any row is
// simply the anchor of the row before it. That is what makes this one pass
// rather than a lookback: the ordering IS the frontier.
func (s *Store) witnessedLags(ctx context.Context, by map[string]*Evidence) error {
	// The era join is what keeps 900,000 backfilled rows out of this scan as
	// well as out of the arithmetic — on the live store it reads 160,315 rows
	// of 1,069,011.
	rows, err := s.db.QueryContext(ctx, `
		SELECT ce.source_id, COALESCE(ce.ended_at, ce.started_at) AS anchor
		FROM canonical_event ce
		JOIN (SELECT source_id, min(started_at) AS collected_from
		        FROM harvest_run GROUP BY source_id) era
		  ON era.source_id = ce.source_id
		WHERE COALESCE(ce.ended_at, ce.started_at) >= era.collected_from
		ORDER BY anchor, ce.id`)
	if err != nil {
		return fmt.Errorf("read the evidence timeline of %s: %w", s.path, err)
	}

	var (
		// frontier is the newest anchor STRICTLY BEFORE the row being read, and
		// group is the anchor of the run of rows sharing the current instant.
		//
		// The strictness is the whole of this bookkeeping and it is not
		// pedantry. Sources routinely record at the same instant — a harvest
		// stamps several knowledgeC streams from one snapshot, and two sources
		// watching the same file produce the same anchor — and taking the
		// previous ROW as the frontier makes whichever of them the id order
		// puts second appear to have been behind by the whole preceding gap.
		// A test written against a day where two sources record together
		// caught exactly that: it charged the second source 9h and 14h of lag
		// across a night in which neither of them had fallen behind at all.
		frontier time.Time
		group    time.Time
		previous = map[string]time.Time{}
	)
	if err := scanRows(rows, func(rows *sql.Rows) error {
		var (
			id     string
			anchor string
		)
		if err := rows.Scan(&id, &anchor); err != nil {
			return err
		}
		at, err := parseStoredTime(anchor)
		if err != nil {
			return err
		}
		if !at.Equal(group) {
			frontier, group = group, at
		}
		if was, seen := previous[id]; seen {
			// How far the frontier had moved past this source's previous event
			// by the time this one arrived.
			//
			// max against zero for the one case that can go negative: a source
			// that recorded twice at the same instant has a previous event
			// inside the current group and therefore later than the frontier.
			// It has not fallen behind by a negative amount; it has not fallen
			// behind.
			e, ok := by[id]
			if !ok {
				e = &Evidence{Source: id}
				by[id] = e
			}
			e.Lags = append(e.Lags, max(frontier.Sub(was), 0))
		}
		previous[id] = at
		return nil
	}); err != nil {
		return fmt.Errorf("read the evidence timeline of %s: %w", s.path, err)
	}
	return nil
}

// scanRows runs each over every row and closes the set, so that the three reads
// above do not each restate the same defer-and-check.
func scanRows(rows *sql.Rows, each func(*sql.Rows) error) error {
	defer rows.Close()
	for rows.Next() {
		if err := each(rows); err != nil {
			return err
		}
	}
	return rows.Err()
}
