package metrics_test

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

// testdata/realday.jsonl is a synthetic working day with the SHAPE of the one
// realdata_test.go reads out of the live store, and none of its content.
//
// The tests below are the portable half of that file. They assert PROPERTIES
// rather than figures, for the same reason it does — but where those properties
// are unreachable on a machine with no store, these are reachable everywhere,
// and where that file must skip when the store holds nothing for the day, these
// cannot skip at all.
//
// WHY A SYNTHETIC CORPUS RATHER THAN AN ANONYMISED EXTRACT. metrics.Compute is
// a pure function of []source.Event; nothing here needs a database. Anonymising
// a real day would still have meant reading ~/.timetattle/timetattle.db, and
// what makes the real day interesting is its SHAPE — three projects interleaved
// in the same clock hours, evidence with a path nobody registered, evidence
// with no path at all, a screen lit before work began and silent while it
// carried on. Every one of those can be written down, and writing them down is
// what makes each of them an assertion instead of a hope about what a harvest
// happened to collect.
//
// The corpus deliberately holds all of them AT ONCE. metrics_test.go already
// tests each in isolation on two- and three-event cases; what neither it nor a
// constructed case establishes is that the arithmetic still closes when they
// are all true of the same day, which is the only situation that ever occurs.
const fixtureDayManifest = "testdata/realday.jsonl"

// fixtureDayDate is the synthetic day, deliberately not 2026-07-28 — the real
// concurrent day project/private/2026-07-31-concurrent-project-validation.md measures —
// so that a figure taken off this corpus can never be mistaken for one of those
// measurements.
var fixtureDayDate = time.Date(2026, 3, 4, 0, 0, 0, 0, time.Local)

// fixtureDayEvent is one line of the corpus. It is a schema of this test's own
// rather than source.Event's field names, so the fixture stays readable and
// does not silently change meaning when a field is added to source.Event.
type fixtureDayEvent struct {
	Source   string `json:"source"`
	At       string `json:"at"`   // a Point, HH:MM:SS
	From     string `json:"from"` // an Interval, with To
	To       string `json:"to"`
	Project  string `json:"project"`
	App      string `json:"app"`
	Document string `json:"document"`
	Locator  string `json:"locator"`
	State    string `json:"state"`
	Note     string `json:"note"` // why the line is in the corpus; ignored
}

var (
	fixtureDayOnce   sync.Once
	fixtureDayReport metrics.Report
	fixtureDayErr    error
	fixtureDayCount  int
)

// fixtureDay computes the report once, exactly as realdata_test.go does for the
// live store.
func fixtureDay(t *testing.T) metrics.Report {
	t.Helper()
	fixtureDayOnce.Do(func() {
		var events []source.Event
		events, fixtureDayErr = fixtureDayEvents()
		if fixtureDayErr != nil {
			return
		}
		fixtureDayCount = len(events)
		cfg := config.Default()
		fixtureDayReport, fixtureDayErr = metrics.Compute(events, sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		})
	})
	if fixtureDayErr != nil {
		// Not a skip. A corpus that is checked in either parses or the suite
		// is broken; there is no machine on which this is a missing
		// precondition.
		t.Fatalf("building the fixture day: %v", fixtureDayErr)
	}
	return fixtureDayReport
}

func fixtureDayEvents() ([]source.Event, error) {
	f, err := os.Open(fixtureDayManifest)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var events []source.Event
	scan := bufio.NewScanner(f)
	for line := 1; scan.Scan(); line++ {
		text := strings.TrimSpace(scan.Text())
		if text == "" || strings.HasPrefix(text, "//") {
			continue
		}
		var in fixtureDayEvent
		if err := json.Unmarshal([]byte(text), &in); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", fixtureDayManifest, line, err)
		}
		e, err := fixtureDayBuild(in)
		if err != nil {
			return nil, fmt.Errorf("%s line %d: %w", fixtureDayManifest, line, err)
		}
		// Every line is checked against source.Event's own rules before the
		// corpus is used, so a fixture cannot assert anything about a shape the
		// rest of the system would have refused to store.
		if err := e.Validate(); err != nil {
			return nil, fmt.Errorf("%s line %d: %w", fixtureDayManifest, line, err)
		}
		events = append(events, e)
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("%s describes no events", fixtureDayManifest)
	}
	return events, nil
}

