// Command attributionskip measures the population of locators that the
// attribution resolver cannot READ, and what one of them costs a
// re-attribution pass.
//
// # Why this exists
//
// Until 2026-08-01, store.ResolveProjects returned on the first resolver
// error, inside one transaction, so a single unreadable directory
// un-attributed nothing and aborted the pass for every other locator too
// (task IYXY1). It now skips, counts and names the locator instead. This tool
// is what takes the two measurements that decision rests on.
//
// # It takes a measurement locatorlink does NOT
//
// scripts/locatorlink prints an "errored" counter, and it reported 0. That
// figure is 0 out of 0: locatorlink only calls Resolve for locators whose
// physical spelling DIFFERS from the stored one, of which the live store has
// none, so its errored counter has never been over a non-empty population.
// "observed not-X" and "nothing was recorded" again. This tool resolves EVERY
// distinct locator, through the same resolver cmd/timetattle wires into the
// pass, and the census it prints is the one that can be quoted.
//
// Measured on the live store on 2026-08-01: 127,411 distinct locators over
// 882,027 events; 127,363 attributed, 48 unattributed, 0 unreadable. So the
// defect was latent rather than live — but 7 locators are unresolvable and 205
// name a path that has vanished, and none of those are far from unreadable.
//
// # -poison is the mutation
//
// The census cannot show what an unreadable locator COSTS, because there are
// none. -poison copies the store, clears every project so the pass has the
// whole attribution to do, plants a REAL symlink cycle as the locator of one
// event, and runs the real pass over the copy. On 2026-08-01:
//
//	before IYXY1: pass FAILED, updated=0,       0 of 927,396 rows attributed
//	after  IYXY1: pass ok,     updated=880,283, 880,283 rows attributed,
//	              127,363 attributed + 48 unattributed + 1 skipped = 127,412 locators
//
// So one unreadable locator on one event used to cost 880,283 events their
// attribution, and now costs one. The 880,283 reconciles with the census
// above: 880,284 attributable events, less the one whose locator -poison
// overwrote with the cycle.
//
// The "before" line is not reproducible from this tool at HEAD, because the
// behaviour it measures no longer exists; reproduce it with
// `git checkout 9d87d98 -- internal/store/attribution.go` and the equivalent
// call, which returned (updated int, err error).
//
// # What it does NOT do
//
// It never writes to the store it is pointed at. -poison operates on a copy,
// and refuses to run if it cannot make one.
//
// Usage:
//
//	go run ./scripts/attributionskip                 # census only, read-only
//	go run ./scripts/attributionskip -pass           # census, then the real pass on a copy
//	go run ./scripts/attributionskip -poison         # -pass, with a cycle planted first
//	go run ./scripts/attributionskip -store PATH -work DIR
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

func main() {
	storePath := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	pass := flag.Bool("pass", false, "also run the real pass over a COPY of the store, from a cleared project column")
	poison := flag.Bool("poison", false, "-pass, with one real symlink-cycle locator planted first")
	work := flag.String("work", "", "directory for the copy and the cycle (default: a temp dir)")
	flag.Parse()

	if err := run(*storePath, *work, *pass, *poison); err != nil {
		fmt.Fprintln(os.Stderr, "attributionskip:", err)
		os.Exit(1)
	}
}

func run(storePath, work string, pass, poison bool) error {
	if storePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		storePath = filepath.Join(home, ".timetattle", "timetattle.db")
	}

	index, err := registry.DefaultIndexPath()
	if err != nil {
		return err
	}
	markers, err := registry.List(index)
	if err != nil {
		return err
	}

	locators, err := readLocators(storePath)
	if err != nil {
		return err
	}
	fmt.Printf("store:              %s\n", storePath)
	fmt.Printf("registered markers: %d\n", len(markers))
	census(locators, markers)

	if !pass && !poison {
		return nil
	}
	if work == "" {
		if work, err = os.MkdirTemp("", "attributionskip"); err != nil {
			return err
		}
		defer os.RemoveAll(work)
	}
	return mutate(storePath, work, markers, poison)
}

// locator is one distinct locator and how many events carry it.
type locator struct {
	path   string
	events int
}

