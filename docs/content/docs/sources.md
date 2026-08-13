---
title: "Adding an evidence source"
weight: 20
description: "How Timetattle finds evidence, and how to teach it about a new application."
---

# Adding an evidence source

Timetattle reconstructs work from traces left in unrelated places: a binary
event log maintained by macOS, a SQLite database, autosave files dropped on
disk by a video compositor. Adding a new one should not mean changing the
programs that read the existing ones.

There are two ways in. Most applications need the first.

## The short version

If your application leaves **timestamped files on disk**, you can add it by
writing a JSON manifest. No Go, no rebuild.

If your application needs real parsing — a binary format, a database, an API —
it needs a small Go package implementing one interface.

## What every source produces

Both routes converge on the same thing: a stream of events, each carrying when
it happened and what it was about.

Three properties matter, and they are deliberately independent:

**How a source is read** — a Biome segment log needs a binary parser; a Motion
autosave vault needs a directory walk. This is the source's *type*.

**What an event means** — a focus interval occupies a span of time; a file save
happens at an instant. This is what the source *emits*: `interval` or `point`.

**Why the evidence exists** — a `.blend1` backup is written by an explicit save;
a Motion autosave is written by a wall-clock timer. This is the source's
*cause*: `user-action`, `timer` or `recovery`.

None of the three implies the others. [Git history](/docs/git-history/) is a
different type emitting the same shape as Motion autosaves — a point per
commit or reflog entry — while working through a clock problem of its own,
since a commit's own author date can be rewritten by rebase or amend. Blender
backups and Motion autosaves share a type, a reader and a shape, and differ
only in cause —
and that difference is worth 4 of 4 against 9 of 15 when the artifacts are
joined to their own application's focus intervals. Keeping the three apart means
the parts of Timetattle that reason about time never need to know about file
formats, and the part that reasons about presence never has to guess which
application it is looking at.

**What it is worth as evidence somebody was there** — a fourth axis, and the one
none of the three above answers. A walked file modification time says a file
changed; it does not say who or what changed it, and a log-rotation cron once
read as 126.2h of presence. A Claude message, a commit and a held task claim all
record a person *doing* something.

This is a judgment about **sources**, made in `internal/source` and named there
rather than deduced from a field that does not carry it: `fs-observations`
declares all 130,594 of its observations `user-action`, and 1,755 of them were
written by cron. It is a whitelist, so a source nobody has weighed reads as weak
— "nothing has been established about this source" and "this source records
acts" are different facts, and defaulting to the second credits an unweighed
stream with a person's presence, silently. Adding a source therefore means
adding a line there once its cause has been measured; until then its days will
be marked `!` in [review](/docs/review/) and its hours will show in the `weak`
column of [metrics](/docs/metrics/), which is the visible failure rather than
the invisible one.

There is a third shape, `unbounded`, for an observation seen at only one edge:
something happened and how long it lasted was never recorded. No source
*declares* that it emits unbounded events, because it is not a property of the
source — it is what a particular record turned out to be. Only a reader working
from paired records produces them, and only Biome does today. See
[App focus](/docs/app-focus) for what the missing edge means and how it is
repaired.

### Identity is what makes evidence useful

An event carries an `app` bundle identifier and a `document`. This is not
labelling — it is the join.

`project` is also part of an event's identity, but a manifest never sets it. A
composition or file name is not a client project — `dark-crystal-v2` is not
ACORN, ACORN is — so that field is left empty here and filled later by a
resolution pass that maps documents to the projects a person has registered.
See [Registering a project](/docs/registry) for how that registration works.

macOS can tell you Motion held focus from 13:49 to 14:25. It cannot tell you
*which composition* was open. A Motion autosave can tell you `dark-crystal-v2`
was written at 14:01, but not for how long anyone worked on it. Neither answers
"how much time went into dark-crystal-v2" alone. The `app` field is what lets
one be attributed to the other, so a manifest that omits it produces evidence
that cannot be connected to anything.

