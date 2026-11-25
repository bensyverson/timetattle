package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/spf13/cobra"
)

// version is the release number. scripts/release rewrites this line, and it is
// stated in the source because Homebrew builds from a tarball, which carries no
// VCS stamp.
const version = "0.1.0"

// The commit and build time are not hardcoded: they come from the VCS stamp the
// Go toolchain embeds when it builds from within a git checkout, and read as
// unknown otherwise.
//
// readBuildInfo is a var, not a direct call to debug.ReadBuildInfo, purely so
// version_test.go can substitute a fixed value — the real one depends on
// working-tree state and would make the tests flaky.
var readBuildInfo = debug.ReadBuildInfo

func versionCommand(args []string) error { return versionTo(os.Stdout, args) }

func versionTo(stdout io.Writer, args []string) error {
	return executeSubcommand(versionCmd(stdout), args)
}

func versionCmd(stdout io.Writer) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version and the commit this binary was built from",
		Long: `Prints the release number, the commit this binary was built at, and whether
the working tree was dirty at build time. The commit is the Go toolchain's VCS
stamp, so a build from a release tarball or with -buildvcs=false reports it as
unknown.`,
		RunE: func(*cobra.Command, []string) error { return versionRun(stdout) },
	}
}

func versionRun(stdout io.Writer) error {
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

	fmt.Fprintf(stdout, "timetattle %s\n", version)
	if haveRevision && dirty {
		fmt.Fprintf(stdout, "commit  %s (dirty)\n", revision)
	} else {
		fmt.Fprintf(stdout, "commit  %s\n", revision)
	}
	fmt.Fprintf(stdout, "built   %s\n", built)

	return nil
}
