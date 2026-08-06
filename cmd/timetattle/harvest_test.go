package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/knowledgec"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// These tests cover the ingestion phase, which is the half of a harvest that
// cannot be proved by looking at the files on disk. Two properties carry most of
// the weight and neither is visible from inside a single package: a source that
// fails must lose only its own events, and re-reading a snapshot must add
// nothing.
//
// Each is written twice where it can be. The synthetic version describes rows a
// real snapshot may not happen to hold; the real one reads whatever the last
// harvest actually wrote and skips when there is none, following
// internal/store/knowledgec_realdata_test.go.

// The source identifiers a harvest is expected to record, spelled out rather
// than read back from the sources themselves. A renamed identifier is a break in
// every stored row's provenance, so it should fail a test rather than quietly
// travel through.
const (
	biomeAppUsage     = "biome-appusage"
	claudeTranscripts = "claude-transcripts"
	kcBacklight       = "knowledgec-backlit"
	kcLocked          = "knowledgec-islocked"
	kcAppUsage        = "knowledgec-appusage"
	kcMediaUsage      = "knowledgec-mediausage"
)

// newStore opens a store under the test's own directory. Nothing here may touch
// ~/.timetattle/timetattle.db: these tests write thousands of real observations,
// and a test that pollutes the machine's own store is worse than no test.
func newStore(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "timetattle", "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { st.Close() })
	return st, path
}

// recordedRun is one harvest_run row, read straight out of the database rather
// than from the Run the writer was holding.
type recordedRun struct {
	sourceID string
	input    string
	since    sql.NullString
	status   string
	failure  sql.NullString
	seen     int
	added    int
}

func harvestRuns(t *testing.T, path string) []recordedRun {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT source_id, input, since, status, error, events_seen, events_new
		FROM harvest_run ORDER BY id`)
	if err != nil {
		t.Fatalf("read harvest_run from %s: %v", path, err)
	}
	defer rows.Close()

	var out []recordedRun
	for rows.Next() {
		var r recordedRun
		if err := rows.Scan(&r.sourceID, &r.input, &r.since, &r.status, &r.failure, &r.seen, &r.added); err != nil {
			t.Fatalf("read a harvest_run row: %v", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read harvest_run rows: %v", err)
	}
	return out
}

func eventCount(t *testing.T, path string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("count events in %s: %v", path, err)
	}
	return n
}

func eventCountFor(t *testing.T, path, sourceID string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()

	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM event WHERE source_id = ?`, sourceID).Scan(&n); err != nil {
		t.Fatalf("count %s events in %s: %v", sourceID, path, err)
	}
	return n
}

// byStatus indexes recorded runs by source, failing on a repeat: one row per
// (invocation, source) is the property most of these tests turn on, so two rows
// for one source must not be silently overwritten here.
func bySource(t *testing.T, runs []recordedRun) map[string]recordedRun {
	t.Helper()
	out := make(map[string]recordedRun, len(runs))
	for _, r := range runs {
		if _, seen := out[r.sourceID]; seen {
			t.Fatalf("%s has more than one harvest_run row for a single ingestion", r.sourceID)
		}
		out[r.sourceID] = r
	}
	return out
}

// kcRow is one ZOBJECT row for a synthetic snapshot.
type kcRow struct {
	stream string
	start  time.Time
	end    time.Time
	value  sql.NullInt64
	text   sql.NullString
}

func flagRow(stream string, start, end time.Time, value int64) kcRow {
	return kcRow{stream: stream, start: start, end: end, value: sql.NullInt64{Int64: value, Valid: true}}
}

func appRow(stream string, start, end time.Time, bundle string) kcRow {
	return kcRow{stream: stream, start: start, end: end, text: sql.NullString{String: bundle, Valid: true}}
}

