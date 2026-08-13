package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// consolidated registers a directory, declares former as one of its former
// paths, and returns the list of registered markers as ResolveAmong's callers
// hand it over — which is exactly what harvest already has in hand from
// registry.List.
func consolidated(t *testing.T, client, project, former string) (dir string, registered []*registry.Marker) {
	t.Helper()
	dir, index := registeredDir(t, client, project)
	if _, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former, IndexPath: index}); err != nil {
		t.Fatalf("Consolidate(%s -> %s): %v", former, dir, err)
	}
	registered, err := registry.List(index)
	if err != nil {
		t.Fatalf("List(%s): %v", index, err)
	}
	return dir, registered
}

func TestResolveAmongMatchesAFormerPathExactly(t *testing.T) {
	former := goneDir(t, "theaitrellis")
	_, registered := consolidated(t, "Ledgerwave", "nobedan", former)

	m, found, err := registry.ResolveAmong(former, former, registered)
	if err != nil {
		t.Fatalf("ResolveAmong(%s) = %v", former, err)
	}
	if !found || m.Project != "nobedan" {
		t.Errorf("ResolveAmong(%s) = %v, %v, want the nobedan marker", former, m, found)
	}
}

// TestResolveAmongMatchesBeneathAFormerPath is the case an exact-match
// implementation gets wrong while looking correct. Measured on the live store
// 2026-08-01: of the 14,218 events under /Users/you/git/theaitrellis, 4,294 sit
// at /Users/you/git/theaitrellis/stacktrace. Matching only the exact directory
// recovers 9,924 of 14,218 — 70% — and reports success.
func TestResolveAmongMatchesBeneathAFormerPath(t *testing.T) {
	former := goneDir(t, "theaitrellis")
	_, registered := consolidated(t, "Ledgerwave", "nobedan", former)

	under := filepath.Join(former, "stacktrace")
	m, found, err := registry.ResolveAmong(under, under, registered)
	if err != nil {
		t.Fatalf("ResolveAmong(%s) = %v", under, err)
	}
	if !found || m.Project != "nobedan" {
		t.Errorf("ResolveAmong(%s) = %v, %v, want the nobedan marker; a former path must match a "+
			"PREFIX, not only the exact directory", under, m, found)
	}
}

// TestResolveAmongMatchesThePathNotTheDirectoryClimbedFrom is the regression
// test for the trap this design walked into on the first implementation.
//
// cmd/timetattle's attributionDirOf cannot tell a vanished directory from a
// file, so it strips a level off both — which is right for the climb and fatal
// here, because the stripped path is the renamed directory's PARENT and the
// former path does not cover it. Measured on the live store 2026-08-01: 9,924
// of the 14,218 stranded observations name /Users/you/git/theaitrellis exactly
// and would have been stripped to /Users/you/git, leaving only the 4,294 under
// theaitrellis/stacktrace to match — 30%, reported as success.
func TestResolveAmongMatchesThePathNotTheDirectoryClimbedFrom(t *testing.T) {
	former := goneDir(t, "theaitrellis")
	_, registered := consolidated(t, "Ledgerwave", "nobedan", former)

	m, found, err := registry.ResolveAmong(filepath.Dir(former), former, registered)
	if err != nil {
		t.Fatalf("ResolveAmong = %v", err)
	}
	if !found || m.Project != "nobedan" {
		t.Errorf("ResolveAmong(climbFrom=%s, path=%s) = %v, %v, want nobedan; the former path must be "+
			"matched against the path itself, not against whatever directory the climb starts from",
			filepath.Dir(former), former, m, found)
	}
}

// TestResolveAmongDoesNotMatchASiblingSharingATextualPrefix guards the
// difference between a path prefix and a string prefix. /x/theaitrellis-old
// starts with /x/theaitrellis and is a different directory.
func TestResolveAmongDoesNotMatchASiblingSharingATextualPrefix(t *testing.T) {
	former := goneDir(t, "theaitrellis")
	_, registered := consolidated(t, "Ledgerwave", "nobedan", former)

	sibling := former + "-old"
	_, found, err := registry.ResolveAmong(sibling, sibling, registered)
	if err != nil {
		t.Fatalf("ResolveAmong(%s) = %v", sibling, err)
	}
	if found {
		t.Errorf("ResolveAmong(%s) = found, want unattributed; %s is a sibling of the former path, not inside it",
			sibling, sibling)
	}
}

