---
title: "Application focus from Biome"
weight: 30
description: "Where the focus intervals come from, what they mean, and the two places the raw data lies."
---

# Application focus from Biome

Biome is the undocumented store behind Screen Time. Its
`ScreenTime.AppUsage` stream records, to the millisecond, every time an
application became frontmost or gave that position up. Timetattle reads it
directly, and it is the deepest and finest-grained evidence available on a
Mac.

```
~/Library/Biome/streams/restricted/ScreenTime.AppUsage/local/
```

Nothing needs configuring. The source registers itself; a harvest collects
from it along with everything else.

## Why this rather than knowledgeC

`knowledgeC` reported the same focus sessions and was easier to read, being
SQLite. It is **not dependable**: its `/app/usage` table wrote its last row
before a gap at 2026-07-27 14:28, produced nothing at all on 07-28 while
healthy neighbouring streams kept logging, and then resumed on its own on
07-29 with 806 rows. A source that goes silent for a whole day cannot be the
one that says which application held the screen.

That is a correction of what this page said until 2026-07-30, which was that
the stream had stopped permanently. It had not. The conclusion is unchanged
and the reasoning is now the honest one: Biome wins on continuity and depth,
not because the alternative is dead. `/app/usage` is still collected, as
`knowledgec-appusage`, for cross-checking — see
[Presence from knowledgeC](/docs/presence/).

It is also better on its own merits. `/app/usage` was capped near 10,000 rows,
under ten days; the Biome stream on the machine this was built against reached
back 24 unbroken days at millisecond resolution — 47,029 records from
2026-07-05 19:55 onward.

`knowledgeC` is still the only source that observes *presence* — that you were
at the machine when no file changed — which is why
[harvesting](/docs/harvest/) it remains the thing that cannot wait.

## Biome is perishable too

macOS fills fixed 512 KB segments in place, rolls to a new one when full, and
eventually discards old ones. `timetattle harvest` captures the segments
alongside the `knowledgeC` snapshot, storing each distinct set of contents
once. The reasoning behind the change detector — and why file size and
modification time both fail silently here — is in
[Harvesting presence data](/docs/harvest/).

It does not wait for a whole segment to age out. A background process called
`BiomeAgent` deletes individual records once they pass **28 days**, editing the
segment in place: the record's bytes are zeroed, and its index entry survives
marked as deleted. Timetattle reads past those, counts them, and keeps every
record after them — but it cannot recover their contents, so whatever a harvest
stored before the deletion is the only copy left. On 2026-08-03 one pass removed
946 records, a little over fifteen hours of one day. Anything older than four
weeks exists only because a harvest ran while it was still there.

That editing is silent in every way a filesystem could tell you about. It moves
the segment's `ctime` but **not its modification time**, which on the segment
this was measured against still read 25 days earlier afterwards. It is also why
`state 3` appears in `scripts/segbindex` output and in this package's tests. The
full measurement is in `project/private/2026-08-03-segb-tombstones.md`.

## What you get

One `interval` event per focus session, carrying the application's bundle
identifier. No document, no project: Biome knows Motion held focus for
thirty-six minutes but has no idea which composition was open. Attributing
that requires a second source, which is what the `app` field on every event
exists to make possible.

Focus is exclusive, so sessions do not nest. The instant one application
resigns is the instant the next takes over, to the same microsecond.

## Events seen at only one edge

Some events come back as `unbounded`: one endpoint is the zero time, and the
event says why. These are real observations, not errors, and they are
deliberately not dropped.

About one focus change in sixty cannot be paired. Checked against
`knowledgeC`'s own records, each unpairable stop lines up with a session that
began when the machine woke: the application was already frontmost, so
nothing *became* frontmost and macOS wrote no start. The obvious repair — date
the session from the previous record — is exactly wrong, because the gap
before it is sleep. In one measured case that would have charged five minutes
of an idle machine to Safari in place of two seconds of real use.

So an unpairable record keeps the instant it has and reports the other side as
unknown. It is not a `point`: a point asserts there was no duration, and there
was one — 6.42h across five confirmed days hangs off these records, twelve
percent of attributable time.

### Why the reason travels with the event

The two causes need opposite treatment and are indistinguishable afterwards
from the row alone, so `Unknown` is part of the event rather than something a
reader works out later.

| Reason | What it is | Repair |
| --- | --- | --- |
| `horizon` | A start at the end of the run. Something is frontmost right now. | The next harvest supplies the stop. `Resolves()` is true. |
| `sleep` | A bare stop. The machine woke with the application already frontmost. | Never resolves. Bounded against presence, which needs two sources and happens above this package. |
| `lost` | A partner record that was never written, for a reason nothing in the data reveals. | Never resolves. Not yet observed in the wild. |

A live stream of 48,047 records classifies as 23,832 pairs, 382 `sleep` and
exactly one `horizon` — the application on screen when it was read.

### The paired intervals do not contain sleep

The `sleep` reason above says macOS writes no *start* on waking. The other half
of that story is that it does write a *stop* on going to sleep — which is what
keeps a paired interval from quietly spanning a sleep and charging it to an
application. An interval like that would look completely ordinary, so it matters
that this is measured rather than assumed.

