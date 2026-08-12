# knowledgeC snapshot deduplication, and why it does not by itself buy hourly

2026-08-12. Task iEzR6, agent `snapkeeper`.

Every figure below reproduces with `go run ./scripts/snapchurn` unless another
tool is named. The tool was written for this measurement and is checked in so
the numbers can be argued with rather than trusted.

## What was asked, and what it turns out to buy

The harvest cadence was capped at four-hourly by snapshot storage. Hourly is
the better schedule on the `mtime`-decay argument and the simpler plist, and it
was rejected purely on disk. The brief was to remove that objection by
extending the content-addressing that `internal/snapshot/vault.go` and
`biome.go` already do to the one capture that lacked it.

**The extension is right and is now in.** It does not remove the objection.

At 1.53 MB a snapshot, and with the skip rate measured separately at each
cadence rather than extrapolated from one of them:

| | runs/yr | before | skipped | after |
| --- | --- | --- | --- | --- |
| twice daily | 730 | 1.09 GB/yr | 0.0% | 1.09 GB/yr |
| four-hourly (installed today) | 2,190 | 3.27 GB/yr | 1.2% | 3.23 GB/yr |
| hourly | 8,760 | 13.1 GB/yr | 26.5% | 9.85 GB/yr |

Every skip figure is an **upper bound**, for reasons in "What the projection
cannot see" below.

**The saving is not a fixed fraction, and this is the finding.** What the check
recovers is exactly the runs during which nothing was written to `knowledgeC`
at all, so its yield is entirely a function of how far the cadence outruns the
source's own write rate. At the installed four-hourly schedule that is 1 window
in 84 and the change is worth essentially nothing; the case for it is that it
stops the cost growing super-linearly as the cadence rises. Anyone quoting
"26.5%" against today's schedule would be wrong by a factor of twenty.

## The measurements

Corpus as it stood, `go run ./scripts/snapchurn`:

- 38 `knowledgeC` snapshots, **58.0 MB** on disk, 1.53 MB each.
  This reconciles with the 63 MB in the task description: that was `du -sm` of
  the whole snapshot directory, which also holds `biome/` and `motion-vault/`.
  58.0 MB is `knowledgeC` alone. Nobody had subtracted the two before.
- **35 distinct** by compressed bytes and **35 distinct** by decompressed
  bytes. The two agreeing matters: it says the compressed form is a function
  of the contents on this machine, so the duplicate count is not an artefact of
  the compressor.
- **3 files, 4.8 MB (8.2%)** repeat a digest already on disk. Two groups:
  three snapshots written 2026-07-29 08:13:19, 08:13:30 and 08:13:30, and two
  written 2026-08-03 18:54:51 and 18:58:27. Those are the writes the new check
  would have skipped against the history actually recorded.

Row-level redundancy, same run:

- **890,104** `ZOBJECT` row-instances stored across the corpus.
- **38,966** distinct rows actually held.
- **22.8×** repeat: 95.6% of stored rows are a row the corpus already had.

Projections, trailing 14 days, one run per cadence:

| `-cadence` | windows | no row created | |
| --- | --- | --- | --- |
| `12h` | 28 | 0 | 0.0% |
| `4h` | 84 | 1 | 1.2% |
| `1h` | 336 | 89 | 26.5% |

## What the projection cannot see, and the trap it was nearly quoted from

Measured over the **whole retained span** the same tool reports 16,273 of
17,527 hourly windows empty — 92.8%. That figure is arithmetically correct and
worthless. `/discoverability/signals` carries rows created in August 2024, two
years before every other stream's oldest row, so the full span puts 17,527
windows in a denominator where roughly 700 cover a period this machine was
being recorded at all. Quoting it would have claimed a 92.8% saving that is
overwhelmingly a statement about 2024 and 2025.

`snapchurn` therefore prints both, trailing window first, and says which one to
quote. This is the same failure `scripts/sleepcheck` shipped in the opposite
direction, and it is the reason the unwindowed population is printed rather
than dropped.

The 26.5% is still an upper bound. A window with no row **created** can still
change contents, because rows also age out of retention, and an aged-out row
changes a snapshot as surely as a new one does. The corpus does observe
deletion where it can: of 37 consecutive pairs, 34 added rows (14,530 in total)
and **15 removed rows (17,027 in total)**. No pair of real snapshots is an hour
apart, so nothing here measures deletion at hourly resolution, and the true
skip rate is somewhere below 26.5% by an amount that has not been measured.

## What "identical" was decided to mean

Not the bytes copied off disk. A `knowledgeC` snapshot is a SQLite database
plus its `-wal` and `-shm` sidecars, and those are not a function of the rows
inside them.

