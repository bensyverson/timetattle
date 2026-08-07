# Covers was a linear scan, and it was the new top of the profile

2026-08-07. Measured on an Apple M1, darwin/arm64, go1.26.4, against the live
store. Every figure below names the command that reproduces it. This is the
direct successor to `project/2026-08-07-clustering-quadratic-fix.md`, which
removed clustering's quadratic rescan and, in its own closing section, found
`internal/timespan.Covers` waiting underneath as the next hot symbol. This
document is that "next reduction."

## Call-site enumeration, and the precondition each one satisfies

`internal/timespan.Covers` had one contract before this change: scan every
span, linearly, no matter how the caller ordered them. Making it a binary
search changes that contract — the input must be sorted ascending by `Start`
and pairwise non-overlapping, or the search discards the wrong half and
returns a wrong answer without any signal that it did. Every call site in the
repository was enumerated (`grep -rn "Covers(" --include="*.go" .`) rather
than assumed:

| call site | slice passed | sorted & disjoint? |
|---|---|---|
| `internal/sessions/sessions.go:297` | `timespan.Covers(absent, s.span.Start)`, `absent = timespan.Subtract(darkU, litU)` | Yes — proved below |
| `internal/timespan/timespan.go:103` (inside `Subtract` itself) | `Covers([]Span{c}, s.Start)` | Trivially — one element |
| `internal/dayview/dayview.go:795` | `timespan.Covers([]Span{window}, a.Start)` | Trivially — one element |

The two single-element calls satisfy the precondition vacuously: there is
nothing to be out of order relative to. The one call that matters for
performance — `sessions.go:297`, called once per evidence observation, which
is what made this the top of the profile — passes `Subtract`'s output.

`Subtract` does **not** sort or merge its `from` argument on its own; it only
ever removes or splits each of `from`'s existing elements in place, so the
sortedness of its output depends on the sortedness of what it was handed.
`sessions.go:288-291` builds `absent` from `Union`'s output on both sides
(`litU, darkU := timespan.Union(lit), timespan.Union(dark)`; `absent :=
timespan.Subtract(darkU, litU)`), and `Union` guarantees a sorted, disjoint
result by construction. That chain — Union guarantees it, Subtract preserves
it, Cluster passes it straight to Covers — is what the brief for this task
called "sorted and disjoint," and it is now pinned by a test rather than
asserted in a comment: `TestSubtractOfUnionedSpansStaysSortedAndDisjoint` runs
200 randomised trials of `Subtract(Union(...), Union(...))` and checks the
output is sorted and non-overlapping every time
(`go test ./internal/timespan -run TestSubtractOfUnionedSpansStaysSortedAndDisjoint -v`).

**Decision:** change `Covers`'s contract rather than add a second function.
`internal/timespan.Intersect` faced the same tension for a different pair of
callers and chose the other way — it stays O(n²) specifically because it does
not require sorted, disjoint input, and its own doc comment says why: "the
cost of getting that precondition wrong is a silently short answer." That
reasoning does not transfer here, because every real call site was checked and
satisfies the precondition already, two of them for a reason that cannot break
(a one-element slice cannot be unsorted), and the third by a property test
against 200 randomised cases rather than by inspection of one example. Adding
a second function would have meant deciding, forever, which of two names a
future caller should reach for; the doc comment on `Covers` now states the
requirement and names why each current caller meets it.

## The benchmark, which is a pair and not a figure

`BenchmarkCovers` in `internal/timespan/bench_test.go` queries a point that
falls in the trailing gap after the last of *n* disjoint one-minute spans —
the worst case for a linear scan, which cannot short-circuit on a miss and
must walk the whole slice, and the common case for `Cluster`'s real workload,
where most evidence observations do not land inside observed darkness. Two
sizes four times apart, so the ratio between them reads directly as the growth
exponent, matching the convention `BenchmarkClusterOneRun` set in
`internal/sessions/bench_test.go`.

```
go test ./internal/timespan -run '^$' -bench BenchmarkCovers -benchtime 5x -count 5
```

Minimum of five counts, ns/op:

| spans | before | after | speed-up |
|-------|--------|-------|----------|
| 2,000 | 9,408 | 58.40 | 161x |
| 8,000 | 37,600 | 66.60 | 565x |
| **growth for 4x input** | **4.00x** | **1.14x** | |

The before-growth is 4.00x for 4x the input, exactly linear, as expected of a
scan with no early exit on a miss. The after-growth is 1.14x, close to the
1.18x that `log2(8000)/log2(2000)` predicts for a true binary search, with the
gap explained by how small the absolute times are at this size (tens of
nanoseconds, where fixed per-call overhead is a larger share of the total than
it is at 2,000 or 8,000).

## The fix

`Covers` walked `spans` end to end. It now finds the index of the first span
whose `Start` is after the query instant with `sort.Search`, and the only
span that could contain the instant is the one just before that index:

```go
i := sort.Search(len(spans), func(i int) bool {
    return spans[i].Start.After(at)
})
if i == 0 {
    return false
}
return spans[i-1].End.After(at)
```

