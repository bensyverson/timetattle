package jobs

import (
	"database/sql"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// --- The point core. Every row is an observation, whatever it is called. ---

// TestEveryEventTypeBecomesAPoint is the whole of the safe core. Planning and
// coordination is work (task aoFma, Ben 2026-08-01), so no event type is
// privileged and none is dismissed as metadata.
func TestEveryEventTypeBecomesAPoint(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "created", at(9, 0, 0)),
		ev("aaaaa", "criteria_added", at(9, 0, 30)),
		ev("aaaaa", "labeled", at(9, 1, 0)),
		ev("aaaaa", "blocked", at(9, 2, 0)),
		ev("aaaaa", "unblocked", at(9, 3, 0)),
		ev("aaaaa", "noted", at(9, 4, 0)),
		ev("aaaaa", "criterion_state", at(9, 5, 0)),
		ev("aaaaa", "focus_set", at(9, 6, 0)),
		ev("aaaaa", "claimed", at(9, 7, 0)),
		ev("aaaaa", "heartbeat", at(9, 8, 0)),
		ev("aaaaa", "done", at(9, 9, 0)),
	)

	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 11 {
		t.Fatalf("got %d events, want 11 — one per row, with no type dropped", len(events))
	}
	seen := map[string]bool{}
	for i, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %d is unusable: %v", i, err)
		}
		if e.Shape != source.Point {
			t.Errorf("event %d has shape %q, want point", i, e.Shape)
		}
		if !e.End.Equal(e.Start) {
			t.Errorf("event %d spans %s; a point has no duration", i, e.End.Sub(e.Start))
		}
		if e.Source != EventsID {
			t.Errorf("event %d came from %q, want %q", i, e.Source, EventsID)
		}
		if e.Cause != source.CauseUserAction {
			t.Errorf("event %d has cause %q, want user-action", i, e.Cause)
		}
		if e.Identity.Document != "aaaaa" {
			t.Errorf("event %d is about %q, want the task short id", i, e.Identity.Document)
		}
		if e.Locator != filepath.Join(dir, DBName) {
			t.Errorf("event %d was read from %q, want %q", i, e.Locator, filepath.Join(dir, DBName))
		}
		seen[e.State] = true
	}
	for _, typ := range []string{"created", "criteria_added", "labeled", "blocked", "unblocked",
		"noted", "criterion_state", "focus_set", "claimed", "heartbeat", "done"} {
		if !seen[typ] {
			t.Errorf("no observation carries state %q; the event type must survive as the reading", typ)
		}
	}
}

// TestAnUnknownEventTypeIsStillAnObservation guards against a whitelist. The
// nine live databases already carry four types the task brief did not list —
// canceled, focus_released, unlabeled and moved — so a reader that enumerated
// what it knew would already have been wrong on the day it shipped.
func TestAnUnknownEventTypeIsStillAnObservation(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "an_event_type_invented_next_year", at(9, 0, 0)),
	)
	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 — an unrecognised type is still something somebody did", len(events))
	}
	if events[0].State != "an_event_type_invented_next_year" {
		t.Errorf("state = %q, want the type verbatim", events[0].State)
	}
}

// TestHeartbeatIsEvidence records a deliberate decision rather than an
// accident. `job heartbeat` is a command somebody ran to extend a claim, not a
// daemon's timer — the tool has no daemon — so under the settled policy that
// activity in the repo is working time it is evidence like any other row, and
// treating it otherwise would mean branching on the event type.
func TestHeartbeatIsEvidence(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(), ev("aaaaa", "heartbeat", at(9, 0, 0)))
	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Cause != source.CauseUserAction {
		t.Errorf("cause = %q, want user-action: a heartbeat is a command, not a timer", events[0].Cause)
	}
}

// --- One command is one observation. ---

// TestRowsFromOneCommandAtOneInstantCollapse is measured behaviour, not a
// tidy-up. `job criteria pass a b c` writes three criterion_state rows at one
// second on one task, and they are one action. Across the five registered
// databases 1,361 of 9,150 rows are these, dominated by criterion_state.
//
// Collapsing here rather than leaving it to the store's event_identity index
// is what keeps harvest's "N seen, M new" honest: the store would collapse
// them anyway, and the difference would then be indistinguishable from
// ordinary re-harvest deduplication.
func TestRowsFromOneCommandAtOneInstantCollapse(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		evDetail("aaaaa", "criterion_state", `{"short_id":"Huk"}`, at(9, 0, 0)),
		evDetail("aaaaa", "criterion_state", `{"short_id":"R9z"}`, at(9, 0, 0)),
		evDetail("aaaaa", "criterion_state", `{"short_id":"5LW"}`, at(9, 0, 0)),
	)
	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 — three criteria marked by one command at one instant", len(events))
	}
}

