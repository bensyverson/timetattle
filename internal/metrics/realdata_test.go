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
	"github.com/bensyverson/timetattle/internal/source"
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

var (
	byClientOnce   sync.Once
	byClientReport metrics.Report
	byClientWhy    string
	// Kept apart from byClientWhy on purpose. "There is no store on this
	// machine" is a reason to skip; "the store is there and Compute REFUSED
	// it" is a failure, and folding the second into the first would report a
	// guard firing on real evidence as a test that measured nothing — the
	// absence-is-not-an-observation collapse this project has shipped three
	// times. Measured: with the client level clustered at a wider gap than the
	// projects, this is the difference between a green run with a skip in it
	// and a red one naming 30m26.849s of inflation.
	byClientRefused string
)

// concurrentDayByClient is the same day with its CLIENTS resolved, computed once.
//
// It exists because concurrentDay above computes from the store's raw events and
// those carry no client at all: the client on an observation is stamped by the
// caller from the client-project log, per observation and at that observation's
// own anchor, because a directory's owner has effective dates. Every shipped
// command does that stamping before it calls into this package — see
// cmd/timetattle and scripts/rollupunion — so a report built without it names
// ZERO clients and any assertion about the roll-up made against it would pass
// having examined nothing.
func concurrentDayByClient(t *testing.T) metrics.Report {
	t.Helper()
	byClientOnce.Do(func() {
		path, err := store.DefaultPath()
		if err != nil {
			byClientWhy = "no home directory: " + err.Error()
			return
		}
		if _, err := os.Stat(path); err != nil {
			byClientWhy = "cannot stat " + path + ": " + err.Error()
			return
		}
		st, err := store.OpenReadOnly(path)
		if err != nil {
			byClientWhy = "cannot read " + path + ": " + err.Error()
			return
		}
		defer st.Close()

		from := time.Date(2026, 7, 28, 5, 0, 0, 0, time.Local)
		events, err := st.Events(context.Background(), store.Filter{From: from, To: from.AddDate(0, 0, 1)})
		if err != nil {
			byClientWhy = "cannot read events: " + err.Error()
			return
		}
		if len(events) == 0 {
			byClientWhy = "the live store holds nothing for 2026-07-28"
			return
		}
		cm, err := st.ClientMap(context.Background(), time.Time{})
		if err != nil {
			byClientWhy = "cannot read the client-project log: " + err.Error()
			return
		}
		for i := range events {
			if events[i].Identity.Project == "" {
				continue
			}
			if c, ok := cm.At(events[i].Identity.Project, events[i].Anchor()); ok {
				events[i].Identity.Client = c
			}
		}
		cfg := config.Default()
		byClientReport, err = metrics.Compute(events, sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		})
		if err != nil {
			byClientRefused = err.Error()
		}
	})
	if byClientRefused != "" {
		t.Fatalf("Compute refused a real day of this store, which is a finding and not a "+
			"reason to skip: %s", byClientRefused)
	}
	if byClientWhy != "" {
		t.Skipf("%s", byClientWhy)
	}
	return byClientReport
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

// THE ONE THING THE ROLL-UP GUARD STILL ASSERTS, ON REAL EVIDENCE. A client
// owning exactly one project is that project's own observations clustered again
// under the same parameters, so it can never be credited more clock than the
// project is. The check that closes Compute is that sentence and nothing more,
// since 2026-08-13 — the one it replaced compared EVERY client against the SUM
// of its projects, which is false wherever a client's projects interleave and
// refused a whole day of this store over a correct figure.
//
// A constructed case cannot establish this. The relation holds by construction
// on evidence a test writes down, because a test writes down evidence that
// resolves cleanly; what it cannot arrange is a project stamped with its client
// from a log with effective dates, over months, with harvests landing in the
// middle. The whole-store reading is in
// project/2026-08-13-rollup-reclustering-resolved.md, reproduced by
// `go run ./scripts/rollupunion`; this is the same relation on the day the rest
// of this file measures.
//
// It says out loud how many clients it found in that state, because a report in
// which NO client owns exactly one project would pass this test having checked
// nothing at all, and that is not the same finding as the relation holding.
func TestAOneProjectClientIsNeverCreditedMoreThanItsProjectOnRealData(t *testing.T) {
	r := concurrentDayByClient(t)
	var checked int
	for _, c := range r.Clients {
		if len(c.Projects) != 1 {
			continue
		}
		p, ok := r.Project(c.Projects[0])
		if !ok {
			t.Errorf("a client names a project the report does not hold, so the roll-up cannot " +
				"be traced to the rows it came from")
			continue
		}
		checked++
		if c.Engaged.Total > p.Engaged.Total {
			t.Errorf("a client with one project is credited %v against the %v its project holds; "+
				"the two are the same evidence clustered twice and this cannot happen",
				c.Engaged.Total, p.Engaged.Total)
		}
	}
	if checked == 0 {
		t.Skipf("no client on 2026-07-28 owns exactly one project, so the relation was not "+
			"exercised; the report names %d client(s)", len(r.Clients))
	}
	t.Logf("checked %d client(s) owning exactly one project", checked)
}

