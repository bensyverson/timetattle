package migrations_test

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/bensyverson/timetattle/internal/migrations"
)

// 0014 guards the shape of event.payload.
//
// The column has existed since 0001 and nothing wrote it until
// internal/transcripts began recording which session a record belonged to and
// what the model spent on it. The roll-up reads it with json_extract, and
// json_extract answers NULL for every path of a value that is not an object —
// so `7`, `"tokens"` and `[1,2]` would all store cleanly and then read back as
// a store that recorded nothing at all. Malformed JSON is worse: json_extract
// raises rather than returning, so one bad row silences every figure.
//
// These tests drive the schema with raw SQL rather than through
// internal/store, deliberately, for the reason schema_test.go gives: Go can be
// bypassed, and the guard is what makes a shape impossible rather than merely
// discouraged.

// notObjects is every payload the guard exists to refuse. The first two are not
// JSON at all; the rest are valid JSON that answers no JSON path.
var notObjects = []string{
	`not json at all`,
	`{"unterminated": `,
	`7`,
	`"a bare string"`,
	`[1, 2, 3]`,
}

// insertPayload attempts one event carrying payload, which may be nil.
func insertPayload(db *sql.DB, run int64, document string, payload any) error {
	_, err := db.Exec(`INSERT INTO event
		(first_seen_run, source_id, shape, started_at, ended_at, document, payload)
		VALUES (?, 'claude-transcripts', 'point', '2026-08-07T09:00:00.000000Z',
		        '2026-08-07T09:00:00.000000Z', ?, ?)`, run, document, payload)
	return err
}

// The before and the after in one test. The after on its own would pass against
// a schema that had never accepted a bad payload in the first place, and would
// go on passing if the migration stopped being applied.
func TestThePayloadGuardArrivesThroughTheAutomaticPath(t *testing.T) {
	d := tsAt(t, 13)
	run := aRun(t, d)

	if err := insertPayload(d, run, "before", `[1, 2, 3]`); err != nil {
		t.Fatalf("at version 13 a payload that is not an object was already refused: %v\n"+
			"this test's before-state is gone, so its after-state proves nothing", err)
	}

	if err := migrations.Apply(d); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	err := insertPayload(d, run, "after", `[1, 2, 3]`)
	if err == nil {
		t.Fatal("after 0014 a payload that is not a JSON object still inserts")
	}
	if !strings.Contains(err.Error(), "JSON object") {
		t.Errorf("the refusal does not say what a payload must be: %v", err)
	}

	// The row written before the guard existed is still there. A migration
	// that quietly deleted rows it now disapproves of would be destroying
	// observations, which is the one thing this store never does.
	var kept int
	if err := d.QueryRow(`SELECT COUNT(*) FROM event WHERE document = 'before'`).Scan(&kept); err != nil {
		t.Fatalf("count the row written before the guard: %v", err)
	}
	if kept != 1 {
		t.Errorf("the row stored before 0014 is gone; %d rows remain", kept)
	}
}

func TestThePayloadGuardRefusesEveryShapeThatIsNotAnObject(t *testing.T) {
	d := migrated(t)
	run := aRun(t, d)

	for i, payload := range notObjects {
		if err := insertPayload(d, run, "u"+string(rune('a'+i)), payload); err == nil {
			t.Errorf("payload %q was stored; it answers no JSON path and would read back as though "+
				"nothing was recorded", payload)
		}
	}
}

func TestAnObjectPayloadAndNoPayloadAreBothOrdinary(t *testing.T) {
	d := migrated(t)
	run := aRun(t, d)

	if err := insertPayload(d, run, "with", `{"session":"s-1","tokens":{"output":214}}`); err != nil {
		t.Errorf("an object payload was refused: %v", err)
	}
	// NULL, not the empty string: a row with nothing extra to say records
	// nothing, and an empty string would be a payload asserting emptiness.
	if err := insertPayload(d, run, "without", nil); err != nil {
		t.Errorf("an event carrying no payload was refused: %v", err)
	}
}

