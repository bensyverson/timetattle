# The harvest gap figures did not reproduce from their own method

2026-08-12. Measured against the live store (`~/.timetattle/timetattle.db`,
685 MB) with `scripts/harvestgap`, which was written for this document because
the figures it corrects were produced by SQL typed at a prompt and recorded only
as prose. Every figure below names the command that reproduces it.

The store is still being written to, so every figure here names an `-until`
bound and the row count that bound selects, rather than "the store today". A
number that stops reproducing tomorrow is the failure this document is about.

Nothing here changes the conclusion the figures were used for: the harvester's
worst outage was over two days long against a nominal twelve-hour timer, and
that is not jitter. What changes is that the published numbers could not be
re-derived from the method printed beside them, that the method was wrong in a
way that made the worst case look slightly better than it was, and that the
count and the mean were both artifacts.

## 1. The published figures come from a ten-minute bucket, not a minute

The measurement was recorded as "min(started_at) per harvest_run batch grouped
to the minute, with lag() over started_at", giving 35 gaps, a mean of 8.49h and
a max of 54.64h. Grouping to the minute does not give that. Over the same
population — the 1,753 rows started before `2026-08-12T03:00:00Z` — truncating
`started_at` to each of these gives:

| grouping | gaps | mean, start to start | max, start to start |
|---|---|---|---|
| hour | 23 | 12.9182h | 54.7004h |
| **ten minutes** | **35** | **8.4899h** | **54.6438h** |
| minute | 94 | 3.1614h | 54.6438h |
| ten seconds | 94 | 3.1614h | 54.6438h |

Every published figure reproduces under the ten-minute bucket and only that one,
including the thresholds (13 over 12h, 6 over 17h, 2 over 24h) and the endpoints
of the worst gap. In SQL that grouping is `substr(started_at,1,15)` — a minute
truncation written one character short, which is the likeliest thing to have
happened at a prompt. Anyone re-running the method as described gets 94 gaps and
a mean of 3.16h and has no way to tell which of the two of them is confused.

```
go run ./scripts/harvestgap -grid 10m -until 2026-08-12T03:00:00Z
go run ./scripts/harvestgap -grid 1m  -until 2026-08-12T03:00:00Z
```

## 2. A clock bucket is the wrong unit, in both directions at once

`harvest_run` holds one row per (invocation, source) and nothing in the schema
says which invocation a row belongs to, so a clock bucket is the obvious
stand-in for the missing column. It fails twice over, and both failures are
present in this store.

**It splits an invocation that straddles a boundary.** The invocation of
2026-08-07 ran from 19:06:58.9167 to 19:10:38.1788 and the ten-minute grid cut
it at 19:10. That is why the worst outage was recorded as 54.6438h beginning
19:10:22 when it is really 54.7004h beginning 19:06:58: **the grid understated
the worst gap in the record by 3m40s**, and it did so by measuring from the
middle of a harvest rather than from its start. Nine such splits exist in the
population.

**It merges invocations that are genuinely distinct.** Four separate invocations
on 2026-08-01 (13:04:35, 13:05:14, 13:07:12, 13:08:01) fall inside bucket 13:0,
and two on 2026-08-03 (23:54:52 and 23:58:27, the second beginning 73 seconds
after the first finished) fall inside 23:5. Four such merges exist.

The arithmetic closes exactly, which is how we know that is the whole of the
difference:

    36 clock batches = 31 invocations + 9 splits − 4 merges
    35 gaps          = 30 real gaps   + 9 intervals inside one invocation − 4 dissolved

Nine of the thirty-five "gaps" were never gaps. They are stretches during which
the harvester was running.

## 3. What actually identifies an invocation

No approximation is needed. Collect runs its sources in sequence in one process,
so within an invocation each row's `started_at` equals the previous row's
`finished_at` to the microsecond. Over the whole store the deltas between
consecutive rows are sharply bimodal:

| | delta |
|---|---|
| widest delta inside an invocation | 149.395ms |
| narrowest delta between two invocations | 17.136911s |

