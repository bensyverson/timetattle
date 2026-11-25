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
excluded: *.bz2, *.gz, *.log, /Users/you/git/Easley/reports/
1755 observations excluded before clustering and not counted below: 86 SQLite
sidecars, 1034 by "*.bz2", 100 by "*.gz", 17 by "*.log", 518 by
"/Users/you/git/Easley/reports/"

PRESENCE 1069.0h across 1370 sittings, median 22m16s, p95 2h58m14s
  293.8h observed, 775.2h (72.5%) inferred by the clustering

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
is over all 38 and does not add up from the six above it. The report above is
also this page's: it was captured with `-all`, which the rest of this section
explains.

Time-scope a run with `-from` and `-to`, narrow it to one client or one
project with `-client` and `-project`, and override any clustering parameter
for that run only with `-gap`, `-lead-in`, `-tail-out`, `-presence-sources`,
`-ignore-sources` and `-exclude`. Nothing writes the configuration back.

## Default scope: the working directory

Run with no flags at all, `metrics` scopes to wherever it is run from, resolved
through the registry the same way [`timetattle register`](registry.md) blesses
a directory and the bare `timetattle` command reads one:

- Inside a registered project — or any subdirectory of one — it reports only
  that project.
- Inside a directory that is itself unregistered but sits above one or more
  registered projects that all name the **same client**, it reports that
  client's projects, and only them. Client identity here is the markers'
  `client` field, not anything about the directory's own name; two or more
  clients found beneath it is refused rather than guessed at.
- Anywhere else, it refuses rather than silently reporting everything:

  ```console
  $ cd ~ && timetattle metrics
  timetattle metrics: /Users/you is not registered, and nothing under it is
  either — `timetattle register` blesses it, or pass -all to report every
  client and project
  ```

`-all` restores the old, unscoped behaviour: every client and project, from
any directory. `-client` and `-project` still narrow the report exactly as
before; giving either one bypasses directory scoping entirely; so does `-all`.

## Evidence not yet attributed

