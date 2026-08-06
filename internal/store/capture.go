package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// CaptureStatus is what a capture phase did. See migration 0007 for the
// argument over where capture outcomes belong at all.
//
// There is deliberately no running state. A Run row exists before the events
// that hang off it, so harvest_run needs one and pays for it: the live store
// carries a row stranded at 'running' since the process died under it. A
// Capture is recorded after the capture returned, from a span measured around
// it, so all four values below are terminal.
type CaptureStatus string

const (
	// CaptureOK is a capture that read its source through.
	CaptureOK CaptureStatus = "ok"
	// CapturePartial is a capture that preserved some of what it tried and lost
	// the rest. Biome captures stream by stream and filedays scans root by
	// root, and losing one must not discard the others.
	CapturePartial CaptureStatus = "partial"
	// CaptureFailed is a capture that preserved nothing, with the reason
	// recorded.
	CaptureFailed CaptureStatus = "failed"
	// CaptureAbsent is a capture that looked and found nothing to preserve: no
	// Motion installed, no filedays roots configured, no such Biome stream on
	// this machine.
	//
	// It is a recorded OBSERVATION and not a shrug. Left unwritten it would be
	// indistinguishable from a harvest that died before reaching this capture,
	// and those two call for opposite actions.
	CaptureAbsent CaptureStatus = "absent"
)

// Capture is one preservation attempt: what a harvest did with one perishable
// source on one invocation.
//
// It is not a Run and does not live in harvest_run. A run is the provenance of
// events and is measured in events seen and events new; a capture produces
// neither, and is measured in files and bytes. Migration 0007 has the full
// argument.
type Capture struct {
	ID int64
	// Kind is which perishable thing was preserved: knowledgec, biome,
	// motion-vault, filedays.
	Kind string
	// Input is what was read — the knowledgeC path, the Biome root, the vault
	// root, the filedays roots. The capture-phase analogue of Run.Input.
	Input string
	// StartedAt and FinishedAt bracket the capture. Both are always set: this
	// row is written once the capture has returned.
	StartedAt  time.Time
	FinishedAt time.Time
	Status     CaptureStatus
	// Error is why a failed or partial capture lost what it lost, and is empty
	// on the two statuses that lost nothing.
	Error string
	// FilesNew is what this capture wrote and FilesHeld what it found already
	// preserved, byte for byte, and skipped. Biome and the Motion vault are
	// content-addressed, so a healthy run over an unchanged source records
	// FilesNew == 0 — which is a measurement, and is the state that must never
	// render like a capture that did not happen.
	FilesNew  int
	FilesHeld int
	// Bytes is the compressed size of what was written.
	Bytes int64
}

