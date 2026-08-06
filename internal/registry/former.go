package registry

import (
	"path/filepath"
	"sort"
	"strings"
)

// ResolveAmong resolves a path against the registry, by climbing from
// climbFrom the way Resolve does and by matching path against the former paths
// declared by the markers in registered.
//
// registered is what List returns. It is passed in rather than read here for
// the same reason Register does not touch the index: this package resolves a
// path, and deciding which index to consult — a real one, or a test's — belongs
// to the caller. Passing nil gives exactly Resolve's behaviour.
//
// # Why climbFrom and path are separate, which is not a stylistic choice
//
// Resolve's contract is that it is given a DIRECTORY, so a caller holding a
// file must hand over the containing directory (climbing from a file path
// fails: reading a marker "inside" a file is ENOTDIR, not ENOENT, and this
// package refuses to treat an unreadable path as an absent one). A path that no
// longer exists cannot be told apart from a file by anything on disk, so
// cmd/timetattle's attributionDirOf strips a level from it — correct for the
// climb, and destructive here: the whole point of a former path is that the
// vanished directory itself is what the evidence names.
//
// Matching the un-stripped path costs nothing, because a former path matches by
// PREFIX and a prefix match does not care whether the tail is a file or a
// directory. Measured against the live store on 2026-08-01, on the rename this
// was built for: 9,924 of the 14,218 stranded observations name the vanished
// directory exactly, and 4,294 name a subdirectory of it. Matching the stripped
// directory recovers only those 4,294 — 30% — and reports success.
//
// # Why a second mechanism is needed at all, and why it is not a second rule
//
// Climbing cannot answer for a path that no longer exists: there is no marker
// to find above it, and the marker that should answer sits in a directory
// somewhere else entirely. So a former path is looked up the other way round —
// across the registered markers, rather than up the tree — and that inversion
// is the whole of the addition. See Consolidate for why the declaration lives
// in the marker rather than in a table.
//
// The precedence is the rule the climb already uses, extended rather than
// replaced: THE MOST SPECIFIC CLAIM ON THE PATH WINS. climb stops at the
// nearest marker for the same reason. Two consequences worth stating, because
// both are cases a simpler ordering gets wrong:
//
//   - A marker at or below the former path beats the declaration. Consolidate
//     refuses a former path that still exists, but nothing stops a new
//     repository being cloned there later and registered to someone else, and
//     from then on the evidence there really is theirs. The filesystem is the
//     fact; the declaration describes what a path USED to be.
//   - A declaration beats a marker further up the tree. Blessing a container
//     directory would otherwise silently shadow every rename declared beneath
//     it, and quietly attributing renamed work to a catch-all is the
//     wrong-attribution-is-worse-than-none failure.
func ResolveAmong(climbFrom, path string, registered []*Marker) (*Marker, bool, error) {
	live, liveFound, err := Resolve(climbFrom)
	if err != nil {
		return nil, false, err
	}

	abs, err := absClean(path)
	if err != nil {
		return nil, false, err
	}
	alias, former, aliasFound := FormerPathOwner(abs, registered)

	switch {
	case !aliasFound:
		return live, liveFound, nil
	case !liveFound:
		return alias, true, nil
	case pathDepth(former) > pathDepth(live.Path):
		return alias, true, nil
	default:
		return live, true, nil
	}
}

// FormerPathOwner finds the most specific declared former path covering path,
// and the marker that declared it.
//
// It matches by path prefix and never touches the filesystem, which is what
// makes it answerable for a path that no longer exists — the only kind of path
// a former path is ever declared for. ResolveAmong uses it for the second half
// of an attribution; it is exported separately because "is this vanished path
// spoken for, or is it evidence nobody can ever claim" is a question reporting
// has to ask without wanting a full resolution, and answering it twice in two
// places is how the two answers start to disagree.
//
// Ties — two projects declaring former paths of equal depth that both cover
// path — are broken by the declaring directory's path. Consolidate refuses a
// former path another project already claims outright, so a tie needs two
// markers that were never checked against one another; resolving it
// deterministically, and saying so, is what this project asks for in place of
// an arbitrary winner that changes between runs.
func FormerPathOwner(path string, registered []*Marker) (m *Marker, former string, found bool) {
	dir := filepath.Clean(path)
	type candidate struct {
		marker *Marker
		former string
	}
	var candidates []candidate
	for _, r := range registered {
		if r == nil {
			continue
		}
		for _, f := range r.FormerPaths {
			if isAncestorOrSame(f, dir) {
				candidates = append(candidates, candidate{marker: r, former: filepath.Clean(f)})
			}
		}
	}
	if len(candidates) == 0 {
		return nil, "", false
	}
	sort.Slice(candidates, func(i, j int) bool {
		di, dj := pathDepth(candidates[i].former), pathDepth(candidates[j].former)
		if di != dj {
			return di > dj
		}
		return candidates[i].marker.Path < candidates[j].marker.Path
	})
	return candidates[0].marker, candidates[0].former, true
}

// pathDepth counts the separators in a cleaned absolute path, so "more
// specific" is measured in directory levels rather than characters — a longer
// name at the same level is not a stronger claim.
func pathDepth(p string) int {
	return strings.Count(filepath.Clean(p), string(filepath.Separator))
}