A project is written onto stored evidence by an [attribution
pass](registry.md#evidence-that-is-already-stored), which `harvest` runs last. A
scoped report over evidence the pass has not reached would otherwise read like a
report over no work, so it says which it is:

```
NOT YET ATTRIBUTED: 42 stored observations under project "Holiday Campaign"
  Observed under its registered directories and carrying no project yet, so
  no figure here counts them. `timetattle harvest` attributes them.
```

The count is of observations in the report's own window that carry no project
although the most specific registered directory above them belongs to the
client or project asked for. They were observed; what is missing is only the
attribution. It is read from the registry and the events already in hand, as a
comparison of paths: no extra query and no walk of the filesystem. So it can
miss evidence spelled through a symlink the registration does not name.

`metrics`, [`dashboard`](dashboard.md) and the [bare
command](#the-bare-command-what-this-directory-is) all print it. When nothing in
scope is attributed yet, `dashboard` stops after the notice instead of drawing
sections of zeros. Under `-csv` the notice goes to stderr. An `-all` report
never prints it.

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

**A client's hours are never the sum of its projects'.** Absolute engaged time
overlaps by design — that is what makes it the invoice figure — so a minute two
of a client's projects were both active in bills to each of them and to the
client *once*. It is close to the union of their spans, and not exactly it; see
[A client is re-clustered](#a-client-is-re-clustered-so-it-can-hold-a-little-clock-no-project-holds)
below for the difference and how large it is.

The difference is not small, and it is invisible on a client with one project.
On the corpus above, `personal`'s 33 project rows sum to **701.7h** absolute
while the client rolls up to **617.7h**. The 84.0h between them is clock where
two or more of those projects ran in the same minute. Allocation, which
partitions rather than overlapping, agrees exactly at both levels: **603.3h**.

Reproduce both figures with:

```console
$ timetattle metrics -client personal      # the "shown" row totals its 33 projects
```

### A client is re-clustered, so it can hold a little clock no project holds

A client's hours are **not** assembled from the project rows. Its own evidence —
every one of its projects' observations at once — is clustered again under the
same parameters, exactly as a project's own evidence is. That matters wherever
one client's projects are worked alternately: a silence too wide for any single
project to bridge can be narrow enough to bridge once the client's evidence is
taken together, and the client is then credited a stretch that none of its
projects is credited.

It is small and it is real. On the corpus above, measured with
`go run ./scripts/rollupunion`, the 33-project client holds **19m13s** — 11
stretches, the longest 4m19s — that no project of its own holds, out of 617.7h.
The same minutes also appear under UNATTRIBUTED PRESENCE as bridged silence,
because a stretch no project covers is a stretch nothing was attributed in.

**That stretch is kept, and it is work rather than an artefact.** Moving between
two of one client's projects costs attention that neither project's own evidence
records; a stretch bridged only once the client's evidence is taken together is
that switching cost made visible. So a client's hours are what that client's
evidence clusters to when taken together, and **a client can exceed the sum of
its projects** — a report where it does is correct, not broken.

Until 2026-08-13 a closing check refused exactly that report. It rejected any
client exceeding its projects' *sum*, and where a client's projects run one after
another rather than concurrently, that sum has no overlap to absorb the bridged
clock: `timetattle metrics -from 2026-04-24 -to 2026-04-24` on the corpus above
exited non-zero and printed nothing at all — no missing row, no report. The check
was wrong rather than the figure, and it is gone. The decision and every figure
behind it are in `project/2026-08-13-rollup-reclustering-resolved.md`.

What still closes the roll-up is the half of it that is true by construction: a
client owning exactly **one** project is that project's own evidence clustered
again, so it can never be credited more than the project is. That is an
inequality and not an equality, because a project can hold clock its client does
not — a directory registered to a client partway through its life has earlier
evidence belonging to the project and to no client, which the report reports as
`Clientless`.

The same relation holds inside **every bucket** of a `-by day` or `-by week`
slice, and is checked there too. A bucket is summed from the same segments the
report is, and a segment names its projects and its clients together, so the clip
a bucket edge makes is one duration booked to both levels at once — an edge
cannot divide a client and its project differently. Note that the *other*
direction gets worse under slicing rather than better: a client's figure can sit
comfortably inside its projects' sum over a whole span and outside that sum in
one bucket of it, because narrowing the window strips out the concurrency that
was inflating the sum. A roll-up that reconciles over a span has said nothing
about the buckets of it.

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

Under `-project`, the `ENGAGED, per client` row above the table is still that
client's **whole** roll-up across every project of theirs in the span, not the
share belonging to the project below it. It is printed so the project can be
seen against the client it belongs to, and the two figures differ whenever the
client has more than one project. The report says so on the run.

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
| unaccounted | nothing at all — silence the gap threshold bridged inside the project's own cluster, or an hour a person assigned here by hand |

The three sum to `absolute` exactly, and the sentence beneath the table prints
all three so the remainder cannot be quietly folded into either neighbour. Once
anybody has recorded an attribution correction the remainder has two sources,
and that sentence names both — see
[Corrections](#corrections-what-a-person-said).

**`weak` and `corroborated` are not opposite ends of one scale.** A focus
interval says somebody was at the machine and says nothing whatever about which
client, so a minute can be both. Nor is `weak` the same as unobserved: a project
can read `0.0h` observed and be attested for almost all of its hours, because a
transcript message is an instant and an instant observes no duration.

Which sources count as recording an act is a judgment made in one place,
`internal/source`, and named out loud there. An **unclassified source is weak**:
not having weighed a stream is not the same as having found it strong.

## The activity mix: what kind of work, not whose

Every other table in the report answers *whose* an hour was. `ACTIVITY MIX`
answers *what was going on in it* — how much of a week was Claude, how much was
git, how much was a file changing on disk.

```
ACTIVITY MIX, by kind of evidence
  family        participated    share    solely  observed  observations
  Apps                352.3h    30.2%     87.3h    324.2h         55635
  Blender               1.6h     0.1%      0.5h      0.0h            10
  Claude              989.3h    84.9%    339.8h      0.0h        856656
  Files               475.2h    40.8%     43.5h      0.0h        132312
  Git                 308.0h    26.4%     16.5h      0.0h          4543
  Jobs                293.7h    25.2%      2.4h    158.7h         16655
  Motion                7.2h     0.6%      3.0h      0.0h            38

        1.2h  reached by NO family: silence the gap threshold bridged BETWEEN
              two streams, which neither of them bridges on its own.
```

Those figures are the whole store, unwindowed, from `timetattle metrics -all` on
2026-08-13. They sum to 2427.3h against the **1165.3h of presence** the same run
reports — **2.08 times the clock**, which is the point of the section rather
than a defect in it.

### The rows overlap and never sum

**Adding that column up produces a figure that is not any quantity at all.** A
commit made in the middle of a Claude Code session is observed by `Git` *and* by
`Claude`, and neither can be made to give the minute up, so there is no
partition of the clock by source. This is the same arithmetic the
[client roll-up](#a-client-is-re-clustered-so-it-can-hold-a-little-clock-no-project-holds)
already lives under, one axis over — and on the live store the families
genuinely do sum to several times presence, so it is not a technicality.

Two figures per row are what make the section safe to read rather than merely
warned about:

| | |
| --- | --- |
| `participated` | the presence this family's own evidence clusters to. It overlaps every other row. |
| `solely` | the part of it **no other family reaches**: what would drop out of this section if the stream were not there. |

`solely` is the honestly comparable figure. The `solely` figures *are* disjoint,
and together with the clock **no** family reaches they tile presence exactly —
which the report checks, two different ways, before printing anything.

### A family's clock is its own evidence re-clustered

Exactly as a project's and a client's are. The alternative — crediting a family
the whole of any sitting its evidence appears anywhere inside — is a model this
tool has already measured wrong one level down, where it summed to 24.01h across
a 12.78h day. Applied to families it would credit `Git` a whole afternoon on the
strength of one commit at the end of it.

**This is why the figure differs from the `dashboard`'s source bar.** That panel
partitions whole *sittings* by which families evidenced them, treating the
sitting as atomic — a legitimately different question asked of the same
evidence. A family's clock there can never be *smaller* than it is here, since a
sitting is credited in full for one observation anywhere inside it. The two are
not the same measurement and must not be subtracted from each other; they also
do not cover the same span, since the dashboard reports a trailing window and
this reports whatever `-from`/`-to` asked for.

### Clock no family reaches

The line beneath the table is printed even when it is zero, because it is what
says the rows do not tile presence. It is silence the gap threshold bridged
**between** two streams: ten minutes of Claude, a commit twelve minutes later
and more Claude twelve minutes after that is one sitting, and no single family's
own clustering covers the whole of it. Awarding that clock to a family would
credit a stream with time it never reached.

### The mapping is printed, not documented

The `built from:` line beneath the table names the source ids that went into
each row, **read from the corpus rather than from the table in the code**. A
grouping a reader cannot check is a verdict whose list nobody has seen. Read
from the corpus, the line names exactly what produced the figures beside it.

| family | streams |
| --- | --- |
| `Apps` | `biome-appusage`, every `knowledgec-` stream |
| `Blender` | `blender-backups` |
| `Claude` | `claude-transcripts` |
| `Files` | `fs-observations` |
| `Git` | every `gitlog-` stream |
| `Jobs` | every `job-` stream |
| `Motion` | `motion-autosave` |

`Files` is its own family and not folded in with `Blender` and `Motion`: the
generic walk says a file changed and cannot say which application changed it or
whether a person did, while the other two name the application they came from.
Both `job-` streams are one family, because rows that never sum would otherwise
leave a reader with two figures and no way to add them.

It is one table, in `internal/source`, and `timetattle dashboard` draws its
source bar from the same one. Two commands disagreeing about what an hour *was*
would be worse than neither reporting it.

**A family with nothing in the span gets no row, and is named anyway.** A
`0.0h` row would state that `Blender` observed none of this week when the only
thing known is that no Blender observation reached it; saying nothing at all
would leave a reader unable to tell a stream that recorded nothing from one the
tool has never heard of. So a `no observation in this span:` line names them.
(On the whole store every family is present and the line does not appear.)

**A source no family has been decided for is named, not dropped.** A manifest
can declare any id, so the mapping is a list, and a list goes stale silently —
in the direction that makes a report look complete. Those observations appear
under `unclassified` with their ids on the `built from:` line, so somebody can
decide where they belong. (`dashboard` refuses to draw at all in that case; a
chart is all-or-nothing, while withholding an entire `metrics` report would hide
every figure the new stream has nothing to do with.)

### What is not in the mix

**Presence and ignored sources**, by role rather than by oversight. A backlight
reading bounds sittings and creates none, and an ignored source is dropped on
the way in, so neither can be responsible for an hour of work. The backlight
has [its own figures](#presence-is-not-screen-on-time).

**Evidence that no project could be attributed is in the mix, and deliberately.**
Biome and knowledgeC carry no path and appear in no project row and no client
row anywhere in the report; this is the one place they are counted. Leaving them
out would answer "what kind of work was this" using only the streams that happen
to resolve to a directory.

**The mix is not narrowed by `-client` or `-project`.** It is a decomposition of
presence, exactly as the `PRESENCE` line at the top is, and a filtered report
says so rather than letting the figures be read as the filtered scope's own mix.

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

Presence is the clock the *sittings* cover, and the sittings are clustered out
of **evidence**. The backlight never creates one, because fourteen hours of lit
screen with no other trace is not fourteen hours of work — and it no longer
shortens one either. A dark reading observes a shut lid, which is a proxy for
somebody being at the machine, which is a proxy for work; a render, a compile or
an unattended agent produces work with the screen off and leaves the same trace
as an empty room. So darkness suppresses only an observation a **wall-clock
timer** wrote, and the [clustering](/docs/sessions/#what-a-dark-backlight-actually-observed-which-is-a-lid)
has the argument.

**Presence therefore includes time nobody was at the keyboard for**, and that is
deliberate rather than an artifact. It is the capacity denominator, and capacity
consumed by a background render is still consumed.

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
              `timetattle orient` says which of them can be registered.
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

A **fourth** line appears once somebody has recorded an attribution correction
saying a span belonged to no project, and only then:

```console
        0.5h  a person having looked and said it belonged to no project. That is an
              observation, not a gap in the registry, and no amount of registering
              will change it.
```

It is deliberately not one of the three above. Those are all ways the machine
*failed* to attribute an hour, and two of them send a reader off to register a
directory; this one says the attribution is settled and the answer is *nobody*.
Folding it into an unregistered directory would send somebody to fix a registry
with nothing wrong with it. See [Corrections](#corrections-what-a-person-said).

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

## Corrections: what a person said

Attribution is a **model**, and it is wrong in ways no source can see. A
directory can be registered to one project and used for another's work for an
afternoon; a meeting leaves a transcript in whichever repository happened to be
open. [`timetattle review`](review.md) and
[`timetattle adjust`](adjustments.md) both record a person's answer, keyed to a
span of wall clock, and this command reads it.

Every report says what it found, **including when it found nothing**:

```console
CORRECTIONS: none recorded over this span, so every figure above is the machine's
own answer. `timetattle review` records one against a block; `timetattle adjust
-assert attributed` records one directly.
```

That line prints on every run for a reason. Until 2026-08-10 the store recorded
attribution assertions faithfully, and nothing read them: a person could
reattribute a span, watch it survive re-derivation exactly as promised, and find
every reported hour unmoved with nothing anywhere saying why. **Silence is
indistinguishable from agreement**, so the report names the population it
checked rather than leaving the reader to infer it from an absent section.

When there are corrections, what they did is broken out:

```console
CORRECTIONS 1 attribution assertion applied
        1.8h  moved to a project somebody named. Those hours are that project's on a
              person's word and nothing else's, and they are counted in its asserted
              column.
        0.2h  fell outside every sitting and moved nothing. An attribution says whose
              an hour was, never that there was one; `timetattle adjust -assert
              engaged` is what makes the hour exist.
```

Four headings, and they partition the asserted clock exactly — the command
refuses to print a report where they do not add up, because a correction that
evaporated between the log and the figure is the failure this whole layer exists
to end.

| | |
| --- | --- |
| moved to a project | presence a person reattributed to a named project |
| moved to no project | presence a person said belonged to nobody |
| agreed | the machine had already said the same thing, so nothing moved |
| outside every sitting | the correction reached clock no sitting covers |

The last two are the ones worth reading hardest. A reader who has recorded a
correction and sees nothing move needs to know **which** silence they are
looking at, and they need different things next: agreement means the model was
right; outside means the hour does not exist yet in the derivation, and
`-assert engaged` is what makes it exist.

### An attribution says whose an hour was, never that there was one

Presence, the sitting-length distribution and the backlight figures are **byte
for byte** what the uncorrected report gives. Letting an attribution create or
destroy presence would make the capacity denominator a function of how much
reviewing had been done.

### Asserted hours are named, never mixed into the evidence

A project handed an hour by a person is not thereby *observed* for that hour. Its
`observed`, `attested` and `weak` columns do not move, so the hour lands in the
unaccounted remainder — and the sentence beneath the table then says how much of
that remainder is a person's word rather than bridged silence:

```console
  of 13.2h absolute: 10.4h attested, 1.3h weak (a file's modification time only),
  1.4h unaccounted, of which 1.4h is a person's assertion and the rest bridged silence
```

The command checks this rather than intending it: if asserted time ever exceeded
the unaccounted remainder, a person's word would have been counted somewhere as
though something had observed it, and the report refuses to print.

### The client follows the project, or the hours go clientless

An assertion names a **project**. `metrics` reads no registry, so the client is
taken from the report's own evidence: if exactly one client in the report owns
that project, the hours follow. If the corpus cannot place the project, or two
directories claim it for different clients, the hours belong to the project and
to no client — the `clientless` figure, which already exists and already says
what it means. Guessing an owner there would be last-writer-wins dressed as a
roll-up.

### Only the attribution half of the log is applied

The same correction log carries `engaged` and `not-engaged` assertions, and
`metrics` deliberately does **not** apply those. They move the capacity
denominator every share here is divided by, and
[the fitting](fitting.md) measures the clustering against exactly that
difference: the residual between what the model said and what the reviewer said
*is* the error signal, and a report that had already absorbed the reviewer's
engagement corrections would shrink it toward zero for reasons having nothing to
do with the model improving. Attribution carries no such hazard, because no
parameter being fitted is an attribution parameter.

### `-uncorrected` gives the machine's answer alone

```console
$ timetattle metrics -uncorrected
```

Every figure is what the derivation says with no human overlay on it, and the
report says so instead of quietly looking like an uncorrected one. It is the
comparison the fitting rests on, so it stays reachable.

## What a run is doing, and what it cost

An unwindowed run reads every event in the store, and that is not instant. While
it works, each phase names itself on a single line that rewrites itself and then
erases itself:

```
client roll-up 4 of 6
```

**It goes to stderr, and only when stderr is a terminal.** Stdout carries the
report and nothing else, because the primary reader of this command is a program
capturing it; a pipe or a log file gets no progress at all, since redrawing a
line into one writes carriage returns nobody reads.

### `-timings` says where the run went

```console
$ timetattle metrics -timings

timings for this run, by phase; a count in brackets is what the phase worked through:
  reading events (1069011)                2.872s   54.2%
  dropping machine activity (1067254)     0.637s   12.0%
  stamping clients                        0.036s    0.7%
  reading corrections                     0.011s    0.2%
  clustering                              0.280s    5.3%
  grouping evidence                       0.183s    3.5%
  per-project time (38)                   0.850s   16.0%
  client roll-up (6)                      0.398s    7.5%
  segmenting                              0.013s    0.3%
  formatting                              0.012s    0.2%
  elsewhere                               0.010s    0.2%
  total                                   5.303s  100.0%
```

The table also goes to stderr, so `-timings` changes not one byte of the report.
`elsewhere` is whatever the phases failed to account for — opening the store,
reading the config, process startup — printed as its own row rather than left as
a difference a reader has to notice and subtract.

The two attribution phases are the ones worth understanding. `per-project time`
re-clusters each project's *own* evidence, and `client roll-up` does the same
one level up, once per client; neither is a sum over the other, for the reason
[Two levels: client and project](#two-levels-client-and-project) gives. So the
cost of both scales with the number of *groups*, not only with the number of
events, and
a client costs more per group than a project does because a client's evidence is
every one of its projects' evidence at once.

**A single run is a noisy sample.** The store read varies by a factor of two
between a cold page cache and a warm one, and it is the largest phase. Use
`scripts/metricsphases` when a figure is going to be written down: it builds a
fresh binary, runs the command several times, and prints the min, mean and max
of every phase, with the population — windowed or not — printed above the table.

**A windowed run and an unwindowed one are different populations.** `-from`/`-to`
narrows what is read, so its timings cannot be compared with an unwindowed run's;
say which one a figure came from, every time.

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
threshold bridged across midnight becomes two sittings instead of one; and a
dark span that begins outside the day is not there to suppress the timer-written
observation inside it. Days computed that way cannot be added up at all.

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
| `unattributed` | `unregistered`, `unlocatable`, `asserted` or `bridged` | presence no project could be attributed, by reason |

The four `unattributed` rows have no total row above them, deliberately: they
sum to the whole exactly, and a further row carrying the total would
double-count for any consumer that added the column up. All four are written on
every period, including `asserted` on a store nobody has ever corrected, so the
export's shape is fixed rather than a property of the data — a row that appeared
only sometimes would break a program the first time somebody reviewed a day.

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
configuration](sessions.md): total presence ran 2.3x across the plausible
range of the gap threshold, nothing has yet fitted it against ground truth, and
the shipped 15-minute default is a placeholder in the middle of that range. Use
`timetattle sessionize -sweep` to see the spread on your own data before quoting
a figure from it, and never quote one without saying which gap produced it.
