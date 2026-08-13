package registry

import (
	"os"
	"path/filepath"
	"testing"
)

// TestBlessedCacheConflictRefusesADeclaredCacheRoot exercises the containment
// check with a synthetic cache root, so it runs deterministically regardless
// of what manifests happen to be installed on the machine running the tests.
// TestRegisterRefusesToBlessADeclaredCacheDirectory in registry_test.go is
// the real-data counterpart, checked against the actual shipped Motion
// manifest; per project convention neither replaces the other.
func TestBlessedCacheConflictRefusesADeclaredCacheRoot(t *testing.T) {
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "Autosave Vault")

	restore := cacheSourceRoots
	cacheSourceRoots = func() ([]string, error) { return []string{cache}, nil }
	defer func() { cacheSourceRoots = restore }()

	if _, blocked := blessedCacheConflict(cache); !blocked {
		t.Error("blessedCacheConflict(cache) = false, want true for the cache directory itself")
	}
	if _, blocked := blessedCacheConflict(tmp); !blocked {
		t.Error("blessedCacheConflict(parent) = false, want true; an ancestor of the cache blesses the cache too when resolved")
	}
	if _, blocked := blessedCacheConflict(filepath.Join(cache, "Composition A")); !blocked {
		t.Error("blessedCacheConflict(inside the cache) = false, want true")
	}
	if _, blocked := blessedCacheConflict(filepath.Join(tmp, "unrelated")); blocked {
		t.Error("blessedCacheConflict(unrelated sibling) = true, want false")
	}
}

// TestCacheConflictAnswersForACallerThatIsNotAboutToRegister exists because a
// caller that RECOMMENDS a registration has to be able to ask the same question
// Register answers, without attempting the registration to find out. Answering
// it a second way, in a second package, is how two answers start to disagree —
// which is exactly the reason FormerPathOwner is exported alongside
// ResolveAmong rather than being reimplemented by reporting.
func TestCacheConflictAnswersForACallerThatIsNotAboutToRegister(t *testing.T) {
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "Autosave Vault")

	restore := cacheSourceRoots
	cacheSourceRoots = func() ([]string, error) { return []string{cache}, nil }
	defer func() { cacheSourceRoots = restore }()

	root, refused := CacheConflict(filepath.Join(cache, "Composition A"))
	if !refused {
		t.Error("CacheConflict(inside the cache) = false, want true")
	}
	if root != cache {
		t.Errorf("CacheConflict named %q as the cache, want %q", root, cache)
	}
	if _, refused := CacheConflict(filepath.Join(tmp, "unrelated")); refused {
		t.Error("CacheConflict(unrelated sibling) = true, want false")
	}

	// The property that makes it worth exporting: it agrees with Register on
	// the same directory, rather than being a second opinion about it.
	if _, err := Register(Registration{
		Dir: cache, Client: "Ledgerwave", Project: "nobedan",
	}); err == nil {
		t.Fatal("Register blessed a declared cache; CacheConflict and Register disagree")
	}
}

