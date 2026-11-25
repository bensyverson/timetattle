// Command attributionprofile times the attribution pass, phase by phase, over a
// COPY of a store, and compares the (locator, project) pairs two runs left
// behind.
//
// It runs the real store pass and the real registry resolver, against the real
// filesystem and the real registry index. It never writes to the store it is
// pointed at.
//
// -nocache turns off the resolver's per-pass memory of each directory, which is
// how the figures from before that memory existed are reproduced.
//
// Its output is counts and durations only. A locator, a project name and an
// error message that quotes either are never printed, so the output can go into
// a published document.
//
// # Where the phases come from
//
// The store is not instrumented. "list locators" runs from the start of the pass
// to the first resolver call, "database updates" is the time between one
// resolver call returning and the next starting, and "commit" is what follows
// the last one (including that locator's update). The lookups inside the
// resolver are reported by registry.Resolver, and "other resolver work" is the
// remainder: former-path matching, path arithmetic and this tool's own
// bookkeeping.
//
// # Conditions
//
// A run reads whatever the filesystem cache holds; nothing here empties it. To
// approximate the launchd agent's Background process type and low-priority I/O
// (scripts/install-agent.sh), build the tool and run it under taskpolicy:
//
//	go build -o /tmp/attributionprofile ./scripts/attributionprofile
//	taskpolicy -b /tmp/attributionprofile -store COPY.db
//
// Usage:
//
//	go run ./scripts/attributionprofile -store PATH [-work DIR] [-clear] [-nocache] [-keep NAME]
//	go run ./scripts/attributionprofile -compare A.db,B.db
//
// -clear empties the project column of the copy first, so that every pair in the
// result was written by this run. -keep leaves the copy in -work under NAME for
// a later -compare.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

type options struct {
	store, work, keep, index string
	clear, noCache           bool
}

func main() {
	var opts options
	flag.StringVar(&opts.store, "store", "", "store to copy and run the pass over (required)")
	flag.StringVar(&opts.work, "work", "", "directory for the copy (default: a temp dir)")
	flag.StringVar(&opts.keep, "keep", "", "keep the copy in -work under this file name")
	flag.StringVar(&opts.index, "index", "", "registry index (default: ~/.timetattle/registered-directories.json)")
	flag.BoolVar(&opts.clear, "clear", false, "empty the copy's project column before the pass")
	flag.BoolVar(&opts.noCache, "nocache", false, "ask the filesystem every time, as the baseline")
	compare := flag.String("compare", "", "two stores, comma separated: count the (locator, project) pairs that differ")
	flag.Parse()

	var err error
	if *compare != "" {
		err = runCompare(*compare, os.Stdout)
	} else {
		err = run(opts, os.Stdout)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "attributionprofile:", err)
		os.Exit(1)
	}
}

func run(opts options, out io.Writer) error {
	if opts.store == "" {
		return errors.New("-store is required")
	}
	if opts.keep != "" && opts.work == "" {
		return errors.New("-keep needs -work, since a temp dir is removed")
	}
	if opts.index == "" {
		path, err := registry.DefaultIndexPath()
		if err != nil {
			return errors.New("no home directory to find the registry index in")
		}
		opts.index = path
	}
	markers, listErr := registry.List(opts.index)
	if listErr != nil && len(markers) == 0 {
		return errors.New("the registry index could not be read")
	}

	if opts.work == "" {
		dir, err := os.MkdirTemp("", "attributionprofile")
		if err != nil {
			return errors.New("no temp dir for the copy")
		}
		defer os.RemoveAll(dir)
		opts.work = dir
	}
	name := opts.keep
	if name == "" {
		name = "attributionprofile.db"
		defer removeStore(filepath.Join(opts.work, name))
	}
	// The pass resolves a relative locator against the working directory, and
	// launchd starts the agent in /. Match it, so two runs of this tool from
	// different directories cannot disagree.
	copyPath, err := filepath.Abs(filepath.Join(opts.work, name))
	if err != nil {
		return errors.New("no absolute path for the copy")
	}
	if err := copyStore(opts.store, copyPath); err != nil {
		return err
	}
	if opts.clear {
		if err := clearProjects(copyPath); err != nil {
			return err
		}
	}
	if err := os.Chdir("/"); err != nil {
		return errors.New("could not change to /")
	}
	st, err := store.Open(copyPath)
	if err != nil {
		return errors.New("the copy could not be opened as a store")
	}
	defer st.Close()

	caching := registry.CachePerResolver
	if opts.noCache {
		caching = registry.NoCache
	}
	rep, err := profile(context.Background(), st, markers, caching)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "registered markers: %d (%t unreadable entries)\n", len(markers), listErr != nil)
	fmt.Fprintf(out, "project column cleared first: %t\n", opts.clear)
	fmt.Fprintf(out, "directory cache: %t\n", !opts.noCache)
	rep.write(out)
	return nil
}

