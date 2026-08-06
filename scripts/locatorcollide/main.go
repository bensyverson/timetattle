// Command locatorcollide measures whether source.Event.Locator can decide
// event_identity dedup — the hazard task Ls0XC exists to quantify.
//
// # Why the store cannot answer this
//
// event_identity is keyed on (source_id, shape, started_at, ended_at, app,
// document, state) and deliberately excludes locator (0001_initial.sql). Two
// events that agree on everything else but locator write ON CONFLICT DO
// NOTHING: the second row is simply not inserted. So grouping the event
// table after the fact can never show a collapse that already happened — the
// losing locator left no trace. Absence of a colliding row in the store is
// not evidence that collisions do not occur; it is the store working exactly
// as designed, which looks identical either way.
//
// This tool instead runs a collector in isolation and inspects its output
// BEFORE anything is written — the "count before insertion" measurement
// described in project/private/2026-07-31-locator-identity.md. It groups a single
// Collect() call's events by the same key event_identity uses, and reports
// any group that would collapse under a fresh store's ON CONFLICT DO
// NOTHING, split into:
//
//   - same locator twice: the identical file observed twice in one run — a
//     collector bug, not the hazard in question.
//   - different locators: the actual hazard. Two distinct files produced one
//     identity, and whichever sorts first in the collector's own output
//     order is the one a real Store.Write would keep — see internal/store/run.go's
//     insertEvent, which inserts a batch in slice order inside one
//     transaction.
//
// # What this does NOT measure
//
// Cross-source collisions. motion-autosave and fs-observations carry
// different source_id, and source_id is the first column of event_identity,
// so two events from different sources can never collide in the store
// regardless of locator, document, or timestamp. Where that matters for this
// analysis, this tool reports document-name overlap across sources
// separately and labels it explicitly as NOT a dedup collision.
//
// # The across-runs question, and what -store answers
//
// Everything above is about one Collect. Task Ows1j asked the other half:
// what happens when the SAME file is harvested twice by two different
// registered paths in two SEPARATE runs — the case Walker.normalizedRoots
// cannot reach, because the two runs never see each other.
//
// -store answers that against the live store without writing to it. It reads
// the store (read-only, no migration, no lock) and compares the identity keys
// already there against the ones this Collect just produced, reporting how
// many rows a harvest right now would actually INSERT. Zero means every
// observation in this Collect is already represented, however its locator is
// spelled. That is the direct measurement of "would re-registering this
// directory under a different path double its evidence" — and, because
// locator is not in the key, the answer is a number rather than an argument.
//
// It also reports the reverse difference: identities in the store that this
// Collect does not produce. Those are not errors. A file modified since an
// earlier harvest leaves its old mtime behind as a permanent row, and a
// deleted file leaves everything it ever contributed. The figure exists so
// that "store rows" and "collect groups" are never quoted side by side
// without the subtraction between them being stated.
//
// # Usage
//
//	go run ./scripts/locatorcollide -motion-vault "/path/to/Autosave Vault"
//	go run ./scripts/locatorcollide -root /path/to/project -root /path/to/other/project
//	go run ./scripts/locatorcollide -root /path/to/project -store ~/.timetattle/timetattle.db
//
// -motion-vault defaults to the motion-autosave manifest source's own root.
// Repeat -root for every directory fs-observations would walk — in
// production that is every directory `timetattle register` has blessed;
// there is no live default because the registry decides that, not this tool.
// -store is off unless given, because this tool must be runnable against a
// machine with no store at all.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timestamped"
)

type roots []string

func (r *roots) String() string { return strings.Join(*r, ", ") }
func (r *roots) Set(v string) error {
	*r = append(*r, v)
	return nil
}

