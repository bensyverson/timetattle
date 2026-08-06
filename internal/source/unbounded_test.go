package source

import (
	"testing"
	"time"
)

// An observation with one endpoint missing is neither a Point nor an Interval.
// A Point asserts there was no duration; an Interval asserts a particular one.
// Both are false for a Biome record whose partner macOS never wrote, and the
// difference is 6.42h across five confirmed days, so the store has to be able
// to say "this happened, and how long is not known".

func TestOpenStartKnowsWhenItEndedAndNotWhenItBegan(t *testing.T) {
	end := time.Date(2026, 7, 20, 9, 7, 9, 0, time.UTC)
	e := NewOpenStart(end, UnknownSleep, Identity{App: "com.apple.Safari"}, "biome-appusage")

	if e.Shape != Unbounded {
		t.Errorf("Shape = %q, want %q", e.Shape, Unbounded)
	}
	if !e.End.Equal(end) {
		t.Errorf("End = %s, want %s", e.End, end)
	}
	if !e.Start.IsZero() {
		t.Errorf("Start = %s, want the zero time; the beginning was never observed", e.Start)
	}
	if e.Unknown != UnknownSleep {
		t.Errorf("Unknown = %q, want %q", e.Unknown, UnknownSleep)
	}
}

func TestOpenEndKnowsWhenItBeganAndNotWhenItEnded(t *testing.T) {
	start := time.Date(2026, 7, 28, 14, 1, 0, 0, time.UTC)
	e := NewOpenEnd(start, UnknownHorizon, Identity{App: "com.apple.motionapp"}, "biome-appusage")

	if e.Shape != Unbounded {
		t.Errorf("Shape = %q, want %q", e.Shape, Unbounded)
	}
	if !e.Start.Equal(start) {
		t.Errorf("Start = %s, want %s", e.Start, start)
	}
	if !e.End.IsZero() {
		t.Errorf("End = %s, want the zero time; it had not ended when this was observed", e.End)
	}
}

// The two causes need opposite treatment and cannot be told apart after the
// fact. A horizon record resolves itself once the next harvest sees the stop
// macOS had not written yet. A sleep record never resolves, because the start
// was never recorded at all, and the only repair is bounding it by presence.
func TestUnknownReasonSeparatesWhatWillResolveFromWhatNeverWill(t *testing.T) {
	if UnknownHorizon == UnknownSleep {
		t.Fatal("UnknownHorizon and UnknownSleep are equal; they must be distinguishable")
	}
	if !UnknownHorizon.Resolves() {
		t.Error("UnknownHorizon.Resolves() = false, want true; the stop arrives on a later harvest")
	}
	if UnknownSleep.Resolves() {
		t.Error("UnknownSleep.Resolves() = true, want false; macOS never wrote the start")
	}
}

// Not every unpaired record sits at the harvest boundary. A start with later
// records already past it will never be completed by re-reading, so calling it
// a horizon would leave the store waiting on a stop that cannot arrive. It is
// not sleep either: sleep names a specific, observed cause, and asserting it
// here would corrupt the one signal that distinguishes the two.
//
// Never seen in the live stream — 48,047 records classify as 23,832 pairs, 382
// bare stops and exactly one horizon — but the branch that would emit it
// exists, and it has to say something true.
func TestUnknownLostIsNeitherHorizonNorSleepAndNeverResolves(t *testing.T) {
	if UnknownLost == UnknownHorizon || UnknownLost == UnknownSleep {
		t.Fatal("UnknownLost collides with another reason; all three must be distinguishable")
	}
	if UnknownLost.Resolves() {
		t.Error("UnknownLost.Resolves() = true, want false; the partner record was never written")
	}
}

