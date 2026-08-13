# Why this store has no ground truth, by construction

**2026-08-13.** Decision by Ben, with the argument worked out on the main thread.
This closes `PkY6I` ("Fit model parameters against ground truth") and its parent
`cfGkg`, and it closes them as **unachievable as written** rather than as
deferred work. That distinction is the whole point of writing this down: a task
left open reads as something nobody got to, and someone would reopen it.

## The problem the fitter reports honestly and cannot solve

`timetattle fit` prints `CIRCULAR` and refuses its own answer:

```
gap 15m, lead-in 5m, tail-out 5m: residual 1.257h, ranked 1 of 210 evaluated points
CIRCULAR: no point in this grid beat the parameters the labels were produced
under.
```

Reproduce with `timetattle fit -skip-days 2026-08-01,2026-02-17,2026-03-21`.

Every label in the store was produced by a reviewer confirming or correcting
blocks the model drew for him. Measured 2026-08-13:

```
sqlite3 ~/.timetattle/timetattle.db "select shown, count(*) from adjustment_review group by shown;"
  derivation|70
```

Seventy reviews, all confirming, zero blind. So the residual measures the
reviewer's agreement with the model rather than the model, and the tool says so.

## Four candidate sources of truth, and why each fails

**1. Confirming reviews.** Circular by definition — the reviewer is shown the
model's answer and asked whether he agrees. Grading the homework against its own
key.

**2. Blind reviews** (`review -blind`, shipped by `cDUJm`, made answerable by
`Qd6W0`). Strictly better: the reviewer sees where evidence was seen, with no
block and no margin on screen. But he is still reconstructing the day **from the
same evidence the model used**. It removes the model's conclusion from view; it
does not introduce an independent observation. It narrows the circularity rather
than breaking it.

**3. Job claims.** Considered seriously, and the most promising of the four,
because a claim brackets a NAMED unit of work in a KNOWN project with an explicit
declaration of when it started and finished — see the note of 2026-08-01 on
`PkY6I` and the closed task `gp0cn`. Two independent objections killed it.

  *Ben's objection, and it is the decisive one for using claims as a target:* a
  claim is preceded and followed by conversation that is itself the work. The
  discussion that decides to make the claim happens before it opens; the review
  that follows happens after `done`. So `[claim, done]` sits strictly INSIDE the
  real span at both ends. Fitting against it as though it were the span would
  make every day look like the model over-reached, and would shrink lead-in and
  tail-out to compensate — a same-signed bias on every sample, which is the exact
  failure mode this project already worries about with the morning warm-up
  margin.

  *The structural objection, which kills it outright:* `job-claims` and
  `job-events` are ALREADY EVIDENCE SOURCES the model reads. 1,637 and 14,922
  events respectively as of 2026-08-13, alongside `claude-transcripts` at
  852,373. A source the inference consumes cannot referee the inference. What
  looked like a free one-sided miss-detector — "a claim is open, so the model
  must not say idle" — is an internal consistency check on how the model weights
  an input it already has, not an outside observation.

**4. External observation.** Ben's own framing: "the only way that could be
possible is if I literally take an 8 hour video of myself working and then we tag
the work manually." That is the only candidate that is genuinely independent, and
it is not a reasonable thing to ask of anyone.

## The conclusion, and why it is not a resource constraint

**Every available label is derived from evidence the model already consumes.**
That is a property of what this tool is — an inference over the complete
observable record of a machine — and not a shortage of anyone's time. More
reviewing does not fix it. More sources make it worse, because each new source
the model reads is one fewer candidate referee.

Ben, 2026-08-13: "in reviewing past days, we are close enough for rock & roll.
It's not necessary for us to get second-level precision on this data."

## What this does NOT mean

**The tool keeps `review -blind` and the fitter keeps its population split.**
Nothing is being removed. The advice `fit` prints — "run `timetattle review
-blind` to produce labels that can" — remains correct guidance for a user whose
evidence mix differs from this one, and the refusal to fit a population nobody
recorded remains right. `xAn46` shipped that machinery and it works; it is closed
because it works, not because it is unwanted.

**The reported figures are not thereby wrong.** They are unvalidated against an
independent standard, which is a different and weaker claim, and one the tool
already states in its own output rather than hiding. `fit` refusing its answer IS
the honest behaviour, and it should keep refusing.

**A general lesson worth carrying to any tool of this shape:** the more complete
an inference's evidence coverage, the fewer independent checks remain available
to it. Coverage and verifiability trade against each other, and a tool that
ingests everything a person does has, by construction, nothing left to be
checked against.
