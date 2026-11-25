package filedays

import (
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultExcludesSkipBuildOutput keeps this list and fsobserve.excludedDirs
// saying the same thing about Swift's ".build".
func TestDefaultExcludesSkipBuildOutput(t *testing.T) {
	root := t.TempDir()
	day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.Local)
	touch(t, filepath.Join(root, "keep.swift"), day)
	touch(t, filepath.Join(root, ".build", "debug", "main.o"), day)
	touch(t, filepath.Join(root, "build", "notes.txt"), day)
	touch(t, filepath.Join(root, ".build-old", "notes.txt"), day)

	filtered, err := Scan(root, DefaultExcludes)
	if err != nil {
		t.Fatalf("Scan(filtered): %v", err)
	}
	if filtered["2026-08-01"] != 3 {
		t.Fatalf("filtered scan found %d files, want 3 (.build excluded whole)", filtered["2026-08-01"])
	}
}
