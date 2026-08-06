package dayview_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
	"github.com/bensyverson/timetattle/internal/dayview"
	"github.com/bensyverson/timetattle/internal/sittings"
)

// How far anything has LOOKED at a day, against how much of it has happened.
//
// The defect these pin is measured rather than supposed. On 2026-08-01 a review
// recorded at 18:23:57.710973Z asserted not-engaged over everything after
// 15:09:08, and 1.4379h of that is now derived as work. The store was NOT
// behind: every source had been harvested by 18:00:39Z, and all 1,989
// observations under the disputed stretch were in `event` by 16:29:24Z. What
// was behind was the DERIVATION, whose last sitting reached 16:39:25.229Z. See
// project/private/2026-08-02-what-the-reviewer-could-not-have-seen.md.

// reach is a store that has been harvested to one instant and clustered to
// another.
func reach(derivedAt, harvestedTo time.Time, behind ...dayview.Lag) dayview.Reach {
	return dayview.Reach{Derived: derivedAt, Harvested: harvestedTo, Behind: behind}
}

func TestTheHorizonIsTheEarliestOfTheThreeThingsThatCouldBound(t *testing.T) {
	now := at(18, 0)
	cases := map[string]struct {
		reach dayview.Reach
		want  time.Time
		bound string
	}{
		"the derivation is behind": {
			reach: reach(at(16, 39), at(17, 30)), want: at(16, 39), bound: "clustering",
		},
		"a source is behind": {
			reach: reach(at(17, 30), at(14, 0)), want: at(14, 0), bound: "harvest",
		},
		"nothing is behind, so the moment of review binds": {
			reach: reach(at(19, 0), at(19, 0)), want: now, bound: "review",
		},
		"an unread bound does not bind": {
			reach: dayview.Reach{Derived: at(16, 39)}, want: at(16, 39), bound: "clustering",
		},
		"nothing read at all leaves the moment of review": {
			reach: dayview.Reach{}, want: now, bound: "review",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			got, bound := c.reach.Bounds(now)
			if !got.Equal(c.want) {
				t.Errorf("horizon = %s, want %s", got.Format(time.TimeOnly), c.want.Format(time.TimeOnly))
			}
			if bound != c.bound {
				t.Errorf("bound = %q, want %q", bound, c.bound)
			}
		})
	}
}

// The whole of TNGyK in one case: the derivation is three hours behind the
// review, and the closure must stop where the clustering stopped rather than
// where the clock did.
func TestAConfirmationAssertsNothingPastTheDerivation(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	day.Reach = reach(at(11, 0), at(17, 55))

	plan, err := day.Confirm(dayview.Answer{
		Mode: dayview.Wholesale, Origin: "ben", At: at(18, 0),
	})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !plan.Review.End.Equal(at(11, 0)) {
		t.Errorf("the review claims to have examined as far as %s; the clustering only "+
			"reached %s", plan.Review.End.Format(time.TimeOnly), at(11, 0).Format(time.TimeOnly))
	}
	for _, a := range plan.Assertions {
		if a.End.After(at(11, 0)) {
			t.Errorf("%s asserted %s..%s, past the derivation's reach at %s",
				a.Assertion, a.Start.Format(time.TimeOnly), a.End.Format(time.TimeOnly),
				at(11, 0).Format(time.TimeOnly))
		}
	}
}

func TestAConfirmationAssertsNothingPastAnUnharvestedSource(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	day.Reach = reach(at(17, 55), at(14, 0), dayview.Lag{Source: "biome-appusage", LastHarvest: at(14, 0)})

	plan, err := day.Confirm(dayview.Answer{
		Mode: dayview.Wholesale, Origin: "ben", At: at(18, 0),
	})
	if err != nil {
		t.Fatalf("Confirm: %v", err)
	}
	if !plan.Review.End.Equal(at(14, 0)) {
		t.Errorf("the review claims to have examined as far as %s; biome-appusage was "+
			"last asked at %s", plan.Review.End.Format(time.TimeOnly), at(14, 0).Format(time.TimeOnly))
	}
}

