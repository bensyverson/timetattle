# Where a `metrics` run actually goes, and what became of the 39% roll-up

2026-08-13. Agent `rollup`, task GfNIy.

## The short version

The recorded claim was that the client roll-up is about **31.5 s, some 39%**, of
a `timetattle metrics` run. It is **not reproduced at HEAD, and it is corrected
to 0.4–0.5 s, about 9%.**

The figure was not wrong when it was taken. It was measured against code that no
longer exists: the four optimisations of 2026-08-07 landed *the day after* the
71.9 s baseline the 39% sits beside, and they took the same run from ~82 s to
~5 s. The roll-up did not get cheaper as a share by being optimised — it got
cheaper in absolute terms by a factor of about 40, while everything around it
got cheaper too, and what is now the largest phase is the store read.

This document also records a **defect found while measuring**, which is not
fixed here: the roll-up's implementation and the roll-up's own closing guard
disagree about what a client's hours are, and on an ordinary interleaving corpus
the guard refuses the whole report. See [A contradiction inside the
roll-up](#a-contradiction-inside-the-roll-up).

## What now reproduces any of this

The instrument ships. It is not an agent's throwaway edit to `internal/metrics`,
which is what every previous figure about this command's cost came from and is
why the 39% could stand unexamined for a week after it stopped being true.

```console
$ timetattle metrics -timings          # one run, per-phase table on stderr
$ scripts/metricsphases -n 5           # fresh build, five runs, min/mean/max
```

`-timings` writes to **stderr**, so it changes not one byte of the report;
`scripts/metricsphases` prints the population — windowed or not — above every
table it produces.

## The measurements

### Population

**Unwindowed: the whole store.** Every figure below is the full store, no
`-from`/`-to`. The store held **1,069,011 events, 1,067,254 after the exclusion
pass**, over 38 projects and 6 clients, at schema 0014, on 2026-08-13. That is
about 9% more events than the 978,701 the task's earlier figures were taken
against, so the older absolute seconds are, if anything, flattered.

A 14-day windowed run measured elsewhere at 14.28 s is **a different population**
and is not compared with anything here.

### The run today

`scripts/metricsphases -n 5`, unwindowed, fresh build of HEAD:

```
  phase                                     min      mean       max   mean%
  reading events (1069011)               1.888s    2.400s    3.293s   41.3%
  dropping machine activity (1067254)    0.615s    0.702s    0.938s   12.1%
  stamping clients                       0.035s    0.039s    0.042s    0.7%
  reading corrections                    0.010s    0.012s    0.013s    0.2%
  clustering                             0.253s    0.423s    0.701s    7.3%
  grouping evidence                      0.155s    0.295s    0.608s    5.1%
  per-project time (38)                  0.824s    1.371s    2.126s   23.6%
  client roll-up (6)                     0.390s    0.537s    0.956s    9.2%
  segmenting                             0.013s    0.019s    0.038s    0.3%
  formatting                             0.009s    0.010s    0.015s    0.2%
  elsewhere                              0.001s    0.002s    0.003s    0.0%
  total                                  4.193s    5.811s    7.463s  100.0%
```

**The client roll-up is 9.2% of the run, not 39%.** The spread matters and is
the reason five runs are quoted rather than one: the store read varies by 1.7x
with the page cache, and it is the largest phase, so a single run's percentages
are not stable to better than a few points.

### The run before the four optimisations

Built from `633aabc`, the parent of `66ca619` "Removes the quadratic maxEnd
rescan from session clustering", against the **same live store, today**, with
the config's `engagements` block stripped (the only key that binary does not
know; the clustering parameters are identical):

- `633aabc`: **82.16 / 88.90 / 93.11 s** wall, 79.29–82.59 s user, three runs.
- HEAD: **5.71 / 6.38 / 6.86 s** wall, 5.26–5.41 s user, three runs.
- The installed binary of 2026-08-12: 4.41 / 4.60 / 5.77 s wall.

For reference, the main thread measured 71.9 s wall / 73.6 s user on 2026-08-06
on an unmodified installed binary. 82–93 s today against 71.9 s then is what a
9%-larger corpus does to a run whose dominant term was quadratic.

Reproduce the timings with
`/usr/bin/time -p <binary> metrics -all >/dev/null` three times; the pre-optimisation
binary has no `-all` flag and reports everything by default, which is the same
computation — `-all` selects rows and never changes what is computed.

### So was 39% ever right? Roughly, yes — and pprof can say so exactly

The two `reduce` calls, per-project and per-client, sit on **different source
lines**, so a pprof *line listing* separates them where a function-level table
cannot. Both binaries support `TIMETATTLE_CPUPROFILE`.

`go tool pprof -list 'metrics.Compute'` on `633aabc`, one unwindowed run,
81.07 s duration / 70.74 s of samples:

```
         .     25.20s    424:	global, err := sessions.Cluster(events, p)
         .     18.99s    525:	projects, err := reduce(byProject, bounds, presence, p)
         .     19.22s    529:	clients, err := reduce(byClient, bounds, presence, p)
```

The client roll-up was **19.22 s, 27.2% of samples** — the same order as the
recorded 31.5 s / 39%, not equal to it. `sessions.maxEnd` alone was 53.57 s
cumulative, **75.7% of the whole run**, which is the quadratic rescan; it is
spread across the global clustering and all 44 per-group clusterings, which is
why *both* attribution phases were enormous and why they were nearly equal
despite one having 38 groups and the other 6.

The same listing at HEAD, `go tool pprof -list 'metrics.Watched'`, one
unwindowed run of 5.46 s / 5.01 s of samples:

