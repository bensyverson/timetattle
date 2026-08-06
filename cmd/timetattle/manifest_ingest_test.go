package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/timestamped"
)

// These tests cover the wiring that was missing: manifest-driven sources
// (internal/manifest + internal/timestamped) fully implemented but never
// ingested into the event store. See job task dd8HA and the harvest package
// doc's own admission at the top of main.go that "Neither the Motion vault
// capture nor the filedays snapshot is ingested as events today".

// manifestSourceIDs are the two built-in manifests' ids, spelled out rather
// than read back from the manifest package itself, matching how
// harvest_test.go and harvest_registry_test.go already name every other
// source id directly: a renamed id should fail this test rather than
// silently travel through.
const (
	motionAutosaveID = "motion-autosave"
	blenderBackupsID = "blender-backups"
)

// TestManifestIngestionsIncludesBothBuiltinSources is the acceptance test for
// the wiring itself: the two built-in manifests (internal/manifest/builtin)
// must each produce exactly one ingestion, with a non-empty input, without
// this test ever calling Collect (Blender's is a Spotlight query with no
// files.root, so Collect would hit the real machine's index — this test only
// proves the plan is built, not what collecting from it finds).
func TestManifestIngestionsIncludesBothBuiltinSources(t *testing.T) {
	plan, err := manifestIngestions()
	if err != nil {
		t.Fatalf("manifestIngestions() = %v", err)
	}
	if len(plan) != 2 {
		t.Fatalf("manifestIngestions() = %d ingestions, want 2 (motion-autosave, blender-backups): %+v", len(plan), plan)
	}
	ids := make([]string, len(plan))
	for i, ing := range plan {
		ids[i] = ing.source.ID()
		if ing.input == "" {
			t.Errorf("%s: input is empty; store.BeginRun refuses an empty input", ids[i])
		}
	}
	sort.Strings(ids)
	want := []string{blenderBackupsID, motionAutosaveID}
	if ids[0] != want[0] || ids[1] != want[1] {
		t.Errorf("manifestIngestions() source IDs = %v, want %v", ids, want)
	}
}

