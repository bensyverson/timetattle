package knowledgec

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// collect reads a source and fails the test rather than returning an error, so
// the bodies below describe observations instead of error handling.
func collect(t *testing.T, s source.Source, since time.Time) []source.Event {
	t.Helper()
	events, err := s.Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("%s Collect: %v", s.ID(), err)
	}
	for i, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("%s event %d is unusable: %v", s.ID(), i, err)
		}
	}
	return events
}

// mixed is a snapshot holding all four streams at once, which is what a real one
// is. Every test that reads one stream reads it out of this, because a source
// that quietly collected its neighbours' rows would otherwise pass.
func mixed(t *testing.T) string {
	t.Helper()
	return writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		backlit(at(9, 0, 0), at(9, 30, 0), 1),
		backlit(at(9, 30, 0), at(10, 0, 0), 0),
		locked(at(9, 30, 0), at(10, 0, 0), 1),
		locked(at(10, 0, 0), at(10, 5, 0), 0),
		used(at(9, 0, 0), at(9, 20, 0), "com.apple.motionapp"),
		played(at(9, 5, 0), at(9, 6, 0), "com.apple.Safari"),
	)
}

func TestBacklightReadsBothReadingsAsIntervals(t *testing.T) {
	events := collect(t, NewBacklight(mixed(t)), time.Time{})

	if len(events) != 2 {
		t.Fatalf("got %d backlight events, want the 2 /display/isBacklit rows", len(events))
	}
	for _, e := range events {
		if e.Shape != source.Interval {
			t.Errorf("event at %s has shape %q, want %q", e.Start, e.Shape, source.Interval)
		}
		if e.Source != "knowledgec-backlit" {
			t.Errorf("event is attributed to %q, want knowledgec-backlit", e.Source)
		}
		if e.Identity != (source.Identity{}) {
			t.Errorf("presence event carries identity %+v, want none; presence is not about an application", e.Identity)
		}
		if e.Locator != "" {
			t.Errorf("presence event carries locator %q, want none; presence is not about a file", e.Locator)
		}
	}
	if got := events[0].State; got != "on" {
		t.Errorf("the isBacklit=1 row reads %q, want \"on\"", got)
	}
	if got := events[1].State; got != "off" {
		t.Errorf("the isBacklit=0 row reads %q, want \"off\"", got)
	}
	if !events[0].Start.Equal(at(9, 0, 0)) || !events[0].End.Equal(at(9, 30, 0)) {
		t.Errorf("first row is %s–%s, want %s–%s", events[0].Start, events[0].End, at(9, 0, 0), at(9, 30, 0))
	}
}

// The epoch is the one conversion that fails by three decades rather than by a
// little, so it is checked against an instant written independently of it.
func TestTimestampsAreLocalCoreDataInstants(t *testing.T) {
	events := collect(t, NewBacklight(mixed(t)), time.Time{})

	if got := events[0].Start; !got.Equal(at(9, 0, 0)) {
		t.Fatalf("the first row is dated %s, want %s", got, at(9, 0, 0))
	}
	// Local rather than UTC, matching the other readers of local sources: the
	// store normalises on write, and a reader that pre-normalised would leave
	// every printed time and every day bucket in this package's callers wrong.
	if loc := events[0].Start.Location(); loc != time.Local {
		t.Errorf("the first row is in %s, want the local zone; internal/store normalises on write", loc)
	}
}

func TestLockedReadsOneAsLocked(t *testing.T) {
	events := collect(t, NewLocked(mixed(t)), time.Time{})

	if len(events) != 2 {
		t.Fatalf("got %d lock events, want the 2 /device/isLocked rows", len(events))
	}
	if events[0].State != "locked" || events[1].State != "unlocked" {
		t.Errorf("lock readings are %q and %q, want \"locked\" and \"unlocked\"", events[0].State, events[1].State)
	}
	for _, e := range events {
		if e.Source != "knowledgec-islocked" {
			t.Errorf("event is attributed to %q, want knowledgec-islocked", e.Source)
		}
		if e.Identity != (source.Identity{}) {
			t.Errorf("lock event carries identity %+v, want none", e.Identity)
		}
		if e.Shape != source.Interval {
			t.Errorf("lock event has shape %q, want %q", e.Shape, source.Interval)
		}
	}
}

func TestAppUsageCarriesTheBundleIdentifierAndNoReading(t *testing.T) {
	events := collect(t, NewAppUsage(mixed(t)), time.Time{})

	if len(events) != 1 {
		t.Fatalf("got %d app-usage events, want the 1 /app/usage row", len(events))
	}
	e := events[0]
	if e.Source != "knowledgec-appusage" {
		t.Errorf("event is attributed to %q, want knowledgec-appusage", e.Source)
	}
	if e.Identity.App != "com.apple.motionapp" {
		t.Errorf("event names application %q, want com.apple.motionapp", e.Identity.App)
	}
	if e.State != "" {
		t.Errorf("app usage carries reading %q; it takes no scalar reading", e.State)
	}
	if e.Shape != source.Interval {
		t.Errorf("event has shape %q, want %q", e.Shape, source.Interval)
	}
	if e.Locator != "" {
		t.Errorf("event carries locator %q, want none", e.Locator)
	}
}

