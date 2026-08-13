-- 0010: the corpus a derivation was drawn FROM, beside the parameters it was
-- drawn WITH.
--
-- 0003 argues at length that every parameter with leverage is restated on every
-- session row, so that a table computed under one model and a question asked
-- under another is a discrepancy anybody can see with a SELECT. It stores the
-- gap, the two margins and the source roles. It does not store which
-- OBSERVATIONS the derivation was allowed to see, and by 2026-08-01 there were
-- two ways to change that without changing anything the table records.
--
-- # The leverage, measured rather than asserted
--
-- sessions.exclude landed on 2026-08-01 (task 4LQHd) and is applied where events
-- are READ. Over the live store, measured by
-- `timetattle metrics -store $COPY -config empty.json -exclude '...'` and
-- recorded in project/private/2026-08-01-machine-generated-file-activity.md:
--
--   no patterns   PRESENCE 1193.2h across 2093 sittings, median 10m18s
--   four patterns PRESENCE 1067.0h across 1363 sittings, median 22m18s
--
-- 126.2h and 730 sittings, from four lines of configuration — more movement than
-- most gap changes, and the median sitting more than doubles. Before this
-- migration both derivations wrote byte-identical values into every column
-- above, so sittings.Params.Equal could not tell them apart and nothing above it
-- could say why a stored answer and a fresh question disagreed.
--
-- store.Filter From/To is the second way. `timetattle sessionize -from 2026-07-01`
-- writes a derivation covering one month into a table that says nothing about
-- the window, and every reader above it then sees a corpus with no work in June
-- — which is this project's oldest bug, the one where an absence of rows is read
-- as an observation of absence. Both halves are recorded here rather than one
-- now and the other after somebody is misled by it.
--
-- # Why the rows are DISCARDED rather than carried forward
--
-- There is no honest value to write into the new columns for a row that already
-- exists. Those rows were computed under an exclude list nobody recorded;
-- defaulting them to "[]" would state as a fact the exact thing this migration
-- exists to stop being guessed, and would do it in the one place a reader is
-- entitled to trust. A NULL would be worse — a third state that every reader
-- would then have to handle and that the first one to forget would collapse into
-- "excluded nothing".
--
-- So the table is recreated empty. derived_session is a cache and 0003 says so:
-- it is produced from canonical_event and nothing else, it is recomputable from
-- scratch at any moment, and `timetattle sessionize` DELETEs every row on every
-- run in any case. Nothing references it — 0008 records that the adjustment
-- overlay deliberately does not — so no answer, review or adjustment is lost.
-- The empty table reads as the third state the design already has a name for:
-- NOTHING HAS BEEN DERIVED, which internal/store.Sittings reports as such and
-- never as a day with no work in it. The cost of the whole discard is one
-- `timetattle sessionize`, measured at a few seconds over 927,385 events.
--
-- A copy-and-default migration would have been possible and is the reason this
-- comment is long: it would have been silently wrong for ever, and the rebuild
-- is loudly right for one command.

-- ---------------------------------------------------------------------------
-- The table, 0009's body with three columns added. Recreated rather than
-- ALTERed: SQLite refuses ADD COLUMN ... NOT NULL without a default, and the
-- default is precisely what must not exist here.
-- ---------------------------------------------------------------------------

DROP TABLE derived_session;

