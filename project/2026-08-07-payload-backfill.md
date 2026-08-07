# The store has never changed a row it had already written, and today it does — once, deliberately, and only where there is nothing

2026-08-07. Measured on an Apple M1, darwin/arm64, go1.26.4, against this
machine's own Claude Code corpus. Every figure below names the command that
reproduces it. The live store was **not** touched by any of this work: the
figures come from temporary stores built and thrown away by the test run, and
the live dry run and live pass are the integrator's step, recorded at the end of
this document.

`project/2026-08-07-transcript-payloads.md` landed `event.payload` and said, in
so many words, that history does not backfill and that filling it would be a
separate deliberate decision. Ben made it the same evening: *"We can backfill the
existing sessions. I think it would be interesting."* This is what that decision
turned into.

## What was actually at stake

Payloads begin at migration 0014. Everything harvested before it carries none,
and nothing in a later harvest fills them: a re-read of a stored record
conflicts on identity and does nothing. So `Store.Usage` would have reported
*nothing was recorded* for all of history and real figures from one arbitrary
date forward, with the boundary visible in every chart that ever showed a
session count and explainable to nobody looking at one.

The alternative to filling them is not "slightly less data". It is a permanent
discontinuity in the middle of the only series these figures will ever have.

## This is the first write in the project that changes a stored observation

That claim needs one correction before it is repeated: `event.project` has been
updated in place since 0001, by the attribution pass. The difference is what
kind of thing each column holds. `project` is a **recomputable cache** — 0001
says so in the column's own comment, and re-registering a directory is expected
to change it. `payload` holds **what a source observed**, and the store's whole
design is that an observation is never edited to reflect a better later reading.

So this is the first mutation of an OBSERVED field, and the bar is accordingly
high. Four properties, each pinned by a test in
`internal/store/backfill_test.go` rather than promised in a comment:

1. **It fills only where there is nothing.** Every write carries
   `payload IS NULL`.
2. **It is idempotent.** A second pass writes nothing at all — same rows, same
   bytes.
3. **It inserts nothing.** A record the store has never seen is counted and
   skipped.
4. **Absence stays absence.** A row nothing re-read keeps its NULL and is
   counted as unmatched, never zeroed.

## Where the code lives, and why it is split

**The pass is in `internal/store`** (`BackfillPayloads`), because the way a row
is matched is the same fact as the way the unique index is spelled, and the two
must not live in different packages. `internal/store/backfill_test.go` covers it
red-first: 16 tests, every one of them observed failing on its own assertion
before the implementation existed.

**The vehicle is `scripts/payloadbackfill`**, not a `timetattle` subcommand, and
the argument has two halves.

*The repo's own convention.* `cmd/timetattle` carries the verbs of the working
week — harvest, metrics, review, status, register — things run again and again.
This is a one-shot repair of a boundary in the store's history, with nothing to
do once it has run. `scripts/` already holds tools of exactly that shape:
`restorebiome` rebuilds a stream, `transcriptreconcile` subtracts this same
corpus from this same store.

*What a subcommand would advertise.* Putting the only operation that rewrites an
observation into the daily command set says it is ordinary, and invites the next
caller who wants "just a small update". The judgement this needs is a judgement,
not a habit.

What is NOT in `scripts/` is the logic. `scripts/restorebiome` claims exemption
from the project's red/green rule as a recovery tool; nothing here claims it,
because the part that could be wrong is tested in a package that is.

## Matching: the insert path's own identity, and what it cannot be confused by

A row is found by the seven columns of `event_identity`, spelled with the
COALESCE forms migration 0001 indexed them with:

```sql
source_id = ? AND shape = ?
AND COALESCE(started_at, '') = ? AND COALESCE(ended_at, '') = ?
AND COALESCE(app, '') = ? AND COALESCE(document, '') = ? AND COALESCE(state, '') = ?
```

The spelling is the access path, and the naive form was measured before the real
one replaced it. From the red stage of
`TestTheBackfillFindsItsRowThroughTheIdentityIndexRatherThanAScan`, with
`started_at = ? AND app = ?` written the obvious way:

```
SEARCH event USING INDEX event_source (source_id=? AND started_at=?)
```

and with the indexed spelling:

```
SEARCH event USING INDEX event_identity
  (source_id=? AND shape=? AND <expr>=? AND <expr>=? AND <expr>=? AND <expr>=? AND <expr>=?)
```

Both return the same rows. One of them probes a unique index; the other scans
within a source of 822,387 rows, once per record offered. The COALESCE is also
what makes the match *correct*: the insert path stores the empty string as NULL,
and `app = ''` matches no row where `app` is NULL.

