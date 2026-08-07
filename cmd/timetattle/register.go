package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/bensyverson/timetattle/internal/registry"
)

func register(args []string) error { return executeSubcommand(registerCmd(), args) }

func registerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "register",
		Short: "Bless a directory with a client and project",
		Long: `Writes a marker naming a client and project into a directory, so evidence
found under it can be attributed without asking again. Run it once per
directory a project's work lives in — a repository root, a shared asset
folder, a scratch render directory outside any repository — using the same
client and project each time to bless more than one; a project is not
limited to one directory.

Identity lives in the directory itself, as a small JSON file named
.timetattle-project.json, rather than in a table kept elsewhere: moving or
cloning the directory carries the identity with it. Contract terms such as a
rate or a budget are never written here, so they can never be committed by
accident; they belong in global config.

Registering the same directory again with the same client and project is a
no-op. Registering it again with a different client or project is refused
unless -force is given.

ONE PROJECT NAME BELONGS TO ONE CLIENT. Registering a project name another
directory already holds for a DIFFERENT client is refused, and the refusal
names the directory and the client holding it. Two clients each with a project
called "website" would otherwise merge into one row in every report that groups
by project. Several directories sharing one client AND project is the case this
protects rather than blocks, and stays a plain success.

Renaming a client means re-registering each of its directories with -force, and
the refusal lists the ones still to do. While that is half done the directories
disagree, on purpose; each event keeps the client its own directory names, so
reports stay correct throughout rather than breaking in the middle.

The directory is also recorded in a small index at -index, separate from the
marker itself, so 'timetattle harvest' can find every registered directory
without searching the whole disk for markers. If that index cannot be
written, the marker is still written and this command still succeeds; only
automatic discovery by harvest is affected, and re-running register later
retries the index too.`,
	}
	fs := cmd.Flags()
	client := fs.String("client", "", "who the work in this directory is for")
	project := fs.String("project", "", "the project within client that this directory belongs to")
	path := fs.String("path", "", "directory to bless (default: the current directory)")
	force := fs.Bool("force", false, "reassign a directory already registered to a different client or project")
	indexPath := fs.String("index", "",
		"registry index to record this directory in, for 'timetattle harvest' to find later (default: ~/.timetattle/registered-directories.json)")
	storePath := fs.String("store", "", "event store holding the client-project log (default: ~/.timetattle/timetattle.db)")
	since := fs.String("since", "",
		"the client changed on this date: work before it keeps the previous client, as 2026-07-01")
	correction := fs.Bool("correction", false,
		"the previous client was wrong and never applied: rewrite the whole history of this project")
	cmd.RunE = func(*cobra.Command, []string) error {
		if *client == "" {
			return errors.New("timetattle register: -client is required")
		}
		if *project == "" {
			return errors.New("timetattle register: -project is required")
		}
		if *since != "" && *correction {
			return errors.New("timetattle register: -since and -correction say opposite things. " +
				"-since dates a real change, so earlier work keeps the client it was done for; " +
				"-correction says the earlier answer was never true and rewrites all of it. Pass one")
		}
		effectiveFrom, err := parseBoundary(*since, "since")
		if err != nil {
			return err
		}

		dir := *path
		if dir == "" {
			wd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("timetattle register: %w", err)
			}
			dir = wd
		}

		// The index is resolved and READ BEFORE registering, not after, because
		// Register now checks this registration against the others: all directories
		// naming one project have to agree on its client, or a report grouped by
		// project silently merges two clients' hours. Register deliberately reads no
		// machine-global path itself (see registry.RecordInIndex on why), so the
		// caller that already needs the index is the one that hands it over.
		idx := *indexPath
		if idx == "" {
			// A default that cannot be resolved is not fatal: the marker is the
			// fact and the index is a convenience. It costs the cross-directory
			// check its input, which is reported below rather than assumed away.
			if resolved, err := registry.DefaultIndexPath(); err == nil {
				idx = resolved
			}
		}
		// A registry that cannot be enumerated leaves the cross-directory check
		// with nothing to compare against, and that is reported rather than passed
		// off as agreement: "no conflict found" and "nothing was looked at" are
		// different answers, and this codebase has shipped the collapsed version of
		// that distinction three times.
		among, listErr := registry.List(idx)
		if listErr != nil {
			fmt.Fprintf(os.Stderr, "timetattle register: some registered directories could not be read (%v), "+
				"so the check that one project name belongs to one client saw only %d of them\n",
				listErr, len(among))
		}

		// What the directory said BEFORE this call, which is what decides whether an
		// intent flag is required. A directory nobody has blessed is a first
		// registration and needs none.
		previous, hadMarker, _ := registry.Resolve(dir)
		reassigning := hadMarker && previous != nil && previous.Path == dir &&
			previous.Client != *client

		// A REASSIGNMENT IS ASKED FOR, NEVER INFERRED, and this is the one place
		// the two meanings can be told apart — by the person typing.
		//
		// A FIRST registration is retroactive: the evidence under a directory
		// usually predates anybody getting round to blessing it, and an assertion
		// starting at the moment it was typed would attribute none of it. So if a
		// RE-registration silently defaulted to "from now", two near-identical
		// commands would mean opposite things and nothing would say which had
		// happened. The failure is silent in both directions: guessing "from now"
		// leaves old work under a client it was not done for, and guessing
		// "retroactive" rewrites work that WAS done for somebody else, which task
		// mjalb's criterion XZk forbids outright.
		if reassigning && *since == "" && !*correction {
			return fmt.Errorf("timetattle register: %s already belongs to %s, and moving it to %s "+
				"could mean either of two things, which cannot be guessed from here:\n"+
				"  -since 2026-07-01   the client CHANGED on that date. Work before it stays with "+
				"%s, which is what an invoice already sent depends on.\n"+
				"  -correction         %s was never right. Every hour this project has ever had "+
				"moves to %s.\n"+
				"Pass whichever is true",
				dir, previous.Client, *client, previous.Client, previous.Client, *client)
		}

		m, err := registry.Register(registry.Registration{
			Dir:     dir,
			Client:  *client,
			Project: *project,
			Force:   *force,
			Among:   among,
		})
		if err != nil {
			return err
		}
		fmt.Printf("%s registered to %s / %s\n", m.Path, m.Client, m.Project)

		if err := recordClientAssertion(*storePath, m, effectiveFrom); err != nil {
			fmt.Fprintf(os.Stderr, "timetattle register: %s was registered, but its client could not be "+
				"recorded in the client-project log (%v); reports will show its hours under no client "+
				"until a harvest records it\n", m.Path, err)
		}

		if idx == "" {
			fmt.Fprintf(os.Stderr, "timetattle register: %s was registered, but could not resolve the default "+
				"directory index; it will not be picked up by 'timetattle harvest' until this is fixed or "+
				"-index is passed explicitly\n", m.Path)
			return nil
		}
		// A failed index write does not fail registration: the marker is the
		// fact, the index is a convenience for enumeration that List always
		// re-verifies against the marker rather than trusts, and a directory
		// that could not be indexed this time is not lost — only harder for
		// harvest to discover automatically until a later register call
		// retries it. See internal/registry.RecordInIndex's doc for why this
		// is a separate, explicitly opt-in step rather than folded into
		// Register itself.
		if err := registry.RecordInIndex(idx, m.Path); err != nil {
			fmt.Fprintf(os.Stderr, "timetattle register: %s was registered, but could not be added to %s (%v); "+
				"it will not be picked up by 'timetattle harvest' until this is fixed\n", m.Path, idx, err)
		}
		return nil
	}
	return cmd
}
