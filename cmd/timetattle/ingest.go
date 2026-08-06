package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/gitlog"
	"github.com/bensyverson/timetattle/internal/jobs"
	"github.com/bensyverson/timetattle/internal/knowledgec"
	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timestamped"
	"github.com/bensyverson/timetattle/internal/transcripts"
)

// fsobserveIngestion builds a single ingestion walking every registered
// directory, or nil if none are registered — the same tolerance
// -filedays-roots gets when nothing is configured, and cheaper than
// recording a harvest_run whose input would otherwise have to be empty,
// which BeginRun refuses.
//
// One Walker over every root, rather than one per directory, matches how
// fsobserve.Walker was already shaped: Roots is a slice for exactly this
// reason (internal/fsobserve's own package doc anticipated this wiring
// before internal/registry could enumerate anything to hand it).
func fsobserveIngestion(markers []*registry.Marker) *ingestion {
	if len(markers) == 0 {
		return nil
	}
	roots := make([]string, len(markers))
	for i, m := range markers {
		roots[i] = m.Path
	}
	return &ingestion{source: fsobserve.New(roots...), input: strings.Join(roots, ", ")}
}

// gitlogIngestions builds Commits and Reflog ingestions for every registered
// directory that is itself a git repository, and none for the rest — a
// registered Motion asset folder or scratch render directory is exactly as
// likely to be registered as a repository, and gitlog has nothing to read
// from one.
//
// Unlike fsobserve, this is one pair of sources per repository rather than
// one source over every root: gitlog.Sources(repo) reads exactly one
// repository's own log and reflog, and a single call cannot span more than
// one, so the repetition happens here rather than inside the package.
func gitlogIngestions(markers []*registry.Marker) []ingestion {
	var out []ingestion
	for _, m := range markers {
		if !isGitRepo(m.Path) {
			continue
		}
		for _, s := range gitlog.Sources(m.Path) {
			out = append(out, ingestion{source: s, input: m.Path})
		}
	}
	return out
}

// jobIngestions builds the two `job` task-database ingestions over every
// registered directory at once, or nil if none are registered — the same
// tolerance fsobserveIngestion gives an empty registry, and for the same
// reason: store.BeginRun refuses a run whose input would be empty.
//
// This follows fsobserve's shape rather than gitlog's, deliberately, even
// though .jobs.db is per-directory exactly as a git repository is.
// gitlogIngestions returns one ingestion per repository, so a harvest writes
// five harvest_run rows for source_id 'gitlog-commits'; `timetattle status`
// groups by source_id and reports the last of them, which is why it showed 81
// seen where the run had seen 932, and why four of five repositories could
// fail while status still printed ok (task uO70D). One collector over every
// root writes one row that means what it says.
//
// No directory is filtered out here for lacking a .jobs.db. internal/jobs
// tolerates a root that has none and fails loudly on one it cannot read, and
// deciding at plan time from a stat would put the two on the same footing
// again — the file can appear, vanish or become unreadable between this stat
// and the read that matters.
func jobIngestions(markers []*registry.Marker) []ingestion {
	if len(markers) == 0 {
		return nil
	}
	roots := make([]string, len(markers))
	for i, m := range markers {
		roots[i] = m.Path
	}
	input := strings.Join(roots, ", ")
	out := make([]ingestion, 0, 2)
	for _, s := range jobs.Sources(roots...) {
		out = append(out, ingestion{source: s, input: input})
	}
	return out
}

// manifestIngestions builds one ingestion per source described by every
// loaded manifest: the built-ins under internal/manifest/builtin (Motion,
// Blender), layered with anything under ~/.timetattle/manifests.
//
// These do not join the package-level source.Sources() registry the way
// internal/biome and internal/transcripts do: a manifest is data on disk,
// read fresh on every harvest, so there is nothing for an init to register
// ahead of time and no registeredInputs entry to maintain by hand.
//
// A source whose manifest failed validation is already absent from what
// manifest.LoadAll returns — parse itself reports that — so the only new
// failure mode here is a validated spec that internal/timestamped still
// refuses to build a reader for (today: none, since timestamped.New checks
// exactly what manifest.normalise already enforced). That is reported and
// skipped rather than failing the whole harvest, the same tolerance
// registeredIngestions gives a source with no input to resolve.
func manifestIngestions() ([]ingestion, error) {
	specs, err := manifest.LoadAll("")
	var (
		out  []ingestion
		errs []error
	)
	if err != nil {
		errs = append(errs, err)
	}
	for _, spec := range specs {
		reader, rerr := timestamped.New(spec)
		if rerr != nil {
			errs = append(errs, fmt.Errorf("manifest source %q: %w", spec.ID, rerr))
			continue
		}
		out = append(out, ingestion{source: reader, input: manifestInput(spec)})
	}
	return out, errors.Join(errs...)
}

