package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// renderSourceFreshness shows every source this store has ever attempted to
// harvest, one line apiece: its id, how long ago its last SUCCESSFUL harvest
// finished — both as an age and as the absolute instant, so a reader never
// subtracts by hand — and, the reason this section exists, an unambiguous
// flag when nothing landed inside the selected period, so a stale source
// cannot masquerade as a quiet week (design principle 3,
// project/2026-08-07-cli-overhaul-plan.md).
//
// # Enumeration and per-source history: store.Freshness, not data.Log
//
// The brief that assigned this task pointed at data.Log (collectionLog,
// series.go) as the authority for "what was collected when." Reading it
// (this task was told to, before writing anything) shows why it cannot fill
// this section: readCollectionLog merges Captures and Runs into one flat
// []time.Time with a lost []bool alongside, and DISCARDS which source or
// capture kind each instant belongs to — by design, per its own doc comment
// ("collection is a property of the STORE," not of any one source). It can
// answer "was collection running during this period" in aggregate; it has no
// per-source identity to enumerate sources by or to report any one source's
// own last harvest, which is what both this section's criteria ask for.
//
// dashboardData's own doc comment in dashboard.go already names the actual
// query: "source freshness (store.Freshness) ... calls Store directly,
// exactly the way metrics.go itself reaches past its own Events() call to
// st.ClientMap, st.Captures and st.Runs." store.Freshness(ctx) returns
// exactly what this section prints: one row per source ever attempted in
// harvest_run, with the instant of its last successful run (the zero time if
// none ever succeeded — see internal/store/freshness.go's own doc comment
// for why a zero time is a third state and not a horizon at year one) and
// whether its latest attempt is currently failing. That three-state shape —
// never tried, tried and never succeeded, succeeded and now possibly stale —
// is this task's own three-state requirement already sitting in the store
// layer, not something to reinvent here.
//
// # Source enumeration is honest, not exhaustive
//
// The rendered set is every source that has ever appeared in harvest_run.
// `timetattle status` answers a wider question — every source the MACHINE is
// configured to run, including one never yet attempted — by reaching into
// internal/registry and internal/snapshot for the configured inputs
// (status.go's own `expected` machinery). dashboardData carries neither of
// those, and this task was told to render the log-known set and record the
// gap rather than add a new API reach — see the job note on aNRUs for the
// pushback this earned. A source truly never once attempted on this machine
// is therefore invisible here exactly as it is invisible to `timetattle
// status` without its extra machinery: that gap is real, and is recorded
// here rather than silently worked around.
func renderSourceFreshness(ctx context.Context, out io.Writer, _ metricsScope, period dashboardPeriod,
	cc colorCapability, data *dashboardData) error {
	fresh, err := data.Store.Freshness(ctx)
	if err != nil {
		return err
	}
	return writeSourceFreshness(out, fresh, period, cc, time.Now())
}

// writeSourceFreshness is renderSourceFreshness's pure body, taking `now`
// explicitly rather than reaching for time.Now() itself, so a test can pin
// "how long ago" without a live clock — the same seam status.go's own
// statusOptions.now gives failureSummary and writeTable.
func writeSourceFreshness(out io.Writer, fresh []store.Fresh, period dashboardPeriod, cc colorCapability,
	now time.Time) error {
	if _, err := fmt.Fprintf(out, "  %-24s  last harvest\n", "source"); err != nil {
		return err
	}
	if len(fresh) == 0 {
		// Silence and "nothing has ever landed here" are different facts —
		// the same rule this whole section exists to apply to every source
		// individually, applied here to the store as a whole.
		_, err := fmt.Fprintln(out, "  (no source has ever been harvested into this store)")
		return err
	}
	for _, f := range fresh {
		if _, err := fmt.Fprintf(out, "  %s\n", freshnessLine(f, period, cc, now)); err != nil {
			return err
		}
	}
	return nil
}

// freshnessState is the three-way answer design principle 3 asks every
// section to keep apart: never produced a successful harvest at all, has but
// not inside the selected period (stale — the case this section exists to
// catch, so a dead source cannot read as a quiet week), or has (fresh).
type freshnessState int

