package main

import (
	"slices"
	"sort"
	"time"
)

// coreDataEpoch is 2001-01-01 UTC; Core Data stores seconds from there.
const coreDataEpoch = 978307200

// Presence is the three-way reading this corpus can give about one stream in
// one snapshot, and keeping the three apart is the whole point of the type.
//
// Collapsing "read it, there is nothing there" into "could not read it" is a
// bug this repository has shipped three times. A stall measured across a
// snapshot nobody could open is not a measured stall.
type Presence int

const (
	// PresenceUnreadable means the snapshot itself could not be opened or
	// decompressed, so NOTHING was observed about any stream in it.
	PresenceUnreadable Presence = iota
	// PresenceNoRows means the snapshot was read and holds no ZOBJECT row
	// naming this stream. That is an observation, but a weak one: it cannot
	// distinguish a stream that is empty from one this macOS version does not
	// have at all, and the report says so rather than implying otherwise.
	PresenceNoRows
	// PresenceRows means the snapshot was read and holds rows for the stream.
	PresenceRows
)

func (p Presence) String() string {
	switch p {
	case PresenceRows:
		return "rows"
	case PresenceNoRows:
		return "no rows"
	default:
		return "unreadable"
	}
}

// Observation is one stream's state inside one snapshot: what the SOURCE held
// at the instant a harvest copied it.
type Observation struct {
	// Snapshot is the file name, and Captured is the instant in that name,
	// which is when the harvest ran rather than when any row was written.
	Snapshot string
	Captured time.Time
	Stream   string
	Presence Presence
	// Rows, Oldest and Newest are meaningful only when Presence is
	// PresenceRows.
	Rows   int
	Oldest time.Time
	Newest time.Time
	// Undated is how many of Rows carry no start date. Those rows exist and
	// are counted, but say nothing about when. Filtering them out in SQL would
	// report a wholly undated stream as a stream with no rows.
	Undated int
	// Days counts rows by the calendar day (UTC) of their start time. It is
	// what turns "the floor is volume-based" into a rate.
	Days map[string]int
	// Starts are every dated row's start time, ascending. Holding them is what
	// lets the series name a SPECIFIC row that was present in one snapshot and
	// gone from the next, which is the only honest way to measure how long a
	// row survives. The min/max alone cannot do it: an oldest row that is only
	// eight days old may mean rows are evicted at eight days, or may mean the
	// stream was quiet and there was never anything older.
	Starts []time.Time
}

// Eviction is a row observed present in one snapshot and absent from the next.
// Unlike a retained span it is a measurement of a real row's real death, and
// it is the only thing in this file that bounds retention from ABOVE.
type Eviction struct {
	Stream string
	// Youngest is the newest row that made the crossing. Rows older than it
	// went too; it is the one that puts the tightest bound on the deadline.
	Youngest time.Time
	// Rows is how many rows crossed.
	Rows int
	// Before and After are the capture times bracketing the disappearance.
	Before time.Time
	After  time.Time
}

// SurvivedAtLeast is the age Youngest had reached while still present.
func (e Eviction) SurvivedAtLeast() time.Duration { return e.Before.Sub(e.Youngest) }

// GoneBy is the age by which Youngest had definitely been deleted. The minimum
// of this across a corpus is the deadline: a row of that age was observed to
// be gone, so nothing may assume a longer window.
func (e Eviction) GoneBy() time.Duration { return e.After.Sub(e.Youngest) }

// evictions names, for each consecutive readable pair, the youngest row that
// was in the first snapshot and not in the second.
//
// "Not in the second" is decided by the second snapshot's oldest row: anything
// starting before it is necessarily gone. That is a conservative test — a row
// deleted from the middle of the range would be missed — and conservative in
// the right direction, since every eviction it does report is one it can name.
func evictions(obs []Observation) []Eviction {
	var out []Eviction
	for i := 1; i < len(obs); i++ {
		prev, cur := obs[i-1], obs[i]
		if prev.Presence != PresenceRows || cur.Presence != PresenceRows {
			continue
		}
		if cur.Oldest.IsZero() {
			continue
		}
		var youngest time.Time
		n := 0
		for _, s := range prev.Starts {
			if s.Before(cur.Oldest) {
				n++
				if s.After(youngest) {
					youngest = s
				}
			}
		}
		if n == 0 {
			continue
		}
		out = append(out, Eviction{
			Stream:   cur.Stream,
			Youngest: youngest,
			Rows:     n,
			Before:   prev.Captured,
			After:    cur.Captured,
		})
	}
	return out
}