func main() {
	var projectRoots roots
	motionVault := flag.String("motion-vault", "", "Motion Autosave Vault directory (default: the motion-autosave manifest source's own root)")
	flag.Var(&projectRoots, "root", "a directory fs-observations would walk (repeatable; none means fs-observations is not tested)")
	show := flag.Int("show", 10, "how many example collisions to print per report")
	storePath := flag.String("store", "", "an existing store to compare each Collect against, READ-ONLY (e.g. ~/.timetattle/timetattle.db); empty skips the comparison")
	flag.Parse()

	var against *store.Store
	if *storePath != "" {
		st, err := store.OpenReadOnly(*storePath)
		if err != nil {
			fatal("%v", err)
		}
		defer st.Close()
		against = st
	}

	if *motionVault == "" {
		root, err := defaultVaultRoot()
		if err != nil {
			fatal("%v", err)
		}
		*motionVault = root
	}

	ctx := context.Background()

	motionEvents, err := collectMotionAutosave(ctx, *motionVault)
	if err != nil {
		fatal("collect motion-autosave: %v", err)
	}
	report("motion-autosave", *motionVault, motionEvents, *show)
	compareToStore(ctx, against, *storePath, "motion-autosave", motionEvents)

	var fsEvents []source.Event
	if len(projectRoots) > 0 {
		fsEvents, err = fsobserve.New(projectRoots...).Collect(ctx, time.Time{})
		if err != nil {
			fatal("collect fs-observations: %v", err)
		}
		report("fs-observations", strings.Join(projectRoots, ", "), fsEvents, *show)
		compareToStore(ctx, against, *storePath, "fs-observations", fsEvents)
	} else {
		fmt.Println("fs-observations: skipped, no -root given")
	}

	if len(motionEvents) > 0 && len(fsEvents) > 0 {
		crossSourceOverlap(motionEvents, fsEvents, *show)
	}
}

// collectMotionAutosave builds the same timestamped.Reader `timetattle
// harvest` uses for the motion-autosave manifest source, pointed at vault
// instead of whatever the manifest says, so a caller can test a vault copied
// elsewhere without editing a manifest file.
func collectMotionAutosave(ctx context.Context, vault string) ([]source.Event, error) {
	specs, err := manifest.LoadAll("")
	if err != nil {
		return nil, err
	}
	for _, spec := range specs {
		if spec.ID != "motion-autosave" {
			continue
		}
		spec.Files.Root = vault
		reader, err := timestamped.New(spec)
		if err != nil {
			return nil, err
		}
		return reader.Collect(ctx, time.Time{})
	}
	return nil, fmt.Errorf("no motion-autosave source in any loaded manifest")
}

func defaultVaultRoot() (string, error) {
	specs, err := manifest.LoadAll("")
	if err != nil {
		return "", err
	}
	for _, s := range specs {
		if s.ID == "motion-autosave" {
			return s.Files.Root, nil
		}
	}
	return "", fmt.Errorf("no motion-autosave source configured; pass -motion-vault explicitly")
}

// identityKey mirrors event_identity's columns exactly, including its
// COALESCE-to-empty-string treatment of a NULL endpoint or a NULL scalar field —
// two empty strings compare equal in Go exactly the way two NULLs coerced by
// COALESCE compare equal in the index. started_at/ended_at are formatted with
// store.TimeLayout, the literal layout Store.Write uses, so a group found
// here is a group SQLite would actually collapse and not an artifact of a
// looser comparison.
type identityKey struct {
	sourceID string
	shape    source.Shape
	started  string
	ended    string
	app      string
	document string
	state    string
}

func keyOf(e source.Event) identityKey {
	return identityKey{
		sourceID: e.Source,
		shape:    e.Shape,
		started:  formatIfSet(e.Start),
		ended:    formatIfSet(e.End),
		app:      e.Identity.App,
		document: e.Identity.Document,
		state:    e.State,
	}
}

func formatIfSet(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return at.UTC().Format(store.TimeLayout)
}

