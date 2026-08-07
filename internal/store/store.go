// Package store is the immutable event store.
//
// One database holds every observation Timetattle has ever made, together with
// the provenance of each read that produced them. Three ideas run through it and
// the rest follows from them.
//
// # Events are observations and never change
//
// A row says "this was seen". It is not a claim about what happened, and it is
// never edited to reflect a better later reading: when the newest Biome segment
// ends with an application still frontmost, the harvest stores an event with no
// end, and the harvest that finds the matching stop stores a second, complete
// event beside it.
//
// Collapsing the two is a derivation, performed on the way out, and it is
// structural rather than a rule callers must remember — Events reads the
// canonical_event view, which supersedes a horizon by the interval that
// completes it and touches nothing else. A rule saying "remember to collapse"
// gets forgotten, and the failure mode is silently double-counted hours.
// EventsIncludingSuperseded exists for provenance and is named at that length so
// that nobody reaches it by picking the shorter word.
//
// Human corrections are overlays keyed to a wall-clock span and never rows here,
// because an append-only log of observations cannot express "that was a meeting,
// not work" without inventing retraction semantics that tax every read forever.
//
// The one exception is event.project, a nullable cache filled by a resolution
// pass against the registry. It is derived rather than observed, which is why
// nothing writes it at collect time: registering a folder later, or fixing a
// mapping, has to re-attribute events that already exist.
//
// There is a second, deliberate and one-shot: BackfillPayloads fills event.payload
// where it is NULL, for the rows stored before migration 0014 gave the column a
// meaning. It writes no new evidence — the payload is the part of an observation
// the collector always read and had nowhere to put — it never overwrites a
// payload already stored, and it is invoked separately rather than from any
// harvest. See backfill.go for the argument and the guards; the point of naming
// it here is that "an event is never rewritten" now has an exception, and an
// exception nobody wrote down is how a rule quietly becomes a habit.
//
// The CLIENT a project belongs to is deliberately NOT here. It has 38 distinct
// values across ~973,000 events, it changes as a whole rather than per event,
// and it is a fact about a moment in time — so it lives in the append-only
// client_project log (0012) and is folded on the way out. See
// internal/clientmap.
//
// # Deduplication is by content, because collectors re-read
//
// Sources do not tail. knowledgeC's /app/usage was measured 23 hours behind
// /display/isBacklit, and a Biome interval that was still running at the last
// harvest is deliberately re-emitted at greater completeness, so the same
// evidence legitimately arrives many times. A watermark cannot fix that; a
// natural key can. Writing is therefore idempotent, and the cost of a re-read is
// a few thousand insert attempts that do nothing.
//
// Two counters make that visible rather than assumed. events_seen is what the
// source returned and events_new is what survived dedup. They are NOT equal on a
// fresh database — knowledgeC repeats itself, with three rows identical in
// stream, endpoints and value on 2026-07-10 at 15:22:32, and collapsing those is
// correct. What must hold is that a second harvest of an unchanged window adds
// nothing at all: events_new is zero and the row count does not move.
//
// # Timestamps are text, and the text has to sort
//
// SQLite has no date type. Every instant here is stored as fixed-width UTC text
// so that lexical order is chronological order — which the schema's
// CHECK (ended_at >= started_at) and every range query depend on. See
// TimeLayout for why the obvious choice, time.RFC3339Nano, is wrong.
//
// Normalising to UTC happens here because here is the only place it can be
// relied on: internal/biome returns UTC and the knowledgeC and file readers
// return local time. Go's own comparisons hide the difference, since they compare
// instants; formatting and day-bucketing do not.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/migrations"
	"github.com/bensyverson/timetattle/internal/source"

	// The driver is pure Go, so Timetattle needs no C toolchain to build.
	_ "modernc.org/sqlite"
)

