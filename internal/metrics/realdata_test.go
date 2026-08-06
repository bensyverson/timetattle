//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package metrics_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/store"
)

// These run against the live store and skip cleanly when it is not there,
// following internal/store/realdata_test.go.
//
// They exist because a fixture cannot establish that this arithmetic survives
// real evidence, and because the specific failure this package could have —
// crediting a client hours it did not work — is one a constructed case cannot
// make plausible. What they assert are PROPERTIES rather than figures: a fixed
// number here would break the first time a harvest ran, and the properties are
// the things that must hold whatever the store holds.
//
// The window is the working day 2026-07-28, which
// project/private/2026-07-31-concurrent-project-validation.md documents as the one
// genuinely concurrent day in the corpus: birch owned the morning, acorn and
// timetattle interleaved the afternoon in the same clock hours. It is scoped to
// one day rather than the whole store so the suite stays in seconds; the
// unwindowed figures are published in project/private/2026-08-01-two-metrics.md with
// the command that reproduces them, because a clipped window that nobody says
// is clipped is a failure mode this project has logged twice.

var (
	realOnce   sync.Once
	realReport metrics.Report
	realWhy    string
)

// concurrentDay is the report for the working day 2026-07-28, computed once.
func concurrentDay(t *testing.T) metrics.Report {
	t.Helper()
	realOnce.Do(func() {
		path, err := store.DefaultPath()
		if err != nil {
			realWhy = "no home directory: " + err.Error()
			return
		}
		if _, err := os.Stat(path); err != nil {
			realWhy = "cannot stat " + path + ": " + err.Error()
			return
		}
		st, err := store.OpenReadOnly(path)
		if err != nil {
			realWhy = "cannot read " + path + ": " + err.Error()
			return
		}
		defer st.Close()

		// The working day, 05:00 to 05:00 local, which is the boundary
		// internal/sessions' calendar defends and the one ground-truth.md's
		// figures for this day are quoted against.
		from := time.Date(2026, 7, 28, 5, 0, 0, 0, time.Local)
		events, err := st.Events(context.Background(), store.Filter{From: from, To: from.AddDate(0, 0, 1)})
		if err != nil {
			realWhy = "cannot read events: " + err.Error()
			return
		}
		if len(events) == 0 {
			realWhy = "the live store holds nothing for 2026-07-28"
			return
		}
		cfg := config.Default()
		realReport, err = metrics.Compute(events, sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		})
		if err != nil {
			realWhy = "Compute: " + err.Error()
		}
	})
	if realWhy != "" {
		t.Skipf("%s", realWhy)
	}
	return realReport
}

// Criterion 37i on real data. Engaged time overlaps across projects, and
// presence counts the clock once — measured on a day three projects genuinely
// interleaved rather than on a fixture arranged to make it true.
//
// The comparison is against the presence those projects SHARE rather than
// against the whole day's, because most of this corpus belongs to directories
// nobody has registered; comparing against total presence would be measuring
// the registry rather than the overlap.
func TestEngagedOverlapsWhilePresenceDoesNotOnAConcurrentDay(t *testing.T) {
	r := concurrentDay(t)
	if len(r.Projects) < 2 {
		t.Skipf("the 2026-07-28 report names %d project; two are needed before anything can overlap",
			len(r.Projects))
	}

	var absolute, allocated time.Duration
	for _, p := range r.Projects {
		absolute += p.Engaged.Total
		allocated += p.Allocated
		if p.Engaged.Total > r.Presence.Total {
			t.Errorf("%s was engaged %v against %v of presence; no project can be worked for longer "+
				"than the clock it sat in", p.Name, p.Engaged.Total, r.Presence.Total)
		}
	}
	// Allocated is the union of the projects' active spans, counted once.
	// Absolute exceeding it IS the overlap, and the whole reason the two
	// metrics cannot collapse.
	if absolute <= allocated {
		t.Errorf("absolute engaged %v against allocated %v; on a day three projects interleaved, "+
			"concurrent work must bill to both and exceed the clock it happened in", absolute, allocated)
	}

	// And the overlap must be real rather than an artefact of one project:
	// some instant of the day belongs to two of them at once.
	shared := 0
	for _, s := range r.Segments {
		if len(s.Projects) > 1 {
			shared++
		}
	}
	if shared == 0 {
		t.Error("no segment of 2026-07-28 holds more than one project, yet " +
			"project/private/2026-07-31-concurrent-project-validation.md measures three interleaving in " +
			"the same clock hours")
	}
}

