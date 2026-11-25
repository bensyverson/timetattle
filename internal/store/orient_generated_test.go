package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/generated"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// The census is a report like any other, so stored generated files stay out of
// every figure in it: the total, the span and the unattributed locators.
func TestLocatorCountsLeavesOutStoredGeneratedFiles(t *testing.T) {
	f := newStore(t)
	run, err := f.BeginRun(context.Background(), store.RunSpec{SourceID: generated.Source, Input: "/tmp/walk"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	at := func(offset time.Duration, sourceID, locator string) source.Event {
		e := source.NewPoint(nine.Add(offset), source.Identity{Document: locator}, sourceID)
		e.Locator = locator
		return e
	}

	const (
		kept    = "/Users/you/git/app/main.swift"
		session = "/Users/you/git/app/.build/checkouts/dep"
	)
	f.write(t, run,
		// Both ends of the span, so that either one leaking through moves it.
		at(-time.Hour, generated.Source, "/Users/you/git/app/.build/debug/early.o"),
		at(time.Hour, generated.Source, "/Users/you/git/app/.build/debug/late.o"),
		at(0, generated.Source, kept),
		at(2*time.Second, generated.Source, "/Users/you/git/app/.venv/lib/site.py"),
		at(3*time.Second, generated.Source, "/Users/you/git/app/pkg/__pycache__/x.pyc"),
		at(4*time.Second, generated.Source, "/Users/you/git/app/.ruff_cache/x"),
		// No locator: the filter must not lose a row it cannot judge.
		at(time.Minute, generated.Source, ""),
	)
	// Another source's locator under .build records somebody working there.
	f.write(t, f.run(t), at(2*time.Minute, sourceID, session))

	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}
	if got.Observations != 3 {
		t.Errorf("Observations = %d, want 3", got.Observations)
	}
	if !got.Earliest.Equal(nine) || !got.Latest.Equal(nine.Add(2*time.Minute)) {
		t.Errorf("span = %s..%s, want %s..%s", stamp(got.Earliest), stamp(got.Latest),
			stamp(nine), stamp(nine.Add(2*time.Minute)))
	}
	if got.NoPath != 1 {
		t.Errorf("NoPath = %d, want 1", got.NoPath)
	}
	if want := map[string]int{kept: 1, session: 1}; !mapsEqual(got.ByLocator, want) {
		t.Errorf("ByLocator = %v, want %v", got.ByLocator, want)
	}
}
