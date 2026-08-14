# Observed darkness may only suppress a timer

**Date:** 2026-08-14
**Agent:** `annal`, task `OEAqy`
**Store:** `~/.timetattle/timetattle.db`, opened read-only for every figure below
**Change:** `93cd2cc`, merged at `3cad959`, with the run table at `7ff0714`/`5439df8`

Until today, observed darkness — time covered by an `isBacklit=0` reading and by
no `isBacklit=1` reading — dropped **every** evidence observation anchored inside
it and subtracted the dark span from any sitting that crossed it. It now
suppresses only observations whose `Cause` is `source.CauseTimer`, and subtracts
nothing from anything.

The decision was taken on policy, not on measurement: agent runtime with nobody
at the keyboard is billable software development time, and a dark backlight
licenses exactly one inference — the lid was shut. That argument is stated where
it belongs, in `internal/sessions`' package header and in
`docs/content/docs/sessions.md`.

**This document is about the arguments, not the decision.** Five claims were
made in support of the change and then refuted, four of them by an independent
read-only audit and one by re-verification afterwards. The change survived all
five. The arguments did not, and the reason to write them down is that a trail
which records only the reasoning that held is a trail nobody can calibrate
against.

## What the rule cost, measured against one corpus with two binaries

The two rules are two binaries, so the honest measurement is a paired capture
rather than a flag. The pre-change binary was built from the commit before the
narrowing and run against the same live store, minutes apart:

```
git worktree add --detach $TMPDIR/oldrule 93cd2cc^      # resolves to 82f0cb2
cd $TMPDIR/oldrule && go build -o $TMPDIR/tt-old ./cmd/timetattle
$TMPDIR/tt-old metrics --all      # the retired rule
timetattle       metrics --all    # the shipped rule
```

Both runs report `1088305 observations read: 1082008 evidence, 2161 presence,
4136 ignored`, identically — so the pair is against one corpus and the
differences below are the rule and nothing else.

| | retired rule | shipped rule |
|---|---|---|
| observations suppressed | 24,723 | **1** |
| time trimmed | 49.1h across 130 splits | **0.0h across 0 splits** |
| observed darkness | 640.3h | 640.3h |
| contested (read lit and dark at once) | 13.3h | 13.3h |
| presence | 1174.6h across 1562 sittings | 1243.8h across **1181** sittings |
| of that, observed | 392.9h (66.5% inferred) | 407.0h (67.3% inferred) |
| absolute engaged, all projects | 1179.9h | 1216.0h |

The single surviving suppression is `motion-autosave` row 766707 at
`2026-07-11T22:53:00Z` — 17:53 local, the Motion autosave the retired rule was
written for. The narrowed rule still hits its design case.

**The sitting-length histogram is where the shape of the old rule is visible.**
Under it, 218 sittings were shorter than five minutes and held 3.6h between
them; that bucket does not print at all now, because it was made of pieces the
rule had cut a longer sitting into. Sittings fell by 381 while presence rose by
69.2h: fewer, longer sittings covering more clock.

The delta by client is +20.3h, +11.7h and +4.0h on three projects and **exactly
0.0h across the 33 projects in the personal bucket** — the whole effect lands on
work that runs unattended, and none of it on work that does not. The per-client
figures are recorded outside this tree, because clients are not published here.

### It is seven weeks, not two

```
timetattle metrics --all --by week --from 2026-06-29 --csv
```

summing the `client` rows' `seconds` per period, old binary against new:

| week of | retired | shipped | delta |
|---|---|---|---|
| 2026-06-29 | 20.24h | 20.73h | +0.48h |
| 2026-07-06 | 15.49h | 15.78h | +0.29h |
| 2026-07-13 | 28.70h | 30.80h | +2.09h |
| 2026-07-20 | 63.58h | 68.60h | +5.02h |
| 2026-07-27 | 70.43h | 83.76h | **+13.33h** |
| 2026-08-03 | 52.95h | 61.04h | +8.10h |
| 2026-08-10 | 38.73h | 45.50h | +6.77h (week still accruing) |
| **total** | | | **+36.07h** |

