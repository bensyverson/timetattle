// Package fsobserve turns the timestamps a filesystem actually stores for a
// project's own working files into durable source.Event observations.
//
// # Not the same job as internal/filedays
//
// internal/filedays exists because an mtime decays: a file's record holds
// only its LAST write, so today's edit on a file last touched on 11 July does
// not add evidence for 11 July, it deletes it. filedays exists to capture a
// day's count before that happens, and what it produces is a perishable
// snapshot that must be taken on a schedule and is only ever a lower bound
// once it is.
//
// This package answers a different question. It does not try to preserve a
// count against future decay; it re-derives, from whatever files exist right
// now, every instant APFS can still attest to. Two files sharing a tree with
// filedays is the only thing they share. A caller wanting a defensible
// historical day count still wants filedays; a caller wanting durable,
// replayable events for the store wants this package.
//
// # Two observations per file, not one
//
// APFS keeps st_birthtime beside mtime, so most files carry two independent
// instants: when they were created and when they were last written. Where a
// file was written once and never touched again the two coincide, and this
// package emits a single event rather than double-counting a file that only
// ever happened once. See TestCollectEmitsOneEventWhenBirthAndMtimeCoincide
// and, against real data, TestRealFilesystemWalkRecoversMotionSession —
// coincidence is decided at whole-second (Unix()) resolution, because the
// two are set by one syscall at creation and differ from each other only by
// the machine's own scheduling jitter (nanoseconds to low milliseconds on
// this build), while two genuinely separate writes have been observed
// landing 658ms apart across a second boundary — see the real-data test for
// the exact file.
//
// # A point, never an interval
//
// Of 512 files touched on 2026-07-11 in a real repository, only 11 had a
// birth-to-mtime gap over a minute, and those were mostly enormous — one
// spanned 09:55 to 23:41. Summing birth-to-mtime as if it were time spent
// would claim 70+ hours on a 13.9-hour day. Each timestamp this package
// emits is a source.Point: something happened at that instant, and how long
// it took, or what happened in between, is genuinely unknown and is never
// represented here as a duration.
//
// # Source documents versus generated output
//
// A thousand exported frames are one render action; a handful of source
// saves are a day of decisions, and a downstream consumer must not weigh
// them as a thousand equally-sized decisions. Every event this package
// produces carries that distinction in its State field as a Class — see
// Classify. State was chosen over adding a field to source.Identity because
// classification is a static property of what was observed (this artifact
// IS a compositor project file, or IS a rendered frame), which is exactly
// what State already exists to carry for other sources — knowledgeC's
// isBacklit reports "on", "off", or nothing sampled the same way. It also
// keeps this package confined to its own files rather than editing
// internal/source, which two sibling collectors are reading at the same
// time this one was written.
//
// # Cause
//
// Every event declares source.CauseUserAction. Unlike Motion's autosave
// vault, which writes on a wall-clock timer regardless of whether anyone is
// at the machine, a file living inside a project's own working tree changes
// mtime or gains a new birthtime because somebody saved it, exported it, or
// created it — there is no timer walking a project directory rewriting
// files on its own. This is the same reasoning the built-in blender-backups
// manifest already uses for .blend1.
//
// # Which directories get walked
//
// This package does not decide that. internal/registry blesses directories
// one at a time (Register) and resolves upward from a known path (Resolve),
// but as of this writing it exposes no way to enumerate every directory that
// has been blessed — there is no "list every marker on disk" function. That
// is a real gap for wiring this collector up automatically; until it exists,
// Walker takes its roots as an explicit parameter, exactly as the registry
// package's own author anticipated for a case like this.
//
// # Overlapping roots are collapsed before walking
//
// Register has no rule against blessing a directory and a sub-folder inside
// it — a repo plus a scratch render directory nested within it, say — and
// nothing stops registry.List from handing this package both paths as
// separate roots. Walked naively, every file under the nested root would be
// visited twice: once because it is reachable from the outer root, once
// because it is a root in its own right. See Walker.normalizedRoots: roots
// are resolved to a canonical, symlink-free absolute path first — the same
// duplication can arrive through an intermediate symlink on the path rather
// than literal nesting, the shape macOS's own /tmp (a symlink to
// /private/tmp) takes — and any root that is the same directory as, or
// nested inside, another surviving root is dropped. Deduplication happens
// here rather than in internal/registry: List enumerates what was blessed,
// which is a fact about the filesystem's markers, while whether two blessed
// paths happen to overlap on disk is a walking concern, and only this
// package's caller ever hands it more than one root at a time.
//
// The canonical form exists ONLY to decide whether two roots are the same
// or nested; it is never what gets walked. What actually gets read, and
// what ends up in every event's Locator, is still the caller's own path
// string. This is not a hypothetical distinction: on macOS, /var — and
// therefore os.TempDir(), and therefore every root this package's own tests
// build files under — is itself a symlink to /private/var, so resolving
// symlinks all the way through would have silently rewritten every event's
// Locator produced by this package's entire test suite, not just the roots
// that were actually overlapping. Whoever next touches root handling should
// keep that split rather than reach for the simpler-looking "just walk the
// resolved path".
//
// # A root that is itself a symlink, and the zero it used to return
//
// filepath.WalkDir calls os.Lstat on its own root argument and does not
// follow a symlink there, so a registered root that IS a symlink to a
// directory used to arrive at the walk function as a non-directory entry:
// the walk never descended, and Collect returned no events, no error, and
// no warning. Measured 2026-07-31 against a real registered directory — 19
// observations by its real path, 0 through a symlink to it. A project
// registered through an alias or a symlinked checkout would have harvested
// nothing from this package for ever, and every downstream report would
// have read "no filesystem work happened here" rather than "this root was
// never walked".
//
// Walker.walkableRoot resolves that one case, and its resolved form is fed
// to filepath.WalkDir alone. registeredPath maps every path the walk yields
// back onto the caller's own root string before anything is observed, so
// the Locator invariant above survives intact: a file found through a
// symlinked root is reported under the symlinked root, not under its
// target. That is why the resolution is not simply folded into
// canonicalPath, which is about equivalence rather than about reading.
//
// The half of that fix which matters more is the warning, not the walk. A
// root that yields nothing because it could not be READ — it is missing, it
// is not a directory, it is a symlink that cannot be resolved, a subtree
// underneath it is unreadable — says so through Walker.warn. A registered
// project that has simply not been saved into yet yields nothing and stays
// silent. "Observed nothing" and "nothing was observed" are the distinction
// this whole package exists to preserve, and a walk that cannot tell them
// apart about its own roots has already lost it.
package fsobserve

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/buildoutput"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
)

