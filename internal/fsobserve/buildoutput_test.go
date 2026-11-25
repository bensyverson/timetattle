package fsobserve

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCollectSkipsBuildOutput: Swift Package Manager writes everything it
// builds, dependency checkouts included, under a directory named ".build", and
// none of it is work somebody did.
func TestCollectSkipsBuildOutput(t *testing.T) {
	dir := t.TempDir()
	buried := filepath.Join(dir, ".build", "checkouts", "dep", "client.motn")
	if err := os.MkdirAll(filepath.Dir(buried), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	write(t, buried, "generated")
	real := filepath.Join(dir, "kept.motn")
	write(t, real, "real work")

	events := collect(t, dir)
	for _, e := range events {
		if e.Locator != real {
			t.Errorf("got an event from inside build output: %+v", e)
		}
	}
	if len(eventsFor(events, real)) == 0 {
		t.Error("the one file outside the build output produced no events at all")
	}
}

// TestCollectKeepsNamesMerelyLikeBuildOutput: the name is matched whole and
// only against directories.
func TestCollectKeepsNamesMerelyLikeBuildOutput(t *testing.T) {
	dir := t.TempDir()
	var want []string
	for _, rel := range []string{"build/main.motn", ".build-old/main.motn", "x.build/main.motn", "plain/.build"} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		write(t, path, "somebody wrote this")
		want = append(want, path)
	}

	events := collect(t, dir)
	for _, path := range want {
		if len(eventsFor(events, path)) == 0 {
			t.Errorf("%s produced no events; the rule reached past a directory named exactly \".build\"", path)
		}
	}
}
