// Command timetattle reconstructs when work happened, and for whom, from the
// traces work leaves behind.
//
// Harvest is the subcommand everything else waits on. Two macOS stores delete
// themselves on a rolling window — knowledgeC, the only source that directly
// observes presence, and Biome's ScreenTime.AppUsage stream, which is now the
// only source of application attribution since knowledgeC's /app/usage stopped
// writing. Capture is the one task that cannot wait; every other source
// Timetattle reads is durable and can be parsed at any time. The remaining
// subcommands are consequently all readers or re-runnable derivations:
// register blesses a directory, status reports whether capture still works,
// and sessionize and chains derive from what harvest already stored.
//
// adjust is the one exception to "readers or derivations", and it is not an
// edit either. It appends a human correction keyed to a wall-clock span, in
// tables no derivation reads or writes, so a rebuild of derived_session cannot
// touch one and a correction cannot contaminate the machine's own answer.
//
// # How this package is laid out, and how helpers are named
//
// One file per subcommand — harvest.go, register.go, status.go,
// sessionize.go, chains.go, metrics.go, fit.go, adjust.go — each holding that subcommand's entry
// point, its flags and its own helpers. main.go holds dispatch and usage and nothing
// else. ingest.go is the exception that proves the shape: it holds the
// ingestion plan and runner, which harvest drives but does not own, and which
// have their own tests.
//
// Go package scope spans every file in this directory, so the file a helper
// sits in does not protect its name. Two agents working in parallel on
// 2026-07-31 each declared report() and parseBoundary() at package level and
// the merged tree did not compile. The compile error was the lucky half: the
// two parseBoundary implementations accepted different date formats, so had
// they been named apart, -from would quietly have meant one thing to
// sessionize and another to chains. Splitting files does nothing about either
// hazard. The naming rule is what does:
//
//   - A helper only one subcommand uses is named for that subcommand:
//     reportChains and reportSessions rather than two report()s,
//     harvestFailure, harvestIngestions. A bare verb in a subcommand's file is
//     a name claimed from the whole package.
//   - A helper genuinely shared by several subcommands moves out to a file
//     named for its concern, and keeps its bare name there: parse.go for flag
//     values (parseBoundary, splitCommaList), format.go for output (plural,
//     round, listOrNone), attribution.go for the project-resolution pass. Move
//     it on the second caller, and never to main.go — dispatch owns no
//     vocabulary. round and listOrNone moved out of sessionize.go on exactly
//     that trigger when metrics.go arrived.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/pprof"
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, "", "")) }

// run is dispatch, and returns the process's exit status rather than calling
// os.Exit, so that the statuses can be asserted on.
//
// # THE BARE INVOCATION SUCCEEDS
//
// `timetattle` with no arguments used to print the usage and exit 2 —
// usage-as-error — and that is now wrong twice over. It is wrong because the
// bare command produces the output it is designed to produce: the headline for
// whatever project the working directory belongs to, and then the usage. And it
// was already inconsistent, because `timetattle -h` prints the same usage and
// exits 0.
//
// The status must NOT depend on the directory. A 0 inside a registered project
// and a 2 outside one would make every script wrapping this command behave
// differently according to where it was run from, which is a worse trap than
// either fixed value: it would fail in exactly the case nobody tested. An
// UNKNOWN COMMAND is still 2, because that genuinely is a usage error.
//
// storePath and configPath are the bare command's only way to be pointed
// somewhere else. Every other subcommand has flags of its own to carry them,
// and the bare invocation by definition has no arguments at all; main passes
// them empty, which resolves the defaults under the home directory.
func run(args []string, stdout, stderr io.Writer, storePath, configPath string) int {
	if path := os.Getenv("TIMETATTLE_CPUPROFILE"); path != "" {
		stop, err := startCPUProfile(path)
		if err != nil {
			fmt.Fprintf(stderr, "timetattle: cpuprofile: %v\n", err)
			return 1
		}
		defer stop()
	}
	if len(args) == 0 {
		if err := bare(stdout, storePath, configPath); err != nil {
			fmt.Fprintf(stderr, "timetattle: %v\n", err)
			return 1
		}
		return 0
	}
	return dispatch(args, stdout, stderr)
}

