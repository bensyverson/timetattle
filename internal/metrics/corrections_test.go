package metrics_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/source"
)

// A PERSON'S ATTRIBUTION ASSERTIONS, READ.
//
// The store has been able to record "that stretch was birch, not acorn" since
// migration 0008, and the review command has been able to produce one from the
// block walk for as long. Until this file existed, nothing read either: the
// correction was written faithfully, survived every re-derivation exactly as
// promised, and changed no figure anybody could see. That is worse than the
// feature not existing, because the silence is indistinguishable from agreement.
//
// Everything here is about ATTRIBUTION only. An engaged or not-engaged
// assertion moves the capacity denominator and is deliberately NOT applied by
// this package — see Corrected's doc for why that is a different decision
// needing a different case made for it.

// said builds one resolved attribution piece, the shape adjustments.Fold hands
// out. An empty project is the assertion that the span belonged to NOBODY,
// which is a thing a person can say; the third state, nobody having said, is
// the absence of a piece and never an empty one.
func said(fromH, fromM, toH, toM int, project string) adjustments.Attribution {
	return adjustments.Attribution{
		Span:    adjustments.Span{Start: at(fromH, fromM), End: at(toH, toM)},
		Project: project,
	}
}

// oneSitting is a project worked steadily from 10:00 to 10:30. The saves are
// ten minutes apart so that no gap reaches the fifteen-minute threshold and the
// whole of it clusters as ONE sitting, 09:55-10:35 once the lead-in and
// tail-out are added. Two points half an hour apart would be two sittings with
// twenty minutes of presence between them, and an assertion laid across the
// pair would only ever reach the ten minutes at either end.
func oneSitting(project string) []source.Event {
	var out []source.Event
	for _, m := range []int{0, 10, 20, 30} {
		out = append(out, point(at(10, m), project))
	}
	return out
}

func corrected(t *testing.T, events []source.Event, log []adjustments.Attribution) metrics.Report {
	t.Helper()
	r, err := metrics.Corrected(events, testParams(), log)
	if err != nil {
		t.Fatalf("Corrected: %v", err)
	}
	return r
}

// THE FIGURE MOVES. This is the whole of criterion XGE: a correction somebody
// recorded has to reach the report, or recording it was theatre.
func TestAnAttributionAssertionMovesEngagedTimeToTheProjectSomebodyNamed(t *testing.T) {
	// acorn's own evidence runs 09:55–10:35 after the lead-in and tail-out.
	events := oneSitting("acorn")
	before := compute(t, events)
	if got := projectOf(t, before, "acorn").Engaged.Total; got != 40*time.Minute {
		t.Fatalf("the machine credits acorn %v, want 40m; the fixture is not what this test thinks", got)
	}

	after := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 30, "birch")})

	if got := projectOf(t, after, "acorn").Engaged.Total; got != 10*time.Minute {
		t.Errorf("acorn keeps %v after half an hour of it was asserted to be birch's, want 10m", got)
	}
	birch := projectOf(t, after, "birch")
	if got := birch.Engaged.Total; got != 30*time.Minute {
		t.Errorf("birch is credited %v, want the 30m somebody said was theirs", got)
	}
	// And the hours birch holds are named as a person's assertion rather than
	// quietly joining the evidence. Nothing observed birch at all.
	if birch.Asserted != 30*time.Minute {
		t.Errorf("birch holds %v of asserted time against %v engaged; the report has to say which "+
			"of its hours came from a person", birch.Asserted, birch.Engaged.Total)
	}
	if birch.Engaged.Observed != 0 || birch.Attested != 0 || birch.Weak != 0 {
		t.Errorf("birch reports %v observed, %v attested and %v weak; an assertion is not an "+
			"observation and must not be counted as one",
			birch.Engaged.Observed, birch.Attested, birch.Weak)
	}
}

