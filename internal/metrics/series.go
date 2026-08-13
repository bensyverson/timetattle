// Slicing a report into a series of periods.
//
// # A SERIES IS A RE-SLICE AND NEVER A RE-COMPUTATION
//
// This is the whole design, and the alternative is the trap. The obvious way to
// report a day at a time is to filter the events to each day and call Compute
// on them; it is wrong, and wrong in a way nobody can see from the output.
// Clustering is not distributive over a partition of the events. A day's events
// clustered alone get their own lead-in at the first observation and their own
// tail-out at the last, so every boundary manufactures margin that the whole
// span does not have; a silence the gap threshold bridged ACROSS midnight
// becomes two sittings instead of one; and the presence readings that trim a
// sitting from outside the day are not there to trim it. Fourteen days computed
// separately and fourteen days sliced out of one report are different numbers,
// and only the second can be added up.
//
// So the buckets here are summed from Report.Segments — the same partition
// every figure in the report is already summed from. Segments are disjoint and
// ordered and tile presence exactly, which makes the conservation property fall
// out of the construction rather than out of any arithmetic:
//
//	sum over buckets of bucket presence          == Report.Presence.Total
//	sum over buckets of a project's absolute     == that project's Engaged.Total
//	sum over buckets of a client's absolute      == that client's Engaged.Total
//
// EXACTLY, to the nanosecond, on a 23-hour day as much as on a 25-hour one,
// because an edge only ever cuts a segment in two and never duplicates one. The
// warning in the brief this was built from — that daily buckets must be greater
// than or equal to the span total because absolute engaged time is a union
// rather than a sum — is true of the re-computation model and is dissolved by
// this one. Absolute engaged time overlaps ACROSS PROJECTS, which is why a
// client roll-up is a union; it does not overlap ACROSS TIME, and a bucket is a
// cut in time.
//
// # A segment crossing an edge is SPLIT at it
//
// The two alternatives are to count it whole in the earlier bucket, or whole in
// both. Counting it whole in both credits a pair of days with more clock than
// elapsed across them, which is the one thing presence exists to make
// impossible; counting it whole in the earlier one can credit a single day with
// more clock than the day is long. Splitting is what internal/sessions'
// calendar.go already does to a sitting crossing a working-day boundary, for
// the same reason and with the same consequence: engaged(bucket) <=
// length(bucket) holds by construction.
//
// # What a bucket does NOT carry, and why the absence is deliberate
//
// A Share is not a Project and a bucket's unattributed figure is not an
// Unattributed. Project carries Events and Sittings and Unattributed carries
// two event counts, and NONE of those four can be derived from the segments a
// bucket is summed from: an event count is a property of the read, and a
// sitting that crosses an edge is not a sitting of either bucket. Reusing the
// richer types would leave those fields at zero, and a zero there reads as a
// measurement — the collapse of "observed none" into "not established" that
// this package's doc names as the project's cardinal bug. A caller that wants
// them has to ask a question that can answer them.
package metrics

import (
	"fmt"
	"slices"
	"time"
)

// Share is one project's or one client's part of one bucket.
//
// The fields mean exactly what the same names on Project and Client mean, one
// slice down. Absolute engaged time still OVERLAPS between shares in the same
// bucket, so the project shares of a bucket sum to more than the bucket's
// presence whenever two projects ran at once, and the client shares are a union
// across each client's projects and never their sum.
type Share struct {
	// Name is the project or client this is a share of.
	Name string
	// Engaged is ABSOLUTE: every instant of this bucket the group was active
	// counts in full. Observed is the part its OWN evidence spanned.
	Engaged Span
	// Allocated is ALLOCATION: an instant k groups shared gives each 1/k of
	// it. The shares of a bucket sum to exactly the part of THAT BUCKET'S
	// presence at least one of them was attributed, to the nanosecond.
	Allocated time.Duration
	// Corroborated is how much of Engaged some source observed without saying
	// whose work it was.
	Corroborated time.Duration
	// Attested, Weak and Unaccounted partition Engaged.Total. See Project for
	// what each one means and why the third is not either of the other two.
	Attested    time.Duration
	Weak        time.Duration
	Unaccounted time.Duration
}

