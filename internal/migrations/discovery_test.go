package migrations_test

import (
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// The embedded scripts are the only thing that says what the schema is, so the
// discovery of them is worth testing directly: a migration that is present on
// disk but invisible to the runner is a schema change that silently never
// happens.
func TestScriptsComeFromTheEmbeddedFiles(t *testing.T) {
	scripts, err := migrations.Scripts()
	if err != nil {
		t.Fatalf("Scripts: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("Scripts found nothing; the embedded SQL is not being read")
	}
	if scripts[0].Version != 1 || scripts[0].Name != "0001_initial.sql" {
		t.Errorf("first script is %d/%q, want 1/\"0001_initial.sql\"", scripts[0].Version, scripts[0].Name)
	}
	if scripts[0].SQL == "" {
		t.Error("first script carries no SQL")
	}
	for i, s := range scripts {
		if i > 0 && s.Version <= scripts[i-1].Version {
			t.Errorf("script %d is version %d, which does not follow version %d; "+
				"discovery must order by the numeric prefix", i, s.Version, scripts[i-1].Version)
		}
	}
}

// Ordering is by version and not by the order a caller happens to supply,
// because a later migration routinely depends on an earlier one and the cost of
// getting it wrong is a half-built schema.
func TestApplyScriptsRunsInVersionOrderWhateverOrderItIsGiven(t *testing.T) {
	db := open(t)
	err := migrations.ApplyScripts(db, []migrations.Script{
		{Version: 2, Name: "0002_second.sql", SQL: `ALTER TABLE first ADD COLUMN note TEXT;`},
		{Version: 1, Name: "0001_first.sql", SQL: `CREATE TABLE first (id INTEGER);`},
	})
	if err != nil {
		t.Fatalf("ApplyScripts out of order: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO first (id, note) VALUES (1, 'ok')`); err != nil {
		t.Errorf("the second migration did not reach a table the first created: %v", err)
	}
}

// Two scripts claiming one version is what two branches adding a migration
// looks like. Whichever ran would depend on iteration order, so it is refused.
func TestApplyScriptsRefusesTwoScriptsWithTheSameVersion(t *testing.T) {
	db := open(t)
	err := migrations.ApplyScripts(db, []migrations.Script{
		{Version: 1, Name: "0001_a.sql", SQL: `CREATE TABLE a (id INTEGER);`},
		{Version: 1, Name: "0001_b.sql", SQL: `CREATE TABLE b (id INTEGER);`},
	})
	if err == nil {
		t.Fatal("ApplyScripts accepted two scripts at version 1")
	}
	if tables(t, db)["a"] || tables(t, db)["b"] {
		t.Error("a rejected set of scripts was partly applied")
	}
}

// A migration numbered below what is already applied would run against a
// schema its author never saw. That is a mistake to report, not to perform.
func TestApplyScriptsRefusesAVersionOlderThanTheSchema(t *testing.T) {
	db := open(t)
	if err := migrations.ApplyScripts(db, []migrations.Script{
		{Version: 2, Name: "0002_second.sql", SQL: `CREATE TABLE second (id INTEGER);`},
	}); err != nil {
		t.Fatalf("ApplyScripts: %v", err)
	}

	err := migrations.ApplyScripts(db, []migrations.Script{
		{Version: 1, Name: "0001_late.sql", SQL: `CREATE TABLE late (id INTEGER);`},
	})
	if err == nil {
		t.Fatal("ApplyScripts applied version 1 onto a schema already at version 2")
	}
	if tables(t, db)["late"] {
		t.Error("the out-of-order migration was applied anyway")
	}
}
