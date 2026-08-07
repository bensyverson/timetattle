package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TIMETATTLE_CPUPROFILE is the hook scripts/profile drives: point it at a
// path and `run` wraps whatever subcommand follows in a pprof CPU profile.
// It has to live in `run` rather than in an individual subcommand's own main
// because the point is to profile ANY subcommand without that subcommand
// knowing profiling exists — see scripts/profile's doc comment for why a
// re-runnable harness needs this rather than a throwaway `sample` session.
//
// `version` is the cheapest real subcommand to drive this test with: it
// touches no store and no filesystem beyond the profile file itself.

func TestRunWritesACPUProfileWhenTheEnvVarNamesAPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cpu.pprof")
	t.Setenv("TIMETATTLE_CPUPROFILE", path)

	var stdout, stderr bytes.Buffer
	status := run([]string{"version"}, &stdout, &stderr, "", "")
	if status != 0 {
		t.Fatalf("run: status %d, stderr: %s", status, stderr.String())
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("expected a profile at %s: %v", path, err)
	}
	if info.Size() == 0 {
		t.Errorf("profile file at %s is empty", path)
	}
}

func TestRunWithoutTheEnvVarWritesNoProfile(t *testing.T) {
	// The common case, and the one that must stay exactly as cheap as it was
	// before this hook existed: no env var, no file touched anywhere.
	t.Setenv("TIMETATTLE_CPUPROFILE", "")

	var stdout, stderr bytes.Buffer
	status := run([]string{"version"}, &stdout, &stderr, "", "")
	if status != 0 {
		t.Fatalf("run: status %d, stderr: %s", status, stderr.String())
	}
}

func TestRunFailsClearlyWhenTheProfilePathCannotBeCreated(t *testing.T) {
	// A directory that does not exist, so os.Create fails outright. The
	// failure must be reported, not swallowed with a silently-missing file.
	path := filepath.Join(t.TempDir(), "no-such-dir", "cpu.pprof")
	t.Setenv("TIMETATTLE_CPUPROFILE", path)

	var stdout, stderr bytes.Buffer
	status := run([]string{"version"}, &stdout, &stderr, "", "")
	if status == 0 {
		t.Fatalf("expected a non-zero status when the profile path can't be created")
	}
	if stderr.Len() == 0 {
		t.Errorf("expected stderr to say why")
	}
}