// PRESENCE IS UNTOUCHED. An attribution assertion says WHOSE an hour was, never
// that there was one. Letting it create or destroy presence would make the
// capacity denominator a function of how much reviewing had been done.
func TestAnAttributionAssertionNeverMovesPresence(t *testing.T) {
	events := oneSitting("acorn")
	before := compute(t, events)
	after := corrected(t, events, []adjustments.Attribution{
		said(10, 0, 10, 30, "birch"),
		said(14, 0, 15, 0, "birch"), // wholly outside any sitting
	})
	if before.Presence != after.Presence {
		t.Errorf("presence went from %+v to %+v under an attribution correction", before.Presence, after.Presence)
	}
	if !reflect.DeepEqual(before.Sittings, after.Sittings) {
		t.Error("the sitting length distribution changed under an attribution correction")
	}
}

// THE THIRD FIGURE NOBODY WOULD THINK TO PRINT. An assertion over clock no
// sitting covers moves nothing, and a reader must be told that rather than left
// to conclude their correction was read and had no effect.
func TestAnAssertionOverClockNoSittingCoversMovesNothingAndSaysSo(t *testing.T) {
	events := oneSitting("acorn")
	r := corrected(t, events, []adjustments.Attribution{said(14, 0, 15, 0, "birch")})

	if r.Corrections.Outside != time.Hour {
		t.Errorf("the report says %v of the correction fell outside every sitting, want 1h",
			r.Corrections.Outside)
	}
	if r.Corrections.Attributed != 0 {
		t.Errorf("%v was reported as moved to a project by a correction that reached no presence",
			r.Corrections.Attributed)
	}
	if _, ok := r.Project("birch"); ok {
		t.Error("birch appears in the report on the strength of an assertion covering no presence " +
			"at all, which would be a row of zero hours reading as a real one")
	}
}

// AGREEMENT IS RECORDED AND CHANGES NOTHING. A person confirming what the
// machine already said is a real observation about the model, and it is a third
// state beside "moved the figure" and "nobody said anything" — but not one
// instant of the report may move because of it.
func TestAnAssertionAgreeingWithTheMachineChangesNoFigure(t *testing.T) {
	events := oneSitting("acorn")
	before := compute(t, events)
	after := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 30, "acorn")})

	if after.Corrections.Agreed != 30*time.Minute {
		t.Errorf("the report says %v was agreed with, want the 30m the machine had already "+
			"credited acorn alone", after.Corrections.Agreed)
	}
	if after.Corrections.Attributed != 0 || after.Corrections.Unattributed != 0 {
		t.Errorf("an agreeing assertion reported %v attributed and %v unattributed",
			after.Corrections.Attributed, after.Corrections.Unattributed)
	}

	// Every figure, not merely the totals. Corrections itself is expected to
	// differ, so it is set aside and everything else compared whole.
	after.Corrections = metrics.Corrections{}
	if !reflect.DeepEqual(before, after) {
		t.Error("an assertion the machine already agreed with changed the report; the corrected " +
			"answer has to be reconstructed faithfully or every figure it moves is suspect")
	}
}

// The cheap path, and the state 100% of days in the live store are in today.
func TestAReportWithNoAssertionsIsTheMachineAnswerExactly(t *testing.T) {
	events := append(oneSitting("acorn"), point(at(10, 15), "birch"))
	before := compute(t, events)
	after := corrected(t, events, nil)
	if !reflect.DeepEqual(before, after) {
		t.Error("a report with no corrections in it differs from the machine's own answer")
	}
	if after.Corrections != (metrics.Corrections{}) {
		t.Errorf("a report with no corrections says %+v", after.Corrections)
	}
}

