package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// seedWorkingDays writes four days of observations in America/Chicago, chosen
// so that every case the boundary exists for appears exactly once:
//
//	Mon 2026-07-06 10:00-11:00   an ordinary daytime sitting
//	Tue 2026-07-07               nothing at all, inside the observed span
//	Wed 2026-07-08 23:30-00:30   crosses midnight, and is still Wednesday
//	Thu 2026-07-09 04:00-06:00   crosses 05:00, and is split by it
//
// With the shipped 5m lead-in and tail-out that is 1h10m on the Monday,
// 1h10m + 1h05m on the Wednesday and 1h05m on the Thursday: 4h30m in all.
func seedWorkingDays(t *testing.T) string {
	t.Helper()
	loc, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skipf("America/Chicago is not in this machine's zoneinfo: %v", err)
	}
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open a store to seed: %v", err)
	}
	defer st.Close()

	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "biome-appusage", Input: "/tmp/seed"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	at := func(m time.Month, d, hh, mm int) time.Time {
		return time.Date(2026, m, d, hh, mm, 0, 0, loc)
	}
	events := []source.Event{
		source.NewInterval(at(time.July, 6, 10, 0), at(time.July, 6, 11, 0),
			source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
		source.NewInterval(at(time.July, 8, 23, 30), at(time.July, 9, 0, 30),
			source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
		source.NewInterval(at(time.July, 9, 4, 0), at(time.July, 9, 6, 0),
			source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	return path
}

// runSessionize captures what the command printed, so a report can be asserted
// on rather than merely not crashing.
func runSessionize(t *testing.T, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := sessionizeTo(&out, args); err != nil {
		t.Fatalf("sessionize %v: %v\n%s", args, err, out.String())
	}
	return out.String()
}

func TestByDayReportsEachWorkingDaySeparately(t *testing.T) {
	path := seedWorkingDays(t)
	out := runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-day", "-dry-run")

	for _, want := range []string{"2026-07-06", "2026-07-07", "2026-07-08", "2026-07-09"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not mention working day %s:\n%s", want, out)
		}
	}
}

// The Wednesday holds its own evening AND the hour before Thursday's boundary:
// 1h10m plus 1h05m. If the sitting that ran to 00:30 had been booked to the
// Thursday this would read 1h05m.
func TestByDayKeepsLateNightWorkOnTheDayItBeganOn(t *testing.T) {
	path := seedWorkingDays(t)
	out := runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-day", "-dry-run")

	// 09:55-11:05 on the Monday; 23:25-00:35 plus 03:55-05:00 on the
	// Wednesday; 05:00-06:05 on the Thursday.
	for _, want := range []struct{ day, engaged string }{
		{"2026-07-06", "1h10m"},
		{"2026-07-08", "2h15m"},
		{"2026-07-09", "1h5m"},
	} {
		line := lineFor(out, want.day)
		if line == "" {
			t.Errorf("no line for %s:\n%s", want.day, out)
			continue
		}
		if !strings.Contains(line, want.engaged) {
			t.Errorf("%s reads %q, want %s of engaged time", want.day, line, want.engaged)
		}
	}
}

// A working day nobody worked on and a working day nobody recorded are
// different answers, and the report must say which it is looking at.
func TestByDayDistinguishesNoWorkFromNoObservations(t *testing.T) {
	path := seedWorkingDays(t)
	out := runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-day", "-dry-run")

	tuesday := lineFor(out, "2026-07-07")
	if tuesday == "" {
		t.Fatalf("2026-07-07 is missing; a day with no work reads as a day nobody harvested:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(tuesday), "no work") {
		t.Errorf("2026-07-07 reads %q; it was observed and held no work, and should say so", tuesday)
	}
	// Nothing was recorded on the 10th, so it must not appear with a zero
	// against it — a zero there would be a claim nobody measured.
	if lineFor(out, "2026-07-10") != "" {
		t.Errorf("2026-07-10 appeared, but nothing was recorded on it:\n%s", out)
	}
}

// The invariant the whole design rests on, asserted at the level a person
// reads: the per-day figures add up to the ungrouped total, and the report
// says so rather than leaving it to be checked by hand.
func TestByDayStatesThatItConservesTheTotal(t *testing.T) {
	path := seedWorkingDays(t)
	out := runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-day", "-dry-run")

	// The report opens with the store path, which under `go test` sits in a
	// temporary directory NAMED AFTER THIS TEST. Matching against the whole
	// output therefore matches the test's own name — which is exactly how this
	// assertion passed before the feature existed. Only the report is
	// searched.
	body := withoutStorePath(out, path)
	if !strings.Contains(body, "4h30m") {
		t.Errorf("the report does not state the 4h30m total:\n%s", body)
	}
	if !strings.Contains(body, "ungrouped") {
		t.Errorf("the report never reconciles the per-day sum against the ungrouped total:\n%s", body)
	}
}

// withoutStorePath drops every line mentioning the store's path, so that an
// assertion about the report cannot be satisfied by the temporary directory
// the test is running in.
func withoutStorePath(out, path string) string {
	var kept []string
	for line := range strings.SplitSeq(out, "\n") {
		if !strings.Contains(line, path) && !strings.Contains(line, filepath.Dir(path)) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// The boundary is a parameter, and moving it must visibly move work. At
// midnight the Wednesday loses its half hour after midnight and the Thursday
// gains everything from 03:55.
func TestTheDayStartFlagMovesWorkBetweenDays(t *testing.T) {
	path := seedWorkingDays(t)
	five := runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-day", "-dry-run")
	midnight := runSessionize(t, "-store", path, "-timezone", "America/Chicago",
		"-day-start", "00:00", "-by-day", "-dry-run")

	if five == midnight {
		t.Fatal("moving the boundary to midnight changed nothing; the flag is not wired to anything")
	}
	// The Wednesday keeps only 23:25-00:00, which is 35 minutes; the Thursday
	// takes 00:00-00:35 and the whole of 03:55-06:05, which is 2h45m. At 05:00
	// those same sittings read 2h15m and 1h5m, so the boundary moves 1h40m of
	// real work between two days on a corpus of three sittings.
	if got := lineFor(midnight, "2026-07-08"); !strings.Contains(got, "35m") {
		t.Errorf("at midnight the Wednesday reads %q, want 35m (23:25 to 00:00)", got)
	}
	if got := lineFor(midnight, "2026-07-09"); !strings.Contains(got, "2h45m") {
		t.Errorf("at midnight the Thursday reads %q, want 2h45m (00:00-00:35 and 03:55-06:05)", got)
	}
}

func TestTheWeekStartFlagIsWired(t *testing.T) {
	path := seedWorkingDays(t)
	monday := runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-week", "-dry-run")
	sunday := runSessionize(t, "-store", path, "-timezone", "America/Chicago",
		"-week-start", "sunday", "-by-week", "-dry-run")

	// 2026-07-06 is a Monday, so a Monday week begins on it and a Sunday week
	// on the 5th.
	if !strings.Contains(monday, "2026-07-06") {
		t.Errorf("with weeks from Monday the week is not labelled 2026-07-06:\n%s", monday)
	}
	if !strings.Contains(sunday, "2026-07-05") {
		t.Errorf("with weeks from Sunday the week is not labelled 2026-07-05:\n%s", sunday)
	}
}

// A misspelled boundary must stop the run. Falling back to the default would
// compute every figure from a boundary the command line contradicts.
func TestAnUnreadableDayStartFlagStopsTheRun(t *testing.T) {
	path := seedWorkingDays(t)
	for _, bad := range []string{"5", "25:00", "five"} {
		var out bytes.Buffer
		if err := sessionizeTo(&out, []string{"-store", path, "-day-start", bad, "-by-day", "-dry-run"}); err == nil {
			t.Errorf("-day-start %q was accepted", bad)
		}
	}
	var out bytes.Buffer
	if err := sessionizeTo(&out, []string{"-store", path, "-timezone", "Mars/Olympus", "-dry-run"}); err == nil {
		t.Error("-timezone Mars/Olympus was accepted")
	}
}

// -by-day is a way of reading the derivation, not of changing it, and the
// stored sessions must come out of it exactly as they went in.
func TestByDayDoesNotChangeTheStoredSessions(t *testing.T) {
	path := seedWorkingDays(t)
	if err := sessionizeTo(&bytes.Buffer{}, []string{"-store", path, "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}
	const fingerprint = `SELECT COALESCE(group_concat(quote(started_at)||quote(ended_at), char(10)), '')
		FROM derived_session ORDER BY id`
	before := query[string](t, path, fingerprint)
	if before == "" {
		t.Fatal("no sessions were stored; the comparison would be vacuous")
	}
	runSessionize(t, "-store", path, "-timezone", "America/Chicago", "-by-day", "-dry-run")
	if after := query[string](t, path, fingerprint); after != before {
		t.Error("-by-day -dry-run changed the stored sessions")
	}
}

// lineFor returns the reported line mentioning key, or "".
func lineFor(out, key string) string {
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, key) {
			return line
		}
	}
	return ""
}
