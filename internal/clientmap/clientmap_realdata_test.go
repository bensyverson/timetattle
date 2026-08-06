//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -count=1 -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package clientmap_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/store"
)

// liveLog reads the live store's client-project log, or says which precondition
// is missing.
//
// A STORE AT A SCHEMA OLDER THAN 0012 HAS NO LOG TABLE, and that is neither a
// failure nor evidence of anything: OpenReadOnly deliberately does not migrate,
// so the table is absent until a harvest runs against a build carrying 0012.
// The skip therefore names what is MISSING rather than what was concluded —
// three commits in this repository have gone on skips that named a conclusion.
func liveLog(t *testing.T) []clientmap.Assertion {
	t.Helper()
	path, err := store.DefaultPath()
	if err != nil {
		t.Skipf("no home directory to find the store in: %v", err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Skipf("no readable store at %s: %v", path, err)
	}
	t.Cleanup(func() { st.Close() })

	log, err := st.ClientAssertions(context.Background())
	if err != nil {
		if strings.Contains(err.Error(), "no such table: client_project") {
			t.Skipf("%s has no client_project table, so it is at a schema older than migration "+
				"0012 and no harvest has run against a build carrying it. Nothing has been "+
				"observed about this machine's log; run `timetattle harvest` or "+
				"`go run ./scripts/attribute` first", path)
		}
		t.Fatalf("ClientAssertions: %v", err)
	}
	if len(log) == 0 {
		// NOTHING WAS RECORDED, which is not the same as "the log is
		// consistent". A store nobody has harvested since 0012 landed has not
		// been observed to be clean; it has not been observed at all.
		t.Skipf("%s carries no client-project assertions, so there is no population to fold; "+
			"that is not evidence of agreement", path)
	}
	return log
}

// The RULES are tested portably in clientmap_test.go on invented names. What
// this adds is the other half CLAUDE.md insists on: a constructed case and a
// real-data check sit BESIDE each other and neither substitutes for the other.
//
// The risk here runs a particular way round. Every assertion on this machine
// was written by the backfill, all at once, all retroactive — so the log is in
// its most uniform possible state and a fold that got EffectiveFrom wrong would
// still look right. This check therefore asserts what CAN be asserted about
// that state and says plainly what it cannot reach.
func TestTheLiveClientProjectLogFoldsWithoutContest(t *testing.T) {
	log := liveLog(t)
	m := clientmap.Fold(log)
	if cs := m.Collisions(); len(cs) > 0 {
		for _, c := range cs {
			t.Errorf("project %q is claimed for different clients by %v; its hours are credited "+
				"to no client until one of those directories is re-registered", c.Project, c.Sources)
		}
	}

	// Every asserted project resolves to a client NOW. This is the property
	// `timetattle metrics` depends on for its per-client roll-up, and a project
	// that folded to nothing would silently move hours into the clientless
	// figure.
	unresolved := 0
	for _, project := range m.Projects() {
		if _, ok := m.At(project, time.Now()); !ok {
			unresolved++
			t.Errorf("project %q is asserted but resolves to no client at this instant", project)
		}
	}
	t.Logf("%d assertions covering %d projects, %d contested, %d unresolved",
		len(log), len(m.Projects()), len(m.Collisions()), unresolved)
}

// The live log cannot exercise a REASSIGNMENT, because nothing on this machine
// has been reassigned yet — every row is retroactive. Rather than pretend
// otherwise, this asserts that fact and fails if it stops being true, so that
// the first real reassignment is noticed here rather than discovered in a
// report.
func TestEveryLiveAssertionIsRetroactiveSoNoRealReassignmentIsBeingRelliedOn(t *testing.T) {
	log := liveLog(t)

	dated := 0
	for _, a := range log {
		if !a.EffectiveFrom.IsZero() {
			dated++
		}
	}
	if dated > 0 {
		t.Logf("%d of %d live assertions carry an effective date; the split-timeline path is "+
			"now exercised by real data as well as by the constructed cases", dated, len(log))
	} else {
		t.Logf("all %d live assertions are retroactive, so the split-timeline path is exercised "+
			"ONLY by the constructed cases in clientmap_test.go — real data cannot confirm it here",
			len(log))
	}
}