// Criterion pRM on real data. Under allocation the shares plus the
// unattributed presence are the presence they were split from, to the
// nanosecond, across a real day's segmentation rather than a two-event one.
func TestAllocationSumsExactlyToPresenceOnRealData(t *testing.T) {
	r := concurrentDay(t)
	var allocated time.Duration
	for _, p := range r.Projects {
		allocated += p.Allocated
	}
	if got := allocated + r.Unattributed.Presence.Total; got != r.Presence.Total {
		t.Errorf("allocated %v plus unattributed %v = %v against %v of presence, off by %v",
			allocated, r.Unattributed.Presence.Total, got, r.Presence.Total, got-r.Presence.Total)
	}
}

// Criterion kHo on real data, and the reconciliation between two tools.
// `timetattle sessionize` quotes a median and a p95 over the same sittings by
// the same nearest-rank rule, so the two must agree exactly rather than nearly.
func TestTheDistributionReconcilesWithTheSittingsItDescribes(t *testing.T) {
	r := concurrentDay(t)
	if r.Sittings.Count != len(r.Sessions) {
		t.Errorf("the distribution describes %d sittings against %d derived", r.Sittings.Count, len(r.Sessions))
	}
	if r.Sittings.Total != r.Presence.Total {
		t.Errorf("the sitting lengths total %v against %v of presence; sittings are disjoint, so the "+
			"two are the same clock", r.Sittings.Total, r.Presence.Total)
	}

	var counted int
	var bucketed time.Duration
	for _, b := range r.Sittings.Buckets {
		counted += b.Count
		bucketed += b.Total
	}
	if counted != r.Sittings.Count || bucketed != r.Sittings.Total {
		t.Errorf("the buckets hold %d sittings totalling %v against %d totalling %v; every length "+
			"must land in exactly one band", counted, bucketed, r.Sittings.Count, r.Sittings.Total)
	}
	if r.Sittings.Median > r.Sittings.P95 || r.Sittings.Shortest > r.Sittings.Median ||
		r.Sittings.P95 > r.Sittings.Longest {
		t.Errorf("the quantiles are out of order: shortest %v, median %v, p95 %v, longest %v",
			r.Sittings.Shortest, r.Sittings.Median, r.Sittings.P95, r.Sittings.Longest)
	}
}

// Criterion 86I on real data, and the distinction that matters most. Both kinds
// of unattributed presence have to be REACHABLE on real evidence, or the
// separation is a decorative one: the store holds evidence with a path nobody
// registered and evidence with no path at all, and they are different problems.
func TestBothKindsOfUnattributedPresenceOccurOnRealData(t *testing.T) {
	r := concurrentDay(t)
	u := r.Unattributed
	if u.Unregistered+u.Unlocatable+u.Bridged != u.Presence.Total {
		t.Fatalf("the reasons sum to %v against %v of unattributed presence",
			u.Unregistered+u.Unlocatable+u.Bridged, u.Presence.Total)
	}
	if u.Unlocatable <= 0 {
		t.Error("no unattributed presence is put down to a source carrying no path, yet knowledgeC " +
			"and Biome carry none and are most of the day's observations")
	}
	if u.UnlocatableEvents == 0 {
		t.Error("no observations counted as unlocatable, yet Biome and knowledgeC emit no locator at all")
	}
	// A bridged stretch is the third state and nothing can have observed it.
	for _, s := range r.Segments {
		if s.Unattributed == metrics.ReasonBridged && s.Observed {
			t.Errorf("segment %v..%v is bridged silence and reads as observed", s.Start, s.End)
		}
		if len(s.Projects) > 0 && s.Unattributed != "" {
			t.Errorf("segment %v..%v names projects %v and a reason for having none", s.Start, s.End, s.Projects)
		}
	}
}