The backlight stream opens at `2026-07-02T02:47:40Z`, so this is the entire
effect there has ever been.

## The five errors

### 1. The census of what the rule reached was computed with the two-state bug

**Claimed:** 1 timer-caused, 1,924 user-action and 23,763 unclassified
observations fell inside observed darkness — 25,688 in all.

**Wrong because** that was measured over raw `isBacklit=0` rows. Darkness is
established only where nothing read the screen as lit, and the derivation
computes `Union(Subtract(darkU, litU))`, not `Union(dark)`. knowledgeC writes
contradictory rows with identical endpoints; a census over raw `off` rows counts
observations inside spans the clustering has never treated as dark.

The irony is on the record deliberately: this is the two-state bug
`internal/sessions`' own package header says the project has shipped three
times, and it was committed while arguing about that header.

**And the correction was itself hand-rolled and itself short.** The replacement
figure offered at the time was 24,563, against a tool trailer reading 24,704 —
141 apart, with nobody subtracting them, which is the reconciliation rule this
repository states in its own contributor guidance. It closed only when the
census was made to share the derivation's code:

```
go run ./scripts/darkcause
```

```
evidence observations 1082008
observed darkness     640.28h across 1062 spans
contested             13.35h (established as neither, and trimmed by nothing)
anchored in darkness  24723 retained, 24732 before excludes (difference 9)
anchored in raw 'off' 25928 — 1205 more, all of them in contested time
  (none)          22849 in darkness      927553 total
  timer               1 in darkness          38 total
  user-action      1873 in darkness      154417 total
```

24,723 is the same number the retired binary's own trailer prints for
observations suppressed, to the record. The 1,205-observation gap between the
two censuses is the 13.3h of contested time the model deliberately refuses to
act on. **Two tools measuring one quantity agree only when one of them is the
tool.**

### 2. "The rule protected against almost nothing" was bad reasoning

**Claimed:** the rule caught one artifact and discarded ~25,700 records of work,
so it was protecting against almost nothing.

**Wrong because** the denominator was never named. `motion-autosave` is the
entire timer-caused population of the store:

```
sqlite3 ~/.timetattle/timetattle.db "select count(*) from event where cause='timer';"
    -> 38
sqlite3 ~/.timetattle/timetattle.db \
  "select count(*) from event where cause='timer' and started_at >= '2026-07-02';"
    -> 23
```

Catching 1 of the 23 that could have been caught is near the arithmetic ceiling,
not far below it. The rule was not failing at its job; it was doing its job and
also doing a great deal else. **That distinction is the actual case for
narrowing it, and it is a better case than the one that was made.**

### 3. "~35h in two weeks" was wrong by about 2.5x

**Claimed:** ~35h added over two weeks.

**Wrong because** the window was clipped to the fortnight under discussion while
the total was taken from the whole corpus. It is 36.07h over seven weeks (table
above). The worst week, +13.33h, falls **outside** the window that was being
discussed — a clipped window hiding the worst case, which is a hazard this
repository's guidance already names and which had already cost it once in
`scripts/sleepcheck`.

Nothing here supports annualising a weekly figure. The series moves by a factor
of 45 across seven weeks.

### 4. The "below the floor" anomaly did not exist

**Claimed:** on 2026-08-10 the model attributed 5.9h to a project whose evidence
covered 442 distinct minutes — 7.37h — so it was attributing below its own
floor.

**Wrong because** minutes are not a floor. Bucketing instants to whole minutes
manufactures duration out of observations that have none, which is the
interpolate-WITHIN-a-source error `internal/sessions` documents by name:

Substituting the project name for `NAME`, and 10:00Z for the 05:00 local
working-day boundary:

