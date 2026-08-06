package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// These tests cover the wiring this task actually adds: internal/registry's
// path resolution and internal/store's event storage already have their own
// exhaustive test suites, so what is missing — and worth proving here — is
// that a real marker on disk, read through registry.Resolve, ends up in a
// real event row's project column, and that a path outside any registered
// project comes back unattributed rather than erroring.

func motionEvent(locator string) source.Event {
	return motionEventAt(time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC), locator)
}

// motionEventAt exists because locator is deliberately NOT part of the store's
// natural key — the same observation replayed from a snapshot is the same
// observation — so two events differing only in their locator collapse into
// one on write. A test that needs two distinct locators in the store must
// therefore vary something that IS in the key.
func motionEventAt(at time.Time, locator string) source.Event {
	e := source.NewPoint(at, source.Identity{App: "com.apple.motionapp"}, "motion-autosave")
	e.Locator = locator
	return e
}

func TestResolveLocatorAttributesAFileInsideARegisteredSubdirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	locator := filepath.Join(root, "compositions", "dark-crystal", "v2.motn")
	project, found, err := resolveLocator(locator, nil)
	if err != nil {
		t.Fatalf("resolveLocator(%s): %v", locator, err)
	}
	if !found || project.Project != "Holiday Campaign" {
		t.Errorf("resolveLocator(%s) = %q, %v, want %q, true", locator, project.Project, found, "Holiday Campaign")
	}
}

// TestResolveLocatorAttributesADirectoryLocatorAtTheRegisteredRoot is the
// regression test for the attribution failure task IFmcU found while trying to
// measure per-project engaged time.
//
// Every test above resolves a FILE inside a subdirectory, so taking
// filepath.Dir unconditionally looked correct. Two of the three largest
// sources in the store emit a DIRECTORY locator instead — internal/transcripts
// stores the record's own cwd and internal/gitlog the repository — and for
// those, stripping a level climbs from the registered root's PARENT and finds
// nothing. Measured on the live store on 2026-08-01: 209,467 events sat at a
// locator that is itself a registered project root and attributed to nothing,
// including 100% of the 1,626 gitlog events.
func TestResolveLocatorAttributesADirectoryLocatorAtTheRegisteredRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	project, found, err := resolveLocator(root, nil)
	if err != nil {
		t.Fatalf("resolveLocator(%s): %v", root, err)
	}
	if !found || project.Project != "Holiday Campaign" {
		t.Errorf("resolveLocator(%s) = %q, %v, want %q, true; a locator that IS the registered "+
			"directory must attribute to it", root, project.Project, found, "Holiday Campaign")
	}
}

// TestResolveLocatorOfAVanishedPathClimbsFromItsParent pins the fallback down,
// because a locator that no longer exists cannot be told apart from a file by
// anything on disk. Climbing from the parent is right for a file and one level
// too high for a vanished directory: it can only ever under-attribute, which is
// the direction this project prefers over inventing an attribution.
func TestResolveLocatorOfAVanishedPathClimbsFromItsParent(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	gone := filepath.Join(root, "deleted", "render.motn")
	project, found, err := resolveLocator(gone, nil)
	if err != nil {
		t.Fatalf("resolveLocator(%s): %v", gone, err)
	}
	if !found || project.Project != "Holiday Campaign" {
		t.Errorf("resolveLocator(%s) = %q, %v, want %q, true", gone, project.Project, found, "Holiday Campaign")
	}
}

func TestResolveLocatorReportsUnattributedOutsideAnyRegisteredProject(t *testing.T) {
	locator := filepath.Join(t.TempDir(), "render.motn")
	project, found, err := resolveLocator(locator, nil)
	if err != nil {
		t.Fatalf("resolveLocator(%s): %v", locator, err)
	}
	if found || project.Project != "" {
		t.Errorf("resolveLocator(%s) = %q, %v, want unattributed", locator, project.Project, found)
	}
}

