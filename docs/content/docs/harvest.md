---
title: "Harvesting presence data"
weight: 10
description: "Capturing macOS presence and application evidence before it is deleted, and ingesting it."
---

# Harvesting presence data

`timetattle harvest` preserves four kinds of evidence that decay if nothing
captures them: the `knowledgeC` database and Biome's `ScreenTime.AppUsage`
stream, which macOS prunes on a schedule; Motion's Autosave Vault, which
Motion prunes for a reason nobody has confirmed yet; and file modification
times, which decay one file at a time every time older work is touched again.

`knowledgeC`, Biome and every declarative manifest source (Motion, Blender —
see [Adding an evidence source](/docs/sources/)) are ingested into the
[event store](/docs/store/) on every run. The filedays scan is the only
capture that is not: it is preserved as raw evidence and read only by this
command's own output. Motion's manifest source is not fed by the vault
capture next to it, either — it reads the live vault (or wherever
`files.root` points) directly, the same way it would with no capture phase
at all, which is what the Blender source has: nothing prunes a `.blend1`
faster than the next save can replace it, so there is nothing to preserve in
advance and no capture step for it, only ingestion. See
[What the Motion vault capture contains](#what-the-motion-vault-capture-contains)
for why the copy and the ingested events are separate things reading the
same files.

This is the only part of Timetattle that is genuinely urgent. Every other
evidence source — Claude Code transcripts, git history — persists indefinitely
and can be parsed whenever the model changes. All four of the above prune
themselves in one way or another, so evidence that is not captured is gone
permanently and cannot be recovered by any means.

## Why it matters

`knowledgeC` is the only source that directly *observes an interval*. Almost
everything else Timetattle reads yields point observations: a commit happens
at an instant, a file is saved at an instant, a Claude message is sent at an
instant. Screen-on time is the sole exception, which is why it is the
denominator for capacity and the only evidence that reaches work in
applications leaving no other trace.

The case that motivates it: on 11 July 2026 there were 13.9 hours at the
machine, zero commits, 1,760 files written and barely any Claude activity.
Git cannot see that day. Presence data records it plainly.

Biome answers the other half — *which application* held the screen. It is
captured for a different reason: `knowledgeC`'s `/app/usage` stream **stalled
for a full day** — its last row before the gap was 2026-07-27 14:28, it wrote
nothing at all on 07-28, and it resumed on its own on 07-29 — which makes Biome
the only application attribution on a current Mac that can be relied on
day to day. See [Application focus from Biome](/docs/app-focus/).

## Running it

```console
$ timetattle harvest
/Users/you/.timetattle/snapshots/knowledgeC-20260730T115142-1e4a83afd60bd771f81f3cc9eccf141c.db.gz
23044 rows, 1.5 MB
ScreenTime.AppUsage: 6 segments captured, 0 unchanged, 0.7 MB
biome-appusage: 24829 events seen, 24829 new
knowledgec-backlit: 1578 events seen, 1571 new
knowledgec-islocked: 1346 events seen, 1346 new
knowledgec-appusage: 10315 events seen, 10315 new
knowledgec-mediausage: 1521 events seen, 1518 new
blender-backups: 10 events seen, 10 new
motion-autosave: 38 events seen, 38 new
```

`motion-autosave` and `blender-backups` are the two manifests that ship with
Timetattle (`internal/manifest/builtin`); a root that does not exist —
Motion not installed, no `.blend1` file anywhere Spotlight can see — reports
`0 events seen, 0 new` rather than an error, and a manifest you add yourself
under `~/.timetattle/manifests/` appears here the same way.

A snapshot's name carries the instant it was taken and a digest of what is
inside it. When a run finds a snapshot already holding those exact contents it
writes nothing and names that file instead, with `already held` in place of a
size:

```console
/Users/you/.timetattle/snapshots/knowledgeC-20260730T115142-1e4a83afd60bd771f81f3cc9eccf141c.db.gz
23044 rows, already held
```

That is a capture that ran and succeeded, not one that failed: the source was
read, staged and extracted in full, and only the redundant write was skipped.
`timetattle status` records it as `0 new, 1 held`, the same way it already
records a Biome stream whose every segment was unchanged. See
[Why snapshots stopped accumulating](#why-snapshots-stopped-accumulating).

The snapshot and stream lines are what was preserved on disk; the lines named
for a source are what was ingested. `seen` is what the source reported and `new` is
what survived deduplication — they are not equal even on an empty store,
because `knowledgeC` repeats itself and identical observations are one row.

| Flag | Meaning |
| --- | --- |
| `-source` | `knowledgeC.db` to read. Defaults to the current user's. |
| `-biome` | Biome restricted stream directory. Defaults to the current user's. |
| `-dir` | Where to write snapshots. Defaults to `~/.timetattle/snapshots`. |
| `-store` | Event store to ingest into. Defaults to `~/.timetattle/timetattle.db`. |
| `-motion-vault` | Motion's Autosave Vault to preserve. Defaults to the `motion-autosave` manifest source's root, so a relocated vault (see [Adding an evidence source](/docs/sources/)) is followed automatically. |
| `-filedays-roots` | Comma-separated directories to scan for file modification days. Defaults to reading `~/.timetattle/filedays-roots.txt`, one path per line (`#` comments and blank lines ignored, `~` expanded). Nothing configured means nothing is scanned. |
| `-filedays-exclude` | Comma-separated directory names to skip whole when scanning. Defaults to `.git,worktrees,node_modules,.venv,.Trash,Autosave Vault`. |
| `-index` | Registry index of registered directories to read (see [Registering a project](/docs/registry/)). Defaults to `~/.timetattle/registered-directories.json`. |
| `-quiet` | Print nothing on success. For scheduled runs. |

### A capture that fails costs only itself

**Every capture is attempted, whatever the ones before it did.** An unreadable
`knowledgeC` does not stop the Biome capture, the vault copy, the filedays
scan or the ingestion phase; it loses the `knowledgeC` snapshot and nothing
else. The four captures read four unrelated perishable things, write to
disjoint places, and none depends on another having succeeded, so abandoning
the readable ones because one is unreadable maximises the loss rather than
minimising it.

Order still matters, and is still the order of urgency: `knowledgeC` goes
first because it has the hardest deadline, then Biome, then the Motion vault,
then the filedays scan. The vault goes ahead of filedays because its pruning
trigger is unconfirmed — a composition can vanish for a reason nothing here
understands — while mtime evidence only decays gradually, one file at a time,
as later work revisits it. Order now decides which source gets the machine's
attention first, rather than which sources get it at all.

The run still exits non-zero, and the error names every source that failed
rather than only the first.

> This is a fix, not a description of how it always worked. Until 2026-07-31
> the capture phase returned on its first error, and because it runs before
> everything else, a `knowledgeC` permission failure cost the whole run. It
> did: three consecutive scheduled harvests from 2026-07-30 12:01 preserved
> nothing at all and never opened the store. Reproduce both behaviours with
> `./scripts/capture-mutation`, which points a harvest at a `knowledgeC` file
> it cannot open. Against the current tree: 2 Biome segments, 1 vault file, 1
> filedays snapshot, 8 `harvest_run` rows. Against the code before the fix,
> from the same fixture: 0, 0, 0, and a store that was never created.
>
> Extended 2026-08-01 (task 4tuVv): the same run now also writes **4
> `capture_run` rows** — `knowledgec: failed`, `biome: ok`, `motion-vault: ok`,
> `filedays: ok` — and `timetattle status` reports
> `knowledgeC  none  -  20s ago  failed` beneath the source table. Before that
> task the capture phase wrote no row of any kind and the same report showed
> only a file count.

The four `knowledgeC` streams still get a `harvest_run` row when their capture
fails — one each, marked `failed`, carrying the reason, having read nothing.
Leaving them out of the plan would be cheaper and wrong: a source with no row
cannot be told apart from a source that ran and found nothing. For the same
reason the run does **not** fall back to the newest snapshot already on disk.
Re-ingesting yesterday's snapshot would add no rows at all (the store
deduplicates by content) while reporting `ok` for all four streams, so the run
would look like a clean success and the lost day would be invisible twice
over.

Running it twice is harmless. Snapshots are named by timestamp and a run
never overwrites an existing one, so nothing already captured can be lost by
running again. Biome and Motion-vault captures are named by the digest of
their contents, so a second run inside the same minute simply finds
everything already held. The same is true of ingestion: a second harvest
re-reads every source in full and adds nothing, because the
[store](/docs/store/) deduplicates by content. Measured on 2026-07-30 with
three consecutive runs of
`timetattle harvest -dir <tmp>/snapshots -store <tmp>/timetattle.db`: 39,579
rows in `event` after the first, and `new` of exactly 0 for all five sources on
both the second and the third.

> The same property holds for the manifest sources added since that
> measurement. Run against this machine's real vault and Spotlight index:
> `motion-autosave` recorded 38 events and `blender-backups` 10 on the first
> run, and both reported `new` of 0 — with `seen` unchanged — on an
> immediate second run.

## Ingesting what was captured

Capture and ingestion are separate phases, and ingestion runs whatever the
capture phase managed. Nothing in it can lose evidence — the snapshots are
already on disk and can be re-read for ever — so a parser bug costs a re-run
rather than history.

The one failure that does stop things is a store that cannot be opened, and it
is not a capture: there is nothing to ingest into, and no `harvest_run` row can
be written to record that. Everything the capture phase preserved is already
safe on disk by then.

Five sources with a fixed location are read every run: Biome's
`ScreenTime.AppUsage`, from the live stream, and the four `knowledgeC`
streams, from **the snapshot this run just wrote** rather than whichever
snapshot happens to be newest. Resolving that independently would race a
concurrent harvest, and a run that recorded a snapshot it did not read is
provenance that lies.

Every manifest source is also read every run, regardless of `-index` or
anything else being registered: `manifest.LoadAll` layers
`~/.timetattle/manifests/*.json` over the built-ins under
`internal/manifest/builtin/` (Motion, Blender today), and each one that
validates gets its own `internal/timestamped.Reader` and its own
`harvest_run` row. Unlike the five sources above, a manifest source has no
snapshot to read from — it reads `files.root` (or, for a Spotlight source,
whatever `mdfind` returns) directly, at harvest time, which is why the vault
capture and the ingested `motion-autosave` events can each be checked
independently against the vault on disk. `harvest_run.input` records exactly
what was read: the joined root and glob for a bounded source, or the literal
`mdfind` query for a Spotlight one — the latter matters because a Spotlight
source's `files.root` is often empty by design (see
[Finding files](/docs/sources/#finding-files)), and an empty `input` is
refused outright rather than silently accepted, since it is what a run that
misrecorded its own provenance would look like.

A variable number of further sources are read from `-index`'s registered
directories (see [Registering a project](/docs/registry/)): `fs-observations`
walks every registered directory's own birth and modification times in one
run (see [Filesystem observations](/docs/filesystem/)), and
`gitlog-commits`/`gitlog-reflog` read commit and reflog history from every
registered directory that is itself a git repository, one run per repository
(see [Git history](/docs/git-history/)). Neither is a fixed OS location the
way the five above are, so neither runs at all with nothing registered, and
the count grows and shrinks with the registry rather than being fixed at
five. Both are durable sources that nothing prunes, so — unlike the four
`knowledgeC` streams and the Biome stream above — there is no capture phase
for either, only ingestion, and the directory they read is re-verified
against the registry (not the index) on every run: a directory whose
registration was deleted or corrected since the last harvest is reflected
immediately, not on some later schedule.

Three properties are worth knowing:

- **One `harvest_run` row per source, per invocation.** A row per invocation
  would make a lost source invisible. See [the event store](/docs/store/).
- **A source that fails is recorded and does not block the others.** Its run
  is marked failed with the reason, its line in the summary says `lost`, the
  remaining sources are still read and their events still stored, and the
  command then exits non-zero. Events written before a failure stay: they were
  observed, and a later failure says nothing about them.
- **Every run reads everything.** `since` is always the zero time and no
  watermark is kept anywhere. `knowledgeC` writes rows up to 23 hours after
  the events they describe, so a mark advanced to the newest event a run had
  seen would step over everything that arrived afterwards, permanently and
  with no symptom. Re-reading is cheap precisely because deduplication is by
  content.

A source with nothing to report is a **success**, not a failure. "No presence
on the 22nd" and "no harvest ran on the 22nd" are different facts, and only a
recorded empty run tells them apart.

## Attribution, last

Once everything is ingested, the harvest [attributes](registry.md) every stored
path against the registry. On a large store this is by far the longest step, and
the counts are printed only when it ends, so it is bracketed:

```
attribution: started at 2026-07-14T09:00:00-05:00
attribution: 1200 locators, 1100 attributed, 100 unattributed, 0 unreadable
attribution: finished after 4.2s
```

A log whose last attribution line is `started at` belongs to a pass that is
still running, or to one that was killed. Until it finishes, the evidence this
run ingested carries no project, and a [scoped report says
so](metrics.md#evidence-not-yet-attributed). `-quiet` prints none of this.

## Scheduling it

```console
$ go install ./cmd/timetattle
$ ./scripts/install-agent.sh
```

That installs a launchd agent that harvests **on the hour, every hour**, local
time, plus once when it is loaded.

Override with `TIMETATTLE_HOURS` (a comma-separated list of hours) or
`TIMETATTLE_BIN`.

The `knowledgeC` snapshot does **not** follow that schedule. It has a cadence of
its own — four hours by default — set by `harvest -snapshot-every` rather than by
the plist. See [The snapshot has its own
cadence](#the-snapshot-has-its-own-cadence).

> **Corrected on 2026-08-12, and anyone who installed the earlier agent should
> re-run the installer.** Until this date the agent was `StartInterval=43200`,
> documented here as "every 12 hours … launchd runs a missed interval on the
> next wake." Both halves were wrong.
>
> `launchd.plist(5)` says the opposite of that last sentence under
> `StartInterval`: "If the system is asleep during the time of the next
> scheduled interval firing, that interval will be missed due to shortcomings
> in kqueue(3)." The promise about waking belongs to `StartCalendarInterval`,
> further down the same page, and had been read onto the wrong key. On a
> laptop that sleeps, `StartInterval` therefore counts something closer to
> awake time. Measured over all 35 inter-harvest gaps since 2026-07-30: mean
> 8.49h, max **54.64h**, with 13 gaps longer than the nominal 12 hours.
>
> The frequency argument was wrong for a different reason: it weighed only the
> sources that prune. See below.

### Why frequency matters, for one kind of source only

Two kinds of loss sit behind a missed harvest, and the schedule turns on the
difference.

`knowledgeC` and Biome **prune**. Anything still inside their windows comes
back in full whenever the next harvest runs, however late — on 2026-08-12 the
store held nothing at all for that day, and a harvest run by hand recovered it
as 8,280 events.

The `mtime` sources **decay**, and no later harvest ever recovers them. A file
records only its last modification, so work done today on a file last touched
on 11 July does not add evidence for 11 July — it moves it. One repository scan
found 1,773 files bearing 2026-07-11; the same scan hours later found 513. The
1,260 that moved cannot be recovered by harvesting harder afterwards, only by
having harvested sooner, and the loss leaves no visible edge: the count simply
gets smaller and stays plausible.

So the cadence is a sampling question. Only a scan taken while a file still
carries a day's `mtime` can record that day, and what matters is how many
harvests land inside a working session. Twice a day puts one inside an ordinary
day and can put none inside a short one; six puts three or four inside any
session long enough to be worth measuring; twenty-four puts one in every hour of
it.

The ceiling used to be storage, and it was never a scheduling problem.

> **Partly lifted, 2026-08-12, and the arithmetic is worth keeping.** The
> sentence that stood here said a `knowledgeC` snapshot "is written afresh on
> every run and is neither deduplicated nor pruned, which puts hourly at
> roughly 13 GB a year against 3.3 GB at every four hours". The first half is
> no longer true — a run whose contents are already on disk now writes nothing
> — and the second half survives it almost intact, which is the finding.
> Measured with `go run ./scripts/snapchurn -cadence 1h`: over a trailing
> 14 days, 89 of 336 hours saw no `ZOBJECT` row created, so at most **26.5%**
> of hourly runs would be skipped, and that is an upper bound because a row
> can also age out of retention inside an otherwise quiet hour. Hourly with
> deduplication therefore projects to about **9.85 GB a year**, not 13 GB and
> not something small. At four-hourly the same measurement skips 1 window in
> 84, so today's schedule is essentially unchanged. Deduplication removes the
> waste; it does not by itself make hourly cheap. See
> [Why snapshots stopped accumulating](#why-snapshots-stopped-accumulating).

> **Lifted for good on 2026-08-13, by separating the two cadences rather than by
> making the snapshot cheaper.** The paragraph above is still true and is still
> why deduplication alone did not settle it. What settled it is that the harvest
> and the snapshot no longer share a number: the schedule went hourly and
> `knowledgeC` kept four-hourly, so `mtime` evidence is scanned 24 times a day
> while snapshot storage stays at about **3.2 GB/yr** rather than 9.85. See
> below.

### The snapshot has its own cadence

The harvest runs hourly. The `knowledgeC` snapshot is taken **at most every four
hours**, whatever the harvest cadence is, and `harvest -snapshot-every` sets that
interval. `0` disables the throttle and takes one on every run.

The reason is the same split as above, read the other way round. Frequency
protects the `mtime` sources and nothing else. `knowledgeC` prunes by **volume**
— `/app/usage` is trimmed back to a 10,000-row floor, and a four-hourly schedule
already puts about sixty captures inside that window — so everything still inside
it comes back whole on the next harvest whenever that is. A denser snapshot cadence
therefore buys `knowledgeC` almost nothing, while costing more than anything
else in the phase: about 1.5 MB written afresh whenever the extracted rows
differ at all.

Biome and the Motion vault are **not** throttled and follow the harvest. Both
are already content-addressed segment by segment and file by file, so a run over
an unchanged source writes nothing at all. They cost a read, never a write, and
throttling them would delay evidence to save nothing.

A throttled run still opens `knowledgeC`, even though it reads nothing out of
it. macOS pins Full Disk Access to a binary's code signature and revokes it
silently on rebuild, and that revocation is invisible until something opens the
database — so skipping the open along with the snapshot would hold the news for
a whole interval while the harvest printed success. One `open(2)` closes that,
and a source that cannot be opened is recorded as a **failed** capture rather
than a throttled one.

A throttled run also still **ingests**. It reads the snapshot the last capture
left, so the four `knowledgeC` streams get a `harvest_run` row on every harvest
rather than on one in four. Re-reading adds nothing — the store deduplicates by
content, so `seen` is non-zero beside `new` of 0 — and the alternative is a
source with no row at all, which cannot be told apart from a source that ran and
found nothing.

#### The third outcome, and why it needed a schema change

A run that declined to take a snapshot is not any of the four capture outcomes
that existed before it:

| what happened | how it is recorded |
| --- | --- |
| a snapshot was written | `ok`, `files_new` 1 |
| the source was read through and its bytes were already on disk | `ok`, `files_held` 1 |
| the schedule said the next one is not due | `skipped` |
| the source could not be read | `failed`, with the reason |
| there is no such source on this machine | `absent` |

The second and third are the pair it would be easiest to collapse and the pair
that must not be. `files_held` 1 is an observation **about the source**:
`knowledgeC` was opened, extracted and digested, and has not changed. `skipped`
is a decision about the **schedule**, taken before anything was opened, and says
nothing whatever about what `knowledgeC` holds. Migration `0015` adds the fifth
status and forbids a `skipped` row from claiming any file, any held file or any
byte — a run that did not read its source preserved nothing from it.

None of this makes the perishable floors safe, because they are **volume**-based
rather than time-based: `/app/usage` is capped near 10,000 rows and Biome fills
fixed 512 KB segments, so "under 10 days" describes typical activity and a busy
stretch shortens the real deadline by an amount nobody has measured. Hourly
harvests with four-hourly snapshots is a cost judgement made under that
uncertainty, not a proven margin.

> **Measured 2026-08-13.** The `knowledgeC` half of that is no longer
> unmeasured, and the caveat was right. Across 43 preserved snapshots
> (`go run ./scripts/streamseries -streams /app/usage -days`):
>
> - A named row was observed alive at 8.92 days and gone by **9.46 days**, so
>   "under ten days" is now a measurement rather than an estimate.
> - At the typical rate of 824 rows a day the cap holds 12.14 days, against a
>   median retained span of 12.24 days actually observed — agreement to 0.10
>   days, which is what confirms the **row count** is what evicts here.
> - At the busiest day on record, 2,798 rows, the same cap holds **3.57 days**.
>
> So a sustained heavy stretch cuts the deadline from ten days to roughly 3.6,
> and the busiest-day figure is a lower bound. A four-hourly snapshot beats all
> three comfortably; what this retires is "ten days" as a *margin*.
> `/app/usage` is also the only stream whose deadline moves this way — the
> others expire by age at ~28 days regardless of load. Biome's segment roll
> rate against activity remains unmeasured. See
> [`project/2026-08-13-knowledgec-snapshot-series.md`](https://github.com/bensyverson/timetattle/blob/main/project/2026-08-13-knowledgec-snapshot-series.md).

### What the calendar schedule does and does not guarantee

`launchd.plist(5)`, under `StartCalendarInterval`: "Unlike cron which skips job
invocations when the computer is asleep, launchd will start the job the next
time the computer wakes up. If multiple intervals transpire before the computer
is woken, those events will be coalesced into one event upon wake from sleep."

That was verified here rather than taken off the page, since misreading this
page is what caused the original defect. macOS ships `com.apple.newsyslog` on a
calendar schedule at minute 30 of every hour. On 2026-08-12 this machine slept
from 13:26:56 to 13:34:57, so the 13:30 slot passed with the machine down; the
run that followed is stamped **13:34:57.652**, 0.65 seconds after the wake, and
it then rescheduled itself for 14:30. A dark wake was enough — the job did not
wait for anyone to open the lid.

```console
$ pmset -g log | grep -E 'Sleep|DarkWake'
$ log show --last 8h --predicate 'eventMessage CONTAINS "newsyslog"' --style compact
```

Read the guarantee for exactly what it says: it bounds the delay after **wake**,
not the gap in wall-clock time. A laptop closed on Friday and opened on Monday
still has a three-day hole. What changes is that it harvests within seconds of
the lid opening instead of somewhere in the next hour, and that it
harvests once rather than eighteen times in a row.

Boot and logout are a separate case the key does not cover, because a job that
is not loaded cannot have missed an interval. `RunAtLoad` is what covers those.

To confirm what launchd actually loaded, rather than what the installer wrote,
note that a calendar schedule does **not** appear as the `run interval` line
`StartInterval` produced. It is one entry per hour under `event triggers`:

```console
$ launchctl print gui/$UID/com.bensyverson.timetattle.harvest | grep -A 8 calendarinterval
```

## Checking that it is still working

```console
$ timetattle status
store:     /Users/you/.timetattle/timetattle.db
schema:    0015_capture_skipped.sql, applied 2026-08-01T15:04:08.783000Z
schedule:  every 1h00m, from StartCalendarInterval in /Users/you/Library/LaunchAgents/com.example.harvest.plist
stale:     1h30m, an interval and a half of that schedule
evidence:  newest 2026-08-01 09:12:44, 12m ago
           whether the machine was idle through that or the collection has stopped is not in this record
last run:  2026-08-01 08:30:08, 55m ago

source                 last run  status  inputs    seen    new  last ok
biome-appusage         56m ago   ok           1   27205     10  56m ago
blender-backups        55m ago   ok           1      10      0  55m ago
claude-transcripts     56m ago   ok           1  722832    413  56m ago
fs-observations        55m ago   ok           1   30847  30815  55m ago
gitlog-commits         55m ago   ok           5     932    930  55m ago
gitlog-reflog          55m ago   ok           5     700    696  55m ago
knowledgec-appusage    55m ago   ok           1   11526      9  55m ago
knowledgec-backlit     55m ago   ok           1    1538      0  55m ago
knowledgec-islocked    55m ago   ok           1    1330      0  55m ago
knowledgec-mediausage  55m ago   ok           1    1549      1  55m ago
motion-autosave        55m ago   ok           1      38      0  55m ago

preserved in /Users/you/.timetattle/snapshots:
  what          on disk   newest     last capture  outcome
  knowledgeC    13 files  11m ago    11m ago       ok, 1 new
  Biome         13 files  11m ago    11m ago       ok, nothing new, 13 already held
  Motion vault  39 files  1h21m ago  11m ago       ok, nothing new, 39 already …
  filedays      none      -          11m ago       nothing to capture on this m…

every source ran and succeeded within the last 6h00m, and every capture held, and the newest evidence in the store is 12m old
```

That is the report **in an 80-column terminal**, where every column has shrunk
to its own content. Piped, it keeps the wider spacing it has always had and
nothing is shortened — the ellipses above appear only on a terminal too narrow
for the whole outcome, and only ever on prose. A source name never shrinks far
enough for two rows to read alike; four knowledgeC streams that all rendered as
`knowledgec-…` would be worse than a line that wrapped. See
[Fitting the terminal](terminal.md).

**This is the command to run when a day looks emptier than it was.** It reads
`harvest_run`, `capture_run` and the snapshot directory, writes nothing, and
does not create the store if it is missing.

It reads through `store.OpenReadOnly`, `Store.Runs` and `Store.Captures`, which
is what makes "writes nothing" structural: the store is opened read-only and is
never migrated, so the schema line above reports the schema a harvest last ran
against rather than one this check applied on its way past. See
[the store's surface](/docs/store#the-surface-and-why-nothing-reaches-around-it).

`never run` is not a rounded-down zero. A source that ran and found nothing
reports `ok` with `0 seen`, which is a healthy answer — "no Motion work on the
22nd" is a measurement. A source with no `harvest_run` row at all has never
been read, which is a different fact entirely, and it says so in those words
with every measured column blank rather than zero:

```
motion-autosave        never run     -             -          -          -  never
```

That happens for ordinary reasons — no Motion installed, nothing registered
yet, or an installed binary older than the source tree that predates the source
entirely — so it is reported and left to you to judge rather than treated as an
alarm.

`inputs` is how many things a source read, and it is there because a source is
not necessarily one read. `gitlog-commits` runs once per registered repository
and writes one `harvest_run` row for each, so its line is the **total across
all five**: 932 seen, 930 new. `fs-observations` reads the same five
directories in a single walk and writes one row, so it shows one input. Both
are honest; the report does not care which shape a collector has.

That distinction was missing until 2026-08-01, and the line above read
`gitlog-commits  ok  81 seen  79 new` — the last repository of five, labelled
as the source. The undercount was the harmless half: nothing aggregated
*status* across the group either, so four repositories could have been lost and
the fifth's `ok` printed as the source's result, with `every source ran and
succeeded` underneath.

A source that read some of its inputs and lost others is **`partial`**, and
`inputs` shows how many survived:

```
gitlog-commits         56m ago       partial     4/5        448        446  never

1 source read some inputs and lost others on the most recent run:
  gitlog-commits: 1 of 5 inputs not read, 1 failure, and it has never once succeeded
    /Users/you/git/birch: cannot read /Users/you/git/birch/.git: operation not permitted
```

The `inputs` column is how many were read out of how many were attempted, and
it is never a count of failures: a repository whose run never finished was not
read either, and a harvest that *died* on the fourth of five repositories is
`partial` with `3/5`, not `ok`.

`ok` is reachable only when **every** input was read, and `partial` exits
non-zero like any other failure. `last ok` stays at the last harvest in which
nothing was lost, so a source stuck at four repositories of five does not
report a fresh success every twelve hours.

`-inputs` lists every input each source read on its most recent harvest, one
line each, with the totals broken back down. That is where "which repository
broke" is answered, and it is behind a flag because the default report is meant
to be scanned.

### `running` and `stranded`, and why the difference is a guess

A harvest running **right now** and a harvest that **died partway** leave the
identical `harvest_run` row: a `started_at` and no `finished_at`. The second one,
by definition, was not there to write anything else. Nothing the writer can
record separates them.

Until 2026-08-01 `status` resolved that by calling every such row *"a harvest
that died partway"* and exiting non-zero — which is also what a live harvest
looks like. Running `timetattle status` from a prompt while the 12-hourly job
happened to be mid-flight produced a failure that was not conservative but
false, and a check that cries wolf is a check nobody reads. That is how 44 hours
were lost once already.

So the two are separated by the only thing left, which is how long the row has
been sitting there:

| `status` | What it means |
| --- | --- |
| `running` | The run has not been at it long enough for anything to be concluded. Reported, not an error. |
| `stranded` | It has outlasted **ten times** the slowest completed run its source has ever recorded, or **five minutes**, whichever is larger. Presumed dead, and exits non-zero. |

A source with **no completed run at all** is never called `stranded`. The first
harvest on a machine reads the whole corpus and is the slowest run that will ever
happen; nothing recorded bounds it, and inventing a bound would be a guess
wearing the clothes of a measurement. It is named and aged instead, and `-stale`
is what reports it if it never finishes.

The limit is derived from the runs rather than fixed, because a fixed one would
break as the corpus grew — `claude-transcripts` already reads 723,905 events and
dominates the harvest. Deriving it means the tolerance grows exactly as fast as
the work does.

Ten is a measurement, not a round number. `scripts/runningcheck` replays the
store run by run: for every run that *did* complete it derives the limit from the
runs before it — all `status` would have had while that run was in flight — and
reports the ones that outlasted it. Each hit is a healthy harvest that would have
been called dead. Against the 76 rows of 2026-08-01, with the floor removed so
only the multiple is under test (`go run ./scripts/runningcheck -floor 1s -slack N`):

| `-slack` | False alarms |
| --- | --- |
| 2 | 1 — run 20 took 1m15.75s, past the 41.92s implied by the 20.96s before it |
| 3 | 1 — the same run, past 1m02.88s |
| 4 | none |
| 10 | none |

Four is where this history first comes clean, and four is the *edge* of a
five-sample record rather than a margin. The edge sits there because the spread
on identical work is already large: grouping completed runs by
`(source, input)`, `claude-transcripts` read ~722,000 events on each of five runs
and took between 12.403s and 75.751s — **6.11x with the work held constant**.
The variation is the machine, not the workload.

The five-minute floor is a different kind of claim and is marked as one.
`runningcheck` reports no false alarms at a 30-second floor either, because this
store was recorded on a machine that was never wedged, and **a store cannot
measure a stall it never had**. The floor is a margin against a variance that has
not been sampled, chosen from the schedule instead: four times the longest whole
harvest ever recorded here (76.17s) and, when it was chosen, 1/144th of the
then-current 12-hour interval. The schedule became four-hourly on 2026-08-12
and hourly on 2026-08-13, which makes the same five minutes 1/12th of an
interval. The floor was not re-derived; it is recorded here so that the fraction
is not read as current — and 1/12th is close enough to the cadence that it
should be re-derived before it is quoted as a margin again.

On the one real stranded row in the store — `harvest_run` 28,
`claude-transcripts`, begun `2026-08-01T13:07:13Z` and never finished — the rule
reports from `13:19:51Z`, which is **5h47m before the staleness bound derived
from today's schedule would have said anything** (23h47m, while `-stale` still
defaulted to a flat 24h). Erring generous is cheap here and erring tight is not:
calling a live harvest dead is a lie that exits non-zero and discredits the whole
report, while calling a dead harvest live only delays the news, and that delay is
backstopped twice, by the next scheduled harvest within one interval and by the
staleness bound within one and a half — 1 hour and 1h30m on the schedule
installed today, though neither is bounded at all across a sleep.

A source part-way through its repositories is `running` too, not `partial`: three
read and a fourth in flight is not a fourth that was lost. But a repository that
was **refused** stays a failure whatever the process is doing now, so a failure
anywhere in the group still decides the word. Being busy is not an excuse the
report accepts on behalf of a lost input.

### The capture section, and why counting files was not enough

The block under `preserved in` is the **capture** phase, and it is a different
question from everything above it. The source table asks *is the evidence still
arriving?*; this asks *is the evidence still being preserved?*

Until 2026-08-01 it counted files in a directory and nothing else, because the
capture phase wrote no row of any kind. That is a hole with a specific shape: a
capture that failed today leaves yesterday's files exactly where they are, so
the count stays plausible and the only hint is a `newest` age that means
nothing unless you already know what it should be. During the 44-hour outage of
2026-07-30 this report would have shown a healthy file count throughout.
Ingestion's half of that outage is still legible in the live store —
`harvest_run` rows 19 and 21 through 24, `failed`, carrying
`operation not permitted`. The capture half left nothing at all.

It is also the worst phase to be blind in, and this is the reason the table
exists rather than a nicety. An ingestion can be repeated at any hour from a
snapshot already on disk; a capture is the **only** chance to hold `knowledgeC`
and Biome before they prune themselves. The one phase that cannot be re-run was
the one phase `status` could not see.

So every capture now writes a `capture_run` row, and each line carries both
halves: what is on disk, and what the last capture recorded doing.

| `outcome` | What it means |
| --- | --- |
| `ok, 2 new` | It ran and preserved two files. |
| `ok, nothing new, 13 already held` | It ran, and every file was already held byte for byte. `knowledgeC` snapshots, Biome segments and vault files are all content-addressed, so **this is the healthy steady state**, not a stall. |
| `nothing to capture on this machine` | It looked and there was nothing there: no Motion installed, no `filedays` roots configured, no such Biome stream. An observation, not a shrug. |
| `partial, 3 preserved and some lost` | It preserved some of what it tried and lost the rest, with the reason named below the table. |
| `failed` | It preserved nothing, with the reason named below the table. |
| `skipped by policy, so nothing was looked at` | It did not run, deliberately: the snapshot is throttled, so its cadence is not welded to the harvest's. A **decision**, and not an observation about the source at all. |
| `no capture recorded` | Nothing has ever recorded a capture of this kind. Reported and left to you to judge, like `never run` above. |
| `a status this report does not recognise ("…")` | `capture_run` holds a status this report has never heard of. Named rather than described, because the alternative is describing it in the words of a healthy one. |

The middle four are the ones that had nowhere to live before. **`ok, nothing
new` and `no capture recorded` are the pair that matters**: they can describe
byte-for-byte identical directories, and only the recorded row tells them
apart.

`skipped by policy` is a second pair with `ok, nothing new`, and a tighter one.
Migration 0015's `CHECK` pins a skipped run to `files_new`, `files_held` and
`bytes` all zero; a run that looked and found everything already held records
`files_new` zero with `files_held` positive. One column apart in the table, and
a world apart in meaning — one of them looked. Until 2026-08-13 this report fell
through to the `ok` wording for any status it did not recognise, so a policy
skip printed as `ok, nothing new`: a decision reported in the words of an
observation. The last row above is the general fix, and matters more than the
`skipped` row: a status this report has never heard of can no longer arrive
wearing the all-clear.

Staleness is checked **per capture** as well as for the harvest as a whole,
because the two come apart in exactly the case this section is about.
Ingestion keeps reading snapshots already on disk and keeps writing fresh
`harvest_run` rows, so the source table stays perfect while the capture feeding
it has silently stopped. A capture older than `-stale` says so and exits
non-zero, whatever the source table looks like.

A harvest killed *during* the capture phase writes no row at all, because the
outcomes are recorded once the store is open and the store is deliberately
opened after every capture — a database that is locked or corrupt must never be
able to delay the one phase that cannot be repeated. That degrades to `no
capture recorded`, which is honest, rather than to a row claiming to be live for
ever.

The totals are always **one harvest's**, never a sum over history. `harvest_run`
records no invocation id, so the boundary is inferred from the rows: the
ingestion loop begins each run the instant the previous one finished, so runs
of one harvest are chained end to end, and a gap of more than a second means a
new harvest. Measured across the 55 rows this store holds, the largest gap
inside a harvest is 0.0014s and the smallest gap between two harvests is 17.1s.
Without that boundary `knowledgec-appusage` reports 23,043 seen instead of
11,526 — two harvests of the same database added together — because its input
is the snapshot it read.

Note that the input is **not** a run identifier and never was. Since snapshots
became content-addressed, two harvests over an unchanged `knowledgeC` read the
same file and record the same input, so grouping by it would merge them. The
boundary above is inferred from the timing of the rows and is unaffected.

`last ok` is what turns "it is broken" into "it has been broken since". A
source that failed this morning and a source that has never once succeeded
have different causes and different costs, and the failure detail says which:

```
4 sources failed on the most recent run:
  knowledgec-backlit: 3 failures since it last succeeded, 33h ago
    cannot read /Users/you/Library/Application Support/Knowledge/knowledgeC.db: operation not permitted
```

| Flag | Meaning |
| --- | --- |
| `-store` | Event store to read. Defaults to `~/.timetattle/timetattle.db`. |
| `-dir` | Snapshot directory to count. Defaults to `~/.timetattle/snapshots`. |
| `-index` | Registry index, used to work out which sources a harvest would attempt. Defaults to `~/.timetattle/registered-directories.json`. |
| `-schedule` | Installed launchd plist to derive the staleness bound from. Defaults to the agent `install-agent.sh` writes, under `~/Library/LaunchAgents/`. |
| `-stale` | How old the newest run may be before the schedule is called stale. **No default.** It is derived from the schedule above, at one interval and a half. Given explicitly it wins, and the report says what it overrode. |
| `-inputs` | List every input each source read on its most recent harvest, one line each, with the failure under any that was lost. |

It **exits non-zero** when a source's most recent run failed, when a source was
only partly read, when a run is `stranded` (a harvest that died partway), when
the most recent capture of anything failed or was only partly preserved, or when
the newest run — or the newest capture of any one kind — is older than `-stale`.
A harvest **in progress**, having never run, and having no recorded capture are
reported but do not fail: sources and captures appear and disappear with what is
installed and registered, and a check that cries wolf is a check nobody reads.

While a harvest is in progress the all-clear changes wording rather than being
suppressed, because "every source ran and succeeded" over a source that is still
reading would be a false negative traded for the false positive it replaced:

```
a harvest is in progress, and this report is a snapshot of it:
  claude-transcripts: running for 12s, and it is not presumed dead until 13m
nothing else is wrong: every source that has finished succeeded within the last 6h00m, and every capture held
```

### Where the staleness bound comes from

`status` does not carry a staleness threshold of its own. It **reads the
installed launchd plist** — the same file `install-agent.sh` writes — and takes
the bound from whichever scheduling key is there: `StartInterval` directly, or
the widest gap between consecutive fires of a `StartCalendarInterval`. The bound
is that interval **and a half**: on the twenty-four hourly slots installed
today, an hour and a half. Verified against a hand-written 24-entry plist —
`timetattle status -schedule <plist>` reports `every 1h00m, from
StartCalendarInterval` and `stale: 1h30m`.

The bound is derived from the **harvest** schedule, and the `knowledgeC`
snapshot no longer follows it. That does not make the four `knowledgeC` streams
stale, because a throttled harvest still ingests the snapshot it already holds
and so still records a `harvest_run` row for each of them. What a throttled run
does not write is a `capture_run` row saying `ok`: it writes one saying
`skipped`, on time, which is the distinction worth having.

The plist rather than a copy of the cadence in a config file, because it cannot
go stale. A number in a config is a record of what someone meant to install; the
plist *is* what launchd will run. That was demonstrated the same day it was
built: the agent changed from a 12-hour `StartInterval` to six calendar slots,
and the bound went from 18 hours to 6 with nothing in `status` edited.

One and a half rather than two, because at twice the interval the age of the
newest run only reaches the bound at the instant the next run is already due —
so a single skipped harvest cannot be reported at all, and it takes two in a
row. At one and a half, a missed run is reported half an interval after it
should have happened and half an interval before the next one is due.

> **Corrects the `-stale` row above as it stood before 2026-08-12.** It read:
> "Defaults to `24h`, twice the interval `install-agent.sh` schedules, so one
> missed run is tolerated and two are not." That documented the defect as a
> feature. On 2026-08-12 the store held no harvest row, no capture and no event
> stamped that day, and `status` printed *every source ran and succeeded within
> the last 24h00m*. There is no default now; the bound is derived, and the
> report names it and the key it came from on every run.

**The bound is not a promise about wall-clock time.** launchd fires a missed
calendar slot within a second of the machine waking, but it cannot run at all
while the machine is off, and nothing bounds how long a laptop stays shut. So a
run older than the bound is reported as a reading and not as a fault: the
verdict states the age and says in so many words that `harvest_run` holds only
what ran and cannot tell a missed harvest from a machine that was not running
through it. In practice the wake catch-up closes most of the difference, because
this report can only be read on a machine that is running — by the time anyone
looks, the catch-up run has already advanced the newest run.

**A schedule that cannot be read produces no bound, and no verdict.** A plist
that is missing, unreadable, or carries neither scheduling key is reported as
itself — freshness is *not checked*, the store is called neither fresh nor
stale, and the all-clear is withheld and replaced by a line saying so. Quietly
falling back to a default would be the same defect one level down. Only the
unreadable case exits non-zero, on the same footing as an unreadable
`capture_run` table: the report could not take a reading it is supposed to take.
Having looked and found no schedule installed is an observation, and a machine
that harvests by hand is entitled to it.

**What this still cannot see.** `status` reads `harvest_run.started_at`, so it
reports whether a harvest RAN and never whether EVIDENCE ARRIVED. A harvest that
fires on time and ingests nothing advances the staleness clock while the store
stands still, and no bound derived from any schedule can catch that. Watching
the store itself advance is a separate check.

> **Closed 2026-08-13.** That separate check is now
> [Whether evidence arrived](#whether-evidence-arrived-which-is-not-whether-a-harvest-ran),
> below. The paragraph above still describes the *staleness bound* correctly —
> it is derived from the schedule and it can only ever see harvests — so it is
> left standing rather than edited. What has changed is that it is no longer the
> only freshness check in the report.

Staleness is not redundant with the failed-run check, and the incident that
prompted this command is why. Because the old capture phase failed before
`store.Open`, those three failed runs wrote **no rows at all** — the whole
33-hour outage is invisible in `harvest_run` to this day. Only the age of the
newest run could have caught it. Since the fix, both would.

> Extended 2026-08-01 (task 4tuVv). Staleness is now also checked **per
> capture**, against `capture_run`, and that is not redundant with the harvest
> staleness above either. Ingestion reads snapshots already on disk, so it goes
> on writing fresh `harvest_run` rows for as long as the store is readable —
> which means the whole source table, and the `last run` line with it, can stay
> perfectly current while the capture feeding them has stopped. Only the age of
> the last recorded capture catches that.

### Whether evidence arrived, which is not whether a harvest ran

Every check above reads `harvest_run`, so every one of them measures whether a
**harvest ran**. A harvest that fires exactly on schedule and ingests nothing
advances all of them while the store itself does not move. Measured on the live
store on 2026-08-13 at 15:04Z:

```console
$ sqlite3 ~/.timetattle/timetattle.db \
    "select max(started_at) from harvest_run;
     select max(coalesce(ended_at, started_at)) from event;"
2026-08-13T13:05:20.006382Z     # the newest harvest run, 1h59m old
2026-08-13T02:52:15.089123Z     # the newest event, 12h12m old
```

and the report read *every source ran and succeeded within the last 6h00m, and
every capture held*, and exited zero. Twelve hours with no evidence, and green.

**The age of the newest evidence is reported and never judged.** 15:04Z was
10:04 in the morning locally and the newest evidence was 21:52 the previous
evening: asleep, then not yet at the desk, is a complete explanation and nothing
in the store contradicts it. A rule that called twelve hours a fault would fire
every single morning. What the age *does* do is take the unqualified all-clear
away — the `evidence:` line above sits beside the schedule, the bottom line
carries the figure, and a reader can no longer be handed a green report over a
store that has not moved since yesterday.

**What is reported is silence somebody witnessed.** A source's silence is
measured as its **witnessed lag**: how far behind the newest evidence in the
whole store its own newest evidence sits. The store's frontier does not move
while nobody is recording, so a night, a weekend and a fortnight's holiday cost
every source exactly nothing. A source falls behind only while some *other*
source is recording — and that other source is the witness that the machine was
alive and this one said nothing through it. That is an observation rather than
an absence, and it is the whole of the distinction.

It is judged against **that source's own history**, because the thirteen sources
have nothing in common. Over each source's collected era on the live store the
longest witnessed lag ranges from 12.33h to 70.00h, so any single global bound
would be five times too tight at one end or six times too loose at the other.
The rule is four times a source's own longest closed lag, with a floor of one
harvest interval under it — derived from the same installed plist the staleness
bound comes from, so it follows the agent down to hourly with nothing edited.
The reason for a floor at all is that evidence cannot arrive faster than the
harvest brings it in.

```console
$ go run ./scripts/quietperiod            # every source's own quiet, and the replay
$ go run ./scripts/quietperiod -slack 1 -episodes
```

`quietperiod` is what the multiple of four was chosen with, replaying the store
prequentially the way `runningcheck` calibrates the stranded-run rule. Over the
fourteen-day record the rule reports 37 times at a multiple of 1, 17 at 2, 7 at
4, 2 at 6 and none at 10. **There is no multiple that comes clean and still sees
a stalled source, and there cannot be**: the maximum of a sample only ratchets
upward, so the first occurrence of any new length of quiet is unprecedented
whatever caused it. Four of the seven reports at the shipped multiple are the
single weekend of 2026-08-08 to 08-10, when the machine was on and recording
system activity while no commit, no task claim and no watched file was touched
— and those are true statements rather than false alarms. It is a trade, and the
right rate to re-measure it against is a longer store.

**A source nothing can judge is reported as such.** A source with no closed
quiet spell inside its own collected era, or fewer than twenty of them, gets no
bound and is named on its own line as unjudged — neither healthy nor faulty.
`blender-backups` and `motion-autosave` are both that on this machine: every
event either of them holds is a file modification time from before this machine
ever ran a harvest.

**And the finding never names a cause.** Whether a source was quiet because it
had nothing to say or because it has stopped working is not in the store, and
the report says so in the same breath as the reading — exactly as the stale
verdict says that `harvest_run` cannot tell a missed harvest from a machine that
was not running:

```console
1 source recorded nothing while the store went on advancing:
  knowledgec-appusage: nothing since 3d ago, while the store advanced 2d past it — 4.1 times the 12h20m it has ever been quiet for, across 8871 recorded quiet spells
    whether it has nothing to say or has stopped working is not in this record
2 sources recorded too little since collection began, so nothing here says what quiet looks like for them: blender-backups, motion-autosave
nothing else is wrong: every source ran and every capture held within the last 6h00m, but 1 source stopped recording, above
```

**It does not set the exit status.** `status`'s contract is that a thing
observed to be wrong fails and a thing never recorded is reported, and a
witnessed silence is on the second side of that line: it is a true reading whose
cause the report explicitly declines to name. What it does instead is replace
the all-clear, which is the half that mattered.

**How this reconciles with `dashboard`.** The two answer different questions and
the difference subtracts exactly. On local 2026-08-13 the store held **0 events
and 219 `harvest_run` rows**; `dashboard` therefore draws that day as *observed
and idle* rather than unobserved, because its rule is recorded presence **or** a
logged run ([the heatmap](dashboard.md)). Its Source Freshness section reports
`knowledgec-appusage` as harvested `2h33m ago`, which is `store.Freshness` —
the last successful *run*. `status` reports the same source as having stored
nothing for 3d, which is `max(COALESCE(ended_at, started_at))` — the newest
*event*. Between those two instants the source ran nine times and succeeded nine
times:

```console
$ sqlite3 ~/.timetattle/timetattle.db \
    "select count(*), sum(status='ok') from harvest_run
      where source_id='knowledgec-appusage' and started_at > '2026-08-11T00:28:45';"
9|9
```

Neither figure contradicts the other. What they share is that **`dashboard`'s
observedness test still counts a harvest row as an observation**, which is the
same weakness `status` carried, and it has not been changed here.

## Full Disk Access

Reading `knowledgeC` and Biome both require Full Disk Access, and **macOS
grants it per binary**. Your terminal having it does not give it to
`timetattle` when launchd runs the binary directly.

System Settings → Privacy & Security → Full Disk Access → **+** → press
`⌘⇧G` → enter `~/go/bin` → add `timetattle`.

A background agent receives no permission prompt. It simply fails, so check
`timetattle status` after the first scheduled run.

### Why `go install` silently revokes the grant

This is the single most expensive failure mode in the project, so it is worth
understanding rather than merely working around.

macOS records a Full Disk Access grant against a program's **code signature**,
not its path. What gets recorded depends on what the signature contains:

- A binary signed with a stable identity carries a **designated requirement** —
  in effect "this identifier, signed by this team". That requirement is what
  the permission is stored against, and any later build that still satisfies it
  keeps the grant. New version, bug fix, rebuild: all fine.
- A binary that is **ad-hoc signed** has no team identifier and no certificate
  chain, so there is no such requirement to store. The only stable thing left
  to key on is the **cdhash** — a hash of the code itself.

Go produces ad-hoc signed binaries. `codesign -dvvv` on one reports
`Signature=adhoc`, `Identifier=a.out` and `TeamIdentifier=not set`, which is
exactly the case with nothing to match on but the hash.

A cdhash changes whenever the code does. So `go install ./cmd/timetattle`
writes a different binary at the same path, the stored hash no longer matches,
and the grant stops applying. **The entry stays listed and switched on in
System Settings**, which is what makes this so hard to see: it looks granted
and is not.

### Recognising it

The July 2026 outage is the worked example. Three identical failures in
`~/.timetattle/harvest.err`:

```
timetattle harvest: cannot read /Users/you/Library/Application Support/Knowledge/knowledgeC.db: operation not permitted
```

and four things that together identify the cause:

1. `timetattle status` shows the affected sources failing, or — as in this
   case, where the failure predated the fix above — simply a `last run` that
   has stopped advancing.
2. `ls -l $(which timetattle)` against the last successful harvest. Here the
   binary was dated 2026-07-30 12:29 and the last good run was 12:01, **28
   minutes earlier**. That coincidence is the signature of this failure.
3. `codesign -dvvv <binary>` reports `Signature=adhoc` and
   `TeamIdentifier=not set`, so there was never a requirement to match.
4. The Full Disk Access entry is still present and still enabled. Presence is
   not the same as applying.

Unsigned, the fix is manual and must be repeated after every install:
**remove the entry and add it again.** Toggling it off and on is not enough,
because the entry itself is the thing keyed to the old hash.

### Signing ends the loop, and the installer does it

`scripts/install-agent.sh` signs the binary before it schedules it, by calling
`scripts/sign.sh`. Signing with a stable identity replaces the cdhash with a
designated requirement naming the identity, so **rebuilds keep the grant**.

This is measured rather than argued. Two builds of different content, signed
with one certificate, produce different cdhashes and the identical requirement:

```console
$ codesign -d -r- "$(which timetattle)"
designated => identifier "com.bensyverson.timetattle" and
              certificate leaf = H"…"
```

**A self-signed certificate is enough for this.** An Apple Developer account is
not required. It *is* required to distribute the binary to anyone else, because
Gatekeeper on another machine needs a Developer ID identity and notarization —
but that is a different problem from Full Disk Access, and either identity
solves this one. `sign.sh` prefers a Developer ID when one exists, falls back to
any valid identity, and refuses with instructions for creating a self-signed
certificate when there is none.

Two forms, and the difference matters when a certificate is eventually replaced:

| identity | requirement pins | survives certificate renewal |
| --- | --- | --- |
| self-signed | `certificate leaf = H"…"`, one specific certificate | no |
| Developer ID | `anchor apple generic` and `certificate leaf[subject.OU]`, the team | yes |

`--options runtime` and `--timestamp` are applied to every signature, including
self-signed ones. Both are notarization prerequisites, both work with a
self-signed certificate, and applying them always means the notarized path is a
change of identity and nothing else.

The grant must be approved once more after the **first** signed build, because
the requirement being stored is new. It then survives subsequent installs. Set
`TIMETATTLE_SKIP_SIGN=1` to install without signing, which restores the old
behaviour and reopens the loop.

> **A stale binary silently omits whole sources.** On this machine
> `claude-transcripts`, `motion-autosave` and `blender-backups` showed
> `never run` in `timetattle status` for exactly that reason until the binary
> was rebuilt. Reinstalling used to cost presence capture until the grant was
> restored, which made that a real trade-off; signed, it no longer does. After
> any reinstall, run `timetattle status` to confirm the sources are advancing.

## What a knowledgeC snapshot contains

Snapshots keep the `ZOBJECT` event table and nothing else. Against a real
37.4 MB database that is 1.6 MB, a 23× reduction, with no loss of anything
Timetattle uses.

**Kept** — every stream except one, so no future timing question is
foreclosed:

| Stream | Retention | Why it matters |
| --- | --- | --- |
| `/display/isBacklit` | ~28 days | Screen on/off. The presence denominator. |
| `/device/isLocked` | ~28 days | At-machine confirmation. |
| `/app/usage` | <10 days, unreliable | Stalled 2026-07-27 to 07-29, and again from 2026-08-10. Cross-check only; Biome is the attribution path. |
| `/app/mediaUsage` | ~28 days | Unevaluated; plausibly a render signal. |
| others | ~28 days | Retained because their value is not yet known. |

> **Measured 2026-08-13**, `go run ./scripts/streamseries`. The "~28 days" is
> confirmed and is an **age** limit: `/display/isBacklit` held a shortest
> retained span of 27.86 days and a median of 28.39 across 43 snapshots, and
> `/device/isLocked` 27.51 / 28.38. `/app/usage` is the exception and is capped
> by **row count** instead, giving 9.46 to 15.11 days depending on how busy the
> machine was. The two mechanisms are independent and the tighter one wins.

**Dropped:**

- **`ZSTRUCTUREDMETADATA`.** This is a privacy decision more than a size one.
  It holds web page URLs, serialized Siri interactions, contact identifiers
  and notification identifiers. A time tracker needs none of it, and keeping
  it indefinitely would be a liability with no upside. It was also verified
  to carry no information for `/app/usage`: all 10,000 rows reference just
  three distinct metadata rows with a single usage type.
- **`/app/webUsage`.** 29% of all rows, carrying two distinct values
  (`com.apple.Safari`, `com.apple.mobilesafari`). `/app/usage` already
  reports Safari focus with real intervals.
- **Indexes and free pages.** Regenerable.

## What a Biome capture contains

Captures go to `~/.timetattle/snapshots/biome/<stream>/`, one gzipped file per
segment per distinct set of contents:

```
biome/ScreenTime.AppUsage/804992150463800-cbe0b9e18f7ecb4659b20b6c35041128.segb.gz
biome/ScreenTime.AppUsage/806945468674832-e313934a5fe2c788457074e44ef743f1.segb.gz
```

The name is the segment's own file name, then the first 128 bits of the
SHA-256 of its contents. Segments are stored **whole and uninterpreted** — the
same discipline as the `knowledgeC` snapshot, and for the same reason: a bug
in the reader must not be able to cost history already in hand.

**Only the streams Timetattle uses are copied, named one by one.**
`~/Library/Biome/streams/restricted` holds 153 streams on a normal Mac,
including Safari navigations, Mail harvesting, Siri interactions and Bluetooth
proximity. The capture never globs and never copies the parent directory.
Today the list is exactly one entry, `ScreenTime.AppUsage`. Widening it is a
privacy decision, and a test fails if the list changes so that nobody can
widen it by accident.

`App.InFocus`, the sibling stream describing the same focus changes, is
deliberately **not** captured. Its newest segment was last written
2026-07-27 12:22 and has not been appended to since — the same pattern
`/app/usage` showed the day before, which turned out there to be a stall rather
than a retirement — and it carries a `remote/` directory of other devices' data
that nothing here wants.

### Why the whole stream is read every run

Biome pre-allocates every segment at 512 KB and fills it in place. Both cheap
ways of noticing a change are wrong, and both fail silently:

- **Size** is constant by construction. All six segments on the machine this
  was built against are exactly 524,288 bytes, including the one still being
  written.
- **Existence** — "copy it if we don't have it" — freezes the live segment at
  whatever partial contents it had when first seen, and loses every record
  written afterwards. No error, no visible gap.

**Modification time looks like the honest answer and is not.** Measured across
all six segments, mtime lags the last record *inside* the segment every time
and never leads it — by 3m59s at best and by 19h25m at worst. That is what a
memory-mapped file looks like, where the kernel stamps mtime on page flush
rather than on write. Either way mtime systematically under-reports how fresh
a segment is, so two runs either side of a stale window would see an unchanged
mtime over changed bytes.

So each segment is read and hashed, about 3 MB per run, and a segment whose
digest is already on disk is not written again. In practice five of six
segments are skipped and only the live one is stored.

The live segment **is** re-stored whenever it has grown, because that is the
only way not to lose the difference. What is guaranteed is the narrower and
correct property: no segment is ever stored twice for the same contents. A
segment settles on its own once a successor supersedes it. That cost was
measured at roughly 50 MB a year for the whole stream while the agent ran
twice a day; the complete 24-day history is 712 KB compressed, against 3 MB
raw. At the six runs a day the agent now schedules, the same arithmetic gives
an upper bound near 150 MB a year rather than a measurement, and the real
figure is lower, because a run whose live segment has not grown stores nothing
at all.

### Recovering a capture

```console
$ go run ./scripts/restorebiome -out /tmp/stream
$ go run ./scripts/appfocus -dir /tmp/stream/ScreenTime.AppUsage/local
```

`restorebiome` picks the newest version of each segment and decompresses it
under its original name, laid out exactly as macOS lays out a live stream.
Do not point a reader at the capture directory itself: it holds several
versions of the segment that was live, and their records would be counted
more than once.

## Known behaviour

**`/app/usage` stalls, and it stalled for a full day.** On 2026-07-28 it had
not advanced past `2026-07-27 14:28` while eight neighbouring streams logged
activity through 20:18, and it was written up here as retired. That was wrong,
and the correction is worth more than the original claim.

It resumed on its own. Row counts by event date, in
`knowledgeC-20260729T211413.db.gz`:

| 07-25 | 07-26 | 07-27 | 07-28 | 07-29 |
| --- | --- | --- | --- | --- |
| 652 | 163 | 793 | **0** | 806 |

Both halves of that matter. The dead day is real — zero rows between two
ordinary ones, and not the row cap, which prunes from the far end. And the
observation that produced "retired" is not evidence of a stop at all: the
stream was already measured **23 hours behind** `/display/isBacklit`, so a
newest row from yesterday afternoon is what a *healthy* copy of this stream
looks like. Within a day of its own lag, a stall and a delay cannot be told
apart.

Application attribution stays Biome's job for exactly that reason: a stream
that goes silent for a day cannot be the one that says which application held
the screen, and Biome has shown no such gap. `/app/usage` is snapshotted and
collected for the overlap it holds, which is what
[Bare stops are sleep](/docs/app-focus/) was validated against.

The consequence for any collector reading a `knowledgeC` stream: **never tail
it.** Re-read a generous overlapping window and rely on deduplication — which
is what [`internal/knowledgec`](/docs/presence/) does, filtering only on an
event's own end and never on when its row appeared. A stall that ever exceeded
the retention window would mean rows are pruned before they are written, which
would be fatal to the source.

Watch it with:

```console
$ go run ./scripts/kcprofile
```

That reports every stream's retention window and how stale it is, flagging
anything over a day old. It also inventories the sensitive fields a raw
database holds, and confirms a snapshot has none of them.

## Why snapshots stopped accumulating

A `knowledgeC` snapshot averages 1.53 MB (measured 2026-08-12: 58.0 MB across
38 of them, `go run ./scripts/snapchurn`). Every one of them used to be written
afresh under a new timestamped name on every run, with no check of any kind —
the one capture here that behaved that way, since a Biome segment and a Motion
vault file are both skipped when a file already on disk holds their exact
bytes. Three of those 38 snapshots, 4.8 MB, are byte-for-byte repeats of a file
sitting next to them, written by runs eleven seconds and three minutes after
the one they duplicate.

A snapshot is now named for its contents as well as its instant, and a run that
would write bytes already on disk writes nothing and reports the file that
holds them. Nothing is ever deleted, moved or overwritten to achieve this, and
the source is still read, staged and extracted in full every time — the only
thing skipped is the redundant write.

### What "identical" means, and why it is not the file

The obvious implementation does not work, and fails silently. A `knowledgeC`
snapshot is a SQLite database **plus** its `-wal` and `-shm` sidecars, and
those three files are not a function of the rows inside them. A checkpoint
folds the write-ahead log back into the main database and truncates the
sidecar, rewriting a large fraction of a 35 MB file while changing no row at
all; a write to `ZSTRUCTUREDMETADATA` or to `/app/webUsage` — both of which a
snapshot deliberately discards — appends log frames for data that is never
preserved. A digest over what was copied off disk therefore reports "changed"
nearly every time, and a deduplication built on one would skip nothing while
reporting success.

The digest is taken over the **extracted** database instead: the `ZOBJECT` rows
that actually get preserved, in the file that actually gets written. Extraction
is what normalises the source. Measured against this machine's real database,
a checkpoint moved the staged bytes from 36,999,800 to 36,864,000 and the
21,965 preserved rows were unchanged, and the snapshot was correctly held.

### What it is worth, and what it is not

Removing the check and running the same capture twice over an unchanged real
`knowledgeC` writes **1,497,605 bytes a second time** — 1.43 MB, on every run,
for ever. That is the whole of what the check recovers per redundant run, and
the number to weigh against a schedule.

At six snapshots a day, `knowledgeC` was about 3.3 GB a year, against 1.1 GB at
the twice-daily schedule it replaced, plus up to 150 MB a year for Biome.

> **Read the table below as snapshot cadence, not harvest cadence, from
> 2026-08-13.** When it was measured the two were the same number. They are not
> any more: the agent harvests hourly and `-snapshot-every` holds `knowledgeC` at
> four hours, so the row that describes today's storage is the **four-hourly**
> one, at about 3.2 GB/yr, while the harvest itself runs on the hourly row's
> schedule and costs nothing extra. The hourly row is now what would happen with
> `-snapshot-every 0`.

Deduplication does **not** scale that down by a fixed factor, and assuming it
does is the easiest way to be wrong here by a wide margin. What it recovers is
exactly the runs during which nothing was written to `knowledgeC` at all, so
its yield depends entirely on how far the cadence outruns the source's own
write rate. Measured over a trailing 14 days with
`go run ./scripts/snapchurn`:

| cadence | windows | no row created | projected |
| --- | --- | --- | --- |
| twice daily | 28 | 0 (0.0%) | 1.09 GB/yr, unchanged |
| four-hourly | 84 | 1 (1.2%) | 3.23 GB/yr, against 3.27 |
| hourly | 336 | 89 (26.5%) | 9.85 GB/yr, against 13.1 |

Each skip figure is an upper bound: a row can also age out of retention inside
an otherwise quiet window, and that changes a snapshot too. So at the snapshot
cadence installed today this recovers almost nothing. What it does is stop the
cost growing in proportion to the cadence — and, in the event, it was not enough
on its own. Hourly snapshots still projected to 9.85 GB/yr with the check in
place, which is what led to giving the snapshot a cadence of its own instead.

The remaining redundancy is at a finer grain than a file. Across the 38
snapshots the corpus stores 890,104 `ZOBJECT` row-instances and holds 38,966
distinct rows, a 22.8× repeat: two snapshots an hour apart differ by a few
dozen rows out of twenty-odd thousand, and a whole-snapshot digest recovers
none of that. Reaching it would mean a snapshot that is no longer a complete
picture on its own, which every reader here currently relies on, and that is a
separate decision rather than a bigger version of this one.

A retention policy remains deliberately deferred until ingestion is trusted:
until the collector is proven, the raw snapshots are the only copy.

## Recovering a knowledgeC snapshot

Snapshots are ordinary gzipped SQLite databases.

```console
$ gunzip -c ~/.timetattle/snapshots/knowledgeC-20260728T180718.db.gz > /tmp/kc.db
$ sqlite3 /tmp/kc.db \
    "SELECT datetime(ZSTARTDATE+978307200,'unixepoch','localtime'), ZVALUESTRING
     FROM ZOBJECT WHERE ZSTREAMNAME='/app/usage' ORDER BY ZSTARTDATE DESC LIMIT 5;"
```

Timestamps are Core Data seconds from 2001-01-01, hence the `978307200`
offset.

Two name shapes coexist in the directory and both are ordinary snapshots.
`knowledgeC-<instant>.db.gz` is what was written before snapshots carried a
content digest; `knowledgeC-<instant>-<digest>.db.gz` is what is written now.
Only the second shape can be matched against, so a run will never hold against
one of the older files — they are the perishable evidence this whole phase
exists to protect, and renaming them to add a digest was rejected on those
grounds. Sorting by name is still sorting by time, because the instant comes
first and is fixed width.

## What the Motion vault capture contains

Captures go to `~/.timetattle/snapshots/motion-vault/`, mirroring the vault's
own composition subdirectories, one gzipped file per distinct set of contents:

```
motion-vault/dark-crystal-v2/dark-crystal-v2_2026-07-11_1745-3f9a1c....motn.gz
```

The name is the original relative path with the first 128 bits of the
SHA-256 of its contents spliced in before the extension — the same
content-addressed scheme Biome segments use, and for the same reason: nothing
here may assume a filename is only ever written once. Job task g7v07's
2026-07-31 controlled test found a clean close and a force-quit both
currently leave a composition's autosaves intact, but the actual pruning
trigger is still unconfirmed, so a file that reappears under a name already
captured with different bytes is preserved as a second version rather than
silently dropped.

This preserves the vault's own bytes, not events, and it is deliberately
separate from ingestion: the `motion-autosave` manifest source reads the
**live** vault directly, through `internal/timestamped`, on every run — it
does not read this capture. So there are two independent things happening to
the same directory on every harvest, for two different reasons:

- **The capture** answers "what was in a composition's autosave", by
  mirroring bytes that Motion's unconfirmed pruning trigger could otherwise
  delete before anyone reads them again.
- **Ingestion** answers "when was a composition open", by turning whatever
  is on disk *right now* into `source.Event`s carrying a timestamp and a
  document name — see [Adding an evidence source](/docs/sources/).

A file the capture already preserved because Motion has since pruned it from
the vault will not be ingested on a later run — ingestion has no way to read
bytes that only the capture still holds. That is the tradeoff of reading the
live vault directly rather than the preserved copy: it costs nothing extra
to keep in sync (there is no copy to reconcile), at the price of losing
autosave *events*, not just autosave *bytes*, to whatever prunes the vault
between one harvest and the next.

A vault that does not exist is what "Motion is not installed" looks like, and
produces no output — the same tolerance a missing Biome stream gets.

A vault root that **is a symlink** — the usual shape of a vault living on an
external volume with a link left behind under `~/Movies` — is followed and
preserved normally. It used to preserve nothing, silently: measured on
2026-08-01, 3 files captured by the real path and 0 through a symlink to the
same directory, reported as a successful capture. Of everything this defect
was found in, the vault had the most at stake, because it is perishable —
what a "successful" run fails to copy is not merely missed, it is gone.

A vault root that exists but **cannot be walked** — a symlink whose target
has been deleted, a symlink to a file, a plain file, a directory that refuses
to be read — is now reported as `motion vault: capture lost (…)` naming the
root, rather than as a capture of nothing. An existing vault holding no
autosaves yet still reports `0 files captured`, because that zero is correct.

## What the filedays capture contains

Snapshots go to `~/.timetattle/snapshots/filedays/`, one JSON file per run,
never overwritten:

```json
{
  "observed_at": "2026-07-28T21:33:00Z",
  "excludes": [".git", "worktrees", "node_modules", ".venv", ".Trash", "Autosave Vault"],
  "roots": [
    {
      "root": "/Users/you/git/acorn",
      "raw": { "2026-07-11": 513, "2026-07-28": 1204 },
      "filtered": { "2026-07-11": 421, "2026-07-28": 980 }
    }
  ]
}
```

`observed_at` is the whole reason this is a snapshot and not a bare count. A
file records only its *last* modification: work done today on a file last
touched on 11 July does not add evidence for 11 July, it **moves** it,
crediting today instead and shrinking every earlier day's count. Measured —
a scan of one repository found 1,773 files modified on 2026-07-11, and the
same scan hours later found 513. Both numbers were true when taken. Neither
means anything without `observed_at` alongside it, and nothing distinguishes
"little work happened that day" from "those files were touched again" once it
decays. See [Adding an evidence source](/docs/sources/#mtime-evidence-decays-and-the-loss-is-invisible)
for the full argument, and `project/private/2026-07-28-source-corrections.md` §4 for
the measurement.

`raw` and `filtered` are both kept for the same reason a `knowledgeC`
snapshot's row count is reported alongside what a source table drops:
`filtered` excludes the directories named in `excludes` (an agent worktree
holds a complete copy of a tree, so one edited file can otherwise be counted
many times over), and keeping `raw` beside it makes the inflation visible in
the record itself.

**No roots configured means nothing is scanned**, and `harvest` prints
nothing for this capture — not an error, the same as an uninstalled
application. Configure roots either with `-filedays-roots` or by creating
`~/.timetattle/filedays-roots.txt`:

```
# one directory per line; blank lines and lines starting with # are ignored
~/git/acorn
~/git/timetattle
```

This is not `config.toml`, deliberately: no TOML parser is a dependency of
this module, and a list of paths does not justify adding one.

Like the Motion vault *capture* — the preserved copy, not the
`motion-autosave` events read from the live vault — these snapshots are not
ingested as events. They are day markers for a human or a future collector to
read, not evidence the store currently reasons about.

Reproduce a scan by hand, outside a harvest, with:

```console
$ go run ./scripts/filedays -root ~/git/acorn
```

### Only regular files are counted, and only walkable roots produce a count

Two ways this used to invent a number, both measured on 2026-08-01 and both
worse than a silent zero, because a plausible count is not questioned.

**A symlink was counted as a file.** A symlink's mtime records when the
pointer was made, not work anybody did, and `d.IsDir()` is false for a link
even when it points at a directory. Counting them inflated a real scan of
`~/git/acorn` from 18,503 to 18,532 raw files — exactly the 29 symlinks
`find ~/git/acorn -type l | wc -l` reports — and put them all on the day the
links happened to be created. Sockets, fifos and device nodes were counted the
same way. Only regular files are now.

**A root that was itself a symlink returned a fabricated 1.** `filepath.WalkDir`
calls `os.Lstat` on its own root argument, so the callback saw the link, called
it a file, counted it, and descended nowhere: a tree of three files scanned as
3 by its real path and as 1 through a symlink to it. Symlinked roots are now
resolved before walking, while `root` in the snapshot stays the path you
configured — a resolved path is not what any roots file names, and on macOS
`/var` resolves to `/private/var`, so recording what was walked would stop two
observations of the same configured root from lining up.

A root that exists but cannot be walked — missing, a dangling symlink, a
symlink to a file, a plain file, a directory that refuses to be read — is now
an error naming the root. `Take` keeps the roots that did scan and reports the
rest as `filedays: capture lost (…)`. An existing, readable, empty directory
still returns no counts and no error, because that zero is a real observation.
