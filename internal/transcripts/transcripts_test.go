package transcripts

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// writeFile writes a jsonl transcript from a slice of already-encoded lines,
// creating any parent directories that do not exist.
func writeFile(t *testing.T, path string, lines []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	var data strings.Builder
	for _, l := range lines {
		data.WriteString(l + "\n")
	}
	if err := os.WriteFile(path, []byte(data.String()), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}

// userLine builds a raw JSONL line for a "user" record. content is embedded
// verbatim as JSON (a quoted string, or a `[...]` array literal) so tests can
// exercise both content shapes real transcripts use.
func userLine(uuid, ts, cwd, sessionID, contentJSON string) string {
	rec := map[string]any{
		"type":      "user",
		"timestamp": ts,
		"cwd":       cwd,
		"sessionId": sessionID,
		"uuid":      uuid,
	}
	b, _ := json.Marshal(rec)
	// Splice the content in verbatim rather than through the map, so a caller
	// can pass a bare JSON string or array without double-encoding it.
	var m map[string]json.RawMessage
	_ = json.Unmarshal(b, &m)
	msg := map[string]json.RawMessage{
		"role":    json.RawMessage(`"user"`),
		"content": json.RawMessage(contentJSON),
	}
	msgB, _ := json.Marshal(msg)
	m["message"] = msgB
	out, _ := json.Marshal(m)
	return string(out)
}

func assistantLine(uuid, ts, cwd, sessionID string) string {
	rec := map[string]any{
		"type":      "assistant",
		"timestamp": ts,
		"cwd":       cwd,
		"sessionId": sessionID,
		"uuid":      uuid,
		"message":   map[string]any{"role": "assistant"},
	}
	b, _ := json.Marshal(rec)
	return string(b)
}

// TestRegisteredReaderResolvesHomeAtCollectTimeNotInit guards against a real
// bug: init runs once per test binary, before any test's t.Setenv("HOME", ...)
// takes effect, so a root resolved eagerly at construction and stored on the
// struct is permanently the real machine's ~/.claude/projects no matter what
// a later test does. Every cmd/timetattle test that exercised the full
// harvest pipeline was silently parsing this developer's actual, multi-GB
// transcript corpus as a result — the dominant cost in that package's test
// suite — and not what any of those tests thought they were isolating. The
// fix is that the registered default instance stores no root at all and
// resolves fresh inside Collect, so a HOME redirected mid-test is honoured.
func TestRegisteredReaderResolvesHomeAtCollectTimeNotInit(t *testing.T) {
	var registered *Reader
	for _, s := range source.Sources() {
		if r, ok := s.(*Reader); ok {
			registered = r
			break
		}
	}
	if registered == nil {
		t.Skip("the source registry holds no *Reader; init registers one only when DefaultRoot() " +
			"resolves at process start")
	}
	if registered.root != "" {
		t.Fatalf("the registered default instance stores a fixed root (%q); "+
			"it must resolve lazily so a redirected HOME is honoured", registered.root)
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, ".claude", "projects")
	got, err := registered.resolveRoot()
	if err != nil {
		t.Fatalf("resolveRoot: %v", err)
	}
	if got != want {
		t.Errorf("resolveRoot() = %q after redirecting HOME, want %q; "+
			"the registered source is not respecting a post-init HOME change", got, want)
	}
}

func TestCollectEmitsAPointEventPerTimedRecord(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"do the thing"`),
		assistantLine("a1", "2026-07-28T10:00:05.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	for _, e := range events {
		if e.Shape != source.Point {
			t.Errorf("event %+v has shape %q, want point", e, e.Shape)
		}
		if e.Start.IsZero() || !e.Start.Equal(e.End) {
			t.Errorf("event %+v is not a well-formed point", e)
		}
		if e.Source != ID {
			t.Errorf("event %+v has source %q, want %q", e, e.Source, ID)
		}
		if e.Locator != "/Users/you/git/acorn" {
			t.Errorf("event %+v has locator %q, want the record's own cwd", e, e.Locator)
		}
		if err := e.Validate(); err != nil {
			t.Errorf("event %+v does not validate: %v", e, err)
		}
	}
}

func TestAttributionUsesEachRecordsOwnCWDNotTheDirectoryName(t *testing.T) {
	root := t.TempDir()
	// The project directory name is the dash-encoded form of an entirely
	// different path than the cwd the record actually carries -- e.g. this
	// project directory would naively decode to "/Users/you/git/other", but
	// every record says the real work happened in a path containing a dash of
	// its own, which the naive decode could never reconstruct.
	writeFile(t, filepath.Join(root, "-Users-ben-git-other", "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/dark-crystal-v2", "s1", `"hello"`),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].Locator != "/Users/you/git/dark-crystal-v2" {
		t.Errorf("locator = %q, want the record's own cwd (a path the directory name cannot decode to)", events[0].Locator)
	}
}

func TestSubagentTranscriptsAreCollected(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "-Users-ben-git-acorn")
	writeFile(t, filepath.Join(proj, "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"start work"`),
	})
	writeFile(t, filepath.Join(proj, "s1", "subagents", "agent-1.jsonl"), []string{
		userLine("sub1", "2026-07-28T10:00:01.000Z", "/Users/you/git/acorn", "s1", `"dispatched task"`),
		assistantLine("sub2", "2026-07-28T10:00:02.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3 (1 main + 2 subagent) -- a subagent file was dropped", len(events))
	}
}

