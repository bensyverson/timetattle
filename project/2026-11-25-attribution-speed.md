# Making a newly registered project show up quickly

2026-11-25

## The problem

A project was registered just before a scheduled harvest started, and its
dashboard showed zero everything while the harvest ran. The evidence was already
in the store. It had not been attributed yet, and nothing said so.

Attribution is the last step of `harvest`. `resolveProjects`
(`cmd/timetattle/attribution.go`) resolves every distinct locator in the store,
every run, inside one transaction (`internal/store/attribution.go`). It prints
nothing until it ends.

## What was measured

One scheduled harvest on one live store of about 2.6 GB. The binary was built
from commit `5439df8`, older than the tree this plan was written against.

| Figure | Value | Reproduce with |
| --- | --- | --- |
| Reading every source | about 96 s | `harvest_run.started_at` and `finished_at` for the run |
| Whole process | about 22 min | `ps -o etime` against the harvest process |
| CPU used after 21 min | 3 min 18 s | `ps -o time` |
| Distinct locators | 384,575 | the `attribution:` line of `harvest.log` |
| Unattributed locators | 21,245 | the same line |
| Distinct parent directories | 77,171 | `os.path.dirname` over `SELECT DISTINCT locator FROM event` |
| Locators under a `.build/` directory | 190,354 | `SELECT COUNT(DISTINCT locator) FROM event WHERE locator LIKE '%/.build/%'` |

The whole-process time is wall clock and includes reading the sources. The
attribution step was not timed by itself.

## What is believed and not yet shown

- **Most of the time is filesystem lookups repeated per locator.** Read from the
  code: each locator is `stat`ed, then the tree is climbed looking for a marker.
- **Unmatched locators are expensive.** When the climb finds no marker,
  `registry.Resolve` runs `git rev-parse` as a new process.
- **The scheduled job is slowed by macOS.** `scripts/install-agent.sh` sets
  background process type and low-priority I/O. Low CPU use is consistent with
  that, and with other explanations.

No profile was taken. The first task is there to replace these with numbers.

## Order, and why

Measure, then cache per directory, because it is small and must not change any
answer. Tagging at `register` is independent of the cache and fixes the symptom
directly. Whether attribution needs to become incremental depends on how fast
the cached pass turns out to be, so it is a decision and not a task yet.

## Binding rules

- **Observations are never deleted or edited.** Build output already in the
  store stays there. An earlier suggestion to purge `.build/` records was wrong.
- **An optimization changes no attribution.** The check is the same
  `(locator, project)` pairs before and after, on a copy of a real store.
- **`project` stays a recomputable cache.** A full pass must still repair any
  state a faster path leaves behind.
- **This repository is public.** Report counts and durations, never locators,
  project names or home paths from a live store.

## Related work

Task `rxXNX` is about `sessionize` losing its work while a harvest holds the
write lock. A shorter attribution transaction should narrow that window. It does
not close it.

## Tasks

Imported into `job` on 2026-11-25 as `UMvRac`, under `ZSyv3`. The tracker now
holds the current version of these tasks.