The half-open boundary semantics are unchanged and had to be: a point exactly
at a span's `Start` is inside it, a point exactly at its `End` is not, and a
zero-length span contains nothing, not even its own instant. `Covers` had two
existing tests pinning exactly this — `TestCoversIsHalfOpen` and
`TestAZeroLengthSpanCoversNothingAtAll` — and both were run against the *old*
implementation before any change was made, to confirm they already held
rather than assume it. They did (`go test ./internal/timespan -run
'TestCoversIsHalfOpen|TestAZeroLengthSpanCoversNothingAtAll' -v`, both PASS on
`main` at 66ca619). Neither test exercises more than one span, though, so a
new test, `TestCoversFindsEachSpanInASortedDisjointRun`, was added ahead of
the implementation: three spans with gaps between them, queried at every
boundary, every gap, and both ends of the whole run. It also passed against
the old linear scan — unsurprising, since a correct implementation was never
going to fail a correctness test, linear or binary — so it is not a red test
in the sense of catching a bug the old code had. Its purpose is coverage the
suite was missing before this change: nothing previously exercised `Covers`
with more than one span, so nothing could have caught a binary search that
only ever looked at `spans[0]`.

### Proved by mutation, not by assertion

Replacing the search predicate's `spans[i].Start.After(at)` with
`!spans[i].Start.Before(at)` — the plausible off-by-one, treating a query
exactly at a span's `Start` as belonging to the *next* candidate rather than
this one — and running the suite:

```
go test ./internal/timespan -run 'TestCovers|TestAZeroLength' -v
```

