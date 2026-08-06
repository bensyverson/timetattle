package gitlog

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// setupGitTestHome points HOME at an empty, disposable directory for the
// duration of one test, and disables system-wide config. Without it, git
// commands invoked here read this machine's real ~/.gitconfig, which makes
// tests nondeterministic across machines at best and, on this project's
// sandboxed agent environments, outright fails ("Operation not permitted") —
// matching internal/registry/worktree_test.go's setupGitTestHome, which
// documents the same failure mode.
func setupGitTestHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_AUTHOR_NAME", "timetattle-test")
	t.Setenv("GIT_AUTHOR_EMAIL", "timetattle-test@example.com")
	t.Setenv("GIT_COMMITTER_NAME", "timetattle-test")
	t.Setenv("GIT_COMMITTER_EMAIL", "timetattle-test@example.com")
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found on PATH")
	}
}

// testGitRepoEnvPrefixes is a copy of gitlog.go's gitRepoEnvPrefixes,
// duplicated rather than imported so this fixture helper strips them
// independently of the package under test — see runGitCmd.
var testGitRepoEnvPrefixes = []string{
	"GIT_DIR=", "GIT_WORK_TREE=", "GIT_INDEX_FILE=", "GIT_COMMON_DIR=",
	"GIT_OBJECT_DIRECTORY=", "GIT_ALTERNATE_OBJECT_DIRECTORIES=", "GIT_PREFIX=",
	"GIT_NAMESPACE=",
}

// runGitCmd is the test-side git runner, building the fixtures these tests
// exercise the package against. It strips GIT_DIR and friends itself,
// duplicating gitlog.go's withoutGitRepoEnv rather than calling it — this
// suite runs as part of this project's own pre-commit hook, whose "go test"
// step inherits a real GIT_DIR naming the outer repository, and without
// stripping it here every `git init`/`git commit` fixture below would be
// silently redirected into that repository instead of the fresh temp
// directory it names. Reusing the package's own function would still fix
// fixture setup, but would also make this file unable to prove the
// production code's stripping independently — see
// TestGitEnvVarsDoNotLeakIntoSubprocess, which sets GIT_DIR deliberately
// and relies on the collector under test doing its OWN stripping, not this
// helper's.
func runGitCmd(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	for _, kv := range os.Environ() {
		blocked := false
		for _, prefix := range testGitRepoEnvPrefixes {
			if strings.HasPrefix(kv, prefix) {
				blocked = true
				break
			}
		}
		if !blocked {
			cmd.Env = append(cmd.Env, kv)
		}
	}
	cmd.Env = append(cmd.Env, env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v (in %s): %v\n%s", args, dir, err, out)
	}
	return string(out)
}

func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGitCmd(t, dir, nil, "init", "-q")
	return dir
}

// commitAt makes a commit whose author and committer dates are both pinned
// to at, and writes content bytes to name so the commit has a real, known
// diff.
func commitAt(t *testing.T, dir string, at time.Time, name, content string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write fixture file: %v", err)
	}
	runGitCmd(t, dir, nil, "add", name)
	env := []string{
		"GIT_AUTHOR_DATE=" + at.Format(time.RFC3339),
		"GIT_COMMITTER_DATE=" + at.Format(time.RFC3339),
	}
	runGitCmd(t, dir, env, "commit", "-q", "-m", "commit "+name)
	return strings.TrimSpace(runGitCmd(t, dir, nil, "rev-parse", "HEAD"))
}

func collectAll(t *testing.T, s source.Source) []source.Event {
	t.Helper()
	events, err := s.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("%s Collect() = %v", s.ID(), err)
	}
	return events
}

