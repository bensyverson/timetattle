package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/filedays"
	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/store"
)

// These tests cover task NkvFg: the capture phase used to return on the first
// error, and it runs before everything else, so one unreadable source cost
// every other capture and the whole ingestion phase.
//
// That is not hypothetical. Between 2026-07-30 12:01 and 2026-07-31 21:00 the
// scheduled harvest failed with
//
//	timetattle harvest: cannot read .../Knowledge/knowledgeC.db: operation not permitted
//
// three times, and each of those runs also skipped the Biome capture, the
// Motion vault copy, the filedays scan and every ingestion — including the
// sources that need no Full Disk Access at all. macOS keys a Full Disk Access
// grant to an ad-hoc-signed binary's cdhash, so `go install` silently revoked
// it; see docs/content/docs/harvest.md.
//
// The property under test is the one harvest's own help text already claims:
// a source that cannot be read loses only its own evidence.

// unreadable makes a file this process cannot open, which is the closest
// reproduction of a lost Full Disk Access grant that does not involve touching
// anything under ~/Library. A process that can read it anyway is running as
// root, where the mutation proves nothing.
func unreadable(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("not a database"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	// Restored so the test's own TempDir cleanup can remove it.
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("this process can open a 0000 file, so an unreadable source cannot be simulated")
	}
	return path
}

// unreadableDir is the same mutation for a directory, which is what an
// unreadable Biome stream looks like.
func unreadableDir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o700) })
	if _, err := os.ReadDir(path); err == nil {
		t.Skip("this process can list a 0000 directory, so an unreadable stream cannot be simulated")
	}
	return path
}

