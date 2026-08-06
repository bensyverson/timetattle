-- 0012: which client a project belongs to, as an append-only log.
--
-- The client dimension was never missing. It is captured on all 38 markers, it
-- is correct, and the attribution pass -- which has the marker in hand -- threw
-- it away and wrote only `project`. This is where it stops being thrown away.
--
-- ===========================================================================
-- WHY A LOG AND NOT A COLUMN ON `event`
-- ===========================================================================
--
-- A column on `event` was built first and is not what shipped. The argument
-- that killed it is a cost argument and it is decisive:
--
--   A project's client has 38 DISTINCT VALUES across ~973,000 events. Changing
--   one of them is ~927,599 row updates, to record a fact the size of a small
--   table.
--
-- And the column is not even a store of that fact. internal/store/attribution.go
-- rewrites every attributed row on EVERY harvest, from the markers, so the
-- column is a cache of exactly the mapping this table holds, applied
-- retroactively. It answers no question a lookup could not answer. Its one
-- genuinely structural advantage -- a past event keeping the client it was
-- actually DONE for, rather than the one that project has today -- is destroyed
-- by the same overwrite that maintains it.
--
-- This table is ~38 rows and gives that advantage back properly, because it is
-- a LOG rather than a mapping: the client an instant belonged to is a question
-- about a moment in time, and a log can answer it for any moment.
--
-- project/private/2026-08-04-the-client-dimension.md carries the full trail, including
-- the column design and the two corrections that led here.
--
-- ===========================================================================
-- TWO TIMESTAMPS, BECAUSE THERE ARE TWO KINDS OF CHANGE
-- ===========================================================================
--
-- This is the design point everything else follows from, and a single timestamp
-- silently picks one of them.
--
--   REASSIGNMENT   the project moved to a new client on some date. Work done
--                  BEFORE that date was done for the OLD client and must keep
--                  it. Rewriting it would make an invoiced period mutable
--                  against a later model change, which task mjalb's criterion
--                  XZk forbids in as many words.
--
--   CORRECTION     it was ALWAYS the new client; the old value was a mistake.
--                  This one MUST apply retroactively, because the old answer
--                  was never true of any instant.
--
-- So `effective_from` -- when the association took effect -- is separate from
-- `recorded_at`, when somebody wrote it down. A reassignment sets
-- effective_from to the date of the move. A correction leaves it NULL, meaning
-- "from the beginning of time". Nothing else distinguishes them and one fold
-- serves both; see internal/clientmap.
--
-- NULL is the right spelling of "from the beginning" rather than a very early
-- timestamp, for the reason this schema gives everywhere else: a sentinel date
-- is a value somebody can compare against and get a true answer from by
-- accident, and '0001-01-01T00:00:00.000000Z' would sort and compare as a real
-- instant. NULL cannot be mistaken for one.
--
-- ===========================================================================
-- WHY `source`, AND WHY IT IS NOT PROVENANCE
-- ===========================================================================
--
-- `source` is the directory whose marker asserted this. It is load-bearing, and
-- without it a CORRECTION and a COLLISION are the same two rows:
--
--   one directory revising its own claim          -> a correction; later wins
--   two directories claiming different clients    -> a collision; NOBODY wins
--
-- Two directories disagreeing is not a correction. Neither of them changed its
-- mind and there is no later-is-truer ordering between them, so resolving it by
-- recorded_at is last-writer-wins, which produces an entirely plausible wrong
-- number -- this project's worst failure mode, shipped three times. The fold
-- refuses to answer for such a project and NAMES it, the way
-- store.Attribution reports an unreadable locator instead of folding it into
-- "unattributed". `timetattle metrics` then reports those hours as a FOURTH
-- figure beside its three existing unattributed reasons, never folded into any
-- of them.
--
-- ===========================================================================
-- WHY THERE IS NO `kind` COLUMN
-- ===========================================================================
--
-- Three kinds of change were named: a SHIFT (the project moved), a MISTYPE (the
-- wrong client was typed) and a NEGLECT (no client was ever assigned). The last
-- two both cover all of history and so fold identically, and it would be easy
-- to add a column saying which.
--
-- It is not added, deliberately, because the distinction is RECOVERABLE FROM
-- THE ROWS. A neglect assertion is the first thing this log ever said about
-- that project; a mistype supersedes an earlier assertion from the SAME source.
-- Folding the log up to just before the new row answers which, exactly, and
-- preserves the superseded value. A column would be a second spelling of a fact
-- the rows already carry -- which is 0011's own argument for not adding one,
-- and dayview.changed's for computing rather than storing.
--
-- ===========================================================================
-- WHY APPEND-ONLY, AND WHY NO UNIQUE INDEX
-- ===========================================================================
--
-- 0008 established both for the adjustments layer and the reasoning transfers
-- unchanged: a change of mind is itself a fact, an UPDATE records only the
-- latest state and silently forgets that anybody thought otherwise, and a table
-- nothing can delete from is the strongest available form of "this survives
-- re-derivation". Triggers enforce it, so it is a property of the database
-- rather than a discipline.
--
-- There is deliberately NO supersession rule of the kind `event` needs. Nothing
-- here is inferred: the log is folded in recorded order, the later assertion
-- wins for the instants it covers and for no others, and no row is touched by
-- the fold.
--
-- And no UNIQUE index, following 0008. Recording the same assertion twice is a
-- FACT -- it says a registration was run twice and said the same thing -- and
-- the fold is idempotent under repetition, so deduping would destroy
-- information while changing no answer. `timetattle register` avoids the growth
-- at the CALLER instead, by appending only when the folded answer actually
-- differs from what is being asserted, which is the same no-op behaviour
-- registry.Register already has for an unchanged marker.
--
-- IF A KEY IS EVER ADDED, effective_from is the column that would need
-- COALESCE(effective_from, ''). It is the one nullable column here, and SQLite
-- treats NULLs as DISTINCT in a UNIQUE index, so a key containing it bare would
-- enforce nothing at all -- the standing trap in this schema, recorded in
-- CLAUDE.md and written around by 0003, 0004 and 0008.

