// Package jobs reads the SQLite task databases the `job` tool writes at
// <directory>/.jobs.db, and turns them into evidence of when work happened and
// what it was.
//
// It is the only source in Timetattle that knows what the work WAS rather than
// merely that something happened. A commit says a file changed; an autosave
// says an application was running; a `job` event says a named unit of work was
// created, claimed, blocked, argued about in a note, or finished. Attribution
// is free, too: .jobs.db sits inside a project directory, so it resolves
// through internal/registry by locator exactly as internal/gitlog does, with no
// heuristics.
//
// # Every row is a point, and no event type is privileged
//
// Planning and coordination IS work — Ben, 2026-08-01, settling the question
// that would otherwise have shaped this package. Creating a task, adding
// criteria, marking one passed, labelling, blocking, unblocking, writing a note
// and reparenting are all deliberate actions on the project, and every one of
// them is evidence. They are also the bulk of the evidence: in the timetattle
// database on the day this was written, planning and coordination rows
// outnumbered claimed/done rows roughly eight to one. A planning session that
// never claims anything — an hour spent building a task tree before any work
// starts — is fully visible here and invisible to every file- and git-based
// source in the project, because it touches no tracked file and produces no
// commit.
//
// So there is no whitelist of interesting event types. There cannot be one: the
// nine live databases measured on 2026-08-01 already carried four types nobody
// had listed (canceled, focus_released, unlabeled, moved), and a reader that
// enumerated what it knew would have been wrong on the day it shipped. Whatever
// the type is, it becomes an event's State — the reading this source took —
// verbatim, and any grouping of types into categories is a decision for
// whatever reports on this, made where it can be argued with.
//
// # The actor is not read at all
//
// Across the nine databases, claims divide into actor=claude 907, actor=ben 85,
// and roughly sixty uniquely-named agent identities. Asked how agent work
// should count, Ben's answer was that it counts fully, that the actor titles
// are disposable, and that activity in the repo IS his working time. Nothing
// here may therefore branch on the actor.
//
// This package goes one step further than not branching: it never SELECTs the
// column. There is nowhere to carry it anyway — source.Event has no free-form
// payload field that internal/store persists — and a value that is read but
// unused is an invitation for the next edit to use it.
//
// # One command is one observation
//
// `job criteria pass a b c` writes three criterion_state rows carrying one
// second, one task and one event type, differing only in a JSON detail. They
// are one thing somebody did. Rows agreeing on (instant, task, event type) are
// therefore collapsed to a single observation, which across the five registered
// databases on 2026-08-01 was 1,361 of 9,150 rows — 1,098 of them
// criterion_state and 188 blocked (`job block x --on a b c`).
//
// The collapse happens HERE rather than being left to the store, even though
// the store's event_identity index would do it anyway. If it were left there,
// harvest would report "9150 events seen, 7789 new" on a first run and
// "9150 seen, 0 new" on the second, and the 1,361 difference would be
// indistinguishable from ordinary re-harvest deduplication. Collapsing at the
// source keeps the seen count equal to the number of observations this package
// claims to have made.
//
// Nothing is lost by it that this project measures: every distinct instant
// survives, every task survives, and the only thing discarded is multiplicity
// within one second of one command, which no timing metric reads. What a
// weight or intensity metric would want — "three criteria passed, not one" —
// is genuinely unavailable, and that is a limitation to state rather than to
// hide.
//
// # Two sources, because a claim span means something different from a claim
//
// job-events is every row as a Point. job-claims is one span per claim. They
// are separate source IDs for the same reason internal/gitlog separates
// commits from reflog: they are different evidence, and internal/sessions
// decides whether a source is clustered as evidence, read as presence, or
// ignored by CONFIGURATION, keyed on the source ID. Folding the spans into
// job-events would take that decision away from whoever calibrates it and bury
// it in this file.
//
// The shapes:
//
//   - claimed → done, released or canceled is an INTERVAL. Both endpoints were
//     directly observed. This is the first source in Timetattle that observes
//     an interval of WORK rather than of presence.
//
//   - claimed → claim_expired is UNBOUNDED with unknown=lost. An expiry is not
//     an observation that work stopped; it is an observation that nobody said
//     anything, and re-reading can never complete it. Synthesising an end at
//     the expiry timestamp would manufacture hours out of silence, which is the
//     same defect as reading /device/isLocked as evidence and inventing a
//     15h32m sitting out of a night's sleep.
//
//     The expiry timestamp is worse than merely uninformative. It is not even
//     the moment of expiry: the tool writes the row lazily, when some later
//     command happens to notice, so three separate claims in the live
//     timetattle database all carry claim_expired at exactly 1785529940, one of
//     them having been claimed 146 minutes earlier against a thirty-minute
//     expiry. It records when somebody next ran `job`, and nothing else.
//
//   - claimed with no terminator yet — a claim open right now — produces NO
//     span. See below; this is the one place this package departs from its
//     brief, which asked for an unbounded horizon.
//
// # Why an open claim gets no span
//
// The store's event_identity index keys on (source_id, shape, endpoints, app,
// document, state) and does NOT include `unknown`. A horizon written for a
// claim that is open now and the lost span written for that same claim once it
// expires would therefore collide on one key: the second insert hits ON
// CONFLICT DO NOTHING, and the row stays labelled 'horizon' for ever, promising
// through Unknown.Resolves that a later harvest will complete something no
// later harvest can. Claims expire in thirty minutes and harvests run hourly,
// so this would not be a corner case — it would be the ordinary path.
//
// A horizon would buy nothing to pay for that, either. It anchors at the claim
// instant and carries no duration, which is precisely what the `claimed` point
// in job-events already records. A horizon earns its keep in internal/biome
// because that reader has no separate point for the start and cannot re-read a
// segment it has already passed; this source re-derives every span from the
// whole database on every harvest, so a span simply appears once its end is
// observed. Nothing has to be held open in the meantime.
//
// A second claim on a task that never terminated the first is the remaining
// case, and it is genuinely lost rather than open: the first claim's end was
// never observed and never will be. No live database contains one — zero
// occurrences across all nine, measured 2026-08-01 — which is exactly why it
// is covered by a constructed test rather than by trusting real data to
// exercise it.
//
// # A claim span bounds the work; it does not measure it
//
// This is stated because the shape does not say it. An interval here asserts
// only that the claim was taken at one instant and given up at another, and a
// person can claim a task, go to lunch and come back. The task brief expected
// this to be the dangerous case, on the strength of a measured "claim → done,
// MAX 583.7 min". That figure does not survive checking: it is task 11 in the
// timetattle database, whose claim was terminated by claim_expired 483.7
// minutes in, with the `done` arriving 100 minutes after that under no claim at
// all. Pairing a claim with the next `done` regardless of what lies between is
// what produced it.
//
// Paired against the FIRST terminator instead, the 1,251 clean spans across all
// nine databases run: median 4m53s, p90 21m35s, p95 28m35s, p99 45m18s, and a
// single span over two hours (2h59m32s), the next longest being 1h19m. The long
// holds are all terminated by claim_expired, which is to say the mechanism that
// produces the dangerous spans is exactly the mechanism this package refuses to
// close. That is the evidence for emitting the interval as evidence, and the
// separate source ID is what leaves the decision reversible without a code
// change.
//
// # One collector over every root, and what that costs
//
// Both sources take every registered directory at once and produce one harvest
// run each, following internal/fsobserve rather than internal/gitlog. gitlog
// runs once per repository and writes one harvest_run row per repository, and
// `timetattle status` groups by source_id and reports the last row — so it
// showed gitlog-commits as "81 seen, 79 new" when the run had actually seen 932
// across five repositories, and four of five repositories could have failed
// while status still printed ok (task uO70D). Reading every root in one call
// avoids inheriting that.
//
// The cost is that a failure is total: if any database is present and cannot be
// read, Collect returns an error and no events at all, because cmd/timetattle's
// ingest discards a source's events whenever Collect errors, so returning the
// readable roots alongside an error would be recorded as a clean, complete,
// silently partial harvest. Nothing is lost permanently by failing loudly —
// .jobs.db is durable, there is no watermark, and every harvest re-reads all of
// it — which is what makes this the opposite judgement from the perishable
// macOS sources, where losing a run loses a day.
//
// # Absent, empty, and unreadable are three different things
//
// A registered directory with no .jobs.db contributes nothing and is not an
// error: a directory is registered because work happens there, not because it
// uses this tool. A .jobs.db that exists and holds no rows contributes nothing
// either, and is likewise a success. A .jobs.db that exists and cannot be
// opened, or does not carry the tables this reader needs, is an ERROR naming
// the path and the reason — never a silent zero. That last distinction is the
// bug this project has now shipped six times in as many tools, most recently
// when a symlinked root made internal/transcripts return 0 of 722,463 events
// with a nil error.
//
// What a single harvest_run row cannot express is WHICH roots were absent, and
// that is the price of not inheriting uO70D. The row records the directories
// searched and the events found; a per-directory census would need a row per
// directory, which is the shape that reports dishonestly.
package jobs

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/source"

	_ "modernc.org/sqlite"
)

