package store_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// Adjustments are the one layer in this database that is written by a person
// and is not a derivation. Everything here is about the two properties that
// follow from that: nothing is ever destroyed, and re-deriving the machine's
// answer must not touch them.
//
// Helpers are prefixed adj. Go package scope spans every file in this
// directory and store_test.go already owns describe, stamp and focus.

var (
	adjDay   = time.Date(2026, 7, 28, 0, 0, 0, 0, time.UTC)
	adjClock = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
)

func adjAt(hour, minute int) time.Time {
	return adjDay.Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
}

func adjSpanRow(a adjustments.Assertion, fromHour, toHour int) adjustments.Adjustment {
	return adjustments.Adjustment{
		Start: adjAt(fromHour, 0), End: adjAt(toHour, 0),
		Assertion: a, Origin: "a-person", RecordedAt: adjClock,
	}
}

// adjFingerprint checksums a whole table, ordered, so "byte-identical" is a
// comparison rather than an assertion. quote() renders NULL unambiguously;
// plain concatenation would collapse a whole row to NULL and pass vacuously.
func adjFingerprint(t *testing.T, db *sql.DB, table string) string {
	t.Helper()
	cols, err := db.Query(`SELECT name FROM pragma_table_info(?) ORDER BY cid`, table)
	if err != nil {
		t.Fatalf("list the columns of %s: %v", table, err)
	}
	var keep []string
	for cols.Next() {
		var name string
		if err := cols.Scan(&name); err != nil {
			t.Fatalf("read a column name: %v", err)
		}
		keep = append(keep, "quote("+name+")")
	}
	cols.Close()
	if len(keep) == 0 {
		t.Fatalf("%s has no columns", table)
	}
	var sum sql.NullString
	q := `SELECT group_concat(row, char(10)) FROM (SELECT ` +
		strings.Join(keep, `||','||`) + ` AS row FROM ` + table + ` ORDER BY rowid)`
	if err := db.QueryRow(q).Scan(&sum); err != nil {
		t.Fatalf("fingerprint %s: %v", table, err)
	}
	return sum.String
}

