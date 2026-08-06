package biome

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

func TestAppUsageIdentifiesItself(t *testing.T) {
	a := NewAppUsage(t.TempDir())
	if a.ID() != "biome-appusage" {
		t.Errorf("ID = %q, want biome-appusage", a.ID())
	}
	if a.Kind() != source.KindBiome {
		t.Errorf("Kind = %q, want %q", a.Kind(), source.KindBiome)
	}
}

// The registry is the whole point of registering from init: a caller collects
// from every source without knowing this one exists.
func TestAppUsageRegistersItself(t *testing.T) {
	for _, s := range source.Sources() {
		if s.ID() == "biome-appusage" {
			if s.Kind() != source.KindBiome {
				t.Errorf("registered source Kind = %q, want %q", s.Kind(), source.KindBiome)
			}
			return
		}
	}
	t.Fatal("biome-appusage is not in the package-level registry")
}

// TestRegisteredAppUsageResolvesHomeAtCollectTimeNotInit guards against a real
// bug: init runs once per test binary, before any test's t.Setenv("HOME", ...)
// takes effect, so a directory resolved eagerly at construction and stored on
// the struct is permanently the real machine's directory no matter what a
// later test does. Every cmd/timetattle test that exercised the full harvest
// pipeline was silently reading this developer's actual Biome stream as a
// result — slow, and not what any of those tests thought they were isolating.
// The fix is that the registered default instance stores no dir at all and
// resolves fresh inside Collect, so a HOME redirected mid-test is honoured.
func TestRegisteredAppUsageResolvesHomeAtCollectTimeNotInit(t *testing.T) {
	var registered *AppUsage
	for _, s := range source.Sources() {
		if au, ok := s.(*AppUsage); ok {
			registered = au
			break
		}
	}
	if registered == nil {
		t.Skip("the source registry holds no *AppUsage; init registers one only when " +
			"DefaultAppUsageDir() resolves at process start")
	}
	if registered.dir != "" {
		t.Fatalf("the registered default instance stores a fixed dir (%q); "+
			"it must resolve lazily so a redirected HOME is honoured", registered.dir)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, appUsagePath)
	got, err := registered.resolveDir()
	if err != nil {
		t.Fatalf("resolveDir: %v", err)
	}
	if got != want {
		t.Errorf("resolveDir() = %q after redirecting HOME, want %q; "+
			"the registered source is not respecting a post-init HOME change", got, want)
	}
}