// ID identifies this source on every event it produces.
const ID = buildoutput.Source

// Kind reports how this source is read, for diagnostics only.
//
// It is declared here rather than added to internal/source's own Kind
// constants because Kind is a plain string type — nothing requires its
// values to live in one place — and two other collectors are landing in
// their own packages at the same time this one was written, each needing a
// Kind of its own. Adding three constants to a shared file three agents are
// touching concurrently is exactly the collision this task was scoped to
// avoid; consolidating them, if wanted, is a one-line change for whoever
// wires all three together.
const Kind source.Kind = "filesystem-walk"

// Class says whether an observed file is a source document a person made a
// decision in, output generated from one, or neither.
type Class string

const (
	// ClassSource is a compositor or design project file: a .motn, a
	// .blend, a .psd. A handful of these in a day is a day of decisions.
	ClassSource Class = "source"
	// ClassOutput is generated from a source document: a rendered frame,
	// an exported clip. A thousand of these in a day is one export, not a
	// thousand decisions.
	ClassOutput Class = "output"
	// ClassOther is neither — a text file, a project config, anything this
	// package has no extension rule for. Distinguishing it from an empty
	// State keeps "classified as neither" apart from "not classified at
	// all", which this package never does: every event gets a Class.
	ClassOther Class = "other"
)

