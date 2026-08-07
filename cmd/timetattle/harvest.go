package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/filedays"
	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/registry"
	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/store"
)

func harvest(args []string) error { return executeSubcommand(harvestCmd(), args) }

func harvestCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "harvest",
		Short: "Preserve perishable evidence, and ingest it",
		Long: `Preserves the evidence that deletes itself — knowledgeC, Biome's
ScreenTime.AppUsage segments, Motion's Autosave Vault, a filedays scan — then
ingests every source into the event store and re-attributes it against the
registry. Each capture is attempted whatever the ones before it did and the
error names every source that failed; the macOS stores need Full Disk Access.`,
	}
	fs := cmd.Flags()
	// Not named "source": that identifier is the package these sources come
	// from, and shadowing it here would be a trap for the next edit.
	sourcePath := fs.String("source", "", "knowledgeC.db to read (default: the current user's)")
	biomeRoot := fs.String("biome", "", "Biome restricted stream directory to read (default: the current user's)")
	dir := fs.String("dir", "", "where to write snapshots (default: ~/.timetattle/snapshots)")
	storePath := fs.String("store", "", "event store to ingest into (default: ~/.timetattle/timetattle.db)")
	motionVault := fs.String("motion-vault", "",
		"Motion's Autosave Vault to preserve (default: the motion-autosave manifest source's root)")
	filedaysRootsFlag := fs.String("filedays-roots", "",
		"comma-separated directories to scan for file modification days (default: read from ~/.timetattle/filedays-roots.txt)")
	filedaysExclude := fs.String("filedays-exclude", strings.Join(filedays.DefaultExcludes, ","),
		"comma-separated directory names to skip whole when scanning filedays roots")
	indexPath := fs.String("index", "",
		"registry index of registered directories to read (default: ~/.timetattle/registered-directories.json)")
	quiet := fs.Bool("quiet", false, "print nothing on success; for scheduled runs")
	cmd.RunE = func(*cobra.Command, []string) error {
		var err error
		if *sourcePath == "" {
			if *sourcePath, err = snapshot.DefaultSourcePath(); err != nil {
				return err
			}
		}
		if *biomeRoot == "" {
			if *biomeRoot, err = snapshot.DefaultBiomeRoot(); err != nil {
				return err
			}
		}
		if *dir == "" {
			if *dir, err = snapshot.DefaultDir(); err != nil {
				return err
			}
		}
		if *storePath == "" {
			if *storePath, err = store.DefaultPath(); err != nil {
				return err
			}
		}
		vaultRoot := *motionVault
		if vaultRoot == "" {
			if vaultRoot, err = defaultVaultRoot(); err != nil {
				return err
			}
		} else if vaultRoot, err = filedays.ExpandHome(vaultRoot); err != nil {
			return err
		}
		filedaysRoots, err := resolveFiledaysRoots(*filedaysRootsFlag)
		if err != nil {
			return err
		}
		registryIndex := *indexPath
		if registryIndex == "" {
			if registryIndex, err = registry.DefaultIndexPath(); err != nil {
				return err
			}
		}

		out := io.Writer(os.Stdout)
		if *quiet {
			out = io.Discard
		}

		// Capture is best-effort, source by source, and every failure is carried to
		// the end of the run rather than returned from the middle of it.
		//
		// These four reads are of four unrelated perishable things. They write to
		// disjoint places and none of them depends on another having succeeded, so
		// abandoning the readable ones because one is unreadable maximises the loss
		// rather than minimising it — which is the opposite of why this phase runs
		// first. It is not hypothetical: from 2026-07-30 12:01 the scheduled
		// harvest lost its Full Disk Access grant on knowledgeC (see
		// docs/content/docs/harvest.md on why `go install` revokes it), returned on
		// the first line of this function three runs in a row, and thereby also
		// cost the Biome capture, the Motion vault copy, the filedays scan and
		// every ingestion — including the sources that need no such grant — for
		// over a day.
		//
		// The order below is unchanged and still runs from the hardest deadline to
		// the softest. It now decides only which source gets the machine's
		// attention first, rather than which sources get it at all.
		// Every capture's outcome is collected here and written to the store once
		// it is open, below. Until this existed, a failed capture left no record of
		// any kind: `timetattle status` counted the files still lying in the
		// snapshot directory from the last run that worked, and printed a healthy
		// number for 44 hours while nothing was being preserved. See capture.go for
		// why the recording happens afterwards rather than around each capture, and
		// migration 0007 for why it is not a harvest_run row.
		var (
			lost     []error
			captures []captureOutcome
		)

		// knowledgeC goes first. It is the source with the hardest deadline.
		kc := captureBegun(captureKindKnowledgeC, *sourcePath)
		res, kcErr := snapshot.Take(*sourcePath, *dir, time.Now())
		kc = kc.captureEnded(kcErr == nil, kcErr)
		if kcErr != nil {
			lost = append(lost, fmt.Errorf("knowledgeC capture: %w", kcErr))
			fmt.Fprintf(out, "knowledgeC: capture lost (%v)\n", kcErr)
		} else {
			// One file per run, always: a knowledgeC snapshot is named for the
			// instant it was taken rather than content-addressed, so unlike Biome
			// and the vault it never finds anything already held.
			kc.filesNew, kc.bytes = 1, res.Bytes
			fmt.Fprintf(out, "%s\n%d rows, %.1f MB\n", res.Path, res.Rows, float64(res.Bytes)/(1<<20))
		}
		captures = append(captures, kc)

		// TakeStreams returns what it captured alongside whatever it could not, so
		// the results are printed whether or not a stream failed.
		biome := captureBegun(captureKindBiome, *biomeRoot)
		streams, biomeErr := snapshot.TakeStreams(*biomeRoot, snapshot.BiomeDir(*dir))
		// No results and no error is a machine with no such stream, which is absent
		// rather than a silent success. A stream that WAS read contributes a result
		// even when every one of its segments was already held.
		biome = biome.captureEnded(len(streams) > 0, biomeErr)
		if biomeErr != nil {
			lost = append(lost, fmt.Errorf("Biome capture: %w", biomeErr))
			fmt.Fprintf(out, "biome: capture lost (%v)\n", biomeErr)
		}
		for _, s := range streams {
			biome.filesNew += len(s.Captured)
			biome.filesHeld += s.Unchanged
			biome.bytes += s.Bytes
			fmt.Fprintf(out, "%s: %s captured, %d unchanged, %.1f MB\n",
				s.Stream, plural(len(s.Captured), "segment"), s.Unchanged, float64(s.Bytes)/(1<<20))
		}
		captures = append(captures, biome)

		// Motion's vault goes next, ahead of the filedays scan: its pruning
		// trigger is unconfirmed (job task g7v07), so a composition can vanish for
		// a reason nothing here understands, whereas mtime evidence decays only
		// gradually, one file at a time, as later work revisits it. The more
		// unpredictable loss goes first.
		vault := captureBegun(captureKindVault, vaultRoot)
		vaultRes, vaultErr := snapshot.PreserveVault(vaultRoot, snapshot.VaultDir(*dir))
		// A nil result with no error is PreserveVault's way of saying the root is
		// simply not there, which is what "no Motion on this machine" looks like.
		// It is recorded as absent rather than left unrecorded: a harvest that
		// looked and found nothing has observed something, and saying nothing about
		// it is indistinguishable from a harvest that never reached the vault.
		vault = vault.captureEnded(vaultRes != nil, vaultErr)
		if vaultErr != nil {
			lost = append(lost, fmt.Errorf("Motion vault capture: %w", vaultErr))
			fmt.Fprintf(out, "motion vault: capture lost (%v)\n", vaultErr)
		}
		if vaultRes != nil {
			vault.filesNew, vault.filesHeld, vault.bytes = len(vaultRes.Captured), vaultRes.Unchanged, vaultRes.Bytes
			fmt.Fprintf(out, "motion vault: %s captured, %d unchanged, %.1f MB\n",
				plural(len(vaultRes.Captured), "file"), vaultRes.Unchanged, float64(vaultRes.Bytes)/(1<<20))
		}
		captures = append(captures, vault)

		// Filesystem mtimes are the last capture, and what is written is a count
		// with the moment it was observed rather than the files themselves — see
		// internal/filedays's package doc for why a bare count would be
		// meaningless. Take is already best-effort across its roots and returns a
		// snapshot of whatever it could scan beside the error, so both are used.
		fd := captureBegun(captureKindFiledays, listOrNone(filedaysRoots))
		fdRes, fdErr := filedays.Take(filedaysRoots, splitCommaList(*filedaysExclude), *dir, time.Now())
		// No roots configured returns (nil, nil), which is absent for the same
		// reason an uninstalled Motion is: nothing was there to scan, and that is a
		// reading rather than a gap in the record.
		fd = fd.captureEnded(fdRes != nil, fdErr)
		if fdErr != nil {
			lost = append(lost, fmt.Errorf("filedays capture: %w", fdErr))
			fmt.Fprintf(out, "filedays: capture lost (%v)\n", fdErr)
		}
		if fdRes != nil {
			// One snapshot file per run, holding every root it managed to scan.
			fd.filesNew = 1
			fmt.Fprintln(out, fdRes.Path)
			for _, r := range fdRes.Snapshot.Roots {
				fmt.Fprintf(out, "%s: %s (raw), %s (filtered)\n",
					r.Root, plural(len(r.Raw), "day"), plural(len(r.Filtered), "day"))
			}
		}
		captures = append(captures, fd)

		// Ingestion is a separate phase, and it runs whatever the capture phase
		// managed. Nothing here can lose evidence — the snapshots are already on
		// disk and can be re-read for ever — so a parser bug costs a re-run rather
		// than history. The filedays snapshot does not feed ingestion: it is raw
		// preservation, with no source.Source shaped to read it back into events.
		// The vault capture above is different: it is a copy for disaster
		// recovery, taken because Motion's own pruning trigger is unconfirmed, but
		// the events attributed to Motion documents are read from the manifest's
		// own files.root directly (manifestIngestions, below), not from the copy.
		//
		// A store that cannot be opened is the one failure that does stop things
		// here, and it is not a capture: there is nothing to ingest into, and no
		// harvest_run row can be written to record that. The captures above are
		// already safe on disk regardless.
		st, err := store.Open(*storePath)
		if err != nil {
			return harvestFailure(errors.Join(append(lost, err)...))
		}
		defer st.Close()

		// The captures above are now recorded, which is the first moment they can
		// be: they ran before the store was opened, deliberately, so that a
		// database which cannot be opened can never delay the one phase that cannot
		// be repeated. A store that could not be opened therefore loses the record
		// and not the evidence — and status reports the missing record as a problem
		// rather than printing a healthy file count over it.
		ctx := context.Background()
		captureErr := captureRecord(ctx, st, out, captures)

		// The four knowledgeC streams are planned either way. When the capture
		// succeeded they read the snapshot this run just wrote; when it failed
		// they read nothing, and each records against itself why. Dropping them
		// from the plan instead would leave no row at all, and a source with no
		// row cannot be told apart from a source that ran and found nothing —
		// which is the single distinction this store exists to preserve.
		var (
			plan    []ingestion
			planErr error
		)
		if kcErr != nil {
			plan, planErr = registeredIngestions()
			plan = append(plan, lostSnapshotIngestions(*sourcePath, kcErr)...)
		} else {
			plan, planErr = harvestIngestions(res.Path)
		}
		// A directory can be registered and then have its marker deleted or
		// corrected between one harvest and the next; List re-verifies each
		// entry against the marker actually on disk rather than trusting the
		// index at face value, so this always reflects the current registry,
		// not whatever it said the last time a harvest ran.
		markers, listErr := registry.List(registryIndex)
		if fs := fsobserveIngestion(markers); fs != nil {
			plan = append(plan, *fs)
		}
		plan = append(plan, gitlogIngestions(markers)...)
		plan = append(plan, jobIngestions(markers)...)
		manifestPlan, manifestErr := manifestIngestions()
		plan = append(plan, manifestPlan...)
		ingestErr := ingest(ctx, st, plan, out)
		// Resolution runs whether or not every source came through cleanly: a
		// source that failed still leaves whatever it already wrote, and those
		// events deserve the same attribution pass as everything else. It also
		// runs on every harvest rather than only when new locators appear,
		// because registering a directory after the events under it already
		// exist is the ordinary way someone starts tracking a project retroactively,
		// and that repair has to happen somewhere.
		// markers carries the former paths declared by 'timetattle consolidate'
		// as well as the directories to plan sources for: evidence recorded under
		// a path that was later renamed away cannot be climbed to, so the marker
		// that owns it is found across the registry instead of up the tree.
		resolveErr := resolveProjects(ctx, st, markers, out)
		return harvestFailure(errors.Join(append(lost, captureErr, planErr, listErr, manifestErr, ingestErr, resolveErr)...))
	}
	return cmd
}

