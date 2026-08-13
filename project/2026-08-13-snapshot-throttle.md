# Throttling the knowledgeC snapshot, and going hourly

2026-08-13. Task 6fByq, agent `throttler`.

Every figure below names the command that reproduces it. Nothing here is
extrapolated from another line of the same table.

## What changed

The harvest and the `knowledgeC` snapshot no longer share a cadence.

- `scripts/install-agent.sh` installs **twenty-four hourly** `StartCalendarInterval`
  slots instead of six four-hourly ones.
- `timetattle harvest -snapshot-every` (default **4h**) is the smallest interval
  between two `knowledgeC` snapshots, whatever the harvest cadence is. `0`
  disables it.
- Biome, the Motion vault and the `filedays` scan follow the harvest and are
  **not** throttled. Biome and the vault are already content-addressed, so a run
  over an unchanged source writes nothing; the `filedays` scan is the source
  frequency exists to protect.
- A run that declines to snapshot records `capture_run.status = 'skipped'`,
  a fifth outcome added by migration `0015_capture_skipped.sql`.

Net effect: `mtime` evidence is scanned 24 times a day instead of 6, and
snapshot storage stays where it was.

## The measurement that decided it, re-taken a day later

The brief carried figures measured on 2026-08-12. Re-measured on 2026-08-13
with `go run ./scripts/snapchurn -cadence <d>`, against a corpus that has grown
from 38 snapshots to 42 and a trailing window that has moved one day:

| snapshot cadence | windows | no row created, 08-12 | no row created, 08-13 |
| --- | --- | --- | --- |
| twice daily | 28 | 0 (0.0%) | 0 (0.0%) |
| four-hourly | 84 | 1 (1.2%) | 4 (4.8%) |
| hourly | 336 | 89 (26.5%) | 91 (27.1%) |

The conclusion is unchanged and the drift is explained by the window moving:
deduplication alone still leaves hourly at roughly 4x the storage of
four-hourly. `go run ./scripts/snapchurn` reports the corpus at **42 snapshots,
63.7 MB**, so a snapshot averages 1.52 MB and hourly projects to
8,760 × 1.52 MB × (1 − 0.271) ≈ **9.7 GB/yr** against four-hourly's
2,190 × 1.52 MB × (1 − 0.048) ≈ **3.2 GB/yr**. Those reconcile with the
9.85 and 3.23 GB/yr in
[the dedup memo](2026-08-12-snapshot-dedup.md) to within the drift above; nobody
should quote a third figure without saying which day it came from.

Both remain **upper bounds** on what the content check saves. A row can age out
of `knowledgeC`'s retention window inside an otherwise quiet window, and that
changes a snapshot as surely as a new row does. The same `snapchurn` run reports
**16 of 41** real consecutive pairs removing rows (17,285 rows removed against
14,646 added), which is the half a creation-time projection cannot see.

## Hour slots, not instants: a one-second bug the real corpus was already showing

The throttle compares `now` and the newest snapshot **truncated to the hour**,
not the instants themselves.

`launchd` delivers a calendar slot at or after its time, never before, and the
delay is not constant. The live corpus holds three consecutive scheduled runs of
the four-hourly agent stamped `00:00:01`, `04:00:00` and `08:01:07` on
2026-08-13. The first two are **3h59m59s** apart. A throttle comparing instants
refuses that 04:00 snapshot by one second, waits for the next harvest, and from
then on the snapshot walks later whenever a run is late — a four-hour policy
delivering a longer cadence, silently, in the direction that loses evidence.

That is measured twice, once constructed and once against the real corpus.

- Constructed. `go test ./internal/snapshot/ -run TestDueOverHourlyRunsKeepsTheFourHourlyCadence`
  replays 336 hourly runs with launchd-shaped delays against a four-hour
  throttle. With the truncation it writes **84** snapshots, the intended
  cadence. Replace `now.Truncate(time.Hour).Sub(at.Truncate(time.Hour))` with a
  bare `now.Sub(at)` and it writes **75** — nine captures of a self-pruning
  source dropped in a fortnight.
