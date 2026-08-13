package store_test

import (
	"context"
	"errors"
	"testing"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

func TestResolveProjectsAttributesEventsSharingALocatorInOneCall(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	locator := "/Users/you/Movies/ACORN/holiday/dark-crystal-v2.motn"
	e1 := focus(nine, tenish, "com.apple.motionapp")
	e1.Locator = locator
	e2 := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp"}, sourceID)
	e2.Locator = locator
	f.write(t, run, e1, e2)

	calls := 0
	resolve := func(l string) (store.Attributed, bool, error) {
		calls++
		if l != locator {
			t.Errorf("resolve called with %q, want %q", l, locator)
		}
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	}

	att, err := f.ResolveProjects(context.Background(), resolve)
	if err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	if calls != 1 {
		t.Errorf("resolve was called %d times, want 1; two events sharing one locator must not re-resolve it", calls)
	}
	if att.Updated != 2 {
		t.Errorf("Updated = %d, want 2; both events sharing the locator must be attributed", att.Updated)
	}
	for _, e := range f.all(t) {
		if e.Identity.Project != "holiday-campaign" {
			t.Errorf("event kept project %q, want holiday-campaign: %s", e.Identity.Project, describe(e))
		}
	}
}

func TestResolveProjectsLeavesAnUnregisteredLocatorUnattributed(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := focus(nine, tenish, "com.apple.motionapp")
	e.Locator = "/Users/you/scratch/render.motn"
	f.write(t, run, e)

	resolve := func(string) (store.Attributed, bool, error) { return store.Attributed{}, false, nil }
	if _, err := f.ResolveProjects(context.Background(), resolve); err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	got := f.all(t)
	if len(got) != 1 || got[0].Identity.Project != "" {
		t.Errorf("events = %+v, want the one event's project left empty for an unregistered locator", got)
	}
}

func TestResolveProjectsSkipsEventsWithNoLocator(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := focus(nine, tenish, "com.apple.Safari") // presence-shaped: no Locator
	f.write(t, run, e)

	called := false
	resolve := func(string) (store.Attributed, bool, error) { called = true; return store.Attributed{}, false, nil }
	if _, err := f.ResolveProjects(context.Background(), resolve); err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	if called {
		t.Error("resolve was called for an event with no locator; there is nothing to resolve a path-shaped attribution against")
	}
}

// project is documented in 0001_initial.sql and internal/store's package doc
// as a recomputable cache, never a stored fact, specifically so registering a
// directory after events already exist repairs them without a new harvest.
// This proves that contract rather than just asserting it.
func TestResolveProjectsIsRerunnableAndRepairsAStaleAttribution(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := focus(nine, tenish, "com.apple.motionapp")
	e.Locator = "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	f.write(t, run, e)

	if _, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{}, false, nil
	}); err != nil {
		t.Fatalf("first ResolveProjects: %v", err)
	}
	if got := f.all(t); got[0].Identity.Project != "" {
		t.Fatalf("project = %q before registration, want empty", got[0].Identity.Project)
	}

	att, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	})
	if err != nil {
		t.Fatalf("second ResolveProjects: %v", err)
	}
	if att.Updated != 1 {
		t.Errorf("Updated = %d, want 1", att.Updated)
	}
	if got := f.all(t); got[0].Identity.Project != "holiday-campaign" {
		t.Errorf("project = %q after registration, want holiday-campaign", got[0].Identity.Project)
	}

	att, err = f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	})
	if err != nil {
		t.Fatalf("third ResolveProjects: %v", err)
	}
	if att.Updated != 0 {
		t.Errorf("Updated = %d on an unchanged re-run, want 0", att.Updated)
	}
}