// TestSameInstantDifferentTaskStaysTwoObservations is the other half of the
// rule, and it is the one that would silently delete work if the key were too
// coarse: a session that creates a task tree writes many `created` rows in the
// same second, one per task.
func TestSameInstantDifferentTaskStaysTwoObservations(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "created", at(9, 0, 0)),
		ev("bbbbb", "created", at(9, 0, 0)),
	)
	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2 — two tasks created in one second are two things", len(events))
	}
}

// TestSameInstantDifferentTypeStaysTwoObservations completes the key.
func TestSameInstantDifferentTypeStaysTwoObservations(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "created", at(9, 0, 0)),
		ev("aaaaa", "labeled", at(9, 0, 0)),
	)
	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
}

// --- Claim spans. ---

func TestClaimToDoneIsAnInterval(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "done", at(9, 30, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if err := e.Validate(); err != nil {
		t.Fatalf("span is unusable: %v", err)
	}
	if e.Shape != source.Interval {
		t.Fatalf("shape = %q, want interval", e.Shape)
	}
	if !e.Start.Equal(at(9, 0, 0)) || !e.End.Equal(at(9, 30, 0)) {
		t.Errorf("span is %s..%s, want %s..%s", e.Start, e.End, at(9, 0, 0), at(9, 30, 0))
	}
	if e.Source != ClaimsID {
		t.Errorf("source = %q, want %q", e.Source, ClaimsID)
	}
	if e.State != ClaimState {
		t.Errorf("state = %q, want %q", e.State, ClaimState)
	}
	if e.Identity.Document != "aaaaa" {
		t.Errorf("document = %q, want the task short id", e.Identity.Document)
	}
}

func TestClaimToReleasedIsAnInterval(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "released", at(9, 10, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 1 || events[0].Shape != source.Interval {
		t.Fatalf("got %#v, want one interval", events)
	}
	if !events[0].End.Equal(at(9, 10, 0)) {
		t.Errorf("end = %s, want the release", events[0].End)
	}
}

// TestClaimToCanceledIsAnInterval covers a terminator the task brief did not
// name and the live databases do carry — 83 of them across nine files.
func TestClaimToCanceledIsAnInterval(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "canceled", at(9, 12, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 1 || events[0].Shape != source.Interval {
		t.Fatalf("got %#v, want one interval", events)
	}
}

// TestExpiredClaimIsUnboundedAndLost is criterion ZEG.
//
// An expiry is not an observation that work stopped; it is an observation that
// nobody said anything. Worse, the timestamp is not even the moment of expiry:
// three separate claims in the live timetattle database all carry
// claim_expired at exactly 1785529940, because the tool writes the row lazily
// when a later command notices. One of them had been claimed 146 minutes
// earlier against a thirty-minute expiry.
func TestExpiredClaimIsUnboundedAndLost(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "claim_expired", at(14, 0, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	e := events[0]
	if err := e.Validate(); err != nil {
		t.Fatalf("span is unusable: %v", err)
	}
	if e.Shape != source.Unbounded {
		t.Fatalf("shape = %q, want unbounded", e.Shape)
	}
	if e.Unknown != source.UnknownLost {
		t.Errorf("unknown = %q, want %q: re-reading cannot complete it", e.Unknown, source.UnknownLost)
	}
	if !e.Start.Equal(at(9, 0, 0)) {
		t.Errorf("start = %s, want the claim", e.Start)
	}
	if !e.End.IsZero() {
		t.Fatalf("end = %s, want no end at all; the expiry timestamp is not an observed end", e.End)
	}
	if e.Duration() != 0 {
		t.Errorf("duration = %s, want zero", e.Duration())
	}
}

// TestDoneAfterAnExpiryDoesNotPairWithTheExpiredClaim is a regression test for
// a figure that reached this task's own brief.
//
// The brief reported "claim -> done: MAX 583.7 min (9.7 hours)" as the case
// that would decide whether claim spans are safe. It is task 11 in the live
// timetattle database, and it is not a claim->done pair: the claim at
// 1785344333 was terminated by claim_expired at 1785373356, and the `done` at
// 1785379354 arrived 100 minutes later under no claim at all. Pairing a claim
// with the next `done` regardless of what lies between manufactures the
// nine-hour interval out of nothing.
func TestDoneAfterAnExpiryDoesNotPairWithTheExpiredClaim(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "claim_expired", at(17, 0, 0)),
		ev("aaaaa", "noted", at(17, 30, 0)),
		ev("aaaaa", "done", at(18, 40, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d spans, want 1", len(events))
	}
	if events[0].Shape != source.Interval {
		return // the expiry produced an unbounded span, which is what should happen
	}
	t.Fatalf("the claim paired with a `done` across its own expiry, inventing a %s interval",
		events[0].Duration())
}

// TestOpenClaimProducesNoSpan is the decision the store's dedup key forces,
// and it is the one place this package departs from the task brief.
//
// event_identity keys on (source_id, shape, endpoints, app, document, state)
// and does NOT include `unknown`. A horizon written for a claim that is open
// now, and the `lost` span written for that same claim once the tool declares
// it expired, would therefore share one key: the second insert hits ON
// CONFLICT DO NOTHING and the row stays labelled 'horizon' for ever, promising
// a completion that can never arrive. A horizon would buy nothing to pay for
// that, either — it anchors at the claim instant with zero duration, which is
// exactly what the `claimed` point in job-events already records, and this
// source re-derives every span from the database on every harvest rather than
// relying on supersession to finish anything.
//
// So an open claim is represented by its point and by no span, and the span
// appears once an end has actually been observed.
func TestOpenClaimProducesNoSpan(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "noted", at(9, 30, 0)),
	)
	spans := collect(t, NewClaims(dir), time.Time{})
	if len(spans) != 0 {
		t.Fatalf("got %d spans, want none: %#v", len(spans), spans)
	}
	points := collect(t, NewEvents(dir), time.Time{})
	if len(points) != 2 {
		t.Fatalf("got %d points, want 2; the claim itself must not disappear", len(points))
	}
}

// TestReclaimWithNoTerminatorLosesTheFirstClaim describes a sequence no live
// database contains — measured 2026-08-01, zero occurrences across all nine —
// which is exactly why it has to be constructed. A rule validated only against
// data that never exercises it is a rule nobody has tested.
func TestReclaimWithNoTerminatorLosesTheFirstClaim(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "claimed", at(10, 0, 0)),
		ev("aaaaa", "done", at(10, 20, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 2 {
		t.Fatalf("got %d spans, want 2", len(events))
	}
	sort.Slice(events, func(i, j int) bool { return events[i].Anchor().Before(events[j].Anchor()) })
	if events[0].Shape != source.Unbounded || events[0].Unknown != source.UnknownLost {
		t.Errorf("first span is %s/%s, want unbounded/lost: its end was never observed",
			events[0].Shape, events[0].Unknown)
	}
	if !events[0].End.IsZero() {
		t.Errorf("first span ends at %s; nothing observed an end", events[0].End)
	}
	if events[1].Shape != source.Interval || !events[1].End.Equal(at(10, 20, 0)) {
		t.Errorf("second span is %s ending %s, want an interval ending at the done",
			events[1].Shape, events[1].End)
	}
}

// TestClaimsOnDifferentTasksDoNotTerminateEachOther guards the pairing key.
func TestClaimsOnDifferentTasksDoNotTerminateEachOther(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("bbbbb", "claimed", at(9, 1, 0)),
		ev("bbbbb", "done", at(9, 2, 0)),
		ev("aaaaa", "done", at(9, 40, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 2 {
		t.Fatalf("got %d spans, want 2", len(events))
	}
	byTask := map[string]source.Event{}
	for _, e := range events {
		byTask[e.Identity.Document] = e
	}
	if got := byTask["aaaaa"].Duration(); got != 40*time.Minute {
		t.Errorf("task aaaaa spans %s, want 40m", got)
	}
	if got := byTask["bbbbb"].Duration(); got != time.Minute {
		t.Errorf("task bbbbb spans %s, want 1m", got)
	}
}

// TestNonTerminatingEventsDoNotEndAClaim: a claim survives being blocked,
// noted on, or heartbeated.
func TestNonTerminatingEventsDoNotEndAClaim(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "blocked", at(9, 5, 0)),
		ev("aaaaa", "noted", at(9, 6, 0)),
		ev("aaaaa", "heartbeat", at(9, 20, 0)),
		ev("aaaaa", "unblocked", at(9, 25, 0)),
		ev("aaaaa", "done", at(9, 30, 0)),
	)
	events := collect(t, NewClaims(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d spans, want 1", len(events))
	}
	if events[0].Duration() != 30*time.Minute {
		t.Errorf("span is %s, want 30m", events[0].Duration())
	}
}

// --- Nothing branches on actor (criterion zSZ). ---

// TestActorChangesNothing proves the policy behaviourally rather than by
// inspection: two databases identical but for who acted produce byte-identical
// events. The implementation does not read the actor column at all, which is
// what makes this pass.
func TestActorChangesNothing(t *testing.T) {
	rows := func(actor string) []row {
		return []row{
			evActor("aaaaa", "created", actor, at(9, 0, 0)),
			evActor("aaaaa", "claimed", actor, at(9, 1, 0)),
			evActor("aaaaa", "noted", actor, at(9, 2, 0)),
			evActor("aaaaa", "done", actor, at(9, 30, 0)),
		}
	}
	human := writeJobsDB(t, filepath.Join(t.TempDir(), "human"), rows("ben")...)
	agent := writeJobsDB(t, filepath.Join(t.TempDir(), "agent"), rows("opus-hdl9u")...)

	for _, s := range []struct {
		name       string
		a, b       source.Source
		normalizer func([]source.Event) []source.Event
	}{
		{name: EventsID, a: NewEvents(human), b: NewEvents(agent)},
		{name: ClaimsID, a: NewClaims(human), b: NewClaims(agent)},
	} {
		got := stripLocator(collect(t, s.a, time.Time{}))
		want := stripLocator(collect(t, s.b, time.Time{}))
		if len(got) == 0 {
			t.Fatalf("%s produced nothing to compare", s.name)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: a human's rows and an agent's rows produced different events:\n%#v\n%#v",
				s.name, got, want)
		}
	}
}

// stripLocator removes the only field that legitimately differs between two
// copies of the same evidence in two directories.
func stripLocator(events []source.Event) []source.Event {
	out := make([]source.Event, len(events))
	copy(out, events)
	for i := range out {
		out[i].Locator = ""
	}
	return out
}

// --- Absent, empty, and unreadable are three different things (criterion kLP). ---

// TestNoDatabaseIsNotAFailure. A registered directory need not use `job` at
// all; most registered directories are not obliged to.
//
// The absent directory is read BESIDE one that has rows, so that "tolerated"
// cannot be satisfied by a collector that reads nothing anywhere.
func TestNoDatabaseIsNotAFailure(t *testing.T) {
	base := t.TempDir()
	absent := filepath.Join(base, "absent")
	if err := os.MkdirAll(absent, 0o755); err != nil {
		t.Fatal(err)
	}
	present := writeJobsDB(t, filepath.Join(base, "present"),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "done", at(9, 5, 0)),
	)

	for _, s := range []source.Source{NewEvents(absent, present), NewClaims(absent, present)} {
		events, err := s.Collect(t.Context(), time.Time{})
		if err != nil {
			t.Errorf("%s: a directory with no %s returned %v, want no error", s.ID(), DBName, err)
		}
		for _, e := range events {
			if e.Locator != filepath.Join(present, DBName) {
				t.Errorf("%s: event read from %q, want only the present database", s.ID(), e.Locator)
			}
		}
		if len(events) == 0 {
			t.Errorf("%s: the directory that does have a database contributed nothing", s.ID())
		}
	}
}

// TestEmptyDatabaseIsObservedRatherThanAbsent. A `job` database that has been
// initialised and never used is a different fact from no database at all, and
// both are different again from one that could not be read. This is the middle
// state, and it is a success with nothing in it.
func TestEmptyDatabaseIsObservedRatherThanAbsent(t *testing.T) {
	base := t.TempDir()
	empty := writeJobsDB(t, filepath.Join(base, "empty"))
	present := writeJobsDB(t, filepath.Join(base, "present"),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "released", at(9, 5, 0)),
	)

	for _, s := range []source.Source{NewEvents(empty, present), NewClaims(empty, present)} {
		events, err := s.Collect(t.Context(), time.Time{})
		if err != nil {
			t.Errorf("%s: an empty database returned %v, want no error", s.ID(), err)
		}
		if len(events) == 0 {
			t.Fatalf("%s: nothing came back at all, so nothing here is being measured", s.ID())
		}
		for _, e := range events {
			if e.Locator == filepath.Join(empty, DBName) {
				t.Errorf("%s: the empty database produced an event", s.ID())
			}
		}
	}
}

// TestUnreadableDatabaseIsAnError is the criterion that matters most, and the
// bug this project has now shipped six times. A file that is present and
// cannot be parsed must never answer with a silent zero.
func TestUnreadableDatabaseIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DBName), []byte("this is not a database"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range Sources(dir) {
		events, err := s.Collect(t.Context(), time.Time{})
		if err == nil {
			t.Errorf("%s: a corrupt %s returned %d events and no error", s.ID(), DBName, len(events))
		}
	}
}

