package dayview_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// Everything here is a constructed case. The real-data checks sit beside them
// in realdata_test.go, because neither substitutes for the other: a fixture
// cannot establish that the closure survives a real day, and a real day cannot
// make an unremembered block happen on demand.

// The calendar is UTC with the shipped 05:00 boundary, so a "day" in these
// tests runs 05:00 to 05:00 and the arithmetic is readable.
var cal = sessions.Calendar{Location: time.UTC, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday}

var theDay = sessions.Day{Year: 2026, Month: time.July, Date: 28}

var params = sittings.Params{
	Gap:             15 * time.Minute,
	LeadIn:          5 * time.Minute,
	TailOut:         5 * time.Minute,
	PresenceSources: []string{"backlit"},
	IgnoreSources:   []string{"locked"},
}

// at is an instant on the reference working day, as local hours and minutes
// past midnight. 26:00 is 02:00 the following morning, which is still this
// working day.
func at(hour, minute int) time.Time {
	return time.Date(2026, 7, 28, 0, minute, 0, 0, time.UTC).Add(time.Duration(hour) * time.Hour)
}

func span(fromHour, fromMin, toHour, toMin int) timespan.Span {
	return timespan.Span{Start: at(fromHour, fromMin), End: at(toHour, toMin)}
}

// sitting is one row of the machine's answer.
func sitting(fromHour, fromMin, toHour, toMin int, events int, sources ...string) sittings.Sitting {
	s := sittings.Sitting{
		Start: at(fromHour, fromMin), End: at(toHour, toMin),
		Events: events, Sources: sources,
	}
	s.Margin = s.Duration()
	return s
}

// derived wraps sittings in the derivation that produced them. A non-zero
// ComputedAt is what says the model has run at all.
func derived(ss ...sittings.Sitting) sittings.Derived {
	return sittings.Derived{
		Params:     params,
		ComputedAt: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		Sittings:   ss,
	}
}

// evidence is one observation of a project's work: a point, which is what
// every attributable source in this corpus actually emits.
func evidence(project, src string, hour, minute int) source.Event {
	e := source.Event{
		Start: at(hour, minute), End: at(hour, minute),
		Shape: source.Point, Source: src, Locator: "/somewhere/" + project,
	}
	e.Identity.Project = project
	return e
}

// presence is a backlight reading, which bounds sittings and creates none.
func presence(state string, fromHour, fromMin, toHour, toMin int) source.Event {
	return source.Event{
		Start: at(fromHour, fromMin), End: at(toHour, toMin),
		Shape: source.Interval, Source: "backlit", State: state,
	}
}

func assemble(t *testing.T, d sittings.Derived, events []source.Event,
	reviews []adjustments.Review, adjs []adjustments.Adjustment) dayview.Day {
	t.Helper()
	day, err := dayview.Assemble(cal, theDay, d, events, reviews, adjs)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	return day
}

// answer is the ordinary shape of a reviewer's reply, with the fields that are
// the same in every test filled in.
func answer(mode dayview.Mode, js []dayview.Judgement) dayview.Answer {
	return dayview.Answer{Mode: mode, Blocks: js, Origin: "a-person", At: time.Now().UTC()}
}

func render(spans []timespan.Span) string {
	var out strings.Builder
	for i, s := range spans {
		if i > 0 {
			out.WriteString(" ")
		}
		out.WriteString(s.Start.Format("15:04") + "–" + s.End.Format("15:04"))
	}
	if out.String() == "" {
		return "(nothing)"
	}
	return out.String()
}

// spansOf pulls the spans out of a run of assertions of one kind, in time
// order, so a plan can be compared against a sentence.
func spansOf(as []adjustments.Adjustment, want adjustments.Assertion) []timespan.Span {
	var out []timespan.Span
	for _, a := range as {
		if a.Assertion == want {
			out = append(out, timespan.Span{Start: a.Start, End: a.End})
		}
	}
	slices.SortFunc(out, func(x, y timespan.Span) int { return x.Start.Compare(y.Start) })
	return out
}

// THREE STATES ON THE DERIVATION ITSELF. A database nobody has sessionized and
// a Sunday with no sittings in it are different facts, and the day review is
// exactly the reader that would otherwise print "no work" for both.

func TestADayWithNoDerivationSaysSoRatherThanReportingAnEmptyDay(t *testing.T) {
	day := assemble(t, sittings.Derived{}, nil, nil, nil)
	if day.Derived() {
		t.Error("a store nobody has sessionized reads as having been derived")
	}
	if len(day.Blocks) != 0 {
		t.Errorf("%d blocks came out of a derivation that never ran", len(day.Blocks))
	}
}

func TestADerivationThatFoundNothingIsNotTheSameAsNoDerivation(t *testing.T) {
	day := assemble(t, derived(), nil, nil, nil)
	if !day.Derived() {
		t.Error("a derivation that ran and found nothing reads as never having run")
	}
	if len(day.Blocks) != 0 {
		t.Errorf("%d blocks came out of an empty derivation", len(day.Blocks))
	}
}

