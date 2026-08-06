package storefixture_test

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/storefixture"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// The fixture's own tests.
//
// They exist because the corpus is only useful if it actually holds the regimes
// it claims to. A fixture that quietly stopped emitting the dark stretch would
// leave every test that depends on it green while measuring nothing, which is
// the exact failure this whole task was raised to stop.

func events(t *testing.T, f *storefixture.Fixture, filter store.Filter) []source.Event {
	t.Helper()
	got, err := f.Store.Events(context.Background(), filter)
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	return got
}

func TestNewBuildsAPopulatedStoreUnderTheTestsOwnTempDirectory(t *testing.T) {
	f := storefixture.New(t)

	def, err := store.DefaultPath()
	if err == nil && f.Path == def {
		t.Fatalf("the fixture opened the machine's own store at %s", def)
	}
	if _, err := os.Stat(f.Path); err != nil {
		t.Fatalf("the fixture reports its store at %s and nothing is there: %v", f.Path, err)
	}

	all := events(t, f, store.Filter{})
	if len(all) == 0 {
		t.Fatal("the fixture store holds no events at all")
	}
}

func TestTheFixtureIsMigratedToTheSchemaThisBinaryCarries(t *testing.T) {
	f := storefixture.New(t)
	schema, err := f.Store.Schema(context.Background())
	if err != nil {
		t.Fatalf("Schema: %v", err)
	}
	if schema.Version < 11 {
		t.Errorf("the fixture is at migration %d; it must carry every migration this binary ships", schema.Version)
	}
}

func TestTheCorpusCarriesEverySourceItNames(t *testing.T) {
	f := storefixture.New(t)
	seen := map[string]int{}
	for _, e := range events(t, f, store.Filter{}) {
		seen[e.Source]++
	}
	for _, id := range storefixture.Sources {
		if seen[id] == 0 {
			t.Errorf("source %q is named by the fixture but wrote no events", id)
		}
	}
	if len(seen) < 4 {
		t.Errorf("the corpus draws on %d sources; the regimes it claims need several", len(seen))
	}
}

func TestTheCorpusCarriesBothPresenceAndEvidence(t *testing.T) {
	f := storefixture.New(t)
	var presence, evidence int
	for _, e := range events(t, f, store.Filter{}) {
		if e.Source == storefixture.PresenceSource {
			presence++
			continue
		}
		if !slices.Contains(storefixture.IgnoredSources, e.Source) {
			evidence++
		}
	}
	if presence == 0 {
		t.Error("no presence readings; a corpus without them cannot bound anything")
	}
	if evidence == 0 {
		t.Error("no evidence events")
	}
}

func TestPresenceIsObservedOnAndObservedOffAndNotOnlyOn(t *testing.T) {
	f := storefixture.New(t)
	states := map[string]int{}
	for _, e := range events(t, f, store.Filter{}) {
		if e.Source == storefixture.PresenceSource {
			states[e.State]++
		}
	}
	if states["on"] == 0 {
		t.Error("no lit presence readings")
	}
	if states["off"] == 0 {
		t.Error("no dark presence readings; observed-off is a row and its absence is a different fact")
	}
}

func TestOneDayCarriesSeveralProjectsAtOnce(t *testing.T) {
	f := storefixture.New(t)
	start, end := f.Calendar.Bounds(f.ConcurrentDay)
	projects := map[string]bool{}
	for _, e := range events(t, f, store.Filter{From: start, To: end}) {
		if e.Identity.Project != "" {
			projects[e.Identity.Project] = true
		}
	}
	if len(projects) < 2 {
		t.Errorf("%s carries %d projects; it is the concurrent day and must carry at least two",
			f.ConcurrentDay, len(projects))
	}
}

func TestOneDayGoesDarkInTheMiddleOfAnObservedInterval(t *testing.T) {
	f := storefixture.New(t)
	start, end := f.Calendar.Bounds(f.DarknessDay)

	var dark []timespan.Span
	var intervals []timespan.Span
	for _, e := range events(t, f, store.Filter{From: start, To: end}) {
		switch {
		case e.Source == storefixture.PresenceSource && e.State == "off":
			dark = append(dark, timespan.Span{Start: e.Start, End: e.End})
		case e.Shape == source.Interval && e.Source != storefixture.PresenceSource &&
			!slices.Contains(storefixture.IgnoredSources, e.Source):
			intervals = append(intervals, timespan.Span{Start: e.Start, End: e.End})
		}
	}
	if len(dark) == 0 {
		t.Fatalf("%s records no darkness", f.DarknessDay)
	}
	cut := false
	for _, d := range dark {
		for _, i := range intervals {
			if i.Start.Before(d.Start) && d.Start.Before(i.End) {
				cut = true
			}
		}
	}
	if !cut {
		t.Errorf("no dark reading begins strictly inside an evidence interval on %s; "+
			"the regime the corpus exists to carry is not in it", f.DarknessDay)
	}
}

