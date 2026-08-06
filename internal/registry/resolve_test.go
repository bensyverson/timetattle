package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

func TestResolveFindsAMarkerFromAnyDescendantDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}

	m, found, err := registry.Resolve(nested)
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}
	if !found {
		t.Fatal("Resolve() found = false, want true")
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Errorf("Resolve() = %s / %s, want ACORN / Holiday Campaign", m.Client, m.Project)
	}
}

func TestResolveReturnsNotFoundForAPathInNoRegisteredDirectory(t *testing.T) {
	dir := t.TempDir()
	_, found, err := registry.Resolve(dir)
	if err != nil {
		t.Fatalf("Resolve() = %v, want a clean not-found rather than an error", err)
	}
	if found {
		t.Fatal("Resolve() found = true for a directory nothing registered, want false")
	}
}

func TestResolveReturnsAnErrorForACorruptMarker(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, registry.MarkerFilename), []byte("not json"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, _, err := registry.Resolve(dir)
	if err == nil {
		t.Fatal("Resolve() over a corrupt marker = nil error, want one; " +
			"a marker that cannot be read is unknown, not observed absent")
	}
}

func TestResolveReturnsAnErrorForAnUnsupportedMarkerVersion(t *testing.T) {
	dir := t.TempDir()
	body := `{"marker_version": 99, "client": "ACORN", "project": "Holiday Campaign"}`
	if err := os.WriteFile(filepath.Join(dir, registry.MarkerFilename), []byte(body), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, _, err := registry.Resolve(dir)
	if err == nil {
		t.Fatal("Resolve() over a marker with an unrecognised version = nil error, want one")
	}
}

func TestResolveInnerMarkerWinsOverAnOuterOne(t *testing.T) {
	outer := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: outer, Client: "Outer", Project: "P1"}); err != nil {
		t.Fatalf("Register(outer) = %v", err)
	}
	inner := filepath.Join(outer, "sub")
	if err := os.Mkdir(inner, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	if _, err := registry.Register(registry.Registration{Dir: inner, Client: "Inner", Project: "P2"}); err != nil {
		t.Fatalf("Register(inner) = %v", err)
	}

	m, found, err := registry.Resolve(filepath.Join(inner, "deeper"))
	if err != nil || !found {
		t.Fatalf("Resolve() = %v, %v, %v", m, found, err)
	}
	if m.Client != "Inner" || m.Project != "P2" {
		t.Errorf("Resolve() = %s / %s, want the nearer marker Inner / P2", m.Client, m.Project)
	}
}