// TimeLayout is how every instant in the database is written.
//
// Fixed width and UTC, so that comparing two of them as text gives the same
// answer as comparing the instants. Do not reach for time.RFC3339Nano: it trims
// trailing zeros, so ".5", ".50" and ".500000" all come out at different widths
// and "09:00:00.5Z" > "09:00:00.123456Z" as text while being earlier in fact.
// Nothing about that failure is loud — it silently mis-sorts a fraction of rows
// near second boundaries and quietly breaks the interval CHECK.
//
// Microseconds, because that is what the sources resolve: Biome segment names
// and knowledgeC's Core Data doubles are both microsecond figures. Anything
// finer is truncated rather than rounded, so re-reading a record always produces
// the same key.
const TimeLayout = "2006-01-02T15:04:05.000000Z"

// Store is the event store.
//
// It is safe for concurrent use; a single Run is not, since it accumulates the
// counts of one source's harvest.
type Store struct {
	db   *sql.DB
	path string
	// readOnly records how this store was opened, so that a write through it
	// is refused in words rather than by SQLite's "attempt to write a readonly
	// database", which names neither the store nor why it was opened that way.
	readOnly bool
}

// DefaultPath is the global store, one per machine.
func DefaultPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".timetattle", "timetattle.db"), nil
}

// Open returns the store at path, creating and migrating it if it is not there.
//
// Migrations run on every open, which is what makes them automatic: there is no
// separate command to forget, and a binary can only ever read a database at the
// schema it was built against.
func Open(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("no store path given")
	}
	// 0700 because this database is a detailed record of one person's working
	// day, and the directory is the only thing standing between it and every
	// other account on the machine.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create the directory for %s: %w", path, err)
	}

	// foreign_keys, because an event whose harvest run does not exist has no
	// provenance and SQLite does not enforce that by default. busy_timeout, so a
	// scheduled harvest and an interactive command wait for each other rather
	// than failing. WAL, so a read never blocks the write. txlock=immediate, so a
	// write transaction takes its lock up front instead of discovering halfway
	// through that it cannot have one.
	dsn := "file:" + path +
		"?_pragma=foreign_keys(1)" +
		"&_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(wal)" +
		"&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := migrations.Apply(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// OpenReadOnly opens an existing store for reading, and changes nothing at all
// about it: it does not create it, does not migrate it, and takes no write
// lock.
//
// Both refusals are load-bearing, and both come from `timetattle status`, whose
// whole job is to report on the store rather than to become part of its
// history. A check that CREATED the database would report a healthy store on a
// machine where the harvest has never once succeeded, which is the same class
// of mistake it exists to catch; a check that MIGRATED it would report a schema
// no harvest had ever run against, which is precisely the fact that diagnosed
// the incident it was written for.
//
// The store this returns can read. Writes through it are refused rather than
// attempted — see ReplaceDerived.
func OpenReadOnly(path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("no store path given")
	}
	// mode=ro rather than the immutable flag, because the file genuinely is
	// being written by harvests while this reads it. busy_timeout for the same
	// reason it is on the writing DSN: a scheduled harvest and an interactive
	// check should wait for each other rather than fail. No journal_mode
	// pragma, because setting one is a write.
	dsn := "file:" + path + "?mode=ro&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s for reading: %w", path, err)
	}
	// sql.Open does not touch the file, so without this a missing store would
	// be reported as a query failure somewhere much later, naming a table
	// rather than the absent database.
	if err := db.PingContext(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s for reading: %w", path, err)
	}
	return &Store{db: db, path: path, readOnly: true}, nil
}

// Schema is how far a store has been migrated.
//
// It is the fact that diagnosed the failure `timetattle status` was written
// for: a store still on 0001 days after 0002 landed had not been opened by a
// successful harvest since, and nothing else about it looked wrong.
type Schema struct {
	Version   int
	Name      string
	AppliedAt time.Time
}

// Schema reports the newest migration this store has run.
//
// It errors on a database that has never been migrated at all, which is not the
// same as one at version zero: an unmigrated file is not a Timetattle store
// yet, and saying so is more use than reporting a zero nobody wrote.
func (s *Store) Schema(ctx context.Context) (Schema, error) {
	applied, err := migrations.Current(ctx, s.db)
	if err != nil {
		return Schema{}, err
	}
	at, err := parseNullTime(sql.NullString{String: applied.AppliedAt, Valid: true})
	if err != nil {
		return Schema{}, fmt.Errorf("read when %s was applied to %s: %w", applied.Name, s.path, err)
	}
	return Schema{Version: applied.Version, Name: applied.Name, AppliedAt: at}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.db.Close() }