// report groups events by identityKey and prints the seen/would-be-new split
// a fresh store's ON CONFLICT DO NOTHING would produce, then separates any
// collision into the harmless (identical locator) and hazardous (differing
// locator) case.
func report(name, input string, events []source.Event, show int) {
	fmt.Printf("=== %s ===\n", name)
	fmt.Printf("input              %s\n", input)
	fmt.Printf("events_seen        %d  (what Collect returned)\n", len(events))

	groups := map[identityKey][]source.Event{}
	var order []identityKey
	for _, e := range events {
		k := keyOf(e)
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}
		groups[k] = append(groups[k], e)
	}
	fmt.Printf("events_new         %d  (distinct identity groups — what a fresh store would accept)\n", len(groups))
	fmt.Printf("dropped by dedup   %d\n", len(events)-len(groups))

	var sameLocator, diffLocator int
	var diffExamples []identityKey
	for _, k := range order {
		g := groups[k]
		if len(g) < 2 {
			continue
		}
		locs := map[string]bool{}
		for _, e := range g {
			locs[e.Locator] = true
		}
		if len(locs) <= 1 {
			sameLocator++
			continue
		}
		diffLocator++
		diffExamples = append(diffExamples, k)
	}

	fmt.Printf("collisions, same locator (duplicate observation, not the hazard)       %d\n", sameLocator)
	fmt.Printf("collisions, DIFFERENT locator (THE HAZARD: locator decides the winner) %d\n\n", diffLocator)

	if diffLocator == 0 {
		fmt.Printf("No identity group in this run's output was produced by more than one\n")
		fmt.Printf("locator. Zero groups examined is not the same claim as zero groups\n")
		fmt.Printf("possible — see the doc comment above for why the store cannot check this\n")
		fmt.Printf("for you, and re-run against a population that plausibly triggers it before\n")
		fmt.Printf("generalising from this one.\n\n")
		return
	}

	sort.Slice(diffExamples, func(i, j int) bool {
		return len(groups[diffExamples[i]]) > len(groups[diffExamples[j]])
	})
	fmt.Printf("worst collisions (winner is whichever locator sorts first in Collect's own\n")
	fmt.Printf("output order — see internal/store/run.go's insertEvent, which writes a batch\n")
	fmt.Printf("in slice order inside one transaction):\n")
	for i, k := range diffExamples {
		if i >= show {
			fmt.Printf("  ...and %d more\n", len(diffExamples)-show)
			break
		}
		g := groups[k]
		fmt.Printf("  %s @ %s  app=%s doc=%q state=%q\n", k.shape, k.started, k.app, k.document, k.state)
		for j, e := range g {
			tag := "LOSES"
			if j == 0 {
				tag = "WINS "
			}
			fmt.Printf("      %s %s\n", tag, e.Locator)
		}
	}
	fmt.Println()
}

// compareToStore reports what a harvest of these events into an EXISTING
// store would actually change — the across-runs measurement described in the
// package doc, and the one that answers task Ows1j.
//
// It reads the event TABLE rather than canonical_event, deliberately: the
// question is what was written, and the view collapses a horizon into the
// interval that completed it. It never writes; the store is opened with
// store.OpenReadOnly, which does not create, does not migrate and takes no
// write lock.
//
// The two differences are reported separately and neither is an error:
//
//   - "not in the store" is what a harvest would INSERT. Zero is the
//     interesting answer, because it says the way a locator is spelled buys
//     no new rows.
//   - "in the store, not in this collect" is history this walk cannot see any
//     more — a file modified since an earlier harvest leaves its previous
//     mtime behind as a permanent row, and a deleted file leaves everything.
//     It is printed so the two totals are never quoted without the
//     subtraction between them.
func compareToStore(ctx context.Context, st *store.Store, path, sourceID string, events []source.Event) {
	if st == nil {
		return
	}
	stored, err := st.EventsIncludingSuperseded(ctx, store.Filter{Source: sourceID})
	if err != nil {
		fatal("read %s out of %s: %v", sourceID, path, err)
	}

	inStore := make(map[identityKey]bool, len(stored))
	for _, e := range stored {
		inStore[keyOf(e)] = true
	}
	collected := make(map[identityKey]bool, len(events))
	for _, e := range events {
		collected[keyOf(e)] = true
	}

	var wouldInsert int
	for k := range collected {
		if !inStore[k] {
			wouldInsert++
		}
	}
	var onlyInStore int
	for k := range inStore {
		if !collected[k] {
			onlyInStore++
		}
	}

	fmt.Printf("--- %s against the store at %s (read-only) ---\n", sourceID, path)
	fmt.Printf("store rows for this source                              %d\n", len(stored))
	fmt.Printf("store distinct identities                               %d\n", len(inStore))
	fmt.Printf("this collect's distinct identities                      %d\n", len(collected))
	fmt.Printf("  a harvest right now WOULD INSERT                      %d\n", wouldInsert)
	fmt.Printf("  already present however the locator is spelled        %d\n", len(collected)-wouldInsert)
	fmt.Printf("in the store but not in this collect                    %d  (modified or deleted since an earlier harvest; not an error)\n\n",
		onlyInStore)
	if wouldInsert == 0 {
		fmt.Printf("Nothing this walk found would add a row. Re-registering these directories\n")
		fmt.Printf("under different path strings therefore cannot inflate the evidence count:\n")
		fmt.Printf("event_identity excludes locator, so the spelling of the path is not part\n")
		fmt.Printf("of what makes an observation distinct. Zero here is a measurement of THESE\n")
		fmt.Printf("roots against THIS store, not a proof about every possible pair of paths —\n")
		fmt.Printf("see internal/fsobserve/crossrun_test.go for the constructed case beside it.\n\n")
	}
}