// TestDatabaseAtADifferentSchemaIsAnError. The nine live files are written by
// a tool this repository does not control and need not all be at one
// migration. A missing table must be reported by name, not read as emptiness.
func TestDatabaseAtADifferentSchemaIsAnError(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", "file:"+filepath.Join(dir, DBName))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE something_else (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	db.Close()

	for _, s := range Sources(dir) {
		if _, err := s.Collect(t.Context(), time.Time{}); err == nil {
			t.Errorf("%s: a database with no events table returned no error", s.ID())
		}
	}
}

// TestOneUnreadableDatabaseDoesNotSilentlyDropTheOthers. With every registered
// directory read by a single collector into a single harvest_run row, a
// failure that returned the readable roots' events alongside an error would be
// written as a success by cmd/timetattle's ingest — it discards events
// whenever Collect errors — and a partial harvest would look complete. So the
// failure is total and loud. Nothing is lost by it: .jobs.db is durable and
// this source re-reads all of it on every harvest.
func TestOneUnreadableDatabaseDoesNotSilentlyDropTheOthers(t *testing.T) {
	base := t.TempDir()
	good := writeJobsDB(t, filepath.Join(base, "good"), ev("aaaaa", "created", at(9, 0, 0)))
	bad := filepath.Join(base, "bad")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad, DBName), []byte("not a database"), 0o644); err != nil {
		t.Fatal(err)
	}

	events, err := NewEvents(good, bad).Collect(t.Context(), time.Time{})
	if err == nil {
		t.Fatalf("got %d events and no error; one unreadable database must fail the run", len(events))
	}
	if len(events) != 0 {
		t.Errorf("got %d events beside the error; ingest would discard them and record a success", len(events))
	}
}

