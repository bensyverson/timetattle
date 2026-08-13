package source_test

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/migrations"
	"github.com/bensyverson/timetattle/internal/source"

	_ "modernc.org/sqlite"
)

// Attribution begins by matching a path against the directories a project has
// blessed, so the path has to survive collection. internal/timestamped has it
// and currently drops it on the floor.
func TestLocatorRecordsWhereTheEvidenceWasFound(t *testing.T) {
	e := source.NewPoint(at(14, 1, 0),
		source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave")
	e.Locator = "/Users/you/Movies/Motion Projects/Autosave Vault/dark-crystal-v2/dark-crystal-v2_2026-07-28_1401.motn"

	if err := e.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want an event carrying its path to be valid", err)
	}
	if !strings.HasSuffix(e.Locator, ".motn") {
		t.Errorf("Locator = %q, want the file the evidence was read from", e.Locator)
	}
}

// Most evidence has no place. Biome knows only a bundle identifier, and
// presence is not about a file at all — so an absent locator is the ordinary
// case and cannot be an error.
func TestLocatorIsEmptyForEvidenceThatHasNowhereToPointAt(t *testing.T) {
	focus := source.NewInterval(at(13, 49, 40), at(14, 25, 40),
		source.Identity{App: "com.apple.motionapp"}, "biome-appusage")
	presence := source.NewInterval(at(5, 18, 24), at(19, 16, 24), source.Identity{}, "knowledgec-presence")

	for _, e := range []source.Event{focus, presence} {
		if e.Locator != "" {
			t.Errorf("Locator = %q, want empty; %s observes no file", e.Locator, e.Source)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("Validate() = %v, want evidence without a path to be valid", err)
		}
	}
}