// THE BLOCKS. A sitting is the machine's answer; a block is that answer as it
// falls inside one working day.

func TestBlocksAreTheDaysSittingsInTimeOrder(t *testing.T) {
	day := assemble(t, derived(sitting(14, 0, 16, 0, 9), sitting(9, 0, 11, 0, 12)), nil, nil, nil)
	if len(day.Blocks) != 2 {
		t.Fatalf("%d blocks, want 2", len(day.Blocks))
	}
	if got := render([]timespan.Span{day.Blocks[0].Span, day.Blocks[1].Span}); got != "09:00–11:00 14:00–16:00" {
		t.Errorf("the blocks are %s, want them in time order", got)
	}
	if day.Engaged() != 4*time.Hour {
		t.Errorf("the day totals %v, want 4h", day.Engaged())
	}
}

// A sitting crossing the 05:00 boundary belongs to two working days and is
// clipped into each. Reporting its whole extent in both would credit the same
// clock twice, which is the one property sittings exist to protect.
func TestASittingCrossingTheBoundaryIsClippedAndSaysSo(t *testing.T) {
	crossing := sitting(3, 0, 7, 0, 40)
	day := assemble(t, derived(crossing), nil, nil, nil)
	if len(day.Blocks) != 1 {
		t.Fatalf("%d blocks, want 1", len(day.Blocks))
	}
	b := day.Blocks[0]
	if got := render([]timespan.Span{b.Span}); got != "05:00–07:00" {
		t.Errorf("the block is %s, want it clipped to the working day at 05:00", got)
	}
	if !b.Clipped {
		t.Error("the block does not say it was clipped, so a reviewer cannot tell it continues")
	}
	if !b.Whole.Start.Equal(crossing.Start) || !b.Whole.End.Equal(crossing.End) {
		t.Errorf("the block lost the sitting's own extent: %s", render([]timespan.Span{b.Whole}))
	}
	if day.Engaged() != 2*time.Hour {
		t.Errorf("the day totals %v, want the 2h inside it", day.Engaged())
	}
}

// ONE ROW PER ACTIVE PROJECT. 74% of days in the live corpus have exactly one,
// so one row is the ordinary picture and several is the concurrent day.

func TestADayWithOneProjectHasOneRow(t *testing.T) {
	events := []source.Event{
		evidence("timetattle", "claude", 9, 10),
		evidence("timetattle", "git", 10, 30),
	}
	day := assemble(t, derived(sitting(9, 5, 10, 35, 2)), events, nil, nil)
	if len(day.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(day.Rows))
	}
	if day.Rows[0].Project != "timetattle" {
		t.Errorf("the row is %q", day.Rows[0].Project)
	}
	if day.Rows[0].Events != 2 {
		t.Errorf("the row rests on %d observations, want 2", day.Rows[0].Events)
	}
}

func TestAConcurrentDayGetsARowPerProjectAndTheBlockNamesBoth(t *testing.T) {
	events := []source.Event{
		evidence("timetattle", "claude", 9, 10),
		evidence("timetattle", "claude", 9, 50),
		evidence("acorn", "motion", 9, 30),
		evidence("acorn", "motion", 10, 5),
	}
	day := assemble(t, derived(sitting(9, 5, 10, 10, 4)), events, nil, nil)
	if len(day.Rows) != 2 {
		t.Fatalf("%d rows, want 2 — the overlap is the thing this layout exists to show", len(day.Rows))
	}
	var names []string
	for _, r := range day.Rows {
		names = append(names, r.Project)
	}
	if !slices.Equal(names, []string{"acorn", "timetattle"}) {
		t.Errorf("the rows are %v, want them sorted by project", names)
	}
	// The two projects INTERLEAVE inside one block rather than overlapping it,
	// which is what alternating between two clients at a 15m gap threshold
	// really looks like. Both rows claim clock inside the same block, and the
	// block names both, so the concurrency is visible without either row
	// claiming time the other was working.
	if !slices.Equal(day.Blocks[0].Projects, []string{"acorn", "timetattle"}) {
		t.Errorf("the block names %v, want both projects", day.Blocks[0].Projects)
	}
	for _, r := range day.Rows {
		if timespan.Total(timespan.Intersect(r.Active, []timespan.Span{day.Blocks[0].Span})) <= 0 {
			t.Errorf("%s claims no clock inside the block: %s", r.Project, render(r.Active))
		}
	}
}

