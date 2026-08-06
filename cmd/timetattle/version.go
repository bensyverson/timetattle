package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

// version exists so a released binary is traceable to a commit — see
// scripts/release.sh, which is the reason this file exists. It deliberately
// carries no version string or commit hash of its own to hardcode; those come
// from the VCS stamp the Go toolchain embeds automatically (since 1.18) when
// a binary is built from within a git checkout. `go version -m <binary>`
// shows the same information from outside the process; this command is that,
// surfaced for someone who does not have the Go toolchain installed.
//
// readBuildInfo is a var, not a direct call to debug.ReadBuildInfo, purely so
// version_test.go can substitute a fixed value — the real one depends on
// working-tree state and would make the tests flaky.
var readBuildInfo = debug.ReadBuildInfo

func versionCommand(args []string) error { return versionTo(os.Stdout, args) }

func versionTo(stdout io.Writer, args []string) error {
	fs := flag.NewFlagSet("version", flag.ExitOnError)
	fs.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage: timetattle version

Prints the version this binary was built from: the module pseudo-version, the
git commit it was built at, and whether the working tree had uncommitted
changes at build time. Takes no arguments.

The commit comes from the VCS stamp the Go toolchain embeds automatically, not
from anything this command computes, so it is only as trustworthy as the build
that produced the binary. A binary built with -buildvcs=false, or built
outside a git checkout entirely, reports the commit as unknown rather than
guessing.
`)
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	info, ok := readBuildInfo()

	revision := "unknown"
	built := "unknown"
	dirty := false
	haveRevision := false
	if ok && info != nil {
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				revision = s.Value
				haveRevision = true
			case "vcs.time":
				built = s.Value
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}

	version := "(unknown)"
	if ok && info != nil && info.Main.Version != "" {
		version = info.Main.Version
	}

	fmt.Fprintf(stdout, "timetattle %s\n", version)
	if haveRevision && dirty {
		fmt.Fprintf(stdout, "commit  %s (dirty)\n", revision)
	} else {
		fmt.Fprintf(stdout, "commit  %s\n", revision)
	}
	fmt.Fprintf(stdout, "built   %s\n", built)

	return nil
}
