-- 0004: reconstructed save-as chains.
--
-- Blender and Motion are used by branching rather than editing. Every .blend in
-- the reference project has a birthtime equal to its mtime to the second (25 of
-- 25, `stat -f '%B %m %N' /Users/ben/git/acorn/blender/*.blend*`), so no file
-- carries a span and the iteration history lives across sibling filenames.
-- internal/chains clusters those names and derives the intervals between them.
--
-- # Why this is a derived table and not events
--
-- The event store is append-only and has no retraction: nothing can delete or
-- rewrite a row, by design, because an observation is not a claim and is never
-- edited to reflect a better later reading. Clustering files by name IS a
-- claim — a model, fitted, and certain to be revised. Written as events it
-- could never be taken back, and every later harvest would pile a second
-- generation of the model on top of the first with nothing able to tell them
-- apart.
--
-- So it lives here instead, under the convention internal/sessions uses on 0003:
--
--   * tables prefixed derived_
--   * rebuilt by DELETE then INSERT inside ONE transaction
--   * never INSERT, UPDATE or DELETE against event or harvest_run
--   * the parameters that produced the output are stored WITH the output
--
-- The last one is what makes a stale recompute auditable instead of silently
-- mixed. A chain row is meaningless without knowing which classes were
-- considered and what prefix length was required, and a reader who cannot
-- recover those has to guess whether two numbers came from the same model.

-- One row. Exactly one derivation is current, and the CHECK is what enforces
-- it: a second run cannot be inserted beside the first, so there is no state in
-- which half the chains came from one parameter set and half from another.
--
-- id is INTEGER PRIMARY KEY, so it is the rowid and can never be NULL. That is
-- the only reason `CHECK (id = 1)` is safe as written: a CHECK passes when it
-- evaluates to NULL, and this schema was bitten by exactly that in 0001. Every
-- other comparison below either sits on a NOT NULL column for the same reason,
-- or carries its own IS NULL guard.
CREATE TABLE derived_chain_run (
    id                INTEGER PRIMARY KEY CHECK (id = 1),
    -- When the derivation ran, in the fixed-width UTC form every timestamp in
    -- this database uses. Not when the evidence was observed.
    computed_at       TEXT    NOT NULL,

    -- The parameters. All NOT NULL: a model parameter that was not recorded is
    -- indistinguishable from one that was zero, and the whole point of this
    -- table is that the difference is recoverable.
    source_id         TEXT    NOT NULL,
    -- JSON arrays, because a class name or an extension containing the
    -- separator would silently corrupt a delimited list and nobody would see it.
    classes           TEXT    NOT NULL,
    extensions        TEXT    NOT NULL,
    min_shared_prefix INTEGER NOT NULL CHECK (min_shared_prefix > 0),
    -- Milliseconds rather than seconds. Stored in seconds, a coincidence window
    -- below one second truncates to zero and stops meaning anything, and the
    -- gap_ms CHECK below would start rejecting honest rows.
    coincidence_ms    INTEGER NOT NULL CHECK (coincidence_ms > 0),

    -- What the run saw. events_considered is what survived the source, class
    -- and extension filters, NOT what was read from the store: a consumer
    -- comparing two runs needs to know whether a change came from new evidence
    -- or from a widened filter.
    events_considered INTEGER NOT NULL CHECK (events_considered >= 0),
    documents         INTEGER NOT NULL CHECK (documents >= 0),
    documents_chained INTEGER NOT NULL CHECK (documents_chained >= 0),

    -- A model that chains everything measures nothing, and one that chains
    -- nothing measures nothing either. Both are legal states — this refuses only
    -- the impossible one.
    CHECK (documents_chained <= documents)
);

