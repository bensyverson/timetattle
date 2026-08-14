# Clustering was quadratic in the length of a run

2026-08-07. Measured on an Apple M1, darwin/arm64, go1.26.4, against the live
store. Every figure below names the command that reproduces it.

`internal/sessions.Cluster` decided whether the next observation joined the run
it was building by rescanning that run from index 0 to find its furthest end.
The cost of building a run of length *k* was therefore the sum of 1..k. This
records what that cost, what removing it bought, and — the part worth reading —
what it did **not** buy.

## Before

Build and time a full metrics pass:

```
go build -o $TMPDIR/tt-before ./cmd/timetattle
/usr/bin/time -p $TMPDIR/tt-before metrics
```

| run | real | user | sys |
|-----|------|------|-----|
| 1 | 76.55s | 75.52s | 1.55s |
| 2 | 80.31s | 80.34s | 1.61s |

`user ≈ real`: CPU-bound and effectively single-threaded. A 20-second `sample`
of the running process, sorted by top of stack, put **10,554 samples in
`internal/sessions.maxEnd`** and **6,307 in `time.Time.After`** — the latter
being `maxEnd`'s callee through `timespan.LaterOf`. Nothing else registered.
(That sample is the one recorded in `project/2026-08-07-cli-overhaul-plan.md`;
it is quoted rather than re-taken.)

## The benchmark, which is a pair and not a figure

`BenchmarkClusterOneRun` in `internal/sessions/bench_test.go` feeds *n*
observations spaced one minute apart under a fifteen-minute gap, which is one
unbroken run, and asserts inside the loop that exactly one sitting came out —
so a change that "optimised" the clustering by fragmenting the run cannot
benchmark well. Two sizes four times apart, so the ratio between them reads
directly as the growth exponent: 4x is linear, 16x is quadratic.

```
go test ./internal/sessions -run '^$' -bench BenchmarkClusterOneRun -benchtime 5x -count 5
```

Minimum of five counts, ns/op:

| observations | before | after | speed-up |
|--------------|--------|-------|----------|
| 2,000 | 8,343,083 | 616,000 | 13.5x |
| 8,000 | 215,115,233 | 1,912,267 | 112.5x |
| **growth for 4x input** | **25.8x** | **3.10x** | |

The before-growth of 25.8x is **worse than the 16x quadratic predicts**, and
the excess is not noise: allocation counts are flat across both columns
(76–77 allocs at 2,000, 101–102 at 8,000, unchanged by the fix), so nothing
about the memory profile changed. The rescan walks the whole run on every
append, and at 8,000 observations that working set no longer fits in L1, so
the quadratic count of comparisons is paid at a worse price per comparison.
Quadratic is the floor of the badness here, not the ceiling.

After the fix the growth is 3.10x for 4x the input — under linear, because a
fixed per-call overhead that dominated at 2,000 observations is amortised at
8,000.

## The fix

A run carries its own reach — the furthest end any member covers — updated in
place on each append instead of recomputed. `maxEnd` is deleted; the expansion
step that also called it now reads the carried value, so there is one source of
the truth rather than two computations of it.

The **semantics are unchanged and had to be**: the gap is measured from the
running maximum END, not from the previous observation's start and not from the
last-appended observation's end. Observations are sorted by start, so a long
interval early in a run can still be the latest end, and it must keep deciding
the gap after shorter observations have been appended behind it.

### Proved by mutation, not by assertion

Replacing `reach = LaterOf(reach, s.span.End)` with `reach = s.span.End` — the
plausible wrong version, "the end of whatever I appended last" — and running
the suite:

```
go test ./internal/sessions -run Test
```

- `TestTheGapIsMeasuredFromTheRunningEndNotTheStart` **passes** under the
  mutation. It has two observations, and with two the running maximum and the
  last-appended end are the same instant, so it cannot see the difference.
- `TestTheRunningEndOutlivesTheObservationsAppendedAfterIt`, added here, fails:
  a two-hour interval, a save ten minutes into it and a save ten minutes after
  it ends come back as `2` sittings spanning `-5m..15m` and `2h5m..2h15m`
  instead of one. Three observations are the minimum that can tell the two
  rules apart.
