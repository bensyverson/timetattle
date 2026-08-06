-- 0001: the immutable event store.
--
-- Three tables. Events are observations and never change. harvest_run is the
-- provenance of each read. registry maps folders to the project they belong to,
-- which is what makes event.project recomputable rather than a stored fact.
--
-- Design notes that are not obvious from the DDL live in internal/store and in
-- project/private/2026-07-29-sleep-asymmetry.md §6.

-- One row per (invocation, source), not per invocation. Collect has
-- partial-failure semantics — one broken source loses only its own events — so a
-- run that lost a source has to record which one and why, or the loss is
-- invisible in every number downstream.
CREATE TABLE harvest_run (
    id          INTEGER PRIMARY KEY,
    source_id   TEXT    NOT NULL,
    started_at  TEXT    NOT NULL,
    finished_at TEXT,
    -- What was read: a Biome stream directory, a knowledgeC snapshot path, a
    -- glob root. The run-level analogue of event.locator.
    input       TEXT    NOT NULL,
    -- The boundary handed to Collect, which is EXCLUSIVE. Recorded so that
    -- "why did this run not see X" is answerable later.
    since       TEXT,
    status      TEXT    NOT NULL CHECK (status IN ('running', 'ok', 'failed')),
    error       TEXT,
    -- events_seen is what the source returned; events_new is what survived
    -- dedup. events_new trending to zero proves dedup works. events_seen
    -- dropping for a source means retention is biting and the window is
    -- closing.
    events_seen INTEGER NOT NULL DEFAULT 0,
    events_new  INTEGER NOT NULL DEFAULT 0,

    CHECK (status <> 'failed' OR error IS NOT NULL)
);

CREATE INDEX harvest_run_source ON harvest_run (source_id, started_at);

-- An observation. Machine-only: human corrections are overlays keyed to a time
-- span, never rows here, because Timetattle is a signal for when work happened
-- and not a system of record for billable hours.
CREATE TABLE event (
    id             INTEGER PRIMARY KEY,
    first_seen_run INTEGER NOT NULL REFERENCES harvest_run (id),

    source_id  TEXT NOT NULL,
    shape      TEXT NOT NULL CHECK (shape IN ('interval', 'point', 'unbounded')),
    unknown    TEXT CHECK (unknown IS NULL OR unknown IN ('horizon', 'sleep', 'lost')),

    -- NULL only where an endpoint was genuinely never observed, which is the
    -- unbounded case and nothing else.
    started_at TEXT,
    ended_at   TEXT,

    app        TEXT,
    document   TEXT,
    -- The scalar reading, where the source carries one. /display/isBacklit uses
    -- 'on' and 'off'; /device/isLocked and /bluetooth/isConnected are the same
    -- shape. It is part of identity because knowledgeC writes contradictory
    -- overlapping rows: on 2026-07-14 at 09:01:12 both an 'on' and an 'off' row
    -- exist with identical endpoints, and they are two observations rather than
    -- one.
    state      TEXT,

    -- Provenance, not identity. Kept out of the key deliberately: it is empty
    -- for Biome, so it contributes nothing where dedup is load-bearing, and it
    -- would split one observation into two if a segment were ever replayed from
    -- a different path.
    locator    TEXT,
    -- Nullable and recomputable, filled by a resolution pass against registry.
    -- Never part of the key: registering a folder later, or fixing a mapping,
    -- must re-attribute events that already exist.
    project    TEXT,
    payload    TEXT,

    -- source.Event.Validate, restated where it cannot be bypassed.
    CHECK (shape <> 'interval' OR (started_at IS NOT NULL AND ended_at IS NOT NULL
                                   AND ended_at >= started_at)),
    -- The IS NOT NULL on the second endpoint is not redundant. A CHECK passes
    -- when it evaluates to NULL, so `started_at = ended_at` alone lets a point
    -- with a NULL end through — and that row would then dedup separately from
    -- the same observation written with both endpoints, because the key
    -- COALESCEs them.
    CHECK (shape <> 'point' OR (started_at IS NOT NULL AND ended_at IS NOT NULL
                                AND started_at = ended_at)),
    CHECK (shape <> 'unbounded' OR ((started_at IS NULL) <> (ended_at IS NULL)
                                    AND unknown IS NOT NULL)),
    CHECK (shape = 'unbounded' OR unknown IS NULL),
    CHECK (locator IS NULL OR locator LIKE '/%')
);

-- The dedup key, and the reason collectors may re-read an overlapping window
-- freely instead of tailing a stream.
--
-- COALESCE is load-bearing rather than cosmetic. SQLite treats NULLs as
-- DISTINCT in a unique index, so a nullable endpoint would let two identical
-- bare stops both insert and every re-harvest would duplicate them.
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

-- The canonical read, and the only one inference is allowed to use.
--
-- A horizon and the complete interval a later harvest produces for it are two
-- observations of one thing at two completenesses. Both rows persist untouched —
-- supersession is a derivation, never a mutation — so something has to collapse
-- them on the way out, and a rule saying "remember to dedupe" gets forgotten.
-- The failure mode of forgetting is silently double-counted hours, which is the
-- exact inflation this project exists to catch, so the collapse is structural.
--
-- ONLY A HORIZON IS EVER SUPERSEDED, and the narrowness is the point:
--
--   horizon  an unpaired start. Something was frontmost when the harvest ran and
--            had not resigned. The next harvest writes the stop, and the two
--            rows share the start they came from, to the microsecond, because it
--            is the same record in the same segment read twice.
--   sleep    an unpaired stop. The machine woke with an application already
--            frontmost, so macOS wrote no start and no later harvest will supply
--            one. Its repair is bounding against presence, permanently. 6.42h
--            across five confirmed days hangs off these, and a rule general
--            enough to collapse them would silently delete it.
--   lost     a partner record never written, cause unseen. Nothing completes it
--            either.
--
-- Identity is matched on everything the natural key treats as identity except
-- shape and the endpoint one of them is missing. state is included even though
-- Biome never sets it: presence carries no app, so the reading is the only thing
-- distinguishing one observation from another, and a view grouping on
-- (source, app) alone would collapse a lit horizon into a dark interval.
--
-- The COALESCEs also keep the predicate two-valued. A CHECK or a WHERE that
-- evaluates to NULL does not behave the way it reads, which is a trap this
-- schema has already been bitten by once.
CREATE VIEW canonical_event AS
SELECT id, first_seen_run, source_id, shape, unknown, started_at, ended_at,
       app, document, state, locator, project, payload
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

-- Folder-level opt-in. A folder need not be a git repository, and many folders
-- compose one project, so resolution is a longest-prefix match of event.locator
-- against path with the deeper registration winning. Contract values are
-- deliberately absent: they live in the global store only, so they cannot leak
-- into a commit.
CREATE TABLE registry (
    id            INTEGER PRIMARY KEY,
    path          TEXT NOT NULL UNIQUE,
    client        TEXT NOT NULL,
    project       TEXT NOT NULL,
    registered_at TEXT NOT NULL,

    CHECK (path LIKE '/%'),
    -- Normalised on the way in, so prefix matching cannot be defeated by a
    -- trailing separator.
    CHECK (path = '/' OR path NOT LIKE '%/')
);
