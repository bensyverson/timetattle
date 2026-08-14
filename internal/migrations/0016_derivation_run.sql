-- 0016: what the derivation DID, recorded beside what it was given — and both
-- of them recorded once rather than on every sitting.
--
-- The normalization in the title is the side effect. The statistics are the
-- point, and the two arrive together only because the row that holds the
-- statistics is the row the parameters belong on.
--
-- # The gap this closes, which is functional and not cosmetic
--
-- internal/sessions.Cluster computes thirteen figures about every derivation —
-- how many observations it read as evidence, as presence and as ignored; how
-- many carried no extent; how many clusters its margins merged; how many
-- sittings observed darkness split, how much time that removed, how many
-- observations it suppressed; how much time two readings contested; how much
-- darkness was observed at all — prints them through `timetattle sessionize`,
-- and throws them away. Nothing is stored.
--
-- So the store could not answer "how much did the darkness rule cost on the
-- last derivation". On 2026-08-14 answering it required running `timetattle
-- metrics --all` twice with different flags and subtracting, which measures a
-- DIFFERENT clustering rather than reading the one that produced the stored
-- table. With a run row it is a SELECT, and it goes on being one as the rule
-- changes.
--
-- That last part is why the darkness policy is a column. A change already
-- decided (task OEAqy) will stop backlight darkness reducing engaged time, and
-- rows derived either side of it are not comparable. Recording the figures
-- without recording which rule produced them would build exactly the kind of
-- series this project refuses: numbers that moved for a reason nobody stored.
--
-- # Why the parameters moved, against 0003's argument that they should not
--
-- 0003 restates seven parameter columns on every session row and defends it at
-- length: "a single shared parameter row would be tidier and would lose exactly
-- that: a session row separated from its parameters is a span with no meaning,
-- and nothing should be able to produce one."
--
-- The GOAL is right and is kept. The mechanism was the weaker half, and two
-- things measured against it decided this.
--
-- FIRST, restating did not actually make an orphan impossible — it made it
-- REPRESENTABLE and then asked the reader to catch it. internal/store.Sittings
-- had to SELECT DISTINCT across the whole table on every read and refuse a
-- table that disagreed with itself, and internal/store's own tests wrote such a
-- table with a hand INSERT to prove the check worked. A foreign key does what
-- the restating was aiming at: with foreign_keys on, which store.Open sets, a
-- sitting cannot be inserted before the derivation that produced it exists, and
-- ON DELETE CASCADE means clearing the derivation clears the sittings. The
-- orphan stops being caught and starts being unrepresentable.
--
-- SECOND, and this is the bug rather than the tidiness: with the parameters on
-- the session rows, A DERIVATION THAT PRODUCED NO SITTINGS WROTE NOTHING AT
-- ALL, and read back identically to a database nobody had ever sessionized.
-- `timetattle sessionize -from <after the last event>` reaches it from the
-- command line. That is this project's oldest bug — an absence of rows read as
-- an observation of absence — sitting inside the very table whose ComputedAt
-- column exists to prevent it. A run row exists whether or not it produced
-- anything, so "the model ran and found none" and "nobody has run the model"
-- are now different rows and not the same silence.
--
-- AND A THIRD ARGUMENT THAT WAS OFFERED AND DOES NOT HOLD, recorded because it
-- is the one somebody will reach for again. The observation was that
-- derived_session held 1557 rows describing exactly one derivation —
--   sqlite3 "file:~/.timetattle/timetattle.db?mode=ro" "SELECT count(*),
--     count(DISTINCT computed_at||gap_micros||lead_in_micros||tail_out_micros||
--     presence_sources||ignore_sources||exclude_patterns) FROM derived_session"
--   -> 1557|1
-- — read as "seven columns repeated 1557 times to record one fact". The count is
-- correct and reproduces. The inference is empty: it is 1 BECAUSE `sessionize`
-- rewrites the table wholesale inside one transaction, so a table written that
-- way cannot exhibit two derivations and the query cannot come back with
-- anything else. The restated column exists to make a STALE row detectable, and
-- counting the distinct values present right now does not test that at all. It
-- measures the write path and reports it as a property of the design.
--
-- The two arguments above stand on their own and neither needs it.
--
-- # Why the rows are DISCARDED rather than carried forward
--
-- The parameters could be carried: the live table holds one distinct set and a
-- SELECT DISTINCT would lift it onto the run row honestly. The THIRTEEN
-- STATISTICS could not. Those derivations recorded what they were given and
-- never what they did, so every statistic column would have to be defaulted,
-- and the live figures say what defaulting costs: the last derivation
-- suppressed 24,704 observations and trimmed 48.6h. Writing zero there states
-- as a fact the exact thing this migration exists to record, in the one place a
-- reader is entitled to trust. A NULL would be worse — a third state every
-- reader must handle and the first one to forget collapses into "it was zero",
-- which is the trap 0007 spells out.
--
-- So both tables are created empty, on 0010's precedent and for its reason.
-- derived_session is a cache: produced from canonical_event and nothing else,
-- recomputable at any moment, and DELETEd wholesale by `timetattle sessionize`
-- on every run in any case. Nothing references it — 0008 records that the
-- adjustment overlay deliberately does not — so no answer, review or adjustment
-- is lost. The empty pair reads as the third state the design already names:
-- NOTHING HAS BEEN DERIVED. The cost of the whole discard is one `timetattle
-- sessionize`, a few seconds over ~1.08M observations.
--
-- # Why one row and not a history
--
-- This was the open question, and it was argued rather than assumed, because
-- the case for a history is real: the darkness rule is about to change, it will
-- move the hours, and a store that keeps only the current answer cannot say what
-- the numbers were before it.
--
-- WHAT DECIDED IT is that a history of THESE rows would not answer that
-- question. The hours are not on this row and cannot be — they are computed from
-- the sittings, and the sittings are replaced wholesale on every run. Two
-- generations of run row would say "before: 24,704 observations suppressed and
-- 48.6h trimmed; after: none", which is the aggregate cost of the RULE and is
-- worth having. It would still say nothing about hours, per project or in total,
-- because the rows those come from are gone. Answering "what did this change
-- cost project X" needs both derivations' SITTINGS kept, which is a different
-- and much larger change: derived_session_span would have to become per-run and
-- would stop guarding an INSERT that ran without its DELETE, every reader above
-- would have to learn which generation it wanted, and `sessionize -gap 2m`
-- would deposit a whole generation on every calibration run.
--
-- So the choice was not "current answer" against "history"; it was "current
-- answer" against "a history that answers a narrower question than the one
-- asked, at the price of the guarantees the current answer rests on".
--
-- THE QUESTION ITSELF HAS A CHEAPER ANSWER, and it is the one to use: run
-- `sessionize` and `metrics --all` BEFORE the change lands and keep the output.
-- A before-measurement is a thing to take once, not a schema to carry for ever,
-- and taking it is what this project's project/ trail is for.
--
-- `CHECK (id = 1)` is otherwise derived_chain_run's, and it fits the contract of
-- the derived_ family: these tables are REPLACED WHOLESALE and are never a place
-- to put a fact. An append-only log of every derivation ever taken is a
-- genuinely useful DIFFERENT thing — never replaced, belonging beside
-- harvest_run and capture_run. 0007 refused to put capture rows into harvest_run
-- for exactly this reason: one table, two contracts, and every query above it
-- learning to exclude half the rows. If that log is wanted it should be its own
-- table under its own name, and it should carry two summary columns this row
-- does not need — the sitting count and the total presence — so that a row stays
-- meaningful after the sittings it describes have been rebuilt away.
--
-- Widening this one later costs a rebuild of a ONE-ROW table. Narrowing a
-- history back down costs a decision about which rows to throw away.

-- ---------------------------------------------------------------------------
-- The derivation. One row: what it was drawn WITH, what it was drawn FROM,
-- what it DID, and under which rule.
--
-- id is INTEGER PRIMARY KEY, so it is the rowid and can never be NULL. That is
-- the only reason `CHECK (id = 1)` is safe as written — a CHECK PASSES when it
-- evaluates to NULL, and this schema was bitten by exactly that in 0001. Every
-- column below is NOT NULL, which is what makes every comparison in every CHECK
-- in this table two-valued and therefore enforceable. There is no nullable
-- column here and so no CHECK that needs its own IS NOT NULL guard; if one is
-- ever added, every constraint mentioning it needs revisiting.
-- ---------------------------------------------------------------------------
CREATE TABLE derived_session_run (
    id INTEGER PRIMARY KEY CHECK (id = 1),

    -- When the derivation ran, not when the evidence was observed.
    computed_at TEXT NOT NULL,

    -- WHAT IT WAS DRAWN WITH. 0003's columns, unchanged in meaning and in
    -- units, moved here from every session row.
    --
    -- Microseconds, not seconds, because that is what internal/store's
    -- TimeLayout resolves and what every endpoint is derived from. Rounding a
    -- derived duration to the second would make a stored session disagree with
    -- the same session recomputed, by up to a second, for ever.
    gap_micros      INTEGER NOT NULL CHECK (gap_micros > 0),
    lead_in_micros  INTEGER NOT NULL CHECK (lead_in_micros >= 0),
    tail_out_micros INTEGER NOT NULL CHECK (tail_out_micros >= 0),
    -- Comma-separated and sorted, empty for none. These change the answer as
    -- much as the durations do — reading /device/isLocked as evidence of work
    -- manufactures a 15h32m sitting out of a night's sleep — so a rebuild that
    -- changed only these must be as visible as one that changed the gap. A
    -- source id cannot hold a comma, which is why these can be delimited and
    -- exclude_patterns below cannot.
    presence_sources TEXT NOT NULL,
    ignore_sources   TEXT NOT NULL,

    -- WHAT OBSERVED DARKNESS WAS ENTITLED TO DO to this derivation.
    --
    -- New in 0016, and the one column here that records the CODE rather than
    -- the configuration. Today's value is 'suppress-all-and-trim': every
    -- evidence observation anchored inside an observed dark span is dropped,
    -- and the dark span is subtracted from the sitting that reached across it.
    -- The successor is expected to be 'suppress-timer-only', under which an
    -- agent producing work behind a sleeping screen keeps its time — a decision
    -- already taken and not yet implemented.
    --
    -- Two derivations either side of that change agree on every other column in
    -- this table and are not comparable, so without this a stored series would
    -- move for a reason nothing recorded. There is no default and no empty
    -- string: a derivation that does not name its rule has not recorded one.
    --
    -- The value set is deliberately NOT pinned by a CHECK, unlike
    -- capture_run.status. SQLite cannot widen a CHECK without rebuilding the
    -- table, and this column exists precisely to hold values that do not exist
    -- yet; a constraint here would make adding a word to a vocabulary cost a
    -- migration. The writer is internal/sittings.CurrentDarknessPolicy, which
    -- is where the names live.
    darkness_policy TEXT NOT NULL CHECK (darkness_policy <> ''),

    -- WHAT IT WAS DRAWN FROM. 0010's columns, unchanged, moved here with the
    -- rest. Its comment carries the full argument; the two halves are that
    -- sessions.exclude is applied where events are READ and moved total
    -- presence over the live corpus by 126.2h from four lines of config, and
    -- that store.Filter From/To narrows the same population by time.
    --
    -- JSON and not the comma-joined form the source columns use, because a
    -- pattern is a path and a path may hold a comma:
    -- "/Users/you/Documents/Smith, John/" is an ordinary directory, and
    -- splitting it on commas invents two patterns, one of which excludes a
    -- great deal more than was written.
    --
    -- "[]" says this derivation excluded nothing, which is a fact. There is
    -- deliberately no value that says nobody recorded it: that state is the
    -- absence of this row.
    exclude_patterns TEXT NOT NULL,
    -- The window the events were read over, empty for unbounded. Empty is a
    -- VALUE — nothing was excluded by time — and it is a string that cannot be
    -- confused with an instant, which is why it is used rather than NULL. A
    -- nullable bound would also make the GLOB below enforce nothing at all.
    corpus_from TEXT NOT NULL,
    corpus_to   TEXT NOT NULL,

    -- WHAT IT DID. sessions.Stats, every field, and the reason this table
    -- exists.
    --
    -- All NOT NULL and all >= 0, on derived_chain_run's discipline. Every count
    -- here separates something that was observed from something that was not,
    -- and a figure that dropped observations without saying how many is a
    -- figure that cannot be reconciled with the store it came from. A negative
    -- one is not a small number; it is a derivation that has miscounted and
    -- would go on being read as a fact.

    -- The three the input divides into: clustered, read as bounds, dropped by
    -- configuration. They sum to what Cluster was handed. The sum is NOT stored
    -- separately — a fourth column restating the other three is a column that
    -- can disagree with them.
    evidence_observations INTEGER NOT NULL CHECK (evidence_observations >= 0),
    presence_observations INTEGER NOT NULL CHECK (presence_observations >= 0),
    ignored_observations  INTEGER NOT NULL CHECK (ignored_observations  >= 0),

    -- Evidence whose extent was never observed. Each contributed its one
    -- endpoint as an instant and no duration; inventing an extent charged 5m09s
    -- of an idle machine to Safari in the one case measured against knowledgeC.
    unbounded_observations INTEGER NOT NULL CHECK (unbounded_observations >= 0),
    -- Presence readings that carried no usable interval — an unbounded reading,
    -- or a state that is neither 'on' nor 'off'. They bounded nothing, and
    -- saying so is not the same as saying the stream was silent.
    presence_without_span INTEGER NOT NULL CHECK (presence_without_span >= 0),

    -- Pairs of clusters joined because the lead-in and tail-out overlapped. A
    -- large number here means the margins have swallowed the gap threshold,
    -- which is a parameter that appears to be set and is not.
    merged_by_expansion INTEGER NOT NULL CHECK (merged_by_expansion >= 0),

    -- The darkness rule's whole cost, in the four ways it is paid. These are
    -- the columns the migration is for: on the live corpus at 2026-08-14 the
    -- last derivation suppressed 24,704 observations and trimmed 48.6h across
    -- 129 splits, and none of it was recoverable from anything stored.
    split_by_absence          INTEGER NOT NULL CHECK (split_by_absence          >= 0),
    trimmed_by_absence_micros INTEGER NOT NULL CHECK (trimmed_by_absence_micros >= 0),
    suppressed_by_absence     INTEGER NOT NULL CHECK (suppressed_by_absence     >= 0),

    -- Fragments left with no observation in them after darkness cut a sitting
    -- up: a middle piece lying BETWEEN two observations rather than containing
    -- either. Nothing observed work there and nothing observed darkness there,
    -- so emitting it would invent a sitting and counting it as trimmed would
    -- claim a reading nobody took. It is dropped, and counted here, which is
    -- the only treatment that is true.
    unevidenced_pieces         INTEGER NOT NULL CHECK (unevidenced_pieces         >= 0),
    unevidenced_dropped_micros INTEGER NOT NULL CHECK (unevidenced_dropped_micros >= 0),

    -- Time the backlight stream claims as both lit and dark. None of it is
    -- trimmed, because contradictory readings establish no darkness; it is
    -- recorded so that a number resting on it is recognisable.
    contested_micros INTEGER NOT NULL CHECK (contested_micros >= 0),
    -- The total span the backlight was read as dark, contested time excluded.
    -- It is the denominator trimmed_by_absence_micros is a fraction of, which
    -- is what makes the CHECK below an invariant rather than a guess.
    observed_absence_micros INTEGER NOT NULL CHECK (observed_absence_micros >= 0),

    -- THE CROSS-COLUMN INVARIANTS. Each is a property of the clustering, not a
    -- taste, and each refuses only the impossible reading: every pair below may
    -- legitimately be EQUAL. A corpus in which every evidence observation was
    -- unbounded is strange and not impossible, and a constraint that refused it
    -- would be refusing an honest reading.
    --
    -- Every column named is NOT NULL, so none of these can evaluate to NULL and
    -- pass by accident.
    --
    -- An unbounded observation is counted only after Cluster has counted it as
    -- evidence; a suppressed one was evidence before darkness dropped it.
    CHECK (unbounded_observations <= evidence_observations),
    CHECK (suppressed_by_absence  <= evidence_observations),
    -- A presence reading with no usable span is counted only after Cluster has
    -- counted it as presence.
    CHECK (presence_without_span <= presence_observations),
    -- Trimming removes only time inside the observed dark spans, and the
    -- expanded clusters it removes from are disjoint by construction — the
    -- merge above leaves consecutive spans strictly non-touching — so the
    -- trimmed pieces are disjoint subsets of the same absence. On the live
    -- corpus that is 48.6h out of 639.7h.
    CHECK (trimmed_by_absence_micros <= observed_absence_micros),

    -- 0009's shape guard. Fixed-width UTC, the same 27 characters
    -- internal/store.TimeLayout writes. 0007 measured why length() = 27 is not
    -- enough on its own: '2026-08-01T08:04:35.0-05:00' is exactly 27
    -- characters, is five hours out, and a length check admits it.
    CHECK (computed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    -- The same guard on the corpus bounds, with an explicit arm for unbounded.
    -- Both columns are NOT NULL, so both arms of every OR below are two-valued.
    CHECK (corpus_from = '' OR corpus_from GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (corpus_to   = '' OR corpus_to   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    -- A window that ends before it begins holds nothing, so no sitting can have
    -- come out of it.
    CHECK (corpus_from = '' OR corpus_to = '' OR corpus_to >= corpus_from),

    -- 0010's guard on the stored exclude list, carried over. json_valid alone
    -- would admit '"*.log"' and '3', which are valid JSON and are not lists of
    -- patterns; json_type pins it to an array. Both are total functions on a
    -- NOT NULL TEXT column.
    --
    -- What this does NOT do, said plainly: it does not check that the elements
    -- are strings, that they are sorted, or that they are patterns anybody
    -- could have written. '[1,2]' satisfies it. A CHECK cannot contain a
    -- subquery, so json_each is unavailable here; the canonical form is the
    -- writer's job (internal/sittings.Corpus) and the round trip is tested in
    -- internal/sessions and internal/store.
    CHECK (json_valid(exclude_patterns) AND json_type(exclude_patterns) = 'array')
);

-- ---------------------------------------------------------------------------
-- The sittings. 0010's body with the nine derivation columns removed and one
-- reference put in their place.
-- ---------------------------------------------------------------------------
DROP TABLE derived_session;

CREATE TABLE derived_session (
    id INTEGER PRIMARY KEY,

    -- The derivation that produced this span. NOT NULL, so a sitting cannot be
    -- written without one, and ON DELETE CASCADE, so the pair is replaced as
    -- one thing: `DELETE FROM derived_session_run` takes every sitting with it,
    -- and a rebuild cannot leave sittings pointing at parameters that have been
    -- deleted out from under them.
    --
    -- CHECK (run_id = 1) as well as the reference, and both earn their place.
    -- The reference is enforced only while PRAGMA foreign_keys is on — which
    -- store.Open sets and sqlite3 on the command line does not — so the CHECK
    -- is what still refuses a hand-written row pointing at run 2 in a shell.
    -- The reference is what refuses one pointing at run 1 before run 1 exists.
    run_id INTEGER NOT NULL CHECK (run_id = 1)
        REFERENCES derived_session_run (id) ON DELETE CASCADE,

    -- The sitting, INCLUDING the lead-in and tail-out. This is the span the
    -- person is inferred to have been working across, not the span anything was
    -- observed in; margin_micros says how much of it nothing observed.
    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,

    -- How many observations evidenced this sitting, and from which sources,
    -- comma-separated and sorted. Provenance for a span that is otherwise
    -- unfalsifiable: a nine-hour session resting on two events is a different
    -- claim from one resting on four hundred.
    events  INTEGER NOT NULL,
    sources TEXT    NOT NULL,

    -- How much of started_at..ended_at no observation covered: the lead-in, the
    -- tail-out, and every silence shorter than the gap that was bridged.
    margin_micros INTEGER NOT NULL,

    -- Both endpoints are NOT NULL, which is what makes this comparison
    -- two-valued and therefore enforceable.
    CHECK (ended_at >= started_at),

    -- 0009's shape guard, unchanged. The comparison above is a TEXT comparison
    -- and is chronological only while every value carries one layout.
    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    -- A session nothing evidenced is not a session. Every row must be traceable
    -- back to at least one observation, or the span was invented.
    CHECK (events > 0),
    CHECK (margin_micros >= 0)

    -- Deliberately NOT constrained: margin_micros <= the span's own length. It
    -- is a true invariant and it is asserted in Go, but SQLite's date functions
    -- resolve to milliseconds at best while these endpoints carry microseconds,
    -- so the SQL form would need a slack of a whole second to avoid false
    -- failures — and a constraint loose enough never to fire is worse than an
    -- absent one, because it reads as protection.
);

-- 0003's index, restored again. It guards the rebuild rather than the
-- clustering: the failure it catches is an INSERT that ran without its DELETE,
-- which would double every session and every hour computed from them.
--
-- run_id is deliberately NOT in the key, unlike derived_chain_identity's. There
-- it distinguishes generations; here `CHECK (run_id = 1)` makes it a constant,
-- and a constant column in a unique key constrains nothing while looking as
-- though it might.
--
-- No COALESCE, because both columns are NOT NULL and there is no NULL for
-- SQLite's DISTINCT rule to act on.
CREATE UNIQUE INDEX derived_session_span ON derived_session (started_at, ended_at);