An invocation is therefore the maximal chain of rows joined by that continuity,
and the `-join` threshold that defines "continuous" sits in an empty band 115x
wide. `harvestgap` prints both edges of that band on every run, so if a future
change to the harvester closes the band — parallel sources would do it — the
tool says so instead of quietly inventing an answer. This is the same discipline
as naming the population: a threshold that lands in the middle of the data is a
verdict written by the analyst, not a measurement.

## 4. The corrected figures

Same population as the original (1,753 rows, started before
`2026-08-12T03:00:00Z`):

| | as published | corrected |
|---|---|---|
| gaps | 35 | 30 |
| mean, start to start | 8.4899h | 9.9040h |
| max, start to start | 54.6438h | **54.7004h** |
| over 12h / 17h / 24h | 13 / 6 / 2 | 13 / 6 / 2 |

And the full record as it stood when this was written (1,826 rows, everything up
to `2026-08-12T21:11:00Z`; the manual harvest of 21:08 that prompted this work is
included, which is why there is one more gap than in the column above):

| measure | mean | median | max | over 12h | over 17h | over 24h |
|---|---|---|---|---|---|---|
| exposure | 10.1558h | 9.1046h | 54.6395h | 14 | 7 | 2 |
| cadence | 10.1970h | 9.1192h | 54.7004h | 14 | 7 | 2 |

31 gaps between 32 invocations, spanning 2026-07-30T17:01:54Z to
2026-08-12T21:10:03Z.

```
go run ./scripts/harvestgap -until 2026-08-12T03:00:00Z    # the corrected column above
go run ./scripts/harvestgap -until 2026-08-12T21:11:00Z    # the full record at time of writing
go run ./scripts/harvestgap                                # unwindowed; grows with every harvest
```

The `-until` bounds are there so these figures stay reproducible after the next
harvest lands. The tool does **not** window by default, and when it is windowed
it measures the excluded population as well and names the widest gap the window
leaves out. The reason is on the record: `scripts/sleepcheck` once clipped to the
span two sources shared, reported "1.88h of gaps, largest 1h27m", and had
silently dropped a 6h23m gap that ended 26 seconds before its window opened.

`go run ./scripts/harvestgap -since 2026-08-10T00:00:00Z` is the demonstration.
It is a window that would conceal exactly this record's worst case, and it says
so instead:

```
HIDDEN BY THE WINDOW  measured, not assumed
  27 gap(s) fall outside the window and are in NO figure above
  the widest of them is 54.6395h exposure, 2026-08-07T19:10:38 .. 2026-08-10T01:49:00
  quote the windowed figures only alongside this line
```

### Exposure and cadence are different questions

**Exposure** is the previous invocation's `finished_at` to the next one's
`started_at`: how long nothing was being read, which is what the retention floors
bite on. **Cadence** is start to start: what a periodic timer controls, and the
one to compare against a `StartInterval`. They differ by the invocation's own
duration, which ranges from 0.9s to 6m05s here. That is small, but it is enough
to move a gap across a threshold — and a figure published without saying which of
the two it is leaves the next reader a coin flip. Both are printed on every run.

## 5. What a gap is not

**A gap is an absence, and absence is not an observation.** `harvest_run` records
runs that reached the point of writing a row. A 54.70h gap is therefore equally
consistent with the timer never firing, with the machine being asleep or shut,
and with a process that started and died before its first write. Nothing in the
store separates those, so "14 gaps over 12h" must never be restated as "14 missed
runs".

**A failed source is the opposite case, and is fully observed.** The harvester
ran, the row exists, and the reason is recorded: six failed rows (five on
2026-08-01, one on 2026-08-03), all of them a source that could not be read
rather than a harvest that did not happen.

**A row still marked running is a third thing.** One exists, from
2026-08-01T13:07:13, which started and never reported either way.

Absence, failure and non-report are three separate counts in the output and are
never added together. Collapsing any two of them into "the schedule was fine" is
a bug this repository has now shipped three times.

**Nothing records what invoked a run.** A scheduled harvest and one run by hand
are indistinguishable in `harvest_run`, and 2026-08-01 alone holds thirteen of
the thirty-two invocations in the record, several of them minutes apart, which
are plainly development runs. Two fifths of the population is one day at a
keyboard, and it drags the mean down. A mean gap *below* the nominal interval
reads as "it runs more often than promised" when what it really says is
"somebody was at the keyboard". The mean is the least informative figure in the
set; the max and the over-threshold counts carry the finding.

