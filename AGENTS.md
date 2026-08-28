IMPORTANT: the documentation is the Hugo site under [docs/content/docs/](docs/content/docs/) — that is what "the docs" means everywhere below.

## Overview

You are working on Timetattle, a Go CLI that reconstructs *when* work happened, and *for whom*, from the traces work leaves behind — Claude Code transcripts, git history, task databases, file timestamps and macOS presence data. It produces **insight, not a system of record**: nothing it reports is a timesheet, and what was observed, what was inferred, and what nobody recorded stay separable all the way to the report.

## Documentation

- [README.md](README.md) — what the tool is, why it exists, and the quick start
- [docs/content/docs/](docs/content/docs/) — the published documentation (Hugo); [confidentiality.md](docs/content/docs/confidentiality.md) explains the publication gate
- [project/](project/) — dated design documents, plans and measurement findings; this trail is published and is part of the repository's record
- [project/backlog.md](project/backlog.md) — work deliberately deferred, one dated H2 per item
- [project/gotchas.md](project/gotchas.md) — project traps and rule feedback; read it at session start
- [project/agents/](project/agents/) — the long-form delegation and evidence rules the sections below point at
- `project/private/` — historical design documents and findings whose subject is the client surface. NOT TRACKED: it is gitignored, stays on this machine and in the private mirror, and is the one part of this repository that does not ship. It measures one person's real working days against real clients, so it names them throughout.

## Confidentiality

Findings go in `project/` as dated documents. A document goes in `project/private/` instead only when its **subject is the client surface itself** (real names, rates, hours against a named client, or the confidentiality pass — publishing such a document is publishing a map of what was removed; see the note in `.gitignore`).

- **`project/private/` is gitignored and does not ship.** Write findings there freely and name real clients, directories and hours — that is what it is for, and a measurement pseudonymised until it cannot be reproduced is worse than one that is simply not published.
- **What ships must stand on its own.** `docs/content/docs/`, `README.md` and the code comments are the published record, and a public reader cannot follow a citation into `project/private/`. A comment that says "see the memo" and nothing else has therefore said nothing. Carry the reasoning, not just the pointer.
- **Nothing that ships may carry a real client name, rate, contract figure or developer identifier** — that includes docs, code comments, test names and fixtures. `go run ./scripts/confidential -names <names> -gate .` is the check. Note what it exists to catch: the live store and the on-disk registry markers still hold REAL names, so pasting a live `review`, `metrics`, `orient` or `dashboard` capture into a published page re-imports exactly what was removed — and `dashboard` is the one that also carries a rate or a contract total, since its Allocation & Effective Rate section reads the config's `engagements` money. Three such leaks shipped this way and were caught three days late.
- **The task database is a third such source, and the only one that also holds money.** `.jobs.db` is gitignored and untracked, so it is not itself a publication risk — but 20 of 53 tasks name a real client and four carry rates or contract values, and `job show <id>` is the first thing any agent runs. Quoting a task description into a document, a commit message, a test name or a code comment re-imports both. Pseudonymise on the way out, exactly as you would a `metrics` capture.

## Build, install and checks

- **Install with `make install`, never a bare `go install`.** `make install` is `go install` *plus* re-signing, and the signing is not optional dressing — a bare `go install` silently revokes this machine's Full Disk Access grant. See the Makefile comment and the 2026-07-30 entry in [project/gotchas.md](project/gotchas.md). `make agent` additionally (re)schedules the four-hourly harvest launchd agent.
- **Schema migrations are numbered files in [internal/migrations/](internal/migrations/).**
- **The pre-commit hook runs the confidentiality gate first.** It is `scripts/git-hooks/pre-commit`, wired in via `core.hooksPath`, and it runs the gate before `go vet`, a `scripts/` layout check, `go fix`, `gofmt`, `go mod tidy`, `go test ./...` and `go build`. The hook asks whether *this commit* adds anything unpublishable; `make gate` asks whether the repository is publishable *as it stands*, across the whole tree and every object in `.git`, and is what to run before pushing to a public mirror.

