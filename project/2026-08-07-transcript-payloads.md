# Sessions and tokens had nowhere to go, and the one place they could go was the place the dedup key cannot see

2026-08-07. Measured on an Apple M1, darwin/arm64, go1.26.4, against this
machine's own Claude Code corpus (4,583 transcript files) and against the live
store, which held **1,031,945 events** and was at migration 0013 throughout.
Every figure below names the command that reproduces it.

Ben asked for two things the store could not record: how many Claude Code
sessions a period held, and what they cost in tokens. Both are in the
transcripts and neither had a column. Dollar cost was ruled out by direction —
no transcript carries one, so it would be tokens multiplied by a pricing table
somebody has to maintain, which is a different kind of claim from an
observation.

This is what landed: `event.payload`, a JSON object outside `event_identity`,
written once at insert; `internal/usage` owning its shape; migration 0014
enforcing that shape; and `Store.Usage`, a roll-up per scope and period.

## The trap, and why it is not a matter of taste

`state` is part of `event_identity`, for the reason 0001 gives: knowledgeC
writes contradictory overlapping rows and the reading is the only thing telling
them apart. It is therefore the one field on an event that a per-record figure
could plausibly be smuggled into, and it is exactly the wrong one.

Anything inside the dedup key has to be a fact that two readings of ONE
observation agree about, because disagreement is spelled *two rows*. Two
readings of one transcript record are not hypothetical here — Claude Code's
compaction and side-question mechanisms replay a conversation, uuid and
timestamp intact, into an internal subagent's file:

```
go run ./scripts/sessioncensus
```

```
RAW PASS (lines on disk, before any rule of the collector's)
  files                                 4583
  timed records                         830955
  repeat readings of one uuid           7182
    disagreeing about the session       0
    disagreeing about the token figures 52
```

Those 52 are the argument. `internal/transcripts` already resolves the
human-or-machine disagreement (7,133 of the 7,182 readings) with a preference
rule, and that rule works because the field has two values and one of them is
evidence. A token count has neither property. In the key, each of those 52
becomes a second row for a record that happened once, and every figure computed
from them double-counts.

The re-read is not an edge case: this source has no watermark and parses every
file on every harvest, so all ~830,000 records are offered to the store every
time. Dedup by content is the only thing between that and a store that grows by
a corpus a day.

**Measured, rather than argued** — `TestAFigureCarriedInStateMakesASecondReadingASecondRow`
in `internal/store` poses both designs against the same two readings: the figure
in `state` stores 2 rows, the figure in `payload` stores 1, and the surviving
payload is the FIRST reading, unrewritten.

## Three places it could have gone

**A sidecar table** (`event_usage(event_id, session, input, …)`) gives typed
columns and no JSON. It was rejected on the write path, and the cost is
countable. The event insert is `ON CONFLICT DO NOTHING`, which returns no id
when the observation is already stored — which, after the first harvest, is
nearly all of them. Attaching a sidecar row therefore means looking each event
id up by its seven-column identity: **822,387 extra SELECTs per harvest** on
this machine, to attach facts already in hand at insert time. It also splits one
observation across two tables that can now disagree.

**Typed columns on `event`** (`session_id`, `input_tokens`, …) are cheap to add
— `ALTER TABLE ADD COLUMN` is O(1) in SQLite and 0002 did exactly that for
`cause`. They were rejected because they put one collector's vocabulary into a
source-agnostic schema. Nothing about a Biome focus interval or a knowledgeC
presence row has a session or a token, and the next source with two facts of its
own arrives at the same argument and wins two more columns.

**The payload column**, which has existed unwritten since 0001, is named by no
index, is already carried by `canonical_event`, and is opaque to the store. It
won. Its cost, stated rather than waved at: about 60 bytes per record where the
model spent nothing and about 110 where it did, so roughly **60 MB across the
822,387 transcript rows** in a 651 MB store. A session lookup table keyed by an
integer would recover about half of that (a session id is 36 characters and
there are 1,590 distinct ones) at the price of a join and an id lookup per
record. At this size that trade is not worth making; at ten times it might be,
and the figure is here so the decision can be revisited against a number.

