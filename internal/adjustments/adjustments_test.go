package adjustments_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
)

// The fold is a pure function, so every test here is a comparison rather than a
// claim. Nothing in this file touches a database.

var day = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)

// at is an instant on the reference day, as hours and minutes.
func at(hour, minute int) time.Time {
	return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

// span is the machine's answer, or a piece of it.
func span(fromHour, fromMin, toHour, toMin int) adjustments.Span {
	return adjustments.Span{Start: at(fromHour, fromMin), End: at(toHour, toMin)}
}

// says builds one adjustment recorded at the given ordinal minute, so that log
// order is written down rather than implied by slice order.
func says(minute int, a adjustments.Assertion, fromHour, fromMin, toHour, toMin int) adjustments.Adjustment {
	return adjustments.Adjustment{
		ID:         int64(minute),
		Start:      at(fromHour, fromMin),
		End:        at(toHour, toMin),
		Assertion:  a,
		Origin:     "a-person",
		RecordedAt: day.Add(240*time.Hour + time.Duration(minute)*time.Minute),
	}
}

func hours(spans []adjustments.Span) time.Duration {
	var total time.Duration
	for _, s := range spans {
		total += s.End.Sub(s.Start)
	}
	return total
}

func describe(spans []adjustments.Span) string {
	var out []string
	for _, s := range spans {
		out = append(out, s.Start.Format("15:04")+"–"+s.End.Format("15:04"))
	}
	if len(out) == 0 {
		return "(nothing)"
	}
	return strings.Join(out, " ")
}

func want(t *testing.T, got []adjustments.Span, expect string) {
	t.Helper()
	if describe(got) != expect {
		t.Errorf("the overlay produced %s, want %s", describe(got), expect)
	}
}

// An empty log changes nothing at all. This is the state 100% of days are in
// today, so it is the one that must be exactly free.
func TestAnEmptyLogLeavesTheMachineAnswerAlone(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0), span(13, 0, 17, 0)}
	got := adjustments.Fold(nil, nil).Engaged(machine)
	want(t, got, "09:00–11:00 13:00–17:00")
	if hours(got) != 6*time.Hour {
		t.Errorf("the total moved to %v", hours(got))
	}
}

// EXTEND. The model found nothing before 09:00; the person says they were at it
// from 08:00.
func TestEngagedExtendsTheMachineAnswer(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0)}
	log := []adjustments.Adjustment{says(1, adjustments.Engaged, 8, 0, 9, 0)}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "08:00–11:00")
}

// TRIM. The model ran the sitting to 11:00; it ended at 10:30.
func TestNotEngagedTrimsTheMachineAnswer(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0)}
	log := []adjustments.Adjustment{says(1, adjustments.NotEngaged, 10, 30, 11, 0)}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–10:30")
}

// DELETE. A whole sitting that was not work at all.
func TestNotEngagedAcrossAWholeSittingRemovesIt(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0), span(13, 0, 17, 0)}
	log := []adjustments.Adjustment{says(1, adjustments.NotEngaged, 9, 0, 11, 0)}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "13:00–17:00")
	if hours(got) != 4*time.Hour {
		t.Errorf("the total is %v, want 4h", hours(got))
	}
}

// TRIM, the interior case. A meeting inside a sitting splits it in two, which
// is what makes trim and split the same operation from different directions.
func TestNotEngagedInsideASittingSplitsIt(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	log := []adjustments.Adjustment{says(1, adjustments.NotEngaged, 12, 0, 13, 0)}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–12:00 13:00–17:00")
	if hours(got) != 7*time.Hour {
		t.Errorf("the total is %v, want 7h", hours(got))
	}
}

// SPLIT with no gap. Two pieces of work run together with nothing observable
// between them: the wall clock is unchanged and the count is not.
func TestABoundaryCutsASittingWithoutChangingTheTotal(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	log := []adjustments.Adjustment{{
		ID: 1, Start: at(12, 0), End: at(12, 0),
		Assertion: adjustments.Boundary, Origin: "a-person",
		RecordedAt: day.Add(240 * time.Hour),
	}}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–12:00 12:00–17:00")
	if hours(got) != 8*time.Hour {
		t.Errorf("a boundary moved the total to %v, want 8h", hours(got))
	}
	if len(got) != 2 {
		t.Errorf("a boundary produced %d sittings, want 2", len(got))
	}
}

func TestABoundaryOutsideEverySittingChangesNothing(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0)}
	log := []adjustments.Adjustment{{
		ID: 1, Start: at(12, 0), End: at(12, 0),
		Assertion: adjustments.Boundary, Origin: "a-person",
		RecordedAt: day.Add(240 * time.Hour),
	}}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–11:00")
}

