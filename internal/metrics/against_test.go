package metrics_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// One project measured against sittings derived somewhere else.
//
// THE WHOLE POINT IS THAT IT IS THE SAME ANSWER. If Against were a second way
// of computing engaged time it would be a second system, and the two would
// disagree the first time either changed. It reduces one group exactly as
// Compute reduces every group, so the constructed case below is an equality and
// not an approximation.

// sittingsOf is the report's own presence, handed back as the spans a caller
// would have read out of derived_session.
func sittingsOf(r metrics.Report) []timespan.Span {
	out := make([]timespan.Span, 0, len(r.Sessions))
	for _, s := range r.Sessions {
		out = append(out, timespan.Span{Start: s.Start, End: s.End})
	}
	return out
}

func TestAgainstGivesTheSameFigureComputeDoes(t *testing.T) {
	events := []source.Event{
		point(at(9, 0), "acorn"),
		point(at(9, 10), "acorn"),
		point(at(9, 5), "birch"),
		point(at(13, 0), "acorn"),
		unlocatable(at(14, 0), at(15, 0)),
	}
	report := compute(t, events)

	got, err := metrics.Against("acorn", events, sittingsOf(report), testParams())
	if err != nil {
		t.Fatalf("Against: %v", err)
	}
	want := projectOf(t, report, "acorn")
	if got.Project.Engaged.Total != want.Engaged.Total {
		t.Errorf("Against credits acorn %v, and Compute %v; they must be one measurement and not two",
			got.Project.Engaged.Total, want.Engaged.Total)
	}
	if got.Project.Attested != want.Attested || got.Project.Weak != want.Weak {
		t.Errorf("Against splits acorn as attested %v / weak %v against Compute's %v / %v",
			got.Project.Attested, got.Project.Weak, want.Attested, want.Weak)
	}
	if got.Project.Corroborated != want.Corroborated {
		t.Errorf("Against corroborates %v against Compute's %v",
			got.Project.Corroborated, want.Corroborated)
	}
	if got.Sittings.Count != want.Sittings.Count {
		t.Errorf("Against counts %d sittings for acorn, Compute %d",
			got.Sittings.Count, want.Sittings.Count)
	}
	if got.Presence != report.Presence.Total {
		t.Errorf("Against reports %v of presence against the report's %v",
			got.Presence, report.Presence.Total)
	}
}

// A project's own cluster is CLIPPED by the sittings it is measured against and
// never added to them. Handing it a shorter derivation than its evidence
// reaches must shrink the answer, or the caller would be quietly re-deriving
// presence out of one project's evidence.
func TestAgainstIsClippedByTheSittingsItIsGiven(t *testing.T) {
	events := []source.Event{point(at(9, 0), "acorn"), point(at(13, 0), "acorn")}

	got, err := metrics.Against("acorn", events,
		[]timespan.Span{{Start: at(8, 0), End: at(10, 0)}}, testParams())
	if err != nil {
		t.Fatalf("Against: %v", err)
	}
	// 08:55..09:05 falls inside the sitting given; 12:55..13:05 does not.
	if want := 10 * time.Minute; got.Project.Engaged.Total != want {
		t.Errorf("Against credits acorn %v, want %v; the second cluster is outside the sittings "+
			"it was measured against", got.Project.Engaged.Total, want)
	}
}

// A project with no evidence at all in the window returns a measured zero AND
// the count that says why. Zero hours and zero observations are different
// facts, and a headline that could not tell them apart would report a quiet day
// where nothing was collected.
func TestAgainstCountsTheObservationsItsFigureRestsOn(t *testing.T) {
	events := []source.Event{point(at(9, 0), "acorn")}
	report := compute(t, events)

	silent, err := metrics.Against("birch", events, sittingsOf(report), testParams())
	if err != nil {
		t.Fatalf("Against: %v", err)
	}
	if silent.Events != 0 || silent.Project.Engaged.Total != 0 {
		t.Errorf("birch has %d observations and %v engaged; it has no evidence here at all",
			silent.Events, silent.Project.Engaged.Total)
	}
	busy, err := metrics.Against("acorn", events, sittingsOf(report), testParams())
	if err != nil {
		t.Fatalf("Against: %v", err)
	}
	if busy.Events != 1 {
		t.Errorf("acorn rests on %d observations, want 1", busy.Events)
	}
}
