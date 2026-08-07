// Package transcripts reads Claude Code's own session history: the JSONL
// files it writes under ~/.claude/projects for every conversation it holds.
//
// # Subagent files are not optional
//
// A session run through the Task tool writes a second file beside the main
// one, at <sessionId>/subagents/<agentId>.jsonl. Walking only the top-level
// *.jsonl files misses these entirely, and on a real corpus of 790,950 timed
// records that is 229,068 of them -- 29% -- gone, not merely under-counted.
// isSidechain looks like the field that should say "this is a subagent
// record", and does not: measured across the same corpus it is false on
// every one of 494,465 top-level records and true on every one of 229,068
// subagent records, which makes it a perfect mirror of the path and useless
// as an independent signal. Directory structure is the only thing this
// package trusts for subagent-ness.
//
// Folding them in means giving a subagent's records exactly the treatment a
// main-session record gets -- the same Source, the same per-record cwd
// attribution, nothing keyed on the subagent's own agentId -- so that
// downstream clustering sees one continuous stream per project rather than a
// island per dispatched agent. The one place a subagent record is still
// treated differently is StateMachine: see below.
//
// # cwd, never the directory name
//
// The directory a transcript lives under encodes a working directory by
// replacing "/" with "-", and decoding it back is lossy wherever a path
// component contains a dash of its own -- a project called dark-crystal-v2
// is indistinguishable, by name alone, from a nested path
// dark/crystal/v2. Every record carries its own cwd field regardless, so
// this package never inspects the directory name for anything but finding
// the files under it; every event's Locator comes from the record.
//
// A subagent's cwd is not always identical to its parent session's: 26% of
// subagent records in the same corpus carry a cwd one or more path segments
// below the parent's (a subagent working in a repo subdirectory the parent
// session sat above). Per-record attribution handles this correctly on its
// own -- registry.Resolve climbs from whatever directory it is given -- so
// nothing here needs to normalise a subagent's cwd against its parent's.
//
// # Human turns, and why the "user" role is not enough
//
// The Anthropic Messages API represents a tool's result as a "user"-role
// turn fed back to the model -- that is a wire-protocol convention, not a
// person at a keyboard, and it dominates the "user" type in practice: 3,229
// of 3,510 "user" records sampled across 40 real transcripts are exactly
// this. A genuine human turn is a "user" record whose content is a plain
// string or a block list that carries no tool_result block, from a file
// that is not a subagent's -- a subagent's own dispatch prompt has that
// exact shape (plain string, no tool_result) and is not a human either,
// because nothing types it: it is the orchestrating agent's own Task
// invocation, running unattended. See event.go for the precise rule and
// isHumanTurn's doc for the content shapes this was checked against.
//
// This is recorded on Event.State rather than Event.Cause. Cause says what
// wrote an ARTIFACT -- a file saved to disk -- and a transcript record is
// not one; State is documented as the scalar reading a source takes where it
// takes one, the same shape /display/isBacklit uses for on/off, and "was a
// human or the machine writing this specific record" is exactly that kind
// of reading, taken once per record rather than once per source.
//
// # A record can genuinely appear twice, and disagree with itself
//
// Two of Claude Code's own internal mechanisms -- compaction
// ("agent-acompact-*.jsonl") and mid-task side questions
// ("agent-aside_question-*.jsonl") -- work by handing an internal subagent a
// replay of the preceding conversation as context, uuid and all, before it
// adds its own new records. A human turn replayed this way sits inside a
// subagents/ file and would be classified StateMachine by the rule above,
// while the very same uuid is correctly StateHuman where it was first
// written. This is not rare: measured against this machine's own corpus,
// 6,822 of 6,922 uuids that appear more than once trace to exactly this,
// always at the record's original timestamp. dedupeReplays resolves it by
// keeping one event per (Document, Start) and preferring whichever
// candidate says StateHuman -- a human-shaped observation confirmed
// anywhere is real signal, and a StateMachine copy of it is a replay
// artifact, never evidence the human turn did not happen.
//
// # Identity.Document is the record's uuid, not left empty
//
// Nothing about "what an event is about" needs a document name here -- there
// is no second source describing the same composition to join against, the
// way a Motion autosave joins a Biome focus interval by app. But
// Event.State and Event.Identity.App together are NOT enough to keep two
// real records from colliding: the store's dedup key is
// (source, shape, started_at, ended_at, app, document, state), Locator is
// deliberately excluded from it, and 24,460 records in a 731,399-record
// corpus -- 3.3% -- share their file's exact millisecond timestamp with
// another record. Two of those with the same State would silently collapse
// into one stored event. Every timed record in this format already carries
// a uuid, so Document is set to it: not because a transcript record is a
// "document" in any real sense, but because it is the field that keeps
// distinct observations distinct, and it happens to be exactly what
// "the record this event is about" would mean if asked literally.
//
// # A root that is itself a symlink, and the zero it used to return
//
// filepath.WalkDir calls os.Lstat on its own root argument and does not follow
// a symlink there, so a root that IS a symlink to the corpus used to arrive at
// the walk callback as a non-directory entry: the walk never descended, and
// Collect returned no events, no error and no warning. Measured 2026-08-01 --
// 722,463 events by ~/.claude/projects, 0 through a symlink to it. Symlinking
// ~/.claude into a dotfiles repository is a common setup, and it would have
// silently zeroed the largest source in this project, with every downstream
// report reading "no Claude Code work happened here" rather than "this root
// was never walked". Reader.walkableRoot resolves that one case, and feeds the
// resolved form to filepath.WalkDir alone.
//
// This package needs no equivalent of internal/fsobserve's registeredPath,
// deliberately and not by oversight: nothing derived from the walked path ever
// becomes a Locator here, because every Locator is the record's own cwd. The
// walk path decides one thing only -- whether a file sits in a subagents/
// directory -- and that reads the immediate parent's name, which is the same
// either side of the resolution.
//
// The half that matters more is the reporting. A root that is missing, is not
// a directory, or is a symlink that cannot be resolved says so through
// Reader.Log and names itself; a root that refuses to be read outright is
// still returned as an error, since there is no "the rest of the corpus is
// fine" to fall back on. A machine where Claude Code has simply held no
// conversation yet yields nothing and stays silent. "Observed nothing" and
// "nothing was observed" must never look alike.
package transcripts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// ID identifies this source on every event it produces.
const ID = "claude-transcripts"