### Where the evidence was, as distinct from what it is about

An event also carries a `locator`: the absolute path it was read from. File
sources set it automatically — there is nothing to declare in a manifest.
Biome leaves it empty, because a bundle identifier is not a location, and so
does presence, which is not about a file at all.

It is provenance rather than identity, and the distinction is load-bearing.
Attribution's first pass matches a locator against the directories a project
has blessed, so this is the field that lets file evidence resolve to a project
*directly* instead of falling back to the name join. Deduplication needs it
too: two autosaves sharing a name in different vaults are different evidence.

But it stays out of identity. The autosave-vault copy and the project-directory
copy of one composition must compare *equal*, because that equality is the name
join by which shadow-cache evidence reaches a project at all. A path inside
identity would make every copy of a document a different document.

A relative locator is refused rather than accepted, because it never matches a
registered directory — the work would attribute to nothing and quietly vanish,
which is the failure mode this project exists to catch.

## Writing a manifest

Here is the manifest for Motion, which ships with Timetattle. It is
`internal/manifest/builtin/motion.json` with its `description` shortened; every
other field is exactly what the binary carries.

```json
{
  "manifest_version": 1,
  "sources": [
    {
      "id": "motion-autosave",
      "type": "timestamped-files",
      "emits": "point",
      "cause": "timer",
      "app": "com.apple.motionapp",
      "perishable": true,
      "description": "Motion autosaves roughly every 15 minutes while a composition is open.",
      "files": {
        "root": "~/Movies/Motion Projects/Autosave Vault",
        "match": "*/*.motn"
      },
      "timestamp": {
        "from": "filename",
        "pattern": "_(\\d{4}-\\d{2}-\\d{2}_\\d{4})\\.motn$",
        "layout": "2006-01-02_1504"
      },
      "project": {
        "from": "path",
        "pattern": "Autosave Vault/([^/]+)/"
      }
    }
  ]
}
```

### The fields

