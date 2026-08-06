package fit

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// Label is one human assertion about a wall-clock span: work, or not work.
//
// Day names the reviewed day it came out of, so a residual can be reported per
// day as well as pooled. Regime names which evidence regime that day sits in —
// see Truth.Coverage — and is what stops a pooled figure hiding the fact that
// the days where the parameters decide something and the days where the labels
// are plentiful are almost disjoint sets.
type Label struct {
	Day    string
	Regime string
	Span   timespan.Span
	// Shown is what the reviewer was looking at when they asserted this, and is
	// the third thing a residual must not be pooled across. Read it through Saw
	// rather than directly; the zero value means a confirmation. See
	// population.go for the whole argument and the measurement behind it.
	Shown adjustments.Shown
	// Engaged is what the reviewer asserted. There is no third value here on
	// purpose: a span nobody asserted anything about is not a Label at all, and
	// the absence of a label must never be readable as an assertion.
	Engaged bool
}

// Residual is the disagreement between a derivation and the labels, in two
// directions that are never summed into one number without both being visible.
//
// Orphaned is time the reviewer said was work and the model does not claim.
// Contradicted is time the reviewer said was NOT work and the model claims.
// They are different failures — the first is a model that sees too little, the
// second a model that sees too much — and a single figure cannot tell a fitter
// which way to move.
//
// AssertedEngaged and AssertedNotEngaged are the denominators. A residual
// without them is a duration with no scale, and 1.257h means one thing against
// 143h of labels and another against 2h.
type Residual struct {
	Orphaned, Contradicted              time.Duration
	AssertedEngaged, AssertedNotEngaged time.Duration
}

// Total is the symmetric difference: the whole disagreement, in both directions.
func (r Residual) Total() time.Duration { return r.Orphaned + r.Contradicted }

// Scale is how much labelled time the residual is a fraction of.
func (r Residual) Scale() time.Duration { return r.AssertedEngaged + r.AssertedNotEngaged }

// Share is the residual as a fraction of the labelled time, and 0 when nothing
// was labelled.
func (r Residual) Share() float64 {
	if r.Scale() == 0 {
		return 0
	}
	return r.Total().Seconds() / r.Scale().Seconds()
}

// Add sums two residuals, which is what a per-day breakdown folds back up with.
func (r Residual) Add(o Residual) Residual {
	return Residual{
		Orphaned:           r.Orphaned + o.Orphaned,
		Contradicted:       r.Contradicted + o.Contradicted,
		AssertedEngaged:    r.AssertedEngaged + o.AssertedEngaged,
		AssertedNotEngaged: r.AssertedNotEngaged + o.AssertedNotEngaged,
	}
}

// Objective scores a derivation. Lower is better, and the unit is wall clock
// rather than an abstract loss, so every figure a report prints can be argued
// with in hours.
//
// It is an interface so that a synthetic corpus whose right answer is known can
// be fitted with a synthetic objective. That is not a convenience: a fitter is
// only verifiable where the answer is known in advance, and the real labels are
// exactly the case where it is not.
type Objective interface {
	// Name says what is being minimised, and is printed beside every figure
	// that came out of it.
	Name() string
	// Score measures one derivation against the truth.
	Score(derived []sittings.Sitting) Residual
	// Nulls are what a model with no skill at all would score. They are part of
	// the interface rather than an extra a caller may look up, because the whole
	// design rule of this package is that a residual is never reported without
	// the scale it sits on, and an objective that could not say what a no-skill
	// answer costs would make that rule unenforceable.
	Nulls() (allEngaged, noneEngaged Residual)
	// Validate refuses an objective that cannot be scored against, naming the
	// problem. Run calls it before it clusters anything, so an empty label set
	// is an error rather than a residual of zero at every point.
	Validate() error
}

// Truth is a set of human labels, and is the Objective the real fit runs
// against.
//
// It scores ONLY inside the labelled spans. A sitting outside every label
// contributes nothing in either direction, because nobody said anything about
// that time and scoring it would turn the absence of a label into an assertion
// that no work happened — the two-state version of a bug this project has
// shipped three times.
type Truth struct {
	Labels []Label
}