// Kind says this source is read by walking a directory of JSONL files.
//
// source.Kind is an unclosed string type -- nothing requires every value of
// it to be declared inside internal/source itself -- so this is declared
// here rather than added there. Two other collectors are landing in their
// own internal/ packages at the same time this one is; not touching a file
// they might also want to touch is the whole point.
const Kind source.Kind = "claude-transcripts"

// subagentsDirName is the one structural signal this package trusts for
// subagent-ness. See the package doc for why isSidechain is not used.
const subagentsDirName = "subagents"

// Reader collects events from Claude Code's own transcripts.
type Reader struct {
	// root is empty on the package-level default instance registered by
	// init, deliberately: init runs once per process, before a test's
	// t.Setenv("HOME", ...) has any effect, so resolving DefaultRoot eagerly
	// here would bake in whatever machine's real home directory happened to
	// exist at process start, immune to every later test's own isolation.
	// resolveRoot resolves it fresh on every Collect instead.
	root string

	// Log receives what is tolerated rather than returned: a root that
	// could not be walked as a directory tree at all. Nil means
	// slog.Default. See walkableRoot for why a root yielding nothing has to
	// say which kind of nothing it is.
	Log *slog.Logger
}

// NewReader returns a source reading transcripts under root.
func NewReader(root string) *Reader { return &Reader{root: root} }

