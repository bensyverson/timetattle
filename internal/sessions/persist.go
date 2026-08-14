package sessions

import (
	"context"
	"database/sql"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/store"
)

// Writing the derivation: one derived_session_run row, and the sittings that
// hang off it.
//
// Only the write lives here, and it takes no handle at all: it is handed a
// transaction the store opened. Reading the tables back is Store.Sittings.

// Replace is the derivation that rebuilds derived_session from res.
//
// It returns the write rather than performing it, because the transaction
// belongs to the store: st.ReplaceDerived(ctx, sessions.Replace(res, at)) is
// the whole of a rebuild. That is what makes "either entirely this run's or
// entirely the previous run's" a property of internal/store rather than a
// discipline this file and internal/chains happen to share, having been written
// separately by people who could not see each other's work.
//
// DELETE then INSERT, never a merge, because a derivation is a whole answer: a
// table half computed at one gap and half at another is a number nobody could
// interpret and nothing would flag.
//
// It writes to derived_session and to nothing else. `event` and `harvest_run`
// come out byte-identical, which is task J04gF's criterion Ybu and is what
// makes recomputing free rather than a thing to be careful about.
//
// computedAt is a parameter rather than a call to time.Now, so that the whole
// derivation is deterministic and "a second run produces identical rows" is a
// comparison rather than a claim with an exception in it. Callers pass the
// wall clock; tests pass a fixed instant.
//
// corpus is which observations the derivation was drawn FROM: the exclude
// patterns that were applied where the events were read, and the window they
// were read over. It is a separate argument rather than a field of Result
// because Cluster never sees it — Retain has already applied it — and a field
// of Result that Cluster did not produce would be a lie about where the value
// came from. The caller that filtered the events is the one that knows.
func Replace(res Result, corpus Corpus, computedAt time.Time) store.Derivation {
	return func(ctx context.Context, tx *sql.Tx) error {
		if err := res.Params.Validate(); err != nil {
			return fmt.Errorf("refusing to store sessions derived with unusable parameters: %w", err)
		}
		if err := corpus.Validate(); err != nil {
			return fmt.Errorf("refusing to store sessions drawn from a corpus that cannot exist: %w", err)
		}

		// ONE delete, and it is the run row rather than the sittings.
		// derived_session.run_id references it ON DELETE CASCADE, so this takes
		// every sitting with it — which is what makes "either entirely this
		// derivation or entirely the previous one" impossible to half-do. Two
		// deletes could be written with one of them forgotten; this one cannot.
		if _, err := tx.ExecContext(ctx, `DELETE FROM derived_session_run`); err != nil {
			return fmt.Errorf("clear the stored derivation: %w", err)
		}

		var (
			at       = formatTime(computedAt)
			presence = joinSorted(res.Params.PresenceSources)
			ignore   = joinSorted(res.Params.IgnoreSources)
			// The canonical form, so that two runs configured with the same
			// patterns in a different order store the same bytes and
			// Corpus.Equal agrees with the storage rather than with itself.
			exclude    = corpus.ExcludeJSON()
			corpusFrom = formatBound(corpus.From)
			corpusTo   = formatBound(corpus.To)
			st         = res.Stats
		)

		// The run row is written whether or not the derivation produced a single
		// sitting, and that is the point of it. With the parameters restated on
		// every session row, a derivation that clustered nothing wrote nothing at
		// all and read back exactly like a store nobody had ever sessionized —
		// this project's oldest bug, reachable from the command line with
		// `sessionize -from <after the last event>`.
		//
		// darkness_policy records the RULE rather than a parameter: nothing
		// configures it, and it is about to change. See
		// sittings.CurrentDarknessPolicy, which must be changed with the rule.
		if _, err := tx.ExecContext(ctx, `INSERT INTO derived_session_run
			(id, computed_at, gap_micros, lead_in_micros, tail_out_micros,
			 presence_sources, ignore_sources, darkness_policy,
			 exclude_patterns, corpus_from, corpus_to,
			 evidence_observations, presence_observations, ignored_observations,
			 unbounded_observations, presence_without_span, merged_by_expansion,
			 split_by_absence, trimmed_by_absence_micros, suppressed_by_absence,
			 unevidenced_pieces, unevidenced_dropped_micros,
			 contested_micros, observed_absence_micros)
			VALUES (1, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			at, micros(res.Params.Gap), micros(res.Params.LeadIn), micros(res.Params.TailOut),
			presence, ignore, string(sittings.CurrentDarknessPolicy),
			exclude, corpusFrom, corpusTo,
			st.Evidence, st.Presence, st.Ignored,
			st.Unbounded, st.PresenceWithoutSpan, st.MergedByExpansion,
			st.SplitByAbsence, micros(st.TrimmedByAbsence), st.SuppressedByAbsence,
			st.UnevidencedPieces, micros(st.UnevidencedDropped),
			micros(st.Contested), micros(st.ObservedAbsence),
		); err != nil {
			return fmt.Errorf("store the derivation computed at %s: %w", at, err)
		}

		stmt, err := tx.PrepareContext(ctx, `INSERT INTO derived_session
			(run_id, started_at, ended_at, events, sources, margin_micros)
			VALUES (1, ?, ?, ?, ?, ?)`)
		if err != nil {
			return fmt.Errorf("prepare the session insert: %w", err)
		}
		defer stmt.Close()

		for _, s := range res.Sessions {
			// Asserted here rather than in SQL: SQLite's date functions resolve
			// to milliseconds at best while these endpoints carry microseconds,
			// so the CHECK would need a second of slack and would catch nothing.
			// See 0003_derived_session.sql.
			if s.Margin > s.Duration() {
				return fmt.Errorf("session %s..%s claims %v of margin across %v; the derivation is wrong",
					formatTime(s.Start), formatTime(s.End), s.Margin, s.Duration())
			}
			if _, err := stmt.ExecContext(ctx,
				formatTime(s.Start), formatTime(s.End), s.Events, strings.Join(s.Sources, ","),
				micros(s.Margin),
			); err != nil {
				return fmt.Errorf("store the session at %s: %w", formatTime(s.Start), err)
			}
		}
		return nil
	}
}

// Reading them back is Store.Sittings, not a function here.
//
// This file used to carry a Stored(ctx, *sql.DB) beside Replace, which meant a
// caller wanting the sittings had to be handed a raw handle on the store — the
// exact seam cmd/timetattle's TestNoCommandReachesPastTheStoreToTheDatabase
// exists to close, and a second reader of derived_session's columns to keep in
// step with this writer. The store returns sittings.Derived, which is the same
// Params and the same Sitting this package clusters into, so the round trip is
// a comparison rather than a translation.

// micros renders a duration at the resolution the store's timestamps carry.
// Seconds would make a stored session disagree with the same session
// recomputed, silently and for ever.
func micros(d time.Duration) int64 { return int64(d / time.Microsecond) }

// joinSorted renders a source list so that two runs configured with the same
// sources in a different order store the same bytes.
func joinSorted(ids []string) string {
	sorted := append([]string(nil), ids...)
	slices.Sort(sorted)
	return strings.Join(sorted, ",")
}

// formatTime uses internal/store's layout rather than a copy of it. The fixed
// width is what makes the schema's CHECK (ended_at >= started_at) a
// chronological comparison instead of a lexical accident.
func formatTime(at time.Time) string { return at.UTC().Format(store.TimeLayout) }

// formatBound renders one end of the corpus window, and writes the empty string
// for an unbounded one.
//
// The empty string is a VALUE and not a missing entry: it says the events were
// read with no bound at that end. It is used rather than a NULL because the
// column takes part in a CHECK, and a CHECK passes when it evaluates to NULL —
// a nullable bound would have made the shape guard on it enforce nothing at
// all. The zero time cannot collide with it: it renders as year 1, which is a
// 27-character instant and not an empty string.
func formatBound(at time.Time) string {
	if at.IsZero() {
		return ""
	}
	return formatTime(at)
}
