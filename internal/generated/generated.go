// Package generated says which stored observations are of files a tool wrote:
// build output, installed dependencies and caches.
//
// The filesystem walk skips such a directory, which governs only the next
// harvest, and the store has no delete, so every reader applies this to what is
// already stored. It is a package of its own because the walker, the readers
// and the store all need it and the walker's tests import the store.
package generated

import (
	"path/filepath"
	"slices"
	"strings"

	"github.com/bensyverson/timetattle/internal/source"
)

// Dirs names the directories whose contents a tool wrote: Swift Package
// Manager's output, Xcode derived data kept under ".derived", a Python
// virtualenv, and Python's bytecode and tool caches. Each is matched whole.
var Dirs = []string{
	".build",
	".derived",
	".venv",
	"__pycache__",
	".mypy_cache",
	".pytest_cache",
	".ruff_cache",
}

// Source is the filesystem observation source, the only one the rule applies
// to: another source's locator under one of Dirs records where somebody was
// working, not a file a tool wrote.
const Source = "fs-observations"

// components matches each of Dirs only where it has something beneath it, so a
// file with such a name is not generated.
var components = func() []string {
	out := make([]string, len(Dirs))
	for i, dir := range Dirs {
		out[i] = string(filepath.Separator) + dir + string(filepath.Separator)
	}
	return out
}()

// Globs is Is's path test as SQLite GLOB patterns, any one of which matching is
// enough, for a query that cannot call it. Like Is, they apply only to rows
// whose source is Source.
func Globs() []string {
	out := make([]string, len(components))
	for i, c := range components {
		out[i] = "*" + c + "*"
	}
	return out
}

// Is reports whether e observes a file a tool wrote.
func Is(e source.Event) bool {
	return e.Source == Source && slices.ContainsFunc(components, func(c string) bool {
		return strings.Contains(e.Locator, c)
	})
}
