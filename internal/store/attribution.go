package store

import (
	"context"
	"fmt"
)

// Attributed is the identity a locator resolved to: who the work was for, and
// which body of their work it belongs to.
//
// The two travel together because they ARE together — one marker names both,
// and one resolve call returns both. Returning only the project and looking the
// client up afterwards would split one fact across two lookups that can
// disagree; see 0012_event_client.sql for the full argument, and
// source.Identity.Client for why the dependency the registry actually has is
// directory -> (client, project) rather than project -> client.
type Attributed struct {
	// Client is who the work was for.
	Client string
	// Project is the body of that client's work.
	Project string
}

// Resolver maps a locator to the identity it belongs to. found is false, with
// the identity zero, for a locator inside no registered project — that is
// unattributed, not an error.
//
// It is injected rather than imported so this package stays ignorant of how
// attribution actually works: cmd/timetattle wires internal/registry.Resolve
// into it, deriving a directory from the locator first, which is that
// package's own doc comment's job for the caller rather than something to
// duplicate here. The indirection also lets ResolveProjects be tested
// without touching a filesystem or spawning git.
type Resolver func(locator string) (Attributed, bool, error)

// ResolveProjects re-attributes every event carrying a locator, by calling
// resolve once per distinct locator and writing the answer into every row
// that shares it.
//
// It writes ONLY the project. The CLIENT that project belongs to is not an
// event-level fact and is not stored per row: it has 38 distinct values across
// ~973,000 events, so writing it here would be ~927,599 row updates to record
// something the size of a small table, and this pass would rewrite all of them
// on every harvest. It lives in the append-only client_project log (0012) and
// is folded on the way out instead; see internal/clientmap.
//
// event.project is a nullable cache, never a stored fact (see 0001_initial.sql
// and this package's doc), precisely so that registering a directory after
// events already exist, or correcting a mapping, is repaired by calling this
// again rather than needing a new harvest. A locator resolve now reports
// unattributed for is written back to NULL, so a marker that is later
// removed or corrected un-attributes rows too, not only attributes them.
//
// A locator resolve FAILS on is skipped, counted and named, and the pass
// continues. It is not a failure of the pass, and the returned error is
// reserved for the store itself failing.
//
// That is a decision about what unattributed means, so here is the argument.
// A resolver error means the directory could not be READ — a permission
// denied, a symlink cycle, a marker written by a build this one does not
// understand. That is a different fact from "this path is in no project", and
// the two must not be written the same way, so a skipped locator's rows are
// left exactly as they were rather than being set to NULL: un-attributing on
// an error would state as observed-absent something nothing observed. Skipping
// loses nothing permanent either, because project is a recomputable cache
// (0001_initial.sql, this package's doc) and the next pass retries the locator
// from scratch.
//
// Aborting instead is what this used to do, and it is strictly worse: it makes
// one unreadable directory cost every OTHER locator its attribution, which
// contradicts the promise harvest makes about its own sources — one that
// cannot be read loses only its own events. Measured on a copy of the live
// store on 2026-08-01 (go run ./scripts/attributionskip -poison), one
// symlink-cycle locator planted on one of 882,027 located events cost 880,283
// events their attribution under the old behaviour, and costs one under this
// one.
//
// The caller is expected to REPORT Skipped rather than test it, which is why
// Locators is returned beside the three buckets: a pass that skipped nothing
// says so with a zero, and cannot be confused with a build that never counted.
//
// Updated counts event rows whose project actually changed value, including
// the change to NULL; re-running over an unchanged registry reports zero and
// writes nothing.
func (s *Store) ResolveProjects(ctx context.Context, resolve Resolver) (Attribution, error) {
	locators, err := s.distinctLocators(ctx)
	if err != nil {
		return Attribution{}, err
	}
	att := Attribution{Locators: len(locators)}
	if len(locators) == 0 {
		return att, nil
	}

	// One transaction still covers the whole pass, and skipping does not change
	// that. The two are about different failures: a resolver error is a fact
	// about ONE locator, and the other locators' answers are unaffected by it,
	// so there is nothing to roll back; a failure of the UPDATE or the commit
	// is attributable to no locator at all and leaves the pass unable to say
	// what it managed, so it rolls back to the last complete attribution rather
	// than committing a torn one. Nothing is lost by that, because the pass is
	// re-runnable from nothing.
	//
	// Per-locator commits would buy only the ability to keep a partial pass
	// across a crash, at the cost of 127,411 fsyncs where there is now one, and
	// of a store that can be observed mid-pass with two different registries'
	// answers in it.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Attribution{}, fmt.Errorf("begin resolving %d locators: %w", len(locators), err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `UPDATE event SET project = ?
		WHERE locator = ? AND COALESCE(project, '') <> COALESCE(?, '')`)
	if err != nil {
		return Attribution{}, fmt.Errorf("prepare the project update: %w", err)
	}
	defer stmt.Close()

	for _, l := range locators {
		id, found, err := resolve(l.locator)
		if err != nil {
			// No UPDATE runs for this locator, deliberately: its rows keep
			// whatever they already had rather than being told they belong to
			// nothing.
			att.Skipped++
			att.Skips = append(att.Skips, Skip{Locator: l.locator, Events: l.events, Err: err})
			continue
		}
		var project any
		if found {
			project = id.Project
			att.Attributed++
		} else {
			att.Unattributed++
		}
		res, err := stmt.ExecContext(ctx, project, l.locator, project)
		if err != nil {
			return Attribution{}, fmt.Errorf("attribute %s: %w", l.locator, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return Attribution{}, fmt.Errorf("count what changed for %s: %w", l.locator, err)
		}
		att.Updated += int(affected)
	}

	if err := tx.Commit(); err != nil {
		return Attribution{}, fmt.Errorf("commit resolving %d locators: %w", len(locators), err)
	}
	return att, nil
}

// Attribution is what one re-attribution pass did, in numbers, so that its
// result can be questioned without re-running it. It follows
// internal/sessions.Stats: every locator handed to the pass lands in exactly
// one bucket, and the buckets sum to the input.
type Attribution struct {
	// Locators is how many DISTINCT locators the pass considered: the input.
	Locators int
	// Attributed is how many resolved to a project; Unattributed how many
	// resolved to no project, which is the ordinary case for most of the
	// filesystem and not a failure; Skipped how many could not be resolved at
	// all. The three sum to Locators.
	//
	// Unattributed and Skipped are the distinction this store exists to keep.
	// The first was observed to be in no project. The second was not observed:
	// its rows kept the attribution they already had, and are re-tried on the
	// next pass. Reporting only a total would collapse them.
	Attributed, Unattributed, Skipped int
	// Updated is how many event ROWS changed project value, including changes
	// to NULL. It is a row count, not a locator count, and does not reconcile
	// with the three above.
	Updated int
	// Skips names every skipped locator and why, because a count alone cannot
	// be acted on: len(Skips) == Skipped always.
	Skips []Skip
}

// Skip is one locator the resolver could not answer for, and the size of what
// that left unresolved.
type Skip struct {
	Locator string
	// Events is how many event rows carry this locator — one unreadable
	// locator on 200,000 events is a different problem from the same locator
	// on one.
	Events int
	// Err is the resolver's own error, unwrapped, so a caller can match on it.
	Err error
}

// distinctLocators lists every locator at least one event carries, so
// ResolveProjects calls resolve once per locator regardless of how many
// events share it — resolve climbs the filesystem and may spawn git, so
// paying that cost once per event rather than once per locator would scale
// with the wrong thing.
func (s *Store) distinctLocators(ctx context.Context) ([]locatorRows, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT locator, COUNT(*) FROM event
		WHERE locator IS NOT NULL GROUP BY locator`)
	if err != nil {
		return nil, fmt.Errorf("list locators to resolve: %w", err)
	}
	defer rows.Close()

	var out []locatorRows
	for rows.Next() {
		var l locatorRows
		if err := rows.Scan(&l.locator, &l.events); err != nil {
			return nil, fmt.Errorf("read a locator to resolve: %w", err)
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// locatorRows is a distinct locator and how many events carry it. The count
// costs nothing over the DISTINCT this used to be — the same index scan
// produces it — and it is what lets a skipped locator report the size of what
// it left unresolved. One unreadable locator on 200,000 events is a different
// problem from the same locator on one.
type locatorRows struct {
	locator string
	events  int
}
