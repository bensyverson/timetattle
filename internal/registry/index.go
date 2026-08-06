package registry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// indexVersion is the schema RecordInIndex writes and List understands, for
// the same reason Version exists for a marker: this file is read back by a
// different build than the one that wrote it, on the same machine, whenever
// somebody upgrades between running register and running harvest.
const indexVersion = 1

// index is the on-disk shape of the file RecordInIndex maintains.
//
// It deliberately holds nothing but a list of directories — no client, no
// project, no timestamp. Everything else about a registration lives in the
// marker inside that directory, which is the one place this package treats
// as a fact rather than a note pointing at one; see List.
type index struct {
	Version     int      `json:"index_version"`
	Directories []string `json:"directories"`
}

// DefaultIndexPath is where RecordInIndex and List keep track of every
// directory that has ever been blessed, by default — following the same
// `~/.timetattle/<name>` convention as store.DefaultPath, snapshot.DefaultDir
// and filedays.DefaultRootsFile.
func DefaultIndexPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".timetattle", "registered-directories.json"), nil
}

// RecordInIndex adds dir to the index at indexPath, so a later List(indexPath)
// can find it. It is idempotent: recording the same directory twice leaves
// the index with one entry, not two.
//
// This is a separate function from Register, not a step Register takes on
// every call, deliberately: Register is settled, widely called code —
// including by every test in this package, none of which isolate HOME — and
// folding an unconditional write to a real, machine-global path into it
// would make every one of those calls touch the real machine's
// ~/.timetattle the moment a test runs, exactly the pollution
// TestTheStoreFlagRedirectsWrites (cmd/timetattle) exists to catch for the
// event store. Keeping index maintenance opt-in, called explicitly by
// whoever wants it recorded, means the settled Register/Resolve pair is
// touched not at all.
func RecordInIndex(indexPath, dir string) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("record %s in the registry index: %w", dir, err)
	}
	abs = filepath.Clean(abs)

	idx, err := readIndex(indexPath)
	if err != nil {
		return err
	}

	if slices.Contains(idx.Directories, abs) {
		return nil
	}
	idx.Directories = append(idx.Directories, abs)
	sort.Strings(idx.Directories)

	if err := os.MkdirAll(filepath.Dir(indexPath), 0o755); err != nil {
		return fmt.Errorf("record %s in the registry index: %w", abs, err)
	}
	data, err := json.MarshalIndent(idx, "", "  ")
	if err != nil {
		return fmt.Errorf("record %s in the registry index: %w", abs, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(indexPath, data, 0o644); err != nil {
		return fmt.Errorf("record %s in the registry index: %w", abs, err)
	}
	return nil
}

// List reports every directory currently registered, by re-reading the
// marker at each directory the index at indexPath names.
//
// It does not trust the index at face value. The index only ever grows —
// RecordInIndex has no matching remove — so an entry can point at a
// directory whose marker was deleted, moved, or corrected to a different
// client and project since it was indexed, and the index has no way to know
// that on its own. Re-verifying against the actual marker on every call is
// what makes "removing or correcting a registration is reflected the next
// time the list is read, not cached stale" true: the index is a hint of
// where to look, and the marker inside the directory — exactly as Resolve
// already treats it — is the only fact. A directory that no longer carries a
// valid marker is silently excluded, the same tolerance Resolve gives a path
// that resolves to nothing: most of the filesystem is not registered, and an
// index entry surviving its marker's deletion is not itself an error.
//
// A marker that IS still there but fails to parse — a version this build
// does not understand, corrupted JSON — is different: that is a directory
// genuinely in an unknown state, not observed absent, and per this
// project's standing rule against collapsing those two, it is reported
// rather than silently dropped. It costs only that one entry; every other
// directory in the index is still read and returned.
func List(indexPath string) ([]*Marker, error) {
	idx, err := readIndex(indexPath)
	if err != nil {
		return nil, err
	}

	var (
		out  []*Marker
		errs []error
	)
	for _, dir := range idx.Directories {
		m, found, err := readMarker(dir)
		if err != nil {
			errs = append(errs, fmt.Errorf("list %s: %w", dir, err))
			continue
		}
		if !found {
			continue
		}
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, errors.Join(errs...)
}

// readIndex reads the index at path, treating a missing file as an empty
// index rather than an error — a fresh machine that has never registered
// anything is the ordinary case, the same tolerance every other
// ~/.timetattle/<file> reader in this codebase gives its own missing file.
func readIndex(path string) (*index, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &index{Version: indexVersion}, nil
		}
		return nil, fmt.Errorf("read registry index %s: %w", path, err)
	}
	var idx index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parse registry index %s: %w", path, err)
	}
	if idx.Version != indexVersion {
		return nil, fmt.Errorf("registry index %s declares index_version %d, but this build understands %d",
			path, idx.Version, indexVersion)
	}
	return &idx, nil
}
