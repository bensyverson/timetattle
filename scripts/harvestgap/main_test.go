package main

import (
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		panic(err)
	}
	return t
}

// row builds a completed harvest_run row. An empty finish means the row is
// still marked running, which is a real shape in the live store.
func row(source, start, finish string, status store.Status) store.Run {
	r := store.Run{SourceID: source, StartedAt: at(start), Status: status}
	if finish != "" {
		r.FinishedAt = at(finish)
	}
	return r
}

func ok(source, start, finish string) store.Run {
	return row(source, start, finish, store.StatusOK)
}

// One invocation writes one row per source, and the live store shows those rows
// chained: each source's started_at equals the previous source's finished_at to
// the microsecond, because Collect runs them in sequence in one process. That
// continuity, not a clock bucket, is what identifies an invocation.
func TestClusterChainsSequentialRowsIntoOneInvocation(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-11T01:09:57.047644Z", "2026-08-11T01:11:14.894921Z"),
		ok("b", "2026-08-11T01:11:14.897265Z", "2026-08-11T01:12:37.862421Z"),
		ok("c", "2026-08-11T01:12:37.863145Z", "2026-08-11T01:12:51.735270Z"),
	}
	got := cluster(runs, time.Second)
	if len(got) != 1 {
		t.Fatalf("cluster() gave %d invocations, want 1", len(got))
	}
	if !got[0].Start.Equal(at("2026-08-11T01:09:57.047644Z")) {
		t.Errorf("start = %s, want the first row's started_at", got[0].Start)
	}
	if !got[0].End.Equal(at("2026-08-11T01:12:51.735270Z")) {
		t.Errorf("end = %s, want the last row's finished_at", got[0].End)
	}
	if got[0].Rows != 3 {
		t.Errorf("rows = %d, want 3", got[0].Rows)
	}
}

func TestClusterSplitsWhereContinuityBreaks(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-11T01:09:57Z", "2026-08-11T01:12:51Z"),
		ok("a", "2026-08-11T13:28:23Z", "2026-08-11T13:31:59Z"),
	}
	got := cluster(runs, time.Second)
	if len(got) != 2 {
		t.Fatalf("cluster() gave %d invocations, want 2", len(got))
	}
}

// A row still marked running has no finished_at. Treating that as a zero time
// would end the invocation in 0001 and make every later row a fresh one, and
// dropping the row would lose the only evidence that a harvest died partway.
func TestClusterCarriesAnUnfinishedRowWithoutBreakingTheChain(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-01T13:07:12.525919Z", "2026-08-01T13:07:13.810839Z"),
		row("b", "2026-08-01T13:07:13.811166Z", "", store.StatusRunning),
	}
	got := cluster(runs, time.Second)
	if len(got) != 1 {
		t.Fatalf("cluster() gave %d invocations, want 1", len(got))
	}
	if got[0].Unfinished != 1 {
		t.Errorf("unfinished = %d, want 1", got[0].Unfinished)
	}
	if !got[0].End.Equal(at("2026-08-01T13:07:13.811166Z")) {
		t.Errorf("end = %s, want the unfinished row's started_at, the last thing observed", got[0].End)
	}
}

// A failed source and an absent source are different observations, and an
// invocation that ran with a source failing is not a missed invocation. The
// count has to survive clustering or the distinction cannot be reported.
func TestClusterCountsFailedSourcesSeparatelyFromRows(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-03T21:48:45.000000Z", "2026-08-03T21:48:45.851689Z"),
		row("b", "2026-08-03T21:48:45.851800Z", "2026-08-03T21:50:06.279290Z", store.StatusFailed),
	}
	got := cluster(runs, time.Second)
	if len(got) != 1 {
		t.Fatalf("cluster() gave %d invocations, want 1", len(got))
	}
	if got[0].Failed != 1 {
		t.Errorf("failed = %d, want 1", got[0].Failed)
	}
	if got[0].Rows != 2 {
		t.Errorf("rows = %d, want 2", got[0].Rows)
	}
}