// Kind reports that both sources here are read by querying a SQLite database.
// It is shared because Kind describes the READ mechanism and both use one.
const Kind source.Kind = "job-tasks"

// Source IDs. Every event carries one, which is what lets a suspicious number
// be traced back to points or to spans, and what internal/config names when it
// decides whether a stream is evidence, presence or ignored.
const (
	// EventsID is every row in the database, as a point.
	EventsID = "job-events"
	// ClaimsID is one span per claim.
	ClaimsID = "job-claims"
)

// DBName is what the `job` tool calls its database, inside the directory it
// manages.
const DBName = ".jobs.db"

// ClaimState is the reading every job-claims event carries.
//
// It is deliberately constant rather than naming the terminator. State is part
// of the store's identity key, so encoding "done" or "released" in it would
// make an interval and any earlier unbounded reading of the same claim two
// unrelated rows; a constant keeps one claim to one identity whatever ended it.
// Which terminator it was is recoverable from the job-events points at the same
// instant, where it is the reading of a real observation rather than a label
// invented here.
const ClaimState = "claim"

// terminators are the event types that end a claim. Everything else — a note, a
// block, a heartbeat, a criterion marked — happens WHILE a task is claimed and
// leaves the claim standing.
//
// This is a closed set and the only place in this package where an event type
// is interpreted at all. It is closed because ending a claim is a specific
// thing the tool does, not an open vocabulary: a type nobody here recognises is
// treated as not ending the claim, which is the conservative direction — the
// claim stays open, and an open claim produces no span, so an unrecognised
// terminator loses a span rather than inventing one.
var terminators = map[string]bool{
	"done":          true,
	"released":      true,
	"canceled":      true,
	"claim_expired": true,
}

