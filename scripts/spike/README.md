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
| `zoomproto` | The web dashboard's zoom timeline, as an interaction rig. Not a Go probe and not read-only — see below. |

### `zoomproto` is the exception to every convention above

It is HTML rather than a `main` package, it reads nothing at all, and it is not
throwaway: it is the design artifact the semantic-zoom timeline is meant to be
tuned in, so its findings get re-implemented rather than discarded.

- **Open it**: double-click `scripts/spike/zoomproto/index.html`, or
  `open scripts/spike/zoomproto/index.html`. No server, no build, no network —
  a strictly offline single file, which is also why it is one file and not a
  set of ES modules (`file://` blocks module loading outright).
- **All data in it is synthetic and seeded.** Five invented client names, 430
  generated days. It cannot reach the store, and nothing in it may be replaced
  with real data — a screen capture of it is safe to publish, which is the
  point.
- **Check it** with `node scripts/spike/zoomproto/check.js`. That runs the
  page's own generator, kernel and colour ramp headlessly and asserts the
  properties the design requires; it slices the functions out of the HTML
  rather than re-implementing them.

## Permissions

Two different barriers, which `env` reports as `denied` in both cases — never as `missing`, because a naive existence check would report a populated 37 MB database as absent.

**`knowledgeC.db` — Full Disk Access.** Granted to whatever binary opens it, which for `go run` is your terminal: System Settings → Privacy & Security → Full Disk Access, add the terminal, restart it. Agent shell sessions are additionally sandboxed out of most of `~/Library`, so a denial there may be the sandbox rather than TCC.

**`.DocumentRevisions-V100` — root, and FDA does not help.** The store is `d--x--x--x root:wheel`: traversable but unreadable, and its `db-V1/db.sqlite` index refuses even to `stat` under a normal user. SIP is enabled. The supported way in is `NSFileVersion`, which needs no elevation and is verified working (see `revisions/`) — though on this machine it returns nothing, because neither Motion nor Photoshop creates document versions.
