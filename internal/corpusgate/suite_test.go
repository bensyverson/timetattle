package corpusgate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/corpusgate"
)

// exempt names the untagged test files that may call a machine resolver, and
// says why each is not a coupling. Every entry here is a claim somebody has to
// defend at review; none of them may be "it was already there".
//
// Two shapes qualify, and nothing else does:
//
//  1. The test asserts what the resolver RETURNS and never opens it. A path is
//     a string until somebody stats it.
//  2. The test is a GUARD: it resolves the default path in order to prove
//     something was NOT written there. Converting or tagging one of these would
//     delete the protection it exists to give. cmd/timetattle's TestMain points
//     HOME at a temp directory for the whole binary, so these resolve an empty
//     home and pass anywhere.
var exempt = map[string]string{
	filepath.Join("cmd", "timetattle", "chains_test.go"):              "guard: stats ~/.timetattle/timetattle.db to prove -store redirected the write. TestMain has already pointed HOME at a temp directory.",
	filepath.Join("cmd", "timetattle", "consolidate_test.go"):         "asserts the registered-directories index path under the TestMain home; nothing is read from it.",
	filepath.Join("cmd", "timetattle", "filedays_vault_test.go"):      "asserts that the default vault root equals the one the shipped manifest declares. A string comparison; the vault is never opened.",
	filepath.Join("cmd", "timetattle", "harvest_test.go"):             "guard: TestTheStoreFlagRedirectsWrites fingerprints the default store before and after to prove -store redirected every write.",
	filepath.Join("cmd", "timetattle", "sessionize_test.go"):          "guard: stats the default store after sessionize -store to prove the derivation did not land in it.",
	filepath.Join("internal", "config", "config_test.go"):             "asserts config.DefaultPath() == ~/.timetattle/config.json as a string.",
	filepath.Join("internal", "corpusgate", "suite_test.go"):          "the gate itself: os.Getwd to find the module root it has to scan. It reads only *_test.go files inside this repository.",
	filepath.Join("internal", "filedays", "filedays_test.go"):         "asserts DefaultRootsFile() as a string.",
	filepath.Join("internal", "gitlog", "realdata_test.go"):           "the 'real data' is THIS repository, read through git from the test's own working directory. It travels with the checkout, so it is not the machine: a fresh clone gets the same history, and the assertions are properties rather than counts.",
	filepath.Join("internal", "jobs", "jobs_test.go"):                 "os.Getwd only to build a path RELATIVE to a t.TempDir(), which is the input to the locator-absolutisation test. Nothing outside the temp dir is opened.",
	filepath.Join("internal", "manifest", "builtin_test.go"):          "reads the embedded manifests and asserts their fields. No root is stat-ed and no Spotlight query is run; internal/manifest/realdata_test.go is the file that does both, and it is behind the tag.",
	filepath.Join("internal", "registry", "vaultroot_test.go"):        "resolves the shipped motion-autosave manifest's root as a STRING, for the consolidate test that asserts on a refusal reason reached before anything is stat-ed. The two tests that go on to stat it are in vault_realdata_test.go.",
	filepath.Join("internal", "snapshot", "biome_test.go"):            "asserts DefaultBiomeRoot() and DefaultBiomeDir() as strings.",
	filepath.Join("internal", "snapshot", "snapshot_test.go"):         "asserts DefaultSourcePath() and DefaultDir() as strings.",
	filepath.Join("internal", "storefixture", "storefixture_test.go"): "guard: names store.DefaultPath() in order to assert the fixture store is NOT it.",
	filepath.Join("internal", "timestamped", "timestamped_test.go"):   "os.Getwd only to check that a relative root is absolutised against it. The vault tests that read a real one moved to vault_realdata_test.go.",
}

// repoRoot walks up from the test's own directory to the module root, so the
// scan covers the whole tree rather than the package it happens to live in.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod in any ancestor of the working directory")
		}
		dir = parent
	}
}

// TestNoUntaggedTestReachesForTheMachine is the gate itself.
//
// It is the third instrument, and the only one that fires BEFORE the coverage
// is lost. `go test ./...` cannot tell a skip from a pass; scripts/foreignsuite
// can, but only after somebody runs it; this one turns the next occurrence red
// in the ordinary suite.
//
// Proved by mutation: adding `store.DefaultPath()` to any untagged test file
// turns this red, naming the file, the line and what the call resolves.
func TestNoUntaggedTestReachesForTheMachine(t *testing.T) {
	root := repoRoot(t)
	found, err := corpusgate.Scan(root, exempt)
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	for _, f := range found {
		t.Errorf("%s\n\tPut it behind //go:build realdata, or exempt it in "+
			"internal/corpusgate/suite_test.go with the reason it reads nothing.", f)
	}
	if len(found) > 0 {
		t.Logf("%d untagged test files reach for the machine the suite is running on", len(found))
	}
}

// An exemption that has stopped buying anything is a stale claim, and a stale
// claim in an allowlist is how the allowlist stops meaning anything. This is
// the half of the gate that shrinks it.
func TestNoExemptionIsStale(t *testing.T) {
	unspent, err := corpusgate.Unspent(repoRoot(t), exempt)
	if err != nil {
		t.Fatalf("Unspent: %v", err)
	}
	for _, file := range unspent {
		t.Errorf("%s is exempted in internal/corpusgate/suite_test.go but no longer "+
			"calls any machine resolver; delete the exemption", file)
	}
}
