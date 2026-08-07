---
title: "The dashboard"
weight: 51
description: "The human-facing sibling of metrics: same data, a trailing window instead of an open-ended span, and color where the terminal supports it."
---

# The dashboard

`timetattle dashboard` is [`metrics`](metrics.md)'s human-facing sibling. It
reads the same store, computes over the same pipeline — the same clustering
parameters from the config file, the same client-project log, the same
`metrics.Compute` — and answers the same two questions, engaged time and
presence. What differs is the shape of the answer: `metrics` is the terse,
stable, machine-facing surface this tool's other commands and any script
piping it are built against, and `dashboard` is free to lay itself out with
box drawing and color and to change that layout at will. Neither command
changes what the other computes or prints.

## Scope: the working directory, exactly as `metrics` resolves it

`dashboard` shares `metrics`' own scoping rule rather than a copy of it —
both commands call the same `resolveWorkingScope`. Run with no flags, it
scopes to wherever it is run from: inside a registered project it reports
only that project, inside an unregistered directory that sits above one or
more projects all registered to the same client it reports that client, and
anywhere else it refuses rather than silently reporting everything. `-all`
restores the unscoped report from any directory; `-client` and `-project`
narrow it explicitly and, like `-all`, bypass directory scoping entirely. See
[Default scope: the working directory](metrics.md#default-scope-the-working-directory)
for the full rule — it is one rule, read by both commands.

## `-days`: a trailing window, not `-from`/`-to`

Where `metrics` takes an open-ended `-from`/`-to` span, `dashboard` takes
`-days N` — a trailing window of `N` working days ending today, tiled by the
same calendar `metrics -by day` cuts its own series with (`-day-start`,
`-week-start` and `-timezone` in the config file, or the defaults). `N`
defaults to 365: a full year, because the activity heatmap this command
builds toward is a GitHub-style year grid, and a shorter default would hand
that section less than a year to draw before anyone had a reason to widen it.

## Color: detected once, shared by every section

Every section of the report paints through one color-capability value,
detected once per run rather than by each section asking the terminal itself:

1. **Not a terminal at all** (a pipe, a file, a test) → plain text,
   unconditionally, before the environment is even read. A `dashboard`
   capture is exactly as legitimate a thing to script or to paste into a
   ticket as a `metrics` one, and raw escape codes in either would corrupt it
   for any reader that is not a terminal.
2. **`NO_COLOR` set**, to anything, outranks every other signal.
3. **`COLORTERM` is `truecolor` or `24bit`** → full 24-bit color.
4. **`TERM` contains `256color`** → the nearest cell in the standard 256-color
   palette's 6×6×6 cube.
5. Anything else → plain text. A terminal that says nothing about its color
   depth is not assumed to have any.

## What renders today

The sections this page will eventually describe in full — an activity
heatmap, source freshness, per-component stats, allocation progress and
effective rate, a time-of-day heatmap, source attribution, and a filetype
breakdown — are being filled in one at a time; see
[the CLI overhaul plan](../../../project/2026-08-07-cli-overhaul-plan.md) for
the current state of each. Until a given section lands, it prints a single
placeholder line in its place, in the fixed order above, so the report's
shape does not change as each one is filled in.

**Allocation and effective rate will print money once it lands**, the same
way a `review` or `metrics -client` capture already can with a real client's
own configuration. Treat a `dashboard` capture under the same rule: it is
fine on your own machine and never in anything that ships — see
[`scripts/confidential`](../../../README.md) for the check.
