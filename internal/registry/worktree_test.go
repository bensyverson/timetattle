package registry_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// setupGitTestHome points HOME at an empty, disposable directory for the
// duration of one test, and disables system-wide config. Without it, git
// commands invoked here — and the git-common-dir lookup inside Resolve
// itself, which inherits the same process environment — read this machine's
// real ~/.gitconfig. That makes tests nondeterministic across machines at
// best, and on this project's sandboxed agent environments reading it
// outright fails ("Operation not permitted"), which is worse: git treats an
// inaccessible (as opposed to absent) config file as fatal, so a test could
// look like a resolver bug when it was actually a sandbox permission.
func setupGitTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "timetattle-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "timetattle-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "timetattle-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "timetattle-test@example.com")
}

// gitRepoEnvPrefixes are the variables git exports into every hook it runs,
// naming the repository that invoked it. This suite runs as part of this
// project's own pre-commit hook, whose "go test" step is a git hook
// execution; a git command spawned from inside it inherits these and they
// silently override -C / cmd.Dir, so a "git commit" meant for a throwaway
// temp repository below instead operates on the repository running the
// suite — which reproduces as this project's own pre-commit hook firing a
// second, nested time, from the wrong working directory. Caught only by
// running the suite through `git commit` rather than invoking it directly,
// which is exactly why it is guarded here rather than assumed away.
var gitRepoEnvPrefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

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

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// TestResolveFromANestedAgentWorktreeFindsItsParentRepoMarker reproduces this
// project's own concrete case: Claude Code agent worktrees live under
// <repo>/.claude/worktrees/agent-*, are real repositories with their own
// .git file, and must attribute to the repo they were forked from — task
// muT3W cites roughly 48 of them under one repo alone.
func TestResolveFromANestedAgentWorktreeFindsItsParentRepoMarker(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := t.TempDir()

	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	if _, err := registry.Register(registry.Registration{Dir: repo, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register(repo) = %v", err)
	}

	worktree := filepath.Join(repo, ".claude", "worktrees", "agent-x")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	runGit(t, repo, "worktree", "add", "-q", "-b", "agent-x", worktree)

	m, found, err := registry.Resolve(worktree)
	if err != nil {
		t.Fatalf("Resolve(worktree) = %v", err)
	}
	if !found {
		t.Fatal("Resolve(worktree) found = false, want true; a worktree nested under its parent repo " +
			"should resolve to that repo's marker even with no marker of its own")
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Errorf("Resolve(worktree) = %s / %s, want ACORN / Holiday Campaign", m.Client, m.Project)
	}
}

// TestResolveFromASiblingWorktreeFindsItsParentRepoMarker covers the layout
// plain upward directory climbing cannot solve on its own: a worktree that
// is not a descendant of the repo it was forked from. The settled design
// explicitly calls this out — "under the root, a sibling, or /tmp" — as the
// reason git-common-dir normalisation is needed at all, rather than only
// walking parent directories.
func TestResolveFromASiblingWorktreeFindsItsParentRepoMarker(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}

	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	if _, err := registry.Register(registry.Registration{Dir: repo, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register(repo) = %v", err)
	}

	worktree := filepath.Join(base, "sibling-worktree")
	runGit(t, repo, "worktree", "add", "-q", "-b", "sibling", worktree)

	m, found, err := registry.Resolve(worktree)
	if err != nil {
		t.Fatalf("Resolve(worktree) = %v", err)
	}
	if !found {
		t.Fatal("Resolve(worktree) found = false, want true via git-common-dir normalisation; " +
			"plain directory climbing can never reach repo from here since it is not an ancestor")
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Errorf("Resolve(worktree) = %s / %s, want ACORN / Holiday Campaign", m.Client, m.Project)
	}
}

// TestResolvePrefersAMarkerRegisteredDirectlyInAWorktreeOverItsParent checks
// that redirecting worktrees to their parent is a fallback, not an
// override: a marker placed directly in the worktree is nearer and still
// wins, the same as any inner marker wins over an outer one.
func TestResolvePrefersAMarkerRegisteredDirectlyInAWorktreeOverItsParent(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := t.TempDir()

	runGit(t, repo, "init", "-q")
	runGit(t, repo, "commit", "-q", "--allow-empty", "-m", "init")

	if _, err := registry.Register(registry.Registration{Dir: repo, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register(repo) = %v", err)
	}

	worktree := filepath.Join(repo, ".claude", "worktrees", "agent-y")
	if err := os.MkdirAll(filepath.Dir(worktree), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	runGit(t, repo, "worktree", "add", "-q", "-b", "agent-y", worktree)

	if _, err := registry.Register(registry.Registration{Dir: worktree, Client: "Standalone", Project: "Side Project"}); err != nil {
		t.Fatalf("Register(worktree) = %v", err)
	}

	m, found, err := registry.Resolve(worktree)
	if err != nil || !found {
		t.Fatalf("Resolve(worktree) = %v, %v, %v", m, found, err)
	}
	if m.Client != "Standalone" || m.Project != "Side Project" {
		t.Errorf("Resolve(worktree) = %s / %s, want the worktree's own marker Standalone / Side Project", m.Client, m.Project)
	}
}

// TestResolveOnAPlainDirectoryWithNoGitStillResolves guards the other half
// of "directory-shaped, not git-shaped": most evidence sources (a Motion
// vault, a scratch render directory) are never git repositories at all, so
// resolution must not depend on a git command succeeding.
func TestResolveOnAPlainDirectoryWithNoGitStillResolves(t *testing.T) {
	dir := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	nested := filepath.Join(dir, "Composition A")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	m, found, err := registry.Resolve(nested)
	if err != nil || !found {
		t.Fatalf("Resolve() = %v, %v, %v", m, found, err)
	}
	if m.Client != "ACORN" {
		t.Errorf("Resolve() = %s, want ACORN", m.Client)
	}
}

// TestResolveIgnoresAnInheritedGitDir is the regression test for a real bug
// this suite tripped over: git exports GIT_DIR (and related variables) into
// every hook it runs, naming the repository that invoked the hook, and
// GIT_DIR silently overrides `-C` for any git command run afterwards — even
// pointing a plain, non-repository directory at somebody else's .git. A
// process that itself shells out to git while running inside one of this
// project's own hooks — this whole test suite, run via `git commit` rather
// than directly, is exactly that case — inherits it. Without the fix,
// Resolve's own git-common-dir lookup on an unrelated directory would
// resolve to whatever repository set GIT_DIR and inherit ITS marker, which
// is a misattribution, not merely a wrong answer to "is this a worktree".
func TestResolveIgnoresAnInheritedGitDir(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)

	other := t.TempDir()
	runGit(t, other, "init", "-q")
	runGit(t, other, "commit", "-q", "--allow-empty", "-m", "init")
	if _, err := registry.Register(registry.Registration{Dir: other, Client: "WrongClient", Project: "WrongProject"}); err != nil {
		t.Fatalf("Register(other) = %v", err)
	}

	// target is a plain directory with no git repository of its own and
	// nothing registered anywhere in its ancestry, so the only way it could
	// resolve to anything is by way of a leaked GIT_DIR.
	target := t.TempDir()

	// Simulates exactly what running inside another repository's git hook
	// does to a process's environment.
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))

	m, found, err := registry.Resolve(target)
	if err != nil {
		t.Fatalf("Resolve(target) = %v", err)
	}
	if found {
		t.Fatalf("Resolve(target) resolved to %s / %s via a leaked GIT_DIR pointing at an unrelated repository; "+
			"target is not part of any repository and should resolve to nothing", m.Client, m.Project)
	}
}
