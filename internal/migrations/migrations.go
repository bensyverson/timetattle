// Package migrations owns the database schema.
//
// The schema is a run of numbered SQL files, embedded in the binary and applied
// in order at startup. Nothing migrates a database by hand: a schema change is a
// new file, and the only way it reaches a database is by the program starting.
//
// Two properties matter more than they look:
//
//   - Applying is idempotent. Apply runs on every start, so a store that is
//     already current must come out untouched rather than erroring.
//   - A migration that fails leaves nothing behind, not even its first
//     statement. Otherwise the next start applies the remainder onto a
//     half-built schema and the failure compounds into something no error
//     message describes.
//
// The second is why each file runs inside one transaction. SQLite is unusual in
// making DDL transactional, and this design leans on it.
package migrations

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"
)

// files holds the schema. Embedding it means a migration cannot be missing from
// an installed binary, which is the failure mode of reading them from disk.
//
//go:embed *.sql
var files embed.FS

// Script is one numbered migration.
type Script struct {
	// Version is the numeric prefix of the file name, and is what the
	// database records. It orders the run.
	Version int
	// Name is the file name, kept so that a report can say which migration
	// ran rather than only how many.
	Name string
	// SQL is the whole file, which may hold any number of statements.
	SQL string
}

// ledger records what has been applied. It is created by the runner rather than
// by a migration, because it has to exist before the first one can run.
//
// applied_at is written by SQLite so the runner needs no clock, in the
// fixed-width UTC form every timestamp in this database uses: %f gives
// milliseconds and the literal zeros pad it to the microseconds internal/store
// writes, so text order is time order in every column of every table.
const ledger = `CREATE TABLE IF NOT EXISTS schema_migration (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    applied_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%f000Z', 'now'))
)`

// Apply brings db up to the schema this binary was built with.
//
// It is safe and cheap to call on every start, and does nothing to a database
// that is already current.
func Apply(db *sql.DB) error {
	scripts, err := Scripts()
	if err != nil {
		return err
	}
	return ApplyScripts(db, scripts)
}

// Pending reports the migrations that db has not run yet, in the order they
// would run. It changes nothing, including on a database that has never been
// migrated at all.
func Pending(db *sql.DB) ([]Script, error) {
	scripts, err := Scripts()
	if err != nil {
		return nil, err
	}
	done, err := applied(db)
	if err != nil {
		return nil, err
	}
	var out []Script
	for _, s := range scripts {
		if !done[s.Version] {
			out = append(out, s)
		}
	}
	return out, nil
}

// Applied is one row of the ledger: a migration a database has actually run.
//
// AppliedAt is the text SQLite wrote, in the fixed-width UTC form every
// timestamp in this database uses. It is left as text because this package owns
// the ledger's format and not anybody's idea of a clock; internal/store parses
// it with the layout it defines.
type Applied struct {
	Version   int
	Name      string
	AppliedAt string
}

// Current reports the newest migration db has run.
//
// It exists so that nothing outside this package has to know the ledger's
// column names to answer "how far has this database been migrated" — which
// `timetattle status` asked with its own SQL until the seam was closed, making
// a command the only reader of a table no migration test covers.
//
// A database with no ledger at all errors rather than reporting version zero.
// Never migrated and migrated to nothing are different facts, and only one of
// them means the file is not a Timetattle store.
func Current(ctx context.Context, db *sql.DB) (Applied, error) {
	var a Applied
	err := db.QueryRowContext(ctx, `SELECT version, name, applied_at FROM schema_migration
		ORDER BY version DESC LIMIT 1`).Scan(&a.Version, &a.Name, &a.AppliedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Applied{}, errors.New("the migration ledger is empty; this database has never been migrated")
	}
	if err != nil {
		return Applied{}, fmt.Errorf("read the migration ledger: %w", err)
	}
	return a, nil
}

// Scripts returns the embedded migrations ordered by version.
func Scripts() ([]Script, error) {
	entries, err := fs.ReadDir(files, ".")
	if err != nil {
		return nil, fmt.Errorf("read the embedded migrations: %w", err)
	}

	out := make([]Script, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, err := parseVersion(entry.Name())
		if err != nil {
			return nil, err
		}
		body, err := fs.ReadFile(files, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", entry.Name(), err)
		}
		out = append(out, Script{Version: version, Name: entry.Name(), SQL: string(body)})
	}
	if len(out) == 0 {
		return nil, errors.New("no migrations were embedded; the schema would be empty")
	}
	return order(out)
}

