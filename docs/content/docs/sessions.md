---
title: "Sittings and configuration"
weight: 45
description: "Clustering observations into sessions, the threshold nobody has fitted yet, and the one file Timetattle is configured with."
---

# Sittings and configuration

Almost every source Timetattle reads yields **point** observations: a commit, a
file save, a Claude message. Presence is the only thing that directly observes
an interval. So duration is always inferred by clustering *across* sources and
never interpolated *within* one — treating each file's birth-to-modification
span as working time claimed 70+ hours on a 13.9-hour day.

`timetattle sessionize` is that clustering. It reads the events already in the
store, groups them into sittings, and replaces one derived table:

```console
$ timetattle sessionize
~/.timetattle/timetattle.db
gap 15m0s, lead-in 5m0s, tail-out 5m0s
presence: knowledgec-backlit
ignored:  knowledgec-islocked, knowledgec-mediausage
excluded: *.bz2, *.gz, *.log, /Users/you/git/Easley/reports/
1757 observations excluded before clustering and not counted below:
  86 SQLite sidecars, 1034 by "*.bz2", 100 by "*.gz", 19 by "*.log",
  518 by "/Users/…/reports/"
1088305 observations read: 1082008 evidence, 2161 presence, 4136 ignored
748 observations had an endpoint that was never observed, and counted as an instant
1181 sittings, 1243.8h presence, median 29m23s, p95 3h50m34s
836.8h of that (67.3%) is margin: lead-in, tail-out and bridged silence
observed absence: 640.3h recorded, 0.0h trimmed across 0 splits, 1 observations suppressed
contested (read as lit and dark at once): 13.3h, trimmed from nothing
```

