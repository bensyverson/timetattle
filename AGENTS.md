IMPORTANT: As you're implementing features, you MUST keep the [Documentation](docs/content/docs/) up to date, updating [README](README.md) only if a new documentation file has been added, or if new users NEED to know about your changes.

## Overview

You are working on Timetattle, a tool to analyze Claude Code repositories to understand *when* work has occurred, *what* work has occurred, and gain insights into the patterns within the data.

## Documentation

When you need additional context, consult the docs:

- [README.md](README.md) - Project overview and quick start
- `project/private/` - Historical design documents and past project plans. NOT TRACKED: it is gitignored, stays on this machine and in the private mirror, and is the one part of this repository that does not ship. It measures one person's real working days against real clients, so it names them throughout.

## CLI conventions

- **Open by running `job orient` (standalone tool, no arguments).** At session start, `job orient` is the landscape briefing.

## General

- If a requirement is ambiguous or could be solved in several ways, choose the most idiomatic way to solve the problem in the given language if that would resolve the ambiguity.
- If the ambiguity is more about the requirement itself, or you face an architectural question, don't make a decision—stop and ask the user instead.
- Avoid dependencies unless the required functionality would be unreasonable to re-implement. If you MUST bring in a dependency, get the user's permission first.
- IMPORTANT: In this project we ALWAYS follow strict "red/green" TDD; write tests for all example cases we need to handle and any new methods we're implementing, verify that they fail, and *then* proceed to implement your code changes. If you must alter a previous test to get it to pass, explain exactly WHY to the user and get their consent.
- Before fixing a bug, try to create a regression test to catch it in the future.
- DO NOT begin a new chat by doing an extensive exploration of the entire codebase. That is wasteful, as this is a large codebase. Instead, use `job` and the README to get oriented.
- To create and manage plans and task lists, always use the `job` command. Do not use Plan Mode.

## Understand the "why"
Before you answer a question or respond to a request, you must understand **why** the user has made this request. Beware of the "XY Problem." If the user's motivation or goal is not 100% clear, first ask clarifying questions until you fully understand what they're trying to achieve.

## Diverge, then converge
Once you understand the prompt, rather than jumping to a solution, you will use divergent thinking. Brainstorm other options. Weigh these options against the user's preferences and overall objectives. Then, converge on a recommendation, and ask for confirmation from the user. Only then can you fully converge and begin executing on a direction.

## Analysis
It will sometimes be valuable to create a script or tool in order to aid your analysis. Before you do, check the `scripts` directory to make sure it doesn't already exist. If it doesn't, rather than creating a disposable or inline script, add it to the `scripts` directory, so the user can re-run the tool in the future.

- **A clipped window can hide the worst case, and will do it silently.** `scripts/sleepcheck` clips to the span two sources share, reported "1.88h of gaps, largest 1h27m", and had excluded a 6h23m gap that ended 26 seconds before its window opened. Before generalising from a windowed measurement, measure the unwindowed population once and say which one you are quoting.
- **Absence of a row is not an observation.** Distinguish "observed X", "observed not-X", and "nothing was recorded" every time. Collapsing the last two is a bug this project has now shipped twice, in three separate tools.
- When two tools measure the same thing, reconcile them numerically and say so. Two figures that differ for an understood reason are fine; two that nobody has subtracted are not.
- **A tool that takes what to look for as an argument is only as good as the argument.** It will report clean against a list that is short, stale or mistyped, and a clean report is indistinguishable from a correct one. Two instances, both in `scripts/confidential`: its name list went unchecked for five days while registered directory names sat in the tracked tree, and its deny list, handed a placeholder string copied out of an example command, produced a finding against the very redaction that placeholder documents. Derive the list from something that cannot go stale, or state in the output which list produced the verdict.
- **A backup is a claim until it is restored.** `git bundle verify` prints "is okay" and "records a complete history" having checked only the pack — it does not tell you the archive is usable. Clone it to a scratch directory and count what comes out. Restoring the 2026-08-06 archive gave 197 commits at a tip and tree hash captured before the wipe; nothing short of that restore would have proved it. This applies before any irreversible step: archive, restore the archive, and only then destroy.

## Delegating execution
Discussing design on the main thread and dispatching execution to subagents works well here, and is the default for anything larger than a small change. Run them in the background, in parallel where their file sets are disjoint.

- The repo *must be pushed* prior to dispatching an agent. Agents spawn with the last push, not the last commit!
- Briefs must carry the VERIFIED FACTS and the cross-checks to validate against, not just the goal — real record counts, known timestamps, the specific property that must hold.
- Say what NOT to build, and which files another agent owns.
- Ask explicitly for pushback on the brief. Agents have repeatedly found real bugs in already-committed code AND in the brief itself; on this project that is the main value of delegating, not the typing.
- Give each agent a unique identity and have it claim its own task: `job claim <id> --as <agent-name>`
- Review before integrating, and verify load-bearing claims independently rather than accepting a report. Subagent claims and numbers can be subtly wrong.
- Commit and push after integration.

## Pre-completion Critique
Before you declare a task done, or a question answered, pause and critique your own work. Return to the context of the original user request—have you truly addressed their need? If you're producing code, do all linting and unit tests pass? If you're synthesizing or analyzing information, what are the gaps or weaknesses in your answer? What would a relevant and intelligent expert say about your work? If you identify serious flaws, keep working until you resolve them.

