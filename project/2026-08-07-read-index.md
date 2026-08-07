# The read was sorting a million rows, and the subquery beside it was innocent

2026-08-07. Measured on an Apple M1, darwin/arm64, go1.26.4, against copies of
the live store, which held **1,021,665 events** (1,021,650 of them canonical)
and occupied 651 MB at the time of measurement. Every figure below names the
command that reproduces it.

This is the fourth and last leg of the metrics performance work, after
`project/2026-08-07-clustering-quadratic-fix.md` (the quadratic clustering
rescan, 73s → 18s), `project/2026-08-07-covers-binary-search.md` (Covers' linear
scan, 15s → 6.5s) and `project/2026-08-07-alloc-churn.md` (13.1 GB → 4.0 GB of
allocation). Each of the three left the under-five-seconds criterion unmet and
named what it had found underneath. This one is a **schema** change and follows
the same method: determinism first, then byte-identity, then one change,
measured, with its guards proved by mutation.

The starting point was the closing section of the allocation document, which
named two things in the read layer: a `USE TEMP B-TREE FOR ORDER BY` and a
`CORRELATED SCALAR SUBQUERY`, together 2.18s of a 5.66s run.

Only one of them was real.

## Correction to `project/2026-08-07-alloc-churn.md`

That document has been corrected in place; the summary is repeated here because
it changed what this task turned out to be.

> It reads: "the `CORRELATED SCALAR SUBQUERY` — the supersession check, run once
> per row — is the other half of the read's cost", and ranks "the `ORDER BY`
> temp B-tree, **and the per-row correlated subquery**" as the largest remaining
> item.
>
> **The subquery is not run once per row, and it is not half the cost. It is 2%
> of it.**

`CORRELATED SCALAR SUBQUERY` in an `EXPLAIN QUERY PLAN` is a **node in the plan
tree, not an execution count**. The `canonical_event` view's predicate is

```sql
WHERE NOT (
    e.shape = 'unbounded'
    AND COALESCE(e.unknown, '') = 'horizon'
    AND EXISTS ( SELECT 1 FROM event AS complete WHERE … )
)
```

and SQLite short-circuits `AND`, so the `EXISTS` is evaluated only for rows that
are already unbounded horizons. The store holds sixteen of them:

```
sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
  "SELECT shape, COALESCE(unknown,'-'), COUNT(*) FROM event GROUP BY 1,2;"
```

| shape | unknown | rows |
|---|---|---|
| `interval` | — | 56,128 |
| `point` | — | 964,925 |
| `unbounded` | `horizon` | **16** |
| `unbounded` | `lost` | 71 |
| `unbounded` | `sleep` | 525 |

Sixteen evaluations, not 1,021,665. That is reasoning, so it was also measured.

### Isolating the two costs

`scripts` has no tool for this and one four-line probe did not warrant adding
one, so the measurement is four `sqlite3` invocations over a copy of the live
store, selecting the same eleven columns the store reads, with `.output
/dev/null` so that formatting cost lands identically on all four. Two runs each;
the table below gives the second of each pair, and `user` is the column to read
because `real` on this machine was contended throughout.

```
sqlite3 "file:<copy>?mode=ro" -cmd ".output /dev/null" "<query>"
```

| query | user, no index | user, with index |
|---|---|---|
| `SELECT … FROM event` | 0.87s | 0.88s |
| `SELECT … FROM canonical_event` | 0.89s | 0.91s |
| `SELECT … FROM event ORDER BY COALESCE(started_at, ended_at), id` | 1.36s | 0.95s |
| `SELECT … FROM canonical_event ORDER BY …` (**the read**) | 1.55s | 0.97s |

Reading the two rows of the left column against each other settles it. The whole
supersession predicate costs **0.02s** — the difference between scanning the
table and scanning the view. The `ORDER BY` costs **0.66s** on top of that. The
subquery is not the other half of anything.

The system time is the same finding from the other side. Without the index the
sorted read spends `sys 0.43–0.56`; with it, `sys 0.06–0.07`. Sorting a million
rows that do not fit in the page cache means writing them to a temp file, and
that is what a read-only connection was doing with `pwrite`.

## The plan, before and after

```
sqlite3 "file:<store>?mode=ro" \
  "EXPLAIN QUERY PLAN SELECT source_id, shape, unknown, started_at, ended_at, app, document,
   state, locator, project, cause FROM canonical_event ORDER BY COALESCE(started_at, ended_at), id;"
```

Before (live store at migration 0012, reproducing the figure the predecessor
recorded):

```
|--SCAN e
|--CORRELATED SCALAR SUBQUERY 3
|  `--SEARCH complete USING INDEX event_source (source_id=? AND started_at=?)
`--USE TEMP B-TREE FOR ORDER BY
```

After migration 0013:

