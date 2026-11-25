package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bensyverson/timetattle/internal/gitenv"
)

// Resolve finds the marker of the nearest directory at or above dir that
// Register has blessed.
//
// dir is expected to be a directory — a cwd, typically. A caller resolving a
// file locator (an autosave path, say) must pass its containing directory;
// mapping the many shapes a locator can take to a directory is a resolution
// concern for the caller, not this package's.
//
// A path that resolves to nothing returns found=false and a nil error. That
// is not a failure: most of the filesystem is not registered, on purpose —
// opt-in is what keeps a rate or a budget from ever reaching a client's own
// repository — and the caller is expected to represent it as unattributed
// rather than treat it as an error.
func Resolve(dir string) (*Marker, bool, error) {
	return new(Resolver).Resolve(dir)
}

// Resolve is the package function Resolve, asked through r.
func (r *Resolver) Resolve(dir string) (*Marker, bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, false, fmt.Errorf("resolve %s: %w", dir, err)
	}
	abs = filepath.Clean(abs)

	if m, ok, err := r.climb(abs); err != nil || ok {
		return m, ok, err
	}

	if m, ok, err := r.climbPhysical(abs); err != nil || ok {
		return m, ok, err
	}

	root, ok := r.gitRoot(abs)
	if !ok || isAncestorOrSame(root, abs) {
		// Either dir is not inside a git repository at all — the ordinary
		// case for a plain directory like a Motion vault — or the
		// repository's root was already on the chain climb just walked, so
		// climbing it again would repeat work for the same answer: no.
		return nil, false, nil
	}
	return r.climb(root)
}

// climbPhysical repeats the climb from dir's physical path, for a dir that
// reaches the tree through a symlink.
//
// # The hole this closes
//
// climb walks filepath.Dir, which is purely LEXICAL, so a path that reaches a
// registered project through a link pointing at a SUBDIRECTORY of it escapes
// the project entirely: the link's lexical parent is the link's own parent, not
// the physical subdirectory's, and the climb leaves without ever seeing the
// marker.
//
//	project/          <- marker here
//	project/sub/
//	alias -> project/sub
//
// A link to the registered directory ITSELF was never affected, and neither is
// a link's immediate child: os.ReadFile follows the link and reads the same
// marker, which is what internal/fsobserve's
// TestBothRegisteredPathsResolveToTheSameProject pins. The escape needs the
// climb to step ABOVE the link.
//
// # Why it runs here and not first
//
// After climb, because the locator is the path that was OBSERVED and a marker
// standing above the path as observed is the answer for it — see
// TestResolvePrefersTheMarkerAboveThePathAsObserved. Running physically first
// would silently relocate every path spelled through a link out of the project
// it was seen in. It also keeps the cost off the ordinary path entirely: this
// only runs for a locator that resolved to nothing, and ResolveProjects calls
// the resolver once per DISTINCT locator rather than once per event. On the
// live store on 2026-08-01 that is 48 calls, out of 127,368 distinct locators
// over 878,074 events — the unattributed ones — and not one of them has a
// physical spelling that differs (go run ./scripts/locatorlink).
//
// Before gitCommonRoot, because the git fallback answers with the repository
// ROOT and then climbs UPWARD, so it cannot see a marker between the root and
// the link's target and returns the outer project instead — confidently, which
// is the wrong-attribution-is-worse-than-none failure. It happens to answer
// correctly whenever the registered directory IS the repository root, since git
// resolves its working directory physically; that accident is why an alias into
// a registered git repository was never broken, and it is not a substitute for
// this, which also covers a registered directory that is not a repository at
// all. TestResolveThroughASymlinkPrefersAMarkerBelowTheGitRoot is the case that
// separates them.
//
// # Why a failure here is silent
//
// A path with no physical spelling — a broken link, a link cycle, a directory
// that has since been renamed away — is not a directory of unknown status. The
// lexical climb has already read every directory it could and surfaced any
// permission error itself; EvalSymlinks failing means only that there is no
// second spelling to try. Erroring instead would still be wrong, because it
// would report as unreadable a path that was read perfectly well and simply
// is not there: 205 of the live store's 127,411 distinct locators name paths
// that no longer exist (go run ./scripts/attributionskip). An earlier draft of
// this comment said 191 of 127,368, and before that 29,689, which was the
// count in a superseded document rather than one measured here.
//
// That draft also justified the silence by saying internal/store.ResolveProjects
// aborts the whole re-attribution transaction on a single resolver error. It no
// longer does — task IYXY1 made it skip, count and name the locator instead —
// so the cost of erroring here is now one locator's attribution rather than
// every locator's. The argument above does not depend on that, and stands.
//
// EvalSymlinks cannot loop: it enforces its own link budget and returns ELOOP.
func (r *Resolver) climbPhysical(dir string) (*Marker, bool, error) {
	real, err := r.physicalPath(dir)
	if err != nil || real == dir {
		// real == dir is the overwhelmingly common case — 127,361 of the
		// 127,368 distinct locators in the live store on 2026-08-01, the other
		// 7 being unresolvable rather than relinked — and the second climb
		// would re-read exactly the same directories for exactly the same
		// answer.
		return nil, false, nil
	}
	return r.climb(real)
}