// A boundary on an endpoint is not a cut. Cutting there would emit a
// zero-length sitting, which is the shape the schema refuses to store and must
// not be manufactured on the way out either.
func TestABoundaryOnAnEndpointEmitsNoEmptySitting(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0)}
	for _, edge := range []time.Time{at(9, 0), at(11, 0)} {
		log := []adjustments.Adjustment{{
			ID: 1, Start: edge, End: edge,
			Assertion: adjustments.Boundary, Origin: "a-person",
			RecordedAt: day.Add(240 * time.Hour),
		}}
		got := adjustments.Fold(nil, log).Engaged(machine)
		want(t, got, "09:00–11:00")
	}
}

// THE FOLD RULE. The later assertion wins, per instant, and only per instant:
// asserting lunch off does not undo the morning.
func TestTheLaterAssertionWinsOnlyWhereItOverlaps(t *testing.T) {
	machine := []adjustments.Span{}
	log := []adjustments.Adjustment{
		says(1, adjustments.Engaged, 9, 0, 17, 0),
		says(2, adjustments.NotEngaged, 12, 0, 13, 0),
	}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–12:00 13:00–17:00")
}

// The other direction, which is the one that catches a fold implemented as
// "subtract every not-engaged last". A person who says "not work", then thinks
// again and says "it was work", must end up with work.
func TestAnAssertionCanBeTakenBackByALaterOne(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	log := []adjustments.Adjustment{
		says(1, adjustments.NotEngaged, 12, 0, 13, 0),
		says(2, adjustments.Engaged, 12, 0, 13, 0),
	}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–17:00")
	if hours(got) != 8*time.Hour {
		t.Errorf("the total is %v, want 8h", hours(got))
	}
}

// Slice order must not be the fold order. A caller reading the log back in a
// different order has to get the same answer, or the overlay depends on a
// query's ORDER BY.
func TestTheFoldOrdersOnRecordedAtRatherThanSliceOrder(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	forward := []adjustments.Adjustment{
		says(1, adjustments.NotEngaged, 12, 0, 13, 0),
		says(2, adjustments.Engaged, 12, 0, 13, 0),
	}
	backward := []adjustments.Adjustment{forward[1], forward[0]}
	if a, b := describe(adjustments.Fold(nil, forward).Engaged(machine)),
		describe(adjustments.Fold(nil, backward).Engaged(machine)); a != b {
		t.Errorf("shuffling the log changed the answer: %s vs %s", a, b)
	}
}

// Two assertions recorded in the same microsecond are ordered by id, which is
// monotonic. Without it the fold is a coin toss nothing would ever flag.
func TestSimultaneousAssertionsAreOrderedById(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	same := day.Add(240 * time.Hour)
	log := []adjustments.Adjustment{
		{ID: 2, Start: at(12, 0), End: at(13, 0), Assertion: adjustments.Engaged, Origin: "a", RecordedAt: same},
		{ID: 1, Start: at(12, 0), End: at(13, 0), Assertion: adjustments.NotEngaged, Origin: "a", RecordedAt: same},
	}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–17:00")
}

// Engagement has three states too, and the day review reads them one instant at
// a time. Asserted-not-work and nobody-has-said are the pair this project keeps
// collapsing, and collapsing them here would make an unreviewed hour look like
// an hour somebody had ruled out.
func TestEngagementHasThreeStates(t *testing.T) {
	log := []adjustments.Adjustment{
		says(1, adjustments.Engaged, 9, 0, 12, 0),
		says(2, adjustments.NotEngaged, 12, 0, 13, 0),
	}
	o := adjustments.Fold(nil, log)

	if worked, said := o.EngagedAt(at(10, 0)); !said || !worked {
		t.Errorf("at 10:00 the overlay says (%v, %v), want (true, true)", worked, said)
	}
	if worked, said := o.EngagedAt(at(12, 30)); !said || worked {
		t.Errorf("at 12:30 the overlay says (%v, %v), want (false, true) — ruled out", worked, said)
	}
	if worked, said := o.EngagedAt(at(15, 0)); said || worked {
		t.Errorf("at 15:00 the overlay says (%v, %v), want (false, false) — nobody has said", worked, said)
	}
}

// The pieces the fold resolves must be disjoint, or the lookup above returns
// whichever assertion happened to sort first. A later not-engaged over part of
// an earlier engaged span is the case that exposes it.
func TestALaterNotEngagedOverwritesTheEarlierEngaged(t *testing.T) {
	log := []adjustments.Adjustment{
		says(1, adjustments.Engaged, 9, 0, 17, 0),
		says(2, adjustments.NotEngaged, 12, 0, 13, 0),
	}
	o := adjustments.Fold(nil, log)
	if worked, said := o.EngagedAt(at(12, 30)); !said || worked {
		t.Errorf("at 12:30 the overlay says (%v, %v), want (false, true); "+
			"the earlier engaged assertion still covers the instant", worked, said)
	}
	if worked, said := o.EngagedAt(at(14, 0)); !said || !worked {
		t.Errorf("at 14:00 the overlay says (%v, %v), want (true, true); "+
			"the not-engaged assertion took more than it covered", worked, said)
	}
}

