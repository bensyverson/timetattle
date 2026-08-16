# Gotchas

Project-specific traps that cost real time and that no general rule predicts. Read at session start; append when you hit one.

- **Fix at the source when you can.** A gotcha is a bug report on our tooling, not a permanent fact — if it can be fixed in code, file it in `job` and fix it instead of recording it here.
- **Delete anything that becomes obvious, gets fixed, or stops recurring.** Keep this list short; a long list is one nobody reads.
- **Feedback about `AGENTS.md` itself** — a rule that was wrong, misread, or cost time — goes here too, prefixed `rule:`. It is harvested when the shared rules are reviewed.

Format: one dated H2 per entry, a bold headline, then what happened and what to do instead.

---

## 2026-07-30 — a bare `go install` silently revokes Full Disk Access; always `make install`

**macOS keys Full Disk Access to the binary's code signature, Go links ad-hoc, and a bare `go install` therefore silently revokes the FDA grant while System Settings still shows it enabled.** That cost this project 44 hours of capture on 2026-07-30 and 33 hours three days earlier — the harvester kept running and kept reporting success while reading nothing. `make install` is `go install` followed by `scripts/sign.sh`, which re-signs with the stable identity so the grant keyed to that identity survives every rebuild; `make agent` does the same and re-schedules the launchd agent. Never install this binary any other way, and if capture volume drops for no visible reason, suspect the signature before the code.

## 2026-08-16 — `scripts/confidential` is only as good as the list it is handed

**Twice the gate reported clean, or reported a phantom, because of the list it was given rather than the tree it scanned.** Its name list went unchecked for five days while registered directory names sat in the tracked tree; and its deny list, handed a placeholder string copied out of an example command, produced a finding against the very redaction that placeholder documents. Before trusting a verdict from it, check where the list came from — `go run ./scripts/confidential -classify` prints what this machine actually knows — and read what the output says produced the verdict.

## 2026-08-16 — `scripts/sleepcheck` clips to the span two sources share

**It reported "1.88h of gaps, largest 1h27m" while excluding a 6h23m gap that ended 26 seconds before its window opened.** The clipping is deliberate (a snapshot's presence stops when it was taken, Biome runs to now), and the tool now prints the window and the whole-stream counts beside the in-window ones — but every derived figure it prints, gaps included, is still window-scoped. Say which population you are quoting, and measure the unwindowed one once before generalising.

## 2026-08-16 — "absence of a row" has shipped twice here, in three separate tools

**The general rule is in `project/agents/evidence.md`; the local fact is that this project has already shipped the bug twice, across three tools.** Treating "nothing was recorded" as "observed not-X" is not a hypothetical failure mode in this codebase — it is the one that keeps recurring, because almost every source here is silent rather than negative when nothing happened.

## 2026-08-16 — the NULL `CHECK` shipped once, and a review agent caught it, not the tests

**A SQLite `CHECK` that evaluates to NULL passes, so a `CHECK (a = b)` we shipped admitted every row where either side was NULL.** The rule is in the Go section; the part worth remembering here is that the test suite did not catch it and could not have — a constraint that enforces nothing looks exactly like a constraint that is satisfied. Guard every comparison with `IS NOT NULL`, and verify a new constraint by inserting a row it should reject.
