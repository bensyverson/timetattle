package store

// Reading derived_session back: the sittings, and the derivation they came out
// of.
//
// The write is still internal/sessions.Replace, handed here as a Derivation,
// because which tables a derivation owns is exactly what this package does not
// know. The READ is here rather than there for the reason every other read is:
// a caller that has to be given a *sql.DB to ask a question can open the store
// behind this package's back, and three separate agents did precisely that
// before cmd/timetattle's seam test existed.
//
// # Three states, not two
//
// A window with no sittings in it and a database nobody has ever sessionized
// are different facts, and the difference is the whole reason Derived carries
// ComputedAt. A zero ComputedAt means nothing has been derived at all; a set
// ComputedAt with no sittings means the model ran and found none in the window
// asked about. Collapsing those two is the bug this project has shipped three
// times in three separate tools, always in the direction of reporting work as
// absent because nobody looked.
//
// That is why the derivation is read from its own row and the sittings only
// within the window. Until migration 0016 the derivation was restated on every
// session row and had to be read across the whole table instead — which almost
// worked, and lost the case a run row makes plain: a derivation that produced
// NO sittings wrote no rows, and so reported itself as no derivation at all.
// `timetattle sessionize -from <after the last event>` reaches it.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
)

// Sittings reads the sittings overlapping from..to, with the parameters they
// were derived under, the corpus they were drawn from, and the moment they
// were derived.
//
// The corpus is the third of those and the newest: which observations the
// derivation was allowed to see. It matters as much as the parameters do —
// Ben's four exclude patterns move total presence over the live store by
// 126.2h — and until migration 0010 it was not recorded, so a table built under
// one exclude list read back identically to one built under another.
//
// Either endpoint may be the zero time to mean unbounded, and the window is by
// OVERLAP rather than containment: a sitting that began before it and ended
// inside it describes time during it.
//
// A table holding two derivations no longer has to be refused, because it can
// no longer be written: migration 0016 put the derivation on one row under
// `CHECK (id = 1)`, and every sitting references it. Reconciling a mixture on
// the way out — which is what this did across 1557 rows on every read — was the
// cost of the parameters being restated, and the mixture it caught was one only
// a hand-written INSERT could produce.
func (s *Store) Sittings(ctx context.Context, from, to time.Time) (sittings.Derived, error) {
	out, err := s.derivation(ctx)
	if err != nil {
		return sittings.Derived{}, err
	}
	// Nothing has been derived. There are no rows to window and no parameters
	// to report, and saying so is not the same as saying the window was empty.
	if out.ComputedAt.IsZero() {
		return out, nil
	}

	where, args := spanWhere(from, to)
	rows, err := s.db.QueryContext(ctx, `SELECT started_at, ended_at, events, sources, margin_micros
		FROM derived_session`+where+` ORDER BY started_at`, args...)
	if err != nil {
		return sittings.Derived{}, fmt.Errorf("read sittings from %s: %w", s.path, err)
	}
	defer rows.Close()

	for rows.Next() {
		var (
			sit                     sittings.Sitting
			started, ended, sources string
			marginUS                int64
		)
		if err := rows.Scan(&started, &ended, &sit.Events, &sources, &marginUS); err != nil {
			return sittings.Derived{}, fmt.Errorf("read a sitting from %s: %w", s.path, err)
		}
		if sit.Start, err = parseStoredTime(started); err != nil {
			return sittings.Derived{}, err
		}
		if sit.End, err = parseStoredTime(ended); err != nil {
			return sittings.Derived{}, err
		}
		sit.Margin = time.Duration(marginUS) * time.Microsecond
		sit.Sources = splitSources(sources)
		out.Sittings = append(out.Sittings, sit)
	}
	return out, rows.Err()
}

