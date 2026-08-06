package store_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// `timetattle status` must not change the thing it is checking. A check that
// migrated the database it was inspecting would report a schema no harvest had
// ever run against, and a check that CREATED the database would report a
// healthy store on a machine where the harvest has never once succeeded — the
// same class of mistake the command exists to catch.
//
// That is why status opened its own read-only handle rather than calling
// store.Open, and why the fix is a read-only Open here rather than reusing the
// writing one.

// TestOpenReadOnlyDoesNotCreateTheStore is the refusal that matters most: on a
// machine where nothing has ever harvested, there is no file, and there must
// still be no file afterwards.
func TestOpenReadOnlyDoesNotCreateTheStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "absent", "timetattle.db")

	st, err := store.OpenReadOnly(path)
	if err == nil {
		st.Close()
		t.Fatal("OpenReadOnly on a path with no store = nil, want an error")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("OpenReadOnly created %s; a status check must not create the store it reports on", path)
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("OpenReadOnly created the directory %s", filepath.Dir(path))
	}
}

// TestOpenReadOnlyDoesNotMigrate. The database here is a real SQLite file with
// no Timetattle schema in it at all. store.Open would bring it up to date;
// OpenReadOnly must leave it exactly as it found it, because the schema it is
// asked to report is the schema a harvest last ran against.
func TestOpenReadOnlyDoesNotMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "unmigrated.db")
	seed, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	if _, err := seed.Exec(`CREATE TABLE marker (x INTEGER)`); err != nil {
		t.Fatalf("write a marker table: %v", err)
	}
	if err := seed.Close(); err != nil {
		t.Fatalf("close the seed handle: %v", err)
	}

	ro, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", path, err)
	}
	defer ro.Close()
	// Asked for something, so that a lazy connection is actually made and any
	// migration a wrong implementation ran would have run by now.
	if _, err := ro.Schema(context.Background()); err == nil {
		t.Error("Schema() = nil error on a database with no schema_migration; it was migrated")
	}

	check, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatalf("reopen %s: %v", path, err)
	}
	defer check.Close()
	var tables int
	if err := check.QueryRow(
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name != 'marker'`).Scan(&tables); err != nil {
		t.Fatalf("count the tables: %v", err)
	}
	if tables != 0 {
		t.Errorf("OpenReadOnly left %d tables beyond the marker; it migrated the database it was reading", tables)
	}
}

// TestOpenReadOnlyReadsWhatAHarvestWrote. Read-only is worth nothing if it
// cannot answer the question, so the accessors have to work through it.
func TestOpenReadOnlyReadsWhatAHarvestWrote(t *testing.T) {
	f := newStore(t)
	began(t, f, "biome-appusage", ok(t))

	ro, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", f.path, err)
	}
	defer ro.Close()

	got, err := ro.Runs(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Runs through a read-only store: %v", err)
	}
	if len(got) != 1 || got[0].SourceID != "biome-appusage" {
		t.Fatalf("Runs through a read-only store returned %d rows, want the one recorded run", len(got))
	}
}

// TestSchemaReportsTheMigrationTheStoreWasBroughtUpTo. This is the fact that
// diagnosed the incident status was written for: a store still on 0001 days
// after 0002 landed had not been opened by a successful harvest since.
func TestSchemaReportsTheMigrationTheStoreWasBroughtUpTo(t *testing.T) {
	f := newStore(t)

	got, err := f.Schema(context.Background())
	if err != nil {
		t.Fatalf("Schema(): %v", err)
	}
	if got.Version <= 0 {
		t.Errorf("Schema().Version = %d, want the newest applied migration", got.Version)
	}
	if got.Name == "" {
		t.Error("Schema().Name is empty; a version number alone does not say which migration ran")
	}
	if got.AppliedAt.IsZero() {
		t.Error("Schema().AppliedAt is the zero time; when it was migrated is the whole diagnostic")
	}

	// The newest, not the oldest. A store that has run every migration must not
	// report the first one.
	var highest int
	if err := f.raw(t).QueryRow(`SELECT MAX(version) FROM schema_migration`).Scan(&highest); err != nil {
		t.Fatalf("read the ledger directly: %v", err)
	}
	if got.Version != highest {
		t.Errorf("Schema().Version = %d, want the highest applied %d", got.Version, highest)
	}
}