// THE ACTIVITY MIX ON REAL EVIDENCE, and the property a fixture cannot
// establish: that the families genuinely OVERLAP over a real day. A constructed
// case can be arranged to overlap; only real data can say the overlap is what
// the corpus is actually like, which is what makes a partition by source the
// wrong shape rather than merely an unnecessary one.
//
// It also exercises the whole of the mix's internal arithmetic, because that
// check lives inside Compute: a family credited clock another family also
// reaches, or a stretch counted as unreached while something reaches it, makes
// Compute REFUSE — and concurrentDayByClient reports a refusal as a failure
// rather than as a reason to skip.
func TestTheFamiliesOverlapOnRealDataAndNoneExceedsPresence(t *testing.T) {
	r := concurrentDayByClient(t)
	if len(r.Mix.Families) < 2 {
		t.Skipf("2026-07-28 holds %d evidence family/families, so nothing can overlap; "+
			"the mix cannot be exercised on this day", len(r.Mix.Families))
	}

	var participated, alone time.Duration
	for _, f := range r.Mix.Families {
		participated += f.Engaged.Total
		alone += f.Solely
		if f.Engaged.Total > r.Presence.Total {
			t.Errorf("family %s participated in %v against %v of presence; a family's clock is "+
				"intersected with the sittings and cannot exceed them", f.Name,
				f.Engaged.Total, r.Presence.Total)
		}
		if f.Solely > f.Engaged.Total {
			t.Errorf("family %s holds %v alone of the %v it participated in", f.Name,
				f.Solely, f.Engaged.Total)
		}
		if len(f.Sources) == 0 {
			t.Errorf("family %s names no source id, so the mapping behind it cannot be checked "+
				"by anybody reading the report", f.Name)
		}
	}
	if participated <= r.Presence.Total {
		t.Errorf("the families summed to %v against %v of presence on a real day; if they never "+
			"overlapped, a partition by source would be the right shape after all and this "+
			"whole section is built on the wrong premise", participated, r.Presence.Total)
	}
	// The disjoint half, asserted from outside as well as inside Compute.
	if alone+r.Mix.Unreached > r.Presence.Total {
		t.Errorf("what the families hold alone (%v) plus what none of them reaches (%v) exceeds "+
			"%v of presence", alone, r.Mix.Unreached, r.Presence.Total)
	}
	t.Logf("%d families, %v participated against %v of presence, %v held alone, %v reached by none",
		len(r.Mix.Families), participated, r.Presence.Total, alone, r.Mix.Unreached)
}

// EVERY STREAM THIS STORE ACTUALLY HOLDS HAS A FAMILY, checked against a list
// read out of the store rather than one written by hand.
//
// cmd/timetattle's TestSourceFamilyMapsEveryKnownSourceID covers every id the
// built binary's own collectors can emit, which is the other half and not this
// one: a MANIFEST may declare any id it likes, so a store can hold a stream no
// Go package names. A mapping checked only against the code would report clean
// against a list that cannot see them — the failure this repository has logged
// twice, where a clean report is indistinguishable from a correct one.
func TestEveryStreamTheLiveStoreHoldsHasAFamily(t *testing.T) {
	path, err := store.DefaultPath()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("cannot stat %s: %v", path, err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Skipf("cannot read %s: %v", path, err)
	}
	defer st.Close()

	// Freshness names every source the store has ever TRIED to harvest, which
	// is the widest list the store can produce and cannot go stale the way a
	// literal one would.
	fresh, err := st.Freshness(context.Background())
	if err != nil {
		t.Fatalf("cannot read what this store has harvested: %v", err)
	}
	if len(fresh) == 0 {
		t.Skip("this store has never harvested anything, so there is no list to check")
	}
	var unclassified []string
	for _, f := range fresh {
		if _, ok := source.FamilyOf(f.Source); !ok {
			unclassified = append(unclassified, f.Source)
		}
	}
	if len(unclassified) > 0 {
		t.Errorf("this store holds %d stream(s) no family has been decided for: %v. They are "+
			"reported under %q rather than dropped, but somebody has to decide where they "+
			"belong: internal/source/family.go", len(unclassified), unclassified,
			metrics.Unclassified)
	}
	t.Logf("checked %d stream(s) this store has harvested", len(fresh))
}
