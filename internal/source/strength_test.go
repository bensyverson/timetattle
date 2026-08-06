package source_test

import (
	"slices"
	"testing"

	"github.com/bensyverson/timetattle/internal/source"
)

// A file's modification time is not, by itself, evidence that anybody was
// there. This is the whole of task 4LQHd restated as a predicate: a
// log-rotation cron wrote 1,755 of them and they read as 126.2h of presence.
func TestAFileModificationTimeIsWeakEvidenceOnItsOwn(t *testing.T) {
	if !source.WeakEvidence("fs-observations") {
		t.Error("WeakEvidence(fs-observations) = false; a walked mtime is a timestamp and " +
			"nothing else, and a cron writes them")
	}
}

// The sources that record an ACT. A Claude message was typed, a commit was
// made, a claim was held; each of those is a person doing something.
func TestASourceThatRecordsAnActIsNotWeakEvidence(t *testing.T) {
	for _, id := range []string{
		"claude-transcripts", "gitlog-commits", "gitlog-reflog", "job-events", "job-claims",
	} {
		if source.WeakEvidence(id) {
			t.Errorf("WeakEvidence(%s) = true; it records something a person did", id)
		}
	}
}

// Blender's .blend1 is written by the Save the person pressed, and this project
// has MEASURED what that is worth: 4 of 4 and then 5 of 5 landing inside
// Blender's own focus intervals. Folding it in with a walked mtime would throw
// that measurement away.
func TestABlenderBackupIsNotWeakEvidence(t *testing.T) {
	if source.WeakEvidence("blender-backups") {
		t.Error("WeakEvidence(blender-backups) = true; it is written by an explicit save and " +
			"landed inside its own application's focus 4 of 4 and 5 of 5")
	}
}

// A Motion autosave is written by a fifteen-minute timer that does not care
// whether anybody is at the machine, and one of them was written during a
// confirmed break with the screen off. Same Kind and same Shape as the Blender
// backup above, worth measurably less; Cause is why they part.
func TestAMotionAutosaveIsWeakEvidence(t *testing.T) {
	if !source.WeakEvidence("motion-autosave") {
		t.Error("WeakEvidence(motion-autosave) = false; a timer wrote it, and one landed in a " +
			"confirmed break with the screen off")
	}
}

// The presence and screen-state streams are not evidence of work at all, and
// the clustering never treats them as such. Asking this question of one of them
// is a category error, and answering "not weak" would let a caller that forgot
// to drop them read a backlight sample as an act somebody performed.
func TestPresenceSourcesAreWeakEvidenceOfWork(t *testing.T) {
	for _, id := range []string{"knowledgec-backlit", "knowledgec-islocked"} {
		if !source.WeakEvidence(id) {
			t.Errorf("WeakEvidence(%s) = false; a backlight sample is not somebody doing "+
				"something", id)
		}
	}
}

// The list is readable, sorted and agrees with the predicate.
//
// A tool that marks a corpus has to be able to print the rule it applied, and a
// second hand-kept copy of the list in a script is a copy that goes stale
// without anybody noticing. This is the one place the judgment lives.
func TestTheAttestingSourcesCanBeListedAndAgreeWithThePredicate(t *testing.T) {
	got := source.Attesting()
	if len(got) == 0 {
		t.Fatal("Attesting() is empty; a whitelist nobody can read is a rule nobody can check")
	}
	if !slices.IsSorted(got) {
		t.Errorf("Attesting() = %v, which is not sorted; a list that reorders between runs "+
			"makes every report that prints it look changed", got)
	}
	for _, id := range got {
		if source.WeakEvidence(id) {
			t.Errorf("Attesting() names %s and WeakEvidence(%s) is true", id, id)
		}
	}
	// And a caller cannot reach in and change the answer for everybody else.
	got[0] = "not-a-source"
	if slices.Contains(source.Attesting(), "not-a-source") {
		t.Error("Attesting() hands out the list itself; one caller's edit rewrites the rule")
	}
}

// A source nobody has classified must not be assumed to be worth something.
// This is the project's cardinal bug in predicate form: "nothing is recorded
// about this source" and "this source records acts" are different facts, and
// defaulting to the second credits an unknown stream with a person's presence.
func TestAnUnclassifiedSourceIsWeakEvidenceRatherThanAssumedStrong(t *testing.T) {
	if !source.WeakEvidence("some-source-nobody-has-weighed") {
		t.Error("WeakEvidence of an unknown source = false; not having classified a source is " +
			"not the same as having found it strong")
	}
}
