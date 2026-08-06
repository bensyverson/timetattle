package fit_test

import (
	"maps"
	"slices"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/fit"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

func label(day string, from, to time.Duration, engaged bool) fit.Label {
	return fit.Label{
		Day:  day,
		Span: timespan.Span{Start: at(from), End: at(to)},
		// Every synthetic day below is in one regime unless a test says
		// otherwise, so that the regime split is exercised where it is the
		// subject and is not noise everywhere else.
		Regime:  "synthetic",
		Engaged: engaged,
	}
}

func sitting(from, to time.Duration) sittings.Sitting {
	return sittings.Sitting{Start: at(from), End: at(to)}
}

// A day tiled the way a wholesale confirm tiles one: engaged over the derived
// blocks, not-engaged over every gap between them.
func tiledDay() fit.Truth {
	return fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		label("2026-03-02", 30*time.Minute, 90*time.Minute, false),
		label("2026-03-02", 90*time.Minute, 120*time.Minute, true),
	}}
}

// The two directions are different failures and must never collapse into one
// figure. A model that sees too little is orphaning a reviewer's claim; a model
// that sees too much is contradicting one.
func TestResidualIsTwoDirectional(t *testing.T) {
	truth := tiledDay()

	// Sees too little: covers half the first engaged block and none of the
	// second, and claims nothing inside the asserted gap.
	short := truth.Score([]sittings.Sitting{sitting(0, 15*time.Minute)})
	if got, want := short.Orphaned, 45*time.Minute; got != want {
		t.Errorf("orphaned = %v, want %v", got, want)
	}
	if short.Contradicted != 0 {
		t.Errorf("contradicted = %v, want 0: nothing was claimed inside the not-engaged span", short.Contradicted)
	}

	// Sees too much: one sitting over the whole day.
	long := truth.Score([]sittings.Sitting{sitting(0, 120*time.Minute)})
	if long.Orphaned != 0 {
		t.Errorf("orphaned = %v, want 0", long.Orphaned)
	}
	if got, want := long.Contradicted, 60*time.Minute; got != want {
		t.Errorf("contradicted = %v, want %v", got, want)
	}

	if got, want := long.AssertedEngaged, 60*time.Minute; got != want {
		t.Errorf("AssertedEngaged = %v, want %v", got, want)
	}
	if got, want := long.AssertedNotEngaged, 60*time.Minute; got != want {
		t.Errorf("AssertedNotEngaged = %v, want %v", got, want)
	}
	if got, want := long.Total(), 60*time.Minute; got != want {
		t.Errorf("Total = %v, want %v", got, want)
	}
}

// The absence of a label is not an assertion. A sitting outside every labelled
// span is neither agreement nor disagreement, and scoring it as either would
// turn 579 unreviewed days into judgements nobody made.
func TestScoreIgnoresTimeOutsideTheLabels(t *testing.T) {
	truth := tiledDay()
	// A derivation that is wrong inside the labels, so that "unchanged" is a
	// claim about a number rather than about two zeroes.
	inside := truth.Score([]sittings.Sitting{sitting(0, 45*time.Minute)})
	if got, want := inside.Total(), 45*time.Minute; got != want {
		t.Fatalf("the residual inside the labels is %v, want %v", got, want)
	}
	outside := truth.Score([]sittings.Sitting{sitting(0, 45*time.Minute), sitting(10*time.Hour, 14*time.Hour)})
	if inside != outside {
		t.Errorf("a sitting outside every label changed the residual: %+v against %+v", outside, inside)
	}
}

// The residual has no meaning without something to read it against. These two
// are what a model with no skill at all would score, and they bound the
// objective from above.
func TestNullsBoundTheObjective(t *testing.T) {
	truth := tiledDay()
	all, none := truth.Nulls()

	if got, want := all.Contradicted, 60*time.Minute; got != want {
		t.Errorf("the all-engaged null contradicted %v, want the whole not-engaged label at %v", got, want)
	}
	if all.Orphaned != 0 {
		t.Errorf("the all-engaged null orphaned %v, want 0", all.Orphaned)
	}
	if got, want := none.Orphaned, 60*time.Minute; got != want {
		t.Errorf("the none-engaged null orphaned %v, want the whole engaged label at %v", got, want)
	}
	if none.Contradicted != 0 {
		t.Errorf("the none-engaged null contradicted %v, want 0", none.Contradicted)
	}
}

