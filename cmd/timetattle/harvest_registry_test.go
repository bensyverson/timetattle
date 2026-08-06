package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/registry"
)

// requireGit skips a test on a machine with no git binary on PATH, mirroring
// internal/registry/worktree_test.go's helper of the same name.
func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// gitRepoEnvPrefixes are the variables git exports into every hook it runs,
// naming the repository that invoked it — the same list
// internal/registry/worktree_test.go strips for the identical reason. This
// suite runs as part of this project's own pre-commit hook, whose "go test"
// step is a git hook execution; a git command spawned from inside these
// tests inherits them, and without stripping them a "git commit" against a
// throwaway fixture repository below instead recurses into the repository
// running the suite, firing its pre-commit hook a second, nested time from
// the wrong working directory. Caught only by running this suite through
// `git commit` rather than invoking it directly.
var gitRepoEnvPrefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

// runGit runs a git command in dir with gitRepoEnvPrefixes stripped from the
// inherited environment, mirroring internal/registry/worktree_test.go's
// helper of the same name.
func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		blocked := false
		for _, prefix := range gitRepoEnvPrefixes {
			if strings.HasPrefix(kv, prefix) {
				blocked = true
				break
			}
		}
		if !blocked {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
}

// initGitRepo creates a minimal, real git repository at dir with one commit,
// isolated from this machine's own gitconfig by TestMain's HOME redirection
// plus GIT_CONFIG_NOSYSTEM, the same combination
// internal/registry/worktree_test.go's setupGitTestHome uses.
func initGitRepo(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "timetattle-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "timetattle-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "timetattle-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "timetattle-test@example.com")

	runGit(t, dir, "init", "-q")
	runGit(t, dir, "commit", "-q", "--allow-empty", "-m", "init")
}

// marker builds a *registry.Marker fixture directly, without going through
// Register, so these tests exercise fsobserveIngestion/gitlogIngestions in
// isolation from internal/registry's own index and marker-file mechanics,
// which are covered by internal/registry's own test suite.
func marker(t *testing.T, dir, client, project string) *registry.Marker {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("filepath.Abs(%s): %v", dir, err)
	}
	return &registry.Marker{Version: registry.Version, Client: client, Project: project, Path: abs}
}

// TestFsobserveIngestionIsNilWhenNothingIsRegistered guards against
// harvest recording a wasted, empty-input harvest_run row when no directory
// is registered at all — the same tolerance -filedays-roots gets when
// nothing is configured.
func TestFsobserveIngestionIsNilWhenNothingIsRegistered(t *testing.T) {
	if got := fsobserveIngestion(nil); got != nil {
		t.Errorf("fsobserveIngestion(nil) = %+v, want nil", got)
	}
}