// Every event has one endpoint that was definitely observed, and which one it
// is varies by shape. Callers that order events, or bucket them by day, need
// that instant without caring which field holds it — sorting an unbounded
// event by Start floats every bare stop to the zero time, decades before the
// data it belongs among.
func TestAnchorReportsWhicheverEndpointWasObserved(t *testing.T) {
	start := time.Date(2026, 7, 20, 9, 2, 0, 0, time.UTC)
	end := time.Date(2026, 7, 20, 9, 7, 9, 0, time.UTC)
	id := Identity{App: "com.apple.Safari"}

	cases := []struct {
		name  string
		event Event
		want  time.Time
	}{
		{"point", NewPoint(start, id, "s"), start},
		{"interval", NewInterval(start, end, id, "s"), start},
		{"open start", NewOpenStart(end, UnknownSleep, id, "s"), end},
		{"open end", NewOpenEnd(start, UnknownHorizon, id, "s"), start},
	}
	for _, c := range cases {
		if got := c.event.Anchor(); !got.Equal(c.want) {
			t.Errorf("%s: Anchor() = %s, want %s", c.name, got, c.want)
		}
	}
}

// An unbounded event must not report a duration. Reporting one would be the
// back-dating mistake in a different costume — the measured case charged 5m09s
// of an idle machine to Safari in place of 2s of real use.
func TestUnboundedReportsNoDuration(t *testing.T) {
	e := NewOpenStart(time.Date(2026, 7, 20, 9, 7, 9, 0, time.UTC),
		UnknownSleep, Identity{App: "com.apple.Safari"}, "biome-appusage")

	if d := e.Duration(); d != 0 {
		t.Errorf("Duration() = %s, want 0; the span was never observed", d)
	}
}

func TestUnboundedIsRejectedWhenBothEndpointsAreKnown(t *testing.T) {
	e := Event{
		Start:    time.Date(2026, 7, 20, 9, 7, 4, 0, time.UTC),
		End:      time.Date(2026, 7, 20, 9, 7, 9, 0, time.UTC),
		Shape:    Unbounded,
		Unknown:  UnknownSleep,
		Identity: Identity{App: "com.apple.Safari"},
		Source:   "biome-appusage",
	}
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil, want an error; an unbounded event with both endpoints is an interval")
	}
}

func TestUnboundedIsRejectedWhenNeitherEndpointIsKnown(t *testing.T) {
	e := Event{
		Shape:    Unbounded,
		Unknown:  UnknownSleep,
		Identity: Identity{App: "com.apple.Safari"},
		Source:   "biome-appusage",
	}
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil, want an error; an event observed at no instant at all is not evidence")
	}
}

// Without a reason the store cannot tell a record it should wait for from one
// it must join against presence, so the reason is required rather than a hint.
func TestUnboundedIsRejectedWithoutAReason(t *testing.T) {
	e := Event{
		End:      time.Date(2026, 7, 20, 9, 7, 9, 0, time.UTC),
		Shape:    Unbounded,
		Identity: Identity{App: "com.apple.Safari"},
		Source:   "biome-appusage",
	}
	if err := e.Validate(); err == nil {
		t.Error("Validate() = nil, want an error; an unbounded event must say why its endpoint is missing")
	}
}

// The mirror: a bounded event carrying a reason is a contradiction, and
// tolerating it would let a superseded horizon record keep the marking that
// says it is still waiting to be completed.
func TestBoundedShapesAreRejectedWhenTheyCarryAReason(t *testing.T) {
	at := time.Date(2026, 7, 28, 14, 1, 0, 0, time.UTC)

	point := NewPoint(at, Identity{Document: "dark-crystal-v2"}, "motion-autosave")
	point.Unknown = UnknownHorizon
	if err := point.Validate(); err == nil {
		t.Error("Validate() = nil for a Point with a reason, want an error")
	}

	interval := NewInterval(at, at.Add(time.Hour), Identity{App: "com.apple.motionapp"}, "biome-appusage")
	interval.Unknown = UnknownSleep
	if err := interval.Validate(); err == nil {
		t.Error("Validate() = nil for an Interval with a reason, want an error")
	}
}

// An unbounded event cannot contain another: containment needs both edges, and
// answering "was this save made while that app held focus" from one edge would
// invent the other.
func TestUnboundedContainsNothing(t *testing.T) {
	outer := NewOpenEnd(time.Date(2026, 7, 28, 13, 49, 0, 0, time.UTC),
		UnknownHorizon, Identity{App: "com.apple.motionapp"}, "biome-appusage")
	inner := NewPoint(time.Date(2026, 7, 28, 14, 1, 0, 0, time.UTC),
		Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave")

	if outer.Contains(inner) {
		t.Error("Contains() = true, want false; the interval's far edge was never observed")
	}
}