// Genuine overlap, where two projects' evidence interleaves inside one gap
// threshold, has to come out as two rows claiming the SAME clock. This is the
// case a single stacked bar cannot represent at all.
func TestTwoProjectsWorkedInTheSameMinutesBothClaimIt(t *testing.T) {
	events := []source.Event{
		evidence("timetattle", "claude", 9, 10),
		evidence("timetattle", "claude", 9, 20),
		evidence("acorn", "motion", 9, 15),
	}
	day := assemble(t, derived(sitting(9, 5, 9, 25, 3)), events, nil, nil)
	if len(day.Rows) != 2 {
		t.Fatalf("%d rows, want 2", len(day.Rows))
	}
	both := timespan.Intersect(day.Rows[0].Active, day.Rows[1].Active)
	if timespan.Total(both) <= 0 {
		t.Errorf("the two rows share no clock: %s against %s",
			render(day.Rows[0].Active), render(day.Rows[1].Active))
	}
}

// A row claims where its OWN evidence clusters, not the whole of every block
// it appears anywhere inside. That second reading is internal/metrics' model A,
// which was measured to sum to 24.01h across a 12.78h day, and it is the shape
// a day view would fall into by accident because it draws so much better.
func TestARowClaimsWhereItsEvidenceClustersRatherThanTheWholeBlock(t *testing.T) {
	events := []source.Event{
		evidence("timetattle", "claude", 9, 10),
		evidence("timetattle", "claude", 9, 20),
		evidence("acorn", "motion", 11, 30),
	}
	block := sitting(9, 5, 12, 0, 3)
	day := assemble(t, derived(block), events, nil, nil)
	if len(day.Rows) != 2 {
		t.Fatalf("%d rows, want 2", len(day.Rows))
	}
	for _, r := range day.Rows {
		if got := timespan.Total(r.Active); got >= 30*time.Minute {
			t.Errorf("%s claims %v of a %v block from a handful of instants; a row that takes the "+
				"whole block is model A, and it double-counts every concurrent day",
				r.Name(), got, block.Duration())
		}
	}
	// And the day's own total is unaffected: rows are a reading of the blocks,
	// never a second source of engaged time.
	if day.Engaged() != block.Duration() {
		t.Errorf("the day totals %v, want the block's %v", day.Engaged(), block.Duration())
	}
}

// Evidence belonging to no registered project is a row of its own and not a
// gap in the picture. A day whose evidence is all unattributed would otherwise
// render as sittings nobody worked.
func TestEvidenceBelongingToNoProjectGetsItsOwnRow(t *testing.T) {
	e := source.Event{Start: at(9, 10), End: at(9, 10), Shape: source.Point, Source: "claude", Locator: "/nowhere"}
	day := assemble(t, derived(sitting(9, 5, 9, 15, 1)), []source.Event{e}, nil, nil)
	if len(day.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(day.Rows))
	}
	if day.Rows[0].Project != "" {
		t.Errorf("the unattributed row is named %q, want the empty name that means no project", day.Rows[0].Project)
	}
}

// Presence bounds sittings and never creates them, so a lit screen is not a
// project and must not become a row. Nor may an ignored source.
func TestPresenceAndIgnoredSourcesNeverBecomeRows(t *testing.T) {
	events := []source.Event{
		evidence("timetattle", "claude", 9, 10),
		presence("on", 8, 0, 12, 0),
		{Start: at(9, 0), End: at(11, 0), Shape: source.Interval, Source: "locked", State: "on"},
	}
	day := assemble(t, derived(sitting(9, 5, 9, 15, 1)), events, nil, nil)
	if len(day.Rows) != 1 {
		var names []string
		for _, r := range day.Rows {
			names = append(names, r.Project)
		}
		t.Fatalf("the rows are %v, want timetattle alone", names)
	}
}

// THE CLOSURE. This is the property the whole design rests on: confirming a day
// wholesale is not a vague "looks right", it is a complete accounting of every
// instant in the day. Without it a confirmation would only ever say where work
// WAS, every parameter that lengthens a sitting would fit better than every
// parameter that shortens one, and task PkY6I could not fit a gap threshold at
// all.

func TestConfirmingADayAssertsWorkOnEveryBlockAndNotWorkOnEverythingElse(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	if got := render(spansOf(plan.Assertions, adjustments.Engaged)); got != "09:00–11:00 14:00–16:00" {
		t.Errorf("the plan asserts work over %s, want the two blocks", got)
	}
	// 05:00 to the first block, between the blocks, and the last block to
	// 05:00 the next morning. Three, not zero.
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–09:00 11:00–14:00 16:00–05:00" {
		t.Errorf("the plan asserts not-work over %s, want the three gaps", got)
	}
}

func TestTheAssertionsOfAConfirmedDayCoverItExactlyAndOverlapNowhere(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	var all []timespan.Span
	for _, a := range plan.Assertions {
		if a.Assertion == adjustments.Engaged || a.Assertion == adjustments.NotEngaged {
			all = append(all, timespan.Span{Start: a.Start, End: a.End})
		}
	}
	if got, want := timespan.Total(all), day.End.Sub(day.Start); got != want {
		t.Errorf("the assertions cover %v of a %v day; a confirmation that leaves clock unsaid "+
			"cannot fit a gap threshold", got, want)
	}
	if got := timespan.Total(timespan.Union(all)); got != day.End.Sub(day.Start) {
		t.Errorf("the assertions union to %v, so two of them overlap", got)
	}
}