func TestMediaUsageCarriesTheBundleIdentifier(t *testing.T) {
	events := collect(t, NewMediaUsage(mixed(t)), time.Time{})

	if len(events) != 1 {
		t.Fatalf("got %d media-usage events, want the 1 /app/mediaUsage row", len(events))
	}
	if events[0].Identity.App != "com.apple.Safari" {
		t.Errorf("event names application %q, want com.apple.Safari", events[0].Identity.App)
	}
	if events[0].Source != "knowledgec-mediausage" {
		t.Errorf("event is attributed to %q, want knowledgec-mediausage", events[0].Source)
	}
}

// A zero-length row satisfies both the point and the interval CHECK, so nothing
// downstream can recover from a collector that chooses shape by measured
// duration: shape is part of an event's identity, and the same observation would
// persist twice, once under each. 113 of 1,670 rows in a real snapshot are this.
func TestAZeroLengthRowIsStillAnInterval(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		backlit(at(9, 1, 12), at(9, 1, 12), 0),
	)

	events := collect(t, NewBacklight(path), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Shape != source.Interval {
		t.Errorf("a zero-length row has shape %q, want %q; knowledgeC reported a start and an end",
			events[0].Shape, source.Interval)
	}
	if events[0].Duration() != 0 {
		t.Errorf("a zero-length row lasts %s, want 0", events[0].Duration())
	}
}

// Deduplication is the store's job and is done by content, so a collector that
// collapsed repeats would be hiding what the source said. Real snapshots carry
// three identical off rows at 2026-07-10 15:22:32 and two at 07-14 09:01:12.
func TestExactDuplicateRowsAreBothEmitted(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		backlit(at(15, 22, 32), at(15, 22, 32), 0),
		backlit(at(15, 22, 32), at(15, 22, 32), 0),
	)

	events := collect(t, NewBacklight(path), time.Time{})
	if len(events) != 2 {
		t.Fatalf("got %d events from 2 identical rows, want both; dedup belongs to the store", len(events))
	}
}

// Contradictory rows are why state is part of the store's natural key. Both are
// observations, and a collector picking a winner would be making a read-time
// judgement at collection time, where it cannot be revisited.
func TestContradictoryRowsAreBothEmitted(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		backlit(at(9, 1, 12), at(9, 1, 12), 1),
		backlit(at(9, 1, 12), at(9, 1, 12), 0),
	)

	events := collect(t, NewBacklight(path), time.Time{})
	if len(events) != 2 {
		t.Fatalf("got %d events from an on row and an off row sharing endpoints, want both", len(events))
	}
	readings := map[string]bool{events[0].State: true, events[1].State: true}
	if !readings["on"] || !readings["off"] {
		t.Errorf("got readings %v, want both \"on\" and \"off\"", readings)
	}
}

func TestSinceKeepsEverythingThatHadNotEndedBeforeIt(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		backlit(at(8, 0, 0), at(9, 0, 0), 1),
		backlit(at(9, 0, 0), at(10, 0, 0), 0),
		backlit(at(10, 0, 0), at(11, 0, 0), 1),
	)
	src := NewBacklight(path)

	if got := collect(t, src, time.Time{}); len(got) != 3 {
		t.Errorf("the zero time returned %d of 3 rows, want all of them", len(got))
	}

	// The boundary is inclusive on the end, so a row ending exactly at since is
	// kept: it had not ended before since.
	got := collect(t, src, at(9, 0, 0))
	if len(got) != 3 {
		t.Errorf("since=09:00 returned %d rows, want 3; a row ending at since has not ended before it", len(got))
	}

	got = collect(t, src, at(9, 0, 1))
	if len(got) != 2 {
		t.Fatalf("since=09:00:01 returned %d rows, want the 2 that had not ended", len(got))
	}
	for _, e := range got {
		if e.End.Before(at(9, 0, 1)) {
			t.Errorf("an event ending %s came back for since=09:00:01", e.End)
		}
	}

	if got := collect(t, src, at(12, 0, 0)); len(got) != 0 {
		t.Errorf("since after every row returned %d events, want none", len(got))
	}
}

func TestAGzippedSnapshotReadsTheSameAsAPlainOne(t *testing.T) {
	dir := t.TempDir()
	rows := []row{
		backlit(at(9, 0, 0), at(9, 30, 0), 1),
		backlit(at(9, 30, 0), at(10, 0, 0), 0),
	}
	plain := writeSnapshot(t, filepath.Join(dir, "kc.db"), rows...)
	gzipped := writeGzSnapshot(t, filepath.Join(dir, "kc.db.gz"), rows...)

	fromPlain := collect(t, NewBacklight(plain), time.Time{})
	fromGz := collect(t, NewBacklight(gzipped), time.Time{})

	if len(fromGz) != len(fromPlain) {
		t.Fatalf("the gzipped snapshot yielded %d events and the plain one %d", len(fromGz), len(fromPlain))
	}
	for i := range fromPlain {
		if fromGz[i] != fromPlain[i] {
			t.Errorf("event %d differs: gzipped %+v, plain %+v", i, fromGz[i], fromPlain[i])
		}
	}
}

