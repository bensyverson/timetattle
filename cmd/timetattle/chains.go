package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/chains"
	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

func chainsCommand(args []string) error { return executeSubcommand(chainsCmd(), args) }

func chainsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "chains",
		Short: "Reconstruct save-as chains from filenames",
		Long: `Reconstructs save-as chains from filesystem observations, and replaces the
derived chain tables with the result. Inference rather than a source: it reads
the store, writes no events, and can be re-run after any harvest.`,
	}
	fs := cmd.Flags()
	storePath := fs.String("store", "", "event store to read and derive into (default: ~/.timetattle/timetattle.db)")
	sourceID := fs.String("source", fsobserve.ID, "source whose observations to cluster")
	classes := fs.String("class", string(fsobserve.ClassSource),
		"comma-separated file classes to consider: source, output, other")
	extensions := fs.String("ext", "",
		"comma-separated file extensions to restrict to, e.g. .blend (default: every extension the classes admit)")
	minPrefix := fs.Int("min-prefix", chains.DefaultMinSharedPrefix,
		"characters two names must share to be relatives where neither is the other's prefix")
	coincidence := fs.Duration("coincidence", chains.DefaultCoincidence,
		"how close two births must be to count as one action")
	from := fs.String("from", "", "consider observations at or after this time (2006-01-02 or RFC3339)")
	to := fs.String("to", "", "consider observations at or before this time")
	roots := fs.String("roots", "",
		"comma-separated directories to walk directly instead of reading the store; diagnostic, never writes")
	dryRun := fs.Bool("dry-run", false, "print the chains without replacing the derived tables")
	quiet := fs.Bool("quiet", false, "write nothing to stdout")
	cmd.RunE = func(*cobra.Command, []string) error {
		opts := chains.Options{
			Source:          *sourceID,
			Classes:         splitCommaList(*classes),
			Extensions:      splitCommaList(*extensions),
			MinSharedPrefix: *minPrefix,
			Coincidence:     *coincidence,
		}
		if opts.MinSharedPrefix < 1 {
			return errors.New("timetattle chains: -min-prefix must be at least 1")
		}
		if opts.Coincidence <= 0 {
			return errors.New("timetattle chains: -coincidence must be positive")
		}
		fromTime, err := parseBoundary(*from, "from")
		if err != nil {
			return err
		}
		toTime, err := parseBoundary(*to, "to")
		if err != nil {
			return err
		}

		out := io.Writer(os.Stdout)
		if *quiet {
			out = io.Discard
		}
		ctx := context.Background()

		if *roots != "" {
			// Deliberately does not write. These observations were never ingested,
			// so a derived table built from them would claim the store holds
			// evidence it does not, and the next honest rebuild would silently
			// contradict it.
			events, err := fsobserve.New(splitCommaList(*roots)...).Collect(ctx, time.Time{})
			if err != nil {
				return fmt.Errorf("walk %s: %w", *roots, err)
			}
			events = window(events, fromTime, toTime)
			reportChains(out, chains.Reconstruct(events, opts), *roots+" (walked directly; nothing was written)")
			return nil
		}

		path := *storePath
		if path == "" {
			if path, err = store.DefaultPath(); err != nil {
				return err
			}
		}
		// store.Open runs the migrations, which is what guarantees the derived
		// tables exist before anything writes to them.
		st, err := store.Open(path)
		if err != nil {
			return err
		}
		defer st.Close()

		events, err := st.Events(ctx, store.Filter{From: fromTime, To: toTime, Source: opts.Source})
		if err != nil {
			return err
		}
		result := chains.Reconstruct(events, opts)

		if len(events) == 0 {
			// Not the same as finding no chains, and the difference decides what
			// the reader should do next. Said on stderr so it survives -quiet:
			// silence here would read as "no chains", which is a conclusion nobody
			// is entitled to draw from an unharvested store.
			fmt.Fprintf(os.Stderr,
				"timetattle chains: %s holds no %s observations. Nothing was measured — "+
					"this is not a finding that no chains exist. Run 'timetattle harvest', "+
					"or pass -roots to look at the disk directly.\n", path, opts.Source)
		}

		reportChains(out, result, path)
		if *dryRun {
			fmt.Fprintln(out, "\ndry run: the derived tables were not replaced")
			return nil
		}

		// Four tables are cleared and repopulated by this one call, through the
		// handle store.Open already returned. The store owns the transaction, so
		// there is no state in which some of them describe this derivation and the
		// rest the last one — see store.ReplaceDerived.
		if err := st.ReplaceDerived(ctx, chains.Rebuild(result, time.Now())); err != nil {
			return err
		}
		fmt.Fprintf(out, "\n%s written to %s\n", plural(len(result.Chains), "chain"), path)
		return nil
	}
	return cmd
}