```
|--SCAN e USING INDEX event_anchor
`--CORRELATED SCALAR SUBQUERY 3
   `--SEARCH complete USING INDEX event_source (source_id=? AND started_at=?)
```

The sort step is gone. The subquery node is still there and always will be — it
is what the view *is* — which is precisely why its presence in a plan was never
evidence of a cost.

## The migration

`internal/migrations/0013_event_anchor_index.sql`:

```sql
CREATE INDEX event_anchor ON event (COALESCE(started_at, ended_at), id);
```

One statement, and three decisions inside it.

### The expression is matched by TEXT, and a mismatch is silent

SQLite will use an expression index to satisfy an `ORDER BY` only when the
`ORDER BY` spells the expression the same way the index does.
`COALESCE(ended_at, started_at)`, `ifnull(started_at, ended_at)` and
`COALESCE(started_at, ended_at)` are three ways of writing nearly the same
thing, and only the third is matched here.

The hazard is that a mismatch **returns the right rows in the right order**,
having sorted a million of them to do it. No correctness test can see it. So
`internal/store` now keeps the `ORDER BY` in exactly one place — `eventsQuery`,
the function the read itself calls — and the plan tests assert against that
function rather than against a copy of the query. That is the only change to Go
code in this task, and it is not a behaviour change: `go test ./...` is 36 ok
before and after.

Proved by mutation. Writing the index as `COALESCE(ended_at, started_at), id`
and changing nothing else:

```
go test ./internal/migrations ./internal/store ./internal/metrics
```

`internal/metrics` stays green, every correctness test in `internal/store` stays
green, and the two plan tests fail — one of them showing the reversed index
being used for the `WHERE` while still sorting the result:

```
SEARCH e USING INDEX event_anchor (<expr>>?)
CORRELATED SCALAR SUBQUERY 3
SEARCH complete USING INDEX event_source (source_id=? AND started_at=?)
USE TEMP B-TREE FOR ORDER BY
```

That is the whole argument for the filtered-read test existing separately from
the unfiltered one: a wrong index can look used and still leave the sort behind.

### `id` is named although SQLite would append it anyway

`event.id` is `INTEGER PRIMARY KEY`, so it is the rowid, and SQLite appends the
rowid to every non-unique index key. An index on the anchor alone therefore
already breaks ties in `id` order and already satisfies this `ORDER BY` — it was
built and confirmed, same plan, same time. It is also smaller:

```
sqlite3 <copy> "SELECT name, SUM(pgsize) FROM dbstat
                WHERE name IN ('event','event_identity','event_anchor') GROUP BY name;"
```

| object | bytes |
|---|---|
| `event` (table) | 198,434,816 |
| `event_identity` | 135,958,528 |
| `event_anchor`, anchor and `id` | **41,029,632** |
| `event_anchor`, anchor alone | 37,040,128 |

Four megabytes, 10% of the index, to not depend on a chain of three facts — id
is the rowid, the rowid is the trailing key, ties therefore come back in id
order — any one of which could stop holding and put the sort back with no error
and no failing test. The explicit spelling was chosen for that reason, and
because it makes the index a transcription of the `ORDER BY`.

### The index is not UNIQUE, and what that guard can actually catch

Two observations may legitimately share an anchor: a knowledgeC point and a
Biome interval beginning at the same microsecond are two facts about that
instant. Deduplication is `event_identity`'s job, by content. A unique anchor
index would refuse the second observation at insert time and be discovered as a
failing harvest, months of evidence later.

`TestTheAnchorIndexDoesNotRefuseTwoObservationsOfOneInstant` guards it, and the
mutation is the interesting part, because it ran in both directions:

| mutation | result |
|---|---|
| `CREATE UNIQUE INDEX … (COALESCE(started_at, ended_at))` | **fails** — `UNIQUE constraint failed: index 'event_anchor'` |
| `CREATE UNIQUE INDEX … (COALESCE(started_at, ended_at), id)` | passes |

The second one cannot fail: `id` is the primary key, so every key containing it
is already distinct and the `UNIQUE` has nothing to refuse. That is a third
argument for naming `id` — it disarms this class of mistake entirely — and it is
also an honest limit on the test, recorded in the test's own comment. It guards
the plausible wrong design, not the impossible one.

Note also what this index does **not** need. `event_identity` wraps its nullable
columns in `COALESCE(col, '')` to defeat SQLite's rule that NULLs are DISTINCT
in a unique index. The `COALESCE` here looks the same and is there for an
entirely different reason — it *is* the sort key — and since the index is not
unique the NULL rule has nothing to act on. Two identical spellings, two
unrelated arguments.

## The tests, red first

Three new tests in `internal/migrations/anchor_index_test.go`, two in
`internal/store/eventsplan_test.go`. Both files read query plans rather than
rows, because the property at issue is not what comes back but how it is
produced, and nothing about a missing index is visible in a result.

