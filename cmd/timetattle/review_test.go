package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// Helpers here are prefixed review, for the reason main.go's package doc gives:
// package scope spans every file in this directory, and two agents have already
// collided on a bare verb.

// reviewConfig pins the calendar and the clustering so that a test's days begin
// where it says they do. UTC, because a test that depends on the machine's zone
// passes in Chicago and fails in Berlin.
const reviewConfig = `{"sessions":{"gap":"15m","lead_in":"5m","tail_out":"5m",
	"presence_sources":[],"ignore_sources":[]},
	"calendar":{"timezone":"UTC","day_start":"05:00","week_start":"monday"}}`

// reviewSeed builds a store holding one cluster of saves per named day, at the
// hours given, and sessionizes it. Each day therefore has a known number of
// blocks and a reviewer can be asked about them.
func reviewSeed(t *testing.T, days []string, hours ...int) (path, cfg string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "fs-observations", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	var events []source.Event
	for _, day := range days {
		at, err := time.Parse("2006-01-02", day)
		if err != nil {
			t.Fatalf("bad seed day %q: %v", day, err)
		}
		for _, hour := range hours {
			// Two saves a minute apart, so a cluster has a real extent and the
			// lead-in and tail-out are not the whole of it.
			for _, offset := range []time.Duration{0, time.Minute} {
				e := source.NewPoint(at.Add(time.Duration(hour)*time.Hour+offset),
					source.Identity{Document: "a.motn", Project: "seedwork"}, "fs-observations")
				e.Locator = "/tmp/seed/a.motn"
				events = append(events, e)
			}
		}
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	// The project column is a recomputable cache resolved from the locator, and
	// store.Write does not accept one, so the seed has to run the attribution
	// pass the way harvest does or every row comes back unattributed.
	if _, err := st.ResolveProjects(ctx, func(locator string) (store.Attributed, bool, error) {
		return store.Attributed{Client: "seedworks", Project: "seedwork"}, strings.HasPrefix(locator, "/tmp/seed"), nil
	}); err != nil {
		t.Fatalf("attribute the seed: %v", err)
	}
	st.Close()

	cfg = writeConfig(t, reviewConfig)
	if err := sessionizeTo(&bytes.Buffer{}, []string{"-store", path, "-config", cfg, "-quiet"}); err != nil {
		t.Fatalf("sessionize the seed: %v", err)
	}
	return path, cfg
}

// reviewRun drives the subcommand with a scripted answer for every prompt.
func reviewRun(t *testing.T, path, cfg, keys string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := reviewTo(&out, strings.NewReader(keys),
		append([]string{"-store", path, "-config", cfg}, args...))
	return out.String(), err
}

func reviewRecorded(t *testing.T, path string) ([]adjustments.Review, []adjustments.Adjustment) {
	t.Helper()
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	reviews, err := st.Reviews(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	adjs, err := st.Adjustments(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	return reviews, adjs
}

// CRITERION AGZ. A day reconstructs across all sources in a single view.

func TestADayReconstructsOnOneScreen(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-print")
	if err != nil {
		t.Fatalf("review: %v", err)
	}

	for _, want := range []string{"2026-07-11", "seedwork", "09:", "14:"} {
		if !strings.Contains(out, want) {
			t.Errorf("the day view never mentions %q:\n%s", want, out)
		}
	}
	// The derivation travels with the day. A span separated from the
	// parameters that produced it is a span with no meaning, and the reviewer
	// is being asked to judge a MODEL'S output rather than a number.
	for _, want := range []string{"gap 15m", "lead-in 5m", "tail-out 5m"} {
		if !strings.Contains(out, want) {
			t.Errorf("the day view never says %q, so the reviewer cannot tell what they are "+
				"judging:\n%s", want, out)
		}
	}
	// One screen. Twenty of these is a few minutes only if each one fits.
	if lines := strings.Count(out, "\n"); lines > 40 {
		t.Errorf("the day view is %d lines; it is meant to be one screen:\n%s", lines, out)
	}
}

// The three states this project keeps collapsing, at the top of the stack. A
// store nobody has sessionized must not render as a day with no work in it.
func TestADayFromAStoreNobodyHasSessionizedSaysSo(t *testing.T) {
	path := seedStore(t)
	cfg := writeConfig(t, reviewConfig)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-print")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	if !strings.Contains(out, "sessionize") {
		t.Errorf("a store with no derivation does not name the command that makes one:\n%s", out)
	}
}

// CRITERION dko. Confirmations and corrections both write to the adjustments
// layer — and a confirmation writes the closure, not merely the blocks.

func TestConfirmingADayAccountsForEveryInstantOfIt(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	if _, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-confirm", "-origin", "a-person"); err != nil {
		t.Fatalf("review -confirm: %v", err)
	}

	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 1 {
		t.Fatalf("%d reviews, want the one that says the day was looked at", len(reviews))
	}
	if reviews[0].Verdict != adjustments.Recalled {
		t.Errorf("the verdict is %q", reviews[0].Verdict)
	}

	var engaged, notEngaged, all []timespan.Span
	for _, a := range adjs {
		s := timespan.Span{Start: a.Start, End: a.End}
		switch a.Assertion {
		case adjustments.Engaged:
			engaged, all = append(engaged, s), append(all, s)
		case adjustments.NotEngaged:
			notEngaged, all = append(notEngaged, s), append(all, s)
		}
		if a.ReviewID != reviews[0].ID {
			t.Errorf("an assertion hangs off review %d rather than %d, so it cannot be traced "+
				"back to the pass that produced it", a.ReviewID, reviews[0].ID)
		}
	}
	if len(engaged) != 2 {
		t.Errorf("%d assertions of work against 2 blocks", len(engaged))
	}
	if len(notEngaged) != 3 {
		t.Errorf("%d assertions of not-work; a confirmed day has to say what the gaps were, "+
			"or the gap threshold cannot be fitted from it", len(notEngaged))
	}
	day := 24 * time.Hour
	if got := timespan.Total(all); got != day {
		t.Errorf("the day's assertions cover %v of %v", got, day)
	}
	if got := timespan.Total(timespan.Union(all)); got != day {
		t.Errorf("the assertions union to %v, so two of them overlap", got)
	}
}

// THE FAST PATH. One keystroke per day is the property the whole design was
// chosen for, so it is a test rather than a hope: twenty blank lines review
// twenty days.
func TestTwentyDaysAreTwentyKeystrokes(t *testing.T) {
	var days []string
	for i := 1; i <= 20; i++ {
		days = append(days, fmt.Sprintf("2026-06-%02d", i))
	}
	path, cfg := reviewSeed(t, days, 9, 14)

	out, err := reviewRun(t, path, cfg, strings.Repeat("\n", 20), "-origin", "a-person", "-drill-every", "0")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 20 {
		t.Fatalf("20 keystrokes reviewed %d days:\n%s", len(reviews), out)
	}
	if len(adjs) != 20*5 {
		t.Errorf("%d assertions from 20 confirmed days, want 5 each — two blocks and three gaps",
			len(adjs))
	}
}

// UNCERTAINTY, CRITERION 7d1. Four states, and the two this project keeps
// collapsing are the ones checked hardest: a day skipped writes NOTHING, and a
// day nobody could remember writes a review and no assertions.

func TestADayNobodyCanRememberRecordsThatSomebodyLooked(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	if _, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-unremembered", "-origin", "a-person"); err != nil {
		t.Fatalf("review -unremembered: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 1 || reviews[0].Verdict != adjustments.Unremembered {
		t.Fatalf("the store holds %d reviews, want one saying the day could not be recalled", len(reviews))
	}
	if len(adjs) != 0 {
		t.Errorf("%d assertions came out of a day nobody could remember; the machine's answer "+
			"has to stand, marked unconfirmed", len(adjs))
	}
}

func TestSkippingADayLeavesItUnexaminedRatherThanUnremembered(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	if _, err := reviewRun(t, path, cfg, "s\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 0 || len(adjs) != 0 {
		t.Errorf("skipping a day wrote %d reviews and %d assertions; a day nobody looked at and "+
			"a day somebody could not remember are different facts", len(reviews), len(adjs))
	}
}

func TestTheQueueCountsAllFourStatesApart(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11", "2026-07-12", "2026-07-13", "2026-07-14"}, 9)
	if _, err := reviewRun(t, path, cfg, "", "-date", "2026-07-11", "-confirm", "-origin", "p"); err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if _, err := reviewRun(t, path, cfg, "", "-date", "2026-07-12", "-unremembered", "-origin", "p"); err != nil {
		t.Fatalf("unremembered: %v", err)
	}
	// A correction recorded by hand over 2026-07-13 makes that day "recalled
	// with corrections" rather than "recalled".
	if err := adjustTo(&bytes.Buffer{}, []string{"-store", path,
		"-from", "2026-07-13T05:00:00Z", "-to", "2026-07-14T05:00:00Z", "-review", "recalled",
		"-origin", "p"}); err != nil {
		t.Fatalf("adjust -review: %v", err)
	}
	if err := adjustTo(&bytes.Buffer{}, []string{"-store", path,
		"-from", "2026-07-13T09:00:00Z", "-to", "2026-07-13T09:30:00Z", "-assert", "not-engaged",
		"-origin", "p"}); err != nil {
		t.Fatalf("adjust -assert: %v", err)
	}

	out, err := reviewRun(t, path, cfg, "", "-list", "-all")
	if err != nil {
		t.Fatalf("review -list: %v", err)
	}
	for _, want := range []string{"agreed", "corrected", "unremembered", "never examined"} {
		if !strings.Contains(out, want) {
			t.Errorf("the listing never says %q, so two states are being reported as one:\n%s", want, out)
		}
	}
}

func TestListingWritesNothing(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	if _, err := reviewRun(t, path, cfg, "", "-list"); err != nil {
		t.Fatalf("review -list: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 0 || len(adjs) != 0 {
		t.Errorf("listing the queue wrote %d reviews and %d assertions", len(reviews), len(adjs))
	}
}

// DRILLING DOWN. The fast path stays one keystroke because detail only costs
// something on the days that are actually wrong.

func TestDrillingDownWalksTheBlocksAndSaysEachWasExamined(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	// n to reject the day, then Enter for each of the two blocks.
	if _, err := reviewRun(t, path, cfg, "n\n\n\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)
	if len(reviews) != 3 {
		t.Fatalf("%d reviews from a drilled day, want one per block and one for the day — "+
			"without them a day walked block by block and one waved through are the same rows",
			len(reviews))
	}
	// The day's own review is recorded LAST, so the latest review covering a
	// day is always the one that speaks for it.
	last := reviews[len(reviews)-1]
	if last.End.Sub(last.Start) != 24*time.Hour {
		t.Errorf("the last review covers %v, want the whole day", last.End.Sub(last.Start))
	}
	if len(adjs) != 5 {
		t.Errorf("%d assertions from a drilled day that agreed with everything, want the same "+
			"5 a wholesale confirmation writes", len(adjs))
	}
}

func TestABlockCanBeTrimmedWithoutLeavingTheKeyboard(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	// n, then e to correct the end, 10:30, then Enter to accept the block.
	if _, err := reviewRun(t, path, cfg, "n\ne\n10:30\n\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	_, adjs := reviewRecorded(t, path)
	var engaged []timespan.Span
	for _, a := range adjs {
		if a.Assertion == adjustments.Engaged {
			engaged = append(engaged, timespan.Span{Start: a.Start, End: a.End})
		}
	}
	if len(engaged) != 1 {
		t.Fatalf("%d assertions of work, want 1", len(engaged))
	}
	want := time.Date(2026, 7, 11, 10, 30, 0, 0, time.UTC)
	if !engaged[0].End.Equal(want) {
		t.Errorf("the corrected block ends at %v, want %v", engaged[0].End, want)
	}
}

func TestABlockCanBeDeletedOutright(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	// n, then n for the first block (not work at all), then Enter for the second.
	if _, err := reviewRun(t, path, cfg, "n\nn\n\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	_, adjs := reviewRecorded(t, path)
	var engaged int
	for _, a := range adjs {
		if a.Assertion == adjustments.Engaged {
			engaged++
		}
	}
	if engaged != 1 {
		t.Errorf("%d assertions of work after deleting one of two blocks, want 1", engaged)
	}
}

// A block nobody can remember is a hole rather than an assertion either way.
func TestAnUnrememberedBlockAssertsNothingAboutItsOwnClock(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	if _, err := reviewRun(t, path, cfg, "n\nu\n\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, adjs := reviewRecorded(t, path)

	var covered []timespan.Span
	for _, a := range adjs {
		if a.Assertion == adjustments.Engaged || a.Assertion == adjustments.NotEngaged {
			covered = append(covered, timespan.Span{Start: a.Start, End: a.End})
		}
	}
	whole := []timespan.Span{{
		Start: time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 7, 12, 5, 0, 0, 0, time.UTC),
	}}
	hole := timespan.Subtract(whole, covered)
	if len(hole) != 1 {
		t.Fatalf("the day has %d unasserted stretches, want the one block nobody could remember", len(hole))
	}
	var unremembered int
	for _, r := range reviews {
		if r.Verdict == adjustments.Unremembered {
			unremembered++
		}
	}
	if unremembered != 1 {
		t.Errorf("%d reviews say a block could not be remembered, so the hole is unexplained", unremembered)
	}
}

// WORK THE MODEL MISSED. Every other correction can only ever agree that the
// machine over-claimed; this is the one that says it under-claimed.
func TestWorkTheModelMissedCanBeAddedFromTheDayView(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	// a to add a span, its two ends, then Enter to confirm the day.
	if _, err := reviewRun(t, path, cfg, "a\n19:00\n21:00\n\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	_, adjs := reviewRecorded(t, path)
	var found bool
	for _, a := range adjs {
		if a.Assertion == adjustments.Engaged &&
			a.Start.Equal(time.Date(2026, 7, 11, 19, 0, 0, 0, time.UTC)) &&
			a.End.Equal(time.Date(2026, 7, 11, 21, 0, 0, 0, time.UTC)) {
			found = true
		}
	}
	if !found {
		t.Errorf("the added evening was not recorded as work; %d assertions were", len(adjs))
	}
}

// SAMPLING. If only rejected days were ever walked block by block, the days
// that got the closest look would be exactly the days somebody already
// suspected, and task PkY6I would be fitting against a sample nobody could
// characterise. A forced drill-down on a share of days is the mitigation, and
// it has to happen whatever the reviewer would have answered.
func TestAShareOfDaysIsDrilledWhateverTheAnswerWouldHaveBeen(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	// Every day forced, and the reviewer presses Enter throughout.
	if _, err := reviewRun(t, path, cfg, "\n\n\n", "-origin", "a-person", "-drill-every", "1"); err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, _ := reviewRecorded(t, path)
	if len(reviews) != 3 {
		t.Errorf("%d reviews from a forced drill-down, want one per block and one for the day", len(reviews))
	}
}

func TestTheForcedDrillDownCanBeTurnedOff(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	if _, err := reviewRun(t, path, cfg, "\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, _ := reviewRecorded(t, path)
	if len(reviews) != 1 {
		t.Errorf("%d reviews with the sampling off, want the day's own alone", len(reviews))
	}
}

// The queue offers recent days first, because recall is what this whole
// interface is spending. 594 days in the live store carry sittings and most of
// them are years old.
func TestTheQueueOffersTheMostRecentDaysFirst(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11", "2026-07-12", "2026-07-13"}, 9)
	out, err := reviewRun(t, path, cfg, "", "-list")
	if err != nil {
		t.Fatalf("review -list: %v", err)
	}
	first, last := strings.Index(out, "2026-07-13"), strings.Index(out, "2026-07-11")
	if first < 0 || last < 0 || first > last {
		t.Errorf("the queue is not newest first:\n%s", out)
	}
}

func TestQuittingStopsWithoutRecordingTheDayOnTheScreen(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11", "2026-07-12"}, 9)
	if _, err := reviewRun(t, path, cfg, "\nq\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("review: %v", err)
	}
	reviews, _ := reviewRecorded(t, path)
	if len(reviews) != 1 {
		t.Errorf("%d days were recorded; quitting recorded the day it was quit on", len(reviews))
	}
}

// A day already reviewed is not offered again unless it is asked for. Recall
// does not improve with a second pass, and an interface that keeps asking is
// one nobody finishes.
func TestADayAlreadyReviewedIsNotOfferedAgain(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11", "2026-07-12"}, 9)
	if _, err := reviewRun(t, path, cfg, "\n\n", "-origin", "a-person", "-drill-every", "0"); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	out, err := reviewRun(t, path, cfg, "\n\n", "-origin", "a-person", "-drill-every", "0")
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	reviews, _ := reviewRecorded(t, path)
	if len(reviews) != 2 {
		t.Errorf("%d reviews after a second pass over the same two days:\n%s", len(reviews), out)
	}
}

// WHICH DAYS ARE SAMPLED, pinned. The two prompt tests below depend on it, and
// a test that merely assumed a day was sampled would pass for the wrong reason
// the moment the hash changed. FNV-1a over the day's own name, mod 5.
func TestWhichDaysTheDefaultSamplingPicks(t *testing.T) {
	s := &reviewSession{drillEvery: 5}
	for _, c := range []struct {
		day  string
		want bool
	}{
		{"2026-07-11", true}, {"2026-07-14", true}, {"2026-07-20", true}, {"2026-07-25", true},
		{"2026-07-31", true}, {"2026-07-12", false}, {"2026-07-13", false}, {"2026-08-01", false},
	} {
		at, err := time.Parse("2006-01-02", c.day)
		if err != nil {
			t.Fatalf("bad day: %v", err)
		}
		day := dayview.Day{
			Day:    sessions.Day{Year: at.Year(), Month: at.Month(), Date: at.Day()},
			Blocks: []dayview.Block{{}},
		}
		if got := s.forced(day); got != c.want {
			t.Errorf("forced(%s) = %v, want %v", c.day, got, c.want)
		}
	}
}

// THE PROMPT MUST TELL THE TRUTH AT THE MOMENT OF CHOICE. On a sampled day
// Enter WALKS THE BLOCKS, and the prompt said "[Enter] right   [n] wrong, walk
// the blocks" — two keys offered for one action, described as opposites. The
// notice above it scrolls away; the prompt is what is on the screen when the
// key is pressed.
func TestASampledDaySaysWhatEnterWillActuallyDo(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "\n\n\n", "-date", "2026-07-11", "-origin", "a-person")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	t.Logf("session:\n%s", out)

	// The DAY prompt specifically. The per-block prompt below it still says
	// "[Enter] right", and correctly so — that is what Enter does to a block.
	day := reviewDayPromptIn(t, out)
	if !strings.Contains(day, "[Enter] walk the blocks") {
		t.Errorf("the day prompt does not say that Enter walks the blocks: %q", day)
	}
	if strings.Contains(day, "[Enter] right") {
		t.Errorf("the day prompt still offers Enter as \"right\": %q", day)
	}
	// The two keys were the same code path. Offering both as opposites was the
	// lie; n keeps working and simply stops being advertised as a contrast.
	if strings.Contains(day, "[n] wrong") {
		t.Errorf("the day prompt still offers [n] as the opposite of Enter: %q", day)
	}
	reviews, _ := reviewRecorded(t, path)
	if len(reviews) != 3 {
		t.Errorf("%d reviews from a sampled day walked with Enter, want one per block and one "+
			"for the day", len(reviews))
	}
}

func TestAnUnsampledDayStillOffersEnterAsAgreement(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "\n", "-date", "2026-07-12", "-origin", "a-person")
	if err != nil {
		t.Fatalf("review: %v", err)
	}
	day := reviewDayPromptIn(t, out)
	if !strings.Contains(day, "[Enter] right") {
		t.Errorf("an unsampled day does not offer Enter as agreement: %q", day)
	}
	if strings.Contains(day, "[Enter] walk the blocks") {
		t.Errorf("an unsampled day claims Enter walks the blocks: %q", day)
	}
}

// reviewDayPromptIn is the prompt put to the reviewer for the DAY, told apart
// from the per-block prompt below it by its indent: block prompts are indented
// two spaces and the day's is not.
func reviewDayPromptIn(t *testing.T, session string) string {
	t.Helper()
	for line := range strings.SplitSeq(session, "\n") {
		if strings.HasPrefix(line, "[") {
			return line
		}
	}
	t.Fatalf("no day prompt in:\n%s", session)
	return ""
}

// FITTING THE TERMINAL. review overran an eighty-column terminal by 46 columns
// on a real day, and the prompt itself was 112 columns — so the one line a
// reviewer has to read before pressing a key was the one that wrapped.
func TestTheDayViewAndItsPromptFitAnEightyColumnTerminal(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 12, 14, 17)

	var out bytes.Buffer
	if err := reviewTo(atWidth{&out, 80}, strings.NewReader("\n"),
		[]string{"-store", path, "-config", cfg, "-date", "2026-07-12", "-origin", "a-person"}); err != nil {
		t.Fatalf("review: %v", err)
	}
	t.Logf("session:\n%s", out.String())
	fits(t, 80, "review", out.String())

	// The prompt survives whole: a prompt clipped to "…" hides the keys it is
	// there to offer.
	if !strings.Contains(out.String(), "> ") {
		t.Errorf("the prompt lost its cursor:\n%s", out.String())
	}
	for _, key := range []string{"[u]", "[a]", "[s]", "[q]"} {
		if !strings.Contains(out.String(), key) {
			t.Errorf("the prompt lost %s at eighty columns:\n%s", key, out.String())
		}
	}
}

// A prompt is the one thing review writes without a trailing newline, so it is
// the one line clipTo can strand a character of. It must therefore end in its
// cursor at every width, and never in an ellipsis — the keys are its whole
// content, and one clipped at the margin hides the keys it exists to offer.
func TestAPromptEndsInItsCursorAtEveryWidth(t *testing.T) {
	for cols := 20; cols <= 130; cols++ {
		for _, keys := range [][]string{reviewDayKeys, reviewSampledKeys, reviewBlockKeys} {
			for _, indent := range []string{"", "  "} {
				got := reviewPrompt(keys, indent, cols)
				if !strings.HasSuffix(got, "  > ") {
					t.Fatalf("at %d columns the prompt does not end in its cursor: %q", cols, got)
				}
				if strings.Contains(got, "…") {
					t.Fatalf("at %d columns the prompt was clipped: %q", cols, got)
				}
				for line := range strings.SplitSeq(strings.TrimPrefix(got, "\n"), "\n") {
					// A line holding a single key can be overlong, because one
					// key is not divisible; "[n] wrong, walk the blocks" is 26
					// columns whatever the terminal says. Every line that holds
					// two comes in one short of the terminal — see reviewPrompt.
					if w := len([]rune(line)); w >= cols && strings.Contains(line, "   ") {
						t.Fatalf("at %d columns a prompt line is %d wide: %q", cols, w, line)
					}
				}
			}
		}
	}
}

// The timeline is drawn as wide as there is room for, rather than at a constant
// sixty that leaves fifteen columns of an eighty-column terminal unused — and
// an explicit -width still wins over the terminal.
func TestTheTimelineFillsTheTerminalUnlessToldOtherwise(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)

	var fitted, pinned, piped bytes.Buffer
	for _, c := range []struct {
		out  *bytes.Buffer
		cols int
		args []string
	}{
		{&fitted, 100, nil},
		{&pinned, 100, []string{"-width", "30"}},
		{&piped, 0, nil},
	} {
		var w io.Writer = c.out
		if c.cols > 0 {
			w = atWidth{c.out, c.cols}
		}
		args := append([]string{"-store", path, "-config", cfg, "-date", "2026-07-12", "-print"}, c.args...)
		if err := reviewTo(w, strings.NewReader(""), args); err != nil {
			t.Fatalf("review -print: %v", err)
		}
	}

	cells := func(report string) int {
		var n int
		for line := range strings.SplitSeq(report, "\n") {
			if strings.HasPrefix(line, "sittings ") {
				n = len([]rune(strings.TrimSpace(strings.TrimPrefix(line, "sittings"))))
			}
		}
		return n
	}
	if got := cells(fitted.String()); got <= 60 {
		t.Errorf("the timeline is %d cells in a hundred-column terminal; it did not grow", got)
	}
	if got := cells(pinned.String()); got != 30 {
		t.Errorf("-width 30 drew %d cells; an explicit width must win over the terminal", got)
	}
	if got := cells(piped.String()); got != 60 {
		t.Errorf("a pipe drew %d cells, want the same 60 as before — piped output may not "+
			"depend on a window nobody is looking at", got)
	}
}

func TestReviewIsNeverShortenedIntoAPipe(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 12, 14, 17)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-12", "-print")
	if err != nil {
		t.Fatalf("review -print: %v", err)
	}
	if strings.Contains(out, "…") {
		t.Errorf("piped output was truncated:\n%s", out)
	}
}

// A block's SOURCE LIST is the part of its line allowed to lose something, and
// it must say how many it dropped rather than trailing off. Eight sources on
// one block is the live corpus, not a hypothetical.
func TestABlockSaysHowManySourcesItLeftOut(t *testing.T) {
	all := []string{"biome-appusage", "claude-transcripts", "fs-observations",
		"gitlog-commits", "gitlog-reflog", "job-claims", "job-events", "knowledgec-backlit"}

	if got := reviewSourcesWithin(all, 200); got != strings.Join(all, ", ") {
		t.Errorf("with room for all eight, the list is %q", got)
	}
	got := reviewSourcesWithin(all, 30)
	if len([]rune(got)) > 30 {
		t.Errorf("the list is %d columns in a budget of 30: %q", len([]rune(got)), got)
	}
	if !strings.Contains(got, "more") {
		t.Errorf("a shortened source list does not say how many it left out: %q", got)
	}
	// Too narrow to name even one: a bare count is still honest, an ellipsis
	// alone is not.
	tiny := reviewSourcesWithin(all, 11)
	if len([]rune(tiny)) > 11 || !strings.Contains(tiny, "8") {
		t.Errorf("with room for nothing the list reads %q; it must still say there are eight", tiny)
	}
	if reviewSourcesWithin(nil, 40) != "(none)" {
		t.Errorf("no sources at all must say so rather than render blank")
	}
}

// TASK KrfMh. AN UNCORROBORATED PROJECT ROW IS MARKED, NEVER DROPPED.
//
// After the machine-generated file activity was excluded, 88 of the 453
// surviving project-days rest on nothing but file modification times — Woodcase
// 2026-03-21 is one note's mtime and Ben judged it by hand and kept it. Until
// this mark, that row and a row resting on two thousand transcript messages
// printed identically.

func TestAWeakProjectRowIsMarkedAndAnAttestedOneIsNot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	ctx := context.Background()
	base := time.Date(2026, 7, 12, 15, 0, 0, 0, time.UTC)
	for _, c := range []struct{ project, src, file string }{
		{"Mtimes", "fs-observations", "notes.md"},
		{"Acted", "claude-transcripts", ""},
	} {
		dir := t.TempDir()
		if _, err := registry.Register(registry.Registration{
			Dir: dir, Client: c.project, Project: c.project,
		}); err != nil {
			t.Fatalf("Register %s: %v", c.project, err)
		}
		run, err := st.BeginRun(ctx, store.RunSpec{SourceID: c.src, Input: dir})
		if err != nil {
			t.Fatalf("begin a run: %v", err)
		}
		var events []source.Event
		for _, offset := range []time.Duration{0, time.Minute} {
			e := source.NewPoint(base.Add(offset), source.Identity{Document: c.project}, c.src)
			e.Locator = dir
			if c.file != "" {
				e.Locator = filepath.Join(dir, c.file)
			}
			events = append(events, e)
		}
		if _, err := run.Write(ctx, events); err != nil {
			t.Fatalf("write the seed events: %v", err)
		}
		if err := run.Finish(ctx); err != nil {
			t.Fatalf("finish the seed run: %v", err)
		}
	}
	if err := resolveProjects(ctx, st, nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("resolve projects: %v", err)
	}
	st.Close()

	cfg := writeConfig(t, reviewConfig)
	if err := sessionizeTo(&bytes.Buffer{}, []string{"-store", path, "-config", cfg, "-quiet"}); err != nil {
		t.Fatalf("sessionize the seed: %v", err)
	}
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-12", "-print")
	if err != nil {
		t.Fatalf("review -print: %v", err)
	}
	t.Logf("day:\n%s", out)

	// The mark sits in its own column, immediately after the label. Read it by
	// RUNE, because the timeline beside it is drawn in multi-byte blocks.
	mark := func(project string) string {
		for line := range strings.SplitSeq(out, "\n") {
			if !strings.HasPrefix(line, project) {
				continue
			}
			runes := []rune(line)
			if len(runes) <= reviewLabelWidth+1 {
				t.Fatalf("the row for %s is too short to carry a mark: %q", project, line)
			}
			return string(runes[reviewLabelWidth+1])
		}
		t.Fatalf("no row for %s at all; the mark must never remove one:\n%s", project, out)
		return ""
	}
	if got := mark("Mtimes"); got != "!" {
		t.Errorf("the row whose whole trace is file mtimes is marked %q, want %q", got, "!")
	}
	if got := mark("Acted"); got != " " {
		t.Errorf("the row with a transcript under it is marked %q, want nothing", got)
	}
	// And the mark explains itself where the reviewer is looking, rather than
	// in a legend they have to remember.
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat,
		"! every observation behind that row is a file's modification time") {
		t.Errorf("the mark is drawn and never explained:\n%s", out)
	}
}