```yaml
tasks:
  - title: Make a newly registered project show up quickly
    desc: |
      Registering a project should make its past evidence visible in seconds,
      and a harvest should not spend most of twenty minutes re-answering
      questions it has already answered. Background and measurements are in
      project/2026-11-25-attribution-speed.md.
    children:
      - title: Time the attribution pass and find where it goes
        ref: measure
        desc: |
          Why: every optimization planned here rests on reading the code, not on
          a profile. A scheduled harvest took about 22 minutes, of which reading
          sources took about 96 seconds.

          What: durations for the attribution step on a COPY of a real store,
          split between listing locators, stat and marker lookups, git rev-parse
          processes, and database updates. Compare a foreground run with one
          under the launchd agent's background, low-priority I/O settings
          (scripts/install-agent.sh).

          Seams: resolveProjects and resolveLocator in
          cmd/timetattle/attribution.go, Store.ResolveProjects in
          internal/store/attribution.go, registry.Resolve in
          internal/registry/resolve.go. scripts/attributionskip already runs the
          real pass over a copy of the store; start there.

          Out of scope: changing behavior or the agent's settings.

          Risks: a second run is faster because the filesystem cache is warm.
          Say which state each figure was taken in.
        criteria:
          - A re-runnable script under scripts/ prints per-phase durations and counts, with no locator or project name in its output
          - A dated note records foreground and background figures and says which of the plan's three beliefs held
      - title: Cache each directory's answer within one attribution pass
        ref: cache
        blockedBy: [measure]
        desc: |
          Why: 384,575 distinct locators shared 77,171 parent directories, so the
          same marker climb is repeated about five times over. A climb that finds
          nothing also spawns git rev-parse.

          What: one pass asks the filesystem about a directory once. Both the
          marker climb and the git common-root lookup benefit.

          Already decided: no attribution may change. The former-path match
          works on the unstripped locator and must keep doing so (see
          resolveLocator's doc comment). An unreadable directory is still
          skipped and reported, never written as unattributed.

          Seams: cmd/timetattle/attribution.go and internal/registry. The cache
          lives for one pass; nothing is persisted.

          Open questions (yours): whether the cache belongs in the registry
          package or the caller.

          Risks: symlinked paths resolve through a second, physical climb, so
          a cache keyed carelessly can give one spelling another's answer.
        criteria:
          - A test shows many locators in one directory cause one marker lookup
          - On a copy of a real store the (locator, project) pairs are identical before and after
          - The measurement script shows the before and after durations
      - title: Attribute a folder's existing evidence when it is registered
        ref: register
        desc: |
          Why: a user registers a project and immediately looks at it. Today it
          reads as no work at all until a later harvest finishes, though the new
          folder in the observed case had only 421 locators.

          What: `timetattle register` attributes the stored events under the
          folder it blesses before it returns, and says how many it tagged.

          Already decided: use the same resolver as harvest, so a marker nested
          inside the folder still wins over the new one. The full pass remains
          and must agree with this one.

          Seams: cmd/timetattle/register.go and a path-limited form of
          Store.ResolveProjects. `register` already takes -store.

          Open questions (yours): what to do when a harvest holds the write lock.
          Registration itself must still succeed, and the output must say the
          evidence is not yet attributed. See task rxXNX.

          Risks: a locator may spell the folder through a symlink.
        criteria:
          - A test registers a directory with existing events and reads them back attributed without a harvest
          - A test shows a nested marker keeps its own events
          - A test shows a locked store leaves registration succeeding with a message that says what was not done
      - title: Show attribution progress, and say when evidence is waiting on it
        ref: say
        desc: |
          Why: a running pass was indistinguishable from a stuck one, and a
          dashboard for an unattributed project was indistinguishable from a
          project with no work.

          What: harvest reports the attribution step as it starts and how long
          it took. A scoped report for a registered project with stored but
          unattributed evidence says so instead of printing bare zeros.

          Seams: cmd/timetattle/harvest.go, attribution.go, dashboard and
          orient. Reports distinguish observed, inferred and unrecorded; keep
          that vocabulary.

          Open questions (yours): how to detect the waiting state cheaply. It
          must not cost a filesystem pass.
        criteria:
          - harvest.log shows when attribution started and how long it took
          - A test shows the waiting state named in a scoped report, and absent once attributed
      - title: Stop scanning Swift build output as file work
        ref: build-output
        desc: |
          Why: 190,354 of 384,575 distinct locators were under a .build/
          directory. It is generated output, so it likely inflates file-write
          figures as well as the attribution pass.

          What: the file scanners skip .build the way they skip node_modules and
          .venv, and reports do not count the build output already stored.

          Already decided: stored observations are never deleted.

          Seams: excludedDirs in internal/fsobserve/fsobserve.go and
          DefaultExcludes in internal/filedays/filedays.go, which mirror each
          other. sessions.exclude in the config already filters at derivation.

          Open questions: how stored .build records stop counting is yours to
          propose. Whether the changed figures are acceptable is the user's.
        criteria:
          - A test shows a .build directory is not walked
          - A dated note compares file-write counts and hours before and after on a real store
      - title: Decide whether attribution should become incremental
        ref: incremental
        labels: [decision]
        blockedBy: [cache, build-output]
        desc: |
          Why: even a cached pass revisits every locator on every harvest.

          The choice: keep the full pass; or handle only new locators and redo
          everything when the registry changes. The second is faster and can go
          stale silently, because a moved directory, a hand-edited marker or a
          new git worktree changes answers without changing the registry index.

          What decides it: the measured duration of the full pass after the two
          earlier tasks, against the hourly schedule. This is the user's call.
        criteria:
          - The ruling and the figure it rests on are recorded in a dated project document
```