func TestConfirmingADayTheModelFoundNothingOnSaysTheWholeDayWasNotWork(t *testing.T) {
	day := assemble(t, derived(), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–05:00" {
		t.Errorf("the plan asserts not-work over %s, want the whole day", got)
	}
	if n := len(spansOf(plan.Assertions, adjustments.Engaged)); n != 0 {
		t.Errorf("%d assertions of work on a day the model found none", n)
	}
}

// A block flush against the start of the working day leaves no gap before it,
// and a zero-width assertion asserts nothing and is refused by the schema.
func TestNoAssertionIsManufacturedForAGapOfNoLength(t *testing.T) {
	day := assemble(t, derived(sitting(5, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "11:00–05:00" {
		t.Errorf("the plan asserts not-work over %s, want only the evening", got)
	}
}

func TestEveryAssertionAPlanProducesIsOneTheStoreWillAccept(t *testing.T) {
	day := assemble(t, derived(sitting(5, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(5, 0, 11, 0)},
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(14, 0, 16, 0),
			Reattribute: true, Project: "acorn"},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if len(plan.Assertions) == 0 {
		t.Fatal("the plan asserts nothing at all")
	}
	for _, a := range plan.Assertions {
		if err := a.Validate(); err != nil {
			t.Errorf("the plan produced an assertion the store would refuse: %v", err)
		}
	}
	if err := plan.Review.Validate(); err != nil {
		t.Errorf("the plan produced a review the store would refuse: %v", err)
	}
	for _, r := range plan.BlockReviews {
		if err := r.Validate(); err != nil {
			t.Errorf("the plan produced a block review the store would refuse: %v", err)
		}
	}
}

// DRILLING DOWN. Every reviewed day produces a closed, fine-grained accounting
// whether it was confirmed wholesale or walked block by block, so the LABELS
// are not biased toward rejected days. What differs is how hard the reviewer
// looked, and that is recorded as review rows rather than as free text: a
// drilled day says out loud which blocks were individually examined.

func TestDrillingDownRecordsAReviewPerBlockAndConfirmingWholesaleDoesNot(t *testing.T) {
	blocks := derived(sitting(9, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7))
	day := assemble(t, blocks, nil, nil, nil)

	wholesale, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm wholesale: %v", err)
	}
	if len(wholesale.BlockReviews) != 0 {
		t.Errorf("a wholesale confirmation claims %d blocks were individually examined",
			len(wholesale.BlockReviews))
	}

	drilled, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(9, 0, 11, 0)},
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(14, 0, 16, 0)},
	}))
	if err != nil {
		t.Fatalf("Confirm drilled: %v", err)
	}
	if len(drilled.BlockReviews) != 2 {
		t.Fatalf("a drilled day recorded %d block reviews, want 2 — without them a day walked "+
			"block by block and one waved through are indistinguishable", len(drilled.BlockReviews))
	}
	// The two produce the SAME assertions. That is the point: the calibration
	// labels are uniform over reviewed days and only the scrutiny differs.
	if a, b := render(spansOf(wholesale.Assertions, adjustments.Engaged)),
		render(spansOf(drilled.Assertions, adjustments.Engaged)); a != b {
		t.Errorf("drilling down changed the labels: %s against %s", b, a)
	}
}

// The day's own review is recorded LAST, so that the latest review overlapping
// a day is always the one that pronounces on the day as a whole. Without that
// ordering the day's state would be decided by whichever block happened to be
// examined last.
func TestTheDaysOwnReviewIsTheLastOneRecorded(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(9, 0, 11, 0)},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !plan.Review.Start.Equal(day.Start) || !plan.Review.End.Equal(day.End) {
		t.Errorf("the day's review covers %s, want the whole day",
			render([]timespan.Span{{Start: plan.Review.Start, End: plan.Review.End}}))
	}
	for _, r := range plan.BlockReviews {
		if r.End.After(plan.Review.End) || r.Start.Before(plan.Review.Start) {
			t.Errorf("a block review at %s falls outside the day it came from",
				render([]timespan.Span{{Start: r.Start, End: r.End}}))
		}
	}
}

// CORRECTIONS, and what each of them writes.

func TestABlockJudgedNotWorkIsAssertedNotEngagedAndTheDayStaysClosed(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: false, Span: span(9, 0, 11, 0)},
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(14, 0, 16, 0)},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Engaged)); got != "14:00–16:00" {
		t.Errorf("the plan asserts work over %s, want the afternoon alone", got)
	}
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–14:00 16:00–05:00" {
		t.Errorf("the plan asserts not-work over %s; the deleted block has to merge with "+
			"the gaps either side rather than leaving three separate assertions", got)
	}
}

