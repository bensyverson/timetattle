package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bensyverson/timetattle/internal/registry"
)

// workingScope is what a directory resolves to for scoping a report to it: a
// single client, a single project, or neither. `metrics` is its first
// caller; the dashboard command (2026-08-07 CLI overhaul plan) is meant to
// share it, which is why it lives in its own file rather than metrics.go.
type workingScope struct {
	Client  string
	Project string
}

// resolveWorkingScope resolves dir to the client or project a report should
// be restricted to, by climbing the registry the way the bare command
// (headline.go) already does and, failing that, looking BENEATH dir for
// registered projects that agree on a client.
//
// # Two ways a directory scopes, and the nearest answer always wins
//
// dir at or below a registered project — registry.Resolve climbs to the
// nearest marker, nearest first — scopes to that PROJECT alone. Client is
// left blank on purpose: metrics already narrows the client roll-up to
// whichever client the report's OWN client-project log currently credits the
// project to (metricsScope.showsClient, given only a Project). The marker's
// Client field is provenance from whenever the directory was blessed and is
// deliberately not read here, for the reason headline.go's headlineClient
// documents at length — THE MARKER IS NOT THE AUTHORITY, and can be stale
// after a reassignment recorded with `register -since`.
//
// dir ABOVE one or more registered projects that all name the same client —
// registry.List enumerates every registered directory; clientBeneath walks
// it for entries dir is an ancestor of — scopes to that CLIENT. More than one
// client beneath dir is refused rather than guessed at; see clientBeneath.
//
// Because registry.Resolve is tried FIRST and returns as soon as it finds
// anything, a directory that is both inside a registered project AND an
// ancestor of some OTHER project's directory resolves to the nearer,
// enclosing project and never looks at what sits beneath it. That nesting is
// not the impossible case it might look like: registry.Register's only
// cross-directory refusal is two directories naming the SAME project for
// different clients (Register's projectClientConflict), which a different
// project name nested inside another client's directory never triggers — see
// TestNestingADifferentClientsProjectInsideAnotherIsNotRefused.
func resolveWorkingScope(dir, indexPath string) (workingScope, error) {
	marker, found, err := registry.Resolve(dir)
	if err != nil {
		return workingScope{}, fmt.Errorf("resolve %s: %w", dir, err)
	}
	if found {
		return workingScope{Project: marker.Project}, nil
	}

	registered, err := registry.List(indexPath)
	if err != nil {
		return workingScope{}, fmt.Errorf("read the registry: %w", err)
	}
	if client, ok := clientBeneath(dir, registered); ok {
		return workingScope{Client: client}, nil
	}
	return workingScope{}, fmt.Errorf("%s is not registered, and nothing under it is either — "+
		"`timetattle register` blesses it, or pass -all to report every client and project", dir)
}

// clientBeneath reports the one client every registered directory beneath dir
// agrees on, deciding by the MARKER'S CLIENT FIELD — the identity the
// directory was blessed with — rather than anything about dir's own name. A
// directory named for a client that has registered nothing beneath it is not
// a client directory, and one named "clients" holding one client's projects
// is.
//
// Two or more distinct clients beneath dir is reported as no client at all,
// not as a guess: it means dir is a workspace root spanning several clients'
// work, and picking whichever sorts first would be exactly the silent
// wrong-attribution this codebase's rules warn against elsewhere.
func clientBeneath(dir string, registered []*registry.Marker) (client string, ok bool) {
	dir = filepath.Clean(dir)
	for _, m := range registered {
		if m == nil || !isBeneath(dir, m.Path) {
			continue
		}
		if !ok {
			client, ok = m.Client, true
			continue
		}
		if m.Client != client {
			return "", false
		}
	}
	return client, ok
}

// workingDirectoryScope resolves the process's actual working directory to a
// scope, defaulting indexPath to registry.DefaultIndexPath when the caller
// did not pass one.
//
// It is the entry point metricsCmd (and, later, dashboard) calls.
// resolveWorkingScope itself takes a directory explicitly rather than reading
// os.Getwd() on its own, so every test above can pin its behaviour against a
// directory it built, without touching the test binary's real cwd — which is
// this worktree, unregistered, and must never silently decide what a test
// asserts.
func workingDirectoryScope(indexPath string) (workingScope, error) {
	if indexPath == "" {
		var err error
		if indexPath, err = registry.DefaultIndexPath(); err != nil {
			return workingScope{}, err
		}
	}
	dir, err := os.Getwd()
	if err != nil {
		return workingScope{}, err
	}
	return resolveWorkingScope(dir, indexPath)
}

// isBeneath reports whether path is strictly inside dir. dir itself does not
// count — resolving what dir names for ITSELF is registry.Resolve's job (via
// climb), and resolveWorkingScope only reaches clientBeneath after Resolve has
// already said no for dir and everything above it.
//
// The separator is appended only when dir does not already end in one, the
// same guard registry's own isAncestorOrSame carries and for the same
// reason: filepath.Clean("/") is "/", and the unconditional form asks whether
// path starts with "//", which nothing does, so the filesystem root would
// silently contain nothing.
func isBeneath(dir, path string) bool {
	dir = filepath.Clean(dir)
	path = filepath.Clean(path)
	if dir == path {
		return false
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(dir, sep) {
		dir += sep
	}
	return strings.HasPrefix(path, dir)
}
