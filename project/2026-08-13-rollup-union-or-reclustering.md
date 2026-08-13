# A client's hours: re-clustered, or the union of its projects?

2026-08-13. Agent `interleave`, task qAkml. **This document measures the choice.
It does not make it.** Nothing in `internal/metrics` is changed by the commit
that carries this file, beyond two tests that pin what was measured.

> **SETTLED LATER THE SAME DAY, and one recommendation below is wrong.** Ben took
> **A** — keep the re-clustering, replace the guard — on the reasoning that
> switching between two of a client's projects has a cost neither project's
> evidence records, so the bridged stretch is work and 19m13s is likely an
> undercount. See `project/2026-08-13-rollup-reclustering-resolved.md` for the
> decision and its acceptance figures.
>
> The correction is to the closing recommendation that the replacement be *"a
> client with exactly one project equals that project to the nanosecond"*. That
> equality is **not true by construction** and this repository's own corpus
> already refutes it: a project worked for one client in the morning and another
> in the afternoon (`TestAClientAndAProjectSharingANameAreNotMerged`, which
> predates this document) gives the afternoon client 10m against its only
> project's 20m, and a directory registered to a client partway through its life
> does the same — the project holds clock its client does not, which the report
> calls `Clientless`. Applied as a mutation, the equality passes the whole live
> store byte-identically while refusing two legitimate reports in the cold
> suite: silent on real data, refusing on ordinary shapes, which is the exact
> latency profile of the guard it would replace. What shipped is the same
> sentence as an **inequality** — a one-project client can never *exceed* its
> project — which catches the mutation this document proved the old guard for,
> at the same 3h38m2.96s. — `unionist`, 2026-08-13

## The short version, and it corrects the record

Three things were believed about the contradiction between the roll-up's
implementation and its own closing guard when this task was written. Two of them
are wrong:

| believed | measured |
|---|---|
| the interleaving has never occurred on the live store | it occurs on **10 day-windows**, and holds **19m13s** of the unwindowed report |
| the guard has never fired, so the defect is latent | it **fires today**: `timetattle metrics -from 2026-04-24 -to 2026-04-24` exits 1 and prints no report |
| the refusal now blanks `dashboard` too | true of the **call path**, and no trailing window refuses today — but 22 of them have **exactly zero** headroom |

The doc comment on `metrics.Client` — that a client's hours are the union of its
projects' spans — is **already false of the shipped report**, on data that
exists, and has been since at least February. It is false by 19m13s.

## What was measured with, and what population each figure describes

The instrument ships, as `scripts/metricsphases` does, rather than being an
agent's throwaway edit to `internal/metrics`:

```console
$ go run ./scripts/rollupunion                          # the whole store
$ go run ./scripts/rollupunion -sweep day               # one closed window per calendar day
$ go run ./scripts/rollupunion -sweep suffix -from 2026-04-01   # the shape `dashboard` computes
$ go run ./scripts/rollupunion -from 2026-04-24 -to 2026-04-24
```

It reads the store **read-only** and re-derives each client's total from
`Report.Segments` — the same partition every figure in the report is already
summed from — so it measures the shipped rules rather than its own idea of them.
Clients are **numbered, not named**: the store holds real client names and this
output is meant to be quotable. `-names` prints them for reading on this machine
and says so in its own header.

Two quantities, and they are different thresholds on one phenomenon:

- **ALONE** is clock the client holds that no project of its own holds — a
  silence bridged only once the client's evidence is taken together. It is what
  falsifies the doc comment, and it is exactly what a merge-the-spans roll-up
  would take away.
- **MARGIN** is `sum of the projects − the client`, which is what the guard
  actually tests. The sum is inflated by every instant two of that client's
  projects are active at once, so the margin can stay large while ALONE is
  non-zero. **When the margin goes negative there is no report at all.**

### Population A — unwindowed, the whole store

`go run ./scripts/rollupunion`, 2026-08-13, shipped defaults (gap 15m, lead-in
and tail-out 5m). 1,073,726 events read, 1,071,969 after exclusions, 6 clients.

| client | projects | engaged | with a project | alone | runs | largest | also unattributed | summed | margin |
|---|---|---|---|---|---|---|---|---|---|
| the 33-project client | 33 | 617h40m54s | 617h21m41s | **19m13s** | 11 | 4m19s | 19m13s | 701h39m46s | 83h58m52s |
| each of the other five | 1 | — | — | 0s | 0 | 0s | 0s | — | **0s** |

Three readings matter here.

**The doc comment is false by 19m13s**, in 11 separate stretches, on the
unwindowed report — while the guard has 83h58m52s of headroom and stays silent.
The two are not the same claim and cannot be settled as one.

**Every one of those 19m13s is printed twice in the same report.** A stretch no
project covers is a segment with no project on it, so it is also counted under
UNATTRIBUTED PRESENCE as "silence the gap threshold bridged". The report says
both that this clock belongs to a client and that it could not be attributed.

