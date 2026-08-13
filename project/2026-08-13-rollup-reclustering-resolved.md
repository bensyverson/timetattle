# A client's hours are re-clustered. The guard was what was wrong.

2026-08-13. Agent `unionist`, task qAkml. This document **settles** the question
`project/2026-08-13-rollup-union-or-reclustering.md` measured the day before and
deliberately left open. Read that one first: every figure it records still
stands, and none of them is repeated here except where this work changed it.

## The decision, and the reason for it

**Resolution A: keep the re-clustering, replace the guard.** Decided by Ben on
2026-08-13.

The argument is not that A is cheaper or safer — it is what a client's hours
*mean*:

> there's mental switching cost between different projects, so that 19 minutes
> across all time is probably an undercount.

A client's hours are what that client's evidence clusters to **when taken
together**. Moving between two of one client's projects costs attention that
neither project's own evidence records, so a stretch bridged only at the client
level is that switching cost made visible. It is arguably *more* real than the
projects' sum, not an artefact of clustering twice — and the sum is the figure
that is missing something.

The consequences follow from that and not the other way round: a client may
exceed the sum of its projects; a report where one does is correct; and a check
that refused such a report was refusing a correct figure. Nothing about the
19m13s the previous document measured changed. What changed is that a window
holding it can now print.

## What changed in the tree

| | |
|---|---|
| `internal/metrics/metrics.go` | the closing guard replaced (below); the doc comment on `Client` and the `Report.Clients` field comment rewritten to describe the re-clustering, the surplus, and the fact that the surplus is also printed as unattributed presence |
| `internal/metrics/rollupcost_test.go` | the pinned contradiction, whose own comment called the refusal a defect, now pins the resolved behaviour: the interleaving corpus computes, the client reads 46m against 40m summed, and presence and allocation close |
| `internal/metrics/rollupunion_test.go` | the twin pin, on the call `dashboard` makes, likewise; and a NEW case for why the replacement is an inequality |
| `internal/metrics/realdata_test.go` | a real-data check of the surviving invariant, and the helper that makes the client roll-up reachable on real data at all |
| `scripts/rollupunion/` | the refusal it parses is the new one; its package doc says the question is settled and what it still measures; its parse test rewritten |
| `docs/content/docs/metrics.md` | the open question closed in the text |

## The replacement guard, and why it is NOT the one that was recommended

The removed check compared **every** client against the **sum** of its projects.
The previous document established by mutation that it did not catch the
addition-shaped bug its own comment named — the equality immediately above it
catches that first, every time — and that its one real catch was inflation of a
client's own clustered spans, which fired on a client with **one** project.

What ships is that catch and nothing more:

> A client owning exactly **one** project is that project's own evidence
> clustered again under the same parameters, so it can never be credited more
> clock than the project is.

It is true by construction. A client's events are exactly its projects'
events that resolved to it, so a one-project client's evidence is a subset of
that project's; the same clustering parameters and the same presence then put
its spans inside the project's. The correction layer preserves it: an assertion
naming a project moves the same span at both levels, and where it cannot — a
project two clients claim — the client gains nothing while the project gains,
which is the safe direction.

**The previous document recommended the strict equality — "a client with exactly
one project equals that project to the nanosecond" — and that recommendation is
wrong.** It is not true by construction, and the repository's own corpus already
refutes it. Applied as a mutation (`>` changed to `!=`) and measured:

| population | equality candidate |
|---|---|
| the whole live store, `timetattle metrics -all` | **passes, byte-identical output.** It catches nothing today |
| the cold test suite, `go test ./...` | **2 failures**, both legitimate reports refused |

The two failures are the two ordinary shapes that break it, and one of them was
already in the tree:

- `TestAClientAndAProjectSharingANameAreNotMerged` (`client_test.go`, predating
  this work): a project worked for one client in the morning and another in the
  afternoon. The afternoon client owns one project and is credited 10m against
  the 20m that project holds. The equality refuses the whole report.
- `TestAOneProjectClientHoldsLessThanItsProjectWhenTheClientResolvedLate` (new):
  a directory registered to a client partway through its life. The client is
  credited 15m against the project's 30m, and the 15m the client does not hold
  is reported as `Clientless`, which is the figure that exists to name it.

That combination — silent on the whole live store, refusing an ordinary
registration — is precisely the latency profile of the guard being removed. So
the check ships as an **inequality**. Neither form is a tolerance.

## The mutation, with numbers

The mutation is the one the previous document identified as the guard's only
real catch: **the CLIENT level clustered at gap 30m while the projects stay at
15m**, applied in `Compute` at the `reduce(byClient, …)` call, measured, and
reverted. Three runs:

| | whole live store, `timetattle metrics -all`, unwindowed | cold suite `go test ./...` | real-data suite `go test -tags realdata ./internal/metrics/` |
|---|---|---|---|
| mutation applied, guard **in** | **REFUSED**: a one-project client credited **51h39m27.261521s** against the **48h1m24.29735s** its only project holds — **3h38m2.964171s** of inflation | passes | **FAILS**, naming a one-project client credited **3h2m51.076s** against **2h32m24.227s** on 2026-07-28: **30m26.849s** |
| mutation applied, guard **removed** | exits 0 and prints a plausible, wrong report: every client inflated, the 33-project client 617.7h → 619.5h and its share 55.4% → 51.8% | passes | passes |

So the new guard is the only thing in the repository that catches it, exactly as
the old one was — and it catches it on the live store *and* on one real day, not
only in a constructed case. The first row's figure reproduces the previous
document's 3h38m2.96s to the nanosecond, from a different check.

## Acceptance, and which population each figure describes

### The window that produced nothing

`timetattle metrics -all -from 2026-04-24 -to 2026-04-24` — one calendar day.

| | before | after |
|---|---|---|
| exit status | 1 | **0** |
| bytes on stdout | **0** | 2,922 |
| stderr | the guard's refusal, a client credited 6h7m1.996532s against 6h4m48.248532s | empty |

The report it now prints is complete and closes: 7.4h of presence across 5
sittings, 7.4h absolute at 1.00x, allocation summing to the presence it divides —
which is what the previous document predicted with the guard mutated off.

### The whole store is unchanged, byte for byte

`timetattle metrics -all`, unwindowed, captured before and after:

```console
$ cksum report-before.txt report-after.txt
347481858 6435 report-before.txt
347481858 6435 report-after.txt
```

Same checksum, same length, `diff` empty. **Resolution A changes no live
figure**, as predicted. (Resolution B would have changed exactly one row.)

`go run ./scripts/rollupunion`, unwindowed, is likewise identical before and
after: the 33-project client still holds **19m13s** across **11** stretches,
largest **4m19s**, against **83h58m52s** of margin, and the other five clients
still hold **0s** alone at a margin of exactly **0s**. The 19m13s survives, which
is what distinguishes A from B in the output rather than in the intent.

### The day sweep: one refusal became none

`go run ./scripts/rollupunion -sweep day` — one closed window per calendar day,
4,712 of them, of which only 82 hold a client with more than one project and can
exercise the interleaving at all.

| | before | after |
|---|---|---|
| windows refused | **1** (2026-04-24) | **0** |
| windows holding clock a client alone held | 10 | **11** |
| worst margin over the 82 multi-project windows | — | **−2m13.748s**, and it prints a report |

**The 11th window is 2026-04-24 itself**, holding 2m13.748s alone. It is not new
clock: it was unobservable before, because a refused window produces no report to
measure. Ten plus the one that could not be counted is eleven, and that
reconciles the two runs exactly. A window with no figures because it was refused
was never a window that measured zero.

### The dashboard, and the windows that sat at zero

`timetattle dashboard --all --days 40` exits **0**, 65 lines, as it did before —
no trailing window refused before this change either. What the previous document
found was 22 consecutive trailing windows at a margin of **exactly 0s**, one
bridged silence away from blanking the human surface.

`go run ./scripts/rollupunion -sweep suffix -from 2026-04-01` — 135 windows, each
running from that day to the end of the store, which is the shape `dashboard`
computes — reproduces the previous document's reading unchanged after this work:
**0 refused**, 24 windows holding clock a client alone held, the most in one
window 6m33.246s, and **22 windows in July still at a margin of exactly 0s**
(116 of the 135 hold a client with more than one project at all).

They are no longer a cliff edge, and the reason is structural rather than
statistical: **the check no longer reads that margin at all.** The quantity those
22 windows were zero on is the client against its projects' sum, and nothing
consults it any more. The demonstration is 2026-04-24, which is not at zero but
*past* it at −2m13.748s and prints a full report.

## What this deliberately did not do

- It did not change how any figure is computed. `reduce` is untouched; the only
  changes to `metrics.go` are the closing check and comments.
- It did not add a check for multi-project clients. There is no true one to add:
  a client's spans need not contain the union of its projects' spans (a project
  can hold clientless clock) and need not be contained by it (that is the
  bridging this decision keeps), so any inequality between them would be a
  belief rather than a construction. The one-project case is where the
  containment is exact, and five of the six live clients are in it.
- It did not touch the double-printing. The 19m13s a client holds alone is still
  also reported under unattributed presence as bridged silence, in the same
  report. Under resolution A that is a true statement about the model rather
  than a wrong number, and it is now written into `Client`'s doc comment so a
  reader meets it there rather than by subtracting two sections.
- It did not re-measure what the previous document measured. Its unwindowed
  table, its day sweep and its four mutations were verified by reproduction
  where this work depended on them, and not repeated where it did not.
