---
title: "Engaged time and presence"
weight: 50
description: "The two metrics a working day is judged by, why they cannot collapse into one, and why every engaged hour in this tool is inferred."
---

# Engaged time and presence

Two metrics, from one timeline, and they cannot be collapsed into one.

- **Engaged** time is per-project and **may overlap**, because concurrent work
  on two projects is billable to both. It is what an invoice is built from.
- **Presence** is wall clock and is **never double-counted**. It is the
  denominator for capacity — a half-time retainer measured against engaged time
  could read as satisfied at a fraction of real attention.

`timetattle metrics` reports both. It reads the events already in the store,
computes on demand, and writes nothing at all: the store is opened read-only,
and there is no derived table to rebuild or to go stale.

```console
$ timetattle metrics
~/.timetattle/timetattle.db
gap 15m0s, lead-in 5m0s, tail-out 5m0s
presence: knowledgec-backlit
ignored:  knowledgec-islocked, knowledgec-mediausage
excluded: *.bz2, *.gz, *.log, /Users/ben/git/Easley/reports/
1755 observations excluded before clustering and not counted below: 86 SQLite
sidecars, 1034 by "*.bz2", 100 by "*.gz", 17 by "*.log", 518 by
"/Users/ben/git/Easley/reports/"

PRESENCE 1069.0h across 1370 sittings, median 22m16s, p95 2h58m14s
  293.8h observed, 775.2h (72.5%) inferred by the clustering
  wall clock at the machine, counted once however many projects shared it

sitting lengths
  under 5m       160 sittings       3.0h
  5m to 15m      397 sittings      68.9h
  15m to 30m     241 sittings      87.4h
  30m to 1h      244 sittings     176.1h
  1h to 2h       185 sittings     252.0h
  2h to 4h       107 sittings     291.4h
  4h and over     36 sittings     190.3h

ENGAGED, per project                          (six of 38 rows shown)
  project           absolute     weak   observed  corroborated   allocated    share  sittings
  Easley                5.4h     3.2h       0.0h          0.0h        5.4h     0.5%        28
  FirstPassPro         64.3h     6.1h       0.0h          0.0h       53.3h     5.0%        97
  acorn                  46.6h    13.1h       4.9h         44.8h       41.4h     3.9%       106
  birch            164.5h     0.5h      46.0h        102.5h      146.9h    13.7%       202
  cypress             185.7h     8.8h      28.6h         58.5h      177.0h    16.6%       192
  timetattle           24.0h     0.2h       6.8h         21.8h       16.6h     1.6%        50
  sum                1136.0h    89.1h     125.5h        279.6h      995.3h
```

The `(six of 38 rows shown)` note is this page's, not the command's: the sum row
is over all 38 and does not add up from the six above it.

Scope a run with `-from` and `-to`, narrow it to one client or one project with
`-client` and `-project`, and override any clustering parameter for that run
only with `-gap`, `-lead-in`, `-tail-out`, `-presence-sources`,
`-ignore-sources` and `-exclude`. Nothing writes the configuration back.

**A bare date on `-to` means the end of that day.** `-to` takes an instant and
the span is inclusive at both ends, so `-to 2026-08-03` once meant midnight that
morning and silently dropped the whole of the day it named — which made
`-from D -to D`, much the most natural way to ask for one day, select nothing at
all. An explicit time is still that time: `-to 2026-08-03T09:00:00` is nine in
the morning. `timetattle adjust` is deliberately unchanged, because its `-to` is
the end of an assertion somebody is writing down, not a window to read through.

## Two levels: client and project

Work is reported at both levels, because they answer different questions and
neither is redundant. In one real registry, five clients have exactly one
project each and a sixth spans 33 — so collapsing to a single key would either
lose the client roll-up or turn 33 distinct bodies of work into one
indistinguishable blob.

```console
$ timetattle metrics
ENGAGED, per client
  client           absolute     weak   observed  corroborated   allocated    share  projects
  acorn               47.0h    13.6h       4.9h         45.2h       41.6h     3.8%         1
  birch              181.0h     0.5h      55.6h        118.9h      161.1h    14.8%         1
  cypress            185.7h     8.8h      28.6h         58.5h      177.2h    16.3%         1
  personal           617.7h    37.0h      38.1h         41.5h      603.3h    55.4%        33
  timetattle          30.8h     0.2h       8.5h         28.4h       20.7h     1.9%         1
```

