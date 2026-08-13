package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// consolidateTestIndex is this package's isolated registry index. TestMain
// redirects HOME, so DefaultIndexPath already points inside a temporary
// directory; naming it here keeps each test reading the same index the
// commands write, without any test knowing the path.
func consolidateTestIndex(t *testing.T) string {
	t.Helper()
	p, err := registry.DefaultIndexPath()
	if err != nil {
		t.Fatalf("DefaultIndexPath: %v", err)
	}
	return p
}

// consolidateTestEvent is a transcript-shaped observation at locator, at a
// distinct hour so two of them cannot collapse into one stored row.
func consolidateTestEvent(locator string, hour int) source.Event {
	at := time.Date(2026, 3, 12, hour, 0, 0, 0, time.UTC)
	e := source.NewPoint(at, source.Identity{App: "com.anthropic.claude-code"}, "claude-transcripts")
	e.Locator = locator
	return e
}

func consolidateTestMarkers(t *testing.T) []*registry.Marker {
	t.Helper()
	markers, err := registry.List(consolidateTestIndex(t))
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return markers
}

// TestConsolidateCommandReattributesEvidenceUnderAVanishedPath is the
// acceptance case, in the shape of the real one measured on 2026-08-01:
// /Users/you/git/theaitrellis was renamed to nobedan, leaving 14,218
// observations at a path that no longer exists — 4,294 of them one level down,
// at theaitrellis/stacktrace, which is why the alias has to match a prefix.
func TestConsolidateCommandReattributesEvidenceUnderAVanishedPath(t *testing.T) {
	current := t.TempDir()
	if err := register([]string{"-client", "Ledgerwave", "-project", "nobedan", "-path", current}); err != nil {
		t.Fatalf("register: %v", err)
	}
	former := filepath.Join(t.TempDir(), "theaitrellis")

	st, _ := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: "claude-transcripts", Input: "/dev/null"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	nested := filepath.Join(former, "stacktrace")
	// Distinct instants, because locator is deliberately not part of
	// event_identity (task Ls0XC): two events differing only in locator
	// collapse into one row, which would quietly halve this fixture.
	if _, err := run.Write(context.Background(), []source.Event{
		consolidateTestEvent(former, 9),
		consolidateTestEvent(nested, 10),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	// Before the declaration, both are unattributed: nothing on disk can be
	// climbed to from a path that is gone.
	if err := resolveProjects(context.Background(), st, consolidateTestMarkers(t), &bytes.Buffer{}); err != nil {
		t.Fatalf("resolveProjects before: %v", err)
	}
	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("Events = %d rows, want 2", len(events))
	}
	for _, e := range events {
		if e.Identity.Project != "" {
			t.Fatalf("event at %s attributed to %q before any declaration", e.Locator, e.Identity.Project)
		}
	}

	if err := consolidateCommand([]string{"-path", current, "-former", former}); err != nil {
		t.Fatalf("consolidateCommand: %v", err)
	}

	if err := resolveProjects(context.Background(), st, consolidateTestMarkers(t), &bytes.Buffer{}); err != nil {
		t.Fatalf("resolveProjects after: %v", err)
	}
	events, err = st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range events {
		if e.Identity.Project != "nobedan" {
			t.Errorf("event at %s = project %q, want nobedan", e.Locator, e.Identity.Project)
		}
	}
}

func TestConsolidateCommandRequiresPath(t *testing.T) {
	if err := consolidateCommand([]string{"-former", filepath.Join(t.TempDir(), "gone")}); err == nil {
		t.Fatal("consolidateCommand() with no -path = nil error, want one")
	}
}

func TestConsolidateCommandRequiresFormer(t *testing.T) {
	dir := t.TempDir()
	if err := register([]string{"-client", "Ledgerwave", "-project", "nobedan", "-path", dir}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := consolidateCommand([]string{"-path", dir}); err == nil {
		t.Fatal("consolidateCommand() with no -former = nil error, want one")
	}
}

func TestConsolidateCommandRefusesAnUnregisteredTarget(t *testing.T) {
	if err := consolidateCommand([]string{
		"-path", t.TempDir(),
		"-former", filepath.Join(t.TempDir(), "theaitrellis"),
	}); err == nil {
		t.Fatal("consolidateCommand() into an unregistered directory = nil error, want a refusal")
	}
}

func TestConsolidateCommandRefusesAFormerPathThatStillExists(t *testing.T) {
	current := t.TempDir()
	if err := register([]string{"-client", "Ledgerwave", "-project", "nobedan", "-path", current}); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := consolidateCommand([]string{"-path", current, "-former", t.TempDir()}); err == nil {
		t.Fatal("consolidateCommand() with a former path that still exists = nil error, want a refusal")
	}
}

// TestConsolidateCommandRecordsTheTargetInTheIndex closes the gap that would
// otherwise make a correct declaration inert: attribution consults former paths
// through registry.List, so a marker whose directory the index does not name is
// never read, and the alias silently does nothing.
func TestConsolidateCommandRecordsTheTargetInTheIndex(t *testing.T) {
	current := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: current, Client: "Ledgerwave", Project: "nobedan"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	// Registered directly, deliberately bypassing the index the register
	// command would have written.
	former := filepath.Join(t.TempDir(), "theaitrellis")
	if err := consolidateCommand([]string{"-path", current, "-former", former}); err != nil {
		t.Fatalf("consolidateCommand: %v", err)
	}

	var found bool
	for _, m := range consolidateTestMarkers(t) {
		if m.Path == current {
			found = true
		}
	}
	if !found {
		if _, err := os.Stat(filepath.Join(current, registry.MarkerFilename)); err != nil {
			t.Fatalf("marker missing from %s: %v", current, err)
		}
		t.Errorf("%s is not in the registry index after consolidate; its former path would never be read", current)
	}
}