The migration test uses the existing `tsAt(t, 12)` harness, which brings a
database to exactly version 12 and no further, and then calls
`migrations.Apply` — **the automatic path**, the same function `store.Open`
calls. It asserts the before-state as well as the after-state, and fails loudly
if the before-state ever stops holding, because an after-assertion alone would
pass against a database that had never sorted.

Red, against the schema at 0012:

```
go test ./internal/migrations ./internal/store -run 'AnchorIndex|WalksTheAnchor|WithoutASort'
```

```
--- FAIL: TestTheAnchorIndexTakesTheSortOutOfTheEventRead
    there is no migration above version 12
--- FAIL: TestTheAnchorIndexAlsoServesTheReadOfTheEventTable
    reading the event table sorts through a temporary B-tree:
    SCAN event / USE TEMP B-TREE FOR ORDER BY
--- FAIL: TestTheUnfilteredEventReadWalksTheAnchorIndex        (both relations)
--- FAIL: TestAFilteredEventReadStillComesBackWithoutASort
```

Green after the migration file lands, all five.

One honest deviation from the red/green rule. The third test —
the not-UNIQUE guard — **was green at the red stage**, because with no index at
all there is nothing to refuse. The rule says such a test is testing nothing and
should be removed or refactored. It was kept, and the mutation table above is
why: it discriminates a design somebody could plausibly write. Its comment now
states exactly which mutation it catches and which it cannot, so nobody reads it
as guarding more than it does.

An incidental finding worth keeping: an **empty** database still shows `USE TEMP
B-TREE FOR ORDER BY` before the migration and `SCAN … USING INDEX event_anchor`
after. The planner reaches this decision structurally rather than from row
counts, so these tests need no fixture data to mean what they say.

## Byte identity

`timetattle metrics` prints the store path as its first line, so a capture taken
against a copy cannot be byte-identical to one taken against the original. That
is dealt with by measuring on **one path throughout** and reconciling the copy to
the live store separately, rather than by excusing a difference after the fact.

Two captures at HEAD (b28a73c) against the live store, read-only:

```
/usr/bin/time -p ./tt-before metrics > <capture>
shasum -a 256 <capture>
```

```
fb3dbd9aff61513ac2b4a73bed677bb12b69d96628e13c13373f7a26809957d5   (twice)
```

That is the digest of record carried from the allocation task, so determinism is
established and the baseline is confirmed before anything is compared to it.

The copy is then shown to be a faithful stand-in: its capture differs from the
live one at line 1 and nowhere else, and the two bodies from line 2 onward share
one digest.

```
tail -n +2 <capture> > <body>;  shasum -a 256 <body>
709d57589fd401b06de2fc8b5f469f45396434933c3d9df10782baff214ab736   (both)
```

Then four captures on the copy, at one path, across the migration:

| # | binary | store state | SHA-256 |
|---|---|---|---|
| 1 | HEAD | 0012, no index | `454d1925…f363` |
| 2 | HEAD | 0012, no index | `454d1925…f363` |
| 3 | this branch | 0013, index | `454d1925…f363` |
| 4 | this branch | 0013, index | `454d1925…f363` |

Determinism first (1 = 2), then before equals after (1 = 3 = 4). Full digest
`454d1925655e78414ddff793bb2211afda0097f0fe228b9223d2d770b2d1f363`. Captures live
in the shared temp directory under a `read-index-` prefix and are quoted nowhere:
they name real clients.

## Nothing was migrated by hand, and the live store was not migrated at all

Two facts about migration-on-open that matter more than they look, and that the
measurement protocol had to be built around.

**`metrics` cannot migrate anything.** It opens the store with
`store.OpenReadOnly`, which by design does not create, does not migrate and
takes no write lock. So do `orient`, `status`, `fit`, `headline` and `review`.
Only `harvest`, `sessionize`, `chains`, `adjust` and `register` open for
writing, and only those call `migrations.Apply`. Verified after the fact rather
than assumed: the live store is still at version 12 with no `event_anchor` after
every read-only run described above.

```
sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
  "SELECT MAX(version) FROM schema_migration;
   SELECT COUNT(*) FROM sqlite_master WHERE name='event_anchor';"
→ 12
→ 0
```

**The consequence is that the index arrives on the next write.** A store gets
0013 the first time a writing command opens it, and until then every reading
command goes on sorting exactly as before. Nothing is broken by the gap — an
index is not a schema a reader depends on — but it is the reason "run metrics
and see" would not have shown the improvement.

The copy used for the after-measurements was therefore migrated by running a
**shipped command** against it, not by typing SQL:

```
./tt-after sessionize --store <copy> --dry-run --quiet
```

