package snapshot

import (
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// writeVaultFile drops a fixture into <root>/<composition>/<name>, the way
// Motion lays its autosave vault out: one directory per composition, holding
// its own autosaves.
func writeVaultFile(t *testing.T, root, composition, name string, data []byte) string {
	t.Helper()
	dir := filepath.Join(root, composition)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// preservedFiles lists every regular file under dir, relative to dir, for
// asserting on what a preservation run actually left behind.
func preservedFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("walk %s: %v", dir, err)
	}
	return out
}

// restoreVaultCapture decompresses one captured file back to the bytes the
// vault held.
func restoreVaultCapture(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open capture %s: %v", path, err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("capture %s is not gzip: %v", path, err)
	}
	defer zr.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(zr); err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	return buf.Bytes()
}

func TestPreserveVaultCopiesFiles(t *testing.T) {
	root := t.TempDir()
	dst := t.TempDir()
	writeVaultFile(t, root, "dark-crystal-v2", "dark-crystal-v2_2026-07-11_1745.motn", []byte("composition one"))
	writeVaultFile(t, root, "scanline-card", "scanline-card_2026-07-11_1800.motn", []byte("composition two"))

	res, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("PreserveVault: %v", err)
	}
	if res == nil {
		t.Fatal("PreserveVault returned nil for an existing root with files")
	}
	if len(res.Captured) != 2 {
		t.Fatalf("Captured has %d entries, want 2: %v", len(res.Captured), res.Captured)
	}
	if res.Unchanged != 0 {
		t.Errorf("Unchanged = %d on a first run, want 0", res.Unchanged)
	}
	if res.Bytes == 0 {
		t.Error("Bytes = 0, want the compressed size of what was written")
	}

	for _, c := range res.Captured {
		if _, err := os.Stat(c); err != nil {
			t.Errorf("captured path %s does not exist: %v", c, err)
		}
	}

	// The actual bytes must round-trip, not just a file of the right name.
	var sawOne, sawTwo bool
	for _, c := range res.Captured {
		data := restoreVaultCapture(t, c)
		switch string(data) {
		case "composition one":
			sawOne = true
		case "composition two":
			sawTwo = true
		}
	}
	if !sawOne || !sawTwo {
		t.Errorf("captured contents were %v, want both fixtures represented byte-for-byte", res.Captured)
	}
}

// TestPreserveVaultSkipsUnchangedOnSecondRun is the same no-recopy guarantee
// TakeStreams gives Biome segments: a harvest that ran twice in a row must
// not double the bytes on disk for files that have not changed.
func TestPreserveVaultSkipsUnchangedOnSecondRun(t *testing.T) {
	root := t.TempDir()
	dst := t.TempDir()
	writeVaultFile(t, root, "dark-crystal-v2", "dark-crystal-v2_2026-07-11_1745.motn", []byte("composition one"))

	first, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("first PreserveVault: %v", err)
	}
	if len(first.Captured) != 1 {
		t.Fatalf("first run captured %d files, want 1", len(first.Captured))
	}
	before := preservedFiles(t, dst)

	second, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("second PreserveVault: %v", err)
	}
	if len(second.Captured) != 0 {
		t.Errorf("second run captured %v, want nothing new", second.Captured)
	}
	if second.Unchanged != 1 {
		t.Errorf("second run Unchanged = %d, want 1", second.Unchanged)
	}
	after := preservedFiles(t, dst)
	if len(before) != len(after) {
		t.Errorf("preserved file count changed from %d to %d on an unchanged vault", len(before), len(after))
	}
}

// TestPreserveVaultKeepsBothVersionsWhenContentChangesUnderTheSameName proves
// the dedup key is content, not the file's name — the guard against silently
// losing a version. Motion's pruning trigger is unconfirmed (job task g7v07),
// so nothing here may assume a filename is only ever written once.
//
// If preservation instead deduplicated on the relative path alone, this test
// would observe the second run reporting Unchanged instead of Captured, and
// only one version's bytes would ever reach disk — which is the exact bug
// this test exists to catch by mutation: comment out the digest in the
// destination name below and both assertions here fail.
func TestPreserveVaultKeepsBothVersionsWhenContentChangesUnderTheSameName(t *testing.T) {
	root := t.TempDir()
	dst := t.TempDir()
	name := "dark-crystal-v2_2026-07-11_1745.motn"
	writeVaultFile(t, root, "dark-crystal-v2", name, []byte("version one"))

	first, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("first PreserveVault: %v", err)
	}
	if len(first.Captured) != 1 {
		t.Fatalf("first run captured %d files, want 1", len(first.Captured))
	}

	// Overwrite the same name with different content, the same shape a torn
	// or re-issued autosave would take.
	writeVaultFile(t, root, "dark-crystal-v2", name, []byte("version two, different bytes"))
	second, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("second PreserveVault: %v", err)
	}
	if len(second.Captured) != 1 {
		t.Fatalf("second run (changed content, same name) captured %d files, want 1", len(second.Captured))
	}

	var contents []string
	for _, c := range append(append([]string{}, first.Captured...), second.Captured...) {
		contents = append(contents, string(restoreVaultCapture(t, c)))
	}
	var sawOne, sawTwo bool
	for _, c := range contents {
		if c == "version one" {
			sawOne = true
		}
		if c == "version two, different bytes" {
			sawTwo = true
		}
	}
	if !sawOne || !sawTwo {
		t.Errorf("captured contents were %v, want both \"version one\" and \"version two, different bytes\" preserved", contents)
	}
}

func TestPreserveVaultMissingRootIsNotAnError(t *testing.T) {
	dst := t.TempDir()
	res, err := PreserveVault(filepath.Join(t.TempDir(), "no-such-vault"), dst)
	if err != nil {
		t.Fatalf("PreserveVault on a missing root: %v", err)
	}
	if res != nil {
		t.Errorf("PreserveVault on a missing root returned %+v, want nil", res)
	}
}

// TestPreserveVaultPreservesCompositionStructure checks that a captured
// file's relative directory still names the composition it came from, so a
// human (or a future reader) can tell which vault subdirectory a capture is
// from without decoding the digest.
func TestPreserveVaultPreservesCompositionStructure(t *testing.T) {
	root := t.TempDir()
	dst := t.TempDir()
	writeVaultFile(t, root, "dark-crystal-v2", "dark-crystal-v2_2026-07-11_1745.motn", []byte("data"))

	res, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("PreserveVault: %v", err)
	}
	if len(res.Captured) != 1 {
		t.Fatalf("captured %d files, want 1", len(res.Captured))
	}
	rel, err := filepath.Rel(dst, res.Captured[0])
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	if filepath.Dir(rel) != "dark-crystal-v2" {
		t.Errorf("captured file lives under %q, want the composition directory %q preserved",
			filepath.Dir(rel), "dark-crystal-v2")
	}
}

func TestVaultDirIsUnderTheSnapshotDirectory(t *testing.T) {
	got := VaultDir("/tmp/snapshots")
	want := filepath.Join("/tmp/snapshots", "motion-vault")
	if got != want {
		t.Errorf("VaultDir = %q, want %q", got, want)
	}
}
