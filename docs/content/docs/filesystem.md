---
title: "Filesystem observations"
weight: 22
description: "Turning a project's own birth and modification times into durable events, and why that is not the same job as internal/filedays."
---

# Filesystem observations

`internal/fsobserve` walks a project's own working files and turns every
timestamp APFS actually stores — creation and last modification — into a
`source.Event`. It is what makes a morning like 2026-07-28 06:00–06:50
visible at all: transcript-based inference scores that window as exactly zero
Claude activity, and it was a real Motion session.

## Not the same job as `internal/filedays`

[`internal/filedays`](/docs/sources/#mtime-evidence-decays-and-the-loss-is-invisible)
already walks a directory and reads mtimes. What it produces is deliberately
perishable: a count of files modified on a given day, true only at the moment
it was taken, because a file records only its *last* write and today's edit
on an old file silently deletes yesterday's evidence. It exists to capture
that count before it decays, and has to be taken on a schedule to mean
anything.

This package answers a different question and cannot substitute for that one.
It does not try to preserve a count against future decay — it re-derives,
from whatever files exist right now, every instant APFS can still attest to,
and hands back durable `source.Event`s meant for the permanent store. The two
packages share a directory walk and nothing else. A caller wanting a
defensible historical day count still wants `filedays`; a caller wanting
replayable evidence for the store wants this package.

## Two observations per file, usually

APFS keeps `st_birthtime` beside `mtime`, so a file carries up to two
independent instants: when it was created and when it was last written. Where
a file was written once and never touched again, the two coincide and
`fsobserve` emits **one** event rather than two identical ones.

Coincidence is judged at whole-second (`Unix()`) resolution, not exact
equality. Both timestamps are set by the same syscall at creation and can
differ by microseconds of scheduling jitter — on the machine this was built
on, a freshly created file showed birth and mtime 88 microseconds apart, both
inside the same second. A genuinely separate write is a different matter: a
real file in the validation repository below has a birth and mtime 658ms
apart, straddling a second boundary, and is correctly read as two
observations rather than one.

## A point, never an interval

Of 512 files touched on 2026-07-11 in a real repository, only 11 had a
birth-to-mtime gap over a minute, and those gaps were mostly enormous — one
spanned 09:55 to 23:41. Summing birth-to-mtime as if it were time spent on
the file would claim 70+ hours on a 13.9-hour day. Every event this package
emits is a `source.Point`: something happened at that instant, and the gap
means the file was touched *at least* twice — what happened between is
genuinely unknown and is never represented here as a duration.

## Source documents versus generated output

A thousand exported frames are one render action; a handful of source saves
are a day of decisions. Weighing them equally would make a batch export look
like a thousand times more decision-making than it was. Every event carries
that distinction in its `State` field:

| `State` | Meaning | Extensions |
| --- | --- | --- |
| `source` | A project file a person edits directly | `.motn` `.psd` `.psb` `.blend` `.blend1` `.ai` `.aep` |
| `output` | Generated from a source document | `.webp` `.png` `.jpg` `.jpeg` `.mov` `.mp4` `.gif` `.exr` `.tif` |
| `other` | Neither — every event gets a class, never a blank one | anything else |

`.blend1` is classified `source` alongside `.blend`: it is Blender's own
backup of a source document, written only because somebody pressed save —
the same reasoning the built-in `blender-backups` manifest already uses to
declare it `user-action`.

`State` was chosen over adding a field to `source.Identity`, on the reasoning
that classification is a static property of what was observed — this
artifact *is* a compositor project file, or *is* a rendered frame — which is
exactly what `State` already exists to carry for other sources (knowledgeC's
`isBacklit` reports `on`, `off`, or nothing sampled, the same shape). It also
keeps this package confined to its own files: two sibling collectors were
being written against `internal/source` at the same time this one was, and a
field added to a struct three agents share is a needless place to collide.
`Classify(path)` is exported directly, for a caller that wants to classify a
locator without re-deriving the extension table.

## Cause is always `user-action`

Unlike Motion's autosave vault, which writes on a wall-clock timer whether or
not anyone is at the machine, a file changing inside a project's own working
tree has no timer behind it — it changes because somebody saved it, exported
it, or created it. Every event this package produces declares
`source.CauseUserAction`.

## Which directories get walked

This package does not decide that. `Walker` takes its roots as an explicit
parameter:

```go
events, err := fsobserve.New("/Users/you/git/acorn").Collect(ctx, since)
```

`timetattle harvest` builds that roots list from
[the registry](/docs/registry/)'s enumeration function, `registry.List`,
rather than searching the filesystem for markers itself — see
[Harvesting presence data](/docs/harvest/). One `Walker` covers every
registered directory in a single run, because `Roots` is a slice for exactly
that reason.

For the same reason `internal/registry` has no central table of every
directory it has blessed — identity lives in the marker inside each
directory, not somewhere else — this package does not self-register at
`init` the way `internal/biome` does against a fixed OS path: there is no
fixed default here to point at. A caller assembling its own roots list some
other way can still register it directly:

```go
source.Register(fsobserve.New(roots...))
```

### Overlapping roots are collapsed, not walked twice

`Register` has no rule against blessing a directory and a sub-folder inside
it — a repo plus a scratch render directory nested within it, say — so
`registry.List` can hand this package two roots where one sits inside the
other. `Walker` resolves every root to a canonical, symlink-free absolute
path before deciding what to walk, and drops any root that names the same
directory as, or is nested inside, another root that survives. A registered
parent and a registered child of it therefore contribute each file exactly
once, not twice, and the same collapse catches two roots that are the same
directory reached through different strings — an unresolved relative path,
a trailing slash, or an intermediate symlink such as macOS's own `/tmp` →
`/private/tmp`.

The canonical form is used only to decide equivalence. What actually gets
walked, and what ends up in each event's `Locator`, is still the caller's
own path string — resolving symlinks for real would silently change every
locator downstream, which this package does not do.

Measured on a synthetic registered-parent-plus-registered-child case (one
file directly under the parent, three nested two-timestamp files under the
child): before this normalisation, `Collect` returned 13 events against a
harvest run recording `events_seen=13`, `events_new=7`, and 7 actual rows —
the store's `event_identity` unique index absorbed the 6 duplicate inserts
silently, so the only visible cost was wasted walk time and an inflated
`events_seen`. After normalisation, the same case reports
`events_seen=7`, `events_new=7`, 7 rows: the duplication no longer happens
in the first place.

### A root that is itself a symlink

Registering a project through an alias — a symlink to the real checkout, a
symlinked home directory, a bind-mount-style layout — walks the real
directory and reports the same observations as registering it by its real
path:

```
timetattle chains -roots ~/git/acorn/blender -ext .blend
  19 observations considered, 19 documents, 17 in a chain, 2 in none

ln -s ~/git/acorn/blender $TMPDIR/blenderlink
timetattle chains -roots $TMPDIR/blenderlink -ext .blend
  19 observations considered, 19 documents, 17 in a chain, 2 in none
```

Only the counts match; the `Locator` does not, and deliberately. A file
found through `$TMPDIR/blenderlink` is reported as
`$TMPDIR/blenderlink/lightbar.blend`, under the path you registered, not
under `~/git/acorn/blender` and not under the fully resolved
`/private/var/…`. That is the same promise the section above makes: the
resolved form exists so the walk can start, never so a caller can read it.

Before this was fixed the second command returned `0 observations
considered` — no error, no warning, exit status 0 — because
`filepath.WalkDir` calls `os.Lstat` on its own root argument and does not
follow a symlink there, so the walk was handed a non-directory and never
descended.

### Zero because empty, or zero because unreadable

Those are different answers and the walk says which. A root that could not
be read warns and names itself:

```
WARN no files collected; this root does not exist, or is a symlink to
     something that no longer does  root=/…/dangling
WARN no files collected; this root is not a directory  root=/…/notes.txt
WARN no files collected; this root is a symlink that could not be resolved
     root=/…/alias  error=…
WARN skipping a path that could not be read  path=/…/lockedproj
     error="open /…/lockedproj: permission denied"
```

A registered project that simply has not been saved into yet collects
nothing and stays silent, because that zero is correct. Without the
distinction, every downstream report of an unwalkable root reads as "no
filesystem work happened here" rather than "this root was never walked",
and both readings are plausible.

## What is excluded

A directory can contain nested copies of itself — a worktree, a
`node_modules` tree, `.git`'s own object store — whose files are not second
helpings of evidence, they are the same evidence again. One measured case in
this project found a directory 86x inflated by exactly this: 6,103 raw files
against 71 once worktree copies were excluded. `fsobserve` skips these
directories whole, mirroring `internal/filedays`' own default exclusions:

```
.git  worktrees  node_modules  .venv  .Trash  Autosave Vault
```

It also skips `.DS_Store` and this project's own `.timetattle-project.json`
marker — housekeeping, not evidence of creative work.

`.venv` is the Python form of `node_modules`, and it was measured before it was
added: on 2026-08-01 a `.venv` held 27,645 of 130,594 attributed filesystem
observations — 21.2% — against 0 for `node_modules` and 0 for `.git`, which
were already skipped. A `pip install` writes a whole dependency tree in one
burst and every file in it carries a fresh modification time.

**The names are matched WHOLE, and the list is as narrow as the evidence.** A
directory called `env`, `venv` or `.venv-old` is NOT skipped. Virtualenvs do go
by other names, but in the measured store `env` and `venv` between them named
two files and both were source somebody wrote — one of them this project's own
`scripts/spike/env/main.go`. Widening the rule to catch a virtualenv nobody has
would delete work somebody did.

**This applies where evidence is COLLECTED, not where it is read.** It stops the
next harvest and changes nothing already in the store; the read-time rules are
`sessions.exclude` and the [SQLite sidecar rule](#sqlite-sidecars). For this
particular rule the difference is immaterial and that was measured too — see
[project/private/2026-08-01-virtualenvs-are-volume-without-harm.md](https://github.com/bensyverson/timetattle/blob/main/project/private/2026-08-01-virtualenvs-are-volume-without-harm.md).

### SQLite sidecars

`fsobserve` skips any file whose name ends in `-wal` or `-shm`: SQLite's
write-ahead log and its shared-memory index. They cannot exist without the
database file they belong to, carry no content of their own, and are deleted on
a clean close, so they are nobody's deliverable on any machine — the same
category as `.DS_Store`.

**The volume is the wrong measure.** Only 86 of the 130,361 filesystem
observations in the live store on 2026-08-01 were sidecars, 0.1%. But one
observation is enough to put a project row on a day's review screen, and on
2026-08-01 seven unrelated repositories — `alder`, `bramble`, `corwin`,
`davenport`, `eastlake`, `fenwick`, `gable` — each showed a whole working day built out
of nothing but a WAL checkpoint, every stamp inside the same second. Real work
does not touch seven repositories in one second.

`-journal` is deliberately **not** in the rule. SQLite's rollback journal is the
same kind of file, but `2026-08-01-journal` is a perfectly ordinary name for a
diary entry, and this collector would rather miss a rollback journal than delete
somebody's day.

This is the only file-content rule in code. A `.db` or a `.log` **can** be
somebody's real deliverable, so excluding those is a declaration the user makes
in `sessions.exclude` — see [Sessions](/docs/sessions/#excluding-machine-activity).

Skipping at collect time governs the *next* harvest and nothing else: the store
has no delete, so the same predicate is applied again where events are read,
which is what makes the rule reach the observations already stored.

## Validated against real data

`internal/fsobserve/realdata_test.go` walks a real repository on the machine
this was built on and checks the 2026-07-28 06:00–06:50 Motion session
directly, rather than against a fixture built to already agree:

- `scanline2-face-granted.motn` was born at 06:00:14 and last saved at
  06:50:34 — almost exactly the session's own edges — and produces two
  events, one per instant.
- `scanline3-face-granted.motn`, born the same second, has an mtime that
  moved to the *next day*. Its birth is the only observation left inside the
  session window; a collector reading mtime alone would show nothing here at
  all. This is `filedays`' own decay problem, caught in the act on a real
  file.
- Four PNG exports with birth and mtime in the same second produce one event
  each; one (`big-x-red-2.png`) has a genuine 658ms gap crossing a second
  boundary and correctly produces two.
- The `.motn` composition classifies as `source` and its PNG exports classify
  as `output`, and the composition's `App` joins to `com.apple.motionapp`.

The test is guarded to skip cleanly on a machine without that repository.
Re-derive the timestamps it asserts against with:

```console
$ stat -f "%SB %Sm" -t "%Y-%m-%d_%H:%M:%S" <file>
```

This one stays real-data-only rather than gaining a checked-in fixture
alongside it: it is validating a specific historical claim (that a real
Motion session transcript-based inference scores as zero activity is
recoverable from the filesystem), which by definition no constructed fixture
can stand in for. The underlying mechanism it exercises — birth and mtime
producing two events when they genuinely differ, one when they coincide,
source/output classification, and the exclusion of nested worktree copies —
is already exercised portably in `internal/fsobserve/fsobserve_test.go`
against files this package creates and stamps itself with `os.Chtimes`, so it
runs the same way on any machine, real repository or not.
