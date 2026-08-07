-- 0014: the payload has a shape, and the shape is now enforced.
--
-- event.payload has existed, unwritten, since 0001. This migration is the point
-- at which something starts writing it — internal/transcripts records the
-- Claude Code SESSION each transcript record belonged to and the TOKENS the
-- model spent on it — and it exists to make the column's one structural
-- assumption true rather than hoped for: a payload is a JSON OBJECT, or there
-- is no payload.
--
-- # The trap this design exists to avoid, with the mechanism measured
--
-- state IS part of event_identity, for a reason 0001 sets out: knowledgeC
-- writes contradictory overlapping rows and the reading is the only thing
-- telling them apart. So state is the one field on an event that a per-record
-- figure could plausibly be smuggled into, and it is exactly the wrong one.
--
-- Anything inside event_identity has to be a fact that two readings of ONE
-- observation agree about, because disagreement is spelled "two rows". Two
-- readings of one transcript record are not hypothetical here: Claude Code's
-- compaction and side-question mechanisms replay a conversation, uuid and
-- timestamp intact, into an internal subagent's file. Measured 2026-08-07 over
-- this machine's corpus (scripts/sessioncensus):
--
--   7,182  repeat readings of a uuid already seen
--   7,133  of them disagree about human-or-machine, which internal/transcripts
--          resolves with a preference rule before the store ever sees them
--      50  of them state DIFFERENT usage figures in the two copies
--
-- Those 50 are the whole argument. A preference rule works for human-or-machine
-- because the field has two values and one of them is evidence; token counts
-- have neither property. In the key, each of those records becomes two rows
-- that look like two observations of one instant, and every figure computed
-- from them double-counts. Out of the key, the first reading is stored, the
-- second conflicts and does nothing, and the disagreement is resolved where it
-- can be seen — in the collector.
--
-- The re-read is not an edge case either. internal/transcripts has no
-- watermark: it parses every file on every harvest, all 830,438 timed records
-- of them, precisely so that a corpus rewritten underneath it cannot drift out
-- of step. Every one of those records is offered to the store on every harvest,
-- and dedup by content is the only thing standing between that and a store that
-- grows by a corpus a day.
--
-- # Why a payload column, rather than a sidecar table
--
-- A sidecar keyed to the event (event_usage(event_id, session, input, ...))
-- gives typed columns and would need no JSON at all. It was rejected on the
-- write path, and the cost is countable rather than aesthetic.
--
-- The event insert is `ON CONFLICT DO NOTHING`, which returns no row and no id
-- when the observation is already stored — which, on every harvest after the
-- first, is nearly all of them. Attaching a sidecar row therefore means looking
-- the event id up by its seven-column identity for every record offered: on
-- this machine that is 822,387 extra SELECTs per harvest to attach facts that
-- are already known at insert time. It also splits one observation across two
-- tables that can now disagree, and puts a second write in a path whose whole
-- guarantee is that a re-read writes nothing.
--
-- Typed columns on the event table itself (session_id TEXT, input_tokens
-- INTEGER, ...) were the other candidate, and ALTER TABLE ADD COLUMN is cheap.
-- They were rejected because they put one collector's vocabulary into a
-- source-agnostic schema: nothing about a Biome focus interval or a knowledgeC
-- presence row has a session or a token, and the next source with two facts of
-- its own would arrive at the same argument and win two more columns. The
-- payload column exists to absorb exactly this, and it is opaque to the store:
-- internal/store never interprets it except in the usage roll-up, which asks
-- for JSON paths that internal/usage declares.
--
-- What the payload costs, stated rather than waved at: about 60 bytes per
-- transcript record where the model spent nothing and about 110 where it did,
-- so roughly 60 MB across the 822,387 transcript rows in a 651 MB store. A
-- session table keyed by an integer would recover about half of that (a session
-- id is 36 characters and there are 1,590 distinct ones), at the price of a
-- join and an id lookup per record. At this size that trade is not worth
-- making; at ten times it might be, and the figure above is here so the
-- decision can be revisited against a number.
--
-- # What this migration actually does, and why it is a trigger
--
-- The column is already there and it is already outside every index, so
-- STORING a payload needs no DDL at all. What needs enforcing is that the
-- payload is an object.
--
-- The reason is that the roll-up reads it with json_extract, which answers NULL
-- for every path of a value that is not an object. `7`, `"tokens"` and
-- `[1,2,3]` are all valid JSON and all of them would store cleanly and then
-- read back as a store that recorded nothing — the third instance in this
-- project of "nothing was recorded" and "observed nothing" being the same
-- bytes. Malformed JSON is worse than that: json_extract raises "malformed
-- JSON" and takes the whole query down with it, so one bad row would silence
-- every figure for every scope.
--
-- 0009 faced the same choice and rebuilt the table, arguing that a CHECK is
-- applied to the rows already there while a trigger guards only what comes
-- after. That argument is right, and it does not apply here, because the
-- population it protects is empty. Measured 2026-08-07 against the live store:
--
--   sqlite3 "file:$HOME/.timetattle/timetattle.db?mode=ro" \
--     "SELECT COUNT(*), SUM(payload IS NOT NULL), SUM(source_id='claude-transcripts') FROM event;"
--   1031945|0|822387
--
-- Not one row in a million carries a payload, so a rebuild would copy 1,031,945
-- rows and recreate six indexes and a view in order to validate nothing. The
-- trigger covers every payload that will ever exist, because none exists yet.
--
-- # Why the guard is spelled with a CASE
--
-- json_type() does not return NULL for input that is not JSON — it RAISES
-- "malformed JSON". So it cannot be the first test in the WHEN clause: on
-- exactly the input this guard exists to catch, the guard itself would be what
-- failed, aborting with a message about JSON rather than about payloads.
-- json_valid() has to run first, and SQL does not promise that the left side of
-- an OR short-circuits. CASE does — SQLite evaluates only the branch it needs —
-- so the two tests are ordered explicitly rather than by hoping.
--
-- The NULL trap this schema has been bitten by twice is present here in its
-- trigger form. A WHEN clause that evaluates to NULL does not fire, exactly as
-- a CHECK that evaluates to NULL passes. `NEW.payload IS NOT NULL` is therefore
-- written out first, and it is not redundant with anything: without it, the
-- ordinary case — an event with no payload at all, which is most of the store —
-- would depend on json_valid(NULL) rather than on a stated rule.
--
-- Proven by mutation rather than asserted, in
-- internal/migrations/payload_shape_test.go: with the trigger dropped, the four
-- refused payloads below insert without complaint and the roll-up then reports
-- a store that recorded nothing.

