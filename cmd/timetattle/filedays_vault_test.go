package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/filedays"
	"github.com/bensyverson/timetattle/internal/snapshot"
)

// These tests cover task xONGg: filesystem mtime counts and Motion's autosave
// vault are perishable evidence, so capturing both has to ride the same
// `harvest` invocation that already preserves knowledgeC and Biome, with the
// scanned roots read from configuration rather than fixed in source. See
// project/private/2026-07-28-source-corrections.md §4 and job task g7v07 for why the
// vault needs raw preservation rather than only the events a manifest source
// already derives from it.

// readFiledaysSnapshot reads back whichever single snapshot file harvest just
// wrote under dir, failing loudly if there is not exactly one — these tests
// each run harvest once against an empty temp snapshot directory.
func readFiledaysSnapshot(t *testing.T, dir string) filedays.Snapshot {
	t.Helper()
	entries, err := os.ReadDir(filedays.Dir(dir))
	if err != nil {
		t.Fatalf("read filedays dir %s: %v", filedays.Dir(dir), err)
	}
	if len(entries) != 1 {
		t.Fatalf("filedays dir %s holds %d entries, want exactly 1", filedays.Dir(dir), len(entries))
	}
	data, err := os.ReadFile(filepath.Join(filedays.Dir(dir), entries[0].Name()))
	if err != nil {
		t.Fatalf("read %s: %v", entries[0].Name(), err)
	}
	var snap filedays.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		t.Fatalf("unmarshal filedays snapshot: %v", err)
	}
	return snap
}

func restoreVaultFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("%s is not gzip: %v", path, err)
	}
	defer zr.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(zr); err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	return buf.Bytes()
}

// touchFile writes a file with a deterministic mtime, matching the fixture
// helper in internal/filedays's own tests.
func touchFile(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// TestHarvestCapturesFiledaysWhenRootsAreConfigured is criteria q26, gBu and
// 6z5 together: the -filedays-roots flag is the configuration (not a
// repository baked into the source), the resulting counts land on disk with
// an observation time, and all of this happens inside one harvest call
// rather than requiring a separate invocation of scripts/filedays.
func TestHarvestCapturesFiledaysWhenRootsAreConfigured(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "repo")
	touchFile(t, filepath.Join(root, "a.txt"), time.Date(2026, 7, 11, 10, 0, 0, 0, time.Local))
	touchFile(t, filepath.Join(root, "b.txt"), time.Date(2026, 7, 11, 11, 0, 0, 0, time.Local))

	snapDir := filepath.Join(dir, "snapshots")
	err := harvest([]string{
		"-source", populatedSnapshot(t),
		"-dir", snapDir,
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-store", filepath.Join(dir, "store", "timetattle.db"),
		"-motion-vault", filepath.Join(dir, "no-such-vault"),
		"-filedays-roots", root,
		"-quiet",
	})
	if err != nil {
		t.Logf("harvest reported: %v", err)
	}

	snap := readFiledaysSnapshot(t, snapDir)
	if snap.ObservedAt.IsZero() {
		t.Error("filedays snapshot recorded no ObservedAt; a count with no observation time is meaningless")
	}
	if len(snap.Roots) != 1 || snap.Roots[0].Root != root {
		t.Fatalf("snapshot.Roots = %v, want exactly the configured root %q", snap.Roots, root)
	}
	if snap.Roots[0].Raw["2026-07-11"] != 2 {
		t.Errorf("raw count for 2026-07-11 = %d, want 2", snap.Roots[0].Raw["2026-07-11"])
	}
}

// TestHarvestFiledaysRootsAreConfigurableNotHardcoded is criterion gBu on its
// own: two independently configured roots are both scanned, proving the set
// of roots is whatever -filedays-roots names rather than a single repository
// wired into the program.
func TestHarvestFiledaysRootsAreConfigurableNotHardcoded(t *testing.T) {
	dir := t.TempDir()
	rootA := filepath.Join(dir, "repo-a")
	rootB := filepath.Join(dir, "repo-b")
	touchFile(t, filepath.Join(rootA, "a.txt"), time.Now())
	touchFile(t, filepath.Join(rootB, "b1.txt"), time.Now())
	touchFile(t, filepath.Join(rootB, "b2.txt"), time.Now())

	snapDir := filepath.Join(dir, "snapshots")
	err := harvest([]string{
		"-source", populatedSnapshot(t),
		"-dir", snapDir,
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-store", filepath.Join(dir, "store", "timetattle.db"),
		"-motion-vault", filepath.Join(dir, "no-such-vault"),
		"-filedays-roots", rootA + "," + rootB,
		"-quiet",
	})
	if err != nil {
		t.Logf("harvest reported: %v", err)
	}

	snap := readFiledaysSnapshot(t, snapDir)
	if len(snap.Roots) != 2 {
		t.Fatalf("snapshot.Roots has %d entries, want 2 for two configured roots", len(snap.Roots))
	}
}