**A client's hours are the union of its projects' spans and never their sum.**
Absolute engaged time overlaps by design — that is what makes it the invoice
figure — so a minute two of a client's projects were both active in bills to
each of them and to the client *once*.

The difference is not small, and it is invisible on a client with one project.
On the corpus above, `personal`'s 33 project rows sum to **701.7h** absolute
while the client rolls up to **617.7h**. The 84.0h between them is clock where
two or more of those projects ran in the same minute. Allocation, which
partitions rather than overlapping, agrees exactly at both levels: **603.3h**.

Reproduce both figures with:

```console
$ timetattle metrics -client personal      # the "shown" row totals its 33 projects
```

Allocation divides by client at the client level and by project at the project
level, so they are different splits rather than the same one re-added. A minute
two of one client's projects and one of another's were all active in gives each
*project* a third and each *client* a half. A client who keeps more repositories
does not thereby consume more of the day.

### Filters select rows and change no figure

`-client` and `-project` narrow which rows are printed. The whole report is
computed either way, so every figure in a filtered row is exactly what it is in
the unfiltered report, `PRESENCE` stays the whole span's wall clock, and a share
stays a share of a real day.

That is deliberate. Filtering the *events* and recomputing would shrink the
denominator — presence is wall clock across every source at once — and drive
every share toward 100%, answering the opposite of the question a single-client
report is asked. The totals row is labelled `shown` rather than `sum` when a
filter is on, because a partial total is not the one the surrounding text
describes.

A filter matching nothing says so, rather than printing a table of zeroes: "this
client was engaged for no time", "this client has no evidence in this span" and
"there is no such client" are three different answers.

### Where the client comes from

**Not from the event.** A project's client has 38 distinct values across some
973,000 events, so storing it per row would be ~927,599 updates to record
something the size of a small table — and the attribution pass would rewrite all
of them on every harvest. It lives in an **append-only log** instead, one row per
registration, and each event asks which client its project belonged to *at its
own instant*.

