package filedays

import (
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultExcludesSkipVirtualenvs keeps this list and fsobserve.excludedDirs
// saying the same thing. They are two copies of one judgement — a directory of
// installed dependencies is not work anybody did — and the whole value of
// scripts/filedays as a cross-check on the collector is that the two agree.
//
// Measured on the live store 2026-08-01 (task 2s1kP): 21.2% of attributed
// fs-observations were inside a `.venv`. `worktrees-extra` is here for the same
// reason it is in TestScanExcludesDirectoriesWhole: the match is on the whole
// base name, so a directory that merely starts with an excluded name survives.
func TestDefaultExcludesSkipVirtualenvs(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(root, "keep.py"), day)
	touch(t, filepath.Join(root, ".venv", "lib", "python3.12", "site-packages", "aiohttp", "client.py"), day)
	// A directory named `env` is this repository's own scripts/spike/env, and a
	// `.venv-old` is a leftover somebody may still be reading. Neither is named
	// exactly ".venv" and neither may be skipped.
	touch(t, filepath.Join(root, "env", "main.go"), day)
	touch(t, filepath.Join(root, ".venv-old", "note.txt"), day)

	raw, err := Scan(root, nil)
	if err != nil {
		t.Fatalf("Scan(raw): %v", err)
	}
	if raw["2026-08-01"] != 4 {
		t.Fatalf("raw scan found %d files, want 4", raw["2026-08-01"])
	}

	filtered, err := Scan(root, DefaultExcludes)
	if err != nil {
		t.Fatalf("Scan(filtered): %v", err)
	}
	if filtered["2026-08-01"] != 3 {
		t.Fatalf("filtered scan found %d files, want 3 (.venv excluded whole; env and .venv-old kept)",
			filtered["2026-08-01"])
	}
}
