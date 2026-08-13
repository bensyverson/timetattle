package sessions

// Excluding machine-generated file activity, at READ time.
//
// # Why this is not a collect-time filter
//
// internal/fsobserve.skipFile already refuses to observe a SQLite sidecar, and
// that is the right thing to do — but it only ever governs the NEXT harvest.
// internal/store exposes no delete, deliberately, so the 130,361
// fs-observations already in the live store on 2026-08-01 are permanent.
// Changing what gets collected therefore moves Easley's 135.2h of attributed
// time by exactly zero minutes. Applying the rule where events are READ makes
// it retroactive over the whole existing store, reversible by editing a config
// file and re-running `timetattle sessionize`, and destructive of nothing.
//
// The same predicate is used in both places rather than being written twice —
// see fsobserve.IsDatabaseSidecar, which both skipFile and Match call.
//
// # Code decides one rule; the person decides the rest
//
// The split is by whether a rule is universal or personal, and it is not a
// matter of taste. A -wal cannot be a deliverable on any machine, so nobody
// should have to configure it away and it lives in code. A .log is another
// matter: Ben's are noise because Easley is a server he runs that syncs its
// own access logs, and somebody else's .log is the output of their scraper.
// Hardcoding that would be the tool deciding something it has no way to know,
// so it is a pattern in sessions.exclude, and sessions.exclude ships EMPTY.
//
// # What must never happen here
//
// Of the 602 project-days in the live store whose only evidence is
// fs-observations (measured 2026-08-01 by scripts/excludecheck), a census
// found 85 made ENTIRELY of ordinary files —
// MBCommon.h, a README.md, a design PDF — which are plausibly a real evening
// spent editing one document. No rule in this file may reach them. That is why
// there is no heuristic here: no "a day with fewer than N observations", no
// "stamped at exactly 05:00 is cron". Every exclusion is either the one
// universal rule above or something a person wrote down, and everything that
// is dropped is counted and reported. Weak evidence is surfaced as weak; it is
// never dropped silently.

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/bensyverson/timetattle/internal/fsobserve"
	"github.com/bensyverson/timetattle/internal/source"
)

// Excludes decides which observations are machine noise rather than evidence
// of somebody's work.
//
// The zero value is not "exclude nothing": it is the universal rule and nothing
// else, which is what a machine with no config file gets.
type Excludes struct {
	// globs match a locator's BASE NAME, and prefixes match the whole
	// absolute path. Both keep their original text so that what is reported
	// as having matched — or as having matched nothing — is the thing the
	// person actually wrote.
	globs    []string
	prefixes []prefix
	// order is every pattern as written, so the accounting can name all of
	// them, including the ones nothing matched.
	order []string
}

// prefix is one path pattern, kept both as written and as compared. The two
// differ — "…/reports/" is compared as "…/reports" — and what is REPORTED must
// be what the person typed, or the accounting names a pattern that is not in
// their config file.
type prefix struct {
	written string
	clean   string
}

// NewExcludes compiles the patterns from sessions.exclude.
//
// THE SYNTAX IS TWO RULES AND NO DEPENDENCY, which is why it is what it is:
//
//   - a pattern with NO slash is a glob matched against the file's BASE NAME
//     with filepath.Match — "*.log.bz2", "*.gz";
//   - a pattern WITH a slash is an absolute path prefix, respecting path
//     boundaries — "/Users/you/git/Easley/reports/".
//
// A directory of generated output cannot be named by a base-name glob and a
// file type cannot be named by a path, so both are needed; doublestar globbing
// would cover both and is a dependency this project does not want for a config
// field. See the package doc of internal/config for the same refusal about TOML.
//
// Everything that could only ever match nothing is refused HERE, where it was
// written, rather than doing nothing forever at the place it was needed. That
// is the same three-valued discipline the rest of this package runs on: a
// pattern that matched nothing is a fact worth reporting, but a pattern that
// CANNOT match anything is a mistake.
func NewExcludes(patterns []string) (Excludes, error) {
	var x Excludes
	for _, p := range patterns {
		switch {
		case p == "":
			return Excludes{}, fmt.Errorf("an empty exclude pattern matches every path there is; " +
				"remove it, or write the glob or absolute path you meant")
		case strings.ContainsRune(p, '/'):
			if !filepath.IsAbs(p) {
				return Excludes{}, fmt.Errorf("the exclude pattern %q contains a slash, so it is read as "+
					"a path prefix, but it is not absolute and every locator is; write it as a full path "+
					"such as \"/Users/you/project/logs/\", or as a base-name glob such as \"*.log\"", p)
			}
			x.prefixes = append(x.prefixes, prefix{written: p, clean: filepath.Clean(p)})
		default:
			// filepath.Match reports a bad pattern only when it is actually
			// reached, so it is exercised here against a name that cannot
			// itself be the reason for a failure.
			if _, err := filepath.Match(p, "x"); err != nil {
				return Excludes{}, fmt.Errorf("the exclude pattern %q is not a valid glob: %w", p, err)
			}
			x.globs = append(x.globs, p)
		}
		x.order = append(x.order, p)
	}
	return x, nil
}