func TestResolveProjectsUnattributesARowWhoseMarkerWasRemoved(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := focus(nine, tenish, "com.apple.motionapp")
	e.Locator = "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	f.write(t, run, e)

	if _, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	}); err != nil {
		t.Fatalf("first ResolveProjects: %v", err)
	}

	att, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{}, false, nil
	})
	if err != nil {
		t.Fatalf("second ResolveProjects: %v", err)
	}
	if att.Updated != 1 {
		t.Errorf("Updated = %d, want 1; a marker going away must repair the row too, not just adding one", att.Updated)
	}
	if got := f.all(t); got[0].Identity.Project != "" {
		t.Errorf("project = %q after the marker was removed, want empty", got[0].Identity.Project)
	}
}

// TestResolveProjectsSkipsAnUnreadableLocatorAndAttributesTheRest is the
// regression test for IYXY1.
//
// It replaces TestResolveProjectsPropagatesAResolverErrorAndTouchesNothing,
// which pinned the defect rather than the contract: it asserted that ONE
// resolver error left the whole store untouched. Measured on a copy of the
// live store on 2026-08-01 (go run ./scripts/attributionskip -poison), one
// symlink-cycle locator planted on one of 882,027 located events cost 880,283
// events their attribution, because the pass returned before writing any of
// them. That is what harvest's own help text promises will not happen: a
// source that cannot be read loses only its own events.
func TestResolveProjectsSkipsAnUnreadableLocatorAndAttributesTheRest(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	readable := "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	unreadable := "/Users/you/git/acorn/locked/render.motn"
	elsewhere := "/Users/you/scratch/render.motn"

	e1 := focus(nine, tenish, "com.apple.motionapp")
	e1.Locator = readable
	e2 := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp"}, sourceID)
	e2.Locator = unreadable
	e3 := source.NewPoint(tenish, source.Identity{App: "com.apple.motionapp"}, sourceID)
	e3.Locator = elsewhere
	f.write(t, run, e1, e2, e3)

	denied := errors.New("permission denied")
	att, err := f.ResolveProjects(context.Background(), func(l string) (store.Attributed, bool, error) {
		switch l {
		case unreadable:
			return store.Attributed{}, false, denied
		case readable:
			return store.Attributed{Project: "holiday-campaign"}, true, nil
		default:
			return store.Attributed{}, false, nil
		}
	})
	if err != nil {
		t.Fatalf("ResolveProjects: %v; one unreadable locator must not fail the pass", err)
	}
	if att.Locators != 3 {
		t.Errorf("Locators = %d, want 3", att.Locators)
	}
	if att.Attributed != 1 || att.Unattributed != 1 || att.Skipped != 1 {
		t.Errorf("Attributed/Unattributed/Skipped = %d/%d/%d, want 1/1/1",
			att.Attributed, att.Unattributed, att.Skipped)
	}
	if sum := att.Attributed + att.Unattributed + att.Skipped; sum != att.Locators {
		t.Errorf("Attributed+Unattributed+Skipped = %d, want it to reconcile with Locators = %d",
			sum, att.Locators)
	}
	if att.Updated != 1 {
		t.Errorf("Updated = %d, want 1; the readable locator's event must be written", att.Updated)
	}
	if len(att.Skips) != 1 {
		t.Fatalf("Skips = %+v, want the one unreadable locator named", att.Skips)
	}
	if att.Skips[0].Locator != unreadable {
		t.Errorf("Skips[0].Locator = %q, want %q", att.Skips[0].Locator, unreadable)
	}
	if !errors.Is(att.Skips[0].Err, denied) {
		t.Errorf("Skips[0].Err = %v, want it to wrap %v", att.Skips[0].Err, denied)
	}
	if att.Skips[0].Events != 1 {
		t.Errorf("Skips[0].Events = %d, want 1; how much a skip costs is part of the report",
			att.Skips[0].Events)
	}

	for _, e := range f.all(t) {
		switch e.Locator {
		case readable:
			if e.Identity.Project != "holiday-campaign" {
				t.Errorf("readable locator kept project %q, want holiday-campaign; "+
					"one unreadable locator must not cost another locator its attribution", e.Identity.Project)
			}
		case elsewhere:
			if e.Identity.Project != "" {
				t.Errorf("unregistered locator got project %q, want empty", e.Identity.Project)
			}
		}
	}
}

