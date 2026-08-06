package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

func TestRegisterCommandWritesAMarkerAtThePathFlag(t *testing.T) {
	dir := t.TempDir()
	if err := register([]string{"-client", "ACORN", "-project", "Holiday Campaign", "-path", dir}); err != nil {
		t.Fatalf("register() = %v", err)
	}

	m, found, err := registry.Resolve(dir)
	if err != nil || !found {
		t.Fatalf("Resolve(%s) = %v, %v, %v", dir, m, found, err)
	}
	if m.Client != "ACORN" || m.Project != "Holiday Campaign" {
		t.Errorf("Resolve(%s) = %s / %s, want ACORN / Holiday Campaign", dir, m.Client, m.Project)
	}
}

func TestRegisterCommandDefaultsPathToTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	if err := register([]string{"-client", "ACORN", "-project", "Holiday Campaign"}); err != nil {
		t.Fatalf("register() = %v", err)
	}

	m, found, err := registry.Resolve(dir)
	if err != nil || !found {
		t.Fatalf("Resolve(%s) = %v, %v, %v", dir, m, found, err)
	}
	if m.Client != "ACORN" {
		t.Errorf("Resolve(%s) client = %s, want ACORN", dir, m.Client)
	}
}

func TestRegisterCommandRequiresClient(t *testing.T) {
	err := register([]string{"-project", "Holiday Campaign", "-path", t.TempDir()})
	if err == nil {
		t.Fatal("register() with no -client = nil error, want one")
	}
}

func TestRegisterCommandRequiresProject(t *testing.T) {
	err := register([]string{"-client", "ACORN", "-path", t.TempDir()})
	if err == nil {
		t.Fatal("register() with no -project = nil error, want one")
	}
}

func TestRegisterCommandWithoutForceRefusesToReassign(t *testing.T) {
	dir := t.TempDir()
	if err := register([]string{"-client", "ACORN", "-project", "Holiday Campaign", "-path", dir}); err != nil {
		t.Fatalf("first register() = %v", err)
	}
	err := register([]string{"-client", "Acme", "-project", "Rebrand", "-path", dir})
	if err == nil {
		t.Fatal("register() over an existing different project without -force = nil error, want one")
	}
}

func TestRegisterCommandForceReassigns(t *testing.T) {
	dir := t.TempDir()
	if err := register([]string{"-client", "ACORN", "-project", "Holiday Campaign", "-path", dir}); err != nil {
		t.Fatalf("first register() = %v", err)
	}
	// -correction says the previous client was never right, which is the
	// simpler of the two meanings and the true one for a fixture with no time
	// dimension. -force alone no longer suffices for a CLIENT change: it says
	// "overwrite the marker" and says nothing about whether the earlier work
	// was done for the earlier client, and that cannot be guessed. See
	// register.go for the argument and TestReassigningAClientMustSayWhichKindOfChangeItIs
	// for the refusal.
	if err := register([]string{"-client", "Acme", "-project", "Rebrand", "-path", dir,
		"-force", "-correction"}); err != nil {
		t.Fatalf("forced register() = %v", err)
	}

	m, found, err := registry.Resolve(dir)
	if err != nil || !found {
		t.Fatalf("Resolve(%s) = %v, %v, %v", dir, m, found, err)
	}
	if m.Client != "Acme" || m.Project != "Rebrand" {
		t.Errorf("Resolve(%s) = %s / %s, want Acme / Rebrand", dir, m.Client, m.Project)
	}
}

func TestRegisterCommandResolvesARelativePathAgainstTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}
	t.Chdir(dir)

	if err := register([]string{"-client", "ACORN", "-project", "Holiday Campaign", "-path", "sub"}); err != nil {
		t.Fatalf("register() = %v", err)
	}

	m, found, err := registry.Resolve(sub)
	if err != nil || !found {
		t.Fatalf("Resolve(%s) = %v, %v, %v", sub, m, found, err)
	}
	if m.Client != "ACORN" {
		t.Errorf("Resolve(%s) client = %s, want ACORN", sub, m.Client)
	}
}

func TestRegisterCommandRejectsANonexistentDirectory(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist")
	if err := register([]string{"-client", "ACORN", "-project", "Holiday Campaign", "-path", missing}); err == nil {
		t.Fatal("register() against a directory that does not exist = nil error, want one")
	}
}
