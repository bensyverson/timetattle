package sittings_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/sittings"
	"github.com/bensyverson/timetattle/internal/timespan"
)

var base = time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)

func at(m int) time.Time { return base.Add(time.Duration(m) * time.Minute) }

func TestASittingsSpanIsTheIntervalItCovers(t *testing.T) {
	s := sittings.Sitting{Start: at(0), End: at(90), Events: 3}
	want := timespan.Span{Start: at(0), End: at(90)}
	if got := s.Span(); !got.Start.Equal(want.Start) || !got.End.Equal(want.End) {
		t.Errorf("Span() = %v..%v, want %v..%v", got.Start, got.End, want.Start, want.End)
	}
	if got, want := s.Duration(), 90*time.Minute; got != want {
		t.Errorf("Duration = %v, want %v", got, want)
	}
}

// Sittings are disjoint by construction, so the total is a sum rather than a
// union — the same property internal/sessions.Result.Total rests on, and the
// reason wall clock is never claimed twice.
func TestDerivedTotalsTheSittings(t *testing.T) {
	d := sittings.Derived{Sittings: []sittings.Sitting{
		{Start: at(0), End: at(90), Events: 2},
		{Start: at(120), End: at(150), Events: 1},
	}}
	if got, want := d.Total(), 2*time.Hour; got != want {
		t.Errorf("Total = %v, want %v", got, want)
	}
}

// A zero ComputedAt is the third state: nothing has been derived at all. It is
// not "no sittings in this window", and a caller that reads the two as the same
// thing reports an empty day for a database nobody has ever sessionized.
func TestTheZeroDerivedSaysNothingHasBeenDerived(t *testing.T) {
	var d sittings.Derived
	if !d.ComputedAt.IsZero() {
		t.Error("the zero Derived claims a derivation time")
	}
	if len(d.Sittings) != 0 || d.Total() != 0 {
		t.Error("the zero Derived claims sittings")
	}
}

// Params.Equal is what refuses a table holding two derivations. It compares
// every field, because a rebuild that changed only the presence sources changes
// the answer as much as one that changed the gap: reading /device/isLocked as
// evidence manufactures a 15h32m sitting out of a night's sleep.
func TestParamsEqualComparesEveryFieldOfTheDerivation(t *testing.T) {
	p := sittings.Params{
		Gap: 15 * time.Minute, LeadIn: 5 * time.Minute, TailOut: 5 * time.Minute,
		PresenceSources: []string{"knowledgec-backlit"},
		IgnoreSources:   []string{"knowledgec-locked"},
	}
	if !p.Equal(p) {
		t.Fatal("a set of parameters is not equal to itself")
	}
	for name, differ := range map[string]func(q *sittings.Params){
		"the gap":              func(q *sittings.Params) { q.Gap = time.Hour },
		"the lead-in":          func(q *sittings.Params) { q.LeadIn = time.Minute },
		"the tail-out":         func(q *sittings.Params) { q.TailOut = time.Minute },
		"the presence sources": func(q *sittings.Params) { q.PresenceSources = nil },
		"the ignored sources":  func(q *sittings.Params) { q.IgnoreSources = []string{"something-else"} },
	} {
		q := p
		differ(&q)
		if p.Equal(q) {
			t.Errorf("two derivations differing in %s were read as one", name)
		}
	}
}

func TestParamsValidateRefusesParametersThatWouldMeanNothing(t *testing.T) {
	good := sittings.Params{Gap: 15 * time.Minute, LeadIn: time.Minute, TailOut: time.Minute}
	if err := good.Validate(); err != nil {
		t.Fatalf("ordinary parameters were refused: %v", err)
	}
	for name, break_ := range map[string]func(p *sittings.Params){
		"a gap of zero":                      func(p *sittings.Params) { p.Gap = 0 },
		"a negative gap":                     func(p *sittings.Params) { p.Gap = -time.Minute },
		"a negative lead-in":                 func(p *sittings.Params) { p.LeadIn = -time.Second },
		"a negative tail-out":                func(p *sittings.Params) { p.TailOut = -time.Second },
		"a source both presence and ignored": func(p *sittings.Params) { p.PresenceSources, p.IgnoreSources = []string{"x"}, []string{"x"} },
	} {
		p := good
		break_(&p)
		if err := p.Validate(); err == nil {
			t.Errorf("Validate accepted %s", name)
		}
	}
}