// REATTRIBUTE, and the three states it has to keep apart.
func TestAttributionHasThreeStates(t *testing.T) {
	log := []adjustments.Adjustment{
		{ID: 1, Start: at(9, 0), End: at(11, 0), Assertion: adjustments.Attributed,
			Project: "a-project", Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
		{ID: 2, Start: at(11, 0), End: at(12, 0), Assertion: adjustments.Unattributed,
			Origin: "a", RecordedAt: day.Add(241 * time.Hour)},
	}
	o := adjustments.Fold(nil, log)

	if p, said := o.Project(at(10, 0)); !said || p != "a-project" {
		t.Errorf("at 10:00 the overlay says (%q, %v), want (a-project, true)", p, said)
	}
	if p, said := o.Project(at(11, 30)); !said || p != "" {
		t.Errorf("at 11:30 the overlay says (%q, %v), want (\"\", true) — asserted to belong to nothing", p, said)
	}
	if p, said := o.Project(at(15, 0)); said || p != "" {
		t.Errorf("at 15:00 the overlay says (%q, %v), want (\"\", false) — nobody has said", p, said)
	}
}

func TestALaterAttributionWins(t *testing.T) {
	log := []adjustments.Adjustment{
		{ID: 1, Start: at(9, 0), End: at(17, 0), Assertion: adjustments.Attributed,
			Project: "first-project", Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
		{ID: 2, Start: at(13, 0), End: at(17, 0), Assertion: adjustments.Attributed,
			Project: "second-project", Origin: "a", RecordedAt: day.Add(241 * time.Hour)},
	}
	o := adjustments.Fold(nil, log)
	if p, _ := o.Project(at(10, 0)); p != "first-project" {
		t.Errorf("the morning is %q, want first-project", p)
	}
	if p, _ := o.Project(at(14, 0)); p != "second-project" {
		t.Errorf("the afternoon is %q, want second-project", p)
	}
}

// Attribution says nothing about engagement, and engagement says nothing about
// attribution. Folding them together would make reattributing a span quietly
// assert that it was worked.
func TestAttributionDoesNotChangeEngagement(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 11, 0)}
	log := []adjustments.Adjustment{
		{ID: 1, Start: at(14, 0), End: at(15, 0), Assertion: adjustments.Attributed,
			Project: "a-project", Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
	}
	got := adjustments.Fold(nil, log).Engaged(machine)
	want(t, got, "09:00–11:00")
}

// THE THIRD STATE. Reviewed-and-recalled, reviewed-and-not-recalled, and never
// reviewed are three answers and the overlay must give three.
func TestReviewHasThreeStates(t *testing.T) {
	reviews := []adjustments.Review{
		{ID: 1, Start: at(9, 0), End: at(12, 0), Verdict: adjustments.Recalled,
			Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
		{ID: 2, Start: at(12, 0), End: at(15, 0), Verdict: adjustments.Unremembered,
			Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
	}
	o := adjustments.Fold(reviews, nil)

	if v, reviewed := o.Review(at(10, 0)); !reviewed || v != adjustments.Recalled {
		t.Errorf("at 10:00 the overlay says (%q, %v), want (recalled, true)", v, reviewed)
	}
	if v, reviewed := o.Review(at(13, 0)); !reviewed || v != adjustments.Unremembered {
		t.Errorf("at 13:00 the overlay says (%q, %v), want (unremembered, true)", v, reviewed)
	}
	if v, reviewed := o.Review(at(20, 0)); reviewed || v != "" {
		t.Errorf("at 20:00 the overlay says (%q, %v), want (\"\", false) — never looked at", v, reviewed)
	}
}

// A review is a record of looking, not a record of working. Marking a span
// unremembered must not delete the machine's guess about it — that would be the
// two-state bug in a new costume, throwing away real evidence because a person
// could not recall it.
func TestAnUnrememberedSpanKeepsTheMachineAnswer(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	reviews := []adjustments.Review{
		{ID: 1, Start: at(9, 0), End: at(17, 0), Verdict: adjustments.Unremembered,
			Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
	}
	got := adjustments.Fold(reviews, nil).Engaged(machine)
	want(t, got, "09:00–17:00")
}

func TestALaterReviewWins(t *testing.T) {
	reviews := []adjustments.Review{
		{ID: 1, Start: at(9, 0), End: at(17, 0), Verdict: adjustments.Unremembered,
			Origin: "a", RecordedAt: day.Add(240 * time.Hour)},
		{ID: 2, Start: at(9, 0), End: at(12, 0), Verdict: adjustments.Recalled,
			Origin: "a", RecordedAt: day.Add(241 * time.Hour)},
	}
	o := adjustments.Fold(reviews, nil)
	if v, _ := o.Review(at(10, 0)); v != adjustments.Recalled {
		t.Errorf("the morning is %q, want recalled", v)
	}
	if v, _ := o.Review(at(14, 0)); v != adjustments.Unremembered {
		t.Errorf("the afternoon is %q, want unremembered", v)
	}
}

// THE CALIBRATION SIGNAL, which is the whole reason adjustments are an overlay
// rather than an input to the clustering. The residual between the machine
// answer and the adjusted one is the model's error, and it is only measurable
// while the machine answer stays untouched.
func TestTheResidualIsTheModelError(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	log := []adjustments.Adjustment{says(1, adjustments.NotEngaged, 12, 0, 13, 0)}
	o := adjustments.Fold(nil, log)

	adjusted := o.Engaged(machine)
	if residual := hours(machine) - hours(adjusted); residual != time.Hour {
		t.Errorf("the residual is %v, want 1h", residual)
	}
	// And the machine answer is the same slice it was handed.
	want(t, machine, "09:00–17:00")
}

func TestFoldCopiesTheMachineAnswerRatherThanEditingIt(t *testing.T) {
	machine := []adjustments.Span{span(9, 0, 17, 0)}
	log := []adjustments.Adjustment{says(1, adjustments.NotEngaged, 12, 0, 13, 0)}
	adjustments.Fold(nil, log).Engaged(machine)
	if len(machine) != 1 || !machine[0].End.Equal(at(17, 0)) {
		t.Errorf("Engaged edited its argument: %s", describe(machine))
	}
}

// Validation, so the CLI and the day review refuse a nonsense correction before
// the database has to.
func TestValidateRefusesTheShapesTheSchemaRefuses(t *testing.T) {
	good := adjustments.Adjustment{
		Start: at(9, 0), End: at(11, 0), Assertion: adjustments.Engaged,
		Origin: "a-person", RecordedAt: day,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("an ordinary adjustment was refused: %v", err)
	}

	cases := map[string]func(a *adjustments.Adjustment){
		"a backwards span":                 func(a *adjustments.Adjustment) { a.Start, a.End = a.End, a.Start },
		"a zero-width engagement":          func(a *adjustments.Adjustment) { a.End = a.Start },
		"an unknown assertion":             func(a *adjustments.Adjustment) { a.Assertion = "probably" },
		"no assertion at all":              func(a *adjustments.Adjustment) { a.Assertion = "" },
		"no origin":                        func(a *adjustments.Adjustment) { a.Origin = "" },
		"no recording time":                func(a *adjustments.Adjustment) { a.RecordedAt = time.Time{} },
		"a project on an engagement":       func(a *adjustments.Adjustment) { a.Project = "a-project" },
		"an attribution naming no project": func(a *adjustments.Adjustment) { a.Assertion = adjustments.Attributed },
		"a boundary across a span":         func(a *adjustments.Adjustment) { a.Assertion = adjustments.Boundary },
	}
	for name, break_ := range cases {
		a := good
		break_(&a)
		if err := a.Validate(); err == nil {
			t.Errorf("Validate accepted %s", name)
		}
	}
}

func TestReviewValidateRefusesTheShapesTheSchemaRefuses(t *testing.T) {
	good := adjustments.Review{
		Start: at(9, 0), End: at(17, 0), Verdict: adjustments.Recalled,
		Origin: "a-person", RecordedAt: day,
	}
	if err := good.Validate(); err != nil {
		t.Fatalf("an ordinary review was refused: %v", err)
	}

	cases := map[string]func(r *adjustments.Review){
		"a zero-width review": func(r *adjustments.Review) { r.End = r.Start },
		"a backwards review":  func(r *adjustments.Review) { r.Start, r.End = r.End, r.Start },
		"an unknown verdict":  func(r *adjustments.Review) { r.Verdict = "maybe" },
		"no verdict at all":   func(r *adjustments.Review) { r.Verdict = "" },
		"no origin":           func(r *adjustments.Review) { r.Origin = "" },
		"no recording time":   func(r *adjustments.Review) { r.RecordedAt = time.Time{} },
	}
	for name, break_ := range cases {
		r := good
		break_(&r)
		if err := r.Validate(); err == nil {
			t.Errorf("Validate accepted %s", name)
		}
	}
}
