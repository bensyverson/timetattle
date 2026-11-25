package filedays

import (
	"path/filepath"
	"testing"
	"time"
)

// TestDefaultExcludesSkipDerivedDataAndPythonCaches keeps this list and
// fsobserve's saying the same thing about directories that tools write.
func TestDefaultExcludesSkipDerivedDataAndPythonCaches(t *testing.T) {
	for _, name := range []string{".derived", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			day := time.Date(2026, 8, 1, 10, 0, 0, 0, time.Local)
			touch(t, filepath.Join(root, "keep.py"), day)
			touch(t, filepath.Join(root, "pkg", name, "deep", "generated"), day)

			filtered, err := Scan(root, DefaultExcludes)
			if err != nil {
				t.Fatalf("Scan(filtered): %v", err)
			}
			if filtered["2026-08-01"] != 1 {
				t.Fatalf("filtered scan found %d files, want 1 (%s excluded whole)", filtered["2026-08-01"], name)
			}
		})
	}
}
