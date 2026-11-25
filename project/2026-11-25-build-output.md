# What leaving Swift build output out changes

2026-11-25. Task `CSZ8wD`, under [the attribution speed plan](2026-11-25-attribution-speed.md).

Swift Package Manager writes what it builds under `.build/`. The file scanners
now skip it, and the build output already stored is no longer counted. On one
real store that is **77% of all filesystem observations, and a much smaller
share of the hours**: 12.8 engaged hours of 2,254.7 summed over projects, and
0.6 presence hours of 2,488.2.

Whether the changed figures are acceptable is not decided here.

## The rule

- `fsobserve` and `filedays` skip a directory named exactly `.build`.
- The rule for stored records is in one place, `internal/buildoutput`.
- Stored observations are not deleted or edited. A `fs-observations` event
  beneath a `.build/` directory is dropped where events are read, by the same
  step that applies `sessions.exclude` (`sessions.Retain`), with nothing to
  configure. `chains` and `orient`'s census, which do not use that step, apply
  the rule themselves.
- Other sources are not touched. The store holds 70 `claude-transcripts` events
  whose working directory is under `.build/`; they record a session, not a file
  a compiler wrote, and still count.

Filtering on read was chosen over filtering inside `Store.Events`, which would
have dropped the records without counting them, and over a change to the
`canonical_event` view, which needs a schema migration.

## How it was measured

A copy of one real store, whose newest event is 2026-11-25T17:59:56Z, and the
machine's own configuration, `sessions.exclude` included. "Before" is the binary
built from `ff74937`, "after" the binary with this change. For each window:

```
<before> metrics -all -by span -csv -store copy.db [-from DATE] > before.csv
<after>  metrics -all -by span -csv -store copy.db [-from DATE] > after.csv
go run ./scripts/buildoutput -store copy.db [-from DATE] -before before.csv -after after.csv
```

The script prints no locator, project name or client name. `-from` dates are
read as local time.

## File-write counts

Stored filesystem observations (a file's creation or last write), and the
distinct files they name.

| Window | Observations before | After | Build output | Files before | After | Build output |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Whole store | 1,359,622 | 310,159 | 1,049,463 (77%) | 382,727 | 192,375 | 190,352 (50%) |
| From 2026-08-27 | 1,212,908 | 174,639 | 1,038,269 (86%) | 252,225 | 72,657 | 179,568 (71%) |
| From 2026-10-26 | 116,348 | 14,916 | 101,432 (87%) | 46,677 | 8,505 | 38,172 (82%) |
| From 2026-11-11 | 8,232 | 8,232 | 0 | 6,543 | 6,543 | 0 |

The plan's 190,354 distinct locators under `.build/` is these 190,352 files plus
the 2 transcript working directories.

## Hours

"Engaged" and "allocated" are summed over project rows. Engaged time overlaps
between projects, so its sum is not wall-clock time; allocated time is the split
that does not overlap. Changes are computed before rounding.

| Window | Presence | Engaged, summed | Allocated, summed |
| --- | --- | --- | --- |
| Whole store | 2,488.2 → 2,487.6 (-0.6) | 2,254.7 → 2,241.8 (-12.8) | 1,750.3 → 1,748.9 (-1.4) |
| From 2026-08-27 | 1,101.9 → 1,101.9 (0.0) | 950.3 → 942.0 (-8.3) | 605.7 → 605.3 (-0.4) |
| From 2026-10-26 | 364.0 → 364.0 (0.0) | 177.0 → 176.2 (-0.8) | 127.7 → 127.6 (0.0) |
| From 2026-11-11 | 171.8 → 171.8 (0.0) | 42.6 → 42.6 (0.0) | 41.7 → 41.7 (0.0) |

Where the whole-store change falls:

| Window | Project rows | Projects whose engaged hours changed | Largest fall for one project | Any rise or lost row |
| --- | ---: | ---: | ---: | --- |
| Whole store | 38 | 5 | 3.8 h | none |
| From 2026-08-27 | 18 | 3 | 3.2 h | none |
| From 2026-10-26 | 17 | 2 | 0.7 h | none |
| From 2026-11-11 | 8 | 0 | 0.0 h | none |

"Changed" means by 0.05 h or more. Over the whole store, hours not owned by any
project moved too: `unregistered` 145.6 → 146.1, `unlocatable` 585.4 → 585.7,
`bridged` 7.0 → 6.9.

## Sittings and chains

`sessionize -dry-run -store copy.db`, whole store:

| | Before | After |
| --- | ---: | ---: |
| Observations read | 3,750,404 | 2,700,941 |
| Sittings | 1,720 | 1,720 |
| Presence hours | 2,488.2 | 2,487.6 |
| Margin hours | 1,033.6 | 1,033.0 |

`chains -dry-run -store copy.db` is unchanged: 131 observations considered, 45
documents, 39 in a chain, 6 in none. It now also reports the 1,049,463
observations the rule left out.

## `orient`'s census

The census leaves build output out of its total, its span and its unattributed
count. On this copy the unattributed count would not have differed: none of the
458,692 unattributed observations was build output
(`SELECT COUNT(*), SUM(source_id = 'fs-observations' AND locator GLOB '*/.build/*') FROM canonical_event WHERE project IS NULL`).
Its total of all observations drops by the build output. `orient` was not itself
run before and after.

## What still counts build output

- **`status`'s per-source event counts**, which describe what the store holds.
- **Stored sittings**, until `timetattle sessionize` is run again. The record of
  what a derivation excluded lists configured patterns only, so
  `sessionize -dry-run` cannot tell that a derivation predates this rule.
- **Old `filedays` snapshots**, whose filtered counts were taken with `.build`
  included. They were not measured.
- **Attribution** still visits these locators. That is the other tasks in the
  plan.

## Limits

- One store, one configuration. `dashboard`, `review` and `fit` use the same
  read step but were not measured separately.
- The rule goes by the directory's name. Hand-made files in a directory called
  `.build` would be dropped too, and so would everything under a registered
  project that itself sits inside one.