// Patterns is every pattern this was built from, in the order it was written.
func (x Excludes) Patterns() []string { return x.order }

// Match reports whether locator names a file that is not evidence of work, and
// says which rule decided.
//
// An empty locator is never excluded and never can be: knowledgeC and Biome
// carry no path at all, and they are the presence stream every capacity figure
// is a fraction of. A file-name rule has nothing whatever to say about them.
func (x Excludes) Match(locator string) bool {
	_, matched := x.match(locator)
	return matched
}

// match returns the pattern responsible, or the empty string for the universal
// sidecar rule, alongside whether anything matched at all.
func (x Excludes) match(locator string) (string, bool) {
	if locator == "" {
		return "", false
	}
	if fsobserve.IsDatabaseSidecar(filepath.Base(locator)) {
		return "", true
	}
	base := filepath.Base(locator)
	for _, g := range x.globs {
		if ok, err := filepath.Match(g, base); err == nil && ok {
			return g, true
		}
	}
	clean := filepath.Clean(locator)
	for _, p := range x.prefixes {
		// Path boundaries are respected on purpose. A literal prefix
		// comparison would let "/Users/you/git/Ea" swallow every file in
		// /Users/you/git/Easley, which is a typo that silently deletes a
		// project. filepath.Clean on both sides is also what makes
		// "…/logs" and "…/logs/" the same instruction rather than two
		// subtly different ones.
		// Proved by mutation: replacing this with a plain
		// strings.HasPrefix makes TestAPrefixStopsAtAPathBoundary fail on
		// "/Users/you/git/Ea" against "/Users/you/git/Easley/logs/access.log".
		// No pattern in the live corpus happens to be a near-miss prefix of
		// another path today, so real data alone would never have caught it.
		if clean == p.clean || strings.HasPrefix(clean, p.clean+string(filepath.Separator)) {
			return p.written, true
		}
	}
	return "", false
}

// Drop is what Retain removed, itemised.
//
// Every count here exists so a figure computed after an exclusion can be
// reconciled with the store it came out of. A total that dropped observations
// without saying how many, or by which rule, is a number nobody can falsify.
type Drop struct {
	// Total is every observation removed, and kept + Total is the input.
	Total int
	// Sidecars is how many the universal SQLite rule took. It is separate
	// from ByPattern because nobody configured it and nobody can turn it off.
	Sidecars int
	// ByPattern counts what each configured pattern took, and holds an entry
	// for EVERY pattern, including those that took nothing. A pattern that
	// matched nothing is a person having said something the tool did nothing
	// with; leaving it out of the accounting would make that indistinguishable
	// from never having configured it.
	ByPattern map[string]int
}

// Unmatched names the patterns that took nothing, in the order they were
// written, so a report can say so out loud.
func (d Drop) Unmatched(x Excludes) []string {
	var out []string
	for _, p := range x.Patterns() {
		if d.ByPattern[p] == 0 {
			out = append(out, p)
		}
	}
	return out
}

// Retain returns the observations that are evidence of somebody's work, and an
// account of what it removed.
//
// It is pure, like Cluster: the caller's slice is neither reordered nor
// written to, and the kept events come back in the order they arrived.
func Retain(events []source.Event, x Excludes) ([]source.Event, Drop) {
	drop := Drop{ByPattern: make(map[string]int, len(x.order))}
	for _, p := range x.order {
		drop.ByPattern[p] = 0
	}

	kept := make([]source.Event, 0, len(events))
	for _, e := range events {
		pattern, matched := x.match(e.Locator)
		if !matched {
			kept = append(kept, e)
			continue
		}
		drop.Total++
		if pattern == "" {
			drop.Sidecars++
			continue
		}
		drop.ByPattern[pattern]++
	}
	return kept, drop
}
