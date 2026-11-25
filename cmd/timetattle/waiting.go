package main

import (
	"fmt"
	"io"
	"path/filepath"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
)

// waitingEvidence is what a scoped report cannot count yet: observations the
// store holds from under the scope's own registered directories that no
// attribution pass has reached. Without naming them a report prints the same
// zeros for a project still waiting on a harvest as for one with no work in it.
type waitingEvidence struct {
	observations int
	scope        metricsScope
}

func (w waitingEvidence) any() bool { return w.observations > 0 }

func (w waitingEvidence) print(out io.Writer) {
	if !w.any() {
		return
	}
	fmt.Fprintf(out, "\nNOT YET ATTRIBUTED: %s under %s\n"+
		"  Observed under its registered directories and carrying no project yet, so\n"+
		"  no figure here counts them. `timetattle harvest` attributes them.\n",
		plural(w.observations, "stored observation"), w.scope.describe())
}

// evidenceWaiting counts the events that carry no project although the most
// specific registered directory above them belongs to scope. An unscoped report
// claims nothing about any one project, so for it nothing is waiting.
//
// It compares paths as text against the events a report has already read, so it
// costs no query and no pass over the filesystem; only the registered
// directories themselves are asked for their physical spelling.
func evidenceWaiting(events []source.Event, scope metricsScope, registered []*registry.Marker,
	cm clientmap.Map) waitingEvidence {
	w := waitingEvidence{scope: scope}
	if !scope.filtering() {
		return w
	}

	// Every registered directory, not only the scope's: one nested inside a
	// scoped directory owns what is under it.
	inScope := map[string]bool{}
	anyInScope := false
	for _, m := range registered {
		client := m.Client
		if logged, ok := cm.At(m.Project, time.Now()); ok {
			client = logged
		}
		mine := (scope.Project == "" || scope.Project == m.Project) &&
			(scope.Client == "" || scope.Client == client)
		anyInScope = anyInScope || mine
		for _, dir := range spellingsOf(m) {
			inScope[filepath.Clean(dir)] = mine
		}
	}
	if !anyInScope {
		return w
	}

	// Many events share a locator, so each is placed once.
	placed := map[string]bool{}
	for _, e := range events {
		if e.Identity.Project != "" || e.Locator == "" {
			continue
		}
		mine, known := placed[e.Locator]
		if !known {
			mine = nearestIn(inScope, e.Locator)
			placed[e.Locator] = mine
		}
		if mine {
			w.observations++
		}
	}
	return w
}

// nearestIn is the value dirs holds for the closest directory at or above path,
// and false when none of them is there.
func nearestIn(dirs map[string]bool, path string) bool {
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if value, ok := dirs[dir]; ok {
			return value
		}
		if dir == filepath.Dir(dir) {
			return false
		}
	}
}

// registeredForWaiting is the registry a report checks for waiting evidence,
// from indexPath or the default index. The check only adds a notice, so a
// registry that cannot be read narrows it rather than failing the report.
func registeredForWaiting(indexPath string) []*registry.Marker {
	if indexPath == "" {
		var err error
		if indexPath, err = registry.DefaultIndexPath(); err != nil {
			return nil
		}
	}
	registered, _ := registry.List(indexPath)
	return registered
}