```
sqlite3 ~/.timetattle/timetattle.db "
with d as (select * from event where project='NAME'
   and coalesce(started_at, ended_at) >= '2026-08-10T10:00:00'
   and coalesce(started_at, ended_at) <  '2026-08-11T10:00:00')
select 'total', count(*) from d
union all select 'zero-width', count(*) from d where started_at = ended_at
union all select 'distinct minutes',
  count(distinct substr(coalesce(started_at,ended_at),1,16)) from d
union all select 'with duration', count(*) from d where started_at <> ended_at;"

    total|7137
    zero-width|7119
    distinct minutes|442
    with duration|18
```

Eighteen of 7,137 observations carry any duration at all. Selecting those 18
rows' endpoints from the same window and merging them (they sum to 7.135h and
overlap into 13 disjoint pieces) gives a footprint of **5.321h** — and the model
attributed **5.9h**, *more* than the observed footprint, not less, which is what
lead-in, tail-out and bridging are for. There was no anomaly, and the day the
argument leaned on is the day that refutes it.

The defensible statement about that day is a different one, and it is stronger:
under the retired rule the day read 5.9h across 18 sittings, and under the
shipped rule 9.6h across 4 (`timetattle metrics --project NAME --from
2026-08-10 --to 2026-08-10`, both binaries). 3.7h of one day.

### 5. The corroborating source was not independent, and the figure proving it was also wrong

**Claimed:** during the dark spans holding agent records, the application-usage
stream recorded zero usage — so the machine really was idle and the darkness
readings were not a knowledgeC artifact.

**Wrong because** the two streams are not independent. Biome stops writing when
the display stops lighting; its silence during darkness is the same fact as the
darkness, counted twice. What a dark reading licenses is "the lid was shut",
which is the argument the change actually rests on — so establishing the
circularity **strengthens** the decision rather than weakening it. An idle
machine would have been an argument *for* suppression under a presence-based
model, and it is the policy that overrules that, not the evidence.

**The number offered against it was wrong too, by 19x, and this is the fifth
error rather than a footnote on the fourth.** It was quoted as *7 of 36,433
Biome rows in the backlight era are anchored inside darkness (0.02%)*. That was
computed over `started_at`:

```
sqlite3 ~/.timetattle/timetattle.db \
  "select shape, count(*) from event where source_id='biome-appusage' group by shape;"
    interval|36643
    unbounded|689
```

689 Biome rows are **unbounded** — one endpoint was never observed — so
`started_at` is NULL, and every SQL comparison against NULL yields NULL and
matches nothing. The query dropped them in silence. 660 of the 689 carry
`unknown = 'sleep'`: they are bare stops written after the machine slept, which
is to say they are precisely the rows most likely to be anchored in darkness.
The census excluded its own best evidence, and looked clean doing it.

`source.Event.Anchor()` returns the endpoint that *was* observed, which in SQL is
`coalesce(started_at, ended_at)`. Run the same census both ways — established
darkness being an `off` reading with no `on` reading covering the same instant:

```
sqlite3 ~/.timetattle/timetattle.db "
with dark as (select started_at s, ended_at e from event
              where source_id='knowledgec-backlit' and state='off'),
     lit  as (select started_at s, ended_at e from event
              where source_id='knowledgec-backlit' and state='on'),
     b    as (select coalesce(started_at, ended_at) a
              from event where source_id='biome-appusage')
select count(*) from b
where     exists (select 1 from dark where b.a >= dark.s and b.a < dark.e)
  and not exists (select 1 from lit  where b.a >= lit.s  and b.a < lit.e );"
```

| anchor expression used | rows in established darkness |
|---|---|
| `started_at` alone | 7 |
| `coalesce(started_at, ended_at)` | **133** |

`go run ./scripts/darkcause` independently reports 132 for `biome-appusage`; the
one-row difference is the exclude pass, and is one of the 9 that tool's own
`24723 retained, 24732 before excludes` line accounts for. The two reconcile.

