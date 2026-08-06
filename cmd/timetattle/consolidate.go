package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/bensyverson/timetattle/internal/registry"
)

// consolidateCommand declares that a path which no longer exists used to be
// part of a registered directory.
//
// # Why a verb of its own rather than a flag on register
//
// `register -former PATH` would add no new vocabulary, which is the right
// instinct — the primary user of this CLI is an agent, and fewer well-named
// verbs beat many. It was rejected because the two operations do not share a
// contract. register REQUIRES -client and -project and requires the directory
// NOT to be a cache; this requires the target to be registered ALREADY, and its
// -client and -project would be meaningless or, worse, silently ignored.
// Folding them together would make register's flags conditionally valid on each
// other, which is exactly what an agent guessing a command line gets wrong.
//
// The name is Ben's ("a consolidation command that marks an old repo name as
// the same as a current repo"), kept over the shorter `alias` because it says
// what happens to the evidence rather than naming the mechanism, and because
// two records becoming one is the outcome the operator is after. The usage line
// carries the words "renamed" and "moved" so the verb is findable by an agent
// that only knows what happened, not what it is called.
func consolidateCommand(args []string) error {
	fs := flag.NewFlagSet("consolidate", flag.ExitOnError)
	path := fs.String("path", "", "the registered directory the work lives in now")
	former := fs.String("former", "", "a path the same work used to live at, which no longer exists")
	indexPath := fs.String("index", "",
		"registry index to check the declaration against (default: ~/.timetattle/registered-directories.json)")
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: timetattle consolidate -path DIR -former PATH

Declares that a directory which was renamed or moved away used to be part of
-path, so evidence recorded under the old name attributes to -path's project
instead of being stranded.

Evidence is recorded under the path it was OBSERVED at, permanently. Renaming a
directory therefore strands everything recorded under the old name: no marker
can be written into a directory that is gone, and registering the new location
does not help, because the stored paths still name the old one.

-former matches by prefix, so declaring a repository root also recovers
everything that was underneath it. It is written into -path's own marker, as
part of that directory's identity, so it travels with the directory the same
way its client and project do.

This declares a rename; it never guesses one. -path must already be registered,
and -former must be a path that no longer exists and that no other project has
claimed. Evidence whose owner nobody recognises stays unattributed, which is the
honest answer, rather than being given an owner by a command that made it easy.

Run 'timetattle harvest' afterwards to re-attribute; project is a recomputable
cache, so nothing needs re-collecting.

Flags:
`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *path == "" {
		return errors.New("timetattle consolidate: -path is required")
	}
	if *former == "" {
		return errors.New("timetattle consolidate: -former is required")
	}

	idx := *indexPath
	if idx == "" {
		var err error
		if idx, err = registry.DefaultIndexPath(); err != nil {
			return fmt.Errorf("timetattle consolidate: %w", err)
		}
	}

	m, err := registry.Consolidate(registry.Consolidation{Dir: *path, Former: *former, IndexPath: idx})
	if err != nil {
		return err
	}

	// Unlike register, this is not best effort. A former path is read back
	// through registry.List, so a target the index does not name is never
	// consulted and the declaration is inert — it would report success and
	// change nothing, which is the worst of the available outcomes. Recording
	// is idempotent, and the ordinary case is that register already did it.
	if err := registry.RecordInIndex(idx, m.Path); err != nil {
		return fmt.Errorf("timetattle consolidate: %s now declares %s as a former path, but %s could not be "+
			"recorded in %s (%w); until it is, attribution will not read that declaration", m.Path, *former, m.Path, idx, err)
	}

	// The acknowledgement names every former path the marker now carries, not
	// only the one just added: a directory can be renamed more than once, and
	// what the project covers in total is what the operator is checking.
	fmt.Printf("%s / %s at %s now also covers %s\nRun 'timetattle harvest' to re-attribute.\n",
		m.Client, m.Project, m.Path, listOrNone(m.FormerPaths))
	return nil
}