func fixtureDayBuild(in fixtureDayEvent) (source.Event, error) {
	id := source.Identity{App: in.App, Document: in.Document, Project: in.Project}
	switch {
	case in.At != "":
		at, err := fixtureDayClock(in.At)
		if err != nil {
			return source.Event{}, err
		}
		e := source.NewPoint(at, id, in.Source)
		e.Locator = in.Locator
		e.State = in.State
		return e, nil
	case in.From != "" && in.To != "":
		from, err := fixtureDayClock(in.From)
		if err != nil {
			return source.Event{}, err
		}
		to, err := fixtureDayClock(in.To)
		if err != nil {
			return source.Event{}, err
		}
		e := source.NewInterval(from, to, id, in.Source)
		e.Locator = in.Locator
		e.State = in.State
		return e, nil
	default:
		return source.Event{}, errors.New(`neither "at" nor "from"/"to" is present, so the line names no instant`)
	}
}

// fixtureDayClock reads HH:MM:SS on the synthetic day, in the machine's own
// zone. The corpus is a wall clock — a morning, an afternoon, a screen coming
// on before work — and reading it in UTC would make the same assertions mean
// different things in different places.
func fixtureDayClock(hhmmss string) (time.Time, error) {
	at, err := time.Parse("15:04:05", hhmmss)
	if err != nil {
		return time.Time{}, err
	}
	return fixtureDayDate.Add(
		time.Duration(at.Hour())*time.Hour +
			time.Duration(at.Minute())*time.Minute +
			time.Duration(at.Second())*time.Second), nil
}

