package metrics_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// THE GUARD AND THE DOC COMMENT ARE NOT THE SAME CLAIM, and this pins the gap
// between them so that settling the question the neighbouring
// rollupcost_test.go pins cannot quietly leave this half unaddressed.
//
// Client's doc comment says a client's hours are the UNION of its projects'
// spans. The guard that closes Compute checks something strictly weaker — that
// the client does not exceed its projects' SUM — and the sum is inflated by
// every instant two of that client's projects are active at once. So a client
// can hold clock no project of its own holds, which is the doc comment being
// false, while the guard stays silent because the concurrent work elsewhere in
// the corpus gave it enough headroom to swallow.
//
// MEASURED ON THE LIVE STORE 2026-08-13, unwindowed, by
// `go run ./scripts/rollupunion`: the one client spanning 33 projects holds
// 19m13s that no project of its own holds, in 11 separate stretches, the
// largest 4m19s — against 83h58m52s of margin. The doc comment is therefore
// already false of the shipped report, on data that exists, while the guard has
// never fired on the unwindowed run. THIS IS NOT A LATENT CONDITION and the
// two thresholds must be reasoned about separately.
//
// Every one of those 19m13s is ALSO printed in the same report as unattributed
// presence — "silence the gap threshold bridged" — because a stretch no project
// covers is a segment with no project on it. The same minutes appear twice, in
// two sections, and only one of the two answers can survive the decision.
func TestAClientHoldsClockNoProjectOfItsOwnHoldsWhileTheGuardStaysSilent(t *testing.T) {
	// Alternating every 12 minutes puts each project's own observations 24
	// apart — beyond the 15-minute gap, so neither project bridges its own
	// silence — while the client's are 12 apart and bridge all three of the
	// stretches between them. The concurrent pair at 12:00 is counted ONCE for
	// the client and TWICE by the sum, and that surplus is the whole of what
	// keeps the guard quiet.
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
		clientPoint(at(12, 0), "atelier", "loom"),
		clientPoint(at(12, 0), "atelier", "kiln"),
	}
	r, err := metrics.Compute(events, testParams())
	if err != nil {
		t.Fatalf("the guard must stay silent on this corpus, and it refused it: %v", err)
	}
	c := clientOf(t, r, "atelier")

	var alone time.Duration
	for _, seg := range r.Segments {
		if !slices.Contains(seg.Clients, c.Name) {
			continue
		}
		held := false
		for _, name := range c.Projects {
			if slices.Contains(seg.Projects, name) {
				held = true
				break
			}
		}
		if !held {
			alone += seg.End.Sub(seg.Start)
		}
	}
	if want := 6 * time.Minute; alone != want {
		t.Errorf("the client holds %v that no project of its own holds, want %v; a roll-up that "+
			"merged its projects' spans would read 0 here and Client's doc comment would become "+
			"true, and one that re-clusters cannot", alone, want)
	}
	// The figures the argument rests on, asserted rather than described: the
	// client is 56m, its projects hold 50m of that between them, and they sum
	// to 60m because the 10m at 12:00 is counted for both.
	if want := 56 * time.Minute; c.Engaged.Total != want {
		t.Errorf("client engaged %v, want %v", c.Engaged.Total, want)
	}
	var summed time.Duration
	for _, name := range c.Projects {
		p, ok := r.Project(name)
		if !ok {
			t.Fatalf("the report has no project %q, which the client says it owns", name)
		}
		summed += p.Engaged.Total
	}
	if want := 60 * time.Minute; summed != want {
		t.Errorf("projects summed %v, want %v", summed, want)
	}
}

// THE REFUSAL REACHES THE HUMAN SURFACE AND NOT ONLY `metrics`, pinned at the
// call rather than argued from the call graph.
//
// Corrected is the entry point `dashboard` uses — cmd/timetattle/dashboard.go
// calls it once for the run and again per engagement in the allocation section,
// and returns its error unchanged, so a refusal there prints no dashboard at
// all rather than a dashboard missing a row. It was wired to this path on
// 2026-08-13; before that it called Compute and was uncorrected, which is a
// different defect that was fixed by giving it this one.
//
// The corpus is the one rollupcost_test.go pins, and this asserts only that the
// SAME refusal arrives through the SAME call with an empty correction log —
// which is what every dashboard run on a store nobody has corrected does.
func TestTheRollUpRefusalArrivesThroughTheCallTheDashboardMakes(t *testing.T) {
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
	}
	_, err := metrics.Corrected(events, testParams(), nil)
	if err == nil {
		t.Fatal("Corrected computed the interleaving corpus; if the contradiction has been " +
			"settled, this test and the one in rollupcost_test.go both go")
	}
	if !strings.Contains(err.Error(), "can never exceed their sum") {
		t.Fatalf("the corpus was refused for some other reason than the roll-up guard: %v", err)
	}
}