func TestSubagentEventsFoldIntoTheirParentsLocatorNotASeparateOne(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "-Users-ben-git-acorn")
	writeFile(t, filepath.Join(proj, "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"start work"`),
	})
	writeFile(t, filepath.Join(proj, "s1", "subagents", "agent-1.jsonl"), []string{
		assistantLine("sub1", "2026-07-28T10:00:01.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	locators := map[string]bool{}
	for _, e := range events {
		locators[e.Locator] = true
	}
	if len(locators) != 1 {
		t.Fatalf("subagent and parent events attribute to %d distinct locators, want 1 (folded): %v", len(locators), locators)
	}
}

func TestSubagentDispatchPromptIsNotCountedAsAHumanTurn(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "-Users-ben-git-acorn")
	writeFile(t, filepath.Join(proj, "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"real human prompt"`),
	})
	// A subagent's first record is a plain-string "user" message too -- it is
	// the orchestrating agent's dispatch, not a human at a keyboard, and the
	// only signal that tells the two apart is the subagents/ path.
	writeFile(t, filepath.Join(proj, "s1", "subagents", "agent-1.jsonl"), []string{
		userLine("sub1", "2026-07-28T10:00:01.000Z", "/Users/you/git/acorn", "s1", `"go do this task unattended"`),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	byDoc := map[string]source.Event{}
	for _, e := range events {
		byDoc[e.Identity.Document] = e
	}
	if byDoc["u1"].State != StateHuman {
		t.Errorf("main-session user record state = %q, want %q", byDoc["u1"].State, StateHuman)
	}
	if byDoc["sub1"].State != StateMachine {
		t.Errorf("subagent dispatch record state = %q, want %q (a human did not type this)", byDoc["sub1"].State, StateMachine)
	}
}

func TestToolResultFedBackAsAUserRecordIsMachineNotHuman(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1",
			`[{"tool_use_id":"t1","type":"tool_result","content":[{"type":"text","text":"ok"}]}]`),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1", len(events))
	}
	if events[0].State != StateMachine {
		t.Errorf("state = %q, want %q -- a tool_result is the harness feeding a result back to the model, not a person typing",
			events[0].State, StateMachine)
	}
}

func TestUserTextBlockContentIsAHumanTurn(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1",
			`[{"type":"text","text":"can you explain this"}]`),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 || events[0].State != StateHuman {
		t.Fatalf("got %+v, want a single human-state event", events)
	}
}

func TestMixedToolResultAndTextContentIsStillMachine(t *testing.T) {
	// Real transcripts append a "Tool loaded." text block after a tool_result
	// when a skill or tool is auto-loaded -- still not a human turn, because
	// the presence of any tool_result block means the harness produced it.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		userLine("u1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1",
			`[{"tool_use_id":"t1","type":"tool_result","content":"..."},{"type":"text","text":"Tool loaded."}]`),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 || events[0].State != StateMachine {
		t.Fatalf("got %+v, want a single machine-state event", events)
	}
}

func TestAssistantRecordsAreMachine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		assistantLine("a1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 || events[0].State != StateMachine {
		t.Fatalf("got %+v, want a single machine-state event", events)
	}
}

func TestRecordsWithNoTimestampAreSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		`{"type":"last-prompt","sessionId":"s1"}`,
		assistantLine("a1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 -- the untimed bookkeeping record should not become an event", len(events))
	}
}

func TestRecordsWithNoUUIDAreSkipped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-07-28T10:00:00.000Z","sessionId":"s1"}`,
		assistantLine("a1", "2026-07-28T10:00:01.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 -- a record with no uuid has no safe dedup key and should be skipped", len(events))
	}
}

func TestRecordsWithNoCWDAreUnattributedNotDropped(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		assistantLine("a1", "2026-07-28T10:00:00.000Z", "", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 -- absence of cwd is not absence of the observation", len(events))
	}
	if events[0].Locator != "" {
		t.Errorf("locator = %q, want empty (unattributed)", events[0].Locator)
	}
	if err := events[0].Validate(); err != nil {
		t.Errorf("event should still validate: %v", err)
	}
}

