// Command transcriptreconcile subtracts the claude-transcripts corpus on disk
// from the claude-transcripts rows in the store, and shows every step of the
// subtraction.
//
// # Why this exists
//
// claude-transcripts is the largest source in the store by two orders of
// magnitude — 722,832 rows on 2026-08-01, against 27,210 for the next largest.
// Nothing had ever checked it against the files it reads. CLAUDE.md requires
// that two tools measuring the same thing be reconciled numerically, and that
// "two figures that differ for an understood reason are fine; two that nobody
// has subtracted are not". This is that subtraction, as a tool rather than as
// a shell pipeline, so the figures published in project/ can be reproduced by
// one command.
//
// # Why the obvious shell version is wrong
//
// The tempting one-liner counts distinct .uuid values among records carrying
// both a timestamp and a uuid, and treats the shortfall as the work of
// dedupeReplays. That is a PROXY, not the rule. dedupeReplays keys on
// (Document, Start) and resolves a State conflict between two readings of one
// record; it does not key on uuid at all. The two counts are close on this
// machine's corpus and are not the same measurement, and quoting one as the
// other attributes a number to a rule that did not produce it.
//
// This tool therefore runs the real collector and reports what it actually
// emitted, rather than re-implementing its rules in jq and hoping they match.
//
// # The four populations, and why each is separate
//
//   - LINES ON DISK. Every line of every *.jsonl under the root.
//   - UNPARSEABLE. A torn final line is the ordinary way an appended-to file
//     shows up mid-write, and is not evidence of corruption.
//   - UNDATED. Records carrying no timestamp or no uuid — mode, ai-title,
//     last-prompt, queue-operation and similar bookkeeping. These cannot
//     become a dated, dedup-safe observation and are dropped by design.
//   - COLLAPSED BY THE COLLECTOR. What dedupeReplays removed, measured as the
//     difference between what scanning produced and what Collect returned.
//
// # Reading the residual
//
// The residual is (collector output − store rows) and IS EXPECTED TO BE
// POSITIVE AND SMALL. These files are appended to by running Claude Code
// sessions, including the session running this tool, so records written since
// the last harvest exist on disk and not yet in the store.
//
//   - A LARGE positive residual means the harvest is stale or a harvest is
//     failing. Check "timetattle status" before concluding anything else.
//   - A NEGATIVE residual is the interesting one: the store holds events the
//     collector no longer produces. That means transcripts were deleted or
//     rewritten under it, or the collector's rules changed since the harvest.
//     The store is append-only and never forgets, so this is not self-healing.
//
// # What this does NOT measure
//
// Whether the events are CORRECT — only whether they are all present. It says
// nothing about whether a timestamp was parsed right, whether State was
// classified right, or whether attribution resolved. It also covers only
// claude-transcripts; every other source needs its own reconciliation and none
// has one yet.
//
// # Usage
//
//	go run ./scripts/transcriptreconcile
//	go run ./scripts/transcriptreconcile -root ~/.claude/projects -store ~/.timetattle/timetattle.db
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/transcripts"
)

func main() {
	root := flag.String("root", "", "transcript root to scan (default: the collector's own default, ~/.claude/projects)")
	dbPath := flag.String("store", "", "store to reconcile against (default: ~/.timetattle/timetattle.db)")
	flag.Parse()

	if err := run(*root, *dbPath); err != nil {
		fmt.Fprintf(os.Stderr, "transcriptreconcile: %v\n", err)
		os.Exit(1)
	}
}

func run(root, dbPath string) error {
	if root == "" {
		r, err := transcripts.DefaultRoot()
		if err != nil {
			return fmt.Errorf("resolving the default transcript root: %w", err)
		}
		root = r
	}
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolving home: %w", err)
		}
		dbPath = filepath.Join(home, ".timetattle", "timetattle.db")
	}

	disk, err := scanDisk(root)
	if err != nil {
		return err
	}

	// The real collector, not a re-implementation of its rules.
	collected, err := transcripts.NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		return fmt.Errorf("running the collector over %s: %w", root, err)
	}

	stored, err := storedCount(dbPath)
	if err != nil {
		return err
	}

	report(root, dbPath, disk, len(collected), stored)
	return nil
}