// passer is the part of the store this tool runs, so a test can stand in for it.
type passer interface {
	ResolveProjects(context.Context, store.Resolver) (store.Attribution, error)
}

type tally struct {
	calls int
	paths map[string]struct{}
	took  time.Duration
}

type report struct {
	att                                         store.Attribution
	relative                                    int
	lookups                                     map[registry.Lookup]*tally
	listing, resolving, updating, commit, total time.Duration
	gitNotDirectory                             int
	selfCPU, childCPU                           time.Duration
}

var lookupKinds = []registry.Lookup{
	registry.LookupStat, registry.LookupMarker, registry.LookupPhysicalPath, registry.LookupGitRoot,
}

func profile(ctx context.Context, st passer, markers []*registry.Marker, caching registry.Caching) (*report, error) {
	rep := &report{lookups: map[registry.Lookup]*tally{}}
	for _, k := range lookupKinds {
		rep.lookups[k] = &tally{paths: map[string]struct{}{}}
	}
	resolver := &registry.Resolver{Caching: caching, Observe: func(kind registry.Lookup, path string, took time.Duration) {
		t := rep.lookups[kind]
		t.calls++
		t.paths[path] = struct{}{}
		t.took += took
	}}

	selfBefore, childBefore := cpu(syscall.RUSAGE_SELF), cpu(syscall.RUSAGE_CHILDREN)
	start := time.Now()
	var last time.Time
	att, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		entered := time.Now()
		if last.IsZero() {
			rep.listing = entered.Sub(start)
		} else {
			rep.updating += entered.Sub(last)
		}
		if !filepath.IsAbs(locator) {
			rep.relative++
		}
		id, found, err := resolveLocator(resolver, locator, markers)
		last = time.Now()
		rep.resolving += last.Sub(entered)
		return id, found, err
	})
	end := time.Now()
	if err != nil {
		// The store's own error quotes the locator it failed on.
		return nil, errors.New("the pass failed; its error is withheld because it can name a locator")
	}
	rep.att = att
	rep.total = end.Sub(start)
	if last.IsZero() {
		rep.listing = rep.total
	} else {
		rep.commit = end.Sub(last)
	}
	rep.selfCPU = cpu(syscall.RUSAGE_SELF) - selfBefore
	rep.childCPU = cpu(syscall.RUSAGE_CHILDREN) - childBefore
	for path := range rep.lookups[registry.LookupGitRoot].paths {
		if info, err := os.Stat(path); err != nil || !info.IsDir() {
			rep.gitNotDirectory++
		}
	}
	return rep, nil
}

func (r *report) write(out io.Writer) {
	fmt.Fprintf(out, "locators:           %d (%d attributed, %d unattributed, %d skipped, %d not absolute)\n",
		r.att.Locators, r.att.Attributed, r.att.Unattributed, r.att.Skipped, r.relative)
	fmt.Fprintf(out, "event rows updated: %d\n", r.att.Updated)

	line := func(name string, took time.Duration, detail string) {
		fmt.Fprintf(out, "  %-22s %10.2fs%s\n", name, took.Seconds(), detail)
	}
	fmt.Fprintln(out, "phases:")
	line("list locators", r.listing, "")
	var inLookups time.Duration
	for _, k := range lookupKinds {
		t := r.lookups[k]
		inLookups += t.took
		line(k.String(), t.took, fmt.Sprintf("  %d calls, %d distinct paths", t.calls, len(t.paths)))
	}
	line("other resolver work", r.resolving-inLookups, "")
	line("database updates", r.updating, "")
	line("commit", r.commit, "")
	line("total", r.total, "")
	fmt.Fprintf(out, "git rev-parse paths that are not directories: %d\n", r.gitNotDirectory)
	fmt.Fprintf(out, "CPU: %.1fs in this process, %.1fs in git processes\n", r.selfCPU.Seconds(), r.childCPU.Seconds())
}