// TestCommitsCollectsBasicCommitWithTimeAndDiffStats is the ordinary case:
// one commit, never touched by rebase or amend, produces one Point event
// whose time is the commit's own timestamp and whose State carries a
// parseable diff-stat weight. This is the criterion "qsc" check.
func TestCommitsCollectsBasicCommitWithTimeAndDiffStats(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	at := time.Date(2026, 6, 1, 9, 0, 0, 0, time.FixedZone("", -5*3600))
	commitAt(t, repo, at, "a.txt", "one\ntwo\nthree\n")

	events := collectAll(t, NewCommits(repo))
	if len(events) != 1 {
		t.Fatalf("Collect() = %d events, want 1", len(events))
	}
	e := events[0]
	if err := e.Validate(); err != nil {
		t.Fatalf("event failed Validate(): %v", err)
	}
	if e.Shape != source.Point {
		t.Errorf("Shape = %q, want point", e.Shape)
	}
	if !e.Start.Equal(at) {
		t.Errorf("Start = %s, want %s", e.Start, at)
	}
	if e.Cause != source.CauseUserAction {
		t.Errorf("Cause = %q, want user-action", e.Cause)
	}
	stat, ok := ParseStat(e.State)
	if !ok {
		t.Fatalf("ParseStat(%q) failed; want a parseable diff-stat weight for a non-merge commit", e.State)
	}
	if stat.FilesChanged != 1 || stat.Insertions != 3 || stat.Deletions != 0 {
		t.Errorf("stat = %+v, want {FilesChanged:1 Insertions:3 Deletions:0}", stat)
	}
}

// TestCommitsUsesCommitterDateNotAuthorDateAfterAmend is the M3B criterion's
// core proof. It manufactures the exact scenario the brief describes: a
// commit is first authored on one (backdated) date, then amended later
// without resetting the author date, which is exactly what `git commit
// --amend` and `git rebase` do by default. The resulting commit object
// carries a stale author date and a fresh committer date; the assertion is
// that Collect reports the FRESH one, because that is when this machine
// actually wrote the ref, not when the content was first drafted.
func TestCommitsUsesCommitterDateNotAuthorDateAfterAmend(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	staleAuthor := time.Date(2026, 5, 1, 8, 0, 0, 0, time.FixedZone("", -5*3600))
	commitAt(t, repo, staleAuthor, "a.txt", "v1\n")

	// Amend without exporting GIT_AUTHOR_DATE or GIT_COMMITTER_DATE: git's
	// default behaviour is to preserve the amended commit's author identity
	// and date, while writing a brand new committer date for the rewrite
	// itself. This is the real mechanism the brief warns about, reproduced
	// rather than assumed.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("v1\nv2\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	runGitCmd(t, repo, nil, "add", "a.txt")
	beforeAmend := time.Now()
	runGitCmd(t, repo, nil, "commit", "-q", "--amend", "--no-edit")
	afterAmend := time.Now()

	authorDate := strings.TrimSpace(runGitCmd(t, repo, nil, "log", "-1", "--pretty=%aI"))
	committerDate := strings.TrimSpace(runGitCmd(t, repo, nil, "log", "-1", "--pretty=%cI"))
	if authorDate == committerDate {
		t.Fatalf("test fixture is broken: author date %s equals committer date %s; "+
			"amend should have preserved the stale author date while writing a fresh committer date", authorDate, committerDate)
	}
	parsedAuthor, err := time.Parse(time.RFC3339, authorDate)
	if err != nil {
		t.Fatalf("parse fixture author date: %v", err)
	}
	if !parsedAuthor.Equal(staleAuthor) {
		t.Fatalf("test fixture is broken: amended commit's author date is %s, want the original stale %s", parsedAuthor, staleAuthor)
	}

	events := collectAll(t, NewCommits(repo))
	if len(events) != 1 {
		t.Fatalf("Collect() = %d events, want 1 (the amend rewrites the one commit in place)", len(events))
	}
	got := events[0].Start

	if got.Equal(staleAuthor) || got.Before(staleAuthor.Add(24*time.Hour)) {
		t.Errorf("Collect() reported the event at %s, which is the STALE author date (or close to it); "+
			"this is exactly the misleading time the brief warns about", got)
	}
	// The committer date must fall within the window the amend actually ran,
	// with a little slack for clock resolution.
	if got.Before(beforeAmend.Add(-2*time.Second)) || got.After(afterAmend.Add(2*time.Second)) {
		t.Errorf("Collect() reported the event at %s, want it inside [%s, %s] — the real wall-clock window the amend ran in",
			got, beforeAmend, afterAmend)
	}
}

