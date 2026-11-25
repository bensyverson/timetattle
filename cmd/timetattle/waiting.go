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
	// unchecked is why some registrations could not be read, so that finding
	// nothing under them is not taken for a check that found nothing.
	unchecked error
}

func (w waitingEvidence) any() bool { return w.observations > 0 }

func (w waitingEvidence) print(out io.Writer) {
	if w.any() {
		fmt.Fprintf(out, "\nNOT YET ATTRIBUTED: %s under %s\n"+
			"  Observed under its registered directories and carrying no project yet.\n"+
			"  `timetattle harvest` attributes them, or reports why it cannot.\n",
			plural(w.observations, "stored observation"), w.scope.describe())
	}
	if w.unchecked != nil {
		fmt.Fprintf(out, "\nSome registrations could not be read, so evidence waiting on attribution\n"+
			"may be missing or miscounted here: %v\n", w.unchecked)
	}
}

// evidenceWaiting counts the events that carry no project although the most
// specific registered directory above them belongs to scope. An unscoped report
// claims nothing about any one project, so for it nothing is waiting.
//
// The client is the one the client-project log names at each event's own
// instant, as it is for attributed events. A project the log has never heard of
// has the marker's, which is what a harvest will record for all of its history.
//
// It compares paths as text against the events a report has already read, so it
// costs no query and no pass over the filesystem; only the registered
// directories themselves are asked for their physical spelling.
func evidenceWaiting(events []source.Event, scope metricsScope, registered registeredDirs,
	cm clientmap.Map) waitingEvidence {
	if !scope.filtering() {
		return waitingEvidence{scope: scope}
	}
	w := waitingEvidence{scope: scope, unchecked: registered.unread}

	// Every registered directory, not only the scope's: one nested inside a
	// scoped directory owns what is under it.
	owners := map[string]*registry.Marker{}
	anyInScope := false
	for _, m := range registered.markers {
		anyInScope = anyInScope || scope.Project == "" || scope.Project == m.Project
		for _, dir := range spellingsOf(m) {
			owners[filepath.Clean(dir)] = m
		}
	}
	if !anyInScope {
		return w
	}

	// Many events share a locator, so each is placed once.
	placed := map[string]*registry.Marker{}
	for _, e := range events {
		if e.Identity.Project != "" || e.Locator == "" {
			continue
		}
		owner, known := placed[e.Locator]
		if !known {
			owner = nearestIn(owners, e.Locator)
			placed[e.Locator] = owner
		}
		if owner == nil || (scope.Project != "" && scope.Project != owner.Project) {
			continue
		}
		if scope.Client != "" && scope.Client != clientAt(owner, e.Anchor(), cm) {
			continue
		}
		w.observations++
	}
	return w
}

// clientAt is who m's project belonged to at the instant at, and "" for nobody.
func clientAt(m *registry.Marker, at time.Time, cm clientmap.Map) string {
	if !cm.Asserted(m.Project) {
		return m.Client
	}
	client, _ := cm.At(m.Project, at)
	return client
}

// nearestIn is the marker of the closest directory at or above path, and nil
// when none of them is registered.
func nearestIn(owners map[string]*registry.Marker, path string) *registry.Marker {
	for dir := filepath.Clean(path); ; dir = filepath.Dir(dir) {
		if m, ok := owners[dir]; ok {
			return m
		}
		if dir == filepath.Dir(dir) {
			return nil
		}
	}
}

// registeredDirs is the registry as far as a report could read it.
type registeredDirs struct {
	markers []*registry.Marker
	// unread is why the rest of it could not be read.
	unread error
}

// registeredForWaiting is the registry a report checks for waiting evidence,
// from indexPath or the default index. The check only adds a notice, so a
// registry that cannot be read is reported beside it rather than failing the
// report.
func registeredForWaiting(indexPath string) registeredDirs {
	if indexPath == "" {
		var err error
		if indexPath, err = registry.DefaultIndexPath(); err != nil {
			return registeredDirs{unread: err}
		}
	}
	markers, err := registry.List(indexPath)
	return registeredDirs{markers: markers, unread: err}
}
