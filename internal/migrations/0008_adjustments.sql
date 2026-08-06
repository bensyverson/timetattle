-- 0008: human corrections, keyed to a wall-clock span and applied over the
-- machine's answer rather than into it.
--
-- The first table in this database written by a person. Everything before it is
-- either an observation (0001, 0002) or an inference derived from one (0003,
-- 0004), and this is neither: it is a judgment, and judgments are revised.
--
-- # Why a span and not a session id
--
-- Migration 0001 said it before there was anything to key: "human corrections
-- are overlays keyed to a time span, never rows here". internal/store's package
-- doc and docs/content/docs/store.md say the same. The reason became structural
-- when 0003 landed: `timetattle sessionize` replaces derived_session wholesale,
-- DELETE then INSERT inside one transaction, so every session id is destroyed
-- and remade on every rebuild. An adjustment keyed to one could not survive a
-- rebuild by construction, and the task's own criterion ZHM requires that it
-- does. A wall-clock span is model-independent: it means the same thing at a
-- two-minute gap and at a two-hour gap, and total engaged time varies 2.3x
-- between those two.
--
-- Nothing here references derived_session, derived_chain or event. That is the
-- point rather than an omission.
--
-- # Why an overlay and not an input to the clustering
--
-- Corrections could be fed INTO sessionize, so that re-deriving produced
-- already-corrected sittings. They are not, and the reason is the calibration
-- loop this task exists to serve.
--
-- Task BNiYj records that corrections "simultaneously become the calibration
-- set", and PkY6I then fits the clustering parameters against them. If
-- corrections were an input, the model's own output would already contain the
-- human's answers, so the residual between model and truth would shrink toward
-- zero for reasons having nothing whatever to do with the model improving. The
-- error signal would be unmeasurable, and unmeasurable in a direction that
-- looks like success. As an overlay, the difference between derived_session and
-- the adjusted view IS the model error, directly and at any moment.
--
-- It also costs nothing in what a report can say: the corrected answer is the
-- overlay applied, and the uncorrected one is still there beside it.
--
-- # Why append-only, with no supersession rule
--
-- A correction to a correction is a NEW ROW, never an UPDATE, and nothing here
-- is ever deleted. Triggers enforce both, so it is a property of the database
-- rather than a discipline.
--
-- Three reasons. First, criterion RLN — "each adjustment records its origin and
-- time" — means nothing under UPDATE, where it records only the latest state
-- and silently forgets that anybody ever thought otherwise. Second, changing
-- one's mind is itself calibration data: a person who confirms a span and later
-- retracts it is telling you something about how far back recall is reliable,
-- and an UPDATE throws that away. Third, "adjustments are sacred and must
-- survive any re-derivation" is a claim about durability, and a table nothing
-- can delete from is the strongest available form of it.
--
-- There is deliberately NO supersession rule of the kind `event` needs, and the
-- distinction matters. A horizon superseded by a completing interval is an
-- INFERENCE about which of two observations to believe, and over-general
-- versions of that rule have nearly destroyed records in this project once
-- already. Here there is nothing to infer: the log is folded in recorded order,
-- the later assertion wins for the instants it covers and for no others, and no
-- row is touched by the fold. internal/adjustments.Fold is that function, and
-- the ordering key is (recorded_at, id) — id because two assertions recorded in
-- the same microsecond would otherwise resolve by whatever ORDER BY the caller
-- happened to write.
--
-- # Why there is no UNIQUE index
--
-- 0003 and 0004 both explain why they use no COALESCE in their unique indexes.
-- This migration has no unique index at all, which is a decision and not an
-- oversight. Recording the same judgment twice is a FACT — it says a person
-- looked twice and said the same thing — and deduping would destroy it while
-- changing no answer, since the fold is idempotent under repetition. If a key
-- is ever added, review_id is the column that would need COALESCE(review_id, 0):
-- it is the one nullable column here, and SQLite treats NULLs as DISTINCT in a
-- UNIQUE index, so a key containing it would enforce nothing at all.