// TestCommitsMarksMergeCommitsDistinctlyFromZeroChangeCommits proves the
// three-way distinction this package draws for diff stats: an ordinary
// commit with real changes, an ordinary commit that happens to have NO diff
// (an --allow-empty commit — an observed zero, not an absence), and a merge
// commit (whose combined diff this package does not attempt to compute, so
// it is marked "not applicable" rather than silently reported as zero).
// Collapsing the last two would misrepresent every merge as "no work done",
// which is the same "absence is not an observation" mistake this project's
// CLAUDE.md calls out.
func TestCommitsMarksMergeCommitsDistinctlyFromZeroChangeCommits(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	commitAt(t, repo, base, "a.txt", "base\n")
	runGitCmd(t, repo, nil, "branch", "feature")

	// An observed, real zero-change commit on main.
	env := []string{"GIT_AUTHOR_DATE=" + base.Add(time.Hour).Format(time.RFC3339), "GIT_COMMITTER_DATE=" + base.Add(time.Hour).Format(time.RFC3339)}
	runGitCmd(t, repo, env, "commit", "-q", "--allow-empty", "-m", "empty marker")

	runGitCmd(t, repo, nil, "checkout", "-q", "feature")
	commitAt(t, repo, base.Add(2*time.Hour), "b.txt", "feature work\n")
	runGitCmd(t, repo, nil, "checkout", "-q", "-")
	mergeEnv := []string{"GIT_AUTHOR_DATE=" + base.Add(3*time.Hour).Format(time.RFC3339), "GIT_COMMITTER_DATE=" + base.Add(3*time.Hour).Format(time.RFC3339)}
	runGitCmd(t, repo, mergeEnv, "merge", "-q", "--no-ff", "-m", "merge feature", "feature")

	events := collectAll(t, NewCommits(repo))
	if len(events) != 4 {
		t.Fatalf("Collect() = %d events, want 4 (base, empty marker, feature commit, merge)", len(events))
	}

	var sawZero, sawMerge, sawReal bool
	for _, e := range events {
		stat, ok := ParseStat(e.State)
		switch {
		case e.State == StateMerge:
			sawMerge = true
			if ok {
				t.Errorf("merge commit's state %q parsed as a numeric stat; it should be the not-applicable marker", e.State)
			}
		case ok && stat == (Stat{}):
			sawZero = true
		case ok:
			sawReal = true
		default:
			t.Errorf("event state %q neither parses as a stat nor is the merge marker", e.State)
		}
	}
	if !sawZero {
		t.Error("no event carried an observed all-zero stat for the --allow-empty commit")
	}
	if !sawMerge {
		t.Error("no event carried the merge marker for the merge commit")
	}
	if !sawReal {
		t.Error("no event carried a real non-zero stat")
	}
}

// TestReflogCollectsCheckoutResetAndCommitActions is the criterion "Jqm"
// check: reflog entries — not just commits — are collected, and they carry
// the true local wall-clock time git recorded when each ref-changing
// operation actually ran on this machine.
func TestReflogCollectsCheckoutResetAndCommitActions(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	commitAt(t, repo, base, "a.txt", "v1\n")
	runGitCmd(t, repo, nil, "branch", "feature")
	runGitCmd(t, repo, nil, "checkout", "-q", "feature")
	runGitCmd(t, repo, nil, "checkout", "-q", "-")
	runGitCmd(t, repo, nil, "reset", "-q", "--soft", "HEAD")

	events := collectAll(t, NewReflog(repo))
	if len(events) == 0 {
		t.Fatal("Reflog Collect() returned no events; expected at least commit, two checkouts and a reset")
	}
	actions := map[string]int{}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("reflog event failed Validate(): %v", err)
		}
		if e.Shape != source.Point {
			t.Errorf("reflog event shape = %q, want point", e.Shape)
		}
		if e.Cause != source.CauseUserAction {
			t.Errorf("reflog event cause = %q, want user-action", e.Cause)
		}
		actions[e.State]++
	}
	for _, want := range []string{"commit (initial)", "checkout", "reset"} {
		if actions[want] == 0 {
			t.Errorf("no reflog event with action %q; saw %v", want, actions)
		}
	}
}