## What the corpus actually says

```
go run ./scripts/sessioncensus
```

```
COLLECTOR PASS (internal/transcripts, after its own replay resolution)
  events emitted                        823769
  carrying a session                    823769
  naming no session                     0
  distinct sessions                     1590
  sessions spanning >1 working dir      286
  carrying token figures                399058
    input          stated by            399058
    output         stated by            399058
    cache_read     stated by            399058
    cache_creation stated by            399058
  input tokens                          18407454
  output tokens                         201285356
  cache read tokens                     44912093420
  cache creation tokens                 1293974299
```

Four things follow from that.

1. **Every record names its session.** 823,769 of 823,769. The session is taken
   from the record's own `sessionId` field, never the file name — the same rule
   `cwd` follows, and for the same reason.
2. **`cache_creation_input_tokens` is not optional.** The task named three
   figures; there are four, and the missing one is 1.29 billion tokens, six
   times the output figure. A parse of the three would have dropped the second
   largest number there is.
3. **The four are never summed.** Cache reads outweigh output tokens 223 to 1
   (44,912,093,420 against 201,285,356), so a single "tokens" total would be the
   cheapest figure wearing a general name.
4. **A figure the record did not state is left unstated.** Every one of the
   399,058 records carrying usage stated all four, several of them explicitly
   zero — and a stated zero is an observation while an absent figure is not.
   `internal/usage` keeps them as pointers for that reason alone.

### Reconciling the two passes

The collector emitted 823,769 events; the raw pass counted 830,955 timed records
and 7,182 repeat readings, which the collector collapses. 823,769 + 7,182 =
**830,951 against 830,955, a residual of 4**. The two passes ran 41.7s apart
over a corpus being appended to by the very session running the tool — the same
reason `scripts/transcriptreconcile` expects a small positive residual — and a
record whose `timestamp` will not parse would land here too, being counted by
the raw pass (a string) and dropped by the collector (a `time.Time`). Four
records out of 830,955 is inside that drift; a residual that grew would not be.

## What a session count for a SCOPE means

This is the part worth stating plainly, because it is a semantic decision and
not an implementation detail.

**286 of 1,590 sessions carry records from more than one working directory** —
18% of them. A session is a conversation, not a project, and a conversation
wanders: a subagent's `cwd` is one or more segments below its parent's in 26% of
subagent records, and plenty of sessions simply move between repositories.

So `Store.Usage(…, Project: "x").Sessions` is **the number of distinct sessions
that TOUCHED x in the period**, and:

- the per-project counts **do not partition** the whole. A session touching two
  projects is counted once in each, and adding the per-project figures overshoots
  the store-wide count on purpose. `TestASessionThatTouchedTwoProjectsCountsInBoth`
  pins exactly that: 1 session in each of two projects, 1 session in total.
- the same applies to periods. A session running across midnight is counted in
  both days.
- tokens, by contrast, DO partition, because they are attributed per record and
  each record resolves to one locator.

The alternative — assigning each session to one owning project (first record?
most records? most time?) — would report a project as untouched by a session
that genuinely worked in it, and would need a rule nobody has evidence for. A
count that overlaps and says so is honest; a partition invented to make the
columns add up is not.

## The migration, and why it is a trigger

Storing a payload needed no DDL at all: the column exists and is outside every
index. What needed enforcing is that a payload is a JSON **object**, because
`json_extract` answers NULL for every path of a value that is not one. `7`,
`"tokens"` and `[1,2,3]` are all valid JSON and would store cleanly and then
read back as a store that recorded nothing — the collapse of *nothing was
recorded* into *observed nothing* that this project has already shipped three
times, in a new place.
Malformed JSON is louder and worse: `json_extract` raises, so one bad row
silences every figure for every scope.

0009 faced the same choice and rebuilt the table, arguing that a CHECK applies
to the rows already there while a trigger guards only what comes after. That
argument is right and it does not apply here, because the population it protects
is empty:

```
sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
  "SELECT COUNT(*), SUM(payload IS NOT NULL), SUM(source_id='claude-transcripts') FROM event;"
1031945|0|822387
```

Not one row in a million carries a payload, so a rebuild would copy 1,031,945
rows and recreate six indexes and a view in order to validate nothing.

Two spellings inside the trigger are load-bearing:

- `json_type()` **raises** on input that is not JSON rather than returning NULL,
  so it cannot be the first test — on exactly the input the guard exists to
  catch, the guard would be what failed. `json_valid()` runs first, inside a
  `CASE`, because SQL does not promise that the left side of an `OR`
  short-circuits and `CASE` does.
- `NEW.payload IS NOT NULL` is written out because a `WHEN` clause that
  evaluates to NULL does not fire, exactly as a CHECK that evaluates to NULL
  passes. That is the trap this schema has been bitten by twice.

**Proved by mutation**, in `internal/migrations/payload_shape_test.go`: with both
triggers dropped, all five refused payloads insert, the roll-up then fails
outright with `malformed JSON`, and with the malformed two deleted the remaining
**3 rows carrying a payload report 0 sessions and 0 tokens**.

### No index, and the plan that says so

The roll-up filters on `payload IS NOT NULL`, a period, and optionally a
project, then reads the payload of every surviving row. An index cannot answer
the sums, and transcript rows are already 80% of the table (822,387 of
1,031,945), so skipping payload-less rows buys little. The period bound already
rides 0013's index, because it is spelled `COALESCE(started_at, ended_at)` here
exactly as it is there — measured through
`TestTheUsageRollUpBoundsThePeriodThroughAnIndexRatherThanAScan`, which prints
the plan it asserts on:

```
period-bounded:  SEARCH e USING INDEX event_anchor (<expr><?)
unbounded:       SCAN e
```

Both carry `USE TEMP B-TREE FOR count(DISTINCT)` for the session count, and no
index removes that one: the counted value is computed per row, and the tree
holds the 1,590 distinct sessions rather than the rows.

## History does not backfill, and that is the honest answer

`ON CONFLICT DO NOTHING` and no `DO UPDATE`: an event is an observation and is
never rewritten. The 822,387 transcript rows already in the live store were
stored before this change and **carry no payload, permanently**. Nothing in a
later harvest will fill them, because a re-read of those records conflicts on
identity and does nothing.

That is deliberate and it has a consequence a dashboard has to say out loud:
session counts and token sums exist for records harvested from now on, and read
as *nothing was recorded* for everything before. `Store.Usage` is built so that
this cannot be misread — `Observations` is how many rows carried a payload at
all, and each token figure carries its own `Records` count beside its `Total`,
so a zero total over zero records is visibly different from a zero total over
four hundred thousand.

**If Ben wants the history, it is a separate, deliberate decision**, and it has
two shapes: a one-shot fill of `payload` where it IS NULL for rows a re-parse
can match (a mutation of existing rows, which this store has never done), or
accepting the gap and reporting from the boundary forward. This document names
the choice rather than making it.

## metrics is byte-identical

Tokens play no part in presence, so nothing about this may move a metrics
figure. Two captures each side, one binary built at `3f65b0c` and one with the
change, against the live store:

```
shasum -a 256 "$TMPDIR"/tokens-sessions-metrics-{before,after}-{1,2}.txt
0784339eb8d90c1285e950b340bc8d379f245b3bbbb5f914002d85d18bc4ffbb  (all four)
```

One hash across four captures: the baseline is demonstrably deterministic and
the change does not move it. The live store was still at migration 0013
afterwards — `metrics` opens read-only and therefore does not migrate — so 0014
reaches it on Ben's next writing command, which is where every migration in this
project has arrived.

## What was NOT built

- No CLI surface. `cmd/timetattle` is untouched; four other agents own its files
  and a follow-up task surfaces these rows.
- No cost. See the top of this document.
- No backfill. See above.
- No index on the payload. See the plan.
- No client roll-up. A client is a fold over the append-only `client_project`
  log at each event's own instant (`internal/clientmap`), not a column, so a
  caller asking about a client resolves it to projects and asks about each.
