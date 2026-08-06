-- 0003: sittings, derived from events and owning nothing.
--
-- The first derived table. Everything before it is an observation or the
-- provenance of one; this is an inference, and the separation is load-bearing
-- enough to be visible in the name.
--
-- A row says "these instants look like one sitting at the keyboard". It is
-- produced by internal/sessions from the canonical_event view and nothing else,
-- so it is recomputable from scratch at any moment and is never a place to put
-- a fact. NOTHING here is edited by hand and nothing here survives a rebuild:
-- `timetattle sessionize` DELETEs every row and INSERTs the new ones inside one
-- transaction, so the table is either entirely one derivation or entirely the
-- previous one, and never a mixture of two.
--
-- It writes to no other table. A recompute must leave `event` and `harvest_run`
-- byte-identical, which is task J04gF's criterion Ybu and is tested by
-- fingerprinting both tables around three consecutive rebuilds
-- (internal/sessions.TestRecomputingChangesNoEventAndNoHarvestRun).
--
-- # Why a table at all
--
-- Clustering 39,593 events takes milliseconds, so computing on demand would
-- also work, and a cache that can go stale is a real cost. The table earns its
-- place because the working-day boundary (Tvfnf), the engaged/presence split
-- (IFmcU) and every report above them read the same sittings, and three readers
-- each re-deriving with their own parameters is how two numbers in one report
-- come to disagree for no discoverable reason.
--
-- Staleness is therefore made VISIBLE rather than prevented. Every row carries
-- the parameters it was derived under and the moment it was derived, so a table
-- computed at one gap and a question asked at another is a discrepancy anybody
-- can see with a SELECT. A single shared parameter row would be tidier and
-- would lose exactly that: a session row separated from its parameters is a
-- span with no meaning, and nothing should be able to produce one.

CREATE TABLE derived_session (
    id INTEGER PRIMARY KEY,

    -- The sitting, INCLUDING the lead-in and tail-out. This is the span the
    -- person is inferred to have been working across, not the span anything
    -- was observed in; margin_micros says how much of it nothing observed.
    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,

    -- How many observations evidenced this sitting, and which sources they
    -- came from, comma-separated and sorted. Provenance for a span that is
    -- otherwise unfalsifiable: a nine-hour session resting on two events is a
    -- different claim from one resting on four hundred.
    events  INTEGER NOT NULL,
    sources TEXT    NOT NULL,

    -- How much of started_at..ended_at no observation covered: the lead-in,
    -- the tail-out, and every silence shorter than the gap that was bridged.
    -- Inferred time, separated from observed time, so a report can say which
    -- it is quoting.
    margin_micros INTEGER NOT NULL,

    -- The derivation, restated on every row.
    computed_at      TEXT    NOT NULL,
    gap_micros       INTEGER NOT NULL,
    lead_in_micros   INTEGER NOT NULL,
    tail_out_micros  INTEGER NOT NULL,
    -- Comma-separated and sorted, empty for none. These change the answer as
    -- much as the durations do — reading /device/isLocked as evidence of work
    -- manufactures a 15h32m sitting out of a night's sleep — so a rebuild that
    -- changed only these must be as visible as one that changed the gap.
    presence_sources TEXT NOT NULL,
    ignore_sources   TEXT NOT NULL,

    -- Microseconds, not seconds, because that is what internal/store's
    -- TimeLayout resolves and what every endpoint here is derived from.
    -- Rounding a derived duration to the second would make a stored session
    -- disagree with the same session recomputed, by up to a second, for ever.

    -- Both endpoints are NOT NULL, which is what makes this comparison
    -- two-valued and therefore enforceable. A CHECK passes when it evaluates
    -- to NULL, so the same line against a nullable column would enforce
    -- nothing at all — the trap migration 0001 spells out at length and that
    -- shipped here once already.
    CHECK (ended_at >= started_at),

    -- Fixed-width UTC, the same 27 characters internal/store.TimeLayout
    -- writes. The comparison above is a TEXT comparison, so it is only
    -- chronological while every value has the same width: a caller that wrote
    -- RFC3339 would produce rows that sort wrongly near second boundaries and
    -- satisfy the CHECK anyway.
    CHECK (length(started_at) = 27 AND length(ended_at) = 27),

    -- A session nothing evidenced is not a session. Every row must be
    -- traceable back to at least one observation, or the span was invented.
    CHECK (events > 0),
    CHECK (margin_micros >= 0),
    CHECK (gap_micros > 0),
    CHECK (lead_in_micros >= 0),
    CHECK (tail_out_micros >= 0)

    -- Deliberately NOT constrained: margin_micros <= the span's own length.
    -- It is a true invariant and it is asserted in Go, but SQLite's date
    -- functions resolve to milliseconds at best while these endpoints carry
    -- microseconds, so the SQL form would need a slack of a whole second to
    -- avoid false failures — and a constraint loose enough never to fire is
    -- worse than an absent one, because it reads as protection.
);

-- Sessions are disjoint by construction: expansion by the lead-in and tail-out
-- can make two clusters touch, and internal/sessions merges them rather than
-- emitting both, precisely so that wall clock is never claimed twice.
--
-- This index is the guard on the rebuild rather than on the clustering. The
-- failure it catches is an INSERT that ran without its DELETE — the ordinary
-- shape of a half-written rebuild — which would otherwise double every session
-- silently and double every hour computed from them.
--
-- No COALESCE, and that is deliberate rather than forgotten: SQLite treats
-- NULLs as DISTINCT in a UNIQUE index, so a nullable key column would let
-- duplicates insert freely for ever. Both columns here are NOT NULL, so there
-- is no NULL for that rule to act on. The wrapping in event_identity is
-- required because those columns genuinely are nullable.
CREATE UNIQUE INDEX derived_session_span ON derived_session (started_at, ended_at);

-- No separate index on started_at: it is the leading column above, so range
-- scans over a day or a week already use that one.