// Name implements Objective.
//
// The census is part of the name rather than a line further down the report,
// because this string is quoted beside every figure that came out of the
// objective, and a residual quoted without which population produced it is the
// exact defect this package exists to prevent.
func (t Truth) Name() string {
	return fmt.Sprintf("symmetric difference against %d human assertions over %d days — %s",
		len(t.Labels), len(t.Days()), presentPhrase(t.Census()))
}

// Score implements Objective.
func (t Truth) Score(derived []sittings.Sitting) Residual {
	return score(t.Labels, model(derived))
}

// ByDay is Score broken out per reviewed day, so that a pooled figure can be
// checked against the days it came from and a day with no discriminating power
// can be recognised as one.
func (t Truth) ByDay(derived []sittings.Sitting) map[string]Residual {
	return t.groupBy(derived, func(l Label) string { return l.Day })
}

// ByRegime is Score broken out per evidence regime. See Coverage.
func (t Truth) ByRegime(derived []sittings.Sitting) map[string]Residual {
	return t.groupBy(derived, func(l Label) string { return l.Regime })
}

func (t Truth) groupBy(derived []sittings.Sitting, key func(Label) string) map[string]Residual {
	if len(t.Labels) == 0 {
		return nil
	}
	m := model(derived)
	out := map[string]Residual{}
	for _, l := range t.Labels {
		k := key(l)
		out[k] = out[k].Add(score([]Label{l}, m))
	}
	return out
}

// Days is every day the labels cover, sorted.
func (t Truth) Days() []string {
	seen := map[string]bool{}
	for _, l := range t.Labels {
		seen[l.Day] = true
	}
	return slices.Sorted(maps.Keys(seen))
}

// Regimes is every regime the labels cover, with how many days each holds. A
// regime represented by one or two days cannot support a fit, and the count is
// what makes that visible rather than a footnote.
func (t Truth) Regimes() map[string]int {
	days := map[string]map[string]bool{}
	for _, l := range t.Labels {
		if days[l.Regime] == nil {
			days[l.Regime] = map[string]bool{}
		}
		days[l.Regime][l.Day] = true
	}
	out := map[string]int{}
	for regime, d := range days {
		out[regime] = len(d)
	}
	return out
}

// Nulls implements Objective: one model claiming the whole labelled span as
// work, and one claiming none of it.
//
// They are the answer to "is this residual small?", which cannot be answered by
// the residual. A fit whose residual is not far below both of these has learnt
// nothing, and against labels that are a strict subset of the derivation they
// were drawn from, that is the expected outcome rather than a surprise.
func (t Truth) Nulls() (allEngaged, noneEngaged Residual) {
	all := make([]sittings.Sitting, 0, len(t.Labels))
	for _, l := range t.Labels {
		all = append(all, sittings.Sitting{Start: l.Span.Start, End: l.Span.End})
	}
	return t.Score(all), t.Score(nil)
}

// Span is the whole extent the labels cover, which is what a corpus is narrowed
// to before the search runs.
func (t Truth) Span() []timespan.Span {
	spans := make([]timespan.Span, 0, len(t.Labels))
	for _, l := range t.Labels {
		spans = append(spans, l.Span)
	}
	return timespan.Union(spans)
}

// Validate refuses labels that cannot be scored against, naming the problem.
//
// Overlap is the one worth spelling out. Two labels covering the same instant
// would count that instant twice in the denominator, and if they disagreed the
// residual would depend on which was read first — an answer that differs
// between two readers of the same database with nothing to flag it.
//
// IT IS CHECKED WITHIN EACH POPULATION AND NOT ACROSS THEM. A day confirmed
// once and then reviewed blind is two complete tilings of the same 24 hours,
// each a partition on its own; a check across the pooled set would call that
// corrupt ground truth and stop `timetattle fit` working outright the first
// time `review -blind` is pointed at a day that was already reviewed. What that
// case actually is is a pair that must not be POOLED, which is Contested.
func (t Truth) Validate() error {
	if len(t.Labels) == 0 {
		return fmt.Errorf("there are no labels to fit against")
	}
	for _, p := range t.Census() {
		if p.Empty() {
			continue
		}
		if err := partition(t.only(p.Shown), p.Shown); err != nil {
			return err
		}
	}
	return nil
}