// sourceExtensions are project files a person edits directly.
//
// .blend1 is included alongside .blend: it is Blender's own backup of a
// source document, written only because somebody pressed save, exactly the
// reasoning the built-in blender-backups manifest already uses to declare it
// source.CauseUserAction. The reference spike this package was built from
// (scripts/spike/dayview) omits it; that omission was not carried forward
// here because it would classify a real save of a real source document as
// "other".
var sourceExtensions = map[string]bool{
	".motn":   true,
	".psd":    true,
	".psb":    true,
	".blend":  true,
	".blend1": true,
	".ai":     true,
	".aep":    true,
}

// outputExtensions are what those applications generate.
var outputExtensions = map[string]bool{
	".webp": true,
	".png":  true,
	".jpg":  true,
	".jpeg": true,
	".mov":  true,
	".mp4":  true,
	".gif":  true,
	".exr":  true,
	".tif":  true,
}

// Classify reports whether path names a source document or generated
// output, by extension alone.
func Classify(path string) Class {
	switch ext := strings.ToLower(filepath.Ext(path)); {
	case sourceExtensions[ext]:
		return ClassSource
	case outputExtensions[ext]:
		return ClassOutput
	default:
		return ClassOther
	}
}

// bundleByExtension maps a file extension to a bundle identifier, but only
// where this codebase already has one independently confirmed —
// internal/manifest/builtin ships exactly these two. Every other extension
// leaves Identity.App empty rather than guess: App is the join key against
// Biome focus intervals, and a wrong one would corrupt that join silently
// instead of just failing to make it.
var bundleByExtension = map[string]string{
	".motn":   "com.apple.motionapp",
	".blend":  "org.blenderfoundation.blender",
	".blend1": "org.blenderfoundation.blender",
}

// excludedDirs are directories skipped whole rather than filtered
// afterwards, mirroring internal/filedays.DefaultExcludes for the same
// measured reason: a project directory can contain nested copies of itself
// (a worktree, a node_modules tree, a .git object store) whose files are not
// second helpings of evidence, they are the same evidence again. One
// measured case in this project found a directory 86x inflated by exactly
// this — 6103 raw files against 71 once worktree copies were excluded.
//
// `.venv` joins them on the same test and a measurement of its own: on
// 2026-08-01 it held 27,645 of 130,594 attributed fs-observations, 21.2%,
// against 0 for node_modules and 0 for .git because those were already skipped.
// The name is exact and deliberately narrow — `env` and `venv` without the dot
// matched one file each in that store and BOTH were source somebody wrote,
// including this repository's own scripts/spike/env/main.go.
//
// `.derived` is a directory Xcode is told to keep derived data in. It and the
// Python bytecode and tool caches hold only what a tool wrote.
var excludedDirs = map[string]bool{
	".git":           true,
	"worktrees":      true,
	"node_modules":   true,
	".venv":          true,
	buildoutput.Dir:  true,
	".derived":       true,
	"__pycache__":    true,
	".mypy_cache":    true,
	".pytest_cache":  true,
	".ruff_cache":    true,
	".Trash":         true,
	"Autosave Vault": true,
}

// Walker collects filesystem observations from a fixed set of root
// directories — see the package doc for why the set is a parameter rather
// than something this package discovers on its own.
type Walker struct {
	// Roots are the directories to walk. Each is walked independently;
	// one that does not exist is skipped rather than treated as a
	// failure, the same tolerance every other source in this codebase
	// gives a missing root.
	Roots []string
	// Log receives what is tolerated rather than returned: a missing
	// root, a path that could not be read. Nil means slog.Default.
	Log *slog.Logger
}

// New returns a Walker over roots. Registering it with source.Register is
// left to the caller, because that requires knowing the roots first — see
// the package doc.
func New(roots ...string) *Walker {
	return &Walker{Roots: roots}
}

// ID implements source.Source.
func (w *Walker) ID() string { return ID }

// Kind implements source.Source.
func (w *Walker) Kind() source.Kind { return Kind }