// Sources are not guaranteed to be sequential forever. If one row starts before
// an earlier one has finished, the invocation must extend to the furthest end
// seen so far, not to the last row's end.
func TestClusterUsesTheRunningMaximumEnd(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-11T01:00:00Z", "2026-08-11T01:10:00Z"),
		ok("b", "2026-08-11T01:00:01Z", "2026-08-11T01:00:05Z"),
		ok("c", "2026-08-11T01:00:06Z", "2026-08-11T01:00:07Z"),
	}
	got := cluster(runs, time.Second)
	if len(got) != 1 {
		t.Fatalf("cluster() gave %d invocations, want 1", len(got))
	}
	if !got[0].End.Equal(at("2026-08-11T01:10:00Z")) {
		t.Errorf("end = %s, want 01:10:00, the furthest finished_at in the invocation", got[0].End)
	}
}

func TestClusterSortsRowsBeforeGrouping(t *testing.T) {
	runs := []store.Run{
		ok("b", "2026-08-11T13:28:23Z", "2026-08-11T13:31:59Z"),
		ok("a", "2026-08-11T01:09:57Z", "2026-08-11T01:12:51Z"),
	}
	got := cluster(runs, time.Second)
	if len(got) != 2 {
		t.Fatalf("cluster() gave %d invocations, want 2", len(got))
	}
	if !got[0].Start.Before(got[1].Start) {
		t.Errorf("invocations came back out of order: %s then %s", got[0].Start, got[1].Start)
	}
}

// The regression that this whole tool exists for. A fixed clock grid splits one
// invocation that straddles a boundary. On 2026-08-07 the invocation beginning
// 19:06:58 ran to 19:10:38 and the 10-minute grid cut it at 19:10, which is why
// the recorded worst gap read 54.6438h from 19:10:22 instead of 54.7004h from
// 19:06:58 — the grid understated the worst outage by 3m40s.
func TestGridSplitsAnInvocationThatStraddlesABoundary(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-07T19:06:58.916700Z", "2026-08-07T19:09:00.120132Z"),
		ok("b", "2026-08-07T19:09:00.120546Z", "2026-08-07T19:10:22.763969Z"),
		ok("c", "2026-08-07T19:10:22.764787Z", "2026-08-07T19:10:38.178760Z"),
	}
	if got := cluster(runs, time.Second); len(got) != 1 {
		t.Fatalf("cluster() gave %d invocations, want 1; the fixture is wrong", len(got))
	}
	got := grid(runs, 10*time.Minute)
	if len(got) != 2 {
		t.Fatalf("grid() gave %d batches, want 2", len(got))
	}
	if !got[1].Start.Equal(at("2026-08-07T19:10:22.764787Z")) {
		t.Errorf("second batch starts %s, want 19:10:22.764787", got[1].Start)
	}
}

// And the other direction: the grid merges invocations that are genuinely
// distinct. On 2026-08-03 one invocation finished 23:57:14 and another began
// 23:58:27; a 10-minute grid reports them as one.
func TestGridMergesDistinctInvocationsInOneBucket(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-03T23:54:52.311047Z", "2026-08-03T23:57:14.031547Z"),
		ok("a", "2026-08-03T23:58:27.364050Z", "2026-08-03T23:59:18.892533Z"),
	}
	if got := cluster(runs, time.Second); len(got) != 2 {
		t.Fatalf("cluster() gave %d invocations, want 2; the fixture is wrong", len(got))
	}
	if got := grid(runs, 10*time.Minute); len(got) != 1 {
		t.Fatalf("grid() gave %d batches, want 1", len(got))
	}
}

// Two measures, deliberately kept apart. Exposure is how long no source was
// being read, which is what the retention floors bite on. Cadence is start to
// start, which is what launchd's StartInterval controls. They differ by the
// invocation's own duration and only one of them answers any given question.
func TestGapsReportExposureAndCadenceSeparately(t *testing.T) {
	b := []batch{
		{Start: at("2026-08-11T01:00:00Z"), End: at("2026-08-11T01:05:00Z")},
		{Start: at("2026-08-11T13:00:00Z"), End: at("2026-08-11T13:04:00Z")},
	}
	got := gapsOf(b)
	if len(got) != 1 {
		t.Fatalf("gapsOf() gave %d gaps, want 1", len(got))
	}
	if want := 11*time.Hour + 55*time.Minute; got[0].Exposure() != want {
		t.Errorf("exposure = %s, want %s", got[0].Exposure(), want)
	}
	if want := 12 * time.Hour; got[0].Cadence() != want {
		t.Errorf("cadence = %s, want %s", got[0].Cadence(), want)
	}
}

