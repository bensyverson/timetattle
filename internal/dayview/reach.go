package dayview

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// HOW FAR ANYTHING HAS LOOKED AT A DAY, WHICH IS NOT HOW MUCH OF IT HAS
// HAPPENED.
//
// `review` draws a day from the store as it stands and then asks the reviewer
// to account for the WHOLE of it. Every gap between blocks becomes a
// `not-engaged` assertion, so a stretch nothing has been asked about is
// recorded as a stretch in which nothing happened. That is this project's
// cardinal bug — absence of a row read as an observation — and it has now been
// measured rather than supposed.
//
// # The measurement, and it is not the one the task assumed
//
// On 2026-08-01 a review recorded at 18:23:57.710973Z produced 4.1627h of
// assertion that the current derivation contradicts inside the already-elapsed
// part of the day. The standing explanation was harvest lag: the store behind
// the world by up to the 12h harvest interval. IT IS NOT, AND THE FIGURES SAY
// SO OUTRIGHT. Harvests ran at 14:14, 16:11–16:35, 17:02–17:07 and 17:57–18:00
// that afternoon, every source succeeding, the last finishing at 18:00:39Z —
// 23 minutes before the review. All 1,989 observations under the disputed
// 12:59:04–14:06:48 sitting were in `event` by 16:29:24Z and NOT ONE arrived
// after the review.
//
// What was behind was the DERIVATION. `review` draws blocks out of
// `derived_session`, which only `timetattle sessionize` writes, and the day
// Ben was shown had its last sitting reaching 16:39:25.229Z. Splitting the
// 4.1627h at that instant:
//
//	shown to the reviewer, and rejected block by block   2.7248h
//	beyond anything the clustering had looked at         1.4379h
//
// The first is not a defect at all — it is a drilled review doing exactly its
// job, and it is the signal this tool exists to collect. The second is the
// defect, and the binding horizon was the clustering's, not the harvest's, by
// a factor of 248 on that day. See
// project/private/2026-08-02-what-the-reviewer-could-not-have-seen.md.
//
// # Three horizons, and the earliest of them is the one that binds
//
//   - the moment of REVIEW. Nothing after it has happened.
//   - the moment of CLUSTERING. After it no sitting can exist for want of
//     having been computed, whatever the evidence says.
//   - the earliest per-source HARVEST. After it at least one source has not
//     been asked, so its silence is the store's rather than the world's.
//
// They are kept apart rather than reduced to one number because they call for
// different actions — re-run `sessionize`, fix a failing source, come back
// tomorrow — and a warning that cannot say which is a warning nobody can act
// on.

// Lag is one source and the last instant it was successfully asked about.
type Lag struct {
	Source      string
	LastHarvest time.Time
}

// Reach is how far the STORE has looked at the world. The moment of review is
// not in here: that is a property of the answer and travels with it.
//
// A zero field is "not read", which is a third state and not a fresh one. A
// caller that did not ask hands a zero and Says reports that it did not ask,
// rather than reporting that nothing is behind.
type Reach struct {
	// Derived is when the clustering last ran.
	Derived time.Time
	// Harvested is the earliest instant to which EVERY source has been asked:
	// the minimum over sources of their last successful harvest. The minimum
	// and not the maximum, because a sitting may rest on any one source, so the
	// store has covered a stretch only once all of them have.
	Harvested time.Time
	// Behind names the sources ordered by how far short they fall, so a warning
	// can say which and by how much. It is every source that was read, not only
	// the laggards: which one is worth naming depends on the moment being
	// compared against, which is the caller's.
	Behind []Lag
}

// Bounds is the last instant a day may be asserted about, and which of the
// three horizons decided it.
//
// The name of the bound is a word for a person: "review", "clustering",
// "harvest". It is returned rather than computed by the caller so that the
// horizon and the reason for it cannot drift apart.
func (r Reach) Bounds(now time.Time) (time.Time, string) {
	horizon, bound := now, "review"
	for _, c := range []struct {
		at   time.Time
		name string
	}{{r.Derived, "clustering"}, {r.Harvested, "harvest"}} {
		if !c.at.IsZero() && c.at.Before(horizon) {
			horizon, bound = c.at, c.name
		}
	}
	return horizon, bound
}

// Says is the warning a day view prints: what is behind, and by how long.
//
// It returns a sentence and not a flag, because "the clustering last ran three
// hours ago" and "biome-appusage has not been asked since lunchtime" are two
// different things to go and do. An empty Reach says it was not read: reporting
// silence as freshness is the bug this whole file exists to remove.
func (r Reach) Says(now time.Time) string {
	if r.Derived.IsZero() && r.Harvested.IsZero() && len(r.Behind) == 0 {
		return "how fresh this store is was not read, so how much of this day has been " +
			"looked at is unknown — which is not the same as all of it"
	}

	var parts []string
	if !r.Derived.IsZero() && r.Derived.Before(now) {
		// "clustered" and not "grouped into sittings", and the wording is load
		// bearing rather than a style preference. This sentence is WRAPPED into
		// the day view, cmd/timetattle's tests find the timeline by looking for
		// a line beginning "sittings ", and a wrap landing on that word puts a
		// second such line on the screen. A reader scanning the same way is
		// misled in exactly the way the test was.
		parts = append(parts, fmt.Sprintf("the clustering last ran %s ago (%s), so nothing "+
			"after that has been clustered at all, however much evidence is under it",
			since(now, r.Derived), stamp(r.Derived)))
	}

	behind := slices.Clone(r.Behind)
	slices.SortStableFunc(behind, func(a, b Lag) int { return a.LastHarvest.Compare(b.LastHarvest) })
	var named []string
	for _, l := range behind {
		if l.LastHarvest.Before(now) {
			named = append(named, fmt.Sprintf("%s %s ago", l.Source, since(now, l.LastHarvest)))
		}
	}
	if len(named) > 0 {
		parts = append(parts, "last asked: "+strings.Join(named, ", "))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ". ") + "."
}

// since renders how long ago something was, to the minute, the way the rest of
// this command renders a duration.
func since(now, then time.Time) string {
	d := max(now.Sub(then).Round(time.Minute), 0)
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

func stamp(at time.Time) string { return at.UTC().Format("2006-01-02 15:04Z") }