CREATE TRIGGER event_payload_is_an_object_on_insert
BEFORE INSERT ON event
WHEN NEW.payload IS NOT NULL
     AND CASE WHEN json_valid(NEW.payload) THEN json_type(NEW.payload) <> 'object' ELSE 1 END
BEGIN
    SELECT RAISE(ABORT, 'an event payload must be a JSON object or absent; the usage roll-up reads it '
        || 'by JSON path, and anything else reads back as though nothing was recorded');
END;

-- The same guard on UPDATE, because event rows ARE updated: the attribution
-- pass writes event.project, and a statement that touches a row is a statement
-- that could touch this column. It fires only on a payload that is not an
-- object, so an ordinary re-attribution — which does not mention payload at all
-- — is unaffected.
CREATE TRIGGER event_payload_is_an_object_on_update
BEFORE UPDATE ON event
WHEN NEW.payload IS NOT NULL
     AND CASE WHEN json_valid(NEW.payload) THEN json_type(NEW.payload) <> 'object' ELSE 1 END
BEGIN
    SELECT RAISE(ABORT, 'an event payload must be a JSON object or absent; the usage roll-up reads it '
        || 'by JSON path, and anything else reads back as though nothing was recorded');
END;

-- # Why there is no index on the payload, said out loud
--
-- The roll-up filters on payload IS NOT NULL, a period, and optionally a
-- project, and then reads the payload of every surviving row to sum it. An
-- index cannot answer the sums — they need the column itself — so the only
-- thing one could buy is skipping rows without payloads, and on the store this
-- is aimed at, transcript rows are already 80% of the table (822,387 of
-- 1,031,945).
--
-- The period bound already rides 0013's index, because it is spelled
-- COALESCE(started_at, ended_at) here exactly as it is there. Measured through
-- internal/store's TestTheUsageRollUpBoundsThePeriodThroughAnIndexRatherThanAScan,
-- which prints the plan it asserts on:
--
--   period-bounded:  SEARCH e USING INDEX event_anchor (<expr><?)
--   unbounded:       SCAN e
--
-- Both also carry USE TEMP B-TREE FOR count(DISTINCT), for the session count.
-- No index removes that one: the value counted is json_extract(payload,
-- '$.session'), which is computed per row, and the tree it builds holds the
-- DISTINCT values rather than the rows — 1,590 of them across this whole
-- corpus. That is the ordinary cost of asking how many sessions there were, not
-- a missing index.
--
-- So: no index, on the grounds that nothing has measured one to be worth its
-- write cost. That is a decision made against a plan rather than an omission,
-- and the plan test above is where it is checked.