// NewDefaultReader returns a source that resolves the current user's
// transcripts directory freshly on every Collect, rather than once at
// construction — see the root field's doc comment for why that distinction
// matters.
func NewDefaultReader() *Reader { return &Reader{} }

// resolveRoot returns the directory this instance reads, resolving
// DefaultRoot on every call when none was given explicitly.
func (r *Reader) resolveRoot() (string, error) {
	if r.root != "" {
		return r.root, nil
	}
	return DefaultRoot()
}

// DefaultRoot is where Claude Code keeps transcripts for the current user.
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, ".claude", "projects"), nil
}

// ID implements source.Source.
func (r *Reader) ID() string { return ID }

// Kind implements source.Source.
func (r *Reader) Kind() source.Kind { return Kind }

// Collect reads every transcript under root and returns the events that had
// not finished before since.
//
// Every file is read on every call, whatever since is: a full parse is cheap
// (measured well under a minute even over several gigabytes -- see
// realdata_test.go for this machine's own number) and there is no watermark
// to fall out of step with a corpus that gets rewritten from underneath it.
func (r *Reader) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	files, err := r.listFiles()
	if err != nil {
		return nil, err
	}

	events, err := scanAll(ctx, files)
	if err != nil {
		return nil, err
	}
	events = dedupeReplays(events)

	sort.SliceStable(events, func(i, j int) bool {
		return events[i].Anchor().Before(events[j].Anchor())
	})

	if since.IsZero() {
		return events, nil
	}
	kept := events[:0]
	for _, e := range events {
		if !e.End.Before(since) {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// dedupeReplays resolves the one case where the same real record legitimately
// produces two candidate events with conflicting State.
//
// Claude Code's own compaction ("agent-acompact-*.jsonl") and side-question
// ("agent-aside_question-*.jsonl") mechanisms work by giving an internal
// subagent a replay of the preceding conversation as context -- uuid,
// timestamp and content identical to the original -- before it adds its own
// new records. Because every record in a subagents/ file is otherwise
// StateMachine (see isHumanTurn), a replayed human turn is misclassified
// there even though the very same uuid is correctly StateHuman where it was
// first written. Measured against this machine's own corpus: 6,822 of 6,922
// repeating uuids trace to exactly this, always at the original timestamp.
//
// The fix keeps one event per (Document, Start) and prefers whichever
// candidate says StateHuman, because a human-shaped observation confirmed
// anywhere is real signal and a StateMachine copy of it is a replay
// artifact, never the other way around.
//
// The kept candidate's PAYLOAD comes with it, which matters for two readings
// that disagree about what the record cost. Measured 2026-08-07 with
// scripts/sessioncensus: of 7,182 repeat readings, 0 disagree about the
// session and 52 disagree about the token figures, so the session a record
// belonged to is never in doubt and 52 records report whichever copy this
// rule kept. That disagreement is resolved HERE, before the store, precisely
// because it cannot be resolved there: the payload is outside
// event_identity, so a second, differing reading conflicts and does nothing
// rather than standing beside the first as a second observation. This is conflict resolution between
// two readings of one already-identified record, not session clustering --
// no candidate is dropped for belonging to the "wrong" session, and nothing
// here decides whether two DIFFERENT records belong together.
func dedupeReplays(events []source.Event) []source.Event {
	type key struct {
		document string
		start    time.Time
	}
	index := make(map[key]int, len(events))
	out := make([]source.Event, 0, len(events))
	for _, e := range events {
		k := key{e.Identity.Document, e.Start}
		if i, ok := index[k]; ok {
			if e.State == StateHuman && out[i].State != StateHuman {
				out[i] = e
			}
			continue
		}
		index[k] = len(out)
		out = append(out, e)
	}
	return out
}

// transcriptFile is one *.jsonl file under root, with the one fact about its
// location that this package cares about.
type transcriptFile struct {
	path       string
	isSubagent bool
}

// listFiles walks root for every *.jsonl file, tagging each as a subagent
// file purely by whether its immediate parent directory is named
// "subagents" -- the structural signal the package doc explains.
func (r *Reader) listFiles() ([]transcriptFile, error) {
	root, err := r.resolveRoot()
	if err != nil {
		return nil, err
	}
	walkRoot, ok, err := r.walkableRoot(root)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}

	var files []transcriptFile
	err = filepath.WalkDir(walkRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if path == walkRoot {
				// The root itself refusing to be read (permission denied,
				// typically) is a real problem worth surfacing -- unlike one
				// project directory further down, there is no "the rest of
				// the corpus is still fine" to fall back on.
				return err
			}
			// A directory that vanishes mid-walk or refuses a stat is not
			// fatal to the whole harvest -- see scanAll for the same
			// reasoning applied to a file that refuses to open.
			return nil
		}
		if d.IsDir() || filepath.Ext(d.Name()) != ".jsonl" {
			return nil
		}
		files = append(files, transcriptFile{
			path:       path,
			isSubagent: filepath.Base(filepath.Dir(path)) == subagentsDirName,
		})
		return nil
	})
	if err != nil {
		// The caller's own root string, not the resolved one: it is the path
		// somebody configured and the only one they can act on.
		return nil, fmt.Errorf("walk %s: %w", root, err)
	}
	return files, nil
}

