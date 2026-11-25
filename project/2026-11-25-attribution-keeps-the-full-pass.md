# Attribution keeps the full pass

2026-11-25. Closes the decision left open by
[the attribution speed plan](2026-11-25-attribution-speed.md).

## The ruling

Every harvest keeps resolving every stored locator. Attribution does not become
incremental. Ben ruled this on 2026-11-25.

## The figure it rests on

With one filesystem answer per directory, a pass over a copy of a real store
(384,575 distinct locators) took **27 s in the foreground and 82 s at
background priority**, against an hourly harvest schedule. Listing locators and
database updates are 63 of the 82 s. These are single runs on a busy machine,
and background priority was imitated with `taskpolicy -b` rather than measured
under the launchd agent. The commands and the full table are in
[where the attribution pass spends its time](2026-11-25-attribution-timing.md)
(`go run ./scripts/attributionprofile`).

## Why not incremental

Handling only new locators, and redoing everything when the registry changes,
would be faster and could go stale silently: a moved directory, a hand-edited
marker or a new git worktree changes answers without changing the registry
index.
