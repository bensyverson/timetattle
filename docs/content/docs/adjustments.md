---
title: "Adjustments"
weight: 47
description: "Human corrections, keyed to wall clock, applied over the machine's answer instead of into it."
---

# Adjustments

Timetattle infers when work happened from the traces it left. The inference is a
model; it is wrong in ways no source can see, and it will be refitted many
times. A warm-up period and a meeting were both found by a person looking at a
reconstruction and saying *I was not working then*. Adjustments are where that
person's answer lives.

```
timetattle adjust -from "2026-07-28 12:00" -to "2026-07-28 13:00" \
    -assert not-engaged -note "lunch"

timetattle adjust -from 2026-07-28 -to 2026-07-29 -review unremembered

timetattle adjust -from 2026-07-28 -to 2026-07-29 -list
```

## Corrections are an overlay, not an edit

Nothing here touches the events, and nothing here touches `derived_session`.
`timetattle sessionize` deletes and reinserts every session row on every run, so
a correction keyed to a session could not survive one; a correction keyed to
**wall clock** survives all of them, because a span means the same thing at a
two-minute gap and at a two-hour one, and total presence varies 2.3× between
those two.

There was a second design available: feed corrections *into* the clustering, so
that re-deriving produced already-corrected sittings. It was rejected, and the
reason is the calibration loop.

Corrections are simultaneously the **calibration set** — the ground truth the
clustering parameters get fitted against. If they were an input, the model's own
output would already contain them, and the difference between what the model
said and what the person said would shrink toward zero for reasons having
nothing whatever to do with the model improving. The error signal would become
unmeasurable, in the one direction that looks like success.

As an overlay, the machine's answer stays untouched beside yours, and the
difference between the two *is* the model error, at any moment, for nothing.

## Six corrections, five assertions

Confirm, trim, extend, split, delete and reattribute are not six kinds of
record. Each is an assertion about a span of wall clock:

| `-assert` | says | covers |
|---|---|---|
| `engaged` | this span **was** work | extend, and confirm |
| `not-engaged` | this span was **not** work | trim, delete, and split at a real gap |
| `boundary` | one sitting ended and another began at this instant | split with no gap |
| `attributed` | this span belongs to `-project` | reattribute |
| `unattributed` | this span belongs to **no** project | reattribute to nothing |

Confirm and extend are the same assertion. What makes one a confirmation rather
than a correction is that the model already agreed — which is recovered by
comparing against `derived_session`, and is not a fact about the row.

Splitting has two forms and the first is almost always the honest one. If there
was a real gap, record `not-engaged` across it: the sitting becomes two and the
gap leaves the total. `boundary` is for a seam with no gap at all, and it
changes the count without changing any total — which makes it a claim about work
*identity* rather than about when work happened, and this tool measures the
latter.

`unattributed` is not the same as recording nothing. *This span was work and it
was nobody's project* is something you are saying; *nobody has said whose it
was* is not. 490,273 of the 809,610 events in the store are unattributed for the
second reason, and a layer that could not tell the two apart would silently turn
every one of them into an assertion nobody made.

## Reviewed, unremembered, and never looked at

`-review` records that you **looked** at a span, which is not the same as what
you found.

```
timetattle adjust -from 2026-07-28 -to 2026-07-29 -review recalled
timetattle adjust -from 2026-07-28 -to 2026-07-29 -review unremembered
```

A day you reviewed and could not remember, and a day you have never opened, are
different facts. Only `unremembered` can say the first one, and it is
deliberately distinct from **both** confirmation and correction — a reviewed day
that needed no corrections writes no adjustments at all, so a table of
corrections alone could never tell a confirmed day from an unexamined one.

`unremembered` removes nothing. The machine's answer for that span stands,
marked unconfirmed. Reading it as *no work happened* would be the two-state
version of a bug this project has shipped three times, and it deletes real work
every time.

A correction cannot hang off an `unremembered` review. The review says the span
could not be recalled and the correction says exactly what happened inside it;
one of them is false and nothing in the data says which. The database refuses
it outright.

## The log is append-only

There is no edit and no delete. The database enforces both with triggers, not
by convention: `UPDATE` and `DELETE` against either table are refused.

