# A bucket carries the one-project containment, and a bucket edge cannot break it

2026-08-13. Agent `bucketeer`, task 65ufO. This is the tail of
`project/2026-08-13-rollup-union-or-reclustering.md` and
`project/2026-08-13-rollup-reclustering-resolved.md`, one layer down. Read the
second of those first: the decision it records is assumed here and not restated.

## The question

`internal/metrics/series_test.go` still asserted, per `Series` bucket, the
invariant the resolved document replaced — that a client is the union of its
projects and never exceeds their sum. The open question was whether the
**replacement** — a client owning exactly one project can never exceed that
project, as an inequality — holds **bucket by bucket** as well as across a span.
It was genuinely open: a bucket edge cuts a segment, and it was not obvious
whether a client's evidence and its project's could divide differently across
the same edge.

## The answers, in order

**1. The old assertion was false at bucket level, not merely unexercised.** It
passed only because its fixture worked one project after the other. Given a
fixture that alternates, at the shipped defaults, it fails immediately:

```
bucket 0 credits the client 46m0s across projects summing to 40m0s
```

That 46m against 40m, surplus 6m, is the same corpus and the same three figures
`internal/metrics/rollupcost_test.go` already pins one level up.

**2. A bucket is falser than the report it was sliced from.** This is the sharper
half and it is new. What the removed assertion compares against is the projects'
**sum**, and the sum is inflated by every instant two of the client's projects
run at once. Cutting that concurrency into a different bucket takes the
inflation away. On the corpus `rollupunion_test.go` carries — alternation plus a
concurrent pair — the report is comfortably inside its projects' sum at
**56m against 60m**, while the first bucket of the very same report is outside it
at **46m against 40m**. A check that reads clean over a span therefore says
nothing whatever about the buckets of it, which is the general form of the
"clipped window hides the worst case" rule this repository already carries.

**3. One-project containment DOES hold bucket by bucket, and an edge cannot
break it.** The reason is structural rather than statistical, and it dissolves
the worry the question was written around. A series is a re-slice and never a
re-computation: a bucket is summed from `Report.Segments`, and a segment names
both its projects and its clients, so the clip an edge makes — the overlap of one
segment with one bucket — is a **single duration booked to both levels in one
call**. There is no second cursor to fall out of step with the first. A boundary
therefore splits a client and its project identically, by construction.

That reduces the bucket claim to a claim about **segments**: no segment carries a
one-project client without its project. Which in turn follows from the same
construction the report-level check rests on — a client's events are exactly its
projects' events that resolved to it, so a one-project client's evidence is a
subset of that project's, the same clustering under the same parameters puts its
spans inside the project's, and the correction layer preserves it (`correct`
computes one `every` set for both levels and gives a one-project client a subset
of what it gives its project, so containment survives the subtraction).

**The bucket claim is strictly stronger than the report claim.** Report-level
containment compares two sums and can hold while a segment violates it, if
another segment compensates in the other direction. Bucket-level containment
cannot. That is exactly why it catches something the report check does not.

## Which population each figure describes

### Unwindowed, the whole store — and it settles every possible edge set

`go run ./scripts/rollupunion`, 2026-08-13, shipped defaults, 1,073,726 events
read and 1,071,969 after exclusions. Reproduced here unchanged from the resolved
document:

| clients | projects each | alone | runs |
|---|---|---|---|
| five of six | 1 | **0s** | **0** |
| the sixth | 33 | 19m13s | 11, largest 4m19s |

`alone` is clock a client holds that no project of its own holds — segment by
segment, over the whole store. **Zero there is the segment claim above, measured.**
And because a bucket is a clip of a segment applied to both levels at once, zero
there is one-project containment for **every conceivable set of edges**, not only
for the ones somebody cut. The 33-project client is out of the relation's scope
entirely and is correct in holding its 19m13s.

### The whole store, cut at the working day

`go run ./cmd/timetattle metrics -all -by day` — 4,712 working-day buckets. Exits
**0** with the check in place, `1165.3h across 4712 working days, against an
ungrouped 1165.3h; difference 0s`.

### One real day, cut at every hour

`go test -count=1 -tags realdata ./internal/metrics/` —
`TestNoRealBucketCreditsAOneProjectClientMoreThanItsProject` over 2026-07-28:
**25 one-project client shares across 17 buckets, 0 of them strictly less than
their project.** All equal, which on a day with old registrations and no
contested directory is the ordinary reading; it is logged rather than asserted so
that a later reader does not assume the inequality was exercised strictly when it
was not.

## What was asserted, and the mutation that justifies it

The bucket-level relation is now asserted **in `checkSeries`** as well as in the
tests, which is a change to shipped behaviour and was not a foregone conclusion.
The reason is that a desync between the two levels is invisible to every other
check in that function. The buckets are reconciled against the report **per
name**, so moving a client's share into a neighbouring bucket while its project's
stays put conserves that client's total exactly.

The mutation: `addSegment` books each segment's client share one bucket later
than its project share — a second cursor one bucket out of step, which is
literally one of the three failures `checkSeries`' own comment says it exists to
catch. Applied, measured, reverted.

| | check absent | check present |
|---|---|---|
| cold suite, `go test ./...` | **2 failures**, both the constructed cases in `series_test.go` | 13 failures |
| `go run ./cmd/timetattle metrics -all -by day -csv`, whole store | **exit 0**, 29,051 rows, of which **297 client rows sit on the wrong day**, silently | **exit 1**, naming a bucket that credits a one-project client 10m0s against the 0s its only project holds there |

297 misdated client rows on the shipped surface, against a named refusal, is the
whole of the argument. Note what the check does NOT rest on: no threshold, no
tolerance, and nothing about multi-project clients, whose relation to their
projects is a belief rather than a construction and is therefore left alone —
exactly as the resolved document left it alone one level up.

`scripts/confidential` was run over the working tree before this document was
written: 16 findings, all of them the carried invented-money strings in the
dashboard files, none from this work.

## What this deliberately did not do

- It did not add any check for **multi-project** clients per bucket. There is
  none to add for the reason the resolved document gives, and finding 2 above
  makes that stronger rather than weaker: a bucket can exceed its projects' sum
  where the report does not, so the quantity is even less bounded per bucket
  than per span.
- It did not touch `reduce`, `Compute`, or the report-level check. **No reported
  figure changed**, verified rather than assumed —
  `go run ./cmd/timetattle metrics -all -by day -csv` over the whole store,
  captured with the check present and with it disabled:

  ```console
  $ cksum series-without-check.csv series-with-check.csv
  745504065 3382783 series-without-check.csv
  745504065 3382783 series-with-check.csv
  ```

  Same checksum, same 3,382,783 bytes. The check never fires on this store, which
  is what a construction-guard should read like until something breaks.
- It did not measure any bucket under a non-default gap. Every figure here is
  the shipped defaults.
- It did not re-measure the unwindowed roll-up. The `rollupunion` table above was
  reproduced to confirm it, not re-derived.
