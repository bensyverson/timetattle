package metrics_test

import (
	"slices"
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

// THE HUMAN SURFACE GETS THE REPORT TOO, pinned at the call rather than argued
// from the call graph.
//
// Corrected is the entry point `dashboard` uses — cmd/timetattle/dashboard.go
// calls it once for the run and again per engagement in the allocation section,
// and returns its error unchanged, so anything Compute refuses prints no
// dashboard at all rather than a dashboard missing a row. Until 2026-08-13 this
// test pinned the interleaving corpus being REFUSED through exactly that call,
// which meant an ordinary day's alternation between two of a client's projects
// blanked the human surface entirely.
//
// The corpus is the one rollupcost_test.go carries, and this asserts the same
// figure arrives through the same call with an empty correction log — which is
// what every dashboard run on a store nobody has corrected does. It is not a
// duplicate of that test: what it pins is the PATH, and the path is the reason
// the defect cost a dashboard and not only a `metrics` run.
func TestTheInterleavedRollUpArrivesThroughTheCallTheDashboardMakes(t *testing.T) {
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
	}
	r, err := metrics.Corrected(events, testParams(), nil)
	if err != nil {
		t.Fatalf("the call the dashboard makes refused the interleaving corpus, so there is no "+
			"dashboard at all for a day worked this way: %v", err)
	}
	c := clientOf(t, r, "atelier")
	if want := 46 * time.Minute; c.Engaged.Total != want {
		t.Errorf("the dashboard's own call credits the client %v, want %v — the same figure "+
			"`metrics` computes, since it is the same computation", c.Engaged.Total, want)
	}
}

// A ONE-PROJECT CLIENT CAN LEGITIMATELY HOLD LESS THAN ITS PROJECT, WHICH IS WHY
// THE CHECK THAT REPLACED THE UNION GUARD IS AN INEQUALITY AND NOT AN EQUALITY.
//
// The guard that closed Compute until 2026-08-13 refused any client exceeding
// its projects' sum, and it went because that invariant is false wherever a
// client's projects interleave. What survives of it is the half that is true by
// construction: a client owning exactly ONE project is that project's own
// evidence, clustered again under the same parameters, so it can never exceed
// it. That is also the shape the removed guard actually caught — a mutation
// inflating the client's own clustering fired on a one-project client, and five
// of the six clients in the live store are in that state.
//
// The obvious tightening — that the two are EQUAL to the nanosecond — is not
// true, and this is the case that breaks it. A project is stamped with a client
// per observation, from a log with effective dates, so a directory registered to
// a client only from some date has evidence BEFORE that date which is the
// project's and no client's. The project then holds clock its client does not,
// and an equality would refuse the whole report over it: no report, which is
// exactly the failure being repaired. `Clientless` is the figure that names that
// clock, and it already exists.
//
// This test is green under the shipped inequality and RED under the equality
// proposed for it, which is the only reason it can be written down at all.
func TestAOneProjectClientHoldsLessThanItsProjectWhenTheClientResolvedLate(t *testing.T) {
	events := []source.Event{
		// Before the registration: the project resolves, the client does not.
		point(at(9, 0), "loom"),
		point(at(9, 5), "loom"),
		// After it: the same project, now stamped with its client.
		clientPoint(at(14, 0), "atelier", "loom"),
		clientPoint(at(14, 5), "atelier", "loom"),
	}
	r := compute(t, events)

	p := projectOf(t, r, "loom")
	c := clientOf(t, r, "atelier")
	if len(c.Projects) != 1 {
		t.Fatalf("the client owns %v, and this case is only about a client with exactly one "+
			"project", c.Projects)
	}
	// Two sittings of 15m for the project — 08:55-09:10 and 13:55-14:10 — and
	// only the second of them is the client's.
	if want := 30 * time.Minute; p.Engaged.Total != want {
		t.Errorf("the project is credited %v, want %v", p.Engaged.Total, want)
	}
	if want := 15 * time.Minute; c.Engaged.Total != want {
		t.Errorf("the client is credited %v, want %v", c.Engaged.Total, want)
	}
	if c.Engaged.Total >= p.Engaged.Total {
		t.Fatalf("the client holds %v against its only project's %v; this corpus no longer "+
			"exercises the direction it was written for", c.Engaged.Total, p.Engaged.Total)
	}
	// The difference is not lost and it is not silence either: it is presence a
	// PROJECT was attributed and no CLIENT was, which the report names.
	if want := 15 * time.Minute; r.Clientless != want {
		t.Errorf("clientless presence %v, want %v; clock the client does not hold has to land "+
			"somewhere that says what it is", r.Clientless, want)
	}
}
