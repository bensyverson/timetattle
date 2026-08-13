---
title: "Reviewing a day"
weight: 48
description: "The `review` command: a whole working day on one screen, confirmed in one keystroke, and why confirming a day is a complete accounting of it rather than a shrug."
---

# Reviewing a day

`timetattle review` puts an entire working day on one screen and asks whether it
is right. Press Enter and it is confirmed. Press `n` and the day is walked block
by block, and each block can be trimmed, extended, deleted or reattributed
without leaving the keyboard.

It exists because nothing downstream is believable until the clustering
parameters are fitted. **75.7% of the presence in the live store is margin
that unfitted parameters invented** — 901.8h of 1191.8h, from `timetattle
sessionize` on 2026-08-01. Fitting them needs ground truth, and this is where
ground truth comes from.

Friction here is therefore not cosmetic. A review pass that feels like a chore
does not happen, and then nothing gets fitted. The fast path is one keystroke
per day, measured: twenty real days from the live store took twenty keystrokes
and produced 568 assertions.

```console
$ timetattle review

Tue 2026-07-28  ·  13 sittings, 12h47m  ·  2h05m (16%) inferred  ·  never examined
gap 15m  lead-in 5m  tail-out 5m  ·  derived 2026-08-01 11:36

                 05   07   09   11   13   15   17   19   21   23   01   03
sittings         ██████████·░██████████████████████·█·█████··················
acorn            · ··█·█··········███████████░███······························
birch       · ████·███░······██···█·······································
timetattle     · ···········░█·████░██████···░██░██···█████··················
Woodcase       ! ·······································█····················
(no project)   · ██████████·░█████████████████████░·█·██·██··················
   1 05:18..07:59 2h41m 1448 obs acorn, birch biome-appusage and 7 more
   2 08:04..08:42   38m   58 obs birch      biome-appusage, claude-transcripts
   ...

  █ observed in the cell   ░ inferred by the clustering   · nothing recorded

[Enter] right   [n] wrong, walk the blocks   [u] cannot remember   [a] add missed
work   [s] skip   [q] quit  >
```

## One row per active project

The rows are the clients and the columns are the time, so looking
client-by-client and looking block-by-block are two readings of one picture
rather than two modes.

One row is the ordinary day. Measured on the live store on 2026-08-01: 589 days
carry attributed evidence, **74.4% of them have exactly one project**, the mean
is 1.67 and the maximum is 9. Concurrency is real and rare, so it has to be
representable without taxing the three quarters of days that have none.

A row shows where that project's **own** evidence clusters, intersected with the
day's sittings — internal/metrics' model B, and not the whole of every sitting
the project appears anywhere inside. That second reading was measured to sum to
24.01h across a 12.78h day. On real evidence model B produces slivers rather
than bars, and the slivers are the finding: every attributable source in this
corpus emits *instants*, so two clients alternating inside one sitting come out
as an interleaving, which is what alternating actually looks like.

A cell reads as observed (`█`) if **any** observation falls in it, rather than
if most of it was covered. Under a majority rule a corpus of instants would
render a day of solid work as entirely inferred.

## A weak row is marked, never dropped

`!` beside a row means every observation behind it is a **file's modification
time**. A file changed, and nothing recorded anybody changing it — a
log-rotation cron writes those too, and one of them once fabricated 135.2h for a
client nobody had opened.

The mark is a mark and **not a filter**. Measured on the live store on
2026-08-01 with the exclude list in place, 88 project-days across 28 projects
rest on nothing else: RapidPro's `MBCommon.h` on 2013-09-19, a cypress design
PDF on 2025-12-04, one note in Woodcase on 2026-03-21. Each is plausibly a real
evening spent editing one document, which is exactly why nothing removes them
and why the screen asks instead. Reproduce the list with:

```console
$ go run ./scripts/weakcensus -verbose
```

**The unit is the project row, not the day.** A quarter of days carry two or
more projects, so a day can be strong for one client and weak for another at
once, and a whole-day verdict would misrepresent both a quarter of the time. In
the screen above the day is attested for acorn, birch and timetattle, and weak
for Woodcase on the strength of one file save at eight in the evening.

The mark costs the timeline two cells and never the terminal, so a marked row
fits the window exactly as an unmarked one does. `timetattle review -list` says
how many rows of each offered day are weak, so a reviewer sees one coming rather
than finding out after opening it.

