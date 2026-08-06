package config

// This file resolves a PSEUDONYM — "acorn" and its siblings, the names
// //go:build realdata corpus checks and published project/ figures use — to
// the real absolute path on this machine that carries the data behind it.
//
// Why this exists: the confidentiality pass (project/2026-08-02-
// confidentiality-pass.md, task 5nCl4) renamed the reference client symbol
// in every fixture to a pseudonym, but nobody's disk changed name to match.
// Six published-figure tests in internal/chains and one in internal/fsobserve
// resolved $HOME/git/<real-name>, found nothing, and skipped — silently,
// because a skip is indistinguishable from a pass in `go test`'s own output.
// Task paDFg is the decision to resolve the pseudonym through this file
// rather than any of the alternatives that were rejected: pointing the tests
// back at the real name (puts the client symbol back in the repository),
// asking the mapping to live as a directory rename or symlink (reaches
// outside the repository into how the work is organised), or deleting the
// tests (discards the real-data cross-check CLAUDE.md requires sitting
// beside every constructed case).
//
// The mapping lives ONLY in ReferenceProjects, a field of this package's own
// Config, read from ~/.timetattle/config.json — outside the repository,
// exactly where every other machine-specific and sensitive value in this
// project already lives. The real name is never in a fixture, never in a
// comment, and never in anything this file writes to disk; it exists only
// in a value Ben typed into his own config file and in the runtime error a
// corpus test may choose to skip with.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
)

// ReferenceProjectErrorKind names which of three states
// ResolveReferenceProject observed. They are not interchangeable: each is a
// different missing precondition, and collapsing any two of them is the
// defect docs/content/docs/testing.md calls "absence of a row is not an
// observation" — a skip that says "the data is missing" when what is
// actually missing is a name mapping sends the next reader after a bug that
// is not there.
type ReferenceProjectErrorKind int

const (
	_ ReferenceProjectErrorKind = iota
	// ReferenceProjectNoConfigFile: nobody has written a config file on this
	// machine at all. There is nowhere a mapping could even be recorded.
	ReferenceProjectNoConfigFile
	// ReferenceProjectNoMapping: the config file exists and parses cleanly,
	// but it does not map this particular pseudonym to anything. It may map
	// others, or none at all; MappedCount says which.
	ReferenceProjectNoMapping
	// ReferenceProjectPathNotOnDisk: the config maps the pseudonym to a
	// path, but that path is not there. This is the one state that
	// legitimately reads like "the data is missing" — because here it is.
	ReferenceProjectPathNotOnDisk
)

// ReferenceProjectError is what ResolveReferenceProject and
// ResolveReferenceProjectAt return when they cannot produce a path. Its
// Error text names the state it observed and nothing else — never a
// conclusion drawn from it.
type ReferenceProjectError struct {
	Kind ReferenceProjectErrorKind
	// ConfigPath is the file that was, or was not, found.
	ConfigPath string
	// Pseudonym is the name that was asked for.
	Pseudonym string
	// MappedCount is how many pseudonyms the config DOES map, valid only
	// when Kind is ReferenceProjectNoMapping. It is what makes "none of them
	// acorn" a counted observation rather than an unsupported claim.
	MappedCount int
	// Path is the real path the config named, valid only when Kind is
	// ReferenceProjectPathNotOnDisk.
	Path string
	// Err is the stat failure behind ReferenceProjectNoConfigFile or
	// ReferenceProjectPathNotOnDisk. Nil for ReferenceProjectNoMapping,
	// which has no error to carry — the config was read successfully and
	// simply does not mention this name.
	Err error
}

func (e *ReferenceProjectError) Error() string {
	switch e.Kind {
	case ReferenceProjectNoConfigFile:
		return fmt.Sprintf("%s does not exist; no reference-project mapping is configured on this machine: %v",
			e.ConfigPath, e.Err)
	case ReferenceProjectNoMapping:
		if e.MappedCount == 0 {
			return fmt.Sprintf("%s exists but configures no reference-project mappings", e.ConfigPath)
		}
		return fmt.Sprintf("%s maps %d reference project(s), none of them %q", e.ConfigPath, e.MappedCount, e.Pseudonym)
	case ReferenceProjectPathNotOnDisk:
		return fmt.Sprintf("%s maps %q to %s, which is not present on this machine: %v",
			e.ConfigPath, e.Pseudonym, e.Path, e.Err)
	default:
		return fmt.Sprintf("could not resolve reference project %q for an unrecognised reason", e.Pseudonym)
	}
}

// Unwrap exposes the underlying stat failure, so a caller can still ask
// errors.Is(err, os.ErrNotExist) without caring which of the two states that
// carry one produced it.
func (e *ReferenceProjectError) Unwrap() error { return e.Err }

// ResolveReferenceProject resolves pseudonym to the real absolute path on
// this machine that carries the data behind it, reading the mapping from
// this machine's own ~/.timetattle/config.json.
func ResolveReferenceProject(pseudonym string) (string, error) {
	path, err := DefaultPath()
	if err != nil {
		return "", err
	}
	return ResolveReferenceProjectAt(pseudonym, path)
}

// ResolveReferenceProjectAt is ResolveReferenceProject against an explicit
// config path, so a test can exercise every failure state directly rather
// than through $HOME.
func ResolveReferenceProjectAt(pseudonym, configPath string) (string, error) {
	if _, statErr := os.Stat(configPath); errors.Is(statErr, fs.ErrNotExist) {
		return "", &ReferenceProjectError{Kind: ReferenceProjectNoConfigFile, ConfigPath: configPath, Pseudonym: pseudonym, Err: statErr}
	} else if statErr != nil {
		return "", fmt.Errorf("stat the config at %s: %w", configPath, statErr)
	}

	cfg, err := Load(configPath)
	if err != nil {
		return "", err
	}

	real, ok := cfg.ReferenceProjects[pseudonym]
	if !ok {
		return "", &ReferenceProjectError{
			Kind:        ReferenceProjectNoMapping,
			ConfigPath:  configPath,
			Pseudonym:   pseudonym,
			MappedCount: len(cfg.ReferenceProjects),
		}
	}

	if _, statErr := os.Stat(real); statErr != nil {
		return "", &ReferenceProjectError{
			Kind:       ReferenceProjectPathNotOnDisk,
			ConfigPath: configPath,
			Pseudonym:  pseudonym,
			Path:       real,
			Err:        statErr,
		}
	}
	return real, nil
}
