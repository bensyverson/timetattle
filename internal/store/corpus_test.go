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
		Exclude: []string{"*.bz2", "*.gz", "*.log", "/Users/ben/git/Easley/reports/"},
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

// The mixture check, extended to the corpus. Two rows agreeing on every
// clustering parameter and disagreeing on what was excluded are two
// derivations, and reading them as one is how a reviewer comes to judge blocks
// drawn from a stale table against rows drawn from freshly filtered events.
func TestSittingsRefusesATableHoldingTwoCorpora(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	computed := sitBase.Format(store.TimeLayout)
	seedSittingsFrom(t, f, sitParams(), sittings.Corpus{}, sitBase, sitting(0, 60))

	err := f.ReplaceDerived(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// Identical in every clustering parameter and in the moment it was
		// computed. Only the exclude list differs.
		_, err := tx.ExecContext(ctx, `INSERT INTO derived_session
			(started_at, ended_at, events, sources, margin_micros, computed_at,
			 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources,
			 exclude_patterns, corpus_from, corpus_to)
			VALUES (?, ?, 1, 'biome-appusage', 0, ?, 900000000, 300000000, 300000000,
			        'knowledgec-backlit', '', '["*.log"]', '', '')`,
			sitAt(300).Format(store.TimeLayout), sitAt(360).Format(store.TimeLayout), computed)
		return err
	})
	if err != nil {
		t.Fatalf("write a row from a second corpus: %v", err)
	}

	if _, err := f.Sittings(ctx, time.Time{}, time.Time{}); err == nil {
		t.Fatal("Sittings read a table holding two corpora without complaint; " +
			"half these sittings were drawn from observations the other half never saw")
	} else if !strings.Contains(err.Error(), "sessionize") {
		t.Errorf("the complaint does not name the way out: %v", err)
	}
}

// The same for the window. A derivation over one month and a derivation over
// everything are different answers even when every parameter matches.
func TestSittingsRefusesATableHoldingTwoWindows(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	computed := sitBase.Format(store.TimeLayout)
	seedSittingsFrom(t, f, sitParams(), sittings.Corpus{}, sitBase, sitting(0, 60))

	err := f.ReplaceDerived(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `INSERT INTO derived_session
			(started_at, ended_at, events, sources, margin_micros, computed_at,
			 gap_micros, lead_in_micros, tail_out_micros, presence_sources, ignore_sources,
			 exclude_patterns, corpus_from, corpus_to)
			VALUES (?, ?, 1, 'biome-appusage', 0, ?, 900000000, 300000000, 300000000,
			        'knowledgec-backlit', '', '[]', ?, '')`,
			sitAt(300).Format(store.TimeLayout), sitAt(360).Format(store.TimeLayout), computed,
			sitAt(-600).Format(store.TimeLayout))
		return err
	})
	if err != nil {
		t.Fatalf("write a row from a second window: %v", err)
	}
	if _, err := f.Sittings(ctx, time.Time{}, time.Time{}); err == nil {
		t.Fatal("Sittings read a table holding two corpus windows without complaint")
	}
}

// A store that has not run 0010 yet has no corpus columns to read, and a
// read-only handle applies no migration. That is a THIRD thing, distinct from
// both "nothing has been derived" and "here is the derivation", and the failure
// has to name it: the rows in such a table were computed under an exclude list
// nobody recorded, and answering "excluded nothing" would be the exact guess
// 0010 exists to stop.
func TestSittingsOnAStoreThatPredatesTheCorpusSaysSoRatherThanNamingAColumn(t *testing.T) {
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
		t.Fatal("0010 is no longer the newest migration; this test needs the version below it")
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
		t.Fatal("Sittings read a derivation out of a store that never recorded what it excluded")
	}
	if !strings.Contains(err.Error(), "0010") || !strings.Contains(err.Error(), "sessionize") {
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
