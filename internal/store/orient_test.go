package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// LocatorCounts exists so `timetattle orient` can census the store without
// reading every event into a source.Event: see cmd/timetattle/orient.go's
// orientCensusOf, which is the caller these tests are written against. It must
// answer exactly what that function currently gets by scanning Events() by
// hand: the store's total size and span, how many unattributed observations
// carry no locator at all, and how many carry each distinct locator that does.

// The reconciliation property: every unattributed observation is either
// counted in NoPath or in exactly one entry of ByLocator, and nothing else is
// double counted or dropped.
func TestLocatorCountsReconcilesUnattributedObservations(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	locatorA := "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	locatorB := "/Users/you/git/theaitrellis/README.md"
	attributed := focus(nine, tenish, "com.apple.motionapp")
	attributed.Locator = locatorA // will be attributed below

	e1 := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp"}, sourceID)
	e1.Locator = locatorA
	e2 := source.NewPoint(nine.Add(time.Minute), source.Identity{App: "com.apple.motionapp"}, sourceID)
	e2.Locator = locatorA
	e3 := source.NewPoint(nine.Add(2*time.Minute), source.Identity{App: "com.apple.motionapp"}, sourceID)
	e3.Locator = locatorB

	noLocator1 := source.NewInterval(nine, tenish, source.Identity{}, "knowledgec-backlight")
	noLocator2 := source.NewInterval(tenish, tenish.Add(time.Hour), source.Identity{}, "knowledgec-backlight")

	f.write(t, run, e1, e2, e3, noLocator1, noLocator2)

	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}

	if got.NoPath != 2 {
		t.Errorf("NoPath = %d, want 2", got.NoPath)
	}
	if want := map[string]int{locatorA: 2, locatorB: 1}; !mapsEqual(got.ByLocator, want) {
		t.Errorf("ByLocator = %v, want %v", got.ByLocator, want)
	}

	// The reconciliation itself: sum(ByLocator) + NoPath must equal every
	// unattributed observation, with nothing folded together and nothing
	// dropped.
	sum := got.NoPath
	for _, n := range got.ByLocator {
		sum += n
	}
	if sum != 5 {
		t.Errorf("NoPath + sum(ByLocator) = %d, want 5 (every unattributed observation written)", sum)
	}
}

// An event a project has already been attributed to must not appear in
// ByLocator or NoPath at all: LocatorCounts only classifies what still needs
// classifying, exactly like the loop in orientCensusOf it replaces.
func TestLocatorCountsExcludesAttributedEvents(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	locator := "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	e := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp"}, sourceID)
	e.Locator = locator
	f.write(t, run, e)

	if _, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	}); err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}

	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}
	if got.NoPath != 0 {
		t.Errorf("NoPath = %d, want 0; the only event is attributed", got.NoPath)
	}
	if len(got.ByLocator) != 0 {
		t.Errorf("ByLocator = %v, want empty; an attributed locator must not be reported as needing classification", got.ByLocator)
	}
	// But it still counts toward Observations: attribution does not shrink the
	// store.
	if got.Observations != 1 {
		t.Errorf("Observations = %d, want 1", got.Observations)
	}
}

// Observations and the Earliest/Latest span cover EVERY event in the store,
// attributed or not — orient reports the whole store's size and age on its
// first line, before it ever gets to what is unattributed.
func TestLocatorCountsObservationsAndSpanCoverAttributedEventsToo(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	locator := "/Users/you/git/acorn/holiday/dark-crystal-v2.motn"
	attributedEvent := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp"}, sourceID)
	attributedEvent.Locator = locator
	f.write(t, run, attributedEvent, focus(tenish, tenish.Add(time.Hour), "com.apple.Safari"))

	if _, err := f.ResolveProjects(context.Background(), func(string) (store.Attributed, bool, error) {
		return store.Attributed{Project: "holiday-campaign"}, true, nil
	}); err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}

	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}
	if got.Observations != 2 {
		t.Errorf("Observations = %d, want 2", got.Observations)
	}
	if !got.Earliest.Equal(nine) {
		t.Errorf("Earliest = %v, want %v", got.Earliest, nine)
	}
	// Anchor() prefers Start whenever it is set, even on a complete interval,
	// so the second event anchors on tenish (its Start) and not
	// tenish.Add(time.Hour) (its End). That is not this test's property —
	// TestLocatorCountsSpanAnchorsABareStopAndAHorizonCorrectly is — but the
	// expectation has to agree with it or this test is asserting a span
	// LocatorCounts was never asked to produce.
	if want := tenish; !got.Latest.Equal(want) {
		t.Errorf("Latest = %v, want %v", got.Latest, want)
	}
}

// A bare stop (open start, no end observed) anchors on its Start; a horizon
// (open end, no start observed) anchors on its End — exactly source.Event's
// own Anchor(), which orientCensusOf currently calls directly. LocatorCounts
// must derive the same span in SQL without ever materialising an Event.
func TestLocatorCountsSpanAnchorsABareStopAndAHorizonCorrectly(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	bareStop := source.NewOpenStart(nine, source.UnknownSleep, source.Identity{App: "bare-stop"}, sourceID)
	horizon := source.NewOpenEnd(tenish, source.UnknownHorizon, source.Identity{App: "horizon"}, sourceID)
	f.write(t, run, bareStop, horizon)

	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}
	if !got.Earliest.Equal(nine) {
		t.Errorf("Earliest = %v, want %v (the bare stop's Start)", got.Earliest, nine)
	}
	if !got.Latest.Equal(tenish) {
		t.Errorf("Latest = %v, want %v (the horizon's End)", got.Latest, tenish)
	}
}

// canonical_event, not the raw table: a horizon a later harvest completed must
// not be double counted, the same rule Events() already follows.
func TestLocatorCountsReadsTheCanonicalViewNotTheRawTable(t *testing.T) {
	f := newStore(t)
	first := f.run(t)
	f.write(t, first, source.NewOpenEnd(nine, source.UnknownHorizon, source.Identity{App: "com.apple.motionapp"}, sourceID))

	second := f.run(t)
	f.write(t, second, focus(nine, tenish, "com.apple.motionapp"))

	// Two rows physically exist (proven elsewhere in this package by
	// TestAnObservationCompletedLaterDoesNotReplaceTheEarlierOne), but the
	// canonical view collapses the superseded horizon.
	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}
	if got.Observations != 1 {
		t.Errorf("Observations = %d, want 1; the completed interval must supersede the horizon exactly as Events() does", got.Observations)
	}
}

// An empty store is a legitimate answer, not an error, and must say zero
// rather than crash on an aggregate over no rows.
func TestLocatorCountsOnAnEmptyStore(t *testing.T) {
	f := newStore(t)
	got, err := f.LocatorCounts(context.Background())
	if err != nil {
		t.Fatalf("LocatorCounts: %v", err)
	}
	if got.Observations != 0 {
		t.Errorf("Observations = %d, want 0", got.Observations)
	}
	if !got.Earliest.IsZero() || !got.Latest.IsZero() {
		t.Errorf("Earliest=%v Latest=%v, want both zero on an empty store", got.Earliest, got.Latest)
	}
	if got.NoPath != 0 || len(got.ByLocator) != 0 {
		t.Errorf("NoPath=%d ByLocator=%v, want both empty on an empty store", got.NoPath, got.ByLocator)
	}
}

func mapsEqual(a, b map[string]int) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
