// Package buildoutput says which stored observations are of files a build
// generated.
//
// The filesystem walk skips such a directory, which governs only the next
// harvest, and the store has no delete, so every reader applies this to what is
// already stored. It is a package of its own because the walker, the readers
// and the store all need it and the walker's tests import the store.
package buildoutput

import (
	"path/filepath"
	"strings"

	"github.com/bensyverson/timetattle/internal/source"
)

// Dir is where Swift Package Manager writes everything it builds, dependency
// checkouts included.
const Dir = ".build"

// Source is the filesystem observation source, the only one the rule applies
// to: another source's locator under Dir records where somebody was working,
// not a file a compiler wrote.
const Source = "fs-observations"

// component matches Dir only where it has something beneath it, so a file
// named ".build" is not build output.
const component = string(filepath.Separator) + Dir + string(filepath.Separator)

// Glob is Is's path test as a SQLite GLOB pattern, for a query that cannot call
// it. Like Is, it applies only to rows whose source is Source.
const Glob = "*" + component + "*"

// Is reports whether e observes a file a build generated.
func Is(e source.Event) bool {
	return e.Source == Source && strings.Contains(e.Locator, component)
}