func TestACorrectedExtentIsWhatGetsAsserted(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(8, 30, 10, 15)},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Engaged)); got != "08:30–10:15" {
		t.Errorf("the plan asserts work over %s, want the corrected extent", got)
	}
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–08:30 10:15–05:00" {
		t.Errorf("the plan asserts not-work over %s, want the complement of the corrected extent", got)
	}
}

func TestAReattributedBlockAssertsWhoItBelongedTo(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(9, 0, 11, 0),
			Reattribute: true, Project: "birch"},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Attributed)); got != "09:00–11:00" {
		t.Errorf("the plan attributes %s, want the block", got)
	}
	for _, a := range plan.Assertions {
		if a.Assertion == adjustments.Attributed && a.Project != "birch" {
			t.Errorf("the attribution names %q", a.Project)
		}
	}
}

// Reattributing to NOBODY is a thing a person can say, and it is not the same
// as saying nothing. The store has a separate assertion for it precisely so
// that "this was work and it was no client's" is recordable.
func TestReattributingToNoProjectAssertsUnattributedRatherThanNothing(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(9, 0, 11, 0), Reattribute: true},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Unattributed)); got != "09:00–11:00" {
		t.Errorf("the plan asserts unattributed over %s, want the block", got)
	}
}

// A WHOLESALE CONFIRMATION SAYS NOTHING ABOUT ATTRIBUTION, deliberately. A
// quarter of days carry two or more projects, so one 'attributed' assertion
// across a block would be false on those; and the parameters being fitted are
// all time-boundary parameters, which attribution is not one of.
func TestConfirmingADayWholesaleAssertsNothingAboutWhoItBelongedTo(t *testing.T) {
	events := []source.Event{evidence("timetattle", "claude", 9, 30)}
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), events, nil, nil)
	plan, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	for _, a := range plan.Assertions {
		if a.Assertion == adjustments.Attributed || a.Assertion == adjustments.Unattributed {
			t.Errorf("a wholesale confirmation asserted %s, which the reviewer never said", a.Assertion)
		}
	}
}

// WORK THE MODEL MISSED. Every other correction here can only ever agree that
// the machine over-claimed. This is the one that says it under-claimed, it is
// the sharpest available signal about the lead-in and about evidence coverage,
// and on a day with no blocks at all it is the ONLY thing a reviewer could say.

func TestWorkTheModelMissedEntirelyIsAssertedAsEngaged(t *testing.T) {
	day := assemble(t, derived(sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	a := answer(dayview.Wholesale, nil)
	a.Added = []timespan.Span{span(9, 0, 11, 0)}
	plan, err := day.Confirm(a)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Engaged)); got != "09:00–11:00 14:00–16:00" {
		t.Errorf("the plan asserts work over %s, want the added morning beside the block", got)
	}
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–09:00 11:00–14:00 16:00–05:00" {
		t.Errorf("the plan asserts not-work over %s; the added span has to come out of the "+
			"closure or the day contradicts itself", got)
	}
}

func TestAddedWorkOnADayTheModelFoundNothingInIsRecordable(t *testing.T) {
	day := assemble(t, derived(), nil, nil, nil)
	a := answer(dayview.Wholesale, nil)
	a.Added = []timespan.Span{span(9, 0, 17, 0)}
	plan, err := day.Confirm(a)
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Engaged)); got != "09:00–17:00" {
		t.Errorf("the plan asserts work over %s on a day the model found none", got)
	}
}

// A correction reaching past the working-day boundary is refused rather than
// clipped. Clipping would silently record something other than what the person
// said; the day on the other side of the boundary is where it belongs.
func TestACorrectionReachingOutsideTheDayIsRefusedRatherThanClipped(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	a := answer(dayview.Wholesale, nil)
	a.Added = []timespan.Span{span(2, 0, 6, 0)}
	if _, err := day.Confirm(a); err == nil {
		t.Error("a span reaching before the 05:00 boundary was accepted")
	}

	b := answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(9, 0, 30, 0)},
	})
	if _, err := day.Confirm(b); err == nil {
		t.Error("a block corrected past the end of the working day was accepted")
	}
}

// UNCERTAINTY, which is the criterion this whole file is judged against.

func TestCannotRememberRecordsThatSomebodyLookedAndAssertsNothing(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan := day.CannotRemember(answer(dayview.Wholesale, nil))
	if plan.Review.Verdict != adjustments.Unremembered {
		t.Errorf("the verdict is %q", plan.Review.Verdict)
	}
	if len(plan.Assertions) != 0 {
		t.Errorf("%d assertions came out of a day nobody could remember; the machine's answer "+
			"has to stand, marked unconfirmed", len(plan.Assertions))
	}
	if !plan.Review.Start.Equal(day.Start) || !plan.Review.End.Equal(day.End) {
		t.Error("the review does not cover the day it was recorded against")
	}
}

