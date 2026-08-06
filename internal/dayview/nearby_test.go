package dayview_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timespan"
)

// THE EVIDENCE FLANKING A SILENCE, WHICH IS THE ONLY CUE THERE CAN BE.
//
// A silence holds no observation, by construction. So a reviewer asked what he
// was doing inside one has nothing whatever to answer from unless the
// observations either side of it are put on the screen — and those are a fact
// about a DIFFERENT span, not the model's answer to the question being asked.

// nearbyApp is an observation carrying an application identifier, which is what
// biome-appusage emits and what a reviewer actually recognises. The reference
// day's own `evidence` helper names a project, and on the day that provoked
// this work 573 of 576 observations named none.
func nearbyApp(src, bundle string, hour, minute int) source.Event {
	e := source.Event{
		Start: at(hour, minute), End: at(hour, minute),
		Shape: source.Point, Source: src,
	}
	e.Identity.App = bundle
	return e
}

func TestNearbySaysWhatWasSeenEitherSideOfASilence(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		nearbyApp("appusage", "com.apple.Safari", 9, 1),
		evidence("beech", "gitlog", 12, 0))

	// The silence is 09:01..12:00. Its flanks are the observed runs either side
	// of it, which the caller bounds with the neighbouring silences: the run up
	// to 09:01 and the instant at 12:00.
	before, after := day.Nearby(span(9, 0, 9, 1), span(12, 0, 12, 0), 4)
	// Both sources that saw anything, NEAREST THE SILENCE FIRST: appusage at
	// 09:01 ahead of transcripts at 09:00. Ordering by distance is what makes
	// the top of the list the freshest cue rather than an alphabetical one.
	if len(before) != 2 || before[0].Source != "appusage" || before[1].Source != "transcripts" {
		t.Fatalf("before = %s, want appusage then transcripts", renderTraces(before))
	}
	if len(before[0].Seen) != 1 || before[0].Seen[0].What != "com.apple.Safari" ||
		!before[0].Seen[0].At.Equal(at(9, 1)) {
		t.Errorf("before = %s, want com.apple.Safari at 09:01", renderTraces(before))
	}
	if len(after) != 1 || after[0].Source != "gitlog" {
		t.Fatalf("after = %s, want the one source that saw something from 12:00",
			renderTraces(after))
	}
	if len(after[0].Seen) != 1 || after[0].Seen[0].What != "beech" {
		t.Errorf("after = %s, want beech at 12:00", renderTraces(after))
	}
}

// THE WHOLE NON-CIRCULARITY PROPERTY, HELD TO BY MEASUREMENT RATHER THAN BY
// ARGUMENT.
//
// The census does not move with the three parameters being fitted, and
// TestTheCensusIsTheSameAtEveryParameterValue holds it to that. Neither may the
// evidence drawn beside the census. If this ever fails, the blind screen is
// showing something that moves with the model, and the labels it collects are
// worth exactly what the confirming pass's are worth.
func TestNearbyDoesNotMoveWhenTheParametersDo(t *testing.T) {
	events := []source.Event{
		evidence("acorn", "transcripts", 9, 0),
		nearbyApp("appusage", "com.apple.Safari", 9, 1),
		nearbyApp("appusage", "us.zoom.xos", 11, 30),
		evidence("beech", "gitlog", 12, 0),
	}
	var first string
	for _, gap := range []time.Duration{2 * time.Minute, 15 * time.Minute, time.Hour} {
		p := params
		p.Gap, p.LeadIn, p.TailOut = gap, gap/3, gap/3
		built, err := dayview.Assemble(cal, theDay,
			sittings.Derived{Params: p, ComputedAt: at(29, 0)}, events, nil, nil)
		if err != nil {
			t.Fatalf("Assemble at %v: %v", gap, err)
		}
		before, after := built.Nearby(span(11, 30, 11, 30), span(12, 0, 12, 0), 4)
		got := renderTraces(before) + " | " + renderTraces(after)
		if first == "" {
			first = got
			continue
		}
		if got != first {
			t.Errorf("at a %v threshold the evidence beside a silence reads %q, and at the "+
				"first value it read %q; a cue that moves with the parameters IS the parameters",
				gap, got, first)
		}
	}
}

// Every source that saw something is named. A listing of only the busiest would
// make "this source recorded nothing here" and "this source was left out of the
// list" render identically, which is the collapse this project has shipped
// twice, in three separate tools.
func TestNearbyNamesEverySourceThatSawSomething(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		nearbyApp("appusage", "com.apple.Safari", 9, 0),
		nearbyApp("appusage", "us.zoom.xos", 9, 1),
		evidence("acorn", "gitlog", 9, 1),
		evidence("beech", "later", 12, 0))

	before, _ := day.Nearby(span(9, 0, 9, 1), span(12, 0, 12, 0), 1)
	var named []string
	for _, tr := range before {
		named = append(named, tr.Source)
	}
	slices.Sort(named)
	if want := []string{"appusage", "gitlog", "transcripts"}; !slices.Equal(named, want) {
		t.Errorf("Nearby named %v, want every source that saw anything: %v", named, want)
	}
}