// record is one row of the events table, with the task's short id joined in.
// The actor column is deliberately absent — see the package doc.
type record struct {
	rowID  int64
	task   string
	typ    string
	at     time.Time
	dbPath string
}

// Events collects one Point per distinct (instant, task, event type) across
// every root's .jobs.db.
type Events struct{ roots []string }

// NewEvents returns a source reading job events from every directory in roots.
func NewEvents(roots ...string) *Events { return &Events{roots: roots} }

// ID implements source.Source.
func (e *Events) ID() string { return EventsID }

// Kind implements source.Source.
func (e *Events) Kind() source.Kind { return Kind }

// Collect implements source.Source.
func (e *Events) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	records, err := readAll(ctx, e.roots)
	if err != nil {
		return nil, err
	}

	events := make([]source.Event, 0, len(records))
	for _, r := range records {
		if !since.IsZero() && r.at.Before(since) {
			continue
		}
		ev := source.NewPoint(r.at, source.Identity{Document: r.task}, EventsID)
		ev.State = r.typ
		ev.Locator = r.dbPath
		ev.Cause = source.CauseUserAction
		events = append(events, ev)
	}
	sortEvents(events)
	return events, nil
}

// Claims collects one span per claim across every root's .jobs.db.
type Claims struct{ roots []string }

// NewClaims returns a source reading claim spans from every directory in roots.
func NewClaims(roots ...string) *Claims { return &Claims{roots: roots} }

// ID implements source.Source.
func (c *Claims) ID() string { return ClaimsID }

// Kind implements source.Source.
func (c *Claims) Kind() source.Kind { return Kind }

