package source

import "slices"

// What an observation is worth as evidence that A PERSON WAS THERE.
//
// This is the fourth axis, and it is not Kind, Shape or Cause. Kind says how a
// source is read, Shape says what an event means, and Cause says what wrote an
// artifact. None of them answers the question a reviewer actually asks of a
// thin day — "did anything here record somebody DOING something?" — and the
// nearest one answers it wrongly today: `fs-observations` declares every one of
// its 130,594 observations `user-action`, and task 4LQHd established by
// measurement that 1,755 of them were written by a log-rotation cron. The
// declared cause is a claim about a mechanism the collector cannot see.
//
// So this is a judgment about SOURCES, made in one place and named out loud,
// rather than a property deduced from a field that does not carry it.

// attests names the sources whose observations record something a person did.
//
// The IDs are literals rather than references to the collectors that define
// them, because every one of those collectors imports this package. That is a
// real cost — a renamed source silently becomes weak — and it is bounded by the
// direction the mistake falls in: see WeakEvidence for why the unknown case is
// the safe one.
//
// blender-backups is HERE and motion-autosave is NOT, and that split is
// measured rather than assumed. A .blend1 is written by the Save the person
// pressed and landed inside Blender's own focus intervals 4 of 4, and 5 of 5
// when task 85pGR checked it again over a different span. A Motion autosave is
// written by a fifteen-minute timer that does not care whether anybody is at
// the machine: 9 of 15, and the one miss that was not clock resolution was
// written on 2026-07-11 at 17:53 during a break Ben independently recalls, with
// the screen off. Same Kind, same Shape, same reader, same join, and worth
// measurably different amounts (project/private/2026-07-29-join-validation.md §4).
var attests = map[string]bool{
	"claude-transcripts": true, // a message was typed and an answer came back
	"gitlog-commits":     true, // a commit was made
	"gitlog-reflog":      true, // a branch was moved, a rebase was run
	"job-events":         true, // a task was created, claimed, noted, closed
	"job-claims":         true, // a task was held, for an interval somebody held it
	"blender-backups":    true, // an explicit save, measured 4 of 4 and 5 of 5
}

// WeakEvidence reports whether a source's observations are, BY THEMSELVES, no
// evidence that a person did anything.
//
// A walked file modification time is the case this exists for. It says a file
// changed at an instant; it does not say who or what changed it, and a
// log-rotation cron running once a day for 513 days read as 126.2h of presence
// and 135.2h of one client's engaged time until it was excluded by hand. A
// hundred such timestamps are not stronger than one: they are the same claim
// repeated.
//
// AN UNCLASSIFIED SOURCE IS WEAK, and that is the whole reason this is a
// whitelist. "Nothing has been established about this source" and "this source
// records acts" are different facts, and defaulting to the second credits an
// unweighed stream with a person's presence — silently, in the direction
// nobody chose. The cost of the other default is a new source that reads weak
// until somebody adds a line here, which is visible the first time anybody
// looks at a day.
//
// It is asked of EVIDENCE. Presence and screen-state streams are dropped before
// anything reaches a project, and they answer true here so that a caller which
// forgot to drop them cannot read a backlight sample as an act.
func WeakEvidence(sourceID string) bool { return !attests[sourceID] }

// Attesting lists every source WeakEvidence answers false for, sorted.
//
// A tool that marks a corpus has to be able to print the rule it applied, and a
// second copy of the list kept by hand in a script is a copy that goes stale
// silently — in the direction that makes a report look right. The slice is
// fresh on every call so that a caller cannot rewrite the judgment for
// everybody else.
func Attesting() []string {
	out := make([]string, 0, len(attests))
	for id := range attests {
		out = append(out, id)
	}
	slices.Sort(out)
	return out
}
