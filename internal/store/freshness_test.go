package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// How far the store has been ASKED, per source.
//
// It is the harvest half of a day's horizon. See internal/dayview/reach.go for
// why the derivation's half turned out to be the binding one on 2026-08-01 and
// this one was not; both are read, because which binds is a property of the day
// and not of the design.

func TestFreshnessIsTheLatestSUCCESSFULRunOfEachSource(t *testing.T) {
	st := freshStore(t)
	ctx := context.Background()

	// biome succeeds, then fails. The failure is the LATEST run and says
	// nothing whatever about coverage: a source that could not be read has not
	// been asked, so the last instant it covered is still the earlier success.
	harvested(t, st, "biome-appusage", "2026-08-01T13:00:00Z", "2026-08-01T13:00:10Z", "ok")
	harvested(t, st, "biome-appusage", "2026-08-01T17:00:00Z", "2026-08-01T17:00:01Z", "failed")
	harvested(t, st, "claude-transcripts", "2026-08-01T17:00:00Z", "2026-08-01T17:05:00Z", "ok")

	got, err := st.Freshness(ctx)
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	by := map[string]store.Fresh{}
	for _, f := range got {
		by[f.Source] = f
	}
	if len(by) != 2 {
		t.Fatalf("Freshness returned %d sources, want 2: %v", len(by), got)
	}
	if want := mustTime(t, "2026-08-01T13:00:10Z"); !by["biome-appusage"].LastHarvest.Equal(want) {
		t.Errorf("biome-appusage last harvested %s, want %s — the 17:00 run FAILED and covered nothing",
			by["biome-appusage"].LastHarvest, want)
	}
	if !by["biome-appusage"].Failing {
		t.Error("biome-appusage's latest run failed and Freshness did not say so")
	}
	if by["claude-transcripts"].Failing {
		t.Error("claude-transcripts is healthy and was reported as failing")
	}
}

// A source that has never succeeded has NO horizon, which is not the same as a
// horizon at the zero time. Reporting it as infinitely stale would pin every
// day in the store to 0001-01-01 and make the whole mechanism unusable; hiding
// it would read as fresh. It comes back with a zero LastHarvest and says so.
func TestASourceThatHasNeverSucceededHasNoHorizon(t *testing.T) {
	st := freshStore(t)
	harvested(t, st, "motion-autosave", "2026-08-01T13:00:00Z", "2026-08-01T13:00:01Z", "failed")

	got, err := st.Freshness(context.Background())
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("Freshness = %v, want the one source that has been tried", got)
	}
	if !got[0].LastHarvest.IsZero() {
		t.Errorf("LastHarvest = %s, want the zero time: this source has never been read through",
			got[0].LastHarvest)
	}
	if !got[0].Failing {
		t.Error("Failing = false on a source whose only run failed")
	}
}

// A run still marked 'running' has not finished, so it has covered nothing. The
// live store carries one stranded since 2026-08-01T13:07:13Z.
func TestARunningRunDoesNotCountAsCoverage(t *testing.T) {
	st := freshStore(t)
	harvested(t, st, "claude-transcripts", "2026-08-01T13:00:00Z", "2026-08-01T13:00:10Z", "ok")
	harvested(t, st, "claude-transcripts", "2026-08-01T17:00:00Z", "", "running")

	got, err := st.Freshness(context.Background())
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if want := mustTime(t, "2026-08-01T13:00:10Z"); !got[0].LastHarvest.Equal(want) {
		t.Errorf("LastHarvest = %s, want %s", got[0].LastHarvest, want)
	}
}

func TestAStoreNobodyHasHarvestedReportsNoSourcesRatherThanFreshOnes(t *testing.T) {
	got, err := freshStore(t).Freshness(context.Background())
	if err != nil {
		t.Fatalf("Freshness: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("Freshness = %v, want nothing at all", got)
	}
}

// The column added by migration 0011, round-tripped. A review recorded without
// saying reads back as a review of the derivation, which is what the 70 rows
// already in the live store are.
func TestWhatAReviewerWasShownSurvivesTheStore(t *testing.T) {
	st := freshStore(t)
	ctx := context.Background()
	for _, shown := range []adjustments.Shown{"", adjustments.Derivation, adjustments.Observations} {
		if _, err := st.RecordReview(ctx, adjustments.Review{
			Start:   mustTime(t, "2026-08-01T10:00:00Z"),
			End:     mustTime(t, "2026-08-01T18:00:00Z"),
			Verdict: adjustments.Recalled, Shown: shown,
			Origin: "ben", RecordedAt: mustTime(t, "2026-08-01T18:00:00Z"),
		}); err != nil {
			t.Fatalf("RecordReview shown=%q: %v", shown, err)
		}
	}
	back, err := st.Reviews(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	want := []adjustments.Shown{adjustments.Derivation, adjustments.Derivation, adjustments.Observations}
	if len(back) != len(want) {
		t.Fatalf("read back %d reviews, want %d", len(back), len(want))
	}
	for i := range want {
		if back[i].Saw() != want[i] {
			t.Errorf("review %d was shown %q, want %q", i, back[i].Saw(), want[i])
		}
	}
}

// harness is a store and the path underneath it, so a case can write history
// that the run API cannot express.
type harness struct {
	*store.Store
	path string
}

func freshStore(t *testing.T) *harness {
	t.Helper()
	path := t.TempDir() + "/timetattle.db"
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return &harness{Store: st, path: path}
}

// record writes one harvest_run row directly.
//
// Not through BeginRun and Finish, which stamp finished_at from the clock.
// These cases are about HISTORY — a run that failed at five o'clock after
// succeeding at one, a run stranded at 'running' since lunchtime — and a helper
// that could only write the present moment could express none of them.
func harvested(t *testing.T, h *harness, src, started, finished, status string) {
	t.Helper()
	db, err := sql.Open("sqlite", h.path)
	if err != nil {
		t.Fatalf("open %s directly: %v", h.path, err)
	}
	defer db.Close()

	var end, reason any
	if finished != "" {
		end = stamped(t, finished)
	}
	if status == "failed" {
		reason = "could not be read"
	}
	if _, err := db.Exec(`INSERT INTO harvest_run
		(source_id, started_at, finished_at, input, status, error)
		VALUES (?, ?, ?, ?, ?, ?)`,
		src, stamped(t, started), end, "/somewhere", status, reason); err != nil {
		t.Fatalf("insert a %s run of %s: %v", status, src, err)
	}
}

// stamped is the fixed-width UTC every timestamp in this database carries.
func stamped(t *testing.T, text string) string {
	return mustTime(t, text).Format("2006-01-02T15:04:05.000000Z")
}

func mustTime(t *testing.T, text string) time.Time {
	t.Helper()
	at, err := time.Parse(time.RFC3339, text)
	if err != nil {
		t.Fatalf("parse %q: %v", text, err)
	}
	return at.UTC()
}