// ASSERTED-UNATTRIBUTED IS NOT BRIDGED SILENCE, AND NOT AN UNREGISTERED
// DIRECTORY. "I looked, and that hour belonged to nobody" is an observation.
// Filing it under any of the three reasons the machine has for failing to
// attribute something would say the registry needs fixing when it does not.
func TestAssertingASpanBelongsToNoProjectGivesUnattributedItsOwnReason(t *testing.T) {
	events := oneSitting("acorn")
	r := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 30, "")})

	if got := r.Unattributed.Asserted; got != 30*time.Minute {
		t.Errorf("%v is filed as asserted-unattributed, want 30m", got)
	}
	if got := r.Unattributed.Presence.Total; got < 30*time.Minute {
		t.Errorf("unattributed presence is %v, which does not even hold the half hour somebody "+
			"said belonged to nobody", got)
	}
	if r.Corrections.Unattributed != 30*time.Minute {
		t.Errorf("the report says %v was moved to no project", r.Corrections.Unattributed)
	}
	if got := projectOf(t, r, "acorn").Engaged.Total; got != 10*time.Minute {
		t.Errorf("acorn still holds %v of the span asserted to be nobody's, want 10m", got)
	}
	var asserted time.Duration
	for _, s := range r.Segments {
		if s.Unattributed == metrics.ReasonAsserted {
			asserted += s.Duration()
		}
	}
	if asserted != 30*time.Minute {
		t.Errorf("the segments carry %v under the asserted reason against %v in the total; the "+
			"partition every figure is summed from disagrees with the figure", asserted,
			r.Unattributed.Asserted)
	}
}

// THE ASSERTION THAT MOVES NO SPAN AT ALL, and therefore contributes no
// boundary through either level of attribution.
//
// Confirming that an already-unattributed stretch belonged to nobody takes
// clock away from no project, so nothing in the project or client answer
// changes shape and neither contributes a cut where the assertion begins or
// ends. Without a cut of its own the segment straddles the edge, cursor.covers
// reports false for the whole of it, and the reason silently reverts to
// whichever of the machine's three the rest of the segment carried — an
// unregistered directory somebody is then sent to go and register, for an hour
// they have already accounted for. It is the exact shape of silent wrongness
// this package's own segmentation check exists for, and no total is off by a
// second when it happens.
func TestAnAssertionInsideAlreadyUnattributedPresenceStillBringsItsOwnEdges(t *testing.T) {
	// Two unregistered observations ten minutes apart: one sitting, 09:55–10:15,
	// which no project is active in and which the machine files as unregistered.
	events := []source.Event{unregistered(at(10, 0)), unregistered(at(10, 10))}
	base := compute(t, events)
	if base.Unattributed.Unregistered != 20*time.Minute {
		t.Fatalf("the fixture files %v as unregistered, want the whole 20m sitting",
			base.Unattributed.Unregistered)
	}

	// Five minutes strictly inside it, touching neither end of the sitting.
	r := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 5, "")})

	if r.Unattributed.Asserted != 5*time.Minute {
		t.Errorf("%v is filed as asserted-unattributed, want the 5m somebody accounted for",
			r.Unattributed.Asserted)
	}
	if r.Unattributed.Unregistered != 15*time.Minute {
		t.Errorf("%v is still filed as an unregistered directory, want 15m: the five minutes "+
			"somebody has already explained are no longer a gap in the registry",
			r.Unattributed.Unregistered)
	}
	if r.Corrections.Agreed != 5*time.Minute {
		t.Errorf("the report says %v was agreed with; the machine already had that stretch "+
			"belonging to nobody, so nothing moved", r.Corrections.Agreed)
	}
}

// A person may name a project no evidence in the corpus mentions. The row that
// results is honest — zero observations, and every hour in it a person's word —
// and refusing to create it would silently discard the correction.
func TestAnAssertionCanNameAProjectNoEvidenceMentions(t *testing.T) {
	events := oneSitting("acorn")
	r := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 30, "a-meeting")})

	p := projectOf(t, r, "a-meeting")
	if p.Events != 0 {
		t.Errorf("the row rests on %d observations, want none: nothing in the corpus mentions it", p.Events)
	}
	if p.Engaged.Total != 30*time.Minute || p.Asserted != 30*time.Minute {
		t.Errorf("the row is %v engaged of which %v asserted, want 30m of both",
			p.Engaged.Total, p.Asserted)
	}
	if p.Unaccounted != 30*time.Minute {
		t.Errorf("%v of the row is unaccounted, want all 30m: no observation is responsible for "+
			"any of it", p.Unaccounted)
	}
}