CREATE TABLE derived_session (
    id INTEGER PRIMARY KEY,

    -- The sitting, INCLUDING the lead-in and tail-out.
    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,

    -- How many observations evidenced this sitting, and from which sources.
    events  INTEGER NOT NULL,
    sources TEXT    NOT NULL,

    -- How much of started_at..ended_at no observation covered.
    margin_micros INTEGER NOT NULL,

    -- The derivation, restated on every row: what it was drawn WITH.
    computed_at      TEXT    NOT NULL,
    gap_micros       INTEGER NOT NULL,
    lead_in_micros   INTEGER NOT NULL,
    tail_out_micros  INTEGER NOT NULL,
    presence_sources TEXT NOT NULL,
    ignore_sources   TEXT NOT NULL,

    -- The corpus, restated on every row: what it was drawn FROM.
    --
    -- exclude_patterns is a JSON array of the patterns from sessions.exclude,
    -- sorted and deduplicated by internal/sittings.Corpus.Canonical so that two
    -- spellings of one instruction store one value. JSON and not the
    -- comma-joined form the source columns use, because a pattern is a path and
    -- a path may hold a comma: "/Users/you/Documents/Smith, John/" is an
    -- ordinary directory, and splitting it on commas invents two patterns, one
    -- of which excludes a great deal more than was written. A source id cannot
    -- hold a comma, which is why presence_sources can get away with it.
    --
    -- NOT NULL, and that is the whole point of the column. "[]" says this
    -- derivation excluded nothing. There is deliberately NO value that says
    -- nobody recorded it: that state is the absence of rows, which
    -- internal/store.Sittings already reports as "nothing has been derived"
    -- rather than as an empty day.
    --
    -- The SQLite sidecar rule (-wal, -shm) is NOT recorded. It is universal,
    -- unconfigurable and identical in every derivation there will ever be, so
    -- storing it would distinguish nothing while looking as though it might.
    exclude_patterns TEXT NOT NULL,

    -- The window the events were read over, empty for unbounded. Empty is a
    -- VALUE — nothing was excluded by time — and it is a string that cannot be
    -- confused with an instant, which is why it is used rather than NULL.
    corpus_from TEXT NOT NULL,
    corpus_to   TEXT NOT NULL,

    CHECK (ended_at >= started_at),

    -- 0009's shape guard, unchanged. The comparison above is a TEXT comparison
    -- and is chronological only while every value carries one layout.
    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (computed_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    -- The same guard on the corpus bounds, with an explicit arm for unbounded.
    -- Written as `= '' OR GLOB` rather than as a nullable column and an IS NULL
    -- arm, on purpose: a CHECK passes when it evaluates to NULL, so a nullable
    -- bound with a bare GLOB would enforce nothing whatever on the value that
    -- mattered. Both columns are NOT NULL, so both arms of every OR below are
    -- two-valued and the constraint is enforceable.
    CHECK (corpus_from = '' OR corpus_from GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (corpus_to   = '' OR corpus_to   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    -- A window that ends before it begins holds nothing, so no sitting can have
    -- come out of it. Both sides are NOT NULL and both unbounded cases are
    -- named, so this is two-valued in every row it can meet.
    CHECK (corpus_from = '' OR corpus_to = '' OR corpus_to >= corpus_from),

    -- The stored exclude list must be readable back as a list. json_valid alone
    -- would admit '"*.log"' and '3', which are valid JSON and are not lists of
    -- patterns; json_type pins it to an array. Both are total functions on a
    -- NOT NULL TEXT column, so neither can go NULL and quietly pass.
    --
    -- What this does NOT do, said plainly: it does not check that the elements
    -- are strings, that they are sorted, or that they are patterns anybody
    -- could have written. '[1,2]' satisfies it. A CHECK cannot contain a
    -- subquery, so json_each is unavailable here; the canonical form is the
    -- writer's job (internal/sittings.Corpus) and the round trip is tested in
    -- internal/sessions and internal/store.
    CHECK (json_valid(exclude_patterns) AND json_type(exclude_patterns) = 'array'),

    CHECK (events > 0),
    CHECK (margin_micros >= 0),
    CHECK (gap_micros > 0),
    CHECK (lead_in_micros >= 0),
    CHECK (tail_out_micros >= 0)
);

-- 0003's index, restored. It guards the rebuild rather than the clustering: the
-- failure it catches is an INSERT that ran without its DELETE, which would
-- double every session and every hour computed from them. No COALESCE, because
-- both columns are NOT NULL and there is no NULL for SQLite's DISTINCT rule to
-- act on.
CREATE UNIQUE INDEX derived_session_span ON derived_session (started_at, ended_at);