func TestResolveAmongWithNoRegistrationsBehavesLikeResolve(t *testing.T) {
	former := goneDir(t, "theaitrellis")
	if _, registered := consolidated(t, "Ledgerwave", "nobedan", former); len(registered) == 0 {
		t.Fatal("fixture produced no registered markers")
	}

	_, found, err := registry.ResolveAmong(former, former, nil)
	if err != nil {
		t.Fatalf("ResolveAmong(%s, nil) = %v", former, err)
	}
	if found {
		t.Error("ResolveAmong with no registrations found a former path; a caller that supplies no " +
			"registry must get the plain climb")
	}
}

// TestResolveAmongPrefersALiveMarkerAtTheFormerPath is the recreated-directory
// case. A former path is refused while it exists, but nothing stops a new
// repository being cloned there afterwards and registered to someone else.
// Events observed under it from then on belong to the new project, and the
// marker on disk is the fact.
func TestResolveAmongPrefersALiveMarkerAtTheFormerPath(t *testing.T) {
	former := goneDir(t, "theaitrellis")
	_, registered := consolidated(t, "Ledgerwave", "nobedan", former)

	if err := os.MkdirAll(former, 0o755); err != nil {
		t.Fatalf("recreate fixture: %v", err)
	}
	if _, err := registry.Register(registry.Registration{Dir: former, Client: "Ledgerwave", Project: "Reoccupied"}); err != nil {
		t.Fatalf("Register(recreated): %v", err)
	}

	m, found, err := registry.ResolveAmong(former, former, registered)
	if err != nil {
		t.Fatalf("ResolveAmong(%s) = %v", former, err)
	}
	if !found || m.Project != "Reoccupied" {
		t.Errorf("ResolveAmong(%s) = %v, %v, want Reoccupied; a marker on disk at the path outranks a "+
			"declaration about what the path used to be", former, m, found)
	}
}

// TestResolveAmongPrefersAFormerPathOverAShallowerLiveMarker applies the rule
// the climb already uses — the most specific claim on a path wins — across both
// mechanisms. Without it, blessing a container directory such as ~/git would
// silently shadow every declared rename beneath it.
func TestResolveAmongPrefersAFormerPathOverAShallowerLiveMarker(t *testing.T) {
	container := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: container, Client: "Ledgerwave", Project: "Container"}); err != nil {
		t.Fatalf("Register(container): %v", err)
	}
	former := filepath.Join(container, "theaitrellis")
	_, registered := consolidated(t, "Ledgerwave", "nobedan", former)

	under := filepath.Join(former, "stacktrace")
	m, found, err := registry.ResolveAmong(under, under, registered)
	if err != nil {
		t.Fatalf("ResolveAmong(%s) = %v", under, err)
	}
	if !found || m.Project != "nobedan" {
		t.Errorf("ResolveAmong(%s) = %v, %v, want nobedan; the former path is a more specific claim "+
			"than a marker further up the tree", under, m, found)
	}
}

// TestResolveAmongPrefersTheDeeperOfTwoFormerPaths settles what happens when
// two projects declare nested former paths. The same most-specific-wins rule
// answers it, so there is no second precedence to learn.
func TestResolveAmongPrefersTheDeeperOfTwoFormerPaths(t *testing.T) {
	outerFormer := goneDir(t, "theaitrellis")
	outerDir, index := registeredDir(t, "Ledgerwave", "nobedan")
	if _, err := registry.Consolidate(registry.Consolidation{Dir: outerDir, Former: outerFormer, IndexPath: index}); err != nil {
		t.Fatalf("Consolidate(outer): %v", err)
	}

	innerDir := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: innerDir, Client: "Ledgerwave", Project: "ThirdyCap"}); err != nil {
		t.Fatalf("Register(inner): %v", err)
	}
	if err := registry.RecordInIndex(index, innerDir); err != nil {
		t.Fatalf("RecordInIndex(inner): %v", err)
	}
	innerFormer := filepath.Join(outerFormer, "stacktrace")
	if _, err := registry.Consolidate(registry.Consolidation{Dir: innerDir, Former: innerFormer, IndexPath: index}); err != nil {
		t.Fatalf("Consolidate(inner): %v", err)
	}

	registered, err := registry.List(index)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	under := filepath.Join(innerFormer, "deeper")
	m, found, err := registry.ResolveAmong(under, under, registered)
	if err != nil {
		t.Fatalf("ResolveAmong(%s) = %v", under, err)
	}
	if !found || m.Project != "ThirdyCap" {
		t.Errorf("ResolveAmong(%s) = %v, %v, want ThirdyCap, the deeper former path", under, m, found)
	}
}
