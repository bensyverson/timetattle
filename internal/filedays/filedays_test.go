package filedays

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// touch creates a file with deterministic content and mtime, making the
// parent directories it needs along the way.
func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func TestScanCountsFilesByLocalModDay(t *testing.T) {
	root := t.TempDir()
	day1 := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	day2 := time.Date(2026, 7, 12, 3, 30, 0, 0, time.Local)
	touch(t, filepath.Join(root, "a.txt"), day1)
	touch(t, filepath.Join(root, "b.txt"), day1)
	touch(t, filepath.Join(root, "sub", "c.txt"), day2)

	got, err := Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	want := map[string]int{"2026-07-11": 2, "2026-07-12": 1}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Scan(%s) = %v, want %v", root, got, want)
	}
}

// TestScanExcludesDirectoriesWhole is the property scripts/filedays exists
// for: a raw scan sees a file inside an excluded directory and a filtered one
// does not, and the difference must be exactly that one file, not a partial
// or accidental match on a directory name that merely shares a prefix.
func TestScanExcludesDirectoriesWhole(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(root, "keep.txt"), day)
	touch(t, filepath.Join(root, ".git", "obj", "pack"), day)
	// worktrees-extra must survive an exclude of "worktrees": a substring
	// match would wrongly skip it too.
	touch(t, filepath.Join(root, "worktrees-extra", "d.txt"), day)

	raw, err := Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan(raw): %v", err)
	}
	if raw["2026-07-11"] != 3 {
		t.Fatalf("raw scan found %d files, want 3 (keep.txt, .git/obj/pack, worktrees-extra/d.txt)", raw["2026-07-11"])
	}

	filtered, err := Scan(root, []string{".git", "worktrees"})
	if err != nil {
		t.Fatalf("Scan(filtered): %v", err)
	}
	if filtered["2026-07-11"] != 2 {
		t.Fatalf("filtered scan found %d files, want 2 (.git excluded, worktrees-extra kept since it is not named exactly \"worktrees\")",
			filtered["2026-07-11"])
	}
}

func TestScanNonexistentRootErrors(t *testing.T) {
	_, err := Scan(filepath.Join(t.TempDir(), "does-not-exist"), nil)
	if err == nil {
		t.Fatal("Scan on a missing root returned no error")
	}
}

// TestTakeRecordsObservationTime is the criterion this package exists to
// satisfy: a count without the moment it was taken is meaningless, so Take
// must store one alongside the counts, and the counts it stores must be
// exactly what an independent Scan call reports for the same root.
func TestTakeRecordsObservationTime(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(root, "keep.txt"), day)
	touch(t, filepath.Join(root, ".git", "obj"), day)

	wantRaw, err := Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan(raw): %v", err)
	}
	wantFiltered, err := Scan(root, []string{".git"})
	if err != nil {
		t.Fatalf("Scan(filtered): %v", err)
	}

	dir := filepath.Join(t.TempDir(), "snapshots")
	now := time.Date(2026, 7, 28, 21, 33, 0, 0, time.UTC)
	res, err := Take([]string{root}, []string{".git"}, dir, now)
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if res == nil {
		t.Fatal("Take returned a nil result for a configured root")
	}
	if !res.Snapshot.ObservedAt.Equal(now) {
		t.Errorf("ObservedAt = %v, want %v", res.Snapshot.ObservedAt, now)
	}
	if len(res.Snapshot.Roots) != 1 {
		t.Fatalf("Snapshot.Roots has %d entries, want 1", len(res.Snapshot.Roots))
	}
	got := res.Snapshot.Roots[0]
	if got.Root != root {
		t.Errorf("Root = %q, want %q", got.Root, root)
	}
	if !reflect.DeepEqual(got.Raw, wantRaw) {
		t.Errorf("Raw = %v, want %v (must match an independent Scan)", got.Raw, wantRaw)
	}
	if !reflect.DeepEqual(got.Filtered, wantFiltered) {
		t.Errorf("Filtered = %v, want %v (must match an independent Scan)", got.Filtered, wantFiltered)
	}

	// The observation must actually be durable: read it back from disk, not
	// from the Result the call happened to return in memory.
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("read %s: %v", res.Path, err)
	}
	var reread Snapshot
	if err := json.Unmarshal(data, &reread); err != nil {
		t.Fatalf("unmarshal %s: %v", res.Path, err)
	}
	if !reread.ObservedAt.Equal(now) {
		t.Errorf("on-disk ObservedAt = %v, want %v", reread.ObservedAt, now)
	}
	if !reflect.DeepEqual(reread.Roots, res.Snapshot.Roots) {
		t.Errorf("on-disk Roots = %v, want %v", reread.Roots, res.Snapshot.Roots)
	}
}