// harvestFailure puts a size and a next step in front of an aggregated failure.
//
// errors.Join renders one error per line and nothing else. A harvest now reads
// a dozen sources, and their messages run to several lines each — the
// knowledgeC permission error alone is six, most of them advice about System
// Settings. Joined raw, the first thing anyone sees in
// ~/.timetattle/harvest.err is the middle of one source's story with no way to
// tell whether it is the only one.
//
// So the first line says how much went wrong and where the whole answer lives,
// and the joined detail follows unchanged beneath it: one labelled entry per
// source, nothing summarised away, every failure still named. The count is of
// leaves rather than of sources, which over-reports only in the case where a
// source was lost AND recording that loss itself failed — two problems, which
// is what it then says.
func harvestFailure(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("this run had %s; run \"timetattle status\" for what is missing and since when\n%w",
		plural(countFailures(err), "problem"), err)
}

// countFailures counts the leaves of a joined error tree. errors.Join nests
// when a joined error is joined again, which is exactly what harvest does with
// ingest's result, so counting the top level alone would report one problem
// for a phase that lost six sources.
func countFailures(err error) int {
	joined, ok := err.(interface{ Unwrap() []error })
	if !ok {
		return 1
	}
	n := 0
	for _, e := range joined.Unwrap() {
		n += countFailures(e)
	}
	return n
}