Both `TestCoversIsHalfOpen` and `TestCoversFindsEachSpanInASortedDisjointRun`
fail under the mutation, every time a query lands exactly on a span's start
(`Covers(10m0s..20m0s, +10m) = false, want true`, and the analogous failure at
each of the three spans' starts in the multi-span test). The mutation was
reverted and the suite confirmed green again before moving on.

## Cross-check: byte-identical output

Determinism was established first, because a diff against a single baseline
proves nothing if the program's output varies run to run: `metrics` was
captured twice from a binary built at `main` (66ca619, before this change) and
twice from a binary built after it.

```
go build -o $TMPDIR/covers-search-tt-before ./cmd/timetattle   # at 66ca619
go build -o $TMPDIR/covers-search-tt-after  ./cmd/timetattle   # after this change
$TMPDIR/covers-search-tt-before metrics > $TMPDIR/covers-search-before-1.txt
$TMPDIR/covers-search-tt-before metrics > $TMPDIR/covers-search-before-2.txt
$TMPDIR/covers-search-tt-after  metrics > $TMPDIR/covers-search-after-1.txt
$TMPDIR/covers-search-tt-after  metrics > $TMPDIR/covers-search-after-2.txt
shasum -a 256 $TMPDIR/covers-search-{before,after}-{1,2}.txt
```

All four captures share one SHA-256 digest
(`309f47a4c36a9e395f1514dfe3806bd870eb8b6339ebc6ceb1161004b6f8272b`), so the
two before-captures agree with each other (determinism holds) and both
after-captures match both before-captures (this is a pure performance change,
not a change of result). The captures themselves stay out of this document:
live `metrics` output names real clients.

## Wall clock, before and after

```
go build -o $TMPDIR/covers-search-tt-before ./cmd/timetattle   # at 66ca619
go build -o $TMPDIR/covers-search-tt-after  ./cmd/timetattle   # after this change
/usr/bin/time -p $TMPDIR/covers-search-tt-before metrics > /dev/null
/usr/bin/time -p $TMPDIR/covers-search-tt-after  metrics > /dev/null
```

| build | run | real | user | sys |
|-------|-----|------|------|-----|
| before | 1 | 14.97s | 17.06s | 0.98s |
| before | 2 | 14.78s | 17.17s | 0.77s |
| after | 1 | 6.72s | 9.02s | 0.76s |
| after | 2 | 6.34s | 8.95s | 0.78s |

Roughly 2.2-2.3x faster wall clock (14.78-14.97s down to 6.34-6.72s). `user`
running ahead of `real` in both before and after rows indicates more than one
core doing work concurrently (most likely GC assist goroutines — see the
profile below), unlike the predecessor's fix, where `user ≈ real` throughout.

## The task carried forward from `9re2r`, and where it stands

**The under-5-second criterion is still not met. It is 6.3-6.7s, down from
14.8-15.0s.**

Two figures exist for `Covers`'s before-fix cost and they are reconciled
here rather than left to disagree quietly. The predecessor document quoted a
12-second `sample` capture with `internal/timespan.Covers` at 2,060 samples;
the task that carried this one forward cited "43% cum, 3.34s flat of a 15.7s
profile" from `scripts/profile metrics`. This document's own `go tool pprof`
capture of the pre-fix binary (same command, same build) gives **3.49s flat,
42.40% cum, of a 15.40s-sample / 15.26s-duration profile** — matching the
cited figure closely enough (43% vs 42.40%, 3.34s vs 3.49s) to call it the
same measurement with ordinary run-to-run noise, not a second thing to
explain. The `sample`-tool figure is a different tool at a different moment
and is not expected to match exactly; its 2,060-sample count cannot be
directly compared to a `flat` time without knowing that capture's sampling
rate, which was not recorded.

After this change, `timespan.Covers` does not appear anywhere in the top 20
nodes of the post-fix profile — not a smaller share, but gone. The symbol
named at the top of the previous document's "what this did not buy" section
is no longer named anywhere in this one's.

The before-fix profile above was captured directly rather than through
`scripts/profile`, because that script always builds from the working tree
and this measurement needed the pre-fix source. It is the same instrumentation
the script uses, run by hand against a binary built at 66ca619 (before this
change):

```
go build -o $TMPDIR/covers-search-tt-before ./cmd/timetattle   # at 66ca619
TIMETATTLE_CPUPROFILE=$TMPDIR/covers-search-before.pprof $TMPDIR/covers-search-tt-before metrics > /dev/null
go tool pprof -top -nodecount=15 $TMPDIR/covers-search-tt-before $TMPDIR/covers-search-before.pprof
```

The after-fix table below used the script directly, against this change's own
working tree:

```
scripts/profile metrics
```

Top of an 8.38s CPU profile (`go tool pprof -top -nodecount=20`, 122% because
GC runs on more than one core concurrently with the measured goroutine):

| symbol | flat | flat% | running sum% | cum% |
|--------|------|-------|------|------|
| `syscall.rawsyscalln` | 2.64s | 31.50% | 31.50% | 31.50% |
| `runtime.tryDeferToSpanScan` | 0.84s | 10.02% | 41.53% | 13.13% |
| `runtime.scanObject` | 0.77s | 9.19% | 50.72% | 23.99% |
| `runtime.madvise` | 0.59s | 7.04% | 57.76% | 7.04% |
| `internal/sessions.Retain` | 0.56s | 6.68% | 64.44% | 6.80% |
| `runtime.memmove` | 0.27s | 3.22% | 67.66% | 3.22% |
| `internal/timespan.Subtract` | 0.11s | 1.31% | 82.58% | 5.25% |
| `internal/metrics.Compute` | 0.08s | 0.95% | 84.61% | 17.90% |
| `internal/sessions.Cluster` | 0.06s | 0.72% | 87.95% | 12.41% |

"running sum%" is `go tool pprof`'s own running total of `flat%` down the
sorted list — the figure it uses to decide how many rows satisfy a "-nodecount"
cutoff. "cum%" is each symbol's own share of total time including everything
it calls; it is the more informative column for the last three rows, whose
`flat` time alone is small precisely because most of their cost is in
functions they call rather than in their own bodies.

The new top is not application code at all — `syscall.rawsyscalln`,
`runtime.tryDeferToSpanScan`, `runtime.scanObject`, and `runtime.madvise`
together account for 57.76% of flat time, and all four are garbage-collector
machinery (`madvise` in particular is the kernel call the GC makes to return
freed pages, and its presence at this size says the program is allocating and
discarding enough that the collector's own bookkeeping is now a bigger cost
than any one function this repository owns).

> **Correction (2026-08-07, later the same day, from
> `project/2026-08-07-alloc-churn.md`):** the claim above that all four
> symbols are garbage-collector machinery is wrong. `syscall.rawsyscalln` is
> the SQL driver reading the database file — `go tool pprof -peek
> 'syscall.syscall6$'` attributes 84% of it to `unix.pread` and 16% to
> `unix.pwrite`, and `modernc.org/sqlite` is pure-Go SQLite, so its page I/O
> appears as ordinary syscalls rather than under a cgo frame. The genuinely
> GC-attributable share of that profile was 26.25% of flat time, not 57.76%.
> The successor document carries the full attribution and what followed from
> it. The largest *application* symbol
by flat time is `internal/sessions.Retain` at 6.68% — inside
`internal/sessions`, which this task was explicitly scoped not to touch. By
cumulative time the largest application symbol is `internal/metrics.Compute`
at 17.90%, which calls `sessions.Cluster` (12.41% cum) among other things.

This is a finding, not a failure, in the same sense the predecessor document
used that phrase: the profile named a hot symbol, that symbol was genuinely
the cost, and removing it uncovered a *different kind* of cost underneath —
last time application-level quadratic behavior, this time GC pressure that no
single function in the call graph owns outright. Whoever picks up the
under-5-second criterion next should start from GC tuning or allocation
reduction (candidates: `sessions.Retain`'s allocation shape, and whatever
`internal/metrics.Compute`'s 17.90% cumulative is doing under it), not from
another linear-scan hunt — there is no comparable single hot function left to
replace this time.

## Documentation

`internal/timespan/timespan.go`'s doc comment on `Covers` now states the
sorted, non-overlapping precondition and names which real call sites satisfy
it and why, rather than leaving the requirement implicit in the
implementation. No `docs/content/docs/` page describes `Covers`'s internals
today, so there was nothing published to update; if one is added later
covering `internal/timespan`, it should carry this precondition rather than
just the binary-search claim, per this repository's rule that what ships must
stand on its own.
