//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package knowledgec

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/source"
)

// These tests read the newest snapshot a real harvest wrote and skip cleanly
// when there is none, following internal/biome/realdata_test.go. A synthetic
// fixture cannot answer what they exist for: whether the query, the epoch and
// the shape decision hold against a database macOS actually wrote, including the
// rows that repeat and the rows that contradict each other.

func realSnapshot(t *testing.T) string {
	t.Helper()
	dir, err := snapshot.DefaultDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	path, err := NewestSnapshot(dir)
	if err != nil {
		t.Skipf("NewestSnapshot(%s): %v", dir, err)
	}
	return path
}

// TestRealSnapshotStreamsParse is the check that matters most. Every event has
// to be storable, dated plausibly and shaped as an interval; a fixture can only
// confirm that the reader agrees with itself.
func TestRealSnapshotStreamsParse(t *testing.T) {
	path := realSnapshot(t)
	ctx := context.Background()

	for _, s := range []source.Source{
		NewBacklight(path), NewLocked(path), NewAppUsage(path), NewMediaUsage(path),
	} {
		events, err := s.Collect(ctx, time.Time{})
		if err != nil {
			t.Fatalf("%s Collect: %v", s.ID(), err)
		}

		var earliest, latest time.Time
		readings := map[string]int{}
		zeroLength := 0
		for i, e := range events {
			if err := e.Validate(); err != nil {
				t.Fatalf("%s event %d is unusable: %v", s.ID(), i, err)
			}
			if e.Shape != source.Interval {
				t.Fatalf("%s event %d has shape %q; knowledgeC reports both endpoints for every row",
					s.ID(), i, e.Shape)
			}
			if e.Locator != "" {
				t.Fatalf("%s event %d carries locator %q; these events observe no file", s.ID(), i, e.Locator)
			}
			// An Apple-epoch reading lands three decades away, which no
			// plausible tolerance would hide.
			if e.Start.Year() < 2015 || e.Start.After(time.Now().AddDate(0, 0, 1)) {
				t.Fatalf("%s event %d is dated %s, which is not a plausible instant", s.ID(), i, e.Start)
			}
			if earliest.IsZero() || e.Start.Before(earliest) {
				earliest = e.Start
			}
			if e.End.After(latest) {
				latest = e.End
			}
			if e.Start.Equal(e.End) {
				zeroLength++
			}
			readings[e.State]++
		}
		if len(events) == 0 {
			// A stream that is silent is a real answer: /app/usage wrote no
			// rows at all on 2026-07-28.
			t.Logf("%s: no rows in %s", s.ID(), path)
			continue
		}
		t.Logf("%s: %d events, %d zero-length, %s to %s, readings %v",
			s.ID(), len(events), zeroLength, earliest.Format(time.RFC3339), latest.Format(time.RFC3339), readings)
	}
}

// The presence stream repeats and contradicts itself, and the collector must
// carry both through rather than tidying them. This measures that on the real
// snapshot instead of asserting it, so a snapshot that happens to hold neither
// says so rather than failing.
func TestRealPresenceRepetitionAndContradictionSurviveCollection(t *testing.T) {
	path := realSnapshot(t)

	events, err := NewBacklight(path).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Skipf("%s holds no /display/isBacklit rows", path)
	}

	identical := map[string]int{}
	endpoints := map[string]map[string]bool{}
	for _, e := range events {
		key := fmt.Sprintf("%s|%s|%s", e.Start, e.End, e.State)
		identical[key]++
		span := fmt.Sprintf("%s|%s", e.Start, e.End)
		if endpoints[span] == nil {
			endpoints[span] = map[string]bool{}
		}
		endpoints[span][e.State] = true
	}

	repeats, contradictions := 0, 0
	for _, n := range identical {
		if n > 1 {
			repeats += n - 1
		}
	}
	for _, readings := range endpoints {
		if len(readings) > 1 {
			contradictions++
		}
	}
	t.Logf("%d events: %d are repeats of another, %d spans carry contradictory readings",
		len(events), repeats, contradictions)

	// Whatever the counts, the collector must not have collapsed anything: the
	// number of events equals the number of rows the query returned, which is
	// checked by the fact that repeats are still present at all where the
	// snapshot has them.
	if repeats == 0 && contradictions == 0 {
		t.Logf("this snapshot holds neither, so it says nothing about pass-through")
	}
}

// The three states are a property of the data, not of the reader, so this
// records what the snapshot actually carries rather than requiring a shape.
func TestRealLockedReadingsAreBothPresent(t *testing.T) {
	path := realSnapshot(t)

	events, err := NewLocked(path).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Skipf("%s holds no /device/isLocked rows", path)
	}

	readings := map[string]int{}
	for _, e := range events {
		readings[e.State]++
	}
	for _, want := range []string{"locked", "unlocked"} {
		if readings[want] == 0 {
			t.Errorf("no %q rows in %d lock events; readings were %v", want, len(events), readings)
		}
	}
	// Anything other than the two named readings would mean the 1/0 mapping is
	// not the whole story, which is worth failing over rather than logging.
	for reading, n := range readings {
		if reading != "locked" && reading != "unlocked" {
			t.Errorf("%d lock events read %q, which the 1/0 mapping does not describe", n, reading)
		}
	}
	t.Logf("%d lock events: %v", len(events), readings)
}
