---
title: "Claude Code transcripts"
weight: 33
description: "Reading Claude Code's own session history, folding in subagents, and telling a human turn from an unattended one."
---

# Claude Code transcripts

Claude Code writes its own session history as JSONL, one file per session,
under:

```
~/.claude/projects/<dash-encoded-cwd>/<sessionId>.jsonl
```

`internal/transcripts` reads it as `claude-transcripts`, registers itself the
same way every Go-native source does, and needs no configuration: a harvest
collects from it along with everything else.

A full parse of this machine's own corpus — 4,221 files, 3.3 GB — completed in
**6.2s**. There is no watermark and no incremental mode: every file is read on
every call, because reading everything is cheap enough that the parent design
document for this work states it "removes any need for incremental ingest,"
and a watermark is one more thing that can fall out of step with a corpus that
gets rewritten from underneath it (see below).

## Every record becomes a point, never an interval

A transcript record is an instant — a message was sent, a tool ran — with
nothing in the format that pairs two records into a span the way Biome pairs a
focus-start with a focus-stop. So every event this source emits is a `point`,
timestamped at the record's own `timestamp` field. Deciding which points
belong to the same working session is downstream inference's job, not this
source's — the same separation of concerns every source in this project
follows.

## Subagent files are not optional

A session that dispatches work through the Task tool writes a second file
beside the main one:

```
<sessionId>/subagents/<agentId>.jsonl
```

Walking only the top-level `*.jsonl` files misses these entirely. On this
machine's corpus that is 229,068 of 790,950 timed records — 29% — silently
gone rather than merely under-counted, because most sessions that use
subagents at all use several.

The JSON carries an `isSidechain` field that looks like it should say "this is
a subagent record." It does not: measured across the whole corpus, it is
`false` on every one of 494,465 top-level records and `true` on every one of
229,068 subagent records — a perfect mirror of the path, and therefore useless
as an independent signal. **The only thing this source trusts for
subagent-ness is the directory structure**: a `*.jsonl` file whose immediate
parent directory is named `subagents`.

Folding a subagent's records in means treating them exactly like a
main-session record — the same source ID, the same per-record `cwd`
attribution, nothing keyed on the subagent's own `agentId` — so that a
subagent's work reads as part of its parent project's stream rather than an
island of its own. A prior spike measured that folding subagent events in adds
only 0.4% wall-clock time to engaged-time totals despite being 29% of the
events, which confirms subagent work is genuinely concurrent with its parent
rather than sequential — exactly what a stream of zero-duration points,
clustered downstream, is supposed to capture correctly on its own.

## cwd, never the directory name

The project directory under `~/.claude/projects` encodes a working directory
by replacing `/` with `-`, and decoding that back is lossy wherever a real
path component contains a dash of its own — a project named `dark-crystal-v2`
is, by directory name alone, indistinguishable from a nested path
`dark/crystal/v2`. Every record carries its own `cwd` field regardless, so
this source never inspects the directory name for anything but finding the
files beneath it. Every event's `Locator` is the record's own `cwd`,
unconditionally.

A subagent's `cwd` is not always identical to its parent session's — on this
corpus, 26% of subagent records carry a `cwd` one or more path segments below
the parent's, typically a subagent working in a repository subdirectory the
parent session sat above. This needs no special handling: `registry.Resolve`
climbs from whatever directory it is given up to the nearest blessed marker,
so a deeper `cwd` still resolves to the same project correctly on its own.

## Human turns, and why the "user" role is not enough

The Anthropic Messages API represents a tool's result as a **`user`-role**
turn fed back to the model — a wire-protocol convention, not a person at a
keyboard — and in practice it dominates the `user` type: a 40-transcript,
3,510-record sample found 3,229 of them were exactly this. Treating every
`user` record as a human turn would overwhelmingly measure tool traffic, not
attention.

A record counts as a **human turn** — `Event.State` is `"human"` — only when
all of the following hold:

- its `type` is `"user"`,
- its message content is a plain string, or a block list containing no
  `tool_result` block (a block list that mixes a `tool_result` with a
  trailing text block, such as the "Tool loaded." message written when a
  skill or tool auto-loads, is still disqualified — the presence of *any*
  `tool_result` block means the harness produced the turn), and
- the record is not inside a `subagents/` file.

That last condition matters on its own: a subagent's very first record is
also a plain-string `user` message, shaped identically to a real human
prompt — but nothing types it. It is the orchestrating agent's own dispatch,
running unattended, and the path is the only signal that tells the two apart.

Everything that is not a human turn — `assistant`, `progress`, `system`,
`attachment` records, and every subagent record — is `Event.State =
"machine"`.

This lives on `Event.State`, not `Event.Cause`. `Cause` says what wrote an
**artifact** — a file saved to disk — and a transcript record is not one.
`State` is documented as the scalar reading a source takes where it takes
one, the same shape `/display/isBacklit` uses for `on`/`off`, and "was a
human or the machine that produced this specific record" is exactly that
kind of reading, taken once per record.

## A record can genuinely appear twice, and disagree with itself

Two of Claude Code's own internal mechanisms work by giving an internal
subagent a **replay** of the preceding conversation as context before it does
its own work:

- **Compaction** (`agent-acompact-*.jsonl`) replays the session so far ahead
  of writing a summary.