// TestResolveProjectsLeavesASkippedLocatorsPreviousAttributionAlone is the
// distinction this whole package exists to keep: a directory that could not be
// read is UNKNOWN, not observed-absent. Writing NULL for it would assert that
// its events belong to no project, which is a claim nothing observed.
func TestResolveProjectsLeavesASkippedLocatorsPreviousAttributionAlone(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := focus(nine, tenish, "com.apple.motionapp")
	e.Locator = "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	f.write(t, run, e)

	if _, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	}); err != nil {
		t.Fatalf("first ResolveProjects: %v", err)
	}

	att, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{}, false, errors.New("permission denied")
	})
	if err != nil {
		t.Fatalf("second ResolveProjects: %v", err)
	}
	if att.Skipped != 1 || att.Updated != 0 {
		t.Errorf("Skipped/Updated = %d/%d, want 1/0", att.Skipped, att.Updated)
	}
	if got := f.all(t); got[0].Identity.Project != "holiday-campaign" {
		t.Errorf("project = %q after an UNREADABLE directory, want holiday-campaign kept; "+
			"un-attributing on an error would state as observed-absent what was never observed",
			got[0].Identity.Project)
	}
}

// TestResolveProjectsReportsZeroSkippedRatherThanNotReportingSkips pins the
// requirement that "nothing was skipped" cannot render the same as "skips were
// never counted". Locators is reported alongside, so a caller can reconcile
// the three buckets against the input arithmetically.
func TestResolveProjectsReportsZeroSkippedRatherThanNotReportingSkips(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := focus(nine, tenish, "com.apple.motionapp")
	e.Locator = "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	f.write(t, run, e)

	att, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	})
	if err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
	if att.Locators != 1 || att.Attributed != 1 || att.Skipped != 0 || len(att.Skips) != 0 {
		t.Errorf("Attribution = %+v, want 1 locator, 1 attributed, 0 skipped", att)
	}
}

// TestResolveProjectsStillAbortsWholeOnAStoreFailure is the other half of the
// decision, and the test that pins the transaction boundary against being
// changed in passing.
//
// A RESOLVER error is one locator's problem, so the pass skips it and carries
// on. A failure of the WRITE is attributable to no locator, and leaves the pass
// unable to say what it managed, so the whole pass rolls back to the last
// complete attribution rather than committing a torn one.
//
// The cancel has to land AFTER the first locator's UPDATE has already run,
// which is why it fires on the second resolve rather than the first. Cancelling
// on the first proves nothing: no write has happened yet, so a pass that
// committed per locator would pass such a test too. Verified by mutation —
// committing each locator separately leaves a.motn attributed here, and leaves
// the earlier version of this test green.
func TestResolveProjectsStillAbortsWholeOnAStoreFailure(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	first := focus(nine, tenish, "com.apple.motionapp")
	first.Locator = "/Users/you/git/acorn/a.motn"
	second := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp"}, sourceID)
	second.Locator = "/Users/you/git/acorn/b.motn"
	f.write(t, run, first, second)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	_, err := f.ResolveProjects(ctx, func(string) (store.Attributed, bool, error) {
		calls++
		if calls > 1 {
			cancel() // the store, not the resolver, is what fails next
		}
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	})
	if err == nil {
		t.Fatal("ResolveProjects = nil error after the store failed mid-pass, want the failure surfaced")
	}
	if calls < 2 {
		t.Fatalf("resolve was called %d times, want at least 2; the test only pins the boundary "+
			"if a write had already succeeded before the failure", calls)
	}
	for _, e := range f.all(t) {
		if e.Identity.Project != "" {
			t.Errorf("project = %q for %s after a store failure, want the whole pass rolled back",
				e.Identity.Project, e.Locator)
		}
	}
}
