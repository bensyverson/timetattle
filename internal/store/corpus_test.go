package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/migrations"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/store"
)

// Reading back the corpus a derivation was drawn FROM, beside the parameters it
// was drawn WITH.
//
// The exclude list is applied where events are read, so it decides which
// observations exist at all. Adding four patterns moved total presence from
// 1193.2h to 1067.0h on the live corpus — more than most gap changes — and
// until this was stored, a derivation built under one list came back
// indistinguishable from one built under another.

func sitCorpus() sittings.Corpus {
	return sittings.Corpus{
		Exclude: []string{"*.bz2", "*.gz", "*.log", "/Users/you/git/Easley/reports/"},
		From:    sitAt(-600),
	}
}

// seedSittingsFrom is seedSittings with the corpus named, through the same path
// `timetattle sessionize` uses.
func seedSittingsFrom(t *testing.T, f fixture, p sittings.Params, c sittings.Corpus,
	computedAt time.Time, ss ...sittings.Sitting) {
	t.Helper()
	res := sessions.Result{Params: p, Sessions: ss}
	if err := f.ReplaceDerived(context.Background(), sessions.Replace(res, c, computedAt)); err != nil {
		t.Fatalf("seed the derivation: %v", err)
	}
}

func TestTheCorpusIsRecoverableFromTheStoredRows(t *testing.T) {
	f := newStore(t)
	computed := time.Date(2026, 8, 1, 13, 32, 33, 0, time.UTC)
	seedSittingsFrom(t, f, sitParams(), sitCorpus(), computed, sitting(0, 60))

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if !got.Corpus.Equal(sitCorpus()) {
		t.Errorf("the corpus read back as %+v, want %+v", got.Corpus, sitCorpus())
	}
	if !got.Params.Equal(sitParams()) {
		t.Errorf("the parameters read back as %+v, want %+v", got.Params, sitParams())
	}
}

// Excluding nothing is a fact, and it has to survive the round trip as one.
func TestADerivationThatExcludedNothingSaysSoRatherThanSayingNothing(t *testing.T) {
	f := newStore(t)
	seedSittingsFrom(t, f, sitParams(), sittings.Corpus{}, sitBase, sitting(0, 60))

	got, err := f.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if !got.Corpus.Equal(sittings.Corpus{}) {
		t.Errorf("a derivation that excluded nothing read back as %+v", got.Corpus)
	}
	// And the state it must never be confused with: a table nobody has derived
	// into at all reports no derivation, not a derivation that excluded nothing.
	empty := newStore(t)
	none, err := empty.Sittings(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings on an empty table: %v", err)
	}
	if !none.ComputedAt.IsZero() {
		t.Error("an underived table claimed a derivation")
	}
}

