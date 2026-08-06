package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// The fixtures in this file all have the shape project/ (marker) + project/sub/
// + an alias pointing at project/sub, which is the case
// project/private/2026-08-01-locator-dedup.md §3.1 found and could not fix in place.
// An alias to the registered directory ITSELF has always worked — os.ReadFile
// follows the link and reads the same marker, which is what
// internal/fsobserve/crossrun_test.go's
// TestBothRegisteredPathsResolveToTheSameProject pins. Only an alias BELOW the
// marker escaped, because the climb is lexical and the alias's lexical parent
// is its own parent rather than the physical subdirectory's.

// aliasIntoSubdirectory builds
//
//	base/project/          <- marker, registered to acme/widget
//	base/project/sub/
//	base/alias -> base/project/sub
//
// and returns the physical project directory and the alias. Both are returned
// already resolved through EvalSymlinks where that matters, because t.TempDir
// on macOS hands back a path under /var, which is itself a symlink to
// /private/var: comparing a marker's Path against an unresolved temp path
// would fail for a reason that has nothing to do with what is under test.
func aliasIntoSubdirectory(t *testing.T) (project, sub, alias string) {
	t.Helper()
	base := t.TempDir()
	project = filepath.Join(base, "project")
	sub = filepath.Join(project, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if _, err := registry.Register(registry.Registration{
		Dir: project, Client: "acme", Project: "widget",
	}); err != nil {
		t.Fatalf("Register(%s) = %v", project, err)
	}
	alias = filepath.Join(base, "alias")
	if err := os.Symlink(sub, alias); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	return physical(t, project), physical(t, sub), alias
}

// physical is EvalSymlinks with the error turned into a test failure.
func physical(t *testing.T, p string) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s) = %v", p, err)
	}
	return real
}

func TestResolveFindsTheProjectThroughASymlinkIntoASubdirectory(t *testing.T) {
	project, _, alias := aliasIntoSubdirectory(t)

	m, found, err := registry.Resolve(alias)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", alias, err)
	}
	if !found {
		t.Fatalf("Resolve(%s) found = false; the alias IS %s/sub and the marker sits at %s, "+
			"but the lexical climb leaves through the alias's own parent and never sees it",
			alias, project, project)
	}
	if m.Client != "acme" || m.Project != "widget" {
		t.Errorf("Resolve(%s) = %s / %s, want acme / widget", alias, m.Client, m.Project)
	}
	if m.Path != project {
		t.Errorf("Resolve(%s).Path = %s, want the directory the marker is really in, %s",
			alias, m.Path, project)
	}
}

