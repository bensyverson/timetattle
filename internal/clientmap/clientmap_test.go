package clientmap_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
)

// THE TWO KINDS OF CHANGE, WHICH A SINGLE TIMESTAMP CANNOT EXPRESS.
//
// A REASSIGNMENT says the project moved to a new client on some date. Work done
// before that date was done for the OLD client and must keep it; rewriting it
// retroactively would be false, and would break mjalb's criterion XZk — an
// invoiced period is immutable against later model changes.
//
// A CORRECTION says it was ALWAYS the new client and the old value was a
// mistake. That one must apply retroactively, because the old answer was never
// true of any instant.
//
// The log separates them exactly as internal/adjustments does: an assertion
// carries its own EFFECTIVE span separately from RECORDED_AT, when somebody
// wrote it down. A reassignment sets EffectiveFrom to the date of the move; a
// correction leaves it zero, meaning "from the beginning of time".
//
// Names here are invented. The live client names are a confidentiality matter
// (task 5nCl4) and none of them appears in this repository.

var (
	june = time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	july = time.Date(2026, 7, 1, 9, 0, 0, 0, time.UTC)
	aug  = time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)
)

// asserted is one row of the log, with recorded_at ordered by id so a test does
// not have to invent microsecond-apart timestamps to control the fold.
func asserted(id int64, project, client, source string, from, recorded time.Time) clientmap.Assertion {
	return clientmap.Assertion{
		ID: id, Project: project, Client: client, Source: source,
		EffectiveFrom: from, RecordedAt: recorded,
	}
}

// A first registration is retroactive, because that is the only reading that
// does any work: registering a directory after its evidence already exists is
// the ordinary way somebody starts tracking a project, and an assertion that
// began at the moment it was typed would attribute none of it.
func TestAFirstAssertionCoversEveryInstantBeforeItWasRecorded(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, aug),
	})

	for _, at := range []time.Time{june, july, aug} {
		got, ok := m.At("loom", at)
		if !ok || got != "atelier" {
			t.Errorf("at %s the project resolves to %q (%v), want atelier", at.Format(time.DateOnly), got, ok)
		}
	}
}

// A REASSIGNMENT splits the timeline. This is the case that makes the log worth
// building rather than storing one client per project.
func TestAReassignmentLeavesEarlierWorkWithTheClientItWasDoneFor(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "bindery", "/git/loom", july, aug),
	})

	if got, _ := m.At("loom", june); got != "atelier" {
		t.Errorf("June resolves to %q, want atelier; work done before the move was done for the "+
			"old client and rewriting it would make an invoiced period mutable", got)
	}
	if got, _ := m.At("loom", aug); got != "bindery" {
		t.Errorf("August resolves to %q, want bindery", got)
	}
	// The boundary itself belongs to the new client: the assertion is effective
	// FROM that instant, inclusively.
	if got, _ := m.At("loom", july); got != "bindery" {
		t.Errorf("the instant the reassignment takes effect resolves to %q, want bindery", got)
	}
}

// A CORRECTION rewrites the whole timeline, because the old answer was never
// true. Same log, same fold, and the ONLY difference from the case above is
// that EffectiveFrom is zero.
func TestACorrectionAppliesRetroactivelyToEveryInstant(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "bindery", "/git/loom", time.Time{}, aug),
	})

	for _, at := range []time.Time{june, july, aug} {
		if got, _ := m.At("loom", at); got != "bindery" {
			t.Errorf("at %s a corrected project resolves to %q, want bindery everywhere; a "+
				"correction says the old value was never true", at.Format(time.DateOnly), got)
		}
	}
}

// TIME TRAVEL, which is the whole reason this is a log. AsOf rebuilds what the
// mapping SAID at a past instant, by folding only the assertions recorded by
// then — the same property internal/adjustments has and for the same reason.
func TestAsOfRecoversWhatTheMappingSaidBeforeACorrectionWasRecorded(t *testing.T) {
	log := []clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "bindery", "/git/loom", time.Time{}, aug),
	}

	if got, _ := clientmap.Fold(log).At("loom", july); got != "bindery" {
		t.Errorf("folding the whole log gives %q, want bindery", got)
	}
	// As the world looked in July, before the August correction existed.
	if got, _ := clientmap.FoldAsOf(log, july).At("loom", july); got != "atelier" {
		t.Errorf("folded as of July the project resolves to %q, want atelier; an answer given "+
			"before a correction was recorded cannot depend on it", got)
	}
}

