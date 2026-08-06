//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package main

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/config"
)

// realAcorn is the reference project, or a skip.
//
// This used to hardcode "/Users/ben/git/acorn" — wrong on two counts, not
// one. TestMain points HOME at an empty temp directory for the whole binary,
// which is why the path could not simply be resolved through it; but the
// pseudonym scrub (task 5nCl4)
// also renamed the reference project everywhere else to "acorn" while
// nobody's disk changed name to match, so the literal path never existed
// either. Both problems have the same fix `liveSnapshotDir` already uses one
// file over: restore the real HOME for this test only, through t.Setenv (see
// task paDFg and internal/config/reference.go), and let
// config.ResolveReferenceProject do the resolving so this is not a second,
// independently-drifting copy of that logic.
func realAcorn(t *testing.T) string {
	t.Helper()
	if realHome == "" {
		t.Skip("the suite started with no HOME, so there is no machine to read")
	}
	t.Setenv("HOME", realHome)
	dir, err := config.ResolveReferenceProject("acorn")
	if err != nil {
		t.Skip(err.Error())
	}
	return dir
}

// -roots walks the disk instead of the store, and must never write. The
// observations it reads were never ingested, so a derived table built from them
// would assert that the store holds evidence it does not — and the next honest
// rebuild would silently contradict it.
//
// Proved by mutation: replacing the early return with a Rebuild of the walked
// result writes 1 run row, 8 chains and 29 steps into a store holding zero
// events — a derivation of the disk, recorded as though it were a derivation of
// recorded evidence, which the next honest rebuild would then contradict.
func TestChainsRootsWalksTheDiskAndNeverWrites(t *testing.T) {
	acorn := realAcorn(t)
	path := filepath.Join(t.TempDir(), "timetattle.db")

	// Create and migrate the store, so the derived tables exist and a write
	// would succeed if one were attempted.
	if _, err := capture(t, func() error {
		return chainsCommand([]string{"-store", path, "-dry-run"})
	}); err != nil {
		t.Fatalf("seed the store: %v", err)
	}

	out, err := capture(t, func() error {
		return chainsCommand([]string{"-store", path, "-roots", acorn})
	})
	if err != nil {
		t.Fatalf("chains -roots: %v", err)
	}
	if !strings.Contains(out, "nothing was written") {
		t.Errorf("output does not say that nothing was written:\n%s", out)
	}
	if !strings.Contains(out, "lightbar") {
		t.Errorf("output does not mention the lightbar chain:\n%s", out)
	}
	for _, table := range []string{"derived_chain_run", "derived_chain", "derived_chain_step"} {
		if n := derivedCount(t, path, table); n != 0 {
			t.Errorf("%s holds %d rows after a -roots run, want 0", table, n)
		}
	}
}