// TestReflogCapturesWorkNeverCommittedToACommitLogWithoutReflog documents
// why reflog matters beyond timing correction: a checkout that never
// produces a commit is invisible to `git log` entirely, and only the
// reflog observes it happened at all.
func TestReflogCapturesWorkNeverCommittedToACommitLogWithoutReflog(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	commitAt(t, repo, base, "a.txt", "v1\n")
	runGitCmd(t, repo, nil, "branch", "scratch")
	runGitCmd(t, repo, nil, "checkout", "-q", "scratch")
	runGitCmd(t, repo, nil, "checkout", "-q", "-")

	commitEvents := collectAll(t, NewCommits(repo))
	reflogEvents := collectAll(t, NewReflog(repo))

	if len(commitEvents) != 1 {
		t.Fatalf("commit log has %d events, want 1; the checkouts must not appear there", len(commitEvents))
	}
	if len(reflogEvents) < 3 {
		t.Fatalf("reflog has %d events, want at least 3 (initial commit, checkout out, checkout back)", len(reflogEvents))
	}
}

// TestCommitsSinceFiltersByCommitterDate and its reflog counterpart check the
// Collect(ctx, since) contract: events whose time is before since are
// dropped, and the boundary is inclusive.
func TestCommitsSinceFiltersByCommitterDate(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	t1 := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 6, 2, 9, 0, 0, 0, time.UTC)
	commitAt(t, repo, t1, "a.txt", "v1\n")
	commitAt(t, repo, t2, "b.txt", "v2\n")

	events, err := NewCommits(repo).Collect(context.Background(), t2)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("Collect(since=t2) = %d events, want 1 (t2's own commit, inclusive boundary)", len(events))
	}
	if !events[0].Start.Equal(t2) {
		t.Errorf("Collect(since=t2) event = %s, want %s", events[0].Start, t2)
	}
}

func TestReflogSinceFiltersOlderEntries(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	commitAt(t, repo, base, "a.txt", "v1\n")

	all := collectAll(t, NewReflog(repo))
	if len(all) == 0 {
		t.Fatal("no reflog events at all; cannot test filtering")
	}
	cutoff := all[len(all)-1].Start.Add(time.Nanosecond)

	filtered, err := NewReflog(repo).Collect(context.Background(), cutoff)
	if err != nil {
		t.Fatalf("Collect() = %v", err)
	}
	for _, e := range filtered {
		if e.Start.Before(cutoff) {
			t.Errorf("Collect(since=%s) returned event at %s, before the cutoff", cutoff, e.Start)
		}
	}
	if len(filtered) >= len(all) {
		t.Errorf("Collect(since=%s) = %d events, want fewer than the unfiltered %d", cutoff, len(filtered), len(all))
	}
}

