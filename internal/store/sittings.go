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
// That is why the derivation is read across the WHOLE table and the sittings
// only within the window. Reading the parameters from the windowed rows would
// make an empty Sunday indistinguishable from an empty database.

import (
	"context"
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
// It refuses a table whose rows disagree about the derivation. That cannot
// happen through ReplaceDerived, which rebuilds wholesale inside one
// transaction; it can happen through a hand-written INSERT, and a mixture reads
// as a perfectly ordinary set of sittings with half of them measured against a
// different model. Derived can carry one Params, so the alternative to refusing
// is returning one of the two as though it described all the rows.
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

// derivation reads the parameters every row was derived under, from the whole
// table rather than from a window, and refuses a table holding more than one.
//
// The parameters are restated on every row deliberately — migration 0003 says
// why at length: a single shared parameter row would be tidier and would make
// it possible to produce a session span separated from its parameters, which is
// a span with no meaning. The cost is that they have to be reconciled on the
// way out, which is this.
func (s *Store) derivation(ctx context.Context) (sittings.Derived, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT computed_at, gap_micros,
		lead_in_micros, tail_out_micros, presence_sources, ignore_sources,
		exclude_patterns, corpus_from, corpus_to
		FROM derived_session ORDER BY computed_at`)
	if err != nil {
		// A store that has not run 0010 has no corpus columns, and a read-only
		// handle applies no migration. That is a third state — not "nothing
		// derived", not "here is the derivation" — and it deserves its own
		// sentence rather than a complaint naming a column. The version is
		// looked up only on the failure path, so the ordinary read pays
		// nothing for it.
		if schema, serr := s.Schema(ctx); serr == nil && schema.Version < 10 {
			return sittings.Derived{}, fmt.Errorf(
				"%s is at migration %d, and the corpus a derivation was drawn from "+
					"(the exclude list, and the window) arrived in 0010. Its stored sittings "+
					"were computed under an exclude list nobody recorded, so they cannot be "+
					"read as having excluded nothing; open the store for writing once to "+
					"migrate it, then rebuild with `timetattle sessionize`",
				s.path, schema.Version)
		}
		return sittings.Derived{}, fmt.Errorf("read the derivation in %s: %w", s.path, err)
	}
	defer rows.Close()

	var (
		out   sittings.Derived
		found bool
	)
	for rows.Next() {
		var (
			computed                       string
			gapUS, leadUS, tailUS          int64
			presenceSources, ignoreSources string
			exclude, corpusFrom, corpusTo  string
		)
		if err := rows.Scan(&computed, &gapUS, &leadUS, &tailUS, &presenceSources, &ignoreSources,
			&exclude, &corpusFrom, &corpusTo); err != nil {
			return sittings.Derived{}, fmt.Errorf("read the derivation in %s: %w", s.path, err)
		}
		at, err := parseStoredTime(computed)
		if err != nil {
			return sittings.Derived{}, err
		}
		p := sittings.Params{
			Gap:             time.Duration(gapUS) * time.Microsecond,
			LeadIn:          time.Duration(leadUS) * time.Microsecond,
			TailOut:         time.Duration(tailUS) * time.Microsecond,
			PresenceSources: splitSources(presenceSources),
			IgnoreSources:   splitSources(ignoreSources),
		}
		c, err := s.corpus(exclude, corpusFrom, corpusTo)
		if err != nil {
			return sittings.Derived{}, err
		}
		if !found {
			out.Params, out.Corpus, out.ComputedAt, found = p, c, at, true
			continue
		}
		if !out.Params.Equal(p) || !out.ComputedAt.Equal(at) {
			return sittings.Derived{}, fmt.Errorf(
				"derived_session in %s holds rows from more than one derivation "+
					"(%s at a gap of %v, and %s at a gap of %v); "+
					"rebuild it with `timetattle sessionize` rather than reading a mixture",
				s.path, formatTime(out.ComputedAt), out.Params.Gap, formatTime(at), p.Gap)
		}
		// The corpus is checked separately so the complaint can name what
		// actually differs. Two rows agreeing on every parameter and
		// disagreeing on what was excluded are still two derivations, and the
		// gap in the message above would be identical in both — which is how a
		// reader comes to believe the table is one answer when half of it was
		// drawn from observations the other half never saw.
		if !out.Corpus.Equal(c) {
			return sittings.Derived{}, fmt.Errorf(
				"derived_session in %s holds rows drawn from more than one corpus "+
					"(one excluding %v over %s, another excluding %v over %s); "+
					"rebuild it with `timetattle sessionize` rather than reading a mixture",
				s.path, out.Corpus.Canonical().Exclude, describeWindow(out.Corpus),
				c.Canonical().Exclude, describeWindow(c))
		}
	}
	return out, rows.Err()
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