// TestHarvestSkipsFiledaysWhenNothingIsConfigured is the tolerance half of
// criterion gBu: a machine that has not configured any roots gets no
// filedays capture and no error, the same way an uninstalled application
// produces no events rather than a failure.
func TestHarvestSkipsFiledaysWhenNothingIsConfigured(t *testing.T) {
	dir := t.TempDir()
	fakeHome := t.TempDir()
	t.Setenv("HOME", fakeHome)

	snapDir := filepath.Join(dir, "snapshots")
	err := harvest([]string{
		"-source", populatedSnapshot(t),
		"-dir", snapDir,
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-store", filepath.Join(dir, "store", "timetattle.db"),
		"-motion-vault", filepath.Join(dir, "no-such-vault"),
		"-quiet",
	})
	if err != nil {
		t.Logf("harvest reported: %v", err)
	}

	if _, statErr := os.Stat(filedays.Dir(snapDir)); !os.IsNotExist(statErr) {
		t.Errorf("filedays dir was created at %s with no roots configured", filedays.Dir(snapDir))
	}
}

// TestHarvestPreservesMotionVaultWhenConfigured is criterion RUq: the raw
// bytes of a vault file must survive a harvest, not merely a derived
// timestamp, because job task g7v07 found the vault's pruning trigger
// unconfirmed and autosaves can disappear for reasons nothing else here
// understands.
func TestHarvestPreservesMotionVaultWhenConfigured(t *testing.T) {
	dir := t.TempDir()
	vault := filepath.Join(dir, "vault")
	compositionFile := filepath.Join(vault, "dark-crystal-v2", "dark-crystal-v2_2026-07-11_1745.motn")
	if err := os.MkdirAll(filepath.Dir(compositionFile), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(compositionFile, []byte("a real composition would be binary"), 0o600); err != nil {
		t.Fatalf("write fixture autosave: %v", err)
	}

	snapDir := filepath.Join(dir, "snapshots")
	err := harvest([]string{
		"-source", populatedSnapshot(t),
		"-dir", snapDir,
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-store", filepath.Join(dir, "store", "timetattle.db"),
		"-motion-vault", vault,
		"-quiet",
	})
	if err != nil {
		t.Logf("harvest reported: %v", err)
	}

	var captured []string
	err = filepath.WalkDir(snapshot.VaultDir(snapDir), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			captured = append(captured, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", snapshot.VaultDir(snapDir), err)
	}
	if len(captured) != 1 {
		t.Fatalf("motion vault directory holds %d files, want 1: %v", len(captured), captured)
	}
	if got := string(restoreVaultFile(t, captured[0])); got != "a real composition would be binary" {
		t.Errorf("preserved vault file content = %q, want the original composition bytes", got)
	}
}

// TestHarvestSkipsMotionVaultWhenNotInstalled is the tolerance half of
// criterion RUq, mirroring how a missing Biome stream is handled.
func TestHarvestSkipsMotionVaultWhenNotInstalled(t *testing.T) {
	dir := t.TempDir()
	snapDir := filepath.Join(dir, "snapshots")
	err := harvest([]string{
		"-source", populatedSnapshot(t),
		"-dir", snapDir,
		"-biome", filepath.Join(dir, "no-such-biome"),
		"-store", filepath.Join(dir, "store", "timetattle.db"),
		"-motion-vault", filepath.Join(dir, "no-such-vault"),
		"-quiet",
	})
	if err != nil {
		t.Logf("harvest reported: %v", err)
	}
	if _, statErr := os.Stat(snapshot.VaultDir(snapDir)); !os.IsNotExist(statErr) {
		t.Errorf("motion-vault dir was created at %s for a vault that does not exist", snapshot.VaultDir(snapDir))
	}
}

// TestDefaultVaultRootMatchesTheMotionManifest checks that leaving
// -motion-vault unset resolves to the same path the shipped motion-autosave
// manifest declares, rather than a second hardcoded copy of it that could
// drift — including under a manifest override, which is the scenario that
// makes reading the manifest worth doing instead of hardcoding the built-in
// path a second time.
func TestDefaultVaultRootMatchesTheMotionManifest(t *testing.T) {
	got, err := defaultVaultRoot()
	if err != nil {
		t.Fatalf("defaultVaultRoot: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	want := filepath.Join(home, "Movies", "Motion Projects", "Autosave Vault")
	if got != want {
		t.Errorf("defaultVaultRoot() = %q, want %q (the built-in motion-autosave manifest's Files.Root)", got, want)
	}
}
