-- 0002: what caused an artifact to exist.
--
-- The schema already recorded how a source is read and what its events mean. It
-- did not record what WROTE the file, and that turns out to decide what the
-- evidence is worth. Measured 2026-07-29 (project/private/2026-07-29-join-validation.md
-- §4): Blender .blend1 backups landed inside their own application's focus
-- intervals 4 of 4, Motion autosaves 9 of 15 — same source kind, same shape,
-- same reader, same join. A .blend1 is written by an explicit save, so somebody
-- was at the keyboard. A Motion autosave is written by a wall-clock timer, and
-- the 2026-07-11 17:53 one was written with the screen off during a confirmed
-- break.
--
-- Three values, and the set is closed:
--
--   user-action  written inside the application's interaction loop by something
--                the person did. The strongest presence signal an artifact
--                carries, and still not proof — a script can press the same key.
--   timer        written on a schedule nobody touches. Asserts that the
--                application was running and nothing about presence.
--   recovery     written because something went wrong: a crash, an abnormal
--                exit, a relaunch offering to restore. Nothing emits one yet.
--                It is its own value rather than folded into the others because
--                its weight is UNMEASURED, and because the manifest field is
--                required: an author describing Photoshop's AutoRecover folder
--                with only the other two available would have to declare
--                something false, on a disk we cannot migrate.

-- NULL means "does not apply", not "nobody said". A Biome focus interval and a
-- knowledgeC presence row are not artifacts and nothing caused them in the sense
-- a manifest can declare; the manifest schema requires the field of every source
-- that CAN declare one, so an undeclared cause never reaches this column.
--
-- The IS NULL guard is written out rather than left implicit. A CHECK passes when
-- it evaluates to NULL, so `cause IN (...)` alone would admit NULL by accident —
-- which is the behaviour wanted here, and that is exactly why it is spelled: the
-- next person must not have to work out whether the NULL case was intended or
-- overlooked. Migration 0001 was bitten by the same rule in the other direction.
--
-- Measured by removing it: a row with cause 'human' inserts silently, and
-- inference would count it as whatever its default arm assumed.
--
-- Deliberately NOT constrained against shape or source_id. Only points carry a
-- cause today, but a future command source could emit an interval that a person
-- caused, and encoding today's coincidence in the most expensive place to change
-- it would buy nothing. Extending the value list later needs a table rebuild,
-- since SQLite cannot alter a CHECK — so the three above name mechanisms rather
-- than the two applications that ship.
ALTER TABLE event ADD COLUMN cause TEXT
    CHECK (cause IS NULL OR cause IN ('user-action', 'timer', 'recovery'));

-- cause is NOT added to event_identity, and that is a decision rather than an
-- omission.
--
-- It is a standing declaration about a source, not a reading taken per
-- observation — which is what separates it from state, which IS in the key
-- because knowledgeC writes two contradictory readings with identical endpoints.
-- source_id therefore already carries the cause, so adding it would widen every
-- row of the index for nothing.
--
-- And it would do harm. A cause is a hand-written claim in a manifest, so it can
-- be CORRECTED. In the key, the first re-harvest after a correction inserts a
-- second row for every file already recorded, and each one looks like a genuine
-- second observation of the same instant. Out of the key, dedup holds and the
-- correction reaches new rows only — an honest consequence of an append-only
-- store, since supersession is a derivation and events are never rewritten.

-- canonical_event names its columns, so a column added to the table does not
-- reach the view on its own, and Events reads the view rather than the table.
-- Dropping and recreating it is the whole reason this migration is more than one
-- ALTER: without it, cause is stored and then invisible to every ordinary read.
-- Measured by removing this: Store.Events fails outright with "no such column:
-- cause". The body below is 0001's, unchanged except for the added column.
DROP VIEW canonical_event;

CREATE VIEW canonical_event AS
SELECT id, first_seen_run, source_id, shape, unknown, started_at, ended_at,
       app, document, state, locator, project, payload, cause
FROM event AS e
WHERE NOT (
    e.shape = 'unbounded'
    AND COALESCE(e.unknown, '') = 'horizon'
    AND EXISTS (
        SELECT 1 FROM event AS complete
        WHERE complete.shape = 'interval'
          AND complete.source_id = e.source_id
          AND complete.started_at = e.started_at
          AND COALESCE(complete.app, '') = COALESCE(e.app, '')
          AND COALESCE(complete.document, '') = COALESCE(e.document, '')
          AND COALESCE(complete.state, '') = COALESCE(e.state, '')
    )
);