func TestResolveFindsTheProjectFromADescendantOfSuchASymlink(t *testing.T) {
	project, sub, alias := aliasIntoSubdirectory(t)
	if err := os.MkdirAll(filepath.Join(sub, "deeper"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	through := filepath.Join(alias, "deeper")

	m, found, err := registry.Resolve(through)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", through, err)
	}
	if !found {
		t.Fatalf("Resolve(%s) found = false, want the marker at %s", through, project)
	}
	if m.Client != "acme" || m.Project != "widget" {
		t.Errorf("Resolve(%s) = %s / %s, want acme / widget", through, m.Client, m.Project)
	}
}

// TestResolveThroughASymlinkStillStopsAtTheNearestMarker is the precedence rule
// from TestResolveInnerMarkerWinsOverAnOuterOne, asked of a path that only
// reaches the tree through a link.
//
// The alias must point BELOW the inner marker, not at it and not at its parent.
// The escape needs the climb to step ABOVE the alias to happen at all: the
// lexical parent of alias/x is alias itself, and reading alias/.timetattle
// follows the link like any other path, so a marker one level under the alias
// is still found the old way. Only once the climb walks past the alias does it
// leave the physical tree.
func TestResolveThroughASymlinkStillStopsAtTheNearestMarker(t *testing.T) {
	project, sub, _ := aliasIntoSubdirectory(t)
	if _, err := registry.Register(registry.Registration{
		Dir: sub, Client: "acme", Project: "widget-sub",
	}); err != nil {
		t.Fatalf("Register(%s) = %v", sub, err)
	}
	deeper := filepath.Join(sub, "deeper")
	if err := os.MkdirAll(deeper, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	alias := filepath.Join(filepath.Dir(project), "deep-alias")
	if err := os.Symlink(deeper, alias); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	m, found, err := registry.Resolve(alias)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", alias, err)
	}
	if !found {
		t.Fatalf("Resolve(%s) found = false, want the nearer marker at %s", alias, sub)
	}
	if m.Project != "widget-sub" {
		t.Errorf("Resolve(%s) = %s, want the NEARER marker widget-sub rather than the outer widget",
			alias, m.Project)
	}
}

// TestResolvePrefersTheMarkerAboveThePathAsObserved fixes the ordering between
// the two climbs, and it is the reason the physical climb is a FALLBACK rather
// than a replacement. The alias sits inside one registered project and points
// into another's subdirectory; the locator is the path that was observed, so
// the project the observed path is under is the answer.
func TestResolvePrefersTheMarkerAboveThePathAsObserved(t *testing.T) {
	base := t.TempDir()
	host := filepath.Join(base, "host")
	elsewhere := filepath.Join(base, "elsewhere", "sub")
	if err := os.MkdirAll(elsewhere, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if err := os.MkdirAll(host, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if _, err := registry.Register(registry.Registration{
		Dir: host, Client: "acme", Project: "host",
	}); err != nil {
		t.Fatalf("Register(host) = %v", err)
	}
	if _, err := registry.Register(registry.Registration{
		Dir: filepath.Dir(elsewhere), Client: "acme", Project: "elsewhere",
	}); err != nil {
		t.Fatalf("Register(elsewhere) = %v", err)
	}
	alias := filepath.Join(host, "alias")
	if err := os.Symlink(elsewhere, alias); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	m, found, err := registry.Resolve(alias)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", alias, err)
	}
	if !found {
		t.Fatalf("Resolve(%s) found = false, want host", alias)
	}
	if m.Project != "host" {
		t.Errorf("Resolve(%s) = %s, want host: the lexical climb answers first, "+
			"because the locator is the path that was observed", alias, m.Project)
	}
}

// TestResolveReturnsACleanNotFoundForABrokenSymlink and its cycle counterpart
// guard the fallback's failure mode rather than its success. A path that cannot
// be resolved physically is not an unknown marker — the lexical climb has
// already answered for every directory it could read, and surfaced any
// permission error itself — so a second spelling that does not exist is simply
// no second spelling, not an error and not a hang.
func TestResolveReturnsACleanNotFoundForABrokenSymlink(t *testing.T) {
	base := t.TempDir()
	alias := filepath.Join(base, "alias")
	if err := os.Symlink(filepath.Join(base, "was-never-here"), alias); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	_, found, err := registry.Resolve(alias)
	if err != nil {
		t.Fatalf("Resolve(a broken symlink) = %v, want a clean not-found", err)
	}
	if found {
		t.Fatal("Resolve(a broken symlink) found = true, want false")
	}
}

// TestResolveTerminatesOnASymlinkCycle pins what a cycle does, which is NOT
// what the broken link does and is deliberately not being changed here.
//
// readMarker reads a/.timetattle before anything symlink-aware runs, gets
// ELOOP, and surfaces it: the package's standing policy is that a directory it
// could not read is unknown rather than observed absent, and ELOOP arrives
// through exactly that door. The property this guards is that Resolve
// TERMINATES and does not attribute — the fallback added for this task cannot
// introduce a loop, because filepath.EvalSymlinks has its own link budget and
// fails rather than spinning, and it is never reached here anyway.
//
// Whether ELOOP deserves the unknown-marker treatment was left open here, with
// the note that it had a real cost: internal/store.ResolveProjects aborted the
// entire re-attribution transaction on any resolver error, so one cyclic
// locator un-attributed the whole run. Task IYXY1 settled it in the CALLER,
// which was the wrong half — the pass now skips, counts and names the locator
// and attributes everything else — so this policy is unchanged and the cost of
// it is now one locator. cmd/timetattle's
// TestResolveProjectsSurvivesARealSymlinkCycleAndAttributesTheRest is the other
// end of the same cycle.
func TestResolveTerminatesOnASymlinkCycle(t *testing.T) {
	base := t.TempDir()
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	_, found, err := registry.Resolve(a)
	if found {
		t.Fatal("Resolve(a symlink cycle) found = true; a cycle is not a project")
	}
	if err == nil {
		t.Fatal("Resolve(a symlink cycle) = nil error; ELOOP leaves the directory unreadable, " +
			"and this package reports an unreadable directory rather than calling it absent")
	}
}

// TestResolveThroughASymlinkIntoAGitRepositoryAgreesWithTheGitFallback
// reconciles the two mechanisms that can answer for the same path, which is a
// standing requirement here rather than a nicety.
//
// git resolves its working directory physically, so gitCommonRoot already
// returns the real repository root for a path spelled through a link, and this
// case therefore resolved even before the physical climb existed. That is worth
// stating because it CORRECTS the reachability argument on task jph7Z: an alias
// into a registered git repository was never broken. The gap is narrower than
// the task claimed — a registered directory that is not a git repository, or a
// marker below the repository root, per the test that follows.
func TestResolveThroughASymlinkIntoAGitRepositoryAgreesWithTheGitFallback(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sub := filepath.Join(repo, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	runGit(t, repo, "init", "-q")
	if _, err := registry.Register(registry.Registration{
		Dir: repo, Client: "acme", Project: "widget",
	}); err != nil {
		t.Fatalf("Register(repo) = %v", err)
	}
	link := filepath.Join(base, "into-repo")
	if err := os.Symlink(sub, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	m, found, err := registry.Resolve(link)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", link, err)
	}
	if !found || m.Project != "widget" {
		t.Errorf("Resolve(%s) = %v / found=%v, want widget", link, m, found)
	}
}

// TestResolveThroughASymlinkPrefersAMarkerBelowTheGitRoot is the case the git
// fallback cannot get right, and the reason the physical climb has to run
// BEFORE it rather than after it.
//
// gitCommonRoot answers with the repository ROOT and climb then walks UPWARD
// from there, so a marker sitting between the root and the link's target is
// unreachable that way: the fallback does not merely fail, it returns the outer
// project confidently. Attributing work to the wrong project is worse than
// attributing it to none, so the physical climb — which preserves the
// nearest-marker rule — has to get there first.
func TestResolveThroughASymlinkPrefersAMarkerBelowTheGitRoot(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	sub := filepath.Join(repo, "sub")
	deeper := filepath.Join(sub, "deeper")
	if err := os.MkdirAll(deeper, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	runGit(t, repo, "init", "-q")
	if _, err := registry.Register(registry.Registration{
		Dir: repo, Client: "acme", Project: "outer",
	}); err != nil {
		t.Fatalf("Register(repo) = %v", err)
	}
	if _, err := registry.Register(registry.Registration{
		Dir: sub, Client: "acme", Project: "inner",
	}); err != nil {
		t.Fatalf("Register(sub) = %v", err)
	}
	link := filepath.Join(base, "into-deeper")
	if err := os.Symlink(deeper, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	m, found, err := registry.Resolve(link)
	if err != nil {
		t.Fatalf("Resolve(%s) = %v", link, err)
	}
	if !found {
		t.Fatalf("Resolve(%s) found = false, want inner", link)
	}
	if m.Project != "inner" {
		t.Errorf("Resolve(%s) = %s, want inner; the git fallback climbs UP from the repository "+
			"root and can only ever reach the outer marker", link, m.Project)
	}
}
