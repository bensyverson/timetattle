---
title: "Save-as chains"
weight: 37
description: "Recovering iteration rhythm from sibling filenames, for applications that branch instead of editing — and why names are never split apart."
---

# Save-as chains

Some applications are not used the way a text editor is used. In Blender and
Motion the working pattern is: create a file, work in it, then **File > Save As**
to a new name beside the old one. The old file is never touched again.

That has a consequence which defeats every other source in Timetattle. A file
that is created and never rewritten has a birthtime equal to its modification
time — for all 25 `.blend` files in the reference project, to the second — so it
carries **no span at all**. Ask any single file how long it was worked on and
the honest answer is zero.

The history is not missing. It is in the filenames:

```
2026-07-11 09:19:26  lightbar
2026-07-11 09:33:38  lightbar-top                        (+14m)
2026-07-11 09:36:26  lightbar-top-filmic                  (+3m)
2026-07-11 13:33:43  lightbar-top-filmic-rect-prefable  (+237m)
2026-07-11 13:40:58  lightbar-top-filmic-rect             (+7m)
2026-07-11 16:53:50  lightbar-top-filmic-fable          (+193m)
```

Tight iteration bursts separated by long gaps: two rounds of work before lunch,
a long absence, seven minutes of adjustment, another long absence. That is
genuine editing rhythm at a granularity nothing else here reaches. **This is the
version history that `.DocumentRevisions-V100` turned out not to hold — it is
not in the Versions store, it is in the filenames.**

`internal/chains` recovers it, and `timetattle chains` runs and reports it.

## Running it

```
timetattle chains
```

reads filesystem observations back out of the event store, clusters them, and
replaces the derived chain tables with the result. Re-run it after any harvest
that added observations.

To look at a project the store has not ingested yet:

```
timetattle chains -roots ~/git/acorn
```

`-roots` walks those directories directly and **never writes**. What it reports
is what the model makes of the disk right now, not of recorded evidence, and
writing that into a derived table would claim the store holds observations it
does not.

Useful flags:

| Flag | Meaning |
| --- | --- |
| `-ext .blend` | restrict to given extensions |
| `-class source,output` | which file classes to consider (default `source`) |
| `-min-prefix N` | characters two names must share where neither is the other's prefix (default 6) |
| `-coincidence 1s` | how close two births count as one action |
| `-from`, `-to` | bound the window |
| `-dry-run` | print without writing |

## This is inference, not a source

Nothing in `internal/chains` implements `source.Source`, and nothing runs during
harvest. That separation is deliberate and worth stating plainly.

Clustering files by name and deriving intervals between them is a **model**. It
will be revised. The event store is append-only with no retraction — an
observation is never rewritten to reflect a better later reading — so a
clustering decision written as events could never be taken back, and every later
harvest would stack a second generation of the model on the first with nothing
able to tell them apart.

So chains live in a derived table (migration `0004`) that is wiped and rebuilt in
a single transaction, together with the parameters that produced it. A reader
who finds a chain row can always recover which classes were considered and what
prefix length was required, so a stale recompute is auditable rather than
silently mixed with a fresh one.

