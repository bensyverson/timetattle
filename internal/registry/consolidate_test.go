package registry_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// goneDir returns a path under a temporary directory that is guaranteed not to
// exist, standing in for a directory that was renamed away — the whole reason
// Consolidate exists. Measured on the live store 2026-08-01: 14,218 events name
// /Users/ben/git/theaitrellis, which was renamed to nobedan and is gone.
func goneDir(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("fixture %s already exists (%v); this test needs a path that is absent", p, err)
	}
	return p
}

// registeredDir blesses a fresh temporary directory and records it in an index
// beside it, returning both. Consolidate needs the index to answer "does
// another project already claim this former path", which is the one question
// that cannot be answered from a single marker.
func registeredDir(t *testing.T, client, project string) (dir, indexPath string) {
	t.Helper()
	dir = t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: client, Project: project}); err != nil {
		t.Fatalf("Register(%s): %v", dir, err)
	}
	indexPath = filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(indexPath, dir); err != nil {
		t.Fatalf("RecordInIndex(%s): %v", dir, err)
	}
	return dir, indexPath
}

func markerFields(t *testing.T, dir string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, registry.MarkerFilename))
	if err != nil {
		t.Fatalf("read marker in %s: %v", dir, err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal marker in %s: %v", dir, err)
	}
	return fields
}

func TestConsolidateRecordsTheFormerPathInTheTargetsMarker(t *testing.T) {
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")
	former := goneDir(t, "theaitrellis")

	m, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former, IndexPath: index})
	if err != nil {
		t.Fatalf("Consolidate() = %v", err)
	}
	if len(m.FormerPaths) != 1 || m.FormerPaths[0] != former {
		t.Errorf("FormerPaths = %v, want [%s]", m.FormerPaths, former)
	}
	if m.Client != "Ledgerwave" || m.Project != "nobedan" {
		t.Errorf("Consolidate() changed identity to %s / %s; it must only add a former path", m.Client, m.Project)
	}

	fields := markerFields(t, dir)
	got, ok := fields["former_paths"]
	if !ok {
		t.Fatalf("marker on disk has no former_paths field: %v", fields)
	}
	list, ok := got.([]any)
	if !ok || len(list) != 1 || list[0] != former {
		t.Errorf("former_paths on disk = %v, want a one-element array holding %s", got, former)
	}
	// The former path is identity, not a contract value, so nothing else may
	// have appeared alongside it. See registry's package doc.
	want := map[string]bool{"marker_version": true, "client": true, "project": true, "registered": true, "former_paths": true}
	for k := range fields {
		if !want[k] {
			t.Errorf("marker has unexpected field %q after Consolidate", k)
		}
	}
}

func TestConsolidateIsIdempotent(t *testing.T) {
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")
	former := goneDir(t, "theaitrellis")

	if _, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former, IndexPath: index}); err != nil {
		t.Fatalf("first Consolidate() = %v", err)
	}
	m, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former, IndexPath: index})
	if err != nil {
		t.Fatalf("second Consolidate() = %v", err)
	}
	if len(m.FormerPaths) != 1 {
		t.Errorf("FormerPaths = %v after declaring the same former path twice, want one entry", m.FormerPaths)
	}
}

func TestConsolidateAccumulatesSeveralFormerPaths(t *testing.T) {
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")
	first := goneDir(t, "theaitrellis")
	second := goneDir(t, "trellis-prototype")

	if _, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: first, IndexPath: index}); err != nil {
		t.Fatalf("Consolidate(first) = %v", err)
	}
	m, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: second, IndexPath: index})
	if err != nil {
		t.Fatalf("Consolidate(second) = %v", err)
	}
	if len(m.FormerPaths) != 2 {
		t.Fatalf("FormerPaths = %v, want both %s and %s; a directory can be renamed more than once",
			m.FormerPaths, first, second)
	}
}

func TestConsolidateRefusesAFormerPathThatStillExists(t *testing.T) {
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")
	live := t.TempDir()

	_, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: live, IndexPath: index})
	if err == nil {
		t.Fatal("Consolidate() with a former path that still exists = nil error, want a refusal; " +
			"a live directory should be registered normally, not aliased")
	}
}

func TestConsolidateRefusesATargetThatIsNotRegistered(t *testing.T) {
	dir := t.TempDir()
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	former := goneDir(t, "theaitrellis")

	_, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former, IndexPath: index})
	if err == nil {
		t.Fatal("Consolidate() into an unregistered directory = nil error, want a refusal; " +
			"inventing an owner for unrecognised evidence is the failure this guard exists for")
	}
}

