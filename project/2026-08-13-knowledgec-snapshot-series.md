# The knowledgeC snapshot corpus as a time series of the source

2026-08-13. Measured with `scripts/streamseries`, new in this change, against
the 43 preserved `knowledgeC` snapshots spanning 2026-07-28 18:07 to
2026-08-13 12:00 local. All 43 were readable.

Every figure below names the command that reproduces it. Times are local
(CDT, UTC−5) unless marked UTC, because the snapshot file names are stamped in
local time and mixing the two is how a retention figure goes wrong by five
hours.

## Why the corpus is the right instrument

Every other tool in this repository measures what Timetattle **stored**. None
of them can see the source change, because a source can stall, roll or trim
between two harvests and the store only ever sees the result. The snapshots
can: each is a copy of macOS's own database at a known instant, so lining them
up shows the source itself moving.

Nobody had read them that way. The 2026-08-11 `/app/usage` stall was diagnosed
from two of the 43, and two points cannot distinguish a rolling window from a
frozen one.

## Reproduce

```console
$ go run ./scripts/streamseries              # the four streams Timetattle reads
$ go run ./scripts/streamseries -table       # the whole per-snapshot series
$ go run ./scripts/streamseries -days        # the per-day histogram behind the rate
$ go run ./scripts/streamseries -streams /app/usage -table -days
```

The tool never opens the live `knowledgeC` database. It reads snapshots, and
gzipped ones are decompressed to a temporary file that is removed afterwards.

---

## 1. The cap ROLLS, and 10,000 is a FLOOR rather than a ceiling

`go run ./scripts/streamseries -streams /app/usage`

```
ROW COUNT against a nominal cap of 10000
  min 10000, median 10322, max 11871 across 43 readings
  17 readings sat at EXACTLY 10000; 26 sat ABOVE it, the largest 11871 at 2026-08-01 12:00:34.
```

**26 of 43 readings held more than 10,000 rows.** The stream is allowed to grow
past 10,000 and is periodically cut back to *exactly* 10,000, oldest first. The
largest table observed held 11,871 rows.

The roll test is the oldest-row column, and it is unambiguous:

```
ROLL TEST — does the oldest row move forward?
  YES. The oldest row advanced on 12 of 42 consecutive readable pairs,
  from 2026-07-18 16:50:55 to 2026-07-29 09:23:50 — 10.7 days of retained span discarded in total.
```

A frozen table's oldest row does not move. This one moved forward by 10.7 days
of span across the corpus.

### The 10,577 reading is explained, and it was never anomalous

The harvest at `2026-08-11T01:11:15Z` recorded `events_seen 10577`, which the
task filed as the strongest evidence against a hard write-stop and as
unexplained. It is explained completely, and the explanation is that nothing
needed explaining:

- The snapshot `knowledgeC-20260810T200956.db.gz` was taken at 20:09:56 local,
  which is `2026-08-11T01:09:56Z` — **79 seconds before that harvest started**.
- That snapshot holds exactly **10,577** `/app/usage` rows.

The harvest read a table that genuinely held 10,577 rows. It is one of 26 such
readings, not an outlier. A `events_seen` above 10,000 is the ordinary state of
this stream between trims, and any check that treats 10,000 as a ceiling will
be wrong about six readings in ten.

> **This corrects the working phrase "capped near 10,000 rows"** as it appears
> in `internal/knowledgec/knowledgec.go`, `docs/content/docs/harvest.md`,
> `docs/content/docs/app-focus.md` and `docs/content/docs/presence.md`. The
> conclusions drawn from it all stand — the stream is short-lived, it is a
> cross-check rather than the attribution path — but the mechanism is a
> **trim to a 10,000-row floor**, not a ceiling that refuses writes. Those
> pages are corrected in place.

---

## 2. Retention: what a row's life is actually worth

Two figures were conflated here and this document separates them, because the
first one is the trap.

### The retained span is a LOWER bound, not a deadline

```
RETAINED SPAN — age of the oldest row the source still held
  shortest 9.46 days, median 12.24, longest 15.11, across 43 readings
```

This says rows of that age were seen **alive**. It cannot be read as the
eviction deadline, because a quiet stream holds nothing old for the simple
reason that nothing old was ever written, and that looks identical to
aggressive eviction. `/media/nowPlaying` demonstrates the trap: its shortest
retained span is 8.28 days, which means only that nothing had been played.

The `longest 15.11 days` figure is also inflated by the current stall. Nothing
new is arriving, so no trim runs, so the oldest row simply ages in place.

### The eviction deadline is a named row that died

```
EVICTION DEADLINE — rows observed present in one snapshot and gone from the next
  12 evictions observed, 9998 rows discarded in total.
  TIGHTEST: the row starting 2026-07-20 10:15:21 was still present at 2026-07-29 08:13:30
  (aged 8.92 days) and gone by 2026-07-29 21:14:13 (aged 9.46 days).
```