// Presence is not screen-on time, and on real data the gap between them is
// large in BOTH directions. The backlight sees clock the sittings do not, which
// is the warm-up bias ground-truth.md finding 4 records; and the sittings cover
// clock the backlight is silent about, which is knowledgeC having pruned itself.
func TestTheBacklightAndTheSittingsDisagreeInBothDirectionsOnRealData(t *testing.T) {
	r := concurrentDay(t)
	b := r.Backlight
	if b.Readings == 0 {
		t.Skip("the live store holds no backlight readings for 2026-07-28")
	}
	if b.Corroborated > r.Presence.Total {
		t.Errorf("the backlight corroborated %v of %v of presence, which is more than there is",
			b.Corroborated, r.Presence.Total)
	}
	if b.Corroborated+b.Unwitnessed > r.Presence.Total {
		t.Errorf("corroborated %v plus unwitnessed %v exceeds %v of presence; the two must be "+
			"disjoint parts of it", b.Corroborated, b.Unwitnessed, r.Presence.Total)
	}
	if b.LitOutsideASitting <= 0 {
		t.Error("no lit clock falls outside a sitting on 2026-07-28, yet ground-truth.md records " +
			"the screen coming on at 05:18 before work began and the day's first block being the " +
			"longest of it")
	}
	if b.Lit < b.Corroborated {
		t.Errorf("lit %v is less than the %v of it that overlapped a sitting", b.Lit, b.Corroborated)
	}
}

// The reconciliation between this package and the clustering it rests on.
// Presence is the sum of the sitting durations and nothing else, so if the two
// ever differ, one of them is counting an instant twice.
func TestPresenceIsExactlyTheSittingsOnRealData(t *testing.T) {
	r := concurrentDay(t)
	var summed time.Duration
	for i, s := range r.Sessions {
		summed += s.Duration()
		if i > 0 && s.Start.Before(r.Sessions[i-1].End) {
			t.Fatalf("sitting %d starts at %v, inside the one before it that ended at %v; presence "+
				"would double-count the overlap", i, s.Start, r.Sessions[i-1].End)
		}
	}
	if summed != r.Presence.Total {
		t.Errorf("the sittings total %v against %v of presence", summed, r.Presence.Total)
	}
	var segmented time.Duration
	for _, s := range r.Segments {
		segmented += s.Duration()
	}
	if segmented != r.Presence.Total {
		t.Errorf("the segments cover %v against %v of presence", segmented, r.Presence.Total)
	}
}

// Every source that can be attributed to a project emits instants, so on real
// data no project's engaged time is OBSERVED as a duration at all. This is the
// finding rather than a gap, and the three quantities must stay ordered
// whatever the corpus grows to hold.
func TestObservedIsInsideCorroboratedIsInsideEngagedOnRealData(t *testing.T) {
	r := concurrentDay(t)
	if len(r.Projects) == 0 {
		t.Skip("the 2026-07-28 report names no project")
	}
	var corroborated time.Duration
	for _, p := range r.Projects {
		if p.Engaged.Observed > p.Corroborated || p.Corroborated > p.Engaged.Total {
			t.Errorf("%s: observed %v, corroborated %v, engaged %v; each must be inside the next",
				p.Name, p.Engaged.Observed, p.Corroborated, p.Engaged.Total)
		}
		corroborated += p.Corroborated
	}
	if corroborated == 0 {
		t.Error("no project's engaged time is corroborated by any source at all, yet Biome recorded " +
			"10.43h of application focus on this day")
	}
}

// Real data can validate a wrong rule, so the model is checked against a figure
// this package did not produce. project/private/2026-07-31-concurrent-project-validation.md
// measures birch's 2026-07-28 from Claude transcripts alone, by different
// code, at a different gap threshold, and gets 2.5h — with birch's evidence
// 97% concentrated before 09:00. Whatever the registry currently attributes,
// birch's engaged time on this day cannot be most of it.
func TestBirchDoesNotSwallowTheConcurrentDay(t *testing.T) {
	r := concurrentDay(t)
	birch, ok := r.Project("birch")
	if !ok {
		// What is missing is the NAME, not the attribution. The report is
		// built here and TestTheConcurrentDayStillCarriesAttribution, on this
		// same report, fails unless the registry resolved something — so a
		// skip claiming the registry had resolved nothing was contradicted by
		// a passing test in this file. Say only what was looked for and what
		// was found. The projects are not listed because their names are the
		// thing this repository does not carry.
		t.Skipf("the 2026-07-28 report names %d projects, none of them %q", len(r.Projects), "birch")
	}
	if share := birch.Engaged.Total.Seconds() / r.Presence.Total.Seconds(); share > 0.5 {
		t.Errorf("birch is engaged %v of %v of presence (%.0f%%), yet the concurrent-project "+
			"validation measures 2.5h of it and puts 97%% of birch's evidence before 09:00. "+
			"Crediting a project the whole of every sitting its evidence touches looks like this",
			birch.Engaged.Total, r.Presence.Total, 100*share)
	}
}