The `excluded` lines appear because this machine has configured
[exclusions](#excluding-machine-activity); the list ships empty, so a fresh
install prints `excluded: (none)`.

**Read the last three lines together.** Two thirds of that presence is margin
the clustering invented rather than anything anybody observed, and the 640.3h of
darkness the backlight recorded removed no time from any sitting and suppressed
exactly one observation. Neither zero is a rounding artifact and neither is a
sign the stream is broken; both are explained below.

It captures nothing and reads no source. Everything it needs is already in the
database, which is what makes it safe to run as often as an argument about the
threshold requires.

## The threshold is not a settled number

**Do not quote a total without saying what gap produced it.** Over the Claude
transcript corpus the spike measured, total presence ran from 530.4h at a
two-minute gap to 1199.9h at two hours — a 2.3x spread. Reproduced through this
package on the same corpus:

```console
$ timetattle sessionize -lead-in 0 -tail-out 0 \
    -presence-sources '' \
    -ignore-sources biome-appusage,knowledgec-appusage,knowledgec-islocked,knowledgec-mediausage,knowledgec-backlit \
    -sweep 2m,5m,10m,15m,30m,45m,1h,2h
```

| Gap | Presence | Gap | Presence |
| --- | --- | --- | --- |
| 2m | 537.1h | 30m | 871.7h |
| 5m | 657.2h | 45m | 955.2h |
| 10m | 727.5h | 1h | 1020.4h |
| 15m | 777.9h | 2h | 1183.6h |

2.20x, against the spike's 2.26x, and within 1.4% at every threshold.

The counter-intuitive part is that the gap distribution is sharply *bimodal* —
93.5% of gaps under ten seconds, only 0.22% between five minutes and two hours —
and that trough does not make the threshold safe to ignore. One 45-minute gap
costs 45 minutes whether or not it has company. **Rarity does not imply
irrelevance.**

The shipped default of 15m is a placeholder in the middle of that range and
nothing has fitted it against ground truth. Use `-sweep` on your own data before
quoting a figure from it.

### The spread is a property of the corpus, not of Timetattle

Measured on the same machine, on the same day, at 2m/15m/2h:

| What is being clustered | Spread |
| --- | --- |
| Claude transcripts alone (sparse points) | 2.20x |
| Biome focus and knowledgeC alone (dense intervals) | 1.06x |
| Both together | 1.93x |

Focus intervals blanket the day, so almost no silence reaches even two minutes
and the threshold has nothing to decide. Point sources are the opposite. A
threshold fitted against one of these corpora is not fitted for the other, and
which sources are in the store when you fit it therefore matters as much as the
value you choose.

## Three roles, and every source has exactly one

Which is which is configuration, not code.

| Role | What it does |
| --- | --- |
| **Evidence** | Clustered. Sittings are built out of it and nothing else. |
| **Presence** | Never creates a sitting, and no longer shortens one. All it does is suppress a timer-written observation recorded in observed darkness — [see below](#observed-absent-observed-present-and-nothing-recorded). |
| **Ignored** | Read from the store and dropped. |

`knowledgec-backlit` is the only stream treated as presence, and it is
deliberately **not** evidence. Fourteen hours of lit screen with no other trace
is not fourteen hours of work; clustering presence would make an evening of
reading indistinguishable from an evening of building, and it would collapse the
[two metrics](/) — engaged, which is per-project and may overlap, and presence,
which is wall clock and never double-counts — into one.

Two sources are ignored by default, both because their *meaning* is
unestablished rather than because they are uninteresting, and the cost of
getting either wrong is measurable:

- **`knowledgec-islocked`** — the polarity is inferred from the field's name and
  [nothing else](/docs/presence/). One `locked` interval in the live store runs
  **15h32m**, so clustering it as evidence manufactures a fifteen-hour sitting
  out of a night's sleep.
- **`knowledgec-mediausage`** — 88% Safari, and documented as *nothing should be
  inferred from it until somebody measures what it tracks*.

`knowledgec-appusage` is **not** ignored, even though it is a cross-check rather
than the attribution path. It observes application use, which is evidence of
work; sittings are wall-clock spans, so a source that agrees with Biome adds no
time, and one that reaches somewhere Biome missed adds real coverage.

## Observed absent, observed present, and nothing recorded

These are three states, not two, and this project has shipped the two-state
version of the mistake in three separate tools. On 2026-07-14 the backlight
sampler emitted nothing of either value between 12:35:20 and 14:02:24 while
Biome recorded dense application switching throughout: read as darkness, that is
1h27m of confirmed work deleted.

So presence acts in one direction only.

- **Observed absent** — covered by an `off` reading and by no `on` reading —
  suppresses an evidence observation *anchored* inside it whose cause is a
  **wall-clock timer**, and does nothing else. It does not split a sitting, it
  is not subtracted from one, and it suppresses nothing written by any other
  mechanism.
- **Observed present** merges nothing. A lit screen between two observations
  three hours apart is not a reason to call them one sitting; it is a reason
  presence will be larger than engaged time, which is the point of keeping the
  two apart.
- **Nothing recorded** does neither. The gap threshold decides alone, exactly as
  if the stream did not exist.

Where the two readings **contradict** each other — knowledgeC writes both an
`isBacklit=1` and an `isBacklit=0` row with identical endpoints at 2026-07-14
09:01:12, and 636 `on` rows sit inside longer `off` rows in one snapshot —
darkness is **not established** and nothing is suppressed. The report says how
much time that covers, so a figure resting on contested readings can be
recognised as one.

### What a dark backlight actually observed, which is a lid

The first state used to do much more than it does. Darkness split a sitting, was
subtracted from it, and dropped any observation anchored inside it — arguing
from a Motion autosave written at 2026-07-11 17:53 with the screen off during a
break: evidence that the application was running, and none at all that a person
was there.

That argument is sound about the autosave and unsound as a rule, because it
generalises from the one case where the mechanism is the whole explanation.
**A video render with the backlight deliberately switched off is unambiguous
work, and it leaves exactly the same trace.** So does a compile, a long export,
and a coding agent running unattended. Reading a dark screen as an absence of
work deletes every one of them.

`knowledgec-backlit` observes a **lid**, and its role name promises more than it
can deliver. Lid state stands in for somebody being at the machine, which stands
in for work being done — a proxy for a proxy — and neither step holds in the
direction that deletes. The three states above are still exactly right; the
mistake was giving the first one a weight it cannot carry.

**Cause is what separates the two cases, and it was built for this.** An
artifact written by a wall-clock timer asserts that an application was running
and nothing whatever about what was happening; joined with a dark reading there
is no evidence of work in either direction, which is true of no other mechanism.
So the timer arm stays and everything else survives — including the **empty**
cause, which means "not an artifact" rather than "nobody said" and which 868,924
live `claude-transcripts` rows carry, and the **recovery** cause, whose
evidentiary weight is documented as unmeasured and would otherwise be settled by
default, in the direction of deleting work.

### What the old rule cost, and two things the figure does not say

On the last derivation taken under it, the rule that was replaced suppressed
**24,704** observations and trimmed **48.6h** — and exactly **one** of those
24,704 was written by a timer. Across three projects it cost **35.8h** of
engaged time. `go run ./scripts/darkcause` recounts that census against the live
store, per cause and per source, computing darkness the way the clustering does
rather than from the raw `off` rows; `timetattle metrics --all` either side of
the change is where the hours came from.

**It is not a rate.** The cost falls across the seven weeks the backlight stream
covers, and it falls unevenly: the weekly deltas run 0.5, 0.3, 2.1, 5.0, 13.4,
8.1, 6.4 hours (`timetattle metrics --all --by week`, differenced). Quoting any
one week as typical misstates it by up to fortyfold.

**And "1 timer observation" is not evidence the old rule was idle**, which is
how it was first read. The store holds **38** timer-caused rows in total —
Motion's autosave is the entire timer population — of which **23** fall after
the backlight stream begins recording on 2026-07-02. Catching one of 23 is near
the arithmetic ceiling, and the one it caught is the 2026-07-11 17:53 autosave
the rule was written for. The old rule hit its design case. The argument for
narrowing it was never that it caught too little; it is that the other 24,703
were not timers, and were never evidence of absence.

The narrowed rule still catches that design case on real data: the single
observation the stored derivation reports as suppressed is that same autosave.

### Engaged time is the headline, and the backlight is not in it

This is why the change is confined to one arm rather than made configurable.
**Engaged time answers "how much did I work on this", and it counts work done
with the screen off exactly as it counts work done with the screen on.** An
overrun on a fixed-price contract is not smaller because the render, the
compile or the agent ran while the lid was shut.

A **hands-on** figure — how much of that time somebody was at the keyboard for —
is a different question with a smaller answer, and the backlight is the right
instrument for *that* one. Nothing here computes it. The point is that darkness
is entitled to affect it and not this.

### Two failure modes that carving created, and that no longer exist

Both are kept here because their counters are still in the schema and still
printed, and a reader has to be able to tell **structurally zero** from
**measured zero**. These two now read structurally zero: there is no mechanism
left that could make them anything else.

**A sitting resting on no observations.** Two dark spans inside one cluster cut
it into three, and the middle piece could sit *between* two observations rather
than containing either. Nothing observed work there and nothing observed
darkness there — emitting it would invent a sitting, and counting it as trimmed
would claim a reading nobody took, so it was dropped and reported. This was not
designed: it was caught by `CHECK (events > 0)` on the first real run, as a
session resting on zero observations at 2026-07-09T23:37:36, and it happened
four times. Pieces were what darkness cut, and there are no pieces now, so
`UnevidencedPieces` can no longer fire at all.

**A point sitting deleted by darkness hours away from it.** A sitting of no
duration — one point observation, no lead-in and no tail-out — used to disappear
entirely if the backlight stream had recorded darkness **anywhere** in the run.
The subtraction dropped every zero-length span whenever it had anything at all
to cut, and nothing counted the loss: no time was trimmed, so `TrimmedByAbsence`
stayed at zero, and no piece was emitted, so `UnevidencedPieces` stayed at zero
too. Silent, and in the direction of deleting real work. It is held by
`TestAPointSittingSurvivesDarknessRecordedElsewhereInTheDay`.

**A cluster is now emitted whole**, and that half of the rule had to move with
the suppression rather than separately. Keeping a non-timer observation while
still carving darkness out of its cluster would retain the evidence and throw
away the only time it could have occupied: the cluster shrinks to nothing, no
sitting is emitted, and nothing counts the loss — a quieter deletion than the
one the change exists to stop, and it would have stranded roughly 24,700 records
on the live store. `TestEverySurvivingObservationLandsInASitting` pins the
property that forbids it: **every observation is either suppressed and counted,
or placed in a sitting.** There is no third outcome.

## Lead-in and tail-out

An isolated observation has no span. Without a lead-in and a tail-out a single
Motion save with no neighbours scores exactly zero, and a day of sparse creative
work disappears.

Measured on the filesystem observations of one creative repository, at a
two-minute gap:

| | Sittings | Presence | Median | Scoring zero |
| --- | --- | --- | --- | --- |
| No margins | 200 | 8.0h | 0s | **108** |
| 5m lead-in, 5m tail-out | 95 | 32.2h | 14m15s | 0 |

One of the 108: `blender/transmission-animation1.blend`, saved at
2026-07-08T01:12:07Z, one observation and nothing near it. With margins it
becomes a ten-minute sitting resting on that one save; without them it is a
sitting of no duration, which is the same as not being there.

**Watch the interaction.** Lead-in plus tail-out at or above the gap makes the
gap meaningless — every pair of clusters closer than the margins merges after
expansion, so the effective threshold becomes gap plus both margins. At the
shipped 5m/5m, a `-gap 2m` sweep really measures 12m, and the apparent spread
collapses from 1.93x to 1.29x. `sessionize` prints a warning when it happens.

## Sittings are global

Clustering runs over every evidence source at once and produces one **disjoint,
ordered** run of wall-clock spans. It is deliberately not per-project.

Engaged time is per-project and may overlap, because concurrent work on two
projects is billable to both. Presence is wall clock and is never
double-counted. A per-project clustering could produce the first and could never
recover the second, because two projects' sittings would each claim the same
hour. These spans are the wall-clock skeleton; per-project engaged time is an
intersection against them.

Overlap cannot leak in through the back door either: expansion by the lead-in
and tail-out is followed by a merge, so two sittings never claim one instant.

## `derived_session`, and why a derived table says so in its name

Everything else in the database is an observation or the provenance of one.
`derived_session` is an *inference*, produced from the `canonical_event` view
and nothing else, and it is thrown away and rebuilt wholesale on every run —
`DELETE` then `INSERT`, inside one transaction, so the table is either entirely
one derivation or entirely the previous one and never a mixture.

The transaction belongs to the store rather than to this derivation:
`sessionize` calls `Store.ReplaceDerived` once, and `sessions.Replace` is the
write it is handed. See [the store's surface](/docs/store#the-surface-and-why-nothing-reaches-around-it)
for the mutation that measures what that buys.

**A rebuild leaves `event` and `harvest_run` byte-identical.** Checked by
fingerprinting both tables around consecutive rebuilds, on the real store: after
two runs over 765,651 events, both fingerprints are unchanged, and so is every
column of `derived_session` and of `derived_session_run` except the latter's
`computed_at`, which is a reading of the wall clock and cannot be reproducible.

### `derived_session_run`: one row for the derivation

The derivation lives on a row of its own, under `CHECK (id = 1)`, and every
sitting references it. Until migration `0016` the parameters were restated on
every session row instead — deliberately, because a session span separated from
its parameters means nothing and nothing should be able to produce one.

The goal is unchanged and the mechanism is stronger. `derived_session.run_id` is
`NOT NULL` and references `derived_session_run` with `ON DELETE CASCADE`, so
with foreign keys on — which `store.Open` sets — a sitting **cannot** be written
before the derivation that produced it exists, and clearing the derivation
clears its sittings. What used to be caught on the way out is now unrepresentable.

Two things it fixed beyond tidiness:

- **A derivation that produced no sittings wrote nothing at all**, and read back
  identically to a store nobody had ever sessionized. `timetattle sessionize
  -from <after the last event>` reaches that from the command line. The run row
  exists whether or not it produced anything, so "the model ran and found none"
  and "nobody has run the model" are now different rows and not the same silence.
- **What the derivation *did* is now stored.** See below.

Measured by removing the cascade: every rebuild after the first fails outright
with `FOREIGN KEY constraint failed`, because `sessionize` clears the derivation
and the sittings would be left pointing at nothing. It fails loudly rather than
orphaning, which is the behaviour worth having.

### What the derivation *did*

`sessions.Cluster` computes thirteen figures about every run — how many
observations it read as evidence, presence and ignored; how many carried no
extent; how many clusters its margins merged; how many sittings observed
darkness split, how much time that removed, how many observations it suppressed;
how much time two readings contested; how much darkness was observed at all.
Before `0016` they were printed by `sessionize` and thrown away.

So the store could not answer **"how much did the darkness rule cost on the last
derivation"**. Answering it took two `timetattle metrics --all` runs with
different flags and a subtraction — which measures a *different* clustering
rather than reading the one that produced the stored table. It is now a `SELECT`
against `derived_session_run`, and the columns reconcile exactly with what
`sessionize` prints.

Every count is `NOT NULL` and `>= 0`, and four cross-column invariants refuse
the impossible readings: an unbounded observation was evidence first, a
suppressed one was evidence first, a spanless presence reading was presence
first, and trimmed time is a subset of the darkness it came out of. Each pair
may legitimately be *equal*; only the contradiction is refused.

`darkness_policy` records what the derivation let observed darkness **do** to
it. It is not configurable and is not a parameter — nothing sets it — but the
rule is going to change, and rows derived either side of that change are not
comparable. Recording the figures without recording which rule produced them
would build exactly the series this project refuses: numbers that moved for a
reason nobody stored. The value set is deliberately not pinned by a `CHECK`,
because the column exists to hold values that do not exist yet.

**One row and not a history.** A history of run rows would say what the darkness
rule cost in *observations and trimmed time*, which is worth having — but not
what it cost in *hours*, because hours are computed from the sittings and the
sittings are replaced wholesale on every run. Answering "what did this change
cost project X" needs both derivations' sittings kept, which is a much larger
change: the span index would have to become per-run and would stop guarding a
rebuild that inserted without deleting, every reader would have to learn which
generation it wanted, and `sessionize -gap 2m` would deposit a whole generation
on every calibration run. The before-and-after question has a cheaper answer:
run `sessionize` and `metrics --all` before a rule changes and keep the output.

### The corpus: what the derivation was drawn *from*

The run row also carries the **corpus** — which observations the derivation was
allowed to see, as against what it did with them:

| column | what it records |
| --- | --- |
| `exclude_patterns` | the `sessions.exclude` patterns applied when the events were read, as a JSON array, sorted and deduplicated. `[]` means this derivation excluded nothing. |
| `corpus_from`, `corpus_to` | the `-from`/`-to` window the events were read over. Empty means unbounded — a value, not a missing entry. |

Both arrived in migration 0010 on `derived_session` and moved to the run row
with the rest of the derivation in `0016`. Both are `NOT NULL`, deliberately:
there is no value that says *nobody recorded what this derivation excluded*.
That state is the absence of the run row, which `Store.Sittings` already reports
as "nothing has been derived".

The exclusion is not a parameter of the clustering — `sessions.Cluster` never
sees it, because `sessions.Retain` has already applied it — so it lives in
`sittings.Corpus` rather than in `sittings.Params`. Every field of `Params` is
consumed by the clustering, and a field it silently ignored would be the first.

It is not a smaller fact for being a different one. Adding four patterns moved
total presence over the live corpus from **1193.2h across 2093 sittings** to
**1067.0h across 1363**, and the median sitting from 10m18s to 22m18s — more
movement than most gap changes. Until 0010 both derivations wrote byte-identical
values into every column of `derived_session`.

Migration 0010 **discards any derivation that predates it** rather than
defaulting it to "excluded nothing", because there is no honest value to write
for rows computed under a list nobody recorded. `0016` discards again, for the
same reason applied to the statistics: those rows recorded what they were given
and never what they did, and the live figures say what defaulting to zero would
cost — the last derivation under the old schema suppressed **24,704
observations** and trimmed **48.6h**. The cost of either discard is one
`timetattle sessionize`.

### Reading it back

`Store.Sittings(ctx, from, to)` returns the sittings overlapping the window
**and the derivation they came out of**: the parameters, the corpus, the
statistics, the darkness policy, and the moment they were computed. The window
is by overlap rather than containment, and either end may be the zero time for
unbounded.

The derivation is read from its **own row** rather than from the windowed
sittings, which is what keeps the three states apart:

- sittings in the window — the model ran and found work there;
- no sittings, but a derivation named — the model ran and found none there;
- a zero `ComputedAt` — nothing has ever been derived, and this is not an empty
  day.

The statistics and the policy describe the **whole run**, so a windowed read
reports the same figures a total one does. A fraction of them for a Tuesday
would be a number nobody could reconcile with anything.

Before `0016` this reconciled the parameters across every row on each read and
refused a table that disagreed with itself. Both are gone: a second derivation
cannot be written, so there is no mixture left to catch. A store below `0016`
is a fourth thing again — not "nothing derived", not "here is the derivation" —
and it is refused by a message naming the migration and `sessionize` as the way
out, rather than by a complaint about a missing table.

A sitting and its parameters are the same types the clustering produces:
`sessions.Session` and `sessions.Params` are `sittings.Sitting` and
`sittings.Params` under another name, defined in a leaf package so that the
store can return them without an import cycle. The round trip is therefore a
comparison and not a translation.

The unique index on `(started_at, ended_at)` guards the rebuild rather than the
clustering. Measured by removing the `DELETE`: the rebuild fails outright on the
first duplicate span and rolls back, leaving the previous table intact. Remove
the index as well and the same run stores **2,256 rows for 1,128 distinct
spans** — every sitting duplicated and every hour doubled, silently.

## The working day, which is not a calendar date

Everything in the store is UTC. A working day is local, is a human convention,
and does not begin at midnight. On 2026-07-11 a sitting ran from 21:33:56 to
01:37:24 the following morning on 550 observations; calling its last hour and a
half Sunday's work would be a clerical fact rather than a true one.

So a working day runs from a configured wall clock, **05:00 by default**, and
is named by the civil date it begins on. The working day `2026-07-11` runs to
05:00 on the 12th.

The default is measured rather than chosen. Across the live corpus of 381
sittings the earliest morning sitting begins at **05:06:24** local, and only
two sittings start before 05:00 — both at 01:39 on a Sunday that was plainly
Saturday's evening. Reproduce with:

```
timetattle sessionize -dry-run -by-day
```

That clearance is only 6m24s, and a sitting's start already includes the
five-minute lead-in, so **a lead-in much above 6m24s would push a real morning
back across the boundary into the day before.** The two parameters interact and
neither is settled.

### A boundary is an instant, not a wall clock

This is the whole of the daylight-saving problem. A day's bounds are two
instants, and consecutive days share an endpoint exactly, so working days tile
the timeline with no gap and no overlap. Every conservation property falls out
of that tiling rather than out of arithmetic on hours: the per-day figures sum
to the ungrouped total exactly, whether a day is 23, 24, 25 or 24.5 hours long.
`-by-day` prints that reconciliation on every run.

Note *which* day a transition lands in. America/Chicago jumps at 02:00, which
is before 05:00, so the 23-hour working day of 2026 is the one named
**2026-03-07** — Saturday 05:00 to Sunday 05:00 — and not the Sunday the
transition is dated to.

A boundary configured to land in a transition hour is decided explicitly rather
than left to `time.Date`, which picks one silently and documents that the
choice is not guaranteed:

- **Repeated hour** (fall-back): the boundary is the *first* occurrence, so the
  day begins as soon as the clock first reads it, and is 25 hours long.
- **Skipped hour** (spring-forward): the boundary is the instant the clock
  jumped *to*, and the day is 23 hours long.

Zones are loaded from the zoneinfo database. A fixed offset is refused in the
config, because an offset cannot say *when* it changes and that is the only
hard part of the problem.

### A sitting crossing the boundary is split

The alternative — booking a crossing sitting whole to the day it began on — is
defensible and is rejected for one reason: it can credit a day with more wall
clock than elapsed inside it. Sittings are the one metric whose defining
property is that wall clock is never double-counted, and a 30-hour sitting
booked entirely to a 24-hour Tuesday breaks that in a way no reader could
detect from the total. Splitting keeps `presence(day) <= length(day)` true by
construction. Conservation holds under either rule.

Because the boundary sits in the trough, the split almost never fires: **zero
of the 381 live sittings cross 05:00.** At midnight, two do.

A split sitting's **margin cannot be apportioned**. A sitting records how much
of it was inferred, not *where* that fell, so which side of a boundary any of
it landed on is not recoverable. It is reported unapportioned and booked once,
against the day the sitting began on, so the column still sums correctly.

### Three states, again

- A working day inside the observed span with no sittings reads **observed, no
  work**.
- A working day nothing was recorded on **does not appear at all**, because a
  zero there would be a claim nobody measured.
- A week the corpus only partly covers is labelled **partial**, and its total
  is not comparable with a whole week's.

## Configuration

One file, `~/.timetattle/config.json`, beside the store and the registry index.
It is JSON because `encoding/json` is in the standard library, and this project
has twice declined a TOML dependency for exactly this file.

```json
{
  "sessions": {
    "gap": "15m",
    "lead_in": "5m",
    "tail_out": "5m",
    "presence_sources": ["knowledgec-backlit"],
    "ignore_sources": ["knowledgec-islocked", "knowledgec-mediausage"],
    "exclude": []
  },
  "calendar": {
    "timezone": "",
    "day_start": "05:00",
    "week_start": "monday"
  }
}
```

Durations are quoted strings in Go's `time.ParseDuration` spelling — `"15m"`,
`"90s"`, `"1h30m"`. A bare `900` is refused, because it could be seconds or
milliseconds and Go's own type would read it as 900 nanoseconds.

`day_start` is a quoted `"HH:MM"`. `timezone` is an IANA name such as
`"America/Chicago"` or `"UTC"`; empty means the machine's own zone, and an
offset like `"-05:00"` is refused. `week_start` is a full weekday name in any
case — a number is refused, because Go counts weeks from Sunday and ISO 8601
from Monday and a `1` would mean a different day to different readers. The
default is `monday`, which is ISO 8601. A week begins at `day_start` on that
weekday, not at midnight.

**A missing file is the defaults. A broken file is an error, and an unknown key
is a broken file.** Those cases are not the same. Nobody has to write a config,
so absence is ordinary; but a file that exists and cannot be understood is
somebody having tried to say something, and ignoring it means every number
afterwards is computed from parameters the file contradicts, with no symptom.
`gap_threshold` is a plausible mis-spelling of `gap`, and skipping it would
leave the most consequential parameter in the system at its default while the
file on disk says 45m.

An empty list is not the same as an absent one: `"ignore_sources": []` ignores
nothing, while omitting the key keeps the default.

**An unknown key can mean the binary is out of date, not that you mis-typed.**
`sessions.exclude` was added after the first release of the config file, so a
config naming it, read by a build that predates it, makes *every* command fail —
including the scheduled harvest. Install the binary first (`go install ./...`
then `scripts/sign.sh`), then write the config. The error says so.

Every flag on `sessionize` overrides the file for one run without changing it,
so a threshold tried on the command line cannot quietly become the configured
one.

## Excluding machine activity

`sessions.exclude` names files that are machine activity rather than work. It is
applied **where the events are read**, not where they are collected: the store
has no delete, so a collect-time rule alone would stop new noise arriving and
move every existing figure by exactly nothing. Excluding at read time makes the
rule reach the whole store retroactively, and deleting a pattern and re-running
`sessionize` puts the time back. Nothing is ever removed from the database.

```json
{
  "sessions": {
    "exclude": ["*.log.bz2", "*.gz", "/Users/you/project/reports/"]
  }
}
```

Two rules, and no dependency beyond the standard library:

| Pattern | Matched against |
| --- | --- |
| No slash — `*.log.bz2`, `*.gz` | The file's **base name**, with `filepath.Match`. |
| A slash — `/Users/you/project/reports/` | The **absolute path**, as a prefix, respecting path boundaries. |

A path pattern must be absolute, because every locator is; a relative one such
as `logs/` is refused where it is written rather than matching nothing for ever.
`…/logs` and `…/logs/` are the same instruction, and `/Users/you/git/Ea` does
**not** match `/Users/you/git/Easley` — a prefix stops at a path boundary.

**The list ships empty, and that is a finding rather than an oversight.**
Whether a `.log` is noise is not something the tool can know. On the machine
this was built on, a project called `Easley` syncs a server's access logs into
its own working tree; 1,025 rotated `.log.bz2` files were being counted as
135.2h of work across 513 days — 11.2% of all attributed time, with 0.0h
corroborated — and excluding them takes that project to 5.7h across 13 days
while leaving every day it has a commit or a real file save on. On somebody
else's machine the same files are a scraper's deliverable. The two rules that
hold everywhere are in code instead and need no configuration: SQLite's `-wal`
and `-shm` sidecars, and
[generated files](/docs/filesystem/#generated-files-already-stored) beneath a
directory such as `.build`, `.venv` or `__pycache__`.

**Every exclusion is counted and itemised**, by rule, on every run of
`sessionize` and `metrics`, and a pattern that matched *nothing* says so:

```
excluded: *.log.bz2, *.gz, /Users/you/project/reports/
1746 observations excluded before clustering and not counted below:
  86 SQLite sidecars, 1025 by "*.log.bz2", 100 by "*.gz", 518 by "/Users/…/reports/"
NOTE: the exclude pattern "*.tmp" matched nothing in this span; it is
configured and is doing nothing
```

A pattern that matched nothing and a pattern nobody wrote are different facts,
and this project has shipped the collapsed version of that distinction three
times.

**No heuristic is applied and none will be.** There is no "a day with fewer than
three observations is not work" and no "a file stamped at exactly 05:00 is
cron", however tempting the live data makes both. Of the project-days in the
store whose only evidence is a file timestamp, a hand census found 85 made
entirely of ordinary files — an `MBCommon.h`, a `README.md`, a design PDF —
which are plausibly a real evening spent editing one document. Weak evidence is
surfaced as weak; it is never dropped silently.

**The exclusion is stored with the derivation**, in `exclude_patterns` — see
[the corpus](#the-corpus-what-the-derivation-was-drawn-from) above. A table
built under one list is therefore no longer indistinguishable from one built
under another, and `timetattle sessionize -dry-run` says which:

```
dry run: the stored sessions were left alone, and they are STALE. They hold
1370 sittings derived at 2026-08-01T21:07:01.902907Z; this run would write 2102.
  excluded: stored *.bz2, *.gz, *.log, /Users/you/git/Easley/reports/; now (none)
  window:   stored the beginning..now; now the beginning..now
  re-run without -dry-run to make the stored derivation this one.
```

Re-run `timetattle sessionize` after editing the list. Until you do, the stored
table is the old answer — but it now says so.

## Flags

| Flag | What it does |
| --- | --- |
| `-store` | Event store to read and write. Default `~/.timetattle/timetattle.db`. |
| `-config` | Configuration to read. Default `~/.timetattle/config.json`. |
| `-gap`, `-lead-in`, `-tail-out` | Override the configured durations. |
| `-presence-sources`, `-ignore-sources` | Override the configured roles, comma-separated. |
| `-exclude` | Override the configured exclusions for one run, comma-separated. Measures a proposed list without writing it. |
| `-from`, `-to` | Bound the events considered. `2026-07-11` or `2026-07-11T09:00:00Z`. A bare date on `-to` means the END of that day, so `-from D -to D` covers the whole of day D. |
| `-sweep` | Report several gaps side by side and write nothing. |
| `-by-day` | Report presence per working day, with the conservation check. |
| `-by-week` | Report presence per week. |
| `-day-start` | Override the boundary for one run, as `05:00`. |
| `-week-start` | Override the week start for one run, as `monday`. |
| `-timezone` | Override the zone for one run, as `America/Chicago`. |
| `-dry-run` | Compute and report, leaving the stored sessions alone, and say whether they are this same answer, are stale, or have never been computed at all. |
| `-quiet` | Print nothing on success. |
