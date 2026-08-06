package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// TestRegisterCommandAddsTheDirectoryToTheIndexAtTheIndexFlag is the new
// half of what 'timetattle register' does: not just writing the marker
// (covered by register_test.go, untouched here) but recording the directory
// where 'timetattle harvest' will later find it via registry.List.
func TestRegisterCommandAddsTheDirectoryToTheIndexAtTheIndexFlag(t *testing.T) {
	dir := t.TempDir()
	idx := filepath.Join(t.TempDir(), "registered-directories.json")

	if err := register([]string{
		"-client", "ACORN", "-project", "Holiday Campaign", "-path", dir, "-index", idx,
	}); err != nil {
		t.Fatalf("register() = %v", err)
	}

	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List(%s) = %v", idx, err)
	}
	if len(list) != 1 {
		t.Fatalf("List(%s) = %d entries, want 1", idx, len(list))
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("filepath.Abs(%s) = %v", dir, err)
	}
	if list[0].Path != abs || list[0].Client != "ACORN" || list[0].Project != "Holiday Campaign" {
		t.Errorf("List(%s)[0] = %+v, want Path=%s Client=ACORN Project=Holiday Campaign", idx, list[0], abs)
	}
}

// TestRegisterCommandToleratesAnIndexWriteFailure is the regression test for
// the design this task settled on: an index-maintenance failure must not
// fail the registration itself. The marker is the fact; the index is a
// convenience for enumeration, explicitly re-verified against the marker by
// List rather than trusted — see internal/registry's List doc — so a
// directory that could not be indexed this time is not lost, only harder to
// discover automatically until the next successful index write.
//
// This is not a hypothetical: writes to a real machine's ~/.timetattle are
// blocked by this project's own sandboxed test environment, which is
// exactly why cmd/timetattle's -index (like -store before it) must default
// to a path that a failure to write does not turn into a failed command.
func TestRegisterCommandToleratesAnIndexWriteFailure(t *testing.T) {
	dir := t.TempDir()

	// A regular file where the index's parent directory needs to be
	// creates an EEXIST-shaped failure for os.MkdirAll, standing in for
	// "the index could not be written" without depending on real
	// permission denial, which is not portable across machines running
	// this suite.
	blocker := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	idx := filepath.Join(blocker, "registered-directories.json")

	if err := register([]string{
		"-client", "ACORN", "-project", "Holiday Campaign", "-path", dir, "-index", idx,
	}); err != nil {
		t.Fatalf("register() = %v, want nil even though the index write behind %s cannot succeed", err, idx)
	}

	m, found, err := registry.Resolve(dir)
	if err != nil || !found {
		t.Fatalf("Resolve(%s) = %v, %v, %v; the marker itself must still have been written", dir, m, found, err)
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Errorf("Resolve(%s) = %s / %s, want ACORN / Holiday Campaign", dir, m.Client, m.Project)
	}
}
