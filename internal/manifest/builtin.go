package manifest

import (
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// builtinFS holds the manifests that ship inside the binary.
//
// Embedding them rather than installing them into a config directory means a
// fresh machine collects Motion and Blender evidence with nothing to set up,
// and there is no half-installed state where the binary and the files on disk
// disagree about what a built-in says.
//
//go:embed builtin/*.json
var builtinFS embed.FS

// builtinDir is where the embedded manifests live inside builtinFS.
const builtinDir = "builtin"

// userSubdir is where a user's own manifests go, under the home directory.
//
// ~/.timetattle already holds the database, config.toml and snapshots, so this
// is a directory inside it rather than a new location. It is a *directory*
// rather than a key in config.toml because adding a source should mean
// dropping in a file and editing nothing — the same property the Go route has,
// and the reason a manifest is worth having at all. Files are read in name
// order, so prefixing them (10-work.json, 20-personal.json) fixes which one
// wins when two claim the same id.
var userSubdir = filepath.Join(".timetattle", "manifests")

// Builtin returns the sources Timetattle ships with, validated.
//
// These go through the same Load path as anything on disk. A built-in that
// failed validation would be a build-time mistake rather than a user's, so it
// is reported as an error and the sources that did validate are still
// returned — the tests in this package are what stop one reaching a release.
func Builtin() ([]Source, error) {
	entries, err := builtinFS.ReadDir(builtinDir)
	if err != nil {
		return nil, fmt.Errorf("read built-in manifests: %w", err)
	}

	var (
		sources []Source
		errs    []error
	)
	// embed.FS returns entries sorted by name, which is what makes the order
	// of the built-in sources a property of the repository rather than of
	// whatever order the files happened to be embedded in.
	for _, e := range entries {
		name := path.Join(builtinDir, e.Name())
		data, err := builtinFS.ReadFile(name)
		if err != nil {
			errs = append(errs, fmt.Errorf("read built-in manifest %s: %w", name, err))
			continue
		}
		m, err := parse(name, data)
		if err != nil {
			errs = append(errs, err)
		}
		if m == nil {
			continue
		}
		sources = layer(sources, m.Sources)
	}
	return sources, errors.Join(errs...)
}

// UserDir is where this machine's own manifests are read from.
func UserDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate the manifest directory: %w", err)
	}
	return filepath.Join(home, userSubdir), nil
}

// LoadAll returns the built-in sources with the user's layered over them.
//
// An empty userDir means UserDir, so a caller cannot quietly end up with
// built-ins only by forgetting to pass anything.
//
// # What layering means
//
// A user source sharing an id with a built-in **replaces it entirely**, and
// keeps its position so the collection order does not shuffle. Fields are not
// merged. A merge would mean that reading an override told you only half of
// what the source did, with the other half living in a binary you cannot open
// — and the half you could not see would change under you on the next release.
// A source with an id nobody has used is appended.
//
// # What is tolerated
//
// A manifest directory that does not exist is the normal state for anyone who
// has not written one, and is not an error. A manifest that fails to parse is
// reported, and every other manifest — including all the built-ins — still
// loads. Manifests are hand-edited, and one typo costing a whole harvest would
// be the same permanent data loss the harvester exists to prevent.
func LoadAll(userDir string) ([]Source, error) {
	sources, err := Builtin()
	var errs []error
	if err != nil {
		errs = append(errs, err)
	}

	if userDir == "" {
		userDir, err = UserDir()
		if err != nil {
			return sources, errors.Join(append(errs, err)...)
		}
	}

	// os.ReadDir sorts by filename, which is what makes layering deterministic
	// across machines; directory order is not a promise any filesystem makes.
	entries, err := os.ReadDir(userDir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf("read manifest directory %s: %w", userDir, err))
		}
		return sources, errors.Join(errs...)
	}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Only .json, so a README, an editor's .bak or a .DS_Store sitting in
		// the directory is not reported as a broken manifest every run.
		if !strings.EqualFold(filepath.Ext(e.Name()), ".json") {
			continue
		}
		m, err := Load(filepath.Join(userDir, e.Name()))
		if err != nil {
			errs = append(errs, err)
		}
		if m == nil {
			continue
		}
		sources = layer(sources, m.Sources)
	}
	return sources, errors.Join(errs...)
}

// layer applies over on top of base by id: a shared id replaces the earlier
// entry where it stands, and a new one is appended.
func layer(base, over []Source) []Source {
	for _, s := range over {
		replaced := false
		for i := range base {
			if base[i].ID == s.ID {
				base[i] = s
				replaced = true
				break
			}
		}
		if !replaced {
			base = append(base, s)
		}
	}
	return base
}
