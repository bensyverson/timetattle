---
title: "Presence from knowledgeC"
weight: 35
description: "Reading the backlight, the lock and application usage out of a preserved snapshot, and the four states a day is actually in."
---

# Presence from knowledgeC

`knowledgeC` is the SQLite database behind macOS's own usage reporting, and it
is the only source Timetattle has that **directly observes an interval**.
Everything else yields instants: a commit happens at a moment, a file is saved
at a moment. The backlight going on and off is a span, observed as a span,
which is why it is the denominator for everything else and the reason
[harvesting](/docs/harvest/) cannot wait.

`internal/knowledgec` reads it. Four streams, four sources, one snapshot file:

| Source | Stream | What it is |
| --- | --- | --- |
| `knowledgec-backlit` | `/display/isBacklit` | Presence. `state` is `on` or `off` |
| `knowledgec-islocked` | `/device/isLocked` | Corroboration. `state` is `locked` or `unlocked` |
| `knowledgec-appusage` | `/app/usage` | Cross-check only. **Not** the attribution path |
| `knowledgec-mediausage` | `/app/mediaUsage` | Unevaluated |

Each gets its own `harvest_run` row, so a stream that goes quiet is visible as
its own run reporting nothing rather than disappearing into a total.

## It reads a snapshot, never the live database

`knowledgeC` prunes itself, so `timetattle harvest` copies it, strips it to the
`ZOBJECT` table and gzips it. This package reads *those files* — either a
`.db.gz` as a harvest writes it, or a plain `.db` somebody has already
decompressed.

That is a deliberate boundary rather than a convenience. Reading the live
database would mean a second Full Disk Access path to get wrong, and would tie
what gets collected to whatever macOS had not yet deleted at the instant the
collector ran. A snapshot is a fixed thing: re-read it as often as you like and
it gives the same answer.

There is consequently **no default path and no self-registration**.
`internal/biome` points at one directory macOS maintains, so it can join the
registry at `init`; a harvest writes a *new* snapshot every time it runs, so
which file to read is a decision the caller makes on the day.

```go
path, err := knowledgec.NewestSnapshot(dir) // dir is ~/.timetattle/snapshots
events, err := knowledgec.NewBacklight(path).Collect(ctx, since)
```

## The backlight has four states, and the stream carries two

A snapshot holds rows asserting `isBacklit=1` and rows asserting `isBacklit=0`.
What neither covers is **unobserved** — a span the sampler said nothing about,
which is an absence of evidence and not evidence of absence. A fourth, **beyond
the snapshot's reach**, is not in the stream at all; it is a property of when
the harvest ran.

```
observed ON + observed OFF + unobserved + beyond the snapshot = the day
```

Storing both readings as rows is what keeps the other two states available:
unobserved is the **absence of a row**, so it needs no representation and cannot
be collapsed by accident. Conflating it with darkness deletes real work. On
2026-07-14 the stream emits nothing of either value between 12:35:20 and
14:02:24 while Biome records dense application switching throughout; read as
darkness, that is 1h27m of confirmed work gone. Every query written before this
was understood collapsed it by selecting `WHERE value = 1`.

## What the collector does not do

It does not merge, deduplicate, resolve or tidy. Rows are passed through as
found, and three properties of real data are why.

**Exact duplicates exist.** Three identical `off` rows sit at 2026-07-10
15:22:32. They carry nothing the first does not, and the store's natural key
collapses them — which is why `events_new` is legitimately below `events_seen`
even on a fresh database. On the 2026-07-29 snapshot: 1,563 backlight events in,
1,556 rows.

**Contradictory rows exist.** At 2026-07-14 09:01:12 there is both an
`isBacklit=1` and an `isBacklit=0` row with identical endpoints. Those are two
observations, which is exactly why `state` is part of the store's key. Which one
wins where they overlap is a **read-time** decision, made against the whole
picture; settling it in the collector would bake one answer into the archive
where nobody could revisit it. It moves observed presence by 4.4% overall, and
by 37% of a single measured day.

