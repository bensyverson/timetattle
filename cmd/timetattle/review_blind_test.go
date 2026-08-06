package main

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// A review pass with the model's answer OFF THE SCREEN, and a day view that
// says where nothing has looked yet.
//
// The two are one file because they are one screen. See
// project/private/2026-08-02-what-the-reviewer-could-not-have-seen.md for the
// measurements both rest on.

// THE NEGATIVE ASSERTION, and it is the whole of cDUJm. A blind pass that
// prints a single block, sitting, margin or parameter has smuggled the answer
// back onto the screen and the labels it collects are worth what the confirming
// pass's are worth, which is nothing.
func TestABlindPassPrintsNoPartOfTheModelsAnswer(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-print")
	if err != nil {
		t.Fatalf("review -blind -print: %v", err)
	}
	for _, forbidden := range []string{
		"sitting", "block", "inferred", "lead-in", "tail-out", "gap ", "derived ",
		"agreed", "corrected",
	} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("a blind pass printed %q, which is the model's answer:\n%s", forbidden, out)
		}
	}
}

// And the positive one: it has to print the observations, or there is nothing
// to answer from.
func TestABlindPassPrintsTheSilencesItWillAskAbout(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-print")
	if err != nil {
		t.Fatalf("review -blind -print: %v", err)
	}
	for _, want := range []string{"silence", "observed", "09:0", "14:0"} {
		if !strings.Contains(out, want) {
			t.Errorf("a blind pass did not print %q:\n%s", want, out)
		}
	}
}

// The question is no longer "is this right?". Nothing was proposed, so the
// vocabulary that agrees with a proposal has to be gone.
func TestABlindPassDoesNotOfferAKeyThatAgreesWithAnything(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "q\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	if strings.Contains(out, "[Enter] right") {
		t.Errorf("a blind pass offered a key that confirms a shape nothing drew:\n%s", out)
	}
	// Assert the KEYS, not bare words. "stopped" now also appears in the
	// sentence naming a silence's direction, so a bare substring check passes
	// on prose even where no key offers it — the assertion would survive the
	// keys being removed entirely. These two are offered whichever way a
	// silence runs; the direction-specific ones are
	// TestEachSilenceOffersKeysThatMatchItsDirection.
	for _, want := range []string{"[w] worked through", "[u] cannot remember"} {
		if !strings.Contains(out, want) {
			t.Errorf("a blind pass did not offer %q:\n%s", want, out)
		}
	}
}

// Answering the silences records assertions, and the review row says the
// reviewer was looking at the observations.
func TestABlindPassRecordsWhatItWasShown(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9, 14)
	// One silence per prompt, in time order: the day's opening, the long gap
	// between the two clusters, and the evening. Worked through the first two,
	// stopped for the third.
	if _, err := reviewRun(t, path, cfg, "w\nw\nn\n", "-date", "2026-07-28", "-blind"); err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 1 {
		t.Fatalf("recorded %d reviews, want one", len(reviews))
	}
	if reviews[0].Saw() != adjustments.Observations {
		t.Errorf("the review says it was shown %q, want %q", reviews[0].Saw(), adjustments.Observations)
	}
	if len(adjs) == 0 {
		t.Fatal("a blind pass answering every silence recorded no assertion at all")
	}
	var engaged, notEngaged int
	for _, a := range adjs {
		switch a.Assertion {
		case adjustments.Engaged:
			engaged++
		case adjustments.NotEngaged:
			notEngaged++
		}
	}
	if engaged == 0 || notEngaged == 0 {
		t.Errorf("%d engaged and %d not-engaged; a pass that answers in both directions "+
			"must record in both", engaged, notEngaged)
	}
}