// Collect walks every root and returns one event per timestamp — birth,
// modification, or both — that falls after since.
//
// since is applied per timestamp, not per file: a file can have a birth this
// caller already has and a later mtime it does not, and only the new stamp
// comes back. Filtering per file instead would either re-report the birth
// forever or silently drop the new mtime, depending on which endpoint a
// caller happened to key on.
func (w *Walker) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var events []source.Event
	for _, root := range w.normalizedRoots() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		walkRoot, ok, err := w.walkableRoot(root)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}

		found, err := w.walk(ctx, root, walkRoot, since)
		if err != nil {
			if ctxErr := ctx.Err(); ctxErr != nil {
				return nil, ctxErr
			}
			return nil, fmt.Errorf("walk %s: %w", root, err)
		}
		events = append(events, found...)
	}

	// Neither a directory walk nor multiple roots returns files in time
	// order, so consumers and tests see one order rather than however many
	// roots happened to be configured.
	sort.SliceStable(events, func(i, j int) bool {
		if !events[i].Start.Equal(events[j].Start) {
			return events[i].Start.Before(events[j].Start)
		}
		return events[i].Locator < events[j].Locator
	})
	return events, nil
}

// normalizedRoots returns w.Roots reduced to one entry per physical
// directory, with any root that is the same directory as, or nested inside,
// another surviving root dropped — see the package doc's "Overlapping roots
// are collapsed before walking" for why this exists rather than trusting
// the caller's list directly.
//
// The ORIGINAL root strings are what get walked and returned, never their
// canonicalPath form: canonicalPath resolves symlinks purely to decide
// whether two roots name the same directory, and on macOS that resolution
// itself rewrites the path (/var, and therefore every os.TempDir() path
// this package's own tests build files under, is a symlink to /private/var).
// Walking the resolved form instead of the caller's own would silently
// change every event's Locator, which is a promise this package makes to
// its callers independently of this fix.
func (w *Walker) normalizedRoots() []string {
	type root struct {
		canonical string
		original  string
	}
	roots := make([]root, 0, len(w.Roots))
	seen := make(map[string]bool, len(w.Roots))
	for _, r := range w.Roots {
		c := w.canonicalPath(r)
		if seen[c] {
			continue
		}
		seen[c] = true
		roots = append(roots, root{canonical: c, original: r})
	}

	// Sorting by canonical path first guarantees an ancestor is always
	// considered before any of its descendants: for two clean absolute
	// paths, a proper prefix always sorts before the longer path built on
	// it (e.g. "/a" < "/a/b"), so a single forward pass checking each
	// candidate against what has already been kept is enough — no
	// candidate can be an ancestor of something that already made the cut.
	sort.Slice(roots, func(i, j int) bool { return roots[i].canonical < roots[j].canonical })

	var kept []root
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		nested := false
		for _, k := range kept {
			if isAncestorOrSame(k.canonical, r.canonical) {
				nested = true
				w.warn("dropping a registered root already covered by another registered root",
					"root", r.original, "covered_by", k.original)
				break
			}
		}
		if !nested {
			kept = append(kept, r)
			out = append(out, r.original)
		}
	}
	return out
}