// A guard on the corpus rather than on this package: if the events on the
// concurrent day stop carrying project attribution altogether, every assertion
// above degrades to a skip and the suite goes quiet. This says so out loud.
func TestTheConcurrentDayStillCarriesAttribution(t *testing.T) {
	r := concurrentDay(t)
	if len(r.Projects) == 0 {
		t.Fatal("no project is attributable on 2026-07-28. Either the registry has lost its markers " +
			"or the attribution pass has stopped resolving them; run `timetattle harvest` and check " +
			"`timetattle metrics -from 2026-07-28 -to 2026-07-29`")
	}
	var events int
	for _, p := range r.Projects {
		events += p.Events
	}
	if events == 0 {
		t.Error("projects are listed but rest on no observations at all")
	}
}

// TASK KrfMh ON REAL DATA. The three-way split of a project's engaged time has
// to close to the nanosecond over a real day's segmentation and not only over a
// constructed one. What it guards is a stretch counted as attested AND weak,
// which would make a project look better evidenced than it is by exactly the
// amount nobody can see.
func TestTheThreeWaySplitClosesOnEveryProjectOfARealDay(t *testing.T) {
	r := concurrentDay(t)
	for _, p := range r.Projects {
		if got := p.Attested + p.Weak + p.Unaccounted; got != p.Engaged.Total {
			t.Errorf("%s: attested %v + weak %v + unaccounted %v = %v against %v engaged, off by %v",
				p.Name, p.Attested, p.Weak, p.Unaccounted, got, p.Engaged.Total, got-p.Engaged.Total)
		}
		if p.Weak < 0 || p.Attested < 0 || p.Unaccounted < 0 {
			t.Errorf("%s: attested %v, weak %v, unaccounted %v — a negative share means one "+
				"stretch was counted twice", p.Name, p.Attested, p.Weak, p.Unaccounted)
		}
		t.Logf("  %-16s engaged %8v = attested %8v + weak %8v + unaccounted %v",
			p.Name, p.Engaged.Total.Round(time.Minute), p.Attested.Round(time.Minute),
			p.Weak.Round(time.Minute), p.Unaccounted.Round(time.Second))
	}
}

// WEAK IS NOT UNOBSERVED RESTATED, and the live corpus is where that is
// provable. Observed asks whether any source watched a DURATION, which on this
// corpus is largely a question about whether Biome and knowledgeC were running.
// A project can read 0.0h observed and still be attested for nearly all of its
// hours, because transcripts and commits are instants and an instant observes
// no duration: over the whole corpus FirstPassPro reads 64.3h absolute, 0.0h
// observed and 6.1h weak (project/private/2026-08-01-weak-project-days.md).
//
// If the two ever coincided, the weak column would be restating a column that
// already exists.
func TestWeakIsNotTheSameQuantityAsUnobservedOnRealData(t *testing.T) {
	r := concurrentDay(t)
	var found bool
	for _, p := range r.Projects {
		inferred := p.Engaged.Total - p.Engaged.Observed
		if inferred > 0 && p.Weak < inferred {
			found = true
			t.Logf("  %-16s %v engaged, %v of it unobserved, only %v of it weak",
				p.Name, p.Engaged.Total.Round(time.Minute), inferred.Round(time.Minute),
				p.Weak.Round(time.Minute))
		}
	}
	if !found {
		t.Error("every project's weak time equals its unobserved time on 2026-07-28, which would " +
			"make the weak column a restatement of the observed one. Check that the attesting " +
			"sources in internal/source still reach this day's evidence")
	}
}
