package registry_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/gitenv"
	"github.com/bensyverson/timetattle/internal/registry"
)

// registeredRepoWorktree is a worktree outside its registered repository, so
// only git can say which project a path in it belongs to.
func registeredRepoWorktree(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo, worktree := filepath.Join(root, "repo"), filepath.Join(root, "worktree")
	mkdirs(t, repo)
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "first"},
		{"worktree", "add", "--quiet", worktree},
	} {
		cmd := gitenv.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	bless(t, repo, "repo-project")
	return worktree
}

// shimGit puts a script named git first on PATH. It runs onFirst the first time
// and the real git every time after.
func shimGit(t *testing.T, onFirst string) {
	t.Helper()
	real, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	bin := t.TempDir()
	used := filepath.Join(bin, "used")
	script := fmt.Sprintf("#!/bin/sh\nif [ ! -e %q ]; then\n  : > %q\n  %s\nfi\nexec %q \"$@\"\n",
		used, used, onFirst, real)
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// A git that failed said nothing about the directory, so the next locator in it
// has to ask again.
func TestResolverDoesNotRememberAGitThatFailed(t *testing.T) {
	for name, failure := range map[string]string{
		"exits with another error": `echo "fatal: unable to read the index" >&2; exit 128`,
		"exits without a word":     `exit 1`,
		"is killed":                `kill -KILL $$`,
		"is killed mid-message":    `echo "fatal: not a git repository" >&2; kill -KILL $$`,
	} {
		t.Run(name, func(t *testing.T) {
			worktree := registeredRepoWorktree(t)
			shimGit(t, failure)

			asked := askLog{}
			r := &registry.Resolver{Observe: asked.observe}
			if _, found, err := r.Resolve(worktree); err != nil || found {
				t.Fatalf("Resolve while git fails = %v, %v, want unattributed", found, err)
			}
			for range 3 {
				m, found, err := r.Resolve(worktree)
				if err != nil || !found || m.Project != "repo-project" {
					t.Fatalf("Resolve after git recovers = %v, %v, %v, want repo-project", m, found, err)
				}
			}
			if got := asked[registry.LookupGitRoot][worktree]; got != 2 {
				t.Errorf("git was run %d times, want 2: the failure and one answer", got)
			}
		})
	}
}

func TestResolverDoesNotRememberThatGitCouldNotBeRun(t *testing.T) {
	worktree := registeredRepoWorktree(t)
	path := os.Getenv("PATH")
	r := &registry.Resolver{}

	t.Setenv("PATH", t.TempDir())
	if _, found, err := r.Resolve(worktree); err != nil || found {
		t.Fatalf("Resolve with no git = %v, %v, want unattributed", found, err)
	}
	t.Setenv("PATH", path)
	if m, found, err := r.Resolve(worktree); err != nil || !found || m.Project != "repo-project" {
		t.Fatalf("Resolve once git is back = %v, %v, %v, want repo-project", m, found, err)
	}
}

// Git translates its messages, and the verdict is recognized by its wording.
func TestResolverRecognizesNotARepositoryUnderATranslatingLocale(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	script := "#!/bin/sh\n" +
		`if [ "$LC_ALL" = C ]; then echo "fatal: not a git repository (or any of the parent directories): .git" >&2` + "\n" +
		`else echo "Schwerwiegend: Kein Git-Repository" >&2; fi` + "\nexit 128\n"
	if err := os.WriteFile(filepath.Join(bin, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("LC_ALL", "de_DE.UTF-8")

	asked := askLog{}
	r := &registry.Resolver{Observe: asked.observe}
	for range 3 {
		if _, found, err := r.Resolve(dir); err != nil || found {
			t.Fatalf("Resolve = %v, %v, want unattributed", found, err)
		}
	}
	if got := asked[registry.LookupGitRoot][dir]; got != 1 {
		t.Errorf("git was run %d times, want 1", got)
	}
}
