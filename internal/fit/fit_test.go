package fit_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
)

// origin is an arbitrary instant every synthetic corpus below is written
// relative to. Offsets are what the tests are about; the wall clock is not.
var origin = time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)

func at(offset time.Duration) time.Time { return origin.Add(offset) }

// mark is one point observation, the shape almost every source Timetattle reads
// produces: a commit, a save, a message. It covers no duration at all, which is
// why the margins decide so much.
func mark(offset time.Duration) source.Event {
	return source.Event{Start: at(offset), End: at(offset), Shape: source.Point, Source: "test-marks"}
}

// observed is an evidence interval — a focus interval, say — which is the one
// shape that contributes duration a person can be charged for without the
// margins inventing it.
func observed(from, to time.Duration) source.Event {
	return source.Event{Start: at(from), End: at(to), Shape: source.Interval, Source: "test-marks"}
}

// lit and dark are presence readings. Presence bounds and never creates.
func lit(from, to time.Duration) source.Event  { return presence("on", from, to) }
func dark(from, to time.Duration) source.Event { return presence("off", from, to) }

func presence(state string, from, to time.Duration) source.Event {
	return source.Event{
		Start: at(from), End: at(to), Shape: source.Interval,
		Source: "test-presence", State: state,
	}
}

func params(gap, lead, tail time.Duration) sittings.Params {
	return sittings.Params{
		Gap: gap, LeadIn: lead, TailOut: tail,
		PresenceSources: []string{"test-presence"},
	}
}

// spansOf renders a derivation as offsets from origin, so a failure message
// reads as "[-5m,7m]" rather than as two RFC3339 timestamps.
func spansOf(s []sittings.Sitting) []string {
	out := make([]string, 0, len(s))
	for _, x := range s {
		out = append(out, "["+fit.Compact(x.Start.Sub(origin))+","+fit.Compact(x.End.Sub(origin))+"]")
	}
	return out
}

func cluster(t *testing.T, events []source.Event, p sittings.Params) []sittings.Sitting {
	t.Helper()
	res, err := sessions.Cluster(events, p)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	return res.Sessions
}

// THE PREMISE OF THIS WHOLE PACKAGE, checked against the clusterer rather than
// quoted from a document.
//
// Point.Inert claims the gap decides nothing at or below lead-in plus tail-out.
// If that is wrong, every inert point this package excludes from candidacy is a
// real candidate it silently dropped. So the rule is tested where it lives: a
// corpus with separations either side of the floor, clustered at every gap from
// one minute to twenty, and the prediction is that the derivation is identical
// to the boundary point's for exactly the gaps Inert calls inert.
func TestTheInertRuleHoldsInTheClusterer(t *testing.T) {
	// Separations of 3m, 8m, 11m, 13m and 30m: two below the 10m floor, three
	// above it, so the floor is bracketed rather than approached from one side.
	events := []source.Event{mark(0), mark(3 * time.Minute), mark(11 * time.Minute),
		mark(22 * time.Minute), mark(35 * time.Minute), mark(65 * time.Minute)}

	const lead, tail = 5 * time.Minute, 5 * time.Minute
	boundary := spansOf(cluster(t, events, params(lead+tail, lead, tail)))

	for m := 1; m <= 20; m++ {
		gap := time.Duration(m) * time.Minute
		p := fit.Point{Gap: gap, LeadIn: lead, TailOut: tail}
		same := slices.Equal(spansOf(cluster(t, events, params(gap, lead, tail))), boundary)
		if same != p.Inert() {
			t.Errorf("gap %v: Inert()=%v but the derivation is %s the boundary point's; got %v, boundary %v",
				gap, p.Inert(), map[bool]string{true: "identical to", false: "different from"}[same],
				spansOf(cluster(t, events, params(gap, lead, tail))), boundary)
		}
	}
}

func TestCanonicalCollapsesAnInertPointOntoTheBoundary(t *testing.T) {
	inert := fit.Point{Gap: 2 * time.Minute, LeadIn: 5 * time.Minute, TailOut: 3 * time.Minute}
	if got, want := inert.Canonical(), (fit.Point{Gap: 8 * time.Minute, LeadIn: 5 * time.Minute, TailOut: 3 * time.Minute}); got != want {
		t.Errorf("Canonical of an inert point = %v, want %v", got, want)
	}
	live := fit.Point{Gap: 15 * time.Minute, LeadIn: 5 * time.Minute, TailOut: 3 * time.Minute}
	if got := live.Canonical(); got != live {
		t.Errorf("Canonical of a live point = %v, want it unchanged at %v", got, live)
	}
}

func TestGridPointsAreEveryCombinationDeduplicated(t *testing.T) {
	g := fit.Grid{
		Gaps:     []time.Duration{15 * time.Minute, 5 * time.Minute, 15 * time.Minute},
		LeadIns:  []time.Duration{0, time.Minute},
		TailOuts: []time.Duration{time.Minute},
	}
	got := g.Points()
	want := []fit.Point{
		{Gap: 5 * time.Minute, LeadIn: 0, TailOut: time.Minute},
		{Gap: 5 * time.Minute, LeadIn: time.Minute, TailOut: time.Minute},
		{Gap: 15 * time.Minute, LeadIn: 0, TailOut: time.Minute},
		{Gap: 15 * time.Minute, LeadIn: time.Minute, TailOut: time.Minute},
	}
	if !slices.Equal(got, want) {
		t.Errorf("Points() = %v, want %v", got, want)
	}
}

func TestGridSplitSeparatesLiveFromInert(t *testing.T) {
	g := fit.Grid{
		Gaps:     []time.Duration{2 * time.Minute, 15 * time.Minute},
		LeadIns:  []time.Duration{5 * time.Minute},
		TailOuts: []time.Duration{5 * time.Minute},
	}
	live, inert := g.Split()
	if len(live) != 1 || live[0].Gap != 15*time.Minute {
		t.Errorf("live = %v, want only the 15m gap", live)
	}
	if len(inert) != 1 || inert[0].Gap != 2*time.Minute {
		t.Errorf("inert = %v, want only the 2m gap", inert)
	}
}

// A grid that is entirely inert is not a narrow search, it is no search: every
// point in it is the same model. Refusing it by name is the difference between
// a fitter that says so and one that returns an arbitrary member of a plateau.
func TestGridValidateRefusesAnEntirelyInertGrid(t *testing.T) {
	g := fit.Grid{
		Gaps:     []time.Duration{time.Minute, 5 * time.Minute},
		LeadIns:  []time.Duration{5 * time.Minute},
		TailOuts: []time.Duration{5 * time.Minute},
	}
	err := g.Validate()
	if err == nil {
		t.Fatal("Validate accepted a grid every point of which has an inert gap")
	}
	if !strings.Contains(err.Error(), "10m") {
		t.Errorf("the complaint does not name the floor the gap has to clear: %v", err)
	}
}

func TestGridValidateRefusesAnEmptyAxis(t *testing.T) {
	g := fit.Grid{Gaps: []time.Duration{15 * time.Minute}, LeadIns: []time.Duration{0}}
	if err := g.Validate(); err == nil {
		t.Fatal("Validate accepted a grid naming no tail-outs")
	}
}

func TestCompactDropsTheZeroTail(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{{0, "0"}, {5 * time.Minute, "5m"}, {time.Hour, "1h"}, {90 * time.Second, "1m30s"}} {
		if got := fit.Compact(c.in); got != c.want {
			t.Errorf("Compact(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
