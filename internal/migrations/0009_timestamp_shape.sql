-- 0009: the timestamp shape guard, applied to every table that predates it.
--
-- Every instant in this database is TEXT, and the whole schema rests on TEXT
-- order being time order. `CHECK (ended_at >= started_at)` on event, on
-- derived_session, on derived_chain, on capture_run and on adjustment; the
-- event_span, derived_session_span, derived_chain_span and adjustment_fold_order
-- indexes; every range read a report makes. All of them are string comparisons,
-- and a string comparison is chronological only while every value carries ONE
-- layout — the fixed-width UTC form internal/store.TimeLayout writes.
--
-- The premise is executable rather than argued. 'Z' is 0x5A and '.' is 0x2E:
--
--   sqlite3 :memory: "SELECT '2026-08-01T14:00:00Z' < '2026-08-01T14:00:00.500000Z';"  →  0
--
-- The shorter form sorts as LATER than a longer one naming an earlier instant.
--
-- # Why the guard that existed was not enough
--
-- 0003 was the first table to see this and guarded derived_session with
--
--   CHECK (length(started_at) = 27 AND length(ended_at) = 27),
--
-- and the reasoning in its comment is right. The guard is not. Width is the easy
-- half of the shape and it is the half that does not matter:
--
--   printf '%s' '2026-08-01T08:04:35.0-05:00' | wc -c   →  27
--
-- That value is EXACTLY the same width the store writes and is five hours out of
-- UTC. A length check reports success while the ordering it exists to protect is
-- wrong by five hours. 0007 measured this while fixing a review finding and
-- shipped the stronger form; 0008 followed it; this migration brings the tables
-- that came before them up to the same footing.
--
-- The pattern below is 0007's, unchanged, and it pins the whole layout through
-- the literal Z, so the width is implied rather than separately asserted. GLOB
-- and not LIKE: in LIKE, `_` matches any character and `.` means nothing, while
-- in GLOB the dot is a literal.
--
-- # It is LATENT, not live, and the difference is the whole claim
--
-- Measured against a byte copy of the live store at ~/.timetattle/timetattle.db
-- taken 2026-08-01T11:40:36-0500, over the ENTIRE population of every timestamp
-- column and not a window. The store is a moving target and the snapshot is
-- therefore named rather than the date alone. Reproduce with
--
--   ./scripts/timestamp-shape-audit
--
--   COLUMN                                   ROWS    NULLS      BAD
--   adjustment.ended_at                         0        -        -
--   adjustment.recorded_at                      0        -        -
--   adjustment.started_at                       0        -        -
--   adjustment_review.ended_at                  0        -        -
--   adjustment_review.recorded_at               0        -        -
--   adjustment_review.started_at                0        -        -
--   capture_run.finished_at                     8        0        0
--   capture_run.started_at                      8        0        0
--   derived_chain.ended_at                      0        -        -
--   derived_chain.started_at                    0        -        -
--   derived_chain_run.computed_at               0        -        -
--   derived_chain_step.observed_at              0        -        -
--   derived_session.computed_at              2092        0        0
--   derived_session.ended_at                 2092        0        0
--   derived_session.started_at               2092        0        0
--   event.ended_at                         919820       53        0
--   event.started_at                       919820      425        0
--   harvest_run.finished_at                   293        1        0
--   harvest_run.since                         293      293        0
--   harvest_run.started_at                    293        0        0
--   schema_migration.applied_at                 7        0        0
--
-- Three different facts and the tool refuses to render them alike. A 0 under BAD
-- beside a non-zero ROWS is an OBSERVED NOT-X across the whole population. A `-`
-- is NOTHING WAS RECORDED: the table holds no rows and the column reports
-- neither success nor failure. Eight columns here are in the first state and
-- twelve in the second.
--
-- harvest_run.since deserves its own sentence: 293 rows and 293 of them NULL, so
-- there is no evidence whatever about what a non-NULL value in that column looks
-- like. The guard on it is a decision about the future, not a finding about the
-- past.
--
-- These figures supersede the ones in task kFO3S, which quoted 1,179
-- derived_session rows against a store at migration 0006. Between the task being
-- written and this migration, the binary was reinstalled and re-granted Full
-- Disk Access, 0007 and 0008 were applied, and a harvest, a consolidate and a
-- sessionize ran. derived_session went 1,179 → 2,092 and event went 914,521 →
-- 919,820, of which the last 316 rows were first seen by harvest runs 221
-- through 227 between 16:34:19Z and 16:34:32Z. Nothing was deleted: max(id)
-- equals count(*) in event, which is what an append-only table looks like.
--
-- Nothing writes a bad value today because every writer goes through
-- internal/store.TimeLayout. The guard exists for the writer that does not — a
-- script, sqlite3 on the command line, an importer, a second tool.
--
-- # What this does NOT do, said plainly rather than implied
--
-- The GLOB makes the FORMAT unambiguous, which is exactly what the TEXT ordering
-- rests on. It does not make the VALUE true.
--
--   * A local instant mislabelled Z is indistinguishable from a real UTC one and
--     passes. Normalising to UTC happens in internal/store and only there.
--   * An impossible date passes: '2026-13-45T99:99:99.000000Z' satisfies every
--     CHECK below, verified.
--
-- Those need a clock and a calendar, not a constraint.
--
-- # Why a table rebuild, and what it costs
--
-- SQLite has no ALTER TABLE ADD CONSTRAINT. A CHECK added to a table that
-- already exists means rebuilding it: create, copy, drop, rename, and restore
-- every index and view that hung off it. The alternative was a BEFORE INSERT and
-- BEFORE UPDATE trigger per column, which is instant and costs no rewrite, and
-- 0008 already establishes triggers as a mechanism this schema uses.
--
-- The rebuild won on one property. A CHECK is applied to the rows ALREADY THERE,
-- because every one of them passes through it on the way into the new table, so
-- a store holding a bad value cannot migrate at all. A trigger guards only what
-- is written after it, which would leave a database that reports a guard it
-- never verified — the same shape of dishonesty as a status line built on
-- counting files. It is also uniform: a reader running `.schema event` sees the
-- guard in the table, in the same form as capture_run and adjustment, rather
-- than having to know to look for a trigger.
--
-- It is not free, and the cost was measured rather than estimated.
-- migrations.Apply was run three times against fresh byte copies of the live
-- store, through the same pure-Go driver the application uses
-- (modernc.org/sqlite):
--
--   Apply took 15.061s, 16.295s, 23.616s
--   harvest_run 293 → 293, event 919,820 → 919,820, canonical_event 919,811 →
--     919,811, derived_session 2,092 → 2,092, the chain family 0 → 0
--   foreign_key_check violations=0, integrity_check=ok
--
-- and the file went from 506,667,008 to 682,340,352 bytes every time. The growth
-- is freelist, reclaimable by VACUUM, which cannot run inside a transaction and
-- is therefore not attempted here.
--
-- So the first `timetattle` invocation after this ships pauses for fifteen to
-- twenty-five seconds with no output, and the store gets a third larger until
-- somebody vacuums it. That is the price of the guard on this store; on a fresh
-- one it is nothing.
--
-- # Why the order below, which is not arbitrary
--
-- PRAGMA foreign_keys is a NO-OP inside a transaction, and every migration runs
-- inside one, so foreign keys CANNOT be turned off here. The rebuild is
-- therefore ordered so they never need to be.
--
-- Both new parents and both new children are created and filled BEFORE anything
-- is dropped, and the children are dropped BEFORE their parents. That matters
-- for more than tidiness: with foreign keys on, DROP TABLE performs an implicit
-- DELETE FROM, which does not fire triggers but DOES apply foreign key actions.
-- derived_chain_unchained references derived_chain_run ON DELETE CASCADE, so
-- dropping the parent while the child still held rows would silently CASCADE
-- them away and the migration would report success having destroyed them. That
-- is why derived_chain_unchained is rebuilt here despite carrying no timestamp
-- at all: it is in the reference graph, and leaving it out is how the cascade
-- would have fired.
--
-- ALTER TABLE ... RENAME rewrites the references in other tables' schemas, so
-- renaming each _new table back to its real name re-points every foreign key
-- that was aimed at it.
--
-- # Deliberately NOT done here
--
-- harvest_run has no `CHECK (finished_at IS NULL OR finished_at >= started_at)`,
-- where capture_run has the equivalent. That is a real gap and 0 of the 293 live
-- rows violate it, so it could have been closed for free while the table was
-- being rewritten. It is left alone because it is a different defect from this
-- one and deserves its own test and its own task rather than riding along
-- unexamined in a migration about layout.
--
-- schema_migration.applied_at is also left unguarded, and that is a decision.
-- The ledger is created by the runner's own DDL with CREATE TABLE IF NOT EXISTS,
-- outside the numbered sequence, so a CHECK added there would reach new
-- databases only and make a fresh store's schema differ from a migrated one. It
-- is written by SQLite's strftime rather than by any caller, and nothing orders
-- on it — Current() orders by version. Its format is covered by
-- TestTheMigrationLedgerRecordsTimeTheSameWayEverythingElseDoes.
--
-- # The IS NULL guards, and what they are actually for
--
-- event.started_at, event.ended_at, harvest_run.finished_at and
-- harvest_run.since are genuinely nullable — an endpoint that was never
-- observed, a run still in flight, a collection with no lower bound — and each
-- one carries its own `IS NULL OR` below.
--
-- Be exact about why. A SQLite CHECK PASSES when it evaluates to NULL, so a bare
-- `x GLOB '…'` on a nullable column admits NULL. Here NULL is legal, so the bare
-- form and the guarded form BEHAVE IDENTICALLY, and no mutation test can tell
-- them apart. The guard is written out for the reason 0002 gives for
-- event.cause: the next reader must not have to work out whether the NULL case
-- was intended or overlooked, and the next migration that folds two of these
-- into one conjunction must not have to rediscover the rule. Claiming a
-- behavioural difference here would be the same over-claim this schema is full
-- of comments warning about.

