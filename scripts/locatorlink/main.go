// Command locatorlink measures whether any locator in the store is spelled
// through a symlink, and whether resolving it physically changes its project.
//
// # Why this exists
//
// registry.Resolve climbs with filepath.Dir, which is LEXICAL. Until
// 2026-08-01 a path reaching a registered project through a link that points
// at a SUBDIRECTORY of it escaped the project entirely — the link's lexical
// parent is the link's own parent, not the physical subdirectory's — so the
// climb never saw the marker. That is task jph7Z; registry.climbPhysical is
// the fix.
//
// The fix was landed against a store with ZERO instances of the defect, and
// "zero" is a measurement with a shelf life. This tool re-takes it. It is the
// difference between "observed not-X over the whole population" and "nothing
// was recorded", which this project insists on stating separately, so the
// output distinguishes four outcomes per locator rather than two:
//
//   - identical: EvalSymlinks returns the stored string unchanged. No link is
//     involved anywhere in the path. This is the benign case.
//   - relinked: EvalSymlinks returns a DIFFERENT string. The locator is
//     spelled through a link, so it is a candidate for the defect and the two
//     spellings are resolved separately and compared.
//   - unresolvable: EvalSymlinks failed — a vanished directory is the usual
//     reason, a broken link or a cycle the others. Nothing is known about
//     whether a link is involved; this is not evidence of absence.
//   - errored: Resolve itself returned an error, which means an unreadable
//     directory rather than an unregistered one.
//
// CORRECTION, 2026-08-01 (task IYXY1). "errored" is counted only within the
// relinked branch, so it is a figure over the relinked population and not over
// the store. The live store has zero relinked locators, so this tool's
// "errored: 0" has always been 0 out of 0 — "nothing was recorded" — and was
// twice quoted as though it were "no locator in the store errors". It is not
// that measurement and cannot be. scripts/attributionskip resolves EVERY
// distinct locator and prints the figure that can be quoted; on 2026-08-01 it
// agrees, at 0 unreadable out of 127,411, but for the first time over a
// non-empty population.
//
// A "relinked" locator whose two spellings resolve to DIFFERENT projects is a
// live instance of the defect, and is listed individually. A relinked locator
// resolving the same way both times is fine and is only counted.
//
// # What it does NOT do
//
// It never writes. The locator stored against an event is the path that was
// OBSERVED and is never rewritten (task TAKiT proved that by mutation); this
// tool reads the store and the filesystem and prints, so running it against
// the live store is safe.
//
// Usage:
//
//	go run ./scripts/locatorlink                    # ~/.timetattle/timetattle.db
//	go run ./scripts/locatorlink -store PATH
//	go run ./scripts/locatorlink -v                 # list every relinked locator
package main

import (
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/bensyverson/timetattle/internal/registry"

	_ "modernc.org/sqlite"
)

func main() {
	store := flag.String("store", "", "event store to read (default: ~/.timetattle/timetattle.db)")
	verbose := flag.Bool("v", false, "list every locator whose physical spelling differs")
	flag.Parse()

	path := *store
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fatal(err)
		}
		path = filepath.Join(home, ".timetattle", "timetattle.db")
	}
	if err := run(path, *verbose); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "locatorlink:", err)
	os.Exit(1)
}

// row is one distinct locator and how many events carry it, because a single
// misresolving locator on 200,000 events is a different size of problem from
// the same locator on one.
type row struct {
	locator string
	events  int
}