// TestFsobserveIngestionWalksEveryRegisteredDirectory is the acceptance test
// for criterion 1r8: given more than one registered directory, the single
// Walker built from them has to actually find files sitting under each one,
// not just the first.
func TestFsobserveIngestionWalksEveryRegisteredDirectory(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	if err := os.WriteFile(filepath.Join(dirA, "composition.motn"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dirB, "render.blend"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	ing := fsobserveIngestion([]*registry.Marker{marker(t, dirA, "ACORN", "P1"), marker(t, dirB, "Acme", "P2")})
	if ing == nil {
		t.Fatal("fsobserveIngestion() = nil, want an ingestion covering both directories")
	}
	if ing.input == "" {
		t.Error("fsobserveIngestion().input is empty; a harvest_run must record what it read")
	}

	events, err := ing.source.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	var sawA, sawB bool
	for _, e := range events {
		if filepath.Dir(e.Locator) == dirA {
			sawA = true
		}
		if filepath.Dir(e.Locator) == dirB {
			sawB = true
		}
	}
	if !sawA || !sawB {
		t.Errorf("Collect() saw dirA=%v dirB=%v, want both walked; events: %+v", sawA, sawB, events)
	}
}

// TestGitlogIngestionsOnlyIncludesRegisteredDirectoriesThatAreGitRepos is
// the acceptance test for criterion nsW: a registered Motion asset folder
// (no .git) must not produce a gitlog ingestion, a registered git repo must
// produce exactly Commits and Reflog for that repo, and a registered
// directory that does not exist on disk at all must not crash the wiring.
func TestGitlogIngestionsOnlyIncludesRegisteredDirectoriesThatAreGitRepos(t *testing.T) {
	requireGit(t)

	repo := t.TempDir()
	initGitRepo(t, repo)

	plainDir := t.TempDir() // registered, but never a git repo — a Motion vault stand-in

	missing := filepath.Join(t.TempDir(), "does-not-exist")

	markers := []*registry.Marker{
		marker(t, repo, "ACORN", "Repo Project"),
		marker(t, plainDir, "ACORN", "Asset Folder"),
		marker(t, missing, "ACORN", "Gone"),
	}

	got := gitlogIngestions(markers)
	if len(got) != 2 {
		t.Fatalf("gitlogIngestions() = %d ingestions, want 2 (Commits + Reflog for the one real repo): %+v", len(got), got)
	}

	ids := make([]string, len(got))
	for i, ing := range got {
		ids[i] = ing.source.ID()
		if ing.input != repo {
			t.Errorf("ingestion %d input = %q, want %q", i, ing.input, repo)
		}
	}
	sort.Strings(ids)
	want := []string{gitlog.CommitsID, gitlog.ReflogID}
	sort.Strings(want)
	if ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("gitlogIngestions() source IDs = %v, want %v", ids, want)
	}
}

// TestGitlogIngestionsIsEmptyWhenNoRegisteredDirectoryIsAGitRepo covers the
// all-Motion-vault case directly, so an empty result is distinguishable in
// the test suite from "the function panicked" or "isGitRepo is inverted".
func TestGitlogIngestionsIsEmptyWhenNoRegisteredDirectoryIsAGitRepo(t *testing.T) {
	dir := t.TempDir()
	got := gitlogIngestions([]*registry.Marker{marker(t, dir, "ACORN", "Asset Folder")})
	if len(got) != 0 {
		t.Errorf("gitlogIngestions() = %d ingestions for a non-repo directory, want 0: %+v", len(got), got)
	}
}

// TestIsGitRepoDetectsAWorktreesFileShapedDotGit checks the .git-presence
// test does not assume .git is always a directory: a linked git worktree —
// this project's own agent worktrees among them — has a .git that is a
// plain FILE containing a gitdir pointer, not a directory.
func TestIsGitRepoDetectsAWorktreesFileShapedDotGit(t *testing.T) {
	requireGit(t)
	main := t.TempDir()
	initGitRepo(t, main)

	worktree := filepath.Join(t.TempDir(), "wt")
	runGit(t, main, "worktree", "add", "-q", "-b", "wt-branch", worktree)

	info, err := os.Lstat(filepath.Join(worktree, ".git"))
	if err != nil || info.IsDir() {
		t.Fatalf("test fixture assumption broken: %s/.git is not a plain file (err=%v, isDir=%v)", worktree, err, info != nil && info.IsDir())
	}

	if !isGitRepo(worktree) {
		t.Errorf("isGitRepo(%s) = false, want true for a worktree's file-shaped .git", worktree)
	}
}

// TestIsGitRepoIsFalseForAPlainDirectory is the negative case that
// TestGitlogIngestionsIsEmptyWhenNoRegisteredDirectoryIsAGitRepo relies on
// indirectly; asserted directly here so a future change to isGitRepo's
// definition of "present" fails close to the code that changed, not three
// layers up in a harvest-shaped test.
func TestIsGitRepoIsFalseForAPlainDirectory(t *testing.T) {
	if isGitRepo(t.TempDir()) {
		t.Error("isGitRepo() = true for a directory with no .git at all")
	}
}

// TestHarvestReadsEveryRegisteredDirectoryThroughTheIndexFlag is the
// end-to-end acceptance test for criteria 1r8 and nsW together: given a real
// -index naming a registered git repository and a registered plain
// directory, a full 'timetattle harvest' run must record harvest_run rows
// for fs-observations (covering files in both directories) and for
// gitlog-commits/gitlog-reflog (covering only the repository), with events
// actually stored for each.
func TestHarvestReadsEveryRegisteredDirectoryThroughTheIndexFlag(t *testing.T) {
	requireGit(t)

	repo := t.TempDir()
	initGitRepo(t, repo)
	if err := os.WriteFile(filepath.Join(repo, "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	assets := t.TempDir()
	if err := os.WriteFile(filepath.Join(assets, "composition.motn"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	mRepo, err := registry.Register(registry.Registration{Dir: repo, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register(repo) = %v", err)
	}
	mAssets, err := registry.Register(registry.Registration{Dir: assets, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register(assets) = %v", err)
	}
	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, mRepo.Path); err != nil {
		t.Fatalf("RecordInIndex(repo) = %v", err)
	}
	if err := registry.RecordInIndex(idx, mAssets.Path); err != nil {
		t.Fatalf("RecordInIndex(assets) = %v", err)
	}

	dir := t.TempDir()
	src := populatedSnapshot(t)
	storePath := filepath.Join(dir, "store", "timetattle.db")
	err = harvest([]string{
		"-source", src,
		"-dir", filepath.Join(dir, "snapshots"),
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-motion-vault", filepath.Join(dir, "no-such-vault"),
		"-store", storePath,
		"-index", idx,
		"-quiet",
	})
	if err != nil {
		// The live Biome source needs Full Disk Access and is not this
		// test's business; every other phase, including the two under
		// test, must still have completed.
		t.Logf("harvest reported: %v", err)
	}

	recorded := bySource(t, harvestRuns(t, storePath))
	fsRun, ok := recorded[fsobserveSourceID]
	if !ok {
		t.Fatalf("no harvest_run row for %s; fsobserve was not wired into harvest", fsobserveSourceID)
	}
	if fsRun.status != "ok" {
		t.Errorf("%s: status %q, error %q", fsobserveSourceID, fsRun.status, fsRun.failure.String)
	}
	if fsRun.added == 0 {
		t.Errorf("%s stored no events, but %s and %s both hold files", fsobserveSourceID, repo, assets)
	}

	for _, id := range []string{gitlog.CommitsID, gitlog.ReflogID} {
		r, ok := recorded[id]
		if !ok {
			t.Fatalf("no harvest_run row for %s; gitlog was not wired into harvest", id)
		}
		if r.status != "ok" {
			t.Errorf("%s: status %q, error %q", id, r.status, r.failure.String)
		}
		if r.input != repo {
			t.Errorf("%s recorded input %q, want the registered repo %q", id, r.input, repo)
		}
		if r.added == 0 {
			t.Errorf("%s stored no events for a repo with a real commit", id)
		}
	}

	if _, ok := recorded[gitlog.CommitsID]; ok {
		if r := recorded[gitlog.CommitsID]; r.input == assets {
			t.Errorf("%s read the non-repo assets directory %q; gitlog must skip registered directories with no .git", gitlog.CommitsID, assets)
		}
	}
}

// fsobserveSourceID is spelled out rather than imported, matching how
// harvest_test.go already names knowledgeC and biome's source IDs directly:
// a renamed identifier should fail this test rather than travel through
// silently.
const fsobserveSourceID = "fs-observations"
