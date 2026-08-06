package store_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/migrations"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// A READ-ONLY READER CANNOT MIGRATE, SO THE QUERY THAT NEEDS THE COLUMN SAYS SO.
//
// Migrations reach a database on a WRITABLE open and nowhere else, which is the
// design. The consequence is that between a schema change landing and the next
// writable open, every read-only tool in this repository meets the new column
// and fails — and it fails as a bare driver error thrown from whichever query
// touched it first: "SQL logic error: no such column: shown (1)", naming
// neither the store, nor the migration, nor what to do about it.
//
// Measured rather than supposed: adding migration 0011 on 2026-08-02 turned
// internal/fit's real-data suite red with exactly that message against
// ~/.timetattle/timetattle.db, which sits at 0010 and cannot be brought forward
// by anything that reads it.
//
// The guard is on the QUERY and not on OpenReadOnly, which is the shape
// corpus_test.go already pins for 0010's columns. A store at 0010 can still
// read every event in itself perfectly well, and refusing the whole handle
// would stop a reader that never touches a review.
func TestReadingReviewsOnAStoreThatPredatesTheColumnSaysSoRatherThanNamingIt(t *testing.T) {
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
		if s.Version <= 10 {
			upTo = append(upTo, s)
		}
	}
	if len(upTo) == len(all) {
		t.Fatal("0011 is no longer the newest migration; this test needs the version below it")
	}
	if err := migrations.ApplyScripts(db, upTo); err != nil {
		t.Fatalf("apply migrations up to 10: %v", err)
	}
	db.Close()

	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer st.Close()

	_, err = st.Reviews(context.Background(), time.Time{}, time.Time{})
	if err == nil {
		t.Fatal("Reviews read a store that cannot say what any of its reviewers were shown")
	}
	for _, want := range []string{"0011", "shown", "writing"} {
		if !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(want)) {
			t.Errorf("the failure does not mention %q: %v", want, err)
		}
	}
}
