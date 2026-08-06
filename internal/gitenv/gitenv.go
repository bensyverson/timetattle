// Package gitenv holds the one rule for keeping a git subprocess pointed at
// the repository its caller intended, rather than the repository that
// happens to have invoked the current process.
//
// # Why this exists
//
// `git commit` exports GIT_DIR, GIT_WORK_TREE, GIT_INDEX_FILE and their
// relatives into every hook it runs, and into every subprocess a hook
// spawns. This project's own pre-commit hook runs `go test ./...`, so the
// whole suite inherits them. Those variables silently override `-C`: an
// unscrubbed `git -C somedir` acts on the repository that invoked the hook,
// not on somedir. On 2026-08-02 this clobbered this repository's own
// .gitignore and left a stray tracked.md at the root, twice, and separately
// made scripts/confidential scan zero files while reporting success — a
// false all-clear, which is worse than a missed finding.
//
// # Why one package rather than four copies
//
// Four call sites (internal/gitlog, internal/registry,
// scripts/confidential, scripts/pseudonymize) each independently
// reimplemented this exact strip on 2026-08-02, none aware of the others.
// Consolidating them here was not assumed to be right merely because the
// duplication was visible — internal/gitlog and internal/registry are core
// collection code, and a shared package is a new dependency for both — but
// two things settled it. First, the four "identical" copies had already
// drifted in representation (one kept the variable name and appended "="
// in its loop; the other three baked "=" into the list itself) within two
// days of being written, which is a live demonstration that "keep them
// pinned identical" needs a human to keep noticing, not just a test that
// could in principle catch it. Second, and more directly: at the moment
// this package was written, a FIFTH real call site —
// scripts/spike/dayview's loadCommits — had never stripped anything at
// all, despite four reference copies and their doc comments sitting a grep
// away. That is the failure mode a pinning test on four copies does not
// prevent: it only catches drift between copies that already exist, not a
// new call site that never copied the rule in the first place. A
// constructor that strips by construction closes that gap; copying a list
// and a comment did not. See internal/symbolscan for the same argument
// already accepted in this repository for a different rule (component
// boundaries) that two rewriters could otherwise disagree about.
//
// This package is deliberately tiny and dependency-free (only os, os/exec,
// strings and context), so the "new dependency for core collection code"
// cost is closer to what internal/symbolscan already cost scripts/ than to
// a real coupling risk.
//
// Every git invocation anywhere in this repository should go through
// Command or CommandContext rather than calling exec.Command("git", ...)
// directly.
package gitenv

import (
	"context"
	"os"
	"os/exec"
	"strings"
)

// Prefixes are the git-repo-identifying variables that must never survive
// into a subprocess's environment. Exported so a caller building its own
// exec.Cmd by hand — or a test fixture that deliberately wants to strip
// INDEPENDENTLY of this package, to prove the package under test is doing
// its own stripping rather than borrowing the test's — can still apply
// exactly this rule without inventing its own list.
var Prefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

// Strip returns env with every variable named in Prefixes removed.
func Strip(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		blocked := false
		for _, prefix := range Prefixes {
			if strings.HasPrefix(kv, prefix) {
				blocked = true
				break
			}
		}
		if !blocked {
			out = append(out, kv)
		}
	}
	return out
}

// Command builds a git invocation with Prefixes stripped from the inherited
// environment, so -C targets the repository the caller named rather than
// whatever repository's hook happened to invoke this process.
func Command(name string, arg ...string) *exec.Cmd {
	cmd := exec.Command(name, arg...)
	cmd.Env = Strip(os.Environ())
	return cmd
}

// CommandContext is Command with a context, for a caller that needs to
// cancel or time out the subprocess.
func CommandContext(ctx context.Context, name string, arg ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, arg...)
	cmd.Env = Strip(os.Environ())
	return cmd
}
