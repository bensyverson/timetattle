package migrations_test

import (
	"database/sql"
	"strings"
	"testing"
)

// The adjustments layer, driven by raw SQL rather than through
// internal/adjustments, for the reason schema_test.go gives: Go can be
// bypassed, and the constraints are what make a shape impossible rather than
// merely discouraged.
//
// Every helper here is prefixed adj. Go package scope spans every file in this
// directory and schema_test.go already owns `insert`, `row` and `aRun`.

const (
	adjT0  = "2026-07-28T09:00:00.000000Z"
	adjT1  = "2026-07-28T11:30:00.000000Z"
	adjNow = "2026-08-01T12:00:00.000000Z"
)

// adjRow is one adjustment's worth of column values. Every field is any so a
// test can write NULL where Go would insist on a string.
type adjRow struct {
	ReviewID   any
	Started    any
	Ended      any
	Assertion  any
	Project    any
	Origin     any
	RecordedAt any
	Note       any
}

func adjInsert(db *sql.DB, r adjRow) error {
	started := r.Started
	if started == nil {
		started = adjT0
	}
	ended := r.Ended
	if ended == nil {
		ended = adjT1
	}
	assertion := r.Assertion
	if assertion == nil {
		assertion = "engaged"
	}
	origin := r.Origin
	if origin == nil {
		origin = "a-person"
	}
	recorded := r.RecordedAt
	if recorded == nil {
		recorded = adjNow
	}
	_, err := db.Exec(`INSERT INTO adjustment
		(review_id, started_at, ended_at, assertion, project, origin, recorded_at, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ReviewID, started, ended, assertion, r.Project, origin, recorded, r.Note)
	return err
}

// adjReviewInsert writes one review with the timestamps spelled by the caller,
// so a test can drive a shape Go would never produce.
func adjReviewInsert(d *sql.DB, started, ended, recorded any) error {
	_, err := d.Exec(`INSERT INTO adjustment_review
		(started_at, ended_at, verdict, origin, recorded_at)
		VALUES (?, ?, 'recalled', 'a-person', ?)`, started, ended, recorded)
	return err
}

// adjReview inserts one review and returns its id.
func adjReview(t *testing.T, db *sql.DB, verdict string) int64 {
	t.Helper()
	res, err := db.Exec(`INSERT INTO adjustment_review
		(started_at, ended_at, verdict, origin, recorded_at)
		VALUES (?, ?, ?, 'a-person', ?)`, adjT0, adjT1, verdict, adjNow)
	if err != nil {
		t.Fatalf("insert a %s review: %v", verdict, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("LastInsertId: %v", err)
	}
	return id
}

func adjCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func TestAnAdjustmentSpanRunsForwards(t *testing.T) {
	db := migrated(t)
	if err := adjInsert(db, adjRow{Started: adjT1, Ended: adjT0}); err == nil {
		t.Error("a span ending before it started was accepted; it would subtract time nobody lived")
	}
}

// The shape guard, of which width is only the easy half.
//
// The CHECK is a TEXT comparison and is chronological only while every value
// carries the identical layout internal/store.TimeLayout writes. Width alone
// does not get there, and this test used to assert only width: it was renamed
// and strengthened rather than replaced, because the forms it already listed
// are still wrong and still have to be refused.
func TestAnAdjustmentTimestampHasTheStoresExactShape(t *testing.T) {
	for _, bad := range adjBadTimestamps {
		if err := adjInsert(db(t), adjRow{Started: bad.text, Ended: adjT1}); err == nil {
			t.Errorf("adjustment.started_at %q (%s) was accepted", bad.text, bad.why)
		}
		if err := adjInsert(db(t), adjRow{Started: adjT0, Ended: bad.text}); err == nil {
			t.Errorf("adjustment.ended_at %q (%s) was accepted", bad.text, bad.why)
		}
		if err := adjInsert(db(t), adjRow{RecordedAt: bad.text}); err == nil {
			t.Errorf("adjustment.recorded_at %q (%s) was accepted; the fold orders on this column",
				bad.text, bad.why)
		}
	}
}

func TestAReviewTimestampHasTheStoresExactShape(t *testing.T) {
	for _, bad := range adjBadTimestamps {
		if err := adjReviewInsert(db(t), bad.text, adjT1, adjNow); err == nil {
			t.Errorf("adjustment_review.started_at %q (%s) was accepted", bad.text, bad.why)
		}
		if err := adjReviewInsert(db(t), adjT0, bad.text, adjNow); err == nil {
			t.Errorf("adjustment_review.ended_at %q (%s) was accepted", bad.text, bad.why)
		}
		if err := adjReviewInsert(db(t), adjT0, adjT1, bad.text); err == nil {
			t.Errorf("adjustment_review.recorded_at %q (%s) was accepted", bad.text, bad.why)
		}
	}
}

// adjBadTimestamps are shapes that must never reach a timestamp column.
//
// The three marked "27 characters" are the point of this table. A length check
// admits every one of them, and the last is not a near miss: it is five hours
// out of UTC, so the TEXT ordering the CHECK exists to protect is wrong by five
// hours while the constraint reports success. Confirmed on this machine:
//
//	printf '%s' '2026-08-01T08:04:35.0-05:00' | wc -c   →  27
//
// The premise underneath is that mixed layouts mis-sort, which is executable
// rather than argued:
//
//	sqlite3 :memory: "SELECT '2026-08-01T14:00:00Z' < '2026-08-01T14:00:00.500000Z';"  →  0
//
// 'Z' is 0x5A and '.' is 0x2E, so the shorter form reads as LATER than a longer
// one naming an earlier instant.
var adjBadTimestamps = []struct{ text, why string }{
	{"2026-07-28T09:00:00Z", "RFC3339 with no fraction, 20 characters"},
	{"2026-07-28T09:00:00.5Z", "RFC3339Nano, trimmed to one digit"},
	{"2026-07-28T09:00:00.000Z", "milliseconds rather than microseconds, 24 characters"},
	{"2026-07-28T09:00:00.000000", "no zone at all, 26 characters"},
	{"2026-08-01T08:04:35.0-05:00", "27 characters, and five hours out of UTC"},
	{"2026-07-28 09:00:00.000000Z", "27 characters, a space where the T belongs"},
	{"2026-07-28T09:00:00.00000Z0", "27 characters, the Z in the wrong place"},
}

// A boundary is the one assertion about an instant rather than a span, and it
// is the ONLY one. A zero-width "engaged" claims nothing and would fold to
// nothing while looking like a correction somebody made.
func TestOnlyABoundaryIsAnInstant(t *testing.T) {
	if err := adjInsert(db(t), adjRow{Assertion: "boundary", Started: adjT0, Ended: adjT0}); err != nil {
		t.Errorf("a boundary at an instant was refused: %v", err)
	}
	if err := adjInsert(db(t), adjRow{Assertion: "boundary", Started: adjT0, Ended: adjT1}); err == nil {
		t.Error("a boundary across a span was accepted; a boundary is one instant")
	}
	if err := adjInsert(db(t), adjRow{Assertion: "engaged", Started: adjT0, Ended: adjT0}); err == nil {
		t.Error("a zero-width engaged assertion was accepted; it asserts nothing")
	}
	if err := adjInsert(db(t), adjRow{Assertion: "not-engaged", Started: adjT0, Ended: adjT0}); err == nil {
		t.Error("a zero-width not-engaged assertion was accepted; it asserts nothing")
	}
}

// Both directions, because only one of them is the obvious one. A project on an
// 'engaged' row would be an attribution nothing reads, and the reader would
// have no way to know it had been silently dropped.
func TestOnlyAnAttributedAdjustmentCarriesAProject(t *testing.T) {
	if err := adjInsert(db(t), adjRow{Assertion: "attributed", Project: nil}); err == nil {
		t.Error("an attributed adjustment with no project was accepted; it names nothing")
	}
	if err := adjInsert(db(t), adjRow{Assertion: "engaged", Project: "a-project"}); err == nil {
		t.Error("an engaged adjustment carrying a project was accepted; nothing would ever read it")
	}
	if err := adjInsert(db(t), adjRow{Assertion: "unattributed", Project: "a-project"}); err == nil {
		t.Error("an unattributed adjustment carrying a project was accepted; it contradicts itself")
	}
	if err := adjInsert(db(t), adjRow{Assertion: "attributed", Project: "a-project"}); err != nil {
		t.Errorf("an ordinary attribution was refused: %v", err)
	}
	if err := adjInsert(db(t), adjRow{Assertion: "unattributed"}); err != nil {
		t.Errorf("an ordinary unattribution was refused: %v", err)
	}
}

// "This span belongs to no project" is an observation. "Nobody has said" is
// not. Both must be storable and they must not be the same row.
func TestUnattributedAndUnsaidAreDifferentRows(t *testing.T) {
	d := db(t)
	if err := adjInsert(d, adjRow{Assertion: "unattributed"}); err != nil {
		t.Fatalf("record an unattribution: %v", err)
	}
	var (
		assertion string
		project   sql.NullString
	)
	if err := d.QueryRow(`SELECT assertion, project FROM adjustment`).Scan(&assertion, &project); err != nil {
		t.Fatalf("read it back: %v", err)
	}
	if assertion != "unattributed" {
		t.Errorf("assertion is %q, want unattributed", assertion)
	}
	if project.Valid {
		t.Errorf("project is %q, want NULL", project.String)
	}
}

func TestAnAdjustmentNamesAnOrigin(t *testing.T) {
	d := db(t)
	if _, err := d.Exec(`INSERT INTO adjustment (started_at, ended_at, assertion, origin, recorded_at)
		VALUES (?, ?, 'engaged', NULL, ?)`, adjT0, adjT1, adjNow); err == nil {
		t.Error("an adjustment with no origin was accepted; criterion RLN requires one")
	}
	if err := adjInsert(d, adjRow{Origin: ""}); err == nil {
		t.Error("an adjustment with an empty origin was accepted; it names nobody")
	}
}

func TestAnUnknownAssertionIsRefused(t *testing.T) {
	if err := adjInsert(db(t), adjRow{Assertion: "probably"}); err == nil {
		t.Error("an unknown assertion was accepted; the fold would silently ignore it")
	}
}

func TestAnUnknownVerdictIsRefused(t *testing.T) {
	d := db(t)
	if _, err := d.Exec(`INSERT INTO adjustment_review (started_at, ended_at, verdict, origin, recorded_at)
		VALUES (?, ?, 'maybe', 'a-person', ?)`, adjT0, adjT1, adjNow); err == nil {
		t.Error("an unknown verdict was accepted")
	}
}

func TestAReviewCoversMoreThanAnInstant(t *testing.T) {
	d := db(t)
	if _, err := d.Exec(`INSERT INTO adjustment_review (started_at, ended_at, verdict, origin, recorded_at)
		VALUES (?, ?, 'recalled', 'a-person', ?)`, adjT0, adjT0, adjNow); err == nil {
		t.Error("a zero-width review was accepted; it reviewed nothing")
	}
}

// The three states, in schema form. A day reviewed and not recalled and a day
// never reviewed must not produce the same rows.
func TestReviewedAndUnremeberedAndNeverReviewedAreThreeStates(t *testing.T) {
	d := db(t)
	if got := adjCount(t, d, "adjustment_review"); got != 0 {
		t.Fatalf("a fresh store already holds %d reviews", got)
	}
	adjReview(t, d, "recalled")
	adjReview(t, d, "unremembered")

	rows, err := d.Query(`SELECT verdict FROM adjustment_review ORDER BY id`)
	if err != nil {
		t.Fatalf("read the reviews: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got = append(got, v)
	}
	if len(got) != 2 || got[0] != "recalled" || got[1] != "unremembered" {
		t.Errorf("verdicts are %v, want [recalled unremembered]", got)
	}
}

// A correction hanging off a review that remembered nothing is a contradiction:
// the review says the span could not be recalled and the correction says
// exactly what happened in it. SQLite cannot express this as a CHECK, since a
// CHECK sees only its own row, so it is a trigger.
func TestNoAdjustmentHangsOffAReviewThatRememberedNothing(t *testing.T) {
	d := db(t)
	recalled := adjReview(t, d, "recalled")
	unremembered := adjReview(t, d, "unremembered")

	if err := adjInsert(d, adjRow{ReviewID: recalled}); err != nil {
		t.Fatalf("an adjustment under a recalled review was refused: %v", err)
	}
	err := adjInsert(d, adjRow{ReviewID: unremembered})
	if err == nil {
		t.Fatal("an adjustment under an unremembered review was accepted; " +
			"the review says the span could not be recalled and the row says what happened in it")
	}
	if !strings.Contains(err.Error(), "remember") {
		t.Errorf("the refusal does not say why: %v", err)
	}
}

func TestAnAdjustmentPointsAtARealReview(t *testing.T) {
	if err := adjInsert(db(t), adjRow{ReviewID: 9999}); err == nil {
		t.Error("an adjustment naming a review that does not exist was accepted")
	}
}

// Append-only, enforced rather than intended. This is criterion ZHM's floor: no
// rebuild, no script and no mistake can destroy a correction, because the
// database refuses.
func TestAnAdjustmentIsNeverUpdatedOrDeleted(t *testing.T) {
	d := db(t)
	if err := adjInsert(d, adjRow{}); err != nil {
		t.Fatalf("record an adjustment: %v", err)
	}

	if _, err := d.Exec(`UPDATE adjustment SET assertion = 'not-engaged'`); err == nil {
		t.Error("an adjustment was updated; a correction to a correction is a new row")
	}
	if _, err := d.Exec(`DELETE FROM adjustment`); err == nil {
		t.Error("an adjustment was deleted; adjustments outlive every derivation")
	}
	if got := adjCount(t, d, "adjustment"); got != 1 {
		t.Errorf("the adjustment table holds %d rows, want 1", got)
	}
}

func TestAReviewIsNeverUpdatedOrDeleted(t *testing.T) {
	d := db(t)
	adjReview(t, d, "recalled")

	if _, err := d.Exec(`UPDATE adjustment_review SET verdict = 'unremembered'`); err == nil {
		t.Error("a review was updated; the trigger guarding adjustments assumes it cannot be")
	}
	if _, err := d.Exec(`DELETE FROM adjustment_review`); err == nil {
		t.Error("a review was deleted")
	}
	if got := adjCount(t, d, "adjustment_review"); got != 1 {
		t.Errorf("the review table holds %d rows, want 1", got)
	}
}

// Deliberately no UNIQUE index. Recording the same judgment twice is itself a
// fact — it says a person looked twice and said the same thing — and a log that
// deduped would lose it. This test exists so that adding one later is a
// decision somebody makes on purpose.
func TestTheSameJudgmentMayBeRecordedTwice(t *testing.T) {
	d := db(t)
	for i := range 2 {
		if err := adjInsert(d, adjRow{}); err != nil {
			t.Fatalf("record adjustment %d: %v", i+1, err)
		}
	}
	if got := adjCount(t, d, "adjustment"); got != 2 {
		t.Errorf("the adjustment table holds %d rows, want 2", got)
	}
}

// db is migrated(t) under a shorter name, since every test above wants a fresh
// one and several want more than one.
func db(t *testing.T) *sql.DB {
	t.Helper()
	return migrated(t)
}
