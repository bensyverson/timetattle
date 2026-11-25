package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
)

// declareVanished registers dir to project and declares former as a path it
// used to have, which Consolidate only accepts while nothing is there.
func declareVanished(t *testing.T, dir, former, project string) *registry.Marker {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	registerScopeDir(t, "", dir, project+"-client", project)
	m, err := registry.Consolidate(registry.Consolidation{Dir: dir, Former: former,
		IndexPath: filepath.Join(t.TempDir(), "other.json")})
	if err != nil {
		t.Fatalf("Consolidate: %v", err)
	}
	return m
}

// waitingOwner is the one project whose report is told that the observation at
// locator is waiting, checked under both the project and the client scope.
func waitingOwner(t *testing.T, locator string, markers []*registry.Marker) string {
	t.Helper()
	events := []source.Event{{Locator: locator, Start: time.Now()}}
	var owners []string
	for _, m := range markers {
		for _, scope := range []metricsScope{{Project: m.Project}, {Client: m.Client}} {
			w := evidenceWaiting(events, scope, registeredDirs{markers: markers}, clientmap.Map{})
			if w.observations > 0 {
				owners = append(owners, m.Project)
			}
		}
	}
	if len(owners) != 2 || owners[0] != owners[1] {
		t.Fatalf("scopes told of the observation = %v, want one project under its project and client", owners)
	}
	return owners[0]
}

func harvestOwner(t *testing.T, locator string, markers []*registry.Marker) string {
	t.Helper()
	id, found, err := resolveLocator(locator, markers)
	if err != nil || !found {
		t.Fatalf("resolveLocator = %v, %v", found, err)
	}
	return id.Project
}

// A former path that has since been registered again belongs to whoever is
// registered there now, whichever order the registry lists them in.
func TestWaitingNoticeGivesAReusedFormerPathToItsLiveProject(t *testing.T) {
	root := t.TempDir()
	reused := filepath.Join(root, "alpha")
	legacy := declareVanished(t, filepath.Join(root, "zeta"), reused, "legacy")
	if err := os.Mkdir(reused, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fresh := registerScopeDir(t, "", reused, "fresh-client", "fresh")

	locator := filepath.Join(reused, "f.motn")
	for _, markers := range [][]*registry.Marker{{fresh, legacy}, {legacy, fresh}} {
		if got := harvestOwner(t, locator, markers); got != "fresh" {
			t.Fatalf("harvest attributes to %s, want fresh", got)
		}
		if got := waitingOwner(t, locator, markers); got != "fresh" {
			t.Errorf("notice counts it under %s, want fresh", got)
		}
	}
}

func TestWaitingNoticeBreaksAFormerPathTieAsAttributionDoes(t *testing.T) {
	root := t.TempDir()
	gone := filepath.Join(root, "gone")
	first := declareVanished(t, filepath.Join(root, "alpha"), gone, "first")
	second := declareVanished(t, filepath.Join(root, "zeta"), gone, "second")

	locator := filepath.Join(gone, "f.motn")
	for _, markers := range [][]*registry.Marker{{first, second}, {second, first}} {
		want := harvestOwner(t, locator, markers)
		if got := waitingOwner(t, locator, markers); got != want {
			t.Errorf("notice counts it under %s, harvest attributes to %s", got, want)
		}
	}
}
