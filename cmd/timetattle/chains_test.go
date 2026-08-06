package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// derivedCount reports how many rows a derived table holds, opening the store
// read-only through the driver rather than through internal/chains, so that a
// bug in the writer cannot hide behind the reader that shares its code.
func derivedCount(t *testing.T, path, table string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// capture runs f with stdout redirected and returns what it printed.
func capture(t *testing.T, f func() error) (string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stdout
	os.Stdout = w
	runErr := f()
	w.Close()
	os.Stdout = saved

	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		b.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	r.Close()
	return b.String(), runErr
}

// An empty store is not a finding. Silence here would read as "no chains
// exist", which nobody is entitled to conclude from a store that was never
// harvested — this project has shipped that conflation twice.
func TestChainsSaysNothingWasMeasuredOnAnEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timetattle.db")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	saved := os.Stderr
	os.Stderr = w
	runErr := chainsCommand([]string{"-store", path, "-quiet"})
	w.Close()
	os.Stderr = saved
	if runErr != nil {
		t.Fatalf("chains: %v", runErr)
	}

	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		b.Write(buf[:n])
		if readErr != nil {
			break
		}
	}
	r.Close()

	warning := b.String()
	if !strings.Contains(warning, "Nothing was measured") {
		t.Errorf("stderr does not distinguish an unmeasured store from one with no chains:\n%s", warning)
	}
	if !strings.Contains(warning, "harvest") {
		t.Errorf("the warning does not say what to do about it:\n%s", warning)
	}

	// It still records the run, so a later reader can tell the derivation
	// happened and found nothing, rather than never having happened.
	if n := derivedCount(t, path, "derived_chain_run"); n != 1 {
		t.Errorf("derived_chain_run holds %d rows, want 1", n)
	}
	if n := derivedCount(t, path, "derived_chain"); n != 0 {
		t.Errorf("derived_chain holds %d rows, want 0", n)
	}
}

// -dry-run must leave the tables exactly as it found them, including on a store
// that already holds a derivation.
func TestChainsDryRunWritesNothing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timetattle.db")
	if _, err := capture(t, func() error {
		return chainsCommand([]string{"-store", path, "-quiet"})
	}); err != nil {
		t.Fatalf("first run: %v", err)
	}
	before := derivedCount(t, path, "derived_chain_run")

	out, err := capture(t, func() error {
		return chainsCommand([]string{"-store", path, "-dry-run"})
	})
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "dry run") {
		t.Errorf("output does not say it was a dry run:\n%s", out)
	}
	if got := derivedCount(t, path, "derived_chain_run"); got != before {
		t.Errorf("derived_chain_run holds %d rows after a dry run, want %d", got, before)
	}
}

// The store flag has to actually redirect. harvest_test.go documents why this
// property is protected explicitly rather than assumed: a test that writes to
// the machine's own store is worse than no test.
func TestChainsStoreFlagRedirectsWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "elsewhere.db")
	if _, err := capture(t, func() error {
		return chainsCommand([]string{"-store", path, "-quiet"})
	}); err != nil {
		t.Fatalf("chains: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("-store did not create %s: %v", path, err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".timetattle", "timetattle.db")); err == nil {
		t.Error("a store was created under HOME despite -store")
	}
}

// Bad parameters are refused rather than silently defaulted, because a
// derivation records its parameters and a corrected zero would be recorded as
// though it had been asked for.
func TestChainsRefusesImpossibleParameters(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timetattle.db")
	for _, args := range [][]string{
		{"-store", path, "-min-prefix", "0"},
		{"-store", path, "-coincidence", "0"},
		{"-store", path, "-from", "not-a-date"},
	} {
		if _, err := capture(t, func() error { return chainsCommand(args) }); err == nil {
			t.Errorf("chains %v was accepted", args)
		}
	}
}

// short ROUNDS to the displayed unit rather than truncating, so a rendered gap
// is never further from the truth than half that unit.
//
// This is not cosmetic. project/private/2026-07-28-spike-findings.md published this
// chain as +14m/+3m/+237m/+7m/+193m, and a truncating render reports the same
// underlying data as +14m/+2m/+237m/+7m/+192m — every interval short by up to
// 59 seconds, and two of the five disagreeing with a figure this project has
// already published. The exact duration is in the database either way; what is
// at stake is whether a reader comparing the two documents concludes the model
// changed when it did not.
//
// The carry cases are the reason this is a function rather than a format verb:
// rounding can push a value across the boundary that chose its own format, so
// 59m45s must read 1h00m and not 60m, and 59.7s must read 1m and not 60s.
func TestShortRoundsRatherThanTruncating(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
		why  string
	}{
		{2*time.Minute + 48*time.Second, "3m", "the published +3m step; truncation renders it 2m"},
		{3*time.Hour + 12*time.Minute + 52*time.Second, "3h13m", "the published +193m step; truncation renders it 3h12m"},
		{14*time.Minute + 12*time.Second, "14m", "the published +14m step, unchanged by rounding"},
		{3*time.Hour + 57*time.Minute + 17*time.Second, "3h57m", "the published +237m step, unchanged by rounding"},
		{7*time.Minute + 15*time.Second, "7m", "the published +7m step, unchanged by rounding"},
		{59*time.Minute + 45*time.Second, "1h00m", "carries into the hour format rather than reading 60m"},
		{59*time.Second + 700*time.Millisecond, "1m", "carries into the minute format rather than reading 60s"},
		{45 * time.Second, "45s", "sub-minute gaps still read in seconds"},
		{30 * time.Minute, "30m", "an exact value is unmoved by rounding"},
	} {
		if got := short(c.in); got != c.want {
			t.Errorf("short(%v) = %q, want %q — %s", c.in, got, c.want, c.why)
		}
	}
}