// window filters events to a span. store.Filter does this for a store read;
// -roots has no store to ask.
func window(events []source.Event, from, to time.Time) []source.Event {
	if from.IsZero() && to.IsZero() {
		return events
	}
	out := events[:0:0]
	for _, e := range events {
		if !from.IsZero() && e.Start.Before(from) {
			continue
		}
		if !to.IsZero() && e.Start.After(to) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// report prints a derivation.
//
// Unchained documents are printed rather than counted. A directory with no
// chain and a directory nobody looked at read identically from a chain list
// alone, and telling them apart is the whole reason the model records both.
func reportChains(out io.Writer, r chains.Result, subject string) {
	fmt.Fprintf(out, "%s\n", subject)
	fmt.Fprintf(out, "%s considered, %s, %d in a chain, %d in none\n\n",
		plural(r.EventsConsidered, "observation"), plural(r.Documents, "document"),
		r.Chained, len(r.Unchained))
	if r.BuildOutput > 0 {
		fmt.Fprintf(out, "%s of build output (.build/) not considered\n\n",
			plural(r.BuildOutput, "observation"))
	}

	for _, c := range r.Chains {
		fmt.Fprintf(out, "%s  %s  (%s, %s)\n", c.Root, c.Directory,
			plural(len(c.Steps), "step"), plural(c.Documents(), "document"))
		for i, s := range c.Steps {
			gap := ""
			if i > 0 {
				gap = "  (+" + short(s.Gap) + ")"
			}
			names := strings.Join(s.Documents, ", ")
			if len(s.Documents) > 1 {
				names = fmt.Sprintf("%s  [%d together, one action]", names, len(s.Documents))
			}
			fmt.Fprintf(out, "  %s  %s%s\n", s.At.Format("2006-01-02 15:04:05"), names, gap)
		}
		fmt.Fprintln(out)
	}

	if len(r.Unchained) > 0 {
		fmt.Fprintf(out, "in no chain (observed, not absent):\n")
		unchained := append([]string(nil), r.Unchained...)
		sort.Strings(unchained)
		for _, d := range unchained {
			fmt.Fprintf(out, "  %s\n", d)
		}
	}
}

// short renders a gap the way the published tables read: whole minutes for
// anything under a day, days beyond that. The exact duration stays in the
// database; this is for the eye.
//
// It ROUNDS to the unit it displays rather than truncating to it, so a rendered
// gap is never further from the truth than half that unit. Truncating is the
// obvious implementation and it is wrong here in a way that costs more than the
// second it hides: project/private/2026-07-28-spike-findings.md published this chain as
// +14m/+3m/+237m/+7m/+193m, and truncation renders the same underlying data as
// +14m/+2m/+237m/+7m/+192m. Two of five figures then disagree with a document
// this project has already published, and a reader comparing them has no way to
// tell a rendering convention from a changed model.
//
// Rounding can carry a value across the boundary that selected its own format,
// so the format is chosen from the ROUNDED value and never from the raw one.
// Otherwise 59m45s renders as "60m" instead of "1h00m", which is a worse bug
// than the one being fixed, because it is not a value the format can produce.
func short(d time.Duration) string {
	if d >= 48*time.Hour {
		return fmt.Sprintf("%.0fd", d.Hours()/24)
	}
	if d.Round(time.Second) < time.Minute {
		return fmt.Sprintf("%ds", int(d.Round(time.Second).Seconds()))
	}
	r := d.Round(time.Minute)
	if r >= time.Hour {
		return fmt.Sprintf("%dh%02dm", int(r.Hours()), int(r.Minutes())%60)
	}
	return fmt.Sprintf("%dm", int(r.Minutes()))
}