// Collect implements source.Source.
//
// since is applied to the end of a completed span and never to an unbounded
// one: a span whose end was never observed has reached no instant at which it
// could be considered past, which is the contract source.Source.Collect states.
func (c *Claims) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	records, err := readAll(ctx, c.roots)
	if err != nil {
		return nil, err
	}

	var events []source.Event
	// A claim is scoped to one task in one database. Keying on both is what
	// keeps two projects' task trees, which share short ids freely, from
	// terminating each other's claims.
	type key struct{ db, task string }
	open := make(map[key]record)

	emit := func(claim record, end record, complete bool) {
		id := source.Identity{Document: claim.task}
		var ev source.Event
		if complete {
			ev = source.NewInterval(claim.at, end.at, id, ClaimsID)
		} else {
			ev = source.NewOpenEnd(claim.at, source.UnknownLost, id, ClaimsID)
		}
		ev.State = ClaimState
		ev.Locator = claim.dbPath
		ev.Cause = source.CauseUserAction
		events = append(events, ev)
	}

	for _, r := range records {
		k := key{db: r.dbPath, task: r.task}
		switch {
		case r.typ == "claimed":
			// A second claim with nothing between it and the first means the
			// first claim's end was never observed, and never will be.
			if prior, held := open[k]; held {
				emit(prior, record{}, false)
			}
			open[k] = r
		case terminators[r.typ]:
			claim, held := open[k]
			if !held {
				// A `done` under no claim. Common: task 11 in the timetattle
				// database was finished 100 minutes after its own claim had
				// expired. It is a point in job-events and no span here.
				continue
			}
			delete(open, k)
			// An expiry observes that nobody said anything, not that the work
			// ended. Its timestamp is when a later command noticed, so it
			// cannot serve as an end.
			emit(claim, r, r.typ != "claim_expired")
		}
	}
	// Whatever is still open was open when the harvest ran, and produces no
	// span at all. See the package doc for why a horizon would be worse than
	// nothing here.

	filtered := events[:0]
	for _, ev := range events {
		if ev.Shape == source.Interval && !since.IsZero() && ev.End.Before(since) {
			continue
		}
		filtered = append(filtered, ev)
	}
	events = filtered
	sortEvents(events)
	return events, nil
}

// Sources returns both collectors over the same set of roots, for a caller that
// has resolved which directories it cares about. Nothing here registers itself
// into source's package-level registry: which directories are registered is a
// decision internal/registry makes on the day a harvest runs, exactly as it is
// for internal/gitlog.
func Sources(roots ...string) []source.Source {
	return []source.Source{NewEvents(roots...), NewClaims(roots...)}
}

// Path is where this package looks inside a directory.
func Path(root string) string { return filepath.Join(root, DBName) }

// readAll reads every root's database into one ordered slice of records.
//
// A root with no database contributes nothing and no error. A database that
// exists and cannot be read fails the whole call — see the package doc for why
// the failure is total rather than partial.
func readAll(ctx context.Context, roots []string) ([]record, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var records []record
	for _, root := range roots {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		abs, err := filepath.Abs(Path(root))
		if err != nil {
			return nil, fmt.Errorf("resolve absolute path of %s: %w", Path(root), err)
		}
		switch _, err := os.Stat(abs); {
		case errors.Is(err, os.ErrNotExist):
			// No task database here, which is not a failure and not an
			// observation of anything either. A directory is registered
			// because work happens there, not because it uses this tool.
			continue
		case err != nil:
			// The file's existence could not be established. That is not the
			// same fact as its absence, and reporting it as absence is the
			// mistake this project keeps shipping.
			return nil, fmt.Errorf("cannot tell whether %s exists: %w", abs, err)
		}
		found, err := readDB(ctx, abs)
		if err != nil {
			return nil, err
		}
		records = append(records, found...)
	}

	// Records come back per database in each one's own order, so ordering
	// across roots happens here. rowID breaks ties within a database, which is
	// what makes a claim and the terminator written in the same second pair in
	// the order the tool wrote them.
	sort.SliceStable(records, func(i, j int) bool {
		if !records[i].at.Equal(records[j].at) {
			return records[i].at.Before(records[j].at)
		}
		if records[i].dbPath != records[j].dbPath {
			return records[i].dbPath < records[j].dbPath
		}
		return records[i].rowID < records[j].rowID
	})
	return records, nil
}

