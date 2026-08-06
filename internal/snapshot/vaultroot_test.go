package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// symlinkedVault returns a populated vault directory, a symlink pointing at
// it, and how many files it holds.
//
// The symlink is the ROOT itself, not something inside the tree. A vault
// reached through a symlink is not exotic: an autosave vault living on an
// external volume with a link left behind in ~/Movies is exactly the shape
// this preserves nothing for.
func symlinkedVault(t *testing.T) (real, link string, files int) {
	t.Helper()
	base := t.TempDir()
	real = filepath.Join(base, "actual")
	if err := os.MkdirAll(real, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	writeVaultFile(t, real, "dark-crystal-v2", "dark-crystal-v2_2026-07-11_1745.motn", []byte("composition one"))
	writeVaultFile(t, real, "scanline-card", "scanline-card_2026-07-11_1800.motn", []byte("composition two"))

	link = filepath.Join(base, "alias")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}
	return real, link, 2
}

// TestPreserveVaultWalksARootThatIsItselfASymlink is the core regression, and
// the one with the most at stake of the three packages this defect was found
// in: this function WRITES, and what it fails to copy from a perishable vault
// is gone for good.
//
// filepath.WalkDir calls os.Lstat on its own root argument and never follows a
// symlink there, so a vault root that IS a symlink arrived at the callback as
// a non-directory entry, the walk never descended, and PreserveVault returned
// a VaultResult with nothing captured and no error. Measured 2026-08-01: 3
// files captured via the real path, 0 via a symlink to it, err=nil.
func TestPreserveVaultWalksARootThatIsItselfASymlink(t *testing.T) {
	real, link, want := symlinkedVault(t)

	viaReal, err := PreserveVault(real, t.TempDir())
	if err != nil {
		t.Fatalf("PreserveVault via the real path: %v", err)
	}
	if viaReal == nil || len(viaReal.Captured) != want {
		t.Fatalf("the real path captured %v files, want %d; the fixture is wrong, not the walk", viaReal, want)
	}

	viaLink, err := PreserveVault(link, t.TempDir())
	if err != nil {
		t.Fatalf("PreserveVault via the symlinked root: %v", err)
	}
	if viaLink == nil {
		t.Fatal("PreserveVault returned nil for a symlinked root that resolves to a real vault")
	}
	if len(viaLink.Captured) != len(viaReal.Captured) {
		t.Fatalf("captured %d files through a symlinked root, want the %d the real path yields: %v",
			len(viaLink.Captured), len(viaReal.Captured), viaLink.Captured)
	}
}

// TestPreserveVaultCapturesUnderTheDestinationWhenRootIsASymlink is this
// package's equivalent of fsobserve's Locator re-rooting, and the reason the
// fix is not one line.
//
// There is no Locator here -- nothing PreserveVault produces becomes an event.
// What plays the same role is the DESTINATION PATH: every capture is written
// to dir/<path relative to the root>, so the relative path must be computed
// against whatever was actually walked. Compute it against the caller's
// original root while walking the resolved one and filepath.Rel returns a
// climb of ".." segments -- on macOS /var resolves to /private/var, and every
// temporary directory here is under it -- so MkdirAll and the capture land
// OUTSIDE the destination directory entirely, scattering preserved evidence
// into arbitrary places on disk.
//
// MUTATION PROOF: change the Rel call in PreserveVault back to
// filepath.Rel(root, path) and this test reports a captured path that is not
// under dst at all.
func TestPreserveVaultCapturesUnderTheDestinationWhenRootIsASymlink(t *testing.T) {
	_, link, _ := symlinkedVault(t)
	dst := t.TempDir()

	res, err := PreserveVault(link, dst)
	if err != nil {
		t.Fatalf("PreserveVault: %v", err)
	}
	if res == nil || len(res.Captured) == 0 {
		t.Fatal("nothing captured through the symlinked root")
	}

	var compositions []string
	for _, c := range res.Captured {
		rel, err := filepath.Rel(dst, c)
		if err != nil {
			t.Fatalf("Rel(%s, %s): %v", dst, c, err)
		}
		if strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
			t.Fatalf("capture %q is not under the destination directory %q (relative path %q); "+
				"the walked path was made relative to the wrong root", c, dst, rel)
		}
		if _, err := os.Stat(c); err != nil {
			t.Errorf("captured path %s does not exist: %v", c, err)
		}
		compositions = append(compositions, filepath.Dir(rel))
	}

	// The composition directory must survive too: it is the only thing that
	// says which composition a capture came from without decoding the digest.
	for _, got := range compositions {
		if got != "dark-crystal-v2" && got != "scanline-card" {
			t.Errorf("capture lives under %q, want a composition directory preserved from the vault", got)
		}
	}
}