- **Side questions** (`agent-aside_question-*.jsonl`) replay the session so
  far so a lightweight agent can answer a question asked mid-task.

The replayed records carry the *same* `uuid` and the *same* `timestamp` as
the originals — genuinely the same record, read a second time. A human turn
caught up in a replay sits inside a `subagents/` file and would be classified
`machine` by the rule above, even though the very same `uuid` is correctly
`human` where it was first written. This is not an edge case: measured
against this machine's own corpus, 6,822 of 6,922 `uuid`s that appear more
than once trace to exactly this.

`Collect` resolves it before returning: events are deduplicated by
`(Identity.Document, Start)`, keeping the `human` reading wherever one of the
candidates has it. A human-shaped observation confirmed anywhere is real
signal; a `machine`-state copy of it is a replay artifact, never evidence the
human turn did not happen. This is conflict resolution between two readings
of one already-identified record — it does not decide whether two *different*
records belong to the same session, which stays out of scope the same way it
does for every other source.

## Identity.Document is the record's uuid

There is no second source describing the same transcript record to join
against, the way a Motion autosave joins a Biome focus interval by `app` — so
`Identity.App` and `Identity.Project` are left empty; resolution fills
`project` later, the same as every other source.

`Identity.Document` is set to the record's own `uuid` rather than left empty,
for a concrete reason: the store's dedup key is `(source, shape, started_at,
ended_at, app, document, state)`, `locator` is deliberately excluded from it,
and 3.3% of records in this corpus share their file's exact millisecond
timestamp with another record. Two of those with the same `state` and no
`document` to tell them apart would silently collapse into one stored event.
Every genuinely timed record in this format already carries a `uuid`, so
`Document` is set to it — not because a transcript record is a "document" in
any real sense, but because it is the field that keeps distinct observations
distinct.

## What is skipped, and why that is not data loss

A record becomes an event only if it carries **both** a `timestamp` and a
`uuid`. Bookkeeping types this format writes — `last-prompt`, `mode`,
`ai-title`, `queue-operation`, `file-history-snapshot`, and similar — mostly
carry neither, or carry a timestamp with no `uuid` to make them dedup-safe.
These are not evidence that work happened; they are the application's own
internal state-tracking, and excluding them is a decision about what counts
as an event, not a parsing failure. A record present but malformed JSON (a
torn final line in a file still being appended to, the ordinary way that
happens) is skipped the same way, without losing the rest of the file.

A `cwd` that is missing or not absolute leaves `Locator` empty rather than
dropping the event — the record is still a real observation, just one
resolution cannot yet attribute to a project.

## A root that is itself a symlink

Keeping `~/.claude` in a dotfiles repository and symlinking it into place is
a common setup, and it used to collect **nothing** — silently. Measured on
2026-08-01: 722,463 events through `~/.claude/projects`, 0 through a symlink
pointing at exactly the same directory, with no error and exit status 0. The
cause is that `filepath.WalkDir` calls `os.Lstat` on its own root argument
and does not follow a symlink there, so the walk was handed a non-directory
and never descended, while the `os.Stat` check just above it followed the
link and reported the root perfectly healthy.

Both routes now yield the same corpus. The equivalent measurement after the
fix, taken in a single run so the growing corpus cannot skew it: 724,156
events by the real path, 724,160 through the symlink — four records appended
by a live session between the two collections.

Unlike `fs-observations`, nothing here has to be mapped back onto the path
you configured, because nothing derived from the walked path ever becomes a
`Locator`: every `Locator` is the record's own `cwd`. The walked path decides
one thing only, whether a file sits in a `subagents/` directory, and that
reads the immediate parent's name.

## Failure handling

A missing `~/.claude/projects` is not an error — it is what a machine that
has never run Claude Code looks like, and `Collect` reports zero events. It
does now say so, though, because "there is nothing here to read" and "there
was nothing to read" are different answers and a report cannot tell them
apart afterwards:

```
WARN no transcripts collected; this root does not exist, or is a symlink to
     something that no longer does  root=/…/projects
WARN no transcripts collected; this root is not a directory  root=/…/s1.jsonl
WARN no transcripts collected; this root is a symlink that could not be
     resolved  root=/…/alias  error=…
```

A corpus that exists and holds no conversations yet collects nothing and
stays **silent**, because that zero is correct.

A single file that cannot be opened (permission denied, deleted mid-walk)
costs only its own events; thousands of good files losing everything over one
bad one would be a worse failure than the one this project's sources exist to
avoid. The root directory itself refusing to be read is different and is
surfaced as a real error, because there is no "the rest of the corpus is
still fine" to fall back on when the walk cannot even start.

See `internal/transcripts/realdata_test.go` for the tests that generate the
numbers on this page — run `go test ./internal/transcripts/...` to reproduce
them against your own corpus. Those tests skip cleanly on a machine with no
`~/.claude/projects`, which is most machines other than the one this was
built on. `internal/transcripts/realcorpus_test.go` and its
`testdata/realcorpus` fixture carry a small, hand-anonymized corpus derived
from the *shape* of two real `agent-aside_question-*.jsonl` files — never
their content — so subagent folding, replay-dedup (including a side-question
reply replayed partway through its file, not only at the top, which no
previous test exercised), and human/machine classification all run the same
way in CI or on a fresh checkout as they do here.
