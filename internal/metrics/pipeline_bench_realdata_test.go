//go:build realdata

// This file is a CORPUS BENCHMARK. Its subject is the cost of one full
// `timetattle metrics` pass over a real store, so `go test ./...` does not
// compile it at all. Run it deliberately:
//
//	go test -tags realdata ./internal/metrics -run '^$' -bench BenchmarkLivePipeline -benchtime 1x -benchmem
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package metrics_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// # Why a live-store benchmark rather than a fixture one
//
// internal/storefixture carries one regime per day and a few dozen events. It
// is the right instrument for behaviour and the wrong one for allocation
// shape: nothing in it grows a slice past its first few doublings, so an
// allocation profile taken against it names the setup code and not the
// pipeline. The cost this benchmark exists to expose is proportional to the
// number of rows read, and only a real store has enough of them.
//
// The consequence is that the figures move with whatever the machine's store
// holds. That is why every recorded number names the store's event count
// alongside it, and why the benchmark reports allocations per operation rather
// than only nanoseconds — allocation counts are the part that can be compared
// across a harvest.

// livePipeline holds the store and the parameters every sub-benchmark shares,
// so that opening the database is not measured as part of any phase.
type livePipeline struct {
	st       *store.Store
	params   sessions.Params
	excludes sessions.Excludes
}

// openLive mirrors what `timetattle metrics` does before it reads anything:
// default config, default store path, read-only open. It skips rather than
// fails when the machine has no store, following internal/store's realdata
// tests.
func openLive(b *testing.B) livePipeline {
	b.Helper()
	path, err := store.DefaultPath()
	if err != nil {
		b.Skipf("no home directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		b.Skipf("cannot stat the store: %v", err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		b.Skipf("cannot read the store: %v", err)
	}
	b.Cleanup(func() { st.Close() })

	cfg := config.Default()
	x, err := sessions.NewExcludes(cfg.Sessions.Exclude)
	if err != nil {
		b.Fatalf("NewExcludes: %v", err)
	}
	return livePipeline{
		st: st,
		params: sessions.Params{
			Gap:             cfg.Sessions.Gap.Duration(),
			LeadIn:          cfg.Sessions.LeadIn.Duration(),
			TailOut:         cfg.Sessions.TailOut.Duration(),
			PresenceSources: cfg.Sessions.PresenceSources,
			IgnoreSources:   cfg.Sessions.IgnoreSources,
		},
		excludes: x,
	}
}

// BenchmarkLivePipeline measures the four phases `timetattle metrics` runs
// between opening the store and formatting a report, separately and then
// together.
//
// They are separate on purpose. A single end-to-end figure cannot say whether
// the cost is in the read layer or in the arithmetic on top of it, and the two
// are fixed by entirely different changes. The whole-pipeline case is here as
// well so that the parts can be reconciled against the sum rather than assumed
// to add up.
//
// Sub-benchmarks:
//
//	events   store read: SQL, scan, and materialising every row
//	retain   dropping observations that are not evidence of work
//	stamp    folding the client-project log over the events
//	compute  clustering and attribution
//	all      every phase, in the order the command runs them
func BenchmarkLivePipeline(b *testing.B) {
	p := openLive(b)
	ctx := context.Background()

	// Read once outside the timed loops, so that the phases downstream of the
	// read are not paying for it. Each of those phases is handed a fresh copy
	// per iteration where it would otherwise mutate what it was given.
	events, err := p.st.Events(ctx, store.Filter{})
	if err != nil {
		b.Fatalf("Events: %v", err)
	}
	if len(events) == 0 {
		b.Skip("the live store holds no events")
	}
	b.Logf("store holds %d events", len(events))

	b.Run("events", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			got, err := p.st.Events(ctx, store.Filter{})
			if err != nil {
				b.Fatalf("Events: %v", err)
			}
			if len(got) != len(events) {
				b.Fatalf("read %d events, expected %d", len(got), len(events))
			}
		}
	})

	kept, _ := sessions.Retain(events, p.excludes)

	b.Run("retain", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			got, _ := sessions.Retain(events, p.excludes)
			if len(got) != len(kept) {
				b.Fatalf("retained %d events, expected %d", len(got), len(kept))
			}
		}
	})

	b.Run("stamp", func(b *testing.B) {
		cm, err := p.st.ClientMap(ctx, time.Time{})
		if err != nil {
			b.Fatalf("ClientMap: %v", err)
		}
		// The stamp writes into the slice it is given, so each iteration gets
		// its own copy; the copy is outside the timer.
		b.ReportAllocs()
		for b.Loop() {
			b.StopTimer()
			work := make([]source.Event, len(kept))
			copy(work, kept)
			b.StartTimer()
			for i := range work {
				if work[i].Identity.Project == "" {
					continue
				}
				if client, ok := cm.At(work[i].Identity.Project, work[i].Anchor()); ok {
					work[i].Identity.Client = client
				}
			}
		}
	})

	b.Run("compute", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if _, err := metrics.Compute(kept, p.params); err != nil {
				b.Fatalf("Compute: %v", err)
			}
		}
	})

	b.Run("all", func(b *testing.B) {
		cm, err := p.st.ClientMap(ctx, time.Time{})
		if err != nil {
			b.Fatalf("ClientMap: %v", err)
		}
		b.ReportAllocs()
		for b.Loop() {
			got, err := p.st.Events(ctx, store.Filter{})
			if err != nil {
				b.Fatalf("Events: %v", err)
			}
			got, _ = sessions.Retain(got, p.excludes)
			for i := range got {
				if got[i].Identity.Project == "" {
					continue
				}
				if client, ok := cm.At(got[i].Identity.Project, got[i].Anchor()); ok {
					got[i].Identity.Client = client
				}
			}
			if _, err := metrics.Compute(got, p.params); err != nil {
				b.Fatalf("Compute: %v", err)
			}
		}
	})
}