// biomeRootWith lays a readable Biome stream out the way macOS does, so a
// capture has something real to preserve.
func biomeRootWith(t *testing.T, root string, segments map[string][]byte) string {
	t.Helper()
	local := filepath.Join(root, snapshot.Streams[0], "local")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatalf("create %s: %v", local, err)
	}
	for name, data := range segments {
		if err := os.WriteFile(filepath.Join(local, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return root
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			n++
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	return n
}

// partialHarvest is the fixture the mutation runs against: every capture but
// knowledgeC has real evidence waiting for it, so anything the run drops is
// measurable rather than merely absent.
type partialHarvest struct {
	dir        string
	snapDir    string
	storePath  string
	sourcePath string
	biomeRoot  string
	vaultRoot  string
	filedays   string
}

func newPartialHarvest(t *testing.T) partialHarvest {
	t.Helper()
	dir := t.TempDir()
	p := partialHarvest{
		dir:       dir,
		snapDir:   filepath.Join(dir, "snapshots"),
		storePath: filepath.Join(dir, "store", "timetattle.db"),
		vaultRoot: filepath.Join(dir, "vault"),
		filedays:  filepath.Join(dir, "repo"),
	}
	p.biomeRoot = biomeRootWith(t, filepath.Join(dir, "biome"), map[string][]byte{
		"segment-a": []byte("SEGB-a"),
		"segment-b": []byte("SEGB-b"),
	})
	if err := os.MkdirAll(p.vaultRoot, 0o700); err != nil {
		t.Fatalf("create vault: %v", err)
	}
	if err := os.WriteFile(filepath.Join(p.vaultRoot, "comp.motn"), []byte("autosave"), 0o600); err != nil {
		t.Fatalf("write a vault file: %v", err)
	}
	touchFile(t, filepath.Join(p.filedays, "a.txt"), time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
	touchFile(t, filepath.Join(p.filedays, "b.txt"), time.Date(2026, 7, 11, 11, 0, 0, 0, time.Local))
	return p
}

func (p partialHarvest) run(t *testing.T, source string) error {
	t.Helper()
	return harvest([]string{
		"-source", source,
		"-dir", p.snapDir,
		"-biome", p.biomeRoot,
		"-store", p.storePath,
		"-motion-vault", p.vaultRoot,
		"-filedays-roots", p.filedays,
		"-quiet",
	})
}

// TestAnUnreadableKnowledgeCLosesOnlyItsOwnEvidence is the mutation the whole
// task turns on. Before the fix this run preserved nothing at all and never
// opened the store; the numbers asserted here are what a best-effort capture
// keeps instead.
func TestAnUnreadableKnowledgeCLosesOnlyItsOwnEvidence(t *testing.T) {
	p := newPartialHarvest(t)
	src := unreadable(t, filepath.Join(p.dir, "knowledgeC.db"))

	err := p.run(t, src)
	if err == nil {
		t.Fatal("harvest reported success with an unreadable knowledgeC; a scheduler would never notice the loss")
	}

	// Every capture that did not depend on knowledgeC still happened.
	if n := countFiles(t, snapshot.BiomeDir(p.snapDir)); n != 2 {
		t.Errorf("the Biome capture holds %d files, want 2; an unreadable knowledgeC cost the Biome stream", n)
	}
	if n := countFiles(t, snapshot.VaultDir(p.snapDir)); n != 1 {
		t.Errorf("the Motion vault capture holds %d files, want 1", n)
	}
	snap := readFiledaysSnapshot(t, p.snapDir)
	if len(snap.Roots) != 1 || snap.Roots[0].Raw["2026-07-11"] != 2 {
		t.Errorf("the filedays capture recorded %v, want one root with 2 files on 2026-07-11", snap.Roots)
	}

	// And the ingestion phase ran: the store exists, was migrated, and holds a
	// run row per source rather than nothing at all.
	if _, statErr := os.Stat(p.storePath); statErr != nil {
		t.Fatalf("the store at %s was never opened: %v", p.storePath, statErr)
	}
	runs := harvestRuns(t, p.storePath)
	if len(runs) == 0 {
		t.Fatal("no harvest_run rows; the ingestion phase did not run")
	}
	t.Logf("captured despite the failure: 2 Biome segments, 1 vault file, 1 filedays snapshot, %d harvest_run rows", len(runs))
}

// TestAFailedKnowledgeCCaptureIsRecordedAgainstEveryStream is the other half of
// "absence of a row is not an observation". Skipping the four knowledgeC
// ingestions when their snapshot was never written would leave no row at all,
// and a source with no row is indistinguishable from a source that ran and
// found nothing.
func TestAFailedKnowledgeCCaptureIsRecordedAgainstEveryStream(t *testing.T) {
	p := newPartialHarvest(t)
	src := unreadable(t, filepath.Join(p.dir, "knowledgeC.db"))

	if err := p.run(t, src); err == nil {
		t.Fatal("harvest reported success with an unreadable knowledgeC")
	}

	recorded := bySource(t, harvestRuns(t, p.storePath))
	for _, id := range []string{kcBacklight, kcLocked, kcAppUsage, kcMediaUsage} {
		r, ok := recorded[id]
		if !ok {
			t.Errorf("no harvest_run row for %s; the lost capture left no record against the stream it lost", id)
			continue
		}
		if r.status != string(store.StatusFailed) {
			t.Errorf("%s: status %q, want %q", id, r.status, store.StatusFailed)
		}
		if !strings.Contains(r.failure.String, src) {
			t.Errorf("%s: error %q does not name the database that could not be read (%s)", id, r.failure.String, src)
		}
		if r.seen != 0 || r.added != 0 {
			t.Errorf("%s: seen=%d new=%d, want 0 and 0 for a stream whose snapshot was never written", id, r.seen, r.added)
		}
	}
}

// TestAFailedCaptureDoesNotIngestAnEarlierSnapshot is the provenance guard. A
// harvest whose capture failed has no snapshot of its own; falling back to the
// newest one on disk would re-ingest yesterday's evidence and report ok, which
// is worse than failing because it looks correct for ever afterwards.
func TestAFailedCaptureDoesNotIngestAnEarlierSnapshot(t *testing.T) {
	p := newPartialHarvest(t)

	// A first run that succeeds, leaving a real snapshot in the directory.
	if err := p.run(t, populatedSnapshot(t)); err != nil {
		t.Logf("the first harvest reported: %v", err)
	}
	first := harvestRuns(t, p.storePath)
	if len(first) == 0 {
		t.Fatal("the first harvest recorded nothing to compare against")
	}
	captured := bySource(t, first)[kcBacklight].input
	if captured == "" {
		t.Fatal("the first harvest recorded no snapshot as the backlight source's input")
	}
	events := eventCountFor(t, p.storePath, kcBacklight)
	if events == 0 {
		t.Fatal("the first harvest stored no backlight events; this fixture cannot prove anything")
	}

	// A second run whose capture fails, with that snapshot still sitting there.
	src := unreadable(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err == nil {
		t.Fatal("the second harvest reported success with an unreadable knowledgeC")
	}

	second := harvestRuns(t, p.storePath)[len(first):]
	if len(second) == 0 {
		t.Fatal("the second harvest recorded no runs at all; it never reached the ingestion phase")
	}
	var backlight int
	for _, r := range second {
		if r.sourceID != kcBacklight {
			continue
		}
		backlight++
		if r.input == captured {
			t.Errorf("the failed run recorded %s as its input; it ingested the previous run's snapshot", captured)
		}
		if r.status != string(store.StatusFailed) {
			t.Errorf("%s: status %q on a run whose capture failed, want %q", r.sourceID, r.status, store.StatusFailed)
		}
	}
	if backlight != 1 {
		t.Errorf("the second harvest recorded %d runs for %s, want exactly 1 saying the capture was lost",
			backlight, kcBacklight)
	}
	if after := eventCountFor(t, p.storePath, kcBacklight); after != events {
		t.Errorf("backlight events went from %d to %d across a harvest that captured nothing", events, after)
	}
}

// TestTheHarvestErrorNamesEverySourceThatFailed pins the reporting property. A
// run losing two sources that names only the first sends whoever reads
// harvest.err to fix one problem and rediscover the other on the next run.
func TestTheHarvestErrorNamesEverySourceThatFailed(t *testing.T) {
	p := newPartialHarvest(t)
	src := unreadable(t, filepath.Join(p.dir, "knowledgeC.db"))
	p.biomeRoot = filepath.Join(p.dir, "locked-biome")
	unreadableDir(t, filepath.Join(p.biomeRoot, snapshot.Streams[0], "local"))

	err := p.run(t, src)
	if err == nil {
		t.Fatal("harvest reported success with two unreadable sources")
	}
	text := err.Error()
	for _, want := range []string{src, snapshot.Streams[0], kcBacklight, kcMediaUsage} {
		if !strings.Contains(text, want) {
			t.Errorf("the harvest error does not mention %q:\n%s", want, text)
		}
	}

	// The vault and filedays captures, which neither failure touches, are still
	// on disk.
	if n := countFiles(t, snapshot.VaultDir(p.snapDir)); n != 1 {
		t.Errorf("the Motion vault capture holds %d files, want 1", n)
	}
	if _, statErr := os.Stat(filedays.Dir(p.snapDir)); statErr != nil {
		t.Errorf("no filedays capture was written: %v", statErr)
	}
}

// TestAFullyReadableHarvestStillReportsSuccess is the guard against the fix
// swallowing failures: best-effort capture must not turn a clean run into a
// noisy one, and must not turn a failed one into a silent success.
func TestAFullyReadableHarvestStillReportsSuccess(t *testing.T) {
	p := newPartialHarvest(t)
	if err := p.run(t, populatedSnapshot(t)); err != nil {
		// The live Biome and transcript sources are read from the real machine
		// and may legitimately fail here; the captures are what this asserts.
		t.Logf("harvest reported: %v", err)
	}
	entries, err := os.ReadDir(p.snapDir)
	if err != nil {
		t.Fatalf("read %s: %v", p.snapDir, err)
	}
	var gz int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".db.gz") {
			gz++
		}
	}
	if gz != 1 {
		t.Errorf("%s holds %d knowledgeC snapshots, want 1", p.snapDir, gz)
	}
	recorded := bySource(t, harvestRuns(t, p.storePath))
	for _, id := range []string{kcBacklight, kcLocked, kcAppUsage, kcMediaUsage} {
		if r := recorded[id]; r.status != string(store.StatusOK) {
			t.Errorf("%s: status %q on a readable run, error %q", id, r.status, r.failure.String)
		}
	}
}
