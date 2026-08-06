package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// fitReviewed is one reviewed day and what the reviewer was looking at when
// they answered.
type fitReviewed struct {
	Date  time.Time
	Shown adjustments.Shown
}

// fitShownSeed builds a store holding one reviewed day per spec, each with the
// same three observations and the same wholesale tiling, differing only in what
// the reviewer had in front of them.
//
// Two specs on the SAME date is the case where a day already confirmed is later
// reviewed blind, which is the workflow `review -blind` invites and the one the
// pooled residual cannot survive.
func fitShownSeed(t *testing.T, days ...fitReviewed) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "fs-observations", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	var events []source.Event
	seen := map[time.Time]bool{}
	for _, d := range days {
		if seen[d.Date] {
			continue
		}
		seen[d.Date] = true
		for _, offset := range []time.Duration{9 * time.Hour, 9*time.Hour + 2*time.Minute, 12 * time.Hour} {
			e := source.NewPoint(d.Date.Add(offset), source.Identity{Document: "a.motn"}, "fs-observations")
			e.Locator = "/tmp/seed/a.motn"
			events = append(events, e)
		}
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}

	for _, d := range days {
		recorded := d.Date.Add(30 * time.Hour)
		review, err := st.RecordReview(ctx, adjustments.Review{
			Start: d.Date, End: d.Date.Add(24 * time.Hour), Verdict: adjustments.Recalled,
			Shown: d.Shown, Origin: "test", RecordedAt: recorded,
		})
		if err != nil {
			t.Fatalf("record a %q review: %v", d.Shown, err)
		}
		for _, a := range []struct {
			from, to time.Duration
			engaged  bool
		}{
			{0, 8*time.Hour + 55*time.Minute, false},
			{8*time.Hour + 55*time.Minute, 9*time.Hour + 7*time.Minute, true},
			{9*time.Hour + 7*time.Minute, 11*time.Hour + 55*time.Minute, false},
			{11*time.Hour + 55*time.Minute, 12*time.Hour + 5*time.Minute, true},
			{12*time.Hour + 5*time.Minute, 24 * time.Hour, false},
		} {
			assertion := adjustments.NotEngaged
			if a.engaged {
				assertion = adjustments.Engaged
			}
			if _, err := st.RecordAdjustment(ctx, adjustments.Adjustment{
				ReviewID: review, Start: d.Date.Add(a.from), End: d.Date.Add(a.to),
				Assertion: assertion, Origin: "test", RecordedAt: recorded,
			}); err != nil {
				t.Fatalf("record an assertion: %v", err)
			}
		}
	}
	return path
}

func fitDate(day int) time.Time { return time.Date(2026, 7, day, 0, 0, 0, 0, time.UTC) }

func fitGrid() []string {
	return []string{"-gaps", "11m,15m,20m", "-lead-ins", "5m", "-tail-outs", "5m"}
}

// THE FAILURE THIS TASK EXISTS TO PREVENT, and it is the one that looks like
// success. There are no blind labels in the live store at all, so a fit
// restricted to them scores a residual of 0.000h over zero labels — which reads
// exactly like a perfect fit and is a statement about nothing.
func TestFitRefusesABlindFitWhenNothingWasReviewedBlind(t *testing.T) {
	var out strings.Builder
	args := append([]string{"-store", fitShownSeed(t, fitReviewed{Date: fitDate(11)}),
		"-config", fitConfig(t), "-shown", "blind"}, fitGrid()...)

	err := fitCommandTo(&out, args)
	if err == nil {
		t.Fatal("fit reported a blind fit against a store holding no blind label at all")
	}
	// "no blind labels" and "no labels" call for different actions, so the
	// complaint has to carry both counts rather than only the missing one.
	for _, want := range []string{"blind", "0", "5"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the complaint does not carry %q: %v", want, err)
		}
	}
	if strings.Contains(out.String(), "residual") {
		t.Errorf("a residual was printed for a population nobody recorded:\n%s", out.String())
	}
}