`--dry-run` computes and writes no derived rows, but `store.Open` still runs
first, so the migration applies and nothing else changes. (`metrics` reads only
events and the client map, so even a non-dry `sessionize` could not have moved
the output — but the weaker action is the one to take.)

**What the migration costs a real store.** The same command against an
already-migrated copy takes 2.99s against 5.29s for the copy that had to build
the index, so the one-time build over 1,021,665 rows is roughly **2.3s** (both
figures contended; `/usr/bin/time -p`).

## Wall clock

Seven back-to-back A/B pairs, **one binary** against two copies of one store
differing only in whether 0013 has been applied, so the index is the only
variable. The machine was contended throughout by another agent — load average
4.2 to 5.2 across the run — which inflates `real` much more than `user`.

```
/usr/bin/time -p ./tt-after metrics --store <copy>
```

| | no index | with index |
|---|---|---|
| `real`, median of 7 | 6.02s | 5.43s |
| `real`, range | 5.86 – 6.58s | 5.31 – 6.00s |
| `user`, median of 7 | 5.85s | 4.82s |
| `user`, range | 5.58 – 6.01s | 4.76 – 5.29s |
| `sys`, range | 0.75 – 0.86s | 0.50 – 0.64s |

CPU time falls by about **1.0s, 17%**; system time by about 0.28s, a third. The
single run taken while nothing else happened to be scheduled came in at **4.08s
real / 4.64s user**, and the profiled run below has a 4.54s duration.

**The under-five-seconds criterion is reported, not claimed.** No measurement
here was taken on a quiet machine, so this document does not assert one. What it
can say: `user` time, which contention inflates least, is 4.76–5.29s and its
median is under five seconds; the best observed `real` is 4.08s; and the median
contended `real` is 5.43s. A quiet-machine number is the integrator's to take,
as it was for each of the three predecessors.

## The profile, matched pair

Same binary, same corpus, twenty seconds apart, one with the index and one
without.

```
scripts/profile -o <dir> -n 20 metrics --store <copy>
go tool pprof -peek 'syscall.syscall6$' <binary> <dir>/metrics.pprof
```

| | no index | with index |
|---|---|---|
| profile duration | 6.47s | 4.54s |
| `syscall.rawsyscalln`, flat | 2.46s (38.92%) | 1.25s (25.00%) |
| ↳ `unix.pread` | 1.90s (77.55%) | 1.25s (**100%**) |
| ↳ `unix.pwrite` | **0.55s (22.45%)** | **absent** |

**The `pwrite` on a read-only connection is gone entirely.** That was the
symptom the allocation document ended on — a command that opens the store
read-only and nevertheless spends half a second writing — and there is now
nothing in the profile that writes. `pread` falls too, from 1.90s to 1.25s,
because the sort was reading its own spill file back.

What is left, with the index (top of a 5s sample, 4.54s duration):

```
1.25s 25.00%  syscall.rawsyscalln          ← unix.pread, 651 MB of pages
0.67s 13.40%  runtime.madvise
0.51s 10.20%  internal/sessions.Retain     (0.62s cum)
0.40s  8.00%  runtime.tryDeferToSpanScan
0.32s  6.40%  runtime.scanObject           (0.86s cum)
              internal/metrics.Compute      1.36s cum, 27.20%
```

## What is left, and what it is not

The read layer is no longer the largest item. Two things now share the top, and
neither is another index:

1. **Reading 651 MB of database pages, 1.25s of `pread`.** This is the floor of
   loading a million events into memory from a file that does not fit in the
   page cache. It moves by reading fewer bytes — a narrower projection, or not
   materialising the whole corpus — not by a query-plan change.
2. **Garbage collection, roughly 28% of flat time** (`madvise`,
   `tryDeferToSpanScan`, `scanObject`), on top of `sessions.Retain`'s 0.51s.
   The allocation document ranked `Retain`'s 203 MB full-corpus copy second on
   its own list and left it alone deliberately: removing it means changing a
   documented purity contract, which is an API decision rather than a tuning
   one. It is still the largest single application symbol.

**Concurrency is still not indicated by anything measured here**, and this
document does not make that call either. What has changed since the allocation
document is that the argument against it — "parallelising the phases downstream
of the read would be parallelising the smaller half" — is now weaker in an
interesting way: the read is smaller, so the downstream half is the larger one.
That makes the parallelism question live for the first time, with the numbers
above attached to it.

## Documentation

No page under `docs/content/docs/` describes the schema's indexes or the shape
of the event read, so there was nothing published to update. The reasoning is
carried where a reader will meet it: the migration file states why the
expression is spelled as it is, why `id` is named, why the index is not unique
and when a real store will pay for it; `eventsQuery` in `internal/store` states
why the `ORDER BY` has exactly one spelling in the package; and the tests state
which mutations they catch. None of it cites this document, per the rule that
what ships must stand on its own.