func TestTheDarknessCutsASittingShortOfTheIntervalContainingIt(t *testing.T) {
	f := storefixture.New(t)
	start, end := f.Calendar.Bounds(f.DarknessDay)
	derived, err := f.Store.Sittings(context.Background(), start, end)
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(derived.Sittings) == 0 {
		t.Fatalf("%s derived no sittings", f.DarknessDay)
	}
	var covering []source.Event
	for _, e := range events(t, f, store.Filter{From: start, To: end}) {
		if e.Shape == source.Interval && e.Source == storefixture.EvidenceSource {
			covering = append(covering, e)
		}
	}
	cut := false
	for _, s := range derived.Sittings {
		for _, e := range covering {
			// The sitting ends inside an interval that was still being
			// observed. Nothing but presence can do that.
			if e.Start.Before(s.End) && s.End.Before(e.End) {
				cut = true
			}
		}
	}
	if !cut {
		t.Errorf("no sitting on %s ends inside an evidence interval; the darkness did not cut anything",
			f.DarknessDay)
	}
}

func TestOneDayHoldsPointsAndNoDurationBearingEvidenceAtAll(t *testing.T) {
	f := storefixture.New(t)
	start, end := f.Calendar.Bounds(f.PointsOnlyDay)
	var points, spans int
	for _, e := range events(t, f, store.Filter{From: start, To: end}) {
		if slices.Contains(storefixture.IgnoredSources, e.Source) {
			continue
		}
		switch e.Shape {
		case source.Point:
			points++
		default:
			spans++
		}
	}
	if points == 0 {
		t.Errorf("%s holds no points", f.PointsOnlyDay)
	}
	if spans != 0 {
		t.Errorf("%s holds %d duration-bearing observations; it is the points-only day", f.PointsOnlyDay, spans)
	}
}

func TestAPointsOnlyDayStillScoresTime(t *testing.T) {
	f := storefixture.New(t)
	start, end := f.Calendar.Bounds(f.PointsOnlyDay)
	derived, err := f.Store.Sittings(context.Background(), start, end)
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if derived.Total() <= 0 {
		t.Errorf("%s scored %v; a day of isolated saves must not disappear", f.PointsOnlyDay, derived.Total())
	}
}

// The fourth presence case.
//
// Two dark stretches inside one cluster carve out a middle piece that lies
// BETWEEN two observations rather than containing either. The clustering must
// drop it rather than emit a sitting nothing evidenced. This was found by a
// CHECK constraint on a real run, not by a test, and the invariant was written
// in the code as a comment and was false.
func TestOneClusterLeavesAPieceThatLiesBetweenTwoObservations(t *testing.T) {
	f := storefixture.New(t)
	res, err := sessions.Cluster(events(t, f, store.Filter{}), f.Params)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	if res.Stats.UnevidencedPieces == 0 {
		t.Error("no piece of any cluster lies between two observations; the fourth presence case " +
			"is not in the corpus")
	}
	if res.Stats.SplitByAbsence == 0 {
		t.Error("no cluster was split by observed darkness")
	}
	for _, s := range res.Sessions {
		if s.Events == 0 {
			t.Errorf("sitting %v..%v rests on no observation at all", s.Start, s.End)
		}
	}
}