Changed your mind? Record the new judgment over the same span. The later
assertion wins **for the instants it covers and for no others** — asserting that
lunch was not work does not undo the morning — the earlier one stays visible,
and the fact that you changed your mind survives.

That last part is the point rather than a side effect. Every adjustment records
its origin and the moment it was recorded, and under `UPDATE` that would
describe only the latest state while silently forgetting that anybody had ever
thought otherwise. Changing your mind is also calibration data in its own right:
a person who confirms a span and later retracts it is saying something about how
far back their recall is reliable.

Recording the same judgment twice is allowed, and there is no unique index. It
says a person looked twice and said the same thing, the fold is unchanged by it,
and deduplicating would destroy the fact while changing no answer.

## What survives a rebuild

Everything. `timetattle sessionize` can be re-run at any threshold, any number
of times, and the adjustment tables come out byte-identical — which is tested by
fingerprinting them around three consecutive rebuilds, and again around a
rebuild at a *different* gap, so that the sessions genuinely change underneath
them.

The stronger property is that a correction still **means** the same thing
afterwards. A row that survived but no longer applied would pass a fingerprint
check and would still have lost the human's judgment, so that is tested
separately: the correction is folded against the rebuilt sittings and must still
remove the span it named.

## Reading them back

```
timetattle adjust -from 2026-07-28 -to 2026-07-29 -list
```

`-list` writes nothing. It prints the reviews, the corrections oldest first with
who made each and when, and the totals asserted — and it says out loud when a
span has nothing recorded against it, because *nothing recorded* and *nothing
found wrong* are the two states this whole layer exists to keep apart.

## Where a correction shows up

Recording one is half of it. Two commands read this log back, and they read
exactly the same half of it, so no two reports of the same span can differ by a
correction one of them applied and the other did not.

[`timetattle metrics`](metrics.md) lays the `attributed` and `unattributed`
assertions over its figures **by default**, and prints a
[CORRECTIONS section](metrics.md#corrections-what-a-person-said) on every
report — including when nothing was recorded over the span, because a reader
who has corrected an hour and sees no section cannot otherwise tell *applied
and agreed* from *never read*. `-uncorrected` gives the machine's answer alone,
which is the comparison [the fitting](fitting.md) rests on.

[`timetattle dashboard`](dashboard.md) applies the same assertions, over the
same window, and says in one line under its header how many were applied and
how much clock they moved. It has no `-uncorrected` of its own: it is the
human surface, and the machine's uncorrected answer stays reachable through
`metrics -uncorrected` for anyone who wants to subtract the two.

**`engaged` and `not-engaged` are deliberately not applied to either report.**
They travel in this same log, and they move the capacity denominator every
share in a report is divided by — which is precisely the residual the
clustering is fitted against. A report that had already absorbed them would
shrink the model's own error signal for reasons having nothing to do with the
model improving. Applying them needs its own case made; attribution carries no
such hazard, because no parameter being fitted is an attribution parameter.

An attribution assertion says **whose** an hour was, never that there was one.
One laid across clock no sitting covers therefore moves nothing whatever, and
both commands report that rather than swallowing it: `-assert engaged` is what
makes the hour exist in the first place.

Neither command read this log at all until 2026-08-10, and the dashboard went
on printing the uncorrected answer until 2026-08-13. A person could reattribute
a span, watch it survive re-derivation exactly as promised, and find every
reported hour unmoved with nothing anywhere saying why — which is why both now
name the population they checked instead of leaving a reader to infer it from
an absent line.

## What is deliberately not here

- **Anything keyed to a session.** Sessions are disposable and are rebuilt
  wholesale; see [the event store](../store/).
- **Invoice freezing.** Freezing a computed answer for a billed period was
  originally scoped here and moved out: an invoice does not correspond to a date
  range of engaged time, so there was nothing for a span-keyed layer to attach
  it to. If it is ever built, the mechanism is already present — the log is
  append-only, so the state of the corrections at any past instant is recovered
  by folding it up to that instant.
- **The day review.** The interface that writes into this layer is a separate
  piece of work. This is the layer it writes into.
