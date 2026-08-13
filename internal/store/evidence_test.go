package store_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// WHETHER EVIDENCE ARRIVED, which is a different question from whether a
// harvest ran.
//
// Freshness (freshness_test.go) answers the second: it reads harvest_run and
// says when each source was last read through. A harvest that fires exactly on
// schedule and ingests nothing advances that reading while the store itself
// stands still. Evidence is the other half — what the store actually holds —
// and the whole difficulty is that a source with nothing new to say and a
// source that has silently stopped write the same nothing.

// The anchor is whichever endpoint was observed, and an interval that ENDED
// later than anything else began is the newest evidence in the store. Reading
// started_at alone would understate every interval source, and biome-appusage,
// the source that most often holds the frontier, is exactly that shape.
func TestTheNewestEvidenceIsTheAnchorAndNotTheStart(t *testing.T) {
	h := freshStore(t)
	harvested(t, h, "biome-appusage", "2026-08-13T13:00:00Z", "2026-08-13T13:00:10Z", "ok")
	interval(t, h, "biome-appusage", "2026-08-13T09:00:00Z", "2026-08-13T11:30:00Z")
	point(t, h, "biome-appusage", "2026-08-13T10:00:00Z")

	got := evidenceBySource(t, h)
	if want := mustTime(t, "2026-08-13T11:30:00Z"); !got["biome-appusage"].Newest.Equal(want) {
		t.Errorf("Newest = %s, want %s: the interval ENDED after the point began, so it is the newer evidence",
			got["biome-appusage"].Newest, want)
	}
}

