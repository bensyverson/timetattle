//go:build realdata

// This file is a CORPUS CHECK, and the note at the head of series_realdata_test.go
// applies to it unchanged: `go test ./...` does not compile it, and a tagged run
// without -count=1 is not evidence.
//
//	go test -count=1 -tags realdata ./internal/metrics/

package metrics_test

import "testing"

// THE ONE-PROJECT CONTAINMENT, ON REAL EVIDENCE, BUCKET BY BUCKET.
//
// A client owning exactly one project is that project's own evidence clustered
// again under the same parameters, so it can never be credited more clock than
// the project is — across the span, which realdata_test.go's
// TestAOneProjectClientIsNeverCreditedMoreThanItsProjectOnRealData asserts, and
// inside every bucket of a slice of it, which is this. The two are not the same
// claim: a bucket edge cuts a segment, and the question of whether the two
// levels can divide differently across one is what this exists to answer.
//
// THEY CANNOT, and the reason is structural rather than statistical. A bucket is
// summed from Report.Segments, and a segment names both its projects and its
// clients, so the clip an edge makes is ONE duration booked to both levels in
// one call. What that reduces the bucket claim to is a claim about segments: no
// segment carries a one-project client without its project. That is measurable
// over the whole store rather than over the buckets anyone happened to cut, and
// it has been measured — `go run ./scripts/rollupunion`, 2026-08-13, unwindowed,
// the whole store, 1,071,969 events after exclusions: five of the six clients
// own one project and each holds 0s that no project of its own holds, in 0 runs.
// Zero there is containment for EVERY conceivable set of edges, not only for the
// ones below. (The sixth owns 33 projects, holds 19m13s alone in 11 stretches,
// and is correct in doing so; it is out of this relation's scope entirely.)
//
// This is the same relation on the day the rest of the real-data suite measures,
// cut at every hour so that far more segments straddle an edge than any calendar
// would make. It says how many clients it found in that state, because a report
// in which no client owns exactly one project would pass having checked nothing,
// and that is a different finding from the relation holding.
func TestNoRealBucketCreditsAOneProjectClientMoreThanItsProject(t *testing.T) {
	r := concurrentDayByClient(t)
	if len(r.Segments) == 0 {
		t.Skip("the live store holds no segments for 2026-07-28")
	}

	// Series itself refuses a violation, so a report reaching this point has
	// already been checked. That is deliberate and this is not therefore
	// redundant: what it establishes is that the check does not FIRE on real
	// evidence, which a suite in which nothing ever called Series over a real
	// report with clients could not tell from the check being right.
	series, err := r.Series(hourlyEdges(t, r.Segments[0].Start, r.Segments[len(r.Segments)-1].End))
	if err != nil {
		t.Fatalf("Series over the live day: %v", err)
	}

	only := map[string]string{}
	for _, c := range r.Clients {
		if len(c.Projects) == 1 {
			only[c.Name] = c.Projects[0]
		}
	}
	var checked, strictlyLess int
	for i, b := range series {
		for _, s := range b.Clients {
			name, one := only[s.Name]
			if !one {
				continue
			}
			checked++
			p, held := b.Project(name)
			if !held {
				t.Errorf("bucket %d credits a one-project client %v and names its only project not "+
					"at all", i, s.Engaged.Total)
				continue
			}
			switch {
			case s.Engaged.Total > p.Engaged.Total:
				t.Errorf("bucket %d credits a client with one project %v against the %v its project "+
					"holds there; the two are one body of evidence clustered twice",
					i, s.Engaged.Total, p.Engaged.Total)
			case s.Engaged.Total < p.Engaged.Total:
				strictlyLess++
			}
		}
	}
	if checked == 0 {
		t.Skipf("no bucket of 2026-07-28 holds a client owning exactly one project, so the relation "+
			"was not exercised; the report names %d client(s) across %d bucket(s)",
			len(r.Clients), len(series))
	}
	// Reported rather than asserted. A day on which every bucket's one-project
	// client EQUALS its project is an ordinary day — the registration is old and
	// nothing else claims the directory — so a zero here is a fact about this
	// corpus and not a defect. Saying it out loud is what stops a later reader
	// assuming the inequality was exercised strictly when it was not.
	t.Logf("checked %d one-project client share(s) across %d bucket(s); %d held strictly less than "+
		"their project", checked, len(series), strictlyLess)
}
