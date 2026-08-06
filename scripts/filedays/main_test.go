package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// touch creates a file at rel under root with the given modification time.
func touch(t *testing.T, root, rel string, mod time.Time) {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", rel, err)
	}
}

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 12, 0, 0, 0, time.Local)
}

func TestScanBucketsFilesByLocalDate(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "a.motn", day(2026, time.July, 11))
	touch(t, root, "b.motn", day(2026, time.July, 11))
	touch(t, root, "c.motn", day(2026, time.July, 12))

	got, err := scan(root, nil)
	if err != nil {
		t.Fatalf("scan() = %v", err)
	}
	if got["2026-07-11"] != 2 {
		t.Errorf("2026-07-11 = %d, want 2", got["2026-07-11"])
	}
	if got["2026-07-12"] != 1 {
		t.Errorf("2026-07-12 = %d, want 1", got["2026-07-12"])
	}
}

// This is the whole reason the tool exists. Agent worktrees hold complete
// copies of the tree, so a raw count reports the same work many times over
// and turns one edited file into twenty-one.
func TestScanSkipsExcludedDirectories(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "scanline/real.motn", day(2026, time.July, 11))
	touch(t, root, ".claude/worktrees/agent-aaa/scanline/real.motn", day(2026, time.July, 11))
	touch(t, root, ".claude/worktrees/agent-bbb/scanline/real.motn", day(2026, time.July, 11))

	raw, err := scan(root, nil)
	if err != nil {
		t.Fatalf("scan() = %v", err)
	}
	if raw["2026-07-11"] != 3 {
		t.Fatalf("unfiltered 2026-07-11 = %d, want 3; the fixture is wrong", raw["2026-07-11"])
	}

	filtered, err := scan(root, []string{"worktrees"})
	if err != nil {
		t.Fatalf("scan() = %v", err)
	}
	if filtered["2026-07-11"] != 1 {
		t.Errorf("filtered 2026-07-11 = %d, want 1", filtered["2026-07-11"])
	}
}

// An excluded directory must not be descended into at all. Matching only leaf
// paths would still pay the cost of walking twenty-one copies of a tree.
func TestScanDoesNotDescendIntoExcludedDirectories(t *testing.T) {
	root := t.TempDir()
	touch(t, root, "keep/a.motn", day(2026, time.July, 11))
	touch(t, root, "skip/deep/nested/b.motn", day(2026, time.July, 11))

	got, err := scan(root, []string{"skip"})
	if err != nil {
		t.Fatalf("scan() = %v", err)
	}
	if got["2026-07-11"] != 1 {
		t.Errorf("2026-07-11 = %d, want 1", got["2026-07-11"])
	}
}

func TestScanExcludesMatchWholeComponentsOnly(t *testing.T) {
	root := t.TempDir()
	// "worktrees-notes" is a different directory from "worktrees" and must
	// survive; a substring match would wrongly discard it.
	touch(t, root, "worktrees-notes/a.motn", day(2026, time.July, 11))
	touch(t, root, "worktrees/b.motn", day(2026, time.July, 11))

	got, err := scan(root, []string{"worktrees"})
	if err != nil {
		t.Fatalf("scan() = %v", err)
	}
	if got["2026-07-11"] != 1 {
		t.Errorf("2026-07-11 = %d, want 1", got["2026-07-11"])
	}
}

func TestScanCountsFilesNotDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "empty/nested"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got, err := scan(root, nil)
	if err != nil {
		t.Fatalf("scan() = %v", err)
	}
	for d, n := range got {
		t.Errorf("counted %d entries on %s, want none; directories are not evidence", n, d)
	}
}

func TestScanMissingRootIsReported(t *testing.T) {
	if _, err := scan(filepath.Join(t.TempDir(), "absent"), nil); err == nil {
		t.Fatal("scan() = nil, want a missing root reported")
	}
}