// TestConsolidateRefusesATargetThatOnlyInheritsAMarkerFromAnAncestor pins down
// that "registered" means a marker in THIS directory, not one resolvable from
// it. Resolve climbing to an ancestor is right for attribution and wrong here:
// writing a former path into a parent's marker would silently widen a claim the
// operator never made.
func TestConsolidateRefusesATargetThatOnlyInheritsAMarkerFromAnAncestor(t *testing.T) {
	parent, index := registeredDir(t, "Ledgerwave", "nobedan")
	child := filepath.Join(parent, "sub")
	if err := os.Mkdir(child, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	former := goneDir(t, "theaitrellis")

	_, err := registry.Consolidate(registry.Consolidation{Dir: child, Former: former, IndexPath: index})
	if err == nil {
		t.Fatal("Consolidate() into a directory that only inherits a marker = nil error, want a refusal")
	}
}

func TestConsolidateRefusesATargetThatDoesNotExist(t *testing.T) {
	index := filepath.Join(t.TempDir(), "registered-directories.json")
	_, err := registry.Consolidate(registry.Consolidation{
		Dir:       goneDir(t, "also-gone"),
		Former:    goneDir(t, "theaitrellis"),
		IndexPath: index,
	})
	if err == nil {
		t.Fatal("Consolidate() into a directory that does not exist = nil error, want a refusal")
	}
}

func TestConsolidateRefusesAFormerPathAnotherProjectAlreadyClaims(t *testing.T) {
	first, index := registeredDir(t, "Ledgerwave", "nobedan")
	second := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: second, Client: "Ledgerwave", Project: "ThirdyCap"}); err != nil {
		t.Fatalf("Register(second): %v", err)
	}
	if err := registry.RecordInIndex(index, second); err != nil {
		t.Fatalf("RecordInIndex(second): %v", err)
	}
	former := goneDir(t, "theaitrellis")

	if _, err := registry.Consolidate(registry.Consolidation{Dir: first, Former: former, IndexPath: index}); err != nil {
		t.Fatalf("Consolidate(first) = %v", err)
	}
	_, err := registry.Consolidate(registry.Consolidation{Dir: second, Former: former, IndexPath: index})
	if err == nil {
		t.Fatal("Consolidate() of a former path a different project already claims = nil error, want a refusal; " +
			"one vanished path cannot have belonged to two projects at once")
	}
}

func TestConsolidateRefusesAFormerPathInsideTheTargetItself(t *testing.T) {
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")
	inside := filepath.Join(dir, "never-existed")

	_, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: inside, IndexPath: index})
	if err == nil {
		t.Fatal("Consolidate() of a path inside the target = nil error, want a refusal; " +
			"resolution already climbs to the target's own marker, so the declaration says nothing")
	}
}

func TestConsolidateRequiresAnIndexPath(t *testing.T) {
	dir, _ := registeredDir(t, "Ledgerwave", "nobedan")
	_, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: goneDir(t, "theaitrellis")})
	if err == nil {
		t.Fatal("Consolidate() without an index path = nil error, want one; without the index there is no way " +
			"to check whether another project already claims the former path, and a silently skipped guard is not a guard")
	}
}

// TestRegisterWithForcePreservesFormerPaths pins down that a former path
// survives a reassignment. "This directory used to be over there" is a fact
// about the LOCATION, not about whoever currently owns it, so correcting a
// client or project must not silently discard it — and rewriting the marker is
// exactly where it would be lost without noticing.
func TestRegisterWithForcePreservesFormerPaths(t *testing.T) {
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")
	former := goneDir(t, "theaitrellis")
	if _, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former, IndexPath: index}); err != nil {
		t.Fatalf("Consolidate: %v", err)
	}

	m, err := registry.Register(registry.Registration{
		Dir: dir, Client: "Ledgerwave", Project: "nobedan-renamed", Force: true,
	})
	if err != nil {
		t.Fatalf("forced Register: %v", err)
	}
	if len(m.FormerPaths) != 1 || m.FormerPaths[0] != former {
		t.Errorf("FormerPaths = %v after a forced reassignment, want [%s] carried through", m.FormerPaths, former)
	}
}

// TestConsolidateRefusesAFormerPathInTheMotionVault is the real-data
// counterpart to the synthetic cache tests in internal_test.go, checked against
// the actual shipped manifest. Declaring the vault a former path of any project
// would attribute every client's compositions to that one project — the same
// failure registry.Register already refuses, reached by a different route.
//
// It asserts on the REASON, not merely on a refusal. The vault exists on a
// machine that has Motion, so the "a former path must not still exist" guard
// would refuse it too, and a test satisfied by any error cannot tell the two
// apart — it would keep passing with the cache guard deleted. Removing that
// guard is exactly what TestConsolidateRefusesACacheAsAFormerPath catches, and
// this is what makes the real vault, not a synthetic stand-in, reach it.
func TestConsolidateRefusesAFormerPathInTheMotionVault(t *testing.T) {
	root := motionVaultRoot(t)
	dir, index := registeredDir(t, "Ledgerwave", "nobedan")

	_, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: root, IndexPath: index})
	if err == nil {
		t.Fatal("Consolidate() naming the Motion autosave vault as a former path = nil error, want a refusal")
	}
	if !strings.Contains(err.Error(), "shared application cache") {
		t.Errorf("Consolidate() refused the Motion vault with %q; want the refusal to be that it is a "+
			"shared application cache, not something incidental such as it happening to exist", err)
	}
}
