---
title: "Orienting cold"
weight: 5
description: "The one command to run with no memory of the last session: what the state is, what to do next, and why it answers neither status' question nor metrics'."
---

# Orienting cold

`timetattle orient` takes no arguments and is meant to be the first thing run in
a session. It says what the state is, names the single action worth taking, and
says what taking it is worth.

It is designed for a reader who arrives with no memory of the last session,
cannot see a dashboard, will not scroll, and will act on the first screen. That
reader is usually an agent. It writes nothing and opens the store read-only.

```console
$ timetattle orient
/Users/ben/.timetattle/timetattle.db
919811 observations from 2013-09-19 to 2026-08-01; 38 registered directories
last harvest 2026-08-01 11:35, 11m42s ago — whether that is a problem is
`timetattle status`'s question

NEXT  timetattle consolidate -former /Users/ben/git/claude/shared \
            -path <the directory that work lives in now>
      325 observations, <0.1% of the store, name paths that are gone from disk,
      117 of them under the one above. Registering cannot reach these; declaring
      the rename can, and where the work moved to is not something this command
      can know.

UNATTRIBUTED, CARRYING A PATH: 1743 observations, 0.2% of the store. What each
class needs:
         0  inside a registered directory already   timetattle harvest
       325  the path is gone from disk              timetattle consolidate
            /Users/ben/git/claude/shared (117)
            /private/tmp/Demo_inspect (113)
            /private/tmp/OperativeKit/Examples/OK (61)
            and 2 more
        69  a directory register would accept       timetattle register
            /private/tmp (66)
            /Users/ben/Documents/Lens Experiments (1)
            /Users/ben/Documents/Mattebox/Artwork/Pro Feature Highlights/3D (1)
            and 1 more
      1311  above a directory already registered    nothing
        38  inside a shared application cache       nothing

45104 observations carry NO PATH AT ALL — knowledgeC and Biome, which no
registration can reach. Counted here and nowhere above.

EVERY FIGURE ABOVE IS A COUNT OF OBSERVATIONS AND NOT TIME. Sources emit rows at
wildly different rates, so a big count is not a lot of work; `timetattle metrics`
is what estimates hours.
```

Each count is what **one** action against that class is worth. Ranking the three
actionable classes against each other by observation count is the only
comparison the command can make honestly without going through the clustering:
they are counts of the same thing, measured the same way. A count is a poor proxy
for hours — an hour of Claude transcript outweighs a week of `gitlog` commits —
which is why the caveat is printed rather than implied.

The two classes whose action column reads `nothing` are the ones a reader is
most likely to reach for `register` over, so the reason each is unreachable is
worth stating here:

- **Above a directory already registered.** A marker there would own every
  unregistered directory beneath it as well, and a wrong attribution is worse
  than none.