- `TestOverlappingIntervalsDoNotDoubleCount` also fails, but through the
  expansion path (the cluster's END is wrong) rather than the gap decision.

So the existing suite was not blind to this mutation, but nothing in it
exercised the *gap decision* over a run long enough for the distinction to
exist. That is the gap the new test fills.

## After

```
go build -o $TMPDIR/tt-after ./cmd/timetattle
/usr/bin/time -p $TMPDIR/tt-after metrics
```

| run | real | user | sys |
|-----|------|------|-----|
| 1 | 21.37s | 18.07s | 0.99s |
| 2 | 18.37s | 18.49s | 1.11s |

**Byte-identical output.** `metrics` was captured twice before the change and
twice after; all four captures share one SHA-256 digest, so this is a pure
performance change and not a change of result. Determinism was established
first — the two before-captures agree with each other — because a diff against
a single baseline proves nothing if the program's output varies run to run.
(The captures themselves stay out of this document: live `metrics` output names
real clients.)

## What this did not buy, and where the time went instead

**The task this work came from expected under 5 seconds. It is 18.4.** The
quadratic rescan was the largest single cost and it is gone, but it was about
three quarters of the wall clock, not all of it.

A 12-second `sample` of the post-fix binary, sorted by top of stack:

| symbol | samples |
|--------|---------|
| `time.Time.Before` | 2,717 |
| `internal/timespan.Covers` | 2,060 |
| `runtime.scanObject` | 746 |
| `internal/sessions.Cluster` | 21 |
| `internal/sessions.Retain` | 20 |

Reproduce with `$TMPDIR/tt-after metrics &` then `sample <pid> 12`. Read
`__psynch_cvwait` (76,640) as parked idle runtime workers rather than work, the
same caveat the earlier sample carried.

Clustering has left the profile: 21 samples where `maxEnd` alone had 10,554,
and `maxEnd` no longer exists. The new top is `timespan.Covers`, with
`time.Time.Before` as its callee — the same before/after pairing the old
profile had, one layer down.

`Covers` is a **linear scan** over the spans it is given
(`internal/timespan/timespan.go:147`). `Cluster` calls it once per evidence
observation to test whether that observation is anchored inside observed
darkness, so one clustering pass costs observations × absent-spans; and
`internal/metrics.reduce` calls `Cluster` once per project and once per client
(`internal/metrics/metrics.go:845`), which multiplies that product by the
number of groups.

The absent spans come out of `timespan.Subtract`, which yields them sorted and
disjoint, so a binary search is available and the scan is not load-bearing.
That is the next reduction, it is in `internal/timespan` rather than
`internal/sessions`, and it is deliberately **not** done here: this change was
scoped to keep the byte-identity proof above narrow enough to mean something.

> **Corrected 2026-08-14: the per-observation call above no longer happens, so
> the cost model in this section is stale.** `93cd2cc` narrowed what observed
> darkness may do to a derivation — it now suppresses only observations whose
> `Cause` is `source.CauseTimer` — and the test at
> `internal/sessions/sessions.go:361` reads
> `s.cause == source.CauseTimer && timespan.Covers(absent, s.span.Start)`. Go
> short-circuits `&&`, so `Covers` is reached only for timer-caused
> observations, of which the whole live store holds **38**
> (`sqlite3 ~/.timetattle/timetattle.db "select count(*) from event where
> cause='timer';"`) against 1,082,008 evidence observations. One clustering pass
> therefore costs 38 × absent-spans at worst, not observations × absent-spans.
>
> Nothing in the measurement above is retracted: `Covers` genuinely was the top
> of the profile on 2026-08-07, and the binary search that
> `project/2026-08-07-covers-binary-search.md` then built was genuinely the right
> answer to it. What changed is the workload, and the successor document carries
> the same correction against its own call-site table. See
> `project/2026-08-14-darkness-may-only-suppress-a-timer.md`.

The general lesson is the one this repository keeps relearning in a new costume:
the profile named one hot symbol, that symbol was genuinely the problem, and
fixing it revealed a second problem of the same shape underneath. A single
profile identifies the current bottleneck and says nothing about the next one.
Re-sample after every fix.
