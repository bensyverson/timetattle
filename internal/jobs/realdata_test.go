//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package jobs

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// The fixtures next door describe rows no live database contains. These tests
// do the opposite job: they run the real reader over whatever real databases
// this machine actually has, because a rule that has only ever met constructed
// data is a rule nobody has tested, and a rule that has only ever met real data
// may simply never have been exercised.
//
// Nothing here asserts a fixed count. The databases are live — the timetattle
// one grew by seven rows while this package was being written, including the
// claim that authorised it — so every figure is measured and logged, and only
// properties that must hold whatever the numbers are get asserted.

// realRoots returns every directory on this machine holding a .jobs.db,
// discovered rather than hardcoded: walk up from the test's own working
// directory to the nearest ancestor that has one (this repository, whether the
// suite runs in the main checkout or in a linked worktree), then take that
// directory's siblings. On the machine this was written for, that is ~/git.
func realRoots(t *testing.T) []string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("no working directory: %v", err)
	}

	repo := ""
	for dir := wd; ; {
		if _, err := os.Stat(Path(dir)); err == nil {
			repo = dir
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if repo == "" {
		t.Skip("no .jobs.db in any ancestor of the working directory")
	}

	entries, err := os.ReadDir(filepath.Dir(repo))
	if err != nil {
		return []string{repo}
	}
	var roots []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(filepath.Dir(repo), e.Name())
		if _, err := os.Stat(Path(dir)); err == nil {
			roots = append(roots, dir)
		}
	}
	if len(roots) == 0 {
		return []string{repo}
	}
	sort.Strings(roots)
	return roots
}

// rawRowCount is what is on disk, read without going through this package, so
// that the reconciliation below subtracts two independent numbers rather than
// one number against itself.
func rawRowCount(t *testing.T, root string) int {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+Path(root)+"?mode=ro")
	if err != nil {
		t.Fatalf("open %s: %v", Path(root), err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count %s: %v", Path(root), err)
	}
	return n
}

// TestRealDatabasesReconcile is the standard this project holds a source to:
// state the number on disk, the number dropped and why, and the number that
// comes out, so that the three subtract.
//
// The only thing dropped is multiplicity within one command — rows agreeing on
// instant, task and event type. Every other row becomes an observation.
func TestRealDatabasesReconcile(t *testing.T) {
	roots := realRoots(t)
	totalDisk, totalOut := 0, 0
	for _, root := range roots {
		disk := rawRowCount(t, root)
		events, err := NewEvents(root).Collect(t.Context(), time.Time{})
		if err != nil {
			t.Fatalf("%s: Collect: %v", root, err)
		}
		collapsed := disk - len(events)
		if collapsed < 0 {
			t.Errorf("%s: %d rows on disk produced %d events; a reader cannot invent rows",
				root, disk, len(events))
		}
		t.Logf("%-40s %6d on disk = %6d observations + %5d rows from the same command",
			filepath.Base(root), disk, len(events), collapsed)
		totalDisk += disk
		totalOut += len(events)
	}
	t.Logf("%-40s %6d on disk = %6d observations + %5d rows from the same command",
		fmt.Sprintf("TOTAL (%d databases)", len(roots)), totalDisk, totalOut, totalDisk-totalOut)
	if totalDisk == 0 {
		t.Skip("no rows in any real database")
	}
}

// TestRealEventsAreAllStorable. An event the store refuses costs a source its
// entire harvest, so every real row must survive validation.
func TestRealEventsAreAllStorable(t *testing.T) {
	roots := realRoots(t)
	for _, s := range Sources(roots...) {
		events, err := s.Collect(t.Context(), time.Time{})
		if err != nil {
			t.Fatalf("%s: Collect: %v", s.ID(), err)
		}
		if len(events) == 0 {
			t.Fatalf("%s: nothing came back from %d real databases", s.ID(), len(roots))
		}
		for i, e := range events {
			if err := e.Validate(); err != nil {
				t.Fatalf("%s: event %d is unusable: %v", s.ID(), i, err)
			}
			if !filepath.IsAbs(e.Locator) {
				t.Fatalf("%s: event %d has locator %q", s.ID(), i, e.Locator)
			}
			if e.Start.Year() < 2020 || e.Anchor().After(time.Now().AddDate(0, 0, 1)) {
				t.Fatalf("%s: event %d is dated %s, which is not a plausible instant",
					s.ID(), i, e.Anchor())
			}
		}
		t.Logf("%s: %d events across %d databases", s.ID(), len(events), len(roots))
	}
}

