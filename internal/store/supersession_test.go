package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// Supersession is a derivation and never a mutation. A horizon — something that
// was still going when the harvest ran — and the complete interval a later
// harvest produces for it are two observations of one thing at two
// completenesses. Both rows persist; the read collapses them.
//
// The reason this is a view rather than a rule for callers to remember is that
// the failure mode of forgetting is silently double-counted hours, which is the
// exact inflation Timetattle exists to catch.

func TestAHorizonIsSupersededByTheIntervalThatCompletesIt(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	first := f.run(t)
	f.write(t, first, source.NewOpenEnd(nine, source.UnknownHorizon, source.Identity{App: "com.apple.motionapp"}, sourceID))
	if err := first.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	second := f.run(t)
	f.write(t, second, focus(nine, tenish, "com.apple.motionapp"))
	if err := second.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if raw := f.count(t); raw != 2 {
		t.Errorf("the table holds %d rows, want 2; neither observation may be edited or dropped", raw)
	}

	got := f.all(t)
	if len(got) != 1 {
		t.Fatalf("the canonical read returned %d events, want 1:\n%v", len(got), got)
	}
	if got[0].Shape != source.Interval || !got[0].End.Equal(tenish) {
		t.Errorf("the surviving event is %s; the completing interval must win", describe(got[0]))
	}
}

// The raw table is still reachable, under a name nobody reads by accident.
func TestEveryObservationIncludingSupersededIsStillReadable(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	f.write(t, run,
		source.NewOpenEnd(nine, source.UnknownHorizon, source.Identity{App: "com.apple.motionapp"}, sourceID),
		focus(nine, tenish, "com.apple.motionapp"),
	)

	got, err := f.EventsIncludingSuperseded(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("EventsIncludingSuperseded: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("read %d observations, want both:\n%v", len(got), got)
	}
	var horizons int
	for _, e := range got {
		if e.Unknown == source.UnknownHorizon {
			horizons++
		}
	}
	if horizons != 1 {
		t.Errorf("the superseded horizon is not visible; provenance reads exist to see exactly that")
	}
}

// The dangerous case, and the reason the rule is narrower than "prefer the most
// complete observation". A bare stop is a machine waking with an application
// already frontmost: macOS wrote no start and no later harvest will supply one,
// so nothing ever completes it. 6.42h across five confirmed days hangs off these,
// and a rule general enough to collapse them would silently delete it.
func TestABareStopIsNeverSuperseded(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	// An interval that ends at the very instant the bare stop was seen — the
	// most collapsible-looking pair in the data, and still two observations.
	f.write(t, run,
		source.NewOpenStart(tenish, source.UnknownSleep, source.Identity{App: "com.apple.Safari"}, sourceID),
		focus(nine, tenish, "com.apple.Safari"),
	)

	got := f.all(t)
	if len(got) != 2 {
		t.Fatalf("the canonical read returned %d events, want both:\n%v", len(got), got)
	}
	var stops int
	for _, e := range got {
		if e.Unknown == source.UnknownSleep {
			stops++
		}
	}
	if stops != 1 {
		t.Error("the bare stop was collapsed; its repair is bounding against presence, permanently")
	}
}

func TestAHorizonSurvivesAnIntervalThatDoesNotComplete(t *testing.T) {
	horizon := func(at time.Time, id source.Identity, from string) source.Event {
		return source.NewOpenEnd(at, source.UnknownHorizon, id, from)
	}
	motion := source.Identity{App: "com.apple.motionapp"}

	cases := []struct {
		name     string
		horizon  source.Event
		interval source.Event
	}{
		{
			// The interval begins somewhere else, so it is a different
			// observation that happens to concern the same application.
			name:     "a different start",
			horizon:  horizon(nine, motion, sourceID),
			interval: focus(nine.Add(time.Minute), tenish, "com.apple.motionapp"),
		},
		{
			name:     "a different application",
			horizon:  horizon(nine, motion, sourceID),
			interval: focus(nine, tenish, "com.apple.Safari"),
		},
		{
			name:     "a different source",
			horizon:  horizon(nine, motion, "knowledgec-appusage"),
			interval: focus(nine, tenish, "com.apple.motionapp"),
		},
		{
			// Presence carries no application, so the reading is the only
			// thing that distinguishes one observation from another. A view
			// grouping on (source, app) alone would collapse a lit horizon
			// into a dark interval.
			name:     "a different reading",
			horizon:  withState(horizon(nine, source.Identity{}, "knowledgec-backlight"), "on"),
			interval: withState(source.NewInterval(nine, tenish, source.Identity{}, "knowledgec-backlight"), "off"),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newStore(t)
			run := f.run(t)
			f.write(t, run, c.horizon, c.interval)

			got := f.all(t)
			if len(got) != 2 {
				t.Fatalf("the canonical read returned %d events, want both:\n%v", len(got), got)
			}
		})
	}
}

func withState(e source.Event, state string) source.Event {
	e.State = state
	return e
}