Against 37,332 Biome rows in the backlight era — counted on the same anchor,
`select count(*) from event where source_id='biome-appusage' and
coalesce(started_at, ended_at) >= (select min(started_at) from event where
source_id='knowledgec-backlit')` — that is 0.35%, not 0.02%. **The conclusion
survives, since 0.35% is still not an independent witness. The figure did not.**

This is the house rule in a new costume, and the costume is what makes it worth
recording. "Absence of a row is not an observation" has always been about the
store; here it was about the *query*. A NULL endpoint means "never observed",
`WHERE a >= b` reads it as "no such row", and SQL will not say which it did.

## One consequence nobody remarked on at the time

`cmd/timetattle/metrics.go:855` prints the concurrency line only when absolute
engaged time exceeds presence. Under the retired rule it printed — 1179.9h
against 1174.6h, `1.00x: concurrent work, billed to each project`. Under the
shipped rule absolute is 1216.0h against 1243.8h of presence, so the line does
not print at all.

The corpus-level concurrency ratio was therefore an artifact of the rule
trimming presence harder than it trimmed project time. Nothing is wrong with the
report — but a conditional line that stops printing is the same failure mode as
a bare zero, one notch quieter, and it should be read as a measurement rather
than as silence.

## Two findings from the integration itself

**A constant recording which rule a derivation used went stale within hours of
being written, in the merge that shipped it.** `sittings.CurrentDarknessPolicy`
is stamped onto every derivation so that two derivations either side of the
change do not read alike. Its own comment warned that nothing mechanical could
notice it going stale — and it was authored beside the run table while a second
worker was narrowing the rule it pointed at, so the two reached integration
disagreeing. Nothing caught it but a human reading both branches.

`TestTheRecordedPolicyMatchesWhatClusterDoes` is now the noticing. It does not
compare the constant to a literal, which would assert only that the constant
equals itself; it clusters the two observations the policies disagree about and
asserts the observed behaviour is what the constant claims. Proved by mutation:
restoring the stale value fails it with a message naming the cause, and nothing
else in the package fails, which is what makes the failure diagnostic.

**Taking either branch whole would have silently dropped the documentation
distinguishing structurally-zero from measured-zero.** `Stats` moved to
`internal/sittings` in one branch while the other narrowed the rule, and the
moved type carried the pre-narrowing comments. `SplitByAbsence`,
`TrimmedByAbsence`, `UnevidencedPieces` and `UnevidencedDropped` are now zero
*because no code path can raise them*, not because a measurement went looking
and found nothing — and that distinction is the entire subject of the package
the type moved out of. `TestDarknessTrimsNoTimeFromTheLiveCorpus` now asserts
all four against real evidence, so "structurally" has to stay true.

## What is now one SELECT

`derived_session_run` records what a derivation did, so the question this
document opens with no longer needs two `metrics` runs under different flags —
and those two runs measured a *different* clustering rather than reading the one
that produced the stored table.

```
sqlite3 ~/.timetattle/timetattle.db "select darkness_policy, suppressed_by_absence,
  trimmed_by_absence_micros, split_by_absence, contested_micros, observed_absence_micros
  from derived_session_run;"
    -> suppress-timer-only|1|0|0|48056000000|2303040000000
```

13.3h contested, 639.7h of observed darkness, at the derivation stored on
2026-08-14T17:17:22Z. `metrics --all` reads 640.3h against the same store today
because the corpus has grown since that derivation was written; the run row is
the derivation's own record and does not move.

## Corrections made to earlier documents in this trail

Three documents in `project/` asserted something this change made false, and
each now carries a marked block rather than an edited number:

- `2026-08-07-clustering-quadratic-fix.md` — `Covers` is no longer called once
  per evidence observation.
- `2026-08-07-covers-binary-search.md` — the same, at the call site its whole
  argument rests on.
- `2026-08-13-activity-mix.md` — its unwindowed table was captured under the
  retired rule.

`2026-08-07-alloc-churn.md` was checked and left alone: its darkness references
are all to `Subtract`, which still runs once per cluster, and every figure and
mutation proof in it stands.