// climb checks dir and each of its ancestors, nearest first, for a marker,
// stopping at the first one found or at the filesystem root.
func (r *Resolver) climb(dir string) (*Marker, bool, error) {
	for {
		m, ok, err := r.marker(dir)
		if err != nil || ok {
			return m, ok, err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, false, nil
		}
		dir = parent
	}
}

// readMarker reads the marker in dir, if there is one.
func readMarker(dir string) (*Marker, bool, error) {
	path := filepath.Join(dir, MarkerFilename)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		// Anything else — permission denied is the realistic case — leaves
		// this directory's status genuinely unknown rather than observed
		// absent, and treating those two the same is a mistake this project
		// has already shipped more than once. Surface it instead of
		// silently climbing past it.
		return nil, false, fmt.Errorf("read marker %s: %w", path, err)
	}
	var m Marker
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, false, fmt.Errorf("parse marker %s: %w", path, err)
	}
	if m.Version != Version {
		return nil, false, fmt.Errorf("marker %s declares marker_version %d, but this build understands %d",
			path, m.Version, Version)
	}
	m.Path = dir
	return &m, true, nil
}

// gitCommonRoot reports the working-tree root of the repository whose
// metadata dir actually shares, using git rather than any assumption about
// where a worktree lives.
//
// For an ordinary checkout this is dir's own toplevel, so it changes
// nothing. For a linked worktree — which is what every agent worktree under
// .claude/worktrees is — git-common-dir names the MAIN repository's .git,
// because a worktree keeps its own HEAD and index but shares refs and
// objects with the checkout it was created from. Its parent is therefore the
// repository the worktree was forked from, deterministically, wherever the
// worktree itself happens to sit: nested under the root, a sibling
// directory, or under /tmp. --path-format=absolute (git 2.31+, 2021) is what
// makes the answer usable regardless of where dir sits relative to it.
//
// Failure is silent and non-fatal by design. Most directories Resolve is
// asked about are not git repositories at all — a Motion autosave vault, a
// scratch render directory — and that is the ordinary case, not an error.
//
// git exports GIT_DIR, GIT_WORK_TREE and their relatives into every hook it
// runs. A process invoked from inside one of this project's own git hooks —
// this package's own tests, run through `git commit`, are the case that
// surfaced it — inherits them, and they silently override -C: the command
// below would resolve to whichever repository invoked the hook instead of
// dir, however unrelated dir is. Since Resolve has no control over what
// invoked it, internal/gitenv.Command strips them first rather than
// trusting -C alone; see internal/gitenv's package doc for why that rule
// lives there rather than as a copy here.
func gitCommonRoot(dir string) (string, bool) {
	cmd := gitenv.Command("git", "-C", dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	out, err := cmd.Output()
	if err != nil {
		return "", false
	}
	commonDir := strings.TrimSpace(string(out))
	if commonDir == "" {
		return "", false
	}
	return filepath.Dir(commonDir), true
}
