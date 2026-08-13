// Command consolidatecheck reports what a proposed
// `timetattle consolidate -path DIR -former PATH` would actually recover,
// before anything is declared, and writes nothing anywhere.
//
// # Why this is worth a tool
//
// The declaration itself is cheap and reversible — a former path is a field in
// a marker, and event.project is a recomputable cache — but it is easy to be
// wrong about the SIZE of what it recovers, in one direction that flatters and
// one that alarms:
//
//   - A former path matches by PREFIX. Evidence recorded one or more levels
//     below the renamed directory comes with it, and a count of the exact path
//     alone understates the recovery. Measured on the live store 2026-08-01,
//     /Users/you/git/theaitrellis carries 9,924 observations at the directory
//     itself and 4,294 at theaitrellis/stacktrace: 30% of the total sits below
//     the name anyone would type.
//   - A prefix is not a string prefix. theaitrellis-old is a different
//     directory from theaitrellis, and this tool reports it as such because it
//     asks registry.FormerPathOwner rather than reimplementing the rule in SQL.
//     That is the whole reason it exists as a Go program with a database
//     connection rather than as a query.
//
// # What it deliberately does not do
//
// It does not declare anything, does not write to the store, and does not
// consult or modify any marker belonging to the directories it names. It opens
// the store READ ONLY. Run `timetattle consolidate` to make the declaration and
// `timetattle harvest` to re-attribute; this only says what that would be
// worth.
//
// It reports OBSERVATION COUNTS, not time. An observation count is a poor proxy
// for hours — a transcript-heavy directory emits far more rows per hour than
// gitlog does — so nothing here should be read as an estimate of engaged time.
//
// # Usage
//
//	go run ./scripts/consolidatecheck -path /Users/you/git/nobedan -former /Users/you/git/theaitrellis
//	go run ./scripts/consolidatecheck -path DIR -former OLD -store /path/to/copy.db
package main

import (
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	path := flag.String("path", "", "the registered directory the work lives in now")
	former := flag.String("former", "", "the path it used to live at, which no longer exists")
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	indexPath := flag.String("index", "", "registry index (default: ~/.timetattle/registered-directories.json)")
	flag.Parse()

	if err := run(*path, *former, *storePath, *indexPath); err != nil {
		fmt.Fprintf(os.Stderr, "consolidatecheck: %v\n", err)
		os.Exit(1)
	}
}

func run(path, former, storePath, indexPath string) error {
	if path == "" || former == "" {
		return fmt.Errorf("-path and -former are both required")
	}
	var err error
	if storePath == "" {
		if storePath, err = store.DefaultPath(); err != nil {
			return err
		}
	}
	if indexPath == "" {
		if indexPath, err = registry.DefaultIndexPath(); err != nil {
			return err
		}
	}
	if path, err = filepath.Abs(path); err != nil {
		return err
	}
	if former, err = filepath.Abs(former); err != nil {
		return err
	}

	// The real registry, exactly as harvest reads it, plus the proposed
	// declaration held only in memory. Nothing on disk is touched.
	registered, err := registry.List(indexPath)
	if err != nil {
		return fmt.Errorf("read the registry: %w", err)
	}
	var target *registry.Marker
	for _, m := range registered {
		if m.Path == filepath.Clean(path) {
			target = m
		}
	}
	if target == nil {
		return fmt.Errorf("%s is not a registered directory; 'timetattle consolidate' would refuse this, "+
			"so there is nothing to project", path)
	}
	proposed := *target
	proposed.FormerPaths = append(append([]string(nil), target.FormerPaths...), filepath.Clean(former))
	projected := []*registry.Marker{&proposed}

	db, err := sql.Open("sqlite", "file:"+storePath+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open %s read only: %w", storePath, err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT locator, project, count(*), min(started_at), max(started_at)
		FROM event WHERE locator IS NOT NULL GROUP BY locator, project`)
	if err != nil {
		return fmt.Errorf("read locators: %w", err)
	}
	defer rows.Close()

	type claim struct {
		locator string
		count   int
		first   string
		last    string
	}
	var (
		claimed        []claim
		alreadyOwned   int
		reassignedFrom = map[string]int{}
		total          int
	)
	for rows.Next() {
		var (
			locator     string
			project     sql.NullString
			n           int
			first, last sql.NullString
		)
		if err := rows.Scan(&locator, &project, &n, &first, &last); err != nil {
			return fmt.Errorf("scan a locator: %w", err)
		}
		total += n
		// The real predicate, not a re-statement of it in SQL.
		if _, _, ok := registry.FormerPathOwner(locator, projected); !ok {
			continue
		}
		claimed = append(claimed, claim{locator: locator, count: n, first: first.String, last: last.String})
		switch {
		case !project.Valid || project.String == "":
			// The ordinary case: stranded evidence, attributed to nothing.
		case project.String == proposed.Project:
			alreadyOwned += n
		default:
			reassignedFrom[project.String] += n
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}

	sort.Slice(claimed, func(i, j int) bool { return claimed[i].locator < claimed[j].locator })

	recovered := 0
	for _, c := range claimed {
		recovered += c.count
	}

	fmt.Printf("store   %s (read only, nothing written)\n", storePath)
	fmt.Printf("target  %s -> %s / %s\n", proposed.Path, proposed.Client, proposed.Project)
	fmt.Printf("former  %s (%s)\n\n", former, existence(former))

	if len(claimed) == 0 {
		fmt.Printf("No locator in the store is covered by %s.\n", former)
		fmt.Printf("Nothing was measured under that prefix — which is not the same as measuring zero work there.\n")
		return nil
	}

	fmt.Printf("%-9s %s\n", "COUNT", "LOCATOR")
	for _, c := range claimed {
		fmt.Printf("%-9d %s   %s..%s\n", c.count, c.locator, day(c.first), day(c.last))
	}
	fmt.Printf("\n%d observations would be claimed by this declaration, of %d carrying any locator at all.\n",
		recovered, total)
	fmt.Printf("  %d are unattributed today and would become %s\n",
		recovered-alreadyOwned-sum(reassignedFrom), proposed.Project)
	if alreadyOwned > 0 {
		fmt.Printf("  %d already attribute to %s and would not change\n", alreadyOwned, proposed.Project)
	}
	for p, n := range reassignedFrom {
		fmt.Printf("  %d would be TAKEN FROM %s — check this before declaring\n", n, p)
	}
	fmt.Printf("\nThese are observation counts, not hours. Declare with:\n")
	fmt.Printf("  timetattle consolidate -path %s -former %s\n", proposed.Path, former)
	fmt.Printf("then re-attribute with 'timetattle harvest'.\n")
	return nil
}

func sum(m map[string]int) int {
	t := 0
	for _, n := range m {
		t += n
	}
	return t
}

// existence says whether the former path is really gone, because consolidate
// refuses one that is not and this is the cheapest place to find that out.
func existence(p string) string {
	if _, err := os.Stat(p); err == nil {
		return "STILL EXISTS — consolidate will refuse this; register it instead"
	} else if !os.IsNotExist(err) {
		return fmt.Sprintf("unreadable: %v", err)
	}
	return "gone, as a former path must be"
}

func day(ts string) string {
	if ts == "" {
		return "?"
	}
	if len(ts) >= len(time.DateOnly) {
		return ts[:len(time.DateOnly)]
	}
	return ts
}
