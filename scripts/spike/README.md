# Spike probes

Throwaway discovery programs backing [the evidence source spike](../../project/private/2026-07-28-evidence-source-spike.md). Each subdirectory is a self-contained `main` package with no shared abstractions — deliberately, since the point of the spike is to *inform* the v1 architecture rather than pre-commit to one.

They live in `scripts/` rather than being written inline and discarded because several are worth re-running: `knowledgeC` prunes on a rolling window, so re-checking retention depth periodically is a real need.

## Conventions

- Run with `go run ./scripts/spike/<name>`.
- **Read-only.** Probes copy a database to scratch before reading it; nothing opens a live system store in place, and nothing writes to `.DocumentRevisions-V100`.
- **SQLite via `modernc.org/sqlite`**, matching `jobs`, `birch` and `cypress`. Pure Go, no cgo, driver name `sqlite`. Read-only access uses SQLite's own `mode=ro`; snapshots of live system databases additionally want `immutable=1`.
- These programs are exempt from the project's red/green TDD rule. Their output is knowledge, not behaviour. The v1 build that follows is not exempt.

## Probes

| Probe | Purpose |
| --- | --- |
| `env` | Pre-flight check: which evidence stores exist, and which are merely unreadable. Distinguishes absence from a TCC refusal, which macOS otherwise conflates. |

## Permissions

Two different barriers, which `env` reports as `denied` in both cases — never as `missing`, because a naive existence check would report a populated 37 MB database as absent.

**`knowledgeC.db` — Full Disk Access.** Granted to whatever binary opens it, which for `go run` is your terminal: System Settings → Privacy & Security → Full Disk Access, add the terminal, restart it. Agent shell sessions are additionally sandboxed out of most of `~/Library`, so a denial there may be the sandbox rather than TCC.

**`.DocumentRevisions-V100` — root, and FDA does not help.** The store is `d--x--x--x root:wheel`: traversable but unreadable, and its `db-V1/db.sqlite` index refuses even to `stat` under a normal user. SIP is enabled. The supported way in is `NSFileVersion`, which needs no elevation and is verified working (see `revisions/`) — though on this machine it returns nothing, because neither Motion nor Photoshop creates document versions.