// TestTakeMultipleRootsAreIndependent guards against one root's exclusions or
// contents leaking into another's count.
func TestTakeMultipleRootsAreIndependent(t *testing.T) {
	rootA := t.TempDir()
	rootB := t.TempDir()
	day := time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(rootA, "a.txt"), day)
	touch(t, filepath.Join(rootB, "b1.txt"), day)
	touch(t, filepath.Join(rootB, "b2.txt"), day)

	dir := filepath.Join(t.TempDir(), "snapshots")
	res, err := Take([]string{rootA, rootB}, nil, dir, time.Now())
	if err != nil {
		t.Fatalf("Take: %v", err)
	}
	if len(res.Snapshot.Roots) != 2 {
		t.Fatalf("Roots has %d entries, want 2", len(res.Snapshot.Roots))
	}
	byRoot := map[string]RootScan{}
	for _, r := range res.Snapshot.Roots {
		byRoot[r.Root] = r
	}
	if byRoot[rootA].Raw["2026-07-11"] != 1 {
		t.Errorf("rootA raw count = %d, want 1", byRoot[rootA].Raw["2026-07-11"])
	}
	if byRoot[rootB].Raw["2026-07-11"] != 2 {
		t.Errorf("rootB raw count = %d, want 2", byRoot[rootB].Raw["2026-07-11"])
	}
}

// TestTakeWithNoRootsWritesNothing mirrors TakeStreams's tolerance for a
// stream nobody has: no roots configured is the normal state for a machine
// where filedays capture has not been set up yet, not an error, and it must
// not litter the snapshot directory with an empty file.
func TestTakeWithNoRootsWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "snapshots")
	res, err := Take(nil, nil, dir, time.Now())
	if err != nil {
		t.Fatalf("Take with no roots: %v", err)
	}
	if res != nil {
		t.Errorf("Take with no roots returned %+v, want nil", res)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("Take with no roots created %s", dir)
	}
}

// TestTakeNeverOverwritesAnExistingSnapshot proves two runs in the same
// instant do not collide, the same guarantee snapshot.Take gives knowledgeC
// captures: a count that silently overwrote an earlier observation would
// destroy the earlier moment's evidence, which is exactly what this package
// exists to preserve.
func TestTakeNeverOverwritesAnExistingSnapshot(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "a.txt"), time.Now())
	dir := filepath.Join(t.TempDir(), "snapshots")
	now := time.Date(2026, 7, 28, 21, 33, 0, 0, time.UTC)

	first, err := Take([]string{root}, nil, dir, now)
	if err != nil {
		t.Fatalf("first Take: %v", err)
	}
	second, err := Take([]string{root}, nil, dir, now)
	if err != nil {
		t.Fatalf("second Take: %v", err)
	}
	if first.Path == second.Path {
		t.Errorf("two Take calls at the same instant wrote the same path %s", first.Path)
	}
	for _, p := range []string{first.Path, second.Path} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("stat %s: %v", p, err)
		}
	}
}

func TestReadRootsFileParsesCommentsBlanksAndTilde(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	path := filepath.Join(t.TempDir(), "roots.txt")
	contents := "# roots to scan\n\n   \n~/git/acorn\n/Users/ben/git/timetattle\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}

	got, err := ReadRootsFile(path)
	if err != nil {
		t.Fatalf("ReadRootsFile: %v", err)
	}
	want := []string{filepath.Join(home, "git", "acorn"), "/Users/ben/git/timetattle"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ReadRootsFile = %v, want %v", got, want)
	}
}

func TestReadRootsFileMissingIsNotAnError(t *testing.T) {
	roots, err := ReadRootsFile(filepath.Join(t.TempDir(), "does-not-exist.txt"))
	if err != nil {
		t.Fatalf("ReadRootsFile on a missing config: %v", err)
	}
	if roots != nil {
		t.Errorf("ReadRootsFile on a missing config = %v, want nil", roots)
	}
}

func TestDefaultRootsFileIsUnderTheTimetattleDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := DefaultRootsFile()
	if err != nil {
		t.Fatalf("DefaultRootsFile: %v", err)
	}
	want := filepath.Join(home, ".timetattle", "filedays-roots.txt")
	if got != want {
		t.Errorf("DefaultRootsFile = %q, want %q", got, want)
	}
}