// ApplyScripts applies scripts that db has not already run.
//
// It exists separately from Apply so that the runner's own guarantees can be
// tested against SQL chosen by the test rather than against the real schema,
// which by construction succeeds.
//
// Concurrency is deliberately not handled: two processes migrating the same
// brand-new database at the same instant will have one of them fail with a
// duplicate table, and Timetattle is a single-user local tool where the loser
// can simply be run again. Sharing a database across processes once migrated is
// fine — that is what WAL and a busy timeout are for.
func ApplyScripts(db *sql.DB, scripts []Script) error {
	ordered, err := order(scripts)
	if err != nil {
		return err
	}
	done, err := applied(db)
	if err != nil {
		return err
	}

	current := 0
	for version := range done {
		if version > current {
			current = version
		}
	}

	var todo []Script
	for _, s := range ordered {
		if done[s.Version] {
			continue
		}
		// A migration numbered below the schema it is being applied to was
		// written against a database its author never saw. Two branches each
		// adding one is the ordinary way this happens, and renumbering is the
		// fix; running it out of order is not.
		if s.Version < current {
			return fmt.Errorf("migration %s is version %d but the schema is already at version %d; "+
				"renumber it above %d rather than applying it out of order", s.Name, s.Version, current, current)
		}
		todo = append(todo, s)
	}
	if len(todo) == 0 {
		return nil
	}

	if _, err := db.Exec(ledger); err != nil {
		return fmt.Errorf("create the migration ledger: %w", err)
	}
	for _, s := range todo {
		if err := apply(db, s); err != nil {
			return err
		}
	}
	return nil
}

// apply runs one migration and records it, both or neither.
func apply(db *sql.DB, s Script) error {
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", s.Name, err)
	}
	// Rollback after a successful commit is a no-op, so this needs no flag.
	defer tx.Rollback()

	if _, err := tx.Exec(s.SQL); err != nil {
		return fmt.Errorf("migration %s: %w", s.Name, err)
	}
	if _, err := tx.Exec(`INSERT INTO schema_migration (version, name) VALUES (?, ?)`, s.Version, s.Name); err != nil {
		return fmt.Errorf("record migration %s: %w", s.Name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", s.Name, err)
	}
	return nil
}

// applied reads the versions db has run. A database with no ledger has run
// nothing, which is not an error — it is what a new store looks like.
func applied(db *sql.DB) (map[int]bool, error) {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='schema_migration'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("look for the migration ledger: %w", err)
	}

	rows, err := db.Query(`SELECT version FROM schema_migration`)
	if err != nil {
		return nil, fmt.Errorf("read the migration ledger: %w", err)
	}
	defer rows.Close()

	out := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("read the migration ledger: %w", err)
		}
		out[version] = true
	}
	return out, rows.Err()
}

// order sorts scripts by version and refuses a set that cannot be ordered.
func order(scripts []Script) ([]Script, error) {
	out := make([]Script, len(scripts))
	copy(out, scripts)
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })

	for i := 1; i < len(out); i++ {
		if out[i].Version == out[i-1].Version {
			return nil, fmt.Errorf("migrations %s and %s are both version %d; "+
				"which one ran would depend on iteration order", out[i-1].Name, out[i].Name, out[i].Version)
		}
	}
	return out, nil
}

// parseVersion reads the numeric prefix of a migration file name.
func parseVersion(name string) (int, error) {
	prefix, _, found := strings.Cut(name, "_")
	if !found {
		return 0, fmt.Errorf("migration %q has no version prefix; name migrations like 0002_add_contract.sql", name)
	}
	version, err := strconv.Atoi(prefix)
	if err != nil {
		return 0, fmt.Errorf("migration %q does not begin with a number; name migrations like 0002_add_contract.sql", name)
	}
	if version < 1 {
		return 0, fmt.Errorf("migration %q is version %d; versions start at 1", name, version)
	}
	return version, nil
}