// The evidence spans of the between day sit either side of a piece that nothing
// reaches, which is what makes the case constructible at all.
func TestTheBetweenDayCarriesTwoDarkStretchesInsideOneCluster(t *testing.T) {
	f := storefixture.New(t)
	start, end := f.Calendar.Bounds(f.BetweenDay)
	var readings, spans []timespan.Span
	for _, e := range events(t, f, store.Filter{From: start, To: end}) {
		switch {
		case e.Source == storefixture.PresenceSource && e.State == "off":
			readings = append(readings, timespan.Span{Start: e.Start, End: e.End})
		case e.Shape == source.Interval && e.Source != storefixture.PresenceSource &&
			!slices.Contains(storefixture.IgnoredSources, e.Source):
			spans = append(spans, timespan.Span{Start: e.Start, End: e.End})
		}
	}
	if len(readings) < 2 {
		t.Errorf("%s carries %d dark stretches; the case needs two inside one cluster",
			f.BetweenDay, len(readings))
	}
	if len(spans) < 2 {
		t.Errorf("%s carries %d evidence spans; the case needs one either side", f.BetweenDay, len(spans))
	}
}

func TestTheCorpusCarriesAnUnboundedEventOfEachReason(t *testing.T) {
	f := storefixture.New(t)
	reasons := map[source.Unknown]int{}
	for _, e := range events(t, f, store.Filter{}) {
		if e.Shape == source.Unbounded {
			reasons[e.Unknown]++
		}
	}
	if reasons[source.UnknownSleep] == 0 {
		t.Error("no sleep-bounded event; a bare stop is 382 of the live records and cannot be synthesised later")
	}
	if reasons[source.UnknownHorizon] == 0 {
		t.Error("no horizon event; the corpus never reaches its own edge")
	}
}

func TestASupersededHorizonIsHiddenByTheCanonicalReadAndKeptByTheOther(t *testing.T) {
	f := storefixture.New(t)
	canonical := events(t, f, store.Filter{})
	all, err := f.Store.EventsIncludingSuperseded(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("EventsIncludingSuperseded: %v", err)
	}
	if len(all) <= len(canonical) {
		t.Errorf("the corpus holds %d canonical and %d total events; nothing in it is superseded, "+
			"so supersession is not exercised", len(canonical), len(all))
	}
}

func TestTheDerivationIsWrittenAndIsASingleOne(t *testing.T) {
	f := storefixture.New(t)
	derived, err := f.Store.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if derived.ComputedAt.IsZero() {
		t.Fatal("the fixture wrote no derivation; a store nobody sessionized cannot stand in for one that was")
	}
	if len(derived.Sittings) == 0 {
		t.Fatal("the derivation holds no sittings")
	}
	if !derived.Params.Equal(f.Params) {
		t.Errorf("the stored derivation used %v; the fixture reports %v", derived.Params, f.Params)
	}
}

func TestEveryHarvestRunFinishedSoTheStoreHasCoverage(t *testing.T) {
	f := storefixture.New(t)
	fresh, err := f.Store.Freshness(context.Background())
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if len(fresh) == 0 {
		t.Fatal("no source reports a horizon; nothing recorded a successful run")
	}
}

func TestTheCorpusIsDeterministic(t *testing.T) {
	one := storefixture.New(t)
	two := storefixture.New(t)
	a, b := events(t, one, store.Filter{}), events(t, two, store.Filter{})
	if len(a) != len(b) {
		t.Fatalf("two builds of the same fixture hold %d and %d events", len(a), len(b))
	}
	for i := range a {
		if !a[i].Start.Equal(b[i].Start) || a[i].Identity != b[i].Identity || a[i].Source != b[i].Source {
			t.Fatalf("event %d differs between two builds: %+v and %+v", i, a[i], b[i])
		}
	}
}

// EVERY NAME IN THE CORPUS IS DRAWN FROM THE FIXTURE'S OWN DECLARED VOCABULARY.
//
// This is an ALLOWLIST and the inversion is the point. The first version of
// this case was a denylist of the four real client symbols, which is both
// leakier and worse: it catches only the four strings it names, and it
// republishes them in the course of asserting they are absent. That is exactly
// what scripts/confidential's own doc comment argues — a gate that hard-coded
// the real names could never report itself clean, because publishing the
// auditor publishes the thing it audits — and it applies here verbatim.
//
// Asking instead "is this name one the fixture invented" needs no real name to
// express and catches ANY real string that leaks in, not only the ones somebody
// thought to list. A new document literal added to Events without being
// declared in Words is red, which is the intended reading: the vocabulary is
// the authority, so an undeclared name is an unreviewed name.
func TestEveryNameInTheCorpusIsOneTheFixtureInvented(t *testing.T) {
	f := storefixture.New(t)
	words := storefixture.Words()

	member := func(field string, allowed []string) bool {
		return field == "" || slices.Contains(allowed, field)
	}
	for _, e := range events(t, f, store.Filter{}) {
		if !member(e.Identity.Project, words.Projects) {
			t.Errorf("event at %v is attributed to project %q, which the fixture never invented",
				e.Start, e.Identity.Project)
		}
		if !member(e.Identity.App, words.Apps) {
			t.Errorf("event at %v names application %q, which the fixture never invented",
				e.Start, e.Identity.App)
		}
		if !member(e.Identity.Document, words.Documents) {
			t.Errorf("event at %v names document %q, which the fixture never invented",
				e.Start, e.Identity.Document)
		}
		if e.Locator != "" && !slices.ContainsFunc(words.Locators, func(p string) bool {
			return strings.HasPrefix(e.Locator, p)
		}) {
			t.Errorf("event at %v was read from %q, which is under none of the fixture's own roots",
				e.Start, e.Locator)
		}
	}
}