// THE MEASUREMENT THIS FILE EXISTS FOR. A source's silence is measured against
// the store's own newest evidence, never against the wall clock. Nobody records
// anything overnight, so the frontier does not move, so no source falls behind
// — which is what stops a quiet night from being read as thirteen stalled
// sources every single morning.
func TestASourceFallsBehindOnlyWhileSomeOtherSourceIsRecording(t *testing.T) {
	h := freshStore(t)
	harvested(t, h, "gitlog-commits", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")
	harvested(t, h, "biome-appusage", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")

	// A working day: both record. Then a fourteen-hour night in which neither
	// does. Then both record again.
	point(t, h, "gitlog-commits", "2026-08-10T09:00:00Z")
	point(t, h, "biome-appusage", "2026-08-10T09:00:00Z")
	point(t, h, "gitlog-commits", "2026-08-10T18:00:00Z")
	point(t, h, "biome-appusage", "2026-08-10T18:00:00Z")
	point(t, h, "gitlog-commits", "2026-08-11T08:00:00Z")
	point(t, h, "biome-appusage", "2026-08-11T08:00:00Z")

	got := evidenceBySource(t, h)
	for _, id := range []string{"gitlog-commits", "biome-appusage"} {
		for _, lag := range got[id].Lags {
			if lag != 0 {
				t.Errorf("%s recorded a witnessed lag of %s across a night nothing was recorded in; "+
					"silence measured against the clock is what makes every morning look like a fault",
					id, lag)
			}
		}
	}
}

// The other half of the same rule, and the case that must raise a fault: one
// source stops while another carries on. The frontier keeps moving, so the
// silent one falls behind by exactly the span the other one witnessed.
func TestASourceSilentWhileAnotherRecordsFallsBehindByThatSpan(t *testing.T) {
	h := freshStore(t)
	harvested(t, h, "knowledgec-appusage", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")
	harvested(t, h, "knowledgec-backlit", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")

	point(t, h, "knowledgec-appusage", "2026-08-10T09:00:00Z")
	point(t, h, "knowledgec-backlit", "2026-08-10T09:00:00Z")
	// backlit carries on for six hours; appusage says nothing, then returns.
	point(t, h, "knowledgec-backlit", "2026-08-10T15:00:00Z")
	point(t, h, "knowledgec-appusage", "2026-08-10T16:00:00Z")

	got := evidenceBySource(t, h)
	want := 6 * time.Hour
	if len(got["knowledgec-appusage"].Lags) != 1 || got["knowledgec-appusage"].Lags[0] != want {
		t.Errorf("knowledgec-appusage closed lags %v, want exactly one of %s: the frontier reached "+
			"15:00 while it had said nothing since 09:00",
			got["knowledgec-appusage"].Lags, want)
	}
}

// EVENTS FROM BEFORE COLLECTION BEGAN ARE NOT EVIDENCE ABOUT COLLECTION.
// fs-observations records file modification times and gitlog records commit
// dates, so both hold events reaching back years before this machine ever ran a
// harvest. Measured over the whole event table on the live store, the largest
// witnessed lag for fs-observations is 5.4 YEARS and for gitlog-commits 1.7
// years — a bound derived from those could never fire, and the report would be
// clean and indistinguishable from a correct one. The era is each source's own
// first recorded harvest run, which is derived from the record rather than
// typed and so cannot go stale.
func TestLagsBeforeASourcesFirstHarvestAreNotCounted(t *testing.T) {
	h := freshStore(t)
	harvested(t, h, "gitlog-commits", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")
	harvested(t, h, "biome-appusage", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")

	// Two commits from years ago, backfilled, with a two-year hole between
	// them, and a biome event from the same era to move the frontier through it.
	point(t, h, "gitlog-commits", "2023-02-27T20:30:12Z")
	point(t, h, "biome-appusage", "2024-06-01T00:00:00Z")
	point(t, h, "gitlog-commits", "2025-02-27T20:30:12Z")
	// And the collected era.
	point(t, h, "gitlog-commits", "2026-08-10T09:00:00Z")
	point(t, h, "biome-appusage", "2026-08-10T12:00:00Z")
	point(t, h, "gitlog-commits", "2026-08-10T14:00:00Z")

	got := evidenceBySource(t, h)
	lags := got["gitlog-commits"].Lags
	if len(lags) != 1 || lags[0] != 3*time.Hour {
		t.Errorf("gitlog-commits closed lags %v, want exactly one of 3h — the two-year hole is "+
			"backfilled commit dates and says nothing about whether collection is working", lags)
	}
	if want := mustTime(t, "2026-08-10T00:00:00Z"); !got["gitlog-commits"].CollectedFrom.Equal(want) {
		t.Errorf("CollectedFrom = %s, want %s", got["gitlog-commits"].CollectedFrom, want)
	}
}

// A source whose every event predates its own first harvest — blender-backups
// and motion-autosave on the live store, both of them — has NO closed lag at
// all. That is the absence of a basis for judging it, and it must come back as
// an empty series rather than as a zero one, which would read as a source that
// has never once fallen behind.
func TestASourceWithNothingInsideItsCollectedEraHasNoLags(t *testing.T) {
	h := freshStore(t)
	harvested(t, h, "blender-backups", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")
	harvested(t, h, "biome-appusage", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")
	point(t, h, "blender-backups", "2021-03-04T00:09:21Z")
	point(t, h, "biome-appusage", "2026-08-10T09:00:00Z")
	point(t, h, "biome-appusage", "2026-08-10T18:00:00Z")

	got := evidenceBySource(t, h)
	b, ok := got["blender-backups"]
	if !ok {
		t.Fatalf("blender-backups is missing from Evidence altogether: a source that has recorded "+
			"nothing since collection began must be reported, not omitted (%v)", got)
	}
	if len(b.Lags) != 0 {
		t.Errorf("blender-backups closed lags %v, want none at all", b.Lags)
	}
	if want := mustTime(t, "2021-03-04T00:09:21Z"); !b.Newest.Equal(want) {
		t.Errorf("Newest = %s, want %s: the backfilled event is still the newest evidence there is",
			b.Newest, want)
	}
}

// A source that has been tried and has never stored an event is present with a
// zero Newest. Absent from the result would be the collapse this whole package
// keeps refusing: nothing recorded read as nothing to record.
func TestASourceThatHasRunAndStoredNothingIsReportedWithNoEvidence(t *testing.T) {
	h := freshStore(t)
	harvested(t, h, "motion-autosave", "2026-08-10T00:00:00Z", "2026-08-10T00:00:01Z", "ok")

	got := evidenceBySource(t, h)
	m, ok := got["motion-autosave"]
	if !ok {
		t.Fatalf("motion-autosave ran and is missing from Evidence: %v", got)
	}
	if !m.Newest.IsZero() {
		t.Errorf("Newest = %s, want the zero time: this source has never stored an event", m.Newest)
	}
	if m.Events != 0 {
		t.Errorf("Events = %d, want 0", m.Events)
	}
}

// evidenceBySource is Evidence keyed by source id.
func evidenceBySource(t *testing.T, h *harness) map[string]store.Evidence {
	t.Helper()
	got, err := h.Evidence(context.Background())
	if err != nil {
		t.Fatalf("Evidence: %v", err)
	}
	by := map[string]store.Evidence{}
	for _, e := range got {
		by[e.Source] = e
	}
	return by
}

// point and interval insert one observation directly, so that its anchor can be
// pinned rather than being whatever the clock said. The same reason
// freshness_test.go writes harvest_run rows with SQL of its own.
// A point carries the same instant at both ends, which is what migration 0009's
// own CHECK requires of the shape.
func point(t *testing.T, h *harness, src, at string) {
	t.Helper()
	insertEvidence(t, h, src, "point", stamped(t, at), stamped(t, at))
}

func interval(t *testing.T, h *harness, src, from, to string) {
	t.Helper()
	insertEvidence(t, h, src, "interval", stamped(t, from), stamped(t, to))
}

func insertEvidence(t *testing.T, h *harness, src, shape string, started string, ended any) {
	t.Helper()
	db, err := sql.Open("sqlite", h.path)
	if err != nil {
		t.Fatalf("open %s directly: %v", h.path, err)
	}
	defer db.Close()

	var run int64
	if err := db.QueryRow(`SELECT id FROM harvest_run WHERE source_id = ? ORDER BY id LIMIT 1`,
		src).Scan(&run); err != nil {
		t.Fatalf("an event of %s needs a harvest run to have read it: %v", src, err)
	}
	if _, err := db.Exec(`INSERT INTO event
		(first_seen_run, source_id, shape, started_at, ended_at, locator)
		VALUES (?, ?, ?, ?, ?, ?)`,
		run, src, shape, started, ended, "/"+src+"/"+started); err != nil {
		t.Fatalf("insert a %s of %s: %v", shape, src, err)
	}
}
