package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// Status is where a harvest run got to.
type Status string

const (
	// StatusRunning is a run that has not reported either way. A run left in
	// this state is a program that died mid-harvest, which is worth being able
	// to see.
	StatusRunning Status = "running"
	// StatusOK is a run that read its source through.
	StatusOK Status = "ok"
	// StatusFailed is a run that lost its source, with the reason recorded.
	StatusFailed Status = "failed"
)

// Run is one source's read, and the provenance of every event it produced.
//
// There is a row per (invocation, source) rather than per invocation because
// collection has partial-failure semantics: one broken source loses only its own
// events. A run that lost a source has to record which one and why, or the loss
// is invisible in every number downstream.
//
// Runs that found nothing are the point rather than an edge case. "No Motion
// work on the 22nd" and "no harvest ran on the 22nd" are different facts, and
// only a recorded empty window tells them apart — which matters directly when
// under-reporting is acceptable and silent blindness is not.
type Run struct {
	ID       int64
	SourceID string
	// Input is what was read: a Biome stream directory, a knowledgeC snapshot
	// path, a glob root. The run-level analogue of an event's locator.
	Input string
	// Since is the boundary handed to Collect, and is the zero time when the
	// run read everything. Recorded so that "why did this run not see X" is
	// answerable later.
	Since      time.Time
	StartedAt  time.Time
	FinishedAt time.Time
	Status     Status
	// Error is why a failed run failed, and is empty otherwise.
	Error string
	// EventsSeen is what the source returned; EventsNew is what survived
	// dedup. See the package comment: these are not equal on a fresh database,
	// and the invariant that matters is EventsNew == 0 on a second harvest of
	// an unchanged window.
	EventsSeen int
	EventsNew  int

	store *Store
}

// RunSpec describes a harvest about to begin.
type RunSpec struct {
	// SourceID is the source being read, e.g. "biome-appusage".
	SourceID string
	// Input is what is being read.
	Input string
	// Since is the boundary handed to Collect, or the zero time for all of it.
	Since time.Time
}