// EVERY INSTANT ASSERTED IS ACCOUNTED FOR, under exactly one of the four
// headings. A correction that vanished between the log and the report would
// otherwise be invisible — which is the failure this whole task exists to fix,
// arriving one level further in.
func TestEveryInstantAssertedIsAccountedForExactlyOnce(t *testing.T) {
	events := append(oneSitting("acorn"), point(at(10, 35), "birch"))
	log := []adjustments.Attribution{
		said(9, 0, 9, 30, "birch"),    // wholly outside presence
		said(10, 0, 10, 20, "birch"),  // takes clock from acorn
		said(10, 20, 10, 30, ""),      // moves clock to nobody
		said(10, 30, 10, 35, "acorn"), // agrees with the machine
	}
	r := corrected(t, events, log)

	var asserted time.Duration
	for _, a := range log {
		asserted += a.End.Sub(a.Start)
	}
	c := r.Corrections
	if got := c.Attributed + c.Unattributed + c.Agreed + c.Outside; got != asserted {
		t.Errorf("the four headings account for %v of the %v asserted: %+v", got, asserted, c)
	}
	if c.Assertions != len(log) {
		t.Errorf("the report counts %d assertions against %d resolved pieces", c.Assertions, len(log))
	}
}

// The roll-up follows the project, and it is the union rather than a sum — the
// same property the uncorrected client level rests on. A correction that moved
// project hours and left the client table alone would produce two plausible
// figures that nobody had subtracted.
func TestReattributingWithinAClientMovesTheRollUpWithIt(t *testing.T) {
	events := []source.Event{
		clientPoint(at(10, 0), "atelier", "loom"),
		clientPoint(at(10, 10), "atelier", "loom"),
		clientPoint(at(10, 20), "atelier", "loom"),
		clientPoint(at(10, 30), "atelier", "loom"),
	}
	before := compute(t, events)
	after := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 30, "kiln")})

	// kiln is unknown to the corpus, so it belongs to no client the report can
	// name, and its hours leave the client level entirely rather than being
	// guessed at. That is the clientless figure, which already exists and
	// already has a sentence explaining it.
	if got := clientOf(t, after, "atelier").Engaged.Total; got != 10*time.Minute {
		t.Errorf("atelier holds %v after half an hour was moved to a project it does not own, want 10m", got)
	}
	if after.Clientless != 30*time.Minute {
		t.Errorf("%v is attributed to a project and no client, want the 30m that moved to a "+
			"project the report cannot place", after.Clientless)
	}
	if before.Clientless != 0 {
		t.Fatalf("the fixture already had %v clientless before any correction", before.Clientless)
	}
}

// And when the project IS one the corpus places, the client follows it. The
// mapping is taken from the report's own evidence rather than from a registry,
// which this package reads nothing of.
func TestReattributingToAProjectTheCorpusPlacesCarriesItsClient(t *testing.T) {
	events := []source.Event{
		clientPoint(at(10, 0), "atelier", "loom"),
		clientPoint(at(10, 10), "atelier", "loom"),
		clientPoint(at(10, 20), "atelier", "loom"),
		clientPoint(at(10, 30), "atelier", "loom"),
		clientPoint(at(14, 0), "foundry", "anvil"),
	}
	after := corrected(t, events, []adjustments.Attribution{said(10, 0, 10, 30, "anvil")})

	if got := clientOf(t, after, "foundry").Engaged.Total; got != 40*time.Minute {
		t.Errorf("foundry holds %v; it should have gained the 30m asserted to its project on top "+
			"of the 10m of its own", got)
	}
	if after.Clientless != 0 {
		t.Errorf("%v went clientless although the asserted project's client is named in the report",
			after.Clientless)
	}
}