// defaultVaultRoot resolves -motion-vault when it is left unset, by reading
// it out of the motion-autosave manifest source rather than keeping a second
// hardcoded copy of the path that could drift from it.
//
// This matters beyond tidiness: docs/content/docs/sources.md documents that a
// manifest overriding motion-autosave replaces its files.root outright, which
// is exactly how someone with the vault on an external volume is expected to
// relocate it. A hardcoded default would keep preserving the wrong directory
// for that person, silently, for as long as they ran harvest.
func defaultVaultRoot() (string, error) {
	sources, err := manifest.LoadAll("")
	for _, s := range sources {
		if s.ID == "motion-autosave" {
			return s.Files.Root, nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("resolve the default Motion vault root: %w", err)
	}
	return "", errors.New("no motion-autosave source is configured in any manifest; pass -motion-vault explicitly")
}

// resolveFiledaysRoots turns -filedays-roots into the list Take should scan.
//
// An explicit flag value replaces the config file entirely rather than
// merging with it, the same posture -dir and -store take toward their own
// defaults: one source of truth per invocation, not two that can disagree.
func resolveFiledaysRoots(flagValue string) ([]string, error) {
	if flagValue == "" {
		path, err := filedays.DefaultRootsFile()
		if err != nil {
			return nil, err
		}
		return filedays.ReadRootsFile(path)
	}
	var roots []string
	for r := range strings.SplitSeq(flagValue, ",") {
		r = strings.TrimSpace(r)
		if r == "" {
			continue
		}
		expanded, err := filedays.ExpandHome(r)
		if err != nil {
			return nil, err
		}
		roots = append(roots, expanded)
	}
	return roots, nil
}