// Path is where this store lives, for error messages and diagnostics.
func (s *Store) Path() string { return s.path }

// Filter selects events to read back.
//
// From and To bound the span, inclusively at both ends, and either may be the
// zero time to mean unbounded. The test is overlap rather than containment: an
// interval that began before From and ended inside the span is in the span,
// because it describes work done during it. An event with only one endpoint
// observed is included when that endpoint falls inside — there is nothing else
// to test, and inventing the missing side is the mistake the Unbounded shape
// exists to refuse.
type Filter struct {
	From   time.Time
	To     time.Time
	Source string
}

// Events reads observations back in anchor order, collapsed.
//
// The anchor is whichever endpoint was observed, so a bare stop sorts among the
// records it belongs with rather than at the zero time, decades early.
//
// This is the normal way to read the store, and it reads the canonical view
// rather than the table: an observation that a later harvest completed is
// represented by the complete one alone. See EventsIncludingSuperseded for the
// table, and the canonical_event view for exactly what is collapsed — defined in
// migration 0001 and recreated by each migration that adds a column, since a view
// names its columns and does not inherit new ones.
func (s *Store) Events(ctx context.Context, f Filter) ([]source.Event, error) {
	return s.events(ctx, "canonical_event", f)
}

// EventsIncludingSuperseded reads the table, holding every observation ever
// made, including ones a later harvest superseded.
//
// It is named at this length on purpose. Summing time over it double-counts
// every session that was ever observed twice at different completeness, which is
// the exact inflation Timetattle exists to catch, so it must not be reachable by
// picking the shorter name. What it is for is provenance: what did we see, when
// did we first see it, and did a horizon ever get completed.
func (s *Store) EventsIncludingSuperseded(ctx context.Context, f Filter) ([]source.Event, error) {
	return s.events(ctx, "event", f)
}

// events reads from either the table or the view. The relation is a constant at
// every call site and never comes from a caller.
func (s *Store) events(ctx context.Context, from string, f Filter) ([]source.Event, error) {
	var (
		where []string
		args  []any
	)
	if !f.From.IsZero() {
		// COALESCE, not ended_at: on an event seen only as it began, the one
		// endpoint there is serves as both.
		where = append(where, `COALESCE(ended_at, started_at) >= ?`)
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `COALESCE(started_at, ended_at) <= ?`)
		args = append(args, formatTime(f.To))
	}
	if f.Source != "" {
		where = append(where, `source_id = ?`)
		args = append(args, f.Source)
	}
	clause := ""
	if len(where) > 0 {
		clause = " WHERE " + strings.Join(where, " AND ")
	}
	query := eventsQuery(from, clause)

	// HOW MANY ROWS ARE COMING, asked before any are read, so the result slice
	// is allocated once at its final size. A slice grown by append allocates
	// about five times the bytes it ends up holding — every regrowth copies
	// into a new array and abandons the one before it — and at a million
	// events that was the largest single allocation in the whole program.
	//
	// This is a HINT and cannot make the result wrong. The store is opened
	// read-only while a harvest may be writing to it, so the count and the
	// read are two moments and may disagree; if more rows arrive between them,
	// append grows the slice exactly as it did before, and if fewer do, the
	// slice is merely longer than it needed to be. Nothing here is trusted as
	// a length. It is also why the count's own error is not fatal below: a
	// failed hint is a slower read, not a failed one.
	out := make([]source.Event, 0, s.countEvents(ctx, from, clause, args))

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read events from %s: %w", s.path, err)
	}
	defer rows.Close()

	// THE SCAN DESTINATIONS ARE DECLARED ONCE AND REUSED, not per row.
	// rows.Scan takes their addresses, so anything declared inside the loop
	// escapes and is heap-allocated on every iteration — ten objects a row,
	// which over a store of a million events is ten million allocations that
	// never appear in the result and are paid for entirely in collector time.
	// Reuse is safe because every field of e is assigned from this row before
	// e is appended: Source and Shape by Scan, and Unknown, Start, End,
	// Identity, State, Locator and Cause below. Adding a field to source.Event
	// and forgetting to assign it here would carry the previous row's value
	// forward, which is the one hazard this shape introduces and the reason
	// Identity is written as a whole struct literal rather than field by field.
	// internal/store's TestReadingEventsAllocatesNoMorePerRowThanTheDriverItself
	// pins the per-row cost against the driver's own, so the loop-local shape
	// cannot come back unnoticed.
	var (
		e                                            source.Event
		unknown, started, ended                      sql.NullString
		app, document, state, locator, projectColumn sql.NullString
		cause                                        sql.NullString
	)
	for rows.Next() {
		if err := rows.Scan(&e.Source, &e.Shape, &unknown, &started, &ended,
			&app, &document, &state, &locator, &projectColumn, &cause); err != nil {
			return nil, fmt.Errorf("read an event from %s: %w", s.path, err)
		}
		e.Unknown = source.Unknown(unknown.String)
		if e.Start, err = parseNullTime(started); err != nil {
			return nil, err
		}
		if e.End, err = parseNullTime(ended); err != nil {
			return nil, err
		}
		e.Identity = source.Identity{
			App:      app.String,
			Document: document.String,
			Project:  projectColumn.String,
		}
		e.State = state.String
		e.Locator = locator.String
		e.Cause = source.Cause(cause.String)
		out = append(out, e)
	}
	return out, rows.Err()
}

