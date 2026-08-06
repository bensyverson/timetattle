package registry_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// TestRecordInIndexThenListFindsTheDirectory is the basic round trip: a
// directory Register blessed, then added to the index, comes back out of
// List with the identity the marker itself carries.
func TestRecordInIndexThenListFindsTheDirectory(t *testing.T) {
	dir := t.TempDir()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}

	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, m.Path); err != nil {
		t.Fatalf("RecordInIndex() = %v", err)
	}

	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() = %d entries, want 1: %+v", len(list), list)
	}
	if list[0].Path != m.Path || list[0].Client != "ACORN" || list[0].Project != "Holiday Campaign" {
		t.Errorf("List()[0] = %+v, want Path=%s Client=ACORN Project=Holiday Campaign", list[0], m.Path)
	}
}

// TestListOnAMissingIndexReturnsEmptyNotError matches every other "nothing
// configured yet" case in this codebase — filedays with no roots file,
// fsobserve with a root that does not exist — rather than treating a fresh
// machine that has never registered anything as an error.
func TestListOnAMissingIndexReturnsEmptyNotError(t *testing.T) {
	idx := filepath.Join(t.TempDir(), "does-not-exist.json")
	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List() on a missing index = %v, want nil error", err)
	}
	if len(list) != 0 {
		t.Errorf("List() on a missing index = %d entries, want 0", len(list))
	}
}

// TestListExcludesADirectoryWhoseMarkerWasDeleted is the regression case for
// criterion Z0U: removing a registration must be reflected the next time the
// list is read, not cached stale. The index entry survives the deletion —
// nothing here prunes it — but List must not report a directory whose marker
// is actually gone, because that would be exactly the kind of silent
// wrong-attribution bug this project's own rules warn about repeatedly.
func TestListExcludesADirectoryWhoseMarkerWasDeleted(t *testing.T) {
	dir := t.TempDir()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, m.Path); err != nil {
		t.Fatalf("RecordInIndex() = %v", err)
	}
	if list, err := registry.List(idx); err != nil || len(list) != 1 {
		t.Fatalf("List() before deletion = %v, %v, want 1 entry and no error", list, err)
	}

	if err := os.Remove(filepath.Join(dir, registry.MarkerFilename)); err != nil {
		t.Fatalf("remove marker fixture: %v", err)
	}

	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List() after the marker was deleted = %v", err)
	}
	if len(list) != 0 {
		t.Errorf("List() after the marker was deleted = %d entries, want 0; a stale index entry must not be reported as still registered", len(list))
	}
}

// TestListReflectsACorrectedRegistration is the other half of Z0U: a
// registration that was corrected (re-registered with -force to a different
// client/project) must show the new identity on the next List, not whatever
// the index happened to remember from when it was first indexed. The index
// never stores client or project — only a directory to re-check — so this
// is really just proving List re-reads the marker rather than trusting
// anything cached about it.
func TestListReflectsACorrectedRegistration(t *testing.T) {
	dir := t.TempDir()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, m.Path); err != nil {
		t.Fatalf("RecordInIndex() = %v", err)
	}

	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "Acme", Project: "Rebrand", Force: true}); err != nil {
		t.Fatalf("forced re-Register() = %v", err)
	}

	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("List() = %d entries, want 1", len(list))
	}
	if list[0].Client != "Acme" || list[0].Project != "Rebrand" {
		t.Errorf("List()[0] = %s / %s, want Acme / Rebrand; List must reflect the corrected registration, not a cached one", list[0].Client, list[0].Project)
	}
}

// TestRecordInIndexDoesNotDuplicateTheSameDirectory guards against List
// returning the same directory twice just because Register (or a setup
// script re-running it) was called against it more than once.
func TestRecordInIndexDoesNotDuplicateTheSameDirectory(t *testing.T) {
	dir := t.TempDir()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, m.Path); err != nil {
		t.Fatalf("first RecordInIndex() = %v", err)
	}
	if err := registry.RecordInIndex(idx, m.Path); err != nil {
		t.Fatalf("second RecordInIndex() = %v", err)
	}

	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List() = %d entries after indexing the same directory twice, want 1", len(list))
	}
}