`manifest_version` is **required**. See [below](#why-manifests-are-versioned).

`id` is a stable identifier. Every event the source produces is attributed to
it, so a suspicious number can be traced back to what produced it.

`type` is how the source is read. Currently `timestamped-files` is the only
declarative type.

`emits` is `point` or `interval`.

`app` is the bundle identifier this evidence belongs to.

`cause` is **required**, and it says what writes these files:

- **`user-action`** — the application wrote it inside its own interaction loop,
  because of something the person did. Blender's Save writing a `.blend1` is
  the case this exists for. The strongest presence signal an artifact carries,
  and still not proof: a script can drive the same menu item.
- **`timer`** — a periodic autosave, on a schedule nobody touches. It says the
  application was **running** and nothing whatsoever about whether anyone was
  there.
- **`recovery`** — written because something went wrong: a crash, an abnormal
  exit, a relaunch offering to restore work. Photoshop's AutoRecover folder is
  the shape it is for. Nothing ships with this cause yet, and what it is worth
  is **unmeasured** — it is a distinct value precisely so that nobody has to
  declare a recovery file a save or an autosave when it is neither.

**There is no default, deliberately.** Guessing `timer` throws away a presence
signal that was really there; guessing `user-action` credits work to time nobody
was at the machine. A source that omits it fails to load and says which field is
missing, and the other sources in the file still load. Nothing downstream can
recover it later either: the mtime of a `.blend1` and the mtime of an autosave
are the same fact, and for a perishable source the file may be gone.

What a cause is *worth* is not decided here. A source reports the mechanism it
knows; how much presence that implies is fitted against ground truth in
inference, the same way clustering thresholds are.

`perishable` marks a source whose record only ever shrinks, so its evidence
needs preserving on a schedule rather than merely reading at the end — the same
reasoning behind [harvesting](/docs/harvest/). Two different things cause it and
both count: the application removes the files, as Motion does to its autosave
vault, or the evidence is destroyed in place, as an mtime is when a later save
moves a `.blend1` off the day it used to attest to.

Note that perishable does not imply a predictable window. Motion's vault still
holds autosaves from 2022 while a two-hour Motion session on 2026-07-28 left
none at all, so an absence there says nothing about whether work happened.

**`perishable: true` does not by itself preserve anything.** This reader turns
files that exist right now into events; it does not copy the files, and it
runs only when something calls `Collect`. What actually protects a perishable
source's *bytes* against being pruned before the next harvest is a separate
mechanism in [`timetattle harvest`](/docs/harvest/) — `internal/snapshot`'s
`PreserveVault` for Motion's vault today — that mirrors the files themselves,
content-addressed, the same way `knowledgeC` and Biome are preserved. The two
are complementary, not redundant: this reader's events answer "when was a
composition open", the preserved bytes answer "what was in it", and only the
second survives being asked after the vault has moved on.

`description` exists because JSON cannot carry comments. Use it to say what a
comment would have said.

### Finding files

`files.root` is where to look. A leading `~/` is expanded, so manifests are
shareable rather than hardcoding whoever wrote them.

`files.match` is a glob relative to the root.

**Recursive globs are refused.** A manifest saying root `~` with match
`**/*.blend1` walks an entire volume, and the resulting harvester looks hung
rather than broken. Rather than guess at a safe depth, `**` is rejected and
broad searches go through Spotlight, which is indexed and bounded:

```json
{
  "id": "blender-backups",
  "type": "timestamped-files",
  "emits": "point",
  "cause": "user-action",
  "app": "org.blenderfoundation.blender",
  "perishable": true,
  "discovery": "spotlight",
  "description": "Read these as day markers, never as a measure of volume.",
  "files": { "query": "kMDItemFSName == '*.blend1'" },
  "timestamp": { "from": "mtime" }
}
```

This one also ships, as `internal/manifest/builtin/blender.json`.

`files.query` is handed to `mdfind`. You may also set `files.root`, which
narrows the search to one directory tree — but it must be an **absolute path**,
because Spotlight roots are not tilde-expanded and searching a directory
literally named `~` would report nothing forever. A manifest that tries is
refused when the source is built rather than at harvest time.

`mdfind` ships with macOS and exists nowhere else. A Spotlight source on a
machine without it reports nothing and says so, the same way a missing root
does. A query `mdfind` *rejects* is different: that will never fix itself, so
it is an error and `mdfind`'s own complaint is included.

### Reading the time

`timestamp.from` is one of:

- **`filename`** — the time is in the name, as Motion writes it. Requires
  `pattern` (a regular expression capturing exactly one group) and `layout` (a
  [Go reference-time layout](https://pkg.go.dev/time#pkg-constants), so
  `2006-01-02_1504` means `2026-07-28_0632`).
- **`mtime`** — the filesystem modification time, for formats like `.blend1`
  that carry no time in the name.
- **`birthtime`** — the filesystem creation time. macOS only; no other platform
  records one, and substituting the modification time would put a wrong answer
  into the record under the name of a right one.

Setting a `pattern` alongside `mtime` or `birthtime` is an error rather than
being ignored, because it means the author expected the name to be read.

A time read from a filename carries no zone, and applications write local
wall-clock time. It is therefore parsed in the machine's local zone —
`2026-07-28_0632` means half past six that morning, not 06:32 UTC.

`emits` must be `point` for this type, and `interval` is refused at load
rather than silently accepted. The reason is narrower than "a file cannot
have a duration": a `timestamped-files` source reads a file's **metadata** —
its name, its mtime, its birth time — and metadata carries exactly one
instant per file, never two. The only way to get a span out of metadata
alone is to pair up consecutive files, and that is inference: fitting the
gap a "session" tolerates, which is explicitly not a source's job (see
[the other route](#the-other-route-a-go-source) below) because that
threshold has to be calibrated globally, not chosen per source.

This does not mean a file can never carry a genuine interval. A log with
start and end timestamps written into its own contents, or a project file
with an embedded session record, really could. But recovering that requires
*reading inside the file* in a format-specific way — a manifest has no field
for that, because it only ever describes how to find a file and read its
metadata. That evidence needs a Go `source.Source` with its own reader, the
same as any other source that needs real parsing; it would never reach a
manifest's `emits` field, let alone this restriction, because it is not
declaring itself as `type: timestamped-files` in the first place.

#### Mtime evidence decays, and the loss is invisible

A file records only its **last** modification. Work done today on a file last
touched on 11 July does not *add* evidence — it **moves** it, deleting 11 July
from the record and crediting today instead. The count for any past day shrinks
monotonically as later work revisits those files, and one measured case in this
project fell from 1,773 files to 513 within hours.

This is worse than a pruned window, because a pruned window has an edge you can
see. An mtime count simply gets smaller and stays plausible, and afterwards
nothing distinguishes "little work happened that day" from "those files were
touched again". **Absence of mtime evidence is not evidence of absence.**

So an `mtime` source produces **day markers, never volume**, and it belongs
under `"perishable": true` alongside the sources that get pruned: the record
only ever shrinks, so it has to be captured on a schedule rather than read once
at the end. Say so in the source's `description`, which is the only place a
JSON file can carry a warning. The shipped Blender manifest does.

Reproduce the decay with `go run ./scripts/filedays -root <repo>`.

### Document identity

By default, `document` is the filename with the timestamp match and extension
removed. A source that reads its timestamp `from: "filename"` has already
declared, via `timestamp.pattern`, exactly which part of the name is not the
document — so `dark-crystal-v2_2026-07-28_1401.motn` yields `dark-crystal-v2`
with no extra field to write. A source that reads `mtime` or `birthtime` has no
match to remove, so only the extension comes off: `spaceship.blend1` yields
`spaceship`.

An optional `document` rule, shaped exactly like `project` below, overrides the
default for a source whose variable part is a counter or a UUID rather than a
timestamp — `working-copy-8f3c.motn` with a pattern anchored on the trailing
hex would yield `working-copy`. Neither built-in needs this; it exists for a
manifest you write yourself.

```json
"document": { "from": "path", "pattern": "/([^/]+)-[0-9a-f]+\\.motn$" }
```

### Corroborating with the path

`project.pattern` captures one group from the path, and today it exists purely
to **corroborate** the document derived above — it no longer sets an event's
`project`. For Motion the containing directory is the composition name a
second way, independent of the filename: agreement is confidence, and
disagreement (a save-as, a rename) is logged as a warning naming both names
rather than silently collapsed into whichever one won. The filename-derived
name is always what ships as `document`.

**Capture the whole name.** Real project names contain spaces and dashes that
belong to them: `dark-crystal-apex`, `scanline-card-denied`, `Infinite Cake
Title`, `Thirdy Cap 01 - Basic`. A pattern that stops at the first dash turns
`dark-crystal-apex` into `dark`, which would report a false disagreement on
every file, and is the kind of mistake a report will never show you.
`([^/]+)` is the right shape — take everything up to the path separator and
nothing less.

## What you get back

One event per matching file, carrying `document` as derived above and no
`project` — that is filled in later, by resolution, not by this reader. Events
arrive oldest first, because neither a glob nor a Spotlight query returns them
in time order and every consumer would otherwise have to sort.

**Running twice does not report the same file twice.** A harvest passes back the
newest timestamp it recorded, and files at or before it are skipped. Nothing is
remembered between runs, so there is no cache to fall out of step with the disk.

**A file the glob catches but the timestamp pattern misses is skipped**, named
in a warning, and the files that did match are still collected. That situation
means a pattern slightly narrower than the directory it points at — worth
fixing, not worth losing a harvest over.

## Does the evidence actually join?

The `app` field only earns its place if points really do land inside focus
intervals. On 2026-07-28, a day with independently confirmed Motion work at
06:00–06:50 and 14:01–14:22, the machine this was built on held **2** surviving
Motion autosaves and **26** `com.apple.motionapp` focus intervals. One autosave
fell inside an interval; one did not.

The miss is the interesting half. `scanline2-card-granted` autosaved at
06:32:00, and Safari held focus from 06:29:56 to 06:33:42 — Motion's autosave
timer runs on wall-clock time and does not wait for the application to be
frontmost. Blender the same day is cleaner, because a `.blend1` is written by
an explicit save the user is present for: every one of its backups inside
Biome's window landed inside a Blender focus interval.

This is what `cause` records, and it is why it is a manifest field rather than
something an analysis learns per source: `motion-autosave` declares `timer`,
`blender-backups` declares `user-action`, and inference can tell the two apart
without knowing which reader produced either. A wider measurement on 2026-07-29
put Blender at 4 of 4 and Motion at 9 of 15, and the one Motion miss that was
not the artifact's own clock resolution was written at 17:53 on 2026-07-11 with
the screen off, during a break — see
[the join validation](https://github.com/bensyverson/timetattle/blob/main/project/private/2026-07-29-join-validation.md).

So an autosave point is evidence that **a document was open**, not that the
application was frontmost at that instant. Containment is the right mechanical
test and `source.Event.Contains` applies no tolerance on purpose, but a point
that misses by two minutes is still attributing the surrounding block of work —
whereas a point that misses by two *hours* means one of the two readers has an
epoch or timezone bug. Whatever decides which of those it is looking at belongs
in inference, where it can be calibrated, and not in a source.

Re-run it yourself:

```
go run ./scripts/joinfocus                          # every day with evidence
go run ./scripts/joinfocus -day 2026-07-28          # one day
go run ./scripts/joinfocus -source blender-backups  # one source
```

It reports, per day, how many points there were, how many focus intervals, how
many joined, and for each miss how far it was and what held focus instead. Days
older than Biome's window are marked as having no focus history rather than
counted as failures.

## Where manifests live

Manifests that ship with Timetattle are **embedded in the binary**, so a fresh
machine collects Motion and Blender evidence with nothing to install and there
is no half-installed state where the binary and a file on disk disagree about
what a built-in says. They live in `internal/manifest/builtin/` and go through
exactly the same validation as anything you write.

Your own go in **`~/.timetattle/manifests/`**, beside the database, the
snapshots and `config.toml`. It is a directory rather than a key in
`config.toml` because adding a source should mean dropping in a file and
editing nothing — the same property the Go route has, and most of the reason
manifests are worth having.

Files are read in **name order**, which is the only ordering a filesystem will
give you the same way twice. Prefixing them (`10-work.json`, `20-personal.json`)
fixes which one wins when two claim the same `id`. Anything that is not a
`.json` file is ignored, so a `README` or an editor's `.bak` in that directory
is not reported as a broken manifest on every run.

### Overriding a built-in

A source of yours sharing an `id` with a built-in **replaces it outright**. Its
fields are not merged with the built-in's, and it keeps the built-in's position
so overriding one source does not shuffle the order everything else is
collected in.

Wholesale replacement is the deliberate choice. Under a field-by-field merge,
reading your own override would tell you only half of what the source did — the
other half living inside a binary you cannot open, and changing under you on the
next release. This way the file in front of you is the whole answer.

The practical consequence: an override must be **complete**. If you relocate
Motion's vault and still want the directory name corroborating the document,
copy the built-in's `project` block across too, because omitting it means no
corroboration rather than the built-in's.

```json
{
  "manifest_version": 1,
  "sources": [
    {
      "id": "motion-autosave",
      "type": "timestamped-files",
      "emits": "point",
      "cause": "timer",
      "app": "com.apple.motionapp",
      "perishable": true,
      "description": "Same as the built-in, but the vault lives on an external volume.",
      "files": {
        "root": "/Volumes/Work/Motion Projects/Autosave Vault",
        "match": "*/*.motn"
      },
      "timestamp": {
        "from": "filename",
        "pattern": "_(\\d{4}-\\d{2}-\\d{2}_\\d{4})\\.motn$",
        "layout": "2006-01-02_1504"
      },
      "project": {
        "from": "path",
        "pattern": "Autosave Vault/([^/]+)/"
      }
    }
  ]
}
```

An `id` nobody has used is simply added, after the built-ins.

There is no "disable" flag. A built-in whose root does not exist already
reports nothing and costs nothing, which is what an uninstalled application
looks like, so there is nothing for a flag to switch off.

## Why manifests are versioned

Elsewhere Timetattle ignores backward compatibility entirely — it is
pre-release and controls every caller, so changing an internal shape costs
nothing.

Manifests are the one exception. They live outside this repository, on other
people's disks, and cannot be migrated by anyone who cannot see them. So
`manifest_version` is required from the first release, and a version this build
does not recognise is refused rather than parsed hopefully. A manifest that
half-loads is worse than one that refuses, because the failure shows up much
later as evidence that is quietly missing.

**A new field does not raise the version, even a required one.** The version
exists to stop a changed schema being *misread*, and a required field cannot be:
a manifest that omits it fails to load with a message naming the field, and its
neighbours still load. The version gate is coarser — it refuses the whole file
and can only tell you which version this build speaks — so using it where a
per-field error works costs you the one thing you need, which is what to fix.
`cause` became required this way. The version moves when a change cannot be
diagnosed per field: a field whose **meaning** changes under the same name and
type, where nothing looks wrong and every value is quietly read as something
else.

## When something is wrong

Everything resolvable is resolved when the manifest loads, not when the
harvester runs: paths are expanded, regular expressions are compiled, and every
field is checked against the values it accepts. A typo in `emits` is reported
immediately, rather than producing zero events for reasons nobody can see.

A manifest is hand-edited, so failures are **per source**. One malformed entry
is dropped and described; the others still load. A typo in the fifth source
does not silently disable the other four.

A root that does not exist is not an error. Most people do not have Motion
installed, and a source with nothing to read simply reports nothing.

## The other route: a Go source

When an application needs real parsing, implement `source.Source`:

```go
type Source interface {
	ID() string
	Kind() Kind
	Collect(ctx context.Context, since time.Time) ([]Event, error)
}
```

Register it from your package's `init`, the way `database/sql` drivers do, and
add a blank import. Nothing existing needs editing — that is the point.

`Collect` takes a `since` and returns what happened at or after it. A source
that fails is reported without stopping the others, and an individual unusable
event is dropped rather than poisoning the run: an uninstalled application or a
renamed directory is ordinary, and losing an entire harvest to one would be the
same permanent loss the harvester exists to prevent.

The built-in Go sources are documented individually:
[Git history](/docs/git-history/), [Filesystem
observations](/docs/filesystem/), [Application focus](/docs/app-focus/),
[Presence](/docs/presence/), [Claude Code transcripts](/docs/transcripts/) and
[Job task databases](/docs/job-tasks/). The last of those is worth reading
before writing a new one: it is the only source that reports *what the work
was* rather than merely that something happened, and it is where the awkward
questions — what an interval actually asserts, how to keep "absent" apart from
"unreadable" when several directories are read at once — are worked through
against real numbers.

What a source must **not** do is decide which events belong to the same working
session. That is inference, it is fitted against ground truth, and measurements
during design showed total presence varying 2.3× across plausible
thresholds. A source that clustered its own events would make that fit
impossible to perform consistently. Sources describe what they observed;
Timetattle decides what it means.
