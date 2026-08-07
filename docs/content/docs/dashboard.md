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

The window then **clamps to the first observation in scope**: a project
registered in July does not open with months of unobserved cells drawn for a
window that predates it. The header states the range actually used —
"N days requested, clamped to the first observation in scope" — so the
divisor under every per-day average describes days the report really covers.

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

## The sections

One rule runs through every section, because this project has shipped its
violation twice: **"no data" never renders as zero work.** A day, hour, or
stat the sources never observed is drawn or labeled distinctly from one they
observed as idle. Where the two cannot be told apart, the section says so
rather than choosing.

### Activity Heatmap

A GitHub-style year grid: one column per week (weeks start where the
configured calendar starts them), seven day-of-week rows, one terminal line
per row. Observed days paint as background-filled cells — a fill is the
terminal cell's own attribute, so the grid stays seamless in fonts that
don't draw block glyphs flush. A cell's color is that
day's hours — presence when unscoped, the scoped engaged share under a
project or client — never the per-project sum, which double-counts concurrent
work. The ramp anchors at eight hours: darkness to green up to the anchor,
then green through yellow and red to white beyond it, so overwork is visible
rather than rewarded with deeper green. The anchor is a parameter awaiting a
per-project allocation config. A day is *observed* if it carries recorded
presence or a logged collection run; only a day with neither draws the dim
unobserved hatch, and observedness is judged unscoped — under `-project`, a
day the other project worked reads as measured idle, not as a gap. Without
color the grid falls back to one plain row per weekday with distinct
characters per bucket.

### Source Freshness

Shown only under `-detailed`. One line per source the store has ever
attempted: its last successful harvest as an age and an absolute instant,
`STALE` when nothing landed inside the window, `NEVER HARVESTED` when
nothing ever has. A source configured but never once attempted cannot appear
here — the harvest log is the authority, and it has no row to show.

### Component Stats

Per-source counts for the window with per-day averages — presence and
screen-on hours, commits and line counts from the git log, file writes and
files touched, task actions from the jobs source, Claude records, prompts,
sessions and four token figures. The token figures stay separate — on a real
corpus cache reads outweigh output tokens by hundreds to one, so a single
"tokens" number would be the cheapest figure wearing a general name. A
session counts toward every scope it *touched*, so per-project session
counts legitimately sum to more than the store-wide total. Token averages
divide only by days that carry token observations, so a window reaching
past the payload era is never diluted.
Averages divide by *observed* days, not calendar days, so a year-long window
over a two-month store reports a rate somebody actually measured. A row
prints `0` only when the sources were collecting and nothing happened;
`unrecorded` means they were not collecting. Under a scope the screen-on row
labels itself "screen-on, machine-wide": the presence sources record no path
and attribute to no project, so that figure never narrows — comparing it to
the scoped presence row reads as "how much of the machine's lit time went
here". Merge commits carry no diff, so they count as commits but contribute
to neither line count. When a collection run inside the window is
recorded as lost or partial, a closing line says the counts are lower
bounds.

### Time of Day

A 24-column strip of when work fell across the day, in the configured
calendar's timezone, cut from the same sittings every command shares —
a sitting spanning an hour boundary contributes exactly its minutes to each
side. Hours weight by observed days, and when the window is partially
observed the strip says over how many days it is averaging.

### Source Attribution

A stacked bar of the window's sitting hours by the combination of source
*families* that evidenced them — Apps (biome and knowledgeC), Claude, Files,
Git, Jobs, Motion, Blender. `Claude` alone and `Claude, Files, Git` are
different segments, never a collapsed "multiple", and each legend line reads
hours first: `403.1h (36.2%): Claude, Files, Git`. The segments sum exactly
to the presence total the report states; a sitting resting on a single
family is visibly different from a corroborated one, which is the point.
Beyond eight combinations the rest fold into one labeled remainder, and a
source id the family map does not know fails the render rather than falling
out of the chart silently.

### Filetype Breakdown

A stacked bar of file-touch activity by category — documentation, tests,
code, graphics, config, and an explicit **other** that always prints and
always sorts last. Tests are recognized by filename convention before
extension. The counts come from the fs-observations source, which walks
registered project roots; the separately-scoped filedays snapshot records
no per-file events, so its wider roots list is out of this panel's reach.

### Allocation & Effective Rate

**This section prints money.** A capture of it carries a client name beside a
rate or a contract total, which is more than a `review` or `metrics -client`
capture carries. Fine on your own machine; never in anything that ships —
`scripts/confidential` is the check, and the config file this reads from lives
outside every repository for the same reason.

Most projects are unbilled personal work, so on most runs this section is one
quiet line saying no engagement is configured. That is deliberate: an
unconfigured scope owes nothing and is owed nothing, and an empty progress bar
beside a `$0.00` rate would be asserting a contract that does not exist.

#### Configuring an engagement

Money lives in the `engagements` section of `~/.timetattle/config.json`, keyed
the two ways a report is scoped — by client and by project. **Each entry
declares exactly one of two shapes.** An entry carrying both is refused when
the config is loaded, not resolved by a silent preference; an entry carrying
neither is refused too, since a key that does nothing is indistinguishable
from a key nobody typed. Every figure below is invented.