// manifestInput names what a manifest source's reader actually read, for
// harvest_run.input.
//
// A glob source's root alone would not be honest: the reader is bounded by
// files.match too, so filepath.Join(root, match) is used, matching the
// string internal/timestamped's glob method actually hands to
// filepath.Glob. A Spotlight source is the harder case this exists for: when
// files.root is empty — true of the blender-backups built-in, which finds
// .blend1 files wherever the .blend happens to live via a bounded mdfind
// query rather than a directory — store.BeginRun would hard-refuse the
// empty string glob would otherwise produce, and a placeholder like "*"
// would be provenance that lies about having scoped anything. Naming the
// query mdfind actually ran is the honest answer either way, so it is used
// whether or not a root also narrowed it.
func manifestInput(spec manifest.Source) string {
	if spec.Discovery == manifest.DiscoverySpotlight {
		if spec.Files.Root != "" {
			return fmt.Sprintf("mdfind -onlyin %s %s", spec.Files.Root, spec.Files.Query)
		}
		return fmt.Sprintf("mdfind %s", spec.Files.Query)
	}
	return filepath.Join(spec.Files.Root, spec.Files.Match)
}

// isGitRepo reports whether dir has a .git entry at all, without shelling
// out to git just to test existence. .git is a directory in an ordinary
// checkout and a plain file (holding a "gitdir: ..." pointer) in a linked
// worktree — this project's own agent worktrees under .claude/worktrees are
// exactly that shape — so presence alone is checked, not which kind it is.
func isGitRepo(dir string) bool {
	_, err := os.Lstat(filepath.Join(dir, ".git"))
	return err == nil
}

// ingestion pairs a source with the input its harvest run will record.
//
// source.Source has no getter for an input, deliberately: a collector reading one
// fixed directory has no reason to publish it, and the knowledgeC collectors are
// handed their snapshot by whoever built them. So the pairing is made here, which
// is the one place both halves are known.
type ingestion struct {
	source source.Source
	input  string
	// unavailable is why this source cannot be read at all on this run, and is
	// set when the capture that should have produced its input failed. It is
	// not the same thing as leaving the source out of the plan: a source with
	// this set is still given a harvest_run row, which is then failed with this
	// reason having collected nothing. A source that is silently skipped
	// produces no row, and no row is indistinguishable from a source that ran
	// and found nothing.
	unavailable error
}

// registeredInputs names what each source in the package-level registry reads.
//
// A source that registers itself at init keeps its own path, so this mapping is
// by hand and a source missing from it is reported rather than ingested against a
// guess. Kind is not used to infer it: internal/source is explicit that callers
// must not branch on Kind, and a run whose input names the wrong directory is
// worse than one that fails, because it looks correct for ever afterwards.
var registeredInputs = map[string]func() (string, error){
	"biome-appusage":     biome.DefaultAppUsageDir,
	"claude-transcripts": transcripts.DefaultRoot,
}

// everything is the since handed to every Collect: a harvest reads all of it,
// every time.
//
// There is no watermark, and that is a decision rather than an omission.
// knowledgeC writes rows well after the events they describe — /app/usage was
// measured 23 hours behind /display/isBacklit — so a mark advanced to the newest
// event a run had seen would step over everything that arrived afterwards,
// permanently and with no symptom. Re-reading is cheap because the store
// deduplicates by content: 24,512 events ingested and then re-ingested add
// nothing at all.
var everything time.Time

// snapshotIngestions are the four knowledgeC streams, read out of one snapshot.
//
// The path is the one snapshot.Take just returned, never knowledgec.NewestSnapshot's
// answer. Resolving it independently would race a concurrent harvest, and a run
// that recorded a snapshot it did not read is provenance that lies.
func snapshotIngestions(path string) []ingestion {
	return []ingestion{
		{source: knowledgec.NewBacklight(path), input: path},
		{source: knowledgec.NewLocked(path), input: path},
		{source: knowledgec.NewAppUsage(path), input: path},
		{source: knowledgec.NewMediaUsage(path), input: path},
	}
}

