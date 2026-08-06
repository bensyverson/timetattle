// Package timestamped reads evidence out of timestamped files on disk.
//
// One reader serves every manifest of this kind. Motion's autosave vault,
// Blender's .blend1 backups and Photoshop's AutoRecover folder differ only in
// where they live and how the time is encoded, so the differences live in data
// and this package holds the single mechanism that walks them.
//
// # These are points, not durations
//
// An autosave says a document existed at an instant. It does not say how long
// anyone worked on it, and the cadence is a property of the application's
// timer rather than of the person. Biome already supplies exact focus
// intervals; what Biome cannot supply is which document was open. So this
// source exists for project attribution, its points are joined against those
// intervals downstream, and it deliberately does no clustering of its own.
//
// # Why Spotlight is a discovery strategy
//
// Recursive globs are refused at manifest load, because a shallow root with
// "**" walks an entire volume and produces a harvester that looks hung. Some
// evidence is genuinely scattered — .blend1 backups sit wherever the .blend
// does — so those manifests reach the same files through mdfind, which reads
// an index instead of the filesystem.
//
// # What is tolerated and what is not
//
// A root that does not exist means an application is not installed, which is
// the common case for any given user, and a file caught by the glob but not
// by the timestamp pattern is a manifest that is slightly off. Both are
// warned about and skipped. Losing an entire harvest over either would be the
// same permanent data loss the harvester exists to prevent. A query the index
// rejects is different: nothing about it will fix itself, so it is returned
// as an error.
package timestamped

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
)

// Reader collects events from the files one manifest source describes.
type Reader struct {
	// Log receives the problems that are tolerated rather than returned:
	// an absent root, a file that does not match its pattern. Nil means
	// slog.Default.
	Log *slog.Logger

	spec manifest.Source
}

// New returns a reader for a loaded manifest source.
//
// The manifest package has already validated and compiled everything by the
// time a source reaches here, so the checks below are about what this reader
// in particular can honour rather than about the file being well formed.
func New(spec manifest.Source) (*Reader, error) {
	if spec.ID == "" {
		return nil, errors.New("source has no id; every source needs one to attribute its events")
	}
	if spec.Type != source.KindTimestampedFiles {
		return nil, fmt.Errorf("source %q has type %q, which this reader does not read; it reads %q",
			spec.ID, spec.Type, source.KindTimestampedFiles)
	}
	// A file carries one timestamp. An interval would have to be invented,
	// and an invented duration is worse than none at all.
	if spec.Emits != source.Point {
		return nil, fmt.Errorf("source %q emits %q, but a timestamped file marks an instant and can only emit %q",
			spec.ID, spec.Emits, source.Point)
	}
	// A loaded manifest cannot get here without a cause, so this catches the
	// hand-built spec — and that failure would be silent, since the events are
	// otherwise perfectly good and merely assert less than they appear to.
	if spec.Cause == "" {
		return nil, fmt.Errorf("source %q does not say what causes its files to be written; "+
			"load it with manifest.Load rather than building it by hand", spec.ID)
	}
	if spec.Timestamp.From == manifest.FromFilename && spec.Timestamp.Compiled == nil {
		return nil, fmt.Errorf("source %q reads its timestamp from the filename but has no compiled pattern; "+
			"load it with manifest.Load rather than building it by hand", spec.ID)
	}
	// Load expands a leading ~ for glob roots but not for Spotlight ones,
	// where the root is optional. Passing it through unexpanded would search
	// a directory literally named "~" and report nothing, forever.
	if strings.HasPrefix(spec.Files.Root, "~") {
		return nil, fmt.Errorf("source %q has an unexpanded root %q; give an absolute path",
			spec.ID, spec.Files.Root)
	}
	return &Reader{spec: spec}, nil
}

// ID is the stable identifier every event this reader produces is attributed to.
func (r *Reader) ID() string { return r.spec.ID }

// Kind reports how this source is read, for diagnostics only.
func (r *Reader) Kind() source.Kind { return source.KindTimestampedFiles }

