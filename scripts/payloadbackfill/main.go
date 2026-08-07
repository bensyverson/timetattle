// Command payloadbackfill fills the session and token payload onto the
// transcript rows that were stored before the store had anywhere to put it.
//
//	go run ./scripts/payloadbackfill                  # dry run: reports, writes nothing
//	go run ./scripts/payloadbackfill -dry-run=false   # the real pass
//	go run ./scripts/payloadbackfill -root /path/to/projects -store /path/to/timetattle.db
//
// # Why this is a script and not a timetattle subcommand
//
// cmd/timetattle carries the verbs of the working week — harvest, metrics,
// review, status, register — things that are run again and again and whose
// meaning a user has to hold in their head. This is not one of those. It is a
// ONE-SHOT REPAIR of a boundary in the store's own history: transcript rows
// stored before migration 0014 carry no payload, so session counts and token
// sums read as "nothing was recorded" for everything before that date, and this
// pass ends that discontinuity once. After it has run there is nothing for it
// to do, for ever, and scripts/ is where this project already keeps tools of
// exactly that shape — scripts/restorebiome rebuilds a stream, and
// scripts/transcriptreconcile subtracts this same corpus from this same store.
//
// The second reason is louder. This is the first thing in Timetattle that
// CHANGES a stored observation, and the store's whole design says an event is
// never rewritten. Putting it in the daily command set would advertise that as
// an ordinary operation and invite a fifth caller; leaving it here says what it
// is. The judgement it needs is a judgement, not a habit.
//
// What is NOT a script is the pass itself. internal/store owns the matching,
// the transaction and the guard, and is tested red-first there, because the
// spelling of the identity match is the same fact as the spelling of the unique
// index and the two must live in one package. This file is the operator's
// surface: it resolves a corpus and a store, runs the collector, calls the pass,
// and prints the ledgers.
//
// # The dry run is the real run with the write withheld
//
// -dry-run defaults to TRUE, so running this command by accident does nothing,
// and its counts are a prediction rather than an estimate: the same traversal,
// the same matching and the same classification produce them, and only the
// UPDATE is withheld. A dry run also opens the store READ-ONLY, so "it wrote
// nothing" is a property of the connection and not of a flag somebody read
// correctly.
//
// # What the counts mean, since three of them look alike
//
//   - UNSTORED records were read from disk and match no row. They are ordinary
//     on a live machine: they were written since the last harvest. This pass
//     does not store them — a backfill fills rows and a harvest makes them.
//   - NOTHING TO STATE means the row was re-read and its record said nothing
//     about a session or tokens. It keeps its NULL.
//   - UNMATCHED means no record on disk spoke for the row at all: its transcript
//     was deleted or rewritten, or the root read here is not the one it was
//     harvested from. It keeps its NULL too, and that is the point — a row
//     nobody re-read must not become a zero.
//
// The two cannot be separated further, and the reason is a property of the
// store rather than a shortcoming here: an event records the working directory
// its record named, never the file it was read from, so no row can be traced
// back to a file that is or is not still on disk. A "missing files" count would
// have to be invented, and this project has a rule against exactly that.
//
// # What it prints, and what it does not
//
// Counts, a root and a store path, and no session id, no working directory and
// no project name — the same rule scripts/sessioncensus follows, so the output
// can be quoted into a published document without re-importing what the
// confidentiality pass removed.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/transcripts"
)

func main() {
	root := flag.String("root", "", "transcript root to re-read (default: the collector's own, ~/.claude/projects)")
	dbPath := flag.String("store", "", "store to fill (default: ~/.timetattle/timetattle.db)")
	dryRun := flag.Bool("dry-run", true, "report what would be filled and write nothing; pass -dry-run=false to write")
	flag.Parse()

	if err := run(*root, *dbPath, *dryRun); err != nil {
		fmt.Fprintln(os.Stderr, "payloadbackfill:", err)
		os.Exit(1)
	}
}