That last part matters: **a client association is a fact about a moment in
time**. A project reassigned in June reports the old client for May's work and
the new one for July's, from one pass over ~38 rows. See
[The registry](/docs/registry/#renaming-a-client) for how a reassignment is
recorded and why it must be stated rather than guessed.

`metrics` still reads neither the registry nor the filesystem. The log lives in
the same database as the events, so the command's answer stays a property of the
store rather than of the machine it runs on.

### Work with no client

Hours attributed to a project but to **no client** are reported as their own
figure and folded into none of the three unattributed reasons below. Those
directories *are* registered; it is the client that is unrecorded, which calls
for a different action. Three things produce it:

- nothing has ever asserted a client for that project;
- an assertion exists but takes effect **later** than the work;
- two directories claim the project for **different clients** — listed as
  `CONTESTED PROJECTS`.

A contested project is **reported, never resolved**. Neither directory changed
its mind, so there is no later-is-truer ordering between them, and picking the
later one would be last-writer-wins — a plausible wrong number. Its hours go to
no client until one of the directories is re-registered.

### What the log does not rewind

The log records history so that history exists to be asked about later; **no
command exposes it today**, deliberately. Capturing it is irreversible if
skipped and cheap now; exposing it is reversible and can wait for a question
that needs it.

When something does expose it, one limitation will apply and is worth knowing
now. The log rewinds the **client mapping** and nothing else. `event.project` is
a cache the attribution pass overwrites on every harvest, with no history, so a
directory blessed today attributes its old evidence as though it always had
been. The honest reading of any past-dated answer is *"what we now know about
that work, under the client mapping as it stood then"* — and the unattributed
figures are always as of now.

`-exclude` is how a proposed exclusion is measured before anybody commits it to
a config file — see
[Excluding machine activity](/docs/sessions/#excluding-machine-activity). What
it dropped is itemised by rule above the figures it changed, so a total here can
always be reconciled against the store it came out of.

## Read the margin, not the total

**Every engaged hour this tool reports is inferred.** A sitting is a cluster of
observations with a lead-in before it, a tail-out after it, and every silence
shorter than the gap threshold bridged. Over the corpus above, 72.5% of presence
is that margin rather than anything anybody observed, so the report prints the
observed and inferred halves separately and never a total on its own.

The `observed` column reading `0.0h` for most projects is not a rounding
artefact and not a bug. Almost every source that can be attributed to a project
— Claude transcripts, git history, file saves — records **instants**, and an
instant observes no duration. The projects with a non-zero figure are the ones a
held task claim covers, which is an interval and does observe one.
`corroborated` is the third state: clock some source
observed the machine in use across, without saying whose work it was, which is
what a Biome focus interval is. Three answers, kept apart:

| | |
| --- | --- |
| `observed` | this project's own evidence spanned this clock |
| `corroborated` | *something* observed the machine in use; nothing said for whom |
| the rest | nothing was recorded, and the clustering bridged it |

The command prints a warning when the first column is empty across the board,
because a figure of 185.7h with nothing beside it is an inference wearing the
clothes of a measurement.

## Weak hours: what the time rests on

`weak` is a **different question** from every column beside it. It asks whether
anything at all recorded a person *doing* something in these hours, or whether
the only evidence is that a file's modification time changed.

A modification time is not somebody working. A log-rotation cron running once a
day for 513 days once read as 126.2h of presence and 135.2h of one client's
engaged time, and excluding it dropped that client from 11.2% of all attributed
time to 0.5% (see
[Excluding machine activity](/docs/sessions/#excluding-machine-activity)). What
survived exclusion is the honest residue: 88 project-days across 28 projects
whose whole trace is a handful of file timestamps, any one of which may be a
real evening spent editing one document. **Nothing removes them.** The column
marks them so a person can judge them.

Three answers, and the third is the one that is easy to lose:

| | |
| --- | --- |
| attested | an observation recorded an act: a message, a commit, a claim held |
| `weak` | nothing but a file's modification time, which a cron writes too |
| unaccounted | nothing at all — silence the gap threshold bridged inside the project's own cluster |

The three sum to `absolute` exactly, and the sentence beneath the table prints
all three so the remainder cannot be quietly folded into either neighbour.

**`weak` and `corroborated` are not opposite ends of one scale.** A focus
interval says somebody was at the machine and says nothing whatever about which
client, so a minute can be both. Nor is `weak` the same as unobserved: a project
can read `0.0h` observed and be attested for almost all of its hours, because a
transcript message is an instant and an instant observes no duration.

Which sources count as recording an act is a judgment made in one place,
`internal/source`, and named out loud there. An **unclassified source is weak**:
not having weighed a stream is not the same as having found it strong.

## Absolute and allocation

A minute two projects were both active across can be counted two ways, and both
are reported side by side because a single column would leave a reader guessing
which one they had.

- **Absolute** counts it in full for each project. The column sums to more than
  the clock on any day two projects ran at once, which is correct for an invoice
  and wrong for capacity.
- **Allocated** splits it between them. The shares sum to **exactly** the
  attributed presence, to the nanosecond — the remainder of an uneven division
  is handed out rather than dropped, and the command refuses to report figures
  that fail to add up.

Use absolute for billing and effective rate. Use allocated, over presence, for
"what share of my working time did this client actually get".

The table fits the terminal, and the **project name** is the only column allowed
to lose a character: every other column is a figure, and the sittings count sits
at the right-hand edge where a clipped line would delete it. Piped, nothing is
shortened at all. See [Fitting the terminal](terminal.md).

## Where per-project time comes from

Sittings are **global** wall-clock spans, clustered over every evidence source
at once. That is deliberate: a per-project clustering could produce engaged time
and could never recover presence, because two projects' sittings would each
claim the same hour.

Per-project engaged time is therefore an intersection against that skeleton — a
project's own evidence, re-clustered under the same parameters, clipped to the
sittings. The obvious alternative, crediting a project the whole of any sitting
its evidence appears anywhere inside, was measured on a genuinely concurrent day
and gave one client 2.4x its independently measured time; it bills a whole
sitting on the strength of one commit at the far end of it. See
[the measurements](../../../project/private/2026-08-01-two-metrics.md) §2.

The cost of the model that shipped is the mirror image: a project whose evidence
is sparse relative to the work is undercounted, and the gap threshold has far
more leverage on per-project engaged time than on presence — 55% against 3.8%
on one measured day. **Do not quote a per-project figure without saying what gap
produced it.**

## Presence is not screen-on time

Presence is the clock the *sittings* cover. The backlight only bounds them, by
splitting a sitting where the screen was observed dark; it never creates one,
because fourteen hours of lit screen with no other trace is not fourteen hours
of work.

The gap between the two is large in both directions, so the report carries it
rather than leaving it to be assumed. On an earlier run of this corpus — before
the exclude list above, so not the same store as the example — the backlight
corroborated 252.1h of the 1001.9h of presence and said **nothing whatever**
about the other 749.9h — knowledgeC prunes itself in 9–28 days while the
transcript corpus reaches back much further. In the other direction, 28.3h of
lit screen falls outside any sitting at all, which is the warm-up before a day
starts that [ground truth](../../../project/private/ground-truth.md) finding 4 records.

Silence is not darkness. A stretch the backlight said nothing about is a third
state, and reading it as "off" has deleted real work in this project three
times.

## Unattributed presence is three figures, not one

```console
UNATTRIBUTED PRESENCE 73.9h (6.2% of presence)
        6.0h  a path resolving to no registered project, across 1743 observations.
              Some of those directories can be registered and some no longer exist;
              telling them apart needs the registry and the filesystem, which this
              command reads neither of. `timetattle orient` separates them.
       65.7h  a source carrying no path at all, across 45104 observations.
              knowledgeC and Biome; registering can never reach these.
        2.2h  silence the gap threshold bridged. Nothing was recorded here, which is
              not the same as nothing having happened.
```

A large first bucket means something is unaccounted for, not necessarily that
something needs registering. The three are separate because they call for three
different responses:

1. **Unresolved.** Evidence carrying a path that resolved to no project. What to
   do about it depends on the path, and this command cannot tell: it is handed
   events and nothing else, so a live unregistered directory, a directory since
   deleted, and the literal path `/` are indistinguishable here. Run
   [`timetattle orient`](orient.md), which has the registry and the filesystem in
   hand, to see which of them is which.
2. **Unlocatable.** Evidence carrying no path at all. Biome focus intervals know
   a bundle identifier and presence is not about a file, so no amount of
   registering will ever reach these; attributing them needs a different
   mechanism.
3. **Bridged.** Clock no observation is near enough to account for. Nothing was
   recorded here, which is not the same as nothing having happened.

> **Corrected 2026-08-01.** This section previously labelled the first figure
> ACTIONABLE and told the reader to run `timetattle register` on the directory.
> That was measurably wrong. After the attribution pass caught up on 2026-08-01
> the figure fell from 529.4h across 445,678 observations to 6.0h across 1,743,
> and of those 1,743 **not one** named a directory `register` could have been
> pointed at: 1,311 carried the locator `/`, 325 named paths that no longer
> exist, 38 sat inside the Motion Autosave Vault, and the 69 that remained were
> scratch directories. The old wording was the same actionable-versus-
> unregisterable collapse one level up from the one the three figures avoid.
> Reproduce with `timetattle orient`.

## A series: day, week and month

`-by day`, `-by week` and `-by month` cut the span into periods and print them
beneath the report. `-by span` is the whole span as one period, which is what
`-csv` emits on its own.

```console
$ timetattle metrics -from 2026-07-21T00:00:00Z -to 2026-08-04T00:00:00Z -by day

BY WORKING DAY, days from 05:00 in America/Chicago (CDT, -05:00 at the first day), weeks from monday
  working day     length   presence   absolute   allocated  unattributed collection
  2026-07-20         24h       1.8h       1.8h        1.8h          0.0h unrecorded
  2026-07-21         24h      13.6h      12.5h       12.2h          1.4h unrecorded
  2026-07-22         24h       9.1h       4.5h        3.9h          5.3h unrecorded
  ...
  2026-08-01         24h      11.9h      18.6h       11.8h          0.1h lost
  2026-08-02         24h       5.0h       6.5h        4.8h          0.1h ok
  2026-08-03         24h      10.4h      11.3h        9.2h          1.1h lost
  sum                        146.5h     154.6h      126.8h         19.7h

146.5h across 15 working days, against an ungrouped 146.5h; difference 0s
```

### It is a re-slice, and that is why it adds up

The obvious way to report a day at a time is to filter the events to each day
and compute each one separately. It is wrong, and wrong invisibly. Clustering is
not distributive over a partition of the events: a day computed alone gets its
own lead-in at its first observation and its own tail-out at its last, so every
boundary manufactures margin the whole span does not have; a silence the gap
threshold bridged across midnight becomes two sittings instead of one; and the
presence readings that would have trimmed a sitting from outside the day are not
there to trim it. Days computed that way cannot be added up at all.

So the periods here are cut out of **the same partition every figure above them
is summed from**. A sitting that crosses a period boundary is **split** at it and
counted once on each side, exactly as
[`sessionize -by-day`](sessions.md) splits one across a working day. The
consequence is that the rows sum to the ungrouped total *exactly*, on a 23-hour
day as much as on a 25-hour one, and the report prints the difference on every
run so that the claim is falsifiable rather than asserted.

**PRESENCE is the column that sums.** ABSOLUTE still overlaps *between projects*
— that is what makes it the invoice figure — so a period two projects worked in
at once shows more absolute than presence, and the report says how many of its
rows do.

Cross-check, measured 2026-08-04: over the fortnight above, every one of the 15
per-day presence figures agrees with `timetattle sessionize -dry-run -by-day`
over the same span **to the second**, and the two totals agree at 527,425s. The
two are independent code paths — one buckets segments, the other buckets stored
sittings — so their agreement is evidence rather than a tautology.

### Periods run from the working-day boundary

05:00 local rather than midnight, so an evening running to 01:37 is still that
evening's work. `-day-start`, `-week-start` and `-timezone` override the
configured calendar for one run, exactly as they do on
[`sessionize`](sessions.md). A day containing a daylight-saving transition is 23
or 25 hours long and the length column says so; a rate quoted per period should
divide by that column rather than by 24.

Weeks and months are **folded from the day rows**, not cut from their own edges,
which is what makes the three units agree exactly instead of nearly. A period the
span only partly covers says how many of its days are present, because "8h that
month" and "8h across the two days of it we asked for" are different claims.

### Every period is printed, and a zero is ambiguous

A period inside the span that nothing fell in still gets a row. A caller handed
a shorter list than it asked for cannot tell a period with no work from a period
nobody sliced.

But a `0.0h` row means one of two things — nobody worked, or nothing was
collected — and the store can separate them only where a capture or a harvest
left a row. The `collection` column is that separation, in three states:

- **`ok`** — at least one capture or harvest run started inside the period and
  none of them reported losing anything.
- **`lost`** — at least one run inside the period failed, or preserved only part
  of what it tried.
- **`unrecorded`** — no run of either kind started inside the period. **A zero
  here cannot be told apart from a period in which nothing was collected**, and
  the report says so by name and quotes the earliest run anywhere in the store.

That last state is the common one for most of any real corpus: capture outcomes
arrived with migration 0007 and nothing before it was recorded, so the harvest
that failed silently for 44 hours in July 2026 — a reinstall dropped its Full
Disk Access grant — is invisible to this column. Where a row *does* exist, the
column says only that collection was **running** at the time, not that the
period's evidence is complete: a later harvest ingests evidence produced days
earlier.

## CSV, for another program

`-csv` writes the series instead of writing a report. It is a data interchange
format, so it carries no prose: everything the report says in sentences it says
in a column.

```console
$ timetattle metrics -from 2026-07-21 -to 2026-08-04 -by day -csv | head -4
period,start,end,unit,days,of_days,covered,collection,level,name,seconds,observed_seconds,allocated_seconds,corroborated_seconds,attested_seconds,weak_seconds,unaccounted_seconds
2026-07-20,2026-07-20T05:00:00-05:00,2026-07-21T05:00:00-05:00,day,1,1,whole,unrecorded,presence,,6576,6570,,,,,
2026-07-20,2026-07-20T05:00:00-05:00,2026-07-21T05:00:00-05:00,day,1,1,whole,unrecorded,clientless,,0,,,,,,
2026-07-20,2026-07-20T05:00:00-05:00,2026-07-21T05:00:00-05:00,day,1,1,whole,unrecorded,unattributed,unregistered,0,,,,,,
```

*(The project and client names in a real export are your own. The pseudonyms
`acorn` and `birch` stand in for them everywhere in this documentation.)*

**One row per period per grouping**, in a long shape rather than a wide one. A
wide row — one column per project — cannot say what a level *means*: presence is
not a project, unattributed presence is not a project, and a client is a
different population from the projects beneath it. A wide table forces all of
them into a project-shaped hole and then needs a zero wherever a project was
idle, which makes an idle project indistinguishable from one that does not
exist. The `level` column says which question each figure answers:

| `level` | `name` | what the `seconds` column is |
| --- | --- | --- |
| `presence` | empty | wall clock in the period, counted once |
| `client` | the client | that client's **absolute** engaged time |
| `project` | the project | that project's **absolute** engaged time |
| `clientless` | empty | presence a project owns and no client does |
| `unattributed` | `unregistered`, `unlocatable` or `bridged` | presence no project could be attributed, by reason |

The three `unattributed` rows have no total row above them, deliberately: they
sum to the whole exactly, and a fourth row carrying the total would double-count
for any consumer that added the column up.

**The figures are exact and re-aggregatable.** The table prints `0.1h`, which is
six minutes of resolution — fine to read and useless to add up. These are
decimal seconds rendered from the integer nanoseconds the report is made of, by
integer arithmetic rather than through a float, so summing every row of a period
reproduces the period and summing every period reproduces the span to the
nanosecond.

`-client` and `-project` select which grouping rows are written, exactly as they
select which rows the table prints, and recompute nothing. The presence rows are
untouched by either: presence is the whole span's wall clock and belongs to no
project, so dropping it under a project filter would leave an export whose
shares had no denominator.

**An empty cell is not a zero.** Presence is not allocated to anybody, so
`allocated_seconds` on a presence row is *empty*: an allocation of none and a
question that does not arise are different facts, and a consumer that read the
second as the first would draw a chart of confident zeroes.

## The bare command: what this directory is

`timetattle` with no arguments prints a headline for the project the current
directory belongs to, and then prints the usage. Both, always, in that order.

```console
$ cd ~/git/acorn && timetattle
acorn for Northgate
  /Users/you/git/acorn
  over the 14 days to 2026-08-04T07:32:20-05:00:
    engaged     24.0h absolute, across 50 sittings of its own, on 35394 observations
    presence   124.7h at the machine, of which this project reaches 19%
  ...
timetattle — reconstruct when work happened, and for whom
  ...
```

The usage never disappears, and that is the point: a bare command that printed
different *kinds* of output in different directories would be the worst version
of this. It exits 0 in every directory, for the same reason — a status that
depended on where you ran it from would break scripts in exactly the case nobody
tested. An unknown command is still a usage error and still exits 2.

It is **cheap by construction**, and the design follows from that. A full report
clusters the whole corpus to establish presence, which is where its fifteen
seconds go; somebody typing `timetattle` to see the command list cannot wait
that long. So the headline reads the sittings
[`sessionize`](sessions.md) already stored — milliseconds — and measures the one
project against them. The consequences are stated rather than hidden: it names
the instant the derivation was computed, calls it stale if the parameters have
changed since, and **counts the observations newer than the derivation**, which
fall in no sitting at all and would otherwise make the most recent work read as
no work.

The percentage it prints is not an allocated share. Absolute engaged time counts
a shared minute in full for each project, so it cannot be added across projects;
`timetattle metrics -project` computes the allocated split, over every project at
once, which is the only way it can be computed.

Three things it refuses to collapse, each into its own paragraph: a directory
that is not registered, a project with no observations in the window, and a
store nothing has ever been derived from. All three would otherwise print as an
absence of hours.

## Sitting length is a signal of its own

Sixteen twenty-minute blocks and two four-hour blocks are both "presence hours"
and are obviously different kinds of time, so the distribution is printed beside
the total rather than behind a flag. Fragmented weekend presence around family
is not sustained work; on the corpus above, 34 sittings — 2.9% of them — hold
18% of all presence.

The median and p95 are nearest-rank, so each is the length of a sitting that
exists rather than an interpolation between two that do, and they are the same
figures [`timetattle sessionize`](sessions.md) prints over the same sittings.

## The threshold is still not a settled number

Everything here inherits the caveat on [sittings and
configuration](sessions.md): total engaged time ran 2.3x across the plausible
range of the gap threshold, nothing has yet fitted it against ground truth, and
the shipped 15-minute default is a placeholder in the middle of that range. Use
`timetattle sessionize -sweep` to see the spread on your own data before quoting
a figure from it, and never quote one without saying which gap produced it.