A specific row was observed present at one instant and absent at the next. It
survived to at least **8.92 days** and was dead by **9.46 days**. The
bracket is 13 hours wide because that is how far apart those two snapshots sit.

**This confirms the published "under ten days" rather than correcting it**, and
upgrades it from an estimate to a measurement of a row that actually died.

### The volume caveat, turned into a number

`go run ./scripts/streamseries -streams /app/usage -days`

```
VOLUME — what the cap costs when the stream is busy
  Typical day: 824 rows. Busiest day on record: 2026-07-21, 2798 rows.
  CROSS-CHECK: 10000 rows at 824 a day is 12.14 days, against a median retained
  span of 12.24 days actually observed — a difference of 0.10 days.
  AT THE BUSIEST RATE a 10000-row cap holds only 3.57 days.
```

The cross-check is the load-bearing part. If a **row count** is what evicts,
then cap ÷ typical daily rate must land near the retained span the corpus
actually shows. It lands within **0.10 days** — 12.14 against 12.24. A stream
expiring by age would not track it at all, and the two sibling streams below
show exactly that contrast.

So the answer to "how long does a record survive in `/app/usage`":

| Condition | Deadline |
| --- | --- |
| Directly observed death of a named row | **9.46 days** |
| At the typical rate (824 rows/day) | 12.14 days |
| At the busiest day observed (2,798 rows/day) | **3.57 days** |

`docs/content/docs/harvest.md` already warns that the floor is volume-based
rather than time-based and that "a heavy stretch shortens the real deadline".
That warning was correct and is now quantified: **a sustained week at the
busiest rate on record cuts the deadline from ten days to about 3.6 days.** A
four-hourly harvest beats all three figures comfortably; nothing here argues
for a change of cadence, only for retiring "ten days" as a planning margin.

The 2,798-row peak is a **lower bound** on the busiest real day: days at the
edge of a snapshot's retained span are partial. 2026-07-21 is not one of them —
it sits wholly inside the 2026-07-28 snapshots' span — but a busier day before
2026-07-18 would be invisible to this corpus entirely.

---

## 3. The stream has stalled before, on working days, and the record already said so

```
FREEZES — runs of snapshots whose newest row did not move
  frozen at            first seen           last seen            witnessed  snapshots  vs p99  vs longest
  2026-07-27 14:28:03  2026-07-28 18:07:18  2026-07-29 08:13:30  14h6m0s    5          61x     0.33x
  2026-08-03 18:36:35  2026-08-03 18:54:51  2026-08-03 18:58:27  4m0s       2          0x      0.00x
  2026-08-10 19:28:31  2026-08-10 20:09:56  2026-08-13 12:00:00  63h50m0s   10         276x    1.49x
```

The middle row is four minutes and is ordinary — it is inside p99. The other
two are the same fault, twice.

> **This contradicts a note recorded on task 9ARWV on 2026-08-13**, which
> described the stream's all-time maximum gap of 42.93h — 2026-07-27T19:28:03Z
> to 2026-07-29T14:23:50Z — as "**a weekend**", and as "the shape a genuinely
> idle stream makes", in the course of arguing that the current stall is
> unprecedented.
>
> **2026-07-27 was a Monday and 2026-07-29 was a Wednesday.** That gap ran
> Monday 14:28 to Wednesday 09:23 local, straight through two working days. It
> was not a weekend and it was not idleness. It was the same fault as the
> current one, and the repository already knew: `internal/knowledgec/knowledgec.go`,
> `docs/content/docs/presence.md` and `docs/content/docs/app-focus.md` have all
> recorded that stall since 2026-07-30, one of them in the exact words "stalled
> for a full day: last row before the gap 2026-07-27 14:28".
>
> The consequence is that **the current stall is the second occurrence of a
> recurring fault, not a first.** Nothing else in the note changes: the cap
> still rolls, the perishability model still stands, and the note's central
> correction — that the freeze-at-cap hypothesis is dead — is right.

The control rules out an idle machine on both occasions.
`go run ./scripts/streamseries -streams /display/isBacklit -days` gives 29
backlight rows on 2026-07-28 against a period median near 50, while
`/app/usage` wrote **no row at all** on that date — 2026-07-28 is simply
absent from its day histogram. The screen was on and the stream was silent.

### Both stalls began within minutes of a trim

| | previous reading | reading at the freeze | oldest row jumped to |
| --- | --- | --- | --- |
| 2026-08-10 | 10,577 rows | **exactly 10,000** | 2026-07-29 09:23:50 |
| 2026-07-27 | (no snapshot exists) | **exactly 10,000** | — |

Both freezes are pinned at *exactly* the post-trim floor. For the August one
the trim is witnessed directly: 10,577 rows at 20:09 on 08-10, exactly 10,000
by 08:28 on 08-11, and the last row written was 19:28 on 08-10. For the July
one the corpus opens mid-stall, so only the 10,000 is observed and the trim
before it is not.

**Two out of two is suggestive and not a finding.** With one witnessed trim and
one inferred, this is a hypothesis worth the reboot experiment, not a
conclusion. It is recorded so the next occurrence has something to test against:
*if a stall again begins with the table at exactly 10,000 immediately after a
trim, the trim is implicated.*