// diskCounts is what a plain read of the files says, independent of the
// collector. Counting these separately is the point: if the collector and an
// independent scan disagree about how many records are even datable, that is a
// finding in itself.
type diskCounts struct {
	files       int
	lines       int
	unparseable int
	undated     int
	datable     int
}

func scanDisk(root string) (diskCounts, error) {
	var c diskCounts

	// filepath.WalkDir calls os.Lstat on its OWN root argument and does not
	// follow symlinks, so a root that is itself a symlink is handed to the walk
	// function as a non-directory and never descended into — silently, with a
	// zero count and a nil error. That is the defect task eAVcU fixed across
	// three packages, and this tool had the same shape: it would have reported
	// a corpus of nothing while the collector it is checking read everything,
	// and the residual would have gone NEGATIVE, which its own doc comment
	// describes as the alarming case. A reconciliation tool that cannot survive
	// the layout it is reconciling is worse than none.
	//
	// os.Stat, unlike Lstat, follows the link.
	walkRoot := root
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil {
			return c, fmt.Errorf("resolving symlinked root %s: %w", root, err)
		}
		walkRoot = resolved
	}

	err := filepath.WalkDir(walkRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// Only regular files. A symlink named *.jsonl would otherwise be
		// counted as a transcript it is not, which is the fabricated-count half
		// of the same defect — the one that shipped live in filedays and
		// inflated every figure it produced by the symlink count under its root.
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") || !d.Type().IsRegular() {
			return nil
		}
		c.files++

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()

		sc := bufio.NewScanner(f)
		// Matching internal/transcripts: tool results are embedded inline and
		// the default 64 KB limit truncates whole lines rather than erroring.
		sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
		for sc.Scan() {
			line := sc.Bytes()
			if len(line) == 0 {
				continue
			}
			c.lines++

			var rec struct {
				Timestamp time.Time `json:"timestamp"`
				UUID      string    `json:"uuid"`
			}
			if err := json.Unmarshal(line, &rec); err != nil {
				c.unparseable++
				continue
			}
			if rec.Timestamp.IsZero() || rec.UUID == "" {
				c.undated++
				continue
			}
			c.datable++
		}
		return sc.Err()
	})

	return c, err
}

func storedCount(path string) (int, error) {
	st, err := store.OpenReadOnly(path)
	if err != nil {
		return 0, fmt.Errorf("opening %s read-only: %w", path, err)
	}
	defer st.Close()

	events, err := st.Events(context.Background(), store.Filter{Source: transcripts.ID})
	if err != nil {
		return 0, fmt.Errorf("reading %s events: %w", transcripts.ID, err)
	}
	return len(events), nil
}

func report(root, dbPath string, d diskCounts, collected, stored int) {
	fmt.Printf("root:  %s\n", root)
	fmt.Printf("store: %s\n\n", dbPath)

	fmt.Printf("%-42s %9d  in %d files\n", "lines on disk", d.lines, d.files)
	fmt.Printf("%-42s %9d\n", "  less unparseable (torn final lines)", -d.unparseable)
	fmt.Printf("%-42s %9d\n", "  less undated (no timestamp or uuid)", -d.undated)
	fmt.Printf("%-42s %9d\n", "datable records", d.datable)

	collapsed := d.datable - collected
	fmt.Printf("%-42s %9d\n", "  less collapsed by the collector", -collapsed)
	fmt.Printf("%-42s %9d\n", "events the collector emits", collected)
	fmt.Printf("%-42s %9d\n\n", "rows in the store", stored)

	residual := collected - stored
	fmt.Printf("residual (collector - store): %d\n", residual)

	switch {
	case residual < 0:
		fmt.Println("\nNEGATIVE RESIDUAL. The store holds events the collector no longer")
		fmt.Println("produces, so transcripts were deleted or rewritten, or the collector's")
		fmt.Println("rules changed since the harvest that wrote them. The store never")
		fmt.Println("forgets, so this will not resolve itself.")
	case residual == 0:
		fmt.Println("\nExactly reconciled. Note this is the SUSPICIOUS case on a machine")
		fmt.Println("with live Claude Code sessions, where a small positive residual is")
		fmt.Println("normal; zero means nothing has been written since the last harvest.")
	default:
		fmt.Println("\nPositive residual is expected: these files are appended to by running")
		fmt.Println("sessions, including this one. Run 'timetattle status' if it looks large")
		fmt.Println("relative to how long ago the last harvest ran.")
	}
}