-- The act of looking, which is not the same as what was found.
--
-- This table exists because "reviewed and could not recall" and "never
-- reviewed" are different facts and must not produce the same rows. Collapsing
-- them is the bug this project has shipped twice in three separate tools, and
-- it is the reason internal/sessions treats observed-absent, observed-present
-- and nothing-recorded as three states rather than two. Without this table the
-- third state would be unrecordable: a reviewed day with no corrections would
-- be indistinguishable from a day nobody has opened.
CREATE TABLE adjustment_review (
    id INTEGER PRIMARY KEY,

    -- The span that was looked at. Wall clock, in the same fixed-width UTC form
    -- every timestamp in this database uses.
    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,

    -- What the reviewer could say about the span AS A WHOLE.
    --
    --   recalled     — the reviewer remembers this span. Corrections may hang
    --                  off it, and their absence means the machine's answer was
    --                  agreed with.
    --   unremembered — the reviewer looked and could not recall. This is
    --                  INFORMATIVE and must not be coerced into either of the
    --                  other two states: it says the machine's answer is
    --                  unconfirmed, which is different from wrong and different
    --                  from unexamined.
    verdict TEXT NOT NULL CHECK (verdict IN ('recalled', 'unremembered')),

    -- Criterion RLN. Who or what, and when the recording happened — not when
    -- the reviewed work happened, which is started_at..ended_at.
    origin      TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    note        TEXT,

    -- Strict, unlike the span CHECK on an adjustment below. A zero-width review
    -- reviewed nothing, and storing one would put a row in the "this was
    -- looked at" table for a span nobody looked at.
    --
    -- Both endpoints are NOT NULL, which is what makes this two-valued and
    -- therefore enforceable. A CHECK passes when it evaluates to NULL, so the
    -- same line against a nullable column would enforce nothing — the trap
    -- 0001 spells out and that shipped in this schema once already.
    CHECK (ended_at > started_at),

    -- The shape guard. The comparison above is a TEXT comparison and is only
    -- chronological while every value carries the identical layout
    -- internal/store.TimeLayout writes. recorded_at is included because the
    -- FOLD orders on it: a mis-sorting there silently reverses which of two
    -- judgments won.
    --
    -- WIDTH IS NOT ENOUGH, and the obvious `length(x) = 27` form was written
    -- here first and was wrong. '2026-08-01T08:04:35.0-05:00' is exactly 27
    -- characters and is five hours out of UTC, so a length check reports
    -- success while the ordering it exists to protect is wrong by five hours:
    --
    --   printf '%s' '2026-08-01T08:04:35.0-05:00' | wc -c   →  27
    --
    -- The premise underneath is executable rather than argued. 'Z' is 0x5A and
    -- '.' is 0x2E, so a shorter form sorts AFTER a longer one naming an
    -- earlier instant:
    --
    --   sqlite3 :memory: "SELECT '2026-08-01T14:00:00Z' < '2026-08-01T14:00:00.500000Z';"  →  0
    --
    -- So the whole shape is pinned, through the literal Z, and the width is
    -- implied rather than asserted beside it. GLOB and not LIKE: LIKE's `_`
    -- would match any character and its `.` carries no meaning, while in GLOB
    -- the dot is a literal — a comma in its place is refused, checked.
    --
    -- WHAT THIS DOES NOT CATCH, said plainly so nobody reads it as airtight:
    -- a correctly shaped LOCAL instant mislabelled Z is indistinguishable from
    -- a real UTC one and passes, and so does an impossible date —
    -- '2026-13-45T99:99:99.000000Z' satisfies this constraint, verified. The
    -- guard is on the LAYOUT, which is what the TEXT ordering depends on. It is
    -- not a calendar and it cannot know which zone a caller was thinking in;
    -- normalising to UTC happens in internal/store and only there.
    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (recorded_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    -- An anonymous correction cannot be traced back and re-examined, which is
    -- the whole of criterion RLN. NOT NULL refuses the absent case; this
    -- refuses the empty-string spelling of the same thing.
    CHECK (origin <> ''),
    -- NULL means "no note". The empty string would be a second spelling of it,
    -- and two spellings of one fact is what the store's `nullable` helper
    -- exists to prevent.
    CHECK (note IS NULL OR note <> '')
);

CREATE INDEX adjustment_review_span ON adjustment_review (started_at, ended_at);

-- One assertion about one span.
--
-- The six corrections the task names — confirm, trim, extend, split, delete,
-- reattribute — are not six row types. They reduce to five assertions, and the
-- reduction is what keeps this table small enough to reason about:
--
--   confirm      → 'engaged' over the span the model derived. It asserts the
--                  same thing as extend; what makes it a confirmation rather
--                  than a correction is that the model already agreed, which is
--                  recoverable by comparison and is not a fact about the row.
--   extend       → 'engaged' over a span the model missed.
--   trim         → 'not-engaged' over the part the model over-claimed.
--   delete       → 'not-engaged' over the whole of a sitting.
--   split        → 'not-engaged' where there was a real gap, which cuts the
--                  sitting in two and removes the gap from the total; or
--                  'boundary' at an instant where there was NO gap, which cuts
--                  it in two and changes no total at all. The first is almost
--                  always the honest one, because a split with no gap is a
--                  claim about work identity rather than about when work
--                  happened, and this tool measures the latter.
--   reattribute  → 'attributed' with a project, or 'unattributed' for a span
--                  asserted to belong to no project.
--
-- 'unattributed' is not redundant with an absent row, and that is exactly why
-- it exists. "This span was work, and it was nobody's project" is an
-- observation; "nobody has said whose it was" is not. 490,273 of the 809,610
-- events in the live store are unattributed for the second reason, and a
-- reattribution layer that could not tell them apart would turn every one of
-- them into a silent assertion nobody made.
CREATE TABLE adjustment (
    id INTEGER PRIMARY KEY,

    -- The review pass this came out of, if any. Nullable, because a correction
    -- can also be made directly — from `timetattle adjust`, or from an import —
    -- without anybody having opened a day. Not a required parent: forcing a
    -- review row into existence to record a single known correction would
    -- manufacture the claim that a whole span had been examined.
    review_id INTEGER REFERENCES adjustment_review (id),

    started_at TEXT NOT NULL,
    ended_at   TEXT NOT NULL,

    assertion TEXT NOT NULL CHECK (assertion IN
        ('engaged', 'not-engaged', 'boundary', 'attributed', 'unattributed')),

    -- The project named by an 'attributed' assertion, and NULL on every other.
    project TEXT,

    origin      TEXT NOT NULL,
    recorded_at TEXT NOT NULL,
    note        TEXT,

    -- Not strict, because 'boundary' is the one assertion about an instant.
    CHECK (ended_at >= started_at),

    -- And it is the ONLY one. Both sides are two-valued — assertion is NOT NULL
    -- so (assertion = 'boundary') is never NULL, and both endpoints are NOT
    -- NULL so the equality is never NULL — which is what makes an equality
    -- between two conditions safe here. This is the shape 0004 uses for
    -- CHECK ((ordinal = 0) = (gap_ms IS NULL)).
    --
    -- It refuses two things at once. A boundary across a span is not a
    -- boundary. A zero-width 'engaged' or 'not-engaged' asserts nothing, folds
    -- to nothing, and would sit in the table looking like a correction somebody
    -- made and the tool ignored.
    CHECK ((assertion = 'boundary') = (ended_at = started_at)),

    -- Both directions, and only one of them is obvious. An 'attributed' row
    -- with no project names nothing. A project on any other row is an
    -- attribution nothing reads — silently dropped, with no way for the person
    -- who typed it to know. Two-valued for the same reason as above:
    -- IS NOT NULL never returns NULL.
    CHECK ((assertion = 'attributed') = (project IS NOT NULL)),

    -- The same shape guard, and the same argument, as on adjustment_review
    -- above: width alone admits a 27-character non-UTC instant and the TEXT
    -- comparison a line above this one then compares two different layouts.
    CHECK (started_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (ended_at   GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),
    CHECK (recorded_at GLOB '[0-9][0-9][0-9][0-9]-[0-9][0-9]-[0-9][0-9]T[0-9][0-9]:[0-9][0-9]:[0-9][0-9].[0-9][0-9][0-9][0-9][0-9][0-9]Z'),

    CHECK (origin <> ''),
    CHECK (project IS NULL OR project <> ''),
    CHECK (note IS NULL OR note <> '')
);

-- The span index serves the day review, which asks for one day at a time.
CREATE INDEX adjustment_span ON adjustment (started_at, ended_at);

-- The fold order, stated as an index because it is the order the answer depends
-- on rather than a convenience. id breaks ties within a microsecond.
CREATE INDEX adjustment_fold_order ON adjustment (recorded_at, id);

CREATE INDEX adjustment_review_id ON adjustment (review_id);

-- A correction hanging off a review that remembered nothing is a
-- contradiction: the review says the span could not be recalled, and the row
-- says exactly what happened inside it. One of them is false and nothing in the
-- data says which.
--
-- This is a trigger rather than a CHECK because a CHECK sees only its own row
-- and this invariant spans two tables. It is the only cross-row rule here, and
-- it is one-directional — INSERT on adjustment only — because a review's
-- verdict cannot change afterwards: the append-only triggers below refuse the
-- UPDATE that would be needed to introduce the contradiction from the other
-- side.
CREATE TRIGGER adjustment_needs_a_review_that_remembers
BEFORE INSERT ON adjustment
WHEN NEW.review_id IS NOT NULL AND (
    SELECT verdict FROM adjustment_review WHERE id = NEW.review_id
) = 'unremembered'
BEGIN
    SELECT RAISE(ABORT, 'this review recorded that the span could not be remembered, '
        || 'so a correction cannot hang off it; record the correction on its own, '
        || 'or record a separate review that says the span was recalled');
END;

-- Append-only, enforced. Four triggers, and every one of them is a guard on a
-- mistake rather than on malice: a rebuild that forgot which tables it owned, a
-- script fixing a typo the honest way, a migration doing a UPDATE ... SET to
-- normalise something. Any of those silently destroys a judgment that cannot be
-- reconstructed from anything else in this database, because it was never
-- observed — it was remembered.
--
-- The way to change an adjustment is to record another one. The fold takes the
-- later of the two, the earlier stays visible, and criterion RLN's "records its
-- origin and time" therefore holds over history rather than only over the
-- latest state.
CREATE TRIGGER adjustment_is_never_updated
BEFORE UPDATE ON adjustment
BEGIN
    SELECT RAISE(ABORT, 'adjustments are append-only; record a new adjustment over the '
        || 'same span instead of editing this one, so that both judgments and their times survive');
END;

CREATE TRIGGER adjustment_is_never_deleted
BEFORE DELETE ON adjustment
BEGIN
    SELECT RAISE(ABORT, 'adjustments are never deleted; they outlive every derivation, '
        || 'which is what makes recomputing sessions safe');
END;

CREATE TRIGGER adjustment_review_is_never_updated
BEFORE UPDATE ON adjustment_review
BEGIN
    SELECT RAISE(ABORT, 'reviews are append-only; record a new review over the same span instead');
END;

CREATE TRIGGER adjustment_review_is_never_deleted
BEFORE DELETE ON adjustment_review
BEGIN
    SELECT RAISE(ABORT, 'reviews are never deleted; a review that happened is a fact about '
        || 'what has been examined, and deleting it makes an examined span look unexamined');
END;