// derivation reads the one row that says what this store's sittings were
// derived with, drawn from, and what the derivation did.
//
// Migration 0016 made this one row. Before it the nine columns were restated on
// every session row, which meant a SELECT DISTINCT across 1557 rows on every
// read and a reconciliation that had to refuse a table disagreeing with itself.
// Both are gone: `CHECK (id = 1)` makes a second derivation unrepresentable, and
// derived_session.run_id references this row, so a sitting cannot exist without
// it.
//
// NO ROW means nothing has been derived. That is the only thing it can mean now,
// and it is a stronger statement than it was: a derivation that produced no
// sittings still writes this row, so "the model ran and found none" is a row and
// no longer an absence.
func (s *Store) derivation(ctx context.Context) (sittings.Derived, error) {
	var (
		out                            sittings.Derived
		computed                       string
		gapUS, leadUS, tailUS          int64
		presenceSources, ignoreSources string
		policy                         string
		exclude, corpusFrom, corpusTo  string
		st                             sittings.Stats
		trimmedUS, droppedUS           int64
		contestedUS, absenceUS         int64
	)
	err := s.db.QueryRowContext(ctx, `SELECT computed_at, gap_micros,
		lead_in_micros, tail_out_micros, presence_sources, ignore_sources, darkness_policy,
		exclude_patterns, corpus_from, corpus_to,
		evidence_observations, presence_observations, ignored_observations,
		unbounded_observations, presence_without_span, merged_by_expansion,
		split_by_absence, trimmed_by_absence_micros, suppressed_by_absence,
		unevidenced_pieces, unevidenced_dropped_micros,
		contested_micros, observed_absence_micros
		FROM derived_session_run WHERE id = 1`).
		Scan(&computed, &gapUS, &leadUS, &tailUS, &presenceSources, &ignoreSources, &policy,
			&exclude, &corpusFrom, &corpusTo,
			&st.Evidence, &st.Presence, &st.Ignored,
			&st.Unbounded, &st.PresenceWithoutSpan, &st.MergedByExpansion,
			&st.SplitByAbsence, &trimmedUS, &st.SuppressedByAbsence,
			&st.UnevidencedPieces, &droppedUS,
			&contestedUS, &absenceUS)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// Nothing has been derived. The zero Derived says so, and saying so is
		// not the same as saying the window was empty.
		return sittings.Derived{}, nil
	case err != nil:
		// A store that has not run 0016 has no such table, and a read-only
		// handle applies no migration. That is a third state — not "nothing
		// derived", not "here is the derivation" — and it deserves its own
		// sentence rather than a complaint naming a table. The version is
		// looked up only on the failure path, so the ordinary read pays
		// nothing for it.
		if schema, serr := s.Schema(ctx); serr == nil && schema.Version < 16 {
			return sittings.Derived{}, fmt.Errorf(
				"%s is at migration %d, and what a derivation DID — how much time the "+
					"darkness rule trimmed, how many observations it suppressed, and under "+
					"which rule — arrived in 0016. Its stored sittings recorded only what "+
					"they were given, so those figures cannot be read as zero; open the "+
					"store for writing once to migrate it, then rebuild with "+
					"`timetattle sessionize`",
				s.path, schema.Version)
		}
		return sittings.Derived{}, fmt.Errorf("read the derivation in %s: %w", s.path, err)
	}

	at, err := parseStoredTime(computed)
	if err != nil {
		return sittings.Derived{}, err
	}
	c, err := s.corpus(exclude, corpusFrom, corpusTo)
	if err != nil {
		return sittings.Derived{}, err
	}
	st.TrimmedByAbsence = time.Duration(trimmedUS) * time.Microsecond
	st.UnevidencedDropped = time.Duration(droppedUS) * time.Microsecond
	st.Contested = time.Duration(contestedUS) * time.Microsecond
	st.ObservedAbsence = time.Duration(absenceUS) * time.Microsecond

	out.ComputedAt = at
	out.Corpus = c
	out.Stats = st
	out.DarknessPolicy = sittings.DarknessPolicy(policy)
	out.Params = sittings.Params{
		Gap:             time.Duration(gapUS) * time.Microsecond,
		LeadIn:          time.Duration(leadUS) * time.Microsecond,
		TailOut:         time.Duration(tailUS) * time.Microsecond,
		PresenceSources: splitSources(presenceSources),
		IgnoreSources:   splitSources(ignoreSources),
	}
	return out, nil
}

// corpus reads the three stored corpus columns back.
//
// Every one of them is NOT NULL in the schema, so there is no "nobody recorded
// this" arm to write here — that state is the absence of rows, which Sittings
// reports through a zero ComputedAt. A store that predates migration 0010 does
// not reach this code: the column does not exist and the query above fails by
// name, loudly, rather than reporting a derivation that excluded nothing.
func (s *Store) corpus(exclude, from, to string) (sittings.Corpus, error) {
	patterns, err := sittings.ParseExcludeJSON(exclude)
	if err != nil {
		return sittings.Corpus{}, fmt.Errorf("read the corpus in %s: %w", s.path, err)
	}
	c := sittings.Corpus{Exclude: patterns}
	if from != "" {
		if c.From, err = parseStoredTime(from); err != nil {
			return sittings.Corpus{}, err
		}
	}
	if to != "" {
		if c.To, err = parseStoredTime(to); err != nil {
			return sittings.Corpus{}, err
		}
	}
	return c, nil
}

// describeWindow renders a corpus window for a complaint, naming an unbounded
// end as unbounded rather than printing year 1.
func describeWindow(c sittings.Corpus) string {
	from, to := "the beginning", "now"
	if !c.From.IsZero() {
		from = formatTime(c.From)
	}
	if !c.To.IsZero() {
		to = formatTime(c.To)
	}
	return from + ".." + to
}

// splitSources reads the comma-separated, sorted source list the derivation
// writes. Empty is none, and none is a legal answer rather than a missing one.
func splitSources(list string) []string {
	if list == "" {
		return nil
	}
	return strings.Split(list, ",")
}