**Ambiguity in either direction was the thing to check, and neither exists.**
Two stored rows for one record cannot happen: `event_identity` is UNIQUE, so a
match is at most one row. Two records for one row is possible in principle, and
is reported rather than resolved away — `RepeatOffers`, with
`DisagreeingRepeatOffers` beside it, and the first offer wins, which is what the
insert path does within a batch. Over the whole real corpus both are **0**
(below), as they must be: `internal/transcripts` resolves replays on
(Document, Start), and both of those are inside `event_identity`.

## First-reading preference, and the one place it is not deterministic

The backfill applies no resolution rule of its own. It is handed the collector's
own output, and `dedupeReplays` has already resolved two readings of one record
before the store sees either. That is exactly what makes a backfilled store
equal a store harvested with payloads from the start, and it is asserted as
such: `TestAStoreBackfilledAfterwardsHoldsWhatAStoreHarvestedWithPayloadsHolds`
writes a corpus to disk, harvests it stripped of payloads, backfills it from an
independent second read, and compares the row digest and the roll-up against a
store harvested normally. They are identical.

> **A caveat that belongs to the collector and not to this pass.**
> `dedupeReplays` keeps one candidate per (Document, Start) and *prefers*
> `StateHuman`. Where two readings are both machine-state and disagree about
> their token figures, the survivor is whichever file's records arrived first —
> and `scanAll` reads files concurrently, so arrival order is not fixed between
> runs. On this corpus that is at most the 52 repeat readings whose token
> figures differ (`go run ./scripts/sessioncensus`), 0.006% of the corpus.
>
> This is pre-existing and is not introduced here: two harvests of the same
> corpus could already store either reading, and whichever one arrives first is
> kept for ever. The backfill inherits it exactly as a fresh harvest does —
> which is the property that matters, since the claim is that history and the
> present were recorded the same way. It is written down because the fixture
> above deliberately contains no such case: a test asserting on a nondeterministic
> survivor would be a flaky test, and a test quietly asserting on whichever one
> it saw first would be worse.

## The guards, proved by removing them

`internal/store/backfill.go` carries two, and both were removed in turn against
`internal/store/backfill_test.go`:

| what was removed | what happened |
| --- | --- |
| both (the `payload IS NULL` clause and the already-carrying branch) | the second pass filled all 3 rows again, and a disagreeing re-reading changed a stored record's output figure from **91 to 92** — silently, since no row count moves |
| the branch only, leaving the SQL clause | the UPDATE matches nothing, `RowsAffected` is 0, and the pass aborts naming the observation and rolls its batch back |

One guard would have been correct. Two are there because the failure the first
prevents is invisible and the failure the second prevents is a stopped program.

## The dry run is the real run with the write withheld

Not a second code path that estimates: the same traversal, the same matching and
the same classification produce its counts, and only the `UPDATE` is withheld.
`TestADryRunWritesNothingAndReportsWhatTheRealRunThenDoes` asserts every count
of the dry run against what the real one then did, and hashes the database file
either side.

A dry run also opens the store **read-only**, so "it wrote nothing" is a
property of the connection rather than of a flag somebody read correctly. It is
the default (`-dry-run` defaults to true), so running the command by accident
does nothing.

## What the counts mean, since three of them look alike

The report carries two ledgers, and every input lands in exactly one bucket:

```
Offered              = Matched + Unstored + RepeatOffers
Matched              = Fillable + NothingToState + AlreadyCarrying
RowsWithoutAPayload  = Fillable + NothingToState + Unmatched
```

- **Unstored** — read from disk, matches no row. Ordinary on a live machine:
  written since the last harvest. Not stored here; a backfill fills rows and a
  harvest makes them.
- **NothingToState** — the row was re-read and its record said nothing about a
  session or tokens. Stays NULL.
- **Unmatched** — no record on disk spoke for the row at all. Stays NULL.

**There is no "missing files" count, and that is a refusal rather than an
omission.** The task asked for one. An event records the working directory its
record named, never the file it was read from — `internal/transcripts` derives
nothing from a path, deliberately — so no row can be traced back to a file that
is or is not still on disk. "The transcript was deleted", "it was rewritten" and
"the root read here is not the one it was harvested from" are one figure,
`unmatched`, and reporting them as two would be a number nobody measured. This
project's rule against exactly that is why it is named here instead.

## The pass at the size it will actually be run at

```
go test -tags realdata -run TestTheWholeTranscriptCorpus -timeout 30m ./internal/store/
```

A temporary store, built from this machine's whole corpus as a pre-0014 harvest
would have left it, then filled. The live store is never opened.

```
collected 824529 records in 7.385s
harvested 824529 payload-less rows in 36.788s
dry run in 18.336s:  Offered:824529 Matched:824529 Unstored:0 RepeatOffers:0
                     DisagreeingRepeatOffers:0 Fillable:824529 Filled:0
                     NothingToState:0 AlreadyCarrying:0
                     RowsWithoutAPayload:824529 Unmatched:0
filled 824529 rows in 49.572s
```