// Criterion aYe: a fit can be run against blind labels alone.
func TestFitRunsAgainstBlindLabelsAlone(t *testing.T) {
	path := fitShownSeed(t,
		fitReviewed{Date: fitDate(11)},
		fitReviewed{Date: fitDate(12), Shown: adjustments.Observations})

	out := fitRun(t, append([]string{"-store", path, "-config", fitConfig(t),
		"-shown", "blind"}, fitGrid()...)...)

	if !strings.Contains(out, "2026-07-12") {
		t.Errorf("the blind-only run does not name the blind day:\n%s", out)
	}
	if strings.Contains(out, "2026-07-11") {
		t.Errorf("the blind-only run scored the confirming day as well:\n%s", out)
	}
	if !strings.Contains(out, "residual") {
		t.Errorf("the blind-only run reported no residual at all:\n%s", out)
	}
}

// Criterion fe0: the two populations are reported separately rather than pooled
// silently. The census is printed whether or not a population holds anything,
// because "nothing was recorded blind" is the finding on this store.
func TestFitNamesThePopulationSplitRatherThanPoolingSilently(t *testing.T) {
	path := fitShownSeed(t,
		fitReviewed{Date: fitDate(11)},
		fitReviewed{Date: fitDate(12), Shown: adjustments.Observations})

	out := fitRun(t, append([]string{"-store", path, "-config", fitConfig(t)}, fitGrid()...)...)

	for _, want := range []string{
		"WHAT THE REVIEWER WAS SHOWN",
		"blind",
		"confirming a derivation",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not carry %q:\n%s", want, out)
		}
	}
	// And the pooled figure has to be readable as a weighted average of the two
	// rather than as one measurement.
	if !strings.Contains(out, "of the labelled time") {
		t.Errorf("the report does not say how much of the labelled time each population is:\n%s", out)
	}
}

// A store where nothing was reviewed blind must SAY so rather than leaving the
// population out of the report, which is the same "absence of a row is not an
// observation" rule the census exists for.
func TestFitSaysWhenNoLabelWasProducedBlind(t *testing.T) {
	out := fitRun(t, append([]string{"-store", fitShownSeed(t, fitReviewed{Date: fitDate(11)}),
		"-config", fitConfig(t)}, fitGrid()...)...)

	if !strings.Contains(out, "no label in this fit was produced blind") {
		t.Errorf("the report does not say the blind population is empty:\n%s", out)
	}
}

// A day reviewed BOTH ways is two tilings of the same 24 hours. Pooling them
// counts that day twice in the denominator, so the pooled residual is not a
// residual over a partition at all and is refused by name.
func TestFitRefusesToPoolTwoPopulationsThatLabelTheSameTime(t *testing.T) {
	path := fitShownSeed(t,
		fitReviewed{Date: fitDate(11)},
		fitReviewed{Date: fitDate(11), Shown: adjustments.Observations})

	err := fitCommandTo(&strings.Builder{},
		append([]string{"-store", path, "-config", fitConfig(t)}, fitGrid()...))
	if err == nil {
		t.Fatal("fit pooled two labellings of the same day")
	}
	for _, want := range []string{"2026-07-11", "-shown"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the complaint does not carry %q: %v", want, err)
		}
	}

	// And each population on its own is fitted perfectly happily.
	for _, shown := range []string{"derivation", "blind"} {
		out := fitRun(t, append([]string{"-store", path, "-config", fitConfig(t),
			"-shown", shown}, fitGrid()...)...)
		if !strings.Contains(out, "residual") {
			t.Errorf("-shown %s reported no residual:\n%s", shown, out)
		}
	}
}

func TestFitRefusesAPopulationItDoesNotRecognise(t *testing.T) {
	err := fitCommandTo(&strings.Builder{}, append([]string{
		"-store", fitShownSeed(t, fitReviewed{Date: fitDate(11)}),
		"-config", fitConfig(t), "-shown", "whatever"}, fitGrid()...))
	if err == nil {
		t.Fatal("fit accepted a -shown value that names no population")
	}
	if !strings.Contains(err.Error(), "-shown") {
		t.Errorf("the complaint does not name the flag: %v", err)
	}
}
