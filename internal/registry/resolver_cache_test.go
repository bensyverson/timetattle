package registry_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/gitenv"
	"github.com/bensyverson/timetattle/internal/registry"
)

func mkdirs(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func bless(t *testing.T, dir, project string) {
	t.Helper()
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "acme", Project: project}); err != nil {
		t.Fatal(err)
	}
}

// onceEach fails for any path the Resolver asked the filesystem about twice.
func onceEach(t *testing.T, asked askLog) {
	t.Helper()
	for kind, paths := range asked {
		for path, n := range paths {
			if n != 1 {
				t.Errorf("%s of %s was asked %d times, want 1", kind, path, n)
			}
		}
	}
}

func TestResolverReadsOneMarkerForManyLocatorsInOneDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "project", "renders")
	mkdirs(t, dir)
	bless(t, filepath.Join(root, "project"), "holiday")

	asked := askLog{}
	r := &registry.Resolver{Observe: asked.observe}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		locator := filepath.Join(dir, name)
		m, found, err := r.ResolveAmong(dir, locator, nil)
		if err != nil || !found || m.Project != "holiday" {
			t.Fatalf("ResolveAmong(%s) = %v, %v, %v, want holiday", locator, m, found, err)
		}
	}
	if got := asked[registry.LookupMarker][dir]; got != 1 {
		t.Errorf("the marker of the shared directory was read %d times, want 1", got)
	}
	onceEach(t, asked)
}

func TestResolverAsksOnceAboutADirectoryNothingClaims(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	mkdirs(t, dir)

	asked := askLog{}
	r := &registry.Resolver{Observe: asked.observe}
	for range 5 {
		if !r.IsDir(dir) {
			t.Fatalf("IsDir(%s) = false", dir)
		}
		if _, found, err := r.Resolve(dir); err != nil || found {
			t.Fatalf("Resolve = %v, %v, want unattributed", found, err)
		}
	}
	for _, kind := range []registry.Lookup{
		registry.LookupStat, registry.LookupPhysicalPath, registry.LookupGitRoot,
	} {
		if got := asked[kind][dir]; got != 1 {
			t.Errorf("%s of the directory was asked %d times, want 1", kind, got)
		}
	}
	onceEach(t, asked)
}

// An unreadable directory has to stay unreadable for every locator under it,
// never turn into "unattributed" once its answer is remembered.
func TestResolverRemembersThatADirectoryCouldNotBeRead(t *testing.T) {
	root := t.TempDir()
	a, b := filepath.Join(root, "a"), filepath.Join(root, "b")
	if err := os.Symlink(a, b); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(b, a); err != nil {
		t.Fatal(err)
	}

	asked := askLog{}
	r := &registry.Resolver{Observe: asked.observe}
	for range 3 {
		if _, found, err := r.Resolve(a); err == nil || found {
			t.Fatalf("Resolve(a symlink cycle) = %v, %v, want an error", found, err)
		}
	}
	onceEach(t, asked)
}

func TestResolverWithNoCacheAsksEveryTime(t *testing.T) {
	dir := t.TempDir()
	asked := askLog{}
	r := &registry.Resolver{Caching: registry.NoCache, Observe: asked.observe}
	for range 3 {
		r.IsDir(dir)
		if _, _, err := r.Resolve(dir); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []registry.Lookup{
		registry.LookupStat, registry.LookupMarker, registry.LookupGitRoot,
	} {
		if got := asked[kind][dir]; got != 3 {
			t.Errorf("%s was asked %d times, want 3", kind, got)
		}
	}
}

// One Resolver, asked about a whole tree of awkward paths in either order, has
// to answer each exactly as a fresh lookup does. A cache keyed carelessly gives
// one spelling of a directory another spelling's answer.
func TestResolverAnswersEveryPathAsAFreshLookupDoes(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	inner := filepath.Join(outer, "inner")
	sub := filepath.Join(inner, "sub")
	plain := filepath.Join(root, "plain", "deeper")
	repo := filepath.Join(root, "repo")
	mkdirs(t, sub, plain, repo, filepath.Join(root, "links", "below"))
	bless(t, outer, "outer-project")
	bless(t, inner, "inner-project")

	toSub := filepath.Join(root, "links", "to-sub")
	toPlain := filepath.Join(outer, "to-plain")
	for link, target := range map[string]string{toSub: sub, toPlain: plain} {
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
	}

	git := func(args ...string) {
		t.Helper()
		cmd := gitenv.Command("git", append([]string{"-C", repo}, args...)...)
		cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	worktree := filepath.Join(root, "worktree")
	git("init", "--quiet")
	git("-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "--quiet", "--allow-empty", "-m", "first")
	git("worktree", "add", "--quiet", worktree)
	bless(t, repo, "repo-project")

	file := filepath.Join(sub, "render.motn")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	paths := []string{
		root, outer, inner, sub, toSub, filepath.Join(toSub, "gone"), toPlain,
		plain, filepath.Dir(plain), repo, worktree, filepath.Join(worktree, "gone"),
		filepath.Join(root, "vanished", "deeper"), filepath.Join(outer, "vanished"), file,
	}

	type answer struct {
		project, path, err string
		found              bool
	}
	ask := func(r *registry.Resolver, p string) answer {
		m, found, err := r.Resolve(p)
		a := answer{found: found}
		if m != nil {
			a.project, a.path = m.Project, m.Path
		}
		if err != nil {
			a.err = err.Error()
		}
		return a
	}

	forward, backward := &registry.Resolver{}, &registry.Resolver{}
	check := func(r *registry.Resolver, p string) {
		t.Helper()
		want := ask(&registry.Resolver{Caching: registry.NoCache}, p)
		for range 2 {
			if got := ask(r, p); got != want {
				t.Errorf("Resolve(%s) through a shared Resolver = %+v, want %+v", p, got, want)
			}
		}
		_, err := os.Stat(p)
		isDir := err == nil && p != file
		if got := r.IsDir(p); got != isDir {
			t.Errorf("IsDir(%s) = %v, want %v", p, got, isDir)
		}
	}
	for i := range paths {
		check(forward, paths[i])
		check(backward, paths[len(paths)-1-i])
	}
}

// git cannot change into a path that is not a directory, so the process would
// only be spawned to fail.
func TestResolverRunsNoGitForAPathThatIsNotADirectory(t *testing.T) {
	asked := askLog{}
	r := &registry.Resolver{Observe: asked.observe}
	if _, found, err := r.Resolve(filepath.Join(t.TempDir(), "vanished", "deeper")); err != nil || found {
		t.Fatalf("Resolve = %v, %v, want unattributed", found, err)
	}
	if got := asked.total(registry.LookupGitRoot); got != 0 {
		t.Errorf("git was run %d times, want 0", got)
	}
}
