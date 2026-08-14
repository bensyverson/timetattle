---
title: "Fitting the parameters"
weight: 49
description: "The `fit` command: searching gap, lead-in and tail-out against the corrections, and the three separate reasons a confident answer from that search would be wrong."
---

# Fitting the parameters

`timetattle fit` searches the three clustering parameters — the idle **gap**
that separates one sitting from the next, and the **lead-in** and **tail-out**
margins each observation is expanded by — against the corrections already in the
store. It writes nothing at all.

Most of this page is about what the command refuses to say. That is the point of
it. A grid search over three parameters is twenty lines of code and will happily
return a number; the work is in establishing that the number means something,
and here it mostly does not yet.

```console
$ timetattle fit
```

**It is slow, and it is meant to be.** The default grid is 288 parameter sets,
each of which re-derives every sitting on the labelled days from scratch. On the
live store that is tens of minutes. Narrow it with `-gaps`, `-lead-ins` and
`-tail-outs` while exploring; run the whole thing when you mean to publish a
figure.

## The three reasons a naive search lies

### The gap is inert at or below lead-in plus tail-out

Each observation is expanded by the lead-in before it and the tail-out after it,
and only then are the expanded spans merged. Two observations closer together
than `lead-in + tail-out` therefore already overlap before the gap threshold is
consulted at all. **Below that floor the gap decides nothing**, and every value
in the band derives byte-identical sittings.

This was verified as a falsifiable prediction on the live corpus and held to the
digit at two settings: at 5m/5m, gaps of 1m, 8m, 9m and 10m all give 2377
sittings and 1181.8h, and 11m gives 2305 and 1182.4h.

So a search over `-gaps 1m,2m,5m,10m,15m` with 5m margins is really a search
over two values, three of which are the same model wearing different names. The
command sets those points aside, does not let them win, and says how many it set
aside:

```
3 of 5 points were not evaluated: their gap is inert, at or below lead-in plus
tail-out, so each derives exactly what the top of its band derives and is not a
distinct model.
```

`-include-inert` evaluates them anyway and checks each against the top of its
band, so the rule is tested against *your* corpus rather than trusted from a
document written about a different one. A point that disagreed would be reported
as a violation, in capitals, because it would mean the points set aside
elsewhere were set aside wrongly.

### The objective is flat over wide bands

Above that floor the objective is still a step function. It only moves when a
threshold crosses a silence that actually occurred, and real corpora have long
stretches with no silence of any particular length in them. Where the best score
is attained by more than one point, the report says so and prints the whole
plateau. It never picks one:

```
the minimum is a plateau, attained by 4 points. This report does not name one of
them, because the data does not.
```

It goes further where it can. Points that derived an **identical** set of
sittings are reported separately from points that merely scored the same,
because the first is a statement no objective could ever overturn and the second
is a property of this one.

Each parameter also gets a *profile*: the best score achievable at each value of
it with the other two free. If the profile never turns around, the command says
`NO MINIMUM` and explains that the best value is where the search stopped rather
than a fitted one. That failure has already happened once on this project —
error against observed screen-on fell monotonically from 126.2h at a 5m gap to
116.9h at an hour, and the premise was wrong rather than the arithmetic.

### The labels may be a restatement of the model

This is the sharpest of the three. `timetattle review` shows a reviewer the
blocks the *current* parameters derived and asks whether they are right. A
reviewer who agrees produces labels that are close to a copy of the derivation,
and a fit against them returns the parameters that were on the screen with an
excellent residual.

Measured on the live store: the reviewer's engaged set is a strict **subset** of
the derivation he was shown, with exactly 0.000h of reviewer-says-work-machine-
did-not, and the symmetric difference by gap is 1.476h at 2m, 1.257h at 15m,
1.983h at 30m and 2.637h at 1h — a curve spanning 1.4h out of 143h whose minimum
sits exactly at the value he was shown.

So `fit` takes a `-reference` point, defaulting to the configured parameters, and
evaluates it whether or not it is in the grid:

```
THE PARAMETERS THE REVIEWER WAS SHOWN
gap 15m, lead-in 5m, tail-out 5m: residual 1.257h, ranked 1 of 210 evaluated points
CIRCULAR: no point in this grid beat the parameters the labels were produced
under. That is the expected outcome when a reviewer confirms blocks he was shown,
and it means this search has measured the reviewer's agreement with the model
rather than the model.
```

## Two populations of label, never averaged silently

A label formed by **confirming** blocks the model drew and a label formed
**blind** — shown where evidence was seen, with no block and no margin on the
screen — are worth completely different amounts. `adjustment_review.shown`
records which is which, and `fit` is the reader that acts on it.

Every run prints the census, and it prints **every** population whether or not
anything was recorded for it:

```
WHAT THE REVIEWER WAS SHOWN
population                 assertions    days     engaged   not-engaged   of the labelled time  fitted
confirming a derivation           438      14    142.147h      193.853h                 100.0%  yes
blind                               0       0      0.000h        0.000h                   0.0%  none recorded
no label in this fit was produced blind, so this search cannot separate the model
from the reviewer's agreement with it. More days of the same kind will not change
that; run `timetattle review -blind` to produce labels that can.
```