It is measured. Over 23,328 paired intervals and 93 recorded sleeps long enough
to be unambiguous — 265h in total, the longest 11h28m — no interval encloses
one, by two independent tests: one comparing against presence, and one using
only Biome's own records. Run `go run ./scripts/sleepcheck` to reproduce it.

**So a duration from a paired interval can be taken at face value.** There is no
sleep-correction pass to apply, and nothing here needs bounding against presence
except the `sleep` records themselves.

### Do not intersect focus with presence

Presence is a **floor** for engaged time, not a ceiling, because the backlight
stream has three states rather than two.

`/display/isBacklit` records rows asserting the screen was on *and* rows
asserting it was off. What is left over — time carrying no row of either value —
is not darkness. It is the sampler having said nothing, and it must not be read
as absence.

The gaps are rare but not negligible. Across the stream there are **11 of them
totalling 8.87h**, the largest running **6h23m**. One runs 1h27m on 2026-07-14
while Biome records dense app switching across ten applications throughout — so
intersecting the two sources would have silently deleted ninety minutes of real
work. Focus during *recorded* darkness, by contrast, is 0.07h, which is edge
jitter.

A query selecting only `isBacklit=1` cannot tell those two situations apart.
Keep the off rows.

There is a fourth state that is not in the stream at all: **beyond the reach of
the snapshot you are reading.** Focus recorded after a snapshot was taken has no
presence data to compare against, and reading that as darkness makes a normal
day look like hours of focus during sleep. Coverage is a property of the harvest
that produced the data, not of the rows, which is why a run records the window it
covered even when it found nothing.

If you want only sessions of known length, filter on `Shape == source.Interval`
rather than on a non-zero duration; a genuine focus session can be short enough
to round to nothing.

## Reading `since`

`Collect` returns intervals that had not ended before `since`. An interval
still running when the previous harvest finished is not old news, so what
`since` removes is what had already finished.

An event whose end was never observed comes back whatever `since` is, because
it has reached no instant at which it could be considered past. Filtering those
on the start they do have would drop the record saying what is on screen right
now, on every single harvest.

The boundary is therefore inclusive, and re-emitting is the point: the second
reading is the same real session at greater completeness. The cost is that the
same evidence legitimately arrives more than once, so deduplication is by
content at the store rather than positional here.

Every segment is read on every run regardless. The whole stream is a few
megabytes, and a session routinely begins in one segment and ends in the next
— reading a subset would silently cut those in half.

## When a segment is damaged

A segment is an opaque half-megabyte, so every failure names the file and the
byte being read when it went wrong.

Each record carries a CRC-32 of its own contents, which the parser checks.
That is a stronger guarantee than it sounds: it is what proves the record
framing was read correctly rather than merely plausibly, and it is why a
corrupt segment is reported instead of quietly yielding a shorter history.

A damaged segment fails the whole source rather than being skipped. Partial
focus history that looks complete is worse than none, because the missing
hours are invisible.

**A missing stream is not a failure.** A directory that is not there is
indistinguishable from an application nobody installed, and reports nothing. A
directory that refuses to be read is different, and says so — that one is
usually a missing Full Disk Access grant.

## Three ways the raw data will mislead you

**The segment file name is not the same clock as the records inside it.** The
name is microseconds since 2001-01-01; the timestamps in the records are
seconds since 1970-01-01. Confusing them is a thirty-year error that still
produces a plausible-looking date. Timetattle never reads the name, ordering
segments by the creation time inside each header instead.

**Records have no length field.** Each segment carries an index, written
backwards from the end of the file, and that index is the only authority on
where one record ends and the next begins. Scanning for records that look
right will appear to work and will quietly lose some.

**The times come back in UTC, and the other sources do not.** This package
returns `time.Time` values in UTC; knowledgeC snapshots and the
[timestamped-file readers](/docs/sources/) return local time. Comparisons are
unaffected, because Go compares instants and not wall clocks — a join written
against these events is correct either way, which is exactly what makes this
dangerous. What breaks is everything that reads a wall clock: formatting an
interval for a person, or bucketing events into a calendar day.

The failure is loud enough to spot and quiet enough to believe. This has
already happened once: a Motion autosave at 16:30 was reported as sitting
inside a focus interval "21:29:03–21:38:11", which is impossible on its face.
The join was right; the printed times were five hours out. Convert with `.Local()` at the boundary where Biome events
enter, as `scripts/joinday` does, or at every print site, as `scripts/appfocus`
does. Do not mix the two approaches in one program.

## App.InFocus

A sibling Biome stream describes the same thing. Its newest segment was last
written 2026-07-27 12:22 and has not been appended to since — the same pattern
`/app/usage` showed the day before, which in that case turned out to be a stall
and not a retirement — so it is noted here rather than depended on, and it is
deliberately **not** captured by a harvest.
It also carries a `remote/` directory holding other devices' data, which
nothing here wants.

If `ScreenTime.AppUsage` ever stops in its turn, that is where to look next,
and adding it to the capture is a one-line change to `snapshot.Streams`.