Four tables are cleared and repopulated by that one transaction, and the
transaction belongs to the store: `chains` calls `Store.ReplaceDerived` once,
and `chains.Rebuild` is the write it is handed. Nothing here can commit half of
it. See [the store's surface](/docs/store#the-surface-and-why-nothing-reaches-around-it).

## Names are never split apart

The obvious implementation cuts each name at its dashes and calls the first
segment the stem. **Do not do this.** It fails exactly the way Claude Code's
project directories fail — unable to tell a separator from a dash inside a real
name. `dark-crystal` is one document, not a `dark` family with a `crystal`
variant.

Measured against the reference project, dash-splitting tears apart **32 pairs of
documents the model correctly joins** — `scanline` from `scanline2-face-denied`,
`lightbar` from `lightbar2-beat3`, `Wall Switch+Keypad_1line_off` from
`..._on` — because the segment before the first dash differs.

Instead, no name is ever cut. Two whole names are compared, and the only thing
computed is how far they already agree:

1. Take the longest prefix the two share that ends at a **name boundary in both**
   of them. A boundary is a separator (`-`, `_`, space, `.`, `+`) or a digit
   following a non-digit. The longest common prefix alone is not enough — it
   overshoots into the component that differs, which is why `..._off` and
   `..._on` share `..._o` and the walk has to back off to `..._1line`.
2. If that prefix **is** one of the names, they are relatives, with no length
   floor. The ancestor is on disk; a two-character document is as real as a
   twenty-character one.
3. Otherwise the family has no surviving ancestor, and the shared prefix must
   reach `-min-prefix` characters **and** be at least half the shorter name.

Step 3 is not hypothetical. The reference project holds seven
`animation_final-*` documents and no `animation_final` — the ancestor was never
kept — agreeing on 15 characters, and they are a real chain. The case the floor
must refuse is `dark-crystal` against a `dark-matter`, agreeing on 4.

The floor is also what keeps step 1's backward walk safe: `IMG_1234` and
`IMG_1235` do reach a real boundary, at `IMG`, and three characters is not a
family.

## Three reductions before anything is clustered

**Only source documents.** A `.motn`, `.blend` or `.psd` — something a person
made decisions in. Rendered output is excluded, and the reason is quantitative:
one export writes a thousand sequentially-named frames sharing a long prefix and
landing in the same second, and a model that chained them would report the
largest chain in the corpus for the action that took the least thought.
(Sequential frames are refused by the name rule too, since `frame-0004` and
`frame-0005` agree only up to a digit that follows a digit — but the class
filter is the cheaper defence.)

**Within one directory only.** Save As offers the current folder, so a branch
lands beside its parent. This is also what keeps two unrelated documents that
share a stem apart, and the reference project has exactly that case:
`blender/lightbar.blend` and `scanline/lightbar.psd` are a Blender chain and a
Photoshop chain, on the same day, with nothing to do with each other. Dropping
directory scoping merges them into one nine-step chain interleaving two
applications — **and loses a document outright**, because two files with the same
stem in different directories collapse into one and the earlier birth wins.

**Earliest observation per document, not every observation.**
`internal/fsobserve` emits up to two points per file, birth and mtime, and does
not label which is which. Only a new *name* is a branch; a save into an existing
file is an edit. Taking both would put `lightbar.psd` in its chain at 09:55 and
again at 23:41 and invent a branch that never happened.

A document is a name, not a file. `foo.blend` and `foo.blend1` are one document
whose time is the earlier of the two, and so are `dark-crystal.psd` and
`dark-crystal.motn` — a design worked in two applications is one lineage.

## Simultaneous births are one action

Seven `.motn` files in the reference project share a birthtime of
`2026-07-11 16:15:22`, because they were duplicated together in one gesture.

Seven steps would be six intervals of zero, which reads as furious activity and
is in fact one action. Documents born within `-coincidence` of the first member
of a group collapse into a single step naming all of them. Across the reference
project this removes **10 spurious steps and 10 intervals of zero**: 39 chained
documents reduce to 29 steps.

The grouping is greedy from the first member rather than by truncating to a
second, because truncation splits `16:15:21.999` from `16:15:22.001` while
treating `16:15:22.001` and `16:15:22.999` as one.

A family born *entirely* in one gesture is therefore one step, and one step is
not a chain. Neither is a lone document. A chain needs at least two steps,
because with fewer there is no rhythm to report.

## A chain is lineage, not a session

No gap threshold is applied and none belongs here.
`lightbar-top-filmic-2` was born 18 days after the rest of its family and is
still the same lineage. Whether those 18 days are one working session is a
question for sessionization, fitted against ground truth, with a parameter this
package must not quietly pre-empt. Every step carries its gap so a consumer can
decide.

Time order is recorded rather than the parent-child tree. The real structure is
a tree — `lightbar-top-filmic-rect` and `lightbar-top-filmic-fable` both branch
from `lightbar-top-filmic` — and the names retain enough to recover it later,
but the question this exists to answer is *when work happened*, and that is the
time order.

## What "no chain" means

Documents that were observed and formed no chain are recorded by name, not
merely counted. A directory with no chain and a directory nobody walked read
identically from a chain list alone, and telling them apart is the point: this
project has shipped that conflation twice, in three separate tools.

For the same reason `timetattle chains` prints a warning on stderr when the
store holds no observations from the source at all. Silence there would read as
"no chains exist", which nobody is entitled to conclude from a store that was
never harvested.

## What it finds

Over the reference project (`timetattle chains -roots ~/git/acorn`): 74 source
observations, 43 documents, **8 chains covering 39 documents in 29 steps, with 4
documents in no chain**. Chain lengths are 2, 2, 2, 3, 3, 3, 7, 7 steps. A model
that chains everything measures nothing, and one that chains nothing measures
nothing either; both degenerate outcomes are asserted against in
`internal/chains`'s real-data tests.

Full figures and the discrepancy this work resolved in the spike record are in
[`project/private/2026-07-31-save-as-chains.md`](../../../project/private/2026-07-31-save-as-chains.md).