// The mixture check, extended to the corpus — now stated as what the SCHEMA
// refuses rather than as what the reader catches.
//
// Two rows agreeing on every clustering parameter and disagreeing on what was
// excluded were two derivations, and reading them as one was how a reviewer
// came to judge blocks drawn from a stale table against rows drawn from freshly
// filtered events. Sittings used to reconcile that on the way out, across every
// row, on every read.
//
// Migration 0016 removed the need: the corpus is on the singleton derivation
// row, so a second corpus cannot be written beside the first at all. Both cases
// the two old tests covered — a second exclude list, and a second window — are
// kept here, because what changed is the mechanism and not the property.
func TestASecondCorpusCannotBeWrittenBesideTheFirst(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	computed := sitBase.Format(store.TimeLayout)
	seedSittingsFrom(t, f, sitParams(), sittings.Corpus{}, sitBase, sitting(0, 60))

	for _, c := range []struct {
		name          string
		exclude, from string
	}{
		{"a second exclude list", `["*.log"]`, ""},
		{"a second window", "[]", sitAt(-600).Format(store.TimeLayout)},
	} {
		// Identical in every clustering parameter and in the moment it was
		// computed. Only the corpus differs.
		err := f.ReplaceDerived(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `INSERT INTO derived_session_run
				(id, computed_at, gap_micros, lead_in_micros, tail_out_micros,
				 presence_sources, ignore_sources, darkness_policy,
				 exclude_patterns, corpus_from, corpus_to,
				 evidence_observations, presence_observations, ignored_observations,
				 unbounded_observations, presence_without_span, merged_by_expansion,
				 split_by_absence, trimmed_by_absence_micros, suppressed_by_absence,
				 unevidenced_pieces, unevidenced_dropped_micros,
				 contested_micros, observed_absence_micros)
				VALUES (2, ?, 900000000, 300000000, 300000000, 'knowledgec-backlit', '',
				        'suppress-all-and-trim', ?, ?, '',
				        0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)`,
				computed, c.exclude, c.from)
			return err
		})
		if err == nil {
			t.Errorf("%s was written beside the first; half these sittings would have been "+
				"drawn from observations the other half never saw", c.name)
		}
	}

	// And the derivation that is there is still the one that was written, rather
	// than something the refused writes left behind.
	got, err := f.Sittings(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Sittings: %v", err)
	}
	if !got.Corpus.Equal(sittings.Corpus{}) || len(got.Sittings) != 1 {
		t.Errorf("after two refused writes the store holds %d sittings drawn from %+v",
			len(got.Sittings), got.Corpus)
	}
}

// A store that has not run the migration the derivation lives in has no row to
// read, and a read-only handle applies no migration. That is a THIRD thing,
// distinct from both "nothing has been derived" and "here is the derivation",
// and the failure has to name it.
//
// The migration named moved from 0010 to 0016 when the derivation moved to its
// own row, and the reason is the same one in a stronger form: a store below
// 0010 recorded no exclude list, and a store below 0016 also recorded nothing
// about what the derivation DID. Answering "excluded nothing, suppressed
// nothing, trimmed nothing" to either would be the exact guess both migrations
// exist to stop.
func TestSittingsOnAStoreThatPredatesTheDerivationRowSaysSoRatherThanNamingATable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	all, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}
	var upTo []migrations.Script
	for _, s := range all {
		if s.Version <= 9 {
			upTo = append(upTo, s)
		}
	}
	if len(upTo) == len(all) {
		t.Fatal("there is no migration above 9; this test needs a store below the current schema")
	}
	if err := migrations.ApplyScripts(db, upTo); err != nil {
		t.Fatalf("apply migrations up to 9: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO derived_session
		(started_at, ended_at, events, sources, margin_micros, computed_at,
		 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources)
		VALUES ('2026-07-11T09:00:00.000000Z', '2026-07-11T10:00:00.000000Z', 1, 'biome', 0,
		        '2026-07-11T11:00:00.000000Z', 900000000, 0, 0, '', '')`); err != nil {
		t.Fatalf("seed a derivation at 0009: %v", err)
	}
	db.Close()

	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer st.Close()

	_, err = st.Sittings(context.Background(), time.Time{}, time.Time{})
	if err == nil {
		t.Fatal("Sittings read a derivation out of a store that never recorded what it excluded " +
			"or what it did")
	}
	if !strings.Contains(err.Error(), "0016") || !strings.Contains(err.Error(), "sessionize") {
		t.Errorf("the failure names neither the migration nor the way out: %v", err)
	}
}

// Replace refuses to store a derivation whose corpus could not have held
// anything, for the same reason it refuses unusable parameters: the rows would
// be a record of something that cannot have happened.
func TestReplaceRefusesACorpusWhoseWindowEndsBeforeItBegins(t *testing.T) {
	f := newStore(t)
	res := sessions.Result{Params: sitParams(), Sessions: []sittings.Sitting{sitting(0, 60)}}
	bad := sittings.Corpus{From: sitAt(600), To: sitAt(0)}
	if err := f.ReplaceDerived(context.Background(), sessions.Replace(res, bad, sitBase)); err == nil {
		t.Error("a derivation drawn from a window that ends before it begins was stored")
	}
}