// The clip must not reach backwards into days that are long over, which is
// every day but today. A day whose horizon is after it ends is unaffected, to
// the assertion.
func TestADayWhollyBehindTheHorizonIsNotClipped(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	plain, err := day.Confirm(dayview.Answer{Mode: dayview.Wholesale, Origin: "ben", At: at(30, 0)})
	if err != nil {
		t.Fatalf("Confirm without a reach: %v", err)
	}

	day.Reach = reach(at(29, 0), at(29, 0))
	bounded, err := day.Confirm(dayview.Answer{Mode: dayview.Wholesale, Origin: "ben", At: at(30, 0)})
	if err != nil {
		t.Fatalf("Confirm with a reach past the day: %v", err)
	}
	if len(plain.Assertions) != len(bounded.Assertions) {
		t.Fatalf("a horizon after the day changed the accounting: %d assertions became %d",
			len(plain.Assertions), len(bounded.Assertions))
	}
	if !plain.Review.End.Equal(bounded.Review.End) {
		t.Errorf("review end %s became %s", plain.Review.End, bounded.Review.End)
	}
}

// The fourth CELL state's data, which is a different vocabulary from the four
// review states. "Nothing was recorded here" and "nothing has looked here yet"
// must not draw alike.
func TestUnlookedIsTheStretchNoHorizonReached(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	day.Reach = reach(at(16, 39), at(17, 55))

	got := day.Unlooked(at(18, 0))
	if len(got) != 1 {
		t.Fatalf("Unlooked = %v, want one span", got)
	}
	if !got[0].Start.Equal(at(16, 39)) || !got[0].End.Equal(day.End) {
		t.Errorf("Unlooked = %s..%s, want %s..%s",
			got[0].Start.Format(time.TimeOnly), got[0].End.Format(time.TimeOnly),
			at(16, 39).Format(time.TimeOnly), day.End.Format(time.TimeOnly))
	}
}

func TestADayEntirelyBehindTheHorizonIsAllLookedAt(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	day.Reach = reach(at(30, 0), at(30, 0))
	if got := day.Unlooked(at(30, 0)); len(got) != 0 {
		t.Errorf("Unlooked = %v, want nothing: every hour of this day has been looked at", got)
	}
}

// CannotRemember records what was LOOKED at, and a stretch nothing has been
// asked about was not looked at however honestly the reviewer failed to recall
// it.
func TestCannotRememberStopsAtTheHorizonToo(t *testing.T) {
	day := build(t, derived(sitting(9, 0, 10, 0, 4, "transcripts")))
	day.Reach = reach(at(16, 39), at(17, 55))
	plan := day.CannotRemember(dayview.Answer{Origin: "ben", At: at(18, 0)})
	if !plan.Review.End.Equal(at(16, 39)) {
		t.Errorf("review end = %s, want the derivation's reach %s",
			plan.Review.End.Format(time.TimeOnly), at(16, 39).Format(time.TimeOnly))
	}
	if plan.Review.Verdict != adjustments.Unremembered {
		t.Errorf("verdict = %q", plan.Review.Verdict)
	}
}

// A named source and how far behind it is, so a warning can say which rather
// than that "something" is stale.
func TestBehindNamesTheSourcesThatHaveNotBeenAsked(t *testing.T) {
	r := reach(at(17, 55), at(14, 0),
		dayview.Lag{Source: "biome-appusage", LastHarvest: at(14, 0)},
		dayview.Lag{Source: "fs-observations", LastHarvest: at(16, 0)})
	said := r.Says(at(18, 0))
	for _, want := range []string{"biome-appusage", "fs-observations", "4h", "2h"} {
		if !strings.Contains(said, want) {
			t.Errorf("Says(...) = %q, which does not name %q", said, want)
		}
	}
}

// A Reach with nothing in it says so rather than reporting everything fresh.
// Three answers, and this is the one this project keeps losing.
func TestAnUnreadReachSaysItWasNotRead(t *testing.T) {
	said := dayview.Reach{}.Says(at(18, 0))
	if said == "" {
		t.Fatal("an unread Reach said nothing at all, which reads as 'nothing is behind'")
	}
	if !strings.Contains(said, "not read") {
		t.Errorf("Says(...) = %q, want it to say the freshness was not read", said)
	}
}

// build assembles the reference day around one derivation, with no evidence.
func build(t *testing.T, d sittings.Derived) dayview.Day {
	t.Helper()
	day, err := dayview.Skeleton(cal, theDay, d, nil, nil)
	if err != nil {
		t.Fatalf("Skeleton: %v", err)
	}
	return day
}