One recorded act carries the whole row: a day of file saves with a single commit
in it is a day somebody was demonstrably there. Which sources count as recording
an act is a judgment made in one place, `internal/source`, and an
**unclassified source is weak** — not having weighed a stream is not the same as
having found it strong.

## Confirming a day is a complete accounting of it

This is the property everything downstream rests on, and it is the reason a
wholesale confirmation is a dense signal rather than a vague one.

Confirming does not only say where work *was*. It says, for every instant of the
day, whether it was work: each block becomes an `engaged` assertion and **every
gap between them becomes `not-engaged`**. Confirming 2026-07-28 writes one
review and 27 assertions, pinning 26 boundaries exactly.

Without the second half a confirmation would be a lower bound. Every parameter
that lengthens a sitting would fit at least as well as every parameter that
shortens one, and the gap threshold — the parameter with 2.3x of leverage over
the headline figure — could not be fitted at all.

The one thing that punches a hole in the accounting is a block nobody can
remember. Filling that hole with `not-engaged` would turn *I cannot remember*
into *I was not working*, so the hole is left open and a review row explains it.

A wholesale confirmation says **nothing about attribution**, deliberately. A
quarter of days carry two or more projects, so one `attributed` assertion across
a block would be false on them; and the parameters being fitted are all
time-boundary parameters, which attribution is not one of. Attribution is
asserted only where a reviewer corrects it with `p`.

### Correcting who a block belonged to — `p`

Walking the blocks (`n` at the day prompt, or a sampled day) offers `p` on each
one. It asks who the block really belonged to and takes a project name, or a
blank line:

```console
  block 2 of 6  10:00..11:30  1.5h  acorn
  [Enter] right  [n] not work at all  [s] start  [e] end  [p] project  ...
  > p
  it belonged to? (a project, or blank for none) birch
```

A name records an `attributed` assertion over the block's span; a blank line
records `unattributed`, which asserts the block belonged to **no** project. That
is a thing a person can say and is not the same as saying nothing — the store
has a separate assertion for it precisely so the two cannot collapse.

`p` does not settle the block, so the same block can be reattributed *and*
trimmed before you press Enter.

