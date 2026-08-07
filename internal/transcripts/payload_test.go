package transcripts

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/usage"
)

// assistantLineWithUsage is assistantLine carrying the usage object a real
// assistant record reports. The figures are passed as a map so a test can omit
// one, which is how "the record did not state it" is posed.
func assistantLineWithUsage(uuid, ts, cwd, sessionID string, reported map[string]any) string {
	rec := map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"cwd":       cwd,
		"sessionId": sessionID,
		"uuid":      uuid,
		"message":   map[string]any{"role": "assistant", "usage": reported},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// payloadOf decodes the payload of the one event whose Document is uuid.
func payloadOf(t *testing.T, events []source.Event, uuid string) usage.Record {
	t.Helper()
	for _, e := range events {
		if e.Identity.Document == uuid {
			rec, err := usage.Decode(e.Payload)
			if err != nil {
				t.Fatalf("the payload of %s cannot be read back: %v", uuid, err)
			}
			return rec
		}
	}
	t.Fatalf("no event for record %s among %d events", uuid, len(events))
	return usage.Record{}
}

func collect(t *testing.T, root string) []source.Event {
	t.Helper()
	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("collect from %s: %v", root, err)
	}
	return events
}

func TestEveryRecordCarriesTheSessionItBelongedTo(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		userLine("u1", "2026-08-07T09:00:00Z", "/work/project", "session-a", `"do the thing"`),
		assistantLine("u2", "2026-08-07T09:00:05Z", "/work/project", "session-a"),
	})

	events := collect(t, root)
	for _, uuid := range []string{"u1", "u2"} {
		if got := payloadOf(t, events, uuid).Session; got != "session-a" {
			t.Errorf("record %s reports session %q, want %q; the session is taken from the record's own "+
				"sessionId field, which every timed record in this format carries", uuid, got, "session-a")
		}
	}
}

func TestAnAssistantRecordCarriesTheFourFiguresItReported(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		assistantLineWithUsage("u1", "2026-08-07T09:00:05Z", "/work/project", "session-a", map[string]any{
			"input_tokens":                2,
			"output_tokens":               214,
			"cache_read_input_tokens":     0,
			"cache_creation_input_tokens": 46724,
			// Real records carry more than the four figures; the parse must
			// take what it needs and leave the rest.
			"service_tier": "standard",
			"cache_creation": map[string]any{
				"ephemeral_5m_input_tokens": 0,
				"ephemeral_1h_input_tokens": 46724,
			},
		}),
	})

	tokens := payloadOf(t, collect(t, root), "u1").Tokens
	if tokens == nil {
		t.Fatal("an assistant record reporting usage carries no tokens in its payload")
	}
	for _, c := range []struct {
		name string
		got  *int64
		want int64
	}{
		{"input", tokens.Input, 2},
		{"output", tokens.Output, 214},
		{"cache_read", tokens.CacheRead, 0},
		{"cache_creation", tokens.CacheCreation, 46724},
	} {
		if c.got == nil {
			t.Errorf("%s was not recorded, though the record stated it", c.name)
			continue
		}
		if *c.got != c.want {
			t.Errorf("%s came back as %d, want %d", c.name, *c.got, c.want)
		}
	}
}

// cache_creation_input_tokens is not a rounding error on this corpus: 1.31
// billion tokens against 202 million output tokens, measured 2026-08-07 with
// scripts/sessioncensus. A parse that took only the three figures named on the
// task would have dropped the second largest figure there is.
func TestTheCacheCreationFigureIsNotDropped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		assistantLineWithUsage("u1", "2026-08-07T09:00:05Z", "/work/project", "session-a", map[string]any{
			"cache_creation_input_tokens": 46724,
		}),
	})
	tokens := payloadOf(t, collect(t, root), "u1").Tokens
	if tokens == nil || tokens.CacheCreation == nil {
		t.Fatal("the cache creation figure was not recorded")
	}
	if *tokens.CacheCreation != 46724 {
		t.Fatalf("cache creation came back as %d, want 46724", *tokens.CacheCreation)
	}
}

// A figure the record never stated must stay unstated. Writing a zero for it
// would publish "the model read nothing from cache" for a record that said
// nothing about cache at all, which is the absence-is-not-an-observation rule
// this project has broken three times.
func TestAFigureTheRecordDidNotStateIsNotInvented(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		assistantLineWithUsage("u1", "2026-08-07T09:00:05Z", "/work/project", "session-a", map[string]any{
			"input_tokens": 2,
		}),
	})

	tokens := payloadOf(t, collect(t, root), "u1").Tokens
	if tokens == nil {
		t.Fatal("no tokens recorded for a record that stated one figure")
	}
	if tokens.Input == nil || *tokens.Input != 2 {
		t.Fatalf("input came back as %v, want 2", tokens.Input)
	}
	for name, figure := range map[string]*int64{
		"output":         tokens.Output,
		"cache_read":     tokens.CacheRead,
		"cache_creation": tokens.CacheCreation,
	} {
		if figure != nil {
			t.Errorf("%s was recorded as %d for a record that never stated it", name, *figure)
		}
	}
}

