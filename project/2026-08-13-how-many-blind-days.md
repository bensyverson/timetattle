# How many blind days a non-circular fit would need

**Date:** 2026-08-13
**Agent:** `blindfit`, task `xAn46` under `PkY6I`
**Store:** `~/.timetattle/timetattle.db`, opened read-only, 1,067,254 observations read
**Tools:** `timetattle fit`, `go test ./internal/fit/`, `sqlite3`

**Nothing shipped changed and nothing was written to the store.** This document
records a measurement and two corrections.

## 1. The correction: the split already landed

`xAn46` says "internal/fit does not read that column". That stopped being true on
2026-08-03. The confusion has a mechanical cause worth writing down, because it
will recur: the repository history was re-initialised on 2026-08-06, so
`git log -- internal/fit/` reports exactly one commit, `4c3645f "Adds initial
files"`, and the commit the work was originally recorded under no longer exists
as an object. Absence from a log is not absence from a tree — the same
distinction this project keeps making about absent rows, in a new place.

What is present at HEAD: `fit.Label.Shown`/`Saw()`, `Truth.Census()`,
`Truth.Only()`, `Truth.ByShown`, `Truth.Contested()`, `Cell.ByShown`,
`Result.Splits`/`CommonBest()`/`Pooled()`, the `-shown` flag, and the two report
sections. `docs/content/docs/fitting.md` documents all of it.

A second precondition on the same task — that the live store sat at migration
0010 and that tests skipped on it — is also stale. The store is at
`0014_event_payload_shape.sql`, and there are **zero skips**:

```
go test ./internal/fit/ ./internal/adjustments/ ./cmd/timetattle/ -v | grep -c '^--- SKIP'
    -> 0
go test -tags realdata ./internal/fit/ -v \
  -run TestTheLivePopulationsAreCountedRatherThanAssumed
    -> PASS
```

Note that the plain `go test ./...` run never compiled those corpus tests at all;
`internal/fit/realdata_test.go` carries `//go:build realdata`. A skip that cannot
fire because the file is not built looks identical to a skip that passed.

## 2. Refusing and reporting are both right, on different cases

The task asks whether a pooled fit should be refused outright or reported with
the split named. The shipped answer refuses one case and reports the other, split
on whether the two populations overlap **in time**, and that is better than
either horn:

- **Two populations labelling the same day — refused.** Each labelling tiles that
  day by itself, so the pooled denominator counts the shared time twice. The
  arithmetic is wrong, not merely hard to read, and no wording repairs a
  denominator.
- **Two populations labelling different days — reported, with the split named.**
  The labels still form a partition, so the residual is sound; what it lacks is
  meaning, because it averages a population worth something with one worth almost
  nothing. That is a reader problem, and the report answers it with each
  population's own minimum rather than with a warning.

The principle worth keeping: **refuse when the arithmetic is wrong, report when
only the interpretation is.**

Both guards are measured rather than asserted. The double-count, taken on the
constructed `splitCorpus()` in `internal/fit/split_test.go` by summing the
per-population asserted time against the union of labelled span:

| quantity | value |
|---|---:|
| pooled denominator as printed | 4.4333h |
| union of labelled span (what the days hold) | 4.0667h |
| **double-counted** | **0.3667h — exactly 22.0 minutes** |

And the headcount, held by `TestSearchReportsEachPopulationsOwnMinimum`: the
confirming population is minimised at gap 25m, the blind one at 5m, both at
residual 0.000h, and the pooled search returns 25m purely because that population
carries 4.1 labelled hours against 0.4. `CommonBest()` is empty.

## 3. The measurement: separation is knowable with zero blind labels

There are no blind labels in the store — 70 rows in `adjustment_review`, every
one `shown='derivation'`:

```
sqlite3 ~/.timetattle/timetattle.db \
  "select shown, count(*) from adjustment_review group by shown;"   -> derivation|70
```