---

## 4. Cadence, measured from the source rather than from the store

```
CADENCE — how this stream is normally spaced
  19523 distinct instants, 19522 gaps between them
  median 8s, p95 3m5s, p99 13m54s, longest 42h56m0s
  gaps over 1h: 45, over 12h: 2, over 24h: 1
```

This reconciles with the store-side measurement recorded on task 9ARWV
(median ~7s, p95 3m, p99 12m, 2 gaps over 12h, max 42.93h). The maxima agree
exactly: 42h56m0s against 42.93h is the same 42h55m47s gap.

The instant counts differ, and the difference is fully accounted for:

| | distinct `/app/usage` instants |
| --- | --- |
| Snapshot corpus | 19,523 |
| Store (`knowledgec-appusage`) | 18,730 |
| Difference | **793** |

```console
$ sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
    "select count(distinct started_at), min(started_at), max(started_at)
     from event where source_id='knowledgec-appusage';"
18730|2026-07-20T15:16:42.000000Z|2026-08-11T00:28:31.000000Z
```

The corpus holds exactly **793** distinct instants earlier than
`2026-07-20T15:16:42Z`, which is the store's oldest. The two agree on every
instant from there onward, including the newest. Those 793 are rows macOS
evicted before Timetattle's store existed, preserved only because the
2026-07-28 and 2026-07-29 snapshots were taken. **The snapshot corpus is a
strict superset of the store for this stream**, which is the concrete return on
keeping the snapshots.

> A caution on the earlier store-side query, which compared timestamps as text:
> `started_at < '2026-07-20T15:16:42Z'` returns a row, because
> `'2026-07-20T15:16:42.000000Z'` sorts before `'2026-07-20T15:16:42Z'` — `.`
> precedes `Z`. Compared as time (`julianday(...)`) the count is 0. Any store
> query in this repository that bounds `started_at` with a literal lacking
> `.000000` is comparing text and may be off by one row.

### The longest gap is itself a stall, so it must not be the yardstick

`vs longest` in the freeze table above says the current stall is only 1.49×
the longest gap on record. That number is nearly meaningless, because the
longest gap on record **is the July stall**. It is a fault divided by a fault.
Against p99 — the tail of ordinary behaviour — the current stall is **276×**
and the July one was **61×**.

The tool prints both columns for exactly this reason, and says in its own
output which one to read. This is the same hazard as the clipped window in
`scripts/sleepcheck`: a comparison whose denominator is contaminated reads as
reassuring precisely when it should not.

---

## 5. The sibling streams, and the two different expiry mechanisms

`go run ./scripts/streamseries`

| Stream | rows (min–max) | retained span (shortest / median) | what evicts |
| --- | --- | --- | --- |
| `/app/usage` | 10,000–11,871 | 9.46 d / 12.24 d | **row count** |
| `/display/isBacklit` | 1,238–1,670 | 27.86 d / 28.39 d | **age, ~28 d** |
| `/device/isLocked` | 1,244–1,397 | 27.51 d / 28.38 d | **age, ~28 d** |
| `/media/nowPlaying` | 26–105 | 8.28 d / 24.43 d | age; too quiet to bind |

Both sibling streams roll — their oldest rows advanced on 15 of 42 pairs — and
both sit at a retained span of ~28 days with a spread of well under two days
across the whole corpus. That is an **age** limit, and it confirms the "~28
days" already in `docs/content/docs/harvest.md`.

`/app/usage` sits at 9–15 days, nowhere near 28, so for that stream the count
cap binds first. The two mechanisms are independent and whichever is tighter
wins. That is why `/app/usage` is the only stream whose deadline moves with how
hard the machine is worked.

`/media/nowPlaying`'s 8.28-day figure is the trap named in §2 and not a
retention window: the stream is nearly always idle, with freezes of 129h and
207h witnessed, and those are "nothing was playing" rather than "it stopped
working". Distinguishing those two is not possible from row counts and this
document does not claim to.

---

## 6. What this does NOT establish

- **Whether the stream is still frozen, and what clears it.** Out of scope
  here by design; it needs the reboot experiment. The record shows the last
  harvest to date, `2026-08-13T17:01:44Z`, still reporting `events_seen 10000,
  events_new 0`, so nothing had cleared it as of that run.
- **Why the writes stopped.** Nothing here is a cause. The trim correlation in
  §3 is a hypothesis with two data points, one of them inferred.
- **The source's true worst case.** The corpus begins 2026-07-28 because that
  is when snapshots began, not when the source began. A longer freeze or a
  shorter retention window before that date is simply not in evidence. Every
  extreme quoted above is an extreme *within these 43 files* and the tool says
  so in its own output.
- **Short stalls.** Snapshots sit from 11 seconds to two days apart. A stall
  shorter than the interval between two of them leaves no trace. "Two stalls
  witnessed" is not "two stalls occurred".
