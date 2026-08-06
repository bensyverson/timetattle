package dayview_test

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// WHAT A ROW'S EVIDENCE IS WORTH.
//
// Task KrfMh. After the machine-generated file activity was excluded, 88 of the
// 453 surviving project-days rest on nothing but file modification times, and
// they printed exactly like a day with a transcript underneath them. These are
// the constructed cases; realdata_test.go carries the live one.

// stamp is a walked file modification time: the observation that fabricated
// 126.2h of presence before 4LQHd, and the one a person still has to judge on
// the 88 days that survived it.
func stamp(project string, hour, minute int) source.Event {
	return evidence(project, "fs-observations", hour, minute)
}

// act is an observation that records something a person did.
func transcript(project string, hour, minute int) source.Event {
	return evidence(project, "claude-transcripts", hour, minute)
}

func strengthOf(t *testing.T, day dayview.Day, project string) dayview.Strength {
	t.Helper()
	for _, r := range day.Rows {
		if r.Project == project {
			return r.Strength
		}
	}
	t.Fatalf("no row for %q; the mark must never remove one", project)
	return ""
}

func TestARowRestingOnlyOnFileTimestampsIsWeak(t *testing.T) {
	events := []source.Event{stamp("Woodcase", 20, 10), stamp("Woodcase", 20, 25)}
	day := assemble(t, derived(sitting(20, 5, 20, 30, 2)), events, nil, nil)
	if got := strengthOf(t, day, "Woodcase"); got != dayview.Weak {
		t.Errorf("Woodcase reads %q, want %q; every observation behind it is a file's mtime",
			got, dayview.Weak)
	}
}

func TestARowWithSomethingSomebodyDidIsAttested(t *testing.T) {
	events := []source.Event{transcript("acorn", 9, 10), transcript("acorn", 9, 20)}
	day := assemble(t, derived(sitting(9, 5, 9, 25, 2)), events, nil, nil)
	if got := strengthOf(t, day, "acorn"); got != dayview.Attested {
		t.Errorf("acorn reads %q, want %q", got, dayview.Attested)
	}
}

// ONE act is enough. Weakness is a statement about the whole of a row's
// evidence, so a day of file saves with a single commit in it is not weak — the
// commit says somebody was there, and the saves are then ordinary work.
func TestOneRecordedActAmongFileTimestampsIsEnoughToAttestTheRow(t *testing.T) {
	events := []source.Event{
		stamp("acorn", 9, 10), stamp("acorn", 9, 20), stamp("acorn", 9, 40),
		evidence("acorn", "gitlog-commits", 9, 30),
	}
	day := assemble(t, derived(sitting(9, 5, 9, 45, 4)), events, nil, nil)
	if got := strengthOf(t, day, "acorn"); got != dayview.Attested {
		t.Errorf("acorn reads %q, want %q; a commit is somebody doing something", got, dayview.Attested)
	}
}

// The unit is the project row WITHIN a day, and this is why. A quarter of days
// carry two or more projects, so a day can be strong for one and weak for
// another at the same time; a whole-day verdict would misrepresent both.
func TestADayCanBeStrongForOneProjectAndWeakForAnotherAtOnce(t *testing.T) {
	events := []source.Event{
		transcript("acorn", 9, 10), transcript("acorn", 9, 20),
		stamp("Woodcase", 9, 15),
	}
	day := assemble(t, derived(sitting(9, 5, 9, 25, 3)), events, nil, nil)
	if got := strengthOf(t, day, "acorn"); got != dayview.Attested {
		t.Errorf("acorn reads %q, want %q", got, dayview.Attested)
	}
	if got := strengthOf(t, day, "Woodcase"); got != dayview.Weak {
		t.Errorf("Woodcase reads %q, want %q on the very same day", got, dayview.Weak)
	}
}

// THIS IS A MARK AND NEVER A FILTER. Woodcase 2026-03-21 is one file
// modification time that Ben judged by hand and kept, and the whole of task
// KrfMh rests on such a row surviving with everything it had.
func TestAWeakRowKeepsItsPlaceAndEverythingItClaims(t *testing.T) {
	events := []source.Event{stamp("Woodcase", 20, 10)}
	block := sitting(20, 5, 20, 15, 1)
	day := assemble(t, derived(block), events, nil, nil)
	if len(day.Rows) != 1 {
		t.Fatalf("%d rows, want 1; marking a row weak removed it", len(day.Rows))
	}
	r := day.Rows[0]
	if r.Strength != dayview.Weak {
		t.Fatalf("the row reads %q, want %q", r.Strength, dayview.Weak)
	}
	if r.Events != 1 {
		t.Errorf("the row rests on %d observations, want 1", r.Events)
	}
	if timespan.Total(r.Active) <= 0 {
		t.Errorf("the weak row claims no clock at all: %s", render(r.Active))
	}
	if len(r.Observed) != 1 {
		t.Errorf("the weak row lost its observation: %s", render(r.Observed))
	}
	if day.Engaged() != block.Duration() {
		t.Errorf("the day totals %v, want the block's %v; a mark may not move an hour",
			day.Engaged(), block.Duration())
	}
	// And the block is untouched: it still names the project and still rests on
	// the observation count the derivation stored.
	if len(day.Blocks) != 1 || len(day.Blocks[0].Projects) != 1 {
		t.Errorf("the block no longer names its project: %v", day.Blocks)
	}
}