func cpu(who int) time.Duration {
	var u syscall.Rusage
	if err := syscall.Getrusage(who, &u); err != nil {
		return 0
	}
	return time.Duration(u.Utime.Nano() + u.Stime.Nano())
}

// resolveLocator mirrors cmd/timetattle's attribution wiring, which lives in
// package main and cannot be imported. A change to one belongs in both, or this
// tool measures something the harvest does not run.
func resolveLocator(r *registry.Resolver, locator string, markers []*registry.Marker) (store.Attributed, bool, error) {
	dir := filepath.Dir(locator)
	if r.IsDir(locator) {
		dir = locator
	}
	m, found, err := r.ResolveAmong(dir, locator, markers)
	if err != nil || !found {
		return store.Attributed{}, found, err
	}
	return store.Attributed{Client: m.Client, Project: m.Project}, true, nil
}

// copyStore clones the store, and its write-ahead log if it has one. cp -c asks
// APFS for a copy-on-write clone and falls back to a plain copy elsewhere.
func copyStore(from, to string) error {
	src, err := os.Stat(from)
	if err != nil {
		return errors.New("-store could not be read")
	}
	if dst, err := os.Stat(to); err == nil && os.SameFile(src, dst) {
		return errors.New("refusing to run the pass over -store itself")
	}
	removeStore(to)
	for _, suffix := range []string{"", "-wal"} {
		if _, err := os.Stat(from + suffix); err != nil {
			continue
		}
		if exec.Command("cp", "-c", from+suffix, to+suffix).Run() == nil {
			continue
		}
		if err := exec.Command("cp", from+suffix, to+suffix).Run(); err != nil {
			return errors.New("-store could not be copied")
		}
	}
	return nil
}

func removeStore(path string) {
	for _, suffix := range []string{"", "-wal", "-shm"} {
		os.Remove(path + suffix)
	}
}

func clearProjects(path string) error {
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		return errors.New("the copy could not be opened")
	}
	defer db.Close()
	if _, err := db.Exec(`UPDATE event SET project = NULL`); err != nil {
		return errors.New("the copy's project column could not be cleared")
	}
	return nil
}

// comparison counts the distinct (locator, project) pairs of two stores.
type comparison struct {
	pairsA, pairsB, onlyA, onlyB, attributedA, attributedB int
}

func comparePairs(a, b string) (comparison, error) {
	var c comparison
	for _, path := range []string{a, b} {
		if _, err := os.Stat(path); err != nil {
			return c, errors.New("a store to compare could not be read")
		}
	}
	db, err := sql.Open("sqlite", "file:"+a)
	if err != nil {
		return c, errors.New("the first store could not be opened")
	}
	defer db.Close()
	// ATTACH belongs to one connection, so there must only be one.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`ATTACH DATABASE ? AS other`, b); err != nil {
		return c, errors.New("the second store could not be attached")
	}
	const pairs = `SELECT DISTINCT locator, project FROM %s.event WHERE locator IS NOT NULL`
	first, second := fmt.Sprintf(pairs, "main"), fmt.Sprintf(pairs, "other")
	for _, q := range []struct {
		into  *int
		query string
	}{
		{&c.pairsA, first},
		{&c.pairsB, second},
		{&c.onlyA, first + " EXCEPT " + second},
		{&c.onlyB, second + " EXCEPT " + first},
		{&c.attributedA, first + " AND project IS NOT NULL"},
		{&c.attributedB, second + " AND project IS NOT NULL"},
	} {
		if err := db.QueryRow("SELECT COUNT(*) FROM (" + q.query + ")").Scan(q.into); err != nil {
			return c, errors.New("the pairs could not be compared")
		}
	}
	return c, nil
}

func runCompare(arg string, out io.Writer) error {
	a, b, ok := strings.Cut(arg, ",")
	if !ok {
		return errors.New("-compare wants two stores, comma separated")
	}
	c, err := comparePairs(a, b)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "distinct (locator, project) pairs: %d first, %d second\n", c.pairsA, c.pairsB)
	fmt.Fprintf(out, "pairs with a project:              %d first, %d second\n", c.attributedA, c.attributedB)
	fmt.Fprintf(out, "only in the first: %d, only in the second: %d\n", c.onlyA, c.onlyB)
	return nil
}
