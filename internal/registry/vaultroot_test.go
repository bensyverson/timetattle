package registry_test

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/manifest"
)

// This helper is on the UNTAGGED side of the realdata tag on purpose, and the
// distinction it rests on is the one this whole boundary turns on: it resolves
// a PATH, and a path is a string until somebody opens it.
//
// consolidate_test.go's TestConsolidateRefusesAFormerPathInTheMotionVault
// passes the string to Consolidate and asserts on the refusal's REASON. The
// cache check is a comparison against the manifest's declared roots and runs
// before anything is stat-ed, so that test holds on a machine with no Motion at
// all. The two tests that go on to stat the directory are in
// vault_realdata_test.go, behind the tag.

// motionVaultRoot is the real, shipped built-in for Apple Motion's Autosave
// Vault, expanded the same way Register itself expands it (internal/manifest
// resolves the leading ~ at load time). It is the concrete case the "never
// bless a cache" rule exists for: every client's Motion compositions land in
// this one directory, so registering it — or anything above it — would
// attribute all of them to whichever client the operator had in mind.
func motionVaultRoot(t *testing.T) string {
	t.Helper()
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("manifest.Builtin() = %v", err)
	}
	for _, s := range sources {
		if s.ID == "motion-autosave" {
			return s.Files.Root
		}
	}
	t.Fatal("built-in manifests no longer declare motion-autosave; this test needs updating along with it")
	return ""
}
