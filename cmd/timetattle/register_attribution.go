package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"

	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/store"
)

// attributeRegistration attributes the evidence already stored under a
// directory that was just registered, so that a report shows it without
// waiting for a harvest's full pass, and says how much it attributed.
//
// It asks the same resolver the full pass asks, so a marker nested inside the
// directory keeps its own evidence and the two passes cannot disagree. It
// covers only what the directory's own spellings name: evidence spelled
// through some other symlink into it, or recorded in a git worktree outside it,
// is left for the full pass.
//
// Like recordClientAssertion it never creates a store that was not asked for.
func attributeRegistration(ctx context.Context, storePath, indexPath string, record indexRecord,
	m *registry.Marker, out io.Writer) error {
	if record == markerNotIndexed {
		m = unindexed(m)
	}
	explicit := storePath != ""
	if !explicit {
		var err error
		if storePath, err = store.DefaultPath(); err != nil {
			return err
		}
		if _, err := os.Stat(storePath); errors.Is(err, fs.ErrNotExist) {
			fmt.Fprintln(out, "attribution: there is no event store yet, so there is no stored evidence to attribute")
			return nil
		}
	}
	st, err := store.Open(storePath)
	if err != nil {
		return err
	}
	defer st.Close()

	registered := registeredWith(indexPath, m)
	att, err := st.ResolveProjectsUnder(ctx, spellingsOf(m), passResolver(&registry.Resolver{}, registered))
	if err != nil {
		return err
	}

	others := 0
	for project, n := range att.Events {
		if project != m.Project {
			others += n
		}
	}
	fmt.Fprintf(out, "attribution: attributed %s to %s", plural(att.Events[m.Project], "stored observation"), m.Project)
	if others > 0 {
		fmt.Fprintf(out, ", and %d to another project registered more specifically", others)
	}
	fmt.Fprintln(out)
	for _, s := range att.Skips {
		fmt.Fprintf(out, "  unreadable: %s (%s): %v\n", s.Locator, plural(s.Events, "event"), s.Err)
	}
	return nil
}

// indexRecord is whether the registry index holds a marker, which decides how
// much of it a harvest can see.
type indexRecord int

const (
	markerIndexed indexRecord = iota
	markerNotIndexed
)

// unindexed is m as a harvest sees it when the index does not hold it. The pass
// still finds the marker by climbing from a path, but reads former paths only
// through the index, so applying them here would be undone by the next harvest.
func unindexed(m *registry.Marker) *registry.Marker {
	climbable := *m
	climbable.FormerPaths = nil
	return &climbable
}

// spellingsOf is every path under which stored evidence may name m's
// directory: as registered, with its symlinks resolved, and wherever it is
// declared to have been before.
func spellingsOf(m *registry.Marker) []string {
	return append(liveSpellingsOf(m), m.FormerPaths...)
}

// liveSpellingsOf is the spellings of where m's directory is now.
func liveSpellingsOf(m *registry.Marker) []string {
	dirs := []string{m.Path}
	if physical, err := filepath.EvalSymlinks(m.Path); err == nil && physical != m.Path {
		dirs = append(dirs, physical)
	}
	return dirs
}

// registeredWith is the registry a harvest would resolve against, with m in it
// whether or not the index could record it. An unreadable entry has already
// been reported by the registration itself.
func registeredWith(indexPath string, m *registry.Marker) []*registry.Marker {
	var registered []*registry.Marker
	if indexPath != "" {
		registered, _ = registry.List(indexPath)
	}
	registered = slices.DeleteFunc(registered, func(r *registry.Marker) bool { return r.Path == m.Path })
	return append(registered, m)
}