// Period is one period's share of a Report.
type Period struct {
	// Start and End are the instants the bucket covers, half-open. They come
	// from the caller's edges and are not derived from the data, so a bucket
	// nothing fell in still says exactly what it covered.
	Start, End time.Time
	// Presence is wall clock inside this bucket, counted once however many
	// projects shared it.
	Presence Span
	// Projects and Clients are the groups ACTIVE in this bucket, sorted. A
	// group with no time here is absent rather than present with a zero, which
	// is legible only because the bucket itself is always present: the bucket
	// is the row that says "this period was looked at".
	Projects []Share
	Clients  []Share
	// Clientless is presence a project owns and no client does. The fourth
	// figure, kept out of Unattributed for the reason Report.Clientless gives.
	Clientless time.Duration
	// Unattributed is presence no project could be attributed, and
	// Unregistered, Unlocatable, Asserted and Bridged partition its Total. See
	// Reason. Asserted is the one a slice would otherwise lose: it is a
	// person's own accounting rather than a failure to attribute, and folding
	// it into bridged silence would report what somebody established as clock
	// nothing was ever recorded in.
	Unattributed Span
	Unregistered time.Duration
	Unlocatable  time.Duration
	Asserted     time.Duration
	Bridged      time.Duration
}

// Length is how long the bucket itself ran. It is 23, 24 or 25 hours for a
// working day across a daylight-saving transition, and a rate quoted per bucket
// should divide by this rather than by a constant.
func (b Period) Length() time.Duration { return b.End.Sub(b.Start) }

// Project finds one project's share of this bucket.
func (b Period) Project(name string) (Share, bool) { return findShare(b.Projects, name) }

// Client finds one client's share of this bucket.
//
// Looked up in a separate list from the projects for the reason Report.Client
// gives: a client and a project may carry the same name, and in the live
// registry one pair does.
func (b Period) Client(name string) (Share, bool) { return findShare(b.Clients, name) }

func findShare(in []Share, name string) (Share, bool) {
	for _, s := range in {
		if s.Name == name {
			return s, true
		}
	}
	return Share{}, false
}

// Series slices the report into the consecutive buckets [edges[i], edges[i+1]).
//
// Every bucket the edges describe is returned, including the ones nothing fell
// in: a caller handed a shorter slice than it asked for could not tell a period
// with no work from a period nobody sliced.
//
// The edges must strictly increase and must cover every segment. Both are
// refused rather than tolerated, because both fail SILENTLY: a repeated edge
// makes a bucket covering no clock, and edges that stop short drop whatever
// falls outside them out of a report that still reads as complete. Note that
// the segments can reach past the span the events were READ over, by the
// lead-in before the first observation and the tail-out after the last, so a
// caller building edges from a requested window has to widen them to the
// presence the report actually holds.
func (r Report) Series(edges []time.Time) ([]Period, error) {
	if len(edges) < 2 {
		return nil, fmt.Errorf("a series needs at least two edges to describe one bucket, and was given %d",
			len(edges))
	}
	for i := 1; i < len(edges); i++ {
		if !edges[i].After(edges[i-1]) {
			return nil, fmt.Errorf("the edges of a series must strictly increase: edge %d (%v) does not "+
				"come after edge %d (%v), which would make a bucket covering no clock at all",
				i, edges[i], i-1, edges[i-1])
		}
	}
	first, last := edges[0], edges[len(edges)-1]
	for _, s := range r.Segments {
		if s.Start.Before(first) || s.End.After(last) {
			return nil, fmt.Errorf("the segment %v..%v falls outside the series %v..%v, so slicing at "+
				"these edges would silently drop it; widen the edges to the presence the report holds, "+
				"which reaches the lead-in before its first observation and the tail-out after its last",
				s.Start, s.End, first, last)
		}
	}

	out := make([]Period, len(edges)-1)
	projects := make([]map[string]*Share, len(out))
	clients := make([]map[string]*Share, len(out))
	for i := range out {
		out[i] = Period{Start: edges[i], End: edges[i+1]}
		projects[i] = map[string]*Share{}
		clients[i] = map[string]*Share{}
	}

	// The segments are ordered and disjoint and the edges increase, so one
	// cursor walks both. It only ever advances, which is what keeps this linear
	// over the hundred thousand segments a fortnight of the live corpus makes.
	i := 0
	for _, seg := range r.Segments {
		for i+1 < len(out) && !edges[i+1].After(seg.Start) {
			i++
		}
		for j := i; j < len(out) && edges[j].Before(seg.End); j++ {
			from, to := laterOf(seg.Start, edges[j]), earlierOf(seg.End, edges[j+1])
			if !to.After(from) {
				continue
			}
			addSegment(&out[j], projects[j], clients[j], seg, to.Sub(from))
		}
	}

	for i := range out {
		out[i].Projects = collectShares(projects[i])
		out[i].Clients = collectShares(clients[i])
	}
	if err := r.checkSeries(out); err != nil {
		return nil, err
	}
	return out, nil
}