// Evidence in the MARGIN either side of the day is read so that a project's own
// clustering is not truncated at the boundary, and it must not strengthen a
// row. A transcript at four in the morning of the next working day says nothing
// about the evening being judged.
func TestAnActOutsideTheDayDoesNotAttestARowInsideIt(t *testing.T) {
	events := []source.Event{
		stamp("Woodcase", 20, 10),
		transcript("Woodcase", 29, 30), // 05:30 the next working day
	}
	day := assemble(t, derived(sitting(20, 5, 20, 15, 1)), events, nil, nil)
	if got := strengthOf(t, day, "Woodcase"); got != dayview.Weak {
		t.Errorf("Woodcase reads %q, want %q; the act it rests on falls in the next working day",
			got, dayview.Weak)
	}
}

// THREE STATES, AND THIS IS THE THIRD. The review queue assembles days without
// reading their evidence, on purpose: a listing needs the blocks and the review
// state, and reading a day's observations costs a query per day. A day
// assembled that way has no rows, and reporting it as a day nothing attributable
// was seen on is this project's cardinal bug — "nothing was recorded" against
// "nothing was there".
func TestADayWhoseEvidenceWasNotReadSaysSoRatherThanReportingNone(t *testing.T) {
	skeleton, err := dayview.Skeleton(cal, theDay, derived(sitting(9, 0, 11, 0, 4)), nil, nil)
	if err != nil {
		t.Fatalf("Skeleton: %v", err)
	}
	if skeleton.Evidence {
		t.Error("a day assembled without its evidence claims the evidence was read")
	}
	if len(skeleton.Blocks) != 1 {
		t.Errorf("%d blocks, want the derivation's 1", len(skeleton.Blocks))
	}
	full := assemble(t, derived(sitting(9, 0, 11, 0, 4)), nil, nil, nil)
	if !full.Evidence {
		t.Error("a day whose evidence was read and held nothing attributable claims it was " +
			"not read; those are different facts")
	}
}

// Weigh is what the queue asks, and it must agree with the rows exactly — one
// rule, two entry points. It does not cluster anything, because the queue
// cannot afford to and because strength is a property of the evidence rather
// than of where the clustering put it.
func TestWeighAgreesWithTheRowsWithoutClusteringAnything(t *testing.T) {
	events := []source.Event{
		transcript("acorn", 9, 10), transcript("acorn", 9, 20),
		stamp("Woodcase", 9, 15),
		stamp("Woodcase", 29, 30), // the next working day, and not this row's business
	}
	day := assemble(t, derived(sitting(9, 5, 9, 25, 3)), events, nil, nil)
	weighed := dayview.Weigh(cal, theDay, params, events)
	if len(weighed) != len(day.Rows) {
		t.Fatalf("Weigh found %d projects and the day has %d rows", len(weighed), len(day.Rows))
	}
	for _, r := range day.Rows {
		if weighed[r.Project] != r.Strength {
			t.Errorf("%s: Weigh says %q and the row says %q", r.Name(), weighed[r.Project], r.Strength)
		}
	}
}

// Presence and ignored sources are never rows, so they can never make one weak
// or attest one either. A backlight sample is not somebody doing something.
func TestPresenceReadingsDoNotAttestARow(t *testing.T) {
	events := []source.Event{
		stamp("Woodcase", 9, 10),
		presence("on", 9, 0, 11, 0),
	}
	day := assemble(t, derived(sitting(9, 5, 9, 15, 1)), events, nil, nil)
	if len(day.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(day.Rows))
	}
	if got := strengthOf(t, day, "Woodcase"); got != dayview.Weak {
		t.Errorf("Woodcase reads %q, want %q; the backlight was lit and that is not an act", got, dayview.Weak)
	}
}

// A row nobody owns is judged like any other. It is evidence, it is drawn, and
// whether anything attests it is exactly as answerable.
func TestTheUnattributedRowIsWeighedLikeAnyOther(t *testing.T) {
	e := source.Event{
		Start: at(9, 10), End: at(9, 10), Shape: source.Point,
		Source: "fs-observations", Locator: "/nowhere",
	}
	day := assemble(t, derived(sitting(9, 5, 9, 15, 1)), []source.Event{e}, nil, nil)
	if len(day.Rows) != 1 {
		t.Fatalf("%d rows, want 1", len(day.Rows))
	}
	if day.Rows[0].Strength != dayview.Weak {
		t.Errorf("the unattributed row reads %q, want %q", day.Rows[0].Strength, dayview.Weak)
	}
}

// Weak is not a verdict about the clock. A weak row and an attested row of the
// same length claim the same hours; the mark says what the claim rests on.
func TestAMarkChangesNoDurationAnywhere(t *testing.T) {
	weak := assemble(t, derived(sitting(9, 5, 10, 5, 2)),
		[]source.Event{stamp("x", 9, 10), stamp("x", 10, 0)}, nil, nil)
	strong := assemble(t, derived(sitting(9, 5, 10, 5, 2)),
		[]source.Event{transcript("x", 9, 10), transcript("x", 10, 0)}, nil, nil)
	if weak.Engaged() != strong.Engaged() {
		t.Errorf("engaged %v against %v", weak.Engaged(), strong.Engaged())
	}
	if timespan.Total(weak.Rows[0].Active) != timespan.Total(strong.Rows[0].Active) {
		t.Errorf("the weak row claims %v and the attested one %v",
			timespan.Total(weak.Rows[0].Active), timespan.Total(strong.Rows[0].Active))
	}
	if weak.Rows[0].Strength == strong.Rows[0].Strength {
		t.Error("the two rows read the same, which is the bug this task exists to fix")
	}
}