// TestGitEnvVarsDoNotLeakIntoSubprocess is the mutation-tested regression
// guard for the bug internal/registry already found and fixed once: git
// exports GIT_DIR and friends into every hook and subprocess it runs, and
// those variables silently override the -C flag used to target a specific
// repository. Without stripping them here, a git-log-based collector
// invoked from inside this project's own pre-commit hook — which is exactly
// how `go test` runs during a real commit — would read whichever repository
// the hook itself belongs to, not the repo this source was constructed for.
//
// This was proven by mutation during development: with the strip removed,
// this test fails by reporting the DECOY repo's single commit instead of
// TARGET's three, across both Commits and Reflog.
func TestGitEnvVarsDoNotLeakIntoSubprocess(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)

	decoy := initRepo(t)
	commitAt(t, decoy, time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), "decoy.txt", "decoy\n")

	target := initRepo(t)
	commitAt(t, target, time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC), "a.txt", "1\n")
	commitAt(t, target, time.Date(2026, 6, 1, 10, 0, 0, 0, time.UTC), "b.txt", "2\n")
	commitAt(t, target, time.Date(2026, 6, 1, 11, 0, 0, 0, time.UTC), "c.txt", "3\n")

	t.Setenv("GIT_DIR", filepath.Join(decoy, ".git"))
	t.Setenv("GIT_WORK_TREE", decoy)

	commitEvents := collectAll(t, NewCommits(target))
	if len(commitEvents) != 3 {
		t.Fatalf("Commits.Collect() with a leaked GIT_DIR = %d events, want 3 (target's own history); "+
			"a leaked GIT_DIR pointed this at the decoy repo instead of -C's target", len(commitEvents))
	}

	reflogEvents := collectAll(t, NewReflog(target))
	for _, e := range reflogEvents {
		if e.Locator != mustAbs(t, target) {
			t.Fatalf("reflog event locator = %s, want %s; leaked GIT_DIR redirected collection", e.Locator, mustAbs(t, target))
		}
	}
}

func mustAbs(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("filepath.Abs(%s): %v", dir, err)
	}
	return abs
}

func TestCollectOnNonGitDirectoryReturnsAnError(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	dir := t.TempDir()

	if _, err := NewCommits(dir).Collect(context.Background(), time.Time{}); err == nil {
		t.Error("Commits.Collect() on a plain directory = nil error, want one; a registered path that is not a git repository is a configuration mistake")
	}
	if _, err := NewReflog(dir).Collect(context.Background(), time.Time{}); err == nil {
		t.Error("Reflog.Collect() on a plain directory = nil error, want one")
	}
}

// TestCollectOnEmptyRepoReturnsNoEventsAndNoError distinguishes "this repo
// exists but has nothing committed yet" from "this path is wrong". Only the
// second is an error.
func TestCollectOnEmptyRepoReturnsNoEventsAndNoError(t *testing.T) {
	requireGit(t)
	setupGitTestHome(t)
	repo := initRepo(t)

	events, err := NewCommits(repo).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Commits.Collect() on an empty repo = %v, want no error", err)
	}
	if len(events) != 0 {
		t.Errorf("Commits.Collect() on an empty repo = %d events, want 0", len(events))
	}

	events, err = NewReflog(repo).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Reflog.Collect() on an empty repo = %v, want no error", err)
	}
	if len(events) != 0 {
		t.Errorf("Reflog.Collect() on an empty repo = %d events, want 0", len(events))
	}
}

func TestSourcesReturnsBothCollectorsForARepo(t *testing.T) {
	repo := t.TempDir()
	srcs := Sources(repo)
	if len(srcs) != 2 {
		t.Fatalf("Sources() = %d sources, want 2", len(srcs))
	}
	ids := map[string]bool{}
	for _, s := range srcs {
		ids[s.ID()] = true
	}
	if !ids[CommitsID] || !ids[ReflogID] {
		t.Errorf("Sources() ids = %v, want both %q and %q", ids, CommitsID, ReflogID)
	}
}

func TestIDsAndKindAreStable(t *testing.T) {
	repo := t.TempDir()
	if got := NewCommits(repo).ID(); got != CommitsID {
		t.Errorf("Commits.ID() = %q, want %q", got, CommitsID)
	}
	if got := NewReflog(repo).ID(); got != ReflogID {
		t.Errorf("Reflog.ID() = %q, want %q", got, ReflogID)
	}
	if NewCommits(repo).Kind() != Kind {
		t.Errorf("Commits.Kind() != gitlog.Kind")
	}
	if NewReflog(repo).Kind() != Kind {
		t.Errorf("Reflog.Kind() != gitlog.Kind")
	}
}