// adjSeedEvents puts enough evidence in the store for sessionize to have
// something to cluster. Three points, so that a small gap makes one sitting and
// a large one makes two — which is what makes "the model changed" a real
// change rather than a claim.
func adjSeedEvents(t *testing.T, f fixture) {
	t.Helper()
	run := f.run(t)
	f.write(t, run, []source.Event{
		focus(adjAt(9, 0), adjAt(9, 30), "an-app"),
		focus(adjAt(10, 0), adjAt(10, 30), "an-app"),
		focus(adjAt(14, 0), adjAt(14, 30), "an-app"),
	}...)
	if err := run.Finish(context.Background()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

// adjSessionize rebuilds derived_session at the given gap, through the store,
// exactly as `timetattle sessionize` does.
func adjSessionize(t *testing.T, f fixture, gap time.Duration) int {
	t.Helper()
	ctx := context.Background()
	events, err := f.Events(ctx, store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	res, err := sessions.Cluster(events, sessions.Params{Gap: gap})
	if err != nil {
		t.Fatalf("Cluster at %v: %v", gap, err)
	}
	if err := f.ReplaceDerived(ctx, sessions.Replace(res, sessions.Corpus{}, adjClock)); err != nil {
		t.Fatalf("ReplaceDerived: %v", err)
	}
	return len(res.Sessions)
}

func adjRowCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// CRITERION RLN. Origin and time come back as they went in, because a
// correction that cannot be traced back cannot be re-examined.
func TestAnAdjustmentComesBackWithItsOriginAndItsTime(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	in := adjustments.Adjustment{
		Start: adjAt(9, 0), End: adjAt(11, 0),
		Assertion: adjustments.NotEngaged, Origin: "a-person",
		RecordedAt: adjClock, Note: "a meeting",
	}
	id, err := f.RecordAdjustment(ctx, in)
	if err != nil {
		t.Fatalf("RecordAdjustment: %v", err)
	}
	if id == 0 {
		t.Error("RecordAdjustment returned no id")
	}

	got, err := f.Adjustments(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("the store holds %d adjustments, want 1", len(got))
	}
	a := got[0]
	if a.ID != id {
		t.Errorf("id came back as %d, want %d", a.ID, id)
	}
	if !a.Start.Equal(in.Start) || !a.End.Equal(in.End) {
		t.Errorf("the span came back as %v..%v, want %v..%v", a.Start, a.End, in.Start, in.End)
	}
	if a.Assertion != in.Assertion {
		t.Errorf("the assertion came back as %q, want %q", a.Assertion, in.Assertion)
	}
	if a.Origin != in.Origin {
		t.Errorf("the origin came back as %q, want %q", a.Origin, in.Origin)
	}
	if !a.RecordedAt.Equal(in.RecordedAt) {
		t.Errorf("recorded_at came back as %v, want %v", a.RecordedAt, in.RecordedAt)
	}
	if a.Note != in.Note {
		t.Errorf("the note came back as %q, want %q", a.Note, in.Note)
	}
}

func TestAnAttributionComesBackWithItsProject(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	in := adjSpanRow(adjustments.Attributed, 9, 11)
	in.Project = "a-project"
	if _, err := f.RecordAdjustment(ctx, in); err != nil {
		t.Fatalf("RecordAdjustment: %v", err)
	}
	if _, err := f.RecordAdjustment(ctx, adjSpanRow(adjustments.Unattributed, 11, 12)); err != nil {
		t.Fatalf("RecordAdjustment: %v", err)
	}

	got, err := f.Adjustments(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("the store holds %d adjustments, want 2", len(got))
	}
	if got[0].Project != "a-project" {
		t.Errorf("the attribution came back as %q, want a-project", got[0].Project)
	}
	if got[1].Project != "" {
		t.Errorf("the unattribution came back carrying %q", got[1].Project)
	}
	if got[1].Assertion != adjustments.Unattributed {
		t.Errorf("the unattribution came back as %q; asserted-to-belong-to-nothing must not "+
			"collapse into nobody-has-said", got[1].Assertion)
	}
}

func TestTheStoreRefusesAnAdjustmentItsSchemaWouldRefuse(t *testing.T) {
	f := newStore(t)
	bad := adjSpanRow(adjustments.Engaged, 11, 9)
	if _, err := f.RecordAdjustment(context.Background(), bad); err == nil {
		t.Error("a backwards adjustment was recorded")
	}
}

// The three review states, through the store rather than through raw SQL.
func TestReviewsComeBackAsThreeStates(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	for _, r := range []adjustments.Review{
		{Start: adjAt(9, 0), End: adjAt(12, 0), Verdict: adjustments.Recalled, Origin: "a-person", RecordedAt: adjClock},
		{Start: adjAt(12, 0), End: adjAt(15, 0), Verdict: adjustments.Unremembered, Origin: "a-person", RecordedAt: adjClock},
	} {
		if _, err := f.RecordReview(ctx, r); err != nil {
			t.Fatalf("RecordReview(%s): %v", r.Verdict, err)
		}
	}

	got, err := f.Reviews(ctx, time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Reviews: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("the store holds %d reviews, want 2", len(got))
	}
	o := adjustments.Fold(got, nil)
	if v, reviewed := o.Review(adjAt(10, 0)); !reviewed || v != adjustments.Recalled {
		t.Errorf("10:00 reads as (%q, %v)", v, reviewed)
	}
	if v, reviewed := o.Review(adjAt(13, 0)); !reviewed || v != adjustments.Unremembered {
		t.Errorf("13:00 reads as (%q, %v)", v, reviewed)
	}
	if v, reviewed := o.Review(adjAt(20, 0)); reviewed {
		t.Errorf("20:00 reads as (%q, %v); nobody has looked at it", v, reviewed)
	}
}

func TestAnAdjustmentUnderAnUnrememberedReviewIsRefused(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	id, err := f.RecordReview(ctx, adjustments.Review{
		Start: adjAt(9, 0), End: adjAt(17, 0), Verdict: adjustments.Unremembered,
		Origin: "a-person", RecordedAt: adjClock,
	})
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}

	a := adjSpanRow(adjustments.Engaged, 9, 11)
	a.ReviewID = id
	if _, err := f.RecordAdjustment(ctx, a); err == nil {
		t.Error("a correction was recorded under a review that remembered nothing")
	}
}

func TestAReadOnlyStoreRecordsNothing(t *testing.T) {
	f := newStore(t)
	if _, err := f.RecordAdjustment(context.Background(), adjSpanRow(adjustments.Engaged, 9, 11)); err != nil {
		t.Fatalf("seed: %v", err)
	}
	ro, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	defer ro.Close()

	if _, err := ro.RecordAdjustment(context.Background(), adjSpanRow(adjustments.Engaged, 13, 15)); err == nil {
		t.Error("a read-only store recorded an adjustment")
	}
	if _, err := ro.RecordReview(context.Background(), adjustments.Review{
		Start: adjAt(9, 0), End: adjAt(17, 0), Verdict: adjustments.Recalled,
		Origin: "a-person", RecordedAt: adjClock,
	}); err == nil {
		t.Error("a read-only store recorded a review")
	}
	// And it can still read what is there, which is the whole point of it.
	got, err := ro.Adjustments(context.Background(), time.Time{}, time.Time{})
	if err != nil {
		t.Fatalf("Adjustments through a read-only store: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("a read-only store read %d adjustments, want 1", len(got))
	}
}

func TestAdjustmentsAreBoundedByTheSpanAsked(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	for _, a := range []adjustments.Adjustment{
		adjSpanRow(adjustments.Engaged, 9, 10),
		adjSpanRow(adjustments.Engaged, 13, 14),
	} {
		if _, err := f.RecordAdjustment(ctx, a); err != nil {
			t.Fatalf("RecordAdjustment: %v", err)
		}
	}

	got, err := f.Adjustments(ctx, adjAt(12, 0), adjAt(15, 0))
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a window over the afternoon read %d adjustments, want 1", len(got))
	}
	if !got[0].Start.Equal(adjAt(13, 0)) {
		t.Errorf("it read the one starting %v", got[0].Start)
	}

	// Overlap, not containment: an adjustment that begins before the window and
	// runs into it is in the window, because it describes time inside it.
	if _, err := f.RecordAdjustment(ctx, adjSpanRow(adjustments.Engaged, 8, 13)); err != nil {
		t.Fatalf("RecordAdjustment: %v", err)
	}
	got, err = f.Adjustments(ctx, adjAt(12, 0), adjAt(15, 0))
	if err != nil {
		t.Fatalf("Adjustments: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("a window over the afternoon read %d adjustments, want 2", len(got))
	}
}

// ============================================================================
// CRITERION ZHM: recomputing sessions from scratch preserves every adjustment.
// ============================================================================

// The direct form, fingerprinted around three consecutive rebuilds the way
// internal/sessions does for its own invariant. A claim that adjustments
// survive re-derivation is worth nothing without this comparison.
func TestThreeRebuildsLeaveEveryAdjustmentByteIdentical(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	db := f.raw(t)
	adjSeedEvents(t, f)

	review, err := f.RecordReview(ctx, adjustments.Review{
		Start: adjAt(9, 0), End: adjAt(17, 0), Verdict: adjustments.Recalled,
		Origin: "a-person", RecordedAt: adjClock, Note: "reviewed on the day",
	})
	if err != nil {
		t.Fatalf("RecordReview: %v", err)
	}
	for _, a := range []adjustments.Adjustment{
		{Start: adjAt(8, 0), End: adjAt(9, 0), Assertion: adjustments.Engaged,
			Origin: "a-person", RecordedAt: adjClock, ReviewID: review},
		{Start: adjAt(12, 0), End: adjAt(13, 0), Assertion: adjustments.NotEngaged,
			Origin: "a-person", RecordedAt: adjClock, Note: "lunch"},
		{Start: adjAt(9, 0), End: adjAt(11, 0), Assertion: adjustments.Attributed,
			Project: "a-project", Origin: "a-person", RecordedAt: adjClock},
		{Start: adjAt(11, 0), End: adjAt(12, 0), Assertion: adjustments.Unattributed,
			Origin: "a-person", RecordedAt: adjClock},
	} {
		if _, err := f.RecordAdjustment(ctx, a); err != nil {
			t.Fatalf("RecordAdjustment(%s): %v", a.Assertion, err)
		}
	}

	beforeAdj := adjFingerprint(t, db, "adjustment")
	beforeRev := adjFingerprint(t, db, "adjustment_review")
	if beforeAdj == "" || beforeRev == "" {
		t.Fatal("there is nothing to protect; the comparison below would be vacuous")
	}

	for range 3 {
		adjSessionize(t, f, 15*time.Minute)
	}
	if got := adjRowCount(t, db, "derived_session"); got == 0 {
		t.Fatal("the rebuild produced no sessions; the test would pass for the wrong reason")
	}

	if got := adjFingerprint(t, db, "adjustment"); got != beforeAdj {
		t.Errorf("rebuilding sessions changed the adjustments:\n%s\n---\n%s", beforeAdj, got)
	}
	if got := adjFingerprint(t, db, "adjustment_review"); got != beforeRev {
		t.Errorf("rebuilding sessions changed the reviews:\n%s\n---\n%s", beforeRev, got)
	}
	if got := adjRowCount(t, db, "adjustment"); got != 4 {
		t.Errorf("%d adjustments survived three rebuilds, want 4", got)
	}
}

// The version that means something. Rebuilding at the SAME parameters proves
// little, because the derivation is deterministic; the real event is the model
// changing, which is what this whole layer exists to survive. So the gap moves,
// derived_session genuinely changes, and the adjustments must not.
func TestChangingTheModelChangesTheSessionsAndNotTheAdjustments(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	db := f.raw(t)
	adjSeedEvents(t, f)

	if _, err := f.RecordAdjustment(ctx, adjSpanRow(adjustments.NotEngaged, 12, 13)); err != nil {
		t.Fatalf("RecordAdjustment: %v", err)
	}
	before := adjFingerprint(t, db, "adjustment")

	tight := adjSessionize(t, f, time.Minute)
	tightSessions := adjFingerprint(t, db, "derived_session")

	loose := adjSessionize(t, f, 6*time.Hour)
	looseSessions := adjFingerprint(t, db, "derived_session")

	if tight == loose {
		t.Fatalf("both gaps produced %d sittings; the model did not change and the test is vacuous", tight)
	}
	if tightSessions == looseSessions {
		t.Fatal("derived_session is byte-identical across two different gaps; the test is vacuous")
	}
	if got := adjFingerprint(t, db, "adjustment"); got != before {
		t.Errorf("changing the clustering parameters changed the adjustments:\n%s\n---\n%s", before, got)
	}
}

// The property behind the fingerprint: the correction still MEANS the same
// thing after the model moved. A row that survived but no longer applies would
// pass every test above and would still have lost the human's judgment.
func TestACorrectionStillAppliesAfterTheModelChanges(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	adjSeedEvents(t, f)

	if _, err := f.RecordAdjustment(ctx, adjSpanRow(adjustments.NotEngaged, 10, 14)); err != nil {
		t.Fatalf("RecordAdjustment: %v", err)
	}

	for _, gap := range []time.Duration{time.Minute, 6 * time.Hour} {
		adjSessionize(t, f, gap)
		stored, err := f.Sittings(ctx, time.Time{}, time.Time{})
		if err != nil {
			t.Fatalf("Sittings at gap %v: %v", gap, err)
		}
		var machine []adjustments.Span
		for _, s := range stored.Sittings {
			machine = append(machine, s.Span())
		}

		log, err := f.Adjustments(ctx, time.Time{}, time.Time{})
		if err != nil {
			t.Fatalf("Adjustments: %v", err)
		}
		adjusted := adjustments.Fold(nil, log).Engaged(machine)

		for _, s := range adjusted {
			if s.End.After(adjAt(10, 0)) && s.Start.Before(adjAt(14, 0)) {
				t.Errorf("at gap %v the adjusted answer still covers %v..%v, "+
					"inside the span a person said was not work", gap, s.Start, s.End)
			}
		}
	}
}