// A silence skipped is asserted NOTHING. That is the difference between a
// display floor and a gap threshold.
func TestASkippedSilenceIsAssertedNothing(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9, 14)
	if _, err := reviewRun(t, path, cfg, "s\ns\ns\n", "-date", "2026-07-28", "-blind"); err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 1 {
		t.Fatalf("recorded %d reviews, want one: the looking happened", len(reviews))
	}
	for _, a := range adjs {
		if a.Assertion == adjustments.NotEngaged {
			t.Errorf("skipping every silence still recorded not-engaged over %s..%s", a.Start, a.End)
		}
	}
}

// The ordinary pass gains a fourth CELL state. Three symbols cannot tell "every
// source reported quiet" from "no source has been asked", and on 2026-08-01
// confusing them cost 1.4379h of false assertion.
func TestTheDayViewSaysWhereNothingHasLookedYet(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-print")
	if err != nil {
		t.Fatalf("review -print: %v", err)
	}
	if !strings.Contains(out, "nothing has looked here yet") {
		t.Errorf("the legend does not name the fourth cell state:\n%s", out)
	}
	if !strings.Contains(out, "nothing recorded") {
		t.Errorf("the legend lost the third cell state:\n%s", out)
	}
}

// And it names WHICH horizon binds, because "re-run sessionize", "fix a failing
// source" and "come back tomorrow" are three different things to go and do.
func TestTheDayViewNamesWhatIsBehind(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-print")
	if err != nil {
		t.Fatalf("review -print: %v", err)
	}
	if !strings.Contains(out, "clustering last ran") {
		t.Errorf("the day view does not say when the clustering last ran:\n%s", out)
	}
	if !strings.Contains(out, "fs-observations") {
		t.Errorf("the day view does not name the source it read freshness for:\n%s", out)
	}
}

// MAKING THE QUESTION ANSWERABLE WITHOUT MAKING THE ANSWER CIRCULAR.
//
// Everything below is task Qd6W0, raised after the first real `review -blind`
// session was abandoned: 24 silences put to a reviewer with nothing on the
// screen but block characters, and a first silence whose prompt asked him to
// name a stop at an instant he was asleep for.

// blindConfig names a real presence stream, so that the withheld-evidence case
// has something to withhold. The calendar is the same UTC 05:00 one reviewConfig
// pins, for the same reason.
const blindConfig = `{"sessions":{"gap":"15m","lead_in":"5m","tail_out":"5m",
	"presence_sources":["knowledgec-backlit"],"ignore_sources":[]},
	"calendar":{"timezone":"UTC","day_start":"05:00","week_start":"monday"}}`

// blindSeed builds one working day out of THREE streams rather than one: file
// saves that name a project, application focus that names no project at all,
// and a backlight reading that falls inside a silence.
//
// The mixture is the point. On 2026-07-17, the day that provoked this task, 573
// of 576 observations named no project whatever, so a screen that groups by
// project tells the reviewer nothing and the application identifier is the only
// cue there is.
func blindSeed(t *testing.T) (path, cfg string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	ctx := context.Background()
	day := time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	on := func(hour, minute int) time.Time {
		return day.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
	}

	saves := []source.Event{
		source.NewPoint(on(9, 0), source.Identity{Document: "a.motn", Project: "seedwork"}, "fs-observations"),
		source.NewPoint(on(9, 1), source.Identity{Document: "a.motn", Project: "seedwork"}, "fs-observations"),
	}
	for i := range saves {
		saves[i].Locator = "/tmp/seed/a.motn"
	}
	focus := []source.Event{
		source.NewInterval(on(14, 0), on(14, 1), source.Identity{App: "com.apple.Safari"}, "biome-appusage"),
		source.NewInterval(on(14, 2), on(14, 3), source.Identity{App: "us.zoom.xos"}, "biome-appusage"),
	}
	lit := source.NewInterval(on(11, 0), on(12, 0), source.Identity{}, "knowledgec-backlit")
	lit.State = "on"

	for _, batch := range []struct {
		id     string
		events []source.Event
	}{
		{"fs-observations", saves}, {"biome-appusage", focus},
		{"knowledgec-backlit", []source.Event{lit}},
	} {
		run, err := st.BeginRun(ctx, store.RunSpec{SourceID: batch.id, Input: "/tmp/seed"})
		if err != nil {
			t.Fatalf("begin a run for %s: %v", batch.id, err)
		}
		if _, err := run.Write(ctx, batch.events); err != nil {
			t.Fatalf("write the %s seed: %v", batch.id, err)
		}
		if err := run.Finish(ctx); err != nil {
			t.Fatalf("finish the %s run: %v", batch.id, err)
		}
	}
	if _, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		return store.Attributed{Client: "seedworks", Project: "seedwork"}, strings.HasPrefix(locator, "/tmp/seed"), nil
	}); err != nil {
		t.Fatalf("attribute the seed: %v", err)
	}
	st.Close()

	cfg = writeConfig(t, blindConfig)
	if err := sessionizeTo(&bytes.Buffer{}, []string{"-store", path, "-config", cfg, "-quiet"}); err != nil {
		t.Fatalf("sessionize the seed: %v", err)
	}
	return path, cfg
}