// TestPreserveVaultReportsARootItCouldNotWalk is the half of the fix that
// matters more than the walk. A vault root that exists but cannot be walked as
// a directory tree must say so and name itself; see
// TestPreserveVaultEmptyRootIsSilent for the other side of the same line.
//
// This package answers with an error rather than a log warning, because it
// already returns one and cmd/timetattle already prints it as
// "motion vault: capture lost (...)". A root that simply is not there stays
// (nil, nil) -- see TestPreserveVaultMissingRootIsNotAnError -- because that
// is what a machine without Motion looks like, and the nil result is itself
// the signal that distinguishes it from "captured nothing".
func TestPreserveVaultReportsARootItCouldNotWalk(t *testing.T) {
	for _, tc := range []struct {
		name string
		root func(t *testing.T, base string) string
	}{
		{
			name: "a symlink whose target has been deleted",
			root: func(t *testing.T, base string) string {
				link := filepath.Join(base, "dangling")
				if err := os.Symlink(filepath.Join(base, "gone"), link); err != nil {
					t.Skipf("symlinks not supported here: %v", err)
				}
				return link
			},
		},
		{
			name: "a symlink to a file rather than a directory",
			root: func(t *testing.T, base string) string {
				file := filepath.Join(base, "not-a-directory.motn")
				if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				link := filepath.Join(base, "alias")
				if err := os.Symlink(file, link); err != nil {
					t.Skipf("symlinks not supported here: %v", err)
				}
				return link
			},
		},
		{
			name: "a plain file given as the root",
			root: func(t *testing.T, base string) string {
				file := filepath.Join(base, "not-a-directory.motn")
				if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return file
			},
		},
		{
			name: "a directory that refuses to be read",
			root: func(t *testing.T, base string) string {
				if os.Geteuid() == 0 {
					t.Skip("running as root; permission bits do not deny this process")
				}
				root := filepath.Join(base, "unreadable")
				writeVaultFile(t, root, "dark-crystal-v2", "a.motn", []byte("x"))
				if err := os.Chmod(root, 0o000); err != nil {
					t.Fatalf("Chmod: %v", err)
				}
				t.Cleanup(func() { _ = os.Chmod(root, 0o700) })
				return root
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := tc.root(t, t.TempDir())
			dst := t.TempDir()
			res, err := PreserveVault(root, dst)
			if err == nil {
				t.Fatalf("a vault root that could not be walked preserved %v and reported nothing; "+
					"'zero because unwalkable' is indistinguishable from 'zero because empty'", res)
			}
			if !strings.Contains(err.Error(), root) {
				t.Errorf("the error does not name the root it could not walk: %v", err)
			}
			if len(preservedFiles(t, dst)) != 0 {
				t.Errorf("something was written to the destination for a root that could not be walked: %v",
					preservedFiles(t, dst))
			}
		})
	}
}

// TestPreserveVaultEmptyRootIsSilent is the constraint that stops the lazy fix
// of erroring whenever nothing was captured. A vault directory that exists and
// holds nothing -- Motion installed, no composition autosaved yet -- is an
// ordinary, correct zero: a VaultResult with nothing in it, and no error.
func TestPreserveVaultEmptyRootIsSilent(t *testing.T) {
	res, err := PreserveVault(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatalf("an empty but perfectly readable vault root was reported as a problem: %v", err)
	}
	if res == nil {
		t.Fatal("an existing but empty vault root returned nil, which is what an ABSENT vault means")
	}
	if len(res.Captured) != 0 || res.Unchanged != 0 {
		t.Errorf("an empty vault captured %v (%d unchanged)", res.Captured, res.Unchanged)
	}
}

// TestPreserveVaultDoesNotFollowASymlinkInsideTheVault is the negative guard
// on the fix. Resolving the ROOT is about being able to read the tree at all;
// it must not turn into following every link met on the way down, which could
// copy an arbitrary part of the filesystem into the capture directory or
// preserve the same bytes twice under two names.
func TestPreserveVaultDoesNotFollowASymlinkInsideTheVault(t *testing.T) {
	root := t.TempDir()
	dst := t.TempDir()
	real := writeVaultFile(t, root, "dark-crystal-v2", "dark-crystal-v2_2026-07-11_1745.motn", []byte("composition one"))
	if err := os.Symlink(real, filepath.Join(root, "dark-crystal-v2", "alias.motn")); err != nil {
		t.Skipf("symlinks not supported here: %v", err)
	}

	res, err := PreserveVault(root, dst)
	if err != nil {
		t.Fatalf("PreserveVault: %v", err)
	}
	if len(res.Captured) != 1 {
		t.Errorf("captured %d files, want 1; a symlink inside the vault is the same bytes again, not a second autosave: %v",
			len(res.Captured), res.Captured)
	}
}
