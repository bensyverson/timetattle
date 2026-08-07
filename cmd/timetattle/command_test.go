package main

import (
	"bytes"
	"slices"
	"testing"
	"time"

	"github.com/spf13/pflag"
)

// longFlags is the one piece of this CLI's argument handling that is not
// Cobra's. Every flag in the documentation, in scripts/ and in every test in
// this package is spelled with ONE dash — -store, -from, -dry-run — because
// stdlib flag accepted one dash and two interchangeably. pflag does not: it
// reads -store as the bundled shorthands -s -t -o -r -e and fails. These cases
// are the contract that keeps every documented invocation working.
func longFlagsTestSet() *pflag.FlagSet {
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	fs.String("store", "", "")
	fs.String("from", "", "")
	fs.String("exclude", "", "")
	fs.Bool("dry-run", false, "")
	fs.Bool("by-day", false, "")
	fs.Int("limit", 0, "")
	fs.Duration("gap", 0, "")
	return fs
}

func TestLongFlagsRewritesSingleDashLongFlags(t *testing.T) {
	for _, c := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "a lone long flag gains its second dash",
			in:   []string{"-dry-run"},
			want: []string{"--dry-run"},
		},
		{
			name: "a flag and its separate value",
			in:   []string{"-from", "2026-08-01"},
			want: []string{"--from", "2026-08-01"},
		},
		{
			name: "a flag joined to its value by an equals sign",
			in:   []string{"-from=2026-08-01"},
			want: []string{"--from=2026-08-01"},
		},
		{
			name: "several flags in one invocation",
			in:   []string{"-dry-run", "-by-day", "-store", "/tmp/x"},
			want: []string{"--dry-run", "--by-day", "--store", "/tmp/x"},
		},
		{
			name: "a flag already spelled with two dashes is left alone",
			in:   []string{"--from", "2026-08-01"},
			want: []string{"--from", "2026-08-01"},
		},
		{
			name: "nothing at all",
			in:   []string{},
			want: []string{},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := longFlags(longFlagsTestSet(), c.in)
			if !slices.Equal(got, c.want) {
				t.Errorf("longFlags(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLongFlagsLeavesValuesAndNonFlagsAlone(t *testing.T) {
	// A value is whatever follows a flag that takes one, even when the value
	// itself begins with a dash. stdlib flag consumed the next argument
	// unconditionally and pflag does the same, so rewriting a VALUE that
	// happens to look like a flag would silently change what was passed.
	for _, c := range []struct {
		name string
		in   []string
		want []string
	}{
		{
			name: "a value that begins with a dash is not a flag",
			in:   []string{"-exclude", "-weird"},
			want: []string{"--exclude", "-weird"},
		},
		{
			name: "a negative number is not a flag",
			in:   []string{"-limit", "-30"},
			want: []string{"--limit", "-30"},
		},
		{
			name: "a single-character flag keeps its single dash",
			in:   []string{"-h"},
			want: []string{"-h"},
		},
		{
			// Rewritten so that pflag says "unknown flag: --nosuchflag"
			// rather than "unknown shorthand flag: 'n' in -nosuchflag",
			// which would name a shorthand this CLI has never had.
			name: "an unregistered name is rewritten and left for pflag to reject",
			in:   []string{"-nosuchflag"},
			want: []string{"--nosuchflag"},
		},
		{
			name: "an unregistered name swallows nothing after it",
			in:   []string{"-nosuchflag", "-from", "2026-08-01"},
			want: []string{"--nosuchflag", "--from", "2026-08-01"},
		},
		{
			name: "everything after a bare double dash is literal",
			in:   []string{"-store", "/tmp/x", "--", "-from", "-dry-run"},
			want: []string{"--store", "/tmp/x", "--", "-from", "-dry-run"},
		},
		{
			name: "a boolean flag does not swallow the argument after it",
			in:   []string{"-dry-run", "-from", "2026-08-01"},
			want: []string{"--dry-run", "--from", "2026-08-01"},
		},
		{
			name: "parsing stops at the first positional argument",
			in:   []string{"positional", "-store", "/tmp/x"},
			want: []string{"positional", "-store", "/tmp/x"},
		},
		{
			name: "a lone dash is a positional argument",
			in:   []string{"-", "-store"},
			want: []string{"-", "-store"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := longFlags(longFlagsTestSet(), c.in)
			if !slices.Equal(got, c.want) {
				t.Errorf("longFlags(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestLongFlagsSurvivesRoundTripThroughPflag(t *testing.T) {
	// The rewrite is only worth anything if pflag then reads the values the
	// old flag package read. This drives the whole path rather than the
	// spelling.
	fs := longFlagsTestSet()
	args := []string{"-dry-run", "-gap", "15m", "-limit=7", "-store", "/tmp/x"}
	if err := fs.Parse(longFlags(fs, args)); err != nil {
		t.Fatalf("parse %q: %v", args, err)
	}
	if v, _ := fs.GetBool("dry-run"); !v {
		t.Errorf("-dry-run did not set the flag")
	}
	if v, _ := fs.GetDuration("gap"); v != 15*time.Minute {
		t.Errorf("-gap = %v, want 15m", v)
	}
	if v, _ := fs.GetInt("limit"); v != 7 {
		t.Errorf("-limit=7 gave %d", v)
	}
	if v, _ := fs.GetString("store"); v != "/tmp/x" {
		t.Errorf("-store gave %q", v)
	}
}

func TestFlagsSetNamesOnlyTheFlagsThatWereGiven(t *testing.T) {
	// A flag left alone and a flag given its own default value have to be
	// told apart: sessionize reads -gap 0 as "no idle gap" and an absent -gap
	// as "take the threshold from the config file".
	fs := longFlagsTestSet()
	if err := fs.Parse(longFlags(fs, []string{"-gap", "0", "-dry-run"})); err != nil {
		t.Fatalf("parse: %v", err)
	}
	set := flagsSet(fs)
	if !set["gap"] {
		t.Errorf("-gap was given its default value and must still count as set")
	}
	if !set["dry-run"] {
		t.Errorf("-dry-run was given and must count as set")
	}
	if set["from"] {
		t.Errorf("-from was never given and must not count as set")
	}
}

// A COMMAND LINE THAT COULD NOT BE PARSED EXITS 2, and a command that ran and
// failed exits 1.
//
// stdlib flag gave this for free: ExitOnError called os.Exit(2) from inside the
// flag package, which is also why no test could reach it — a test that tripped
// it killed the test binary. Cobra returns the error instead, so dispatch has
// to reconstruct the status, and the reconstruction is what this pins.
// headline_test.go covers the third case, an unknown command, which is also 2.
func TestAFlagErrorExitsTwoAndAFailedCommandExitsOne(t *testing.T) {
	for _, c := range []struct {
		name string
		args []string
		want int
	}{
		{"an undefined flag", []string{"metrics", "-nosuchflag"}, 2},
		{"an unparseable flag value", []string{"status", "-stale", "notaduration"}, 2},
		{"a command that ran and refused", []string{"consolidate"}, 1},
	} {
		t.Run(c.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if got := run(c.args, &stdout, &stderr, "", ""); got != c.want {
				t.Errorf("run(%q) = %d, want %d\nstderr: %s", c.args, got, c.want, stderr.String())
			}
			if stderr.Len() == 0 {
				t.Errorf("run(%q) said nothing on stderr", c.args)
			}
		})
	}
}

// Every name usageTo lists has to be a command the root actually holds, and
// every command the root holds has to be listed. dispatch resolves a name
// against the root's own children, so a command registered but unlisted is
// invisible and a name listed but unregistered reports "unknown command".
func TestEveryCommandTheUsageNamesIsRegistered(t *testing.T) {
	root := rootCommand()
	for _, name := range []string{
		"orient", "harvest", "status", "register", "consolidate",
		"sessionize", "chains", "metrics", "dashboard", "review", "fit", "adjust", "version",
	} {
		if _, ok := subcommandNamed(root, name); !ok {
			t.Errorf("usageTo names %q but the root command has no such child", name)
		}
	}
	for _, cmd := range root.Commands() {
		if cmd.Short == "" {
			t.Errorf("%s has no one-line summary", cmd.Name())
		}
	}
}