## 6. Latency for two sources, permanent loss for the others

The 2026-08-12 gap is the clean case. The store had nothing stamped that day and
the dashboard said so; a harvest run by hand at 21:08 backfilled it. That was
**latency, not loss**, and the store can prove it: an event's `first_seen_run`
never changes, so evidence dated inside a gap that no run had seen until after
the gap ended is evidence that survived the gap.

| gap | ended | evidence dated inside it, first seen only afterwards |
|---|---|---|
| 18.9286h | 2026-08-12T21:08:22Z | 9,008 events — 7,746 transcripts, 750 Biome, 65 knowledgeC, 165 mtime, 282 task and git |
| 54.6395h | 2026-08-10T01:49:00Z | 2,225 events — 1,660 transcripts, 96 Biome, 349 knowledgeC, 28 mtime, 92 task and git |
| 33.1399h | 2026-08-01T02:10:20Z | 37,278 events |

```
go run ./scripts/harvestgap -recovered -top 10 -until 2026-08-12T21:11:00Z
```

**This asymmetry is the whole point of the section, and it is easy to misread.**
A large count proves the evidence survived. A small count proves *nothing at
all*, because evidence that expired before it was harvested leaves nothing to
count. The tool prints that sentence next to the numbers for the same reason it
is repeated here.

For the mtime sources it is worse than unmeasurable, it is known to be lossy.
`internal/filedays` documents the mechanism and the measurement: a file records
only its **last** modification, so work done today on a file last touched on
11 July does not add evidence for 11 July, it *moves* it — one scan of a
repository found 1,773 files modified on 2026-07-11 and the same scan hours later
found 513. Both numbers were true when taken. Evidence not harvested before a
file is touched again is gone, and no later harvest recovers it. The 165 and 28
mtime events in the table above are the files that happened *not* to be touched
again; the ones that were touched again are invisible here by construction, which
is exactly why they cannot be counted.

So: for Biome, knowledgeC and transcripts a gap costs freshness. For the mtime
sources a gap costs evidence outright, and the bill is unquantifiable after the
fact.

### The floors are volume-based, not time-based

Both perishable sources expire by **volume, not by age**. knowledgeC's
`/app/usage` is capped near 10,000 rows (`internal/knowledgec/knowledgec.go`) and
Biome fills fixed 512 KB segments (`internal/biome/segb.go`,
`internal/snapshot/biome.go`). The familiar "under ten days" for knowledgeC is a
figure for typical activity; a heavy stretch shortens the real deadline, and
nothing here or anywhere else in this repository has yet measured segment roll
rate against activity.

> **Corrected in part, 2026-08-13.** Two claims in the paragraph above need
> adjusting, and the third is now measured. Source:
> [`2026-08-13-knowledgec-snapshot-series.md`](2026-08-13-knowledgec-snapshot-series.md),
> `go run ./scripts/streamseries -days`.
>
> 1. **"Both perishable sources expire by volume" is too broad for knowledgeC.**
>    Only `/app/usage` does. `/display/isBacklit` and `/device/isLocked` expire
>    by **age** at ~28 days — shortest retained spans of 27.86 and 27.51 days,
>    medians of 28.39 and 28.38 across 43 snapshots, with no relationship to
>    load. Both mechanisms exist and the tighter one wins; for `/app/usage` that
>    is the count, for the rest it is the age.
>
> 2. **"Capped near 10,000 rows" describes a floor, not a ceiling.** 26 of the
>    43 snapshots held more than 10,000 rows, the largest 11,871. macOS lets the
>    table grow and trims it back to exactly 10,000 oldest-first.
>
> 3. **The knowledgeC half of "nobody has measured it" is now measured, and the
>    caveat was right.** A named row was watched die between 8.92 and 9.46 days.
>    At the typical 824 rows a day the cap holds 12.14 days, against a 12.24-day
>    median retained span observed — agreement to 0.10 days, which is itself the
>    evidence that the count is what evicts. At the busiest day on record, 2,798
>    rows, it holds **3.57 days**. So "under ten days" stands as typical, and the
>    real deadline under load is roughly a third of it.
>
> Biome's segment roll rate against activity is still unmeasured. Nothing in
> this correction changes any harvest-cadence conclusion in this document: the
> installed four-hourly snapshot beats 3.57 days by a wide margin. What it
> retires is the use of "ten days" as a **margin** rather than as a typical
> figure.