// startCPUProfile begins a pprof CPU profile at path and returns a func that
// stops it and closes the file. It lives in `run`, wrapping every subcommand
// uniformly, rather than in an individual subcommand's own main, because the
// whole point is to profile ANY subcommand without that subcommand knowing
// profiling exists. scripts/profile is the re-runnable harness that drives
// this — see its doc comment for why a throwaway `sample` session is not
// good enough here.
func startCPUProfile(path string) (func(), error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		pprof.StopCPUProfile()
		f.Close()
	}, nil
}

// dispatch runs a named subcommand.
//
// Each subcommand owns its own output and writes to os.Stdout itself; the
// writers here are for what DISPATCH says, which is the usage and the line
// naming a command that failed. Rewiring thirteen subcommands to take a writer
// would be a change to every one of them for the benefit of this one function,
// and each already has a *To variant its own tests use.
func dispatch(args []string, stdout, stderr io.Writer) int {
	fail := func(name string, err error) int {
		fmt.Fprintf(stderr, "timetattle %s: %v\n", name, err)
		return 1
	}
	name, rest := args[0], args[1:]
	switch name {
	case "orient":
		if err := orient(rest); err != nil {
			return fail(name, err)
		}
	case "harvest":
		if err := harvest(rest); err != nil {
			return fail(name, err)
		}
	case "register":
		if err := register(rest); err != nil {
			return fail(name, err)
		}
	case "consolidate":
		if err := consolidateCommand(rest); err != nil {
			return fail(name, err)
		}
	case "status":
		if err := status(rest); err != nil {
			return fail(name, err)
		}
	case "sessionize":
		if err := sessionize(rest); err != nil {
			return fail(name, err)
		}
	case "chains":
		if err := chainsCommand(rest); err != nil {
			return fail(name, err)
		}
	case "metrics":
		if err := metricsCommand(rest); err != nil {
			return fail(name, err)
		}
	case "review":
		if err := review(rest); err != nil {
			return fail(name, err)
		}
	case "fit":
		if err := fitCommand(rest); err != nil {
			return fail(name, err)
		}
	case "adjust":
		if err := adjust(rest); err != nil {
			return fail(name, err)
		}
	case "version":
		if err := versionCommand(rest); err != nil {
			return fail(name, err)
		}
	case "-h", "--help", "help":
		// Asked for on purpose, so it goes to stdout and succeeds. It used to
		// go to stderr, which meant `timetattle -h | less` showed nothing.
		usageTo(stdout)
	default:
		fmt.Fprintf(stderr, "timetattle: unknown command %q\n\n", name)
		usageTo(stderr)
		return 2
	}
	return 0
}

// usageTo prints the map of the tool. The bare invocation prints it BENEATH the
// headline for whatever project the working directory belongs to, and prints it
// unconditionally: the answer changes with the directory and the map must not.
func usageTo(out io.Writer) {
	fmt.Fprint(out, `timetattle — reconstruct when work happened, and for whom

Usage:
  timetattle orient              Say what the state is and what to do next
  timetattle harvest [flags]     Preserve perishable evidence, and ingest it
  timetattle status [flags]      Report whether the harvest is still working
  timetattle register [flags]    Bless a directory with a client and project
  timetattle consolidate [flags] Declare a directory renamed or moved away as part of a registered one
  timetattle sessionize [flags]  Group events into sittings and store them
  timetattle chains [flags]      Reconstruct save-as chains from filenames
  timetattle metrics [flags]     Report engaged time per project, and presence
  timetattle review [flags]      Confirm a whole day at a time, and correct it
  timetattle fit [flags]         Search the clustering parameters against the corrections
  timetattle adjust [flags]      Correct a span by hand, or say you reviewed it
  timetattle version             Print the commit this binary was built from

Run orient first. It takes no arguments, says what the state is and names the
single action worth taking, and is the one command that assumes no memory of a
previous session. Three commands answer three different questions and none of
them answers another's: orient says what to DO, status says whether collection
is still WORKING, metrics says what the NUMBERS are.

Observations are harvested and never edited; sessionize and chains derive from
them and can be re-run at any time without touching what was observed. orient,
status and metrics read and write nothing, and status is the one to run when a
day looks empty.

review and adjust are the two commands a person writes through, and review is
the one to reach for. It puts a whole working day on one screen and confirms it
in a single keystroke; adjust is the same corrections one span at a time, for a
correction you already know you want. Both write an overlay keyed to wall clock,
never an edit: sessionize can rebuild every session row underneath them and they
still mean the same thing.

Run "timetattle <command> -h" for a command's flags.
`)
}
