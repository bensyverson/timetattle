package chains

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/source"
)

// TestStoredBuildOutputIsNotChained: a copy a build made is not a version
// somebody saved. What was left out is counted.
func TestStoredBuildOutputIsNotChained(t *testing.T) {
	r := Reconstruct([]source.Event{
		src(day(10, 0, 0), "/Users/you/git/app/.build/res/bar.motn"),
		src(day(11, 0, 0), "/Users/you/git/app/.build/res/bar-2.motn"),
		src(day(12, 0, 0), "/Users/you/git/app/art/title.motn"),
	}, Options{})

	if r.EventsConsidered != 1 || r.Documents != 1 || len(r.Chains) != 0 {
		t.Errorf("considered %d, %d documents, %d chains; want only the file outside .build",
			r.EventsConsidered, r.Documents, len(r.Chains))
	}
	if r.BuildOutput != 2 {
		t.Errorf("BuildOutput = %d, want 2", r.BuildOutput)
	}
}
