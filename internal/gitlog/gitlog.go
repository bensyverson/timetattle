// Package gitlog turns a git repository's own history into evidence of when
// work happened, by reading both `git log` and `git reflog` and treating
// them as two different kinds of evidence rather than one.
//
// # Two readers, because they observe different things
//
// `git log` reports commits: what was recorded, and — on the surface — when.
// `git reflog` reports what THIS repository's HEAD actually did on THIS
// machine: every commit, checkout, reset, rebase step and merge, in the
// order they really happened, including work that was amended away, rebased
// into a different shape, or never turned into a commit at all (a checkout
// to look something up leaves a reflog entry and no commit whatsoever).
// Commits is the record of what a repository's history now says; Reflog is
// the record of what a person actually did to produce that history. Both
// are collected, under separate source IDs, because they are separate
// evidence rather than two views of the same fact — the same reason
// Blender's `.blend1` backups and Motion's autosaves are collected
// separately even though both assert "a document was open".
//
// # What "the event's time" means for each
//
// This is the crux of the package, and it is why a naive `git log` reader
// would be actively misleading rather than merely imprecise:
//
// A commit's AUTHOR date is set once, when the commit's content was first
// drafted, and normal `git commit` never touches it again. But `git commit
// --amend` and `git rebase` REWRITE the commit object while preserving that
// original author date by default — the commit that results has today's
// content sitting under a timestamp from whenever it was first written,
// possibly days earlier and on a different machine entirely. Treating the
// author date as "when this happened" would silently attribute a rebase
// done on a Tuesday afternoon to whatever Saturday morning the first draft
// happened to carry.
//
// A commit's COMMITTER date, by contrast, is written fresh every time the
// commit object itself is created or rewritten — by an ordinary commit, by
// `--amend`, by each commit `rebase` replays — UNLESS the operator
// explicitly overrides it (GIT_COMMITTER_DATE, or
// `--committer-date-is-author-date`). It is therefore the closest thing
// `git log` alone can offer to "when this machine last wrote this ref", and
// it is what Commits.Collect reports as an event's Start. The author date is
// not carried into the event at all: there is nowhere in source.Event that
// means "the time this data claims to have been true, as opposed to the
// time it was recorded" without inventing one, and inventing an unused field
// under time pressure was judged worse than simply not carrying a value this
// task does not need. See TestCommitsUsesCommitterDateNotAuthorDateAfterAmend
// for this reproduced directly: an amend that changes nothing but the
// timestamp leaves author date stale and committer date fresh, and Collect
// reports the fresh one.
//
// A reflog entry's date needs none of this reasoning, because git recorded
// it as the entry was written, in the moment, on this machine, and nothing
// afterwards can rewrite it — a rebase creates NEW reflog entries for each
// step it takes rather than editing old ones. Reflog.Collect reports exactly
// that date. It is the ground truth the committer-date heuristic above is
// approximating, and where the two disagree (an operator who explicitly set
// GIT_COMMITTER_DATE, say) the reflog is correct and the commit-derived
// event is not — one more reason both are collected rather than only the
// cheaper one.
//
// # Diff statistics are a weight, not a duration
//
// Every non-merge commit event's State carries a diff-stat string — see
// Stat and ParseStat — reporting files changed, lines inserted and lines
// deleted. This package does not convert that into an estimated duration: a
// one-line typo fix and an 800-line refactor are not assumed to take
// proportional time, because nothing here has measured what the true
// relationship is. The number is carried through so that whichever part of
// Timetattle eventually weighs commits against each other has the raw
// material to do it with, calibrated against ground truth rather than
// invented here.
//
// Merge commits are marked with the distinct StateMerge value rather than a
// zero stat. `git log --shortstat` computes no diff at all for a merge
// commit unless asked to diff it against each parent individually (-m),
// which this package does not do — the result would be one stat line per
// parent, ambiguous to attribute to a single event. Reporting StateMerge
// keeps "no diff was computed" distinct from "a diff was computed and it was
// empty", which is a real, observed case (an --allow-empty commit) and would
// otherwise be indistinguishable from a merge — the same "absence is not an
// observation" mistake this project's other sources have already been
// caught making.
//
// # Locator, Identity and Cause
//
// Every event's Locator is the repository's own absolute path, so that
// internal/registry's directory-based attribution — which every other
// filesystem-rooted source in this project relies on — works for git
// evidence exactly the way it works for a Motion vault or a Blender backup.
// Identity is left empty: a commit is not about one application or one
// document the way a Motion autosave is, and forcing a commit's subject
// line into Identity.Document would stretch a field this project documents
// as meaning "a file or composition name" into something it was never
// designed to join against. Cause is CauseUserAction for every event both
// sources produce: everything either reader observes — a commit, a
// checkout, a reset, a rebase step — is git recording an explicit command a
// person typed, never a background timer or a crash-recovery write.
//
// # What this package does not do
//
// It does not register itself into source's package-level registry at
// init. A knowledgeC snapshot's path is a decision its caller makes on the
// day a harvest runs, and internal/knowledgec follows the same rule for the
// identical reason: a git repository's path is likewise a decision made by
// whoever registers a project (internal/registry), not a fixed location
// this package could know about in advance. Sources returns both collectors
// for a given repository path, for a caller to register once it knows which
// repositories it cares about.
package gitlog

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// Kind reports that both sources in this package are read by shelling out to
// the system git binary. It is shared between Commits and Reflog because
// Kind describes the READ mechanism, and both use the same one.
const Kind source.Kind = "gitlog"

// Source IDs. Every event either source produces carries one of these,
// which is what lets a suspicious number be traced back to log or reflog.
const (
	CommitsID = "gitlog-commits"
	ReflogID  = "gitlog-reflog"
)