// CRITERION TbV. A silence with nothing beside it is not a question anybody can
// answer, and a reviewer handed one abandons the pass — which is what happened.
//
// The day screen carries it as ONE CHRONOLOGY, silence and seen alternating. The
// first version drew both flanks under every silence and repeated the same four
// applications twenty-four times over on the real day.
func TestABlindPassPutsWhatWasSeenBetweenTheSilencesOnTheScreen(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-print")
	if err != nil {
		t.Fatalf("review -blind -print: %v", err)
	}
	for _, want := range []string{
		"fs-observations", "seedwork/a.motn",
		"biome-appusage", "com.apple.Safari", "us.zoom.xos",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the blind screen never shows %q, so the silences beside it cannot be "+
				"answered from anything:\n%s", want, out)
		}
	}
	// And the day view is not a wall. Twenty-four silences is a few minutes
	// only if each one costs a couple of lines.
	if lines := strings.Count(out, "\n"); lines > 45 {
		t.Errorf("a three-silence day draws %d lines; the chronology is meant to cost one line "+
			"per stretch, not one screen:\n%s", lines, out)
	}
}

// And beside the QUESTION, labelled for the span it really describes. The
// silence itself holds nothing by construction, so a cue that could be mistaken
// for an answer about it would be the worst of both.
func TestEachSilenceIsAskedWithTheEvidenceFlankingItLabelled(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "s\ns\ns\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	_, second, found := strings.Cut(out, "silence 2 of 3")
	if !found {
		t.Fatalf("the interior silence was never put to the reviewer:\n%s", out)
	}
	prompt, _, _ := strings.Cut(second, "\n  silence ")
	for _, want := range []string{"before it", "seedwork/a.motn", "after it", "com.apple.Safari"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("the prompt for the interior silence never shows %q:\n%s", want, prompt)
		}
	}
	// AND THE BOUND. A flank may not reach past the silence on its far side: on
	// 2026-07-17 an unbounded version cued the 08:26..08:36 silence with
	// com.apple.finder at 10:08, four silences away, under a heading reading
	// "after it". Here the day's opening silence ends at 09:00 and the next
	// silence begins at 09:01, so its whole forward flank is that one minute —
	// the 14:00 application focus is on the far side of a 4h59m silence.
	_, first, _ := strings.Cut(out, "silence 1 of 3")
	opening, _, _ := strings.Cut(first, "\n  silence ")
	if strings.Contains(opening, "com.apple.Safari") {
		t.Errorf("the opening silence is cued with evidence from beyond the next silence:\n%s",
			opening)
	}
	if !strings.Contains(opening, "seedwork/a.motn") {
		t.Errorf("the opening silence lost the one thing that IS adjacent to it:\n%s", opening)
	}
}