// The mark is a MARK. Every figure the day carried before it is unchanged, and
// the weak row is still drawn with its own clock.
func TestMarkingARowWeakRemovesNothingFromTheDay(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "", "-date", "2026-07-12", "-print")
	if err != nil {
		t.Fatalf("review -print: %v", err)
	}
	if !strings.Contains(out, "seedwork") {
		t.Fatalf("the weak row was dropped from the day:\n%s", out)
	}
	if !strings.Contains(out, "2 sittings") {
		t.Errorf("the day no longer reports its two sittings:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, "seedwork") && !strings.ContainsRune(line, '█') {
			t.Errorf("the weak row claims no clock at all: %q", line)
		}
	}
}

// The queue says which days carry a weak row, so that a reviewer can see one
// coming rather than finding out after opening it.
func TestTheQueueSaysWhichDaysCarryAWeakRow(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)
	out, err := reviewRun(t, path, cfg, "", "-list")
	if err != nil {
		t.Fatalf("review -list: %v", err)
	}
	if !strings.Contains(out, "! 1 of 1 row weak") {
		t.Errorf("the queue does not say the day rests on file timestamps:\n%s", out)
	}
	if flat := strings.Join(strings.Fields(out), " "); !strings.Contains(flat,
		"It is a mark and not a filter") {
		t.Errorf("the queue marks a day weak without saying nothing drops it:\n%s", out)
	}
}

