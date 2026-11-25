package buildoutput

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// TestIs: only a filesystem observation of something beneath a ".build"
// directory qualifies. Another source's locator is a place somebody was
// working, not a file a compiler wrote.
func TestIs(t *testing.T) {
	for _, c := range []struct {
		source, locator string
		want            bool
	}{
		{Source, "/Users/you/git/app/.build/debug/main.o", true},
		{Source, "/Users/you/git/app/.build/x", true},
		{Source, "/Users/you/git/app/.build", false},
		{Source, "/Users/you/git/app/.build-old/x", false},
		{Source, "/Users/you/git/app/build/x", false},
		{Source, "", false},
		{"claude-transcripts", "/Users/you/git/app/.build/checkouts/dep", false},
	} {
		e := source.NewPoint(time.Unix(0, 0), source.Identity{}, c.source)
		e.Locator = c.locator
		if got := Is(e); got != c.want {
			t.Errorf("Is(%s, %q) = %v, want %v", c.source, c.locator, got, c.want)
		}
	}
}
