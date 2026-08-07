package main

import (
	"errors"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// This file holds the argument handling every subcommand shares: the rules for
// turning an argument slice into a parsed *cobra.Command, and the one place
// this CLI departs from stock pflag.
//
// Each subcommand builds its own command with a constructor named for it —
// orientCmd, harvestCmd, metricsCmd — that returns a FRESH *cobra.Command with
// freshly bound flag variables every call. Cobra examples usually keep one
// package-level command value per subcommand; that would be wrong here,
// because this package's tests call the subcommands repeatedly in one process
// and a flag set to a value by one test would still hold it in the next.

// executeSubcommand parses args with cmd and runs it.
//
// It is the seam the per-subcommand entry points keep: their tests drive a
// subcommand directly with an argument slice rather than through the root
// command, exactly as they did when each subcommand owned a flag.FlagSet. cmd
// must therefore have no parent, or Cobra would route the call back up to the
// root and re-read os.Args.
func executeSubcommand(cmd *cobra.Command, args []string) error {
	prepareSubcommand(cmd)
	cmd.SetArgs(longFlags(cmd.Flags(), args))
	return cmd.Execute()
}

// prepareSubcommand applies the parsing and reporting rules every subcommand
// shares, whether it was entered through the root command or directly.
//
// Interspersed parsing is turned OFF because stdlib flag stopped parsing at
// the first non-flag argument and pflag does not. No subcommand reads a
// positional argument today, so the difference is invisible now; leaving it on
// would make it appear silently the first time one does.
//
// Errors and usage are silenced because dispatch prints both itself, in the
// wording and with the exit statuses main.go documents.
func prepareSubcommand(cmd *cobra.Command) {
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.Flags().SetInterspersed(false)
}

// subcommandNamed finds the root's child called name. Cobra's own resolution
// also matches aliases and unique prefixes; this deliberately does not, so
// that anything the usage does not name is an unknown command and gets the
// exit status main.go reserves for one.
func subcommandNamed(root *cobra.Command, name string) (*cobra.Command, bool) {
	for _, cmd := range root.Commands() {
		if cmd.Name() == name {
			return cmd, true
		}
	}
	return nil, false
}

// flagError marks an error as having come from parsing rather than from the
// work, so dispatch can exit 2 for it the way stdlib flag's ExitOnError did
// and 1 for everything else.
type flagError struct{ err error }

func (e flagError) Error() string { return e.err.Error() }
func (e flagError) Unwrap() error { return e.err }

func isFlagError(err error) bool {
	var e flagError
	return errors.As(err, &e)
}

// longFlags rewrites a single-dash long flag into the double-dash spelling
// pflag requires.
//
// Every flag in this CLI is documented, scripted and tested as -store, -from,
// -dry-run, because stdlib flag accepted one dash and two interchangeably.
// pflag does not: it reads -store as the bundled shorthands -s -t -o -r -e and
// rejects it. No command here defines a shorthand of any kind, so a single-dash
// token naming a registered flag is unambiguously that flag.
//
// The rewrite is flag-set aware rather than a blanket "-x becomes --x" because
// a VALUE may itself begin with a dash. stdlib flag consumed the argument after
// a value-taking flag unconditionally, and so does pflag, so `-exclude -weird`
// passes "-weird" as the value; blindly rewriting it would change what was
// passed. fs therefore says which names exist and which of them take a value —
// a boolean carries a NoOptDefVal and takes none.
//
// A name the flag set does NOT know is rewritten too, and then left for pflag
// to reject. It is a typo either way, and the two spellings produce different
// sentences: -storee reported as a long flag reads "unknown flag: --storee",
// which names what was mistyped, while the same token read as shorthands reads
// "unknown shorthand flag: 's' in -storee", which sends the reader looking for
// a shorthand this CLI has never had.
//
// Left alone: a lone "-", everything at and after a bare "--", a
// single-character flag such as -h, and everything from the first positional
// argument onwards, which is where flag parsing stopped before and where
// prepareSubcommand stops it now.
func longFlags(fs *pflag.FlagSet, args []string) []string {
	out := make([]string, 0, len(args))
	value := func(i int) int {
		if i+1 < len(args) {
			out = append(out, args[i+1])
			return i + 1
		}
		return i
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			return append(out, args[i:]...)
		case strings.HasPrefix(arg, "--"):
			name, _, joined := strings.Cut(arg[2:], "=")
			out = append(out, arg)
			if !joined && takesValue(fs, name) {
				i = value(i)
			}
		case len(arg) > 2 && arg[0] == '-':
			name, _, joined := strings.Cut(arg[1:], "=")
			out = append(out, "-"+arg)
			// An unknown name consumes nothing: pflag is about to refuse it,
			// and guessing whether it would have taken a value could swallow
			// the next real flag out of the message.
			if !joined && takesValue(fs, name) {
				i = value(i)
			}
		case len(arg) == 2 && arg[0] == '-':
			// A single-character flag: -h, and nothing else in this CLI.
			out = append(out, arg)
			if takesValue(fs, arg[1:]) {
				i = value(i)
			}
		default:
			// A lone "-", or the first positional argument. Flag parsing
			// stops here, so nothing after it may be rewritten.
			return append(out, args[i:]...)
		}
	}
	return out
}

// flagsSet names the flags actually given on the command line, as against left
// at their default. Several subcommands need that distinction because a zero
// value and an unset flag mean different things — sessionize's -gap of 0 is
// "take the threshold from the config file", not "no idle gap at all".
func flagsSet(fs *pflag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *pflag.Flag) { set[f.Name] = true })
	return set
}

// takesValue reports whether name is a registered flag that consumes the
// argument after it. A boolean does not: pflag gives it a NoOptDefVal, so
// -dry-run stands alone and the next argument is the next flag.
func takesValue(fs *pflag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	return f != nil && f.NoOptDefVal == ""
}