// lostSnapshotIngestions are the same four streams as they stand when the
// capture that should have written their snapshot failed: named, planned, and
// certain to be recorded as failed without reading anything.
//
// The set is derived from snapshotIngestions rather than restated, so a fifth
// knowledgeC stream can never be added to one and forgotten in the other. The
// sources are constructed against a path they will never open, because only
// their identifiers are used.
//
// The input recorded is the live database that could not be read, which is the
// honest answer to what this run was trying to read. The snapshot an earlier
// run left in the directory is deliberately not reached for: re-ingesting it
// would add nothing at all — the store deduplicates by content — while
// reporting ok for every stream, so the run would look like a clean success
// and the day it lost would be invisible for a second time.
func lostSnapshotIngestions(src string, cause error) []ingestion {
	out := snapshotIngestions(src)
	for i := range out {
		out[i].unavailable = cause
	}
	return out
}

// registeredIngestions are the sources that joined the package-level registry at
// init, which today is internal/biome's.
//
// A source whose input cannot be resolved is left out rather than failing the
// harvest, which is the judgement internal/biome's own init makes: the only way
// this happens is a machine with no home directory, where there is no stream to
// point at and nothing an error message would help with.
func registeredIngestions() ([]ingestion, error) {
	var (
		out  []ingestion
		errs []error
	)
	for _, s := range source.Sources() {
		input, known := registeredInputs[s.ID()]
		if !known {
			errs = append(errs, fmt.Errorf("source %q registered itself but harvest cannot say what it reads, "+
				"so its events would carry provenance that is not true; add it to registeredInputs", s.ID()))
			continue
		}
		where, err := input()
		if err != nil {
			continue
		}
		out = append(out, ingestion{source: s, input: where})
	}
	return out, errors.Join(errs...)
}

// harvestIngestions is everything one harvest reads: the registered sources, plus
// the four knowledgeC streams, which cannot register themselves because the
// snapshot they read is written by the run in progress.
func harvestIngestions(snapshotPath string) ([]ingestion, error) {
	registered, err := registeredIngestions()
	return append(registered, snapshotIngestions(snapshotPath)...), err
}

// ingest reads every source and records what each one found.
//
// One harvest run per source, begun before anything is collected, because an
// event's provenance is not optional and because collection has partial-failure
// semantics: a source that cannot be read loses its own events and nothing else.
// Every source is attempted before the combined failure is returned, so the
// command exits non-zero without that costing the sources that could be read.
//
// A source finding nothing is a success. "No presence on the 22nd" and "no
// harvest ran on the 22nd" are different facts, and only a recorded empty run
// tells them apart.
func ingest(ctx context.Context, st *store.Store, plan []ingestion, out io.Writer) error {
	var errs []error
	for _, p := range plan {
		run, err := st.BeginRun(ctx, store.RunSpec{
			SourceID: p.source.ID(),
			Input:    p.input,
			Since:    everything,
		})
		if err != nil {
			// No run row, so there is nothing to record the loss against. The
			// error is the only report available.
			errs = append(errs, err)
			continue
		}

		// A source whose capture failed is not collected from at all: it takes
		// the failure path below having read nothing, which records the loss
		// against the stream that suffered it in the one place anything reads
		// back. It shares that path rather than having its own so that the two
		// kinds of loss cannot drift apart in how they are reported.
		//
		// A batch the store refuses is a failure of this source's run rather
		// than of the harvest: the store validates every event before writing
		// any of it, so an unusable event costs one stream, and the streams
		// already ingested keep what they wrote.
		var events []source.Event
		if err = p.unavailable; err == nil {
			events, err = p.source.Collect(ctx, everything)
		}
		if err == nil {
			_, err = run.Write(ctx, events)
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", run.SourceID, err))
			if lost := run.Fail(ctx, err); lost != nil {
				errs = append(errs, lost)
			}
			// Named here as well as in the returned error, so that a source
			// missing from the summary can never be read as a source with
			// nothing to report.
			fmt.Fprintf(out, "%s: lost (%v)\n", run.SourceID, err)
			continue
		}
		if err := run.Finish(ctx); err != nil {
			errs = append(errs, err)
			continue
		}
		fmt.Fprintf(out, "%s: %s seen, %d new\n", run.SourceID, plural(run.EventsSeen, "event"), run.EventsNew)
	}
	return errors.Join(errs...)
}