func TestGapsOfCountsOneFewerThanTheInvocations(t *testing.T) {
	if got := gapsOf(nil); len(got) != 0 {
		t.Errorf("gapsOf(nil) gave %d gaps, want 0", len(got))
	}
	one := []batch{{Start: at("2026-08-11T01:00:00Z"), End: at("2026-08-11T01:05:00Z")}}
	if got := gapsOf(one); len(got) != 0 {
		t.Errorf("gapsOf(one invocation) gave %d gaps, want 0; one run is not evidence of a cadence", len(got))
	}
	three := []batch{
		{Start: at("2026-08-11T01:00:00Z"), End: at("2026-08-11T01:05:00Z")},
		{Start: at("2026-08-11T13:00:00Z"), End: at("2026-08-11T13:05:00Z")},
		{Start: at("2026-08-12T01:00:00Z"), End: at("2026-08-12T01:05:00Z")},
	}
	if got := gapsOf(three); len(got) != 2 {
		t.Errorf("gapsOf(three invocations) gave %d gaps, want 2", len(got))
	}
}

// The join threshold is a parameter, and a parameter that lands in the middle
// of the data is a verdict invented by the analyst. The tool has to print how
// much room it had, so separation reports the widest delta it swallowed and the
// narrowest it cut on.
func TestSeparationReportsTheRoomAroundTheJoinThreshold(t *testing.T) {
	runs := []store.Run{
		ok("a", "2026-08-11T01:00:00.000000Z", "2026-08-11T01:00:10.000000Z"),
		ok("b", "2026-08-11T01:00:10.500000Z", "2026-08-11T01:00:20.000000Z"),
		ok("a", "2026-08-11T01:01:00.000000Z", "2026-08-11T01:01:10.000000Z"),
	}
	absorbed, split := separation(runs, time.Second)
	if want := 500 * time.Millisecond; absorbed != want {
		t.Errorf("widest absorbed = %s, want %s", absorbed, want)
	}
	if want := 40 * time.Second; split != want {
		t.Errorf("narrowest split = %s, want %s", split, want)
	}
}

func TestOverCountsGapsPastAThreshold(t *testing.T) {
	gaps := []gap{
		// 12h05m start to start, 11h55m of it with nothing being read.
		{PrevStart: at("2026-08-11T00:00:00Z"), PrevEnd: at("2026-08-11T00:10:00Z"), Start: at("2026-08-11T12:05:00Z")},
		{PrevStart: at("2026-08-11T12:05:00Z"), PrevEnd: at("2026-08-11T12:15:00Z"), Start: at("2026-08-12T01:00:00Z")},
	}
	if got := over(gaps, 12*time.Hour, gap.Cadence); got != 2 {
		t.Errorf("over(12h, cadence) = %d, want 2", got)
	}
	// The same population judged on exposure loses the first gap, which is the
	// whole reason both measures are reported rather than one being picked.
	if got := over(gaps, 12*time.Hour, gap.Exposure); got != 1 {
		t.Errorf("over(12h, exposure) = %d, want 1", got)
	}
	if got := over(gaps, 24*time.Hour, gap.Cadence); got != 0 {
		t.Errorf("over(24h, cadence) = %d, want 0", got)
	}
}

func TestMedianOfAnEvenCountAveragesTheMiddlePair(t *testing.T) {
	if got := median([]time.Duration{4 * time.Hour, 1 * time.Hour, 3 * time.Hour, 2 * time.Hour}); got != 150*time.Minute {
		t.Errorf("median = %s, want 2h30m", got)
	}
	if got := median([]time.Duration{3 * time.Hour, 1 * time.Hour, 2 * time.Hour}); got != 2*time.Hour {
		t.Errorf("median = %s, want 2h", got)
	}
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %s, want 0", got)
	}
}

