package metrics

import (
	"sort"
	"time"
)

// Interval arithmetic over half-open spans.
//
// internal/timespan is the shared implementation, used by internal/sessions,
// internal/adjustments and internal/store. This one stays here rather than
// importing it, because they answer different questions and are allowed to
// diverge: timespan keeps ZERO-LENGTH spans, since a point observation is one
// and the clustering has to keep it, while everything here is about how much
// CLOCK something covers and a zero-length span covers none — union below
// drops them on the way in. Merging the two would mean one set of rules serving
// two meanings, which is how the zero-length case would eventually get decided
// by whichever caller was modified last.
//
// The cursor, cuts and carve below have no counterpart there at all; they exist
// for the segmentation this package builds and nothing else reads.

// span is a half-open interval [start, end).
type span struct{ start, end time.Time }

func (s span) length() time.Duration { return s.end.Sub(s.start) }

func laterOf(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func earlierOf(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

// union merges overlapping and touching spans into a disjoint, ordered run,
// DROPPING the empty ones. A point observation covers no clock, and a run that
// kept it would report an instant as though it were a duration.
func union(in []span) []span {
	kept := make([]span, 0, len(in))
	for _, s := range in {
		if s.end.After(s.start) {
			kept = append(kept, s)
		}
	}
	if len(kept) == 0 {
		return nil
	}
	sort.Slice(kept, func(i, j int) bool { return kept[i].start.Before(kept[j].start) })

	out := []span{kept[0]}
	for _, s := range kept[1:] {
		last := &out[len(out)-1]
		if !s.start.After(last.end) {
			last.end = laterOf(last.end, s.end)
			continue
		}
		out = append(out, s)
	}
	return out
}

// intersect is the clock both runs claim. Both must already be disjoint and
// ordered, which lets it walk them once rather than comparing every pair — the
// unattributed cover on the live store is tens of thousands of spans and the
// quadratic version is the difference between a command and a wait.
func intersect(a, b []span) []span {
	var out []span
	for i, j := 0, 0; i < len(a) && j < len(b); {
		start, end := laterOf(a[i].start, b[j].start), earlierOf(a[i].end, b[j].end)
		if end.After(start) {
			out = append(out, span{start, end})
		}
		if a[i].end.Before(b[j].end) {
			i++
		} else {
			j++
		}
	}
	return out
}

// subtract removes every instant of cut from from. Both must already be
// disjoint and ordered.
func subtract(from, cut []span) []span {
	if len(cut) == 0 {
		return from
	}
	var out []span
	j := 0
	for _, s := range from {
		// j only ever advances past cuts that end before this span begins,
		// and from is ordered, so such a cut cannot matter to any later span
		// either. The inner scan starts at j without consuming it, so one long
		// cut spanning several spans is applied to each of them.
		for j < len(cut) && !cut[j].end.After(s.start) {
			j++
		}
		cursor := s.start
		for k := j; k < len(cut) && cut[k].start.Before(s.end); k++ {
			if !cut[k].end.After(cursor) {
				continue
			}
			if cut[k].start.After(cursor) {
				out = append(out, span{cursor, cut[k].start})
			}
			cursor = laterOf(cursor, cut[k].end)
		}
		if s.end.After(cursor) {
			out = append(out, span{cursor, s.end})
		}
	}
	return out
}

// total sums a disjoint run.
func total(in []span) time.Duration {
	var sum time.Duration
	for _, s := range in {
		sum += s.length()
	}
	return sum
}

// cursor answers "does this disjoint, ordered run cover [start, end)?" for a
// sequence of segments that is itself ordered and non-overlapping, in one pass
// over both.
//
// Every segment is carved at the endpoints of every run it is tested against,
// so a segment is either wholly inside a span or wholly outside one, and a
// segment that straddles a boundary is a bug rather than a third answer to
// represent. It reports false there, and the failure would be silent — a
// project quietly losing part of an hour — which is why Compute checks its own
// totals against each other before returning rather than trusting this
// precondition to a comment.
type cursor struct {
	spans []span
	at    int
}

func (c *cursor) covers(s span) bool {
	for c.at < len(c.spans) && !c.spans[c.at].end.After(s.start) {
		c.at++
	}
	if c.at >= len(c.spans) {
		return false
	}
	return !c.spans[c.at].start.After(s.start) && !c.spans[c.at].end.Before(s.end)
}

// cuts collects every endpoint of a run, for building a segmentation fine
// enough that no segment straddles a boundary of it.
func cuts(runs ...[]span) []time.Time {
	var out []time.Time
	for _, run := range runs {
		for _, s := range run {
			out = append(out, s.start, s.end)
		}
	}
	return out
}

// carve cuts a disjoint, ordered run at every instant in at, dropping the cuts
// that fall outside it and the empty pieces that would result.
func carve(run []span, at []time.Time) []span {
	if len(run) == 0 {
		return nil
	}
	sort.Slice(at, func(i, j int) bool { return at[i].Before(at[j]) })

	var out []span
	i := 0
	for _, s := range run {
		for i < len(at) && at[i].Before(s.start) {
			i++
		}
		cursor := s.start
		for j := i; j < len(at) && at[j].Before(s.end); j++ {
			if at[j].After(cursor) {
				out = append(out, span{cursor, at[j]})
				cursor = at[j]
			}
		}
		if s.end.After(cursor) {
			out = append(out, span{cursor, s.end})
		}
	}
	return out
}
