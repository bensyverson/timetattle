package source_test

import (
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

func point(t *testing.T) source.Event {
	t.Helper()
	return source.NewPoint(time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC),
		source.Identity{Document: "record-1"}, "claude-transcripts")
}

// The payload is what a source knows about an observation that no column on the
// event describes. It is not what the observation is ABOUT, so it must not
// reach Identity: two records of one session are not two readings of one thing,
// and a join by Identity would be broken by a figure that differs per record.
func TestAPayloadIsNotPartOfWhatAnEventIsAbout(t *testing.T) {
	first, second := point(t), point(t)
	first.Payload = `{"session":"s-1","tokens":{"input":2}}`
	second.Payload = `{"session":"s-1","tokens":{"input":9999}}`

	if first.Identity != second.Identity {
		t.Fatal("a payload reached Identity; two records that differ only in what the model spent " +
			"are about the same thing, and Identity is compared by struct equality to say so")
	}
	for _, e := range []source.Event{first, second} {
		if err := e.Validate(); err != nil {
			t.Fatalf("an event carrying a payload is invalid: %v", err)
		}
	}
}

func TestAnEventWithNoPayloadIsOrdinary(t *testing.T) {
	e := point(t)
	if e.Payload != "" {
		t.Fatalf("a fresh event carries the payload %q; nothing was recorded is the empty string", e.Payload)
	}
	if err := e.Validate(); err != nil {
		t.Fatalf("an event with no payload is invalid: %v", err)
	}
}

// A payload that is not a JSON object cannot be read back by json_extract: the
// store's roll-up would return NULL for every path and report a source that
// recorded nothing at all. The registry drops one invalid event rather than
// losing a source's whole harvest, so refusing it here is the cheap place.
func TestAPayloadThatIsNotAJSONObjectIsRefused(t *testing.T) {
	for _, payload := range []string{
		"not json at all",
		`[1, 2, 3]`,
		`"a bare string"`,
		`7`,
		`{"unterminated": `,
	} {
		e := point(t)
		e.Payload = payload
		err := e.Validate()
		if err == nil {
			t.Errorf("payload %q was accepted; a payload that is not a JSON object reads back as "+
				"nothing recorded rather than as a fault", payload)
			continue
		}
		if !strings.Contains(err.Error(), "claude-transcripts") {
			t.Errorf("the refusal of %q does not name the source that produced it: %v", payload, err)
		}
	}
}
