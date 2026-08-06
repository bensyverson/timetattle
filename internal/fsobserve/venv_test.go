package fsobserve

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCollectSkipsVirtualenvs is the same test node_modules already passes,
// applied to the Python equivalent: an installed dependency tree is not the
// user's work on any machine.
//
// Measured on the live store 2026-08-01 (task 2s1kP): 27,645 of 130,594
// attributed fs-observations — 21.2% — were inside a `.venv`, against 0 for
// node_modules and 0 for .git, which are skipped whole. 27,591 of those 27,645
// were under site-packages, and there were 0 site-packages files anywhere
// outside a virtualenv. A `pip install` writes a dependency tree in one burst
// and every file in it carries a fresh mtime.
func TestCollectSkipsVirtualenvs(t *testing.T) {
	dir := t.TempDir()
	buried := filepath.Join(dir, ".venv", "lib", "python3.12", "site-packages", "aiohttp", "client.motn")
	if err := os.MkdirAll(filepath.Dir(buried), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	write(t, buried, "somebody else's package")
	real := filepath.Join(dir, "kept.motn")
	write(t, real, "real work")

	events := collect(t, dir)
	for _, e := range events {
		if e.Locator != real {
			t.Errorf("got an event from inside a virtualenv: %+v", e)
		}
	}
	if len(eventsFor(events, real)) == 0 {
		t.Error("the one file outside the virtualenv produced no events at all")
	}
}

// TestCollectKeepsDirectoriesMerelyNamedLikeAVirtualenv is the narrowness half,
// and it is not hypothetical. The live store holds
// /Users/ben/git/timetattle/scripts/spike/env/main.go — this repository's own
// source, in a directory called `env`. Excluding `env`, or `venv` without its
// dot, to catch "virtualenvs are sometimes called that" would delete it.
//
// Measured 2026-08-01 across the whole store: `.venv` matched 27,645
// observations; `/venv/` matched 1 and `/env/` matched 1, and BOTH of those
// were the two files named above rather than any virtualenv. `.tox`, `.env/`
// and `virtualenv/` matched nothing at all. The rule is as wide as the
// evidence and no wider.
//
// This test is GREEN before the change as well as after, so it was proved by
// MUTATION rather than by the red stage. Replacing the exact base-name lookup
// with `strings.Contains(d.Name(), "env")` makes all four of these files vanish
// and fails this test on all four. Against the real tree the same widening
// costs /Users/ben/git/timetattle exactly one file — scripts/spike/env/main.go,
// 289 filtered files down to 288 under `go run ./scripts/filedays -root
// /Users/ben/git/timetattle -exclude '...,env,venv,...'`.
func TestCollectKeepsDirectoriesMerelyNamedLikeAVirtualenv(t *testing.T) {
	dir := t.TempDir()
	var want []string
	for _, sub := range []string{"env", "venv", ".venv-old", "venv2"} {
		path := filepath.Join(dir, sub, "main.motn")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		write(t, path, "source somebody wrote")
		want = append(want, path)
	}

	events := collect(t, dir)
	for _, path := range want {
		if len(eventsFor(events, path)) == 0 {
			t.Errorf("%s produced no events; the virtualenv rule reached past a directory named exactly \".venv\"",
				path)
		}
	}
}
