# What leaving derived data, virtualenvs and Python caches out changes

2026-11-25. Task `JURZZf`, under [the attribution speed plan](2026-11-25-attribution-speed.md),
following [the same change for `.build`](2026-11-25-build-output.md).

On one real store, no longer counting what is stored under `.derived`, `.venv`,
`__pycache__`, `.mypy_cache`, `.pytest_cache` and `.ruff_cache` takes **42% of
the filesystem observations that still counted** (130,156 of 310,159) and a very
small share of the hours: **2.3 engaged hours of 2,241.8** summed over projects,
and **1.2 presence hours of 2,487.6**.

## The two changes

They are separate, so that the second can be reverted alone.

1. `fsobserve` and `filedays` skip a directory named exactly `.derived`,
   `__pycache__`, `.mypy_cache`, `.pytest_cache` or `.ruff_cache`. (`.venv` was
   already skipped.) This governs later harvests only.
2. Stored filesystem observations beneath any of those names, or `.venv`, are no
   longer counted. It is the rule `.build` already had, in the same places;
   `internal/buildoutput` is now `internal/generated`, since a virtualenv and a
   cache are not build output. Nothing is deleted or edited, and no other source
   is touched.

`build`, `out`, `target`, `public`, `pkg` and `vendor` are not excluded, and
`.gitignore` is not consulted.

## What was in `.derived`

The name is not one Xcode picks by itself, so its contents were checked before
including it. In the store measured here, all 35,786 distinct files under it sit
beneath a single parent directory.

- 33,736 are inside the directories Xcode lays derived data out in (`Build`,
  `Index.noindex`, `SourcePackages`, `Logs`, `ModuleCache.noindex` and the
  like). The source-like files among them are dependency checkouts, generated
  sources and resources copied into built bundles.
- 2,050 are outside them: profiler traces, benchmark screenshots and result
  files, and precompiled modules from build probes. Six of these are a small set
  of images and design files that may have been put there by hand; their names
  do not settle it.

No hand-written source tree was found. This was judged from paths and
extensions, not file contents.

## How it was measured

A copy of one real store, whose newest event is 2026-11-25T17:59:56Z, and the
machine's own configuration, `sessions.exclude` included. "Before" is the binary
built from `bdd3e31`, which already leaves `.build` out; "after" is the binary
with this change. For each window:

```
<before> metrics -all -by span -csv -store copy.db [-from DATE] > before.csv
<after>  metrics -all -by span -csv -store copy.db [-from DATE] > after.csv
go run ./scripts/buildoutput -store copy.db [-from DATE] -before before.csv -after after.csv
```

The script prints no locator, project name or client name. `-from` dates are
read as local time.

## File counts

Distinct files named by stored filesystem observations. A file under two of the
names counts once, under `.build` first. "Counted before" is everything outside
`.build`.

| | Whole store | From 2026-08-27 | From 2026-10-26 | From 2026-11-11 |
| --- | ---: | ---: | ---: | ---: |
| All stored | 382,727 | 252,225 | 46,677 | 6,543 |
| under `.build` | 190,352 | 179,568 | 38,172 | 0 |
| under `.derived` | 35,786 | 35,786 | 95 | 0 |
| under `.venv` | 27,638 | 0 | 0 | 0 |
| under `__pycache__` | 1,456 | 439 | 23 | 0 |
| under `.mypy_cache` | 5,228 | 53 | 1 | 0 |
| under `.pytest_cache` | 34 | 8 | 3 | 0 |
| under `.ruff_cache` | 39 | 0 | 0 | 0 |
| **Counted before** | 192,375 | 72,657 | 8,505 | 6,543 |
| **Counted after** | 122,194 | 36,371 | 8,383 | 6,543 |