// canonicalPath resolves root to an absolute, symlink-free path so that two
// roots naming the same physical directory by different strings — a
// trailing slash, a relative path, or an intermediate symlink such as
// macOS's own /tmp -> /private/tmp — compare equal.
//
// A root that cannot be resolved (it does not exist yet, or a component is
// unreadable) falls back to Abs+Clean rather than being treated as an
// error: Collect already tolerates a missing root as the ordinary case of
// an unregistered or moved directory, and normalization must not turn that
// tolerance into a hard failure.
func (w *Walker) canonicalPath(root string) string {
	abs, err := filepath.Abs(root)
	if err != nil {
		return filepath.Clean(root)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

// isAncestorOrSame reports whether a is b, or a directory somewhere above
// b, given two already-clean absolute paths. Mirrors
// internal/registry.isAncestorOrSame, which cannot be imported directly
// since it is unexported.
func isAncestorOrSame(a, b string) bool {
	if a == b {
		return true
	}
	return strings.HasPrefix(b, a+string(filepath.Separator))
}

// walkableRoot returns the path to hand filepath.WalkDir for root, reporting
// false where root cannot be walked as a directory tree at all.
//
// Two jobs, and the second is the one that matters. First, it resolves a
// root that is ITSELF a symlink. filepath.WalkDir calls os.Lstat on its own
// root argument and never follows a symlink there, so a symlinked root
// arrives at the walk function as a non-directory entry: the walk never
// descends and returns nothing, with no error. Measured 2026-07-31 against a
// real registered directory — 19 observations by its real path, 0 through a
// symlink to it. (A symlink met INSIDE a walked tree is a different case,
// and one encountered on the way to a root is resolved by ordinary path
// lookup before this package ever sees it.)
//
// Second, and independently of symlinks, every remaining way a root can
// yield nothing because it could not be READ is warned about here. Zero
// events from a registered project that has simply not been saved into yet
// is correct and stays silent; zero events because the root was never
// walked is a defect, and the two must never look alike to a downstream
// report. This package exists to distinguish "observed nothing" from
// "nothing was observed", so it cannot afford to blur it in its own walk.
//
// A root that is not there is tolerated rather than returned as an error,
// as every other source in this codebase does: most of the filesystem is
// not a registered project, on purpose.
func (w *Walker) walkableRoot(root string) (string, bool, error) {
	// os.Stat, unlike the os.Lstat filepath.WalkDir performs, follows a
	// symlinked root through to whatever it points at.
	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			w.warn("no files collected; this root does not exist, or is a symlink to something that no longer does",
				"root", root)
			return "", false, nil
		}
		return "", false, fmt.Errorf("read root %s: %w", root, err)
	}
	if !info.IsDir() {
		w.warn("no files collected; this root is not a directory", "root", root)
		return "", false, nil
	}

	link, err := os.Lstat(root)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		return root, true, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		w.warn("no files collected; this root is a symlink that could not be resolved",
			"root", root, "error", err)
		return "", false, nil
	}
	return resolved, true, nil
}

// registeredPath maps a path the walk produced back onto the root string the
// caller actually registered.
//
// Where the root was a symlink the walk runs over its resolved target,
// because that is the only thing filepath.WalkDir will descend into, but
// nothing outside the walk ever sees that form. On macOS /var — and
// therefore os.TempDir(), and therefore every root this package's own tests
// build files under — is itself a symlink to /private/var, so reporting what
// was walked rather than what was registered would rewrite the Locator of
// every event this package produces. See the package doc: the resolved form
// exists only so the walk can start, never so that a caller can read it.
func registeredPath(root, walkRoot, path string) (string, error) {
	if root == walkRoot {
		return path, nil
	}
	rel, err := filepath.Rel(walkRoot, path)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, rel), nil
}

func (w *Walker) walk(ctx context.Context, root, walkRoot string, since time.Time) ([]source.Event, error) {
	var events []source.Event
	err := filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subtree costs that subtree, not the whole
			// walk — the same posture every WalkDir callback in this
			// codebase takes toward a permission error partway through.
			w.warn("skipping a path that could not be read", "path", path, "error", err)
			return nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if d.IsDir() {
			if path != walkRoot && excludedDirs[d.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() || skipFile(d.Name()) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			w.warn("skipping a file whose metadata could not be read", "path", path, "error", err)
			return nil
		}

		reported, err := registeredPath(root, walkRoot, path)
		if err != nil {
			w.warn("skipping a file that could not be placed under its registered root",
				"path", path, "root", root, "error", err)
			return nil
		}

		abs, err := filepath.Abs(reported)
		if err != nil {
			w.warn("could not resolve a path to an absolute location", "path", reported, "error", err)
			return nil
		}

		events = append(events, w.observe(reported, abs, info, since)...)
		return nil
	})
	return events, err
}

