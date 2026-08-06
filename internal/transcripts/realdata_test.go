//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package transcripts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// TestRealCorpusParsesInSeconds is the benchmark behind task FglLP's xpf
// criterion: a full backfill must complete in seconds, not minutes. It also
// exercises everything the synthetic fixtures cannot -- the actual shapes
// Claude Code writes, at the actual scale of a real corpus -- which is the
// standing rule this project follows for every source: constructed cases sit
// BESIDE a real-data check, neither substitutes for the other.
//
// It skips cleanly, not fails, on a machine with no ~/.claude/projects: that
// is what CI and a fresh checkout look like, and is not this test's failure
// to report.
func TestRealCorpusParsesInSeconds(t *testing.T) {
	root := realRoot(t)

	start := time.Now()
	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated transcript corpus")
	}

	// A generous ceiling: the criterion is "seconds, not minutes", and this
	// catches a real regression (an accidental sequential read, an O(n^2)
	// somewhere) without being a flaky trip-wire on a loaded CI box. The
	// actual number is logged below for the record.
	const ceiling = 60 * time.Second
	if elapsed > ceiling {
		t.Errorf("parsing the real corpus took %s, want well under %s", elapsed, ceiling)
	}
	t.Logf("parsed %d events from %s in %s", len(events), root, elapsed)
}

// TestRealEventsValidateAndSplitHumanFromMachine cross-checks the shape of
// what Collect actually returns against a real corpus: every event must
// stand on its own (source.Event.Validate), a real conversation has both a
// human and machine turns, and per-record cwd attribution must reach more
// than one project, since a real corpus spans many repositories.
func TestRealEventsValidateAndSplitHumanFromMachine(t *testing.T) {
	root := realRoot(t)

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated transcript corpus")
	}

	var (
		human, machine int
		attributed     int
		locators       = map[string]bool{}
		byDocument     = map[string][]source.Event{}
	)
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v does not validate: %v", e, err)
		}
		if e.Shape != source.Point {
			t.Fatalf("event %+v has shape %q, want point -- this source never claims a duration", e, e.Shape)
		}
		switch e.State {
		case StateHuman:
			human++
		case StateMachine:
			machine++
		default:
			t.Fatalf("event %+v has state %q, want %q or %q", e, e.State, StateHuman, StateMachine)
		}
		if e.Locator != "" {
			attributed++
			locators[e.Locator] = true
		}
		if e.Identity.Document == "" {
			t.Fatalf("event %+v has no document -- every real record carries a uuid", e)
		}
		byDocument[e.Identity.Document] = append(byDocument[e.Identity.Document], e)
	}

	if human == 0 {
		t.Error("got zero human-state events from a real corpus that includes real conversations")
	}
	if machine == 0 {
		t.Error("got zero machine-state events from a real corpus that includes assistant and tool activity")
	}
	// Tool-result turns fed back as "user" records dominate the "user" type
	// in this format (measured 3,229 of 3,510 sampled) and every assistant,
	// progress, system and attachment record is machine too, so machine
	// activity should be the large majority.
	if machine < human {
		t.Errorf("got %d human, %d machine -- expected machine to dominate given how the format represents tool results", human, machine)
	}
	if len(locators) < 2 {
		t.Errorf("events attribute to %d distinct locators, want at least 2 -- a real corpus spans several repositories", len(locators))
	}

	// Collect's own dedupeReplays step must have already resolved every
	// compaction/side-question replay down to one event per (Document,
	// Start) -- so by the time events reach here, a uuid must not repeat at
	// all. See dedupeReplays' doc for why the replay exists in the raw data
	// and why StateHuman is what survives it.
	for doc, occurrences := range byDocument {
		if len(occurrences) > 1 {
			t.Errorf("uuid %s appears %d times after dedupeReplays, want 1: %+v", doc, len(occurrences), occurrences)
		}
	}

	t.Logf("%d events: %d human, %d machine, %d attributed across %d locators",
		len(events), human, machine, attributed, len(locators))
}

// TestRealCorpusHasSubagentFiles confirms the fixture-driven subagent tests
// exercise a shape the real corpus actually contains, rather than a
// structure this package invented for itself.
func TestRealCorpusHasSubagentFiles(t *testing.T) {
	root := realRoot(t)

	found := false
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if filepath.Base(filepath.Dir(path)) == subagentsDirName {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if !found {
		t.Skipf("no file under %s sits in a %s/ directory", root, subagentsDirName)
	}
}

// TestRealCorpusCollectsIdenticallyThroughASymlinkedRoot is the real-data half
// of the symlinked-root regression (task eAVcU). The constructed fixtures in
// symlinkroot_test.go hold three events; this holds several hundred thousand,
// across the actual directory shapes Claude Code writes, and it is the source
// with by far the most at stake in this project.
//
// Both routes are measured in the SAME run and compared against each other
// rather than against a number written down somewhere: the corpus grows while
// the test suite runs, so any fixed figure is stale by the time it is checked.
// A tolerance is allowed for exactly that reason -- Claude Code may append to
// a transcript between the two collections -- but the two must agree to within
// a hair, and a fix that fails to walk the symlink at all yields zero, which no
// tolerance can hide.
func TestRealCorpusCollectsIdenticallyThroughASymlinkedRoot(t *testing.T) {
	root := realRoot(t)

	link := filepath.Join(t.TempDir(), "projects-alias")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	// Each corpus is counted and released before the next is collected: this
	// machine's corpus runs to hundreds of thousands of events, and holding
	// two full slices at once buys nothing a count does not.
	count := func(from string) (int, int, int) {
		t.Helper()
		events, err := NewReader(from).Collect(context.Background(), time.Time{})
		if err != nil {
			t.Fatalf("Collect via %s: %v", from, err)
		}
		var human, machine int
		for _, e := range events {
			if e.State == StateHuman {
				human++
			} else {
				machine++
			}
		}
		return len(events), human, machine
	}

	viaReal, humanReal, machineReal := count(root)
	if viaReal == 0 {
		t.Fatal("the real corpus produced no events; the fixture is wrong, not the walk")
	}
	viaLink, humanLink, machineLink := count(link)
	if viaLink == 0 {
		t.Fatalf("collected 0 events through a symlink to %s, which yields %d by its real path; "+
			"a root that IS a symlink was never walked", root, viaReal)
	}

	// One percent of a corpus this size is thousands of events -- far more
	// than a concurrent session can append between two collections, and far
	// less than any real walking difference.
	const tolerance = 0.01
	drift := float64(viaReal-viaLink) / float64(viaReal)
	if drift < -tolerance || drift > tolerance {
		t.Errorf("collected %d events through the symlinked root and %d through the real path (%.2f%% apart)",
			viaLink, viaReal, drift*100)
	}
	t.Logf("real path: %d events (%d human, %d machine); symlinked root: %d events (%d human, %d machine)",
		viaReal, humanReal, machineReal, viaLink, humanLink, machineLink)
}

func realRoot(t *testing.T) string {
	t.Helper()
	root, err := DefaultRoot()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skipf("no readable transcript corpus at %s: %v", root, err)
	}
	if len(entries) == 0 {
		t.Skipf("%s holds no projects", root)
	}
	return root
}
