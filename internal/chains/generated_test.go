package chains

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/source"
)

// TestStoredGeneratedFilesAreNotChained: a copy a tool made is not a version
// somebody saved. What was left out is counted.
func TestStoredGeneratedFilesAreNotChained(t *testing.T) {
	r := Reconstruct([]source.Event{
		src(day(10, 0, 0), "/Users/you/git/app/.build/res/bar.motn"),
		src(day(11, 0, 0), "/Users/you/git/app/.build/res/bar-2.motn"),
		src(day(11, 1, 0), "/Users/you/git/app/.derived/res/bar-3.motn"),
		src(day(11, 2, 0), "/Users/you/git/app/.venv/res/bar-4.motn"),
		src(day(12, 0, 0), "/Users/you/git/app/art/title.motn"),
	}, Options{})

	if r.EventsConsidered != 1 || r.Documents != 1 || len(r.Chains) != 0 {
		t.Errorf("considered %d, %d documents, %d chains; want only the file outside the generated directories",
			r.EventsConsidered, r.Documents, len(r.Chains))
	}
	if r.Generated != 4 {
		t.Errorf("Generated = %d, want 4", r.Generated)
	}
}
