# The activity mix: what kind of work, measured as a union

2026-08-13. Agent `mixer`, task FLXUP. `timetattle metrics` could report that a
week held forty hours and could not say how much of that was Claude, how much
was git, how much was a file changing on disk. This document records what was
built, the mapping argument, what it costs, and one finding about the brief that
turned out to matter more than the feature.

## The finding: the family taxonomy already existed and was already Ben's

The task asked for a design call — "argue the mapping rather than assuming it,
and note that `fs-observations` and `job-claims` both need a deliberate home".

**Both already had one.** `cmd/timetattle/dashboard_sources.go` has shipped a
seven-family rollup since 2026-08-07 (task 57dP7, integrated at `9f9bbbc`):
`Apps`, `Blender`, `Claude`, `Files`, `Git`, `Jobs`, `Motion`. Its own notes
record that `fs-observations` was deliberately kept as its own `Files` family
rather than folded in with the creative applications, and that **Ben confirmed
that decision mid-flight**. `job-claims` and `job-events` were put in one
`Jobs` family by a prefix rule over the collector's namespace.

So the mapping was not an open question, and inventing a second one — the
`agent` / `scm` / `creative` / `ambient` shape the task sketched — would have
shipped two taxonomies in one tool, with `dashboard` calling an hour Claude's
and `metrics` calling it something else and nobody subtracting. That is the
failure this repository's own rules name twice over.

**What shipped instead: one table, moved to where both readers can see it.** The
judgment now lives in `internal/source/family.go`, beside `WeakEvidence`, which
is the same kind of judgment about the same subject and already carries the same
"the ids are literals because every collector imports this package" reasoning.
`dashboard_sources.go` keeps its own `family` type and its own error policy, but
defines its seven constants in terms of the shared ones and delegates
`sourceFamily` to `source.FamilyOf`. **Every one of its tests passes unchanged**,
which is the evidence that the taxonomy is byte-identical rather than merely
similar.

It also answers Ben's question in his own words. He asked how much of the work
"was Claude, Git, etc."; the rows are literally named `Claude` and `Git`.

### The one place the two commands still differ, stated

`dashboard` partitions whole **sittings** by which families evidenced them —
model A, crediting a family the whole of any sitting its evidence appears
anywhere inside. `metrics` re-clusters each family's **own** evidence — model B,
the model this package measured model A wrong against one level down (24.01h
summed across a 12.78h day). Neither is wrong: the dashboard asks what evidenced
a sitting, treating the sitting as atomic. Both `internal/metrics`' package
documentation and `docs/content/docs/metrics.md` now say so, so that nobody
subtracts them.

**By construction the dashboard's per-family figure can never be smaller.** A
family's own cluster cannot reach outside the global sittings — the global
clustering sees strictly more events at the same gap, so it merges at least as
much — and it contributes nothing to a sitting holding none of its evidence. So
model A ≥ model B for every family, always.

**I could not decide it numerically, and say so rather than implying I did.**
`timetattle dashboard --all` caps its legend at eight segments, so the six named
combinations containing `Claude` sum to **946.4h** with a **160.2h** remainder
folded into "25 more combinations" that certainly contains more of it. Against
the **989.3h** `metrics` reports for `Claude`, that puts the true model A figure
somewhere in 989.3h–1106.6h: consistent with the inequality, not a confirmation
of it. **The two runs are also different populations** — the dashboard drew a
265-day trailing window totalling 1155h of presence, against the unwindowed
1165.3h here — which is a second reason the numbers must not be subtracted.
Deciding it properly needs an uncapped model-A total, which nothing prints
today.

## The semantics

Per family, two figures, following the client roll-up exactly one axis over:

| | |
|---|---|
| `participated` | the presence this family's own evidence clusters to, intersected with the global sittings. **Overlaps every other row.** |
| `solely` | the part of it no other family reaches — what would drop out of the section without that stream. |

Plus a third figure that is neither: **clock no family reaches at all**, which
is silence the global gap threshold bridged *between* two streams and which
neither stream bridges alone. It is printed even at zero, because it is what
tells a reader the rows do not tile presence.

The report states in the table's own footer that the rows overlap and never sum.

A family with **nothing** in the span gets no row — a `0.0h` row would state
that it observed none of this week when the only thing known is that no
observation of it reached the span — and the missing ones are **named** on a
`no observation in this span:` line, read from the shared mapping rather than
from the corpus, since a family absent from the corpus is precisely what cannot
be read out of it. On the whole store all seven are present and the line does
not appear; it is a windowed report's line.

## The whole store, unwindowed

`timetattle metrics -all`, 2026-08-13, 1,073,726 observations read, presence
**1165.3h across 1548 sittings**:

| family | participated | share | solely | observed | observations |
|---|---|---|---|---|---|
| Apps | 352.3h | 30.2% | 87.3h | 324.2h | 55,635 |
| Blender | 1.6h | 0.1% | 0.5h | 0.0h | 10 |
| Claude | 989.3h | 84.9% | 339.8h | 0.0h | 856,656 |
| Files | 475.2h | 40.8% | 43.5h | 0.0h | 132,312 |
| Git | 308.0h | 26.4% | 16.5h | 0.0h | 4,543 |
| Jobs | 293.7h | 25.2% | 2.4h | 158.7h | 16,655 |
| Motion | 7.2h | 0.6% | 3.0h | 0.0h | 38 |

Reached by **no** family: **1.2h**.

**The rows sum to 2427.3h against 1165.3h of presence — 2.08 times the clock.**
That is the whole case for the union shape rather than a partition: had the
overlap been negligible, columns that summed would have been defensible. They
are not, on this corpus, by a factor of two.

Reconciled against the checks the report makes internally: 493h3m27.4s is held
by exactly one family, 671h1m23.2s by more than one, 1h14m4.0s by none, and
those three come to the 1165h18m54.5s of presence exactly (the table above is
the same figures rounded to `%.1fh`).

> **Corrected 2026-08-14: this capture was taken under a clustering rule that
> has since been retired, so every hour in it moved — not by corpus growth
> alone.** `93cd2cc` stopped observed darkness suppressing evidence and
> subtracting time from sittings; it now suppresses only observations whose
> `Cause` is `source.CauseTimer`. Re-running `timetattle metrics --all` on
> 2026-08-14 gives presence **1243.8h across 1181 sittings** (from 1165.3h
> across 1548), and, for instance, `Apps` 412.2h and `Claude` 1035.4h where this
> table has 352.3h and 989.3h. Sitting counts moved most: the retired rule cut
> sittings at every dark span.
>
> **The finding is unaffected and that is the point of leaving the table
> standing.** The rows still overlap and still never sum — 2603.1h against
> 1243.8h of presence, 2.09x, against the 2.08x recorded here. The union shape
> was the right call for a structural reason, and a structural reason does not
> move when a rule does. See
> `project/2026-08-14-darkness-may-only-suppress-a-timer.md`.

Two readings worth recording:

- **`Jobs` holds 293.7h and only 2.4h of it alone.** The task database is
  almost never the only thing observing an hour, which is what one would hope:
  it is a running commentary on work other streams also see.
- **`Files` holds 475.2h with 43.5h alone, and none of it observed.** It is the
  second-largest participant and the one resting entirely on modification
  times — the stream a log-rotation cron once read as 135.2h of a client's work.
  Being able to see its share is a large part of why the section was asked for.
- **`Apps` is the only family whose participation is nearly all observed**
  (324.2h of 352.3h): focus intervals span real clock, where every attributable
  stream emits instants.

## What it costs

**The task's cost premise was already known to be stale and is corrected again
here.** The task says the client roll-up is ~39% of a 72-second run; agent
`rollup` corrected that to 0.537s and 9.2% of a 5.811s run on 2026-08-13. Every
figure below was taken on the same machine on 2026-08-13 **while several other
agents were running**, so the absolute totals are inflated against that 5.811s
baseline and only the within-run shares and the interleaved pairs are worth
quoting.

`scripts/metricsphases -n 5`, unwindowed, with the mix in place:

```
  phase                                      min      mean       max   mean%
  reading events (1073726)                4.411s    5.402s    6.558s   45.2%
  per-project time (38)                   1.129s    1.681s    3.109s   14.1%
  client roll-up (6)                      0.593s    1.120s    2.461s    9.4%
  activity mix (7)                        0.741s    1.402s    3.232s   11.7%
  total                                   9.262s   11.946s   15.291s  100.0%
```

**The activity mix over 7 families costs 1.25x the client roll-up over 6
clients**, by both the minima (0.741s vs 0.593s) and the means (1.402s vs
1.120s) — a within-run ratio, which is the comparison machine load does not
move. It is 0.83x the per-project pass over 38 projects. The reason 7 families
cost as much as 38 projects is that one family is `Claude`, holding 856,656 of
the 1,071,969 retained observations: clustering it is very nearly clustering the
whole store a second time.

`grouping evidence` also rose, from a mean of 0.344s to 0.473s (minima 0.199s to
0.288s), which is the extra bucket every evidence event is now filed into.

Interleaved whole-run A/B, the same store, alternating a binary built from the
tree without this change and one with it, so load falls on both equally. Eight
pairs, in two rounds of four; `/usr/bin/time -p` on each binary,
`timetattle metrics -all` unwindowed, output discarded:

| pair | before | after | delta |
|---|---|---|---|
| 1 | 5.88s | 5.55s | **−0.33s** |
| 2 | 4.87s | 7.01s | +2.14s |
| 3 | 5.72s | 6.43s | +0.71s |
| 4 | 6.17s | 8.71s | +2.54s |
| 5 | 4.29s | 5.59s | +1.30s |
| 6 | 4.45s | 4.85s | +0.40s |
| 7 | 4.26s | 4.95s | +0.69s |
| 8 | 6.14s | 5.31s | **−0.83s** |

Mean 5.22s against 6.05s: **+0.83s, +15.9%**. Median delta **+0.70s**, which is
within a rounding error of the mix phase's own 0.741s minimum and is the figure
to quote. **Two of the eight pairs have the instrumented binary FASTER**, which
is how much noise a loaded machine puts on a whole-run timing; the phase table
above is the more trustworthy instrument precisely because its comparison is
made inside a single run, and the two agree.

**Per-family re-clustering is affordable, and the reason is structural rather
than lucky: the family count is bounded at eight** however many projects or
clients a store grows to. A per-project *and* per-family cross-tabulation would
be 38 × 7 and is deliberately not built.

### The memory cost is larger than the time cost, proportionally

`/usr/bin/time -l`, same two binaries, unwindowed:

| | maximum resident set size |
|---|---|
| before | 1,628,291,072 (1.63 GB) |
| after | 1,877,442,560 (1.88 GB) |

**+249 MB, +15.3%.** That is one more copy of every evidence observation:
1,071,969 retained events at roughly 230 bytes each is 246 MB, which accounts
for the whole of it. `reduce` takes `map[string][]source.Event`, so filing
events under a family means copying them, exactly as filing them under a project
already does — but the project buckets hold only *attributed* evidence while the
family buckets hold all of it.

It was left as it is rather than optimised. Removing it means changing `reduce`
to take indices instead of events, which is a change to the one function the
project and client levels also go through, and the failure mode of getting that
wrong is a roll-up computed under subtly different rules from the rows beneath
it. That trade is not worth making for 249 MB without somebody asking for it —
but it is the first thing to look at if this ever needs to run somewhere small.

## The guards, proved by mutation

The mix's own rows cannot be added up, so the section has no arithmetic a reader
could catch it out by. Two things that *are* disjoint are checked instead,
inside `Compute`, before anything prints — and each was proved by removing it.

**1. `solely` computed two ways.** Pairwise subtraction of the other families,
against a sweep counting how many families cover each instant. Mutation: make
the subtraction consider only the first other family.

| | result |
|---|---|
| `timetattle metrics -all`, live store | **REFUSED**: 1752h13m6.8s held alone by subtraction against 493h3m27.4s by the depth sweep — **1259h9m39.5s** of inflation |
| `go test ./...` | **14 failures** |

**2. The three-way tiling.** Clock exactly one family holds, clock two or more
hold, and clock none reaches must come to presence. Mutation: force the
unreached figure to zero.

| | result |
|---|---|
| `timetattle metrics -all`, live store | **REFUSED**, off by **1h14m3.96s** — which is exactly the 1.2h the report prints as reached by no family |
| `go test ./...` | **15 failures** |

Both mutations were reverted and the suite re-run green.

## What this deliberately did not do

- **No per-project or per-client family mix.** 38 projects × 7 families is 266
  clusterings, and the question asked was about the shape of the work overall.
- **Nothing in the series or the CSV.** `internal/metrics/series.go` is another
  agent's file this wave. Families are report-level only, so
  `metrics -by day -csv` carries no family column. This is the one obvious
  follow-on and it needs a decision, not just an edit: see below.
- **No medium or billability axis.** A source family says which evidence stream
  OBSERVED an hour, never whether it was billable. Ben skipped that question on
  2026-08-03 and this keeps the two axes apart.
- **No change to how any existing figure is computed.** Presence, the projects,
  the clients, the segmentation and the corrections are untouched; the mix is
  read off the same span arithmetic and added beside them. The dashboard's
  source bar draws exactly what it drew before.
- **The dashboard's fatal-on-unmapped policy was not changed** to match
  `metrics`, and `metrics` did not adopt the dashboard's. A chart is
  all-or-nothing; withholding a whole `metrics` report over one unrecognised
  stream would hide every figure that stream has nothing to do with. Both
  refuse to guess; they differ only in what refusing costs the reader.

## For Ben

1. **Should the mix appear in the day/week series and the CSV?** It is a
   genuine question rather than a missing feature: a family's clock is its own
   evidence re-clustered, and re-clustering per family *per period* is a
   different cost curve from anything the series does today. `series.go` is
   agent `bucketeer`'s file this wave, so nothing was touched there.
2. **`Files` at 475.2h, 40.8% of all presence, none of it observed.** The mix
   makes visible how much of this corpus rests on file modification times. That
   is a measurement, not a defect, but it is a large number to have not been
   looking at.