// TestListFindsEveryRegisteredDirectory covers more than one entry, which is
// the shape harvest actually needs: every registered directory, not just
// the first one indexed.
func TestListFindsEveryRegisteredDirectory(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	mA, err := registry.Register(registry.Registration{Dir: dirA, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register(dirA) = %v", err)
	}
	mB, err := registry.Register(registry.Registration{Dir: dirB, Client: "Acme", Project: "Rebrand"})
	if err != nil {
		t.Fatalf("Register(dirB) = %v", err)
	}
	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, mA.Path); err != nil {
		t.Fatalf("RecordInIndex(dirA) = %v", err)
	}
	if err := registry.RecordInIndex(idx, mB.Path); err != nil {
		t.Fatalf("RecordInIndex(dirB) = %v", err)
	}

	list, err := registry.List(idx)
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("List() = %d entries, want 2: %+v", len(list), list)
	}
	byPath := map[string]*registry.Marker{}
	for _, m := range list {
		byPath[m.Path] = m
	}
	if byPath[mA.Path] == nil || byPath[mA.Path].Client != "ACORN" {
		t.Errorf("List() missing or wrong entry for dirA: %+v", byPath[mA.Path])
	}
	if byPath[mB.Path] == nil || byPath[mB.Path].Client != "Acme" {
		t.Errorf("List() missing or wrong entry for dirB: %+v", byPath[mB.Path])
	}
}

// TestRecordInIndexCreatesTheParentDirectory matches every other
// ~/.timetattle/<file> writer in this codebase (store.Open, snapshot.Take):
// a fresh machine has no ~/.timetattle yet, and the first call must not
// require it to already exist.
func TestRecordInIndexCreatesTheParentDirectory(t *testing.T) {
	idx := filepath.Join(t.TempDir(), "nested", "does-not-exist-yet", "registered-directories.json")
	dir := t.TempDir()
	if err := registry.RecordInIndex(idx, dir); err != nil {
		t.Fatalf("RecordInIndex() with a missing parent = %v", err)
	}
	if _, err := os.Stat(idx); err != nil {
		t.Errorf("RecordInIndex() did not create %s: %v", idx, err)
	}
}

// TestListSurfacesAnErrorFromAMarkerItCannotParseButStillReturnsTheRest
// checks List does not let one bad entry silence every other one — the same
// partial-failure posture internal/manifest's LoadAll and this command's own
// ingest() already take — while still surfacing the problem rather than
// swallowing it, per this project's standing rule against collapsing "read
// and failed" into "not registered".
func TestListSurfacesAnErrorFromAMarkerItCannotParseButStillReturnsTheRest(t *testing.T) {
	good := t.TempDir()
	mGood, err := registry.Register(registry.Registration{Dir: good, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register(good) = %v", err)
	}

	bad := t.TempDir()
	badMarker := map[string]any{
		"marker_version": 999,
		"client":         "Broken",
		"project":        "Broken",
		"registered":     "2026-07-31T00:00:00Z",
	}
	data, err := json.Marshal(badMarker)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(bad, registry.MarkerFilename), data, 0o644); err != nil {
		t.Fatalf("write fixture marker: %v", err)
	}

	idx := filepath.Join(t.TempDir(), "registered-directories.json")
	if err := registry.RecordInIndex(idx, mGood.Path); err != nil {
		t.Fatalf("RecordInIndex(good) = %v", err)
	}
	if err := registry.RecordInIndex(idx, bad); err != nil {
		t.Fatalf("RecordInIndex(bad) = %v", err)
	}

	list, err := registry.List(idx)
	if err == nil {
		t.Error("List() with an unparseable marker in the index = nil error, want one naming the problem")
	}
	if len(list) != 1 || list[0].Path != mGood.Path {
		t.Errorf("List() = %+v, want the one good entry to survive the other's failure", list)
	}
}
