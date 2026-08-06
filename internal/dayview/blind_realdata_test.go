//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package dayview_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/config"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// The silence census, over real days rather than three constructed points.
//
// Constructed cases sit beside these rather than instead of them, which is this
// project's rule and has been earned: an over-general supersession rule once
// passed against the entire live Biome stream purely because no real interval
// happened to exercise it.
//
// THESE FOUND A REAL HOLE IN THE CLAIM THE PRESENTATION WAS FIRST ARGUED FROM,
// which is what they are for. The first version asserted that every boundary
// the clustering can place falls inside a silence, full stop. It does not, and
// the counter-example is on 2026-07-28 at 12:59:36Z: a biome-appusage interval
// runs to 12:59:39.106956Z and the sitting ends three seconds BEFORE it,
// because knowledgec-backlit recorded the screen going dark. Observed darkness
// cuts a sitting in the middle of an observed stretch.
//
// The corrected claim is narrower and is the one that matters. See
// TestOnAPointsOnlyDayEveryBoundaryFallsInsideASilence.

// A day in each regime. The dates are the ones the corpus documents rather than
// convenient ones: 2026-03-21 is one of the two highest-leverage days in the
// whole store and 2026-07-28 is the one genuinely concurrent day.
const (
	pointsOnlyDay = "2026-03-21"
	presenceDay   = "2026-07-28"
)

// THE PROPERTY THE PRESENTATION RESTS ON, in the regime it is aimed at.
//
// The blind pass exists for the points-only regime: 175 days, 809.1h, 75.7% of
// all engaged time, two caveated reviewed days and zero job claims behind it.
// knowledgec-backlit — the ONLY stream this project reads as presence — has no
// row before 2026-07-02T02:47:40Z, checked below rather than quoted. So in that
// entire regime there is no darkness to cut anything, every boundary comes from
// a silence, and the parameter-free census is the complete space of boundaries
// with none of them chosen.
func TestOnAPointsOnlyDayEveryBoundaryFallsInsideASilence(t *testing.T) {
	day, events := liveObserved(t, pointsOnlyDay)
	for _, e := range events {
		if e.Source == "knowledgec-backlit" {
			t.Fatalf("%s carries a presence reading; this case's whole premise is that the "+
				"points-only regime has none", pointsOnlyDay)
		}
	}

	silences := day.Silences(day.End, 0)
	var checked int
	for _, p := range gridPoints(day.Params) {
		for _, seam := range seams(t, events, p, day) {
			checked++
			if !insideAny(seam, silences) {
				t.Errorf("at gap %v the clustering placed a boundary across %s..%s, which the "+
					"parameter-free census does not report as silent — so the census is NOT the "+
					"space of boundaries and the blind presentation is unsound for this regime",
					p.Gap, seam.Start.Format(time.RFC3339), seam.End.Format(time.RFC3339))
			}
		}
	}
	if checked == 0 {
		t.Fatalf("%s produced no internal boundary at any parameter value; this case checked "+
			"nothing", pointsOnlyDay)
	}
	t.Logf("%s: %d internal boundaries across the grid, all inside %d silences",
		pointsOnlyDay, checked, len(silences))
}

// AND WHERE IT DOES NOT HOLD, MEASURED RATHER THAN WAVED AT.
//
// In the presence-covered regime a boundary can also be placed by observed
// DARKNESS, which sits wherever the screen went off and not in a silence. That
// is not a parameter inventing a boundary — it is an observation placing one,
// and it does not move with the gap, the lead-in or the tail-out — but it IS a
// boundary the census does not offer the reviewer, and the blind screen
// therefore cannot ask about it.
//
// This case does not assert that no such boundary exists. It asserts that every
// one of them is explained by a presence reading, so the exception is bounded
// and named rather than open-ended.
func TestOnAPresenceCoveredDayTheOnlyExceptionsAreObservedDarkness(t *testing.T) {
	day, events := liveObserved(t, presenceDay)
	silences := day.Silences(day.End, 0)

	var edges []time.Time
	for _, e := range events {
		if e.Source != "knowledgec-backlit" {
			continue
		}
		for _, at := range []time.Time{e.Start, e.End} {
			if !at.IsZero() {
				edges = append(edges, at)
			}
		}
	}
	if len(edges) == 0 {
		t.Skipf("%s carries no knowledgec-backlit event, so there is no exception to bound", presenceDay)
	}

	var checked, dark int
	for _, p := range gridPoints(day.Params) {
		for _, seam := range seams(t, events, p, day) {
			checked++
			// COVERED BY the census rather than inside ONE silence, and the
			// difference is a real reading rather than a loosening. A sleep
			// marker is an unbounded event with one endpoint — a zero-length
			// observation — so it cuts an hour of silence into two adjacent
			// ones without covering any of it. 2026-07-28T13:47:50Z..14:48:02Z
			// is exactly that: an hour holding nothing but two biome sleep
			// horizons. A seam may span such a run; what it may not do is cross
			// an observation with DURATION in it.
			if coveredBy(seam, silences) {
				continue
			}
			// The seam has to be explained by a presence reading at one of its
			// ends, stripped of the margin that pushed it there.
			if !touchesAny(seam.Start.Add(p.TailOut), edges) && !touchesAny(seam.End.Add(-p.LeadIn), edges) {
				t.Errorf("at gap %v a boundary across %s..%s is neither silent nor at a presence "+
					"reading; the exception to the census is wider than observed darkness",
					p.Gap, seam.Start.Format(time.RFC3339), seam.End.Format(time.RFC3339))
				continue
			}
			dark++
		}
	}
	t.Logf("%s: %d internal boundaries, %d of them placed by observed darkness rather than "+
		"by silence", presenceDay, checked, dark)
}