// TestIsAncestorOrSameAnswersForTheFilesystemRoot is a regression test for a
// defect found on 2026-08-01 by the first run of `timetattle orient` against
// the live store.
//
// filepath.Clean("/") is "/", so a+string(filepath.Separator) is "//" when a is
// the root, and no path begins with "//". The root therefore contained nothing,
// with three consequences inside this package: Register would have blessed "/"
// even though the Motion Autosave Vault sits beneath it and blessedCacheConflict
// exists precisely to refuse that; Consolidate would have accepted "/" as a
// former path; and FormerPathOwner would never have matched one. The root is not
// a hypothetical locator — 1,311 Claude transcript observations on this machine
// carry "/" as the cwd they were recorded at.
func TestIsAncestorOrSameAnswersForTheFilesystemRoot(t *testing.T) {
	if !isAncestorOrSame("/", "/Users/you/git/acorn") {
		t.Error(`isAncestorOrSame("/", "/Users/you/git/acorn") = false; the root is above everything`)
	}
	if !isAncestorOrSame("/", "/") {
		t.Error(`isAncestorOrSame("/", "/") = false; the "OrSame" half must still hold`)
	}
	if isAncestorOrSame("/Users/you/git/acorn", "/") {
		t.Error(`isAncestorOrSame("/Users/you/git/acorn", "/") = true; nothing is above the root`)
	}
	if isAncestorOrSame("/Users/you/git/ac", "/Users/you/git/acorn") {
		t.Error(`isAncestorOrSame("/Users/you/git/ac", "/Users/you/git/acorn") = true; a string prefix is not a path prefix`)
	}

	// The consequence that matters, exercised through the caller it was found
	// in: a cache anywhere beneath the root makes the root unblessable.
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "Autosave Vault")
	restore := cacheSourceRoots
	cacheSourceRoots = func() ([]string, error) { return []string{cache}, nil }
	defer func() { cacheSourceRoots = restore }()

	if _, blocked := blessedCacheConflict("/"); !blocked {
		t.Error("blessedCacheConflict(\"/\") = false; blessing the root blesses the vault under it")
	}
}

// TestConsolidateRefusesACacheAsAFormerPath is the synthetic counterpart to
// TestConsolidateRefusesAFormerPathInTheMotionVault, run against a cache root
// this test controls so it does not depend on what manifests are installed.
//
// It covers the direction that matters most and is easiest to miss: the guard
// on the TARGET is nearly redundant, because Register already refused to bless
// a cache, but nothing else stops a vanished path INSIDE a cache being declared
// a former path, which would attribute every client's work in it to one
// project by a different route.
func TestConsolidateRefusesACacheAsAFormerPath(t *testing.T) {
	tmp := t.TempDir()
	cache := filepath.Join(tmp, "Autosave Vault")

	dir := t.TempDir()
	if _, err := Register(Registration{Dir: dir, Client: "Ledgerwave", Project: "nobedan"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := RecordInIndex(index, dir); err != nil {
		t.Fatalf("RecordInIndex: %v", err)
	}

	restore := cacheSourceRoots
	cacheSourceRoots = func() ([]string, error) { return []string{cache}, nil }
	defer func() { cacheSourceRoots = restore }()

	// A composition folder inside the vault that has since been deleted: absent
	// on disk, so every other guard passes it.
	former := filepath.Join(cache, "Composition A")
	if _, err := os.Stat(former); !os.IsNotExist(err) {
		t.Fatalf("fixture %s exists (%v); this test needs an absent path", former, err)
	}
	if _, err := Consolidate(Consolidation{Dir: dir, Former: former, IndexPath: index}); err == nil {
		t.Error("Consolidate() naming a vanished path inside a declared cache = nil error, want a refusal")
	}
	if _, err := Consolidate(Consolidation{Dir: dir, Former: cache, IndexPath: index}); err == nil {
		t.Error("Consolidate() naming a declared cache root itself = nil error, want a refusal")
	}
}

// TestConsolidateRefusesACacheAsATarget covers the other direction. It is only
// reachable if a manifest starts declaring a directory a cache AFTER it was
// registered, but the check costs one call and the failure it prevents is the
// same one Register refuses.
func TestConsolidateRefusesACacheAsATarget(t *testing.T) {
	dir := t.TempDir()
	if _, err := Register(Registration{Dir: dir, Client: "Ledgerwave", Project: "nobedan"}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := RecordInIndex(index, dir); err != nil {
		t.Fatalf("RecordInIndex: %v", err)
	}

	restore := cacheSourceRoots
	cacheSourceRoots = func() ([]string, error) { return []string{dir}, nil }
	defer func() { cacheSourceRoots = restore }()

	former := filepath.Join(t.TempDir(), "theaitrellis")
	if _, err := Consolidate(Consolidation{Dir: dir, Former: former, IndexPath: index}); err == nil {
		t.Error("Consolidate() into a directory a manifest now declares a shared cache = nil error, want a refusal")
	}
}