// Collect returns one event per file whose timestamp falls after since.
//
// since is the whole of the "do not report this twice" mechanism. A harvest
// records the newest timestamp it saw and passes it back next time; files that
// have not been written since are simply not emitted. Nothing is remembered
// between calls, so a reader holds no state that could disagree with the disk.
func (r *Reader) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	paths, err := r.discover(ctx)
	if err != nil {
		return nil, err
	}

	events := make([]source.Event, 0, len(paths))
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		info, err := os.Lstat(path)
		if err != nil {
			// Perishable sources are pruned by the application that writes
			// them, so a file can vanish between discovery and reading.
			r.warn("skipping a file that could not be read", "path", path, "error", err)
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}

		at, err := r.timestamp(path, info)
		if err != nil {
			r.warn("skipping a file with no readable timestamp", "path", path, "error", err)
			continue
		}
		if !at.After(since) {
			continue
		}

		e := source.NewPoint(at, r.identity(path), r.spec.ID)
		// Attribution's first pass matches the locator against the
		// directories a project has blessed, and a relative path can never
		// match one — it attributes to nothing rather than failing. A glob
		// against a relative root yields relative results, so this is resolved
		// once here instead of being re-checked at every later stage.
		e.Locator = r.locator(path)
		// Declared by the manifest, because nothing about the file says it: the
		// mtime of a .blend1 written by a save and of an autosave written by a
		// timer are the same fact, and only one of them means somebody was here.
		e.Cause = r.spec.Cause
		events = append(events, e)
	}

	// Spotlight returns results in index order and a glob returns them in
	// name order; neither is chronological. Sorting here means consumers and
	// tests see one order rather than two.
	slices.SortStableFunc(events, func(a, b source.Event) int {
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return strings.Compare(a.Identity.Document, b.Identity.Document)
	})
	return events, nil
}

// discover finds the candidate files, by whichever strategy the manifest names.
func (r *Reader) discover(ctx context.Context) ([]string, error) {
	switch r.spec.Discovery {
	case manifest.DiscoverySpotlight:
		return r.spotlight(ctx)
	default:
		return r.glob()
	}
}

// glob walks the bounded tree the manifest describes.
func (r *Reader) glob() ([]string, error) {
	root := r.spec.Files.Root
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Most people have no Motion. An application that was never
			// installed is not a failure of the harvest.
			r.warn("no files collected; this source's root does not exist", "source", r.spec.ID, "root", root)
			return nil, nil
		}
		// Permission, on the other hand, is actionable and worth saying.
		return nil, fmt.Errorf("read root %s: %w", root, err)
	}

	// filepath.Glob expands each path segment against the directory above it,
	// so a multi-segment pattern like */*.motn stays bounded to the depth the
	// pattern actually names.
	matches, err := filepath.Glob(filepath.Join(root, r.spec.Files.Match))
	if err != nil {
		return nil, fmt.Errorf("match %s in %s: %w", r.spec.Files.Match, root, err)
	}
	return matches, nil
}

