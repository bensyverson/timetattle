package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/store"
)

// A REASSIGNMENT IS ASKED FOR, NEVER INFERRED.
//
// A FIRST registration is retroactive, because the evidence under a directory
// usually predates anybody getting round to blessing it. So if a
// RE-registration silently defaulted to "from now", two near-identical commands
// would mean opposite things and nothing would say which had happened — and the
// failure is silent in both directions. Guessing "from now" leaves old work
// under a client it was not done for; guessing "retroactive" rewrites work that
// WAS done for somebody else, which task mjalb's criterion XZk forbids.
//
// Names here are invented. The live client names are a confidentiality matter
// (task 5nCl4) and none of them appears in this repository.

func TestReassigningAClientMustSayWhichKindOfChangeItIs(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "timetattle.db")
	if err := register([]string{"-client", "atelier", "-project", "loom", "-path", dir,
		"-store", db}); err != nil {
		t.Fatalf("first register(): %v", err)
	}

	err := register([]string{"-client", "bindery", "-project", "loom", "-path", dir,
		"-force", "-store", db})
	if err == nil {
		t.Fatal("a client reassignment with neither -since nor -correction was accepted; it " +
			"silently picked one of two opposite meanings")
	}
	// THE MESSAGE IS THE FEATURE. This repository has spent three commits on
	// messages that named a conclusion instead of the choice, so the refusal
	// has to name BOTH options concretely enough to act on.
	for _, want := range []string{"-since", "-correction", "atelier", "bindery"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %q, so it does not present the choice: %v", want, err)
		}
	}
}

// The two flags say opposite things and passing both says nothing.
func TestSinceAndCorrectionTogetherAreRefused(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "timetattle.db")
	err := register([]string{"-client", "atelier", "-project", "loom", "-path", dir,
		"-store", db, "-since", "2026-07-01", "-correction"})
	if err == nil {
		t.Fatal("-since and -correction together were accepted")
	}
}

// A FIRST registration needs no flag and reaches backwards, which is the whole
// reason the default is what it is: registering a directory after its evidence
// exists is the ordinary way somebody starts tracking a project.
func TestAFirstRegistrationIsRetroactiveAndNeedsNoFlag(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "timetattle.db")
	if err := register([]string{"-client", "atelier", "-project", "loom", "-path", dir,
		"-store", db}); err != nil {
		t.Fatalf("register(): %v", err)
	}

	m := foldedMap(t, db)
	long, _ := time.Parse(time.RFC3339, "2020-01-01T00:00:00Z")
	if got, ok := m.At("loom", long); !ok || got != "atelier" {
		t.Errorf("work from 2020 resolves to %q (%v), want atelier; a first registration that "+
			"did not reach backwards would attribute none of the evidence it was made for", got, ok)
	}
}

// -since SPLITS THE TIMELINE, which is the case the whole log exists for. Work
// before the date keeps the client it was done for.
func TestSinceLeavesEarlierWorkWithTheClientItWasDoneFor(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "timetattle.db")
	if err := register([]string{"-client", "atelier", "-project", "loom", "-path", dir,
		"-store", db}); err != nil {
		t.Fatalf("first register(): %v", err)
	}
	if err := register([]string{"-client", "bindery", "-project", "loom", "-path", dir,
		"-force", "-store", db, "-since", "2026-07-01"}); err != nil {
		t.Fatalf("reassigning register(): %v", err)
	}

	m := foldedMap(t, db)
	june := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	august := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if got, _ := m.At("loom", june); got != "atelier" {
		t.Errorf("June resolves to %q, want atelier; a reassignment must not rewrite work that "+
			"was genuinely done for the previous client", got)
	}
	if got, _ := m.At("loom", august); got != "bindery" {
		t.Errorf("August resolves to %q, want bindery", got)
	}
}

// -correction REWRITES THE WHOLE TIMELINE, because the old answer was never
// true. Same command, same log, opposite result — which is exactly why it
// cannot be inferred.
func TestCorrectionRewritesTheWholeHistory(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "timetattle.db")
	if err := register([]string{"-client", "atlier", "-project", "loom", "-path", dir,
		"-store", db}); err != nil {
		t.Fatalf("first register(): %v", err)
	}
	if err := register([]string{"-client", "atelier", "-project", "loom", "-path", dir,
		"-force", "-store", db, "-correction"}); err != nil {
		t.Fatalf("correcting register(): %v", err)
	}

	m := foldedMap(t, db)
	for _, at := range []time.Time{
		time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC),
		time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	} {
		if got, _ := m.At("loom", at); got != "atelier" {
			t.Errorf("at %s a corrected project resolves to %q, want atelier everywhere",
				at.Format(time.DateOnly), got)
		}
	}
}

// Re-registering with nothing changed appends nothing. 0012 carries no UNIQUE
// index on purpose — recording the same assertion twice is a fact and the fold
// is idempotent — so the no-op is enforced at the caller, exactly as
// registry.Register already treats an unchanged marker as a no-op.
func TestReRegisteringUnchangedAppendsNothing(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(t.TempDir(), "timetattle.db")
	for range 3 {
		if err := register([]string{"-client", "atelier", "-project", "loom", "-path", dir,
			"-store", db}); err != nil {
			t.Fatalf("register(): %v", err)
		}
	}

	st, err := store.Open(db)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer st.Close()
	log, err := st.ClientAssertions(context.Background())
	if err != nil {
		t.Fatalf("ClientAssertions: %v", err)
	}
	if len(log) != 1 {
		t.Errorf("three identical registrations wrote %d assertions, want 1; a script that "+
			"registers on every run would grow the log without ever changing an answer", len(log))
	}
}

func foldedMap(t *testing.T, path string) clientmap.Map {
	t.Helper()
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer st.Close()
	m, err := st.ClientMap(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("ClientMap: %v", err)
	}
	return m
}