// THREE ANSWERS ON THE DAY SCREEN. A day whose evidence nobody read has no
// rows, and saying "no source that names a project saw any of this day" about
// it would report an absence as an observation — this project's cardinal bug,
// and the reason the queue's days are skeletons rather than empty days.
func TestADayWhoseEvidenceWasNotReadIsNotReportedAsUnattributable(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	derivation, err := st.Sittings(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	cal := sessions.Calendar{Location: time.UTC, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday}
	skeleton, err := dayview.Skeleton(cal, sessions.Day{Year: 2026, Month: time.July, Date: 12},
		derivation, nil, nil)
	if err != nil {
		t.Fatalf("Skeleton: %v", err)
	}

	var out bytes.Buffer
	sess := &reviewSession{out: &out, cal: cal, derivation: derivation, width: 60}
	sess.draw(skeleton, false)
	flat := strings.Join(strings.Fields(out.String()), " ")
	if strings.Contains(flat, "no source that names a project saw any of this day") {
		t.Errorf("a day nobody read the evidence for is reported as unattributable:\n%s", out.String())
	}
	if !strings.Contains(flat, "observations were not read") {
		t.Errorf("the day does not say its evidence went unread:\n%s", out.String())
	}
	_ = cfg
}

// THE THIRD STATE ON THE QUEUE, tested where it lives rather than through a
// line of output that cannot currently produce it.
//
// A day carrying no derivation is a day nothing could be weighed for. It must
// come back ABSENT from weighEach — which the listing draws as "(not weighed)"
// — rather than as an empty map, which means "weighed, and nothing was weak".
// Collapsing those two is this project's cardinal bug, and the queue's own days
// all carry a derivation, so nothing else would ever catch it.
func TestADayWithNoDerivationIsNotWeighedRatherThanWeighedEmpty(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 14)
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer st.Close()
	ctx := context.Background()
	derivation, err := st.Sittings(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	cal := sessions.Calendar{Location: time.UTC, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday}
	seeded := sessions.Day{Year: 2026, Month: time.July, Date: 12}
	weighable, err := dayview.Skeleton(cal, seeded, derivation, nil, nil)
	if err != nil {
		t.Fatalf("Skeleton: %v", err)
	}
	// Another day, assembled against a derivation that was never computed. A
	// different date, because the result is keyed by day and one key cannot
	// hold both answers.
	bare, err := dayview.Skeleton(cal, sessions.Day{Year: 2026, Month: time.July, Date: 13},
		sittings.Derived{}, nil, nil)
	if err != nil {
		t.Fatalf("Skeleton without a derivation: %v", err)
	}
	if bare.Derived() {
		t.Fatal("a day built from an uncomputed derivation says it was derived")
	}

	sess := &reviewSession{out: &bytes.Buffer{}, store: st, cal: cal, derivation: derivation}
	got, err := sess.weighEach(ctx, []dayview.Day{weighable, bare})
	if err != nil {
		t.Fatalf("weighEach: %v", err)
	}
	if _, present := got[bare.Day]; present {
		t.Errorf("the underived day was weighed, so a reader cannot tell 'nothing was weak' "+
			"from 'nothing was read': %v", got)
	}
	if w, present := got[weighable.Day]; !present || w == nil {
		t.Errorf("the derived day came back unweighed: %v", got)
	}
	_ = cfg
}

// And nothing is read at all when no offered day carries a derivation: the
// query is skipped rather than run over a zero time range, which on a store
// with no filter bounds would read every event there is.
func TestWeighingNothingReadsNothing(t *testing.T) {
	cal := sessions.Calendar{Location: time.UTC, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday}
	bare, err := dayview.Skeleton(cal, sessions.Day{Year: 2026, Month: time.July, Date: 12},
		sittings.Derived{}, nil, nil)
	if err != nil {
		t.Fatalf("Skeleton: %v", err)
	}
	// A nil store, so a read of any kind panics rather than merely being slow.
	sess := &reviewSession{out: &bytes.Buffer{}, cal: cal}
	got, err := sess.weighEach(context.Background(), []dayview.Day{bare})
	if err != nil {
		t.Fatalf("weighEach: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("weighEach returned %v for a day it could not weigh", got)
	}
}

// THE MARK MAY NOT COST THE TIMELINE A CELL. The day screen was fitted to the
// terminal in commit 9d87d98, and a column added beside the labels has to come
// out of the timeline's budget rather than out of the terminal's — otherwise
// every marked row wraps, or clipTo eats the last cell of the day and a
// reviewer reads four in the morning as empty when nobody drew it.
//
// COUNTED IN RUNES. The timeline is drawn in multi-byte block characters and a
// previous session measured 102 columns where the truth was 126.
func TestAMarkedRowStillFitsTheTerminalAtEveryWidth(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-12"}, 9, 12, 14, 17)
	for _, cols := range []int{60, 80, 100, 120, 126, 200} {
		var out bytes.Buffer
		if err := reviewTo(atWidth{&out, cols}, strings.NewReader(""),
			[]string{"-store", path, "-config", cfg, "-date", "2026-07-12", "-print"}); err != nil {
			t.Fatalf("review -print at %d columns: %v", cols, err)
		}
		got := out.String()
		if !strings.Contains(got, "seedwork") {
			t.Fatalf("no weak row to measure at %d columns:\n%s", cols, got)
		}
		fits(t, cols, fmt.Sprintf("review at %d columns", cols), got)

		// And the row was not clipped: an ellipsis in a timeline is a cell of
		// the day the reviewer never saw.
		for line := range strings.SplitSeq(got, "\n") {
			if strings.HasPrefix(line, "seedwork") && strings.Contains(line, "…") {
				t.Errorf("at %d columns the marked row was clipped: %q", cols, line)
			}
		}
		// The marked row and the unmarked heading above it are the same width,
		// so the timeline keeps one left edge for the whole screen.
		var sittings, marked int
		for line := range strings.SplitSeq(got, "\n") {
			switch {
			case strings.HasPrefix(line, "sittings "):
				sittings = len([]rune(line))
			case strings.HasPrefix(line, "seedwork"):
				marked = len([]rune(line))
			}
		}
		if sittings != marked {
			t.Errorf("at %d columns the sittings row is %d runes and the marked row %d; "+
				"the timeline has two left edges", cols, sittings, marked)
		}
	}
}

// A DAY CONFIRMED WHILE IT WAS STILL HAPPENING COMES BACK AROUND.
//
// dayview.Confirm leaves the hours after the moment of review unexamined rather
// than asserting them empty, so the queue must ask whether the WHOLE day was
// looked at. Asking merely whether anybody looked would take today off the
// queue for ever and its evening would never be offered — which is the same
// collapse of "nothing recorded" into "observed absent" this project keeps
// shipping, arriving through the review queue instead of through a source.
func TestADayExaminedOnlyInPartIsOfferedAgainAndSaysSoInTheListing(t *testing.T) {
	cal := sessions.Calendar{Location: time.UTC, DayStart: sessions.Clock{Hour: 5}, WeekStart: time.Monday}
	day := sessions.Day{Year: 2026, Month: time.July, Date: 28}
	start, end := cal.Bounds(day)

	block := sittings.Sitting{Start: start.Add(4 * time.Hour), End: start.Add(6 * time.Hour), Events: 4}
	derivation := sittings.Derived{
		Params:     sittings.Params{Gap: 15 * time.Minute, LeadIn: 5 * time.Minute, TailOut: 5 * time.Minute},
		ComputedAt: start, Sittings: []sittings.Sitting{block},
	}

	// Examined as far as lunchtime, which is what confirming today produces.
	part := adjustments.Review{
		Start: start, End: start.Add(8 * time.Hour), Verdict: adjustments.Recalled,
		Origin: "a-person", RecordedAt: start.Add(8 * time.Hour),
	}
	partly, err := dayview.Assemble(cal, day, derivation, nil, []adjustments.Review{part}, nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if got := reviewOffered([]dayview.Day{partly}, false, 20, false); len(got) != 1 {
		t.Error("a day examined only as far as lunchtime is not offered again, so its evening " +
			"can never be reviewed")
	}
	if got := reviewSayCoverage(partly); !strings.Contains(got, "part of the day") {
		t.Errorf("the listing says %q for a day only partly examined, which reads as a whole "+
			"day agreed with", got)
	}

	// And the whole day, examined end to end, still drops off the queue.
	whole := part
	whole.End = end
	fully, err := dayview.Assemble(cal, day, derivation, nil, []adjustments.Review{whole}, nil)
	if err != nil {
		t.Fatalf("Assemble: %v", err)
	}
	if got := reviewOffered([]dayview.Day{fully}, false, 20, false); len(got) != 0 {
		t.Error("a day examined end to end is offered again")
	}
	if got := reviewSayCoverage(fully); strings.Contains(got, "part of the day") {
		t.Errorf("the listing says %q for a day examined end to end", got)
	}
}

// WHETHER A PERSON CAN ACTUALLY PRODUCE AN ATTRIBUTION ASSERTION.
//
// The store's schema has carried 'attributed' and 'unattributed' since
// migration 0008, internal/dayview builds both from a Judgement, and its own
// tests cover that. None of that is evidence that a reviewer sitting at the
// prompt can reach either one: the write path existing in code and the write
// path being reachable through the keys the command actually offers are
// different claims, and the second one had never been checked. The live store
// holds no attribution assertion of either kind, so nothing about the corpus
// distinguishes "nobody has needed to reattribute a block" from "there is no
// way to".
//
// This drives the command the way a person does — one key at a time, through
// the block walk — so that the answer is established rather than inferred from
// the presence of a case in a switch. It is deliberately end to end: the same
// question asked of dayview.Confirm alone is the question that was already
// answered.
func TestAReviewerCanReattributeABlockThroughTheKeysTheCommandOffers(t *testing.T) {
	// n walks the blocks, p asks who the block belonged to, and the blank line
	// after the name settles the block — reattributing does not end the
	// conversation about it, so the Enter is part of the script and not padding.
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	out, err := reviewRun(t, path, cfg, "n\np\nbirch\n\n",
		"-date", "2026-07-11", "-origin", "a-person", "-drill-every", "0")
	if err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}

	_, adjs := reviewRecorded(t, path)
	var attributed []adjustments.Adjustment
	for _, a := range adjs {
		if a.Assertion == adjustments.Attributed {
			attributed = append(attributed, a)
		}
	}
	if len(attributed) != 1 {
		t.Fatalf("%d attribution assertions came out of a reattributed block, want 1; the "+
			"transcript was:\n%s", len(attributed), out)
	}
	if attributed[0].Project != "birch" {
		t.Errorf("the assertion names project %q, want the one that was typed", attributed[0].Project)
	}
	if attributed[0].Start.IsZero() || !attributed[0].End.After(attributed[0].Start) {
		t.Errorf("the assertion covers %v..%v, which is not a span of work",
			attributed[0].Start, attributed[0].End)
	}
}

// The other half of the same question. Answering the project prompt with
// nothing asserts the block belonged to NO project, which is a thing a person
// can say and is not the same as saying nothing at all — and if the blank
// answer were dropped on the floor the two would be indistinguishable in the
// store.
func TestAReviewerCanAssertABlockBelongedToNoProjectAtAll(t *testing.T) {
	path, cfg := reviewSeed(t, []string{"2026-07-11"}, 9)
	out, err := reviewRun(t, path, cfg, "n\np\n\n\n",
		"-date", "2026-07-11", "-origin", "a-person", "-drill-every", "0")
	if err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}

	_, adjs := reviewRecorded(t, path)
	var unattributed int
	for _, a := range adjs {
		if a.Assertion == adjustments.Unattributed {
			unattributed++
		}
	}
	if unattributed != 1 {
		t.Fatalf("%d assertions of belonging to no project, want 1; the transcript was:\n%s",
			unattributed, out)
	}
}