The whole test, including a second pass that filled 0 and found 824,529 rows
already carrying a payload, ran in **135.25s**. Four things follow.

1. **The pass is a minute's work at this scale**, not an hour's. The integrator
   should expect the live fill to take roughly as long, plus the 7s the corpus
   read costs.
2. **`Unstored` and `Unmatched` are both 0 here**, because the same read
   produced the rows and the records offered back. On the live store neither
   will be: records written since the last harvest land in `Unstored`, and
   transcripts deleted since a harvest land in `Unmatched`.
3. **`RepeatOffers` is 0 over 824,529 real records**, which is the ambiguity
   check done against the corpus rather than argued from the collector's source.
4. **The roll-up equals the corpus.** The test computes the session count and
   all four token totals in Go from what the collector read, and compares them
   to `Store.Usage` over the filled store. They match exactly — so the pass put
   the right payload on the right row, not merely a payload on every row.

The corpus was 823,769 records on 2026-08-07 in
`project/2026-08-07-transcript-payloads.md` and is 824,529 here, hours later.
The difference is the sessions that ran in between, including the one doing this
work.

## The command, exercised end to end

Against a temporary store seeded with a five-record fixture corpus, one of whose
transcripts was then deleted from disk — so the store holds a row whose evidence
is gone, which is the case the live run will meet and the corpus test above
cannot pose:

```
payloadbackfill -root <fixture corpus> -store <temporary store>
```

```
mode:  DRY RUN, through a read-only connection; nothing can be written
  records read                                  4
    matched a stored row                        4
      fillable                                  4
    matched no row                              0
  rows                                          5
    fillable                                    4
    unmatched (stay NULL)                       1
  rows filled by this pass                      0
```

`shasum -a 256` on the store file was
`35e2f563ee412a4486ad00770e0e3b25ecd1bacee0fb64bf7a715534577dc558` before the
dry run and unchanged after it. With `-dry-run=false` it filled 4, left the
unmatched row NULL, and the roll-up went from 0 observations to 4. Run a third
time it filled 0 and reported 4 already carrying a payload and 1 still
unmatched.

Seeding a payload-less store needs a corpus collected through
`internal/transcripts` with `Payload` blanked before it is written, which no
committed tool does — the state only exists once, in the store this pass is
about. The reproducible form of the same proof is the test suite: the fixture
run above is a rehearsal of the operator surface, and
`TestADryRunWritesNothingAndReportsWhatTheRealRunThenDoes` is the assertion.

## metrics byte-identity does not apply here, and that is worth stating

Every change touching this store's rows in the last week has been checked by
capturing `timetattle metrics` either side and comparing hashes. That check is
skipped here **on purpose**, and skipping it silently is exactly what this
document exists to prevent.

`metrics` reports presence and utilisation. Payloads play no part in either: no
metrics figure reads `event.payload`, the roll-up that does is
`Store.Usage`, and nothing in `cmd/timetattle/metrics.go` calls it. More to the
point, the pass writes only rows where `payload IS NULL` and touches no other
column, so no timestamp, locator, state or project moves. There is nothing for a
byte comparison to notice, and a hash presented as evidence that the change is
safe would be evidence of nothing.

The check that DOES apply is the roll-up comparison in the corpus test above,
and it is the stronger one: it says the figures the store now reports are the
figures the collector actually read.

## The live run — TO BE FILLED BY THE INTEGRATOR

The pass has not been run against `~/.timetattle/timetattle.db`, dry or
otherwise. Both runs below are the integrator's, and both counts belong here
because a dry run whose prediction nobody checked against the real pass is a
prediction nobody checked.

Expected before either: 822,387 transcript rows carrying no payload
(`project/2026-08-07-transcript-payloads.md`, measured 2026-08-07 against the
live store), plus whatever has been harvested since.

```
go run ./scripts/payloadbackfill
```

```
(dry-run output here: records read, matched, fillable, nothing to state,
 already carrying, matched no row, repeat offers, rows carrying no payload,
 unmatched, and the roll-up cross-check)
```

```
go run ./scripts/payloadbackfill -dry-run=false
```

```
(real-run output here — Filled must equal the dry run's Fillable)
```

```
go run ./scripts/payloadbackfill
```

```
(third run: Filled 0, AlreadyCarrying equal to what the second run filled.
 This is the idempotence check, and it costs one corpus read)
```

Three things to record beside the output:

- **whether the dry run's `Fillable` equalled the real run's `Filled`.** If they
  differ, a harvest ran in between, and the difference is how many records it
  added.
- **the `Unmatched` figure**, which is the first measurement anyone has of how
  much of this machine's transcript history is no longer on disk. It stays NULL
  and that is correct; it is also a fact about the corpus worth knowing.
- **`timetattle metrics` before and after anyway**, if it is cheap. The argument
  above says it cannot move. An argument and a hash are not the same thing, and
  the hash costs two commands.