-- ---------------------------------------------------------------------------
-- harvest_run and event. The parents first, then the child, then the drops in
-- the reverse order, so nothing is ever referenced by a table that no longer
-- exists and no cascade can fire.
-- ---------------------------------------------------------------------------

CREATE TABLE harvest_run_new (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT    NOT NULL,
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    input       TEXT    NOT NULL,
    since       TEXT,
    status      TEXT    NOT NULL CHECK (status IN ('running', 'ok', 'failed')),
    error       TEXT,
    events_seen INTEGER NOT NULL DEFAULT 0,
    events_new  INTEGER NOT NULL DEFAULT 0,

    CHECK (status <> 'failed' OR error IS NOT NULL),

    -- NOT NULL, so no IS NULL arm is possible and none is written.
    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    -- NULL means the run has not finished. The live store carries one such row.
    CHECK (finished_at IS NULL OR finished_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    -- NULL means no lower bound was handed to Collect. All 293 live rows are
    -- NULL, so this arm is the only thing observed about the column.
    CHECK (since IS NULL OR since GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
);

INSERT INTO harvest_run_new
    (id, source_id, started_at, finished_at, input, since, status, error, events_seen, events_new)
SELECT id, source_id, started_at, finished_at, input, since, status, error, events_seen, events_new
FROM harvest_run;

-- The column list is 0001's plus the `cause` column 0002 added by ALTER, with
-- its CHECK carried across: a rebuild silently dropping a constraint is the
-- failure mode this whole file is guarding against.
CREATE TABLE event_new (
    id             INTEGER PRIMARY KEY,
    first_seen_run INTEGER NOT NULL REFERENCES harvest_run_new (id),

    source_id  TEXT NOT NULL,
    shape      TEXT NOT NULL CHECK (shape IN ('interval', 'point', 'unbounded')),
    unknown    TEXT CHECK (unknown IS NULL OR unknown IN ('horizon', 'sleep', 'lost')),

    -- NULL only where an endpoint was genuinely never observed, which is the
    -- unbounded case and nothing else.
    started_at TEXT,
    ended_at   TEXT,

    app        TEXT,
    document   TEXT,
    state      TEXT,

    locator    TEXT,
    project    TEXT,
    payload    TEXT,
    cause      TEXT CHECK (cause IS NULL OR cause IN ('user-action', 'timer', 'recovery')),

    CHECK (shape <> 'interval' OR (started_at IS NOT NULL AND ended_at IS NOT NULL
                                   AND ended_at >= started_at)),
    CHECK (shape <> 'point' OR (started_at IS NOT NULL AND ended_at IS NOT NULL
                                AND started_at = ended_at)),
    CHECK (shape <> 'unbounded' OR ((started_at IS NULL) <> (ended_at IS NULL)
                                    AND unknown IS NOT NULL)),
    CHECK (shape = 'unbounded' OR unknown IS NULL),
    CHECK (locator IS NULL OR locator LIKE '/%'),

    -- The shape guard, and the precondition the interval CHECK above silently
    -- depended on. Both endpoints are nullable and both carry their own IS NULL
    -- arm; see the header for why that is about intent rather than behaviour.
    CHECK (started_at IS NULL OR started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   IS NULL OR ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
);

INSERT INTO event_new
    (id, first_seen_run, source_id, shape, unknown, started_at, ended_at,
     app, document, state, locator, project, payload, cause)
SELECT id, first_seen_run, source_id, shape, unknown, started_at, ended_at,
       app, document, state, locator, project, payload, cause
FROM event;

-- The view names the table, so it has to go before the table does; ALTER TABLE
-- RENAME would otherwise rewrite it to point at the old name.
DROP VIEW canonical_event;
DROP TABLE event;
DROP TABLE harvest_run;

ALTER TABLE harvest_run_new RENAME TO harvest_run;
ALTER TABLE event_new       RENAME TO event;

CREATE INDEX harvest_run_source ON harvest_run (source_id, started_at);

-- 0001's index, restored exactly. The COALESCEs are load-bearing and not
-- cosmetic: SQLite treats NULLs as DISTINCT in a unique index, so a nullable
-- endpoint would let two identical bare stops both insert and every re-harvest
-- would duplicate them.
CREATE UNIQUE INDEX event_identity ON event (
    source_id,
    shape,
    COALESCE(started_at, ''),
    COALESCE(ended_at, ''),
    COALESCE(app, ''),
    COALESCE(document, ''),
    COALESCE(state, '')
);

CREATE INDEX event_span    ON event (started_at, ended_at);
CREATE INDEX event_source  ON event (source_id, started_at);
CREATE INDEX event_project ON event (project) WHERE project IS NOT NULL;
CREATE INDEX event_locator ON event (locator) WHERE locator IS NOT NULL;

-- 0002's body, restored unchanged. Only a horizon is ever superseded, and the
-- narrowness is the point — a rule general enough to collapse a bare stop would
-- silently delete the 6.42h across five confirmed days that hangs off them.
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

-- ---------------------------------------------------------------------------
-- derived_session. All three timestamps are NOT NULL, so none takes an IS NULL
-- arm. The length() = 27 pair 0003 shipped is REPLACED rather than kept beside
-- the GLOB: the GLOB implies the width, and two guards for one property is how a
-- reader comes to believe the weaker one is doing something.
-- ---------------------------------------------------------------------------

CREATE TABLE derived_session_new (
    id INTEGER PRIMARY KEY,

    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,

    events  INTEGER NOT NULL,
    sources TEXT    NOT NULL,

    margin_micros INTEGER NOT NULL,

    computed_at      TEXT    NOT NULL,
    gap_micros       INTEGER NOT NULL,
    lead_in_micros   INTEGER NOT NULL,
    tail_out_micros  INTEGER NOT NULL,
    presence_sources TEXT NOT NULL,
    ignore_sources   TEXT NOT NULL,

    CHECK (ended_at >= started_at),

    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    -- computed_at had no guard of any kind before this. It records WHEN the
    -- derivation ran, which is how a stale table is told from a fresh one, and a
    -- staleness comparison against a value in another layout answers wrongly.
    CHECK (computed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    CHECK (events > 0),
    CHECK (margin_micros >= 0),
    CHECK (gap_micros > 0),
    CHECK (lead_in_micros >= 0),
    CHECK (tail_out_micros >= 0)
);

INSERT INTO derived_session_new
    (id, started_at, ended_at, events, sources, margin_micros, computed_at,
     gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources)
SELECT id, started_at, ended_at, events, sources, margin_micros, computed_at,
       gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources
FROM derived_session;

DROP TABLE derived_session;
ALTER TABLE derived_session_new RENAME TO derived_session;

-- 0003's index, restored. No COALESCE, deliberately: both columns are NOT NULL,
-- so there is no NULL for the DISTINCT rule to act on.
CREATE UNIQUE INDEX derived_session_span ON derived_session (started_at, ended_at);

-- ---------------------------------------------------------------------------
-- The chain family. Four tables in one reference graph:
--
--   derived_chain_run  ←  derived_chain        ←  derived_chain_step
--                      ←  derived_chain_unchained
--
-- All four are rebuilt, including derived_chain_unchained, which carries no
-- timestamp. See the header: its foreign key is ON DELETE CASCADE, so leaving it
-- in place while its parent was dropped would have cascaded its rows away
-- silently.
-- ---------------------------------------------------------------------------

CREATE TABLE derived_chain_run_new (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    computed_at       TEXT    NOT NULL,

    source_id         TEXT    NOT NULL,
    classes           TEXT    NOT NULL,
    extensions        TEXT    NOT NULL,
    min_shared_prefix INTEGER NOT NULL CHECK (min_shared_prefix > 0),
    coincidence_ms    INTEGER NOT NULL CHECK (coincidence_ms > 0),

    events_considered INTEGER NOT NULL CHECK (events_considered >= 0),
    documents         INTEGER NOT NULL CHECK (documents >= 0),
    documents_chained INTEGER NOT NULL CHECK (documents_chained >= 0),

    CHECK (documents_chained <= documents),
    CHECK (computed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
);

INSERT INTO derived_chain_run_new
    (id, computed_at, source_id, classes, extensions, min_shared_prefix, coincidence_ms,
     events_considered, documents, documents_chained)
SELECT id, computed_at, source_id, classes, extensions, min_shared_prefix, coincidence_ms,
       events_considered, documents, documents_chained
FROM derived_chain_run;

CREATE TABLE derived_chain_new (
    id             INTEGER PRIMARY KEY,
    run_id         INTEGER NOT NULL REFERENCES derived_chain_run_new (id) ON DELETE CASCADE,

    directory      TEXT    NOT NULL,
    root           TEXT    NOT NULL,

    started_at     TEXT    NOT NULL,
    ended_at       TEXT    NOT NULL,
    step_count     INTEGER NOT NULL CHECK (step_count >= 2),
    document_count INTEGER NOT NULL,

    CHECK (directory LIKE '/%'),
    CHECK (ended_at > started_at),
    CHECK (document_count >= step_count),

    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
);

INSERT INTO derived_chain_new
    (id, run_id, directory, root, started_at, ended_at, step_count, document_count)
SELECT id, run_id, directory, root, started_at, ended_at, step_count, document_count
FROM derived_chain;

CREATE TABLE derived_chain_step_new (
    id             INTEGER PRIMARY KEY,
    chain_id       INTEGER NOT NULL REFERENCES derived_chain_new (id) ON DELETE CASCADE,
    ordinal        INTEGER NOT NULL CHECK (ordinal >= 0),
    observed_at    TEXT    NOT NULL,

    documents      TEXT    NOT NULL,
    locators       TEXT    NOT NULL,
    document_count INTEGER NOT NULL CHECK (document_count >= 1),

    gap_ms         INTEGER CHECK (gap_ms IS NULL OR gap_ms > 0),

    CHECK ((ordinal = 0) = (gap_ms IS NULL)),

    -- The step's instant is what the gap_ms above is measured between, and the
    -- steps of a chain are read in observed_at order.
    CHECK (observed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
);

INSERT INTO derived_chain_step_new
    (id, chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms)
SELECT id, chain_id, ordinal, observed_at, documents, locators, document_count, gap_ms
FROM derived_chain_step;

-- No timestamp here. Rebuilt only so that its parent can be dropped without the
-- ON DELETE CASCADE above taking its rows with it.
CREATE TABLE derived_chain_unchained_new (
    id       INTEGER PRIMARY KEY,
    run_id   INTEGER NOT NULL REFERENCES derived_chain_run_new (id) ON DELETE CASCADE,
    document TEXT    NOT NULL,

    CHECK (document LIKE '/%')
);

INSERT INTO derived_chain_unchained_new (id, run_id, document)
SELECT id, run_id, document FROM derived_chain_unchained;

DROP TABLE derived_chain_step;
DROP TABLE derived_chain_unchained;
DROP TABLE derived_chain;
DROP TABLE derived_chain_run;

ALTER TABLE derived_chain_run_new       RENAME TO derived_chain_run;
ALTER TABLE derived_chain_new           RENAME TO derived_chain;
ALTER TABLE derived_chain_step_new      RENAME TO derived_chain_step;
ALTER TABLE derived_chain_unchained_new RENAME TO derived_chain_unchained;

CREATE UNIQUE INDEX derived_chain_identity ON derived_chain (run_id, directory, root);
CREATE INDEX derived_chain_span ON derived_chain (started_at, ended_at);
CREATE UNIQUE INDEX derived_chain_step_identity ON derived_chain_step (chain_id, ordinal);
CREATE UNIQUE INDEX derived_chain_unchained_identity
    ON derived_chain_unchained (run_id, document);
