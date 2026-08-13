package gitenv_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/gitenv"
)

// setupGitTestHome points HOME at an empty, disposable directory for the
// duration of one test, and disables system-wide config. Without it, git
// commands invoked here read this machine's real ~/.gitconfig, which makes
// tests nondeterministic across machines at best and, on this project's
// sandboxed agent environments, outright fails ("Operation not permitted") —
// matching internal/gitlog/gitlog_test.go's and
// internal/registry/worktree_test.go's identically named helper.
func setupGitTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "timetattle-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "timetattle-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "timetattle-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "timetattle-test@example.com")
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// testGitRepoEnvPrefixes is an INDEPENDENT copy of gitenv.Prefixes, used only
// to build test fixtures below. Using gitenv.Strip itself to build fixtures
// would let a bug in the package under test also corrupt fixture setup,
// which could mask the very failure this suite exists to catch — the same
// reasoning internal/gitlog/gitlog_test.go's and
// scripts/confidential/main_test.go's identically named copies document.
var testGitRepoEnvPrefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	for _, kv := range os.Environ() {
		blocked := false
		for _, prefix := range testGitRepoEnvPrefixes {
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
	return string(out)
}

func initGitDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "-q")
	return dir
}

// TestStripRemovesGitRepoEnv is the unit-level check: every variable git
// exports to name a specific repository is gone, and everything else
// survives untouched.
func TestStripRemovesGitRepoEnv(t *testing.T) {
	in := []string{
		"PATH=/usr/bin",
		"GIT_DIR=/somewhere/.git",
		"GIT_WORK_TREE=/somewhere",
		"GIT_INDEX_FILE=/somewhere/.git/index",
		"GIT_COMMON_DIR=/somewhere/.git",
		"GIT_OBJECT_DIRECTORY=/somewhere/.git/objects",
		"GIT_ALTERNATE_OBJECT_DIRECTORIES=/other",
		"GIT_PREFIX=sub/",
		"GIT_NAMESPACE=ns",
		"HOME=/home/you",
	}
	out := gitenv.Strip(in)
	want := []string{"PATH=/usr/bin", "HOME=/home/you"}
	if len(out) != len(want) {
		t.Fatalf("Strip(%v) = %v, want %v", in, out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("Strip(%v) = %v, want %v", in, out, want)
		}
	}
}

// TestCommandDoesNotLeakGitDirIntoADecoyRepo is the mutation-tested
// regression guard for the bug this package exists to fix once and for all:
// git exports GIT_DIR and friends into every hook and subprocess it runs,
// and those variables silently override -C. With Strip's call removed from
// Command, `git -C dir init` silently reinitializes the DECOY's .git in
// place instead of creating dir/.git — exactly the failure that corrupted
// this repository's own .gitignore and left a stray tracked.md at the root,
// twice, on 2026-08-02.
func TestCommandDoesNotLeakGitDirIntoADecoyRepo(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)

	decoy := initGitDir(t)
	dir := t.TempDir()

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	cmd := gitenv.Command("git", "-C", dir, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gitenv.Command init: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("dir has no .git after `git -C dir init`: %v -- "+
			"a leaked GIT_DIR pointed init at the decoy repo instead of dir", err)
	}
}

// TestCommandContextDoesNotLeakGitDirIntoADecoyRepo is CommandContext's
// identical proof — internal/gitlog's runGit needs the context-aware
// constructor, so it needs its own guard rather than inheriting Command's.
func TestCommandContextDoesNotLeakGitDirIntoADecoyRepo(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)

	decoy := initGitDir(t)
	dir := t.TempDir()

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	cmd := gitenv.CommandContext(context.Background(), "git", "-C", dir, "init", "-q")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("gitenv.CommandContext init: %v\n%s", err, out)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		t.Fatalf("dir has no .git after `git -C dir init`: %v -- "+
			"a leaked GIT_DIR pointed init at the decoy repo instead of dir", err)
	}
}