// BeginRun records that a source is being read and returns the run to write
// events against.
//
// The run exists before any event does, because an event's provenance is not
// optional: event.first_seen_run is NOT NULL and the foreign key is enforced.
func (s *Store) BeginRun(ctx context.Context, spec RunSpec) (*Run, error) {
	if spec.SourceID == "" {
		return nil, errors.New("a harvest run must name the source it read, or a lost source cannot be reported")
	}
	if spec.Input == "" {
		return nil, errors.New("a harvest run must record what it read, or a source that read the wrong thing looks like a source with nothing to say")
	}

	started := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO harvest_run
		(source_id, started_at, input, since, status) VALUES (?, ?, ?, ?, ?)`,
		spec.SourceID, formatTime(started), spec.Input, nullableTime(spec.Since), string(StatusRunning))
	if err != nil {
		return nil, fmt.Errorf("record the start of a harvest of %q: %w", spec.SourceID, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("record the start of a harvest of %q: %w", spec.SourceID, err)
	}

	return &Run{
		ID:        id,
		SourceID:  spec.SourceID,
		Input:     spec.Input,
		Since:     spec.Since,
		StartedAt: started.Truncate(time.Microsecond),
		Status:    StatusRunning,
		store:     s,
	}, nil
}

// insertEvent is the deduplicating write.
//
// ON CONFLICT DO NOTHING rather than INSERT OR IGNORE, and the difference is
// load-bearing: OR IGNORE swallows every constraint violation, including the
// CHECKs that describe what an event may look like, so a malformed event would
// vanish silently instead of being reported. DO NOTHING applies to uniqueness
// alone, which is the only collision that is ordinary.
//
// project is left to default NULL and is filled by a resolution pass.
//
// payload IS written here, and it is written ONCE. It names no column of
// event_identity, so a re-read of the same observation conflicts on identity
// and DO NOTHING applies — the row already there keeps the payload it was
// stored with. There is deliberately no DO UPDATE: an event is an observation
// and is never rewritten, so a row stored before its source emitted payloads
// carries none permanently, which is "nothing was recorded" and not zero. See
// migration 0014.
const insertEvent = `INSERT INTO event
	(first_seen_run, source_id, shape, unknown, started_at, ended_at, app, document, state, locator, cause, payload)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT DO NOTHING`

// Write stores a batch of events and reports how many were new.
//
// The whole batch is one transaction and any event the database refuses fails all
// of it. That is deliberate: source.Registry drops invalid events before they
// reach here, so an event the store cannot write means a bug rather than untidy
// data, and the granularity of an acceptable partial failure is the source —
// which is this run, whose failure is recordable.
//
// A source-supplied Identity.Project is not stored. It is a different thing
// wearing the same word: for Motion it is the composition folder, while
// event.project means the client project a resolution pass attributed the
// evidence to. The observation itself keeps its locator, from which the folder
// is recoverable.
func (r *Run) Write(ctx context.Context, events []source.Event) (int, error) {
	if r.Status != StatusRunning {
		return 0, fmt.Errorf("harvest run %d for %q is already %s; events written now would carry provenance that is not true",
			r.ID, r.SourceID, r.Status)
	}
	if len(events) == 0 {
		return 0, nil
	}
	// Validated before anything is written, so a batch holding one unusable
	// event does not half-apply.
	for i, e := range events {
		if err := e.Validate(); err != nil {
			return 0, fmt.Errorf("event %d of %d from %q cannot be stored: %w", i+1, len(events), r.SourceID, err)
		}
	}

	tx, err := r.store.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin writing %d events from %q: %w", len(events), r.SourceID, err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, insertEvent)
	if err != nil {
		return 0, fmt.Errorf("prepare the event insert: %w", err)
	}
	defer stmt.Close()

	inserted := 0
	for _, e := range events {
		// cause is nullable for the same reason as the rest: evidence that is
		// not an artifact has no cause, and NULL is the only spelling of that
		// which cannot be confused with a source that failed to declare one.
		res, err := stmt.ExecContext(ctx, r.ID, e.Source, string(e.Shape), nullable(string(e.Unknown)),
			nullableTime(e.Start), nullableTime(e.End),
			nullable(e.Identity.App), nullable(e.Identity.Document), nullable(e.State), nullable(e.Locator),
			nullable(string(e.Cause)), nullable(e.Payload))
		if err != nil {
			return 0, fmt.Errorf("store the %s event from %s anchored at %s: %w",
				e.Shape, e.Source, formatTime(e.Anchor()), err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return 0, fmt.Errorf("count what was stored: %w", err)
		}
		inserted += int(affected)
	}

	// The counters move in the same transaction as the rows, so they cannot
	// drift from what is actually there.
	if _, err := tx.ExecContext(ctx, `UPDATE harvest_run
		SET events_seen = events_seen + ?, events_new = events_new + ? WHERE id = ?`,
		len(events), inserted, r.ID); err != nil {
		return 0, fmt.Errorf("record what harvest run %d saw: %w", r.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit %d events from %q: %w", len(events), r.SourceID, err)
	}

	r.EventsSeen += len(events)
	r.EventsNew += inserted
	return inserted, nil
}

// Finish records that the source was read through.
func (r *Run) Finish(ctx context.Context) error {
	return r.close(ctx, StatusOK, nil)
}

// Fail records that the source was lost, and why.
//
// The events already written stay. They were observed, and the reason a later
// read failed says nothing about them.
func (r *Run) Fail(ctx context.Context, cause error) error {
	if cause == nil {
		return fmt.Errorf("harvest run %d for %q was failed with no reason given; "+
			"an unexplained loss is indistinguishable from a source with nothing to report", r.ID, r.SourceID)
	}
	return r.close(ctx, StatusFailed, cause)
}

func (r *Run) close(ctx context.Context, status Status, cause error) error {
	if r.Status != StatusRunning {
		return fmt.Errorf("harvest run %d for %q is already %s", r.ID, r.SourceID, r.Status)
	}

	finished := time.Now().UTC()
	var reason any
	if cause != nil {
		reason = cause.Error()
	}
	if _, err := r.store.db.ExecContext(ctx, `UPDATE harvest_run
		SET finished_at = ?, status = ?, error = ? WHERE id = ?`,
		formatTime(finished), string(status), reason, r.ID); err != nil {
		return fmt.Errorf("record the end of harvest run %d for %q: %w", r.ID, r.SourceID, err)
	}

	r.FinishedAt = finished.Truncate(time.Microsecond)
	r.Status = status
	if cause != nil {
		r.Error = cause.Error()
	}
	return nil
}

// runColumns is every column a Run carries, in the order scanRun expects.
const runColumns = `id, source_id, started_at, finished_at, input, since,
	status, error, events_seen, events_new`

// scanRun reads one row of runColumns. It takes the Scan method rather than a
// *sql.Row or *sql.Rows so that the single-row and many-row readers cannot
// drift apart in the order they name the columns.
func (s *Store) scanRun(scan func(...any) error) (Run, error) {
	var (
		run                      Run
		since, finished, failure sql.NullString
		started, status          string
	)
	if err := scan(&run.ID, &run.SourceID, &started, &finished, &run.Input, &since,
		&status, &failure, &run.EventsSeen, &run.EventsNew); err != nil {
		return Run{}, err
	}

	var err error
	if run.StartedAt, err = parseNullTime(sql.NullString{String: started, Valid: true}); err != nil {
		return Run{}, err
	}
	// The zero time rather than a substitute: a run that never finished has no
	// finished_at, and inventing one would make a harvest that died partway
	// read as a harvest that ended.
	if run.FinishedAt, err = parseNullTime(finished); err != nil {
		return Run{}, err
	}
	if run.Since, err = parseNullTime(since); err != nil {
		return Run{}, err
	}
	run.Status = Status(status)
	run.Error = failure.String
	run.store = s
	return run, nil
}

// LoadRun reads a recorded harvest back.
func (s *Store) LoadRun(ctx context.Context, id int64) (*Run, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT `+runColumns+` FROM harvest_run WHERE id = ?`, id)
	run, err := s.scanRun(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("no harvest run %d in %s", id, s.path)
	}
	if err != nil {
		return nil, fmt.Errorf("read harvest run %d: %w", id, err)
	}
	return &run, nil
}