// walkableRoot returns the path to hand filepath.WalkDir for root, reporting
// false where root cannot be walked as a directory tree at all.
//
// Two jobs, and the second is the one that matters. First, it resolves a root
// that is ITSELF a symlink. filepath.WalkDir calls os.Lstat on its own root
// argument and never follows a symlink there, so a symlinked root arrived at
// the walk callback as a non-directory entry: the walk never descended and
// Collect returned nothing, with no error and no warning. Measured 2026-08-01
// against this machine's corpus -- 722,463 events by the real path, 0 through
// a symlink to it. Symlinking ~/.claude into a dotfiles repository is a common
// setup, and it would have silently zeroed the largest source in this project
// while every downstream report read "no Claude Code work happened".
//
// This is deliberately NOT paired with an fsobserve-style registeredPath: the
// walked path never becomes a Locator here. Every event's Locator is the
// record's own cwd field (see the package doc), so walking a resolved root
// cannot rewrite attribution the way it would there. The only thing the walk
// path decides is subagent-ness, which reads the immediate parent directory
// name and is identical either side of the resolution. See
// TestCollectReportsTheSameLocatorsThroughASymlinkedRoot, which pins that so
// it stays true rather than remaining a claim in a comment.
//
// Second, and independently of symlinks, a root that yields nothing BECAUSE IT
// COULD NOT BE READ says so and names itself. Zero events from a machine where
// Claude Code has held no conversation yet is correct and stays silent. A root
// that refuses to be read outright stays an error rather than a warning, which
// is this package's existing and deliberate choice: unlike one project
// directory further down, there is no "the rest of the corpus is still fine".
func (r *Reader) walkableRoot(root string) (string, bool, error) {
	// os.Stat, unlike the os.Lstat filepath.WalkDir performs, follows a
	// symlinked root through to whatever it points at.
	info, err := os.Stat(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Not an error: a machine that has never run Claude Code has no
			// such directory, the same tolerance every other source in this
			// codebase gives a missing root. It is still said out loud,
			// because "there is nothing to read here" and "there was nothing
			// to read" are different answers.
			r.warn("no transcripts collected; this root does not exist, or is a symlink to something that no longer does",
				"root", root)
			return "", false, nil
		}
		return "", false, fmt.Errorf("read root %s: %w", root, err)
	}
	if !info.IsDir() {
		r.warn("no transcripts collected; this root is not a directory", "root", root)
		return "", false, nil
	}

	link, err := os.Lstat(root)
	if err != nil || link.Mode()&os.ModeSymlink == 0 {
		return root, true, nil
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		r.warn("no transcripts collected; this root is a symlink that could not be resolved",
			"root", root, "error", err)
		return "", false, nil
	}
	return resolved, true, nil
}