// spotlight asks the macOS metadata index for the files, which is how a source
// searches broadly without walking a volume.
func (r *Reader) spotlight(ctx context.Context) ([]string, error) {
	bin, err := exec.LookPath("mdfind")
	if err != nil {
		// mdfind ships with macOS and is absent everywhere else. That is the
		// same situation as an uninstalled application: report and move on.
		r.warn("no files collected; this source needs mdfind, which is not available",
			"source", r.spec.ID, "error", err)
		return nil, nil
	}

	args := make([]string, 0, 4)
	if r.spec.Files.Root != "" {
		args = append(args, "-onlyin", r.spec.Files.Root)
	}
	// NUL separation, because a filename may legally contain a newline.
	args = append(args, "-0", r.spec.Files.Query)

	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		// A rejected query is a manifest bug that will never fix itself, so
		// unlike a missing root it is worth failing over. mdfind's own
		// complaint is the only thing that says which part it disliked.
		return nil, fmt.Errorf("mdfind %q failed: %w: %s",
			r.spec.Files.Query, err, strings.TrimSpace(stderr.String()))
	}

	var paths []string
	for path := range strings.SplitSeq(stdout.String(), "\x00") {
		if path != "" {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// timestamp reads when the file's event happened.
func (r *Reader) timestamp(path string, info fs.FileInfo) (time.Time, error) {
	switch r.spec.Timestamp.From {
	case manifest.FromFilename:
		name := filepath.Base(path)
		match := r.spec.Timestamp.Compiled.FindStringSubmatch(name)
		if match == nil {
			return time.Time{}, fmt.Errorf("%q does not match timestamp.pattern %q",
				name, r.spec.Timestamp.Pattern)
		}
		// ParseInLocation, not Parse: applications write local wall-clock
		// time into filenames with no zone, and Parse would read 06:32 as
		// UTC and shift every session by the offset.
		at, err := time.ParseInLocation(r.spec.Timestamp.Layout, match[1], time.Local)
		if err != nil {
			return time.Time{}, fmt.Errorf("parse %q from %q with layout %q: %w",
				match[1], name, r.spec.Timestamp.Layout, err)
		}
		return at, nil
	case manifest.FromMtime:
		return info.ModTime(), nil
	case manifest.FromBirthtime:
		return birthtime(info)
	default:
		return time.Time{}, fmt.Errorf("unknown timestamp.from %q", r.spec.Timestamp.From)
	}
}

// locator is where the evidence was found, resolved against the working
// directory when discovery handed back something relative.
//
// A path that cannot be made absolute is returned unchanged rather than
// dropped: the observation is still real, and source.Event.Validate refuses
// the relative locator at the registry, where the failure is reported once
// with the file named instead of being silently swallowed here.
func (r *Reader) locator(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		r.warn("could not resolve a path to an absolute location", "path", path, "error", err)
		return path
	}
	return abs
}

// identity says what the event is about. App comes from the manifest and is
// the key that joins these points to focus intervals, so it is not a label.
//
// Project is deliberately left empty. A resolution pass fills it later by
// mapping the document to a client project; nothing this reader could extract
// from a path is that, and setting it here would be a different fact wearing
// the same field.
func (r *Reader) identity(path string) source.Identity {
	return source.Identity{
		App:      r.spec.App,
		Document: r.document(path),
	}
}

// document names the artifact this file records.
//
// The default strips the same span the timestamp pattern matched, because a
// source that reads its timestamp from the filename has already declared,
// via that pattern, exactly which part of the name is not the document.
// An explicit document rule overrides the default for a source whose
// variable part is a counter or a UUID rather than a timestamp.
//
// Where the manifest also declares a project-shaped path pattern (Motion's
// vault directory), it is read here purely to corroborate the result:
// agreement is confidence, and disagreement is a save-as or rename that must
// be visible rather than silently collapsed into whichever name won.
func (r *Reader) document(path string) string {
	doc := r.filenameDocument(filepath.Base(path))
	if r.spec.Document != nil && r.spec.Document.Compiled != nil {
		if match := r.spec.Document.Compiled.FindStringSubmatch(path); match != nil {
			doc = match[1]
		}
	}
	if r.spec.Project != nil && r.spec.Project.Compiled != nil {
		if match := r.spec.Project.Compiled.FindStringSubmatch(path); match != nil && match[1] != doc {
			r.warn("path-derived document disagrees with the filename-derived one",
				"path-derived", match[1], "filename-derived", doc)
		}
	}
	return doc
}

// filenameDocument removes the timestamp pattern's match and any remaining
// extension from a filename. A source that reads its time from the
// filesystem rather than the name has no match to remove, so only the
// extension comes off.
func (r *Reader) filenameDocument(name string) string {
	if r.spec.Timestamp.From == manifest.FromFilename && r.spec.Timestamp.Compiled != nil {
		if loc := r.spec.Timestamp.Compiled.FindStringIndex(name); loc != nil {
			name = name[:loc[0]] + name[loc[1]:]
		}
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

func (r *Reader) warn(msg string, args ...any) {
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn(msg, args...)
}
