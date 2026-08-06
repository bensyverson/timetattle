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
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/knowledgec"
	"github.com/bensyverson/timetattle/internal/snapshot"
	"github.com/bensyverson/timetattle/internal/source"
)

// The two dedup claims in knowledgec_realdata_test.go, restated against
// whatever the last harvest on THIS machine actually wrote. The synthetic
// versions next door describe rows a real snapshot may not happen to hold
// today; these describe thousands of rows a machine produced, including the
// ones it repeats and contradicts, and neither substitutes for the other.

// TestShapeChosenByDurationWouldDoubleStoreZeroLengthRows prices the one
// invariant a collector author cannot get wrong, by committing the mistake here
// where it can be measured.
//
// knowledgeC reports a start and an end for every row, so every event is an
// interval even where the two are equal. A zero-length row satisfies BOTH the
// point and the interval CHECK, and shape is part of the natural key, so a
// collector choosing shape by the duration it happened to measure would file one
// observation under each shape and deduplication could not tell they were one
// thing. This writes the events the real collector produces, then writes the
// points a duration-branching collector would have produced from exactly the
// same rows, and reports how many observations end up stored twice.
//
// The events written second are deliberately WRONG. Nothing in this package
// produces them; they exist for the length of this test to turn a warning in the
// documentation into a number.
func TestShapeChosenByDurationWouldDoubleStoreZeroLengthRows(t *testing.T) {
	dir, err := snapshot.DefaultDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	path, err := knowledgec.NewestSnapshot(dir)
	if err != nil {
		t.Skipf("NewestSnapshot(%s): %v", dir, err)
	}

	ctx := context.Background()
	f := newStore(t)
	seen, zeroLength, doubled := 0, 0, 0

	for _, s := range []source.Source{
		knowledgec.NewBacklight(path), knowledgec.NewLocked(path),
		knowledgec.NewAppUsage(path), knowledgec.NewMediaUsage(path),
	} {
		events, err := s.Collect(ctx, time.Time{})
		if err != nil {
			t.Fatalf("%s Collect: %v", s.ID(), err)
		}
		honest := f.runFor(t, s.ID(), path)
		f.write(t, honest, events...)
		if err := honest.Finish(ctx); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		seen += len(events)

		var asPoints []source.Event
		for _, e := range events {
			if !e.Start.Equal(e.End) {
				continue
			}
			// The collector's own guarantee, checked on the rows where it is
			// tempting to break it rather than in the abstract.
			if e.Shape != source.Interval {
				t.Fatalf("%s reported a zero-length row as %q; knowledgeC observed both endpoints",
					s.ID(), e.Shape)
			}
			p := source.NewPoint(e.Start, e.Identity, e.Source)
			p.State = e.State
			asPoints = append(asPoints, p)
		}
		zeroLength += len(asPoints)

		mistaken := f.runFor(t, s.ID(), path)
		n := f.write(t, mistaken, asPoints...)
		if err := mistaken.Finish(ctx); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		doubled += n
		t.Logf("%s: %d events, %d zero-length, %d stored a second time as points",
			s.ID(), len(events), len(asPoints), n)
	}

	if zeroLength == 0 {
		t.Skipf("%s holds no zero-length rows, so this snapshot cannot price the mistake", path)
	}
	// Every distinct zero-length observation inserts again under the other
	// shape: dedup cannot collapse them, because shape is part of what makes an
	// observation itself.
	if doubled == 0 {
		t.Errorf("%d zero-length observations re-shaped as points inserted nothing; "+
			"if shape were not part of the key, the invariant would not need stating", zeroLength)
	}
	if doubled > zeroLength {
		t.Errorf("re-shaping %d observations inserted %d rows", zeroLength, doubled)
	}
	t.Logf("choosing shape by measured duration would store %d of %d observations twice",
		doubled, seen)
}

// The same dedup claim against whatever the last harvest really wrote. A fixture
// cannot tell us whether the property holds over thousands of rows a machine
// produced, including the ones it repeats.
func TestReharvestingTheNewestRealSnapshotInsertsNothing(t *testing.T) {
	dir, err := snapshot.DefaultDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	path, err := knowledgec.NewestSnapshot(dir)
	if err != nil {
		t.Skipf("NewestSnapshot(%s): %v", dir, err)
	}

	ctx := context.Background()
	f := newStore(t)
	sources := []source.Source{
		knowledgec.NewBacklight(path),
		knowledgec.NewLocked(path),
		knowledgec.NewAppUsage(path),
		knowledgec.NewMediaUsage(path),
	}

	total := 0
	for _, s := range sources {
		events, err := s.Collect(ctx, time.Time{})
		if err != nil {
			t.Fatalf("%s Collect: %v", s.ID(), err)
		}
		total += len(events)

		first := f.runFor(t, s.ID(), path)
		inserted := f.write(t, first, events...)
		if err := first.Finish(ctx); err != nil {
			t.Fatalf("Finish: %v", err)
		}
		t.Logf("%s: seen %d, new %d", s.ID(), len(events), inserted)

		second := f.runFor(t, s.ID(), path)
		again := f.write(t, second, events...)
		if again != 0 {
			t.Errorf("%s: re-reading the same snapshot inserted %d rows, want 0", s.ID(), again)
		}
		if err := second.Finish(ctx); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	}
	if total == 0 {
		t.Skipf("%s holds no rows in any of the four streams", path)
	}

	rows := f.count(t)
	if canonical := len(f.all(t)); canonical != rows {
		t.Errorf("the canonical read returns %d of %d rows; knowledgeC produces no horizons to supersede",
			canonical, rows)
	}

	// A third pass re-reads the snapshot rather than replaying the slice, which
	// is what a real harvest does. A snapshot never changes, so this must be
	// exactly zero rather than merely small.
	for _, s := range sources {
		events, err := s.Collect(ctx, time.Time{})
		if err != nil {
			t.Fatalf("%s second Collect: %v", s.ID(), err)
		}
		third := f.runFor(t, s.ID(), path)
		if grown := f.write(t, third, events...); grown != 0 {
			t.Errorf("%s: re-reading %s inserted %d rows, want 0", s.ID(), path, grown)
		}
		if err := third.Finish(ctx); err != nil {
			t.Fatalf("Finish: %v", err)
		}
	}
	if after := f.count(t); after != rows {
		t.Errorf("the store grew from %d rows to %d on a re-read of %s", rows, after, path)
	}
	t.Logf("%d events across four streams, %d rows in the store", total, rows)
}