// Runs reads recorded harvests back, oldest first.
//
// This is how "is the evidence still arriving?" is answered. Everything needed
// to notice a harvest that has been failing for a day and a half is already in
// harvest_run — one row per source per invocation, with status, error,
// events_seen and events_new — and for a while the only thing that asked was a
// command reaching past this package with SQL of its own.
//
// It returns rows and not a verdict. What "the latest run of each source" or
// "how many failures since it last succeeded" means belongs to the reader
// asking, and a store that decided it here would have to be changed by the
// next reader who wanted it counted differently.
//
// The order is the order the runs began, which is what makes the LAST row for a
// source its most recent attempt: harvest_run ids are assigned as runs start,
// so two runs sharing a started_at to the microsecond still order correctly.
//
// A source with no rows is a source that has never run, and that is not the
// same fact as a source that ran and found nothing. Only the caller can tell
// them apart, and only because this returns nothing rather than a zero.
func (s *Store) Runs(ctx context.Context, f Filter) ([]Run, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT ` + runColumns + ` FROM harvest_run`)

	var (
		where []string
		args  []any
	)
	// Bounded on started_at, the one endpoint every run has. A run still marked
	// running has no finished_at, and bounding on that would silently drop
	// exactly the runs most worth noticing.
	if !f.From.IsZero() {
		where = append(where, `started_at >= ?`)
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `started_at <= ?`)
		args = append(args, formatTime(f.To))
	}
	if f.Source != "" {
		where = append(where, `source_id = ?`)
		args = append(args, f.Source)
	}
	if len(where) > 0 {
		query.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	query.WriteString(` ORDER BY id`)

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("read harvest runs from %s: %w", s.path, err)
	}
	defer rows.Close()

	var out []Run
	for rows.Next() {
		run, err := s.scanRun(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("read a harvest run from %s: %w", s.path, err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}
