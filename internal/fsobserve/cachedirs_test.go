package fsobserve

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCollectSkipsDerivedDataAndPythonCaches: Xcode, the interpreter and the
// Python tools write these themselves, so none of it is work somebody did.
func TestCollectSkipsDerivedDataAndPythonCaches(t *testing.T) {
	for _, name := range []string{".derived", "__pycache__", ".mypy_cache", ".pytest_cache", ".ruff_cache"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			buried := filepath.Join(dir, "pkg", name, "deep", "client.motn")
			if err := os.MkdirAll(filepath.Dir(buried), 0o755); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			write(t, buried, "generated")
			kept := filepath.Join(dir, "kept.motn")
			write(t, kept, "real work")

			events := collect(t, dir)
			for _, e := range events {
				if e.Locator != kept {
					t.Errorf("got an event from inside %s: %+v", name, e)
				}
			}
			if len(eventsFor(events, kept)) == 0 {
				t.Errorf("the one file outside %s produced no events at all", name)
			}
		})
	}
}
