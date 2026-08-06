//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package registry_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/registry"
)

// The RULE these two confirm is already tested portably: internal_test.go
// overrides cacheSourceRoots with a directory under t.TempDir() and exercises
// every branch of blessedCacheConflict on it, so tagging these out costs no
// coverage of the logic. What they add is that the SHIPPED manifest's root, on
// a machine where it actually EXISTS, is one of the directories the rule
// refuses — and no fixture can make ~/Movies/Motion Projects/Autosave Vault
// exist.

func TestRegisterRefusesToBlessADeclaredCacheDirectory(t *testing.T) {
	root := motionVaultRoot(t)
	if _, err := os.Stat(root); err != nil {
		t.Skipf("motion vault %s is not present/readable on this machine: %v", root, err)
	}
	_, err := registry.Register(registry.Registration{Dir: root, Client: "ACORN", Project: "Anything"})
	if err == nil {
		t.Fatal("Register() against the Motion autosave vault = nil error, want a refusal")
	}
}

func TestRegisterRefusesToBlessAnAncestorOfADeclaredCache(t *testing.T) {
	root := motionVaultRoot(t)
	parent := filepath.Dir(root)
	if _, err := os.Stat(parent); err != nil {
		t.Skipf("%s is not present/readable on this machine: %v", parent, err)
	}
	_, err := registry.Register(registry.Registration{Dir: parent, Client: "ACORN", Project: "Anything"})
	if err == nil {
		t.Fatal("Register() against the Motion vault's parent = nil error, want a refusal; " +
			"resolution would walk up from inside the vault and find this marker")
	}
}
