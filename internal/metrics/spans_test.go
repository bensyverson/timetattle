package metrics

import (
	"testing"
	"time"
)

// The interval arithmetic every figure in this package is a sum over. It is
// tested directly because the two-pointer walks are the kind of code that is
// wrong only on the awkward input — a cut spanning several spans, a cut ending
// exactly where a span begins — and a wrong answer there would show up as a
// total that is quietly a few minutes off rather than as a failure.

func t0(m int) time.Time {
	return time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC).Add(time.Duration(m) * time.Minute)
}

func run(pairs ...int) []span {
	var out []span
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, span{t0(pairs[i]), t0(pairs[i+1])})
	}
	return out
}

func minutes(in []span) []int {
	out := make([]int, 0, 2*len(in))
	for _, s := range in {
		out = append(out, int(s.start.Sub(t0(0))/time.Minute), int(s.end.Sub(t0(0))/time.Minute))
	}
	return out
}

func equal(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestUnionDropsTheEmptySpansAPointObservationProduces(t *testing.T) {
	got := minutes(union(run(0, 0, 5, 10, 8, 12, 20, 20, 30, 40)))
	if want := []int{5, 12, 30, 40}; !equal(got, want) {
		t.Errorf("union = %v, want %v; a point covers no clock and must not appear as a span", got, want)
	}
}

func TestUnionMergesSpansThatMerelyTouch(t *testing.T) {
	got := minutes(union(run(0, 10, 10, 20)))
	if want := []int{0, 20}; !equal(got, want) {
		t.Errorf("union = %v, want %v", got, want)
	}
}

func TestIntersectWalksBothRunsOnce(t *testing.T) {
	got := minutes(intersect(run(0, 10, 20, 30, 40, 50), run(5, 25, 45, 60)))
	if want := []int{5, 10, 20, 25, 45, 50}; !equal(got, want) {
		t.Errorf("intersect = %v, want %v", got, want)
	}
}

func TestIntersectTreatsATouchingEndpointAsNoOverlap(t *testing.T) {
	if got := intersect(run(0, 10), run(10, 20)); len(got) != 0 {
		t.Errorf("intersect = %v, want nothing: the spans are half-open and share no instant", minutes(got))
	}
}

// The case the two-pointer gets wrong if it consumes its cursor: one cut
// covering the middle of several spans has to be applied to every one of them.
func TestSubtractAppliesOneLongCutToEverySpanItCovers(t *testing.T) {
	got := minutes(subtract(run(0, 10, 20, 30, 40, 50), run(5, 45)))
	if want := []int{0, 5, 45, 50}; !equal(got, want) {
		t.Errorf("subtract = %v, want %v", got, want)
	}
}

func TestSubtractCutsAHoleAndKeepsBothSides(t *testing.T) {
	got := minutes(subtract(run(0, 60), run(10, 20, 30, 40)))
	if want := []int{0, 10, 20, 30, 40, 60}; !equal(got, want) {
		t.Errorf("subtract = %v, want %v", got, want)
	}
}

func TestSubtractingNothingLeavesTheRunAlone(t *testing.T) {
	got := minutes(subtract(run(0, 10, 20, 30), nil))
	if want := []int{0, 10, 20, 30}; !equal(got, want) {
		t.Errorf("subtract = %v, want %v", got, want)
	}
}

// carve's contract is that no piece straddles any cut it was given, which is
// what lets every later question be answered with a yes or a no rather than a
// fraction. Cuts outside the run are ignored rather than creating pieces.
func TestCarveCutsAtEveryInstantInsideTheRunAndNoneOutsideIt(t *testing.T) {
	got := minutes(carve(run(10, 40, 50, 60), []time.Time{t0(0), t0(20), t0(30), t0(45), t0(70)}))
	if want := []int{10, 20, 20, 30, 30, 40, 50, 60}; !equal(got, want) {
		t.Errorf("carve = %v, want %v", got, want)
	}
}

func TestCarveMakesNoEmptyPieceFromACutOnAnEndpoint(t *testing.T) {
	got := minutes(carve(run(10, 20), []time.Time{t0(10), t0(20)}))
	if want := []int{10, 20}; !equal(got, want) {
		t.Errorf("carve = %v, want %v", got, want)
	}
}

// A cursor is only ever asked about pieces carve produced, so every answer is
// whole. This pins that it stays monotonic across a sequence rather than
// re-scanning or losing its place.
func TestACursorAnswersASequenceOfPiecesInOnePass(t *testing.T) {
	c := &cursor{spans: run(10, 20, 40, 50)}
	pieces := carve(run(0, 60), []time.Time{t0(10), t0(20), t0(40), t0(50)})
	var covered []bool
	for _, p := range pieces {
		covered = append(covered, c.covers(p))
	}
	want := []bool{false, true, false, true, false}
	if len(covered) != len(want) {
		t.Fatalf("carve produced %d pieces, want %d", len(covered), len(want))
	}
	for i := range want {
		if covered[i] != want[i] {
			t.Errorf("piece %d (%v) covered = %v, want %v", i, minutes(pieces[i:i+1]), covered[i], want[i])
		}
	}
}
