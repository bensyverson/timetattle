package sessions_test

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// TestStoredGeneratedFilesAreExcludedWithNoConfigurationAtAll: observations already
// in the store are never deleted, so they stop counting where they are read.
func TestStoredGeneratedFilesAreExcludedWithNoConfigurationAtAll(t *testing.T) {
	session := source.NewPoint(at(0), source.Identity{}, "claude-transcripts")
	session.Locator = "/Users/you/git/app/.build/checkouts/dep"
	events := []source.Event{
		located("/Users/you/git/app/.build/debug/main.o"),
		located("/Users/you/git/app/.build/debug/main.log"),
		located("/Users/you/git/app/.venv/lib/site/pip.log"),
		located("/Users/you/git/app/pkg/__pycache__/main.pyc"),
		located("/Users/you/git/app/.build-old/notes.md"),
		located("/Users/you/git/app/Sources/main.swift"),
		session,
	}

	kept, drop := sessions.Retain(events, excludes(t, "*.log"))
	if len(kept) != 3 {
		t.Fatalf("kept %d observations, want 3 (lookalike, source file, transcript): %+v", len(kept), kept)
	}
	if drop.Generated != 4 || drop.Total != 4 {
		t.Errorf("drop = %+v, want 4 of 4 counted as generated", drop)
	}
	// A pattern's count must not also claim what the code rule already took.
	if drop.ByPattern["*.log"] != 0 {
		t.Errorf("*.log was credited with %d generated observations", drop.ByPattern["*.log"])
	}
}
