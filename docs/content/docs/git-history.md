---
title: "Git history"
weight: 33
description: "Reading a repository's own log and reflog as evidence, and why they cannot share a clock."
---

# Git history

`internal/gitlog` turns a git repository's own history into evidence, by
reading **both** `git log` and `git reflog` and treating them as different
kinds of evidence rather than two views of one fact.

| Source | Reads | What it observes |
| --- | --- | --- |
| `gitlog-commits` | `git log --all` | Every commit reachable from any ref |
| `gitlog-reflog` | `git log -g HEAD` | Every checkout, commit, reset, rebase step and merge this checkout's `HEAD` has actually done |

Both are durable: unlike a knowledgeC snapshot or a Biome segment, nothing
prunes a git repository's own history, so there is no harvest schedule to
race and no window that closes.

## Why both, and why they cannot share a clock

`git log` reports what a repository's history now **says**. `git reflog`
reports what this specific checkout's `HEAD` actually **did**, in the order
it really happened, including a checkout that never became a commit at all —
`git log` cannot see that at all, because nothing was ever recorded as a
commit.

That difference is why they need separate handling of "when did this
happen":

**A commit's author date is set once**, when its content is first drafted,
and an ordinary `git commit` never touches it again. But `git commit --amend`
and `git rebase` **rewrite** the commit object while preserving that original
author date by default. The result carries today's content under a timestamp
from whenever the first draft happened — possibly days earlier, possibly on a
different machine. Treating author date as "when this happened" would
silently credit a Tuesday-afternoon rebase to whatever Saturday morning the
first draft carried.

**A commit's committer date** is written fresh every time the commit object
is created or rewritten, unless an operator explicitly overrides it. It is
the closest `git log` alone can get to "when this machine last wrote this
ref", so `gitlog-commits` reports **committer date**, never author date, as
an event's time.

**A reflog entry's date** needs none of that reasoning: git records it as the
entry is written, in the moment, and nothing afterwards can rewrite it — a
rebase creates *new* reflog entries for each step rather than editing old
ones. `gitlog-reflog` reports exactly that recorded instant.

```console
$ go test ./internal/gitlog/ -run UsesCommitterDateNotAuthorDateAfterAmend -v
```

reproduces the mechanism directly: a commit is authored on a backdated date,
then amended without resetting the author date — exactly what `--amend` and
`rebase` do by default — and `gitlog-commits` reports the fresh committer
date, not the stale author date.

Measured against this repository's own history reachable from a normal
checkout: **0 of 41** commits carry a committer date that differs from their
author date, because every commit here is made once and merged with `git
merge`, never amended or rebased in place. That is a real property of this
project's workflow, not a property of the mechanism — the constructed test
above is what proves the mechanism holds when history *is* rewritten; see
`TestRealAuthorAndCommitterDatesInThisRepo`.

## Diff statistics are a weight, not a duration

Every non-merge commit event's `state` carries a diff-stat string:

```
files=7 insertions=466 deletions=1
```

`gitlog.ParseStat` reads it back into a `Stat{FilesChanged, Insertions,
Deletions}`. Nothing in this package converts that into an estimated
duration — a one-line fix and an 800-line refactor are not assumed to take
proportional time, because nothing here has measured what the true
relationship is. It exists so a downstream consumer can **weight** commits
against each other, calibrated separately, rather than Timetattle inventing
an hour-per-commit rule.

Merge commits carry the distinct value `gitlog.StateMerge` ("`merge`")
instead of a stat. `git log --shortstat` computes no diff at all for a merge
commit unless asked to diff it against each parent separately, which would
produce one stat line per parent — ambiguous to attribute to a single event —
so this package does not ask for it. Keeping that "not computed" state
distinct from an observed zero (an `--allow-empty` commit, a real and
unremarkable case) matters: collapsing the two would report every merge as
"no work done", which is the same absence-is-not-an-observation mistake
this project's other sources have already been caught making.

## What an event carries

`locator` is the repository's own absolute path, so
[the registry](/docs/registry/)'s directory-based attribution works for git
evidence exactly the way it works for a Motion vault or a Blender backup.
`identity` is left empty — a commit is not about one application or document
the way a Motion autosave is, and a commit's subject line has nowhere in the
current event schema that means what it means without stretching a field
documented for something else. `cause` is always `user-action`: everything
either reader observes is git recording an explicit command a person typed,
never a background timer or a crash-recovery write.

## No self-registration

Neither source registers itself at `init`. A git repository's path is a
decision made by whoever has registered a project with
[the registry](/docs/registry/), the same reason `internal/knowledgec`
does not self-register against a fixed snapshot path either — there is no
single repository this package could know about in advance.

```go
events, err := gitlog.NewCommits(repoPath).Collect(ctx, since)
events, err := gitlog.NewReflog(repoPath).Collect(ctx, since)

// or, for both at once:
sources := gitlog.Sources(repoPath)
```

`timetattle harvest` calls `Sources` once per directory `registry.List`
reports, skipping any that has no `.git` — a registered Motion asset folder
or scratch render directory is exactly as likely to be registered as a
repository, and this package has nothing to read from one. See
[Harvesting presence data](/docs/harvest/).

## What this package does not do

`gitlog-reflog` reads only `HEAD`'s own reflog, not `--all` refs. `HEAD`'s
reflog already captures essentially everything a person does at the
keyboard in one checkout — every operation that moves `HEAD` moves through
it. Reading every ref's reflog would additionally pull in remote-tracking
ref updates from `git fetch`, a different kind of evidence (network
activity, not necessarily local wall-clock presence) this package does not
attempt to characterise.

It does not carry a commit's subject line, author name or parent list into
the event. Those answer *what* happened and *who*, and this task's scope was
*when* and *how much* — the diff-stat weight above. Nothing in
`source.Event` currently has a place for a free-text message without
stretching a field documented to mean something narrower.
