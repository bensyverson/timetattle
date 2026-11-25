# Where the attribution pass spends its time

2026-11-25

Follows [2026-11-25-attribution-speed.md](2026-11-25-attribution-speed.md),
which planned the work from a reading of the code and one wall-clock figure.

## The answer

Most of the pass was git. It ran `git rev-parse` once for every locator the
marker climb could not place, 21,247 processes for 1,884 distinct directories,
and 1,789 of those directories no longer existed. Asking about each directory
once took the pass from 278 s to 27 s in the foreground, and from 841 s to 82 s
at background priority. These are single runs on a busy machine, and load alone
moved one of them by a factor of 2.7 (below), so read them to one figure.

## How it was measured

`scripts/attributionprofile` runs the real store pass and the real registry
resolver over a copy of a store, with the real filesystem and registry index.

```
go build -o /tmp/attributionprofile ./scripts/attributionprofile
/tmp/attributionprofile -store COPY.db                 # foreground, after
/tmp/attributionprofile -store COPY.db -nocache        # foreground, before
taskpolicy -b /tmp/attributionprofile -store COPY.db   # background
```

The copy was a snapshot of one live store of about 2.6 GB, with 38 registered
markers. All four runs found the same 384,575 distinct locators: 363,330
attributed, 21,245 unattributed, none skipped, and no event row changed.

The "before" runs used a build made before the cache was written, with only the
timing seam added. `-nocache` is how to repeat them from a tree that has the
cache: a later foreground `-nocache` run asked the same questions, call for call
(2,808,500 marker reads and 21,247 git processes), and took 746 s while the
machine was far busier, with load averages of 12 to 20.

## Figures

Seconds, one run each.

| Phase | Before, foreground | Before, background | After, foreground | After, background |
| --- | ---: | ---: | ---: | ---: |
| List locators | 8.18 | 33.11 | 7.97 | 16.04 |
| `stat` | 6.54 | 12.12 | 2.34 | 10.34 |
| Marker reads | 17.58 | 43.01 | 0.37 | 1.09 |
| Symlink resolution | 0.37 | 1.56 | 0.01 | 0.06 |
| `git rev-parse` | 215.78 | 673.22 | 0.84 | 3.09 |
| Other resolver work | 3.12 | 7.46 | 1.47 | 4.77 |
| Database updates | 25.99 | 70.29 | 13.80 | 46.68 |
| Commit | 0.00 | 0.00 | 0.00 | 0.00 |
| **Total** | **277.55** | **840.78** | **26.80** | **82.08** |
| CPU, this process | 61.4 | 161.2 | 10.8 | 44.2 |
| CPU, git processes | 135.4 | 395.9 | 0.6 | 1.7 |

Filesystem questions actually asked:

| Question | Before | After |
| --- | ---: | ---: |
| `stat` | 384,575 | 386,369 |
| Marker reads | 2,808,500 | 79,054 |
| Symlink resolutions | 21,247 | 1,884 |
| `git rev-parse` processes | 21,247 | 95 |

"List locators" and "database updates" are measured from outside the store (the
time before the first resolver call, and between calls), and nothing in this
change touches either. They differ between columns anyway, so read that much
variation as the machine, not the code. The same goes for `stat`, whose time
fell although the number of calls did not.

## What state each figure was taken in

- **The filesystem cache was not emptied for any run.** None of these is a
  cold-cache figure. The runs were made in the order before-foreground,
  before-background, after-foreground, after-background, so every run after
  the first followed one that had walked the same paths.
- **The machine was busy** with unrelated builds and tests. One-minute load
  averages read about 4 to 8 during before-foreground, 7 to 14 during
  before-background, and 8 to 10 during the two "after" runs.
- **Background is `taskpolicy -b`, which approximates the agent and is not the
  agent.** It calls `setpriority` with `PRIO_DARWIN_BG`: lowest scheduling
  priority and throttled disk I/O, inherited by the git processes. The plist
  (`scripts/install-agent.sh`) asks for `ProcessType Background` and
  `LowPriorityIO`, which launchd applies through its own resource limits. The
  agent itself was not run or inspected.
- Each run used `/` as its working directory, as launchd does.

## The plan's three beliefs

1. **"Most of the time is filesystem lookups repeated per locator."** Held in
   part. Repeated lookups were most of the time (marker reads and git, 233 of
   278 s), but the plain filesystem ones were small. Marker reads repeated
   about 35 times per directory and cost 18 s. The `stat` of each locator was
   never repeated, since locators are distinct, and cost 7 s.
2. **"Unmatched locators are expensive."** Held, and it was the main cost.
   git took 216 of 278 s (78%) in the foreground and 673 of 841 s (80%) at
   background priority, about 10 ms and 32 ms a process, on behalf of 5.5% of
   the locators.
3. **"The scheduled job is slowed by macOS."** Held for the approximation, on
   weaker evidence than the other two. The two "after" runs were back to back
   under similar load, and background priority was 3.1 times slower. The
   "before" pair gives 3.0 times, but its background run was also the busier
   one, and load alone took the foreground pass from 278 s to 746 s. How much
   launchd slows the agent was not measured.

The scheduled harvest in the plan took about 22 minutes, built from an older
commit. The background "before" pass plus the plan's 96 s of reading sources is
about 16 minutes. The two were not reconciled.

## What changed

`registry.Resolver` remembers, for one pass, what each path answered: whether it
is a directory, whether it holds a marker (or could not be read), where its
symlinks lead, and its git common root. It also does not run git for a path that
is not a directory, where `git -C` can only fail. Remembering alone would have
left 1,884 git processes; the second change leaves 95.

Answers are keyed by the path as spelled, so two spellings of one directory
never share one. Nothing is persisted.

## No attribution changed

Two clones of the same snapshot had their `project` column cleared, so every
pair in each result was written by that run. One got the resolver as it was
before this work, the other the cached one, one run straight after the other.
Both held 384,575 distinct `(locator, project)` pairs, 363,330 of them with a
project, and neither held a pair the other did not. Both skipped no locator.

```
/tmp/attributionprofile -store SNAPSHOT.db -work DIR -clear -nocache -keep before.db
/tmp/attributionprofile -store SNAPSHOT.db -work DIR -clear -keep after.db
/tmp/attributionprofile -compare DIR/before.db,DIR/after.db
```

The "before" side of the check was a temporary build of the unmodified resolver,
not `-nocache`; the commands above are the way to repeat it now.

This covers the paths this store holds. It held no unreadable locator, so the
rule that an unreadable directory stays skipped rests on tests alone.

## What is left

After the change, listing locators and database updates are 22 of 27 s in the
foreground and 63 of 82 s in the background. Whether attribution should become
incremental is task `incremental` in the plan, and this is the figure it asked
for.