- Real. `go test -tags realdata ./internal/snapshot/ -run TestTheLiveCorpusExercisesTheHourSlotComparison -v`
  finds **4** consecutive pairs in the live corpus that an instant comparison
  would have wrongly refused, at gaps of 3h4m45s, 3h40m27s, 3h47m9s and
  3h59m59s.

The same run's companion,
`go test -tags realdata ./internal/snapshot/ -run TestTheLiveCorpusIsEntirelyDatable -v`,
reports **42 datable snapshots, 2026-07-28T18:07:18-05:00 to
2026-08-13T08:01:07-05:00** — 42 of 42, which matters because a name the
throttle cannot date is a snapshot it behaves as though were not there.

## A regression the existing suite caught, and the fix

The first working version of the throttle broke
`TestAFailedCaptureDoesNotIngestAnEarlierSnapshot`, which had nothing to do with
cadence. A throttled run skipped the `knowledgeC` read entirely, so a harvest
whose Full Disk Access grant had been revoked no longer noticed: it declined the
snapshot, ingested the one it already held, and printed a clean run. That is the
44-hour outage of 2026-07-30 arriving by a new route, delayed by up to one
snapshot interval rather than by a broken grant.

The fix is one `open(2)`. `snapshot.Readable` is exported for this single caller:
a throttled run still opens `knowledgeC` while reading nothing out of it, and a
source that cannot be opened is a **failed** capture rather than a throttled one.
Nothing is staged, extracted or written. Pinned by
`go test ./cmd/timetattle/ -run TestAThrottledCaptureStillNoticesAnUnreadableSource`.

## The staleness bound at hourly

`timetattle status` derives its bound from the installed plist rather than from a
constant, so changing the schedule changes the bound with nothing in `status`
edited. Verified against a hand-written twenty-four-entry plist:

```console
$ timetattle status -schedule <hourly.plist>
schedule:  every 1h00m, from StartCalendarInterval in .../hourly.plist
stale:     1h30m, an interval and a half of that schedule
```

Sensible, and the schedule is not flagged uneven, because twenty-four evenly
spaced fires have equal widest and narrowest gaps. The parser reads the
twenty-four-dict form and the single wildcard dict identically, so the installer
keeping one dict per hour costs nothing.

**The bound is derived from the harvest schedule, and the snapshot no longer
follows it.** That does not make the four `knowledgeC` streams stale, because a
throttled harvest still ingests the snapshot it already holds and so still writes
a `harvest_run` row for each of them, with `seen` non-zero and `new` zero. Pinned
by `go test ./cmd/timetattle/ -run TestAThrottledHarvestStillRecordsEveryKnowledgeCStream`.

## Two findings about the machine, not the code

**The installed binary predates write-time deduplication.**
`~/go/bin/timetattle version` reports `commit 6f2b5a947dcf`, built
2026-08-07T20:25:11Z; deduplication landed at `10db18d`. Every one of the 42
snapshots in the live corpus carries an undigested name
(`knowledgeC-<stamp>.db.gz`), which is what a pre-dedup binary writes — including
the three taken today. So the deduplication measured in the memo above is **not
running on this machine yet**, and neither is this throttle until
`go install ./cmd/timetattle` and `./scripts/install-agent.sh` are re-run. The
storage figures are projections of what the schedule will cost once it is
installed, not descriptions of what it costs today.

**A `skipped` capture does not yet render as itself.**
`cmd/timetattle/status.go`'s `statusCaptureOutcome` switches on `absent`,
`failed` and `partial` and falls through to the `ok` wording for anything else,
so a `skipped` row prints as *nothing new* — which is exactly the collapse
migration `0015` exists to prevent, one layer up. The store and the schema keep
the two apart; the report does not. `status.go` was owned by another task while
this was written and is deliberately untouched. **One `case` is owed there before
the hourly schedule is installed**, or the throttle ships the collapse it was
written to avoid.
