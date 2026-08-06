package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// setupGitTestHome points HOME at an empty, disposable directory for the
// duration of one test, and disables system-wide config. Without it, git
// commands invoked here read this machine's real ~/.gitconfig, which makes
// tests nondeterministic across machines at best and, on this project's
// sandboxed agent environments, outright fails ("Operation not permitted") —
// matching internal/gitlog/gitlog_test.go's identically named helper.
func setupGitTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// testGitRepoEnvPrefixes is an INDEPENDENT copy of internal/gitenv.Prefixes,
// used only to build the fixture repositories below. Using loadCommits's own
// gitenv-based stripping to build the fixtures would let a bug there also
// corrupt fixture setup, masking the very failure this suite exists to
// catch — the same reasoning internal/gitlog/gitlog_test.go's and
// scripts/confidential/main_test.go's identically named copies document.
var testGitRepoEnvPrefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

func runGit(t *testing.T, dir string, env []string, args ...string) string {
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
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, nil, "init", "-q")
	return dir
}

func commitAt(t *testing.T, dir string, at time.Time, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("x\n"), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	runGit(t, dir, nil, "add", name)
	env := []string{
		"GIT_AUTHOR_NAME=timetattle-test", "GIT_AUTHOR_EMAIL=timetattle-test@example.com",
		"GIT_COMMITTER_NAME=timetattle-test", "GIT_COMMITTER_EMAIL=timetattle-test@example.com",
		"GIT_AUTHOR_DATE=" + at.Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + at.Format(time.RFC3339),
	}
	runGit(t, dir, env, "commit", "-q", "-m", "commit "+name)
}

// TestLoadCommitsDoesNotLeakGitDirIntoADecoyRepo is the mutation-tested
// regression guard for the same bug fixed at the other four git-shelling
// call sites in this repository (internal/gitlog, internal/registry,
// scripts/confidential, scripts/pseudonymize): git exports GIT_DIR,
// GIT_WORK_TREE and friends into every hook and subprocess it runs, and
// those variables silently override -C. loadCommits was the one call site
// that had never stripped them at all -- found during the audit that added
// this test, not assumed clean. Without the strip, `git -C repo log`
// resolves against whichever repository set GIT_DIR, not repo, so a day
// with real commits in the DECOY would be misreported for the target
// instead.
func TestLoadCommitsDoesNotLeakGitDirIntoADecoyRepo(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)

	d := day{
		start: time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		end:   time.Date(2026, 6, 2, 0, 0, 0, 0, time.UTC),
	}

	decoy := initRepo(t)
	commitAt(t, decoy, time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), "decoy.txt")

	target := initRepo(t)
	// target has no commits in the window at all: a leak would surface
	// decoy's commit here instead of reporting zero.

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	got := loadCommits(target, d)
	if len(got) != 0 {
		t.Fatalf("loadCommits(target) = %d commits with a leaked GIT_DIR, want 0; "+
			"a leaked GIT_DIR pointed this at the decoy repo instead of -C's target", len(got))
	}
}