**Rows nest.** 636 `on` rows sit inside longer `off` rows in the 2026-07-29
08:13 snapshot, and the reverse nesting happens too, more rarely. How much time
the two readings both claim is reported per day as `CONTESTED` by
`go run ./scripts/joinday`. Merging is arithmetic for a reader that knows what
question is being asked, and a collector does not.

## Every event is an interval, including the ones with no duration

`knowledgeC` reports a start and an end for every row in these streams, so the
shape is `interval` even where the two are equal — and 106 of 1,563 presence
rows in the 2026-07-29 snapshot are.

This is the invariant a collector author must not get wrong. A zero-length row
satisfies *both* the `point` and the `interval` CHECK, and `shape` is part of an
event's identity, so a collector branching on measured duration would file one
observation twice, once under each shape, and deduplication could not tell they
were the same thing. See [the event store](/docs/store/).

That is priced rather than asserted. Committing the mistake against the real
snapshot — writing the collector's events, then the points a duration-branching
collector would have produced from the same rows — stores **583 of 14,404
observations twice**, 99 backlight, 308 `/app/usage` and 176 `/app/mediaUsage`.
Reproduce with:

```console
$ go test ./internal/store/ -run ShapeChosenByDuration -v
```

`locator` is always empty: these events are not about a file. The snapshot they
came from is recorded once, on the harvest run.

## Never tail it

`Collect(ctx, since)` returns every row whose end is not before `since`, and
`since` of the zero time returns everything. Nothing consults when a row
*arrived*, and no column would say.

That is the whole mechanism for late arrival, and it is not theoretical:
`/app/usage` was measured **23 hours** behind `/display/isBacklit`, with row
creation times equal to event times, so rows appear well after the work they
describe. A harvest that advanced a watermark to the newest event it had seen
would step over everything that arrived afterwards, permanently and without a
symptom. Re-read a generous overlapping window instead; deduplication makes it
free.

Proved rather than asserted, in `internal/store/knowledgec_realdata_test.go`:
re-reading the newest real snapshot inserts **zero** rows on the second pass,
and a row present only in a later snapshot is still captured when the window
reaches back past it.

## Timestamps

Core Data counts seconds from 2001-01-01, 978307200 after the Unix epoch.
Reading one against the wrong epoch lands three decades away, which is at least
loud.

Instants come back in **local** time, matching the other readers of local
sources. `internal/store` normalises to UTC on write; a reader that
pre-normalised would leave every printed time and every day bucket in its
callers silently shifted by the offset. Every row in a real snapshot carries a
whole number of seconds — checked across all 14,404 rows of the four streams —
but the conversion keeps microseconds anyway, so a fractional value appearing
later would move an event by nothing rather than by up to a second.

## `/device/isLocked`: the polarity is an inference

`1` is read as *locked* from the field's own name and nothing else. The
supporting evidence is indirect and weaker than one number makes it look. Of the
674 rows asserting `isLocked=1` in `knowledgeC-20260729T211413.db.gz`:

| Where the row begins | Rows |
| --- | --- |
| inside a row asserting `isBacklit=0` | 487 (72%) |
| inside a row asserting `isBacklit=1` | 268 (40%) |
| where the backlight sampler said nothing | 2 |

Those do not sum to 674 because backlight rows nest and contradict each other,
so one instant can be covered by rows of both values — which is why the second
line is not the complement of the first, and why "72% correlated" flatters it.
It is consistent with `1=locked`; it does not establish it.

So the stream is safe to **store** and not yet safe to draw presence **from**.
Settling it needs an experiment — lock the machine at a known instant and read
the row back — rather than another correlation.

## `/app/usage` is a cross-check, not attribution

[Biome](/docs/app-focus/) is the attribution path. `/app/usage` is capped near
10,000 rows, under ten days, and it **stalled for a full day**: last row before
the gap 2026-07-27 14:28, nothing on 07-28, 806 rows on 07-29 when it resumed on
its own. It is collected because it is cheap, and because cross-checking two
independent readings of one fact is how the bare-stop analysis was validated at
all.

`/app/mediaUsage` is collected on the same terms and with less to show for it:
1,495 rows across eight bundle identifiers, 88% of them Safari. That is
consistent with video playback in a browser and equally consistent with several
other things. Nothing should be inferred from it until somebody measures what it
tracks.