// gapProfile describes how a stream's rows are spaced — its own idea of
// normal, against which a freeze either is or is not remarkable.
type gapProfile struct {
	// Instants is how many distinct start times were seen; Gaps is one fewer,
	// and is zero rather than absent when a single instant was observed.
	Instants int
	Gaps     int

	Median, P95, P99, Max time.Duration

	// sorted is every gap, ascending. It is kept because the maximum gap of a
	// stream that has stalled IS the stall, so a report that judges a freeze
	// against the maximum is comparing a fault to itself. Counting how many
	// gaps clear a threshold separates the ordinary tail from the fault.
	sorted []time.Duration
}

// countOver is how many gaps are longer than d.
func countOver(p gapProfile, d time.Duration) int {
	i := sort.Search(len(p.sorted), func(k int) bool { return p.sorted[k] > d })
	return len(p.sorted) - i
}

// profileGaps measures the spacing of distinct start instants. Repeats collapse:
// two rows sharing a start time are one observation of the source, not a
// zero-second gap, and counting them would drag every percentile to zero.
func profileGaps(starts []time.Time) gapProfile {
	s := append([]time.Time(nil), starts...)
	slices.SortFunc(s, func(a, b time.Time) int { return a.Compare(b) })
	s = slices.CompactFunc(s, func(a, b time.Time) bool { return a.Equal(b) })

	p := gapProfile{Instants: len(s)}
	if len(s) < 2 {
		return p
	}
	gaps := make([]time.Duration, 0, len(s)-1)
	for i := 1; i < len(s); i++ {
		gaps = append(gaps, s[i].Sub(s[i-1]))
	}
	slices.Sort(gaps)
	p.Gaps = len(gaps)
	p.sorted = gaps
	p.Median = quantile(gaps, 0.50)
	p.P95 = quantile(gaps, 0.95)
	p.P99 = quantile(gaps, 0.99)
	p.Max = gaps[len(gaps)-1]
	return p
}

