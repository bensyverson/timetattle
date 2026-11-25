package generated

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// TestIs: only a filesystem observation of something beneath one of the named
// directories qualifies. Another source's locator is a place somebody was
// working, not a file a tool wrote.
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
		{Source, "/Users/you/git/app/.derived/Build/x.o", true},
		{Source, "/Users/you/git/app/.venv/lib/site.py", true},
		{Source, "/Users/you/git/app/pkg/__pycache__/x.pyc", true},
		{Source, "/Users/you/git/app/.mypy_cache/3.12/x.json", true},
		{Source, "/Users/you/git/app/.pytest_cache/v/x", true},
		{Source, "/Users/you/git/app/.ruff_cache/0.1/x", true},
		{Source, "/Users/you/git/app/.venv", false},
		{Source, "/Users/you/git/app/venv/x.py", false},
		{Source, "/Users/you/git/app/.venv-old/x.py", false},
		{Source, "/Users/you/git/app/derived/x", false},
		{Source, "/Users/you/git/app/__pycache__.bak/x", false},
		{"claude-transcripts", "/Users/you/git/app/.venv/lib/dep", false},
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
