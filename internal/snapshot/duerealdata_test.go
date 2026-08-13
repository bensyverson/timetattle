//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the snapshot
// directory on one particular machine, so `go test ./...` does not compile it
// at all. Run these deliberately:
//
//	go test -tags realdata ./internal/snapshot/
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package snapshot

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

// liveSnapshotDir is the real preserved corpus, or a skip when this machine has
// none. Nothing here writes to it.
func liveSnapshotDir(t *testing.T) string {
	t.Helper()
	dir, err := DefaultDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if _, err := os.ReadDir(dir); err != nil {
		t.Skipf("cannot read the live snapshot directory %s: %v", dir, err)
	}
	return dir
}

// liveStamps is every real knowledgeC snapshot name, oldest first, with the
// instant read out of the name beside it.
func liveStamps(t *testing.T) ([]string, []time.Time) {
	t.Helper()
	dir := liveSnapshotDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	var (
		names   []string
		stamps  []time.Time
		undated []string
	)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), snapshotPrefix) {
			continue
		}
		when, ok := snapshotTakenAt(e.Name())
		if !ok {
			undated = append(undated, e.Name())
			continue
		}
		names = append(names, e.Name())
		stamps = append(stamps, when)
	}
	// An undatable name is invisible to the throttle, and enough of them would
	// make it write a snapshot on every run while reporting nothing wrong.
	if len(undated) > 0 {
		t.Errorf("%d of %d real snapshot names could not be dated, so the throttle cannot see them: %v",
			len(undated), len(undated)+len(names), undated)
	}
	if len(stamps) < 2 {
		t.Skipf("only %d datable snapshots in %s; there is no cadence to measure", len(stamps), dir)
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i].Before(stamps[j]) })
	return names, stamps
}

// TestTheLiveCorpusIsEntirelyDatable is the half of the real-data check that
// only needs the names, and the half whose failure would be silent everywhere
// else: a snapshot the throttle cannot date is a snapshot it behaves as though
// were not there.
func TestTheLiveCorpusIsEntirelyDatable(t *testing.T) {
	names, stamps := liveStamps(t)
	t.Logf("%d datable snapshots, %s to %s",
		len(names), stamps[0].Format(time.RFC3339), stamps[len(stamps)-1].Format(time.RFC3339))
}

// TestTheLiveCorpusExercisesTheHourSlotComparison is the real-data half of the
// guard whose constructed case is TestDueIsNotDefeatedByASlotThatFiresASecondEarly.
//
// It looks for consecutive real snapshots that a throttle comparing INSTANTS
// would have refused and one comparing HOUR SLOTS takes: pairs less than the
// interval apart on the clock but a whole number of interval-sized hour steps
// apart on the schedule. On 2026-08-13 the corpus holds exactly such a pair —
// 00:00:01 and 04:00:00, 3h59m59s — written by two consecutive scheduled runs
// of the four-hourly agent.
//
// A failure here does not mean the guard is wrong. It means this machine's
// corpus no longer demonstrates it, and the constructed case has become the
// only evidence for it.
func TestTheLiveCorpusExercisesTheHourSlotComparison(t *testing.T) {
	_, stamps := liveStamps(t)

	var exercised [][2]time.Time
	for i := 1; i < len(stamps); i++ {
		prev, next := stamps[i-1], stamps[i]
		byInstant := next.Sub(prev) >= every4h
		bySlot := next.Truncate(time.Hour).Sub(prev.Truncate(time.Hour)) >= every4h
		if bySlot && !byInstant {
			exercised = append(exercised, [2]time.Time{prev, next})
		}
	}
	if len(exercised) == 0 {
		t.Error("no consecutive pair in the live corpus is under 4h apart on the clock and " +
			"four hour-steps apart on the schedule; the hour-slot comparison is no longer " +
			"demonstrated by real data, only by the constructed case")
		return
	}
	for _, p := range exercised {
		t.Logf("a throttle comparing instants would have refused %s: it is %v after %s",
			p[1].Format(time.RFC3339), p[1].Sub(p[0]), p[0].Format(time.RFC3339))
	}
}

// TestDueAgainstTheLiveCorpusNamesTheRealNewestSnapshot runs the throttle over
// the real directory rather than a fixture, which is the only way to find out
// that it reads the names macOS and this program have actually left there.
func TestDueAgainstTheLiveCorpusNamesTheRealNewestSnapshot(t *testing.T) {
	dir := liveSnapshotDir(t)
	_, stamps := liveStamps(t)
	newest := stamps[len(stamps)-1]

	held, err := Due(dir, newest.Add(time.Hour), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if held.Take {
		t.Errorf("Due took a snapshot one hour after the real newest one at %s", newest.Format(time.RFC3339))
	}
	if !held.At.Equal(newest) {
		t.Errorf("Due dated the newest snapshot %s, want %s",
			held.At.Format(time.RFC3339), newest.Format(time.RFC3339))
	}
	if filepath.Dir(held.Newest) != dir {
		t.Errorf("Due named %q, which is not in %s", held.Newest, dir)
	}
	if _, err := os.Stat(held.Newest); err != nil {
		t.Errorf("Due named a snapshot that cannot be read back: %v", err)
	}

	due, err := Due(dir, newest.Add(every4h), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !due.Take {
		t.Errorf("Due refused a snapshot %v after the real newest one at %s",
			every4h, newest.Format(time.RFC3339))
	}
}