## Tidiness
Don't create temporary files in the root of the directory and leave them there. If you truly need a transient file, that's fine, but delete it when you're done. But if the artifact is something valuable (such as an agent's report, or a script), please save it in the correct directory.

## Git workflow
At moments when significant work has been completed and accepted by the user, offer to commit the changes for them. You may need to pull changes and resolve conflicts, which you should do for the user using git rebase whenever possible. If there is a real conflict, ask the user how they want to resolve it, explaining the situation clearly. Always commit all uncommitted files together, as your changes may depend on prior changes, and we don't want to commit the code in a half-working state. Do not amend previous commits.

## Development workflow

- A commit subject COMPLETES the sentence "This commit…". So a subject begins with a third-person singular verb — Adds, Records, Stops, Skips, Retires, Fixes, etc. Example: "Adds an email verification flow"
- PASS A COMMIT MESSAGE WITH `-F <file>`, NEVER INLINE WITH `-m`. Anything in an inline `-m` is interpreted by the shell first. The same hazard applies to `job note -m`, which this file already warns about; it applies identically to `git commit`.
- IMPORTANT: In this project we ALWAYS follow strict "red/green" TDD; write tests for all example cases we need to handle and any new methods we're implementing, verify that they *all* fail, and *then* proceed to implement your code changes. If a new test is green during the red stage, it's testing nothing and should be removed or refactored.
- If, during development, you must update an existing test to get it to pass, explain exactly WHY to the user and get their consent.
- Before fixing any bug, try to create a regression test to catch it in the future.
- **Real data can validate a wrong rule.** An over-general supersession rule passed against the entire live Biome stream — deleting nothing, purely because no interval happened to share an endpoint with a bare stop — and would have begun destroying records the first time a second source arrived. Constructed cases must sit BESIDE real-data checks; neither substitutes for the other.
- Prove a guard by **mutation**: remove it, show the specific breakage with a number, then put it back. "The COALESCE is needed" is an assertion; "dropping it inserts 386 duplicates on every harvest" is a measurement.

## Development Stage

- We are in BUILD mode. This tool is pre-launch, with zero users or existing files. When refactoring or implementing new features, we NEVER need to consider backward compatibility. We can assume that every use of the project is green-field. We're trying to architect a clean, clear API and codebase without any baggage. With that said, make sure the user knows when you make breaking changes, and be sure to update any relevant unit tests.
- In build mode, we are ambitious. Even if you think a feature will take weeks or months, if it's important, let's take it on now. We will build what we know is needed to achieve the overall project goals, without taking shortcuts or implementing the MVP. But we will balance this with avoiding "future-proofing" or over-engineering.

## Database Migrations

- Whenever you need to make a change to the database schema, you must create a migration in the [migrations](./internal/migrations/) folder, with an incremented leading number.
- DO NOT run this migration manually. There is an automatic migration system that runs when the server starts up, and it records the current migration number in the database. So instead of manually migrating the database, just start or restart the server.
- Read the FULL note history on the task (`job show <id>`) before writing schema, not just what `job orient` prints. A settled decision was missed exactly this way, and schema is the most expensive thing in this repo to change.
- **A SQLite `CHECK` constraint passes when it evaluates to NULL.** `CHECK (a = b)` therefore admits any row where either side is NULL. Every comparison in a CHECK needs its own `IS NOT NULL` guard, or it silently enforces nothing. This shipped once and was caught by a review agent, not by tests.
- **SQLite treats NULLs as DISTINCT in a UNIQUE index.** A nullable column in a dedup key means duplicate rows insert freely, for ever. Wrap nullable key columns in `COALESCE(col, '')` in the index expression.

## Documentation practice

- Findings and measurements go in `project/private/` as a dated document. Correct an earlier document **in place**, as a marked block quote, rather than silently editing the number or leaving the stale claim standing. Several documents there now correct each other and that trail is the point.
- Every figure recorded in `project/private/` must name the tool and flags that reproduce it.
- **`project/private/` is gitignored and does not ship.** Write findings there freely and name real clients, directories and hours — that is what it is for, and a measurement pseudonymised until it cannot be reproduced is worse than one that is simply not published.
- **What ships must stand on its own.** `docs/content/docs/`, `README.md` and the code comments are the published record, and a public reader cannot follow a citation into `project/private/`. A comment that says "see the memo" and nothing else has therefore said nothing. Carry the reasoning, not just the pointer.
- **Nothing that ships may carry a real client name, rate, contract figure or developer identifier** — that includes docs, code comments, test names and fixtures. `go run ./scripts/confidential -names <names> -gate .` is the check. Note what it exists to catch: the live store and the on-disk registry markers still hold REAL names, so pasting a live `review`, `metrics` or `orient` capture into a published page re-imports exactly what was removed. Three such leaks shipped this way and were caught three days late.
- **The task database is a third such source, and the only one that also holds money.** `.jobs.db` is gitignored and untracked, so it is not itself a publication risk — but 20 of 53 tasks name a real client and four carry rates or contract values, and `job show <id>` is the first thing any agent runs. Quoting a task description into a document, a commit message, a test name or a code comment re-imports both. Pseudonymise on the way out, exactly as you would a `metrics` capture.

## Golang-specific

- Prior to commiting changes, you MUST run `go fix` and `gofmt` to correctly format your code, and you must run the tests related to any features you added or changed. `go fix`, `gofmt` and unit tests with all be run as pre-commit hooks, so this will allow you to catch problems before attempting a commit.
