package store

import (
	"context"
	"fmt"
	"strings"
)

// locatorsUnderQuery lists the distinct locators that are one directory or
// beneath it. Its arguments are the directory, the directory with a trailing
// separator, and the first string sorting after everything with that prefix.
//
// It is a pair of comparisons rather than LIKE or GLOB so that event_locator
// can be searched, and so that a "_" or "%" in a directory name stays a
// character.
const locatorsUnderQuery = `SELECT locator, COUNT(*) FROM event
	WHERE locator = ? OR (locator >= ? AND locator < ?) GROUP BY locator`

// PathAttribution is what a path-limited re-attribution did.
type PathAttribution struct {
	Attribution
	// Events is how many event rows under the directories each project now
	// owns, whether or not the pass had to change them. Updated cannot answer
	// that: it is zero for a directory that was already attributed.
	Events map[string]int
}

// ResolveProjectsUnder is ResolveProjects limited to the locators that are one
// of dirs or beneath one. dirs are clean absolute paths, compared as text: a
// caller wanting another spelling of a directory passes it as well.
//
// Unlike ResolveProjects it asks resolve about every locator BEFORE beginning
// its transaction, so the write lock is held only for the updates. It exists
// for an interactive command, which must neither hold off a harvest while it
// climbs the filesystem nor, when a harvest holds the lock, fail for any longer
// than the store's busy timeout. An error means nothing was written.
func (s *Store) ResolveProjectsUnder(ctx context.Context, dirs []string, resolve Resolver) (PathAttribution, error) {
	var none PathAttribution
	locators, err := s.locatorsUnder(ctx, dirs)
	if err != nil {
		return none, err
	}
	att := PathAttribution{Attribution: Attribution{Locators: len(locators)}, Events: map[string]int{}}

	type answer struct {
		locator string
		project any
	}
	var answers []answer
	for _, l := range locators {
		id, found, err := resolve(l.locator)
		if err != nil {
			att.Skipped++
			att.Skips = append(att.Skips, Skip{Locator: l.locator, Events: l.events, Err: err})
			continue
		}
		a := answer{locator: l.locator}
		if found {
			a.project = id.Project
			att.Attributed++
			att.Events[id.Project] += l.events
		} else {
			att.Unattributed++
		}
		answers = append(answers, a)
	}
	if len(answers) == 0 {
		return att, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return none, fmt.Errorf("begin attributing %d locators: %w", len(answers), err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, setProjectQuery)
	if err != nil {
		return none, fmt.Errorf("prepare the project update: %w", err)
	}
	defer stmt.Close()

	for _, a := range answers {
		res, err := stmt.ExecContext(ctx, a.project, a.locator, a.project)
		if err != nil {
			return none, fmt.Errorf("attribute %s: %w", a.locator, err)
		}
		affected, err := res.RowsAffected()
		if err != nil {
			return none, fmt.Errorf("count what changed for %s: %w", a.locator, err)
		}
		att.Updated += int(affected)
	}
	if err := tx.Commit(); err != nil {
		return none, fmt.Errorf("commit attributing %d locators: %w", len(answers), err)
	}
	return att, nil
}

// locatorsUnder lists each distinct locator at or beneath any of dirs once,
// however many of them cover it.
func (s *Store) locatorsUnder(ctx context.Context, dirs []string) ([]locatorRows, error) {
	var out []locatorRows
	seen := map[string]bool{}
	for _, dir := range dirs {
		prefix := strings.TrimSuffix(dir, "/") + "/"
		// "0" is the byte after "/".
		end := strings.TrimSuffix(prefix, "/") + "0"
		rows, err := s.db.QueryContext(ctx, locatorsUnderQuery, dir, prefix, end)
		if err != nil {
			return nil, fmt.Errorf("list locators under %s: %w", dir, err)
		}
		for rows.Next() {
			var l locatorRows
			if err := rows.Scan(&l.locator, &l.events); err != nil {
				rows.Close()
				return nil, fmt.Errorf("read a locator under %s: %w", dir, err)
			}
			if !seen[l.locator] {
				seen[l.locator] = true
				out = append(out, l)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("list locators under %s: %w", dir, err)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list locators under %s: %w", dir, err)
		}
	}
	return out, nil
}
