package sessions_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// located is one observation of a file at an absolute path.
func located(locator string) source.Event {
	e := source.NewPoint(at(0), source.Identity{Document: "x"}, "fs-observations")
	e.Locator = locator
	return e
}

func excludes(t *testing.T, patterns ...string) sessions.Excludes {
	t.Helper()
	x, err := sessions.NewExcludes(patterns)
	if err != nil {
		t.Fatalf("NewExcludes(%q): %v", patterns, err)
	}
	return x
}

// TestSidecarsAreExcludedWithNoConfigurationAtAll is the code half of the split:
// a SQLite sidecar cannot be a work product on any machine, so nobody should
// have to configure it away, and the 86 already in the store must stop counting
// without anything being deleted.
func TestSidecarsAreExcludedWithNoConfigurationAtAll(t *testing.T) {
	x := excludes(t)
	for _, locator := range []string{"/Users/ben/git/acorn/.jobs.db-wal", "/Users/ben/git/kura/.jobs.db-shm"} {
		if !x.Match(locator) {
			t.Errorf("%s is a SQLite sidecar and survived an empty exclude list", locator)
		}
	}
	if x.Match("/Users/ben/git/acorn/.jobs.db") {
		t.Error("the database itself was excluded; only its sidecars are never a deliverable")
	}
}

// TestABasenameGlobMatchesTheFileNameAndNotThePath pins the first half of the
// pattern syntax Ben settled on: a pattern with no slash is matched against the
// file's base name with filepath.Match.
func TestABasenameGlobMatchesTheFileNameAndNotThePath(t *testing.T) {
	x := excludes(t, "*.log.bz2")
	if !x.Match("/Users/ben/git/Easley/logs/access-2026-03-22.log.bz2") {
		t.Error("*.log.bz2 did not match a rotated log")
	}
	if x.Match("/Users/ben/git/Easley/viewer/js/ui.js") {
		t.Error("*.log.bz2 matched a file that is not one")
	}
	// A glob with no slash must not be tempted into matching path segments:
	// "logs" names a directory here and the pattern is about file names.
	if excludes(t, "logs").Match("/Users/ben/git/Easley/logs/access.log") {
		t.Error("a basename glob matched a directory component of the path")
	}
}

// TestAPatternWithASlashIsAPathPrefix pins the second half: a pattern
// containing a slash is an absolute path prefix, which is what names a
// directory whose whole contents are machine output.
func TestAPatternWithASlashIsAPathPrefix(t *testing.T) {
	x := excludes(t, "/Users/ben/git/Easley/reports/")
	if !x.Match("/Users/ben/git/Easley/reports/2024-12-09.json") {
		t.Error("the prefix did not match a file inside the directory it names")
	}
	if x.Match("/Users/ben/git/Easley/viewer/stats-server.py") {
		t.Error("the prefix matched a file outside the directory it names")
	}
}

// TestAPrefixStopsAtAPathBoundary is the foot-gun a literal prefix comparison
// would leave open: "/Users/ben/git/Ea" must not swallow "/Users/ben/git/Easley".
func TestAPrefixStopsAtAPathBoundary(t *testing.T) {
	x := excludes(t, "/Users/ben/git/Ea")
	if x.Match("/Users/ben/git/Easley/logs/access.log") {
		t.Error("a prefix matched across a path boundary; /Users/ben/git/Ea is not /Users/ben/git/Easley")
	}
	// The directory itself, written with or without a trailing slash, is the
	// same instruction. A silent difference between the two would be a
	// configuration that appears to be set and is not.
	for _, pattern := range []string{"/Users/ben/git/Easley/logs", "/Users/ben/git/Easley/logs/"} {
		if !excludes(t, pattern).Match("/Users/ben/git/Easley/logs/access.log") {
			t.Errorf("%q did not match a file inside the directory it names", pattern)
		}
	}
}

// TestAnObservationWithNoPathIsNeverExcluded. knowledgeC and Biome carry no
// locator at all, and a rule about file names has nothing to say about them.
// Excluding them would delete presence, which is the denominator every capacity
// figure rests on.
func TestAnObservationWithNoPathIsNeverExcluded(t *testing.T) {
	if excludes(t, "*").Match("") {
		t.Error("an observation carrying no path was excluded by a file-name rule")
	}
}

// TestARelativePatternIsRefusedRatherThanSilentlyMatchingNothing. A locator is
// always absolute, so "logs/" can never match anything; accepting it would
// leave a person's configuration doing nothing with no symptom for ever.
func TestARelativePatternIsRefusedRatherThanSilentlyMatchingNothing(t *testing.T) {
	_, err := sessions.NewExcludes([]string{"logs/"})
	if err == nil {
		t.Fatal("a relative path pattern was accepted; it can never match a locator")
	}
	if !strings.Contains(err.Error(), "logs/") {
		t.Errorf("the error does not name the pattern it refused: %v", err)
	}
}