// quantile indexes a sorted slice by nearest rank.
func quantile(sorted []time.Duration, q float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	i := int(q * float64(len(sorted)))
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

// Stall is a maximal run of consecutive snapshots in which a stream's newest
// row did not move. It is bounded by capture times, not by row times: the
// corpus says the stream was already stalled when the first snapshot was taken
// and still stalled at the last, and says nothing whatever about the interval
// before the first or after the last.
type Stall struct {
	Stream string
	// Newest is the row the stream was stuck on.
	Newest time.Time
	// FirstSeen and LastSeen are the capture times of the first and last
	// snapshots that observed the freeze.
	FirstSeen time.Time
	LastSeen  time.Time
	// Snapshots is how many consecutive readable snapshots observed it.
	Snapshots int
}

// Observed is how long the freeze was directly witnessed. The real stall is
// at least this long and may be longer at both ends.
func (s Stall) Observed() time.Duration { return s.LastSeen.Sub(s.FirstSeen) }

// Trim is a consecutive pair of snapshots between which the stream's oldest
// row advanced — the direct evidence that rows leave the source.
type Trim struct {
	Stream string
	// Before and After are the two capture times bracketing the eviction. The
	// trim happened somewhere inside that interval and the corpus cannot say
	// where.
	Before time.Time
	After  time.Time
	// OldestBefore and OldestAfter bracket what was discarded: every row
	// between them is gone.
	OldestBefore time.Time
	OldestAfter  time.Time
	RowsBefore   int
	RowsAfter    int
}

// Discarded is how much of the retained span the trim cut away.
func (t Trim) Discarded() time.Duration { return t.OldestAfter.Sub(t.OldestBefore) }

// stalls returns every run of two or more consecutive readable observations
// whose newest row is identical.
//
// A run is broken by any observation that is not PresenceRows, and that is the
// rule the whole type exists for. Two frozen readings either side of a
// snapshot nobody could open are not a witnessed freeze: the stream could have
// advanced and rolled back to the same value in between, which is unlikely, or
// far more plainly, the corpus simply did not look. Bridging the hole would
// publish a stall duration longer than anything was observed for.
func stalls(obs []Observation) []Stall {
	var out []Stall
	for i := 0; i < len(obs); {
		if obs[i].Presence != PresenceRows {
			i++
			continue
		}
		j := i + 1
		for j < len(obs) && obs[j].Presence == PresenceRows && obs[j].Newest.Equal(obs[i].Newest) {
			j++
		}
		if j-i >= 2 {
			out = append(out, Stall{
				Stream:    obs[i].Stream,
				Newest:    obs[i].Newest,
				FirstSeen: obs[i].Captured,
				LastSeen:  obs[j-1].Captured,
				Snapshots: j - i,
			})
		}
		i = j
	}
	return out
}

// trims returns every consecutive readable pair whose oldest row advanced.
//
// The pair must be adjacent in the record AND both readable. A trim inferred
// across an unreadable snapshot would still be a real eviction, but the
// interval bracketing it would be wrong, and the bracket is the only thing the
// corpus actually establishes about when a trim ran.
func trims(obs []Observation) []Trim {
	var out []Trim
	for i := 1; i < len(obs); i++ {
		prev, cur := obs[i-1], obs[i]
		if prev.Presence != PresenceRows || cur.Presence != PresenceRows {
			continue
		}
		if prev.Oldest.IsZero() || cur.Oldest.IsZero() || !cur.Oldest.After(prev.Oldest) {
			continue
		}
		out = append(out, Trim{
			Stream:       cur.Stream,
			Before:       prev.Captured,
			After:        cur.Captured,
			OldestBefore: prev.Oldest,
			OldestAfter:  cur.Oldest,
			RowsBefore:   prev.Rows,
			RowsAfter:    cur.Rows,
		})
	}
	return out
}

// horizons is the age of the oldest row the source still held, one per
// readable observation. The MINIMUM of these is the shortest retention the
// source was ever seen to offer, which is the deadline a harvest schedule has
// to beat.
func horizons(obs []Observation) []time.Duration {
	var out []time.Duration
	for _, o := range obs {
		if o.Presence != PresenceRows || o.Oldest.IsZero() {
			continue
		}
		out = append(out, o.Captured.Sub(o.Oldest))
	}
	return out
}

// summary is the min, median and max of a set of durations.
type summary struct {
	Min, Median, Max time.Duration
	N                int
}

func summarize(d []time.Duration) summary {
	if len(d) == 0 {
		return summary{}
	}
	s := append([]time.Duration(nil), d...)
	slices.Sort(s)
	return summary{Min: s[0], Median: s[len(s)/2], Max: s[len(s)-1], N: len(s)}
}

// impliedHorizon turns a row cap into a deadline at a given daily rate: how
// long a row survives when the cap is what evicts it.
//
// It reports ok=false rather than a duration when the rate is zero. No rows a
// day does not mean infinite retention; it means nothing was recorded, and a
// tool that returns a number there is asserting something it did not measure.
func impliedHorizon(cap int, rowsPerDay float64) (time.Duration, bool) {
	if cap <= 0 || rowsPerDay <= 0 {
		return 0, false
	}
	return time.Duration(float64(cap) / rowsPerDay * float64(24*time.Hour)), true
}

// medianCount is the typical number of rows a day. It returns 0 for an empty
// set, which callers must treat as "no rate measured" rather than dividing by
// it.
func medianCount(days map[string]int) int {
	if len(days) == 0 {
		return 0
	}
	counts := make([]int, 0, len(days))
	for _, n := range days {
		counts = append(counts, n)
	}
	slices.Sort(counts)
	return counts[len(counts)/2]
}

// busiestDay returns the calendar day with the most rows and that count. Ties
// break on the earlier day so the answer does not depend on map order.
func busiestDay(days map[string]int) (string, int) {
	best, most := "", 0
	for day, n := range days {
		if n > most || (n == most && best != "" && day < best) {
			best, most = day, n
		}
	}
	return best, most
}
