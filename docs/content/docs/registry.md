---
title: "Registering a project"
weight: 25
description: "How to tell Timetattle which client and project a directory belongs to, and how that identity is found again later."
---

# Registering a project

Harvested evidence carries only where it was found — a working directory, an
autosave's path, a bundle identifier — never who it was for. `timetattle
register` closes that gap for the evidence that has a locatable path, by
letting you bless a directory once and have every event found beneath it
resolve to a client and project.

Registration is **opt-in**, and that is deliberate. Of 182 repositories one
measurement found, 19 held 91.5% of engaged time; registering all of them
would be mostly wasted effort. Anything unregistered still counts toward
presence — a day is not shorter because nobody named the directory it
happened in — it just does not resolve to a project. A large unattributed
bucket in a report is Timetattle telling you to register something, not an
error.

## Registering a directory

```
timetattle register -client ACORN -project "Holiday Campaign"
```

Run from inside the directory to bless — typically a repository root — or
pass `-path` explicitly:

```
timetattle register -client ACORN -project "Holiday Campaign" -path ~/Movies/ACORN/holiday-render
```

This writes a small JSON file, `.timetattle-project.json`, into that
directory:

```json
{
  "marker_version": 1,
  "client": "ACORN",
  "project": "Holiday Campaign",
  "registered": "2026-07-31T18:22:01.123456Z"
}
```

That file is the entire registry. There is no separate table mapping paths
to projects anywhere else, which is what makes registration robust to the
directory being moved or cloned: the identity travels with it.

Running the command again with the same client and project is a no-op — the
existing marker is returned unchanged, so a setup script can register
unconditionally on every run. Running it again with a *different* client or
project is refused unless `-force` is given, so a mistyped path cannot
silently reassign a directory someone else already blessed.

## Evidence that is already stored

Evidence usually predates the registration, so `register` attributes what the
store already holds under the directory before it returns, and says how much:

```console
$ timetattle register -client ACORN -project "Holiday Campaign"
/Users/you/Movies/ACORN/holiday-render registered to ACORN / Holiday Campaign
attribution: attributed 42 stored observations to Holiday Campaign
```

It resolves each path exactly as a harvest does, so a directory with its own
marker inside this one keeps its own evidence, and that evidence is counted
separately. The count is of observations reports count: stored [build
output](filesystem.md) is attributed with the rest but left out of it. The store
is the one `-store` names (default
`~/.timetattle/timetattle.db`); `register` never creates the default one.

**While a harvest holds the store's write lock**, which a large store's
attribution pass can do for many minutes, registration still succeeds. It waits
a few seconds, then says the stored evidence is not yet attributed. A harvest
that starts from then on attributes it, and so does running the same `register`
command again once the lock is free. A harvest already running may not: it may
have passed those paths before the marker existed. Running the command again on
a directory whose evidence is already attributed writes nothing to attribution,
so the lock does not stop it saying so.

It looks under the directory as you named it, under its physical path if that
differs, and under any [former path](#when-a-directory-is-renamed-or-moved-away)
its marker declares. Former paths are skipped, and `register` says so, when the
[directory index](#finding-every-registered-directory) could not record the
marker: a harvest reads them through that index alone. Evidence spelled through some *other* symlink pointing into
the directory, or recorded in a git worktree outside it, waits for the next
harvest.

## One project, many directories

A project is not limited to one directory. A repository, a shared asset
folder outside any repository, a scratch render directory — register each of
them with the same client and project, and each gets its own marker:

```
timetattle register -client ACORN -project "Holiday Campaign" -path ~/git/acorn-site
timetattle register -client ACORN -project "Holiday Campaign" -path "/Volumes/Work/ACORN/Holiday Assets"
```

There is nothing to reconcile between the two; resolution treats each marker
independently and both happen to agree on the identity they name.

### One project name belongs to one client

Agreement is checked rather than hoped for. Registering a project name that
another directory already holds **for a different client** is refused, and the
refusal names the directory and the client holding it:

```console
$ timetattle register -client GLOBEX -project website -path ~/git/globex-site
register: project "website" is already held for a different client by
/Users/you/git/acme-site (ACME), and every directory naming one project has to
agree on its client — otherwise a report grouped by project silently merges two
clients' hours. Register /Users/you/git/globex-site under a project name of its
own, or pass Force to move this directory to GLOBEX and re-register the
directory listed above
```

Without that rule, two clients could each keep a project called `website`, both
registrations would succeed, and every report that groups by project — which is
every report `timetattle metrics` prints — would merge their hours into one row
with nothing to say it had happened.

The many-directories case above is unaffected: same client *and* same project is
agreement, and stays a plain success.

### Renaming a client

Re-register each of that client's directories with `-force`, one at a time.

**You must say which kind of change it is**, because the two mean opposite
things and neither can be guessed:

| Flag | Meaning |
|---|---|
| `-since 2026-07-01` | the client **changed** on that date. Work before it keeps the previous client — which is what an invoice already sent depends on. |
| `-correction` | the previous client was **never right**. Every hour this project has ever had moves to the new one. |

A *first* registration needs neither and is retroactive: evidence under a
directory usually predates anybody getting round to blessing it, so an assertion
beginning when it was typed would attribute none of it. That is exactly why a
*re*-registration cannot silently default to "from now" — two near-identical
commands would mean opposite things with nothing to say which had happened.

```console
$ timetattle register -client GLOBEX -project website -path ~/git/site -force
timetattle register: ~/git/site already belongs to ACME, and moving it to GLOBEX
could mean either of two things, which cannot be guessed from here:
  -since 2026-07-01   the client CHANGED on that date. Work before it stays
                      with ACME, which is what an invoice already sent depends on.
  -correction         ACME was never right. Every hour this project has ever
                      had moves to GLOBEX.