// Criterion 37i, portable. Engaged time overlaps across projects and presence
// counts the clock once — on a day two projects genuinely interleave rather
// than on a two-event case arranged to make it true.
func TestFixtureDayEngagedOverlapsWhilePresenceDoesNot(t *testing.T) {
	r := fixtureDay(t)
	if len(r.Projects) != 3 {
		t.Fatalf("%d projects attributable, want 3: %v", len(r.Projects), fixtureDayNames(r))
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
	if absolute <= allocated {
		t.Errorf("absolute engaged %v against allocated %v; on a day two projects interleaved, "+
			"concurrent work must bill to both and exceed the clock it happened in", absolute, allocated)
	}

	shared := 0
	for _, s := range r.Segments {
		if len(s.Projects) > 1 {
			shared++
		}
	}
	if shared == 0 {
		t.Error("no segment of the fixture day holds more than one project, yet compass and anvil " +
			"are written into the same clock hours")
	}
	t.Logf("absolute %v, allocated %v, %d shared segments", absolute, allocated, shared)
}

// Criterion pRM, portable. The shares plus the unattributed presence are the
// presence they were split from, to the nanosecond, across a whole day's
// segmentation rather than a two-event one.
func TestFixtureDayAllocationSumsExactlyToPresence(t *testing.T) {
	r := fixtureDay(t)
	var allocated time.Duration
	for _, p := range r.Projects {
		allocated += p.Allocated
	}
	if got := allocated + r.Unattributed.Presence.Total; got != r.Presence.Total {
		t.Errorf("allocated %v plus unattributed %v = %v against %v of presence, off by %v",
			allocated, r.Unattributed.Presence.Total, got, r.Presence.Total, got-r.Presence.Total)
	}
}

// Criterion kHo, portable, and the reconciliation between the distribution and
// the sittings it describes.
func TestFixtureDayDistributionReconcilesWithItsSittings(t *testing.T) {
	r := fixtureDay(t)
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
	// More than one sitting, or the quantiles above are all the same number
	// and the ordering they assert is vacuous.
	if r.Sittings.Count < 3 {
		t.Errorf("the day holds %d sittings; the quantile ordering asserts nothing below three",
			r.Sittings.Count)
	}
}

// Criterion 86I, portable, and the distinction that matters most: BOTH kinds of
// unattributed presence have to be reachable, or the separation is decorative.
// Evidence with a path nobody registered and evidence with no path at all are
// different problems, and only one of them is actionable.
func TestFixtureDayReachesBothKindsOfUnattributedPresence(t *testing.T) {
	r := fixtureDay(t)
	u := r.Unattributed
	if u.Unregistered+u.Unlocatable+u.Bridged != u.Presence.Total {
		t.Fatalf("the reasons sum to %v against %v of unattributed presence",
			u.Unregistered+u.Unlocatable+u.Bridged, u.Presence.Total)
	}
	if u.Unregistered <= 0 {
		t.Error("no unattributed presence is put down to an unregistered directory, yet the corpus " +
			"holds commits under a path no project claims")
	}
	if u.Unlocatable <= 0 {
		t.Error("no unattributed presence is put down to a source carrying no path, yet the corpus " +
			"holds a focus interval that names an application and no file")
	}
	if u.UnregisteredEvents == 0 || u.UnlocatableEvents == 0 {
		t.Errorf("%d unregistered and %d unlocatable observations counted; both must be reachable",
			u.UnregisteredEvents, u.UnlocatableEvents)
	}
	// The third state. Clock inside a sitting that nothing at all accounts for
	// is neither of the two reasons above, and if the corpus held none of it
	// the loop below would be asserting nothing.
	if u.Bridged <= 0 {
		t.Error("no unattributed presence is bridged silence, so the third state is unexercised; " +
			"the corpus holds two observations 14 minutes apart, which their lead-in and tail-out " +
			"do not join")
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
	t.Logf("unattributed %v = unregistered %v + unlocatable %v + bridged %v",
		u.Presence.Total, u.Unregistered, u.Unlocatable, u.Bridged)
}

// Presence is not screen-on time, and the gap between them runs in BOTH
// directions. The backlight sees clock the sittings do not — a screen lit
// before work began — and the sittings cover clock the backlight is silent
// about, which is a sampler that pruned itself. Silence is neither lit nor
// dark, and the whole reason the three states exist is that collapsing the last
// two has shipped as a bug here twice.
func TestFixtureDayBacklightAndSittingsDisagreeInBothDirections(t *testing.T) {
	r := fixtureDay(t)
	b := r.Backlight
	if b.Readings == 0 {
		t.Fatal("the corpus holds no backlight readings at all")
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
		t.Error("no lit clock falls outside a sitting, yet the corpus has the screen coming on " +
			"before the day's first observation")
	}
	if b.Unwitnessed <= 0 {
		t.Error("no presence is unwitnessed, yet the corpus has the sampler going silent while " +
			"work carried on. Silence is not darkness and must not be counted as either")
	}
	if b.Lit < b.Corroborated {
		t.Errorf("lit %v is less than the %v of it that overlapped a sitting", b.Lit, b.Corroborated)
	}
	t.Logf("lit %v, corroborated %v, lit outside a sitting %v, unwitnessed %v",
		b.Lit, b.Corroborated, b.LitOutsideASitting, b.Unwitnessed)
}

// The reconciliation between this package and the clustering it rests on.
// Presence is the sum of the sitting durations and nothing else, so if the two
// ever differ, one of them is counting an instant twice.
func TestFixtureDayPresenceIsExactlyTheSittings(t *testing.T) {
	r := fixtureDay(t)
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

// Observed is inside corroborated is inside engaged, for every project, and at
// least one project must actually be corroborated by something — or the
// ordering above is three zeroes agreeing with each other.
func TestFixtureDayObservedIsInsideCorroboratedIsInsideEngaged(t *testing.T) {
	r := fixtureDay(t)
	var corroborated time.Duration
	for _, p := range r.Projects {
		if p.Engaged.Observed > p.Corroborated || p.Corroborated > p.Engaged.Total {
			t.Errorf("%s: observed %v, corroborated %v, engaged %v; each must be inside the next",
				p.Name, p.Engaged.Observed, p.Corroborated, p.Engaged.Total)
		}
		corroborated += p.Corroborated
	}
	if corroborated == 0 {
		t.Error("no project's engaged time is corroborated by any source at all, yet the corpus " +
			"holds a held task claim running across an afternoon")
	}
}

// The three-way split has to close to the nanosecond over a whole day's
// segmentation and not only over a constructed one. What it guards is a stretch
// counted as attested AND weak, which would make a project look better
// evidenced than it is by exactly the amount nobody can see.
func TestFixtureDayThreeWaySplitClosesOnEveryProject(t *testing.T) {
	r := fixtureDay(t)
	for _, p := range r.Projects {
		if got := p.Attested + p.Weak + p.Unaccounted; got != p.Engaged.Total {
			t.Errorf("%s: attested %v + weak %v + unaccounted %v = %v against %v engaged, off by %v",
				p.Name, p.Attested, p.Weak, p.Unaccounted, got, p.Engaged.Total, got-p.Engaged.Total)
		}
		if p.Weak < 0 || p.Attested < 0 || p.Unaccounted < 0 {
			t.Errorf("%s: attested %v, weak %v, unaccounted %v — a negative share means one "+
				"stretch was counted twice", p.Name, p.Attested, p.Weak, p.Unaccounted)
		}
		t.Logf("  %-8s engaged %8v = attested %8v + weak %8v + unaccounted %v",
			p.Name, p.Engaged.Total, p.Attested, p.Weak, p.Unaccounted)
	}
}

// WEAK IS NOT UNOBSERVED RESTATED. Observed asks whether any source watched a
// DURATION; weak asks whether what accounts for a stretch is somebody doing
// something or a file timestamp. A project can read zero observed and still be
// attested for nearly all its hours, because an instant observes no duration.
//
// If the two ever coincided, the weak column would be restating a column that
// already exists — so the corpus has to contain a project where they differ,
// and one where weak is genuinely non-zero.
func TestFixtureDayWeakIsNotTheSameQuantityAsUnobserved(t *testing.T) {
	r := fixtureDay(t)
	var differ, weakAtAll int
	for _, p := range r.Projects {
		inferred := p.Engaged.Total - p.Engaged.Observed
		if inferred > 0 && p.Weak < inferred {
			differ++
			t.Logf("  %-8s %v engaged, %v of it unobserved, only %v of it weak",
				p.Name, p.Engaged.Total, inferred, p.Weak)
		}
		if p.Weak > 0 {
			weakAtAll++
		}
	}
	if differ == 0 {
		t.Error("every project's weak time equals its unobserved time, which would make the weak " +
			"column a restatement of the observed one")
	}
	if weakAtAll == 0 {
		t.Error("no project has any weak time at all, so the column is not exercised; the corpus " +
			"holds file timestamps that no recorded act covers")
	}
}

// A guard on the corpus rather than on the package. Every assertion above is
// about a day carrying several things at once; if a line is deleted the tests
// would keep passing while proving less, which is the failure mode this whole
// task tree exists for.
func TestFixtureDayCarriesEveryShapeItsAssertionsNeed(t *testing.T) {
	r := fixtureDay(t)
	events, err := fixtureDayEvents()
	if err != nil {
		t.Fatalf("reading the corpus: %v", err)
	}
	if fixtureDayCount != len(events) {
		t.Errorf("the report was computed over %d events and the corpus now holds %d",
			fixtureDayCount, len(events))
	}

	ignored := map[string]bool{}
	for _, id := range config.Default().Sessions.IgnoreSources {
		ignored[id] = true
	}
	kinds := map[string]int{}
	for _, e := range events {
		switch {
		case ignored[e.Source]:
			// Counted apart, or a source that contributes nothing would be
			// mistaken here for one of the kinds that does.
			kinds["ignored"]++
		case e.Source == "knowledgec-backlit":
			kinds["presence"]++
		case e.Identity.Project != "" && !source.WeakEvidence(e.Source):
			kinds["attesting"]++
		case e.Identity.Project != "":
			kinds["weak"]++
		case e.Locator != "":
			kinds["unregistered"]++
		default:
			kinds["unlocatable"]++
		}
		if e.Shape == source.Interval {
			kinds["interval"]++
		}
	}
	for _, want := range []string{"presence", "attesting", "weak", "unregistered", "unlocatable", "interval", "ignored"} {
		if kinds[want] == 0 {
			t.Errorf("the corpus holds no %s observations; the assertions resting on them are vacuous", want)
		}
	}
	t.Logf("%d events: %v", len(events), kinds)
	t.Logf("presence %v across %d sittings; projects %v", r.Presence.Total, len(r.Sessions), fixtureDayNames(r))
}

func fixtureDayNames(r metrics.Report) []string {
	out := make([]string, 0, len(r.Projects))
	for _, p := range r.Projects {
		out = append(out, p.Name)
	}
	return out
}
