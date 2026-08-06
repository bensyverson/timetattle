---
title: "Job task databases"
weight: 34
description: "Reading the `job` tool's own task database as evidence of what the work was, not merely that work happened."
---

# Job task databases

`internal/jobs` reads the SQLite database the [`job`](https://github.com/bensyverson/job)
task tool writes at `<directory>/.jobs.db`, and turns it into evidence.

It is the only source in Timetattle that knows **what the work was**. A commit
says a file changed. An autosave says an application was running. A `job` event
says a named unit of work was created, claimed, blocked, argued about in a
note, or finished.

| Source | What it observes | Shape |
| --- | --- | --- |
| `job-events` | Every row in the database — created, claimed, noted, criteria marked, labelled, blocked, done, and anything else the tool writes | `point` |
| `job-claims` | One span per claim | `interval`, or `unbounded` where no end was observed |

Both are durable. Nothing prunes a `.jobs.db`, so there is no harvest schedule
to race and no window that closes — the same standing as
[git history](/docs/git-history/) and unlike knowledgeC or Biome.

Attribution is free. The database sits *inside* a project directory, so it
resolves through the [registry](/docs/registry/) by locator with no heuristics
at all. In the first real harvest, **every one of 7,793 job events resolved to
a project** and none was left unattributed.

## Planning and coordination is work

Creating a task, adding criteria, marking one passed, labelling, blocking,
unblocking, writing a note and reparenting are all deliberate actions on a
project, and every one is evidence. They are also the **bulk** of the evidence:
across the five registered databases, planning and coordination rows outnumber
`claimed`/`done` rows roughly eight to one.

This matters because a planning session that never claims anything — an hour
spent building out a task tree before any work starts — is fully visible here
and **invisible to every other source in the project**. It touches no tracked
file, so the filesystem walker sees nothing. It produces no commit, so git
history sees nothing.

There is consequently **no whitelist of interesting event types**, and there
cannot be one: the nine live databases already carry four types nobody had
listed when this source was designed (`canceled`, `focus_released`,
`unlabeled`, `moved`). Whatever the type is, it becomes the event's `state`
verbatim, and grouping types into categories is left to whatever reports on
them.

## The actor is not read

`job` records who acted, and across nine databases that is `claude` 907 times,
`ben` 85 times, and roughly sixty uniquely-named agent identities. Agent work
counts fully: activity in the repository is working time, and the actor titles
are disposable.

Nothing here branches on the actor. This source goes one step further and never
reads the column at all.

## One command is one observation

`job criteria pass a b c` writes three `criterion_state` rows carrying one
second, one task and one event type, differing only in a JSON detail. They are
one thing somebody did, so rows agreeing on **(instant, task, event type)**
collapse to a single observation.

Across the five registered databases that is **1,361 of 9,154 rows** — 1,098 of
them `criterion_state` and 188 `blocked` (`job block x --on a b c`). Every
distinct instant survives, and every task survives; what is discarded is
multiplicity within one second of one command.

The collapse happens in the reader rather than being left to the store's
deduplication, so that `7793 events seen, 7793 new` means what it says. Left to
the store, a first harvest would report `9154 seen, 7793 new` and the
difference would be indistinguishable from ordinary re-harvest deduplication.

## What a claim span means

A claim carries a start and an end for a named unit of work, which makes
`job-claims` the first source in Timetattle that directly observes an interval
of **work** rather than of presence.

**`claimed` → `done`, `released` or `canceled` is an interval.** Both endpoints
were directly observed.

**`claimed` → `claim_expired` is unbounded, with `unknown = lost`.** An expiry
is not an observation that work stopped. It is an observation that nobody said
anything, and no later harvest can complete it.

The expiry timestamp is worse than merely uninformative — it is not even the
moment of expiry. The tool writes the row lazily, when a later command happens
to notice, so three separate claims in one database all carry `claim_expired`
at exactly the same second, one of them having been claimed 146 minutes earlier
against a thirty-minute expiry. It records when somebody next ran `job`.

Ending those claims at their expiry timestamps would have invented **sixteen
intervals out of silence, including one of 22h35m and one of 167h9m** — nearly
seven days — measured by removing the guard and re-reading the real databases.

**A claim that is open right now produces no span at all.** Its `claimed` point
in `job-events` already records the instant, and a span held open would collide
in the store with the `lost` span that replaces it when the claim expires,
because the store's identity key does not include *why* an endpoint is missing.

## An interval bounds the work; it does not measure it

A person can claim a task, go to lunch and come back. So the distribution
matters, and it was measured rather than assumed. Across nine databases,
**1,253 completed claim spans**:

| | |
| --- | --- |
| median | 4m54s |
| p90 | 21m35s |
| p95 | 28m59s |
| p99 | 46m13s |
| max | 2h59m32s |
| over two hours | 1 of 1,253 |

The long holds are all terminated by `claim_expired` — which is to say the
mechanism that produces dangerous spans is exactly the mechanism this source
refuses to close.

`job-events` and `job-claims` are **separate source IDs** precisely so that
whether a claim span is clustered as evidence, read as a bound, or ignored
stays a [configuration](/docs/sessions/) decision rather than something buried
in a reader.

## Absent, empty and unreadable are three different things

- A registered directory with **no `.jobs.db`** contributes nothing and is not
  an error. A directory is registered because work happens there, not because
  it uses this tool.
- A `.jobs.db` that exists and holds **no rows** contributes nothing either,
  and is likewise a success.
- A `.jobs.db` that exists and **cannot be opened**, or does not carry the
  tables this reader needs, is an **error** naming the path and the reason —
  never a silent zero.

That last distinction is the bug this project has shipped repeatedly, most
recently when a symlinked root made the transcript reader return 0 of 722,463
events with no error at all.

## One collector over every directory

Both sources take every registered directory at once and write **one harvest
run each**, however many directories are registered.

This is deliberate, and it is not the shape [git history](/docs/git-history/)
uses. `gitlog` runs once per repository and writes one run row per repository,
so `timetattle status` — which groups by source and reports the last row —
showed `gitlog-commits` as *81 seen* when the run had actually seen 932 across
five repositories. Four of five repositories could fail while status still
printed `ok`.

The cost is that failure is total: if any database is present and cannot be
read, the run fails and no events are stored at all, rather than a partial
harvest being recorded as a complete one. Nothing is lost permanently by that —
`.jobs.db` is durable, there is no watermark, and every harvest re-reads all of
it.

The one thing a single run row cannot say is *which* directories had no
database. That is the price of the honest count.

## Schema drift

These files are written by a tool this repository does not control, and several
on one disk need not all be at the same migration. The reader checks that
`events` and `tasks` exist and carry the columns it needs, and names what is
missing if they do not. The database is opened **read-only**, with a busy
timeout, because `job` may well be writing to it while a harvest runs.