// The vocabulary has to be a vocabulary. A test asserting membership of an
// empty set passes over an empty corpus and means nothing.
func TestTheDeclaredVocabularyIsActuallyUsed(t *testing.T) {
	f := storefixture.New(t)
	words := storefixture.Words()
	if len(words.Projects) == 0 || len(words.Apps) == 0 ||
		len(words.Documents) == 0 || len(words.Locators) == 0 {
		t.Fatalf("the declared vocabulary is empty somewhere: %+v", words)
	}

	seen := map[string]bool{}
	for _, e := range events(t, f, store.Filter{}) {
		seen[e.Identity.Project] = true
		seen[e.Identity.App] = true
		seen[e.Identity.Document] = true
	}
	for _, group := range [][]string{words.Projects, words.Apps, words.Documents} {
		for _, word := range group {
			if !seen[word] {
				t.Errorf("%q is declared in the vocabulary and appears nowhere in the corpus; "+
					"an allowlist wider than the corpus admits names nobody reviewed", word)
			}
		}
	}
}

// The one literal string worth keeping. Ben's own name and paths are not
// confidential — his clients are — so this carries no disclosure, and a corpus
// that reached into a real home directory would be machine-coupled even if
// every name in it were invented.
func TestNoLocatorReachesIntoARealHomeDirectory(t *testing.T) {
	f := storefixture.New(t)
	for _, e := range events(t, f, store.Filter{}) {
		if strings.HasPrefix(e.Locator, "/Users/") {
			t.Errorf("event at %v was read from %q, which is inside somebody's home directory",
				e.Start, e.Locator)
		}
	}
}

func TestTheFixtureCalendarDoesNotDependOnTheMachinesZone(t *testing.T) {
	f := storefixture.New(t)
	if f.Calendar.Location == nil || f.Calendar.Location.String() != "UTC" {
		t.Errorf("the fixture calendar is in %v; a corpus whose day boundaries move with the machine "+
			"is not machine-independent", f.Calendar.Location)
	}
}

func TestTheRetainedCorpusClustersToWhatTheStoreReadsBack(t *testing.T) {
	f := storefixture.New(t)
	all := events(t, f, store.Filter{})
	res, err := sessions.Cluster(all, f.Params)
	if err != nil {
		t.Fatalf("Cluster: %v", err)
	}
	stored, err := f.Store.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if len(res.Sessions) != len(stored.Sittings) {
		t.Fatalf("clustering the corpus gives %d sittings; the stored table holds %d",
			len(res.Sessions), len(stored.Sittings))
	}
	for i := range res.Sessions {
		if !res.Sessions[i].Start.Equal(stored.Sittings[i].Start) ||
			!res.Sessions[i].End.Equal(stored.Sittings[i].End) {
			t.Errorf("sitting %d is %v..%v clustered and %v..%v stored", i,
				res.Sessions[i].Start, res.Sessions[i].End,
				stored.Sittings[i].Start, stored.Sittings[i].End)
		}
	}
}

func TestTheCorpusCarriesAReviewAndACorrection(t *testing.T) {
	f := storefixture.New(t)
	reviews, err := f.Store.Reviews(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(reviews) == 0 {
		t.Error("no review; a store nobody has looked at cannot stand in for one somebody has")
	}
	adjs, err := f.Store.Adjustments(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	if len(adjs) == 0 {
		t.Error("no correction; the overlay is never exercised")
	}
}