<!-- agents:begin core@3a7a5e -->
## Working rules

**Understand the why.** If the goal behind a request isn't clear, ask before solving — beware the XY problem.

**Diverge, then converge.** First brainstorm options (create choices), weigh them against the user's goals, recommend one (make choices), confirm, then execute.

**Ambiguity.** If the *code* could go several ways, choose the idiomatic one for the language. If the *requirement* is ambiguous or the question is architectural, stop and ask — don't decide.

**Dependencies.** Avoid them unless re-implementing would be unreasonable; ask before adding one; each is security and maintenance surface.

**TDD, strictly red/green.** Write tests for every case and every new method first, watch them *all* fail, then implement. A test that is green during red tests nothing — remove or rewrite it. If an existing test must change to pass because the behavior or expectation has changed, explain why clearly. Every bug fix starts with a regression test.

**Plans and tasks live in `job`.** Open every session with `job orient` (no arguments), then read `project/gotchas.md` — while reading, prune it: delete any entry that's now fixed, obvious, or a general rule, marking it `rule:` first if it's general. Don't use Plan Mode or ad-hoc todo lists.

**Don't tour the codebase.** Start from the README and the docs (an Explore agent is fine); dig only where the task leads — once you have a specific need, read as much as that need requires.

**Scripts.** Analysis tooling goes in `scripts/` so it can be re-run — check there before writing one.

**Critique before declaring done.** Re-read the original request: is the need actually met? Do lint and tests pass? Are docs updated? What would an expert flag? Fix serious flaws before reporting.

**Tidiness.** No stray files in the repo root; delete transients, and file valuable artifacts (reports, scripts) where they belong.

**Documentation.** Keep the project docs current as you build. Touch the README only when a doc file is added or new users must know.

**Gotchas.** When a project quirk costs you time and no rule predicts it, append it to `project/gotchas.md`. If a rule in this file was wrong or misled you, record that there too, prefixed `rule:`.

**Where these rules come from.** The marked regions are generated and shared across repos via a CLI tool named `agents`; don't edit inside them. If a rule here is wrong or cost you time, say so in `project/gotchas.md` prefixed `rule:`; that is how shared rules get reviewed.

**Local rulings.** A repo-local ruling, or an override of a shared rule, lives in the project-owned head of `AGENTS.md`, above the generated regions — say plainly that it overrides, and link a dated project doc for the why.

## Git

- Offer to commit when a unit of work is complete and accepted. Rebase onto upstream; ask on real conflicts, explaining the conflict in plain terms first.
- Commit all uncommitted files together — later changes usually depend on earlier ones, and a half-working state helps nobody. Never amend.
- The subject completes "This commit…": present-tense verb first — "Adds…", "Fixes…", "Retires…". Detail goes in the body.
- Pass the message with `-F <file>`, not inline `-m`; the shell interprets `-m` first. Same for `job`: `note`, `done`, `add` and `edit` all take `-F <file>` (`-F -` reads stdin).
- Pre-commit hooks run the formatter and tests. Run them yourself first (see the stack rules).
- Never pipe a gating command (`git commit … | tail`) — the pipe swallows its exit status, so a following `&&` runs even after a failure.
<!-- agents:end core -->

<!-- agents:begin principles@7a5b19 -->
## Principles

Defaults, not laws. When we break one, we do it consciously and say so in the report and the docs.

