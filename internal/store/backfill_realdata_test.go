//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run it
// deliberately:
//
//	go test -tags realdata -run TestTheWholeTranscriptCorpus -timeout 30m ./internal/store/
//
// See docs/content/docs/testing.md for what the two runs buy, and
// internal/corpusgate for what stops the boundary eroding.

package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/transcripts"
	"github.com/bensyverson/timetattle/internal/usage"
)

// The backfill at the size it will actually be run at, against a store built
// from this machine's whole corpus and thrown away afterwards. THE LIVE STORE IS
// NEVER OPENED: every row here is written into a temporary one, which is the
// only way to exercise a first-ever mutation at scale without performing it.
//
// A constructed fixture cannot answer what this asks. Eight hundred thousand
// real records include every shape the corpus has — replayed human turns,
// subagent files, records stating no figures — and the properties that matter
// (nothing left unfilled, nothing offered twice, and a roll-up that equals what
// the collector itself read) are only meaningful over all of them at once.
func TestTheWholeTranscriptCorpusBackfillsOntoAStoreHarvestedWithoutPayloads(t *testing.T) {
	root, err := transcripts.DefaultRoot()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	ctx := context.Background()

	collected := time.Now()
	events, err := transcripts.NewReader(root).Collect(ctx, time.Time{})
	if err != nil {
		t.Fatalf("collect %s: %v", root, err)
	}
	if len(events) < 1000 {
		t.Skipf("%s holds %d records; this check is about a corpus", root, len(events))
	}
	t.Logf("collected %d records in %s", len(events), time.Since(collected).Round(time.Millisecond))

	// What the collector itself read, computed here rather than queried, so the
	// roll-up below is checked against an independent count of the same corpus.
	var want store.Usage
	sessions := map[string]bool{}
	for _, e := range events {
		rec, err := usage.Decode(e.Payload)
		if err != nil {
			t.Fatalf("a payload the collector produced cannot be read back: %v", err)
		}
		if e.Payload != "" {
			want.Observations++
		}
		if rec.Session != "" {
			sessions[rec.Session] = true
		}
		if rec.Tokens == nil {
			continue
		}
		for _, f := range []struct {
			figure *int64
			into   *store.Tokens
		}{
			{rec.Tokens.Input, &want.Input},
			{rec.Tokens.Output, &want.Output},
			{rec.Tokens.CacheRead, &want.CacheRead},
			{rec.Tokens.CacheCreation, &want.CacheCreation},
		} {
			if f.figure != nil {
				f.into.Records++
				f.into.Total += *f.figure
			}
		}
	}
	want.Sessions = len(sessions)

	// The store as it stood before migration 0014: every observation, no payload.
	f := newStore(t)
	stored := time.Now()
	if added := harvest(t, f, unpaid(events)); added != len(events) {
		t.Fatalf("the harvest stored %d of %d records", added, len(events))
	}
	t.Logf("harvested %d payload-less rows in %s", len(events), time.Since(stored).Round(time.Millisecond))

	dryStarted := time.Now()
	dry := backfill(t, f.Store, events, store.BackfillOptions{DryRun: true})
	t.Logf("dry run in %s: %+v", time.Since(dryStarted).Round(time.Millisecond), dry)

	filled := time.Now()
	first := backfill(t, f.Store, events, store.BackfillOptions{})
	t.Logf("filled %d rows in %s", first.Filled, time.Since(filled).Round(time.Millisecond))

	if first.Filled != dry.Fillable {
		t.Errorf("the dry run predicted %d fillable rows and the pass filled %d", dry.Fillable, first.Filled)
	}
	if first.Unstored != 0 {
		t.Errorf("%d records matched no row; every record offered here was harvested from the same read",
			first.Unstored)
	}
	if first.Unmatched != 0 {
		t.Errorf("%d rows went unmatched; every row here was written from the records that were offered back",
			first.Unmatched)
	}
	if first.RepeatOffers != 0 {
		t.Errorf("%d records repeated an identity already offered; internal/transcripts resolves replays on "+
			"(Document, Start), both of which are inside event_identity, so this must be 0 over a real corpus",
			first.RepeatOffers)
	}

	// The figures the store now reports have to be the figures the collector
	// read. Anything else means the pass wrote a payload onto the wrong row.
	got, err := f.Usage(ctx, store.UsageFilter{Source: transcripts.ID})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got != want {
		t.Errorf("the backfilled store rolls up to %+v; the corpus it was filled from says %+v", got, want)
	}

	second := backfill(t, f.Store, events, store.BackfillOptions{})
	if second.Filled != 0 {
		t.Errorf("a second pass over the whole corpus filled %d rows", second.Filled)
	}
	if second.AlreadyCarrying != first.Filled {
		t.Errorf("the second pass found %d rows already carrying a payload and the first filled %d",
			second.AlreadyCarrying, first.Filled)
	}
}