// TestAnUnparseableGlobIsRefusedAndNamed. filepath.Match rejects an unclosed
// character class, and a pattern nothing can match must fail where it was
// written rather than where it was needed.
func TestAnUnparseableGlobIsRefusedAndNamed(t *testing.T) {
	_, err := sessions.NewExcludes([]string{"[bad"})
	if err == nil {
		t.Fatal("an unparseable glob was accepted")
	}
	if !strings.Contains(err.Error(), "[bad") {
		t.Errorf("the error does not name the pattern it refused: %v", err)
	}
}

// TestAnEmptyPatternIsRefused. An empty string as a prefix matches everything,
// which is a typo that would silently delete the whole corpus.
func TestAnEmptyPatternIsRefused(t *testing.T) {
	if _, err := sessions.NewExcludes([]string{""}); err == nil {
		t.Fatal("an empty pattern was accepted; as a prefix it matches every locator there is")
	}
}

// TestRetainCountsWhatItDroppedAndSumsToTheInput is the accounting rule this
// project runs on. A figure that dropped observations without saying how many
// cannot be reconciled with the store it came from.
func TestRetainCountsWhatItDroppedAndSumsToTheInput(t *testing.T) {
	events := []source.Event{
		located("/Users/ben/git/Easley/logs/access-2026-03-22.log.bz2"),
		located("/Users/ben/git/Easley/logs/error-2026-03-22.log.bz2"),
		located("/Users/ben/git/Easley/reports/2024-12-09.json"),
		located("/Users/ben/git/acorn/.jobs.db-wal"),
		located("/Users/ben/git/Easley/viewer/stats-server.py"),
		backlit(0, time.Hour, "on"),
	}
	kept, drop := sessions.Retain(events, excludes(t, "*.log.bz2", "/Users/ben/git/Easley/reports/"))

	if len(kept)+drop.Total != len(events) {
		t.Errorf("kept %d and dropped %d out of %d; the accounting does not sum to the input",
			len(kept), drop.Total, len(events))
	}
	if drop.Total != 4 {
		t.Errorf("dropped %d, want 4 (two rotated logs, one generated report, one sidecar)", drop.Total)
	}
	if drop.Sidecars != 1 {
		t.Errorf("counted %d sidecars, want 1", drop.Sidecars)
	}
	if got := drop.ByPattern["*.log.bz2"]; got != 2 {
		t.Errorf("*.log.bz2 dropped %d, want 2", got)
	}
	if got := drop.ByPattern["/Users/ben/git/Easley/reports/"]; got != 1 {
		t.Errorf("the reports prefix dropped %d, want 1", got)
	}
	if len(kept) != 2 {
		t.Fatalf("kept %d observations, want 2 (the dashboard source file and the backlight reading)", len(kept))
	}
}

// TestRetainReportsAPatternThatMatchedNothing. A pattern nobody's files match is
// a person having said something the tool did nothing with, and silence there is
// the same failure as a config key quietly ignored.
func TestRetainReportsAPatternThatMatchedNothing(t *testing.T) {
	_, drop := sessions.Retain([]source.Event{located("/Users/ben/git/acorn/main.go")},
		excludes(t, "*.log.bz2"))
	count, named := drop.ByPattern["*.log.bz2"]
	if !named {
		t.Fatal("a pattern that matched nothing is missing from the accounting entirely; " +
			"matched-nothing and never-configured must not look the same")
	}
	if count != 0 {
		t.Errorf("the pattern matched %d observations, want 0", count)
	}
}

// TestRetainDoesNotReorderOrMutateTheCallersSlice keeps Retain as ordinary as
// Cluster: everything upstream of the clustering is a pure function.
func TestRetainDoesNotReorderOrMutateTheCallersSlice(t *testing.T) {
	events := []source.Event{
		located("/a/keep.go"),
		located("/a/.jobs.db-wal"),
		located("/a/also-keep.go"),
	}
	kept, _ := sessions.Retain(events, excludes(t))
	if events[1].Locator != "/a/.jobs.db-wal" {
		t.Errorf("the caller's slice was rewritten: element 1 is now %q", events[1].Locator)
	}
	if len(kept) != 2 || kept[0].Locator != "/a/keep.go" || kept[1].Locator != "/a/also-keep.go" {
		t.Errorf("kept %d events in the wrong order: %v", len(kept), kept)
	}
}
