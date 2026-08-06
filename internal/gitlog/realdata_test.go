package gitlog

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// realRepo is wherever this test binary happens to be running from. Timetattle
// itself is the "real data" this package's own brief asks for: a repository
// that is always available, has real history, and — because this suite runs
// inside a linked git worktree of that same repository — reading `git log
// --all` from here already returns the FULL shared commit history (worktrees
// share one object store and one set of refs), with no hardcoded path to any
// particular checkout. Only the reflog differs per worktree; see
// TestRealReflogParses for what that means for this specific run.
func realRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
	dir, err := os.Getwd()
	if err != nil {
		t.Skipf("no working directory: %v", err)
	}
	// Strips GIT_DIR/GIT_WORK_TREE the same way git.go's runGit does (calling
	// that unexported helper directly, since this file is part of the same
	// package): this project's own pre-commit hook runs "go test ./..." as a
	// subprocess of `git commit`, which exports GIT_DIR naming the outer
	// repository. Without stripping, this check would report on THAT
	// repository's status instead of dir's. The value returned here (dir,
	// from os.Getwd()) never depended on the leaked command's output, so this
	// could only ever mis-skip the test, never corrupt what Collect() reads
	// -- git.go's own runGit strips independently for the collection path,
	// covered by TestGitEnvVarsDoNotLeakIntoSubprocess. Fixed for the same
	// reason and the same convention, not because this one was observed to
	// misbehave.
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	cmd.Env = withoutGitRepoEnv(os.Environ())
	if err := cmd.Run(); err != nil {
		// Not "this is not a repository": that is one of several things this
		// exit status can mean, and dubious ownership or a broken git is
		// another. Report the command and what it did.
		t.Skipf("git -C %s rev-parse --is-inside-work-tree: %v", dir, err)
	}
	return dir
}

// TestRealCommitsParse is the check that matters most: every commit in
// Timetattle's own history must produce a usable, storable event with a
// plausible time and a diff-stat weight that is either a real observation or
// the explicit merge marker — never silently dropped or malformed.
func TestRealCommitsParse(t *testing.T) {
	repo := realRepo(t)
	events, err := NewCommits(repo).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Skipf("NewCommits(%s).Collect returned no events", repo)
	}

	var earliest, latest time.Time
	merges, zero, real := 0, 0, 0
	for i, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %d is unusable: %v", i, err)
		}
		if e.Shape != source.Point {
			t.Fatalf("event %d has shape %q, want point", i, e.Shape)
		}
		if e.Start.Year() < 2015 || e.Start.After(time.Now().AddDate(0, 0, 1)) {
			t.Fatalf("event %d is dated %s, which is not a plausible instant", i, e.Start)
		}
		if e.Cause != source.CauseUserAction {
			t.Fatalf("event %d has cause %q, want user-action", i, e.Cause)
		}
		switch stat, ok := ParseStat(e.State); {
		case e.State == StateMerge:
			merges++
		case ok && stat == (Stat{}):
			zero++
		case ok:
			real++
		default:
			t.Fatalf("event %d state %q is neither a parseable stat nor the merge marker", i, e.State)
		}
		if earliest.IsZero() || e.Start.Before(earliest) {
			earliest = e.Start
		}
		if e.Start.After(latest) {
			latest = e.Start
		}
	}
	t.Logf("%d commit events: %d merges, %d zero-diff, %d with real stats, %s to %s",
		len(events), merges, zero, real, earliest.Format(time.RFC3339), latest.Format(time.RFC3339))
}

// TestRealAuthorAndCommitterDatesInThisRepo measures, rather than assumes,
// whether this repository's own history contains a commit whose author date
// and committer date diverge — the fingerprint of a rebase or amend. This is
// the specific real-data cross-check the task brief called for.
//
// MEASURED 2026-07-31: across every commit reachable from this worktree,
// author date equals committer date on all of them. This repository's commits
// are produced by agents that commit once and never amend or rebase — the
// merges recorded here are made with `git merge`, not `git rebase`, and none
// of the history is rewritten after the fact. So the brief's claim that this
// repo "has real rebases/amends in its own history to check assertions
// against" does not hold for the history actually reachable from a worktree
// today; the mechanism is instead proven by construction in
// TestCommitsUsesCommitterDateNotAuthorDateAfterAmend. This test stays as a
// live measurement rather than a fixed assertion in either direction, so it
// keeps telling the truth if that ever changes.
func TestRealAuthorAndCommitterDatesInThisRepo(t *testing.T) {
	repo := realRepo(t)
	out, err := runGit(context.Background(), repo, "log", "--all", "--pretty=format:%H%x1f%aI%x1f%cI")
	if err != nil {
		t.Fatalf("git log: %v", err)
	}
	total, diverged := 0, 0
	for _, line := range splitNonEmptyLines(string(out)) {
		fields := splitFields(line)
		if len(fields) != 3 {
			t.Fatalf("unexpected log line shape: %q", line)
		}
		total++
		if fields[1] != fields[2] {
			diverged++
		}
	}
	if total == 0 {
		t.Skipf("git log --all in %s printed no lines", repo)
	}
	t.Logf("%d commits reachable from this worktree, %d with author date != committer date", total, diverged)
}

// TestRealReflogParses exercises the reflog reader against this worktree's
// own HEAD reflog. Worktrees each keep an independent HEAD reflog — this one
// was created for this very task, so it legitimately holds only the handful
// of entries this session produced rather than years of history. That
// sparseness is itself the real-data answer for a freshly created worktree,
// not a reason to fake a larger fixture.
func TestRealReflogParses(t *testing.T) {
	repo := realRepo(t)
	events, err := NewReflog(repo).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Skipf("NewReflog(%s).Collect returned no events", repo)
	}
	actions := map[string]int{}
	for i, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %d is unusable: %v", i, err)
		}
		if e.Start.Year() < 2015 || e.Start.After(time.Now().AddDate(0, 0, 1)) {
			t.Fatalf("event %d is dated %s, which is not a plausible instant", i, e.Start)
		}
		actions[e.State]++
	}
	t.Logf("%d reflog events: %v", len(events), actions)
}
