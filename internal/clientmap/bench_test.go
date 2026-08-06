package clientmap_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/clientmap"
)

// The cost of folding an append-only log on every read, measured rather than
// estimated. Task g8vEQ criterion VhH.
//
// TWO COSTS, AND THEY SCALE WITH DIFFERENT THINGS. Conflating them is how a
// "the fold is cheap" claim survives contact with a log a thousand times bigger:
//
//	Fold      once per command. Linear in the number of ASSERTIONS, and it
//	          allocates a map keyed by whatever the log is keyed by.
//	At        once per EVENT — `timetattle metrics` calls it 978,701 times on
//	          the live store (cmd/timetattle/metrics.go, the stamping loop).
//	          Linear in the assertions held for the ONE key it is asked about,
//	          which is what keeps it flat as the log grows in KEYS and is what
//	          makes it grow if the log grows in REVISIONS PER KEY.
//
// The live log is 38 assertions over 38 projects, one each. That is small
// enough that a quadratic fold would not show up, so the sizes below run past
// it deliberately, up to 127,878 — the number of DISTINCT LOCATORS in the live
// store, which is the size a locator-to-project log keyed per locator would
// have on day one.
//
// Reproduce:
//
//	go test ./internal/clientmap -bench . -benchmem -run '^$' -count=1

// keysAndRevisions is the two dimensions a log can grow in, held apart on
// purpose: 38x1 and 1x38 are both 38 rows and they do not cost the same.
var foldSizes = []struct{ keys, revisions int }{
	{38, 1},      // the live client_project log, 2026-08-04
	{38, 10},     // the same projects, each corrected ten times
	{3800, 1},    // a hundred times the projects
	{127878, 1},  // one assertion per distinct locator in the live store
	{127878, 10}, // and each of those revised ten times
}

func buildLog(keys, revisions int) []clientmap.Assertion {
	base := time.Date(2026, 8, 4, 3, 4, 13, 0, time.UTC)
	log := make([]clientmap.Assertion, 0, keys*revisions)
	var id int64
	for k := range keys {
		key := fmt.Sprintf("project-%06d", k)
		source := fmt.Sprintf("/w/%s", key)
		for r := range revisions {
			id++
			log = append(log, clientmap.Assertion{
				ID:         id,
				Project:    key,
				Client:     fmt.Sprintf("client-%d", k%6),
				Source:     source,
				RecordedAt: base.Add(time.Duration(id) * time.Microsecond),
			})
			_ = r
		}
	}
	return log
}

func BenchmarkFold(b *testing.B) {
	for _, s := range foldSizes {
		log := buildLog(s.keys, s.revisions)
		b.Run(fmt.Sprintf("keys=%d/revisions=%d/rows=%d", s.keys, s.revisions, len(log)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = clientmap.Fold(log)
			}
		})
	}
}

// BenchmarkStamp is the cost `metrics` actually pays: one At per event. The
// event count is fixed at the live corpus so the only variable is the log.
//
// It is the figure that decides whether a SECOND log doubles the work on every
// read, which is the claim task g8vEQ's description makes and does not measure.
func BenchmarkStamp(b *testing.B) {
	const events = 978701
	instant := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	for _, s := range foldSizes {
		log := buildLog(s.keys, s.revisions)
		m := clientmap.Fold(log)
		keys := make([]string, s.keys)
		for k := range keys {
			keys[k] = fmt.Sprintf("project-%06d", k)
		}
		b.Run(fmt.Sprintf("keys=%d/revisions=%d/rows=%d", s.keys, s.revisions, len(log)), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for i := range events {
					m.At(keys[i%len(keys)], instant)
				}
			}
		})
	}
}