So no blind fit can be demonstrated. But the quantity that decides **how many
blind days would be enough** can be measured today, and the circularity is what
makes it possible.

The residual at 15m/5m/5m against the confirming labels is exactly 0.000h. That
means the confirming labels *are* the reference derivation. Therefore the
residual at any other grid point is precisely the symmetric difference between
that point's derivation and the reference's — which is the **separation** between
two candidate parameter sets, measured on real working days, at no cost. The
labels are useless for fitting and exact for this.

Re-measured today rather than quoted, because the corpus has grown from 933,452
observations to 1,067,254 since 2026-08-02:

```
timetattle fit -skip-days 2026-08-01,2026-02-17,2026-03-21 \
  -gaps 12m,15m,20m -lead-ins 2m,5m,10m -tail-outs 2m,5m,10m
```

438 assertions over 14 days, residual 0.000h of 336.0h, the reference ranked 1 of
18 live points. **Every separation figure is unchanged to three decimals from the
2026-08-02 run despite roughly 134,000 additional observations**, because the
additions fall outside the labelled days' closure. Two runs measuring the same
thing, reconciled.

| neighbour | total | per day | per day (s) | N@1m | N@5m | N@15m | N@30m |
|---|---:|---:|---:|---:|---:|---:|---:|
| gap 15m→12m | 0.193h | 0.01379h | 49.6 | 2 | **37** | 329 | 1316 |
| gap 15m→20m | 0.437h | 0.03121h | 112.4 | 1 | 8 | 65 | 257 |
| lead-in 5m→2m | 0.448h | 0.03200h | 115.2 | 1 | 7 | 62 | 245 |
| lead-in 5m→10m | 0.812h | 0.05800h | 208.8 | 1 | 3 | 19 | 75 |
| tail-out 5m→2m | 0.716h | 0.05114h | 184.1 | 1 | 3 | 24 | 96 |
| tail-out 5m→10m | 1.064h | 0.07600h | 273.6 | 1 | **2** | 11 | 44 |

`N` is the number of blind days needed to separate that neighbour from the
reference, at an assumed per-day blind labelling error of 1m, 5m, 15m or 30m.

**The assumption is named because it is not measured.**
`N = ceil((error / separation)^2)`, which assumes the reviewer's per-day errors
are *independent* across days. If they are systematic instead, days never help at
all: the criterion collapses to `separation > error`, and no number of reviews
fixes a biased reviewer. Nothing can settle which regime applies until blind
labels exist, which is exactly why no number from this table is printed by the
tool.

## 4. What to actually ask for

The axes are not equally expensive, and that changes the ask.

Tail-out and lead-in are cheap: at a 5-minute per-day error, **2 to 3 blind days**
separate a 5-minute step on either, and even at a sloppy 30 minutes per day it is
44 to 96. The gap is the expensive one — and it is the parameter the reviews were
meant to settle. Separating 15m from 12m needs 37 blind days at a 5-minute error
and 329 at 15 minutes.

So the concrete ask is **not** "review blind until the fit works". It is: **five to
ten blind days**. They will settle tail-out and lead-in outright, and — more
valuable — they will be the first measurement of the per-day labelling error that
fixes every other number in the table above. The second column of this document
is currently the only unmeasured term in the whole calculation.

There is independent reason to expect real signal on tail-out. Four separate cuts
of 235 declared work units put the tail between 24s and 41s, while this fit puts
it at 5m and charges 1.064h for moving it to 10m. Those cannot both be right, and
a blind pass is what adjudicates.

## 5. What was deliberately not built

No "blind days needed" estimator was shipped into `timetattle fit`. Printing this
table would require the tool to take the per-day error budget as an argument, and
a tool is only as good as the argument it is handed; a figure quoted at a
made-up error is the kind of number this task exists to stop being printed. The
error term is a requirement question with no data behind it, so it is recorded
here for a human to decide rather than defaulted in code.

No blind labels were written to the store. Fabricated ground truth would be worse
than none, and circular evidence has already fooled this fitter once.
