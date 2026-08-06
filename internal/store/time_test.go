package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// Timestamps are TEXT, the schema carries CHECK (ended_at >= started_at), and
// range queries compare them as strings. So lexical order must equal
// chronological order, which means fixed width and one zone. This is the
// silent-poisoning failure of the whole design: time.RFC3339Nano trims trailing
// zeros, so its widths vary and its comparisons are wrong at exactly the
// boundaries nobody checks.

// zones are the ones the readers actually produce. internal/biome returns UTC;
// the knowledgeC and file readers return local time. Both are fixed here so the
// test means the same thing on a machine set to any zone.
var (
	india = time.FixedZone("IST", 5*60*60+30*60)
	texas = time.FixedZone("CDT", -5*60*60)
)

func TestStoredTimestampsAreFixedWidthUtcText(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	// Fractions chosen to catch trimming: none, a trailing zero, and a full
	// six digits. RFC3339Nano writes these at three different widths.
	base := time.Date(2026, 7, 14, 9, 0, 0, 0, texas)
	f.write(t, run,
		focus(base, base.Add(time.Hour), "none"),
		focus(base.Add(500*time.Millisecond), base.Add(time.Hour), "trailing-zero"),
		focus(base.Add(123456*time.Microsecond), base.Add(time.Hour), "six-digits"),
	)

	rows, err := f.raw(t).Query(`SELECT started_at, LENGTH(started_at) FROM event`)
	if err != nil {
		t.Fatalf("read timestamps: %v", err)
	}
	defer rows.Close()

	widths := map[int][]string{}
	for rows.Next() {
		var text string
		var width int
		if err := rows.Scan(&text, &width); err != nil {
			t.Fatalf("scan: %v", err)
		}
		widths[width] = append(widths[width], text)
		if text[len(text)-1] != 'Z' {
			t.Errorf("%q is not written in UTC", text)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows: %v", err)
	}
	if len(widths) != 1 {
		t.Errorf("timestamps were stored at %d different widths (%v); lexical order cannot equal chronological order", len(widths), widths)
	}
}

// The same instant written down in three zones is one observation, not three.
// Normalising on the way in is what makes that true, and the store is the only
// place it can be relied on to happen: the readers disagree about zones and
// Go's own comparisons hide it, because they compare instants.
func TestOneInstantInSeveralZonesIsOneEvent(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	start := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	end := start.Add(25 * time.Minute)
	inserted := f.write(t, run,
		focus(start, end, "com.apple.motionapp"),
		focus(start.In(india), end.In(india), "com.apple.motionapp"),
		focus(start.In(texas), end.In(texas), "com.apple.motionapp"),
	)
	if inserted != 1 {
		t.Errorf("the same interval in three zones inserted %d rows, want 1", inserted)
	}

	got := f.all(t)
	if len(got) != 1 {
		t.Fatalf("the store holds %d rows, want 1", len(got))
	}
	if !got[0].Start.Equal(start) || !got[0].End.Equal(end) {
		t.Errorf("read back %s to %s, want %s to %s", got[0].Start, got[0].End, start, end)
	}
	if got[0].Start.Location() != time.UTC {
		t.Errorf("read back in zone %s; the store hands out UTC so day buckets cannot drift", got[0].Start.Location())
	}
}

// Ordering the stored TEXT as text has to produce chronological order, because
// that is what every range query and the interval CHECK rest on. The batch
// below is deliberately mixed in zone, fraction and shape.
func TestLexicalOrderOfStoredTextIsChronologicalOrder(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	base := time.Date(2026, 7, 14, 23, 59, 59, 0, time.UTC)
	instants := []time.Time{
		base.Add(2 * time.Second).In(india),
		base.In(texas),
		base.Add(999999 * time.Microsecond),
		base.Add(time.Second),
		base.Add(1500 * time.Millisecond).In(texas),
	}
	var events []source.Event
	for i, at := range instants {
		events = append(events, source.NewPoint(at, source.Identity{App: string(rune('a' + i))}, sourceID))
	}
	f.write(t, run, events...)

	rows, err := f.raw(t).Query(`SELECT started_at FROM event ORDER BY started_at`)
	if err != nil {
		t.Fatalf("read timestamps: %v", err)
	}
	defer rows.Close()

	var previous time.Time
	var n int
	for rows.Next() {
		var text string
		if err := rows.Scan(&text); err != nil {
			t.Fatalf("scan: %v", err)
		}
		at, err := time.Parse(store.TimeLayout, text)
		if err != nil {
			t.Fatalf("stored timestamp %q does not parse with store.TimeLayout: %v", text, err)
		}
		if !previous.IsZero() && at.Before(previous) {
			t.Errorf("sorting the text put %s after %s", at, previous)
		}
		previous = at
		n++
	}
	if n != len(instants) {
		t.Fatalf("read %d timestamps, want %d", n, len(instants))
	}
}

// The store keeps microseconds, which is what the sources actually resolve:
// Biome segment names and knowledgeC's Core Data doubles are both microsecond
// figures. Anything finer is truncated rather than rounded, so re-reading the
// same record always yields the same key.
func TestTimestampsKeepMicrosecondsAndTruncateBelowThem(t *testing.T) {
	f := newStore(t)
	run := f.run(t)

	at := time.Date(2026, 7, 14, 9, 0, 0, 123456789, time.UTC)
	f.write(t, run, source.NewPoint(at, source.Identity{App: "com.apple.motionapp"}, sourceID))

	got := f.all(t)
	if len(got) != 1 {
		t.Fatalf("read back %d events, want 1", len(got))
	}
	want := at.Truncate(time.Microsecond)
	if !got[0].Start.Equal(want) {
		t.Errorf("read back %s, want %s", got[0].Start.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}

// The boundary handed to Collect is recorded so that "why did this run not see
// X" is answerable later. A run with no boundary read everything, which is a
// different fact from having read from the epoch.
func TestTheBoundaryHandedToCollectIsRecorded(t *testing.T) {
	f := newStore(t)
	ctx := context.Background()

	since := time.Date(2026, 7, 14, 6, 0, 0, 0, texas)
	bounded, err := f.BeginRun(ctx, store.RunSpec{SourceID: sourceID, Input: "/tmp/stream", Since: since})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	got, err := f.LoadRun(ctx, bounded.ID)
	if err != nil {
		t.Fatalf("LoadRun: %v", err)
	}
	if !got.Since.Equal(since) {
		t.Errorf("since came back as %s, want %s", got.Since, since)
	}

	everything, err := f.BeginRun(ctx, store.RunSpec{SourceID: sourceID, Input: "/tmp/stream"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	var stored *string
	if err := f.raw(t).QueryRow(`SELECT since FROM harvest_run WHERE id = ?`, everything.ID).Scan(&stored); err != nil {
		t.Fatalf("read since: %v", err)
	}
	if stored != nil {
		t.Errorf("a run with no boundary recorded since=%q; it must be NULL", *stored)
	}
}