That has a direct consequence for how the figures above may be used. **"No loss
yet" is an observation about the days measured. It is not a margin, and it must
not be spent as one.** A 54.70h outage survived by Biome in early August says
nothing about a 54.70h outage during a week of heavy use.

## 7. Open, and deliberately not settled here

- `knowledgec-appusage` contributed 107 events dated 2026-08-11 and **zero**
  dated 2026-08-12, against 557 to 914 a day on the working days before, while
  Biome kept producing normally (862 events dated 2026-08-12). The stream is known to stall and resume on its
  own (`internal/knowledgec/knowledgec.go` records one such stall in July), so
  this is *an observation, not a diagnosis* — but it is a different question from
  harvest cadence, and no figure in this document depends on it. It is also the
  one figure here that no tool reproduces, which is the fault this document
  exists to correct; it is written out in full rather than described, and it
  keeps moving as harvests land:

  ```sql
  SELECT substr(started_at,1,10) AS day,
         SUM(source_id='knowledgec-appusage'), SUM(source_id='biome-appusage')
  FROM event WHERE started_at >= '2026-08-05' GROUP BY day ORDER BY day;
  ```

  > **Settled 2026-08-13, and a tool now reproduces it.** The observation was
  > right and the absence is in the SOURCE, not in ingestion: macOS wrote its
  > last `/app/usage` row at 2026-08-10 19:28:31 local and ten consecutive
  > snapshots over the next 63h50m held the identical newest timestamp while
  > `/display/isBacklit` recorded normally. `go run ./scripts/streamseries
  > -streams /app/usage` reports it, and `timetattle status` now flags it
  > unprompted.
  >
  > One correction to the sentence above: this is the **second** such stall, not
  > a repeat of a single July one. The July stall ran 2026-07-27 14:28 to
  > 2026-07-29 09:23 local — a **Monday to a Wednesday**, through two working
  > days, not a quiet weekend as it was described elsewhere on 2026-08-13.
  > `/app/usage` wrote no row at all on 2026-07-28 while the backlight wrote 29.
- `~/.timetattle/harvest.log` and `harvest.err` are a second, independent record
  of when the harvester ran, and could separate "launchd never fired" from "the
  process started and died" — the one distinction §5 says the store cannot make.
  Neither file was read for this document. Reconciling them against
  `harvest_run` is worth doing and is not done.
- The tool's `-interval` is an argument, not something read from launchd, so it
  can go stale against the installed plist without the output changing. The
  output says so where the count is printed.

## 8. Reproducing everything above

```
go run ./scripts/harvestgap                                        # default: unwindowed, by invocation
go run ./scripts/harvestgap -until 2026-08-12T21:11:00Z            # §4, full record at time of writing
go run ./scripts/harvestgap -until 2026-08-12T03:00:00Z            # §4, corrected, original population
go run ./scripts/harvestgap -grid 10m -until 2026-08-12T03:00:00Z  # §1, the published figures
go run ./scripts/harvestgap -grid 1m  -until 2026-08-12T03:00:00Z  # §1, the method as described
go run ./scripts/harvestgap -grid 1h  -until 2026-08-12T03:00:00Z  # §1
go run ./scripts/harvestgap -recovered -top 10 -until 2026-08-12T21:11:00Z   # §6
```

Reconciled independently: every figure in §1, §3 and §4 was first derived in SQL
against the same store and then re-derived by the Go tool, which shares no code
with the queries. They agree to the printed precision.

One caution for whoever runs these next. The tool's output is safe to read and is
**not** safe to paste into anything that ships: the FAILURES section prints error
text straight out of the store, and that text carries absolute filesystem paths.