// The census does not move with the parameters, over a real day.
func TestTheLiveCensusIsTheSameAtEveryParameterValue(t *testing.T) {
	day, events := liveObserved(t, presenceDay)

	var first []timespan.Span
	for _, gap := range []time.Duration{2 * time.Minute, 15 * time.Minute, time.Hour} {
		p := day.Params
		p.Gap, p.LeadIn, p.TailOut = gap, gap/3, gap/3
		built, err := dayview.Assemble(cal, day.Day,
			sittings.Derived{Params: p, ComputedAt: day.End}, events, nil, nil)
		if err != nil {
			t.Fatalf("Assemble at gap %v: %v", gap, err)
		}
		got := built.Silences(built.End, 2*time.Minute)
		if first == nil {
			first = got
			t.Logf("%s reports %d silences of 2m or more", presenceDay, len(got))
			continue
		}
		if len(got) != len(first) {
			t.Fatalf("gap %v gives %d silences of 2m or more, want the %d every other "+
				"parameter value gives", gap, len(got), len(first))
		}
		for i := range got {
			if !got[i].Start.Equal(first[i].Start) || !got[i].End.Equal(first[i].End) {
				t.Fatalf("silence %d moved to %s..%s at gap %v", i,
					got[i].Start.Format(time.RFC3339), got[i].End.Format(time.RFC3339), gap)
			}
		}
	}
}

// The census and the observations tile the day exactly. A census that lost time
// would hide stretches nobody could ever be asked about.
func TestTheCensusAndTheObservationsAccountForTheWholeLiveDay(t *testing.T) {
	for _, name := range []string{pointsOnlyDay, presenceDay} {
		day, _ := liveObserved(t, name)
		covered := timespan.Union(append(day.Silences(day.End, 0), day.Observed...))
		left := timespan.Subtract([]timespan.Span{{Start: day.Start, End: day.End}}, covered)
		var lost time.Duration
		for _, s := range left {
			lost += s.Duration()
		}
		if lost > 0 {
			t.Errorf("on %s, %v of the working day is neither an observation nor a silence; "+
				"a reviewer could never be asked about it and nothing would say so", name, lost)
		}
	}
}

// liveObserved assembles one working day from the live store's EVIDENCE alone,
// reading no review and no correction.
//
// It exists because these cases are about observations and nothing else, and
// liveDay reads the whole day including its reviews. That coupling made them
// skip for an unrelated reason the moment migration 0011 landed against a store
// still at 0010 — and a test that skips proves nothing, least of all a property
// a whole presentation rests on.
func liveObserved(t *testing.T, name string) (dayview.Day, []source.Event) {
	t.Helper()
	path, err := store.DefaultPath()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Skipf("cannot stat %s: %v", path, err)
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Skipf("cannot read %s: %v", path, err)
	}
	defer st.Close()

	cfg := config.Default()
	liveCal, err := cfg.Calendar.Sessions()
	if err != nil {
		t.Skipf("the default calendar will not load: %v", err)
	}
	at, err := time.Parse("2006-01-02", name)
	if err != nil {
		t.Fatalf("bad day %q: %v", name, err)
	}
	d := sessions.Day{Year: at.Year(), Month: at.Month(), Date: at.Day()}
	start, end := liveCal.Bounds(d)

	ctx := context.Background()
	derivation, err := st.Sittings(ctx, start, end)
	if err != nil {
		t.Skipf("cannot read the sittings: %v", err)
	}
	if derivation.ComputedAt.IsZero() {
		// Sittings was asked for this one day, so that is all this observes.
		t.Skipf("the live store holds no derivation covering %s", start.Format(time.DateOnly))
	}
	pad := derivation.Params.Gap + derivation.Params.LeadIn + derivation.Params.TailOut
	events, err := st.Events(ctx, store.Filter{From: start.Add(-pad), To: end.Add(pad)})
	if err != nil {
		t.Skipf("cannot read the events: %v", err)
	}
	built, err := dayview.Assemble(liveCal, d, derivation, events, nil, nil)
	if err != nil {
		t.Fatalf("Assemble %s: %v", name, err)
	}
	return built, events
}
