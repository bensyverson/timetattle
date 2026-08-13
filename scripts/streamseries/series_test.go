package main

import (
	"compress/gzip"
	"database/sql"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// Every test here builds what it reads. Nothing in this file looks at
// ~/.timetattle, so the suite runs on a machine that has never harvested
// anything — which is the only way a skip stays out of scripts/foreignsuite.

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

// seen is a readable observation of a stream holding rows.
func seen(captured string, rows int, oldest, newest string) Observation {
	return Observation{
		Snapshot: "knowledgeC-fixture.db.gz",
		Captured: at(captured),
		Stream:   "/app/usage",
		Presence: PresenceRows,
		Rows:     rows,
		Oldest:   at(oldest),
		Newest:   at(newest),
	}
}

// unreadable is a snapshot that could not be opened. It is deliberately not a
// zero-row observation, and the tests below insist on the difference.
func unreadable(captured string) Observation {
	return Observation{
		Snapshot: "knowledgeC-broken.db.gz",
		Captured: at(captured),
		Stream:   "/app/usage",
		Presence: PresenceUnreadable,
	}
}

func TestCaptureTimeReadsTheStampOutOfASnapshotName(t *testing.T) {
	got, err := captureTime("knowledgeC-20260813T080107.db.gz")
	if err != nil {
		t.Fatalf("captureTime: %v", err)
	}
	want := time.Date(2026, 8, 13, 8, 1, 7, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("captureTime = %s, want %s", got, want)
	}
}

func TestCaptureTimeIgnoresACollisionSuffix(t *testing.T) {
	got, err := captureTime("knowledgeC-20260729T081330-1.db.gz")
	if err != nil {
		t.Fatalf("captureTime: %v", err)
	}
	want := time.Date(2026, 7, 29, 8, 13, 30, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("captureTime = %s, want %s", got, want)
	}
}

func TestCaptureTimeRefusesANameWithNoStamp(t *testing.T) {
	if _, err := captureTime("notes.txt"); err == nil {
		t.Error("captureTime accepted a name carrying no instant; a silent zero time" +
			" would sort to the front of the series and misdate every finding")
	}
}

func TestStallsFindsARunOfSnapshotsWhoseNewestDidNotAdvance(t *testing.T) {
	obs := []Observation{
		seen("2026-07-28T18:07:00Z", 10000, "2026-07-18T21:50:55Z", "2026-07-27T19:28:03Z"),
		seen("2026-07-28T20:20:00Z", 10000, "2026-07-18T21:50:55Z", "2026-07-27T19:28:03Z"),
		seen("2026-07-29T08:13:00Z", 10000, "2026-07-18T21:50:55Z", "2026-07-27T19:28:03Z"),
		seen("2026-07-29T21:14:00Z", 10000, "2026-07-20T15:16:42Z", "2026-07-30T02:02:14Z"),
	}
	got := stalls(obs)
	if len(got) != 1 {
		t.Fatalf("stalls() gave %d, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.Snapshots != 3 {
		t.Errorf("Snapshots = %d, want 3", s.Snapshots)
	}
	if !s.Newest.Equal(at("2026-07-27T19:28:03Z")) {
		t.Errorf("Newest = %s, want the frozen row", s.Newest)
	}
	if !s.FirstSeen.Equal(at("2026-07-28T18:07:00Z")) || !s.LastSeen.Equal(at("2026-07-29T08:13:00Z")) {
		t.Errorf("bounds = %s..%s, want the first and last snapshot that saw the freeze",
			s.FirstSeen, s.LastSeen)
	}
	if s.Observed() != 14*time.Hour+6*time.Minute {
		t.Errorf("Observed() = %s, want 14h6m", s.Observed())
	}
}

// The load-bearing one. A snapshot nobody could read is not evidence that the
// freeze continued through it, and a tool that bridges the two reports a stall
// it never observed.
func TestStallsWillNotBridgeASnapshotItCouldNotRead(t *testing.T) {
	obs := []Observation{
		seen("2026-08-11T08:28:00Z", 10000, "2026-07-29T14:23:50Z", "2026-08-11T00:28:31Z"),
		unreadable("2026-08-12T16:08:00Z"),
		seen("2026-08-13T12:00:00Z", 10000, "2026-07-29T14:23:50Z", "2026-08-11T00:28:31Z"),
	}
	if got := stalls(obs); len(got) != 0 {
		t.Errorf("stalls() reported %+v across an unreadable snapshot; two lone"+
			" observations either side of a hole are not a witnessed run", got)
	}
}

func TestStallsReportsNothingWhenEveryNewestAdvances(t *testing.T) {
	obs := []Observation{
		seen("2026-08-01T08:04:00Z", 11507, "2026-07-21T10:53:36Z", "2026-08-01T13:04:17Z"),
		seen("2026-08-01T08:07:00Z", 11514, "2026-07-21T10:53:36Z", "2026-08-01T13:07:06Z"),
		seen("2026-08-01T08:08:00Z", 11517, "2026-07-21T10:53:36Z", "2026-08-01T13:07:22Z"),
	}
	if got := stalls(obs); len(got) != 0 {
		t.Errorf("stalls() = %+v, want none", got)
	}
}

func TestTrimsReportsTheOldestRowAdvancing(t *testing.T) {
	obs := []Observation{
		seen("2026-08-01T12:00:34Z", 11871, "2026-07-21T10:53:36Z", "2026-08-01T16:56:43Z"),
		seen("2026-08-01T12:57:25Z", 10063, "2026-07-21T20:11:27Z", "2026-08-01T17:55:47Z"),
	}
	got := trims(obs)
	if len(got) != 1 {
		t.Fatalf("trims() gave %d, want 1", len(got))
	}
	tr := got[0]
	if tr.RowsBefore != 11871 || tr.RowsAfter != 10063 {
		t.Errorf("rows %d -> %d, want 11871 -> 10063", tr.RowsBefore, tr.RowsAfter)
	}
	if tr.Discarded() != 9*time.Hour+17*time.Minute+51*time.Second {
		t.Errorf("Discarded() = %s, want 9h17m51s of retained span cut away", tr.Discarded())
	}
}

func TestTrimsIgnoresAPairWhoseOldestDidNotMove(t *testing.T) {
	obs := []Observation{
		seen("2026-08-13T08:01:07Z", 10000, "2026-07-29T14:23:50Z", "2026-08-11T00:28:31Z"),
		seen("2026-08-13T12:00:00Z", 10000, "2026-07-29T14:23:50Z", "2026-08-11T00:28:31Z"),
	}
	if got := trims(obs); len(got) != 0 {
		t.Errorf("trims() = %+v, want none: nothing left the source", got)
	}
}

func TestTrimsWillNotBridgeASnapshotItCouldNotRead(t *testing.T) {
	obs := []Observation{
		seen("2026-08-01T12:00:34Z", 11871, "2026-07-21T10:53:36Z", "2026-08-01T16:56:43Z"),
		unreadable("2026-08-01T12:30:00Z"),
		seen("2026-08-01T12:57:25Z", 10063, "2026-07-21T20:11:27Z", "2026-08-01T17:55:47Z"),
	}
	if got := trims(obs); len(got) != 0 {
		t.Errorf("trims() = %+v; the pair is not consecutive in the record", got)
	}
}

func TestHorizonsMeasuresTheAgeOfTheOldestRowStillHeld(t *testing.T) {
	obs := []Observation{
		seen("2026-07-28T23:07:18Z", 10000, "2026-07-18T21:50:55Z", "2026-07-27T19:28:03Z"),
		unreadable("2026-07-29T00:00:00Z"),
		seen("2026-08-01T17:00:34Z", 11871, "2026-07-21T10:53:36Z", "2026-08-01T16:56:43Z"),
	}
	got := horizons(obs)
	if len(got) != 2 {
		t.Fatalf("horizons() gave %d, want 2 — the unreadable snapshot measures nothing", len(got))
	}
	want := at("2026-07-28T23:07:18Z").Sub(at("2026-07-18T21:50:55Z"))
	if got[0] != want {
		t.Errorf("horizons()[0] = %s, want %s", got[0], want)
	}
}

func TestSummarizeReportsMinMedianAndMax(t *testing.T) {
	got := summarize([]time.Duration{5 * time.Hour, time.Hour, 3 * time.Hour})
	if got.N != 3 {
		t.Errorf("N = %d, want 3", got.N)
	}
	if got.Min != time.Hour || got.Median != 3*time.Hour || got.Max != 5*time.Hour {
		t.Errorf("summarize = %+v, want 1h/3h/5h", got)
	}
}

func TestSummarizeOfNothingIsNotAZero(t *testing.T) {
	got := summarize(nil)
	if got.N != 0 {
		t.Errorf("N = %d, want 0 so the caller can tell an empty set from a set of zeroes", got.N)
	}
}

func TestImpliedHorizonTurnsARowCapIntoADeadline(t *testing.T) {
	got, ok := impliedHorizon(10000, 2000)
	if !ok {
		t.Fatal("impliedHorizon refused a measured rate")
	}
	if got != 5*24*time.Hour {
		t.Errorf("impliedHorizon = %s, want 120h — 10000 rows at 2000 a day", got)
	}
}

// A cap divided by no rows a day is not infinite retention. It is a day on
// which nothing was written, and returning a duration there would publish a
// deadline nobody measured.
func TestImpliedHorizonSaysNothingWhenNothingWasRecorded(t *testing.T) {
	if _, ok := impliedHorizon(10000, 0); ok {
		t.Error("impliedHorizon returned a deadline from a zero rate")
	}
}

func TestBusiestDayPicksTheHeaviestDayAndItsCount(t *testing.T) {
	day, n := busiestDay(map[string]int{
		"2026-08-01": 2257,
		"2026-08-02": 310,
		"2026-08-03": 1904,
	})
	if day != "2026-08-01" || n != 2257 {
		t.Errorf("busiestDay = %q/%d, want 2026-08-01/2257", day, n)
	}
}

// The median daily rate is the reconciliation: a count-capped stream's
// retention ought to be the cap divided by the typical rate, and if that does
// not land near the retained span the corpus actually shows, the model of what
// evicts rows is wrong.
func TestMedianCountIsTheTypicalDailyRate(t *testing.T) {
	got := medianCount(map[string]int{
		"2026-08-01": 100,
		"2026-08-02": 300,
		"2026-08-03": 200,
	})
	if got != 200 {
		t.Errorf("medianCount = %d, want 200", got)
	}
}

func TestMedianCountOfNothingIsNotZeroRowsADay(t *testing.T) {
	if got := medianCount(nil); got != 0 {
		t.Errorf("medianCount = %d, want 0 so the caller can refuse to divide by it", got)
	}
}

func TestBusiestDayOfNothingReportsNothing(t *testing.T) {
	if day, n := busiestDay(nil); day != "" || n != 0 {
		t.Errorf("busiestDay = %q/%d, want an empty answer", day, n)
	}
}

// withStarts attaches row start times to a readable observation.
func withStarts(o Observation, starts ...string) Observation {
	for _, s := range starts {
		o.Starts = append(o.Starts, at(s))
	}
	return o
}

func TestEvictionsNamesTheYoungestRowThatWasPresentAndThenGone(t *testing.T) {
	obs := []Observation{
		withStarts(seen("2026-08-01T12:00:00Z", 4, "2026-07-21T10:00:00Z", "2026-08-01T11:00:00Z"),
			"2026-07-21T10:00:00Z", "2026-07-21T18:00:00Z", "2026-07-22T09:00:00Z", "2026-08-01T11:00:00Z"),
		withStarts(seen("2026-08-01T13:00:00Z", 2, "2026-07-22T09:00:00Z", "2026-08-01T11:00:00Z"),
			"2026-07-22T09:00:00Z", "2026-08-01T11:00:00Z"),
	}
	got := evictions(obs)
	if len(got) != 1 {
		t.Fatalf("evictions() gave %d, want 1", len(got))
	}
	e := got[0]
	if !e.Youngest.Equal(at("2026-07-21T18:00:00Z")) {
		t.Errorf("Youngest = %s, want the 18:00 row — the newest one that vanished", e.Youngest)
	}
	if e.Rows != 2 {
		t.Errorf("Rows = %d, want 2", e.Rows)
	}
	// It was alive at 12:00 on the 1st, aged 10d2h; gone by 13:00, aged 10d3h.
	if e.SurvivedAtLeast() != 10*24*time.Hour+18*time.Hour {
		t.Errorf("SurvivedAtLeast = %s, want 258h", e.SurvivedAtLeast())
	}
	if e.GoneBy() != 10*24*time.Hour+19*time.Hour {
		t.Errorf("GoneBy = %s, want 259h", e.GoneBy())
	}
}

// A stream that simply has nothing old in it has not evicted anything, and
// must not be reported as though it had. This is the /media/nowPlaying case:
// its oldest row was eight days old because nothing played, not because rows
// die at eight days.
func TestEvictionsReportsNothingWhenNoRowActuallyVanished(t *testing.T) {
	obs := []Observation{
		withStarts(seen("2026-08-12T12:00:00Z", 2, "2026-08-04T10:00:00Z", "2026-08-04T17:00:00Z"),
			"2026-08-04T10:00:00Z", "2026-08-04T17:00:00Z"),
		withStarts(seen("2026-08-13T12:00:00Z", 2, "2026-08-04T10:00:00Z", "2026-08-04T17:00:00Z"),
			"2026-08-04T10:00:00Z", "2026-08-04T17:00:00Z"),
	}
	if got := evictions(obs); len(got) != 0 {
		t.Errorf("evictions() = %+v, want none: no row left the source", got)
	}
}

func TestEvictionsWillNotBridgeASnapshotItCouldNotRead(t *testing.T) {
	obs := []Observation{
		withStarts(seen("2026-08-01T12:00:00Z", 2, "2026-07-21T10:00:00Z", "2026-08-01T11:00:00Z"),
			"2026-07-21T10:00:00Z", "2026-08-01T11:00:00Z"),
		unreadable("2026-08-01T12:30:00Z"),
		withStarts(seen("2026-08-01T13:00:00Z", 1, "2026-08-01T11:00:00Z", "2026-08-01T11:00:00Z"),
			"2026-08-01T11:00:00Z"),
	}
	if got := evictions(obs); len(got) != 0 {
		t.Errorf("evictions() = %+v; the bracket around the deletion is not established", got)
	}
}

func TestProfileGapsReportsTheSpacingOfDistinctInstants(t *testing.T) {
	starts := []time.Time{
		at("2026-08-01T00:00:00Z"),
		at("2026-08-01T00:00:07Z"),
		at("2026-08-01T00:00:14Z"),
		at("2026-08-01T12:00:14Z"),
	}
	got := profileGaps(starts)
	if got.Instants != 4 || got.Gaps != 3 {
		t.Errorf("Instants/Gaps = %d/%d, want 4/3", got.Instants, got.Gaps)
	}
	if got.Max != 12*time.Hour {
		t.Errorf("Max = %s, want 12h", got.Max)
	}
	if got.Median != 7*time.Second {
		t.Errorf("Median = %s, want 7s", got.Median)
	}
}

// Two rows sharing a start instant are one observation of the source. Counting
// the pair as a zero-second gap would drag the median to zero and make any
// stream look continuous.
func TestProfileGapsCollapsesRepeatedInstants(t *testing.T) {
	starts := []time.Time{
		at("2026-08-01T00:00:00Z"),
		at("2026-08-01T00:00:00Z"),
		at("2026-08-01T00:00:00Z"),
		at("2026-08-01T01:00:00Z"),
	}
	got := profileGaps(starts)
	if got.Instants != 2 || got.Gaps != 1 {
		t.Errorf("Instants/Gaps = %d/%d, want 2/1", got.Instants, got.Gaps)
	}
	if got.Median != time.Hour {
		t.Errorf("Median = %s, want 1h", got.Median)
	}
}

// One instant yields no gap at all. That is not a gap of zero, and a caller
// has to be able to tell the difference before it quotes a cadence.
func TestProfileGapsOfASingleInstantMeasuresNoCadence(t *testing.T) {
	got := profileGaps([]time.Time{at("2026-08-01T00:00:00Z")})
	if got.Instants != 1 {
		t.Errorf("Instants = %d, want 1", got.Instants)
	}
	if got.Gaps != 0 {
		t.Errorf("Gaps = %d, want 0", got.Gaps)
	}
	if got.Max != 0 || got.Median != 0 {
		t.Errorf("profileGaps invented a cadence from one instant: %+v", got)
	}
}

// The longest gap in a stream that has stalled IS the stall, so judging a new
// freeze against the maximum is circular. countOver lets the report say how
// many gaps clear a threshold, which separates the tail from the fault.
func TestCountOverCountsGapsClearingAThreshold(t *testing.T) {
	p := profileGaps([]time.Time{
		at("2026-08-01T00:00:00Z"),
		at("2026-08-01T00:00:10Z"),
		at("2026-08-02T00:00:10Z"),
		at("2026-08-04T00:00:10Z"),
	})
	if got := countOver(p, 12*time.Hour); got != 2 {
		t.Errorf("countOver(12h) = %d, want 2", got)
	}
	if got := countOver(p, 30*time.Hour); got != 1 {
		t.Errorf("countOver(30h) = %d, want 1", got)
	}
	if got := countOver(p, 100*time.Hour); got != 0 {
		t.Errorf("countOver(100h) = %d, want 0", got)
	}
}

func TestProfileGapsSortsWhatItIsGiven(t *testing.T) {
	got := profileGaps([]time.Time{
		at("2026-08-01T01:00:00Z"),
		at("2026-08-01T00:00:00Z"),
	})
	if got.Max != time.Hour {
		t.Errorf("Max = %s, want 1h from unsorted input", got.Max)
	}
}

// --- reading a snapshot ------------------------------------------------

// writeFixture builds a miniature knowledgeC snapshot, gzipped exactly as a
// harvest writes one. Building it rather than copying a real snapshot keeps a
// record of when a machine was in use out of the repository.
func writeFixture(t *testing.T, path string, rows []fixtureRow) {
	t.Helper()
	writeFixtureRaw(t, path, rows, 0)
}

// writeFixtureRaw additionally writes undated rows on the first row's stream:
// real ZOBJECT rows whose ZSTARTDATE is NULL.
func writeFixtureRaw(t *testing.T, path string, rows []fixtureRow, undated int) {
	t.Helper()
	plain := filepath.Join(t.TempDir(), "fixture.db")

	db, err := sql.Open("sqlite", "file:"+plain)
	if err != nil {
		t.Fatalf("create fixture: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE ZOBJECT (
		Z_PK INTEGER PRIMARY KEY,
		ZSTREAMNAME TEXT,
		ZVALUESTRING TEXT,
		ZSTARTDATE REAL,
		ZENDDATE REAL)`); err != nil {
		t.Fatalf("create ZOBJECT: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(
			`INSERT INTO ZOBJECT (ZSTREAMNAME, ZSTARTDATE, ZENDDATE) VALUES (?,?,?)`,
			r.stream,
			float64(r.start.Unix()-coreDataEpoch),
			float64(r.start.Add(time.Minute).Unix()-coreDataEpoch),
		); err != nil {
			t.Fatalf("insert: %v", err)
		}
	}
	for range undated {
		if _, err := db.Exec(
			`INSERT INTO ZOBJECT (ZSTREAMNAME, ZSTARTDATE, ZENDDATE) VALUES (?,NULL,NULL)`,
			rows[0].stream,
		); err != nil {
			t.Fatalf("insert undated: %v", err)
		}
	}
	db.Close()

	raw, err := os.ReadFile(plain)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	zw := gzip.NewWriter(out)
	if _, err := zw.Write(raw); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("compress: %v", err)
	}
	if err := out.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

type fixtureRow struct {
	stream string
	start  time.Time
}

func TestReadSnapshotReportsRowsOldestAndNewest(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledgeC-20260813T120000.db.gz")
	writeFixture(t, path, []fixtureRow{
		{"/app/usage", at("2026-08-10T09:00:00Z")},
		{"/app/usage", at("2026-08-11T00:28:31Z")},
		{"/app/usage", at("2026-08-10T23:00:00Z")},
	})

	obs, err := readSnapshot(path, []string{"/app/usage"})
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	if len(obs) != 1 {
		t.Fatalf("got %d observations, want 1", len(obs))
	}
	o := obs[0]
	if o.Presence != PresenceRows {
		t.Fatalf("Presence = %v, want rows", o.Presence)
	}
	if o.Rows != 3 {
		t.Errorf("Rows = %d, want 3", o.Rows)
	}
	if !o.Oldest.Equal(at("2026-08-10T09:00:00Z")) || !o.Newest.Equal(at("2026-08-11T00:28:31Z")) {
		t.Errorf("span %s..%s, want 09:00 on the 10th to 00:28:31 on the 11th", o.Oldest, o.Newest)
	}
	if !o.Captured.Equal(time.Date(2026, 8, 13, 12, 0, 0, 0, time.Local)) {
		t.Errorf("Captured = %s, want the instant in the file name", o.Captured)
	}
	if o.Days["2026-08-10"] != 2 || o.Days["2026-08-11"] != 1 {
		t.Errorf("Days = %v, want two rows on the 10th and one on the 11th", o.Days)
	}
}

// A stream the snapshot never mentions must not read as a stream with zero
// rows, and must not read as a failure either.
func TestReadSnapshotSeparatesAStreamItHasNoRowsForFromOneItCouldNotRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledgeC-20260813T120000.db.gz")
	writeFixture(t, path, []fixtureRow{
		{"/display/isBacklit", at("2026-08-13T09:00:00Z")},
	})

	obs, err := readSnapshot(path, []string{"/app/usage", "/display/isBacklit"})
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	by := map[string]Observation{}
	for _, o := range obs {
		by[o.Stream] = o
	}
	if got := by["/app/usage"].Presence; got != PresenceNoRows {
		t.Errorf("/app/usage Presence = %v, want %v", got, PresenceNoRows)
	}
	if got := by["/display/isBacklit"].Presence; got != PresenceRows {
		t.Errorf("/display/isBacklit Presence = %v, want %v", got, PresenceRows)
	}
}

// A stream whose rows carry no start date is present but untimed. Filtering
// those away in SQL would report it as a stream with no rows at all, which is
// the same collapse this file is otherwise built to prevent.
func TestReadSnapshotCountsRowsThatCarryNoTimestamp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledgeC-20260813T120000.db.gz")
	// One dated row and two that carry no start date at all.
	writeFixtureRaw(t, path, []fixtureRow{{"/app/usage", at("2026-08-10T09:00:00Z")}}, 2)

	obs, err := readSnapshot(path, []string{"/app/usage"})
	if err != nil {
		t.Fatalf("readSnapshot: %v", err)
	}
	o := obs[0]
	if o.Presence != PresenceRows {
		t.Fatalf("Presence = %v, want rows", o.Presence)
	}
	if o.Rows != 3 {
		t.Errorf("Rows = %d, want 3 — every row the stream has", o.Rows)
	}
	if o.Undated != 2 {
		t.Errorf("Undated = %d, want 2", o.Undated)
	}
	if !o.Oldest.Equal(at("2026-08-10T09:00:00Z")) {
		t.Errorf("Oldest = %s, want the only dated row", o.Oldest)
	}
}

func TestStreamNamesComeOutOfTheSnapshotRatherThanAConstant(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledgeC-20260813T120000.db.gz")
	writeFixture(t, path, []fixtureRow{
		{"/display/isBacklit", at("2026-08-13T09:00:00Z")},
		{"/app/usage", at("2026-08-13T09:00:00Z")},
		{"/app/usage", at("2026-08-13T10:00:00Z")},
	})

	got, err := streamNames(path)
	if err != nil {
		t.Fatalf("streamNames: %v", err)
	}
	sort.Strings(got)
	want := []string{"/app/usage", "/display/isBacklit"}
	if !slices.Equal(got, want) {
		t.Errorf("streamNames = %v, want %v", got, want)
	}
}

func TestReadSnapshotRefusesAFileThatIsNotASnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "knowledgeC-20260813T120000.db.gz")
	if err := os.WriteFile(path, []byte("this is not gzip"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readSnapshot(path, []string{"/app/usage"}); err == nil {
		t.Error("readSnapshot accepted rubbish; an unreadable snapshot must be" +
			" reported as unreadable rather than as a stream with nothing in it")
	}
}
