// Command attribute runs the attribution pass against a store, and nothing
// else.
//
// # Why this exists
//
// The pass that resolves a locator to the client and project it belongs to runs
// as a phase of `timetattle harvest`, and there was no other way to invoke it.
// That is fine for the ordinary case — registering a directory and then
// harvesting repairs the events already under it — but it makes one question
// impossible to answer honestly: does re-attribution alone repair existing
// events, or does it need a fresh read of the sources?
//
// Migration 0012 added the append-only client-project log, and the claim that
// already-harvested events acquire a client WITHOUT a re-harvest is exactly
// that question. Running `harvest` to demonstrate it would prove nothing, since
// a harvest also re-reads every source and writes new events. This runs the
// pass and only the pass: it opens the store, applies any pending migration,
// resolves every distinct locator against the registry, backfills a
// client-project assertion for any registered directory the log has never heard
// from, and reports what moved.
//
// scripts/attributionskip already runs the pass, and is the wrong tool here: it
// works on a COPY which it deletes on the way out, because its subject is what
// happens when one locator cannot be read. This one operates on the store it is
// given and leaves it there.
//
// # Use
//
//	go run ./scripts/attribute                      # the default store
//	go run ./scripts/attribute -store /path/to.db   # a copy, to look first
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

func main() {
	storePath := flag.String("store", "", "event store to attribute (default: ~/.timetattle/timetattle.db)")
	indexPath := flag.String("index", "", "registry index to read (default: ~/.timetattle/registered-directories.json)")
	flag.Parse()

	if err := run(*storePath, *indexPath); err != nil {
		fmt.Fprintln(os.Stderr, "attribute:", err)
		os.Exit(1)
	}
}

func run(storePath, indexPath string) error {
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

	// A marker that fails to parse is REPORTED and costs only itself; every
	// other directory is still read. That is List's own tolerance and this
	// tool does not second-guess it, but it does say so rather than letting a
	// short list pass for a complete one.
	markers, listErr := registry.List(indexPath)
	if listErr != nil {
		fmt.Fprintf(os.Stderr, "attribute: some registered directories could not be read, and their "+
			"events keep whatever attribution they had: %v\n", listErr)
	}
	fmt.Printf("store:     %s\n", storePath)
	fmt.Printf("registry:  %s (%d directories)\n", indexPath, len(markers))

	clients := map[string]map[string]bool{}
	for _, m := range markers {
		if clients[m.Client] == nil {
			clients[m.Client] = map[string]bool{}
		}
		clients[m.Client][m.Project] = true
	}
	fmt.Printf("           %d clients across %d directories\n", len(clients), len(markers))

	st, err := store.Open(storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	att, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		m, found, err := registry.ResolveAmong(attributionDirOf(locator), locator, markers)
		if err != nil || !found {
			return store.Attributed{}, found, err
		}
		return store.Attributed{Client: m.Client, Project: m.Project}, true, nil
	})
	if err != nil {
		return err
	}

	// Every locator lands in exactly one bucket and the three sum to the input,
	// and the zeroes are printed too: a pass that skipped nothing says "0
	// unreadable" rather than saying nothing, because a silent pass is what a
	// build that never counted skips would also produce.
	fmt.Printf("\nlocators:  %d   (%d attributed, %d unattributed, %d unreadable)\n",
		att.Locators, att.Attributed, att.Unattributed, att.Skipped)
	fmt.Printf("events:    %d re-attributed\n", att.Updated)
	for _, s := range att.Skips {
		fmt.Printf("  unreadable: %s (%d event rows): %v\n", s.Locator, s.Events, s.Err)
	}

	// The client-project log, backfilled for any directory it has never heard
	// from — the same thing harvest's attribution phase does, and the reason
	// this tool exists: it is what gives already-harvested events a client
	// without re-reading a single source.
	//
	// IT ONLY EVER ADDS. An existing assertion is left exactly alone however
	// stale it looks, because changing one has two possible meanings (a
	// reassignment from a date, or a correction covering all of history) and
	// neither can be inferred from a marker. `timetattle register -since` and
	// `-correction` are where that intent is supplied.
	known, err := st.ClientMap(ctx, time.Time{})
	if err != nil {
		return err
	}
	recorded := 0
	for _, m := range markers {
		if m.Client == "" || m.Project == "" || known.AssertedBy(m.Project, m.Path) {
			continue
		}
		if err := st.AppendClientAssertion(ctx, clientmap.Assertion{
			Project: m.Project, Client: m.Client, Source: m.Path,
			RecordedAt: time.Now().UTC(),
		}, "attribute"); err != nil {
			return err
		}
		recorded++
	}
	after, err := st.ClientMap(ctx, time.Time{})
	if err != nil {
		return err
	}
	// The zero is printed too. A run that recorded nothing says so, rather than
	// saying nothing, because silence is also what a build that never looked
	// would produce.
	fmt.Printf("clients:   %d assertions recorded, %d projects mapped, %d contested\n",
		recorded, len(after.Projects()), len(after.Collisions()))
	for _, c := range after.Collisions() {
		fmt.Printf("  contested: %s is claimed by %v from %v\n", c.Project, c.Clients, c.Sources)
	}
	return nil
}

// attributionDirOf mirrors cmd/timetattle's function of the same name, exactly
// as scripts/attributionskip and scripts/locatorlink already duplicate it, and
// for the same reason: a locator is a FILE for some sources and a DIRECTORY for
// others, and climbing from the parent of a registered root finds no marker at
// all.
func attributionDirOf(path string) string {
	if info, err := os.Stat(path); err == nil && info.IsDir() {
		return path
	}
	return filepath.Dir(path)
}
