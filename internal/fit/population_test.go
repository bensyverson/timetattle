package fit_test

import (
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/sittings"
)

// blind is a label the reviewer formed while looking at the observations rather
// than at the machine's answer.
func blind(day string, from, to time.Duration, engaged bool) fit.Label {
	l := label(day, from, to, engaged)
	l.Shown = adjustments.Observations
	return l
}

// A label recorded before there was anything else to look at was a label about
// the derivation. That is KNOWN rather than merely unrecorded — `timetattle
// review` had no other mode — which is the same argument migration 0011 makes
// for its NOT NULL DEFAULT, restated at the type that reads the column.
func TestALabelThatSaysNothingAboutItsPopulationIsReadAsAConfirmation(t *testing.T) {
	if got, want := (fit.Label{}).Saw(), adjustments.Derivation; got != want {
		t.Errorf("a label with no population recorded reads as %q, want %q", got, want)
	}
	if got, want := blind("2026-03-02", 0, min5, true).Saw(), adjustments.Observations; got != want {
		t.Errorf("a blind label reads as %q, want %q", got, want)
	}
}

// THE TRAP THIS WHOLE CHANGE EXISTS TO AVOID. There are no blind labels in the
// live store at all, so a census that reported only the populations it found
// would say "derivation: 70" and leave the reader to infer the rest. "Nothing
// was recorded blind" is an OBSERVATION and has to be printable as one; a
// missing key is not.
func TestCensusCountsEveryPopulationIncludingTheEmptyOne(t *testing.T) {
	census := tiledDay().Census()

	if got, want := len(census), len(adjustments.Everything()); got != want {
		t.Fatalf("the census holds %d populations, want %d — one per kind of label, "+
			"present or not: %+v", got, want, census)
	}
	byShown := map[adjustments.Shown]fit.Population{}
	for _, p := range census {
		byShown[p.Shown] = p
	}

	confirming, ok := byShown[adjustments.Derivation]
	if !ok {
		t.Fatalf("the census has no entry for %q: %+v", adjustments.Derivation, census)
	}
	if got, want := confirming.Labels, 3; got != want {
		t.Errorf("the confirming population holds %d labels, want %d", got, want)
	}
	if got, want := confirming.Days, 1; got != want {
		t.Errorf("the confirming population covers %d days, want %d", got, want)
	}
	if got, want := confirming.Engaged, time.Hour; got != want {
		t.Errorf("the confirming population asserts %v engaged, want %v", got, want)
	}
	if got, want := confirming.NotEngaged, time.Hour; got != want {
		t.Errorf("the confirming population asserts %v not engaged, want %v", got, want)
	}

	empty, ok := byShown[adjustments.Observations]
	if !ok {
		t.Fatalf("the census omits %q entirely, so a reader cannot tell a population "+
			"nobody recorded from one nobody asked about: %+v", adjustments.Observations, census)
	}
	if empty.Labels != 0 || empty.Days != 0 || empty.Asserted() != 0 {
		t.Errorf("the empty population reports %+v, want zeroes", empty)
	}
	if !empty.Empty() {
		t.Error("Empty() is false for a population holding no labels")
	}
}

// A residual over zero labels is 0.000h, which is indistinguishable from a
// perfect fit. Restricting to a population nobody recorded therefore has to
// FAIL, and the complaint has to carry the whole census — otherwise "no blind
// labels" reads exactly like "no labels", and the two call for different
// actions.
func TestOnlyRefusesAPopulationNothingWasRecordedFor(t *testing.T) {
	_, err := tiledDay().Only(adjustments.Observations)
	if err == nil {
		t.Fatal("Only returned a Truth for a population holding no labels; " +
			"a fit against it would score 0.000h over nothing and read as perfect")
	}
	for _, want := range []string{"blind", "3", "0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the complaint does not carry %q, so the reader cannot tell "+
				"an empty population from an empty store: %v", want, err)
		}
	}
}

func TestOnlyKeepsExactlyOnePopulation(t *testing.T) {
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		label("2026-03-02", 30*time.Minute, 60*time.Minute, false),
		blind("2026-03-03", 24*time.Hour, 25*time.Hour, true),
	}}

	confirming, err := truth.Only(adjustments.Derivation)
	if err != nil {
		t.Fatalf("Only(%q): %v", adjustments.Derivation, err)
	}
	if got, want := len(confirming.Labels), 2; got != want {
		t.Errorf("the confirming population holds %d labels, want %d", got, want)
	}

	observed, err := truth.Only(adjustments.Observations)
	if err != nil {
		t.Fatalf("Only(%q): %v", adjustments.Observations, err)
	}
	if got, want := len(observed.Labels), 1; got != want {
		t.Fatalf("the blind population holds %d labels, want %d", got, want)
	}
	if got, want := observed.Labels[0].Day, "2026-03-03"; got != want {
		t.Errorf("the blind population kept %q, want %q", got, want)
	}
}