// An unremembered BLOCK punches a hole in the closure rather than being
// asserted either way. Filling it in with not-engaged would turn "I cannot
// remember" into "I was not working", which is the collapse this project has
// shipped three times.
func TestAnUnrememberedBlockLeavesAHoleRatherThanBeingAssertedEitherWay(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	plan, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Unremembered, Span: span(9, 0, 11, 0)},
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(14, 0, 16, 0)},
	}))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}

	var covered []timespan.Span
	for _, a := range plan.Assertions {
		if a.Assertion == adjustments.Engaged || a.Assertion == adjustments.NotEngaged {
			covered = append(covered, timespan.Span{Start: a.Start, End: a.End})
		}
	}
	hole := timespan.Subtract([]timespan.Span{{Start: day.Start, End: day.End}}, covered)
	if got := render(hole); got != "09:00–11:00" {
		t.Errorf("the unasserted part of the day is %s, want exactly the block nobody could "+
			"remember", got)
	}
	// And the block review says so, so the hole is explained rather than merely
	// empty.
	var unremembered int
	for _, r := range plan.BlockReviews {
		if r.Verdict == adjustments.Unremembered {
			unremembered++
		}
	}
	if unremembered != 1 {
		t.Errorf("%d block reviews record that the block could not be remembered", unremembered)
	}
}

// A correction cannot hang off a review that remembered nothing — the store's
// own trigger refuses it — so an unremembered block's assertions must not be
// parented to its own review.
func TestNoAssertionIsParentedToAReviewThatRememberedNothing(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan := day.CannotRemember(answer(dayview.Wholesale, nil))
	if len(plan.Assertions) != 0 {
		t.Fatalf("%d assertions", len(plan.Assertions))
	}
	if plan.Review.Verdict != adjustments.Unremembered {
		t.Fatal("setup: the verdict is not unremembered")
	}
}

func TestAPlanRefusesJudgementsThatDoNotLineUpWithTheBlocks(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(14, 0, 16, 0, 7)), nil, nil, nil)
	if _, err := day.Confirm(answer(dayview.Drilled, []dayview.Judgement{
		{Verdict: adjustments.Recalled, Engaged: true, Span: span(9, 0, 11, 0)},
	})); err == nil {
		t.Error("a drill-down covering one of two blocks was accepted, so a half-walked day " +
			"would be recorded as a complete accounting")
	}
}

// THE FOUR STATES. "I never looked" and "I looked and cannot remember" are
// different facts, and so are "I looked and agreed" and "I looked and
// corrected". Collapsing any pair of them loses something this project has
// already lost three times.

func stateOf(t *testing.T, reviews []adjustments.Review, adjs []adjustments.Adjustment) dayview.State {
	t.Helper()
	return assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, reviews, adjs).State()
}

func reviewOf(v adjustments.Verdict, id int64, minute int) adjustments.Review {
	return adjustments.Review{
		ID: id, Start: at(5, 0), End: at(29, 0), Verdict: v,
		Origin: "a-person", RecordedAt: time.Date(2026, 8, 2, 9, minute, 0, 0, time.UTC),
	}
}

func assertionOf(a adjustments.Assertion, id int64, fromHour, fromMin, toHour, toMin int) adjustments.Adjustment {
	return adjustments.Adjustment{
		ID: id, Start: at(fromHour, fromMin), End: at(toHour, toMin), Assertion: a,
		Origin: "a-person", RecordedAt: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
	}
}

func TestADayNobodyHasOpenedIsUnexamined(t *testing.T) {
	if got := stateOf(t, nil, nil); got != dayview.Unexamined {
		t.Errorf("a day with no review reads as %q, want unexamined", got)
	}
}

func TestADayConfirmedWithoutCorrectionsIsAgreed(t *testing.T) {
	reviews := []adjustments.Review{reviewOf(adjustments.Recalled, 1, 0)}
	adjs := []adjustments.Adjustment{
		assertionOf(adjustments.Engaged, 1, 9, 0, 11, 0),
		assertionOf(adjustments.NotEngaged, 2, 5, 0, 9, 0),
		assertionOf(adjustments.NotEngaged, 3, 11, 0, 29, 0),
	}
	if got := stateOf(t, reviews, adjs); got != dayview.Agreed {
		t.Errorf("a day whose corrections reproduce the machine's answer reads as %q, want agreed", got)
	}
}

