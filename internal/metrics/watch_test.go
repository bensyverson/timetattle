package metrics_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// A WATCHER MAY NOT CHANGE AN ANSWER, and these are the tests that hold the
// line. Reporting progress is the one kind of instrumentation that gets added
// under time pressure and reviewed least, and a phase counter that quietly
// re-ordered a loop or consumed an iterator would move a billing figure for a
// reason nobody could see in the report.
//
// Names here are invented; the live ones are a confidentiality matter.

// recorder is a Watcher that remembers everything it was told, in order.
type recorder struct {
	calls  []string
	steps  map[metrics.Phase]int
	total  map[metrics.Phase]int
	opened []metrics.Phase
}

func newRecorder() *recorder {
	return &recorder{steps: map[metrics.Phase]int{}, total: map[metrics.Phase]int{}}
}

func (r *recorder) Begin(p metrics.Phase, steps int) {
	r.calls = append(r.calls, "begin "+string(p))
	r.total[p] = steps
	r.opened = append(r.opened, p)
}

func (r *recorder) Step(p metrics.Phase, done int) {
	r.calls = append(r.calls, "step "+string(p))
	r.steps[p] = done
}

func (r *recorder) Done(p metrics.Phase) {
	r.calls = append(r.calls, "done "+string(p))
	r.opened = slices.DeleteFunc(r.opened, func(o metrics.Phase) bool { return o == p })
}

// threeProjectsTwoClients is the shape that separates a roll-up from a sum: one
// client with two overlapping projects, and a second client alongside it.
func threeProjectsTwoClients() []source.Event {
	return []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 3), "atelier", "kiln"),
		clientPoint(at(9, 6), "foundry", "anvil"),
		clientPoint(at(9, 9), "atelier", "loom"),
		clientPoint(at(9, 20), "foundry", "anvil"),
	}
}

// THE WHOLE POINT OF THE INSTRUMENT: it must not perturb what it measures.
//
// A report computed with a watcher attached has to be indistinguishable from
// one computed without, field for field. Anything less and a reader could not
// tell whether a figure moved because the work changed or because somebody
// switched the timings on.
func TestAWatcherChangesNoFigureInTheReport(t *testing.T) {
	events := threeProjectsTwoClients()

	plain, err := metrics.Corrected(events, testParams(), nil)
	if err != nil {
		t.Fatalf("Corrected: %v", err)
	}
	watched, err := metrics.Watched(events, testParams(), nil, newRecorder())
	if err != nil {
		t.Fatalf("Watched: %v", err)
	}

	if !reflect.DeepEqual(plain, watched) {
		t.Errorf("the watched report differs from the unwatched one; an instrument that changes "+
			"the answer is measuring something other than the run\nplain:   %+v\nwatched: %+v",
			plain.Clients, watched.Clients)
	}
}

// A nil watcher is the ordinary case — Compute and Corrected both pass one —
// so it has to cost nothing and crash nothing.
func TestWatchedWithNoWatcherIsCorrected(t *testing.T) {
	events := threeProjectsTwoClients()

	plain, err := metrics.Corrected(events, testParams(), nil)
	if err != nil {
		t.Fatalf("Corrected: %v", err)
	}
	watched, err := metrics.Watched(events, testParams(), nil, nil)
	if err != nil {
		t.Fatalf("Watched with a nil watcher: %v", err)
	}
	if !reflect.DeepEqual(plain, watched) {
		t.Error("Watched with a nil watcher differs from Corrected; they are the same computation")
	}
}

// EVERY PHASE OPENED IS CLOSED. An unbalanced phase is how a progress display
// comes to sit for ever on a stage that finished, which is exactly the "is it
// hung?" reading the display exists to end.
func TestEveryPhaseThatBeginsAlsoEnds(t *testing.T) {
	rec := newRecorder()
	if _, err := metrics.Watched(threeProjectsTwoClients(), testParams(), nil, rec); err != nil {
		t.Fatalf("Watched: %v", err)
	}
	if len(rec.opened) != 0 {
		t.Errorf("%v began and never ended; a phase left open reads on screen as a hang",
			rec.opened)
	}
}