// writeKnowledgeC builds a snapshot-shaped database. It is a plain .db rather
// than a .db.gz, which internal/knowledgec opens transparently, so nothing here
// depends on the compression a real harvest applies.
func writeKnowledgeC(t *testing.T, path string, rows ...kcRow) string {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer db.Close()

	if _, err := db.Exec(`CREATE TABLE ZOBJECT (
		Z_PK INTEGER PRIMARY KEY,
		ZSTREAMNAME TEXT,
		ZVALUESTRING TEXT,
		ZVALUEINTEGER INTEGER,
		ZSTARTDATE REAL,
		ZENDDATE REAL)`); err != nil {
		t.Fatalf("create ZOBJECT: %v", err)
	}
	for _, r := range rows {
		if _, err := db.Exec(`INSERT INTO ZOBJECT
			(ZSTREAMNAME, ZVALUEINTEGER, ZVALUESTRING, ZSTARTDATE, ZENDDATE) VALUES (?, ?, ?, ?, ?)`,
			r.stream, r.value, r.text,
			float64(r.start.Unix()-978307200), float64(r.end.Unix()-978307200)); err != nil {
			t.Fatalf("insert a %s row: %v", r.stream, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
	return path
}

// populatedSnapshot holds rows in all four streams, so every knowledgeC source
// has something to report and a source that reported nothing is a real failure
// rather than an empty fixture.
func populatedSnapshot(t *testing.T) string {
	t.Helper()
	base := time.Date(2026, 7, 14, 9, 0, 0, 0, time.Local)
	return writeKnowledgeC(t, filepath.Join(t.TempDir(), "kc.db"),
		flagRow("/display/isBacklit", base, base.Add(30*time.Minute), 1),
		flagRow("/display/isBacklit", base.Add(30*time.Minute), base.Add(time.Hour), 0),
		flagRow("/device/isLocked", base.Add(time.Hour), base.Add(90*time.Minute), 1),
		appRow("/app/usage", base, base.Add(20*time.Minute), "com.apple.Safari"),
		appRow("/app/mediaUsage", base.Add(5*time.Minute), base.Add(6*time.Minute), "com.apple.Safari"),
	)
}

// TestIngestingTheSameSnapshotTwiceAddsNothing is the acceptance test for the
// whole no-watermark design as the CLI now performs it. since is the zero time on
// every run, so a second harvest re-reads the same window in full and the store's
// dedup is the only thing standing between that and duplicated history.
//
// The Biome source is deliberately not in this plan. Its input is the live
// stream, which can genuinely grow between the two ingestions, and a test that
// fails when somebody switches applications is testing the machine rather than
// the code.
func TestIngestingTheSameSnapshotTwiceAddsNothing(t *testing.T) {
	path := populatedSnapshot(t)
	st, storePath := newStore(t)
	ctx := context.Background()

	if err := ingest(ctx, st, snapshotIngestions(path), io.Discard); err != nil {
		t.Fatalf("first ingestion: %v", err)
	}
	first := bySource(t, harvestRuns(t, storePath))
	rows := eventCount(t, storePath)
	if rows == 0 {
		t.Fatal("the first ingestion stored nothing; this fixture cannot prove anything about dedup")
	}
	total := 0
	for id, r := range first {
		if r.status != string(store.StatusOK) {
			t.Fatalf("%s: status %q, error %q", id, r.status, r.failure.String)
		}
		total += r.added
	}
	if total != rows {
		t.Errorf("the runs claim %d new events but the store holds %d rows", total, rows)
	}

	if err := ingest(ctx, st, snapshotIngestions(path), io.Discard); err != nil {
		t.Fatalf("second ingestion: %v", err)
	}
	if after := eventCount(t, storePath); after != rows {
		t.Errorf("the store grew from %d rows to %d on a re-ingestion of %s", rows, after, path)
	}
	runs := harvestRuns(t, storePath)
	if len(runs) != 2*len(first) {
		t.Fatalf("two ingestions of %d sources recorded %d runs, want %d", len(first), len(runs), 2*len(first))
	}
	for _, r := range runs[len(first):] {
		if r.status != string(store.StatusOK) {
			t.Errorf("%s: second run status %q, error %q", r.sourceID, r.status, r.failure.String)
		}
		if r.added != 0 {
			t.Errorf("%s: re-ingesting %s added %d rows, want 0", r.sourceID, path, r.added)
		}
		if r.seen == 0 {
			t.Errorf("%s: the second run saw nothing; a re-read reads everything again", r.sourceID)
		}
	}
}

// TestEverySourceGetsItsOwnHarvestRun pins the granularity of provenance. A row
// per invocation would make a lost source invisible, which is the whole reason
// harvest_run has partial-failure semantics.
//
// The Biome source is included here because the point is the shape of what was
// recorded, not what it found: whether the live stream is readable decides its
// status and nothing else.
func TestEverySourceGetsItsOwnHarvestRun(t *testing.T) {
	path := populatedSnapshot(t)
	plan, err := harvestIngestions(path)
	if err != nil {
		t.Fatalf("harvestIngestions: %v", err)
	}
	want := []string{biomeAppUsage, claudeTranscripts, kcBacklight, kcLocked, kcAppUsage, kcMediaUsage}
	if len(plan) != len(want) {
		t.Fatalf("the plan holds %d sources, want %d", len(plan), len(want))
	}

	st, storePath := newStore(t)
	// A failure is not asserted against: the live Biome stream needs Full Disk
	// Access, and a machine without it must still record the four snapshot runs.
	if err := ingest(context.Background(), st, plan, io.Discard); err != nil {
		t.Logf("ingestion reported: %v", err)
	}

	runs := harvestRuns(t, storePath)
	if len(runs) != len(want) {
		t.Fatalf("%d sources produced %d harvest_run rows, want %d", len(plan), len(runs), len(want))
	}
	recorded := bySource(t, runs)
	for _, id := range want {
		r, ok := recorded[id]
		if !ok {
			t.Errorf("no harvest_run row for %s", id)
			continue
		}
		if r.input == "" {
			t.Errorf("%s recorded no input", id)
		}
		// since is the zero time on every run, which the store writes as NULL.
		if r.since.Valid {
			t.Errorf("%s recorded since=%q; every harvest reads everything", id, r.since.String)
		}
	}
	for _, id := range []string{kcBacklight, kcLocked, kcAppUsage, kcMediaUsage} {
		if r := recorded[id]; r.status != string(store.StatusOK) {
			t.Errorf("%s: status %q, error %q", id, r.status, r.failure.String)
		}
		if recorded[id].input != path {
			t.Errorf("%s recorded input %q, want the snapshot this run wrote, %q", id, recorded[id].input, path)
		}
	}
}

// TestABrokenSourceLosesOnlyItsOwnEvents is the partial-failure guarantee. One
// unreadable path must cost that stream and nothing else, and the loss has to be
// recorded: a source that failed and a source with nothing to say are different
// facts, and only the status tells them apart.
func TestABrokenSourceLosesOnlyItsOwnEvents(t *testing.T) {
	good := populatedSnapshot(t)
	missing := filepath.Join(t.TempDir(), "not-a-snapshot.db")

	plan := snapshotIngestions(good)
	if len(plan) != 4 {
		t.Fatalf("a snapshot plan holds %d sources, want the four knowledgeC streams", len(plan))
	}
	if plan[0].source.ID() != kcBacklight {
		t.Fatalf("the plan starts with %s, not %s; this test breaks the wrong source", plan[0].source.ID(), kcBacklight)
	}
	plan[0] = ingestion{source: knowledgec.NewBacklight(missing), input: missing}

	st, storePath := newStore(t)
	summary := &bytes.Buffer{}
	err := ingest(context.Background(), st, plan, summary)
	if err == nil {
		t.Fatal("ingestion reported success with an unreadable source; the CLI would exit 0 on a lost stream")
	}

	// Every source appears in the summary, including the lost one. A source
	// missing from the list is indistinguishable from a source with nothing to
	// report, which is the one thing that must never be true here.
	for _, p := range plan {
		if !strings.Contains(summary.String(), p.source.ID()+":") {
			t.Errorf("the summary does not mention %s:\n%s", p.source.ID(), summary)
		}
	}
	if !strings.Contains(summary.String(), kcBacklight+": lost") {
		t.Errorf("the summary does not say %s was lost:\n%s", kcBacklight, summary)
	}

	recorded := bySource(t, harvestRuns(t, storePath))
	if len(recorded) != len(plan) {
		t.Fatalf("%d sources produced %d harvest_run rows", len(plan), len(recorded))
	}

	broken := recorded[kcBacklight]
	if broken.status != string(store.StatusFailed) {
		t.Errorf("%s: status %q, want %q", kcBacklight, broken.status, store.StatusFailed)
	}
	if broken.failure.String == "" {
		t.Errorf("%s failed with no reason recorded", kcBacklight)
	}
	if broken.added != 0 {
		t.Errorf("%s stored %d events from a path that does not exist", kcBacklight, broken.added)
	}

	for _, id := range []string{kcLocked, kcAppUsage, kcMediaUsage} {
		r := recorded[id]
		if r.status != string(store.StatusOK) {
			t.Errorf("%s: status %q, error %q; one broken source must not stop the others", id, r.status, r.failure.String)
		}
		if r.added == 0 {
			t.Errorf("%s stored nothing, but %s holds rows for it", id, good)
		}
		if stored := eventCountFor(t, storePath, id); stored != r.added {
			t.Errorf("%s: the run claims %d new events but the store holds %d", id, r.added, stored)
		}
	}
	if eventCountFor(t, storePath, kcBacklight) != 0 {
		t.Errorf("%s stored events despite failing", kcBacklight)
	}
}

// TestASourceWithNothingToReportSucceeds keeps the one distinction this project
// has collapsed before. "Nothing new" and "something broke" are different, and a
// harvest that treated an empty stream as a failure would make a quiet week look
// like an outage — and, worse, exit non-zero on a machine that was simply idle.
func TestASourceWithNothingToReportSucceeds(t *testing.T) {
	empty := writeKnowledgeC(t, filepath.Join(t.TempDir(), "empty.db"))
	st, storePath := newStore(t)

	if err := ingest(context.Background(), st, snapshotIngestions(empty), io.Discard); err != nil {
		t.Fatalf("ingesting a snapshot with no rows: %v", err)
	}
	if n := eventCount(t, storePath); n != 0 {
		t.Errorf("a snapshot with no rows produced %d events", n)
	}

	runs := harvestRuns(t, storePath)
	if len(runs) != 4 {
		t.Fatalf("recorded %d runs, want 4", len(runs))
	}
	for _, r := range runs {
		if r.status != string(store.StatusOK) {
			t.Errorf("%s: status %q, error %q; a stream with nothing to report was read through",
				r.sourceID, r.status, r.failure.String)
		}
		if r.seen != 0 || r.added != 0 {
			t.Errorf("%s: seen=%d new=%d, want 0 and 0", r.sourceID, r.seen, r.added)
		}
		if !r.failure.Valid || r.failure.String == "" {
			continue
		}
		t.Errorf("%s recorded an error on a successful run: %q", r.sourceID, r.failure.String)
	}
}

// TestTheStoreFlagRedirectsWrites goes through the CLI entry point, because that
// is where the flag has to be wired. The machine's own store must be untouched:
// every other test here can use a helper, and this one cannot, so it is the only
// place the default path could leak in.
func TestTheStoreFlagRedirectsWrites(t *testing.T) {
	dir := t.TempDir()
	storePath := filepath.Join(dir, "store", "timetattle.db")
	src := populatedSnapshot(t)

	defaultPath, err := store.DefaultPath()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	before := describe(defaultPath)

	// A Biome root that is not there is what a machine without the stream looks
	// like, and TakeStreams reports it as nothing to capture rather than an error.
	err = harvest([]string{
		"-source", src,
		"-dir", filepath.Join(dir, "snapshots"),
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-store", storePath,
		"-quiet",
	})
	// The live Biome source is in the registry and needs Full Disk Access, so its
	// failure is not this test's business; where the writes landed is.
	if err != nil {
		t.Logf("harvest reported: %v", err)
	}

	if after := describe(defaultPath); after != before {
		t.Errorf("the default store at %s changed from %q to %q; -store did not redirect the writes",
			defaultPath, before, after)
	}

	recorded := bySource(t, harvestRuns(t, storePath))
	if len(recorded) == 0 {
		t.Fatalf("%s holds no harvest_run rows", storePath)
	}
	backlight := recorded[kcBacklight]
	if backlight.status != string(store.StatusOK) {
		t.Fatalf("%s: status %q, error %q", kcBacklight, backlight.status, backlight.failure.String)
	}
	if backlight.added == 0 {
		t.Errorf("%s stored nothing from %s", kcBacklight, src)
	}
	// The input recorded is the snapshot this run wrote, not the database it was
	// taken from: a run that recorded the source database would name a file the
	// events cannot be re-read from.
	if backlight.input == src {
		t.Errorf("%s recorded the live database %s as its input rather than the snapshot", kcBacklight, src)
	}
	if _, err := os.Stat(backlight.input); err != nil {
		t.Errorf("%s recorded input %q, which cannot be read back: %v", kcBacklight, backlight.input, err)
	}
}

// describe renders whatever can be learned about a path in one comparable
// string, including the error. os.Stat on a home directory this process cannot
// read fails rather than reporting absence, and "the stat failed the same way
// twice" is the honest form of "nothing changed" — collapsing an error into
// "not there" is the mistake that would let a real write through unnoticed.
func describe(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "stat error: " + err.Error()
	}
	return info.ModTime().Format(time.RFC3339Nano) + " " + time.Duration(info.Size()).String()
}

// TestEveryRegisteredSourceHasAnInput fails when a source joins the package-level
// registry that harvest cannot say what it read. There is no getter for an input
// on the interface, so this mapping is by hand, and a new collector that
// registered itself would otherwise be ingested with a run that misreports its
// provenance — or dropped.
func TestEveryRegisteredSourceHasAnInput(t *testing.T) {
	registered := source.Sources()
	if len(registered) == 0 {
		t.Skip("the source registry is empty; internal/transcripts and internal/biome are the only " +
			"packages that register, and each does so only when its Default* root resolves at " +
			"process start")
	}
	for _, s := range registered {
		if _, ok := registeredInputs[s.ID()]; !ok {
			t.Errorf("source %q is registered but harvest cannot name what it reads; "+
				"add it to registeredInputs", s.ID())
		}
	}
}
