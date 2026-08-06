//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -count=1 -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package registry_test

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// The RULE is tested portably in client_test.go, on invented names under
// t.TempDir(). What this adds is the other half CLAUDE.md insists on: a
// constructed case and a real-data check sit BESIDE each other, and neither
// substitutes for the other. An over-general rule once passed against the whole
// live Biome stream purely because nothing happened to trigger it.
//
// Here the risk runs the other way round. The enforcement in Register is being
// added to a machine whose markers were all written BEFORE it existed, so if
// any two of them already disagree, the rule lands on data that violates it and
// the next `timetattle register` on an unrelated directory starts failing. This
// is the check that says whether a data fix is needed, and it names the
// colliding project rather than only counting.
func TestNoRegisteredProjectOnThisMachineIsClaimedByTwoClients(t *testing.T) {
	path, err := registry.DefaultIndexPath()
	if err != nil {
		t.Skipf("no home directory to find the registry index in: %v", err)
	}
	registered, err := registry.List(path)
	if err != nil {
		t.Fatalf("List(%s): %v", path, err)
	}
	if len(registered) == 0 {
		// NOTHING WAS RECORDED, which is not the same as "no collisions". A
		// machine with an empty index has not been observed to be clean; it has
		// not been observed at all.
		t.Skipf("%s names no registered directory, so this machine has no marker population "+
			"to check; that is not evidence of agreement", path)
	}

	owner := map[string]*registry.Marker{}
	for _, m := range registered {
		first, seen := owner[m.Project]
		if seen && first.Client != m.Client {
			t.Errorf("project %q is claimed by two clients: %s holds it for one and %s for "+
				"another. registry.Register now refuses this, so one of the two has to be "+
				"corrected before the next registration on this machine will succeed",
				m.Project, first.Path, m.Path)
			continue
		}
		if !seen {
			owner[m.Project] = m
		}
	}
	t.Logf("%d registered directories carry %d distinct project names, none claimed twice",
		len(registered), len(owner))
}