// readDB reads one database, collapsing the rows one command wrote at one
// instant into a single record. See the package doc for the measurement.
func readDB(ctx context.Context, path string) ([]record, error) {
	// mode=ro because the `job` tool may well be writing to this file while a
	// harvest runs — it certainly was while this package was written — and
	// busy_timeout so a concurrent write is waited out rather than failing a
	// harvest. This is the DSN internal/store uses to read itself, for the
	// same two reasons.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer db.Close()

	// sql.Open does not touch the file, so without this a database that is
	// present but unopenable would only fail at the first query, where the
	// error would be about a table rather than about the file.
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := checkSchema(ctx, db, path); err != nil {
		return nil, err
	}

	// LEFT JOIN, because task_id is nullable in seven of the nine live schemas
	// and an inner join would silently drop a row whose task had gone. No live
	// database has one today, which is precisely why the join must not be the
	// thing that decides it.
	//
	// GROUP BY is the collapse: `job criteria pass a b c` writes three rows at
	// one second on one task, and they are one action. MIN(e.id) keeps the
	// first row of the group so the tie-break below is stable across harvests.
	rows, err := db.QueryContext(ctx, `
		SELECT MIN(e.id), COALESCE(t.short_id, ''), e.event_type, e.created_at
		FROM events AS e
		LEFT JOIN tasks AS t ON t.id = e.task_id
		GROUP BY e.created_at, COALESCE(t.short_id, ''), e.event_type
		ORDER BY e.created_at, MIN(e.id)`)
	if err != nil {
		return nil, fmt.Errorf("read events from %s: %w", path, err)
	}
	defer rows.Close()

	var out []record
	for rows.Next() {
		var (
			r    record
			unix int64
		)
		if err := rows.Scan(&r.rowID, &r.task, &r.typ, &unix); err != nil {
			return nil, fmt.Errorf("read events from %s: %w", path, err)
		}
		// created_at is unix SECONDS, which is neither the store's TimeLayout
		// nor knowledgeC's Core Data epoch. UTC because everything downstream
		// formats in UTC and a local zone here would only be discarded.
		r.at = time.Unix(unix, 0).UTC()
		r.dbPath = path
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read events from %s: %w", path, err)
	}
	return out, nil
}

// checkSchema refuses a database that is not the shape this reader assumes.
//
// These files are written by a tool this repository does not control, and nine
// of them on one disk need not all be at one migration. A missing table read as
// an empty result would be the silent zero this package exists to avoid, so it
// is named instead.
func checkSchema(ctx context.Context, db *sql.DB, path string) error {
	for _, want := range []struct {
		table   string
		columns []string
	}{
		{table: "events", columns: []string{"id", "task_id", "event_type", "created_at"}},
		{table: "tasks", columns: []string{"id", "short_id"}},
	} {
		present, err := columns(ctx, db, want.table)
		if err != nil {
			return fmt.Errorf("inspect %s: %w", path, err)
		}
		if len(present) == 0 {
			return fmt.Errorf("%s has no %s table; it is not a job task database, "+
				"or it is at a migration this reader does not know", path, want.table)
		}
		for _, c := range want.columns {
			if !present[c] {
				return fmt.Errorf("%s has a %s table with no %s column; "+
					"it is at a migration this reader does not know", path, want.table, c)
			}
		}
	}
	return nil
}

// columns returns the column names of a table, or an empty map if there is no
// such table. PRAGMA table_info answers with no rows for a table that does not
// exist rather than failing, which is what makes both questions one query.
func columns(ctx context.Context, db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

// sortEvents puts events in one order whatever order the roots were given in,
// anchored rather than started: an unbounded span's start is its only endpoint,
// and sorting on Start alone would be fine here but would stop being fine the
// moment a start-less shape appeared.
func sortEvents(events []source.Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Anchor().Equal(events[j].Anchor()) {
			return events[i].Anchor().Before(events[j].Anchor())
		}
		if events[i].Locator != events[j].Locator {
			return events[i].Locator < events[j].Locator
		}
		if events[i].Identity.Document != events[j].Identity.Document {
			return events[i].Identity.Document < events[j].Identity.Document
		}
		return events[i].State < events[j].State
	})
}
