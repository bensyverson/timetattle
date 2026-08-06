//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package fit_test

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/migrations"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// These run against the live store and skip cleanly when it is not readable,
// following internal/store/realdata_test.go.
//
// A constructed corpus cannot answer what they exist for. An over-general
// supersession rule once passed against the entire live Biome stream, deleting
// nothing, purely because no interval happened to share an endpoint with a bare
// stop — so a narrowing that is exact on eleven synthetic events proves nothing
// about one taken over 923,000 real ones, where the chains are long, the
// presence stream is contested, and the shapes are mixed.
func liveStore(t *testing.T) *store.Store {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	path := filepath.Join(home, ".timetattle", "timetattle.db")
	if _, err := os.Stat(path); err != nil {
		t.Skipf("cannot stat %s: %v", path, err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Skipf("cannot open %s read-only: %v", path, err)
	}
	t.Cleanup(func() { st.Close() })

	// A STORE BEHIND THIS BINARY IS UNREADABLE, NOT WRONG, and it has to skip
	// rather than fail. Migrations are applied by a writable open and by
	// nothing else, so between a schema change landing here and the next
	// harvest there is a window in which the live store genuinely lacks columns
	// this package reads. That is a fact about when the file was last written,
	// not a finding about the labels, and reporting it as a failure would put a
	// red suite in front of anybody who pulled before harvesting.
	//
	// Not hypothetical: migration 0011 added adjustment_review.shown on
	// 2026-08-02 and this suite went red against a live store sitting at 0010.
	scripts, err := migrations.Scripts()
	if err != nil {
		t.Skipf("cannot read the migrations: %v", err)
	}
	schema, err := st.Schema(context.Background())
	if err != nil {
		t.Skipf("cannot read the schema of %s: %v", path, err)
	}
	if latest := scripts[len(scripts)-1]; schema.Version < latest.Version {
		t.Skipf("%s is at migration %d and this binary carries %s; open the store for "+
			"writing once to migrate it", path, schema.Version, latest.Name)
	}
	return st
}

// liveEvents reads the whole corpus ONCE for the whole package.
//
// It is nearly a million rows and every test below wants the same slice; a read
// per test would double the cost of the file for no additional coverage. Cluster
// is documented not to reorder its caller's slice and nothing here writes to it,
// which is what makes one copy safe to share.
var liveEvents = sync.OnceValues(func() ([]source.Event, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	st, err := store.OpenReadOnly(filepath.Join(home, ".timetattle", "timetattle.db"))
	if err != nil {
		return nil, err
	}
	defer st.Close()
	return st.Events(context.Background(), store.Filter{})
})

func liveCorpus(t *testing.T) []source.Event {
	t.Helper()
	events, err := liveEvents()
	if err != nil {
		t.Skipf("cannot read the live corpus: %v", err)
	}
	if len(events) < 100_000 {
		t.Skipf("the live store holds only %d events; these tests are about a large one", len(events))
	}
	return events
}

// liveParams is the shipped role assignment, which these tests use rather than
// a convenient subset.
//
// knowledgec-backlit ALONE is presence: Biome's focus intervals record which
// application was frontmost and continue perfectly well with the screen off, so
// scoring them as presence produces a units error that reads like a finding. And
// the ignore list is not decoration - /device/isLocked holds a 15h32m interval
// in this store, so clustering it as evidence manufactures a fifteen-hour
// sitting out of a night's sleep and makes every figure here a figure about a
// corpus nobody uses.
func liveParams() sittings.Params {
	return sittings.Params{
		PresenceSources: []string{"knowledgec-backlit"},
		IgnoreSources:   []string{"knowledgec-islocked", "knowledgec-mediausage"},
	}
}

// liveLabels reads the whole-day confirmations out of the live store, exactly
// the way cmd/timetattle/fit.go does.
func liveLabels(t *testing.T, st *store.Store) fit.Truth {
	t.Helper()
	ctx := context.Background()
	reviews, err := st.Reviews(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("read reviews: %v", err)
	}
	day, saw := map[int64]string{}, map[int64]adjustments.Shown{}
	for _, r := range reviews {
		if r.Verdict == adjustments.Recalled && r.End.Sub(r.Start) >= 20*time.Hour {
			day[r.ID], saw[r.ID] = r.Start.UTC().Format("2006-01-02"), r.Saw()
		}
	}
	adjs, err := st.Adjustments(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("read adjustments: %v", err)
	}
	var truth fit.Truth
	for _, a := range adjs {
		d, ok := day[a.ReviewID]
		if !ok || (a.Assertion != adjustments.Engaged && a.Assertion != adjustments.NotEngaged) {
			continue
		}
		truth.Labels = append(truth.Labels, fit.Label{
			Day:     d,
			Shown:   saw[a.ReviewID],
			Span:    timespan.Span{Start: a.Start, End: a.End},
			Engaged: a.Assertion == adjustments.Engaged,
		})
	}
	return truth
}

// THE POPULATION SPLIT, TAKEN ON THE REAL STORE RATHER THAN ASSUMED FROM IT.
//
// As of 2026-08-03 every one of the 70 rows in adjustment_review carries
// shown='derivation' and there is not one blind label in the corpus, so the
// interesting case is the one that cannot be exercised by data that exists. The
// assertions below are written as a CONDITIONAL on the census rather than as
// the count, so this stays true — and stays load-bearing — the first time Ben
// runs `timetattle review -blind`, instead of going red for having succeeded.
func TestTheLivePopulationsAreCountedRatherThanAssumed(t *testing.T) {
	truth := liveLabels(t, liveStore(t))
	census := truth.Census()

	// Completeness first: a census that listed only what it found could not say
	// "nothing was recorded blind", which is the finding on this store.
	if got, want := len(census), len(adjustments.Everything()); got != want {
		t.Fatalf("the census holds %d populations, want %d: %+v", got, want, census)
	}
	total := 0
	for _, p := range census {
		t.Logf("%-24s %4d assertions over %2d days, %.3fh engaged, %.3fh not-engaged",
			fit.Describe(p.Shown), p.Labels, p.Days, p.Engaged.Hours(), p.NotEngaged.Hours())
		total += p.Labels
	}
	if total != len(truth.Labels) {
		t.Errorf("the census accounts for %d of %d labels, so some population is unnamed",
			total, len(truth.Labels))
	}

	for _, p := range census {
		only, err := truth.Only(p.Shown)
		if p.Empty() {
			if err == nil {
				t.Errorf("Only(%q) returned a Truth over zero labels; a fit against it would "+
					"score 0.000h and read as a perfect one", p.Shown)
			}
			continue
		}
		if err != nil {
			t.Errorf("Only(%q) refused a population holding %d labels: %v", p.Shown, p.Labels, err)
			continue
		}
		for _, l := range only.Labels {
			if l.Saw() != p.Shown {
				t.Fatalf("Only(%q) kept a %q label", p.Shown, l.Saw())
			}
		}
	}
}

// The counts settled on the main thread, checked rather than quoted: 503
// assertions over 17 whole-day reviews, 163.481h asserted engaged and 244.519h
// asserted not-engaged, which tile 17 days of 24 hours exactly.
func TestTheLiveLabelsAreWhatTheyWereMeasuredToBe(t *testing.T) {
	truth := liveLabels(t, liveStore(t))
	if got, want := len(truth.Labels), 503; got != want {
		t.Errorf("%d assertions parent to a whole-day review, want %d", got, want)
	}
	if got, want := len(truth.Days()), 17; got != want {
		t.Errorf("%d reviewed days, want %d", got, want)
	}
	if err := truth.Validate(); err != nil {
		t.Errorf("the live labels do not form a partition: %v", err)
	}

	_, none := truth.Nulls()
	engaged, notEngaged := none.AssertedEngaged.Hours(), none.AssertedNotEngaged.Hours()
	if diff := engaged - 163.481; diff > 0.001 || diff < -0.001 {
		t.Errorf("asserted engaged = %.3fh, want 163.481h", engaged)
	}
	if diff := notEngaged - 244.519; diff > 0.001 || diff < -0.001 {
		t.Errorf("asserted not-engaged = %.3fh, want 244.519h", notEngaged)
	}
	if total := engaged + notEngaged; total < 407.99 || total > 408.01 {
		t.Errorf("the labels cover %.3fh, want 408h — seventeen whole days", total)
	}
}

// THE PROPERTY THE NARROWING RESTS ON, taken over the real corpus rather than
// over eleven constructed events.
//
// Reconciled numerically as well as structurally: the residual from the
// narrowed corpus and the residual from all 900,000 events must be equal to the
// nanosecond at every point of the grid, not merely close.
func TestNarrowingTheLiveCorpusChangesNoResidual(t *testing.T) {
	if testing.Short() {
		t.Skip("-short was requested; this clusters the whole live corpus several times")
	}
	truth := liveLabels(t, liveStore(t))
	events := liveCorpus(t)

	base := liveParams()
	// Two points, not twelve. Each one clusters the WHOLE corpus and the
	// property being checked is structural rather than statistical: it holds at
	// the extremes of the grid or the closure argument is wrong. Twelve points
	// here cost thirteen minutes and proved nothing the two do not.
	grid := fit.Grid{
		Gaps:     []time.Duration{11 * time.Minute, time.Hour},
		LeadIns:  []time.Duration{5 * time.Minute},
		TailOuts: []time.Duration{5 * time.Minute},
	}
	kept, n := fit.Narrow(events, truth.Span(), grid.Reach(), base)
	if n.Kept >= n.Read {
		t.Fatalf("narrowing kept %d of %d, so it removed nothing and proves nothing", n.Kept, n.Read)
	}
	t.Logf("narrowed %d events to %d across %d chains, %d of which reach a label",
		n.Read, n.Kept, n.Runs, n.RunsKept)

	for _, p := range grid.Points() {
		at := base
		at.Gap, at.LeadIn, at.TailOut = p.Gap, p.LeadIn, p.TailOut
		whole, err := sessions.Cluster(events, at)
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		short, err := sessions.Cluster(kept, at)
		if err != nil {
			t.Fatalf("%v: %v", p, err)
		}
		if got, want := truth.Score(short.Sessions), truth.Score(whole.Sessions); got != want {
			t.Errorf("%v: narrowed scores %+v, the whole corpus scores %+v", p, got, want)
		}
	}
}

// The inertness rule, checked once against the live corpus rather than only
// against a corpus written to demonstrate it.
func TestTheInertRuleHoldsOnTheLiveCorpus(t *testing.T) {
	if testing.Short() {
		t.Skip("-short was requested; this clusters the live corpus several times")
	}
	truth := liveLabels(t, liveStore(t))
	events := liveCorpus(t)

	base := liveParams()
	grid := fit.Grid{
		Gaps:     []time.Duration{time.Minute, 5 * time.Minute, 10 * time.Minute, 11 * time.Minute},
		LeadIns:  []time.Duration{5 * time.Minute},
		TailOuts: []time.Duration{5 * time.Minute},
	}
	kept, _ := fit.Narrow(events, truth.Span(), grid.Reach(), base)

	spans := func(gap time.Duration) []timespan.Span {
		at := base
		at.Gap, at.LeadIn, at.TailOut = gap, 5*time.Minute, 5*time.Minute
		res, err := sessions.Cluster(kept, at)
		if err != nil {
			t.Fatalf("gap %v: %v", gap, err)
		}
		out := make([]timespan.Span, 0, len(res.Sessions))
		for _, s := range res.Sessions {
			out = append(out, s.Span())
		}
		return out
	}
	boundary := spans(10 * time.Minute)
	if len(boundary) == 0 {
		t.Fatal("the narrowed live corpus derived no sittings at all")
	}
	for _, gap := range []time.Duration{time.Minute, 5 * time.Minute} {
		if !slices.Equal(spans(gap), boundary) {
			t.Errorf("gap %v derives something other than the top of its inert band", gap)
		}
	}
	if slices.Equal(spans(11*time.Minute), boundary) {
		t.Error("gap 11m derives exactly what 10m does, so the floor is not where the rule says it is")
	}
}