- **Inside a shared application cache.** `register` refuses it, because the cache
  holds every client's work at once — see
  [What is refused](registry.md#what-is-refused). What attributes this evidence
  instead is the manifest's document-name join.

A `COULD NOT BE CLASSIFIED` row appears only when something was read that fits
none of the five, and it carries the reason. Nothing was learned about those
observations, which is not the same as their having no remedy.

## Three commands, three questions

Getting this boundary wrong gives three commands that each half-answer the same
question, so it is worth stating plainly.

| Command | Question | Reads |
| --- | --- | --- |
| `timetattle status` | Is collection still working? | `harvest_run`, `capture_run`, the snapshot directory |
| `timetattle metrics` | What are the numbers? | events, through the clustering |
| `timetattle orient` | What should I do next, and what is it worth? | events, the registry, and the filesystem |

Three consequences follow, and each is enforced by a test rather than by
convention.

**`orient` prints no time at all.** Not one figure it reports goes through
`internal/sessions`, so not one may wear an hours suffix. Counts of observations
are what it has and counts of observations are what it says. A count is a poor
proxy for hours — a Claude transcript emits orders of magnitude more rows per
hour than `gitlog` does — so the caveat is printed rather than implied, and
`metrics` is named as the place hours come from.

**`orient` returns no verdict on collection health.** It prints the age of the
newest harvest run as a bare fact and names `status` as the thing that judges
it. `status` derives its tolerances from each source's own run history, measured
against 76 real runs; a second opinion here, with a second threshold, is exactly
how two commands start to disagree.

**`orient` names an action or says there is none.** A section with nothing in it
still prints its zero, because a section that vanishes when empty reads as "not
measured" rather than as "measured, and none".

## What the five classes mean

`orient` divides every observation carrying a path that no project owns. The
axis is *what would clear it*, not size, because those are different orderings
and only the first survives the pool changing shape. The classes are decided in
this order:

1. **Inside a registered directory already.** Asked of `registry.ResolveAmong` —
   the real resolver, with the real registry, exactly as the attribution pass
   asks it. These attribute the next time the resolution pass runs, and that
   pass runs only as a phase of `harvest`.
2. **The path is gone from disk.** No marker can be written into a directory
   that does not exist, so `register` cannot reach these however many times it
   is run. [`timetattle consolidate`](registry.md) declares the rename instead.
   Checked before anything else about the directory, because a vanished
   directory is indistinguishable from a file and would otherwise be classified
   against its parent — which is usually a container above half the registry.
3. **A directory `register` would accept.** Exists, unregistered, not a
   container, not a cache.
4. **Above a directory already registered.** Refused as a recommendation. A
   marker there becomes the catch-all for every unregistered sibling beneath it,
   and quietly attributing somebody else's work to a catch-all is the
   wrong-attribution-is-worse-than-none failure. The live case is the locator
   `/`, which 1,311 Claude transcript observations carry because an agent
   session's working directory really was the filesystem root.
5. **Inside a shared application cache.** Asked of `registry.CacheConflict`, so
   this can never recommend what `register` rejects. The Motion Autosave Vault
   holds every client's compositions and belongs to no single project; it is
   attributed by the manifest's document-name join instead. See
   [Adding an evidence source](sources.md).

Evidence carrying **no path at all** is counted separately from all five and
never folded into a figure that carries an action. knowledgeC and Biome observe
the machine rather than a file, so no registration can ever reach them.

## Rolling up to the directory that would actually be blessed

Both the registration and the consolidation lists are rolled up, and a naive
ranking gets this wrong in both.

`registry.Resolve` climbs to the *nearest* marker, so a marker in the parent
already covers the child. A flat ranking by count would happily recommend
`Foo/Examples/Hello` before `Foo`, because a subdirectory can easily carry more
evidence than its root — and following it leaves the root's own observations
unattributed and costs a second registration.

A declared former path matches by *prefix*, so one `consolidate -former` on the
vanished root recovers everything recorded beneath it. Measured on the live
store: `/Users/ben/git/theaitrellis` carried 9,924 observations and
`theaitrellis/stacktrace` another 4,294. Naming them separately would be two
declarations for one rename, and the second would be refused, because the first
already claims the prefix.

Only candidates that actually appear are folded together. A common ancestor of
two siblings is never invented, because the ancestor of two project directories
is a container, and blessing a container is what class 4 exists to refuse.

## What it costs

`orient` calls `store.Store.LocatorCounts`, which aggregates in SQL — a
`GROUP BY` over `locator`, plus `COUNT(*)` and `MIN`/`MAX` of the anchor —
rather than decoding every event into Go. Classification (does the registry
already resolve it, does the path still exist, is it a container or a
declared cache) still costs one filesystem check and one registry lookup per
*distinct* locator, which is a small number even when the event count is not:
48 on a 923,000-event live store. Measured on that store: about 4.4 seconds
before this aggregate existed, about 0.9-1.0 seconds after, and the remainder
no longer grows linearly with the event count the way the first version did.

## Flags

```
-store   event store to read (default: ~/.timetattle/timetattle.db)
-index   registry index to resolve against
         (default: ~/.timetattle/registered-directories.json)
```

Both exist so the command can be pointed at a copy. There are deliberately no
others: an orientation with options is one that has to be configured before it
can orient anybody — including no width flag. The prose is wrapped to the
terminal it is printing into, and to 80 when it is printing into a pipe, which
is what it was hardcoded to before. See [Fitting the terminal](terminal.md).

A command the report tells you to run is never broken across a line, however
narrow the margin: `` `timetattle status` `` split in half is neither runnable
nor greppable.
