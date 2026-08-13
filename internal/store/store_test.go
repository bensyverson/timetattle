package store_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

const sourceID = "biome-appusage"

// fixture is a store on a temporary file, plus the path so a test can look at
// what is actually in the columns rather than at what the store says is there.
type fixture struct {
	*store.Store
	path string
}

func newStore(t *testing.T) fixture {
	t.Helper()
	// A nested directory that does not exist: opening the store is what creates
	// ~/.timetattle on a fresh machine, so it has to make its own parents.
	path := filepath.Join(t.TempDir(), "timetattle", "timetattle.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("Open(%s): %v", path, err)
	}
	t.Cleanup(func() { st.Close() })
	return fixture{Store: st, path: path}
}

// raw is a second connection, used only to read the columns directly.
func (f fixture) raw(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+f.path)
	if err != nil {
		t.Fatalf("open %s directly: %v", f.path, err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func (f fixture) run(t *testing.T) *store.Run {
	t.Helper()
	run, err := f.BeginRun(context.Background(), store.RunSpec{SourceID: sourceID, Input: "/tmp/stream"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	return run
}

func (f fixture) write(t *testing.T, run *store.Run, events ...source.Event) int {
	t.Helper()
	n, err := run.Write(context.Background(), events)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	return n
}

// all reads back everything, which needs no bounds: a span query with no ends
// is the whole store.
func (f fixture) all(t *testing.T) []source.Event {
	t.Helper()
	events, err := f.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	return events
}

func (f fixture) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.raw(t).QueryRow(`SELECT COUNT(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

var (
	nine   = time.Date(2026, 7, 14, 9, 0, 0, 0, time.UTC)
	tenish = nine.Add(90 * time.Minute)
)

func focus(start, end time.Time, app string) source.Event {
	return source.NewInterval(start, end, source.Identity{App: app}, sourceID)
}

// stamp renders an instant the way it is stored, so a failure prints something
// comparable rather than two Go time structs.
func stamp(at time.Time) string {
	if at.IsZero() {
		return "-"
	}
	return at.UTC().Format(store.TimeLayout)
}

// describe is the whole of what an event claims, for comparing a round trip.
func describe(e source.Event) string {
	return fmt.Sprintf("source=%s shape=%s unknown=%s start=%s end=%s app=%s doc=%s state=%s locator=%s project=%s cause=%s",
		e.Source, e.Shape, e.Unknown, stamp(e.Start), stamp(e.End),
		e.Identity.App, e.Identity.Document, e.State, e.Locator, e.Identity.Project, e.Cause)
}

func TestOpenCreatesTheDatabaseAndItsParents(t *testing.T) {
	f := newStore(t)
	if _, err := os.Stat(f.path); err != nil {
		t.Fatalf("no database at %s: %v", f.path, err)
	}
	for _, table := range []string{"event", "harvest_run", "schema_migration"} {
		var name string
		err := f.raw(t).QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %q is missing after Open: %v", table, err)
		}
	}
}

// Open runs on every start, so it has to be a no-op against a store that is
// already current — and it must not lose what is in it.
func TestReopeningAnExistingStoreKeepsItsEvents(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"))
	if err := run.Finish(context.Background()); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	again, err := store.Open(f.path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer again.Close()

	events, err := again.Events(context.Background(), store.Filter{})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(events) != 1 {
		t.Errorf("reopened store holds %d events, want 1", len(events))
	}
}

// events_seen is what the source returned and events_new is what survived
// dedup. They are NOT equal even on a fresh database: knowledgeC repeats itself
// — 2026-07-10 15:22:32 carries three rows identical in stream, endpoints and
// value — and collapsing those is correct, not a bug.
func TestARunRecordsWhatItSawAndWhatWasNew(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	repeated := focus(nine, tenish, "com.apple.Safari")
	inserted := f.write(t, run,
		focus(nine, tenish, "com.apple.motionapp"),
		repeated,
		repeated,
	)
	if inserted != 2 {
		t.Errorf("Write reported %d new events out of 3, want 2", inserted)
	}

	got, err := f.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got.EventsSeen != 3 || got.EventsNew != 2 {
		t.Errorf("run recorded seen=%d new=%d, want seen=3 new=2", got.EventsSeen, got.EventsNew)
	}
}

// The property the whole natural key exists for. Collectors re-read overlapping
// windows rather than tailing, so the same evidence arrives again on every run.
func TestASecondHarvestOfTheSameWindowInsertsNothing(t *testing.T) {
	f := newStore(t)
	events := []source.Event{
		focus(nine, tenish, "com.apple.motionapp"),
		source.NewOpenStart(tenish, source.UnknownSleep, source.Identity{App: "com.apple.Safari"}, sourceID),
		source.NewOpenEnd(tenish, source.UnknownHorizon, source.Identity{App: "com.googlecode.iterm2"}, sourceID),
		source.NewPoint(nine, source.Identity{App: "com.apple.motionapp", Document: "dark-crystal-v2"}, sourceID),
	}

	first := f.run(t)
	if n := f.write(t, first, events...); n != len(events) {
		t.Fatalf("first harvest inserted %d of %d events", n, len(events))
	}
	before := f.count(t)

	second := f.run(t)
	if n := f.write(t, second, events...); n != 0 {
		t.Errorf("re-harvesting the same window inserted %d events, want 0", n)
	}
	if after := f.count(t); after != before {
		t.Errorf("the store grew from %d rows to %d on a re-harvest", before, after)
	}

	got, err := f.LoadRun(context.Background(), second.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got.EventsSeen != len(events) || got.EventsNew != 0 {
		t.Errorf("second run recorded seen=%d new=%d, want seen=%d new=0", got.EventsSeen, got.EventsNew, len(events))
	}
}

func TestARunCountsEveryBatchItIsGiven(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"))
	f.write(t, run, focus(tenish, tenish.Add(time.Hour), "com.apple.Safari"))

	got, err := f.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got.EventsSeen != 2 || got.EventsNew != 2 {
		t.Errorf("run recorded seen=%d new=%d after two batches, want 2 and 2", got.EventsSeen, got.EventsNew)
	}
}

// An empty window is the whole point of recording runs. "No Motion work on the
// 22nd" and "no harvest ran on the 22nd" are different facts, and only a run
// that found nothing can tell them apart.
func TestARunThatFoundNothingIsStillRecorded(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	if err := run.Finish(context.Background()); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	got, err := f.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got.Status != store.StatusOK {
		t.Errorf("status is %q, want %q", got.Status, store.StatusOK)
	}
	if got.EventsSeen != 0 || got.EventsNew != 0 {
		t.Errorf("empty run recorded seen=%d new=%d, want 0 and 0", got.EventsSeen, got.EventsNew)
	}
	if got.FinishedAt.IsZero() {
		t.Error("a finished run has no finish time")
	}
	if got.SourceID != sourceID || got.Input != "/tmp/stream" {
		t.Errorf("run describes source=%q input=%q, want %q and /tmp/stream", got.SourceID, got.Input, sourceID)
	}
}

// Collect has partial-failure semantics: one broken source loses only its own
// events. That loss has to be recorded against the source, with the reason, or
// it is invisible in every number downstream.
func TestAFailedRunSaysWhyAndKeepsWhatItAlreadyWrote(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"))

	cause := errors.New("cannot read the Biome stream: permission denied")
	if err := run.Fail(context.Background(), cause); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	got, err := f.LoadRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if got.Status != store.StatusFailed {
		t.Errorf("status is %q, want %q", got.Status, store.StatusFailed)
	}
	if !strings.Contains(got.Error, "permission denied") {
		t.Errorf("recorded error is %q, which does not say what went wrong", got.Error)
	}
	if n := len(f.all(t)); n != 1 {
		t.Errorf("a failed run discarded its events; %d remain, want 1", n)
	}
}

func TestAFailedRunNeedsAReason(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	if err := run.Fail(context.Background(), nil); err == nil {
		t.Error("Fail accepted a nil cause; the schema refuses a failure with no reason")
	}
}

func TestAFinishedRunIsClosedToFurtherWriting(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()
	run := f.run(t)
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	if err := run.Finish(ctx); err == nil {
		t.Error("a run was finished twice")
	}
	if _, err := run.Write(ctx, []source.Event{focus(nine, tenish, "com.apple.Safari")}); err == nil {
		t.Error("events were written to a finished run; their provenance would be a lie")
	}
}

func TestEveryFieldTheSourceReportedSurvivesTheRoundTrip(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	interval := focus(nine, tenish, "com.apple.motionapp")
	point := source.NewPoint(nine.Add(time.Minute), source.Identity{
		App:      "com.apple.motionapp",
		Document: "dark-crystal-v2",
	}, "motion-autosave")
	point.Locator = "/Users/you/Movies/Motion Projects/Autosave Vault/dark-crystal/v2.motn"
	// A Motion autosave is written by a wall-clock timer, which is the whole of
	// what makes it weaker evidence than a save. Nothing can recover that from
	// the row afterwards, so it has to survive the write.
	point.Cause = source.CauseTimer
	stop := source.NewOpenStart(tenish, source.UnknownSleep, source.Identity{App: "com.apple.Safari"}, sourceID)
	horizon := source.NewOpenEnd(tenish.Add(time.Hour), source.UnknownHorizon, source.Identity{App: "com.googlecode.iterm2"}, sourceID)
	presence := source.NewInterval(nine, tenish, source.Identity{}, "knowledgec-backlight")
	presence.State = "off"

	want := []source.Event{interval, presence, point, stop, horizon}
	f.write(t, run, want...)

	got := f.all(t)
	if len(got) != len(want) {
		t.Fatalf("read back %d events, want %d", len(got), len(want))
	}
	seen := map[string]bool{}
	for _, e := range got {
		seen[describe(e)] = true
	}
	for _, e := range want {
		if !seen[describe(e)] {
			t.Errorf("event did not survive the round trip:\n want %s\n  got %v", describe(e), keys(seen))
		}
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// Evidence that observes no file must store NULL rather than the empty string,
// because the schema refuses a locator that is not an absolute path and the
// empty string is not one. The same holds for every other optional column: empty and NULL are the
// same value in the natural key, and only one of them is a fact.
func TestAbsentDetailIsStoredAsNullRatherThanAnEmptyString(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"))

	var app string
	var document, state, locator, project, unknown, cause sql.NullString
	err := f.raw(t).QueryRow(`SELECT app, document, state, locator, project, unknown, cause FROM event`).
		Scan(&app, &document, &state, &locator, &project, &unknown, &cause)
	if err != nil {
		t.Fatalf("read the row: %v", err)
	}
	if app != "com.apple.motionapp" {
		t.Errorf("app is %q", app)
	}
	// cause is NULL here for a reason that is not tidiness. A focus interval is
	// not an artifact, so nothing caused it in the sense a manifest can declare,
	// and NULL is the only spelling of that which cannot be mistaken for a source
	// that failed to say.
	for name, col := range map[string]sql.NullString{
		"document": document, "state": state, "locator": locator,
		"project": project, "unknown": unknown, "cause": cause,
	} {
		if col.Valid {
			t.Errorf("%s is %q; what was never observed must be NULL", name, col.String)
		}
	}
}

// project is a recomputable cache owned by a resolution pass against the
// registry, so nothing is written into it at collect time. Identity.Project is a
// different thing wearing the same word — for Motion it is the composition
// folder, not a client project — and storing it here would look exactly like a
// resolved attribution while being nothing of the kind.
func TestASourceSuppliedProjectIsNotStored(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	e := source.NewPoint(nine, source.Identity{
		App:      "com.apple.motionapp",
		Document: "v2",
		Project:  "dark-crystal",
	}, "motion-autosave")
	e.Locator = "/Users/you/Movies/Motion Projects/Autosave Vault/dark-crystal/v2.motn"
	f.write(t, run, e)

	got := f.all(t)
	if len(got) != 1 {
		t.Fatalf("read back %d events, want 1", len(got))
	}
	if got[0].Identity.Project != "" {
		t.Errorf("project came back as %q; it is filled by resolution, not by collection", got[0].Identity.Project)
	}
	if got[0].Identity.Document != "v2" || got[0].Locator == "" {
		t.Errorf("the observation itself was not kept: %s", describe(got[0]))
	}
}

// Locator is provenance rather than identity, so the same observation read from
// the live stream and from a snapshot of it is one row. The first path stored
// wins, which is arbitrary but harmless: both name a place the same evidence was.
func TestTheSameObservationReadFromTwoPathsIsOneEvent(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	live := source.NewPoint(nine, source.Identity{App: "com.apple.motionapp", Document: "v2"}, "motion-autosave")
	live.Locator = "/Users/you/Movies/Motion Projects/Autosave Vault/dark-crystal/v2.motn"
	replayed := live
	replayed.Locator = "/Users/you/.timetattle/snapshots/2026-07-29/dark-crystal/v2.motn"

	if n := f.write(t, run, live, replayed); n != 1 {
		t.Errorf("the same autosave read from two paths inserted %d rows, want 1", n)
	}
	got := f.all(t)
	if len(got) != 1 || got[0].Locator != live.Locator {
		t.Errorf("stored %d rows (%v), want one carrying the path it was first read from", len(got), got)
	}
}

// The store is the last line rather than the only one — source.Registry drops
// invalid events before they get here — so an unusable event reaching it is a
// bug, and the batch fails loudly rather than being quietly half-written.
func TestWriteRefusesABatchHoldingAnUnusableEvent(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	bad := focus(nine, tenish, "com.apple.motionapp")
	bad.Source = ""
	_, err := run.Write(context.Background(), []source.Event{
		focus(nine, tenish, "com.apple.Safari"),
		bad,
	})
	if err == nil {
		t.Fatal("Write accepted an event that names no source")
	}
	if n := f.count(t); n != 0 {
		t.Errorf("a refused batch left %d rows behind; it must be all or nothing", n)
	}
}

// Supersession is a derivation, never a mutation. The horizon row and the
// complete interval that later replaces it are two observations, and both
// persist untouched: collapsing them is the canonical view's job.
func TestAnObservationCompletedLaterDoesNotReplaceTheEarlierOne(t *testing.T) {
	f := newStore(t)
	first := f.run(t)
	f.write(t, first, source.NewOpenEnd(nine, source.UnknownHorizon, source.Identity{App: "com.apple.motionapp"}, sourceID))

	second := f.run(t)
	if n := f.write(t, second, focus(nine, tenish, "com.apple.motionapp")); n != 1 {
		t.Errorf("the completed interval inserted %d rows, want 1", n)
	}
	if n := f.count(t); n != 2 {
		t.Errorf("the store holds %d rows, want 2; the earlier observation must survive", n)
	}
}

func TestEventsReadsBackOverASpan(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	day := time.Date(2026, 7, 14, 0, 0, 0, 0, time.UTC)
	before := focus(day.Add(-2*time.Hour), day.Add(-time.Hour), "before")
	overlapping := focus(day.Add(-30*time.Minute), day.Add(30*time.Minute), "overlapping")
	inside := focus(day.Add(2*time.Hour), day.Add(3*time.Hour), "inside")
	after := focus(day.Add(25*time.Hour), day.Add(26*time.Hour), "after")
	instant := source.NewPoint(day.Add(4*time.Hour), source.Identity{App: "instant"}, sourceID)
	bareStop := source.NewOpenStart(day.Add(5*time.Hour), source.UnknownSleep, source.Identity{App: "bare-stop"}, sourceID)
	horizon := source.NewOpenEnd(day.Add(6*time.Hour), source.UnknownHorizon, source.Identity{App: "horizon"}, sourceID)
	elsewhere := source.NewOpenStart(day.Add(30*time.Hour), source.UnknownSleep, source.Identity{App: "elsewhere"}, sourceID)

	f.write(t, run, before, overlapping, inside, after, instant, bareStop, horizon, elsewhere)

	got, err := f.Events(context.Background(), store.Filter{
		From: day,
		To:   day.Add(24 * time.Hour).Add(-time.Millisecond),
	})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}

	var apps []string
	for _, e := range got {
		apps = append(apps, e.Identity.App)
	}
	want := []string{"overlapping", "inside", "instant", "bare-stop", "horizon"}
	if fmt.Sprint(apps) != fmt.Sprint(want) {
		t.Errorf("the day holds %v, want %v (in anchor order)", apps, want)
	}
}

func TestEventsCanBeReadForOneSource(t *testing.T) {
	f := newStore(t)
	run := f.run(t)
	presence := source.NewInterval(nine, tenish, source.Identity{}, "knowledgec-backlight")
	presence.State = "on"
	f.write(t, run, focus(nine, tenish, "com.apple.motionapp"), presence)

	got, err := f.Events(context.Background(), store.Filter{Source: "knowledgec-backlight"})
	if err != nil {
		t.Fatalf("Events: %v", err)
	}
	if len(got) != 1 || got[0].State != "on" {
		t.Fatalf("filtering by source returned %d events (%v), want the one presence row", len(got), got)
	}
}