**What it moves.** [`timetattle metrics`](metrics.md#corrections-what-a-person-said)
reads these and reports what they changed: hours leave every project the span
was not asserted to and arrive at the one it was, counted as *asserted* rather
than as evidence, and a blank answer moves them into unattributed presence under
a reason of its own. Presence never moves — an attribution says whose an hour
was, never that there was one — so a correction laid over clock no sitting
covers is reported as having moved nothing rather than silently doing nothing.

## Four states, and they are four on purpose

| | what it means | what it writes |
|---|---|---|
| never examined | nobody has opened the day | nothing at all |
| agreed | looked at, recalled, corrections reproduce the machine's answer | a review and the closure |
| corrected | looked at, recalled, corrections change the answer | a review and the corrected closure |
| unremembered | looked at and could not be recalled | a review, and no assertion either way |

`s` skips and records nothing, leaving the first. `u` records the fourth. *I
never looked* and *I looked and cannot remember* are different facts, and this
project has collapsed that distinction three times in three separate tools.

Agreed and corrected are **computed** rather than stored: the machine's answer
is compared against the overlay folded over it. A flag would be a second
spelling of a fact the rows already carry.

## Reviewing a day that has not finished

Recall is best on the day itself and the queue offers the most recent days
first, so reviewing **today** is the ordinary case rather than an edge one. But
the closure above accounts for the *whole* working day, and the gap running from
the last block to the day's end would be asserted `not-engaged` like any other.
Confirmed at lunchtime, that is a claim about hours nobody could have looked at.

So the accounting stops at the moment of review. Everything up to it is asserted
either way as usual; **the remainder gets no row at all**, which is the same
*never examined* state a skip leaves. The day's own review row records the span
that was actually looked at, and the queue offers the day again so its evening
can be reviewed once it has happened. A listing marks it:

```
2026-08-01  Sat   11 sittings   8h14m  agreed, part of the day
```

Clipping silently was the alternative and was rejected: it records a partial day
that a later reader cannot tell from a whole one.

The last block may reach a few minutes *past* the moment of review, and that is
not the same thing. A sitting runs to its last observation plus the tail-out, so
a block drawn from an event at 12:59 ends at 13:04 under the shipped 5m and a
reviewer confirming at 13:00 is endorsing it. That endorses an inference about
work already observed rather than asserting anything about unobserved hours, so
it stands and the review row covers it. Work **added** by hand past that moment
is refused rather than clipped — a person may report work the model missed, but
not work that has not happened.

This was not hypothetical. Exactly one review row and one adjustment in the live
store reach past their own `recorded_at`, both on 2026-08-01, both by 15h36m,
and the resulting assertion contradicts 2.194h of sitting that happened
afterwards. The adjustment tables are append-only, so that row is permanent; see
`project/private/2026-08-02-are-the-reviews-still-valid.md`.

## Three horizons, and the earliest of them binds

The moment of review is not the only thing that bounds what may be asserted, and
treating it as though it were cost 1.4379h of false `not-engaged` on a single
day. A review may account for a stretch only as far as something has **looked**
at it, and three things can stop short:

| horizon | what it means | what to do |
|---|---|---|
| **review** | the rest of the day has not happened | come back tomorrow |
| **clustering** | `sessionize` has not run this far, so no sitting can exist there whatever the evidence says | re-run `timetattle sessionize` |
| **harvest** | some source has not been asked this far, so its silence is the store's rather than the world's | fix or re-run the harvest |

The day view draws the stretch past the earliest of them as a fourth cell state
and says which one is binding:

```
                 05   07   09   11   13   15   17   19   21   23   01   03
sittings         ██████████·░████████████████·█????????????????????????????

the clustering last ran 1h45m ago (2026-08-01 16:39Z), so nothing after that has
been clustered at all, however much evidence is under it. last asked:
biome-appusage 23m ago, claude-transcripts 23m ago

NOTHING HAS LOOKED AT THIS DAY PAST 11:39 (the clustering), so the ? cells are
unasked rather than empty and nothing will be recorded over them.
```

`·` and `?` are different facts and this project has collapsed that pair three
times. `·` is *every source reported quiet*; `?` is *no source has been asked*.
A reviewer shown `·` for an unharvested morning correctly agrees that it was
empty, and the morning was not empty — the store was.

**The measurement, and it corrects an earlier one.** The 2026-08-01 defect was
attributed to harvest lag. It was not: harvests ran through that afternoon, the
last finishing 23 minutes before the review, and every observation under the
disputed stretch was already stored. The **clustering** was 1h45m behind, and
that alone accounts for the 1.4379h. See
`project/private/2026-08-02-what-the-reviewer-could-not-have-seen.md`.

A cell that straddles the horizon and holds real evidence still draws as
observed. Hiding an observation would be the same error pointing the other way.

## Reviewing from the observations instead — `-blind`

The ordinary pass has a defect that no amount of further reviewing can fix.
Because it draws the derived sittings and asks whether they are right, an
agreeing reviewer produces an engaged set that is a **strict subset of the
derivation**. Over the fourteen presence-covered reviewed days the fit residual
at the shipped 15m/5m/5m is *exactly* 0.000h, contradicted is 0.000h at every
point in the grid, and `timetattle fit` prints `CIRCULAR` and refuses to report
an answer.

`-blind` puts the **observations** on the screen and nothing else — no block, no
sitting, no margin, no parameter — and changes the question from *is this
right?* to *when did you start and stop?*

The day is drawn as **one chronology** — silence, what was seen, silence — and
each silence is then put with the raw observations flanking it:

```console
$ timetattle review -date 2026-07-17 -blind

Fri 2026-07-17 · 576 observations · 24 silences of 2m or more, 24 of them asked about

                 05   07   09   11   13   15   17   19   21   23   01   03
observed         ·······▄▁▄▄▁▄▄▁▄·▁·····▄▁▁▁▁▄▁▁·▁·▁▁·▄▄▄·▁▁·················
dogwood          ····························▄·······························
(no project)     ·······▄▁▄▄▁▄▄▁▄·▁·····▄▁▁▁▁▁▁▁·▁·▁▁·▄▄▄·▁▁·················

   1  05:00..08:03     3h03m  before anything was seen
      08:03..08:09 biome-appusage  08:03 com.apple.Safari
                                   08:03 com.googlecode.iterm2
                                   08:03 com.apple.mail
                                   08:03 com.tapbots.Pastebot2Mac
   2  08:09..08:26       17m
      08:26        biome-appusage  08:26 com.apple.Safari
   ...
  10  11:53..14:16     2h24m
      14:16..14:20 biome-appusage  14:16 com.apple.MobileSMS
                                   14:16 com.apple.Safari
   ...

  silence 10 of 24  11:53..14:16  2h24m
  before it    biome-appusage  11:53 com.apple.Safari
  after it     biome-appusage  14:16 com.apple.MobileSMS
                               14:16 com.apple.Safari
  Something was seen either side of this one, so t asks for both ends of the
  break inside it.

  [w] worked through   [n] not working for any of it
  [t] I stopped and came back inside it, and can say when
  [u] cannot remember   [s] skip   [q] quit  >
```

### The cue, and the line it stops at

A reviewer asked what he was doing between 08:03 and 08:26 three weeks ago,
handed a row of block characters and nothing else, **does not answer** — the
first real session was abandoned for that reason. So the observations flanking
each silence go on the screen.

That is not the circularity `-blind` exists to remove, and the test is whether
the display **moves when the parameters move**. The model's derived answer does,
by definition; nothing here does. The silence comes from the census, which is
invariant under the gap, the lead-in and the tail-out, and the sightings are raw
observations selected only by their position relative to it.
`TestNearbyDoesNotMoveWhenTheParametersDo` holds it to that at 2m, 15m and 1h.

Seeing `com.apple.Safari 08:02` may lead you to reason *so I probably started
about 08:00*, and 08:00 is a boundary the lead-in also tries to place. That is
inference from an observation, not a restatement of the model: to restate the
model you would have to know the lead-in is five minutes, which is exactly what
the screen withholds. You are equally free to answer 07:30 or 08:02, and that
freedom is the quantity being measured.

**Each flank is bounded by the silence on its far side.** Without that bound the
08:26..08:36 silence on 2026-07-17 was cued with `com.apple.finder 10:08` —
four silences away, under a heading reading *after it*.

**Presence readings are held back, and the screen says so.** A silence is the
hole in *evidence of work*, so a presence reading never bounds one and can fall
*inside* one: the 2h24m silence at 11:53 on 2026-07-17 holds six
`knowledgec-backlit` rows, five reading `on`. Drawing them would not be circular
— it would be worse. *The screen was lit from 11:55 to 12:40* **answers** the
question, from a stream a script can read unaided, and the answer would then be
filed as a person's recollection. The reviewer's whole value is the part no
source recorded.

### A silence has a direction, and the question matches it

The day's **first** silence runs from the working day's boundary to the first
thing anything saw. Asking *you stopped at?* there names a stop that never
happened, at an instant you were asleep for — and the reading that comes
naturally from it asserts hours of phantom overnight work into ground truth.

So `t` asks **one** instant at either edge, in the direction the transition runs:

| silence | `t` asks | records |
|---|---|---|
| before anything was seen | `you started at?` | not work up to it, work after it |
| between two observations | `you stopped at?` then `and started again at?` | work, break, work |
| after the last thing seen | `you stopped at?` | work up to it, not work after it |

What an edge therefore *cannot* express is work, then a break, then work again,
inside the opening or closing silence. That loss is chosen: preferring the loss
of a rare true answer to the availability of a common false one. The screen says
so at the moment of choice rather than in this page.

### How long ago the day was

Every blind screen reports the day's distance from now, and **no threshold acts
on it**. The corpus needs labels most exactly where memory is weakest — the
pre-July regime holds 75.7% of all presence — so a warning that discourages
old days would starve the thing it is trying to protect.

The number can be measured instead of guessed. Every review row already carries
the day it covers and the instant it was recorded, so recall lag is stored for
every label in the corpus. Measured on the live store on 2026-08-03: of eighteen
reviewed days, sixteen were reviewed within 13.4 days; the two remaining are
**2026-02-17 at 164.3 days and 2026-03-21 at 132.4** — which are precisely the
two days the points-only regime rests on.

```sql
SELECT substr(started_at,1,10) AS day,
       round(julianday(recorded_at) - julianday(ended_at), 1) AS lag_days
  FROM adjustment_review ORDER BY lag_days DESC;
```

What the screen does say, every time, is that pressing `u` costs nothing: **a
guessed answer cannot be told from a remembered one** by anything downstream, so
it would move what is being fitted and carry false confidence into it.

### Why the silence census, and why it is not a model in disguise

A day is 81,984 observations on 2026-02-17. Nobody can read that, so something
must reduce it, and **every reduction is a choice**. The choice here is the
*silence census*: the holes between consecutive observations. Sort, subtract,
report the holes.

It is defensible for one specific reason — it carries **no clustering
parameter**. The census is invariant under exactly the three quantities being
fitted, and in the points-only regime every boundary the clustering can produce
lies inside one of its silences. So it shows the entire space of boundaries with
none of them chosen. Measured on 2026-03-21 across a 2m/15m/1h grid: 63 internal
boundaries, all 63 inside a silence.

Measured with `scripts/silencecensus` against the live store, evidence sources
only:

| day | observations | silences ≥ 2m | ≥ 15m | hours in silences < 2m |
|---|---|---|---|---|
| 2026-02-17 | 81,984 | 45 | 14 | 4.31 |
| 2026-03-21 | 12,832 | 50 | 12 | 6.26 |
| 2026-08-01 | 38,772 | 24 | 2 | 1.22 |

**81,984 observations reduce to 45 questions.** That is the whole argument for
the presentation.

The timeline is an *index*, not the answer. At sixty cells a working day is
twenty-four minutes to the cell, so a binary any-observation-lights-the-cell
rendering would dilate each instant by roughly the parameter under test. Two
things make it harmless: the reviewer answers about *named* silences with their
clock times printed beside them, and the grid is a **density ramp** (`▁ ▄ █`)
rather than a threshold, so one stray observation and four hundred do not render
alike. A ramp is monotone in a count and has no cut point in it.

### A silence nobody was asked about is asserted nothing

`-silence` sets the shortest silence that will be asked about (2m) and `-ask` how
many will be put, longest first (40). Everything below the floor, and everything
past the budget, gets **no row at all**.

That is the difference between a display floor and a gap threshold. Defaulting
the short silences to *work continued* would be asserting a bridging rule at the
floor — and the floor would then be a fitted parameter chosen by whoever set the
default. The cost is stated rather than hidden: 4.31h of sub-2m silence on
2026-02-17, 6.26h on 2026-03-21. It costs statistical power, not correctness.

The one thing assumed is that an instant *some* source observed was work unless
the reviewer says otherwise. That is the tool's premise and it is not one of the
three parameters, so holding it while the gap, the lead-in and the tail-out are
all off the screen smuggles nothing.

`t` — *and I can say when* — is the answer the confirming pass can never produce.
Agreeing with a block endorses a boundary the tail-out placed; saying "I stopped
at 14:20" places one where no observation is, which is the only thing that can
measure a margin from outside the model. A silence left out of the question
budget is still **listed**, marked `·`, so that a budget cannot make a day look
quieter than it was.

### What it records, and the known gap

Every review row now says what the reviewer was **shown**: `derivation` or
`observations` (migration 0011). A fitter that pools the two reports a precision
it has not got, and this is what lets it refuse to.

**Aim `-blind` at pre-July days.** The points-only regime holds 75.7% of all
presence across 175 days with two caveated reviewed days and zero job claims
behind it, and it is also the regime in which the census is complete.
`knowledgec-backlit` — the only stream read as presence — has no row before
2026-07-02, so before then there is no observed darkness to place a boundary the
census does not offer. From July onwards there is: on 2026-07-28, 22 of 39
internal boundaries are placed by darkness rather than by silence, and a stretch
the screen was dark through but Biome kept recording focus in is drawn as
observed, never put to the reviewer, and therefore taken as work.

A blind pass is never bounded by the clustering horizon, only by the harvest and
the clock. It draws observations, which owe the derivation nothing.

## Wholesale and drilled produce the same labels

A day walked block by block and a day waved through emit *identical* assertions
when the reviewer agreed with everything. That dissolves most of the
sampling-bias worry this design carried: the fine-grained calibration labels are
uniform over reviewed days rather than existing only for the rejected ones.

What still differs is how hard the reviewer looked, and that is recorded without
a schema change and without parsing free text. **A drilled day writes one review
row per block** beside the day's own, because the reviewer really did examine
each one; a day carrying only a day-spanning review was waved through. Task
PkY6I can weight on that, and every row it reads is literally true.

```sql
-- days that were walked block by block
SELECT count(*) FROM adjustment_review
 WHERE (julianday(ended_at) - julianday(started_at)) * 24 < 23.9;
```

`-drill-every N` forces a full walk on one day in N whatever the answer would
have been, so that the days examined closely are not only the days somebody
already suspected. The default is 5. The day is chosen by a hash of its own
name, so the sample is stable across passes and independent of the order the
queue happens to be in.

**On a sampled day the prompt says so, and names what Enter does.** Enter walks
the blocks there, and the prompt used to read `[Enter] right   [n] wrong, walk
the blocks` on those days too — two keys offered as opposites, running the same
code, under a heading that was false. A sampled day gets its own prompt:

```console
This day was sampled for a full walk, so that the days examined closely are not
only the days somebody already suspected.

[Enter] walk the blocks   [u] cannot remember   [a] add missed work   [s] skip
[q] quit  >
```

`n` still works and is simply no longer advertised as the other half of a choice
that does not exist. The notice above is printed once, as before; the prompt is
what is on the screen at the moment the key is pressed, and it is the prompt
that has to be true.

Measured cost, twenty real days from the live store:

| | keystrokes | days sampled |
|---|---|---|
| `-drill-every 0` | 20 | 0 |
| `-drill-every 5` (default) | 78 | 4 |

## Recall is what the queue spends

The queue is **newest first**, bounded to 20 days a pass, and offers only days
nobody has looked at.

597 days in the live store carry sittings and the corpus reaches back to 2013.
Most of it is not reviewable at any speed, because nobody remembers 2014. The
calibration set will therefore be recent-biased however the interface is built,
and that is a bigger caveat for PkY6I than the wholesale-versus-drilled question
— parameters fitted against 2026 evidence should not be assumed to describe a
2014 Blender corpus with a completely different source mix.

## A working day, not a date

Days begin at 05:00 local by default and are named by the civil date they begin
on, so an evening that ran past midnight belongs to the day before it. See
[Sittings and configuration](sessions.md). A sitting crossing the boundary is
clipped into both days and says so; its inferred share is reported whole and
left out of the day's margin rather than being apportioned across the boundary,
which is arithmetic nobody measured.

`09:30` typed at a prompt is resolved to the first instant at or after the day
begins whose wall clock reads that, so `02:00` on a day starting at 05:00 means
two in the morning at the *end* of that working day.

## Corrections are an overlay

Nothing here touches an event and nothing here touches `derived_session`, which
`timetattle sessionize` deletes and rebuilds wholesale on every run. The
difference between the machine's answer and yours **is** the model error, and
that difference is the point. See [Adjustments](adjustments.md) for the full
argument and for `timetattle adjust`, which records the same corrections one
span at a time.

The log is append-only and there is no edit and no delete. Reviewing a day again
appends a second, complete answer; the fold takes the later one and the earlier
one stays visible, because changing your mind is itself calibration data.

## Without a terminal

The primary reader of this CLI is an agent, so every path has a non-interactive
form.

```console
$ timetattle review -list                        # the queue, and the four states
$ timetattle review -date 2026-07-28 -print      # draw one day and stop
$ timetattle review -date 2026-07-28 -confirm    # record it as right
$ timetattle review -date 2026-07-28 -unremembered
$ timetattle review -date 2026-03-21 -blind -print   # the observations, and nothing else
```

`-blind` and `-confirm` are refused together: they are opposite claims about
what was in front of the reviewer, and the whole value of a blind pass is that
the claim is true.

`-list` and `-print` open the store read-only and write nothing.

`-list` weighs the days it **offers** and reports the weak rows on each; the
queue behind it deliberately reads no evidence, so a day it never offers is
listed as `(not weighed)` rather than as a day with nothing weak on it. Those
are different facts. The evidence for a whole pass is read in one query rather
than one per day: 200 days took 31.9s the second way and 5.7s the first, for
byte-identical output.

**Nothing is shortened when the output is not a terminal.** The day view fits
itself to the terminal it is printing into — the timeline fills the width, the
block list gives the room it saves to the source names — but a pipe has no
width, so a caller reading `-list` or `-print` gets every column at its full
length, exactly as it has always been. `-width N` pins the timeline explicitly
and wins over both.

See [Fitting the terminal](terminal.md) for the rule the other commands follow
too, and for what may be shortened and in what order.

## What it deliberately does not do

- **No raw terminal mode.** Enter is the confirmation, which makes twenty days
  twenty keystrokes without a platform-specific dependency. Raw mode would save
  one keystroke a day against the several seconds that *reading* a day costs.
- **No transaction around a day's rows.** `internal/store` gives out no
  transaction for the adjustment tables, on purpose — see migration 0008. A run
  interrupted mid-day leaves a review with some of its assertions, which
  re-reviewing the day supersedes.
- **No per-project hours.** The rows show shape, not billing. `timetattle
  metrics` is where per-project time is computed, and it says how much of each
  figure was observed and how much of it is weak.
- **No dropping of weak rows, ever.** `!` is a mark and there is no flag that
  turns it into a filter. A rule that removed them would have removed 88 real
  project-days to be rid of some machine noise, and the noise is already gone —
  see [Excluding machine activity](/docs/sessions/#excluding-machine-activity),
  which is where a *rule* belongs.