// A relative path cannot be prefix-matched against a registered directory, so
// it does not fail loudly — it simply never resolves, and the work attributes
// to nothing. That is the failure this project exists to catch, so it is
// refused at the boundary instead.
func TestValidateRejectsARelativeLocator(t *testing.T) {
	e := source.NewPoint(at(14, 1, 0),
		source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave")
	e.Locator = "Autosave Vault/dark-crystal-v2/dark-crystal-v2_2026-07-28_1401.motn"

	err := e.Validate()
	if err == nil {
		t.Fatal("Validate() = nil, want a relative locator to be refused")
	}
	if !strings.Contains(err.Error(), "dark-crystal-v2") {
		t.Errorf("error %q does not name the subject, so a harvest log cannot be acted on", err)
	}
}

// This is why Locator is not part of Identity. The same composition is
// observed in two places — the autosave vault and the project directory — and
// the two must compare equal, because that equality IS the name join that
// attributes shadow-cache evidence to a project. A path inside Identity would
// make every copy of a document a different document.
func TestIdentityIsUnaffectedByWhereTheEvidenceWasFound(t *testing.T) {
	vault := source.NewPoint(at(14, 1, 0),
		source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave")
	vault.Locator = "/Users/you/Movies/Motion Projects/Autosave Vault/dark-crystal-v2_2026-07-28_1401.motn"

	repo := source.NewPoint(at(14, 30, 0),
		source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, "motion-autosave")
	repo.Locator = "/Users/you/git/acorn/motion/dark-crystal-v2.motn"

	if vault.Identity != repo.Identity {
		t.Fatalf("identities differ across locations:\n vault %+v\n repo  %+v\n"+
			"the name join rests on this equality", vault.Identity, repo.Identity)
	}
	if vault.Locator == repo.Locator {
		t.Error("the two locators are equal, so this test is not exercising two places")
	}
}

// TestDuplicateLocatorCollisionKeepsWhicheverWasInsertedFirst is the case
// TestIdentityIsUnaffectedByWhereTheEvidenceWasFound stops short of: two
// events observed at the SAME instant, so they collide at the real
// event_identity key (source_id, shape, started_at, ended_at, app, document,
// state) and not merely at the looser source.Identity struct.
//
// It runs against a real database rather than asserting on Go values alone,
// because the hazard task Ls0XC exists to quantify lives in what SQLite's
// ON CONFLICT DO NOTHING actually keeps, not in what source.Event says. The
// insert statement is copied from internal/store/run.go's insertEvent rather
// than importing internal/store, because this package does not own that one
// and the two must stay decoupled — see that file if this ever drifts from
// what the store actually executes.
//
// The result answers criterion 90v (job show Ls0XC): which copy wins is
// DETERMINISTIC for a fixed insertion order — SQLite always keeps whichever
// row's INSERT reached it first within the unique index — but that order is
// not a decision anyone made about which copy matters more. It falls out of
// whatever order a harvest happens to hand rows to Store.Write (collector
// iteration order, ingestion plan order across cmd/timetattle/main.go's
// registeredIngestions/snapshotIngestions/fsobserveIngestion/gitlogIngestions
// slice, or which source a future manifest lists first). Swap the insertion
// order below and the outcome flips, which is what "arbitrary" means here:
// arbitrary with respect to attribution, not with respect to SQLite.
func TestDuplicateLocatorCollisionKeepsWhicheverWasInsertedFirst(t *testing.T) {
	whenText := time.Date(2026, 7, 28, 14, 1, 0, 0, time.UTC).Format("2006-01-02T15:04:05.000000Z")

	// vaultCopy sits in the Motion Autosave Vault, which registry.List never
	// blesses — the vault is a shared cache belonging to no single project.
	// repoCopy sits in a project's own working tree, exactly the kind of path
	// registry.Register blesses. Same instant, same everything Identity
	// carries, because that equality is the premise under test.
	const (
		vaultLocator = "/Users/you/Movies/Motion Projects/Autosave Vault/dark-crystal-v2/dark-crystal-v2_2026-07-28_1401.motn"
		repoLocator  = "/Users/you/git/acorn/motion/dark-crystal-v2.motn"
	)

	// insertEvent is internal/store/run.go's own statement, copied rather than
	// imported — see the doc comment above.
	const insertEvent = `INSERT INTO event
		(first_seen_run, source_id, shape, unknown, started_at, ended_at, app, document, state, locator, cause)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING`

	run := func(t *testing.T, firstLocator, secondLocator string) string {
		t.Helper()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatalf("open in-memory db: %v", err)
		}
		defer db.Close()
		if err := migrations.Apply(db); err != nil {
			t.Fatalf("apply the real schema: %v", err)
		}
		if _, err := db.Exec(`INSERT INTO harvest_run (source_id, started_at, input, status) VALUES ('motion-autosave', ?, 'test', 'ok')`, whenText); err != nil {
			t.Fatalf("seed a harvest_run for the foreign key: %v", err)
		}

		for _, locator := range []string{firstLocator, secondLocator} {
			if _, err := db.Exec(insertEvent,
				1, "motion-autosave", "point", nil, whenText, whenText,
				"com.apple.motionapp", "dark-crystal-v2", nil, locator, "timer"); err != nil {
				t.Fatalf("insert locator %q: %v", locator, err)
			}
		}

		var count int
		if err := db.QueryRow(`SELECT count(*) FROM event`).Scan(&count); err != nil {
			t.Fatalf("count surviving rows: %v", err)
		}
		if count != 1 {
			t.Fatalf("event_identity did not collapse the two rows: got %d rows, want 1 — "+
				"this test's premise (same source_id/shape/started_at/ended_at/app/document/state, "+
				"different locator) may no longer hold against 0001_initial.sql", count)
		}

		var survivor string
		if err := db.QueryRow(`SELECT locator FROM event`).Scan(&survivor); err != nil {
			t.Fatalf("read the surviving locator: %v", err)
		}
		return survivor
	}

	t.Run("vault inserted first", func(t *testing.T) {
		got := run(t, vaultLocator, repoLocator)
		if got != vaultLocator {
			t.Fatalf("survivor = %q, want the vault copy %q — first-insert-wins should have kept it", got, vaultLocator)
		}
		// This is the hazard, demonstrated: the row that survives is the one a
		// resolution pass can never attribute anywhere, and the project copy
		// that WOULD have resolved is gone without a trace — no error, no
		// second row, nothing in events_seen vs events_new to flag it, because
		// both events were new to an empty store and dedup did exactly what
		// its own index says to do.
	})

	t.Run("repo inserted first", func(t *testing.T) {
		got := run(t, repoLocator, vaultLocator)
		if got != repoLocator {
			t.Fatalf("survivor = %q, want the repo copy %q — first-insert-wins should have kept it", got, repoLocator)
		}
		// Same two events, opposite order, opposite outcome. Nothing about
		// the observation changed — only which Write() call reached SQLite
		// first — which is exactly why "arbitrary" rather than "buggy" is the
		// right word for this: the mechanism is doing what it was told, and
		// nothing was told to prefer a registered path over a cache.
	})
}
