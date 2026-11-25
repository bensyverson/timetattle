package registry

import (
	"os"
	"path/filepath"
	"time"
)

// Lookup is a kind of question a Resolver puts to the filesystem.
type Lookup int

const (
	// LookupStat asks whether a path is a directory.
	LookupStat Lookup = iota
	// LookupMarker reads the marker in one directory.
	LookupMarker
	// LookupPhysicalPath follows the symlinks in a path.
	LookupPhysicalPath
	// LookupGitRoot runs git to find a directory's common repository root.
	LookupGitRoot
)

func (l Lookup) String() string {
	switch l {
	case LookupStat:
		return "stat"
	case LookupMarker:
		return "marker read"
	case LookupPhysicalPath:
		return "physical path"
	case LookupGitRoot:
		return "git rev-parse"
	}
	return "unknown lookup"
}

// Caching says what a Resolver remembers between questions.
type Caching int

const (
	// CachePerResolver puts each question about a path to the filesystem once
	// for the life of the Resolver and answers from memory afterwards.
	CachePerResolver Caching = iota
	// NoCache asks every time, and takes none of the shortcuts a remembered
	// answer allows. It is the baseline a measurement or a test compares
	// against; the two modes give the same answers over an unchanging tree.
	NoCache
)

// Resolver answers the questions Resolve and ResolveAmong answer, for a caller
// asking many of them: many locators share a directory, and nearly all share
// their ancestors.
//
// It remembers what it saw, failures included, so a directory that could not be
// read stays unreadable rather than turning into one with no marker. Nothing
// tells it the filesystem has changed, so it belongs to one pass and is never
// persisted. Answers are kept per spelling of a path, never merged across
// symlinks: two spellings of one directory have different ancestors, and so can
// resolve differently.
//
// A Marker it returns may be returned again and must not be modified. The zero
// value is ready to use. It is not safe for concurrent use.
type Resolver struct {
	// Caching is CachePerResolver unless set otherwise.
	Caching Caching
	// Observe, when set, is told of every question actually put to the
	// filesystem and how long it took. The path can name a client, so a caller
	// that prints what it observes must print counts and durations only.
	Observe func(kind Lookup, path string, took time.Duration)

	dirs     map[string]bool
	markers  map[string]markerAnswer
	physical map[string]physicalAnswer
	gitRoots map[string]gitAnswer
}

type markerAnswer struct {
	marker *Marker
	found  bool
	err    error
}

type physicalAnswer struct {
	path string
	err  error
}

type gitAnswer struct {
	root string
	ok   bool
}

// ask answers from known when r remembers the path, and otherwise puts the
// question to the filesystem.
func ask[T any](r *Resolver, kind Lookup, known *map[string]T, path string, question func(string) T) T {
	if answer, ok := (*known)[path]; ok {
		return answer
	}
	start := time.Now()
	answer := question(path)
	if r.Observe != nil {
		r.Observe(kind, path, time.Since(start))
	}
	if r.Caching == CachePerResolver {
		if *known == nil {
			*known = map[string]T{}
		}
		(*known)[path] = answer
	}
	return answer
}

// IsDir reports whether path is a directory that can be seen. A path that
// cannot be stat'ed reports false.
func (r *Resolver) IsDir(path string) bool {
	return ask(r, LookupStat, &r.dirs, path, func(p string) bool {
		info, err := os.Stat(p)
		return err == nil && info.IsDir()
	})
}

func (r *Resolver) marker(dir string) (*Marker, bool, error) {
	a := ask(r, LookupMarker, &r.markers, dir, func(d string) markerAnswer {
		m, found, err := readMarker(d)
		return markerAnswer{m, found, err}
	})
	return a.marker, a.found, a.err
}

func (r *Resolver) physicalPath(dir string) (string, error) {
	a := ask(r, LookupPhysicalPath, &r.physical, dir, func(d string) physicalAnswer {
		path, err := filepath.EvalSymlinks(d)
		return physicalAnswer{path, err}
	})
	return a.path, a.err
}

func (r *Resolver) gitRoot(dir string) (string, bool) {
	// git -C fails on anything it cannot change into, so a vanished directory
	// is answered without spawning it. Most unmatched locators name one.
	if r.Caching == CachePerResolver && !r.IsDir(dir) {
		return "", false
	}
	a := ask(r, LookupGitRoot, &r.gitRoots, dir, func(d string) gitAnswer {
		root, ok := gitCommonRoot(d)
		return gitAnswer{root, ok}
	})
	return a.root, a.ok
}