// crossSourceOverlap reports (app, document) pairs seen by both collectors.
// This is NOT an event_identity collision — source_id differs, and source_id
// is the first column of the key, so these rows can never collapse into one
// no matter how everything else agrees. It exists to make an easy mistake
// visible: reading "the vault copy and the project copy compare equal" as a
// statement about the store's dedup, when nothing in the current collector
// wiring lets these two sources' rows ever meet at that key.
func crossSourceOverlap(motion, fsEvents []source.Event, show int) {
	type pair struct{ app, doc string }
	motionDocs := map[pair][]string{}
	for _, e := range motion {
		p := pair{e.Identity.App, e.Identity.Document}
		motionDocs[p] = append(motionDocs[p], e.Locator)
	}
	fsDocs := map[pair][]string{}
	for _, e := range fsEvents {
		p := pair{e.Identity.App, e.Identity.Document}
		fsDocs[p] = append(fsDocs[p], e.Locator)
	}

	var shared []pair
	for p := range motionDocs {
		if _, ok := fsDocs[p]; ok {
			shared = append(shared, p)
		}
	}
	sort.Slice(shared, func(i, j int) bool { return shared[i].doc < shared[j].doc })

	fmt.Printf("=== cross-source (app, document) overlap — informational only ===\n")
	fmt.Printf("motion-autosave documents  %d distinct (app, document) pairs\n", len(motionDocs))
	fmt.Printf("fs-observations documents  %d distinct (app, document) pairs\n", len(fsDocs))
	fmt.Printf("shared by name             %d\n\n", len(shared))
	if len(shared) == 0 {
		fmt.Printf("No document name is shared between the two sources' output for the roots\n")
		fmt.Printf("given. Not evidence either way about the general case.\n\n")
		return
	}
	fmt.Printf("These do NOT collide in event_identity: source_id differs, so the rows\n")
	fmt.Printf("coexist forever regardless of timestamp or locator. Whatever downstream\n")
	fmt.Printf("mechanism is supposed to join them by name is not event_identity dedup.\n\n")
	for i, p := range shared {
		if i >= show {
			fmt.Printf("  ...and %d more\n", len(shared)-show)
			break
		}
		fmt.Printf("  app=%s doc=%q\n", p.app, p.doc)
		fmt.Printf("      motion-autosave: %s\n", strings.Join(uniq(motionDocs[p]), ", "))
		fmt.Printf("      fs-observations: %s\n", strings.Join(uniq(fsDocs[p]), ", "))
	}
	fmt.Println()
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "locatorcollide: "+format+"\n", args...)
	os.Exit(1)
}
