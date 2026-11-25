package store_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// located is one observation at locator. The app varies with the locator
// because locator is not part of the natural key, so two events that differed
// only in it would collapse into one on write.
func located(locator string) source.Event {
	e := focus(nine, tenish, locator)
	e.Locator = locator
	return e
}

func projectsByLocator(t *testing.T, events []source.Event) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, e := range events {
		out[e.Locator] = e.Identity.Project
	}
	return out
}

func TestResolveProjectsUnderTouchesOnlyTheNamedDirectoriesAndBeneath(t *testing.T) {
	f := newStore(t)
	f.write(t, f.run(t),
		located("/work/site"),
		located("/work/site/a.motn"),
		located("/work/site/deep/b.motn"),
		located("/work/site-old/c.motn"),
		located("/work/sitf"),
		located("/work/other/d.motn"),
		located("/gone/site/e.motn"),
	)

	var asked []string
	att, err := f.ResolveProjectsUnder(context.Background(), []string{"/work/site", "/gone/site"},
		func(l string) (store.Attributed, bool, error) {
			asked = append(asked, l)
			return store.Attributed{Project: "p"}, true, nil
		})
	if err != nil {
		t.Fatalf("ResolveProjectsUnder: %v", err)
	}

	slices.Sort(asked)
	want := []string{"/gone/site/e.motn", "/work/site", "/work/site/a.motn", "/work/site/deep/b.motn"}
	if !slices.Equal(asked, want) {
		t.Errorf("resolved %q, want %q", asked, want)
	}
	if att.Locators != 4 || att.Attributed != 4 || att.Updated != 4 {
		t.Errorf("Locators, Attributed, Updated = %d, %d, %d, want 4, 4, 4",
			att.Locators, att.Attributed, att.Updated)
	}
	if att.Events["p"] != 4 {
		t.Errorf("Events = %v, want 4 observations under p", att.Events)
	}
	got := projectsByLocator(t, f.all(t))
	for _, outside := range []string{"/work/site-old/c.motn", "/work/sitf", "/work/other/d.motn"} {
		if got[outside] != "" {
			t.Errorf("%s was attributed to %q; it is not under either directory", outside, got[outside])
		}
	}
}

// A directory listed twice, or one inside another, must not resolve or count a
// locator twice.
func TestResolveProjectsUnderCountsAnOverlappedLocatorOnce(t *testing.T) {
	f := newStore(t)
	f.write(t, f.run(t), located("/work/site/deep/b.motn"))

	calls := 0
	att, err := f.ResolveProjectsUnder(context.Background(), []string{"/work/site", "/work/site/deep", "/work/site"},
		func(string) (store.Attributed, bool, error) {
			calls++
			return store.Attributed{Project: "p"}, true, nil
		})
	if err != nil {
		t.Fatalf("ResolveProjectsUnder: %v", err)
	}
	if calls != 1 || att.Locators != 1 || att.Updated != 1 {
		t.Errorf("calls, Locators, Updated = %d, %d, %d, want 1, 1, 1", calls, att.Locators, att.Updated)
	}
}

func TestResolveProjectsUnderSkipsAnUnreadableLocatorAndAttributesTheRest(t *testing.T) {
	f := newStore(t)
	f.write(t, f.run(t), located("/work/site/bad"), located("/work/site/good"))

	denied := errors.New("denied")
	att, err := f.ResolveProjectsUnder(context.Background(), []string{"/work/site"},
		func(l string) (store.Attributed, bool, error) {
			if strings.HasSuffix(l, "bad") {
				return store.Attributed{}, false, denied
			}
			return store.Attributed{Project: "p"}, true, nil
		})
	if err != nil {
		t.Fatalf("ResolveProjectsUnder: %v", err)
	}
	if att.Skipped != 1 || len(att.Skips) != 1 || !errors.Is(att.Skips[0].Err, denied) {
		t.Errorf("Skipped = %d, Skips = %+v, want the one unreadable locator", att.Skipped, att.Skips)
	}
	if got := projectsByLocator(t, f.all(t))["/work/site/good"]; got != "p" {
		t.Errorf("the readable locator has project %q, want p", got)
	}
}

// Resolving climbs the filesystem and may spawn git, so no write lock may be
// held while it runs: another writer has to be able to get in.
func TestResolveProjectsUnderHoldsNoWriteLockWhileResolving(t *testing.T) {
	f := newStore(t)
	f.write(t, f.run(t), located("/work/site/a"))

	other, err := store.Open(f.Path())
	if err != nil {
		t.Fatalf("open a second connection: %v", err)
	}
	defer other.Close()

	calls := 0
	_, err = f.ResolveProjectsUnder(context.Background(), []string{"/work/site"},
		func(string) (store.Attributed, bool, error) {
			calls++
			if _, err := other.BeginRun(context.Background(),
				store.RunSpec{SourceID: sourceID, Input: "/dev/null"}); err != nil {
				t.Errorf("a second writer was locked out during resolution: %v", err)
			}
			return store.Attributed{Project: "p"}, true, nil
		})
	if err != nil {
		t.Fatalf("ResolveProjectsUnder: %v", err)
	}
	if calls != 1 {
		t.Errorf("resolve was called %d times, want 1", calls)
	}
}

// The point of limiting by path is not to read the whole store, so both halves
// of the listing have to search the locator index rather than scan.
func TestTheListingUnderADirectorySearchesTheLocatorIndex(t *testing.T) {
	plan := planFor(t, store.LocatorsUnderQuery, "/work/site", "/work/site/", "/work/site0")
	// Both bounds, because a bare "locator IS NOT NULL" is also reported as a
	// search of this index and reads every located row.
	if !strings.Contains(plan, "event_locator (locator>? AND locator<?)") ||
		!strings.Contains(plan, "event_locator (locator=?)") || strings.Contains(plan, "SCAN event") {
		t.Errorf("plan does not search event_locator by value and by range:\n%s", plan)
	}
}
