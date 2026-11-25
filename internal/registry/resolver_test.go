package registry_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/registry"
)

// askLog records every question a Resolver actually put to the filesystem.
type askLog map[registry.Lookup]map[string]int

func (a askLog) observe(kind registry.Lookup, path string, _ time.Duration) {
	if a[kind] == nil {
		a[kind] = map[string]int{}
	}
	a[kind][path]++
}

func (a askLog) total(kind registry.Lookup) int {
	n := 0
	for _, c := range a[kind] {
		n += c
	}
	return n
}

func TestResolverReportsEveryKindOfLookup(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "plain")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	asked := askLog{}
	r := &registry.Resolver{Observe: asked.observe}
	if !r.IsDir(dir) {
		t.Fatalf("IsDir(%s) = false, want true", dir)
	}
	if _, found, err := r.ResolveAmong(dir, dir, nil); err != nil || found {
		t.Fatalf("ResolveAmong = %v, %v, want unattributed", found, err)
	}

	for _, kind := range []registry.Lookup{
		registry.LookupStat, registry.LookupMarker, registry.LookupPhysicalPath, registry.LookupGitRoot,
	} {
		if asked.total(kind) == 0 {
			t.Errorf("no %s lookup was observed", kind)
		}
	}
}
