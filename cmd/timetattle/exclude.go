package main

import (
	"fmt"
	"io"
	"strings"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/sessions"
)

// Wiring sessions.exclude into the commands that read events.
//
// The exclusion is applied where events are READ rather than where they are
// collected, and that is the whole point of it. internal/store exposes no
// delete, so everything already harvested is permanent: a collect-time rule
// alone would stop new machine noise arriving and would move the 135.2h
// currently credited to a log-rotation cron by exactly zero minutes. Filtering
// at the read boundary makes the rule retroactive over the whole store,
// reversible by deleting a line from a config file, and destructive of nothing.
//
// Three commands read events for the purpose of saying what somebody did:
// sessionize, metrics and review. All three go through here so the three cannot
// disagree about what the corpus is. `chains` deliberately does NOT: it asks
// which file became which, which is a question about the filesystem's history
// rather than about whose work a day was, and a rotated log is a real answer to
// it.

// excludeFlagUsage is the one description of the flag, so sessionize and
// metrics cannot document it two different ways.
//
// It is the longest usage string in the CLI and stays that way deliberately.
// The two spellings mean different things — a glob is matched against a base
// name, a pattern containing a slash against the whole path as a prefix — and a
// reader who is not told which is which cannot write a correct value at all. A
// one-line-per-flag rule that shortened this would send them to the docs to
// type an argument, which costs more than the line it saves.
const excludeFlagUsage = "comma-separated machine activity to drop; a glob matches a base name " +
	"(*.log.bz2), a pattern with a slash is an absolute path prefix (/Users/you/reports/)"

// sessionExcludesFrom is the configured exclusion, with a flag overriding the
// file for one run without changing it.
//
// set says whether the flag was actually given. An empty -exclude means exclude
// nothing and an absent one means "whatever the file says" — two different
// instructions that an empty string alone cannot tell apart.
func sessionExcludesFrom(cfg *config.Config, override string, set bool) (sessions.Excludes, error) {
	patterns := cfg.Sessions.Exclude
	if set {
		patterns = splitCommaList(override)
	}
	return sessions.NewExcludes(patterns)
}

// reportExclusions says what was dropped before anything was clustered.
//
// It prints even when nothing was dropped. A count of zero is a measurement —
// the store was read and held none of this — and printing nothing would leave
// "excluded none" indistinguishable from "never looked", which is the collapse
// this project has shipped three times in three separate tools.
func reportExclusions(out io.Writer, x sessions.Excludes, drop sessions.Drop) {
	fmt.Fprintf(out, "excluded: %s\n", listOrNone(x.Patterns()))
	fmt.Fprintf(out, "%s excluded before clustering and not counted below%s\n",
		plural(drop.Total, "observation"), excludeBreakdown(x, drop))
	for _, p := range drop.Unmatched(x) {
		fmt.Fprintf(out, "NOTE: the exclude pattern %q matched nothing in this span; "+
			"it is configured and is doing nothing\n", p)
	}
}

// excludeBreakdown itemises a drop, naming the rule responsible for each part.
// The SQLite sidecars are listed separately from the configured patterns
// because nobody configured them and nobody can turn them off — see
// fsobserve.IsDatabaseSidecar for why that one rule is in code.
func excludeBreakdown(x sessions.Excludes, drop sessions.Drop) string {
	var parts []string
	if drop.Sidecars > 0 {
		parts = append(parts, plural(drop.Sidecars, "SQLite sidecar"))
	}
	for _, p := range x.Patterns() {
		if n := drop.ByPattern[p]; n > 0 {
			parts = append(parts, fmt.Sprintf("%d by %q", n, p))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, ", ")
}