// The phases arrive in the order the work is done, and all of them are
// reported. A run that named only its cheapest stage would be worse than
// silence: it would tell a reader the expensive part was somewhere it is not.
func TestEveryPhaseIsReportedInTheOrderItRuns(t *testing.T) {
	rec := newRecorder()
	if _, err := metrics.Watched(threeProjectsTwoClients(), testParams(), nil, rec); err != nil {
		t.Fatalf("Watched: %v", err)
	}

	want := []metrics.Phase{
		metrics.PhaseCluster,
		metrics.PhaseGroup,
		metrics.PhaseProjects,
		metrics.PhaseClients,
		metrics.PhaseSegments,
	}
	var got []metrics.Phase
	for _, p := range want {
		if slices.Contains(rec.calls, "begin "+string(p)) {
			got = append(got, p)
		}
	}
	if !slices.Equal(got, want) {
		t.Errorf("the phases reported were %v, wanted %v", got, want)
	}

	// Order, checked against the call log rather than against the list above:
	// clustering must be reported as finished before the per-project pass
	// begins, and the client roll-up before the segmentation.
	for i, earlier := range want[:len(want)-1] {
		later := want[i+1]
		endOfEarlier := slices.Index(rec.calls, "done "+string(earlier))
		startOfLater := slices.Index(rec.calls, "begin "+string(later))
		if endOfEarlier < 0 || startOfLater < 0 || endOfEarlier > startOfLater {
			t.Errorf("%s is reported as ending at call %d and %s as beginning at %d; the phases "+
				"do not nest in the order the work runs", earlier, endOfEarlier, later, startOfLater)
		}
	}
}

// THE ROLL-UP COUNTS ITSELF, N OF M. That is the requirement in its own words:
// a display that says "client roll-up" and nothing else cannot distinguish a
// slow client from a stuck one, and the roll-up is the phase whose cost the
// task exists to explain.
func TestTheClientRollUpReportsHowManyClientsAndHowFarThroughItIs(t *testing.T) {
	rec := newRecorder()
	r, err := metrics.Watched(threeProjectsTwoClients(), testParams(), nil, rec)
	if err != nil {
		t.Fatalf("Watched: %v", err)
	}
	if len(r.Clients) != 2 {
		t.Fatalf("the fixture produced %d clients, so it cannot test a count of them", len(r.Clients))
	}

	if got := rec.total[metrics.PhaseClients]; got != len(r.Clients) {
		t.Errorf("the client roll-up announced %d clients against %d in the report; a total that "+
			"does not match is a progress bar that never reaches its end", got, len(r.Clients))
	}
	if got := rec.steps[metrics.PhaseClients]; got != len(r.Clients) {
		t.Errorf("the client roll-up stepped to %d of %d; every client rolled up has to be counted",
			got, len(r.Clients))
	}
	if got := rec.total[metrics.PhaseProjects]; got != len(r.Projects) {
		t.Errorf("the per-project pass announced %d projects against %d in the report", got,
			len(r.Projects))
	}
	if got := rec.steps[metrics.PhaseProjects]; got != len(r.Projects) {
		t.Errorf("the per-project pass stepped to %d of %d", got, len(r.Projects))
	}
}

// A corpus with nothing in it returns before any phase past the clustering, and
// must still leave no phase open. This is the path Corrected takes when there
// is no presence at all, and it returns early.
func TestAnEmptyCorpusLeavesNoPhaseOpen(t *testing.T) {
	rec := newRecorder()
	if _, err := metrics.Watched(nil, testParams(), nil, rec); err != nil {
		t.Fatalf("Watched over no events: %v", err)
	}
	if len(rec.opened) != 0 {
		t.Errorf("%v began and never ended on an empty corpus", rec.opened)
	}
}
