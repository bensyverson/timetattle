package migrations_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func tables(t *testing.T, db *sql.DB) map[string]bool {
	t.Helper()
	rows, err := db.Query(`SELECT name FROM sqlite_master WHERE type='table'`)
	if err != nil {
		t.Fatalf("list tables: %v", err)
	}
	defer rows.Close()

	out := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan table name: %v", err)
		}
		out[name] = true
	}
	return out
}

func TestApplyCreatesEveryTable(t *testing.T) {
	db := open(t)
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	got := tables(t, db)
	for _, want := range []string{"event", "harvest_run", "schema_migration"} {
		if !got[want] {
			t.Errorf("table %q was not created; got %v", want, got)
		}
	}
}

// TestApplyDropsTheVestigialRegistryTable is criterion uxI (task o9W0x):
// 0001 shipped a "registry" table (path, client, project, registered_at)
// that predates the marker-file design actually built — registration writes
// a marker file into the directory itself, with an index at
// ~/.timetattle/registered-directories.json (internal/registry). Nothing in
// this codebase reads or writes that SQL table, so it reads as authoritative
// schema for a storage mechanism the design abandoned. BUILD MODE means it
// is dropped outright rather than deprecated.
func TestApplyDropsTheVestigialRegistryTable(t *testing.T) {
	db := open(t)
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	if got := tables(t, db); got["registry"] {
		t.Error(`table "registry" still exists after Apply; it predates the marker-file design ` +
			"actually shipped and nothing reads or writes it — see internal/registry")
	}
}

// The assertions here count against Scripts() rather than a literal, so that
// adding a migration does not mean editing tests that are about the runner and
// not about the schema.
func TestApplyRecordsTheVersionItReached(t *testing.T) {
	db := open(t)
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	scripts, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}
	want := scripts[len(scripts)-1].Version

	var version int
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_migration`).Scan(&version); err != nil {
		t.Fatalf("read version: %v", err)
	}
	if version != want {
		t.Errorf("recorded version = %d, want %d, the newest embedded migration", version, want)
	}
}

// Apply runs on every start, so running it twice must be a no-op rather than an
// error. This is the property that lets the migration system be automatic.
func TestApplyIsIdempotent(t *testing.T) {
	db := open(t)
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("first Apply: %v", err)
	}
	if err := migrations.Apply(db); err != nil {
		t.Fatalf("second Apply: %v", err)
	}

	scripts, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}

	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migration`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != len(scripts) {
		t.Errorf("schema_migration holds %d rows after two Applies, want %d", applied, len(scripts))
	}
}

func TestPendingReportsWhatHasNotRun(t *testing.T) {
	db := open(t)

	scripts, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}

	before, err := migrations.Pending(db)
	if err != nil {
		t.Fatalf("Pending before: %v", err)
	}
	if len(before) != len(scripts) {
		t.Fatalf("Pending before Apply = %d migrations, want %d, every embedded one", len(before), len(scripts))
	}

	if err := migrations.Apply(db); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	after, err := migrations.Pending(db)
	if err != nil {
		t.Fatalf("Pending after: %v", err)
	}
	if len(after) != 0 {
		t.Errorf("Pending after Apply = %v, want none", after)
	}
}

// A migration that fails halfway must leave nothing behind, or the next start
// applies the remainder onto a half-built schema and the failure compounds.
func TestAFailedMigrationLeavesNoPartialSchema(t *testing.T) {
	db := open(t)
	if err := migrations.ApplyScripts(db, []migrations.Script{{
		Version: 1,
		Name:    "0001_broken.sql",
		SQL:     `CREATE TABLE good (id INTEGER); CREATE TABLE bad (this is not sql);`,
	}}); err == nil {
		t.Fatal("ApplyScripts on invalid SQL returned no error")
	}

	if tables(t, db)["good"] {
		t.Error("the first statement of a failed migration was committed; it must roll back")
	}
	var applied int
	if err := db.QueryRow(`SELECT COUNT(*) FROM schema_migration`).Scan(&applied); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if applied != 0 {
		t.Errorf("a failed migration recorded %d versions, want 0", applied)
	}
}