func TestARecordThatSpendsNothingCarriesASessionAndNoTokens(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		userLine("u1", "2026-08-07T09:00:00Z", "/work/project", "session-a", `"do the thing"`),
	})

	rec := payloadOf(t, collect(t, root), "u1")
	if rec.Session != "session-a" {
		t.Fatalf("session came back as %q", rec.Session)
	}
	if rec.Tokens != nil {
		t.Fatalf("a human turn reports tokens %+v; nothing was spent on it and nothing was recorded", rec.Tokens)
	}
}

// Measured 2026-08-07 with scripts/sessioncensus: all 830,438 timed records in
// this machine's corpus carry a sessionId. A record without one is therefore
// hypothetical, and it must still become an event — the observation that a
// record exists at an instant does not depend on the session being named.
func TestARecordWithNoSessionStillBecomesAnEventCarryingNothing(t *testing.T) {
	root := t.TempDir()
	line := map[string]any{
		"type":      "assistant",
		"timestamp": "2026-08-07T09:00:00Z",
		"cwd":       "/work/project",
		"uuid":      "u1",
		"message": map[string]any{
			"role":  "assistant",
			"usage": map[string]any{"output_tokens": 214},
		},
	}
	encoded, _ := json.Marshal(line)
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{string(encoded)})

	events := collect(t, root)
	if len(events) != 1 {
		t.Fatalf("collected %d events, want 1", len(events))
	}
	rec := payloadOf(t, events, "u1")
	if rec.Session != "" {
		t.Errorf("a record naming no session reports %q; an unnamed session is no session, not a guess", rec.Session)
	}
	// The figure it DID state survives. One fact missing does not discard the
	// other: that collapse is how a store comes to report nothing where it
	// observed something.
	if rec.Tokens == nil || rec.Tokens.Output == nil || *rec.Tokens.Output != 214 {
		t.Fatalf("the figure the record stated was lost: %+v", rec.Tokens)
	}
}

// A subagent's records carry the session they ran under rather than an identity
// of their own — measured 2026-08-07, all 304,982 subagent records in this
// machine's corpus carry the sessionId of the directory that owns them. Folding
// them into their parent session therefore needs no rule here, and this pins
// that the payload does not invent one.
func TestASubagentRecordCarriesTheSessionItRanUnder(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		userLine("u1", "2026-08-07T09:00:00Z", "/work/project", "session-a", `"do the thing"`),
	})
	writeFile(t, filepath.Join(root, "-work-project", "session-a", "subagents", "agent-1.jsonl"), []string{
		assistantLine("u2", "2026-08-07T09:01:00Z", "/work/project/sub", "session-a"),
	})

	events := collect(t, root)
	if got := payloadOf(t, events, "u2").Session; got != "session-a" {
		t.Fatalf("a subagent record reports session %q, want %q", got, "session-a")
	}
}

// A record replayed into a compaction subagent's file keeps its original uuid,
// timestamp AND session: measured 2026-08-07, 6,873 uuids appear in more than
// one file and not one of them carries two different session ids. So the
// conflict dedupeReplays resolves is about State alone, and whichever candidate
// it keeps reports the same session.
func TestAReplayedRecordResolvesToOneEventReportingOneSession(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-work-project", "session-a.jsonl"), []string{
		userLine("u1", "2026-08-07T09:00:00Z", "/work/project", "session-a", `"do the thing"`),
	})
	writeFile(t, filepath.Join(root, "-work-project", "session-a", "subagents", "agent-acompact-1.jsonl"), []string{
		userLine("u1", "2026-08-07T09:00:00Z", "/work/project", "session-a", `"do the thing"`),
	})

	events := collect(t, root)
	if len(events) != 1 {
		t.Fatalf("collected %d events for one replayed record, want 1", len(events))
	}
	if events[0].State != StateHuman {
		t.Fatalf("the surviving event reads %q; a human turn confirmed anywhere is real signal", events[0].State)
	}
	if got := payloadOf(t, events, "u1").Session; got != "session-a" {
		t.Fatalf("the surviving event reports session %q, want %q", got, "session-a")
	}
}
