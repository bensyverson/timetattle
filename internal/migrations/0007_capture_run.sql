-- 0007: what the capture phase did, recorded where a reader can find it.
--
-- A harvest has two phases. Capture preserves the sources that delete
-- themselves — knowledgeC, Biome's ScreenTime.AppUsage segments, Motion's
-- autosave vault, a filedays scan — as files under ~/.timetattle/snapshots.
-- Ingestion then reads those files into `event`, recording one `harvest_run`
-- row per source per invocation.
--
-- Until this migration, only the second phase left a record. `timetattle
-- status` therefore had two sections built on entirely different footings: a
-- source table fed by harvest_run, and a snapshots section that COUNTED FILES
-- IN A DIRECTORY. A capture that failed today left yesterday's files exactly
-- where they were, so status printed a plausible count and no failure signal.
--
-- That is not a hypothetical. Between 2026-07-30 12:01 and 2026-08-01 08:08 a
-- reinstall revoked the Full Disk Access grant — macOS pins it to an ad-hoc
-- binary's cdhash, so `go install` silently drops it — and every capture failed
-- for 44 hours. Ingestion's half of that outage is still legible: harvest_run
-- rows 19 and 21 through 24 carry status='failed' and the permission error. The
-- capture half left NO ROW OF ANY KIND. The only durable trace was six lines in
-- ~/.timetattle/harvest.err, which is the file nobody reads, and that is
-- precisely why the outage ran for 44 hours instead of one.
--
-- Capture is also the phase where that matters most. Ingestion reads durable
-- snapshots and can be repeated at any hour, so a lost ingestion costs a re-run.
-- Capture is the ONLY chance to hold knowledgeC and Biome before they prune
-- themselves: knowledgeC's /display/isBacklit retains roughly 28 days, and what
-- is not captured inside that window is gone. So the phase that cannot be
-- re-run was the one phase status could not see.
--
-- # Why a table of its own, and not more harvest_run rows
--
-- This was the open question on task 4tuVv (criterion xqM), and the answer is
-- not obvious. Reusing harvest_run costs no migration, no reader, and no new
-- vocabulary, and `timetattle status` already knows how to aggregate it. Three
-- things decided it the other way.
--
-- FIRST, harvest_run's columns are the wrong measurements, and the wrongness is
-- not cosmetic. Its payload is events_seen and events_new: what a source
-- returned and what survived content deduplication. A capture returns no events
-- at all. It returns FILES and BYTES — 2 segments written, 11 already held,
-- 4.1 MB. Writing bytes into events_seen would put two incommensurable
-- populations in one column, and every existing reader that sums it would start
-- adding megabytes to event counts. `timetattle metrics` and status's own
-- totals both do exactly that sum. Leaving the columns at 0 instead is no
-- better: a capture row would then be indistinguishable from an ingestion that
-- ran and found nothing, which is a real and healthy state this store exists to
-- keep legible.
--
-- SECOND, harvest_run is the provenance chain, not a log. `event.first_seen_run`
-- is NOT NULL and carries a foreign key into it, so every row there is
-- answerable for events and most are actually referenced. Capture rows would be
-- rows in that table that no event can ever point at — a second population
-- inside the one table whose whole job is "which read produced this
-- observation". Any provenance query would have to learn to exclude them, for
-- ever, and a query that forgot would silently widen its denominator.
--
-- THIRD, the two phases genuinely fail differently and are read at different
-- moments. The question harvest_run answers is "is the evidence still
-- arriving?". The question here is "is the evidence still being PRESERVED?",
-- and it is asked when the answer to the first one is already known to be no.
-- Keeping them apart means a schema change to one cannot break the other, and
-- means neither table has a column that is meaningful only for half its rows.
--
-- The rejected third option was a `phase` column on harvest_run with the
-- event counters made nullable. That is the worst of both: it keeps the two
-- populations in one table AND makes NOT NULL counters nullable, which is how a
-- count that was never measured comes to read as a count that was zero — the
-- exact confusion this project has now shipped three times.
--
-- # Why there is no 'running' status here
--
-- harvest_run has one, and it must: a run row exists BEFORE any event does,
-- because an event's provenance is not optional, so there is an interval during
-- which the row is honestly live. The cost is that a process which dies
-- mid-flight leaves a row marked 'running' for ever. The live store carries one
-- right now — id 28, claude-transcripts, started 2026-08-01T13:07:13Z,
-- finished_at NULL, stranded when the process died under it — and nothing can
-- tell it apart from a harvest genuinely in progress.
--
-- A capture row has no such need. It is written AFTER the capture returns, from
-- a start and an end measured around it, so both endpoints are known facts by
-- the time anything is inserted and finished_at is NOT NULL. The status set is
-- therefore closed at four terminal outcomes and 'running' is refused outright.
--
-- What this trades away is deliberate. A harvest killed mid-capture writes no
-- row at all, so the captures that had already succeeded go unrecorded. That
-- degrades to "nothing recorded", which status reports honestly and which reads
-- as a problem — rather than to a row that says 'running' and means nothing.
-- The recording pass also runs AFTER the store is opened, which keeps the whole
-- capture phase independent of the database: a store that cannot be opened must
-- never delay or prevent the preservation of a perishable source, which is why
-- capture runs first in the first place.
--
-- # The four outcomes, and why 'absent' is one of them
--
--   ok       the capture read its source through.
--   partial  it preserved some of what it tried and lost the rest. Biome
--            captures stream by stream and filedays scans root by root, and
--            both are best-effort: losing one must not discard the others.
--   failed   it preserved nothing and says why.
--   absent   it looked, and there was nothing there.
--
-- The last is the one that would be easiest to leave out, and it is the reason
-- this table earns its keep. A machine with no Motion installed has no vault to
-- preserve; a machine with no filedays roots configured has nothing to scan.
-- Recording nothing for those would make them indistinguishable from a harvest
-- that died before it got that far — collapsing "observed not-X" into "nothing
-- was recorded", which is the standing lesson in CLAUDE.md and a bug this
-- project has shipped twice in three separate tools. An absent row is an
-- OBSERVATION: on this run, we looked, and Motion is not installed.

