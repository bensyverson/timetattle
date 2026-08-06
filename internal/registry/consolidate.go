package registry

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// Consolidation is what Consolidate needs to declare that a vanished path used
// to be part of an already-registered directory.
type Consolidation struct {
	// Dir is the directory the work lives in now. It must already carry a
	// marker of its own: Consolidate adds a former path to an identity, it
	// does not create one.
	Dir string
	// Former is the path the same work used to live at, and which no longer
	// exists. Relative paths are resolved against the current working
	// directory. It matches by path prefix, so declaring a repository root
	// covers everything that was underneath it.
	Former string
	// IndexPath is the registry index to check the declaration against, so a
	// former path two projects both claim is refused rather than resolved
	// arbitrarily. It is required: a guard that is skipped when a caller
	// leaves a field empty is not a guard.
	IndexPath string
}

// Consolidate declares that a path which no longer exists used to be part of an
// already-registered directory, so evidence recorded under the old path
// attributes to the project that owns the new one.
//
// # Why registration cannot do this
//
// Attribution matches the locator an observation was recorded under — the path
// as it was OBSERVED, kept permanently in an append-only store — against
// registered directories, by climbing to the nearest marker. When a directory
// is renamed, every observation recorded under the old name is stranded twice
// over: no marker can be written into a directory that is gone, and registering
// the new location does not help, because the stored locators still name the
// old one. Measured on the live store on 2026-08-01, one rename accounted for
// 14,218 observations, 88% of the affected project's total evidence.
//
// # Why the declaration lives in the marker
//
// Because a former path is part of the directory's identity, and this package
// keeps identity in the directory rather than in a table elsewhere (see the
// package doc). A marker saying "I was once over there" travels with the
// directory when it is moved or cloned, and there is nothing external to keep
// in sync. The registry index is still consulted, but only to know WHICH
// markers to read — its standing role as a hint about where to look, never a
// fact in itself (see List).
//
// # What it deliberately will not do
//
// It will not infer a rename. Two confirmed renames in the live data produced
// two incompatible signatures — one disjoint and adjacent, one overlapping on
// the only day the old path existed — and a detector loose enough to catch both
// would also fire on a genuinely abandoned experiment, inventing an owner for
// evidence that has none. A rename is DECLARED by someone who knows, which took
// under a minute when asked, or it is not recorded at all.
func Consolidate(c Consolidation) (*Marker, error) {
	if c.IndexPath == "" {
		return nil, errors.New("consolidate: an index path is required, so a former path another project " +
			"already claims can be refused rather than resolved arbitrarily")
	}

	dir, err := absClean(c.Dir)
	if err != nil {
		return nil, fmt.Errorf("consolidate: %w", err)
	}
	former, err := absClean(c.Former)
	if err != nil {
		return nil, fmt.Errorf("consolidate: %w", err)
	}

	// The cache checks come first, on both paths, so the vault gets the
	// informative refusal rather than the incidental one. A shared cache holds
	// every client's work and belongs to no project: blessing it is refused by
	// Register, and naming it a former path would reach the same misattribution
	// by another route.
	if root, blocked := blessedCacheConflict(former); blocked {
		return nil, fmt.Errorf("consolidate: %s is, contains, or sits inside a shared application cache "+
			"declared by a manifest (%s); evidence there belongs to every client at once, so it cannot be "+
			"declared a former path of one project", former, root)
	}
	if root, blocked := blessedCacheConflict(dir); blocked {
		return nil, fmt.Errorf("consolidate: %s is, contains, or sits inside a shared application cache "+
			"declared by a manifest (%s); it should never have been registered and cannot take a former path",
			dir, root)
	}

	if _, err := os.Stat(former); err == nil {
		return nil, fmt.Errorf("consolidate: %s still exists; a live directory is registered with "+
			"'timetattle register', not declared a former path of somewhere else", former)
	} else if !os.IsNotExist(err) {
		// Permission denied leaves the path's status genuinely unknown rather
		// than observed absent, and this project does not collapse those two.
		return nil, fmt.Errorf("consolidate: cannot tell whether %s exists: %w", former, err)
	}

	if isAncestorOrSame(dir, former) {
		return nil, fmt.Errorf("consolidate: %s is inside %s, which resolution already climbs to; "+
			"the declaration would say nothing", former, dir)
	}

	info, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("consolidate: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("consolidate: %s is not a directory", dir)
	}

	// Registered means a marker in THIS directory, not one Resolve could climb
	// to. Writing a former path into an ancestor's marker would widen a claim
	// nobody made, and requiring the target to be registered already is what
	// stops this command being an easy way to invent an owner for unrecognised
	// evidence — the failure that makes a wrong attribution worse than none.
	m, found, err := readMarker(dir)
	if err != nil {
		return nil, fmt.Errorf("consolidate: %w", err)
	}
	if !found {
		return nil, fmt.Errorf("consolidate: %s is not registered; run 'timetattle register' on it first, "+
			"so the evidence at %s is consolidated into a project someone has actually named", dir, former)
	}

	registered, listErr := List(c.IndexPath)
	if listErr != nil {
		return nil, fmt.Errorf("consolidate: %w", listErr)
	}
	for _, other := range registered {
		if other.Path == dir {
			continue
		}
		if slices.Contains(other.FormerPaths, former) {
			return nil, fmt.Errorf("consolidate: %s is already a former path of %s / %s at %s; "+
				"one vanished path cannot have been two projects at once", former,
				other.Client, other.Project, other.Path)
		}
	}

	if slices.Contains(m.FormerPaths, former) {
		return m, nil
	}
	m.FormerPaths = append(m.FormerPaths, former)
	sort.Strings(m.FormerPaths)
	if err := writeMarker(m); err != nil {
		return nil, fmt.Errorf("consolidate: %w", err)
	}
	return m, nil
}

// absClean turns a possibly relative path into an absolute, cleaned one.
func absClean(p string) (string, error) {
	if p == "" {
		return "", errors.New("a path is required")
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}
