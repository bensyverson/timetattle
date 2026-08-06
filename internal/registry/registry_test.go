package registry_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
)

func TestRegisterWritesAMarkerFile(t *testing.T) {
	dir := t.TempDir()
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Fatalf("Register() = %+v, want client ACORN project Holiday Campaign", m)
	}
	if m.Registered.IsZero() {
		t.Error("Registered is zero, want a timestamp")
	}

	raw, err := os.ReadFile(filepath.Join(dir, registry.MarkerFilename))
	if err != nil {
		t.Fatalf("read marker file: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatalf("unmarshal marker file: %v", err)
	}
	want := map[string]bool{"marker_version": true, "client": true, "project": true, "registered": true}
	for k := range fields {
		if !want[k] {
			t.Errorf("marker file has unexpected field %q; only identity may be written here, never a contract value", k)
		}
	}
	for k := range want {
		if _, ok := fields[k]; !ok {
			t.Errorf("marker file is missing field %q", k)
		}
	}
}

func TestRegisterRejectsEmptyClient(t *testing.T) {
	_, err := registry.Register(registry.Registration{Dir: t.TempDir(), Client: "", Project: "x"})
	if err == nil {
		t.Fatal("Register() with empty client = nil error, want one")
	}
}

func TestRegisterRejectsEmptyProject(t *testing.T) {
	_, err := registry.Register(registry.Registration{Dir: t.TempDir(), Client: "x", Project: ""})
	if err == nil {
		t.Fatal("Register() with empty project = nil error, want one")
	}
}

func TestRegisterRejectsAPathThatIsNotADirectory(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, err := registry.Register(registry.Registration{Dir: file, Client: "x", Project: "y"})
	if err == nil {
		t.Fatal("Register() against a file = nil error, want one")
	}
}

func TestRegisterIsIdempotentForTheSameClientAndProject(t *testing.T) {
	dir := t.TempDir()
	first, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("first Register() = %v", err)
	}
	time.Sleep(2 * time.Millisecond) // give a rewrite something to change, if it happens
	second, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"})
	if err != nil {
		t.Fatalf("second Register() = %v", err)
	}
	if !first.Registered.Equal(second.Registered) {
		t.Errorf("Registered changed from %s to %s on a re-registration with identical identity; it should be a no-op",
			first.Registered, second.Registered)
	}
}

func TestRegisterWithoutForceRefusesToReassignAnExistingProject(t *testing.T) {
	dir := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("first Register() = %v", err)
	}
	_, err := registry.Register(registry.Registration{Dir: dir, Client: "Acme", Project: "Rebrand"})
	if err == nil {
		t.Fatal("Register() over an existing different project without Force = nil error, want one")
	}

	m, found, err := registry.Resolve(dir)
	if err != nil || !found {
		t.Fatalf("Resolve() after refused re-registration = %v, %v, want the original marker still in place", found, err)
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Errorf("marker changed to %s / %s despite the refusal", m.Client, m.Project)
	}
}

func TestRegisterWithForceReassignsAnExistingProject(t *testing.T) {
	dir := t.TempDir()
	if _, err := registry.Register(registry.Registration{Dir: dir, Client: "ACORN", Project: "Holiday Campaign"}); err != nil {
		t.Fatalf("first Register() = %v", err)
	}
	m, err := registry.Register(registry.Registration{Dir: dir, Client: "Acme", Project: "Rebrand", Force: true})
	if err != nil {
		t.Fatalf("forced Register() = %v", err)
	}
	if m.Client != "Acme" || m.Project != "Rebrand" {
		t.Errorf("Register() with Force = %s / %s, want Acme / Rebrand", m.Client, m.Project)
	}
}