```json
{
  "engagements": {
    "clients": {
      "Northwind": { "currency": "$", "rate": 150, "budgeted_hours": 30, "period": "week" }
    },
    "projects": {
      "atlas": { "currency": "$", "contract_total": 24000, "expected_hours": 160,
                 "started": "2026-01-15" }
    }
  }
}
```

- **Hourly** — `rate`, `budgeted_hours` and `period` (`week` or `month`). All
  three are required together: a budget with no rate has no money in it, and a
  rate with no budget has nothing to draw a bar against.
- **Contract** — `contract_total` and `expected_hours`, both required, plus an
  optional `started`.
- `currency` is a **label**, printed in front of an amount. Timetattle converts
  nothing and defaults to nothing: an entry that states no currency prints its
  amounts bare rather than acquiring a dollar sign nobody asked for.
- A stated zero is refused. Absence and zero are different facts, and only
  absence means "unsaid".

#### The hourly shape: a budget pro-rated to the window

```
  Northwind (client) — 30.0h per week at $150.00/h
    [██████████████░░░░░░░░░░░░░░░░] 22.2h of 47.1h budgeted (47%), under by 25.0h
```

The budget is **pro-rated to the window** by the calendar's own working days —
the same days every other section is tiled by, beginning at `day_start` rather
than at midnight. Thirty hours a week over eleven days is 30 × 11/7 = 47.1h.
Three details of that rule are load-bearing:

- **Not Monday-to-Friday.** The calendar has no concept of a weekend, and
  inventing one would assert a working week this tool has never observed. A
  budget stated per week is per week.
- **A partial day counts as a fraction.** A window that clamped to the first
  observation in scope opens at an *instant*, not at a day boundary; counting
  that day whole would inflate the budget by up to a full day, which on a short
  window is enough to flip an over-budget verdict to an under-budget one.
- **A month is not a fixed number of days.** A monthly budget divides by the
  real length of each day's own month, so a window covering exactly February
  consumes exactly one February's budget. An averaged 30.44-day month would
  misreport January by 10% — sixteen hours on a 160h budget.

Over budget is marked three ways over, so no single channel carries it: the
bar fills a different color, an overrun mark runs *past* the closing bracket
(visible with no color at all, in a pipe or a test capture), and the verdict
word is capitalised.

```
  Northwind (client) — 8.0h per week at $150.00/h
    [██████████████████████████████]++++++ 22.2h of 12.6h budgeted (176%), OVER by 9.6h
```

An hourly engagement shows its **nominal rate** and no effective rate. Dividing
the budget's money by the hours worked would answer "what did you actually earn
per hour" only if over-budget hours went unbilled — and whether they did is a
fact about a contract that nothing in the record states. The tool declines to
guess rather than printing a number that looks measured.

#### The contract shape: a burn-down over the contract's life

```
  atlas (project) — fixed contract $24,000.00 for 160.0h expected, since 2026-01-15
    [█████░░░░░░░░░░░░░░░░░░░░░░░░░] 28.2h of 160.0h expected (18%), 131.8h remaining
    effective rate $852.07/h over 28.2h to date; 22.2h of that in this window
    collection began 2026-07-28, after this contract started on 2026-01-15 —
    hours to date are a lower bound and the effective rate an upper one
```

A contract does not renew with the reporting window, so the burn-down and the
effective rate both read **all-time** hours — everything from `started` (or from
the beginning of the store, when no start is given) to the end of the window.
Cutting them to a trailing week would report 158h remaining on a contract with
2h left. The window's own share is stated on the same line, so no figure here
is ambiguous about which span it covers.

The **effective rate** is the contract total divided by the hours it has
actually taken, and it *falls* as those hours accumulate — that fall is the
whole point of the figure. When no hours are recorded against the contract at
all, the rate is **omitted with a reason**, never printed as `$0.00/h`: a zero
there would assert that this work earns nothing per hour, which is the one
thing the record cannot say.

Reaching outside the window costs one extra store query and one extra
clustering pass, and it is paid only when a contract is actually in scope. A
contract whose whole life the window already covers is narrowed in memory
instead, and several contracts in one report share a single fetch.

#### When the record cannot support the figure

Collection began when this tool was installed; a contract began when somebody
signed it, and there is no reason those are the same day. When the store's
first recorded collection run falls after the contract's `started`, the panel
says so and names which direction each figure is wrong in — hours to date are a
**lower** bound, and since the rate divides a fixed total by those hours, the
rate is an **upper** one.

Likewise, a window in which nothing at all was collected renders `unrecorded`
rather than `0.0h` against the budget. Zero hours against a budget is a real
measurement when the sources were running; it is an assertion nobody made when
they were not.

### Scope Caveat

The source-attribution and time-of-day sections are cut from sittings, which
are deliberately not per-project — so those two sections show the same
numbers under any scope, exactly as the presence line of `metrics` does.
