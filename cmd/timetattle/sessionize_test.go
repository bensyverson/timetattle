package main

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// seedStore returns a store holding three saves — two minutes apart, then one
// three hours later — which is two sittings at any gap between two minutes and
// three hours.
func seedStore(t *testing.T) string {
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
	base := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	var events []source.Event
	for _, offset := range []time.Duration{0, 2 * time.Minute, 3 * time.Hour} {
		e := source.NewPoint(base.Add(offset), source.Identity{Document: "a.motn"}, "fs-observations")
		e.Locator = "/tmp/seed/a.motn"
		events = append(events, e)
	}
	if _, err := run.Write(ctx, events); err != nil {
		t.Fatalf("write the seed events: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish the seed run: %v", err)
	}
	return path
}

// query runs a single-value query against a store, for assertions about the
// rows sessionize left behind.
func query[T any](t *testing.T, path, sql_ string) T {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	var out T
	if err := db.QueryRow(sql_).Scan(&out); err != nil {
		t.Fatalf("%s: %v", sql_, err)
	}
	return out
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write a config: %v", err)
	}
	return path
}

func TestSessionizeWritesOnlyToTheStoreItWasGiven(t *testing.T) {
	// The property TestTheStoreFlagRedirectsWrites states for harvest, restated
	// for the one command that writes a table nobody harvested: nothing here
	// may touch the machine's own store, and a test that pollutes it is worse
	// than no test.
	path := seedStore(t)

	if err := sessionize([]string{"-store", path, "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}

	if got := query[int](t, path, `SELECT COUNT(*) FROM derived_session`); got != 2 {
		t.Errorf("derived_session holds %d rows, want 2", got)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".timetattle", "timetattle.db")); err == nil {
		t.Error("sessionize wrote to the default store even though -store named another")
	}
}

func TestSessionizeLeavesTheObservationsAlone(t *testing.T) {
	// Criterion Ybu at the command level. Two rebuilds, and the event and
	// harvest_run tables must come out of both untouched.
	path := seedStore(t)
	const fingerprint = `SELECT COALESCE(group_concat(r, char(10)), '') FROM (
		SELECT quote(started_at)||quote(ended_at)||quote(source_id)||quote(shape) AS r
		FROM event ORDER BY id)`
	const runs = `SELECT COALESCE(group_concat(quote(source_id)||quote(status)||quote(events_new), char(10)), '')
		FROM harvest_run ORDER BY id`

	before, beforeRuns := query[string](t, path, fingerprint), query[string](t, path, runs)
	if before == "" {
		t.Fatal("the seeded store has no events; the comparison would be vacuous")
	}
	for range 2 {
		if err := sessionize([]string{"-store", path, "-quiet"}); err != nil {
			t.Fatalf("sessionize: %v", err)
		}
	}

	if after := query[string](t, path, fingerprint); after != before {
		t.Error("sessionize changed the event table")
	}
	if after := query[string](t, path, runs); after != beforeRuns {
		t.Error("sessionize changed the harvest_run table")
	}
}

func TestAFlagOverridesTheConfiguredValueForOneRunOnly(t *testing.T) {
	path := seedStore(t)
	cfg := writeConfig(t, `{"sessions": {"gap": "4h"}}`)

	if err := sessionize([]string{"-store", path, "-config", cfg, "-gap", "2m", "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}

	if got, want := query[int64](t, path, `SELECT DISTINCT gap_micros FROM derived_session`),
		int64(2*time.Minute/time.Microsecond); got != want {
		t.Errorf("stored gap %d microseconds, want %d: the flag did not override the file", got, want)
	}
	// And the file is unchanged, so a threshold tried on the command line
	// cannot quietly become the configured one.
	raw, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatalf("read the config back: %v", err)
	}
	if !strings.Contains(string(raw), `"4h"`) {
		t.Errorf("the config file was rewritten: %s", raw)
	}
}

func TestTheConfiguredValueIsUsedWhenNoFlagOverridesIt(t *testing.T) {
	// The other half of the pair, and the one that fails silently if the
	// wiring is wrong: a config nobody reads looks exactly like a config that
	// agrees with the defaults.
	path := seedStore(t)
	cfg := writeConfig(t, `{"sessions": {"gap": "4h", "lead_in": "1m", "tail_out": "1m"}}`)

	if err := sessionize([]string{"-store", path, "-config", cfg, "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}

	// A four-hour gap bridges the three-hour silence, so the three saves are
	// one sitting rather than two.
	if got := query[int](t, path, `SELECT COUNT(*) FROM derived_session`); got != 1 {
		t.Errorf("derived_session holds %d rows, want 1 at a four-hour gap", got)
	}
	if got, want := query[int64](t, path, `SELECT DISTINCT lead_in_micros FROM derived_session`),
		int64(time.Minute/time.Microsecond); got != want {
		t.Errorf("stored lead-in %d microseconds, want %d", got, want)
	}
}

func TestDryRunLeavesTheStoredSessionsAlone(t *testing.T) {
	path := seedStore(t)
	if err := sessionize([]string{"-store", path, "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}
	before := query[int64](t, path, `SELECT DISTINCT gap_micros FROM derived_session`)

	if err := sessionize([]string{"-store", path, "-gap", "9h", "-dry-run", "-quiet"}); err != nil {
		t.Fatalf("sessionize -dry-run: %v", err)
	}

	if after := query[int64](t, path, `SELECT DISTINCT gap_micros FROM derived_session`); after != before {
		t.Errorf("a dry run rewrote the table: gap is now %d microseconds, was %d", after, before)
	}
}

func TestSweepWritesNothing(t *testing.T) {
	path := seedStore(t)

	if err := sessionize([]string{"-store", path, "-sweep", "2m,4h", "-quiet"}); err != nil {
		t.Fatalf("sessionize -sweep: %v", err)
	}

	if got := query[int](t, path, `SELECT COUNT(*) FROM derived_session`); got != 0 {
		t.Errorf("a sweep wrote %d rows; it is a report and must store nothing", got)
	}
}

func TestABrokenConfigStopsTheRunRatherThanFallingBackToDefaults(t *testing.T) {
	// The whole reason config.Load refuses an unknown key. Falling back here
	// would compute every number from parameters the file on disk contradicts.
	path := seedStore(t)
	cfg := writeConfig(t, `{"sessions": {"gap_threshold": "45m"}}`)

	err := sessionize([]string{"-store", path, "-config", cfg, "-quiet"})
	if err == nil {
		t.Fatal("sessionize ran with a config it could not understand")
	}
	if !strings.Contains(err.Error(), "gap_threshold") {
		t.Errorf("the error does not name the key: %v", err)
	}
	if got := query[int](t, path, `SELECT COUNT(*) FROM derived_session`); got != 0 {
		t.Errorf("%d sessions were written despite the config failing", got)
	}
}

func TestUnusableParametersAreRefusedBeforeAnythingIsWritten(t *testing.T) {
	path := seedStore(t)
	if err := sessionize([]string{"-store", path, "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}
	before := query[int](t, path, `SELECT COUNT(*) FROM derived_session`)

	if err := sessionize([]string{"-store", path, "-gap", "0s", "-quiet"}); err == nil {
		t.Fatal("sessionize accepted a zero gap")
	}
	if after := query[int](t, path, `SELECT COUNT(*) FROM derived_session`); after != before {
		t.Errorf("the table changed from %d rows to %d despite the run failing", before, after)
	}
}

func TestAnIsolatedSaveScoresSomething(t *testing.T) {
	// Criterion kRK at the command level, on the shape the criterion names: a
	// single save with nothing near it.
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: "fs-observations", Input: "/tmp/lonely"})
	if err != nil {
		t.Fatalf("begin a run: %v", err)
	}
	lonely := source.NewPoint(time.Date(2026, 7, 8, 1, 12, 7, 0, time.UTC),
		source.Identity{Document: "transmission-animation1.blend"}, "fs-observations")
	lonely.Locator = "/tmp/lonely/transmission-animation1.blend"
	if _, err := run.Write(ctx, []source.Event{lonely}); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("finish: %v", err)
	}
	st.Close()

	if err := sessionize([]string{"-store", path, "-quiet"}); err != nil {
		t.Fatalf("sessionize: %v", err)
	}

	if got := query[int](t, path, `SELECT COUNT(*) FROM derived_session`); got != 1 {
		t.Fatalf("derived_session holds %d rows, want 1", got)
	}
	span := query[string](t, path, `SELECT started_at || ' ' || ended_at FROM derived_session`)
	start, end, _ := strings.Cut(span, " ")
	if start == end {
		t.Fatalf("the lone save produced a sitting of no duration at %s", start)
	}
}