```
         .      210ms    507:	global, err := sessions.Cluster(events, p)
         .      580ms    647:	projects, err := reduce(byProject, ... PhaseProjects)
         .      280ms    651:	clients, err := reduce(byClient, ... PhaseClients)
```

**The roll-up went from 19.22 s to 0.28 s: a factor of about 69 by this
instrument, about 40 by the wall-clock one.**

### The two instruments, reconciled

The same single run, measured both ways — `-timings` gives wall clock per phase,
pprof gives on-CPU samples:

| phase | `-timings` | pprof line listing |
|---|---|---|
| reading events | 2.872 s | 1.57 s (`store.(*Store).events`, cum) |
| clustering | 0.280 s | 0.210 s |
| per-project time | 0.850 s | 0.580 s |
| client roll-up | 0.398 s | 0.280 s |
| whole run | 5.303 s | 5.46 s duration, 5.01 s sampled |

They are **not the same measurement and are not expected to match**. pprof counts
on-CPU samples only, so the store read — the one phase that blocks on I/O — is
the one that diverges most, by 1.3 s, and the profile captured 91.7% of the run's
duration as samples. The compute phases agree within about 30%, and the ratio
that matters agrees closely: per-project ÷ client roll-up is 2.14 by wall clock
and 2.07 by samples. Nothing here is unaccounted for.

## Why the roll-up costs what it costs

A client's absolute hours are the union of its projects' spans and never their
sum, so the roll-up cannot add columns — it re-clusters each client's own
evidence, exactly as a project's is derived. It is therefore a *second* pass of
the same shape as the per-project one, over larger groups: 38 projects cost
1.371 s (36 ms each) and 6 clients cost 0.537 s (90 ms each), because a client's
evidence is all of its projects' evidence at once.

At 9% of a 5-second run, **there is nothing here worth optimising**, and the
task's open question — must it re-cluster, or can it merge the per-project
segments already computed — is now a correctness question rather than a
performance one. Which is where the defect below comes in.

## A contradiction inside the roll-up

**Found while measuring. Not fixed: it is an architectural question with two
defensible answers and it needs a decision, not an agent's preference.**

`internal/metrics` says two different things about what a client's hours are:

- **The implementation** re-clusters the client's *own evidence* under the same
  parameters — "model B" applied one level up, which is what `reduce` does.
- **`Client`'s doc comment and the closing guard** say the figure is the *union
  of the client's projects' spans*, and the guard refuses any report in which a
  client exceeds its projects' sum.

They agree everywhere except where one client's projects **interleave**, and
there they cannot both be right, because clustering does not distribute over a
union: gaps too wide to bridge inside any one project are narrow enough to
bridge once that client's projects are taken together.

At the shipped defaults — gap 15m, lead-in and tail-out 5m — two of one client's
projects touched **alternately every 8 to 15 minutes** put each project's own
observations more than a gap apart while the client's stay inside one. Walking
the spacing:

| alternation | client | projects summed | result |
|---|---|---|---|
| 10 min | 40m | 40m | passes, with exactly zero margin |
| 12 min | 46m | 40m | **refused** |
| 14 min | 52m | 40m | **refused** |

The refusal is a whole failed command, not a wrong row: `Compute` returns
`client X is credited 46m0s across projects summing to 40m0s; a roll-up is the
union of its projects' spans and can never exceed their sum`, and there is no
report at all. It has never fired on the live store — every run in this document
exited 0 — and the live registry holds one client spanning 33 projects, which is
the shape most able to produce it. **`dashboard` is about to be wired to the same
call, so it inherits the same failure.**

Either answer resolves it, and they point opposite ways:

- If a client's hours are its own evidence re-clustered, the guard and `Client`'s
  doc are wrong, and the roll-up must keep re-clustering.
- If they are the union of its projects' spans, the guard is right and the
  roll-up should **merge** the spans already computed instead of clustering a
  second time — which is also the cheaper of the two, and is exactly the open
  question this task's cost measurement was meant to inform.

The reproduction is pinned by
`TestTheRollUpAndItsOwnGuardContradictWhenAClientsProjectsInterleave` in
`internal/metrics/rollupcost_test.go`, which asserts the current refusal and says
in its own comment that the refusal is a defect. Whoever settles the question
will see it fail, and should delete it.

## What this corrects

> The task record states: "MEASURED: the client roll-up accounts for roughly
> 31.5s of a metrics run, about 39% of it". That is superseded. It was
> approximately right for the code of 2026-08-06 — measured here at 19.22 s and
> 27.2% of samples on the pre-optimisation tree, against a corpus 9% larger than
> the one it was originally taken on — and it has been wrong since 2026-08-07,
> when the four optimisations landed. At HEAD the roll-up is 0.537 s and 9.2% of
> the run.

> `project/2026-08-07-cli-overhaul-plan.md` records `metrics` at 72.97 s real /
> 74.06 s user at commit `e3ec48e`. That figure is not corrected — it was taken
> before the same four optimisations — but a reader arriving at it today should
> know the same command now runs in about 5 s, and that `scripts/metricsphases`
> is how to check rather than assume.

## The general lesson, which this project keeps relearning

A performance figure has a **commit** attached to it as surely as a windowed
measurement has a population, and it goes stale the same way and just as
silently. The 39% was recorded on 2026-08-04, confirmed on 2026-08-06, and
falsified on 2026-08-07 by work that nobody connected to it — including a brief
written on 2026-08-13 that cited *both* the 71.9 s baseline and the four
optimisations that had superseded it, without noticing they were in conflict.

The fix is the same fix as everywhere else here: ship the instrument. A number
that can be re-taken by a flag on the shipped binary cannot rot unnoticed for a
week.