func run(path string, verbose bool) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()

	rows, err := db.Query(`SELECT locator, COUNT(*) FROM event
		WHERE locator IS NOT NULL GROUP BY locator ORDER BY locator`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.locator, &r.events); err != nil {
			return err
		}
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	var (
		identical, unresolvable, errored, vanished int
		relinked                                   []row
		events                                     int
		disagreed                                  []string
		relinkedEvents                             int
	)
	for _, r := range all {
		events += r.events
		if _, err := os.Lstat(r.locator); os.IsNotExist(err) {
			vanished++
		}
		dir := attributionDirOf(r.locator)
		real, err := filepath.EvalSymlinks(dir)
		switch {
		case err != nil:
			unresolvable++
		case real == dir:
			identical++
		default:
			relinked = append(relinked, r)
			relinkedEvents += r.events
			lex, lexOK, lexErr := resolveWithoutFollowing(dir)
			m, physOK, physErr := registry.Resolve(real)
			if lexErr != nil || physErr != nil {
				errored++
				continue
			}
			var phys string
			if physOK {
				phys = m.Project
			}
			if lexOK != physOK || lex != phys {
				disagreed = append(disagreed, fmt.Sprintf(
					"  %s (%d events)\n    as observed: %s\n    physically:  %s",
					r.locator, r.events, describe(lex, lexOK), describe(phys, physOK)))
			}
		}
	}
	sort.Strings(disagreed)

	fmt.Printf("store:              %s\n", path)
	fmt.Printf("distinct locators:  %d over %d events\n", len(all), events)
	fmt.Printf("  identical:        %d   (EvalSymlinks returns the stored string; no link involved)\n", identical)
	fmt.Printf("  relinked:         %d   (%d events; spelled through a link somewhere)\n", len(relinked), relinkedEvents)
	fmt.Printf("  unresolvable:     %d   (path is gone, broken, or cyclic; nothing observed either way)\n", unresolvable)
	fmt.Printf("  errored:          %d   (Resolve could not read a directory)\n", errored)
	fmt.Printf("\nlocators naming a path that is no longer there: %d\n", vanished)
	fmt.Printf("  Reported separately because it is a DIFFERENT population from\n")
	fmt.Printf("  'unresolvable' above: the classification climbs from the locator's\n")
	fmt.Printf("  DIRECTORY, and a vanished file usually has a surviving parent, so\n")
	fmt.Printf("  it classifies as identical. This is the figure comparable with\n")
	fmt.Printf("  project/private/2026-08-01-locator-dedup.md section 3.2.\n")
	fmt.Printf("\nrelinked locators whose two spellings disagree: %d\n", len(disagreed))
	for _, d := range disagreed {
		fmt.Println(d)
	}
	if verbose {
		fmt.Printf("\nevery relinked locator:\n")
		for _, r := range relinked {
			fmt.Printf("  %s (%d events)\n", r.locator, r.events)
		}
	}
	return nil
}

// resolveWithoutFollowing is what Resolve did BEFORE climbPhysical existed:
// the lexical climb, and nothing else. It is reimplemented here rather than
// exported from internal/registry because nothing in the product should ever
// want it — it is the defect. Keeping the old behaviour available exactly once,
// in the tool that measures the defect, is the point.
//
// It also omits the git-common-dir fallback deliberately. That fallback masks
// the defect for a link into a registered repository ROOT, because git resolves
// its working directory physically; including it here would report the defect
// as absent in precisely the case where it exists but is being covered for.
func resolveWithoutFollowing(dir string) (project string, found bool, err error) {
	for {
		data, err := os.ReadFile(filepath.Join(dir, registry.MarkerFilename))
		switch {
		case err == nil:
			var m registry.Marker
			if err := json.Unmarshal(data, &m); err != nil {
				return "", false, err
			}
			return m.Project, true, nil
		case !os.IsNotExist(err):
			return "", false, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false, nil
		}
		dir = parent
	}
}

func describe(project string, found bool) string {
	if !found {
		return "(unattributed)"
	}
	return project
}

// attributionDirOf mirrors cmd/timetattle's function of the same name: a
// locator is a file for some sources and a directory for others, and the climb
// starts from the directory. Duplicated rather than imported because it lives
// in package main there; the two must agree, and this tool is worthless if
// they drift, so any change to one belongs in both.
func attributionDirOf(locator string) string {
	if info, err := os.Stat(locator); err == nil && info.IsDir() {
		return locator
	}
	return filepath.Dir(locator)
}
