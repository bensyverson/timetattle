package main

import (
	"bytes"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/store"
)

// These tests are task 4tuVv. `timetattle status` has two sections, and until
// now only one of them was fed by anything the harvest recorded.
//
// The SOURCE table reads harvest_run. The SNAPSHOTS section read the snapshot
// DIRECTORY — it counted files. So a capture that failed today left yesterday's
// files exactly where they were and status printed a plausible count with no
// failure signal at all. The only hint was the "newest" mtime, and only to a
// reader who already knew what it should have been.
//
// That is the phase whose silent failure is most expensive. Ingestion reads
// durable snapshots and can be repeated at any hour; capture is the one chance
// to hold knowledgeC and Biome before they prune themselves. Between 2026-07-30
// 12:01 and 2026-08-01 08:08 the captures failed on a lost Full Disk Access
// grant for 44 hours. Ingestion's failures are still in the live store as
// harvest_run rows 19 and 21 through 24. The capture failures left no row of
// any kind.

// capturedRow is one capture_run row, read straight out of the database so that
// a test looks at what is in the columns rather than at what the store says is
// there.
type capturedRow struct {
	kind       string
	input      string
	startedAt  string
	finishedAt string
	status     string
	failure    sql.NullString
	filesNew   int
	filesHeld  int
	bytes      int64
}

