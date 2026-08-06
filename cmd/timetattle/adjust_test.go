package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/store"
)

// Helpers here are prefixed adjust, for the reason main.go's package doc gives.

func adjustStore(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	st.Close()
	return path
}

// adjustRun drives the subcommand and returns what it printed.
func adjustRun(t *testing.T, path string, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := adjustTo(&out, append([]string{"-store", path}, args...))
	return out.String(), err
}

func adjustRead(t *testing.T, path string) ([]adjustments.Review, []adjustments.Adjustment) {
	t.Helper()
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer st.Close()
	reviews, err := st.Reviews(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	adjs, err := st.Adjustments(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	return reviews, adjs
}

func TestAdjustRecordsACorrectionOverASpan(t *testing.T) {
	path := adjustStore(t)
	out, err := adjustRun(t, path,
		"-from", "2026-07-28T12:00:00", "-to", "2026-07-28T13:00:00",
		"-assert", "not-engaged", "-origin", "a-person", "-note", "a meeting")
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if !strings.Contains(out, "was not work") {
		t.Errorf("the command did not say what it recorded:\n%s", out)
	}

	_, adjs := adjustRead(t, path)
	if len(adjs) != 1 {
		t.Fatalf("the store holds %d corrections, want 1", len(adjs))
	}
	a := adjs[0]
	if a.Assertion != adjustments.NotEngaged {
		t.Errorf("the assertion is %q", a.Assertion)
	}
	if a.Origin != "a-person" {
		t.Errorf("the origin is %q", a.Origin)
	}
	if a.Note != "a meeting" {
		t.Errorf("the note is %q", a.Note)
	}
	if a.RecordedAt.IsZero() {
		t.Error("the correction records no time, so criterion RLN is unmet")
	}
	if a.End.Sub(a.Start) != time.Hour {
		t.Errorf("the span is %v, want 1h", a.End.Sub(a.Start))
	}
}

func TestAdjustRecordsAnAttribution(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T09:00:00", "-to", "2026-07-28T11:00:00",
		"-assert", "attributed", "-project", "some-project", "-origin", "a-person"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	_, adjs := adjustRead(t, path)
	if len(adjs) != 1 || adjs[0].Project != "some-project" {
		t.Fatalf("the store holds %v", adjs)
	}
}

func TestAdjustRefusesAProjectOnAnAssertionThatCarriesNone(t *testing.T) {
	path := adjustStore(t)
	_, err := adjustRun(t, path,
		"-from", "2026-07-28T09:00:00", "-to", "2026-07-28T11:00:00",
		"-assert", "engaged", "-project", "some-project", "-origin", "a-person")
	if err == nil {
		t.Fatal("a project on an engaged assertion was accepted; it would be stored and never read")
	}
	if _, adjs := adjustRead(t, path); len(adjs) != 0 {
		t.Errorf("the refusal still wrote %d rows", len(adjs))
	}
}

// A boundary takes -from alone. Requiring -to for it would make a person invent
// an end for an instant.
func TestAdjustRecordsABoundaryFromOneInstant(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T12:00:00", "-assert", "boundary", "-origin", "a-person"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	_, adjs := adjustRead(t, path)
	if len(adjs) != 1 {
		t.Fatalf("the store holds %d corrections, want 1", len(adjs))
	}
	if !adjs[0].Start.Equal(adjs[0].End) {
		t.Errorf("the boundary spans %v..%v, want one instant", adjs[0].Start, adjs[0].End)
	}
}

func TestAdjustRefusesABoundaryAcrossASpan(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T12:00:00", "-to", "2026-07-28T13:00:00",
		"-assert", "boundary", "-origin", "a-person"); err == nil {
		t.Fatal("a boundary across a span was accepted")
	}
}

func TestAdjustRefusesAnAssertionAcrossNoTime(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T12:00:00", "-assert", "engaged", "-origin", "a-person"); err == nil {
		t.Fatal("an engaged assertion with no -to was accepted; it asserts nothing")
	}
}

// THE THIRD STATE, through the CLI. A day reviewed and not remembered has to be
// recordable, and it must not look like a day nobody has opened.
func TestAdjustRecordsThatASpanWasReviewedAndNotRemembered(t *testing.T) {
	path := adjustStore(t)
	out, err := adjustRun(t, path,
		"-from", "2026-07-28", "-to", "2026-07-29", "-review", "unremembered", "-origin", "a-person")
	if err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if !strings.Contains(out, "unremembered") {
		t.Errorf("the command did not say what it recorded:\n%s", out)
	}

	reviews, adjs := adjustRead(t, path)
	if len(reviews) != 1 || reviews[0].Verdict != adjustments.Unremembered {
		t.Fatalf("the store holds %v", reviews)
	}
	if len(adjs) != 0 {
		t.Errorf("recording an unremembered review wrote %d corrections; it should assert nothing", len(adjs))
	}
}

func TestAdjustRecordsAReviewAndTheCorrectionItProduced(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T09:00:00", "-to", "2026-07-28T17:00:00",
		"-review", "recalled", "-assert", "engaged", "-origin", "a-person"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	reviews, adjs := adjustRead(t, path)
	if len(reviews) != 1 || len(adjs) != 1 {
		t.Fatalf("the store holds %d reviews and %d corrections, want 1 and 1", len(reviews), len(adjs))
	}
	if adjs[0].ReviewID != reviews[0].ID {
		t.Errorf("the correction points at review %d, want %d", adjs[0].ReviewID, reviews[0].ID)
	}
}