// The measurement behind the document's central distinction. Evidence dated
// inside a gap that no run had seen until after the gap ended is evidence that
// SURVIVED the gap, which is what makes a Biome or knowledgeC gap latency
// rather than loss. The test runs against a database migrated by the real
// migrations, so it cannot pass against a schema the harvester does not have.
func TestRecoveredCountsEvidenceNoRunSawUntilAfterTheGap(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	db := openRaw(t, path)

	// Two runs either side of a gap, and one long before it.
	before := insertRun(t, db, "biome-appusage", "2026-08-12T02:09:04.000000Z")
	after := insertRun(t, db, "biome-appusage", "2026-08-12T21:08:22.000000Z")

	// Dated inside the gap, first seen only by the run after it: survived.
	insertEvent(t, db, after, "biome-appusage", "2026-08-12T10:00:00.000000Z")
	insertEvent(t, db, after, "claude-transcripts", "2026-08-12T11:00:00.000000Z")
	// Dated inside the gap but seen by a run that predates it, which cannot
	// happen in the field and is here to prove the predicate is on the RUN's
	// time and not merely on the event's.
	insertEvent(t, db, before, "biome-appusage", "2026-08-12T12:00:00.000000Z")
	// Dated outside the gap.
	insertEvent(t, db, after, "biome-appusage", "2026-08-12T21:30:00.000000Z")
	st.Close()

	g := gap{
		PrevStart: at("2026-08-12T02:09:04Z"),
		PrevEnd:   at("2026-08-12T02:12:39Z"),
		Start:     at("2026-08-12T21:08:22Z"),
	}
	got, total, err := recovered(db, g)
	if err != nil {
		t.Fatalf("recovered() = %v", err)
	}
	if total != 2 {
		t.Errorf("total = %d, want 2", total)
	}
	if len(got) != 2 {
		t.Fatalf("got %d sources, want 2: %v", len(got), got)
	}
	if got[0].Source != "biome-appusage" || got[0].N != 1 {
		t.Errorf("first source = %+v, want biome-appusage 1", got[0])
	}
}

func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open raw: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func insertRun(t *testing.T, db *sql.DB, source, started string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO harvest_run (source_id, started_at, finished_at, input, status)
	                     VALUES (?, ?, ?, 'x', 'ok')`, source, started, started)
	if err != nil {
		t.Fatalf("insert run: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("run id: %v", err)
	}
	return id
}

func insertEvent(t *testing.T, db *sql.DB, run int64, source, started string) {
	t.Helper()
	_, err := db.Exec(`INSERT INTO event (first_seen_run, source_id, shape, started_at, ended_at, locator)
	                   VALUES (?, ?, 'point', ?, ?, '/x')`, run, source, started, started)
	if err != nil {
		t.Fatalf("insert event: %v", err)
	}
}

// A regression. short trimmed the zero tail off a duration string, and
// trimming "0m" off "10m0s" left "1" — so `-grid 10m` labelled itself "a fixed
// 1 grid" in the very line that tells a reader which method produced the
// figures below it.
func TestShortTrimsOnlyTheZeroTail(t *testing.T) {
	for _, c := range []struct {
		in   time.Duration
		want string
	}{
		{12 * time.Hour, "12h"},
		{10 * time.Minute, "10m"},
		{time.Second, "1s"},
		{90 * time.Minute, "1h30m"},
		{1500 * time.Millisecond, "1.5s"},
	} {
		if got := short(c.in); got != c.want {
			t.Errorf("short(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}

// Windowing is how this project has twice hidden its own worst case. A window
// must therefore be able to say what it left out, and the excluded population
// has to be measured rather than assumed empty.
func TestExcludedReportsTheWidestGapOutsideTheWindow(t *testing.T) {
	all := []gap{
		{PrevStart: at("2026-08-07T19:06:58Z"), PrevEnd: at("2026-08-07T19:10:38Z"), Start: at("2026-08-10T01:49:00Z")},
		{PrevStart: at("2026-08-10T01:49:00Z"), PrevEnd: at("2026-08-10T01:55:05Z"), Start: at("2026-08-11T01:09:57Z")},
	}
	kept := all[1:]
	n, widest := excluded(all, kept)
	if n != 1 {
		t.Fatalf("excluded count = %d, want 1", n)
	}
	if want := at("2026-08-10T01:49:00Z").Sub(at("2026-08-07T19:10:38Z")); widest.Exposure() != want {
		t.Errorf("widest excluded exposure = %s, want %s", widest.Exposure(), want)
	}
}