// TestManifestInputForSpotlightSourceWithNoRootDescribesTheQuery is the
// acceptance test for the input-string decision: a Spotlight-discovery
// source with no files.root (blender-backups is exactly this shape) must
// still record a non-empty, honest harvest_run.input naming what was
// actually read, since store.BeginRun hard-refuses an empty Input.
func TestManifestInputForSpotlightSourceWithNoRootDescribesTheQuery(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point",
		"cause": "user-action", "app": "com.example.app", "discovery": "spotlight",
		"files": {"query": "kMDItemFSName == '*.blend1'"}, "timestamp": {"from": "mtime"}}]}`
	path := filepath.Join(t.TempDir(), "spotlight.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if len(m.Sources) != 1 {
		t.Fatalf("fixture manifest has %d sources, want 1", len(m.Sources))
	}
	spec := m.Sources[0]
	if spec.Files.Root != "" {
		t.Fatalf("fixture assumption broken: files.root = %q, want empty", spec.Files.Root)
	}

	got := manifestInput(spec)
	if got == "" {
		t.Fatal("manifestInput() = \"\", want a non-empty description of what was read")
	}
	if !strings.Contains(got, spec.Files.Query) {
		t.Errorf("manifestInput() = %q, want it to name the Spotlight query %q it actually ran", got, spec.Files.Query)
	}
}

// TestManifestInputForGlobSourceNamesRootAndMatch covers the ordinary case
// beside the Spotlight one above, so a future change that makes the glob
// branch empty for some edge case is caught here rather than only noticed as
// a BeginRun failure at harvest time.
func TestManifestInputForGlobSourceNamesRootAndMatch(t *testing.T) {
	body := `{"manifest_version": 1, "sources": [{"id": "x", "type": "timestamped-files", "emits": "point",
		"cause": "timer", "app": "com.example.app",
		"files": {"root": "` + filepath.ToSlash(t.TempDir()) + `", "match": "*/*.motn"},
		"timestamp": {"from": "mtime"}}]}`
	path := filepath.Join(t.TempDir(), "glob.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	spec := m.Sources[0]

	got := manifestInput(spec)
	if got == "" {
		t.Fatal("manifestInput() = \"\", want a non-empty description of what was read")
	}
	if !strings.Contains(got, spec.Files.Root) || !strings.Contains(got, "motn") {
		t.Errorf("manifestInput() = %q, want it to name both the root %q and the match pattern", got, spec.Files.Root)
	}
}

// TestManifestIngestionsAreWiredIntoHarvest is the end-to-end acceptance
// test: a real 'timetattle harvest' run must record harvest_run rows for
// both built-in manifest sources, proving they are reached from harvest()
// and not merely constructible. TestMain isolates HOME, so
// motion-autosave's root (~/Movies/Motion Projects/Autosave Vault) will not
// exist on the isolated home and that is tolerated (0 events, status ok) —
// blender-backups' Spotlight query has no files.root and therefore queries
// the real machine's index regardless of HOME, so this only asserts
// presence and status, never a specific event count.
func TestManifestIngestionsAreWiredIntoHarvest(t *testing.T) {
	dir := t.TempDir()
	src := populatedSnapshot(t)
	storePath := filepath.Join(dir, "store", "timetattle.db")
	err := harvest([]string{
		"-source", src,
		"-dir", filepath.Join(dir, "snapshots"),
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-motion-vault", filepath.Join(dir, "no-such-vault"),
		"-store", storePath,
		"-quiet",
	})
	if err != nil {
		// The live Biome source needs Full Disk Access and is not this
		// test's business; every other phase, including manifest ingestion,
		// must still have completed.
		t.Logf("harvest reported: %v", err)
	}

	recorded := bySource(t, harvestRuns(t, storePath))
	for _, id := range []string{motionAutosaveID, blenderBackupsID} {
		r, ok := recorded[id]
		if !ok {
			t.Fatalf("no harvest_run row for %s; manifest sources were not wired into harvest", id)
		}
		if r.status != "ok" {
			t.Errorf("%s: status %q, error %q", id, r.status, r.failure.String)
		}
		if r.input == "" {
			t.Errorf("%s recorded no input", id)
		}
	}
}

// TestReRunningManifestIngestionAddsNothing is the dedup cross-check: a
// source over synthetic timestamped files, run through the same ingest()
// path manifestIngestions feeds into, must add zero new rows the second
// time. A synthetic glob-discovery fixture is used rather than the real
// built-ins so this is deterministic and does not depend on what is
// actually on the test machine's disk or Spotlight index.
func TestReRunningManifestIngestionAddsNothing(t *testing.T) {
	vault := t.TempDir()
	compDir := filepath.Join(vault, "my-composition")
	if err := os.MkdirAll(compDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(compDir, "my-composition_2026-07-11_1545.motn"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	body := `{"manifest_version": 1, "sources": [{"id": "` + motionAutosaveID + `", "type": "timestamped-files",
		"emits": "point", "cause": "timer", "app": "com.apple.motionapp",
		"files": {"root": "` + filepath.ToSlash(vault) + `", "match": "*/*.motn"},
		"timestamp": {"from": "filename", "pattern": "_(\\d{4}-\\d{2}-\\d{2}_\\d{4})\\.motn$", "layout": "2006-01-02_1504"}}]}`
	path := filepath.Join(t.TempDir(), "test-manifest.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	m, err := manifest.Load(path)
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}

	reader, err := timestamped.New(m.Sources[0])
	if err != nil {
		t.Fatalf("build reader: %v", err)
	}
	plan := []ingestion{{source: reader, input: manifestInput(m.Sources[0])}}

	st, storePath := newStore(t)
	ctx := context.Background()
	if err := ingest(ctx, st, plan, io.Discard); err != nil {
		t.Fatalf("first ingestion: %v", err)
	}
	rows := eventCount(t, storePath)
	if rows == 0 {
		t.Fatal("first ingestion stored nothing; this fixture cannot prove anything about dedup")
	}

	if err := ingest(ctx, st, plan, io.Discard); err != nil {
		t.Fatalf("second ingestion: %v", err)
	}
	if after := eventCount(t, storePath); after != rows {
		t.Errorf("the store grew from %d rows to %d on a re-ingestion of the same manifest source", rows, after)
	}
	runs := harvestRuns(t, storePath)
	if len(runs) != 2 {
		t.Fatalf("two ingestions of one source recorded %d harvest_run rows, want 2", len(runs))
	}
	if runs[1].added != 0 {
		t.Errorf("second run added %d rows, want 0", runs[1].added)
	}
}