// --- Several roots, one collector. ---

func TestEveryRootIsRead(t *testing.T) {
	base := t.TempDir()
	one := writeJobsDB(t, filepath.Join(base, "one"), ev("aaaaa", "created", at(9, 0, 0)))
	two := writeJobsDB(t, filepath.Join(base, "two"),
		ev("bbbbb", "created", at(9, 0, 0)),
		ev("bbbbb", "done", at(9, 5, 0)),
	)
	none := filepath.Join(base, "none")
	if err := os.MkdirAll(none, 0o755); err != nil {
		t.Fatal(err)
	}

	events := collect(t, NewEvents(one, two, none), time.Time{})
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 across two databases", len(events))
	}
	locators := map[string]int{}
	for _, e := range events {
		locators[e.Locator]++
	}
	if locators[filepath.Join(one, DBName)] != 1 || locators[filepath.Join(two, DBName)] != 2 {
		t.Errorf("locators = %v; each event must name the database it was read from", locators)
	}
	for i := 1; i < len(events); i++ {
		if events[i].Anchor().Before(events[i-1].Anchor()) {
			t.Fatalf("events are not in time order at %d", i)
		}
	}
}

// TestRelativeRootsBecomeAbsoluteLocators. source.Event.Validate refuses a
// relative locator because it can never prefix-match a registered directory,
// so the work would attribute to nothing and vanish.
func TestRelativeRootsBecomeAbsoluteLocators(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(), ev("aaaaa", "created", at(9, 0, 0)))
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(wd, dir)
	if err != nil {
		t.Skipf("filepath.Rel(%s, %s): %v", wd, dir, err)
	}
	events := collect(t, NewEvents(rel), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if err := events[0].Validate(); err != nil {
		t.Fatalf("event is unusable: %v", err)
	}
	if !filepath.IsAbs(events[0].Locator) {
		t.Errorf("locator = %q, want an absolute path", events[0].Locator)
	}
}

