package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

// resolveProjects re-attributes every event carrying a locator against the
// registry.
//
// It runs as a phase after ingestion rather than inside Collect: the store's
// project column is documented (internal/store's package doc,
// 0001_initial.sql) as a recomputable cache rather than a stored fact,
// specifically so that registering a directory after a harvest, or fixing a
// mapping, repairs events that already exist. store.Write does not even
// accept a project value for that reason. Running this every harvest is what
// makes that repair automatic instead of a step someone has to remember to
// re-run.
// registered is what registry.List returned for this run. It is passed in
// rather than read here because harvest already has it in hand for the sources
// it plans, and because a former path is only readable through the marker of a
// directory the index names — see resolveLocator.
//
// # What it prints, and why the zeroes are printed too
//
// The summary line is unconditional and reports Locators beside the three
// buckets, which sum to it. A pass that skipped nothing says "0 unreadable"
// rather than saying nothing, because a silent pass is what a build that never
// counted skips would also produce, and those are different facts. This is the
// same rule ingest follows in printing a source that found nothing.
//
// # Why a skip is still an error
//
// ResolveProjects treats an unreadable locator as data rather than as a failure
// — correctly, since the pass did its job for every other locator. But a
// directory this machine could not read is a problem the RUN had, and harvest
// aggregates and counts those. So one error is returned per skipped locator,
// joined rather than wrapped, so harvestFailure's leaf count reports three
// unreadable directories as three problems rather than one. The events are
// already safe: they keep the attribution they had, and the next harvest
// retries them.
func resolveProjects(ctx context.Context, st *store.Store, registered []*registry.Marker, out io.Writer) error {
	att, err := st.ResolveProjects(ctx, passResolver(&registry.Resolver{}, registered))
	if err != nil {
		return fmt.Errorf("resolve projects: %w", err)
	}
	fmt.Fprintf(out, "attribution: %s, %d attributed, %d unattributed, %d unreadable\n",
		plural(att.Locators, "locator"), att.Attributed, att.Unattributed, att.Skipped)
	if att.Updated > 0 {
		fmt.Fprintf(out, "%s re-attributed\n", plural(att.Updated, "event"))
	}
	if err := backfillClientAssertions(ctx, st, registered, out); err != nil {
		return err
	}
	var errs []error
	for _, s := range att.Skips {
		fmt.Fprintf(out, "  unreadable: %s (%s): %v\n", s.Locator, plural(s.Events, "event"), s.Err)
		errs = append(errs, fmt.Errorf("attribution: %s could not be resolved, so its %s keep the project "+
			"they already had: %w", s.Locator, plural(s.Events, "event"), s.Err))
	}
	return errors.Join(errs...)
}

// backfillClientAssertions records a client-project assertion for any
// registered directory the log has never heard from.
//
// IT ONLY EVER ADDS, and that is the whole safety argument. If the log already
// carries an assertion from this directory about this project, nothing happens
// here however stale it looks — changing an association is a decision with two
// possible meanings (a reassignment from a date, or a correction covering all
// of history) and neither can be inferred from a marker. `timetattle register`
// is where that intent is supplied; see its -since and -correction flags.
//
// So this closes exactly one gap: a marker that exists and has never been
// recorded. That happens for every directory blessed before migration 0012, for
// a repository cloned onto a second machine, and for a marker somebody wrote by
// hand. Without it those projects would report hours under no client at all,
// which is a true statement and a useless one.
//
// The assertion it writes is RETROACTIVE — zero effective_from, "from the
// beginning of time" — because that is what a first registration means: the
// evidence under a directory usually predates anyone getting round to blessing
// it, and an assertion starting at the moment it was written would attribute
// none of it.
func backfillClientAssertions(ctx context.Context, st *store.Store,
	registered []*registry.Marker, out io.Writer) error {
	if len(registered) == 0 {
		return nil
	}
	known, err := st.ClientMap(ctx, time.Time{})
	if err != nil {
		return fmt.Errorf("read the client-project log: %w", err)
	}
	recorded := 0
	for _, m := range registered {
		if m.Client == "" || m.Project == "" {
			continue
		}
		if known.AssertedBy(m.Project, m.Path) {
			continue
		}
		if err := st.AppendClientAssertion(ctx, clientmap.Assertion{
			Project: m.Project, Client: m.Client, Source: m.Path,
			RecordedAt: time.Now().UTC(),
		}, "harvest"); err != nil {
			return err
		}
		recorded++
	}
	// Printed only when something happened, unlike the attribution line above
	// it: that one reports a population every run and its zero is meaningful,
	// while this reports an event and a zero here is the ordinary steady state.
	if recorded > 0 {
		fmt.Fprintf(out, "client-project log: %s recorded for directories it had not heard from\n",
			plural(recorded, "assertion"))
	}
	return nil
}