// TestRealExpiryTimestampsAreNeverAnEnd is criterion ZEG against real data.
//
// It is not enough to check that expired claims are unbounded. The specific
// defect being guarded is an END synthesised at an expiry timestamp, so this
// collects every claim_expired instant and asserts that no emitted span ends at
// one.
//
// The expiry is keyed by DATABASE AND TASK, not by instant alone, and that
// precision was forced by real data rather than chosen. A global set of expiry
// instants flags seven perfectly sound spans, because the tool writes
// claim_expired lazily: `job done X` runs the expiry sweep as a side effect, so
// the sweep's rows carry the same second as the `done` that provoked them, for
// entirely unrelated tasks. In cypress at unix 1780531158 one claim_expired on
// task l5P7n sits beside four `done` rows on four other tasks; in Jobs at
// 1777497881, two expiries sit beside another task's `done`. Those `done` rows
// are real observed ends and their spans are real. Only an end taken from the
// expiry of the SAME claim is the invention this guards against.
func TestRealExpiryTimestampsAreNeverAnEnd(t *testing.T) {
	roots := realRoots(t)

	type expiry struct {
		db, task string
		at       int64
	}
	expiries := map[expiry]bool{}
	for _, root := range roots {
		db, err := sql.Open("sqlite", "file:"+Path(root)+"?mode=ro")
		if err != nil {
			t.Fatalf("open %s: %v", Path(root), err)
		}
		rows, err := db.Query(`
			SELECT COALESCE(t.short_id, ''), e.created_at
			FROM events AS e LEFT JOIN tasks AS t ON t.id = e.task_id
			WHERE e.event_type = 'claim_expired'`)
		if err != nil {
			db.Close()
			t.Fatalf("read %s: %v", Path(root), err)
		}
		for rows.Next() {
			var (
				task string
				at   int64
			)
			if err := rows.Scan(&task, &at); err != nil {
				t.Fatal(err)
			}
			expiries[expiry{db: Path(root), task: task, at: at}] = true
		}
		rows.Close()
		db.Close()
	}
	if len(expiries) == 0 {
		t.Skip("no expired claims in any real database, so nothing here is being measured")
	}

	spans, err := NewClaims(roots...).Collect(t.Context(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	lost := 0
	for i, e := range spans {
		if e.Shape == source.Unbounded {
			if e.Unknown != source.UnknownLost {
				t.Errorf("span %d is unbounded for reason %q, want lost", i, e.Unknown)
			}
			if !e.End.IsZero() {
				t.Errorf("span %d is unbounded and yet ends at %s", i, e.End)
			}
			lost++
			continue
		}
		if expiries[expiry{db: e.Locator, task: e.Identity.Document, at: e.End.Unix()}] {
			t.Errorf("span %d on task %s ends at %s, which is that claim's own expiry: "+
				"%s of work has been invented out of silence",
				i, e.Identity.Document, e.End, e.Duration())
		}
	}
	t.Logf("%d expiries across %d databases, %d spans, %d of them unbounded and lost",
		len(expiries), len(roots), len(spans), lost)
}

// TestRealClaimSpansAreBounded measures rather than assumes what a claim
// interval is worth, and asserts the one property that must hold: an interval's
// end cannot precede its start.
//
// MEASURED 2026-08-01 across nine live databases, paired against the FIRST
// terminator: 1,251 clean spans, median 4m53s, p90 21m35s, p95 28m35s, p99
// 45m18s, exactly one over two hours at 2h59m32s. The task brief's "claim →
// done, MAX 583.7 min" does not appear, because it was never a claim → done
// pair — see TestDoneAfterAnExpiryDoesNotPairWithTheExpiredClaim. This test
// stays a live measurement rather than a fixed assertion, so it keeps telling
// the truth if the shape of the work changes.
func TestRealClaimSpansAreBounded(t *testing.T) {
	roots := realRoots(t)
	spans, err := NewClaims(roots...).Collect(t.Context(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var durations []time.Duration
	for i, e := range spans {
		if e.Shape != source.Interval {
			continue
		}
		if e.End.Before(e.Start) {
			t.Fatalf("span %d ends %s before it starts", i, e.Start.Sub(e.End))
		}
		durations = append(durations, e.Duration())
	}
	if len(durations) == 0 {
		t.Skip("no completed claims in any real database")
	}
	slices.Sort(durations)

	pct := func(p float64) time.Duration {
		i := int(float64(len(durations)) * p)
		if i >= len(durations) {
			i = len(durations) - 1
		}
		return durations[i]
	}
	overTwoHours := 0
	for _, d := range durations {
		if d > 2*time.Hour {
			overTwoHours++
		}
	}
	t.Logf("%d completed claim spans: median %s, p90 %s, p95 %s, p99 %s, max %s; %d over two hours",
		len(durations), pct(0.5), pct(0.9), pct(0.95), pct(0.99), durations[len(durations)-1],
		overTwoHours)
}

// TestRealOpenClaimsProduceNoSpan. This repository's own database always has at
// least one open claim while this suite runs, because running it is work
// somebody claimed. The property under test is that the claim's point survives
// and no span is invented for it.
func TestRealOpenClaimsProduceNoSpan(t *testing.T) {
	roots := realRoots(t)
	spans, err := NewClaims(roots...).Collect(t.Context(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	for i, e := range spans {
		if e.Unknown == source.UnknownHorizon {
			t.Fatalf("span %d is a horizon; a horizon and the lost span that replaces it "+
				"share one event_identity key and the horizon would win for ever", i)
		}
	}

	points, err := NewEvents(roots...).Collect(t.Context(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	claims := 0
	for _, e := range points {
		if e.State == "claimed" {
			claims++
		}
	}
	if claims == 0 {
		t.Skip("no claims in any real database")
	}
	t.Logf("%d claim points, %d spans; %d claims produced no span (open or unrecognised terminator)",
		claims, len(spans), claims-len(spans))
}

// TestRealCollectIsRepeatable. The store deduplicates by content, so two reads
// of an unchanged database must produce identical events or every harvest would
// write the same evidence again under a new identity.
func TestRealCollectIsRepeatable(t *testing.T) {
	roots := realRoots(t)
	for _, s := range Sources(roots...) {
		first, err := s.Collect(t.Context(), time.Time{})
		if err != nil {
			t.Fatalf("%s: Collect: %v", s.ID(), err)
		}
		second, err := s.Collect(t.Context(), time.Time{})
		if err != nil {
			t.Fatalf("%s: Collect: %v", s.ID(), err)
		}
		if len(first) != len(second) {
			// The live database can grow between the two reads; only a
			// shrinking or reordered answer is a defect.
			t.Logf("%s: %d then %d events — the database is live", s.ID(), len(first), len(second))
		}
		n := min(len(first), len(second))
		for i := range n {
			if first[i] != second[i] {
				t.Fatalf("%s: event %d differs between two reads:\n%#v\n%#v",
					s.ID(), i, first[i], second[i])
			}
		}
	}
}

// TestRealIdentitiesDoNotCollideAcrossDatabases.
//
// event_identity keys on (source_id, shape, endpoints, app, document, state)
// and NOT on locator, so two databases could in principle write one row
// between them: the same task short id, the same event type, the same second.
// Short ids are per-database and randomly assigned, so this should never
// happen, but "should never" is what this project measures rather than assumes.
func TestRealIdentitiesDoNotCollideAcrossDatabases(t *testing.T) {
	roots := realRoots(t)
	if len(roots) < 2 {
		t.Skip("only one real database, so nothing can collide")
	}
	for _, s := range Sources(roots...) {
		events, err := s.Collect(t.Context(), time.Time{})
		if err != nil {
			t.Fatalf("%s: Collect: %v", s.ID(), err)
		}
		type key struct {
			start, end string
			doc, state string
			shape      source.Shape
		}
		seen := make(map[key]string, len(events))
		collisions := 0
		for _, e := range events {
			k := key{
				start: e.Start.String(), end: e.End.String(),
				doc: e.Identity.Document, state: e.State, shape: e.Shape,
			}
			if prior, dup := seen[k]; dup {
				collisions++
				t.Errorf("%s: %s and %s produce one stored row: %+v", s.ID(), prior, e.Locator, k)
			}
			seen[k] = e.Locator
		}
		t.Logf("%s: %d events, %d distinct store identities, %d collisions",
			s.ID(), len(events), len(seen), collisions)
	}
}