// A snapshot is written by this program, so a path that is not there is a caller
// naming the wrong file rather than a machine without the source installed —
// which is the case internal/biome tolerates. Reporting nothing here would make
// a typo indistinguishable from a day with no evidence.
func TestASnapshotThatIsNotThereIsAnError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nothing-here.db.gz")

	_, err := NewBacklight(missing).Collect(context.Background(), time.Time{})
	if err == nil {
		t.Fatal("reading a snapshot that does not exist returned no error")
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the path it could not read: %v", err)
	}
}

// A reading that is neither 0 nor 1 has never appeared in 2,909 real rows across
// both presence streams. It is kept rather than dropped, under its own decimal
// spelling, because dropping it would make an unrecognised reading look exactly
// like a sampler that said nothing — the distinction the third presence state
// depends on.
func TestAnUnrecognisedReadingKeepsItsRawValue(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		row{stream: streamBacklight, start: at(9, 0, 0), end: at(9, 1, 0), value: 2},
	)

	events := collect(t, NewBacklight(path), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want the 1 row", len(events))
	}
	if events[0].State != "2" {
		t.Errorf("an isBacklit=2 row reads %q, want \"2\"", events[0].State)
	}
}

// A NULL reading is the one row nothing true can be said about: it is not "on",
// not "off", and calling it either would invent an observation. It is skipped and
// reported rather than stored.
func TestARowWithNoReadingIsSkipped(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		row{stream: streamBacklight, start: at(9, 0, 0), end: at(9, 1, 0), value: nil},
		backlit(at(9, 1, 0), at(9, 2, 0), 1),
	)

	events := collect(t, NewBacklight(path), time.Time{})
	if len(events) != 1 {
		t.Fatalf("got %d events, want only the row that carries a reading", len(events))
	}
	if events[0].State != "on" {
		t.Errorf("the surviving event reads %q, want \"on\"", events[0].State)
	}
}

// An empty snapshot is a real answer rather than a failure: a stream can be
// silent for a whole day, as /app/usage was on 2026-07-28.
func TestAStreamWithNoRowsCollectsNothing(t *testing.T) {
	path := writeSnapshot(t, filepath.Join(t.TempDir(), "kc.db"),
		backlit(at(9, 0, 0), at(9, 30, 0), 1),
	)

	if got := collect(t, NewAppUsage(path), time.Time{}); len(got) != 0 {
		t.Errorf("got %d app-usage events from a snapshot holding none", len(got))
	}
}

func TestNewestSnapshotPicksTheLexicallyGreatestName(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"knowledgeC-20260728T180718.db.gz",
		"knowledgeC-20260729T211413.db.gz",
		"knowledgeC-20260729T081330.db.gz",
		// Not a snapshot, and must not win despite sorting last.
		"notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	got, err := NewestSnapshot(dir)
	if err != nil {
		t.Fatalf("NewestSnapshot: %v", err)
	}
	want := filepath.Join(dir, "knowledgeC-20260729T211413.db.gz")
	if got != want {
		t.Errorf("NewestSnapshot chose %s, want %s", got, want)
	}
}

func TestNewestSnapshotSaysWhenThereIsNothingToRead(t *testing.T) {
	empty := t.TempDir()
	if _, err := NewestSnapshot(empty); err == nil {
		t.Error("a directory holding no snapshots returned no error")
	} else if !strings.Contains(err.Error(), empty) {
		t.Errorf("the error does not name the directory: %v", err)
	}

	missing := filepath.Join(empty, "not-a-directory")
	if _, err := NewestSnapshot(missing); err == nil {
		t.Error("a directory that does not exist returned no error")
	} else if !strings.Contains(err.Error(), missing) {
		t.Errorf("the error does not name the directory: %v", err)
	}
}

func TestKindIsKnowledgeCForEverySource(t *testing.T) {
	path := mixed(t)
	for _, s := range []source.Source{
		NewBacklight(path), NewLocked(path), NewAppUsage(path), NewMediaUsage(path),
	} {
		if s.Kind() != source.KindKnowledgeC {
			t.Errorf("%s reports kind %q, want %q", s.ID(), s.Kind(), source.KindKnowledgeC)
		}
	}
}

// Unlike internal/biome there is no single fixed path to read, because a harvest
// writes a new snapshot every time. A source registered at init would have to
// name one, and it would be stale by the next run.
func TestNothingSelfRegisters(t *testing.T) {
	for _, s := range source.Sources() {
		if strings.HasPrefix(s.ID(), "knowledgec-") {
			t.Errorf("%s registered itself; a knowledgeC source has no fixed path to read", s.ID())
		}
	}
}