// addSegment books d of one segment into one bucket. It is the same
// accumulation Compute runs over a whole segment, applied to the part of one
// that fell here, which is what makes a bucket the same kind of number as the
// total rather than a second opinion about it.
func addSegment(b *Period, projects, clients map[string]*Share, seg Segment, d time.Duration) {
	b.Presence.Total += d
	if seg.Observed {
		b.Presence.Observed += d
	}

	if len(seg.Clients) == 0 && len(seg.Projects) > 0 {
		b.Clientless += d
	}
	accumulate(clients, seg.Clients, nil, seg.Attested, seg.Weak, seg, d)
	accumulate(projects, seg.Projects, seg.Evidenced, seg.Attested, seg.Weak, seg, d)

	if len(seg.Projects) > 0 {
		return
	}
	b.Unattributed.Total += d
	if seg.Observed {
		b.Unattributed.Observed += d
	}
	switch seg.Unattributed {
	case ReasonUnregistered:
		b.Unregistered += d
	case ReasonUnlocatable:
		b.Unlocatable += d
	case ReasonAsserted:
		b.Asserted += d
	default:
		b.Bridged += d
	}
}

// accumulate books d against every group active across a segment, and divides
// it among them.
//
// evidenced is nil at the client level, where a Segment records no
// client-evidenced set. That is an honest gap rather than a zero: Segment
// carries Evidenced for projects only, so a client's Engaged.Observed is left
// at nothing was established rather than asserted to be none.
func accumulate(into map[string]*Share, active, evidenced, attested, weak []string,
	seg Segment, d time.Duration) {
	if len(active) == 0 {
		// Nothing at this level owns the segment, which is the ordinary case for
		// unattributed presence and for a project whose client never resolved.
		// There is nothing to divide it among, and dividing by nobody is the
		// arithmetic this returns before reaching.
		return
	}
	for _, name := range active {
		s, ok := into[name]
		if !ok {
			s = &Share{Name: name}
			into[name] = s
		}
		s.Engaged.Total += d
		if seg.Observed {
			s.Corroborated += d
		}
		if slices.Contains(evidenced, name) {
			s.Engaged.Observed += d
		}
		// The same priority Compute states: where an act and a file timestamp
		// both account for an instant, the act is the answer.
		switch {
		case slices.Contains(attested, name):
			s.Attested += d
		case slices.Contains(weak, name):
			s.Weak += d
		}
	}
	// Divided among the groups present at THIS level, exactly as Compute
	// divides it, and never re-added from the other level's split.
	for name, share := range allocate(d, active) {
		into[name].Allocated += share
	}
}

// collectShares sorts the shares and computes each one's remainder, so that
// what nothing accounts for cannot be anything other than what the other two
// leave behind.
func collectShares(in map[string]*Share) []Share {
	if len(in) == 0 {
		return nil
	}
	out := make([]Share, 0, len(in))
	for _, s := range in {
		s.Unaccounted = s.Engaged.Total - s.Attested - s.Weak
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b Share) int { return compareNames(a.Name, b.Name) })
	return out
}