func TestMalformedLineIsSkippedNotFatal(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		`{not valid json`,
		assistantLine("a1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 -- one corrupt line should not lose the rest of the file", len(events))
	}
}

func TestOutOfOrderRecordsAreSortedOnOutput(t *testing.T) {
	root := t.TempDir()
	// Written out of chronological order, exactly as real transcripts are.
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		assistantLine("a2", "2026-07-28T10:00:10.000Z", "/Users/you/git/acorn", "s1"),
		assistantLine("a1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1"),
		assistantLine("a3", "2026-07-28T10:00:20.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if !sort.SliceIsSorted(events, func(i, j int) bool { return events[i].Anchor().Before(events[j].Anchor()) }) {
		t.Errorf("events are not sorted by time: %+v", events)
	}
}

func TestSinceFiltersOutEventsThatFinishedBeforeIt(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		assistantLine("a1", "2026-07-28T09:00:00.000Z", "/Users/you/git/acorn", "s1"),
		assistantLine("a2", "2026-07-28T11:00:00.000Z", "/Users/you/git/acorn", "s1"),
	})

	since := time.Date(2026, 7, 28, 10, 0, 0, 0, time.UTC)
	events, err := NewReader(root).Collect(context.Background(), since)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 (only the record at or after since)", len(events))
	}
	if events[0].Identity.Document != "a2" {
		t.Errorf("kept the wrong event: %+v", events[0])
	}
}

func TestMissingRootIsNotAnError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "does-not-exist")
	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v, want nil -- a machine with no transcripts looks like this", err)
	}
	if len(events) != 0 {
		t.Errorf("got %d events from a directory that does not exist", len(events))
	}
}

func TestOneUnreadableFileDoesNotLoseTheOthers(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "-Users-ben-git-acorn", "s1.jsonl"), []string{
		assistantLine("a1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1"),
	})
	bad := filepath.Join(root, "-Users-ben-git-other", "s2.jsonl")
	writeFile(t, bad, []string{assistantLine("a2", "2026-07-28T10:00:00.000Z", "/Users/you/git/other", "s2")})
	if err := os.Chmod(bad, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(bad, 0o644)

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v, want nil -- one unreadable file should not fail the whole harvest", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events, want 1 from the readable file", len(events))
	}
}

func TestCompactionReplayOfAHumanTurnStaysHuman(t *testing.T) {
	// Real shape, measured against this machine's own corpus: Claude Code's
	// /compact mechanism spins up an internal subagent and replays the
	// preceding conversation into it -- same uuid, same timestamp, same
	// content -- as context before the summary. The replayed copy sits in a
	// subagents/ file and is machine-shaped by that rule alone, even though
	// the original was a real human prompt. The one event that survives for
	// this uuid must side with the human reading, not the replay artifact.
	root := t.TempDir()
	proj := filepath.Join(root, "-Users-ben-git-acorn")
	writeFile(t, filepath.Join(proj, "s1.jsonl"), []string{
		userLine("h1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"implement the thing"`),
	})
	writeFile(t, filepath.Join(proj, "s1", "subagents", "agent-acompact-1.jsonl"), []string{
		// Identical uuid and timestamp to the record above -- the replay.
		userLine("h1", "2026-07-28T10:00:00.000Z", "/Users/you/git/acorn", "s1", `"implement the thing"`),
		assistantLine("c1", "2026-07-28T10:00:05.000Z", "/Users/you/git/acorn", "s1"),
	})

	events, err := NewReader(root).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var forH1 []source.Event
	for _, e := range events {
		if e.Identity.Document == "h1" {
			forH1 = append(forH1, e)
		}
	}
	if len(forH1) != 1 {
		t.Fatalf("got %d events for the replayed uuid, want exactly 1 (deduped): %+v", len(forH1), forH1)
	}
	if forH1[0].State != StateHuman {
		t.Errorf("state = %q, want %q -- the human reading must survive the replay", forH1[0].State, StateHuman)
	}
	// The compaction subagent's own new record (c1) is untouched.
	found := false
	for _, e := range events {
		if e.Identity.Document == "c1" {
			found = true
		}
	}
	if !found {
		t.Error("the compaction subagent's own new record was lost")
	}
}

func TestIDAndKind(t *testing.T) {
	r := NewReader(t.TempDir())
	if r.ID() != ID {
		t.Errorf("ID() = %q, want %q", r.ID(), ID)
	}
	if r.Kind() != Kind {
		t.Errorf("Kind() = %q, want %q", r.Kind(), Kind)
	}
}