// StateMerge marks a commit event whose diff-stat weight was not computed
// because the commit is a merge. See the package doc for why this is kept
// distinct from an observed zero.
const StateMerge = "merge"

// Stat is a commit's diff statistics, carried as an intensity weight rather
// than converted into any estimate of duration.
type Stat struct {
	FilesChanged int
	Insertions   int
	Deletions    int
}

// String renders a Stat in the fixed, ParseStat-compatible form stored in an
// event's State.
func (s Stat) String() string {
	return fmt.Sprintf("files=%d insertions=%d deletions=%d", s.FilesChanged, s.Insertions, s.Deletions)
}

// ParseStat reads a Stat back out of an event's State. It returns
// ok=false for StateMerge, an empty State, or anything else that is not one
// of the strings this package itself writes — a caller should not treat
// "did not parse" as zero, since zero is itself a meaningful, observed
// value distinct from "not applicable".
func ParseStat(state string) (Stat, bool) {
	var s Stat
	n, err := fmt.Sscanf(state, "files=%d insertions=%d deletions=%d", &s.FilesChanged, &s.Insertions, &s.Deletions)
	if err != nil || n != 3 {
		return Stat{}, false
	}
	return s, true
}

// Commits collects one Point event per commit reachable from any ref in the
// repository (`git log --all`), timed by committer date. See the package
// doc for why committer date and not author date.
type Commits struct {
	repo string
}

// NewCommits returns a source reading commits from the repository at repo.
func NewCommits(repo string) *Commits { return &Commits{repo: repo} }

// ID implements source.Source.
func (c *Commits) ID() string { return CommitsID }

// Kind implements source.Source.
func (c *Commits) Kind() source.Kind { return Kind }

// Collect implements source.Source.
//
// Every commit is read on every call and filtered here on committer date
// against since, rather than trusting git's own --since/--until — those
// filter by whichever date --date-order or a config setting resolves to,
// which this package does not want to depend on when its whole point is
// choosing a specific timestamp deliberately.
func (c *Commits) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := repoValid(ctx, c.repo); err != nil {
		return nil, err
	}
	if !hasHEAD(ctx, c.repo) {
		// An unborn HEAD is a repository with nothing committed yet, not a
		// wrong path — repoValid already ruled the latter out.
		return nil, nil
	}

	out, err := runGit(ctx, c.repo, "log", "--all", "--shortstat", "--date=iso-strict",
		"--pretty=format:"+commitLogFormat)
	if err != nil {
		return nil, fmt.Errorf("collect commits from %s: %w", c.repo, err)
	}
	records, err := parseCommitLog(out)
	if err != nil {
		return nil, fmt.Errorf("parse git log from %s: %w", c.repo, err)
	}

	locator, err := filepath.Abs(c.repo)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path of %s: %w", c.repo, err)
	}

	events := make([]source.Event, 0, len(records))
	for _, r := range records {
		if !since.IsZero() && r.committerDate.Before(since) {
			continue
		}
		e := source.NewPoint(r.committerDate, source.Identity{}, CommitsID)
		e.Locator = locator
		e.Cause = source.CauseUserAction
		if r.isMerge {
			e.State = StateMerge
		} else {
			e.State = r.stat.String()
		}
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events, nil
}

// Reflog collects one Point event per entry in the repository's own HEAD
// reflog (`git log -g HEAD`) — every checkout, commit, reset, rebase step
// and merge this specific working copy's HEAD has actually done, timed by
// the reflog's own recorded instant rather than any commit's author or
// committer date.
//
// Scope is deliberately HEAD only, not --all refs. HEAD's reflog already
// captures essentially everything a person does at the keyboard in this
// checkout — every operation that moves HEAD moves through it — where
// widening to every ref's reflog would additionally pull in remote-tracking
// ref updates from `git fetch`, which are a different kind of evidence
// (network activity, not necessarily local wall-clock presence) that this
// package does not attempt to characterise.
type Reflog struct {
	repo string
}

// NewReflog returns a source reading HEAD's reflog from the repository at
// repo.
func NewReflog(repo string) *Reflog { return &Reflog{repo: repo} }

// ID implements source.Source.
func (r *Reflog) ID() string { return ReflogID }

// Kind implements source.Source.
func (r *Reflog) Kind() source.Kind { return Kind }

// Collect implements source.Source.
func (r *Reflog) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := repoValid(ctx, r.repo); err != nil {
		return nil, err
	}
	if !hasHEAD(ctx, r.repo) {
		return nil, nil
	}

	out, err := runGit(ctx, r.repo, "log", "-g", "--date=iso-strict", "--pretty=format:"+reflogFormat, "HEAD")
	if err != nil {
		return nil, fmt.Errorf("collect reflog from %s: %w", r.repo, err)
	}
	records, err := parseReflog(out)
	if err != nil {
		return nil, fmt.Errorf("parse git reflog from %s: %w", r.repo, err)
	}

	locator, err := filepath.Abs(r.repo)
	if err != nil {
		return nil, fmt.Errorf("resolve absolute path of %s: %w", r.repo, err)
	}

	events := make([]source.Event, 0, len(records))
	for _, rec := range records {
		if !since.IsZero() && rec.at.Before(since) {
			continue
		}
		e := source.NewPoint(rec.at, source.Identity{}, ReflogID)
		e.Locator = locator
		e.Cause = source.CauseUserAction
		e.State = rec.action
		events = append(events, e)
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Start.Before(events[j].Start) })
	return events, nil
}

// Sources returns both collectors for the repository at repo, for a caller
// that has resolved which repositories it wants to collect from. Nothing in
// this package registers these automatically — see the package doc.
func Sources(repo string) []source.Source {
	return []source.Source{NewCommits(repo), NewReflog(repo)}
}