// --- since ---

func TestSinceDropsSettledObservations(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "created", at(9, 0, 0)),
		ev("aaaaa", "noted", at(11, 0, 0)),
	)
	events := collect(t, NewEvents(dir), at(10, 0, 0))
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if !events[0].Start.Equal(at(11, 0, 0)) {
		t.Errorf("kept the event at %s, want the one at 11:00", events[0].Start)
	}
}

// TestSinceKeepsAnUnfinishedClaim. A claim whose end was never observed has
// reached no instant at which it could be considered past, so it comes back
// whatever since says — the contract source.Source.Collect states.
func TestSinceKeepsAnUnfinishedClaim(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "claim_expired", at(9, 40, 0)),
	)
	events := collect(t, NewClaims(dir), at(20, 0, 0))
	if len(events) != 1 {
		t.Fatalf("got %d events, want the lost span back regardless of since", len(events))
	}
}

// --- Reading a live database. ---

// TestCollectDoesNotWriteToTheDatabase. The `job` tool may be writing to the
// same file while a harvest runs — it certainly was while this package was
// built — so the database is opened read-only.
func TestCollectDoesNotWriteToTheDatabase(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(), ev("aaaaa", "created", at(9, 0, 0)))
	path := filepath.Join(dir, DBName)
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if n := len(collect(t, NewEvents(dir), time.Time{})); n != 1 {
		t.Fatalf("read %d events; a collector that reads nothing writes nothing either", n)
	}
	collect(t, NewClaims(dir), time.Time{})
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("the database changed while being read: %d bytes at %s became %d bytes at %s",
			before.Size(), before.ModTime(), after.Size(), after.ModTime())
	}
}