func run(root, dbPath string, dryRun bool) error {
	if root == "" {
		resolved, err := transcripts.DefaultRoot()
		if err != nil {
			return err
		}
		root = resolved
	}
	if dbPath == "" {
		resolved, err := store.DefaultPath()
		if err != nil {
			return err
		}
		dbPath = resolved
	}

	ctx := context.Background()
	started := time.Now()

	// The real collector, and its own replay resolution with it. Re-implementing
	// its rules here would fill the store with THIS file's idea of them, and the
	// whole claim of the pass is that a backfilled store equals a store
	// harvested with payloads from the start.
	offered, err := transcripts.NewReader(root).Collect(ctx, time.Time{})
	if err != nil {
		return fmt.Errorf("read the corpus at %s: %w", root, err)
	}
	read := time.Since(started)

	// A dry run cannot write, rather than choosing not to. A real run opens the
	// store the ordinary way, which is also what applies migration 0014 — whose
	// UPDATE trigger is what refuses a payload that is not a JSON object.
	open := store.Open
	if dryRun {
		open = store.OpenReadOnly
	}
	st, err := open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	before, err := st.Usage(ctx, store.UsageFilter{Source: transcripts.ID})
	if err != nil {
		return fmt.Errorf("read the roll-up before the pass: %w", err)
	}

	filled, err := st.BackfillPayloads(ctx, offered, store.BackfillOptions{
		Source: transcripts.ID,
		DryRun: dryRun,
	})
	if err != nil {
		return err
	}

	after, err := st.Usage(ctx, store.UsageFilter{Source: transcripts.ID})
	if err != nil {
		return fmt.Errorf("read the roll-up after the pass: %w", err)
	}

	report(root, dbPath, read, time.Since(started), filled, before.Observations, after.Observations)
	return nil
}

func report(root, dbPath string, read, total time.Duration, b store.Backfill, before, after int) {
	fmt.Printf("root:  %s\n", root)
	fmt.Printf("store: %s\n", dbPath)
	if b.DryRun {
		fmt.Print("mode:  DRY RUN, through a read-only connection; nothing can be written\n\n")
	} else {
		fmt.Print("mode:  WRITING\n\n")
	}

	fmt.Println("RECORDS OFFERED (internal/transcripts, after its own replay resolution)")
	fmt.Printf("  records read                          %9d\n", b.Offered)
	fmt.Printf("    matched a stored row                %9d\n", b.Matched)
	fmt.Printf("      fillable                          %9d\n", b.Fillable)
	fmt.Printf("      nothing to state                  %9d\n", b.NothingToState)
	fmt.Printf("      already carrying a payload        %9d\n", b.AlreadyCarrying)
	fmt.Printf("    matched no row                      %9d\n", b.Unstored)
	fmt.Printf("    repeat offers of one identity       %9d\n", b.RepeatOffers)
	fmt.Printf("      disagreeing with the first        %9d\n\n", b.DisagreeingRepeatOffers)

	fmt.Printf("ROWS CARRYING NO PAYLOAD (%s, counted before the pass)\n", transcripts.ID)
	fmt.Printf("  rows                                  %9d\n", b.RowsWithoutAPayload)
	fmt.Printf("    fillable                            %9d\n", b.Fillable)
	fmt.Printf("    nothing to state (stay NULL)        %9d\n", b.NothingToState)
	fmt.Printf("    unmatched (stay NULL)               %9d\n\n", b.Unmatched)

	fmt.Printf("  rows filled by this pass              %9d\n\n", b.Filled)

	// The roll-up is an independent measurement of the same event, and this
	// project's rule is that two tools measuring one thing are subtracted rather
	// than left side by side.
	fmt.Println("CROSS-CHECK against the usage roll-up")
	fmt.Printf("  observations before                   %9d\n", before)
	fmt.Printf("  observations after                    %9d\n", after)
	fmt.Printf("  difference                            %9d  (the pass filled %d)\n", after-before, b.Filled)
	if after-before != b.Filled {
		fmt.Println("\n  THESE DISAGREE. The roll-up reads canonical_event and this pass writes the")
		fmt.Println("  table, so a filled row that a later observation SUPERSEDED is counted by one")
		fmt.Println("  and not the other. Transcript records are points and are never superseded")
		fmt.Println("  today, so a difference here is a finding and not a rounding.")
	}

	fmt.Printf("\ncorpus read in %s, whole pass %s\n", read.Round(time.Millisecond), total.Round(time.Millisecond))

	switch {
	case b.DryRun:
		fmt.Println("\nDRY RUN: nothing was written. Re-run with -dry-run=false to fill the rows above.")
	case b.Filled == 0:
		fmt.Println("\nNothing was filled. If the rows above already carry payloads this pass is done and")
		fmt.Println("re-running it will keep saying so; a store with unmatched rows and no fillable ones")
		fmt.Println("means the evidence for them is no longer on disk, and they stay as they are.")
	default:
		fmt.Println("\nRe-running this now must report 0 filled and the same number already carrying a")
		fmt.Println("payload. That is the pass's own idempotence check and it costs one more read.")
	}

	if b.Unmatched < 0 {
		fmt.Println("\nNEGATIVE UNMATCHED COUNT. Rows carrying no payload were INSERTED while this pass")
		fmt.Println("ran, so a harvest was running at the same time. The rows filled are still correct;")
		fmt.Println("the ledger is not. Re-run it with nothing else touching the store.")
	}
}