// resolveLocator adapts registry.ResolveAmong, which climbs from a directory
// and consults declared former paths, to store.Resolver, which is handed a
// locator. Mapping the shapes a locator can take onto a directory is
// deliberately left to the caller by Resolve's own doc comment rather than
// being that package's job, so it happens here, in the one place both concerns
// meet, rather than being duplicated wherever a locator needs resolving.
//
// registered carries the second half of the answer. A locator whose directory
// was renamed away cannot be climbed from at all — there is nothing above it
// holding a marker — so the marker that owns it has to be found by looking
// across the registry instead of up the tree. Passing nil gives a plain climb,
// which is the right answer for any caller with no registry to consult.
//
// The UNSTRIPPED locator is what goes to the former-path match, and the
// stripped directory only to the climb. attributionDirOf cannot tell a vanished
// directory from a file, so it strips a level from exactly the locators a
// former path exists to catch. On the live store on 2026-08-01, passing the
// stripped directory instead would have matched 4,294 of the 14,218 stranded
// observations and left the 9,924 that name the renamed directory itself.
// It returns BOTH halves of the identity, because the marker holds both and
// this is the only place that has it. Returning the project alone and looking
// the client up afterwards is what this pass used to do implicitly, by throwing
// the client away — the marker was in hand on every one of the 38 registered
// directories and only `project` reached the store.
func resolveLocator(locator string, registered []*registry.Marker) (store.Attributed, bool, error) {
	return resolveLocatorWith(&registry.Resolver{}, locator, registered)
}

// passResolver answers every locator of one pass through r, so that what r
// remembers about a directory serves all the locators in it. r must not outlive
// the pass: nothing tells it the filesystem or the registry has changed.
func passResolver(r *registry.Resolver, registered []*registry.Marker) store.Resolver {
	return func(locator string) (store.Attributed, bool, error) {
		return resolveLocatorWith(r, locator, registered)
	}
}

// resolveLocatorWith is resolveLocator, asking the filesystem through r.
func resolveLocatorWith(r *registry.Resolver, locator string, registered []*registry.Marker) (store.Attributed, bool, error) {
	m, found, err := r.ResolveAmong(attributionDirIn(r, locator), locator, registered)
	if err != nil || !found {
		return store.Attributed{}, found, err
	}
	return store.Attributed{Client: m.Client, Project: m.Project}, true, nil
}

// attributionDirOf is the directory to climb from for a locator.
//
// A locator is "the absolute path the evidence was read from" (source.Event),
// and that is a FILE for some sources and a DIRECTORY for others.
// internal/timestamped names a Blender backup, .../acorn/blender/tile.blend1;
// internal/transcripts names the record's own cwd and internal/gitlog names the
// repository, both of which are directories, and both of which are ordinarily
// the registered directory ITSELF rather than something under it.
//
// Taking filepath.Dir unconditionally is therefore right for the first and one
// level too high for the other two, and it fails silently: the climb starts at
// the registered root's PARENT, finds no marker, and reports unattributed,
// which is indistinguishable from a directory nobody registered. Measured on
// the live store on 2026-08-01, before this was corrected: 209,467 events sat
// at a locator that is itself a registered project root and attributed to
// nothing — every one of the 1,626 gitlog events in the store, and 90% of the
// 722,832 Claude transcript events. Total attribution went from 100,176 events
// to 309,643 when this was fixed, with no change to any other code.
//
// A path that is not there is treated as a file. It cannot be told apart from
// one by anything on disk, and climbing from the parent can then only ever
// under-attribute a vanished directory by one level, which is the direction
// this project prefers over inventing an attribution. Stat is affordable
// because ResolveProjects calls the resolver once per DISTINCT locator, not
// once per event.
func attributionDirOf(locator string) string {
	return attributionDirIn(&registry.Resolver{}, locator)
}

// attributionDirIn is attributionDirOf, asking the filesystem through r.
func attributionDirIn(r *registry.Resolver, locator string) string {
	if r.IsDir(locator) {
		return locator
	}
	return filepath.Dir(locator)
}