// TestEventWithNoTaskIsStillAnObservation. task_id is nullable in seven of the
// nine live schemas, and a LEFT JOIN is what keeps a row whose task was
// deleted from disappearing along with it. No live database has one today.
func TestEventWithNoTaskIsStillAnObservation(t *testing.T) {
	dir := writeJobsDB(t, t.TempDir(), row{typ: "created", actor: "ben", at: at(9, 0, 0)})
	events := collect(t, NewEvents(dir), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Identity.Document != "" {
		t.Errorf("document = %q, want empty for a row with no task", events[0].Identity.Document)
	}
	if err := events[0].Validate(); err != nil {
		t.Errorf("event is unusable: %v", err)
	}
}

// TestSourcesReadsBothStreamsFromOneRootSet. Sources is what cmd/timetattle
// plans from, so it has to hand back both collectors already pointed at every
// registered directory — a set of roots that reached only one of them would
// lose a whole stream silently.
func TestSourcesReadsBothStreamsFromOneRootSet(t *testing.T) {
	base := t.TempDir()
	one := writeJobsDB(t, filepath.Join(base, "one"),
		ev("aaaaa", "claimed", at(9, 0, 0)),
		ev("aaaaa", "done", at(9, 5, 0)),
	)
	two := writeJobsDB(t, filepath.Join(base, "two"),
		ev("bbbbb", "claimed", at(10, 0, 0)),
		ev("bbbbb", "done", at(10, 5, 0)),
	)

	got := Sources(one, two)
	if len(got) != 2 {
		t.Fatalf("Sources returned %d sources, want 2", len(got))
	}
	want := map[string]int{EventsID: 4, ClaimsID: 2}
	for _, s := range got {
		n, known := want[s.ID()]
		if !known {
			t.Fatalf("unexpected source %q", s.ID())
		}
		if s.Kind() != Kind {
			t.Errorf("%s reports kind %q, want %q", s.ID(), s.Kind(), Kind)
		}
		if got := len(collect(t, s, time.Time{})); got != n {
			t.Errorf("%s returned %d events across two roots, want %d", s.ID(), got, n)
		}
	}
}