// scanAll reads every file concurrently. Files are independent and I/O
// bound, and this corpus runs to thousands of files and several gigabytes,
// which is what makes the seconds-not-minutes criterion (xpf) achievable.
func scanAll(ctx context.Context, files []transcriptFile) ([]source.Event, error) {
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		events []source.Event
		sem    = make(chan struct{}, runtime.GOMAXPROCS(0))
	)

	for _, f := range files {
		if err := ctx.Err(); err != nil {
			wg.Wait()
			return nil, err
		}
		wg.Add(1)
		go func(f transcriptFile) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// A single file that cannot be opened or read -- deleted mid-walk,
			// permission denied, truncated by a concurrent write -- loses only
			// its own events. Thousands of good files losing everything over
			// one bad one would be a much worse failure than the one this
			// project's sources exist to avoid.
			got, err := scanFile(f.path, f.isSubagent)
			if err != nil {
				return
			}
			mu.Lock()
			events = append(events, got...)
			mu.Unlock()
		}(f)
	}
	wg.Wait()
	return events, nil
}

// scanFile reads one transcript, emitting a Point event for every record
// that carries both a timestamp and a uuid -- the two facts every genuinely
// timed record in this format has, and the only two this package requires.
func scanFile(path string, isSubagent bool) ([]source.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	// Tool results are embedded inline and can be enormous; the default 64 KB
	// limit silently truncates whole lines rather than erroring.
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)

	var events []source.Event
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var rec record
		if err := json.Unmarshal(line, &rec); err != nil {
			// One malformed line is not evidence the rest of the file is
			// unusable -- these files are appended to by a running process
			// and a torn final line is the ordinary way that shows up.
			continue
		}
		// Bookkeeping records (mode, ai-title, last-prompt, queue-operation)
		// carry no timestamp, no uuid, or both, and cannot become a dated,
		// dedup-safe observation.
		if rec.Timestamp.IsZero() || rec.UUID == "" {
			continue
		}

		state := StateMachine
		if isHumanTurn(rec, isSubagent) {
			state = StateHuman
		}

		e := source.NewPoint(rec.Timestamp, source.Identity{Document: rec.UUID}, ID)
		e.State = state
		if rec.CWD != "" && filepath.IsAbs(rec.CWD) {
			e.Locator = rec.CWD
		}
		// The session and the tokens ride OUTSIDE identity, in the payload.
		// Putting a per-record figure on State — the one field of an event
		// that is in the store's dedup key — would make every record its own
		// identity, and every harvest would insert the whole corpus again.
		// See migration 0014 and internal/usage.
		//
		// A payload that will not encode loses this one record rather than
		// the file: the timestamp and uuid are what an event needs, and the
		// payload is what rides along with it.
		payload, err := rec.payload().Encode()
		if err != nil {
			continue
		}
		e.Payload = payload
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return events, fmt.Errorf("read %s: %w", path, err)
	}
	return events, nil
}

func (r *Reader) warn(msg string, args ...any) {
	log := r.Log
	if log == nil {
		log = slog.Default()
	}
	log.Warn(msg, args...)
}

// init registers this source, the same way database/sql drivers do: adding
// a collector means adding a package and a blank import, never editing this
// one.
//
// A machine with no home directory has nowhere to point at, and registering
// a source with nothing to read would turn that into a per-harvest error
// about something nobody can fix. Registrability is checked here, but the
// root itself is not kept — see NewDefaultReader's doc comment.
func init() {
	if _, err := DefaultRoot(); err != nil {
		return
	}
	source.Register(NewDefaultReader())
}