Pass whichever is true
```

The refusal for a *project* held by another client lists the directories still
to do, so the remaining work is visible rather than remembered.

While a rename is half done the directories genuinely disagree — some naming the
old client, some the new — and that is expected rather than a broken state.
Each event carries the client its **own** directory named, so reports stay
correct throughout and show the rename in progress. `-force` moves the one
directory it was pointed at and never silently rewrites the others.

## How a path resolves back to a project

Resolution walks upward from a given directory, checking each ancestor in
turn, until it finds a marker or runs out of parents. A file three levels
inside a registered repository resolves to that repository's root; a
directory that was never registered resolves to nothing, which is reported
as unattributed rather than as an error.

Three things are tried, in this order, and the first one that finds a marker
wins: the path as observed, then the path with its symlinks resolved, then the
repository the path belongs to. The order is the rule, not an implementation
detail — see each section below for what the ordering buys.

### Symlinks into a project

Walking upward is *lexical*: it strips the last component of the path it was
given, which is not the same as stepping into the parent directory on disk. A
path that reaches a project through a symlink pointing at a **subdirectory** of
it therefore escapes the project when the walk steps above the link, because
the link's textual parent is the link's own parent rather than the physical
subdirectory's:

```
~/work/widget/          <- registered
~/work/widget/assets/
~/current -> ~/work/widget/assets
```

Nothing above `~/current` holds a marker, so on its own the upward walk reports
`~/current` as unattributed. When that happens, resolution retries once from
the path with its symlinks resolved — `~/work/widget/assets`, from which the
marker at `~/work/widget` is an ordinary ancestor.

A link pointing *at* a registered directory, or at a directory just below one,
was never affected: reading the marker follows the link like any other path
lookup. Only a walk that steps above the link leaves the tree.

The path **as observed** is tried first, and that matters when a link sits in
one registered project and points into another. The evidence was seen at the
path it was seen at, so the project that path is under is the answer; resolving
symlinks is a fallback for a path that resolved to nothing, never a rewrite of
where the evidence came from. Nothing in the store is rewritten either — a
locator is always the path that was observed.

A link that cannot be resolved — broken, cyclic, or naming a directory that has
since been deleted — is simply no second spelling to try, and leaves the answer
as the upward walk found it.

### Git worktrees are not their own project

A git worktree is a real repository — it has its own `.git` — so a resolver
that simply asked "what repository is this" would stop there and treat the
worktree as unregistered, even though the repository it was forked from is
registered. Agent worktrees under `.claude/worktrees/agent-*` are the
concrete case this matters for: one repository can hold dozens of them, and
none of them should need registering separately.

Resolution handles this by normalising with git rather than assuming
anything about where a worktree sits. If plain upward walking finds no
marker, and the directory is inside a linked worktree, `git rev-parse
--git-common-dir` names the *main* repository's `.git`, and resolution
climbs from its parent instead. This works whether the worktree is nested
under the repository it belongs to, sits beside it, or lives in a scratch
directory such as `/tmp` — nothing about the layout is assumed.

A marker registered directly inside a worktree still wins over its parent's,
the same way any nearer marker wins over a farther one; the git fallback only
runs when neither the path as observed nor its resolved form finds anything.

It runs last for a reason. It answers with the repository *root* and then walks
upward from there, so it can never see a marker sitting between that root and
the directory being resolved — it would return the outer project, confidently,
rather than the nearer one. Resolving symlinks first keeps the nearer marker
reachable. It also covers the case git cannot help with at all: a registered
directory that is not a repository.

### One harvest asks about a directory once

A store holds many locators per directory, so the attribution pass remembers
what each directory answered — whether it holds a marker, where its symlinks
lead, which repository it belongs to — and asks the filesystem and git once.
The memory lasts for that one pass and is never saved. A marker added or
removed while a pass is running is picked up by the next one. A directory that
could not be read stays unreadable for the whole pass: its events keep the
attribution they had.

Git is the exception to remembering. Its answer is kept when it names a
repository or says there is none. A git that could not be run, was killed or
failed for another reason is asked again for the next locator in the directory.
The locator it failed on is still treated as outside any repository.

Git is not run at all for a path that is not a directory, such as one that has
since been deleted, because it could only fail there.

`go run ./scripts/attributionprofile -store <path>` times a pass over a copy of
a store, phase by phase; `-nocache` times it without the memory.

## When a directory is renamed or moved away

Evidence is recorded under the path it was **observed** at, permanently, in a
store that is only ever appended to. Rename a repository and everything
recorded under the old name is stranded twice over: no marker can be written
into a directory that no longer exists, and registering the new location does
not help, because the stored paths still name the old one.

`timetattle consolidate` declares the connection:

```
timetattle consolidate -path ~/git/lantern -former ~/git/the-lantern-app
```

`-path` must already be registered. `-former` is written into *its* marker,
as part of that directory's identity, alongside the client and project:

```json
{
  "marker_version": 1,
  "client": "personal",
  "project": "lantern",
  "former_paths": ["/Users/you/git/the-lantern-app"],
  "registered": "2026-08-01T10:41:55.201947Z"
}
```

Keeping it there rather than in a table elsewhere is the same choice the
marker itself represents: "I was once over there" travels with the directory
when it is moved or cloned, and there is nothing external to keep in sync.
Adding the field needed no `marker_version` bump — it is omitted entirely
when empty, so a marker written by `register` alone is unchanged.

`-former` matches by **prefix**, so declaring a repository root also recovers
everything that was underneath it. This is not a detail: on one real rename,
14,218 stranded observations split 9,924 at the renamed directory itself and
4,294 one level below it, so an exact-path rule would have recovered 70% and
looked like it worked.

Nothing needs re-collecting afterwards. `project` is a recomputable cache, so
the next `timetattle harvest` re-attributes what the declaration now covers.

To see what a declaration would be worth before making it:

```
go run ./scripts/consolidatecheck -path ~/git/lantern -former ~/git/the-lantern-app
```

It reads the store read-only, writes nothing, and reports observation counts
— not hours.

### It declares a rename; it never guesses one

There is no rename detection, deliberately. Two confirmed renames in one real
store produced two incompatible signatures — one where the old and new
directories were disjoint and two days apart, one where they overlapped on the
only day the old path existed — and a rule loose enough to catch the second
would also fire on a genuinely abandoned experiment. Inventing an owner for
evidence nobody recognises is worse than leaving it unattributed, so the
mapping comes from someone who knows.

That is why the refusals are shaped the way they are. `consolidate` refuses a
`-former` path that still exists (that is a live directory: register it), one
another project already claims (a vanished path cannot have been two projects
at once), a `-path` that is not already registered (so the command cannot be
used to conjure a project around unfamiliar evidence), and a `-former` path
inside a declared shared cache, for the same reason `register` refuses to
bless one.

### Which claim wins

A path can be spoken for twice: by a marker found while walking upward, and by
a former path someone declared. One rule settles it, and it is the rule
walking already uses — **the most specific claim wins**.

So a marker at or below the former path beats the declaration. A former path
must not exist when it is declared, but nothing stops a new repository being
cloned there later and registered to someone else, and from then on the
evidence there really is theirs. And a declaration beats a marker further up
the tree, so blessing a container directory does not silently shadow every
rename declared beneath it.

## What is refused

A marker never holds a rate, a budget, or anything else about a contract.
Those live in global configuration, never inside a file this command writes,
because that file sits inside a client's own repository and can be committed
by accident. `register` only ever writes client and project identity.

Registration also refuses to bless a directory a manifest already declares
as a shared application cache — see [Adding an evidence
source](/docs/sources) for what that means. Apple Motion's Autosave Vault
holds every client's compositions in one place; blessing it, or any
directory above it, would attribute all of them to whichever project you
had in mind when you ran the command, and the result would look entirely
plausible right up until someone checked. The command refuses this whether
the cache directory is the exact path given, an ancestor of it, or a
directory inside it.

That includes the filesystem root, which sits above the vault like everything
else. It did not until 2026-08-01: the containment check appended a separator
to the directory it was asked about, and for `/` that produced `//`, which is a
prefix of nothing. The root therefore contained nothing, and `register /` would
have succeeded. It was found by [`timetattle orient`](orient.md), which asks the
same question in order to avoid recommending a registration that would be
refused, and which had 1,311 observations carrying the locator `/` to ask it
about.