// observe returns the events one file contributes: up to one for its birth
// and one for its mtime, never two for the same instant.
func (w *Walker) observe(path, abs string, info fs.FileInfo, since time.Time) []source.Event {
	mtime := info.ModTime()
	birth, hasBirth := birthtime(info)

	var stamps []time.Time
	// A file written once and never touched again has a birth and mtime
	// set by the same syscall; they agree to the second even though they
	// can differ by microseconds of scheduling jitter, so coincidence is
	// judged at Unix() resolution rather than exact equality. See the
	// package doc for the real file that crosses a second boundary by
	// 658ms and is correctly treated as two writes, not one.
	if hasBirth && !birth.IsZero() && (mtime.IsZero() || birth.Unix() != mtime.Unix()) {
		stamps = append(stamps, birth)
	}
	if !mtime.IsZero() {
		stamps = append(stamps, mtime)
	}
	if len(stamps) == 0 {
		return nil
	}

	id := source.Identity{
		App:      bundleByExtension[strings.ToLower(filepath.Ext(path))],
		Document: document(path),
	}
	class := Classify(path)

	events := make([]source.Event, 0, len(stamps))
	for _, at := range stamps {
		if !at.After(since) {
			continue
		}
		e := source.NewPoint(at, id, ID)
		e.Locator = abs
		e.Cause = source.CauseUserAction
		e.State = string(class)
		events = append(events, e)
	}
	return events
}

// document names the artifact a file records: the filename with its
// extension removed. Unlike internal/timestamped, which strips a manifest's
// timestamp pattern first, a raw filesystem walk has no such pattern to
// remove — the whole name apart from the extension is the document.
func document(path string) string {
	name := filepath.Base(path)
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// skipFile reports whether a file is housekeeping rather than evidence:
// macOS's own per-directory metadata cache, this project's own marker blessing
// the directory it sits in, or a SQLite sidecar.
func skipFile(name string) bool {
	return name == ".DS_Store" || name == registry.MarkerFilename || IsDatabaseSidecar(name)
}

// IsDatabaseSidecar reports whether name is SQLite's write-ahead log or its
// shared-memory index — the two files a database writes beside itself.
//
// This is the ONE noise rule this package hardcodes beyond .DS_Store and the
// registry marker, and the line it sits on is "cannot be a work product by
// construction". A -wal or a -shm cannot exist without the database file it
// belongs to, carries no content of its own, and is deleted on a clean close.
// Nobody's deliverable is a -wal, on any machine. A .db or a .log is a
// different matter entirely: either can be somebody's real output — a scraper's
// corpus, a shipped dataset — and excluding those is the user's declaration to
// make in sessions.exclude, not this package's to assume.
//
// WHY THE VOLUME IS THE WRONG MEASURE. Only 86 of the 130,361 fs-observations
// in the live store on 2026-08-01 were sidecars, 0.1%. But one observation is
// enough to put a project row on a day's review screen, and on 2026-08-01 seven
// unrelated repositories — cypress, dogwood, Hayes, Jobs, Throughline, kura,
// acorn — each showed a whole working day built out of nothing but a WAL
// checkpoint, every stamp inside the same second. Real work does not touch
// seven repositories in one second.
//
// WHY -journal IS NOT HERE. SQLite's rollback journal is the same kind of file
// and would be just as safe to skip on its own merits, but the suffix is not:
// "2026-08-01-journal" is a perfectly ordinary name for a diary entry, and this
// package would rather miss a rollback journal than delete somebody's day.
// -wal and -shm carry no such collision.
//
// It is exported because the rule has to be applied twice and must not be
// written twice. Skipping at collect time stops new sidecars arriving; it does
// nothing at all about the 86 already stored, and internal/store exposes no
// delete. internal/sessions applies this same predicate at read time so the
// stored ones stop counting without anything being destroyed.
func IsDatabaseSidecar(name string) bool {
	return strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm")
}

func (w *Walker) warn(msg string, args ...any) {
	log := w.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn(msg, args...)
}
