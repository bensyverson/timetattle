//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package timestamped_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// The fixtures above are shaped like the real vault, but only the real vault
// proves the shape was right. This skips wherever Motion is not installed,
// which is most machines.
func TestRealMotionVaultIfPresent(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	root := filepath.Join(home, "Movies", "Motion Projects", "Autosave Vault")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("no Motion autosave vault at %s: %v", root, err)
	}

	events := collect(t, newReader(t, only(t, motionManifest(root))), time.Time{})
	if len(events) == 0 {
		t.Skipf("the reader collected no events from %s", root)
	}
	for _, e := range events {
		if e.Identity.Document == "" {
			t.Error("event has no document; the timestamp pattern did not match its filename")
		}
		if e.Identity.Project != "" {
			t.Errorf("event for %q carries project %q, want empty; that is a later resolution pass's job",
				e.Identity.Document, e.Identity.Project)
		}
		if e.Start.Year() < 2000 {
			t.Errorf("event for %q starts at %s, which is not a plausible autosave time",
				e.Identity.Document, e.Start)
		}
	}
	t.Logf("read %d autosaves from %s", len(events), root)
}
