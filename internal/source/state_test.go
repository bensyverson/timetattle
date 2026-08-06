package source_test

import (
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// Presence is not a table and not a special case. It is an ordinary interval
// carrying the reading the sampler took, which is what makes the third state —
// "the sampler was silent here" — fall out of the data model as the absence of a
// row rather than something a schema has to represent. That only works if an
// event about no application and no file is fully valid, so this pins it.
func TestAPresenceObservationCarriesItsReadingAndIsValid(t *testing.T) {
	start := time.Date(2026, 7, 14, 9, 1, 12, 0, time.UTC)
	end := start.Add(48 * time.Second)

	lit := source.NewInterval(start, end, source.Identity{}, "knowledgec-backlight")
	lit.State = "on"
	dark := source.NewInterval(start, end, source.Identity{}, "knowledgec-backlight")
	dark.State = "off"

	for _, e := range []source.Event{lit, dark} {
		if err := e.Validate(); err != nil {
			t.Errorf("presence observation with state %q is invalid: %v", e.State, err)
		}
	}
	if lit.State == dark.State {
		t.Fatal("the two readings compare equal, so nothing distinguishes them")
	}

	// The reading is not part of Identity. Identity is compared by struct
	// equality to join a document seen in two places, and a scalar reading has
	// nothing to do with what an event is about.
	if lit.Identity != dark.Identity {
		t.Error("state leaked into Identity; two readings of one thing are about the same thing")
	}
}
