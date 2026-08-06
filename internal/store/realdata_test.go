//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package store_test

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// These tests run against the live Biome stream and skip cleanly when it is not
// there, following internal/biome/realdata_test.go. A synthetic fixture cannot
// answer the question they exist for: dedup is only worth anything if it holds
// over tens of thousands of real observations, including the ones the source
// repeats and the ones that are missing an endpoint.

func biomeDir(t *testing.T) string {
	t.Helper()
	dir, err := biome.DefaultAppUsageDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no readable ScreenTime.AppUsage stream at %s: %v", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return dir
		}
	}
	t.Skipf("%s holds no segments", dir)
	return ""
}

// The acceptance test for the whole design. Collectors re-read overlapping
// windows rather than tailing, so the second harvest of a window that has not
// moved must insert nothing at all.
func TestReharvestingTheWholeBiomeStreamInsertsNothing(t *testing.T) {
	dir := biomeDir(t)
	ctx := context.Background()

	events, err := biome.NewAppUsage(dir).Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated stream")
	}

	var intervals, stops, horizons, lost int
	for _, e := range events {
		switch {
		case e.Shape == source.Interval:
			intervals++
		case e.Unknown == source.UnknownSleep:
			stops++
		case e.Unknown == source.UnknownHorizon:
			horizons++
		case e.Unknown == source.UnknownLost:
			lost++
		}
	}
	t.Logf("%d events: %d intervals, %d bare stops, %d horizons, %d lost",
		len(events), intervals, stops, horizons, lost)

	f := newStore(t)

	first := f.run(t)
	inserted := f.write(t, first, events...)
	rows := f.count(t)
	// events_new < events_seen on a fresh store is ordinary, not a bug: a source
	// that reports the same observation twice in one read has said one thing.
	t.Logf("first harvest: seen %d, new %d, %d rows", len(events), inserted, rows)
	if err := first.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	second := f.run(t)
	again := f.write(t, second, events...)
	if again != 0 {
		t.Errorf("re-harvesting %d unchanged events inserted %d rows, want 0", len(events), again)
	}
	if after := f.count(t); after != rows {
		t.Errorf("the store grew from %d rows to %d on a re-harvest", rows, after)
	}
	if err := second.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	run, err := f.LoadRun(ctx, second.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if run.EventsSeen != len(events) || run.EventsNew != 0 {
		t.Errorf("second run recorded seen=%d new=%d, want seen=%d new=0", run.EventsSeen, run.EventsNew, len(events))
	}

	// The canonical read differs from the table by exactly the horizons that
	// something completed, counted here independently of the view rather than by
	// asking the view what it did. On an untouched stream that is nothing: the
	// one trailing horizon has no completing interval yet.
	canonical := len(f.all(t))
	want := rows - superseded(events)
	if canonical != want {
		t.Errorf("the canonical read returns %d of %d rows, want %d", canonical, rows, want)
	}
	t.Logf("%d rows, %d canonical, %d superseded horizons", rows, canonical, rows-canonical)

	// A third pass reads the stream again rather than replaying the same slice,
	// which is what a real harvest does. macOS may have appended while this test
	// ran, and then new rows are correct; the exact assertion is only available
	// when it did not.
	fresh, err := biome.NewAppUsage(dir).Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("second Collect: %v", err)
	}
	third := f.run(t)
	grown := f.write(t, third, fresh...)
	t.Logf("re-read the live stream: %d events, %d new", len(fresh), grown)
	if len(fresh) == len(events) && grown != 0 {
		t.Errorf("re-reading an unchanged stream of %d events inserted %d rows, want 0", len(fresh), grown)
	}
	if err := third.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
}

