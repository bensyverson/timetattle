---
title: "The event store"
weight: 40
description: "Where observations are kept, why they are never edited, and why re-reading the same window is free."
---

# The event store

One SQLite database at `~/.timetattle/timetattle.db` holds every observation
Timetattle has ever made, and a record of every read that produced them. It is
created and brought up to date the first time anything opens it; there is no
setup step and no migrate command.

Three ideas run through it. The rest of the design follows from them, and each
one exists because of something that was measured rather than assumed.

## Events are observations, and they never change

A row says *this was seen*. It is not a claim about what happened, and it is
never edited when a better reading arrives later.

The clearest case is the newest Biome segment, which usually ends with an
application still frontmost. That harvest stores an event with a start and no
end. The next harvest finds the matching stop and stores a second, complete
event beside it — it does not go back and finish the first one. Collapsing the
pair is a *derivation*, done on the way out, and both observations survive it.

### The canonical read, and why it is a view

Because both rows persist, something has to collapse them on the way out — and a
rule saying *remember to collapse* gets forgotten. The failure mode of
forgetting is silently double-counted hours, which is the exact inflation
Timetattle exists to catch, so the collapse is structural: migration `0001`
ships a `canonical_event` view, and that is what `Store.Events` reads.

**Only a horizon is ever superseded.** A horizon is an unpaired *start* —
something was frontmost when the harvest ran and had not resigned yet — and it
is superseded when an `interval` exists with the same start and the same
identity. The two share that start to the microsecond, because it is the same
record in the same segment read twice.

Nothing else collapses, and the narrowness is the whole point:

| Reason | Completes? |
| --- | --- |
| `horizon` | yes, on the next harvest — this is the only superseded case |
| `sleep` | never. The machine woke with an app already frontmost, so macOS wrote no start and no later harvest will supply one. Its repair is bounding against presence, permanently, and 6.42h across five confirmed days hangs off these |
| `lost` | never. The partner record was not written and the cause was not observed |

A rule general enough to touch a bare stop — "prefer the most complete
observation" — would silently delete that 6.42h. Worth knowing: on the live
Biome stream such a rule deletes *nothing*, because no focus interval happens to
end at the same microsecond as a bare stop. It would pass every real-data check
and bite later, on `knowledgeC` presence rows, which share endpoints
constantly. That is why the narrow rule is tested against constructed cases and
not only against the stream.

Identity is matched on `source_id`, `app`, `document` and `state` — not just
`(source, app)`. Presence carries no application, so the reading is the only
thing distinguishing one observation from another, and a view grouping on the
application alone would collapse a lit horizon into a dark interval.

The table itself is still reachable, under a name nobody reads by accident:
`Store.EventsIncludingSuperseded`. It is for provenance — what did we see, when
did we first see it, did a horizon ever get completed — and never for summing
time.

Human corrections are not rows here either. They are overlays keyed to a
wall-clock span, because an append-only log of observations cannot express
"that was a meeting, not work" without inventing retraction semantics that
would tax every read forever. Timetattle is a signal for when work happened,
not a system of record for billable hours, and the store is shaped for the
first job only.

The one column that is derived rather than observed is `project`. It is
nullable, it is filled by a resolution pass against the registry, and nothing
writes it at collection time — because registering a folder later, or fixing a
mapping, has to re-attribute events that already exist.