func TestADayWhoseCorrectionsChangeTheAnswerIsCorrected(t *testing.T) {
	reviews := []adjustments.Review{reviewOf(adjustments.Recalled, 1, 0)}
	adjs := []adjustments.Adjustment{
		assertionOf(adjustments.Engaged, 1, 9, 0, 10, 0),
		assertionOf(adjustments.NotEngaged, 2, 5, 0, 9, 0),
		assertionOf(adjustments.NotEngaged, 3, 10, 0, 29, 0),
	}
	if got := stateOf(t, reviews, adjs); got != dayview.Corrected {
		t.Errorf("a day trimmed by an hour reads as %q, want corrected", got)
	}
}

func TestADayLookedAtAndNotRecalledIsItsOwnState(t *testing.T) {
	reviews := []adjustments.Review{reviewOf(adjustments.Unremembered, 1, 0)}
	if got := stateOf(t, reviews, nil); got != dayview.Unremembered {
		t.Errorf("a day nobody could recall reads as %q", got)
	}
	if dayview.Unremembered == dayview.Unexamined {
		t.Error("the two states this project keeps collapsing are the same value")
	}
}

// The latest review is the one that speaks for the day. A person who could not
// remember a day, looked again with a calendar open and confirmed it, has
// confirmed it.
func TestTheLatestReviewIsTheOneThatSpeaksForTheDay(t *testing.T) {
	reviews := []adjustments.Review{
		reviewOf(adjustments.Unremembered, 1, 0),
		reviewOf(adjustments.Recalled, 2, 30),
	}
	adjs := []adjustments.Adjustment{
		assertionOf(adjustments.Engaged, 1, 9, 0, 11, 0),
		assertionOf(adjustments.NotEngaged, 2, 5, 0, 9, 0),
		assertionOf(adjustments.NotEngaged, 3, 11, 0, 29, 0),
	}
	if got := stateOf(t, reviews, adjs); got != dayview.Agreed {
		t.Errorf("the day reads as %q, want the later review to win", got)
	}
}

// A block review from a drilled day must not decide the day's state. Recording
// the day's own review last is what guarantees it, and this is the test that
// makes the ordering a decision rather than an accident.
func TestABlockReviewDoesNotSpeakForTheWholeDay(t *testing.T) {
	blockReview := adjustments.Review{
		ID: 1, Start: at(9, 0), End: at(11, 0), Verdict: adjustments.Unremembered,
		Origin: "a-person", RecordedAt: time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC),
	}
	dayReview := reviewOf(adjustments.Recalled, 2, 0)
	adjs := []adjustments.Adjustment{
		assertionOf(adjustments.Engaged, 1, 9, 0, 11, 0),
		assertionOf(adjustments.NotEngaged, 2, 5, 0, 9, 0),
		assertionOf(adjustments.NotEngaged, 3, 11, 0, 29, 0),
	}
	if got := stateOf(t, []adjustments.Review{blockReview, dayReview}, adjs); got != dayview.Agreed {
		t.Errorf("the day reads as %q; a review of one block spoke for the whole day", got)
	}
}

// A DAY THAT HAS NOT FINISHED YET.
//
// Reviewing today is the common case, not an edge case: recall is best on the
// day itself, and the review queue offers the most recent days first. But a
// confirmation is a CLOSED accounting of the whole working day, and the gap
// running from the last block to the day's end is asserted not-engaged like any
// other. Reviewed at lunchtime, that gap is an assertion about hours nobody
// could have looked at.
//
// It is not hypothetical. Measured on the live store 2026-08-02: exactly one
// review row and exactly one adjustment reach past their own recorded_at, both
// on 2026-08-01, both by 15h36m, and the resulting not-engaged assertion now
// contradicts 2.194h of derived sitting that happened after the review was
// recorded. The tables are append-only, so that row is permanent.
//
// THE ANSWER IS THE FOURTH STATE, not a shorter day. The hours after the moment
// of review get NO ROW AT ALL, which is what "nobody has looked yet" already
// means everywhere else in this package. Clipping silently would record a
// partial day that a later reader cannot tell from a whole one.

// theMoment is lunchtime on the reference working day, used as the instant a
// review is recorded so that the rest of the day is still to come.
var theMoment = at(13, 0)

// unfinished is an answer recorded at theMoment rather than at real wall-clock
// time, which is what every other test here uses and is why they are unaffected.
func unfinished(mode dayview.Mode, js []dayview.Judgement) dayview.Answer {
	a := answer(mode, js)
	a.At = theMoment
	return a
}

func TestConfirmingADayThatHasNotFinishedAssertsNothingAboutTheHoursStillToCome(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(12, 0, 12, 30, 7)), nil, nil, nil)
	plan, err := day.Confirm(unfinished(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	for _, a := range plan.Assertions {
		if a.End.After(theMoment) {
			t.Errorf("a confirmation recorded at %s asserts %s over %s–%s, which had not happened yet",
				theMoment.Format("15:04"), a.Assertion,
				a.Start.Format("15:04"), a.End.Format("15:04"))
		}
	}
	// The two gaps BEFORE the moment of review, and nothing after it. The
	// evening is not asserted empty; it is left unexamined.
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–09:00 11:00–12:00 12:30–13:00" {
		t.Errorf("the plan asserts not-work over %s, want only the gaps that had elapsed", got)
	}
}