// Nearest the silence first, and what was left out is COUNTED rather than
// trailed off. A budget nobody mentions is a truncated list read as a whole one.
func TestNearbyKeepsTheSightingNearestTheSilenceAndSaysWhatItLeftOut(t *testing.T) {
	day := blindDay(t,
		nearbyApp("appusage", "com.apple.Mail", 6, 0),
		nearbyApp("appusage", "us.zoom.xos", 7, 0),
		nearbyApp("appusage", "com.apple.Safari", 9, 0),
		evidence("beech", "later", 12, 0))

	before, _ := day.Nearby(span(6, 0, 9, 0), span(12, 0, 12, 0), 2)
	if len(before) != 1 {
		t.Fatalf("before = %s, want one source", renderTraces(before))
	}
	got := before[0]
	if len(got.Seen) != 2 {
		t.Fatalf("Seen = %s, want the two nearest of three", renderTraces(before))
	}
	if got.Seen[0].What != "com.apple.Safari" || got.Seen[1].What != "us.zoom.xos" {
		t.Errorf("Seen = %s, want the nearest first", renderTraces(before))
	}
	if got.More != 1 {
		t.Errorf("More = %d, want the one thing left out to be counted", got.More)
	}
}

// A caller reads a MARGIN either side of the day, so that a project's own
// clustering is not truncated at the edge. That margin is the model's own width
// and the evidence in it belongs to a different working day. Neither reason
// permits it beside a silence.
func TestNearbyReadsNothingFromOutsideTheWorkingDay(t *testing.T) {
	yesterday := nearbyApp("appusage", "com.apple.Mail", 4, 30)
	day := blindDay(t, yesterday,
		evidence("acorn", "transcripts", 9, 0),
		evidence("beech", "later", 12, 0))

	// The span asked about deliberately reaches back past the day's own start,
	// which is where the caller's margin lives.
	before, _ := day.Nearby(span(4, 0, 8, 0), span(12, 0, 12, 0), 4)
	for _, tr := range before {
		for _, s := range tr.Seen {
			t.Errorf("the opening silence was cued with %s at %s, which is before this working "+
				"day begins", s.What, s.At.Format(time.TimeOnly))
		}
	}
}

// THE LINE THE TASK DID NOT DRAW, AND IT HAD TO BE DRAWN SOMEWHERE.
//
// The census is built from evidence of WORK: presence and ignored streams never
// reach Day.Observed. So a presence reading can and does fall INSIDE a silence
// — on 2026-07-17 the 2h24m silence at 11:53 holds six knowledgec-backlit rows,
// five of them reading "on". Drawing those beside the silence would not be
// circular. It would be worse: it would ANSWER the question, from a stream a
// script can read unaided, and the answer would be recorded as a person's
// recollection.
func TestNearbyLeavesOutTheSourcesTheCensusIsNotBuiltFrom(t *testing.T) {
	day := blindDay(t,
		evidence("acorn", "transcripts", 9, 0),
		presence("on", 10, 0, 11, 0),
		source.Event{Start: at(10, 30), End: at(10, 30), Shape: source.Point, Source: "locked"},
		evidence("beech", "later", 12, 0))

	// The readings really are inside the silence: that is this case's premise,
	// and it is the fact the whole decision rests on.
	if got := day.Silences(at(29, 0), 0); !slices.ContainsFunc(got, func(s timespan.Span) bool {
		return !s.Start.After(at(10, 0)) && !s.End.Before(at(11, 0))
	}) {
		t.Fatalf("Silences = %d spans, none covering 10:00..11:00; this case needs one", len(got))
	}
	// The whole silence is passed as the span, which is the strongest form of
	// the case: even asked directly about the stretch these readings fall in,
	// nothing draws them.
	before, after := day.Nearby(span(9, 0, 12, 0), span(12, 0, 12, 0), 4)
	for _, tr := range append(slices.Clone(before), after...) {
		if tr.Source == "backlit" || tr.Source == "locked" {
			t.Errorf("Nearby drew %q beside a silence its own readings fall inside; that answers "+
				"the question rather than cueing it", tr.Source)
		}
	}

	n, sources := day.Unshown()
	if n != 2 {
		t.Errorf("Unshown counted %d readings, want the 2 that were withheld", n)
	}
	if want := []string{"backlit", "locked"}; !slices.Equal(sources, want) {
		t.Errorf("Unshown named %v, want %v; a withheld stream must be named, or its absence "+
			"from the screen reads as nothing having been recorded", sources, want)
	}
}

func renderTraces(traces []dayview.Trace) string {
	out := make([]string, 0, len(traces))
	for _, tr := range traces {
		var line strings.Builder
		line.WriteString(tr.Source + ":")
		for _, s := range tr.Seen {
			line.WriteString(" " + s.What + "@" + s.At.Format(time.TimeOnly))
		}
		if tr.More > 0 {
			line.WriteString(" (+" + strings.Repeat("*", tr.More) + ")")
		}
		out = append(out, line.String())
	}
	if len(out) == 0 {
		return "(nothing)"
	}
	return strings.Join(out, "; ")
}