The **client** a project belongs to is deliberately not a column here. It has 38
distinct values across some 973,000 events and changes as a whole rather than per
event, so it lives in the append-only `client_project` log (migration 0012) and
is folded on the way out. See [Engaged time and presence](/docs/metrics/#where-the-client-comes-from). For app focus and
presence it stays NULL permanently: a bundle identifier is not a path, and
those events are attributed transitively or not at all. See [Registering a
project](/docs/registry) for how a directory gets blessed and how a path
resolves back to one.

`Store.ResolveProjects` is that pass. It takes a `Resolver` — a function from
a locator to an `Attributed{Client, Project}`, injected rather than imported so
this package stays ignorant of how attribution actually works — and calls it
once per *distinct* locator in the table, writing the **project** into every row
that shares it. The client half of that answer is not written per row; it goes to
the `client_project` log, once per registered directory rather than once per
event.
`cmd/timetattle` wires `internal/registry.ResolveAmong` into it and runs it
after every harvest's ingestion phase, whether or not every source came through
cleanly. That resolver answers in two ways, because a stored locator can name a
directory that no longer exists: it walks upward from the path for the ordinary
case, and it matches the path against the former paths declared by
`timetattle consolidate` for a directory that was renamed away, which nothing
can walk upward from. Both are read from markers; see [Registering a
project](/docs/registry) for which claim wins when a path is spoken for twice.

Re-running the pass is not merely harmless, it is the mechanism: a locator
resolve now reports unattributed for is written back to `NULL`, so a marker
that is later removed or corrected un-attributes rows too, and `Write`'s own
insert statement does not even name the `project` column, so nothing upstream
of this pass can populate it by accident.

### Unattributed and unreadable are different answers

A locator the resolver cannot *read* — a permission-denied directory, a symlink
cycle, a marker written by a newer build — is **skipped**, counted and named,
and the pass carries on. Its rows keep whatever project they already had; they
are emphatically not set to `NULL`, because that would state as observed-absent
something nothing observed. The next pass retries it from scratch, which costs
nothing, since `project` is a recomputable cache.

`ResolveProjects` returns an `Attribution` saying so, in the shape
`internal/sessions.Stats` uses: `Attributed + Unattributed + Skipped ==
Locators`, always, so the result can be reconciled against the input
arithmetically. `harvest` prints the same three numbers with the locator count
they came from, names every skipped locator with the reason and how many events
it covers, and exits non-zero — a directory it could not read is a problem the
run had, exactly as a source it could not read is.

The zero is printed when nothing was skipped. "Nothing was unreadable" and "this
build does not count unreadable directories" must not look the same.

This used to abort. One resolver error returned from inside the pass's single
transaction, so one unreadable directory un-attributed nothing and cost every
*other* locator its attribution — which contradicted `harvest`'s own promise
that a source it cannot read loses only its own events. Measured on a copy of
the live store on 2026-08-01 with one real symlink cycle planted on one event
(`go run ./scripts/attributionskip -poison`): the old behaviour left 0 of
927,396 rows attributed, the new one leaves 880,283 and reports 1 skipped.

The pass is still **one transaction**, and skipping does not change that. A
resolver error is a fact about one locator and leaves the others' answers
untouched, so there is nothing to roll back; a failure of the write itself is
attributable to no locator, so it still rolls the whole pass back to the last
complete attribution rather than committing a torn one.

## Deduplication is by content, because collectors re-read

No collector tails its source. `knowledgeC`'s `/app/usage` was observed 23
hours behind `/display/isBacklit`, so a tail would simply miss it; and a Biome
interval that was still running at the last harvest is deliberately re-emitted
at greater completeness, because otherwise the first, unfinished reading would
stand as the permanent record of a session that has since ended.

So the same evidence arrives many times, and a watermark cannot help. What
does is a natural key:

```
UNIQUE (source_id, shape, started_at, ended_at, app, document, state)
```

with the nullable columns wrapped in `COALESCE(…, '')`. Writing is therefore
idempotent, and a re-harvest costs a few thousand insert attempts that do
nothing at all. Measured on the live Biome stream: 24,512 events ingested, then
the same window ingested again, **zero** rows added.

The `COALESCE` is not cosmetic. SQLite treats NULLs as *distinct* in a unique
index, so without it every endpoint that was never observed compares unequal to
itself. Dropping it and re-running that same test inserts 386 duplicate rows on
the second pass — exactly the 385 bare stops and 1 horizon in the stream, and
then 386 more on every harvest after that.

`locator` is deliberately **not** in the key. It is provenance: the same
segment replayed from a snapshot rather than from the live stream is the same
observation, and splitting it in two would double-count the work. Neither is
`cause`, for a different reason: it is a standing declaration about a source
rather than a reading taken per observation, so `source_id` already carries it —
and because a manifest author who *corrects* a mis-stated cause would otherwise
insert a second row for every file already harvested, each looking like a genuine
second observation of the same instant. The correction reaches new rows only,
which is what an append-only store means. `state`
**is** in the key, because `knowledgeC` writes contradictory overlapping rows —
on 2026-07-14 at 09:01:12 both an `isBacklit=1` and an `isBacklit=0` row exist
with identical endpoints. Those are two observations, and without the reading in
the key they collapse into one.

### Two counters, and what they mean

Every harvest writes one `harvest_run` row **per source**, not per invocation,
because collection has partial-failure semantics: one broken source loses only
its own events. A run that lost a source records which one and why, or the loss
is invisible in every number downstream. Runs that found nothing are recorded
too — "no Motion work on the 22nd" and "no harvest ran on the 22nd" are
different facts, and only an empty window tells them apart.

Each run carries `events_seen`, what the source returned, and `events_new`,
what survived dedup:

- **`events_new` trending to zero across runs is dedup working.** On a second
  harvest of an unchanged window it must be exactly zero, and the total row
  count must not move.
- **`events_seen` and `events_new` are not equal on a fresh database, and that
  is not a bug.** `knowledgeC` repeats itself: 2026-07-10 at 15:22:32 carries
  three rows identical in stream, endpoints and value, and 2026-07-14 at
  09:01:12 carries two. Nothing distinguishes them, so they carry no extra
  information and collapsing them is correct.
- **`events_seen` falling for a source** means retention is biting and the
  window is closing.

### `capture_run`, and why it is not more `harvest_run` rows

A harvest has two phases, and `harvest_run` records only the second. **Capture**
preserves the sources that delete themselves — `knowledgeC`, Biome segments, the
Motion vault, a `filedays` scan — as files on disk; **ingestion** then reads
those files into `event`. Until 2026-08-01 the capture phase wrote nothing at
all, so `timetattle status` could only count the files in the snapshot
directory, and a capture that failed today left yesterday's files sitting there
looking healthy. That is what made the 44-hour outage of 2026-07-30 invisible.

Reusing `harvest_run` would have been cheaper and was rejected for three
reasons, argued in full in the header of `internal/migrations/0007_capture_run.sql`:

1. **The columns measure the wrong thing.** A run's payload is `events_seen` and
   `events_new`. A capture returns no events — it returns files and bytes.
   Writing bytes into `events_seen` puts two incommensurable populations in one
   column that several readers already sum; leaving them at `0` makes a capture
   row indistinguishable from an ingestion that ran and found nothing, which is
   a real and healthy state.
2. **`harvest_run` is the provenance chain, not a log.** `event.first_seen_run`
   is `NOT NULL` with a foreign key into it. Capture rows would be rows nothing
   can ever point at, and every provenance query would have to learn to exclude
   them for ever.
3. **The two phases fail differently and are read at different moments.**
   `harvest_run` answers *is the evidence still arriving?*; `capture_run`
   answers *is it still being preserved?*, and is asked once the answer to the
   first is known to be no.

`capture_run` carries `kind`, `input`, a span, one of five statuses, and
`files_new` / `files_held` / `bytes`. Three details are load-bearing:

- **`finished_at` is `NOT NULL` and there is no `running` status.** A capture
  row is written *after* the capture returns, so both endpoints are known facts
  and a live capture row cannot exist. `harvest_run` cannot do this — a run must
  exist before the events that hang off it — and pays for it: the live store
  still carries row 28, `claude-transcripts`, stranded at `running` since the
  process died under it on 2026-08-01, indistinguishable from a harvest in
  progress.
- **`absent` is a status.** A machine with no Motion installed has no vault to
  preserve. Recording nothing for that would collapse *observed not-X* into
  *nothing was recorded*, which is the standing distinction this whole store is
  built to keep. An `absent` row means: on this run, we looked, and there was
  nothing there.
- **`skipped` is a status too, added by migration `0015`.** The `knowledgeC`
  snapshot has a cadence of its own — four hours, whatever the harvest cadence —
  so a run can finish having deliberately not preserved it. That is neither
  `ok`, which claims a preservation that did not happen, nor `absent`, which
  would say there is no `knowledgeC` on this machine. It is also not the
  content-addressed reading of *nothing new*, which is `files_new` 0 with
  `files_held` 1 and means the source **was** read through and has not changed.
  *`knowledgeC` has not changed in four hours* and *we have not looked in four
  hours* are different facts. A `skipped` row is forbidden by `CHECK` from
  claiming any file, any held file or any byte: a run that did not read its
  source preserved nothing from it.

## Timestamps are text, and the text has to sort

SQLite has no date type. Every instant is stored as fixed-width UTC text —
`2026-07-14T09:01:12.000000Z` — so that comparing two of them as strings gives
the same answer as comparing the instants. The schema's
`CHECK (ended_at >= started_at)` and every range query depend on that.

The obvious choice is wrong. `time.RFC3339Nano` trims trailing zeros, so
`.5`, `.50` and `.500000` come out at three different widths and
`09:00:00.5Z` sorts *after* `09:00:00.123456Z` while being earlier in fact.
Nothing about that failure is loud: it mis-sorts a scattering of rows near
second boundaries and quietly weakens the interval constraint.

Normalising to UTC happens in the store because the store is the only place it
can be relied on. `internal/biome` returns UTC; the `knowledgeC` and file
readers return local time. Go's comparisons hide the difference, since they
compare instants — formatting, day-bucketing and text ordering do not.

Microseconds, because that is what the sources resolve: Biome segment names and
`knowledgeC`'s Core Data doubles are both microsecond figures. Anything finer is
truncated rather than rounded, so re-reading a record always produces the same
key.

### The layout is enforced, not merely expected

Every timestamp column in the schema carries a `CHECK` that pins the whole
layout:

```sql
CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
```

Width alone is not enough, and migration 0003 shipped the weaker form for a
while: `'2026-08-01T08:04:35.0-05:00'` is *exactly* the same 27 characters the
store writes and is five hours out of UTC, so `length(x) = 27` reports success
while the ordering it exists to protect is wrong by five hours. Migration
`0009_timestamp_shape.sql` brought every table up to the `GLOB` form. Nullable
columns — `event.started_at`, `event.ended_at`, `harvest_run.finished_at`,
`harvest_run.since` — carry their own `IS NULL OR` arm, because an instant that
was never observed is a fact and not a failure.

`derived_session.corpus_from` and `corpus_to`, added by `0010`, carry a
`= '' OR GLOB` arm instead: they are `NOT NULL` and spell "unbounded" as the
empty string. That is deliberate rather than stylistic. A `CHECK` passes when it
evaluates to `NULL`, so a nullable bound with a bare `GLOB` would have enforced
nothing at all on the values that mattered.

**What the guard does not do.** It makes the *format* unambiguous, which is what
the text ordering rests on. It does not make the *value* true. A local instant
mislabelled `Z` passes, and so does an impossible date —
`'2026-13-45T99:99:99.000000Z'` satisfies every one of these constraints. Those
need a clock and a calendar, not a `CHECK`; normalising to UTC happens in
`internal/store` and only there.

`./scripts/timestamp-shape-audit` counts, for every timestamp column in a store,
how many rows hold something that is not the layout — reporting rows, NULLs and
failures as three separate numbers, and reporting nothing at all for a table
that holds no rows.

## What one event row is

| Column | Meaning |
| --- | --- |
| `shape` | `interval`, `point` or `unbounded` |
| `unknown` | why an endpoint is missing: `horizon`, `sleep` or `lost` |
| `started_at`, `ended_at` | NULL only on an `unbounded` event, and only on one side |
| `app`, `document` | what the event is about; either may be absent |
| `state` | the scalar reading a source took, e.g. `on`/`off` for the backlight |
| `locator` | the absolute path the evidence was read from; provenance, not identity |
| `cause` | what wrote the artifact: `user-action`, `timer` or `recovery`; NULL where the event is not an artifact |
| `project` | the recomputable attribution, filled by resolution |
| `payload` | a JSON object of what the source knows that no column describes; NULL where there is nothing extra to say |
| `first_seen_run` | the harvest that first saw it |

`cause` is NULL for everything that is not an artifact — a Biome focus interval
and a presence row are not written by anything a manifest can describe — and
that NULL means "does not apply" rather than "nobody said". The manifest schema
requires the field of every source that can declare one, so an undeclared cause
never reaches the column at all. The distinction matters because the whole point
of the field is to tell evidence of a person from evidence of a running process,
and a third state that quietly means "unknown" would be read as one of them.

Presence has no table of its own. It is an ordinary `interval` carrying
`state`, which is what makes its three states fall out of the data model rather
than needing to be represented: observed on is a row, observed off is a row, and
**never sampled is the absence of a row**. That distinction decided whether 90
minutes of confirmed work on 2026-07-14 counted, and every query written before
it was understood collapsed it by selecting `WHERE value = 1`.

### The payload, and why it sits outside identity

`payload` is the one free-form column on an event, and it exists for facts that
are real, per-record and describable by no column: today, which Claude Code
session a transcript record belonged to and what the model spent on it. The
shape is owned by `internal/usage`, which declares both the JSON keys and the
paths the store's roll-up extracts, so the two cannot drift apart silently.

```json
{"session":"…","tokens":{"input":2,"output":214,"cache_read":0,"cache_creation":46724}}
```

**It is named by no part of `event_identity`, and that is the whole design.**
`state` is in the dedup key, so it is the field a per-record figure could
plausibly be smuggled into — and anything in the key has to be a fact that two
readings of one observation agree about, because disagreement is spelled "two
rows". Two readings of one transcript record are not hypothetical: 7,182
repeated readings exist in this machine's corpus and 52 of them state different
token figures (`go run ./scripts/sessioncensus`). In the key those become 52
duplicate observations of records that happened once; outside it the first
reading is stored and the second does nothing.

A payload is written **once**, when the observation is first stored, and no
harvest ever rewrites one: a re-read of a stored observation conflicts on
identity and does nothing, whatever it now says the record cost. A row stored
before its source emitted payloads therefore carries none. That reads as
*nothing was recorded*, never as zero, and `Store.Usage` reports it that way:
its `Observations` count is how many rows carried a payload at all, and every
token figure carries its own count of the records that stated it beside the
total they add up to.

### Backfilling history, the one write that changes an observation

Payloads began at migration 0014, which left every transcript row harvested
before it carrying none — so session counts and token sums would have read as
*nothing was recorded* for all of history and as real figures from that date
onwards, with the boundary visible in every chart and explainable to nobody.

`scripts/payloadbackfill` ends that discontinuity once. It re-reads the corpus
through the collector itself, matches each record to its row by the seven
columns of `event_identity`, and writes the payload **only where the column is
NULL**:

```
go run ./scripts/payloadbackfill                  # dry run: reports, writes nothing
go run ./scripts/payloadbackfill -dry-run=false   # the real pass
```

It is a script rather than a `timetattle` verb because it is a one-shot repair
with nothing to do afterwards, and because this is the only thing in Timetattle
that changes a stored observation — a daily command set is the wrong place to
advertise that. Four properties make it safe to say that history and the present
now mean the same thing, and each is pinned by a test rather than promised:

- **It fills only where there is nothing.** A payload already stored is never
  rewritten, so the first reading of a record stays the stored one — the same
  rule `ON CONFLICT DO NOTHING` applies on the way in. Both guards were removed
  in turn to measure what they buy: with neither, a second pass refilled every
  row and changed one record's output figure from 91 to 92, silently, since no
  row count moves.
- **It is idempotent.** A second pass finds every row already carrying a payload
  and writes nothing at all — same rows, same bytes.
- **It inserts nothing.** A record the store has never seen is counted and
  skipped, because an event created here would carry provenance no harvest run
  ever had.
- **Absence stays absence.** A row no re-read record speaks for — its transcript
  deleted or rewritten — keeps its NULL and is counted as unmatched. It never
  becomes a zero.

The dry run is the real run with the write withheld: the same traversal and the
same matching produce its counts, and it opens the store read-only, so "it wrote
nothing" is a property of the connection rather than of a flag. It is the
default, so running the command by accident does nothing.

One count it deliberately does not report is how many transcript FILES have gone
missing. An event records the working directory its record named, never the file
it was read from, so no row can be traced to a file that is or is not still
there; "the transcript was deleted" and "the root was wrong" are one figure —
`unmatched` — and splitting them would mean inventing the difference.

A payload that is not a JSON object is refused at write time by the triggers
migration 0014 adds, for a measured reason: `json_extract` answers NULL for
every path of a value that is not an object, so `7` or `[1,2]` would store
cleanly and then read back as a store that recorded nothing, while malformed
JSON raises and takes the whole roll-up down with it.

### The invariant a collector author must not get wrong

**`shape` reflects what the source claims to have observed, never the duration
that happened to be measured.** `knowledgeC` and Biome both report a start and
an end, so their events are `interval` even where the two are equal — and many
presence rows are equal. A file mtime is one instant, so it is `point`.

The database cannot enforce this: a zero-length row satisfies both the `point`
and the `interval` constraint, and nothing in the row says which was meant.
Because `shape` is part of the natural key, a collector choosing by duration
would store the same observation twice, once under each shape, and dedup could
not save it.

## What is deliberately not here

- **Sessions.** Derived and disposable; the inference model will be refitted
  repeatedly and total presence varies 2.3× across plausible clustering
  parameters, so nothing may depend on a stored session identifier.
- **Adjustments *in `event`*.** They are overlays keyed to a wall-clock span,
  for the reason above, and they now live in tables of their own — `adjustment`
  and `adjustment_review`, added by migration 0008. Nothing there references a
  session, so a rebuild cannot touch one; nothing in `event` references them, so
  a correction can never be mistaken for an observation. See
  [adjustments](../adjustments/).
- **Contracts and periods.** Configuration-shaped, not evidence-shaped.
- **A `last_seen_run` column.** It would make the row mutable, so "immutable"
  would stop being literally true. The clean form is an append-only sighting
  table; it costs a row per event per harvest, buys nothing currently used, and
  can be added later without touching `event`.

## The surface, and why nothing reaches around it

`internal/store` exposes no database handle. Handing out a `*sql.DB` would make
"events are never edited" a convention rather than a property of the API, so
everything a command needs is a method instead.

Three of those methods exist because three separate commands, each written
without sight of the others, reached past the package to the file:

- **`Store.ReplaceDerived(ctx, derivation)`** writes a derived table. It opens
  one transaction, hands it to the derivation, commits only when the derivation
  returns without error, and rolls back on anything else including a panic. So a
  derived table is either entirely one derivation or entirely the previous one,
  and that is a property of the store rather than a discipline each derivation
  has to be told about. `internal/sessions.Replace` and `internal/chains.Rebuild`
  return the derivation; neither opens a transaction any more, and neither can.

  Measured by mutation: commit regardless of the derivation's error, and a
  replacement that fails after two rows leaves the table holding **those two
  rows instead of the previous five**. Drop the rollback instead and nothing in
  the data looks wrong at all — the abandoned transaction is invisible to every
  other connection — but it keeps the write lock, and the next rebuild fails
  with `SQLITE_BUSY` after the full five seconds of `busy_timeout`.

- **`Store.Runs(ctx, Filter)`** reads `harvest_run` back, oldest first, filtered
  the same way `Events` is. It returns rows and not a verdict: `timetattle
  status` decides what "the latest run" and "failures since it last succeeded"
  mean, in Go where a reader can see it. A source with no rows has never run,
  which is not the same fact as a source that ran and found nothing.

- **`Store.RecordCapture(ctx, Capture)`** and **`Store.Captures(ctx, Filter)`**
  are the same pair for `capture_run`, which records what the **capture** phase
  did rather than what an ingestion saw. A capture produces files and bytes, not
  events, so it does not fit in `harvest_run` and does not go there — see
  migration `0007_capture_run.sql` for the whole argument. `RecordCapture` takes
  a finished capture rather than beginning and closing one: a run has to exist
  before the events that hang off it, while a capture has nothing to hang off it
  and is fully known only once it has returned. A kind with no rows has never
  had a capture recorded, which is not the same fact as a capture that ran and
  preserved nothing new.

- **`Store.Sittings(ctx, from, to)`** is the fourth, added when reading
  `derived_session` back would otherwise have needed the same raw handle: it
  used to be `sessions.Stored(ctx, *sql.DB)`, callable only by a caller holding
  the thing this package exists not to hand out. It returns the sittings
  overlapping the window **with the derivation they came out of** — the
  parameters, and the moment they were computed — because a sitting separated
  from its parameters is a span with no meaning. The parameters are read across
  the whole table rather than the window, so a Sunday with no sittings still
  names the model it was judged under, while a zero derivation time means
  nothing has ever been derived, which is a third state and not an empty day. A
  table whose rows disagree about the derivation is refused by name rather than
  half-answered. See [sessions](../sessions/#reading-it-back).

- **`Store.LocatorCounts(ctx)`** is the fifth, added when `timetattle orient`'s
  census of unattributed evidence turned out to be reading every event into a
  `source.Event` just to run a `GROUP BY` in Go — 4.4 seconds against a
  923,000-event store, and linear in it. It aggregates in SQL instead: the
  store's total size and the span its anchors cover (`COUNT(*)` and
  `MIN`/`MAX(COALESCE(started_at, ended_at))`), plus, among observations no
  project has been attributed to, how many carry no locator at all against how
  many distinct locators appear and how many observations back each one
  (`GROUP BY locator` restricted to `project IS NULL`). It stops there
  deliberately: it does not resolve a locator against the registry and does
  not stat the filesystem, both of which reach past this package, so
  classifying what each locator's count actually means stays in
  `cmd/timetattle/orient.go`, which is the only caller that knows what a
  project registry is.

- **`store.OpenReadOnly(path)`** opens an existing store without creating it,
  migrating it, or taking a write lock, and `Store.Schema(ctx)` reports the
  migration it was last brought up to. A check that created the database would
  report a healthy store on a machine where the harvest has never once
  succeeded; a check that migrated it would report a schema no harvest had ever
  run against. Writes through a read-only store are refused by name rather than
  by SQLite's "attempt to write a readonly database".

`cmd/timetattle` imports neither `database/sql` nor the SQLite driver, so a
command cannot open a handle to the store or spell a statement against it even
by accident. `TestNoCommandReachesPastTheStoreToTheDatabase` measures that by
parsing the package's own imports. Test files are exempt: reading the columns
back through the driver is how a bug in a writer is kept from hiding behind the
reader that shares its code.

## Changing the schema

A schema change is a new file in `internal/migrations/`, numbered above the
last one:

```
internal/migrations/0001_initial.sql
internal/migrations/0002_event_cause.sql
```

A migration that adds a column to `event` must also **drop and recreate
`canonical_event`**. A view stores the query it was created with, so a view that
names its columns does not gain new ones — the column would be written and then
be invisible to `Events`, which reads the view rather than the table. Migration
0002 does this and says so.

They are embedded in the binary, ordered by that numeric prefix, and applied at
startup inside one transaction each — so a migration that fails halfway leaves
nothing behind, not even its first statement, and the next start does not apply
the remainder onto a half-built schema. Applied versions are recorded in
`schema_migration`. Never migrate a database by hand.