// AND THE LINE THE EVIDENCE STOPS AT. A presence reading falls INSIDE a
// silence, so showing it would answer the question from a stream a script can
// read unaided. It is withheld — and SAID to be withheld, because a stream that
// simply vanishes from the screen reads as one that recorded nothing.
func TestABlindPassSaysWhichStreamItIsNotShowing(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-print")
	if err != nil {
		t.Fatalf("review -blind -print: %v", err)
	}
	if !strings.Contains(out, "knowledgec-backlit") {
		t.Errorf("the blind screen never names the stream it is holding back:\n%s", out)
	}
	if !strings.Contains(out, "not drawn") {
		t.Errorf("the blind screen does not say that a stream is being held back:\n%s", out)
	}
}

// CRITERION iwa, AND THE DEFECT THAT RAISED THIS TASK.
//
// The first silence of a day runs from the working day's own boundary to the
// first thing anything saw. Asking "you stopped at?" there names a stop that
// never happened, at an instant the reviewer was asleep for. Answering it
// honestly required entering 05:00 as a stop, and answering it the way it reads
// would have asserted three hours of phantom overnight work into calibration
// ground truth.
func TestTheFirstSilenceOfADayAsksWhenWorkStartedRatherThanWhenItStopped(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "t\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	if !strings.Contains(out, "you started at?") {
		t.Errorf("the day's first silence does not ask when work STARTED:\n%s", out)
	}
	if strings.Contains(out, "you stopped at?") {
		t.Errorf("the day's first silence asks for a stop, which is the transition running the "+
			"other way:\n%s", out)
	}
}

// And the assertion it produces, which is where the phantom would have landed.
func TestTheFirstSilenceOfADayRecordsNoWorkBeforeTheInstantGiven(t *testing.T) {
	path, cfg := blindSeed(t)
	if _, err := reviewRun(t, path, cfg, "t\n08:00\ns\ns\n", "-date", "2026-07-28", "-blind"); err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	_, adjs := reviewRecorded(t, path)
	started := time.Date(2026, 7, 28, 8, 0, 0, 0, time.UTC)
	var denied bool
	for _, a := range adjs {
		if a.Assertion == adjustments.Engaged && a.Start.Before(started) {
			t.Errorf("work was asserted from %s, before the %s the reviewer named; the hours he "+
				"was asleep for are now ground truth", a.Start.Format(time.RFC3339), "08:00")
		}
		if a.Assertion == adjustments.NotEngaged && !a.Start.After(day2807(5, 0)) && !a.End.Before(started) {
			denied = true
		}
	}
	if !denied {
		t.Errorf("nothing records that 05:00..08:00 was not work; naming the instant work " +
			"started has to assert the other side of it, or the answer says nothing")
	}
}

// The other end of the day, and the same defect in mirror image.
func TestTheLastSilenceOfADayAsksWhenWorkStoppedRatherThanWhenItStartedAgain(t *testing.T) {
	path, cfg := blindSeed(t)
	// The instant is answered rather than left at end-of-input, because the
	// second question is what has to be gone: a run that stops before it would
	// pass against the very code this case exists to change.
	out, err := reviewRun(t, path, cfg, "s\ns\nt\n15:00\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	if !strings.Contains(out, "you stopped at?") {
		t.Errorf("the day's last silence does not ask when work STOPPED:\n%s", out)
	}
	if strings.Contains(out, "started again at?") {
		t.Errorf("the day's last silence asks when work resumed, after the last thing anything "+
			"saw:\n%s", out)
	}
}

// A REGRESSION FOR A BUG FOUND BY READING, NOT BY FAILURE. drawBlind labels the
// edges against the day's HORIZON and askBlind labelled them against `now`, so
// on any day but today the closing silence was named on the day screen and
// unnamed at the prompt — where the wording of the question depends on it.
func TestTheLastSilenceIsNamedAtThePromptAndNotOnlyOnTheDayScreen(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "s\ns\nq\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	_, prompts, found := strings.Cut(out, "silence 3 of 3")
	if !found {
		t.Fatalf("the third silence was never put to the reviewer:\n%s", out)
	}
	if !strings.Contains(prompts, "after the last thing seen") {
		t.Errorf("the prompt for the day's last silence does not say it is the day's last "+
			"silence:\n%s", prompts)
	}
}