// partition refuses one population that does not tile the time it claims.
func partition(labels []Label, shown adjustments.Shown) error {
	sorted := slices.Clone(labels)
	slices.SortStableFunc(sorted, func(a, b Label) int { return a.Span.Start.Compare(b.Span.Start) })
	for i, l := range sorted {
		if !l.Span.End.After(l.Span.Start) {
			return fmt.Errorf("the %s label on %s over %s..%s covers no time, so it asserts nothing",
				Describe(shown), l.Day, stamp(l.Span.Start), stamp(l.Span.End))
		}
		if i > 0 && l.Span.Start.Before(sorted[i-1].Span.End) {
			return fmt.Errorf("two %s labels claim the same time: %s..%s and %s..%s overlap, "+
				"so the labelled total would count it twice",
				Describe(shown),
				stamp(sorted[i-1].Span.Start), stamp(sorted[i-1].Span.End),
				stamp(l.Span.Start), stamp(l.Span.End))
		}
	}
	return nil
}

// Coverage is, per day, the share of the time a reviewer asserted was work that
// some observation with an actual DURATION covered.
//
// It is the parameter-free discriminator between the two evidence regimes this
// corpus holds, and it is a better one than a date cut because it measures the
// quantity the date cut is a proxy for. Every interval-shaped source in the live
// store begins in July 2026; before that a sitting's whole extent is
// manufactured by lead-in, tail-out and the gap, so a day whose coverage is zero
// cannot discriminate between parameter values however many labels it carries.
// Fourteen of the sixteen usable reviewed days sit above it and two sit at zero.
//
// A day with no engaged labels at all gets 0, which here means "no asserted work
// to cover" rather than "asserted work nothing observed". The two are
// distinguishable from the same Truth by asking whether the day has any engaged
// label, and this function deliberately does not collapse that question into its
// answer.
//
// PRESENCE DOES NOT COUNT, which is the whole reason this takes the parameters.
// A lit screen bounds a sitting and never creates one, so a day whose only
// duration-bearing observation is the backlight has an engaged extent that is
// still entirely manufactured by the margins — which is exactly the state this
// measurement exists to detect. Counting the backlight here would report such a
// day as fully covered and hide the thing being looked for.
func (t Truth) Coverage(events []source.Event, p sittings.Params) map[string]float64 {
	skip := make(map[string]bool, len(p.PresenceSources)+len(p.IgnoreSources))
	for _, id := range slices.Concat(p.PresenceSources, p.IgnoreSources) {
		skip[id] = true
	}
	var intervals []timespan.Span
	for _, e := range events {
		// Shape is the second question. A point observes an instant and no
		// duration whatever it came from, and an unbounded one observed one
		// endpoint and no extent.
		if skip[e.Source] || e.Shape != source.Interval || e.End.Before(e.Start) {
			continue
		}
		intervals = append(intervals, timespan.Span{Start: e.Start, End: e.End})
	}
	observed := timespan.Union(intervals)

	asserted, covered := map[string]time.Duration{}, map[string]time.Duration{}
	out := map[string]float64{}
	for _, l := range t.Labels {
		out[l.Day] = 0
		if !l.Engaged {
			continue
		}
		asserted[l.Day] += l.Span.Duration()
		covered[l.Day] += timespan.Total(timespan.Intersect([]timespan.Span{l.Span}, observed))
	}
	for day, total := range asserted {
		if total > 0 {
			out[day] = covered[day].Seconds() / total.Seconds()
		}
	}
	return out
}

// score measures one set of labels against an already-unioned model.
func score(labels []Label, model []timespan.Span) Residual {
	var r Residual
	for _, l := range labels {
		length := l.Span.Duration()
		covered := timespan.Total(timespan.Intersect([]timespan.Span{l.Span}, model))
		if l.Engaged {
			r.AssertedEngaged += length
			r.Orphaned += length - covered
			continue
		}
		r.AssertedNotEngaged += length
		r.Contradicted += covered
	}
	return r
}

// model is the derivation as a disjoint run. Sittings are disjoint already, but
// unioning is what makes that a property of this function rather than an
// assumption about its caller.
func model(derived []sittings.Sitting) []timespan.Span {
	spans := make([]timespan.Span, 0, len(derived))
	for _, s := range derived {
		spans = append(spans, s.Span())
	}
	return timespan.Union(spans)
}

func stamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05Z") }