The same in observations (a file's creation or last write):

| | Whole store | From 2026-08-27 | From 2026-10-26 | From 2026-11-11 |
| --- | ---: | ---: | ---: | ---: |
| All stored | 1,359,622 | 1,212,908 | 116,348 | 8,232 |
| under `.build` | 1,049,463 | 1,038,269 | 101,432 | 0 |
| under `.derived` | 94,702 | 94,701 | 218 | 0 |
| under `.venv` | 27,645 | 0 | 0 | 0 |
| under `__pycache__` | 2,064 | 972 | 23 | 0 |
| under `.mypy_cache` | 5,574 | 58 | 2 | 0 |
| under `.pytest_cache` | 117 | 66 | 5 | 0 |
| under `.ruff_cache` | 54 | 0 | 0 | 0 |
| **Counted before** | 310,159 | 174,639 | 14,916 | 8,232 |
| **Counted after** | 180,003 | 78,842 | 14,668 | 8,232 |

## Hours

"Engaged" and "allocated" are summed over project rows. Engaged time overlaps
between projects, so its sum is not wall-clock time; allocated time is the split
that does not overlap. Changes are computed before rounding.

| Window | Presence | Engaged, summed | Allocated, summed |
| --- | --- | --- | --- |
| Whole store | 2,487.6 → 2,486.4 (-1.2) | 2,241.8 → 2,239.6 (-2.3) | 1,748.9 → 1,747.7 (-1.2) |
| From 2026-08-27 | 1,101.9 → 1,101.9 (0.0) | 942.0 → 941.1 (-0.9) | 605.3 → 605.3 (0.0) |
| From 2026-10-26 | 364.0 → 364.0 (0.0) | 176.2 → 176.2 (0.0) | 127.6 → 127.6 (0.0) |
| From 2026-11-11 | 171.8 → 171.8 (0.0) | 42.6 → 42.6 (0.0) | 41.7 → 41.7 (0.0) |

| Window | Project rows | Projects whose engaged hours changed | Largest fall for one project | Any rise or lost row |
| --- | ---: | ---: | ---: | --- |
| Whole store | 38 | 4 | 1.0 h | none |
| From 2026-08-27 | 18 | 2 | 0.8 h | none |
| From 2026-10-26 | 17 | 0 | 0.0 h | none |
| From 2026-11-11 | 8 | 0 | 0.0 h | none |

"Changed" means by 0.05 h or more. Hours not owned by any project
(`unregistered`, `unlocatable`, `bridged`) are the same to 0.1 h in every window.

## Sittings and chains

`sessionize -dry-run -store copy.db`, whole store:

| | Before | After |
| --- | ---: | ---: |
| Observations read | 2,700,941 | 2,570,923 |
| Sittings | 1,720 | 1,718 |
| Presence hours | 2,487.6 | 2,486.4 |
| Margin hours | 1,033.0 | 1,031.8 |

The rule now takes 1,179,619 observations, 130,156 more than `.build` alone.
Observations read falls by 130,018, which is 138 fewer: the configured
`sessions.exclude` patterns had already been taking those.

`chains -dry-run -store copy.db` finds the same 131 observations considered, 45
documents, 39 in a chain and 6 in none. It reports 1,179,619 observations left
out, up from 1,049,463.

## `orient`'s census

Measured by running the census's three queries (`internal/store/orient.go`)
against `copy.db` directly, once with the one pattern and once with all seven:

| | `.build` alone | All seven |
| --- | ---: | ---: |
| Observations | 2,704,278 | 2,574,122 |
| Observations with no project | 458,692 | 443,481 |

The difference in observations is the same 130,156. The queries took 2.2 to 3.6
seconds either way. `orient` as a whole was not compared: a run of the "before"
binary had not finished after more than five minutes and was stopped.

## What still counts them

As for `.build`: `status`'s per-source event counts; stored sittings, until
`timetattle sessionize` is run again; and `filedays` snapshots taken before the
directory was excluded (not measured).

## Limits

- One store, one configuration. `dashboard`, `review` and `fit` use the same
  read step but were not measured separately.
- Hours are measured for the six names together, not one name at a time.
- The rule goes by the directory's name. Hand-made files in a directory with one
  of these names are dropped too, and so would everything under a registered
  project that itself sits inside one.