func readLocators(path string) ([]locator, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`SELECT locator, COUNT(*) FROM event
		WHERE locator IS NOT NULL GROUP BY locator ORDER BY locator`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []locator
	for rows.Next() {
		var l locator
		if err := rows.Scan(&l.path, &l.events); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// census resolves every locator and prints the three outcomes, at locator and
// at event level, both summing to the population. Nothing is windowed: this is
// every locator in the store.
func census(locators []locator, markers []*registry.Marker) {
	var attributed, unattributed, unreadable int
	var evA, evU, evR int
	for _, l := range locators {
		project, found, err := resolveLocator(l.path, markers)
		switch {
		case err != nil:
			unreadable++
			evR += l.events
			fmt.Printf("  unreadable: %s (%d events): %v\n", l.path, l.events, err)
		case found:
			attributed++
			evA += l.events
			_ = project
		default:
			unattributed++
			evU += l.events
		}
	}
	fmt.Printf("distinct locators:  %d   (%d attributed, %d unattributed, %d unreadable)\n",
		len(locators), attributed, unattributed, unreadable)
	fmt.Printf("events:             %d   (%d attributed, %d unattributed, %d unreadable)\n",
		evA+evU+evR, evA, evU, evR)
	fmt.Printf("  'unattributed' is OBSERVED to be in no registered project.\n")
	fmt.Printf("  'unreadable' was not observed at all: the directory could not be read,\n")
	fmt.Printf("  its rows keep the project they already had, and the next pass retries it.\n")
}

// mutate runs the real pass over a COPY of the store carrying one genuinely
// unreadable locator, which is the only way to see what a skip is worth on
// real data: the live store has no erroring locator to borrow.
func mutate(storePath, work string, markers []*registry.Marker, poison bool) error {
	copyPath := filepath.Join(work, "attributionskip-copy.db")
	if copyPath == storePath {
		return fmt.Errorf("refusing to run the pass over %s itself", storePath)
	}
	if err := copyFile(storePath, copyPath); err != nil {
		return err
	}
	defer os.Remove(copyPath)

	db, err := sql.Open("sqlite", "file:"+copyPath)
	if err != nil {
		return err
	}
	// Clearing project first is what makes Updated the WHOLE attribution
	// rather than a delta against whatever the last harvest already wrote.
	if _, err := db.Exec(`UPDATE event SET project = NULL`); err != nil {
		return err
	}
	if poison {
		planted, err := plantCycle(work)
		if err != nil {
			return err
		}
		if _, err := db.Exec(`UPDATE event SET locator = ? WHERE id = (
			SELECT id FROM event WHERE locator IS NOT NULL ORDER BY id LIMIT 1)`, planted); err != nil {
			return err
		}
	}
	db.Close()

	st, err := store.Open(copyPath)
	if err != nil {
		return err
	}
	defer st.Close()

	att, err := st.ResolveProjects(context.Background(), func(l string) (store.Attributed, bool, error) {
		return resolveLocator(l, markers)
	})
	if poison {
		fmt.Printf("\nthe pass over a copy, with one real symlink-cycle locator planted on 1 event:\n")
	} else {
		fmt.Printf("\nthe pass over an unmodified copy, from a cleared project column:\n")
	}
	fmt.Printf("  error:            %v\n", err)
	fmt.Printf("  locators:         %d   (%d attributed, %d unattributed, %d skipped)\n",
		att.Locators, att.Attributed, att.Unattributed, att.Skipped)
	fmt.Printf("  events updated:   %d\n", att.Updated)
	for _, s := range att.Skips {
		fmt.Printf("  skipped:          %s (%d event rows): %v\n", s.Locator, s.Events, s.Err)
	}

	chk, err := sql.Open("sqlite", "file:"+copyPath+"?mode=ro")
	if err != nil {
		return err
	}
	defer chk.Close()
	var attributed, total int
	if err := chk.QueryRow(`SELECT COUNT(*) FROM event WHERE project IS NOT NULL`).Scan(&attributed); err != nil {
		return err
	}
	if err := chk.QueryRow(`SELECT COUNT(*) FROM event`).Scan(&total); err != nil {
		return err
	}
	fmt.Printf("  rows left attributed after the pass: %d of %d\n", attributed, total)
	return nil
}

// plantCycle builds a directory that cannot be read: a -> b -> a. os.ReadFile
// returns ELOOP for anything under it, which is how a genuinely unreadable
// directory reaches the resolver without needing a permission this process
// cannot revoke.
func plantCycle(work string) (string, error) {
	cycle := filepath.Join(work, "cycle")
	if err := os.MkdirAll(cycle, 0o755); err != nil {
		return "", err
	}
	a, b := filepath.Join(cycle, "a"), filepath.Join(cycle, "b")
	os.Remove(a)
	os.Remove(b)
	if err := os.Symlink(b, a); err != nil {
		return "", err
	}
	if err := os.Symlink(a, b); err != nil {
		return "", err
	}
	return filepath.Join(a, "render.motn"), nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(to)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// resolveLocator mirrors cmd/timetattle's function of the same name, which
// lives in package main and cannot be imported. The two must agree; this tool
// measures nothing if they drift, so a change to one belongs in both.
func resolveLocator(path string, markers []*registry.Marker) (store.Attributed, bool, error) {
	m, found, err := registry.ResolveAmong(attributionDirOf(path), path, markers)
	if err != nil || !found {
		return store.Attributed{}, found, err
	}
	return store.Attributed{Client: m.Client, Project: m.Project}, true, nil
}

// attributionDirOf mirrors cmd/timetattle's function of the same name, exactly
// as scripts/locatorlink already duplicates it, and for the same reason.
func attributionDirOf(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}