const (
	freshnessFresh freshnessState = iota
	freshnessStale
	freshnessNever
)

// classify decides fresh/stale/never from f.LastHarvest against the period
// alone, deliberately independent of `now`: a source's last SUCCESSFUL
// harvest either landed at or after period.From or it did not, and that fact
// does not change with the wall clock. There is no upper check against
// period.To — a harvest landing after the window's own end is still not
// stale, since nothing about it suggests collection stopped; it is simply
// fresher than the window asked for.
func classify(f store.Fresh, period dashboardPeriod) freshnessState {
	if f.LastHarvest.IsZero() {
		return freshnessNever
	}
	if f.LastHarvest.Before(period.From) {
		return freshnessStale
	}
	return freshnessFresh
}

// freshnessStaleTag and freshnessNeverTag are the plain-text markers a
// colorNone reader — or a test — sees. They are written whether or not color
// is available; see freshnessLine.
const (
	freshnessStaleTag = "STALE"
	freshnessNeverTag = "NEVER HARVESTED"
)

// freshnessStaleColor and freshnessNeverColor are this section's own
// palette: amber for a source that has worked before and simply has nothing
// in this window, a harder red for one that has never once succeeded — the
// two are different diagnoses (see freshnessState's own doc comment) and get
// visually distinct colors, not just distinct words.
var (
	freshnessStaleColor = RGB{224, 168, 0}
	freshnessNeverColor = RGB{214, 64, 64}
)

// freshnessLine renders one source's row.
//
// cc.Paint/cc.Reset are written UNCONDITIONALLY around the plain-text tag,
// never behind an `if cc != colorNone` branch — the contract
// dashboardRenderFunc's own doc comment in dashboard.go states. Under
// colorNone both return "", so the tag's plain word is exactly what a
// colorNone reader — and every test above using colorNone — sees; under
// color, the word is still there, just painted (see
// TestStaleTagSurvivesUnderColor).
func freshnessLine(f store.Fresh, period dashboardPeriod, cc colorCapability, now time.Time) string {
	name := fmt.Sprintf("%-24s", f.Source)
	switch classify(f, period) {
	case freshnessNever:
		return name + "  " + cc.Paint(freshnessNeverColor) + freshnessNeverTag + cc.Reset()
	case freshnessStale:
		return fmt.Sprintf("%s  %s (%s)  %s%s%s%s", name, freshnessAge(f, now), freshnessAbsolute(f),
			cc.Paint(freshnessStaleColor), freshnessStaleTag, cc.Reset(), freshnessFailingSuffix(f))
	default:
		return fmt.Sprintf("%s  %s (%s)%s", name, freshnessAge(f, now), freshnessAbsolute(f),
			freshnessFailingSuffix(f))
	}
}

// freshnessAge is the "2h ago" style relative reading, reusing the same
// rounding rule chains.go's `short` already gives status.go's own age
// columns, so a duration reads identically in `timetattle status` and here.
func freshnessAge(f store.Fresh, now time.Time) string {
	return short(now.Sub(f.LastHarvest)) + " ago"
}

// freshnessAbsolute is the instant beside the age, so a reader who does not
// trust "ago" arithmetic — or is comparing against a log timestamp — has the
// figure it was computed from.
func freshnessAbsolute(f store.Fresh) string {
	return f.LastHarvest.Local().Format("2006-01-02 15:04")
}

// freshnessFailingSuffix flags a source whose LATEST attempt is failing,
// even when its last SUCCESSFUL harvest is fresh or merely stale rather than
// nonexistent — store.Fresh's own doc comment: "a source can be both covered
// to lunchtime and failing since," and a line that could show only one of
// those would either cry wolf or say nothing. Never state already implies
// the latest attempt is failing (a source that ever succeeded would not have
// a zero LastHarvest without ALSO having a later failure), so this suffix is
// only written for the other two states.
func freshnessFailingSuffix(f store.Fresh) string {
	if !f.Failing {
		return ""
	}
	return "  (latest attempt failing)"
}
