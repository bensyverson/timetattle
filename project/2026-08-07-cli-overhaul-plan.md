# CLI overhaul: fast, terse, scoped, and a dashboard

2026-08-07. Ben's feedback on the metrics view, plus measurements taken before
planning. Decisions confirmed in conversation: adopt Cobra (it has paid off in
Jobs); keep `metrics` as the terse agent-facing surface and add a separate
`dashboard` command for humans; "no data" must never render as "zero work."

## Measurements

**`metrics` takes 73 seconds, and it is one hot loop.**

- `go build -o $TMPDIR/tt ./cmd/timetattle && /usr/bin/time -p $TMPDIR/tt metrics`
  → real 72.97s, user 74.06s, sys 1.22s. user ≈ real: CPU-bound and effectively
  single-threaded.
- `sample <pid> 20` during the run, "Sort by top of stack": 10,554 samples in
  `internal/sessions.maxEnd`, 6,307 in `time.Time.After` (its callee via
  `timespan.LaterOf`). Everything else is noise; the parked-thread counts
  (`__psynch_cvwait`) are idle runtime workers, not work.
- Cause, by reading: `internal/sessions/sessions.go:323` calls `maxEnd(last)`
  once per observation appended to the growing cluster, and `maxEnd` rescans
  the whole cluster from index 0. O(k²) per cluster. The fix is a running
  maximum carried alongside each cluster.
- Prior worth recording because it was wrong: I expected JSONL transcript
  parsing to dominate. It does not even appear in the sample. Measure first.

**Cobra migration scope:** ~20 subcommands in `cmd/timetattle` (20,662 lines
including tests), currently stdlib `flag` with a hand-rolled dispatcher in
`main.go` (205 lines).

**Directory scoping already has its primitive:** `internal/registry`
`Resolve(dir)` climbs from a directory to the owning marker
(`internal/registry/resolve.go:26`), and `ResolveAmong` handles former paths.
The `metrics` scoping task is wiring, not new machinery.

## Design principles for the new work

1. **Terminal output states findings; docs explain nuance.** `--help` is one
   line per flag. Prose currently printed by commands either moves to
   `docs/content/docs/` or is deleted.
2. **`metrics` is the machine contract, `dashboard` is the human view.** Two
   commands, not a flag: `metrics` stays plain, stable and cheap in tokens;
   `dashboard` may be visually ambitious and change layout freely.
3. **"No data" is not "zero work."** A heatmap cell for a day before a source
   existed, or after a source went stale, must render distinctly from an idle
   day (dim hatch / distinct glyph), and the dashboard carries a source
   freshness line so a stale source cannot masquerade as a quiet week. This is
   the absence-of-a-row rule in visual form; it has shipped as a bug twice in
   three tools and must not ship a third time.
4. **The dashboard prints money.** Allocation progress and effective hourly
   rate make `dashboard` output the fourth surface (after `review`, `metrics`,
   `orient`) whose captures re-import client names and rates if pasted into
   anything that ships. Same handling rule as the others; CLAUDE.md gets a line
   when the feature lands.
5. **Color: detect truecolor, fall back to 256.** The `▀` half-block (fg =
   upper cell, bg = lower cell) renders the 7-row year grid in 4 terminal
   lines. Allocation-aware ramp where an allocation is known: 0–8h clear→green,
   8h+ green→yellow→red→white, so overwork is visible rather than rewarded.

   > **Correction (2026-08-07, evening):** the half-block was built as
   > specified here, and Ben's review of the result retired it the same day —
   > two days per line reads "squinty" at one-cell-per-day density. The grid
   > now draws its full seven rows in every capability, one line per weekday,
   > observed days as background-filled cells (a fill is the terminal cell's
   > own attribute, so no glyph-seam artifacts in bitmap fonts). The ramp
   > half of this principle stands unchanged.

## Plan

Sequencing: the perf fix first (everything else is nicer when the tool is
fast), then Cobra and the terseness pass together (both touch every command's
help and output), then `metrics` scoping, then the dashboard feature by
feature, heatmap first.

