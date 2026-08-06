//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
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
)

// realVaultRoot is the path the built-in motion-autosave manifest names, which
// is where cmd/timetattle harvests from when -motion-vault is not passed. This
// package does not import internal/manifest to find it -- a test reaching for
// a manifest loader to learn one path would drag the whole validation and
// layering machinery into a package that has nothing to do with it.
//
// It skips cleanly, not fails, on any machine without Motion, which is most of
// them.
func realVaultRoot(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	root := filepath.Join(home, "Movies", "Motion Projects", "Autosave Vault")
	// Two conditions, two messages. Collapsing them made a vault this process
	// cannot read indistinguishable from one that is not there.
	info, err := os.Stat(root)
	if err != nil {
		t.Skipf("cannot stat %s: %v", root, err)
	}
	if !info.IsDir() {
		t.Skipf("%s is not a directory (mode %s)", root, info.Mode())
	}
	return root
}

// TestRealVaultPreservesIdenticallyThroughASymlinkedRoot is the real-data half
// of the symlinked-root regression (task eAVcU). The constructed fixtures in
// vaultroot_test.go hold two files in two composition directories; this runs
// against whatever this machine's actual Motion vault holds, at its real
// nesting, with the filenames Motion actually writes.
//
// Both routes are measured in the same run and compared to each other rather
// than to a number written down anywhere, because the vault is perishable: its
// pruning trigger is unconfirmed (job task g7v07), so any fixed figure can be
// wrong through no fault of the walk.
//
// The capture LAYOUT is what is compared, not just the count. A capture's
// destination is dir joined with the file's path relative to what was walked,
// so a fix that resolves the root but computes that relative path against the
// caller's original string writes outside the destination entirely -- see
// TestPreserveVaultCapturesUnderTheDestinationWhenRootIsASymlink for the
// mutation that demonstrates it.
func TestRealVaultPreservesIdenticallyThroughASymlinkedRoot(t *testing.T) {
	root := realVaultRoot(t)

	link := filepath.Join(t.TempDir(), "vault-alias")
	if err := os.Symlink(root, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	dstReal, dstLink := t.TempDir(), t.TempDir()
	viaReal, err := PreserveVault(root, dstReal)
	if err != nil {
		t.Fatalf("PreserveVault via the real path: %v", err)
	}
	if viaReal == nil || len(viaReal.Captured) == 0 {
		t.Skipf("the vault at %s holds no files to preserve", root)
	}
	viaLink, err := PreserveVault(link, dstLink)
	if err != nil {
		t.Fatalf("PreserveVault via the symlinked root: %v", err)
	}
	if viaLink == nil {
		t.Fatal("PreserveVault returned nil for a symlink to a real vault")
	}

	if len(viaLink.Captured) != len(viaReal.Captured) {
		t.Fatalf("preserved %d files through the symlinked root, want the %d the real path yields",
			len(viaLink.Captured), len(viaReal.Captured))
	}

	relReal := relativeTo(t, dstReal, viaReal.Captured)
	relLink := relativeTo(t, dstLink, viaLink.Captured)
	for i := range relReal {
		if relReal[i] != relLink[i] {
			t.Fatalf("capture %d landed at %q through the symlink and %q through the real path",
				i, relLink[i], relReal[i])
		}
	}
	t.Logf("preserved %d files (%d bytes) identically through %s and a symlink to it",
		len(viaLink.Captured), viaLink.Bytes, root)
}

// relativeTo sorts the captured paths by where they landed under dir, and
// fails outright on any that escaped it.
func relativeTo(t *testing.T, dir string, paths []string) []string {
	t.Helper()
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", dir, p, err)
		}
		if filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") {
			t.Fatalf("capture %q is not under the destination directory %q", p, dir)
		}
		out = append(out, rel)
	}
	sort.Strings(out)
	return out
}