func captureRows(t *testing.T, path string) []capturedRow {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT kind, input, started_at, finished_at, status, error,
		files_new, files_held, bytes FROM capture_run ORDER BY id`)
	if err != nil {
		t.Fatalf("read capture_run from %s: %v", path, err)
	}
	defer rows.Close()

	var out []capturedRow
	for rows.Next() {
		var c capturedRow
		if err := rows.Scan(&c.kind, &c.input, &c.startedAt, &c.finishedAt, &c.status,
			&c.failure, &c.filesNew, &c.filesHeld, &c.bytes); err != nil {
			t.Fatalf("read a capture_run row: %v", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read capture_run rows: %v", err)
	}
	return out
}

// capturesByKind keeps the LAST row for each kind, which is that kind's most
// recent attempt.
func capturesByKind(t *testing.T, path string) map[string]capturedRow {
	t.Helper()
	out := map[string]capturedRow{}
	for _, c := range captureRows(t, path) {
		out[c.kind] = c
	}
	return out
}

// captureLine returns the report's line for one capture, which is what a reader
// actually looks at.
func captureLine(t *testing.T, report, label string) string {
	t.Helper()
	for line := range strings.SplitSeq(report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), label+" ") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no line for the %s capture in:\n%s", label, report)
	return ""
}

// backdateCaptures moves every capture_run row back by d, leaving harvest_run
// alone. That is the state this whole task is about, isolated: ingestion still
// running, capture silently stopped.
func backdateCaptures(t *testing.T, path string, d time.Duration) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()

	rows := captureRows(t, path)
	for i, c := range rows {
		started, err := time.Parse(store.TimeLayout, c.startedAt)
		if err != nil {
			t.Fatalf("parse started_at %q: %v", c.startedAt, err)
		}
		finished, err := time.Parse(store.TimeLayout, c.finishedAt)
		if err != nil {
			t.Fatalf("parse finished_at %q: %v", c.finishedAt, err)
		}
		if _, err := db.Exec(`UPDATE capture_run SET started_at = ?, finished_at = ? WHERE id = ?`,
			started.Add(-d).UTC().Format(store.TimeLayout),
			finished.Add(-d).UTC().Format(store.TimeLayout), i+1); err != nil {
			t.Fatalf("backdate capture_run row %d: %v", i+1, err)
		}
	}
}

// forgetCaptures deletes every capture_run row, leaving the files and every
// harvest_run row exactly as they were. It is the mutation that produces a
// store which preserved things but recorded nothing about doing so.
func forgetCaptures(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()
	if _, err := db.Exec(`DELETE FROM capture_run`); err != nil {
		t.Fatalf("delete capture_run rows: %v", err)
	}
}

// statusCaptureReport renders the report at a chosen instant, ignoring the exit
// status, for the tests that compare two renderings against each other.
func statusCaptureReport(t *testing.T, p partialHarvest, now time.Time) string {
	t.Helper()
	var out bytes.Buffer
	//nolint:errcheck // the verdict is asserted separately where it matters
	_ = reportStatus(&out, statusOptions{
		store: p.storePath, snapshots: p.snapDir, stale: 24 * time.Hour, now: now,
	})
	return out.String()
}

// TestEveryCaptureRecordsWhatItDid is the row that did not exist. All four
// captures run on every harvest, and every one of them must leave a record of
// its outcome whether it preserved anything or not.
func TestEveryCaptureRecordsWhatItDid(t *testing.T) {
	p := newPartialHarvest(t)
	src := writeKnowledgeC(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err != nil {
		t.Fatalf("a harvest with everything readable failed: %v", err)
	}

	got := capturesByKind(t, p.storePath)
	for _, kind := range []string{captureKindKnowledgeC, captureKindBiome, captureKindVault, captureKindFiledays} {
		c, ok := got[kind]
		if !ok {
			t.Errorf("no capture_run row for %s; a capture that left no record is a capture status cannot see", kind)
			continue
		}
		if c.status != string(store.CaptureOK) {
			t.Errorf("%s: status %q, want %q", kind, c.status, store.CaptureOK)
		}
		if c.input == "" {
			t.Errorf("%s: the row names nothing it read", kind)
		}
		if c.startedAt == "" || c.finishedAt == "" {
			t.Errorf("%s: the row has no span (%q..%q)", kind, c.startedAt, c.finishedAt)
		}
	}
	if c := got[captureKindBiome]; c.filesNew != 2 {
		t.Errorf("the Biome capture recorded %d new files, want 2 (the fixture lays down two segments)", c.filesNew)
	}
	if c := got[captureKindVault]; c.filesNew != 1 {
		t.Errorf("the Motion vault capture recorded %d new files, want 1", c.filesNew)
	}
}

// TestAFailedCaptureIsVisibleAfterTheRun is criterion SqV, and it is the
// incident reproduced. The first harvest preserves everything. knowledgeC then
// becomes unreadable, exactly as it did when a reinstall revoked Full Disk
// Access, and the second harvest leaves the first run's snapshot sitting in the
// directory. Counting files reports a healthy number. It did, for 44 hours.
func TestAFailedCaptureIsVisibleAfterTheRun(t *testing.T) {
	p := newPartialHarvest(t)
	good := writeKnowledgeC(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, good); err != nil {
		t.Fatalf("the first harvest failed: %v", err)
	}
	onDisk := countFiles(t, p.snapDir)
	if onDisk == 0 {
		t.Fatal("the first harvest preserved nothing, so there is no stale evidence to be fooled by")
	}

	broken := unreadable(t, filepath.Join(p.dir, "broken", "knowledgeC.db"))
	if err := p.run(t, broken); err == nil {
		t.Fatal("the second harvest reported success with an unreadable knowledgeC")
	}
	if after := countFiles(t, p.snapDir); after < onDisk {
		t.Fatalf("the failed run removed preserved files (%d then %d); the fixture no longer reproduces the incident",
			onDisk, after)
	}

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: p.storePath, snapshots: p.snapDir, stale: 24 * time.Hour, now: time.Now(),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	if err == nil {
		t.Error("status exited zero after a failed knowledgeC capture; a scheduler would never notice")
	}
	line := captureLine(t, got, "knowledgeC")
	if !strings.Contains(line, string(store.CaptureFailed)) {
		t.Errorf("the knowledgeC line does not say the capture failed: %q", line)
	}
	// The Biome capture succeeded on the same run and must not be tarred with it.
	if biome := captureLine(t, got, "Biome"); strings.Contains(biome, string(store.CaptureFailed)) {
		t.Errorf("the Biome capture succeeded and its line says failed: %q", biome)
	}
	if !strings.Contains(got, broken) {
		t.Errorf("the report never names the file that could not be read (%s):\n%s", broken, got)
	}
}

// TestAStaleCaptureIsToldFromAFreshUnchangedOne is criterion t38, and it is the
// distinction the file count structurally cannot make. Both halves below show
// the SAME files on disk with the SAME mtimes. Only the recorded capture tells
// them apart.
func TestAStaleCaptureIsToldFromAFreshUnchangedOne(t *testing.T) {
	p := newPartialHarvest(t)
	src := writeKnowledgeC(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err != nil {
		t.Fatalf("the first harvest failed: %v", err)
	}
	// A second harvest over an unchanged Biome stream and an unchanged vault:
	// both are content-addressed, so this run preserves no new file at all. It
	// is a healthy run that wrote nothing, which is the state that must not read
	// as a stale one.
	if err := p.run(t, src); err != nil {
		t.Fatalf("the second harvest failed: %v", err)
	}

	if c := capturesByKind(t, p.storePath)[captureKindBiome]; c.filesNew != 0 || c.filesHeld != 2 {
		t.Fatalf("the second Biome capture recorded %d new and %d held, want 0 and 2; "+
			"the fixture is not producing a fresh-but-unchanged capture", c.filesNew, c.filesHeld)
	}

	var fresh bytes.Buffer
	freshErr := reportStatus(&fresh, statusOptions{
		store: p.storePath, snapshots: p.snapDir, stale: 24 * time.Hour, now: time.Now(),
	})
	t.Logf("fresh:\n%s", fresh.String())
	if freshErr != nil {
		t.Errorf("status failed on a harvest whose captures all succeeded: %v", freshErr)
	}
	freshLine := captureLine(t, fresh.String(), "Biome")

	// Now the only thing that changes is WHEN the captures last ran. Every file
	// is byte-identical and every harvest_run row is untouched, so a report that
	// reads the directory alone cannot tell this from the state above.
	backdateCaptures(t, p.storePath, 48*time.Hour)

	var stale bytes.Buffer
	staleErr := reportStatus(&stale, statusOptions{
		store: p.storePath, snapshots: p.snapDir, stale: 24 * time.Hour, now: time.Now(),
	})
	t.Logf("stale:\n%s", stale.String())
	if staleErr == nil {
		t.Error("status exited zero with captures 48h old and a 24h staleness allowance")
	}
	staleLine := captureLine(t, stale.String(), "Biome")
	if staleLine == freshLine {
		t.Errorf("a capture 48h stale renders identically to one that ran just now and found nothing new: %q", freshLine)
	}
	if !strings.Contains(stale.String(), "stale") {
		t.Errorf("the report never uses the word stale:\n%s", stale.String())
	}
}

// TestACaptureThatNeverRanIsNotACaptureThatFoundNothing is the third state, and
// the standing lesson: absence of a row is not an observation. A store with no
// capture_run rows at all — every harvest that wrote it predates this table, or
// died before reaching the store — must say so rather than reporting whatever
// files happen to be lying in the directory.
func TestACaptureThatNeverRanIsNotACaptureThatFoundNothing(t *testing.T) {
	p := newPartialHarvest(t)
	src := writeKnowledgeC(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err != nil {
		t.Fatalf("harvest: %v", err)
	}
	recorded := captureLine(t, statusCaptureReport(t, p, time.Now()), "Biome")

	// The files stay exactly where they are; only the record of how they got
	// there is removed.
	forgetCaptures(t, p.storePath)
	if n := countFiles(t, snapshot.BiomeDir(p.snapDir)); n != 2 {
		t.Fatalf("the Biome directory holds %d files after forgetting the rows, want 2", n)
	}
	unrecorded := captureLine(t, statusCaptureReport(t, p, time.Now()), "Biome")

	if unrecorded == recorded {
		t.Errorf("a capture nothing recorded renders identically to one that ran and succeeded: %q", recorded)
	}
	if !strings.Contains(unrecorded, "no capture recorded") {
		t.Errorf("the line for a capture with no row does not say so in those words: %q", unrecorded)
	}
}

// TestAnAbsentCaptureIsNotAFailure holds the tolerance the source table already
// grants: a machine with no Motion installed and no filedays roots configured
// is an ordinary machine, not a broken one, and a check that cries wolf is a
// check nobody reads.
func TestAnAbsentCaptureIsNotAFailure(t *testing.T) {
	p := newPartialHarvest(t)
	p.vaultRoot = filepath.Join(p.dir, "no-such-vault")
	p.filedays = ""
	src := writeKnowledgeC(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err != nil {
		t.Fatalf("a harvest on a machine with no Motion and no filedays roots failed: %v", err)
	}

	got := capturesByKind(t, p.storePath)
	for _, kind := range []string{captureKindVault, captureKindFiledays} {
		c, ok := got[kind]
		if !ok {
			t.Errorf("no capture_run row for %s; a harvest that looked and found nothing recorded nothing, "+
				"which is indistinguishable from a harvest that never looked", kind)
			continue
		}
		if c.status != string(store.CaptureAbsent) {
			t.Errorf("%s: status %q, want %q", kind, c.status, store.CaptureAbsent)
		}
	}

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: p.storePath, snapshots: p.snapDir, stale: 24 * time.Hour, now: time.Now(),
	})
	t.Logf("report:\n%s", out.String())
	if err != nil {
		t.Errorf("status failed on a machine that simply has no Motion and no filedays roots: %v", err)
	}
	vault := captureLine(t, out.String(), "Motion vault")
	if strings.Contains(vault, string(store.CaptureFailed)) {
		t.Errorf("an absent Motion vault is reported as a failure: %q", vault)
	}
	if strings.Contains(vault, "no capture recorded") {
		t.Errorf("a harvest DID look at the vault, and the line says nothing was recorded: %q", vault)
	}
}

// TestStatusSurvivesAStoreWithoutTheCaptureTable is the case this command may
// least afford to get wrong. `timetattle status` deliberately never migrates the
// store it is checking, so between this code landing and the next successful
// harvest the live store is still on 0006 and has no capture_run table at all.
// A report that aborted with a SQL error there would be broken at exactly the
// moment it is reached for — and the store on this machine has been sitting on
// 0006 since 2026-08-01T02:52:06Z.
func TestStatusSurvivesAStoreWithoutTheCaptureTable(t *testing.T) {
	p := newPartialHarvest(t)
	src := writeKnowledgeC(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err != nil {
		t.Fatalf("harvest: %v", err)
	}
	dropCaptureTable(t, p.storePath)

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: p.storePath, snapshots: p.snapDir, stale: 24 * time.Hour, now: time.Now(),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	// The source table is what a reader came for, and it is still there.
	if !strings.Contains(got, "biome-appusage") {
		t.Errorf("the source table is missing; a store one migration behind lost the whole report:\n%s", got)
	}
	line := captureLine(t, got, "Biome")
	if !strings.Contains(line, "no capture recorded") {
		t.Errorf("the Biome line does not say no capture was recorded: %q", line)
	}
	// And it is not silent about why. A report that quietly printed "no capture
	// recorded" over an unreadable table would be claiming an observation it
	// never made, which is the exact confusion this task exists to remove.
	if !strings.Contains(got, "capture_run") {
		t.Errorf("the report never says the capture record itself could not be read:\n%s", got)
	}
	if err == nil {
		t.Error("status exited zero although it could not read the capture record at all")
	}
	// The all-clear is the one line in this report a reader is entitled to
	// trust absolutely, and it may not be printed over a problem already
	// reported higher up.
	if strings.Contains(got, "every source ran and succeeded") {
		t.Errorf("the report signs off with an all-clear over a capture record it could not read:\n%s", got)
	}
}

// dropCaptureTable makes a store look like one migrated only as far as 0006.
func dropCaptureTable(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()
	if _, err := db.Exec(`DROP TABLE capture_run`); err != nil {
		t.Fatalf("drop capture_run: %v", err)
	}
}