// RecordCapture stores the outcome of one capture.
//
// It takes a whole Capture rather than beginning and closing one the way
// BeginRun does, and that is the design rather than an omission. A run has to
// exist before the events it is the provenance of; a capture has nothing to
// hang off it and is fully known only once it has returned, so there is no
// moment at which a half-written capture row would be truer than no row.
//
// The consequence is deliberate: a harvest killed mid-capture writes nothing,
// which reads as "nothing recorded" and is honest, rather than leaving a row
// claiming to be live for ever.
func (s *Store) RecordCapture(ctx context.Context, c Capture) (int64, error) {
	if err := c.validate(); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO capture_run
		(kind, input, started_at, finished_at, status, error, files_new, files_held, bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.Kind, c.Input, formatTime(c.StartedAt), formatTime(c.FinishedAt),
		string(c.Status), nullable(c.Error), c.FilesNew, c.FilesHeld, c.Bytes)
	if err != nil {
		return 0, fmt.Errorf("record the %s capture of %q: %w", c.Status, c.Kind, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("record the %s capture of %q: %w", c.Status, c.Kind, err)
	}
	return id, nil
}

// validate refuses the rows migration 0007's CHECKs also refuse, so that a
// caller gets a sentence rather than a constraint name. Both layers exist: the
// CHECKs stop a script or a future collector writing a shape nobody can read
// back, and this stops the ordinary caller guessing what went wrong.
func (c Capture) validate() error {
	if c.Kind == "" {
		return errors.New("a capture must name the kind of thing it preserved, or its record cannot be matched to the files it left")
	}
	if c.Input == "" {
		return errors.New("a capture must record what it read, or a capture that read the wrong thing looks like one with nothing to preserve")
	}
	if c.StartedAt.IsZero() {
		return fmt.Errorf("the %s capture has no start time", c.Kind)
	}
	if c.FinishedAt.IsZero() {
		return fmt.Errorf("the %s capture has no end time; a capture is recorded once it has returned, so both endpoints are known", c.Kind)
	}
	if c.FinishedAt.Before(c.StartedAt) {
		return fmt.Errorf("the %s capture finished before it started (%s before %s)",
			c.Kind, formatTime(c.FinishedAt), formatTime(c.StartedAt))
	}
	switch c.Status {
	case CaptureFailed, CapturePartial:
		if c.Error == "" {
			return fmt.Errorf("the %s capture was recorded as %s with no reason given; "+
				"an unexplained loss is indistinguishable from a capture with nothing to preserve", c.Kind, c.Status)
		}
	case CaptureOK, CaptureAbsent:
		if c.Error != "" {
			return fmt.Errorf("the %s capture succeeded (%s) but carries the error %q; the record would argue with itself",
				c.Kind, c.Status, c.Error)
		}
	default:
		return fmt.Errorf("the %s capture has status %q, which is not one of ok, partial, failed or absent",
			c.Kind, c.Status)
	}
	return nil
}

// captureColumns is every column a Capture carries, in the order scanCapture
// expects.
const captureColumns = `id, kind, input, started_at, finished_at, status, error,
	files_new, files_held, bytes`

func scanCapture(scan func(...any) error) (Capture, error) {
	var (
		c                        Capture
		started, finished, state string
		failure                  sql.NullString
	)
	if err := scan(&c.ID, &c.Kind, &c.Input, &started, &finished, &state, &failure,
		&c.FilesNew, &c.FilesHeld, &c.Bytes); err != nil {
		return Capture{}, err
	}

	var err error
	if c.StartedAt, err = parseNullTime(sql.NullString{String: started, Valid: true}); err != nil {
		return Capture{}, err
	}
	if c.FinishedAt, err = parseNullTime(sql.NullString{String: finished, Valid: true}); err != nil {
		return Capture{}, err
	}
	c.Status = CaptureStatus(state)
	c.Error = failure.String
	return c, nil
}

// Captures reads recorded capture outcomes back, oldest first.
//
// This is how "is the evidence still being PRESERVED?" is answered, which is a
// different question from the one Runs answers and is asked at the moment the
// answer to that one is already known to be no.
//
// It returns rows and not a verdict, for the same reason Runs does: what "the
// latest capture of each kind" or "stale" means belongs to the reader asking.
//
// The order is the order the captures ran, which makes the LAST row of a kind
// its most recent attempt.
//
// A kind with no rows has never had a capture recorded, and that is not the
// same fact as a capture that ran and preserved nothing new. Only the caller
// can tell them apart, and only because this returns nothing rather than a
// zero.
//
// Filter.Source selects a kind, which is the same position kind occupies here
// that source_id occupies in harvest_run.
func (s *Store) Captures(ctx context.Context, f Filter) ([]Capture, error) {
	query := strings.Builder{}
	query.WriteString(`SELECT ` + captureColumns + ` FROM capture_run`)

	var (
		where []string
		args  []any
	)
	// Bounded on started_at, matching Runs: it is the endpoint that orders the
	// table, and bounding on finished_at would silently reorder captures that
	// took very different lengths of time.
	if !f.From.IsZero() {
		where = append(where, `started_at >= ?`)
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `started_at <= ?`)
		args = append(args, formatTime(f.To))
	}
	if f.Source != "" {
		where = append(where, `kind = ?`)
		args = append(args, f.Source)
	}
	if len(where) > 0 {
		query.WriteString(" WHERE " + strings.Join(where, " AND "))
	}
	query.WriteString(` ORDER BY id`)

	rows, err := s.db.QueryContext(ctx, query.String(), args...)
	if err != nil {
		return nil, fmt.Errorf("read capture runs from %s: %w", s.path, err)
	}
	defer rows.Close()

	var out []Capture
	for rows.Next() {
		c, err := scanCapture(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("read a capture run from %s: %w", s.path, err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