// NEGLECT AND MISTYPE FOLD IDENTICALLY AND ARE STILL DISTINGUISHABLE, which is
// why this log carries no `kind` column.
//
// Ben named three cases. A SHIFT is a reassignment and is tested above. The
// other two both cover all of history and so fold the same way:
//
//	MISTYPE   "I typed the wrong client" — superseding a claim that was false.
//	NEGLECT   "I never assigned a client to this project" — filling a hole.
//
// They are not the same event, and this repository has shipped the collapse of
// "nothing was recorded" into "something was recorded and it was wrong" twice
// in three tools. But the distinction is RECOVERABLE FROM THE ROWS: a neglect
// assertion is the first thing the log ever said about that project, and a
// mistype supersedes an earlier assertion from the same directory. Folding the
// log up to just before the new row answers which, exactly.
//
// So a column would be a second spelling of a fact the rows already carry,
// which is the argument 0011 makes for not adding one and which dayview.changed
// makes for computing rather than storing. Folded deliberately, not by
// accident.
func TestNeglectAndMistypeFoldAlikeButRemainDistinguishableInTheLog(t *testing.T) {
	// NEGLECT: nothing had ever been said about "press".
	neglect := []clientmap.Assertion{
		asserted(1, "press", "atelier", "/git/press", time.Time{}, aug),
	}
	// MISTYPE: a false claim already stood, from the same directory.
	mistype := []clientmap.Assertion{
		asserted(1, "loom", "atlier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "atelier", "/git/loom", time.Time{}, aug),
	}

	// Both are retroactive: that is the shared behaviour.
	for _, at := range []time.Time{june, july, aug} {
		if got, _ := clientmap.Fold(neglect).At("press", at); got != "atelier" {
			t.Errorf("neglect at %s gives %q, want atelier", at.Format(time.DateOnly), got)
		}
		if got, _ := clientmap.Fold(mistype).At("loom", at); got != "atelier" {
			t.Errorf("mistype at %s gives %q, want atelier", at.Format(time.DateOnly), got)
		}
	}

	// And they are still told apart, by what the log said BEFORE each landed.
	if clientmap.FoldAsOf(neglect, july).Asserted("press") {
		t.Error("a neglected project appears asserted before anything asserted it; the log " +
			"cannot then tell filling a hole from superseding a false claim")
	}
	if !clientmap.FoldAsOf(mistype, july).Asserted("loom") {
		t.Error("a mistyped project appears unasserted before its correction; the false claim " +
			"it superseded has been lost")
	}
	if got, _ := clientmap.FoldAsOf(mistype, july).At("loom", june); got != "atlier" {
		t.Errorf("the superseded claim reads %q, want the original typo preserved; an "+
			"append-only log that forgets what it used to say is an UPDATE with extra rows", got)
	}
}

// Two assertions recorded at the same instant resolve by id, so a fold is
// deterministic rather than depending on whatever ORDER BY a caller wrote. This
// is internal/adjustments' rule, unchanged.
func TestTwoAssertionsRecordedTogetherResolveByID(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(2, "loom", "bindery", "/git/loom", time.Time{}, aug),
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, aug),
	})

	if got, _ := m.At("loom", aug); got != "bindery" {
		t.Errorf("the later id resolves to %q, want bindery", got)
	}
}

// A COLLISION IS A FOURTH THING AND MUST NOT BE FOLDED INTO ANY OF THE OTHERS.
//
// Two DIRECTORIES asserting different clients for one project is not a
// correction — neither of them changed its mind — and picking the later one is
// last-writer-wins, which produces a plausible wrong number. The fold refuses to
// answer and names the project, exactly as store.Attribution reports an
// unreadable locator rather than treating it as unattributed.
func TestTwoDirectoriesDisagreeingIsACollisionAndNotACorrection(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "bindery", "/git/assets/loom", time.Time{}, aug),
	})

	if got, ok := m.At("loom", aug); ok {
		t.Errorf("a project two directories claim for different clients resolved to %q; "+
			"last-writer-wins is a plausible wrong number and this must refuse instead", got)
	}
	cs := m.Collisions()
	if len(cs) != 1 {
		t.Fatalf("%d collisions reported, want 1", len(cs))
	}
	if cs[0].Project != "loom" {
		t.Errorf("the collision names project %q, want loom", cs[0].Project)
	}
	if len(cs[0].Clients) != 2 {
		t.Errorf("the collision names clients %v, want both of them so it can be acted on",
			cs[0].Clients)
	}
}

// ONE DIRECTORY CHANGING ITS MIND IS NOT A COLLISION, which is what makes the
// source column load-bearing rather than provenance. Without it, a correction
// and a disagreement are the same two rows.
func TestOneDirectoryChangingItsMindIsNotACollision(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "bindery", "/git/loom", time.Time{}, aug),
	})

	if len(m.Collisions()) != 0 {
		t.Errorf("one directory revising its own assertion was reported as a collision: %v",
			m.Collisions())
	}
}

// Several directories AGREEING is the many-directories-one-project arrangement
// the registry documents and supports. It must stay a plain answer.
func TestSeveralDirectoriesAgreeingIsNotACollision(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "atelier", "/git/loom", time.Time{}, june),
		asserted(2, "loom", "atelier", "/git/assets/loom", time.Time{}, june),
	})

	if got, ok := m.At("loom", aug); !ok || got != "atelier" {
		t.Errorf("two directories agreeing resolved to %q (%v), want atelier", got, ok)
	}
	if len(m.Collisions()) != 0 {
		t.Errorf("agreement was reported as a collision: %v", m.Collisions())
	}
}

// A project nothing has asserted has NO CLIENT, and that is a third answer
// distinct from a collision. Absence of a row is not an observation.
func TestAProjectNothingAssertsHasNoClientAndIsNotACollision(t *testing.T) {
	m := clientmap.Fold(nil)

	if got, ok := m.At("loom", aug); ok {
		t.Errorf("an unasserted project resolved to %q", got)
	}
	if len(m.Collisions()) != 0 {
		t.Errorf("silence was reported as a collision: %v", m.Collisions())
	}
}

// An instant BEFORE any assertion takes effect has no client, rather than
// falling back to the earliest one. A reassignment dated July says nothing
// whatever about June unless something else does.
func TestAnInstantBeforeEveryAssertionHasNoClient(t *testing.T) {
	m := clientmap.Fold([]clientmap.Assertion{
		asserted(1, "loom", "bindery", "/git/loom", july, aug),
	})

	if got, ok := m.At("loom", june); ok {
		t.Errorf("June resolved to %q from an assertion effective in July; that would invent "+
			"an attribution nothing stated", got)
	}
}