func TestCollectPairsEachStartWithTheNextStop(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "us.zoom.xos"),
		stop(2, "us.zoom.xos"),
		start(2, "com.granola.app"),
		stop(62, "com.granola.app"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewInterval(at(0), at(2), source.Identity{App: "us.zoom.xos"}, "biome-appusage"),
		source.NewInterval(at(2), at(62), source.Identity{App: "com.granola.app"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// Focus is exclusive, so two applications interleave rather than nest: the
// pairing must key on the bundle identifier, not on the previous record.
func TestCollectPairsPerBundleRatherThanInSequence(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "a"),
		stop(10, "a"),
		start(10, "b"),
		stop(20, "b"),
		start(20, "a"),
		stop(45, "a"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewInterval(at(0), at(10), source.Identity{App: "a"}, "biome-appusage"),
		source.NewInterval(at(10), at(20), source.Identity{App: "b"}, "biome-appusage"),
		source.NewInterval(at(20), at(45), source.Identity{App: "a"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// macOS rolls to a new segment mid-session, so the start of an interval and
// its stop routinely live in different files. Ordering by the creation time in
// the header rather than by the file name keeps the epoch trap in the name out
// of the reader entirely.
func TestCollectPairsAcrossASegmentBoundary(t *testing.T) {
	dir := t.TempDir()
	// Written in the wrong order on purpose, and named so that sorting by
	// name would also put them the wrong way round.
	writeSegment(t, dir, "b-newer", buildSegment(t, at(100),
		stop(120, "com.apple.motionapp"),
		start(120, "com.apple.finder"),
		stop(130, "com.apple.finder"),
	))
	writeSegment(t, dir, "a-older", buildSegment(t, at(0),
		start(10, "com.apple.motionapp"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewInterval(at(10), at(120), source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
		source.NewInterval(at(120), at(130), source.Identity{App: "com.apple.finder"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// A stop with no start is a session that began while macOS was not recording
// transitions — the machine woke with that application already frontmost. Its
// beginning is genuinely unknown, and back-dating it to the previous record
// would charge the intervening sleep to the application.
//
// It is reported as unbounded rather than as an instant: an instant asserts
// there was no duration, and there was one — 6.42h across five confirmed days
// hangs off these. The reason travels with it because the only repair is
// bounding it against presence, which needs two sources and happens above this
// package.
func TestCollectReportsAnUnpairedStopWithoutInventingItsBeginning(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "com.apple.Safari"),
		stop(10, "com.apple.Safari"),
		stop(320, "com.apple.Safari"),
		start(320, "com.googlecode.iterm2"),
		stop(330, "com.googlecode.iterm2"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewInterval(at(0), at(10), source.Identity{App: "com.apple.Safari"}, "biome-appusage"),
		source.NewOpenStart(at(320), source.UnknownSleep, source.Identity{App: "com.apple.Safari"}, "biome-appusage"),
		source.NewInterval(at(320), at(330), source.Identity{App: "com.googlecode.iterm2"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// A start with no stop at the end of the run is the ordinary state of the
// newest segment: that application is frontmost right now. How long it will
// hold focus is not yet observable, so it is reported unbounded and marked as
// a horizon, which is what tells the store a later harvest will supersede it
// with the complete interval rather than leaving it to be bounded by presence.
func TestCollectReportsAnUnpairedStartWithoutInventingItsEnd(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "com.apple.finder"),
		stop(10, "com.apple.finder"),
		start(10, "com.apple.motionapp"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewInterval(at(0), at(10), source.Identity{App: "com.apple.finder"}, "biome-appusage"),
		source.NewOpenEnd(at(10), source.UnknownHorizon, source.Identity{App: "com.apple.motionapp"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// Two starts with no stop between them cannot both be closed by the later
// stop. The earlier one is unbounded like any start whose end was never
// observed, but it is NOT a horizon: records already exist past it, so no
// later harvest can complete it. Marking it lost is what keeps Resolves()
// honest, and stops the canonical view waiting forever on a stop macOS is
// never going to write.
func TestCollectDoesNotStretchAStartOverAnotherStart(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "com.apple.Safari"),
		start(60, "com.apple.Safari"),
		stop(70, "com.apple.Safari"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewOpenEnd(at(0), source.UnknownLost, source.Identity{App: "com.apple.Safari"}, "biome-appusage"),
		source.NewInterval(at(60), at(70), source.Identity{App: "com.apple.Safari"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// Every event must survive the registry's own validation, or it is dropped
// there and the source has produced nothing but an error message. For an
// unbounded event that means carrying exactly one endpoint and a reason, which
// is easy to emit incorrectly from the pairing loop and invisible until a
// whole day's evidence has silently gone missing.
func TestCollectProducesOnlyValidEvents(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "us.zoom.xos"),
		stop(2, "us.zoom.xos"),
		stop(9, "com.apple.Safari"),
		start(2, "com.granola.app"),
	))

	events := collect(t, dir)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("event %+v is invalid: %v", e, err)
		}
		switch e.Shape {
		case source.Interval, source.Unbounded:
		default:
			t.Errorf("event %+v has shape %q; this source reports focus, which is either "+
				"a span or a span seen at one edge", e, e.Shape)
		}
	}
}

// An event whose end was never observed has not ended, so there is no instant
// at which it became old news. Filtering it out because its start predates the
// watermark loses the one record that says what is on screen right now — and
// on a machine harvested once an hour, that is every harvest.
func TestCollectKeepsAnUnfinishedIntervalHoweverOldItsStart(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "a"),
		stop(10, "a"),
		start(10, "com.googlecode.iterm2"),
	))

	events, err := NewAppUsage(dir).Collect(context.Background(), at(500))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []source.Event{
		source.NewOpenEnd(at(10), source.UnknownHorizon, source.Identity{App: "com.googlecode.iterm2"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// A bare stop carries no start, so ordering by Start puts it at the zero time,
// decades ahead of the run it belongs in. Order has to key on whichever
// endpoint was actually observed.
func TestCollectOrdersABareStopByWhenItWasObserved(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "a"),
		stop(10, "a"),
		stop(300, "com.apple.Safari"),
		start(310, "b"),
		stop(320, "b"),
	))

	events := collect(t, dir)
	want := []source.Event{
		source.NewInterval(at(0), at(10), source.Identity{App: "a"}, "biome-appusage"),
		source.NewOpenStart(at(300), source.UnknownSleep, source.Identity{App: "com.apple.Safari"}, "biome-appusage"),
		source.NewInterval(at(310), at(320), source.Identity{App: "b"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// An interval still running when the previous harvest ran is not old news, so
// since drops only what had already finished.
func TestCollectHonoursSince(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "a"),
		stop(10, "a"),
		start(10, "b"),
		stop(100, "b"),
		start(100, "c"),
		stop(200, "c"),
	))

	events, err := NewAppUsage(dir).Collect(context.Background(), at(50))
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []source.Event{
		source.NewInterval(at(10), at(100), source.Identity{App: "b"}, "biome-appusage"),
		source.NewInterval(at(100), at(200), source.Identity{App: "c"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

func TestCollectStopsWhenTheContextIsCancelled(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0), start(0, "a"), stop(10, "a")))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewAppUsage(dir).Collect(ctx, time.Time{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Collect returned %v, want context.Canceled", err)
	}
}

// Biome keeps a tombstone directory beside the segments, and a directory is
// not a segment.
func TestCollectIgnoresSubdirectories(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0), start(0, "a"), stop(10, "a")))
	if err := os.Mkdir(filepath.Join(dir, "tombstone"), 0o700); err != nil {
		t.Fatalf("create tombstone: %v", err)
	}

	if got := len(collect(t, dir)); got != 1 {
		t.Fatalf("got %d events, want 1", got)
	}
}

// A stream nobody has is not a failure. Most machines running Timetattle will
// have this one, but a missing directory is indistinguishable from an
// uninstalled application, and losing a harvest over it would help nobody.
// The populated half is what makes the empty half mean anything.
func TestCollectTreatsAnAbsentStreamAsEmptyRatherThanBroken(t *testing.T) {
	present := t.TempDir()
	writeSegment(t, present, "1", buildSegment(t, at(0), start(0, "a"), stop(10, "a")))
	if got := len(collect(t, present)); got != 1 {
		t.Fatalf("got %d events from a populated stream, want 1", got)
	}

	events, err := NewAppUsage(filepath.Join(t.TempDir(), "absent")).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect on an absent stream: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("got %d events from an absent stream, want none", len(events))
	}
}

func TestCollectNamesTheSegmentThatFailedToParse(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "good", buildSegment(t, at(0), start(0, "a"), stop(10, "a")))
	bad := buildSegment(t, at(100), start(100, "b"))
	bad[41] ^= 0x01
	path := writeSegment(t, dir, "bad", bad)

	_, err := NewAppUsage(dir).Collect(context.Background(), time.Time{})
	requireErrorNaming(t, err, path, "byte 32")
}

func TestCollectOrdersEventsChronologically(t *testing.T) {
	dir := t.TempDir()
	writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "a"),
		stop(30, "a"),
		start(30, "b"),
		stop(40, "b"),
		start(40, "c"),
	))

	events := collect(t, dir)
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	for i := 1; i < len(events); i++ {
		if events[i].Anchor().Before(events[i-1].Anchor()) {
			t.Fatalf("event %d is anchored at %s, before event %d at %s",
				i, events[i].Anchor(), i-1, events[i-1].Anchor())
		}
	}
}

func collect(t *testing.T, dir string) []source.Event {
	t.Helper()
	events, err := NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return events
}

func requireEvents(t *testing.T, got, want []source.Event) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d:\n got: %s\nwant: %s", len(got), len(want), describe(got), describe(want))
	}
	for i := range want {
		g, w := got[i], want[i]
		if !g.Start.Equal(w.Start) || !g.End.Equal(w.End) || g.Shape != w.Shape ||
			g.Unknown != w.Unknown || g.Identity != w.Identity || g.Source != w.Source {
			t.Errorf("event %d:\n got %s\nwant %s", i, one(g), one(w))
		}
	}
}

func describe(events []source.Event) string {
	parts := make([]string, len(events))
	for i, e := range events {
		parts[i] = one(e)
	}
	return "\n  " + strings.Join(parts, "\n  ")
}

// one renders an event for a failure message. An unbounded event prints the
// missing side as its reason rather than as a zero time, so a diff shows why
// the endpoint is absent instead of a year that means nothing.
func one(e source.Event) string {
	edge := func(t time.Time) string {
		if t.IsZero() {
			return "?(" + string(e.Unknown) + ")"
		}
		return t.UTC().Format(time.RFC3339Nano)
	}
	return e.Identity.App + " " + edge(e.Start) + " -> " + edge(e.End)
}