// event rows ARE updated — the attribution pass writes event.project — so the
// guard has to cover UPDATE as well, and must not fire on an update that leaves
// the payload alone.
func TestThePayloadGuardCoversUpdateWithoutObstructingAttribution(t *testing.T) {
	d := migrated(t)
	run := aRun(t, d)
	if err := insertPayload(d, run, "u1", `{"session":"s-1"}`); err != nil {
		t.Fatalf("store an ordinary payload: %v", err)
	}

	if _, err := d.Exec(`UPDATE event SET project = 'quarry'`); err != nil {
		t.Errorf("re-attributing an event carrying a payload was refused: %v; the guard fires on the "+
			"payload's shape and must ignore a statement that does not change it", err)
	}
	if _, err := d.Exec(`UPDATE event SET payload = '[1,2,3]'`); err == nil {
		t.Error("a payload was updated to something that is not a JSON object")
	}
}

// The guard proved by mutation rather than asserted: dropped, the five refused
// payloads all insert, and the figures computed from them go silent — three
// answer NULL from every path, and the two that are not JSON at all take the
// whole query down with them.
func TestDroppingThePayloadGuardStoresFiveUnreadableRowsAndSilencesTheRollUp(t *testing.T) {
	d := migrated(t)
	run := aRun(t, d)
	for _, name := range []string{"event_payload_is_an_object_on_insert", "event_payload_is_an_object_on_update"} {
		if _, err := d.Exec(`DROP TRIGGER ` + name); err != nil {
			t.Fatalf("drop %s: %v", name, err)
		}
	}

	stored := 0
	for i, payload := range notObjects {
		if err := insertPayload(d, run, "u"+string(rune('a'+i)), payload); err == nil {
			stored++
		}
	}
	if stored != len(notObjects) {
		t.Fatalf("without the guard %d of %d unreadable payloads inserted; the mutation is not "+
			"demonstrating what it claims", stored, len(notObjects))
	}

	// The roll-up over those rows: it fails outright, because two of them are
	// not JSON and json_extract raises on them.
	const rollUp = `SELECT COUNT(DISTINCT json_extract(payload, '$.session')),
		COALESCE(SUM(json_extract(payload, '$.tokens.output')), 0)
		FROM canonical_event WHERE payload IS NOT NULL`
	var sessions, total int64
	err := d.QueryRow(rollUp).Scan(&sessions, &total)
	if err == nil {
		t.Fatalf("the roll-up succeeded over malformed payloads, reporting %d sessions and %d tokens",
			sessions, total)
	}
	if !strings.Contains(err.Error(), "malformed JSON") {
		t.Errorf("the roll-up failed for some reason other than the malformed payloads: %v", err)
	}

	// And with only the well-formed-but-not-object ones left, it reports a
	// store that recorded nothing — which is the quieter half of the failure
	// and the reason the guard refuses these too.
	if _, err := d.Exec(`DELETE FROM event WHERE NOT json_valid(payload)`); err != nil {
		t.Fatalf("remove the malformed rows: %v", err)
	}
	if err := d.QueryRow(rollUp).Scan(&sessions, &total); err != nil {
		t.Fatalf("roll up over the remaining rows: %v", err)
	}
	if sessions != 0 || total != 0 {
		t.Fatalf("the remaining rows reported %d sessions and %d tokens", sessions, total)
	}
	var rows int
	if err := d.QueryRow(`SELECT COUNT(*) FROM event WHERE payload IS NOT NULL`).Scan(&rows); err != nil {
		t.Fatalf("count the rows: %v", err)
	}
	if rows != 3 {
		t.Fatalf("%d rows carry a payload, want 3", rows)
	}
	t.Logf("without the guard, %d rows carrying a payload report 0 sessions and 0 tokens", rows)
}