[`timetattle orient`](orient.md) is also the place to see which directories are
worth registering. It reports every observation carrying a path no project owns,
grouped by what would clear it, and never names a directory `register` would
refuse or one whose marker an existing registration already covers.

## Where this fits

`project` on a stored event is a nullable column, filled by a resolution
pass against the registry rather than written at collection time — see
[The event store](/docs/store). That pass is not part of this command; it
reads what `register` writes.

`timetattle harvest` runs that pass over every stored path automatically, every
time, after ingestion — not only when new events arrive. `register` also runs
it, limited to the directory it just blessed — see below. There is no separate
command to remember to run.

## Finding every registered directory

A marker lives inside the directory it blesses, and markers can be scattered
anywhere on disk with nothing else connecting them — there is no fixed root
to search. Two collectors need exactly that list, though:
[Filesystem observations](/docs/filesystem/) needs every registered directory
to walk, and [Git history](/docs/git-history/) needs every registered
directory that is a git repository, to read one at a time.

`register` records the directory it just blessed into a small index,
`-index` (default `~/.timetattle/registered-directories.json`), separate
from the marker itself:

```json
{
  "index_version": 1,
  "directories": [
    "/Users/you/Movies/ACORN/holiday-render",
    "/Users/you/git/acorn-site"
  ]
}
```

If that index cannot be written — a read-only home directory, a full disk —
`register` still succeeds: the marker is the fact that matters, and a
warning is printed rather than the command failing over what is ultimately a
convenience for enumeration. Re-running `register` later retries it.

The index is never trusted at face value. It only ever grows — nothing
removes an entry from it — so an entry can point at a directory whose marker
was since deleted, moved, or corrected to a different client and project.
Reading it back with `registry.List` re-checks the actual marker at every
directory the index names, exactly the way `Resolve` already treats a
marker as the one fact and everything else as a note pointing at it. A
directory whose marker is gone is silently excluded, the same tolerance
`Resolve` gives a path that resolves to nothing; a directory whose marker was
corrected shows the corrected identity, not whatever was true when it was
first indexed. `timetattle harvest -index` reads this list on every run, so
removing or correcting a registration is reflected on the very next harvest,
not cached stale.