// CRITERION KF1. The tool knows how long ago the day was and the reviewer is
// being asked to remember it. Reporting the distance is not a threshold and
// refuses nothing; it is the fact that decides how much the answer is worth.
func TestABlindPassSaysHowLongAgoTheDayWas(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-print")
	if err != nil {
		t.Fatalf("review -blind -print: %v", err)
	}
	if !strings.Contains(out, "days ago") {
		t.Errorf("the blind screen never says how long ago this day was:\n%s", out)
	}
}

// CRITERION yBQ. A guessed label and a remembered one are the same row to the
// fitter, and the guess moves the parameters with false confidence. So the
// screen has to say that where the answer is given, not in a manual.
func TestABlindPassSaysThatAGuessCannotBeToldFromAMemory(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-print")
	if err != nil {
		t.Fatalf("review -blind -print: %v", err)
	}
	if !strings.Contains(out, "cannot be told from") {
		t.Errorf("the blind screen never says a guessed answer is indistinguishable from a "+
			"remembered one:\n%s", out)
	}
}

// Each silence offers the keys its own direction admits — and `u` stays one
// keystroke at all three, which is the half of criterion yBQ a reworded key list
// could quietly lose.
func TestEachSilenceOffersKeysThatMatchItsDirection(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "s\ns\ns\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	for i, want := range map[int]string{
		1: "[t] I started", 2: "[t] I stopped and came back", 3: "[t] I stopped",
	} {
		_, rest, found := strings.Cut(out, fmt.Sprintf("silence %d of 3", i))
		if !found {
			t.Fatalf("silence %d was never put to the reviewer:\n%s", i, out)
		}
		prompt, _, _ := strings.Cut(rest, "\n  silence ")
		if !strings.Contains(prompt, want) {
			t.Errorf("silence %d does not offer %q, so its keys do not match its direction:\n%s",
				i, want, prompt)
		}
		if !strings.Contains(prompt, "[u] cannot remember") {
			t.Errorf("silence %d does not offer [u] cannot remember:\n%s", i, prompt)
		}
	}
}

// THE NEGATIVE ASSERTION, RESTATED OVER THE PART OF THE PASS THAT ASKS. The
// existing case covers the drawn day; the evidence beside a silence is new
// surface and is printed at the prompt, where nothing was checking.
func TestABlindPassPrintsNoPartOfTheModelsAnswerWhileAsking(t *testing.T) {
	path, cfg := blindSeed(t)
	out, err := reviewRun(t, path, cfg, "s\ns\ns\n", "-date", "2026-07-28", "-blind")
	if err != nil {
		t.Fatalf("review -blind: %v", err)
	}
	// The positive half first, so that the negative half is checking a screen
	// with the new evidence on it rather than an empty one.
	for _, want := range []string{"before it", "after it", "com.apple.Safari"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the asking pass never shows %q, so the case below checks nothing:\n%s",
				want, out)
		}
	}
	for _, forbidden := range []string{
		"sitting", "block", "inferred", "lead-in", "tail-out", "gap ", "derived ",
	} {
		if strings.Contains(strings.ToLower(out), forbidden) {
			t.Errorf("a blind pass printed %q while asking, which is the model's answer:\n%s",
				forbidden, out)
		}
	}
}

func day2807(hour, minute int) time.Time {
	return time.Date(2026, 7, 28, hour, minute, 0, 0, time.UTC)
}

// -blind and -confirm are opposite claims about what was on the screen.
func TestBlindAndConfirmAreRefusedTogether(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-28"}, 9)
	_, err := reviewRun(t, path, cfg, "", "-date", "2026-07-28", "-blind", "-confirm")
	if err == nil {
		t.Fatal("-blind -confirm was accepted; one of them is false about what was shown")
	}
}