**A client with one project has a margin of exactly zero, by construction** —
its union and its sum are one span. Five of the six clients are in that state, so
the guard is a strict equality for them and has no slack whatever. That is not a
weakness; see the mutation below, where it is the whole of what the guard caught.

Reconciling with `rollup`'s figures of 2026-08-13 in
`project/2026-08-13-metrics-phase-timings.md`: 1,069,011 read / 1,067,254 kept
there against 1,073,726 / 1,071,969 here, **+4,715 in both columns** — the same
store, later in the day, after further harvests. No figure is in conflict.

### Population B — one closed window per calendar day

`go run ./scripts/rollupunion -sweep day`. The store's segments span 4,712
calendar days (2013-09-19 to 2026-08-13; the early dates are file modification
times). Only **82** of those windows hold a client with more than one project,
and only those can exercise the guard at all — a sweep that quoted 4,712 as its
denominator would be reporting mostly on days when nothing could have happened.

- **10 windows** hold clock a client alone held: 2026-02-16, -17, -20, 03-11,
  -12, -24, -25, -27, -28, 04-02. Each holds exactly one such stretch, between
  9.5s and 4m19s.
- **1 window is REFUSED: 2026-04-24.** A client credited **6h7m1.996532s**
  against projects summing to **6h4m48.248532s**; margin **−2m13.748s**.

Verified independently against the shipped binary rather than taken from the
script:

```console
$ timetattle metrics -all -from 2026-04-24 -to 2026-04-24 ; echo $?
timetattle metrics: client <redacted> is credited 6h7m1.996532s across projects
summing to 6h4m48.248532s; a roll-up is the union of its projects' spans and can
never exceed their sum
1
```

Scoping does not avoid it. `metrics -project <one project>` over the same day
fails identically, because the scope selects rows for display and `Compute` has
already run over everything.

**Two day conventions, both refused.** The sweep cuts its windows at the
configured working-day boundary — 05:00 in the calendar this store uses — while
`metrics -from D -to D` cuts at midnight. Those are different windows over the
same date and they are not interchangeable; 2026-04-24 is refused under both,
which is why the script's finding and the binary's are quoted side by side
rather than one standing for the other.

**The anatomy of the refusal**, from auditing that day with the guard mutated
off: the client has **three projects that day with no overlap between them at
all** — union and sum are both 6h4m48.248532s — and one bridged stretch of
2m13.748s. The sum offered no inflation to hide behind, so a single bridged
silence was enough. That is the general shape: *the guard fires when a client
works its projects one after another rather than concurrently*, which is the
ordinary way to spend a day.

### Population C — trailing windows, which is what `dashboard` computes

`go run ./scripts/rollupunion -sweep suffix -from 2026-04-01`: 135 windows, each
running from that day to the end of the store, which is the shape `dashboard
--days N` and its per-engagement allocation reports actually compute.

- **0 of 135 refused.** `timetattle dashboard --all --days 40` exits 0 today.
- 24 windows hold clock a client alone held, up to 6m33s in one window.
- **22 consecutive windows — trailing periods starting 2026-07-04 through
  2026-07-25 — have a margin of exactly 0s** for a client with more than one
  project. Not "small": zero. One bridged silence in that window and the
  dashboard prints nothing.

So the brief's claim that the refusal "now also blanks the human surface" is
**true of the call path and not yet true of this store**. `dashboard` calls
`metrics.Corrected` and returns its error unchanged, so a refusal there produces
no dashboard at all rather than a dashboard missing a row; that is pinned by
`TestTheRollUpRefusalArrivesThroughTheCallTheDashboardMakes`. What is not
established is any trailing window that refuses today. `metrics` does refuse
today, on a one-day window.

## What the guard was catching, established by mutation

Four mutations, each applied to `internal/metrics/metrics.go` alone, measured,
and reverted. The question is not "does the guard fire" but **"does it fire on
anything the check immediately above it does not already catch"** — that check
compares each client's segment-derived total against its own clustered spans and
is a strict equality.