// eventsQuery is the SQL of an event read: the columns, the relation, whatever
// WHERE the filter produced, and the anchor order every read comes back in.
//
// It is one function rather than a string assembled at the call site because
// THE ORDER BY IS MATCHED BY AN INDEX, and the match is textual. Migration 0013
// creates event_anchor on the expression COALESCE(started_at, ended_at), id;
// SQLite will use an expression index to satisfy an ORDER BY only when the
// ORDER BY spells the expression the same way the index does. Reordering the
// COALESCE arguments here, or dropping the trailing id, or writing
// ifnull() instead, all still return the right rows — and all silently put a
// temporary B-tree back in front of a million of them, spilled to disk, on a
// connection opened read-only. Nothing about that is visible in a result, which
// is why internal/store's plan test asserts against this function rather than
// against a copy of the query, and why the ORDER BY has exactly one spelling in
// this package.
func eventsQuery(from, clause string) string {
	return `SELECT source_id, shape, unknown, started_at, ended_at,
		app, document, state, locator, project, cause FROM ` + from + clause +
		` ORDER BY COALESCE(started_at, ended_at), id`
}

// countEvents is how many rows the read is about to return, for sizing the
// slice that will hold them.
//
// It returns zero rather than an error on any failure, because it is a
// capacity hint and nothing else: a caller that could not count is a caller
// that grows its slice, which is what every caller did before this existed. An
// error here must never be the reason a read that would have succeeded fails.
func (s *Store) countEvents(ctx context.Context, from, clause string, args []any) int {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+from+clause, args...).Scan(&n); err != nil {
		return 0
	}
	return n
}

// formatTime renders an instant for storage: UTC, fixed width, truncated to the
// microsecond the sources actually resolve.
func formatTime(at time.Time) string {
	return at.UTC().Format(TimeLayout)
}

// nullableTime is formatTime for an endpoint that may never have been observed.
// The zero time is not a very old instant, it is the absence of one, and only
// NULL says that.
func nullableTime(at time.Time) any {
	if at.IsZero() {
		return nil
	}
	return formatTime(at)
}

// nullable keeps the empty string out of the database. An empty string and NULL are the same
// value in the natural key, so storing both would be two spellings of one fact —
// and the locator CHECK refuses an empty locator outright, since it is not an absolute path.
func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func parseNullTime(text sql.NullString) (time.Time, error) {
	if !text.Valid {
		return time.Time{}, nil
	}
	at, err := time.Parse(TimeLayout, text.String)
	if err != nil {
		return time.Time{}, fmt.Errorf("timestamp %q is not in the store's format: %w", text.String, err)
	}
	return at, nil
}