The empty row is the point. "Nothing was recorded blind" is an observation, and a
table listing only what it found could not express it.

What that line is saying is that every assertion in the fit is a reviewer
agreeing or disagreeing with blocks the model itself drew for him. His engaged
set is therefore close to a restatement of the derivation, which is why the
remedy is a different *kind* of label rather than more of the same one.

`-shown` fits one population alone: `derivation` (spelt `confirming`) or
`observations` (spelt `blind`). **A population nobody recorded is refused rather
than fitted**, because a residual over zero labels is `0.000h`, which is
indistinguishable from a perfect fit:

```
$ timetattle fit -shown blind
timetattle fit: -shown blind cannot be fitted against …/timetattle.db: no label in
this ground truth was produced blind: 496 confirming a derivation and 0 blind. A fit
restricted to a population nobody recorded would score 0.000h over zero labels,
which reads as a perfect fit and is a statement about nothing
```

Both counts appear on purpose. "No blind labels" and "no labels" call for
different actions.

### A blind label narrows the circularity; it does not break it

Worth being exact about what `-blind` buys, because it is easy to read the advice
above as promising more than it does. A blind review removes the model's
*conclusion* from the reviewer's screen. It does not give him a second,
independent observation of the day: he is still reconstructing from the same
evidence the model read. That makes a blind label worth much more than a
confirming one, and still less than a measurement taken from outside.

Which raises the question of what a genuinely independent check would be, and the
answer is uncomfortable for a tool of this kind. Anything this tool ingests is
disqualified from refereeing it. A task tracker looks like an excellent
candidate — a claim brackets a named unit of work with a declared start and
finish — right up until you notice `job-claims` is already a source, at which
point checking the model against it measures the model's own weighting of its own
input. The same disqualifies AI transcripts, git history and file mtimes.

**So coverage and verifiability trade against each other.** The more of a
person's observable record an inference consumes, the fewer independent standards
remain to judge it by, until the only one left is watching them work. That is a
property of the design rather than a gap in it, and the honest response is the
one `fit` already takes: report the circularity in the output, refuse a residual
that would read as perfect, and let the reader discount the number accordingly.

On this repository's own store that reasoning is followed to its conclusion in
`project/2026-08-13-why-there-is-no-ground-truth.md`, which records why the
calibration phase was closed as unachievable rather than left open as unfinished.
A user whose evidence mix is narrower than this one has more room, and for them
the advice above stands unchanged.

### A pooled run says where each population would have gone alone

Reporting the split at the winning point is not enough, because the question is
whether the blind labels would have chosen a *different* point. Every evaluated
point therefore carries its residual split by population, and a pooled run prints
each population's own minimum over the whole grid:

```
EACH POPULATION ON ITS OWN
population                best residual    labelled     moved   attained at
confirming a derivation          0.000h        4.1h    0.600h   gap 25m, lead-in 1m, tail-out 1m
blind                            0.000h        0.4h    0.300h   gap 5m, lead-in 1m, tail-out 1m
NO POINT IS BEST FOR BOTH POPULATIONS. The pooled minimum reported above is
therefore the larger population's answer carried by weight, and not something the
labels agree on. […] prefer the blind row: it is the only one formed without the
parameters on the screen.
```

Read that table. **Both populations fit their own labels perfectly, at different
answers**, and the pooled search returns the larger one's — 4.1 labelled hours
against 0.4 — with a pooled residual of 0.300h. Those figures come from the
constructed corpus in `TestSearchReportsEachPopulationsOwnMinimum`; reproduce
them with `go test ./internal/fit/ -run
TestSearchReportsEachPopulationsOwnMinimum -v`.

### The one case that is refused rather than reported

Each whole-day review tiles its day completely. A day confirmed once and later
reviewed blind is therefore covered **twice**, so a pooled residual counts it
twice in its own denominator and is not a residual over a partition at all.
There is no wording that makes that number mean something, so the pooled run
refuses it:

```
timetattle fit: the labels in …/timetattle.db cannot be pooled: 1 day (2026-07-11)
labelled both blind and confirming a derivation, and each labelling tiles the whole
day, so a pooled residual would count that time twice in its own denominator. Fit
one population at a time with -shown derivation or -shown blind
```

Note that this is a refusal to *pool*, not a refusal to fit: each population is
still a sound partition on its own, and `-shown` fits either happily.

## What a residual is worth

Every residual is printed beside the two no-skill nulls — what a model claiming
the whole labelled span as work would score, and what a model claiming none of
it would — and beside how far the objective moved across the entire search.

That last figure is the one to read first. If the objective separates the
candidates by 1.4h against a null of 244h, the parameters explain 0.6% of the
available error, and a small residual is a statement about how much the labels
and the derivation already agree rather than about how good the parameters are.
Whenever that share falls below 5% the command prints the two figures under a
`READ THIS BEFORE QUOTING THE RESIDUAL` heading and says that an interval from
the search is worth reporting and a point estimate from it is not.

The residual itself is always two-directional and never collapsed:

- **orphaned** — time the reviewer called work that the model does not claim.
- **contradicted** — time he called *not* work that the model claims.

A model that sees too little and a model that sees too much are different
failures, and a single figure cannot tell a fitter which way to move.

## Two evidence regimes, reported apart

Every interval-shaped source in the live store begins in July 2026:
`biome-appusage` from 2026-07-06, `knowledgec-backlit` from 2026-07-02. Before
that, every observation is a **point** — a commit, a save, a message — which
covers no duration at all. A sitting assembled only from points has zero observed
length, so its whole extent is manufactured by the margins.

`fit` therefore measures, per reviewed day, the share of the time the reviewer
called work that some observation with an actual duration covered. It calls that
**coverage**, and splits the days into a `point-only` and a `presence-covered`
regime at `-coverage-floor`, which defaults to 1%.

Coverage rather than a date cut, deliberately: the date is a proxy for this
quantity, and a day that happens to hold an interval source early, or none late,
lands where it belongs rather than where its date says.

The split matters because the two regimes are not equally represented. On the
live store, fourteen of sixteen usable reviewed days sit in the presence-covered
regime at a median margin near 5%, where the parameters have almost nothing to
decide. The regime that holds 75.7% of all presence is represented by **two**
days, both of which lost some evidence to the exclude list. Pooling the sixteen
gives a precise-looking answer dominated by the fourteen that cannot
discriminate, so the report does not pool them, and it says outright when a
regime has too few days to fit anything from.

## Narrowing, and why it does not change the answer

The live store holds nearly a million events and one clustering of it takes about
half a minute, so a two-hundred-point grid over the whole corpus is a long wait.
The labelled days are sixteen. `fit` therefore narrows the corpus to the events
that can affect what is derived inside those days — and the narrowing is exact
rather than approximate, or it would be a silent corruption of the answer rather
than an optimisation of it.

The rule: take `reach` to be the largest distance across which two observations
can end up in one sitting anywhere in the grid, which is the largest gap or the
largest lead-in plus the largest tail-out, whichever is greater. Group the
evidence into maximal chains no two consecutive members of which are further
apart than `reach`, and keep a chain when its span, expanded by `reach`, touches
a labelled day. A chain that fails that test cannot contribute to any sitting
overlapping any label under any point of the grid, because every route by which
it could is a step of at most `reach` and every such step is already inside a
chain.

Presence readings are kept where they overlap a kept chain's reach and only
there. Darkness acts on a single instant — the start of an observation it might
suppress — so a reading that reaches no kept chain can change nothing inside a
labelled day. Anything whose extent cannot be read at all is kept, because being
wrong in the direction of carrying too much costs a little time and being wrong
in the other direction changes an answer silently.

Every run says what it removed:

```
narrowed to the labelled days: 258386 observations kept of 933452 read, 675066
dropped, closure taken at 15m across 1123 chains of which 105 could reach a label
```

The last figure is worth reading. Widen the reach to an hour and the same corpus
narrows to **six** chains holding 94,811 observations for a two-day window,
because dense July days leave no silence longer than an hour for weeks at a time.
Narrowing buys a factor of three to ten rather than a factor of sixty, and that
is a fact about the corpus rather than about the closure.

`-narrow=false` searches the whole corpus, and the two must produce the same
residual. That is held by a test against the live store as well as against
constructed cases, because a closure that is exact on eleven synthetic events
proves nothing about one taken over 900,000 real ones.

## What this command is not for

- **It does not change any parameter.** It prints; `internal/config` is edited
  by a person who has read the printout.
- **It does not feed corrections into the clustering.** Adjustments are an
  overlay on purpose. If they were an input, the model's output would already
  contain the human's answers and the residual would shrink toward zero for
  reasons having nothing to do with the model improving.
- **It does not fit billability.** "Did a human spend time here" is what these
  parameters are for; "is that time billable" is a judgement made at invoice
  time.

## Flags worth knowing

| flag | what it does |
|---|---|
| `-gaps`, `-lead-ins`, `-tail-outs` | the grid, as comma-separated durations |
| `-shown` | which labels to fit: `all`, `derivation` (`confirming`) or `observations` (`blind`); a population nobody recorded is refused rather than scored as zero |
| `-reference` | the parameters the reviewer was shown, as `15m/5m/5m`; `none` to omit the circularity check |
| `-include-inert` | evaluate the inert points too and check the rule on this corpus |
| `-tolerance` | how much worse than the best a point may score and still be inside the reported interval; zero, meaning exact ties, is the default and any other value is an assertion about noise nobody has measured |
| `-skip-days` | reviewed days to leave out; defaults to `2026-08-01`, which was reviewed while it was still happening |
| `-coverage-floor` | the share of asserted work needing a duration-bearing observation before a day counts as presence-covered |
| `-narrow` | `-narrow=false` searches the whole corpus instead of the closure of the labelled days |

## See also

- [Sessions and sittings](sessions.md) — what the parameters actually do.
- [Adjustments](adjustments.md) — why corrections are an overlay and never an
  input.
- [Reviewing a day](review.md) — where the labels come from, and why a reviewer
  agreeing with a derivation is weaker evidence than it looks.