// A residual must never be quotable without the population it came from, so the
// objective's own name carries it. It names what was SCORED and not what the
// store holds: a `-shown blind` run has legitimately been restricted, and
// printing "0 confirming a derivation" beside its residual would read as a claim
// that the store has none.
func TestTheObjectiveNamesThePopulationItWasScoredOver(t *testing.T) {
	if got := tiledDay().Name(); !strings.Contains(got, "all confirming a derivation") {
		t.Errorf("Name() = %q, want it to say the labels are all of one population", got)
	}

	mixed := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		blind("2026-03-03", 24*time.Hour, 25*time.Hour, true),
	}}
	got := mixed.Name()
	for _, want := range []string{"1 confirming a derivation", "1 blind"} {
		if !strings.Contains(got, want) {
			t.Errorf("Name() = %q, want it to carry %q", got, want)
		}
	}
}

func TestByShownFoldsBackToThePooledScore(t *testing.T) {
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		label("2026-03-02", 30*time.Minute, 60*time.Minute, false),
		blind("2026-03-03", 24*time.Hour, 25*time.Hour, true),
	}}
	derived := []sittings.Sitting{
		sitting(0, 45*time.Minute),
		sitting(24*time.Hour, 24*time.Hour+30*time.Minute),
	}

	byShown := truth.ByShown(derived)
	want := []string{string(adjustments.Derivation), string(adjustments.Observations)}
	if got := slices.Sorted(maps.Keys(byShown)); !slices.Equal(got, want) {
		t.Fatalf("ByShown keys = %v, want %v", got, want)
	}
	var sum fit.Residual
	for _, r := range byShown {
		sum = sum.Add(r)
	}
	if pooled := truth.Score(derived); sum != pooled {
		t.Errorf("the per-population residuals sum to %+v, against a pooled %+v", sum, pooled)
	}
}

// A day reviewed BOTH ways is two complete tilings of the same 24 hours. Each
// is a partition on its own; together they are not, and the overlap check has
// to be made within a population rather than across it or `timetattle fit`
// stops working outright the first time a reviewed day is re-reviewed blind.
func TestValidateChecksThePartitionWithinEachPopulationRatherThanAcross(t *testing.T) {
	both := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		label("2026-03-02", 30*time.Minute, 120*time.Minute, false),
		blind("2026-03-02", 0, 45*time.Minute, true),
		blind("2026-03-02", 45*time.Minute, 120*time.Minute, false),
	}}
	if err := both.Validate(); err != nil {
		t.Errorf("Validate refused a day reviewed both ways, which is two partitions "+
			"and not one broken one: %v", err)
	}

	// Within one population the check still bites, and says which population.
	broken := fit.Truth{Labels: []fit.Label{
		blind("2026-03-02", 0, 30*time.Minute, true),
		blind("2026-03-02", 20*time.Minute, 60*time.Minute, false),
	}}
	err := broken.Validate()
	if err == nil {
		t.Fatal("Validate accepted two blind labels claiming the same twenty minutes")
	}
	if !strings.Contains(err.Error(), "blind") {
		t.Errorf("the complaint does not say which population is broken: %v", err)
	}
}

// Pooling two populations that claim the same time counts that time twice in
// the denominator, so the pooled residual is not a residual over a partition at
// all. Contested is what a caller has to check before pooling.
func TestContestedNamesTheDaysTwoPopulationsBothClaim(t *testing.T) {
	both := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 120*time.Minute, true),
		blind("2026-03-02", 0, 45*time.Minute, true),
		blind("2026-03-02", 45*time.Minute, 120*time.Minute, false),
		blind("2026-03-03", 24*time.Hour, 25*time.Hour, true),
	}}
	if got, want := both.Contested(), []string{"2026-03-02"}; !slices.Equal(got, want) {
		t.Errorf("Contested() = %v, want %v", got, want)
	}

	apart := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 120*time.Minute, true),
		blind("2026-03-03", 24*time.Hour, 25*time.Hour, true),
	}}
	if got := apart.Contested(); len(got) != 0 {
		t.Errorf("Contested() = %v for two populations that share no instant, want none", got)
	}
}