```yaml
tasks:
  - title: Make the CLI fast, terse, scoped, and give humans a dashboard
    desc: >-
      Umbrella for the 2026-08-07 CLI overhaul. Context, measurements and
      design principles are in
      project/2026-08-07-cli-overhaul-plan.md. Decisions already
      made with Ben: adopt Cobra; metrics stays the terse agent surface and a
      new dashboard command serves humans; "no data" must render distinctly
      from "zero work" everywhere.
    children:
      - title: Fix the quadratic maxEnd rescan in session clustering
        ref: perf-fix
        desc: >-
          timetattle metrics takes 73s wall-clock (real 72.97 user 74.06 via
          /usr/bin/time -p on a fresh build, 2026-08-07) and a 20s `sample`
          puts effectively all of it in internal/sessions.maxEnd:
          sessions.go:323 calls maxEnd(last) once per observation appended to
          the growing cluster and maxEnd rescans from index 0, so clustering
          is O(k^2) per cluster. Carry a running maximum end alongside each
          cluster instead. TDD: write a benchmark demonstrating superlinear
          scaling on synthetic single-cluster input FIRST, watch it be slow,
          then fix. Clustering semantics must not change: the comparison
          stays against the running maximum END, not the previous start
          (sessions.go:314-318 explains why), and all existing sessions
          tests must pass untouched.
        criteria:
          - A benchmark exists that showed superlinear cluster scaling before the fix and near-linear after
          - Full metrics run drops from ~73s to under 5s, measured with /usr/bin/time -p on a fresh build
          - No existing sessions test was modified to get to green
          - The before/after measurement is recorded as a dated project/ document with the reproducing commands
      - title: Add a scripts/profile harness
        ref: profile-harness
        desc: >-
          Per the analysis rule, profiling must be re-runnable, not a
          throwaway. A small harness in scripts/ that builds fresh (stale
          binaries have burned this project before: an installed binary that
          predated an attribution fix reported hundreds of hours as
          actionable-unattributed when the truly recoverable amount was
          approximately zero), runs a named
          timetattle command under CPU profiling, and writes a pprof profile
          plus a plain-text top-N summary. Used to verify perf-fix and to
          catch the next regression.
        criteria:
          - scripts/profile builds a fresh binary, runs a given subcommand, and emits a pprof profile and top-N text summary
          - Running it before and after the maxEnd fix reproduces the finding and confirms the fix
      - title: Migrate the CLI to Cobra
        ref: cobra
        desc: >-
          Ben approved the dependency; Cobra paid off in Jobs. Mechanical
          migration of all ~20 subcommands from stdlib flag plus the
          hand-rolled dispatcher in cmd/timetattle/main.go. No behavior
          change beyond help formatting. BUILD mode: no
          backward-compatibility shims. New commands (dashboard) are born
          into Cobra, so this precedes them.
        blockedBy: [perf-fix]
        criteria:
          - Every existing subcommand runs under Cobra with identical output on identical input
          - All existing cmd tests pass, modified only where they invoked the old dispatcher directly
          - go.mod gains cobra and its transitive deps and nothing else
      - title: Terseness pass over all command output and help
        ref: terse
        desc: >-
          The CLI currently prints documentation-grade prose. Rule: terminal
          output states findings, docs explain nuance, help is one line per
          flag. Move displaced explanations into docs/content/docs/ rather
          than deleting understanding. Done together with the Cobra
          migration since both touch every command's help and output; run as
          a separate pass so review is possible.
        blockedBy: [cobra]
        criteria:
          - Every help screen is one line per flag with a one-or-two-sentence command summary
          - Prose removed from command output that explained concepts now lives in docs/content/docs/
          - Docs updated and README touched only if a new docs file was added
      - title: Scope metrics to the working directory by default
        ref: scoping
        desc: >-
          metrics currently reports on everything. Default becomes the scope
          of the working directory resolved through the registry
          (internal/registry Resolve climbs from cwd; ResolveAmong handles
          former paths). Inside a project, scope to the project; inside a
          client directory that contains projects, scope to the client; in
          an unregistered directory, error with a hint naming the fix. -all
          restores today's everything behavior. TDD against the registry
          fixtures.
        blockedBy: [cobra]
        criteria:
          - Run inside a registered project, metrics reports only that project
          - Run inside a client directory, metrics reports that client's projects
          - Run in an unregistered directory, metrics errors with a hint rather than silently reporting everything
          - -all preserves the previous full report
      - title: Build the dashboard command
        desc: >-
          Human-facing sibling of metrics: same underlying data, free to use
          box drawing and 24-bit color. metrics itself does not change.
          Shared constraints for every child: detect truecolor and fall back
          to 256-color; nothing rendered may collapse "no data" into "zero
          work"; no real client name, rate or contract figure in any test,
          fixture or doc (go run ./scripts/confidential -names <names>
          -gate . is the check, and mind its own known weakness: the
          verdict is only as good as the names list handed to it).
        children:
          - title: Dashboard skeleton and period selection
            ref: dash-skeleton
            desc: >-
              The dashboard command exists under Cobra, shares metrics'
              directory scoping and -all, takes a period (default the
              trailing N days), and renders section stubs. Establishes the
              truecolor/256 detection helper the other sections use.
            blockedBy: [terse, scoping]
            criteria:
              - dashboard runs with the same scoping rules as metrics
              - Truecolor detection with 256-color fallback is implemented once and shared
          - title: Activity heatmap
            ref: dash-heatmap
            desc: >-
              GitHub-style year grid, 7 day-of-week rows, half-block
              rendering (two day-rows per terminal line). Hours per day
              drive a ramp; where a project allocation is known, the ramp
              is allocation-aware (0-8h clear to green, beyond 8h green to
              yellow to red to white) so overwork is visible. Days with no
              observations render as a distinct unobserved glyph, never as
              an idle cell - this is the load-bearing requirement.
            blockedBy: [dash-skeleton]
            criteria:
              - A day with no observations is visually distinct from a day observed idle
              - Allocation-aware ramp applies when an allocation is known and a neutral ramp otherwise
              - Renders correctly in both truecolor and 256-color modes
          - title: Source freshness line
            desc: >-
              Each source's most recent harvest time, rendered so a stale
              source is impossible to mistake for a quiet week. Companion to
              the heatmap's unobserved-day rendering.
            blockedBy: [dash-skeleton]
            criteria:
              - Every registered source shows its last harvest time
              - A source with no harvest in the selected period is visually flagged
          - title: Component stats panel
            desc: >-
              For the selected period, per-source counts with per-day
              averages. Screen-on hours, commits, LOC inserted and removed,
              files created or updated, Jobs tasks closed, Jobs agents
              active, Claude sessions and tokens or reported cost where
              present. A stat whose source has no data for the period says
              so rather than printing zero.
            blockedBy: [dash-skeleton]
            criteria:
              - Each stat shows period total and per-day average
              - A stat backed by no observations renders as no-data, not zero
          - title: Allocation progress and effective rate
            desc: >-
              Where the scoped project maps to a client allocation, a
              horizontal indicator of hours against allocation and the
              effective hourly rate. This section prints real money - its
              output falls under the same never-paste-into-shipping rule as
              review and orient captures, and CLAUDE.md gets a line saying
              so when this lands.
            blockedBy: [dash-skeleton]
            criteria:
              - Under and over allocation are both legible at a glance
              - Effective hourly rate is shown when rate data exists and omitted, not zeroed, when it does not
              - No test, fixture or doc carries a real client name or rate, and the confidential gate passes
          - title: Time-of-day heatmap
            desc: >-
              Horizontal 24-hour strip showing when work typically happened
              across the period, from sitting spans, honoring the
              no-data-versus-zero rule for hours outside any observation
              window.
            blockedBy: [dash-skeleton]
            criteria:
              - Typical working hours are legible at a glance for the selected period
              - Unobserved hours render distinctly from observed-idle hours
          - title: Source attribution stacked bar
            desc: >-
              Horizontal stacked bar of estimated hours by evidencing
              source combination, with explicit segments for multi-source
              corroboration. Shows what drives the estimate: a day resting
              on one source visibly differs from a corroborated one.
              Session.Sources already carries the per-sitting source list.
            blockedBy: [dash-skeleton]
            criteria:
              - Single-source and multi-source segments are distinct and labeled
              - Segment totals reconcile numerically with the metrics figures for the same scope and period
          - title: Filetype breakdown
            desc: >-
              Stacked bar of file activity by category over the period -
              docs versus tests versus code versus graphics - from the
              filedays data. Categories derived from extensions, with an
              explicit other bucket rather than silent omission.
            blockedBy: [dash-skeleton]
            criteria:
              - Documentation, tests, code and other are distinguishable with an explicit other bucket
              - Totals reconcile with filedays counts for the same scope and period
```

## Not in this plan

- Progress indicator for long runs: the perf fix likely removes the need;
  revisit only if runs are still slow enough to want one.
- Parallelizing the pipeline: `user ≈ real` says there is headroom, but
  measure again after the quadratic fix before adding concurrency anywhere.