func TestTheReviewOfAnUnfinishedDaySaysItLookedOnlyAtWhatHadHappened(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(unfinished(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !plan.Review.End.Equal(theMoment) {
		t.Errorf("the review claims to have examined up to %s, having been recorded at %s; "+
			"a review row is the record of what was LOOKED AT",
			plan.Review.End.Format("15:04"), theMoment.Format("15:04"))
	}
	if !plan.Review.Start.Equal(day.Start) {
		t.Errorf("the review starts at %s, want the start of the working day %s",
			plan.Review.Start.Format("15:04"), day.Start.Format("15:04"))
	}
}

func TestLookingAtAnUnfinishedDayAndNotRecallingItAlsoCoversOnlyWhatHappened(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan := day.CannotRemember(unfinished(dayview.Wholesale, nil))
	if !plan.Review.End.Equal(theMoment) {
		t.Errorf("the review claims somebody looked at the day up to %s, having been recorded "+
			"at %s", plan.Review.End.Format("15:04"), theMoment.Format("15:04"))
	}
}

// The one way a reviewer could still assert about the future is by hand, and it
// is refused rather than clipped — for the same reason a correction reaching
// outside the working day is refused.
func TestWorkAddedAfterTheMomentOfReviewIsRefusedRatherThanClipped(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	a := unfinished(dayview.Wholesale, nil)
	a.Added = []timespan.Span{span(12, 0, 15, 0)}
	_, err := day.Confirm(a)
	if err == nil {
		t.Fatal("work was added over a stretch that had not happened yet, and it was accepted")
	}
	if !strings.Contains(err.Error(), "15:00") {
		t.Errorf("the refusal does not name the span that reaches into the future: %v", err)
	}
}

// A day whose whole span was examined and a day examined only as far as
// lunchtime are different facts, and the review queue is the reader that would
// otherwise conflate them: without this, confirming today would take it off the
// queue for ever and the evening would never be reviewed at all.
func TestADayExaminedOnlyAsFarAsLunchtimeIsNotAFullyReviewedDay(t *testing.T) {
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil, nil, nil)
	plan, err := day.Confirm(unfinished(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	partly := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil,
		[]adjustments.Review{plan.Review}, plan.Assertions)

	if !partly.Reviewed() {
		t.Error("somebody looked at this day and it reports that nobody has")
	}
	if partly.ReviewedInFull() {
		t.Error("a day examined only as far as lunchtime reports that the whole of it was " +
			"examined, so the review queue will never offer the evening")
	}

	whole, err := day.Confirm(answer(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	fully := assemble(t, derived(sitting(9, 0, 11, 0, 5)), nil,
		[]adjustments.Review{whole.Review}, whole.Assertions)
	if !fully.ReviewedInFull() {
		t.Error("a day examined end to end reports that it was not")
	}
}

// THE LAST BLOCK OF AN UNFINISHED DAY REACHES PAST THE MOMENT OF REVIEW, and
// that is not the same defect as asserting over the evening.
//
// A sitting runs to its last observation PLUS the tail-out, so under the
// shipped 5m a block drawn from an event at 12:59 ends at 13:04, and a reviewer
// confirming at 13:00 is endorsing it. Refusing that would make same-day review
// impossible on any day whose work reaches the present moment, which is every
// day somebody reviews while working. Endorsing it asserts an inference about
// work already observed rather than a claim about unobserved hours.
//
// So the examined extent stretches to cover what was asserted, and the day's
// own review says so rather than ending before the last thing it endorsed.
func TestABlockRunningPastTheMomentOfReviewIsStillConfirmedAndTheReviewCoversIt(t *testing.T) {
	// Ends at 13:04, four minutes after the review at 13:00.
	day := assemble(t, derived(sitting(9, 0, 11, 0, 5), sitting(12, 40, 13, 4, 7)), nil, nil, nil)
	plan, err := day.Confirm(unfinished(dayview.Wholesale, nil))
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if got := render(spansOf(plan.Assertions, adjustments.Engaged)); got != "09:00–11:00 12:40–13:04" {
		t.Errorf("the plan asserts work over %s, want both blocks including the one still running", got)
	}
	if !plan.Review.End.Equal(at(13, 4)) {
		t.Errorf("the review covers up to %s, but it endorsed a block ending at 13:04; a review "+
			"must cover everything it asserted", plan.Review.End.Format("15:04"))
	}
	// And still nothing about the evening.
	if got := render(spansOf(plan.Assertions, adjustments.NotEngaged)); got != "05:00–09:00 11:00–12:40" {
		t.Errorf("the plan asserts not-work over %s, want only the elapsed gaps", got)
	}
}