func TestByDayAndByRegimeFoldBackToThePooledScore(t *testing.T) {
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		label("2026-03-02", 30*time.Minute, 60*time.Minute, false),
		{Day: "2026-03-03", Regime: "other", Engaged: true,
			Span: timespan.Span{Start: at(24 * time.Hour), End: at(25 * time.Hour)}},
	}}
	derived := []sittings.Sitting{sitting(0, 45*time.Minute), sitting(24*time.Hour, 24*time.Hour+30*time.Minute)}

	pooled := truth.Score(derived)
	byDay := truth.ByDay(derived)
	if got, want := slices.Sorted(maps.Keys(byDay)), []string{"2026-03-02", "2026-03-03"}; !slices.Equal(got, want) {
		t.Fatalf("ByDay keys = %v, want %v", got, want)
	}
	var sum fit.Residual
	for _, r := range byDay {
		sum = sum.Add(r)
	}
	if sum != pooled {
		t.Errorf("the per-day residuals sum to %+v, against a pooled %+v", sum, pooled)
	}

	byRegime := truth.ByRegime(derived)
	if got, want := slices.Sorted(maps.Keys(byRegime)), []string{"other", "synthetic"}; !slices.Equal(got, want) {
		t.Fatalf("ByRegime keys = %v, want %v", got, want)
	}
	sum = fit.Residual{}
	for _, r := range byRegime {
		sum = sum.Add(r)
	}
	if sum != pooled {
		t.Errorf("the per-regime residuals sum to %+v, against a pooled %+v", sum, pooled)
	}

	if got, want := truth.Regimes(), map[string]int{"synthetic": 1, "other": 1}; !maps.Equal(got, want) {
		t.Errorf("Regimes() = %v, want %v", got, want)
	}
}

// Overlapping labels would double-count a denominator, and two labels asserting
// opposite things about one instant would make the residual depend on which was
// read first. Both are refused by name rather than averaged over.
func TestValidateRefusesOverlappingLabels(t *testing.T) {
	truth := fit.Truth{Labels: []fit.Label{
		label("2026-03-02", 0, 30*time.Minute, true),
		label("2026-03-02", 20*time.Minute, 60*time.Minute, false),
	}}
	if err := truth.Validate(); err == nil {
		t.Fatal("Validate accepted two labels claiming the same twenty minutes")
	}
	if err := tiledDay().Validate(); err != nil {
		t.Errorf("Validate refused a properly tiled day: %v", err)
	}
}

func TestValidateRefusesNoLabelsAtAll(t *testing.T) {
	if err := (fit.Truth{}).Validate(); err == nil {
		t.Fatal("Validate accepted an empty set of labels; there is nothing to fit against")
	}
}

// Coverage is the parameter-free discriminator between the two regimes: how
// much of the time a reviewer called work was covered by an observation that
// actually had a duration. On a day made only of point observations it is zero
// however the parameters are set, and the whole sitting is manufactured by the
// margins.
func TestCoverageMeasuresHowMuchAssertedWorkWasObservedWithDuration(t *testing.T) {
	truth := tiledDay()

	p := params(min15, min5, min5)

	pointsOnly := truth.Coverage([]source.Event{mark(5 * time.Minute), mark(95 * time.Minute)}, p)
	got, ok := pointsOnly["2026-03-02"]
	if !ok {
		t.Fatalf("no coverage was reported for the labelled day; Coverage = %v", pointsOnly)
	}
	if got != 0 {
		t.Errorf("coverage from point observations alone = %v, want 0", got)
	}

	// A lit screen across the WHOLE day covers nothing, because presence bounds
	// a sitting and never creates one. A day whose only interval is the
	// backlight still has an engaged extent entirely manufactured by the
	// margins, which is the state this measurement exists to find.
	backlitOnly := truth.Coverage([]source.Event{lit(0, 120*time.Minute)}, p)
	if got := backlitOnly["2026-03-02"]; got != 0 {
		t.Errorf("coverage from presence alone = %v, want 0", got)
	}

	// One fifteen-minute EVIDENCE interval inside the first engaged block, of
	// sixty minutes asserted engaged across the day.
	withInterval := truth.Coverage([]source.Event{observed(0, 15*time.Minute)}, p)
	if got, want := withInterval["2026-03-02"], 0.25; got != want {
		t.Errorf("coverage = %v, want %v", got, want)
	}
}
