# The GC pressure was allocation churn, and a third of the clock was never GC at all

2026-08-07. Measured on an Apple M1, darwin/arm64, go1.26.4, against the live
store, which held **1,021,650 events** at the time of measurement. Every figure
below names the command that reproduces it. This is the third and last leg of
the metrics performance work, after
`project/2026-08-07-clustering-quadratic-fix.md` (the quadratic clustering
rescan) and `project/2026-08-07-covers-binary-search.md` (Covers' linear scan).
Both predecessors set the method this one follows: determinism first, then
byte-identity, then one change at a time, each measured on its own, each guard
proved by mutation.

The starting point was the closing section of the Covers document, which named
`syscall.rawsyscalln`, `runtime.scanObject`, `runtime.madvise` and
`runtime.tryDeferToSpanScan` — 57.76% of flat time — as **garbage-collector
machinery**, and pointed the next reduction at allocation.

## Correction to `project/2026-08-07-covers-binary-search.md`

> That document's closing section reads: "`syscall.rawsyscalln`,
> `runtime.tryDeferToSpanScan`, `runtime.scanObject`, and `runtime.madvise`
> together account for 57.76% of flat time, and all four are garbage-collector
> machinery".
>
> **`syscall.rawsyscalln` is not garbage-collector machinery.** It is the SQL
> driver reading the database file. Asking pprof who calls it settles it in one
> command:
>
> ```
> go tool pprof -peek 'syscall.syscall6$' $TMPDIR/alloc-churn-tt-before $TMPDIR/alloc-churn-head-cpu.pprof
> ```
>
> ```
>                                    2.29s 83.88% |   golang.org/x/sys/unix.pread
>                                    0.44s 16.12% |   golang.org/x/sys/unix.pwrite
>          0     0%     0%   2.73s 29.71%         | syscall.syscall6
> ```
>
> `modernc.org/sqlite` is a pure-Go SQLite, so its page reads are ordinary
> `pread` calls in the profile rather than anything belonging to the runtime.
> In that document's own 8.38s profile the three genuinely-GC symbols
> (`tryDeferToSpanScan` 0.84s, `scanObject` 0.77s, `madvise` 0.59s) came to
> **26.25%** of flat time, not 57.76%; in the profile taken here at 175c98a
> they come to 28.9%. The correction matters because it changes what the
> remaining work is:
> a third of the clock was in the read layer, where no amount of allocation
> tuning reaches it, and the closing section of this document is about exactly
> that.

## The allocation profile, before any fix

There is no memory-profile hook in the binary, and the fixture store in
`internal/storefixture` carries a few dozen events — enough to establish
behaviour, and far too few to make an allocation profile mean anything, since
nothing in it grows a slice past its first doublings. So the instrument is a
new benchmark over the live store,
`BenchmarkLivePipeline` in `internal/metrics/pipeline_bench_realdata_test.go`,
which runs exactly what `timetattle metrics` runs between opening the store and
formatting a report, phase by phase and then together. It is behind
`//go:build realdata` for the reason `internal/corpusgate` exists.

```
go test -tags realdata ./internal/metrics -run '^$' -bench BenchmarkLivePipeline -benchtime 1x -benchmem -v
```

| phase | ns/op | B/op | allocs/op |
|---|---|---|---|
| `events` (store read) | 4,541,226,375 | 1,933,189,504 | 26,524,671 |
| `retain` (exclusions) | 109,909,959 | 212,508,720 | 2 |
| `stamp` (client fold) | 32,672,125 | 0 | 0 |
| `compute` (cluster + attribute) | 1,710,533,708 | 7,294,045,400 | 4,270,444 |
| `all` | 7,602,244,583 | 13,135,956,640 | 32,798,800 |

The parts do not sum to `all`, and the difference is not noise: `compute` is
run against events that have **not** been client-stamped, so it builds no
client groups, while `all` stamps first and therefore clusters once per client
as well. `all` is the figure to compare against the command.

The sites, from the same benchmark under `-memprofile`:

```
go test -tags realdata ./internal/metrics -run '^$' -bench 'BenchmarkLivePipeline/events' \
  -benchtime 1x -memprofile $TMPDIR/alloc-churn-events.mem -memprofilerate 1 \
  -o $TMPDIR/alloc-churn-metrics.test
go tool pprof -top -sample_index=alloc_space $TMPDIR/alloc-churn-metrics.test $TMPDIR/alloc-churn-events.mem
go tool pprof -list '<symbol>' -sample_index=alloc_space ...
```

| site | bytes | why |
|---|---|---|
| `store.(*Store).events`, `out = append(out, e)` | 1.09 GB | the result slice's own growth |
| `store.(*Store).events`, ten loop-local scan destinations | 0.42 GB / 10.2M objects | escape to the heap once per row |
| `sessions.Cluster`, `evidence` / `kept` / `covered` appends | 1.55 GB | unsized buffers |
| `timespan.Subtract`, `next := make(...)` inside the cut loop | 1.89 GB / 3.9M objects | a new run allocated per cut |
| `timespan.Union` under `Subtract`, per cluster | 0.71 GB | re-deriving a union that already held |
| `metrics.Compute`, `byProject` / `byClient` / `observedRaw` | 1.27 GB | unsized buckets |
| `modernc.org/sqlite` `columnText` and `rows.Next` | 0.31 GB / 14.0M objects | the driver's own, not reachable from here |

## Determinism first, then byte-identity

A diff against a single baseline proves nothing if the program's output varies
run to run, so `metrics` was captured twice before any change and twice after
the last one. Captures live in `$TMPDIR` and are never quoted anywhere: live
`metrics` output names real clients.

```
go build -o $TMPDIR/alloc-churn-tt-before ./cmd/timetattle    # at 175c98a
go build -o $TMPDIR/alloc-churn-tt-after2 ./cmd/timetattle    # after every change below
$TMPDIR/alloc-churn-tt-before metrics > $TMPDIR/alloc-churn-before-1.txt
$TMPDIR/alloc-churn-tt-before metrics > $TMPDIR/alloc-churn-before-2.txt
$TMPDIR/alloc-churn-tt-after2 metrics > $TMPDIR/alloc-churn-after2-1.txt
$TMPDIR/alloc-churn-tt-after2 metrics > $TMPDIR/alloc-churn-after2-2.txt
shasum -a 256 $TMPDIR/alloc-churn-{before,after2}-{1,2}.txt
```

All four share one SHA-256 digest,
`309f47a4c36a9e395f1514dfe3806bd870eb8b6339ebc6ceb1161004b6f8272b` — the same
digest the Covers document recorded at 1f62745, so the Cobra migration at
175c98a did not move it either. The two before-captures agreeing establishes
determinism; the after-captures matching them makes every change below a pure
performance change.

## Six changes, each measured on its own

Every row is `BenchmarkLivePipeline` at `-benchtime 1x -benchmem`, run
immediately after the change and compared against the row above it. `all` is
the pipeline figure; the phase column is the one the change is in.

| # | change | phase B/op | phase allocs/op | `all` B/op | `all` ns/op |
|---|---|---|---|---|---|
| 0 | (baseline at 175c98a) | 1,933 MB `events` | 26,524,671 | 13,136 MB | 7,602 ms |
| 1 | scan destinations hoisted out of the row loop | 1,500 MB | 16,308,180 | 12,703 MB | 6,264 ms |
| 2 | `Subtract` reuses two buffers | 5,250 MB `compute` | 201,147 | 10,219 MB | 6,094 ms |
| 3 | `Compute` sizes its buckets before filling them | 4,167 MB | 200,440 | 8,275 MB | 5,715 ms |
| 4 | `Cluster` sizes `evidence`, filters `kept` in place, reuses `covered` | 2,727 MB | 174,161 | 6,058 MB | 5,359 ms |
| 5 | `Subtract` skips a union the caller already has | 2,001 MB | 104,384 | 4,989 MB | 5,409 ms |
| 6 | the read counts its rows before allocating for them | 545 MB `events` | 16,308,157 | 4,035 MB | 4,969 ms |

**13.14 GB to 4.03 GB, and 32.8M allocations to 16.5M.** What remains of the
allocation count is almost entirely the SQL driver's: `columnText` and
`rows.Next` decode eleven TEXT columns into fresh Go strings on every one of
1,021,650 rows, which is ~14 objects a row and not something this repository
can change without abandoning `database/sql`.

### 1. Ten scan destinations, allocated a million times

`rows.Scan` takes the address of each destination, so a destination declared
inside the row loop escapes and is heap-allocated on every iteration.
`go build -gcflags=-m ./internal/store` named all ten:

```
internal/store/store.go:316:4: moved to heap: e
internal/store/store.go:317:4: moved to heap: unknown
... eight more
```

Declaring them once above the loop and reusing them removed exactly
10,216,491 allocations, which is 10.00 per row over 1,021,650 rows. Reuse is
safe because every field of `e` is assigned from the current row before it is
appended — which is why `Identity` is written as a whole struct literal rather
than field by field, and why the comment there says so.

Pinned by `TestReadingEventsAllocatesNoMorePerRowThanTheDriverItself` in
`internal/store/eventsalloc_test.go`. **The ceiling calibrates itself**: the
test measures the driver's own per-row cost first, over the same relation and
the same eleven columns through `database/sql` directly, and then requires the
store's reader to cost no more than that plus two. A hard number would have
been a guard against one version of one driver.

Red against the old shape, green after, and the gap is the whole finding:

```
go test ./internal/store -run TestReadingEventsAllocatesNoMorePerRowThanTheDriverItself -v
```

```
reading 4000 events allocated 24.0 objects a row against the driver's own 14.0
```

### 2. Subtract allocated a whole run per cut

`Subtract` walked its cuts, allocating a fresh output run for each one. The
allocation count was therefore the *cut* count, and clustering subtracts a run
of observed darkness thousands of spans long once per project and once per
client. Two buffers, swapped each pass, replace it.

`TestSubtractAllocationsDoNotScaleWithTheNumberOfCuts` is a pair and not a
figure, following `BenchmarkClusterOneRun` and `BenchmarkCovers`: 2,000 cuts
and 8,000, so the ratio reads directly as the growth exponent.

```
go test ./internal/timespan -run TestSubtractAllocationsDoNotScaleWithTheNumberOfCuts -v
```

| cuts | before | after |
|---|---|---|
| 2,000 | 2,018 allocations | 43 |
| 8,000 | 8,023 | 57 |
| **growth for 4x the cuts** | **3.98x** | **1.33x** |

**Proved by mutation.** Reusing the buffers without swapping them — replacing
`remaining, next = next, remaining` with the old `remaining = next` — leaves
both names pointing at one array, so the next pass truncates and overwrites the
run it is reading. `go test ./internal/timespan` fails with
`Subtract's output overlaps: ... 1h35m0s..1h45m0s 1h35m0s..1h45m0s
1h35m0s..1h45m0s` in `TestSubtractOfUnionedSpansStaysSortedAndDisjoint`. The
mutation was reverted and the suite confirmed green again.

### 3 and 4. Buckets sized before they are filled

A slice grown by `append` allocates about **five** times the bytes it ends up
holding. Above 256 elements Go grows a slice by roughly 1.25x, and the sum of
that geometric series is `1/(1-0.8) = 5` times the final size; every regrowth
copies into a new array and abandons the one before it, and all of it is
garbage the collector then walks.

In `metrics.Compute` a counting pass over the events sizes `byProject`,
`byClient` and `observedRaw` before anything is put in them. **The pass is a
hint and not a contract**: if it ever counted differently from the pass it
sizes, `append` still produces exactly the same buckets, at the old cost.
Nothing there can make a result wrong, which is why it is allowed to duplicate
two conditions rather than being fused into the loop.

In `sessions.Cluster`, `evidence` is sized for the worst case (every event is
evidence), `covered` is hoisted out of two loops and truncated per piece rather
than reallocated, and `kept` is filtered **in place** over `evidence`'s own
array — the standard Go filter, safe here because `evidence` is dead the moment
that loop ends and everything downstream copies observations by value rather
than holding the slice.

`BenchmarkClusterOneRun`, which the first document in this trail introduced,
carries a portable measurement of changes 2 and 4 together — it exercises
`Cluster`, which calls `Subtract`, so the two cannot be separated in it, and it
is quoted here as the pair it is rather than attributed to change 4 alone:

```
go test ./internal/sessions -run '^$' -bench BenchmarkClusterOneRun -benchtime 5x -count 3 -benchmem
```

Minimum of three counts:

| observations | before B/op | after B/op | before allocs | after allocs |
|---|---|---|---|---|
| 2,000 | 1,887,104 | 1,251,072 | 77 | 52 |
| 8,000 | 10,349,443 | 6,559,491 | 102 | 67 |

### 5. A union derived once and re-derived thousands of times

`Subtract` unions its cut argument on every call, and `sessions.Cluster` hands
it the same run of observed darkness once per cluster — a run `Subtract` itself
produced. `Subtract` now skips both the union and its copy when the run it is
given is already sorted and disjoint, which it reads with a new `isUnioned`.

**The first attempt at this bought almost nothing, and that is the useful
part.** Putting the shortcut inside `Union` instead saved 9 MB of 6 GB, because
the run being handed to it was *not* quite a union: a zero-length lit reading
lying strictly inside a dark span splits it into two pieces that **touch** at
an instant, and `Union` merges touching spans. The live corpus has such
readings. So `sessions.Cluster` now unions once, at the point `absent` is
built, and hands a genuine union down. Touching is invisible to everything
downstream — the same instants are covered, the same total elapses, and
subtracting `[a,b)` then `[b,c)` removes what `[a,c)` does — but it is the
difference between a run `Subtract` and `Covers` can take cheaply and one they
cannot.

The lesson is the predecessor documents' one in a new costume: a plausible
mechanism, profiled rather than assumed, turned out to be blocked by a property
nobody had looked at. The prediction was 700 MB, the measurement was 9 MB, and
the difference was a single zero-length span.

`TestUnionOfARunThatIsAlreadyAUnionOnlyCopiesIt` pins both the answer and the
cost — the spans come back unchanged and unaliased, in one allocation rather
than 17.

**Proved by mutation.** Dropping the gap half of `isUnioned` and keeping only
the sortedness half makes `Union` return an unmerged run, and
`go test ./internal/timespan ./internal/sessions` fails in two packages:
`TestSubtractOfUnionedSpansStaysSortedAndDisjoint` with overlapping output, and
`TestOverlappingIntervalsDoNotDoubleCount` with `margin 0s, want 10m`. Notably
`TestUnionMergesOverlappingAndTouchingSpans` does **not** catch it — Union's own
merge test does not present its input in an order that reaches the shortcut —
so the guard is held by the two callers' tests rather than by the obvious one.
The mutation was reverted and the suite confirmed green again.

### 6. The read counts its rows before allocating for them

The result slice's own growth was the single largest allocation in the program
at 1.09 GB. Removing it needs the row count, and the only honest source of that
is the database, so `events` now asks `SELECT COUNT(*)` over the same relation
and the same `WHERE` before it reads.

The obvious objection is that this is a second full scan. It was measured
rather than argued: a paired, back-to-back run of the two binaries put the
counting build at 5.63-5.90s against 6.05s for the non-counting one, so the
count pays for itself, and the allocation figure fell from 1,500 MB to 545 MB
for the phase.

It is a **hint and cannot make the result wrong**. The store is opened
read-only while a harvest may be writing to it, so the count and the read are
two moments and may disagree; more rows and `append` grows the slice as before,
fewer and the slice is merely longer than it needed to be. Nothing is trusted
as a length, which is also why the count's own error is swallowed: a failed
hint is a slower read, not a failed one.

## Wall clock, and the criterion

Another agent was working on this machine throughout, so wall-clock numbers are
only meaningful **back to back** and are labelled contended. `user` is the more
robust column here, being less sensitive to which core was available when.

```
go build -o $TMPDIR/alloc-churn-tt-before ./cmd/timetattle   # at 175c98a
go build -o $TMPDIR/alloc-churn-tt-after2 ./cmd/timetattle   # after all six changes
/usr/bin/time -p $TMPDIR/alloc-churn-tt-before metrics > /dev/null
/usr/bin/time -p $TMPDIR/alloc-churn-tt-after2 metrics > /dev/null
```

| pair | build | real | user | sys |
|---|---|---|---|---|
| 1 | before | 7.35s | 8.94s | 0.90s |
| 1 | after | 5.54s | 5.44s | 0.74s |
| 2 | before | 6.58s | 8.99s | 0.76s |
| 2 | after | 5.43s | 5.74s | 0.68s |

`user` falls from 8.94-8.99s to 5.44-5.74s, a 36-39% reduction that contention
cannot explain away, and `user` no longer runs ahead of `real` — the extra core
in the before rows was GC assist work, and there is much less of it to do.

**The under-five-seconds criterion is not met. It is 5.4-5.5s real on a
contended machine, from 6.6-7.4s.** A quiet-machine number is the integrator's
to take. The shortfall is reported below with the profile that explains it,
which is what the criterion asks for when it is not reached.

## Where the remaining time is, and why allocation work will not reach it

```
TIMETATTLE_CPUPROFILE=$TMPDIR/alloc-churn-final-cpu.pprof $TMPDIR/alloc-churn-tt-after2 metrics > /dev/null
go tool pprof -top -nodecount=20 $TMPDIR/alloc-churn-tt-after2 $TMPDIR/alloc-churn-final-cpu.pprof
```

Top of a 5.66s-duration / 5.90s-sample profile:

| symbol | flat | flat% | cum% |
|---|---|---|---|
| `syscall.rawsyscalln` | 2.19s | 37.12% | 37.12% |
| `runtime.madvise` | 0.61s | 10.34% | 10.34% |
| `internal/sessions.Retain` | 0.43s | 7.29% | 9.32% |
| `runtime.scanObject` | 0.37s | 6.27% | 14.41% |
| `runtime.tryDeferToSpanScan` | 0.31s | 5.25% | 7.12% |
| `internal/store.(*Store).events` | 0.14s | 2.37% | 43.90% |
| `internal/metrics.Compute` | 0.08s | 1.36% | 17.29% |
| `internal/sessions.Cluster` | 0.08s | 1.36% | 12.54% |

GC has fallen from 28.9% of flat time to 21.9%, and `internal/store` now
accounts for 43.90% cumulative — nearly half the run, in the read layer.

**37% of the remaining clock is `pread` and `pwrite`, and none of it is
allocation.**

```
go tool pprof -peek 'syscall.syscall6$' $TMPDIR/alloc-churn-tt-after2 $TMPDIR/alloc-churn-final-cpu.pprof
```

```
                                   1.72s 78.90% |   golang.org/x/sys/unix.pread
                                   0.46s 21.10% |   golang.org/x/sys/unix.pwrite
```

The 0.46s of **`pwrite` on a read-only connection** is SQLite spilling a
temporary B-tree to disk, and the query plan says why:

```
sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
  "EXPLAIN QUERY PLAN SELECT source_id, shape, unknown, started_at, ended_at, app, document,
   state, locator, project, cause FROM canonical_event ORDER BY COALESCE(started_at, ended_at), id;"
```

```
|--SCAN e
|--CORRELATED SCALAR SUBQUERY 3
|  `--SEARCH complete USING INDEX event_source (source_id=? AND started_at=?)
`--USE TEMP B-TREE FOR ORDER BY
```

No index can satisfy `ORDER BY COALESCE(started_at, ended_at), id`, so every
read of the store sorts a million rows through a temp file. An **expression
index** on `COALESCE(started_at, ended_at), id` would remove that sort, and the
`CORRELATED SCALAR SUBQUERY` — the supersession check, run once per row — is
the other half of the read's cost. Both are schema work and belong in a
migration, which is a different discipline from this task and was deliberately
not attempted here.

> **Correction, 2026-08-07, from `project/2026-08-07-read-index.md`.** The
> paragraph above is right about the sort and wrong about the subquery. The
> supersession check is **not** run once per row, and it is not "the other half
> of the read's cost" — it is 2% of it.
>
> `CORRELATED SCALAR SUBQUERY` in a query plan is a NODE, not an execution
> count. The view's predicate reads
> `NOT (shape = 'unbounded' AND COALESCE(unknown, '') = 'horizon' AND EXISTS (…))`,
> and SQLite short-circuits the `AND`, so the `EXISTS` is reached only by rows
> that are already unbounded horizons. There are **16** of those in 1,021,665:
>
> ```
> sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
>   "SELECT shape, COALESCE(unknown,'-'), COUNT(*) FROM event GROUP BY 1,2;"
> ```
>
> Measured rather than argued, over a copy of the live store, selecting the same
> eleven columns with `.output /dev/null` (second of two runs, user time): the
> table without the `ORDER BY` costs 0.87s and the view without it 0.89s, so the
> whole supersession predicate is **0.02s**. Adding the `ORDER BY` to the view
> takes it to 1.55s. The sort was the entire difference, and the index alone
> removes it: `pwrite` falls from 0.55s to **zero** in a matched profile pair.
>
> The failure mode is worth naming, because a plan node that names a subquery
> looks like a cost and reads like one. Neither predecessor measured it, and it
> was carried forward into the successor task's brief as fact.

### The next reductions, in the order the profile ranks them

1. **The `ORDER BY` temp B-tree, and the per-row correlated subquery in
   `canonical_event`.** 2.18s of a 5.66s run is in the read layer's syscalls.
   This is the largest remaining item by a wide margin and it is a schema
   question, not a Go one.
   > **Corrected, 2026-08-07:** the subquery is not per-row and is not part of
   > this item; see the correction block above. The temp B-tree alone accounts
   > for it, and migration 0013 removes it —
   > `project/2026-08-07-read-index.md`.
2. **`sessions.Retain`, 0.43s flat.** It allocates and fills a second copy of
   the whole corpus, 203 MB, because its documented contract is purity: "the
   caller's slice is neither reordered nor written to". The only ways to remove
   that copy are to let it filter in place or to fuse it into the read, and
   both change a public contract. The single caller in `cmd/timetattle`
   immediately writes into what it gets back, so the copy buys nothing there —
   but that is an API decision rather than a tuning one, and it is not made
   here. Note also that its cost in the benchmark (110 ms) and in the profile
   (0.43s) disagree, and the reconciliation is that the benchmark reuses a hot
   229 MB span while the command allocates a cold one, which is page-fault time
   — the same cost, charged to `madvise` in one measurement and to `Retain` in
   the other.
3. **`clusters[n-1].members`, 459 MB.** A cluster's members are contiguous in
   `kept`, so they could be a capacity-capped sub-slice of it rather than a
   grown copy. That is a shape change with an aliasing hazard and was left
   alone.

### On concurrency

The plan defers parallelism to a measured decision, and this document does not
make it. What it can say is that **the remaining path to under five seconds is
not allocation**: after these six changes, 37% of the run is file I/O and SQL
execution in one goroutine, and the largest single lever available is an index,
not a thread. Parallelising the phases downstream of the read would be
parallelising the part that is already the smaller half. If the schema work in
(1) lands and the run is still over five seconds, that is the moment the
concurrency question has real numbers attached to it.

## Documentation

No page under `docs/content/docs/` describes the internals of any of the four
functions changed, so there was nothing published to update. The reasoning for
each change is carried in the code comment beside it rather than by a citation
to this document, per this repository's rule that what ships must stand on its
own: the row-loop escape and its self-calibrating guard, the two swapped
buffers and why swapping is load-bearing, why the bucket-sizing pass is a hint
and cannot make a result wrong, why the in-place filter is safe and what must
stay true for it to remain so, why `absent` is unioned before it is used, and
why a failed row count is not a failed read.