CREATE TABLE client_project (
    id INTEGER PRIMARY KEY,

    -- The body of work, and the key this log is folded by. It matches
    -- event.project, which is itself a recomputable cache of the registry; see
    -- 0001_initial.sql.
    project TEXT NOT NULL,
    -- Who it belongs to.
    client  TEXT NOT NULL,
    -- The directory whose marker asserted it. See the header: this is what
    -- tells a correction from a collision.
    source  TEXT NOT NULL,

    -- WHEN THE ASSOCIATION TOOK EFFECT. NULL means from the beginning of time,
    -- which is what a correction and a first registration both want:
    -- registering a directory after its evidence already exists is the ordinary
    -- way somebody starts tracking a project, and an assertion that began at
    -- the moment it was typed would attribute none of it.
    effective_from TEXT,
    -- WHEN SOMEBODY WROTE IT DOWN, which is a different fact and never decides
    -- which instants an assertion covers -- only which of two assertions
    -- covering the same instant is the later word.
    recorded_at TEXT NOT NULL,
    -- Who or what recorded it. An anonymous assertion cannot be traced back and
    -- re-examined.
    origin TEXT NOT NULL,

    -- EVERY CHECK BELOW CARRIES ITS OWN IS NULL GUARD WHERE THE COLUMN IS
    -- NULLABLE. A CHECK constraint PASSES when it evaluates to NULL, so a
    -- comparison against a nullable column enforces nothing whatever -- the
    -- trap 0001 spells out, that shipped in this schema once already, and that
    -- CLAUDE.md now records. Only effective_from is nullable here, and it is
    -- the only one that needs the guard.
    --
    -- NOT NULL refuses the absent case; these refuse the empty-string spelling
    -- of the same thing, which is a second way to say nothing.
    CHECK (project <> ''),
    CHECK (client  <> ''),
    CHECK (origin  <> ''),
    -- A source is an absolute directory path, the same shape event.locator is
    -- constrained to in 0001.
    CHECK (source LIKE '/%'),

    -- The shape guard. Both of these are TEXT and the FOLD orders on
    -- recorded_at and compares effective_from against event timestamps, so a
    -- mis-sorting here silently reverses which of two assertions won, or moves
    -- a reassignment boundary.
    --
    -- WIDTH IS NOT ENOUGH and the obvious length() = 27 form is wrong:
    -- '2026-08-01T08:04:35.0-05:00' is exactly 27 characters and is five hours
    -- out of UTC. The whole layout is pinned through the literal Z, so the
    -- width is implied rather than asserted beside it. GLOB and not LIKE: in
    -- LIKE the `_` matches any character and the `.` means nothing, while in
    -- GLOB the dot is a literal.
    CHECK (effective_from IS NULL OR effective_from GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (recorded_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z')
);

-- The fold reads one project's assertions in recorded order, and time-travels
-- by restricting on recorded_at. This index serves both, and the id is included
-- because it is the fold's tiebreak for two assertions written in the same
-- microsecond.
CREATE INDEX client_project_fold ON client_project (project, recorded_at, id);

-- The way to change an assertion is to record another one. The fold takes the
-- later of the two for the instants it covers, the earlier stays visible, and
-- "what did this say before that correction" therefore remains answerable.
CREATE TRIGGER client_project_is_never_updated
BEFORE UPDATE ON client_project
BEGIN
    SELECT RAISE(ABORT, 'client-project assertions are append-only; record a new assertion '
        || 'for the same project instead of editing this one, so that both answers and their '
        || 'times survive and a past report can still be reproduced');
END;

CREATE TRIGGER client_project_is_never_deleted
BEFORE DELETE ON client_project
BEGIN
    SELECT RAISE(ABORT, 'client-project assertions are never deleted; deleting one makes a '
        || 'period that WAS attributed look as though it never was, which is the difference '
        || 'between an observation and a gap');
END;
