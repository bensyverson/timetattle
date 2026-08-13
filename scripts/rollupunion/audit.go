package main

import (
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
)

// rollup is one client's hours decomposed against the projects underneath it.
//
// Union and Excess PARTITION Engaged: every instant a client is credited either
// has one of that client's own projects active in it or it does not, and there
// is no third answer. Excess is therefore the whole of the disagreement this
// tool was built to measure — clock the client holds BECAUSE its evidence was
// clustered together, which no project of its own holds.
type rollup struct {
	Client   string
	Projects int
	// Engaged is what the report itself credits the client, as printed.
	Engaged time.Duration
	// Union is Engaged less Excess: client clock at least one of its own
	// projects also holds.
	Union time.Duration
	// Excess is client clock NO project of its own holds. It is exactly the
	// amount a merge-the-project-spans roll-up would take away from this
	// client, and exactly the amount by which the doc comment on metrics.Client
	// is false of this report.
	Excess time.Duration
	// ExcessRuns and LargestExcess describe the SHAPE of that clock rather than
	// only its size: one bridged hour and sixty bridged minutes are the same
	// total and are not the same finding.
	ExcessRuns    int
	LargestExcess time.Duration
	// AloneAndUnattributed is the part of Excess that NO project of any client
	// covers, which is the part the same report ALSO prints as unattributed
	// presence under "silence the gap threshold bridged". Those minutes are in
	// two places at once in one report today: inside a client's engaged hours,
	// and inside the presence the report says it could not attribute.
	AloneAndUnattributed time.Duration
	// Orphan is the opposite direction — project clock the client does NOT
	// hold. metrics.Compute cannot produce it today, because a client's
	// evidence is a superset of each of its projects', so a non-zero reading
	// here is a defect and not a measurement of interleaving. It is reported
	// rather than assumed away: "we looked and it was zero" is a different
	// statement from "we never looked".
	Orphan time.Duration
	// Summed is the arithmetic sum of the projects' own Engaged.Total, which is
	// the quantity the shipped guard compares the client against.
	Summed time.Duration
	// Margin is Summed - Engaged. THE SHIPPED GUARD REFUSES THE WHOLE REPORT
	// WHEN THIS IS NEGATIVE, so it is the headroom a client has before this
	// store stops producing any report at all.
	Margin time.Duration
	// Mismatch is this tool's own recomputation of Engaged from the segments,
	// less the report's figure. Two computations of one number, subtracted, so
	// that a silent divergence cannot pass for agreement.
	Mismatch time.Duration
}

// audit decomposes every client in a report against its own projects.
//
// It reads nothing but the report's exported surface — Segments, Clients and
// Projects — and re-derives the client totals from the same partition every
// figure in the report is already summed from. That is deliberate: an
// instrument that re-implemented the clustering would be measuring its own idea
// of the rules rather than the ones that produced the report.
func audit(r metrics.Report) []rollup {
	out := make([]rollup, 0, len(r.Clients))
	for _, c := range r.Clients {
		row := rollup{Client: c.Name, Projects: len(c.Projects), Engaged: c.Engaged.Total}
		for _, name := range c.Projects {
			if p, ok := r.Project(name); ok {
				row.Summed += p.Engaged.Total
			}
		}
		row.Margin = row.Summed - row.Engaged

		// A run is a MAXIMAL stretch of excess: consecutive segments, touching
		// end to start, in none of which any of this client's projects is
		// active. Segments are already disjoint and ordered, and the report
		// carves them at every boundary either level contributes, so a bridged
		// silence can span several of them and must not be counted as several
		// findings.
		var run time.Duration
		var previousEnd time.Time
		for _, seg := range r.Segments {
			d := seg.End.Sub(seg.Start)
			client := slices.Contains(seg.Clients, c.Name)
			var project bool
			for _, name := range c.Projects {
				if slices.Contains(seg.Projects, name) {
					project = true
					break
				}
			}
			switch {
			case client && project:
				row.Union += d
			case client:
				if run > 0 && seg.Start.Equal(previousEnd) {
					run += d
				} else {
					row.ExcessRuns++
					run = d
				}
				row.Excess += d
				if len(seg.Projects) == 0 {
					row.AloneAndUnattributed += d
				}
				previousEnd = seg.End
				row.LargestExcess = max(row.LargestExcess, run)
				continue
			case project:
				row.Orphan += d
			}
			run = 0
		}
		row.Mismatch = row.Union + row.Excess - row.Engaged
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b rollup) int {
		if a.Client < b.Client {
			return -1
		}
		if a.Client > b.Client {
			return 1
		}
		return 0
	})
	return out
}