func TestAdjustRefusesACorrectionUnderAnUnrememberedReview(t *testing.T) {
	path := adjustStore(t)
	_, err := adjustRun(t, path,
		"-from", "2026-07-28T09:00:00", "-to", "2026-07-28T17:00:00",
		"-review", "unremembered", "-assert", "engaged", "-origin", "a-person")
	if err == nil {
		t.Fatal("a correction was recorded under a review that remembered nothing")
	}
	// The review itself is still recorded: the person did look, and that is
	// true regardless of what they then tried to assert.
	reviews, adjs := adjustRead(t, path)
	if len(reviews) != 1 {
		t.Errorf("the store holds %d reviews, want 1", len(reviews))
	}
	if len(adjs) != 0 {
		t.Errorf("the store holds %d corrections, want 0", len(adjs))
	}
}

// A correction that names nobody cannot be traced back, which is criterion RLN.
func TestAdjustRefusesAnAnonymousCorrection(t *testing.T) {
	path := adjustStore(t)
	t.Setenv("USER", "")
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T09:00:00", "-to", "2026-07-28T11:00:00", "-assert", "engaged"); err == nil {
		t.Fatal("an anonymous correction was recorded")
	}
}

func TestAdjustTakesTheOriginFromTheEnvironment(t *testing.T) {
	path := adjustStore(t)
	t.Setenv("USER", "a-person")
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T09:00:00", "-to", "2026-07-28T11:00:00", "-assert", "engaged"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if _, adjs := adjustRead(t, path); len(adjs) != 1 || adjs[0].Origin != "a-person" {
		t.Fatalf("the store holds %v", adjs)
	}
}

// Changing your mind is a new row, and both rows survive. This is what makes
// criterion RLN mean something over history rather than over a latest state.
func TestChangingYourMindAppendsRatherThanReplacing(t *testing.T) {
	path := adjustStore(t)
	for _, assertion := range []string{"not-engaged", "engaged"} {
		if _, err := adjustRun(t, path,
			"-from", "2026-07-28T12:00:00", "-to", "2026-07-28T13:00:00",
			"-assert", assertion, "-origin", "a-person"); err != nil {
			t.Fatalf("adjust %s: %v", assertion, err)
		}
	}

	_, adjs := adjustRead(t, path)
	if len(adjs) != 2 {
		t.Fatalf("the store holds %d corrections, want 2 — the earlier judgment must survive", len(adjs))
	}
	// And the fold takes the later one.
	machine := []adjustments.Span{{
		Start: adjs[0].Start.Add(-3 * time.Hour), End: adjs[0].End.Add(3 * time.Hour),
	}}
	got := adjustments.Fold(nil, adjs).Engaged(machine)
	if len(got) != 1 {
		t.Errorf("the fold produced %d spans, want 1 — the later engaged assertion should have won", len(got))
	}
}

// -list has to distinguish nothing-recorded from nothing-wrong, out loud.
func TestListSaysSoWhenNothingHasBeenReviewed(t *testing.T) {
	path := adjustStore(t)
	out, err := adjustRun(t, path, "-list")
	if err != nil {
		t.Fatalf("adjust -list: %v", err)
	}
	if !strings.Contains(out, "nothing has been reviewed") {
		t.Errorf("-list on an empty store printed:\n%s", out)
	}
	if !strings.Contains(out, "not the same as") {
		t.Errorf("-list did not distinguish an unexamined span from a clean one:\n%s", out)
	}
}

func TestListReportsWhatWasRecorded(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28T12:00:00", "-to", "2026-07-28T13:00:00",
		"-assert", "not-engaged", "-origin", "a-person", "-note", "a meeting"); err != nil {
		t.Fatalf("adjust: %v", err)
	}
	if _, err := adjustRun(t, path,
		"-from", "2026-07-28", "-to", "2026-07-29", "-review", "unremembered", "-origin", "a-person"); err != nil {
		t.Fatalf("adjust -review: %v", err)
	}

	out, err := adjustRun(t, path, "-list")
	if err != nil {
		t.Fatalf("adjust -list: %v", err)
	}
	for _, want := range []string{"was not work", "a meeting", "a-person", "unremembered", "Reviewed and not remembered"} {
		if !strings.Contains(out, want) {
			t.Errorf("-list did not mention %q:\n%s", want, out)
		}
	}
}

func TestListWritesNothing(t *testing.T) {
	path := adjustStore(t)
	if _, err := adjustRun(t, path, "-list", "-assert", "engaged"); err == nil {
		t.Fatal("-list was combined with -assert; one reports and the other writes")
	}
}

// The help text and docs/content/docs/adjustments.md advertise particular date
// forms, and this command does not own the parser: -from goes through the
// shared parseBoundary, so a form that looks reasonable is not necessarily one
// that works. The first draft of the help text advertised "2026-07-28T12:00",
// which parseBoundary rejects — caught by running the binary, not by any test.
// This is that test.
func TestEveryDateFormTheHelpTextAdvertisesParses(t *testing.T) {
	path := adjustStore(t)
	for _, form := range []string{
		"2026-07-28",
		"2026-07-28 09:00",
		"2026-07-28T09:00:00",
		"2026-07-28T09:00:00Z",
	} {
		if _, err := adjustRun(t, path, "-from", form, "-assert", "boundary", "-origin", "a-person"); err != nil {
			t.Errorf("-from %q is advertised but not accepted: %v", form, err)
		}
	}
}

func TestAdjustWithNothingToDoSaysSo(t *testing.T) {
	path := adjustStore(t)
	_, err := adjustRun(t, path, "-from", "2026-07-28", "-to", "2026-07-29")
	if err == nil {
		t.Fatal("a call with neither -assert nor -review nor -list was accepted")
	}
	if !strings.Contains(err.Error(), "-assert") {
		t.Errorf("the error does not say what to do instead: %v", err)
	}
}
