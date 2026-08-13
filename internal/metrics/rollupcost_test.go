package metrics_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// A CLIENT WHOSE PROJECTS INTERLEAVE IS CREDITED THE STRETCH ONLY THEIR COMBINED
// EVIDENCE BRIDGES, AND THE REPORT IS WHOLE. This test used to pin the opposite —
// a refusal, which its own comment called a defect — and the question it was
// waiting on was settled on 2026-08-13 in favour of the re-clustering.
//
// The two claims this package used to make at once were:
//
//	the implementation   re-clusters the CLIENT'S OWN evidence under the same
//	                     parameters, which is what reduce does at both levels.
//	the old doc and the
//	closing guard        said a client's hours were the UNION of its projects'
//	                     spans, and refused any report where the client exceeded
//	                     its projects' SUM.
//
// They cannot both hold, because clustering does not distribute over a union: a
// silence too wide to bridge inside any one project is narrow enough to bridge
// once that client's evidence is taken together. The union claim was the one
// that was wrong, and it was already wrong of the shipped report by 19m13s
// before anything here changed — see project/2026-08-13-rollup-union-or-reclustering.md
// for that measurement and project/2026-08-13-rollup-reclustering-resolved.md
// for the decision and what replaced the guard.
//
// WHY THE RE-CLUSTERED FIGURE IS THE HONEST ONE, and it is not an accounting
// convenience: moving between two of one client's projects costs attention that
// neither project's own evidence records. A stretch bridged only at the client
// level IS that switching cost, so a client's hours are what that client's
// evidence clusters to when taken together — which is arguably more real than
// the projects' sum, not less.
//
// The window is ordinary rather than exotic. At the shipped defaults — gap 15m,
// lead-in and tail-out 5m — two of one client's projects touched ALTERNATELY
// every 8 to 15 minutes put each project's own observations more than a gap
// apart while the client's stay inside one. 12-minute alternation is the case
// below: the client reads 46m against 40m summed, and used to produce no report
// at all.
func TestAClientsInterleavedProjectsAreCreditedAndTheReportIsWhole(t *testing.T) {
	// Alternating every 12 minutes. Each project's own observations are then 24
	// minutes apart — more than the 15-minute gap, so neither project bridges
	// its own silence — while the client's are 12 apart throughout and bridge
	// all of it into one sitting.
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
	}

	r, err := metrics.Compute(events, testParams())
	if err != nil {
		t.Fatalf("the interleaving corpus must produce a report and it was refused: %v", err)
	}

	c := clientOf(t, r, "atelier")
	// 08:55 to 09:41 in one sitting: every gap between the client's own
	// observations is 12 minutes and the threshold is 15.
	if want := 46 * time.Minute; c.Engaged.Total != want {
		t.Errorf("the client is credited %v, want %v", c.Engaged.Total, want)
	}

	var summed time.Duration
	for _, name := range c.Projects {
		p, ok := r.Project(name)
		if !ok {
			t.Fatalf("the report has no project %q, which the client says it owns", name)
		}
		summed += p.Engaged.Total
	}
	// Each project holds two 10-minute sittings of its own — 08:55-09:05 and
	// 09:19-09:29 for loom, 09:07-09:17 and 09:31-09:41 for kiln — so the
	// arithmetic sum is 40m and the client legitimately exceeds it by the 6m of
	// switching neither project can see.
	if want := 40 * time.Minute; summed != want {
		t.Errorf("the projects sum to %v, want %v", summed, want)
	}
	if got, want := c.Engaged.Total-summed, 6*time.Minute; got != want {
		t.Errorf("the client exceeds its projects' sum by %v, want %v; that surplus is the whole "+
			"of the decision, and a roll-up that merged the project spans would read 0", got, want)
	}
	// The report is not merely permitted, it CLOSES: presence, the client's
	// share of it and the three-way split all reconcile, which is what Compute's
	// remaining assertions check and what the refusal used to deny sight of.
	if want := 46 * time.Minute; r.Presence.Total != want {
		t.Errorf("presence %v, want %v; this is not the corpus the case describes",
			r.Presence.Total, want)
	}
	if want := 46 * time.Minute; c.Allocated != want {
		t.Errorf("the client is allocated %v of %v of presence, want all of it; it is the only "+
			"client in the corpus", c.Allocated, want)
	}
}