- **Pragmatism.** Builders, not purists. Practical choices that serve the near-term goal and protect the long-term one.
- **Eat the frog.** No band-aids. Given an easy-but-compromised path and a correct one, take the correct one; fix problems at the source. Keep YAGNI in mind, but when a need is obvious, don't underdeliver.
- **Composability.** Simple, strong components composed into systems — never a monolith.
- **Library + thin executable.** Core logic in a library; the app or CLI is a light consumer, so the core can be reused elsewhere. An adapter that holds a decision rather than wiring one is a bug.
- **Decoupling.** Tight coupling makes testing, debugging and refactoring hard — separate concerns. Separating a model, its storage and its UI is the everyday case: databases and UI frameworks change; today's web app may grow a CLI or mobile app.
- **Just enough abstraction.** One layer around an LLM provider is prudent; a `TextGenerationProvider` above it is not.
- **Readable file sizes.** Aim for files a reader can hold in their head (a few hundred lines; ~400 is the comfortable ceiling). Past ~2k lines, navigation degrades and errors accumulate; splitting also makes functionality discoverable by filename.
- **Comments say why, not what.** Doc comments state *what* concisely; other comments only explain the non-obvious. No change history in comments. Most code needs none.
- **Strongly typed.** Prefer enums, named constants and config over magic strings and numbers; prefer typed structs over dictionaries, even for wire types. Two packages exchanging data across a serialization seam share **one** struct that both import, never a hand-written twin on each side — the type checker cannot see across encode/decode, and two definitions drift. Given a bool and a typed constant, take the typed constant: a bool named for one consequence gets reused to gate the others until it means several things, so name the underlying *fact* as a type and let the behaviors follow.
- **Previews.** Give each UI component a way to render in its various states — a SwiftUI `#Preview`, a demo page, a story — the foundation for tests and for human review.
- **Async by default.** Keep the app interactive during heavy work; surface loading and error states. On the web, prefer progressive enhancement over full reloads.
- **Event streams where they fit.** Append-only logs are auditable, undoable, and time-travelable.
<!-- agents:end principles -->

<!-- agents:begin stage-build@3d5d83 -->
## Stage: BUILD

Pre-launch, zero users, no existing data. Never spend effort on backward compatibility — assume every use is green-field — but flag breaking changes and update the affected tests. Be ambitious: if a feature is important, build it fully now rather than an MVP; balance that against over-engineering and future-proofing.
<!-- agents:end stage-build -->

<!-- agents:begin go@91ab6a -->
## Go

- Before committing: `go fix ./...`, `gofmt -w .`, `go vet ./...` and `go mod tidy`, then the tests you touched. `go fix` converges over several passes — "re-run to apply more" is progress, not failure; re-run until clean before editing code.
- **Run `go fix ./...` before staging, not just before committing.** A pre-commit hook that re-stages `gofmt` rewrites will not re-stage `go fix` rewrites: a file `go fix` changed that is already gofmt-clean commits unfixed, and your working tree quietly diverges from what you committed.
- **Tests that share a database need `-p 1` and a database per agent.** `go test ./...` runs packages in parallel, so packages that seed the same fixtures and truncate the same tables produce a wall of unrelated-looking failures that survives a re-run and reads as a real regression.
- **Schema changes are numbered migrations** in the project's migrations directory (the head names it). Never run one by hand — the binary migrates when it starts or opens the database and records the version; the next run applies it. Read the full note history on the task (`job show <id>`) before writing schema; it is the most expensive thing to change.
- **On SQLite:** **`CHECK` passes on NULL.** `CHECK (a = b)` admits any row where either side is NULL — guard every comparison with `IS NOT NULL`, or it enforces nothing.
- **On SQLite:** **NULLs are distinct in a `UNIQUE` index.** A nullable column in a dedup key admits duplicates forever; wrap it in `COALESCE(col, '')` in the index expression.
- **On SQLite:** **Never hold a transaction open across a model or network call.** `BeginTx` is deferred — it pins a read snapshot at the first read, so the write at the end fails with `SQLITE_BUSY_SNAPSHOT` if any other connection committed meanwhile, and `busy_timeout` cannot rescue it because waiting cannot refresh a stale snapshot. Split into a step that reads and calls but writes nothing, and a short transaction that persists the result.
- Wire types are structs, not `map[string]any`, unless the shape is genuinely dynamic.
- **`r.ParseForm()` reads a body only when it is urlencoded**; for multipart it leaves it empty without erroring. Keep one wire format per route — a handler that accepts two body shapes needs two sets of checks where the design wanted one.
<!-- agents:end go -->

