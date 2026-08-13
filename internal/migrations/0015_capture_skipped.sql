-- 0015: a capture that was DECLINED by policy is a fifth outcome, and it now
-- has a name.
--
-- 0007 closed capture_run's status at four terminal values and argued each one.
-- A fifth is added here because the harvest gained a decision it did not have
-- then: the knowledgeC snapshot is throttled independently of the harvest
-- cadence, so a run can now finish having deliberately not preserved
-- knowledgeC.
--
-- # Why the harvest and the snapshot no longer share a cadence
--
-- They protect two different kinds of loss and only one of them is helped by
-- frequency.
--
-- The mtime sources DECAY. A file records only its last modification, so work
-- done today on a file last touched three weeks ago does not add evidence for
-- that day — it MOVES it, deleting the old day and crediting today. Measured:
-- one repository scan found 1,773 files bearing a given day and the same scan
-- hours later found 513. No later harvest recovers the difference, so for those
-- sources frequency IS the protection, and the harvest wants to run hourly.
--
-- knowledgeC PRUNES. Its binding floor is /app/usage, capped near 10,000 rows,
-- and everything still inside that window comes back whole whenever the next
-- harvest runs. A four-hourly schedule already puts about sixty captures inside
-- it. So hourly buys knowledgeC essentially nothing — and costs the most of any
-- capture in the harvest, because a snapshot is about 1.5 MB written afresh
-- whenever the extracted rows differ at all. Measured 2026-08-12 with
-- `go run ./scripts/snapchurn -cadence 1h` and `-cadence 4h`, after write-time
-- deduplication landed: 9.85 GB/yr at hourly against 3.23 GB/yr at four-hourly.
-- Both are upper bounds on what dedup saves, because a row can also age out of
-- retention inside an otherwise quiet window.
--
-- So the harvest runs hourly and the snapshot every four hours, and three runs
-- in four now end with knowledgeC deliberately unpreserved.
--
-- # Why that is not any of the four values already here
--
--   ok       says the capture read its source through. This one did not read it
--            at all.
--   partial  and failed say something was LOST, and both are required to carry
--            a reason. Nothing was lost and there is no reason to give.
--   absent   says we looked and there was nothing there — no Motion installed,
--            no filedays roots configured. Here the source is present and
--            healthy and we chose not to read it yet. Recording that as absent
--            would be a machine reporting knowledgeC is not installed.
--
-- The nearest miss is the fifth reading that is NOT a status at all: files_new =
-- 0 with files_held = 1, which 0007 already argues for and which means the run
-- read the source through, extracted it, digested it, and found a snapshot
-- already on disk holding those exact bytes. That is an OBSERVATION about the
-- source. A policy skip is a decision about the schedule, taken before anything
-- was read. Collapsing the two would make "knowledgeC has not changed in four
-- hours" and "we have not looked in four hours" render alike — which is the
-- absence-is-not-an-observation rule in CLAUDE.md, and a collapse this project
-- has shipped three times in three separate tools.
--
-- # What a skipped row may say
--
-- Nothing was read, so nothing was written, held or measured: files_new,
-- files_held and bytes are all forced to 0. Without that CHECK the fifth status
-- could be stamped over a row that really did preserve something, and every file
-- and byte total in the store would silently disagree with the directory. And
-- error stays NULL, on the same footing as ok and absent: a skip lost nothing,
-- and an error here would turn "the throttle said not yet" into "knowledgeC
-- could not be read", which are opposite readings calling for opposite actions.
--
-- What a skipped row DOES carry is started_at, finished_at, kind and input —
-- the same span and the same subject every other capture row carries. That is
-- the whole point: `timetattle status` can see that the capture phase ran, on
-- time, and made a decision, rather than seeing nothing and being unable to tell
-- a throttled harvest from a dead one.
--
-- # Why a rebuild
--
-- SQLite has no ALTER TABLE ADD CONSTRAINT and no way to widen an existing
-- CHECK, so a change to the status set means create, copy, drop, rename, and
-- restore the index. 0009 sets the precedent and its comment carries the full
-- argument; the property that decides it is that a CHECK is applied to the rows
-- ALREADY THERE, because every one passes through it on the way into the new
-- table.
--
-- The ordering caution 0009 needed does not apply here. PRAGMA foreign_keys is
-- a no-op inside a transaction and every migration runs in one, so foreign keys
-- cannot be turned off — but capture_run references nothing and nothing
-- references it, by the deliberate design 0007 argues for (capture rows are not
-- provenance and no event can ever point at one). There is therefore no cascade
-- to fire and no order to get wrong. The cost is proportionate for the same
-- reason: capture_run holds one row per source per harvest, not one per event.

CREATE TABLE capture_run_new (
    id          INTEGER PRIMARY KEY,

    kind        TEXT    NOT NULL CHECK (kind <> ''),
    input       TEXT    NOT NULL CHECK (input <> ''),

    started_at  TEXT    NOT NULL,
    finished_at TEXT    NOT NULL,

    -- The fifth value is the whole of this migration. See above for why it is
    -- not ok, not absent, and not files_new = 0 with files_held = 1.
    status      TEXT    NOT NULL CHECK (status IN ('ok', 'partial', 'failed', 'absent', 'skipped')),
    error       TEXT,

    files_new   INTEGER NOT NULL DEFAULT 0 CHECK (files_new  >= 0),
    files_held  INTEGER NOT NULL DEFAULT 0 CHECK (files_held >= 0),
    bytes       INTEGER NOT NULL DEFAULT 0 CHECK (bytes      >= 0),

    -- Every CHECK below is carried over from 0007 unchanged except the two that
    -- name the status values, and its argument is recorded there. They are
    -- restated rather than referenced because a rebuild that dropped one would
    -- otherwise leave no trace of what it dropped.
    CHECK (finished_at >= started_at),

    CHECK (started_at  GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (finished_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    CHECK (status NOT IN ('failed', 'partial') OR error IS NOT NULL),
    -- 'skipped' joins ok and absent here: three ways of having lost nothing, and
    -- an error on any of them would be a row arguing with itself.
    CHECK (status NOT IN ('ok', 'absent', 'skipped') OR error IS NULL),

    -- New in 0015. A run that declined to read the source cannot have preserved
    -- anything from it. status is NOT NULL and all three counters are NOT NULL,
    -- so no side of this can evaluate to NULL and pass by accident — the trap
    -- CLAUDE.md names and 0001 shipped.
    CHECK (status <> 'skipped' OR (files_new = 0 AND files_held = 0 AND bytes = 0))
);

INSERT INTO capture_run_new
    (id, kind, input, started_at, finished_at, status, error, files_new, files_held, bytes)
SELECT id, kind, input, started_at, finished_at, status, error, files_new, files_held, bytes
FROM capture_run;

DROP TABLE capture_run;
ALTER TABLE capture_run_new RENAME TO capture_run;

-- The index goes with the table and has to be recreated by name. Same
-- definition as 0007: the read this exists for is "the most recent capture of
-- each kind", a descending scan of one kind's rows, and there is deliberately
-- no UNIQUE index because every capture attempt is its own row for ever.
CREATE INDEX capture_run_kind ON capture_run (kind, started_at);