// The end-to-end proof of supersession over real data. Every paired interval in
// the stream was, at the moment its start was written and before its stop was,
// exactly a horizon row — so the harvest that would have seen it can be
// reconstructed faithfully rather than imagined.
func TestOnlySupersededHorizonsSeparateTheCanonicalReadFromTheTable(t *testing.T) {
	dir := biomeDir(t)
	ctx := context.Background()

	events, err := biome.NewAppUsage(dir).Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	// Sampled across the whole stream rather than taken as a prefix, so the
	// cases are spread over every segment.
	var earlier []source.Event
	for i, e := range events {
		if e.Shape == source.Interval && i%500 == 0 {
			earlier = append(earlier, source.NewOpenEnd(e.Start, source.UnknownHorizon, e.Identity, e.Source))
		}
	}
	if len(earlier) < 10 {
		t.Skipf("only %d intervals in the stream to reconstruct harvests from", len(earlier))
	}

	f := newStore(t)

	past := f.run(t)
	if n := f.write(t, past, earlier...); n != len(earlier) {
		t.Fatalf("the reconstructed earlier harvest inserted %d of %d horizons; they must all be distinct", n, len(earlier))
	}
	if err := past.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	now := f.run(t)
	f.write(t, now, events...)
	if err := now.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	raw := f.count(t)
	if raw != len(events)+len(earlier) {
		t.Fatalf("the table holds %d rows, want %d; nothing may be edited or dropped", raw, len(events)+len(earlier))
	}

	canonical := f.all(t)
	if raw-len(canonical) != len(earlier) {
		t.Errorf("the canonical read hides %d rows, want the %d completed horizons", raw-len(canonical), len(earlier))
	}
	// The same rule computed independently of the view, so that agreement means
	// something rather than being the view agreeing with itself.
	if got := superseded(append(append([]source.Event{}, earlier...), events...)); got != len(earlier) {
		t.Errorf("counted %d superseded horizons in the input, want %d", got, len(earlier))
	}

	completed := map[string]bool{}
	for _, e := range earlier {
		completed[completionKey(e)] = true
	}
	var survivingHorizons, completingIntervals int
	for _, e := range canonical {
		if !completed[completionKey(e)] {
			continue
		}
		switch {
		case e.Unknown == source.UnknownHorizon:
			survivingHorizons++
		case e.Shape == source.Interval:
			completingIntervals++
		}
	}
	if survivingHorizons != 0 {
		t.Errorf("%d completed horizons are still in the canonical read", survivingHorizons)
	}
	if completingIntervals != len(earlier) {
		t.Errorf("%d of %d completing intervals are in the canonical read", completingIntervals, len(earlier))
	}
	t.Logf("%d rows, %d canonical, %d reconstructed horizons superseded", raw, len(canonical), len(earlier))
}

// superseded counts the distinct horizons that some interval completes, computed
// from the events rather than by asking the store what it did.
func superseded(events []source.Event) int {
	completions := map[string]bool{}
	for _, e := range events {
		if e.Shape == source.Interval {
			completions[completionKey(e)] = true
		}
	}
	horizons := map[string]bool{}
	for _, e := range events {
		if e.Shape == source.Unbounded && e.Unknown == source.UnknownHorizon && completions[completionKey(e)] {
			horizons[completionKey(e)] = true
		}
	}
	return len(horizons)
}

// completionKey is what makes a horizon and an interval the same observation:
// the source, the start they share, and everything the store treats as identity
// apart from the shape and the end that one of them is missing.
func completionKey(e source.Event) string {
	return strings.Join([]string{
		e.Source,
		e.Start.UTC().Format(store.TimeLayout),
		e.Identity.App,
		e.Identity.Document,
		e.State,
	}, "\x00")
}

// Reading back is the other half: rows that go in but cannot be found again over
// a span are as good as lost, and the span query is where the TEXT timestamps
// either order correctly or quietly do not.
func TestTheWholeBiomeStreamCanBeReadBackByDay(t *testing.T) {
	dir := biomeDir(t)
	ctx := context.Background()

	events, err := biome.NewAppUsage(dir).Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated stream")
	}

	f := newStore(t)
	run := f.run(t)
	f.write(t, run, events...)

	stored := f.all(t)
	if len(stored) == 0 {
		t.Fatal("nothing came back from a populated store")
	}

	// Every event is anchored somewhere, so bucketing by UTC day and summing the
	// buckets has to account for every row exactly once.
	earliest, latest := stored[0].Anchor(), stored[0].Anchor()
	for _, e := range stored {
		if at := e.Anchor(); at.Before(earliest) {
			earliest = at
		} else if at.After(latest) {
			latest = at
		}
	}

	day := earliest.UTC().Truncate(24 * time.Hour)
	var total, days int
	for ; !day.After(latest); day = day.AddDate(0, 0, 1) {
		got, err := f.Events(ctx, store.Filter{From: day, To: day.AddDate(0, 0, 1).Add(-time.Microsecond)})
		if err != nil {
			t.Fatalf("Events: %v", err)
		}
		// An interval spanning midnight belongs to both days, so only anchors
		// are counted here — otherwise the sum legitimately exceeds the total.
		for _, e := range got {
			if !e.Anchor().Before(day) && e.Anchor().Before(day.AddDate(0, 0, 1)) {
				total++
			}
		}
		if len(got) > 0 {
			days++
		}
	}
	if total != len(stored) {
		t.Errorf("day buckets account for %d of %d stored events", total, len(stored))
	}
	t.Logf("%d events across %d days, %s to %s", len(stored), days,
		earliest.UTC().Format(store.TimeLayout), latest.UTC().Format(store.TimeLayout))
}
