package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
)

// The pass's resolver, not only the registry underneath it, has to share one
// cache across locators.
func TestAttributionPassReadsOneMarkerForManyLocatorsInOneDirectory(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "renders")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.Register(registry.Registration{Dir: root, Client: "acme", Project: "holiday"}); err != nil {
		t.Fatal(err)
	}

	reads := map[string]int{}
	pass := passResolver(&registry.Resolver{Observe: func(kind registry.Lookup, path string, _ time.Duration) {
		if kind == registry.LookupMarker {
			reads[path]++
		}
	}}, nil)
	for _, name := range []string{"a.motn", "b.motn", "c.motn"} {
		id, found, err := pass(filepath.Join(dir, name))
		if err != nil || !found || id.Project != "holiday" || id.Client != "acme" {
			t.Fatalf("pass(%s) = %+v, %v, %v, want acme/holiday", name, id, found, err)
		}
	}
	if reads[dir] != 1 {
		t.Errorf("the shared directory's marker was read %d times, want 1", reads[dir])
	}
}