-- One lineage: a family of names in a directory.
CREATE TABLE derived_chain (
    id             INTEGER PRIMARY KEY,
    run_id         INTEGER NOT NULL REFERENCES derived_chain_run (id) ON DELETE CASCADE,

    -- Absolute, and matched the same way event.locator is, so a chain can be
    -- attributed to a project by the same longest-prefix walk over registry.
    directory      TEXT    NOT NULL,
    -- What every member agrees on, cut back to a name boundary. A LABEL, not a
    -- file: the animation_final family in the reference project has seven
    -- members and no animation_final of its own. Storing it as a path would
    -- assert a file that nobody observed.
    root           TEXT    NOT NULL,

    started_at     TEXT    NOT NULL,
    ended_at       TEXT    NOT NULL,
    step_count     INTEGER NOT NULL CHECK (step_count >= 2),
    document_count INTEGER NOT NULL,

    CHECK (directory LIKE '/%'),
    -- Both endpoints are NOT NULL, so this cannot evaluate to NULL and pass by
    -- accident. Strict rather than >=: a chain needs two steps and steps are
    -- separated by at least the coincidence window, so a zero-width chain is a
    -- bug in the collapse rather than a shape the model can legitimately reach.
    CHECK (ended_at > started_at),
    -- Every step names at least one document, and a step naming several is the
    -- simultaneous-birth collapse. So documents can exceed steps and can never
    -- fall below them.
    CHECK (document_count >= step_count)
);

-- Two chains in one directory cannot share a root: if they did they would have
-- been one cluster. run_id is in the key so the constraint is stated against
-- the derivation it belongs to rather than across generations.
--
-- No COALESCE is used here, and that is a decision rather than an oversight.
-- SQLite treats NULLs as DISTINCT in a UNIQUE index, so a nullable key column
-- would let duplicates insert freely for ever — 0001 documents that trap. All
-- three columns here are NOT NULL, which is what makes the plain form correct;
-- had root been nullable it would need COALESCE(root, '').
CREATE UNIQUE INDEX derived_chain_identity ON derived_chain (run_id, directory, root);
CREATE INDEX derived_chain_span ON derived_chain (started_at, ended_at);

-- One branch point: one or more new names appearing at one instant.
CREATE TABLE derived_chain_step (
    id             INTEGER PRIMARY KEY,
    chain_id       INTEGER NOT NULL REFERENCES derived_chain (id) ON DELETE CASCADE,
    ordinal        INTEGER NOT NULL CHECK (ordinal >= 0),
    observed_at    TEXT    NOT NULL,

    -- JSON arrays. documents holds the names created together; locators holds
    -- the files behind them and can be longer, because foo.blend and foo.blend1
    -- are one document. Keeping the locators is what lets a chain be checked
    -- against the disk later.
    documents      TEXT    NOT NULL,
    locators       TEXT    NOT NULL,
    document_count INTEGER NOT NULL CHECK (document_count >= 1),

    -- Milliseconds since the previous step. NULL on the first step, where there
    -- is no previous step — NOT zero. Zero would say two branch points happened
    -- at the same instant, which is precisely the thing the collapse rule
    -- exists to prevent from ever being recorded.
    gap_ms         INTEGER CHECK (gap_ms IS NULL OR gap_ms > 0),

    -- The invariant that makes the NULL above readable. Both sides are
    -- two-valued: ordinal is NOT NULL so (ordinal = 0) is never NULL, and
    -- IS NULL never returns NULL. Without this, a first step could carry a gap
    -- measured from nothing, or a later step could lose its gap and be read as
    -- the start of a chain it is in the middle of.
    CHECK ((ordinal = 0) = (gap_ms IS NULL))
);

CREATE UNIQUE INDEX derived_chain_step_identity ON derived_chain_step (chain_id, ordinal);

-- Documents that were observed and formed no chain.
--
-- This table is the whole reason the derivation is honest. "This document is in
-- no chain" and "this document was never walked" are different facts, and
-- collapsing them is a bug this project has shipped twice in three tools. A
-- consumer that sees no chain for a directory can now tell whether the model
-- looked and found no rhythm, or never looked at all.
CREATE TABLE derived_chain_unchained (
    id       INTEGER PRIMARY KEY,
    run_id   INTEGER NOT NULL REFERENCES derived_chain_run (id) ON DELETE CASCADE,
    -- directory/name, the same identity a chain member carries.
    document TEXT    NOT NULL,

    CHECK (document LIKE '/%')
);

CREATE UNIQUE INDEX derived_chain_unchained_identity
    ON derived_chain_unchained (run_id, document);