Measured on this machine's real database: a `wal_checkpoint(TRUNCATE)` moved
the staged bytes from **36,999,800 to 36,864,000** while the **21,965**
preserved rows were unchanged. A write to `ZSTRUCTUREDMETADATA` or to
`/app/webUsage`, both of which a snapshot discards, does the same on a smaller
scale. A digest over the staged source would have reported "changed" on both
and skipped nothing, while every counter said the deduplication was working.

The digest is taken over the **extracted** database — the rows that actually
get preserved. The evidence that this is the right layer is passive as well as
constructed: the three duplicate pairs on disk were produced by separate
harvest runs, each staging the live database afresh into its own temporary
directory while macOS held it open, and their outputs are byte-identical.
Extraction is what normalises the source.

Constructed cases sit beside that in `internal/snapshot/dedup_test.go`
(`TestTakeIsNotDefeatedByAChangedWALOverUnchangedRows`) and real-data ones in
`snapshotrealdata_test.go`, run with `go test -tags realdata ./...`.

## The mutation

`internal/snapshot/snapshot.go`, guard removed, `go test -tags realdata
./internal/snapshot/ -run TestRealKnowledgeC`:

> a second Take of a frozen real knowledgeC wrote **1,497,605 bytes** to
> `knowledgeC-20260812T110000-d00f88463fe63d38bb7eb95b24c97ec8.db.gz`

and

> a checkpoint of the real database defeated the dedup: 21,965 rows before,
> 21,965 after, and a second 1,497,605-byte snapshot was written

Guard restored, both hold and the second run writes 0 bytes. So: dropping the
check writes 1.43 MB of identical bytes on every redundant run, for ever, and
the second failure is the one that matters — it is what a deduplication keyed
on the wrong bytes would have done silently.

## Recommendation, for decision rather than for implementing

### 1. Hourly is defensible now, but on a corrected number

At ~9.9 GB/yr it is roughly 4× the installed four-hourly schedule, not the
13 GB that was being weighed. Whether that is acceptable is a judgement about
disk, and it is worth stating what is bought: hourly protects the `mtime`
sources, whose evidence moves rather than accumulates. It buys `knowledgeC`
almost nothing — its own floor is `/app/usage` at ~10,000 rows, and
four-hourly already puts about sixty captures inside that window.

### 2. So the cheapest option was not asked for: throttle the snapshot, not the harvest

If hourly is wanted for the `mtime` sources and 9.9 GB/yr is not, the two can
be separated. Run the harvest hourly and take a `knowledgeC` snapshot at most
every N hours. Storage stays exactly where it is today while the cadence that
matters goes up sixfold. This is a handful of lines and no reader changes.
It is **not implemented** — the brief ruled cadence out of scope, and this is a
cadence decision wearing a different hat.

### 3. Row-level capture is where the remaining 22.8× is, and it is a different project

Storing only rows not already held would take the corpus from ~13 GB/yr to
roughly 30–50 MB/yr at hourly. It is not a bigger version of what landed here,
and should not be treated as one:

- A snapshot would stop being a complete picture. `cmd/timetattle/ingest.go`,
  `internal/knowledgec.NewestSnapshot`, `scripts/kcprofile`, `sleepcheck`,
  `joinday` and the `dayview` spike all read one snapshot and assume it holds
  the whole retained window. Every one of them would need a reconstruction
  layer.
- The dedup key must be the row's **contents**, not `ZUUID`. `knowledgeC`
  extends an interval's `ZENDDATE` in place, so a UUID is a name that gets
  revisited — the exact thing `biome.go` refuses to key on.
- The "what is already held" set must fail **open**: any error reading it has
  to mean "hold nothing, capture everything". Being wrong in that direction
  costs a duplicate file; being wrong in the other costs evidence that cannot
  be recovered by any means.

### 4. No retention policy is proposed, and none should be adopted yet

Nothing was deleted and no flag was added that can delete. The existing
argument still holds: until ingestion is proven, the raw snapshots are the only
copy. If one is ever wanted, the cheapest defensible shape is thinning by age
rather than by count — keep everything inside the retention window a snapshot
could still be the only witness to, thin older ones to one a day — and it needs
its own measurement of what ingestion has actually reconstructed, which does
not exist today. **This is a note, not a proposal.**

## Corrections this document makes to earlier records

- Task iEzR6's description says snapshots are "neither deduplicated nor
  pruned". The first half is no longer true as of this change. Pruning remains
  untouched and deliberately so.
- `docs/content/docs/harvest.md` said hourly was "roughly 13 GB a year". With
  deduplication it is roughly 9.9 GB. The old sentence is corrected in place
  there rather than replaced, because the 13 GB figure is still the right
  answer to what it was asked.
- The task's "63 MB across 40 files, ~1.57 MB each" is the whole snapshot
  directory including `biome/` and `motion-vault/`, and 40 counts those two
  subdirectories as entries. `knowledgeC` alone is 58.0 MB across 38 files at
  1.53 MB each. The two agree once the subdirectories are taken out; nothing
  was wrong, but the figures were not the same population.