| # | mutation | what caught it | number |
|---|---|---|---|
| 1 | the guard itself removed | 2 tests in the whole repository, **both of them pins of this refusal** (`rollupcost_test.go`, and the script's own parse test) | nothing else in ~2,000 tests objects |
| 2 | the CLIENT level clustered at gap 30m while projects stay at 15m | **the guard, and nothing else** — the entire suite passes | on the live store, unwindowed: a **one-project** client credited 51h39m27.261521s against 48h1m24.29735s, catching **3h38m2.96s** of inflation |
| 3 | client engaged over-accumulated by 1% in the segment loop | the preceding check, "missing a cut" | 16 failures; the guard fired 0 times |
| 4 | client engaged accumulated once per active project — *the "roll-up computed as an addition" bug the guard's own comment names* | the preceding check, "missing a cut" | 14 failures; the guard fired 0 times |

**So the guard does not catch the bug its comment says it exists to catch.** An
addition-shaped error in the segment accumulation makes the client's total
disagree with its own clustered spans, and the tighter check above catches it
first, every time.

What the guard **does** catch is mutation 2: an error in `reduce` that inflates
the client's own spans, where the segments and the spans agree with each other
and only the relationship to the projects is wrong. That is a real class and
nothing else in the repository catches it — the whole suite passed under it.

Note *where* mutation 2 fired: on a client with **one** project, where union and
sum are the same span and the guard is an exact equality. That is worth carrying
into whatever replaces it.

And with the guard removed, the refused day is not merely permitted — it
produces a complete and self-consistent report: 7.4h of presence across 5
sittings, absolute 7.4h against 7.4h of presence at 1.00x, allocation closing
exactly, every other assertion in `Compute` passing. The guard was the only
objection to it.

## The two resolutions, with the figures each changes

Cost is not a discriminator and should not be argued: `rollup` measured the
roll-up at 0.537s, 9.2% of a 5.8s run.

### A — keep re-clustering; the guard and the doc comment are what is wrong

**What changes on the live store: no figure at all.** Every number in the
unwindowed report stays as it is. What changes is that windows that produce
nothing today start producing a report — one of 82 multi-project day-windows
immediately, and the 22 zero-margin trailing windows stop being one bridged
silence away from blanking the dashboard.

Gained: the two levels stay computed by one rule. A project's hours already
include the silences its own gap threshold bridged — that is what clustering
*is* — and under A a client's do too, one level up. `reduce` exists so that both
tiers go through one implementation, and A is the answer that keeps that true.

Lost: the guard, and with it the only thing in the repository that catches
mutation 2 — unless it is replaced. It must be replaced; see below.

Left standing: the 19m13s that is credited to a client and simultaneously
printed as unattributed bridged presence. Under A that duplication is permanent
and should be documented in `Client`'s doc comment rather than left for a reader
to discover by subtracting two sections of the same report.

### B — merge the per-project spans; make the guard's invariant true by construction

Measured, not predicted. Implemented crudely — each client's active, evidenced,
attested and weak spans replaced by the union of its projects' — and run against
the live store:

- The whole-store report differs in **exactly one row**: the 33-project client's
  absolute **617.7h → 617.4h** (−19m13s, 0.052% of that client's hours) and its
  allocated **603.3h → 603.0h**, share 51.8% → 51.7%. Every other figure in the
  report, including all 38 project rows, presence, and unattributed presence, is
  byte-identical.
- The refused day computes, and the client reads 6h4m48s — equal to its
  projects' sum, since they did not overlap.
- The test suite: one failure, the pinned contradiction test, which its own
  comment says to delete once the question is settled.

Gained: the guard's invariant becomes true by construction, so nothing can
refuse; the doc comment becomes true; and the double-printing above disappears —
the 19m13s stays in unattributed presence and only there.

Lost: a client no longer gets credit for a silence bridged only when their
projects are considered together, which means **the two levels are computed by
different rules**. A project keeps its bridged silence, its client does not.
Nineteen minutes is small; the rule is not.

## Recommendation, which is a recommendation and not a decision

**Take A — keep re-clustering — and replace the guard rather than deleting it.**

1. B is not a restoration. The union invariant is already false on real data, so
   B does not "make the guard true again"; it changes what a client's hours mean
   and then makes the guard true. That is a legitimate choice, but it should be
   made as a change of definition and not as a bug fix.
2. Bridged silence is the model. The whole package credits a project the silence
   its own evidence bridges; a roll-up that refuses to do the same one level up
   is two models in one report, which is the failure `reduce` was written to
   prevent.
3. The costs point the same way. A changes no live figure and unblocks a
   surface that fails today; B changes a live figure by 19m13s and unblocks the
   same surface.

The one strong argument for B is the double-printing, and it survives under A as
a documentation debt: 19m13s of one client's hours is also reported as presence
nothing could be attributed. That is a true fact about the model and should be
written into `Client`'s doc comment, not hidden by changing the number.

**Whichever is chosen, mutation 2 must stay caught.** Two candidate replacements,
and only one of them works:

- *A client with exactly one project must equal that project, to the
  nanosecond.* Unconditionally true under both models, since the two are
  clustered from identical evidence. **This is what caught mutation 2** — the
  mutation fired on a one-project client — and five of six live clients are in
  that state.
- *A client's spans must contain the union of its projects' spans.* True under
  A by construction and an equality under B, and it does **not** catch mutation
  2, which inflates the client's spans in the containing direction.

Neither is a tolerance, and neither admits "close enough" on a quantity nobody
has subtracted.

## What this did not do

- It did not change `internal/metrics`. The guard, `reduce` and `Client`'s doc
  comment are exactly as they were at HEAD.
- It did not delete `TestTheRollUpAndItsOwnGuardContradictWhenAClientsProjectsInterleave`.
  That test's own comment says whoever settles the question should; the question
  is not settled here.
- It did not sweep trailing windows earlier than 2026-04-01. Those are the
  windows with the *most* masking overlap and the least chance of refusing, and
  each costs a near-full read of the store. Nothing about them is claimed.
- It did not measure any window under a non-default gap. Every figure here is
  the shipped defaults, and the gap threshold moves engaged time by 2.3x across
  the plausible range, so none of these numbers should be quoted for another
  parameter set.