func compareNames(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// checkSeries proves the slice conserved what it sliced, against the report's
// own figures rather than against itself.
//
// THIS IS THE GUARD, and it is worth stating what it catches. Every plausible
// mistake here is silent: a segment booked whole into both of the buckets it
// straddles inflates the pair; a cursor that advances one bucket too far drops
// a segment; an off-by-one at the last edge loses the tail of the span. All
// three produce a table that looks entirely reasonable. Summing the buckets and
// asking the report for the same figure are two independent computations, so a
// disagreement is a bug and never a rounding artefact — every quantity here is
// an integer number of nanoseconds and no division has happened.
//
// ONE CHECK HERE IS NOT A CONSERVATION CHECK, and it sits in the bucket loop
// beside the ones that are: a client owning exactly ONE project can never be
// credited more than that project INSIDE ANY BUCKET, as an inequality. It is
// there because a desync between the
// two levels is invisible to everything else in this function. The buckets are
// reconciled against the report per NAME, so booking a segment's client share
// into a neighbouring bucket while its project share stays put conserves that
// client's total exactly and reads clean. That is the second failure above — a
// cursor one bucket out of step — arriving at a level where comparing sums
// cannot see it, because moving clock between buckets changes no sum.
//
// Applied as a mutation and measured, 2026-08-13. WITHOUT this check the whole
// cold suite passed except the two constructed cases in series_test.go — at 20m0s
// credited against a project holding 10m0s — and `timetattle metrics -all -by day
// -csv` over the whole store exited 0 and printed a plausible table in which 297
// client rows sat on the wrong day. WITH it, that same run refuses, naming a
// bucket that credits a one-project client 10m0s against the 0s its project holds
// there. So the difference this makes is a silently misdated report against a
// named one, on the shipped surface, on real data.
//
// ALLOCATION IS THE ONE EXCEPTION and it is checked differently, deliberately.
// An edge that cuts a segment shared by k groups makes two divisions where the
// whole-span report made one, and k-1 nanoseconds of remainder can land on a
// different group in the pieces than in the whole. That is bounded by the
// number of cuts and cannot reach a second, so it is checked as a bound rather
// than as an equality. What is NOT relaxed is allocation's defining property
// inside each bucket: the shares of a bucket sum to exactly the presence that
// bucket divided, and that is an equality.
func (r Report) checkSeries(buckets []Period) error {
	var presence, observed, unattributed, clientless time.Duration
	byProject := map[string]time.Duration{}
	byClient := map[string]time.Duration{}
	allocated := map[string]time.Duration{}
	// The clients whose relation to the rows beneath them is fixed by
	// construction, and only those. A client with SEVERAL projects is
	// deliberately absent: its hours are its own evidence re-clustered, so it
	// legitimately holds a silence too wide for any one of its projects to
	// bridge and can exceed both their union and their sum. On the live store
	// that surplus is 19m13s of an unwindowed 617h40m54s, in 11 stretches
	// — `go run ./scripts/rollupunion` — and a check reading it would be reading
	// a correct report as a broken one. A client with ONE project is the other
	// case entirely: its events are exactly that project's events that resolved
	// to it, so the same clustering under the same parameters puts its spans
	// inside the project's, and every segment it is active in is a segment the
	// project is active in too. That containment is what the buckets inherit.
	only := make(map[string]string, len(r.Clients))
	for _, c := range r.Clients {
		if len(c.Projects) == 1 {
			only[c.Name] = c.Projects[0]
		}
	}
	for i, b := range buckets {
		presence += b.Presence.Total
		observed += b.Presence.Observed
		unattributed += b.Unattributed.Total
		clientless += b.Clientless
		var shares time.Duration
		for _, s := range b.Projects {
			byProject[s.Name] += s.Engaged.Total
			allocated[s.Name] += s.Allocated
			shares += s.Allocated
		}
		for _, s := range b.Clients {
			byClient[s.Name] += s.Engaged.Total
			// An INEQUALITY and never an equality, for the reason Compute's own
			// check is one. A project is stamped with its client per observation
			// from a log with effective dates, so a directory registered to a
			// client partway through its life has earlier evidence that is the
			// project's and no client's; the project then holds clock its client
			// does not, which this report calls Clientless. A project worked for
			// one client in the morning and another in the afternoon does the
			// same. An equality would refuse both, and refusing here yields no
			// series at all rather than a series missing a row.
			name, one := only[s.Name]
			if !one {
				continue
			}
			p, _ := b.Project(name)
			if s.Engaged.Total > p.Engaged.Total {
				return fmt.Errorf("the bucket %d (%v..%v) credits client %s %v against the %v held "+
					"there by its only project %s; the two are one body of evidence clustered twice, "+
					"so the client cannot hold clock the project does not, and a level booked into "+
					"the wrong bucket conserves every total this function already compares",
					i, b.Start, b.End, s.Name, s.Engaged.Total, p.Engaged.Total, name)
			}
		}
		if got := shares + b.Unattributed.Total; got != b.Presence.Total {
			return fmt.Errorf("the bucket %v..%v allocated %v and left %v unattributed, coming to %v "+
				"against %v of its own presence, off by %v; a shared instant was divided and the "+
				"remainder lost", b.Start, b.End, shares, b.Unattributed.Total, got, b.Presence.Total,
				got-b.Presence.Total)
		}
		if b.Presence.Total > b.Length() {
			return fmt.Errorf("the bucket %d (%v..%v) holds %v of presence across a period %v long; a "+
				"segment was counted in more than one bucket", i, b.Start, b.End, b.Presence.Total,
				b.Length())
		}
	}
	if presence != r.Presence.Total {
		return fmt.Errorf("the buckets cover %v of presence against the report's %v, off by %v; the "+
			"series does not tile the report and every figure in it is wrong",
			presence, r.Presence.Total, presence-r.Presence.Total)
	}
	if observed != r.Presence.Observed {
		return fmt.Errorf("the buckets observed %v against the report's %v, off by %v",
			observed, r.Presence.Observed, observed-r.Presence.Observed)
	}
	if unattributed != r.Unattributed.Presence.Total {
		return fmt.Errorf("the buckets leave %v unattributed against the report's %v, off by %v",
			unattributed, r.Unattributed.Presence.Total, unattributed-r.Unattributed.Presence.Total)
	}
	if clientless != r.Clientless {
		return fmt.Errorf("the buckets leave %v attributed to a project and no client against the "+
			"report's %v, off by %v", clientless, r.Clientless, clientless-r.Clientless)
	}
	for _, p := range r.Projects {
		if got := byProject[p.Name]; got != p.Engaged.Total {
			return fmt.Errorf("%s is engaged %v across the buckets and %v across the span, off by %v; "+
				"absolute engaged time overlaps between PROJECTS and never between periods, so a "+
				"series of it must sum exactly", p.Name, got, p.Engaged.Total, got-p.Engaged.Total)
		}
		if drift := allocated[p.Name] - p.Allocated; drift > maxDrift(buckets) || drift < -maxDrift(buckets) {
			return fmt.Errorf("%s is allocated %v across the buckets and %v across the span, off by %v; "+
				"cutting a shared segment can move at most a nanosecond of remainder per cut and this "+
				"is more than the cuts can account for", p.Name, allocated[p.Name], p.Allocated, drift)
		}
	}
	for _, c := range r.Clients {
		if got := byClient[c.Name]; got != c.Engaged.Total {
			return fmt.Errorf("client %s is engaged %v across the buckets and %v across the span, off "+
				"by %v", c.Name, got, c.Engaged.Total, got-c.Engaged.Total)
		}
	}
	return nil
}

// maxDrift is how far one group's allocation may legitimately move when the
// series cuts segments the whole-span report did not. Each edge cuts at most
// one segment, and a cut turns one division into two, which can move at most
// one nanosecond onto or off any one group.
func maxDrift(buckets []Period) time.Duration { return time.Duration(len(buckets)) }