<!-- agents:begin docs@7ba2fd -->
## Documentation practice

- **Plans, findings, designs and decisions go in `project/` as dated documents** (`YYYY-MM-DD-title.md`) — the written history of the project. They are point-in-time records: correct an earlier one *in place*, as a marked block quote, rather than silently editing a number or leaving a stale claim standing.
- **Every figure names the tool and flags that reproduce it.**
- **Work decided against goes in `project/backlog.md`**, not into silence: one dated H2 per item — what it is, why it's parked, and *what would un-park it*. Nothing there is scheduled or blocking; active work lives in `job`. Check it before proposing something that sounds novel.
- **When a finding overturns a premise, edit the premise.** Readers act on the title and opening; a correction appended underneath doesn't reach them.
- **A wrong documented cause is worse than none** — it stops the next reader looking. Correcting one means saying it was wrong, not quietly rewording.
- **Open the note before you cite it**, and check whether a recorded ruling has been superseded before passing it on.
- **Every repo has a README; if none exists, write one (delegate it if you can).** It is tight: the project's name and a one-line description a non-technical reader understands (6th-grade reading level); one short paragraph of what it is; how to install or consume it; a crisp Quick Start with an example or two; links out to the specific docs for anything more; authorship and license at the end.
- **The head of `AGENTS.md` lists where the docs live; keep that list current.**
<!-- agents:end docs -->

<!-- agents:begin delegation-brief@7bb732 -->
## Delegating to subagents

Design on the main thread; dispatch execution to agents for anything larger than a small change. **Read `project/agents/delegation.md` before dispatching** — it carries what to delegate, how to carve the work, the worktree workflow, the traps, and the briefing template.

- Fanning out is a decision, not a default: map each leaf's file surface first, parallelize only the disjoint set, pre-carve or reserve a contended file to one writer, and serialize the rest.
- Commit before dispatching — a worktree branches from local HEAD, so uncommitted work is invisible to the agent; pushing is backup.
- Agents never commit: the integrator wip-commits the agent's branch with hooks off, merges it `--no-ff --no-commit`, reads the diff (that is the code review), runs the full suite once, then commits and closes the leaves.
- Choose the model deliberately, require **deviations from the brief** and **"what in this brief is wrong?"** in every report, and verify what comes back — the pushback, not the typing, is usually the value.
<!-- agents:end delegation-brief -->

<!-- agents:begin jobs-brief@42b137 -->
## Jobs

`job` is the tracker for plans and tasks. **Read `project/agents/jobs.md` before filing or claiming work** — it carries the shape of the tree, criteria and blockers, the identity rules for agents, and how big a leaf should be.

- Subagents pass a unique `--as <name>` and an absolute `--db` on every call; they `claim`, `note` and `release`, never `done`.
<!-- agents:end jobs-brief -->

<!-- agents:begin harness-brief@a03f30 -->
## Harness

The harness an agent runs inside has facts of its own — the Bash sandbox, `$TMPDIR`, no TTY, worktree isolation, background processes. **`project/agents/harness.md` carries them.** Read it the first time a tool call fails with a permission error or a "too complex to verify" refusal, and before writing a brief for a subagent.
<!-- agents:end harness-brief -->

<!-- agents:begin background@882e19 -->
## Background

**Weigh decisions against `project/background.md` where it exists** — who the work is for, the people involved, the constraints that come with them, what they want, and what they call things. It is context for judging a decision rather than for writing code: read it when a choice turns on who is on the other end, and use its vocabulary in what you write.

**It holds *current* state, so it is rewritten, not appended.** Every number, date and name appears there once and links the dated `project/` document it came from; when a fact changes, edit the sentence that holds it and let the dated record keep the history.
<!-- agents:end background -->