func TestResolveProjectsAttributesAStoredEventAgainstARealMarker(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	st, path := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: "motion-autosave", Input: "/dev/null"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	locator := filepath.Join(root, "compositions", "dark-crystal", "v2.motn")
	if _, err := run.Write(context.Background(), []source.Event{motionEvent(locator)}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var out bytes.Buffer
	if err := resolveProjects(context.Background(), st, nil, &out); err != nil {
		t.Fatalf("resolveProjects: %v", err)
	}

	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Events returned %d rows, want 1 (store at %s)", len(events), path)
	}
	if events[0].Identity.Project != "Holiday Campaign" {
		t.Errorf("project = %q, want Holiday Campaign", events[0].Identity.Project)
	}
}

// TestResolveProjectsSurvivesARealSymlinkCycleAndAttributesTheRest is the
// real-error half of IYXY1: no injected resolver failure, an actual cycle on
// disk, read through the actual registry resolver.
//
// registry.readMarker gets ELOOP from os.ReadFile and surfaces it rather than
// climbing past it, which internal/registry's
// TestResolveTerminatesOnASymlinkCycle pins and which is the right policy — an
// unreadable directory is unknown, not observed-absent. What was wrong was the
// caller turning that one unknown into a total failure of the pass.
func TestResolveProjectsSurvivesARealSymlinkCycleAndAttributesTheRest(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	cycle := t.TempDir()
	a, b := filepath.Join(cycle, "a"), filepath.Join(cycle, "b")
	if err := os.Symlink(b, a); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	if err := os.Symlink(a, b); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	st, _ := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: "motion-autosave", Input: "/dev/null"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	good := filepath.Join(root, "compositions", "dark-crystal", "v2.motn")
	cyclic := filepath.Join(a, "render.motn")
	if _, err := run.Write(context.Background(), []source.Event{
		motionEvent(good),
		motionEventAt(time.Date(2026, 7, 14, 10, 0, 0, 0, time.UTC), cyclic),
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var out bytes.Buffer
	err = resolveProjects(context.Background(), st, nil, &out)
	if err == nil {
		t.Error("resolveProjects = nil error with an unreadable locator in the pass; " +
			"a directory that could not be read is a problem this run had, and harvest counts problems")
	} else if !strings.Contains(err.Error(), cyclic) {
		t.Errorf("error = %v, want it to name %s", err, cyclic)
	}

	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	for _, e := range events {
		switch e.Locator {
		case good:
			if e.Identity.Project != "Holiday Campaign" {
				t.Errorf("project = %q for the readable locator, want Holiday Campaign; "+
					"one cyclic locator must not cost every other locator its attribution", e.Identity.Project)
			}
		case cyclic:
			if e.Identity.Project != "" {
				t.Errorf("project = %q for the cyclic locator, want it left alone", e.Identity.Project)
			}
		}
	}
	if got := out.String(); !strings.Contains(got, "1 unreadable") || !strings.Contains(got, cyclic) {
		t.Errorf("output = %q, want it to count the unreadable locator and name it", got)
	}
}

// TestResolveProjectsPrintsZeroUnreadableWhenNothingWasSkipped is the
// distinguishability requirement at the output layer. A pass that skipped
// nothing must not print what a build that never counted skips would print.
func TestResolveProjectsPrintsZeroUnreadableWhenNothingWasSkipped(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	st, _ := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: "motion-autosave", Input: "/dev/null"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if _, err := run.Write(context.Background(), []source.Event{motionEvent(filepath.Join(root, "v2.motn"))}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var out bytes.Buffer
	if err := resolveProjects(context.Background(), st, nil, &out); err != nil {
		t.Fatalf("resolveProjects: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "1 locator, 1 attributed, 0 unattributed, 0 unreadable") {
		t.Errorf("output = %q, want the three buckets reported against the locator count, "+
			"with the zero stated rather than omitted", got)
	}
}

func TestResolveProjectsLeavesAnUnregisteredEventUnattributed(t *testing.T) {
	st, _ := newStore(t)
	run, err := st.BeginRun(context.Background(), store.RunSpec{SourceID: "motion-autosave", Input: "/dev/null"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	locator := filepath.Join(t.TempDir(), "scratch", "render.motn")
	if _, err := run.Write(context.Background(), []source.Event{motionEvent(locator)}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var out bytes.Buffer
	if err := resolveProjects(context.Background(), st, nil, &out); err != nil {
		t.Fatalf("resolveProjects: %v", err)
	}

	events, err := st.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 || events[0].Identity.Project != "" {
		t.Errorf("events = %+v, want the one event unattributed", events)
	}
}