CREATE TABLE capture_run (
    id          INTEGER PRIMARY KEY,

    -- Which perishable thing was being preserved: knowledgec, biome,
    -- motion-vault, filedays. NOT NULL and non-empty, because a row that does
    -- not say what it captured cannot be matched to the files it left.
    kind        TEXT    NOT NULL CHECK (kind <> ''),

    -- What was read: the knowledgeC database path, the Biome root, the vault
    -- root, the list of filedays roots. The capture-phase analogue of
    -- harvest_run.input, and it is what makes "cannot read X" checkable against
    -- the X the run actually tried.
    input       TEXT    NOT NULL CHECK (input <> ''),

    -- Both NOT NULL, unlike harvest_run.finished_at. See above: a capture row
    -- is written after the capture returned, so a row with no end could only
    -- ever be a lie. Fixed-width UTC (internal/store.TimeLayout), which is what
    -- makes TEXT comparison chronological — and which is enforced by the GLOB
    -- CHECKs below rather than merely stated here.
    started_at  TEXT    NOT NULL,
    finished_at TEXT    NOT NULL,

    status      TEXT    NOT NULL CHECK (status IN ('ok', 'partial', 'failed', 'absent')),
    error       TEXT,

    -- What the capture preserved. files_new is what this run wrote; files_held
    -- is what it found already preserved, byte for byte, and skipped. Biome and
    -- the vault are content-addressed, so a healthy run over an unchanged
    -- source writes files_new = 0 — and that row is the whole of task 4tuVv's
    -- criterion t38. "Ran and found nothing new" and "did not run" must never
    -- render alike, and only a row with files_new = 0 sitting beside a
    -- finished_at can express the first.
    --
    -- bytes is the compressed size of what was written, so 0 alongside a
    -- non-zero files_held is a normal reading rather than a missing one.
    files_new   INTEGER NOT NULL DEFAULT 0 CHECK (files_new  >= 0),
    files_held  INTEGER NOT NULL DEFAULT 0 CHECK (files_held >= 0),
    bytes       INTEGER NOT NULL DEFAULT 0 CHECK (bytes      >= 0),

    -- A capture that finished before it started is a clock or a caller bug, and
    -- either way the span is unusable. Safe as a bare comparison ONLY because
    -- both columns are NOT NULL: a SQLite CHECK PASSES when it evaluates to
    -- NULL, so a comparison against a nullable column enforces nothing at all.
    -- This schema was bitten by exactly that in 0001.
    CHECK (finished_at >= started_at),

    -- The precondition the line above silently depends on, enforced rather than
    -- asserted in a comment.
    --
    -- That comparison is a TEXT comparison, and TEXT comparison is chronological
    -- only while every value has ONE shape. 'Z' is 0x5A and '.' is 0x2E, so
    -- '2026-08-01T14:00:00Z' compares GREATER than
    -- '2026-08-01T14:00:00.500000Z' — the earlier instant sorting as the later
    -- one. A caller writing RFC3339 therefore satisfies the ordering CHECK and
    -- corrupts every ORDER BY and every range read anyway, silently, near each
    -- second boundary. 0003 guards its own endpoints for this reason.
    --
    -- 0003 uses length() = 27 alone. That is not enough, and this table
    -- measured it: '2026-08-01T08:04:35.0-05:00' is EXACTLY 27 characters, is
    -- five hours out, and a length check admits it. The GLOB pins the whole
    -- shape — four-digit year through six-digit fraction, and a literal Z — so
    -- the width is implied rather than separately asserted.
    --
    -- What this does NOT catch, and nothing cheap in SQL could: a correctly
    -- shaped string carrying a local instant mislabelled Z, or an impossible
    -- date like month 13. Those need a clock and a calendar, not a constraint.
    -- The guard makes the FORMAT unambiguous, which is what the ordering rests
    -- on; it does not make the value true.
    CHECK (started_at  GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (finished_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    -- A loss with no reason recorded is indistinguishable from a capture with
    -- nothing to report, which is the failure mode this whole table exists to
    -- remove. status is NOT NULL, so neither implication can be short-circuited
    -- by a NULL on the left.
    CHECK (status NOT IN ('failed', 'partial') OR error IS NOT NULL),
    -- And the converse: a successful capture carrying an error is a row that
    -- argues with itself, and 'absent' carrying one would turn "there is no
    -- Motion on this machine" into "Motion could not be read".
    CHECK (status NOT IN ('ok', 'absent') OR error IS NULL)
);

-- The read this exists for is "the most recent capture of each kind", which is
-- a descending scan of one kind's rows. There is deliberately no UNIQUE index:
-- every capture attempt is its own row for ever, and a dedup key would mean
-- some attempt overwrote another's record — the opposite of what an append-only
-- log of a perishable phase is for.
CREATE INDEX capture_run_kind ON capture_run (kind, started_at);
