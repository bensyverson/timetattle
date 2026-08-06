package transcripts

import (
	"context"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// testdata/realcorpus is a small, hand-anonymized fixture corpus derived from
// the *shape* of real transcript files on Ben's machine (two real
// agent-aside_question-*.jsonl files under ~/.claude/projects, inspected for
// record type sequences, content shapes and field names -- never for prompt
// or reply text, none of which is reproduced here). It exists beside
// transcripts_test.go's hand-constructed fixtures, not instead of them: those
// already exercise subagent folding, tool_result classification and
// compaction replay-dedup against the developer's own model of the format.
// This corpus exists to check that model against structure actually pulled
// from real files, and to close one gap the existing suite had: nothing
// exercised agent-aside_question-*.jsonl at all before this file.
//
// What real inspection found, and what it changed here:
//
//   - Every record in a subagents/ file really does carry isSidechain: true,
//     and every top-level record really does carry isSidechain: false --
//     consistent with the package doc's claim that path, not this field, is
//     what the reader trusts. Recorded here as flavor; the reader does not
//     parse the field either way, so this is not itself a new assertion.
//   - A real tool_result content block's keys are exactly
//     {content, is_error, tool_use_id, type} -- matching what
//     transcripts_test.go already assumed.
//   - The genuine gap: a real aside_question file does not only replay the
//     human turn that opens the conversation. Walking one such file found
//     four replayed uuids, each matching a real human turn in the parent
//     session by uuid and timestamp, sitting at file positions 0, 1, 3 and
//     47 of a 49-record file -- interleaved with the subagent's own
//     thinking/tool_use/tool_result activity, not clustered at the top.
//     dedupeReplays keys on (Document, Start) and does not care where in the
//     file a candidate sits, so nothing breaks today -- but nothing before
//     this file proved that, and a future optimisation that only checked a
//     subagent file's first record for a replay would pass every existing
//     test while silently losing this. TestAsideQuestionReplayMidFileStaysHuman
//     is built to catch exactly that regression: session-1/subagents/
//     agent-aside_question-1.jsonl replays h2 at its own first record and h3
//     at its second-to-last, exactly the two file positions real inspection
//     found a replay at.
//
// Anonymization: every uuid, cwd, session id and agentId here is invented,
// not copied from the real files (uuids are opaque random identifiers with
// no need to match; reusing Ben's real ones would add re-identification risk
// for zero fidelity benefit). Every message body is a placeholder string.
// Timestamps are an arbitrary fixture morning, not the real April dates.
func realCorpusRoot() string { return "testdata/realcorpus" }

func collectRealCorpus(t *testing.T) []source.Event {
	t.Helper()
	events, err := NewReader(realCorpusRoot()).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return events
}

func byDocument(events []source.Event) map[string]source.Event {
	out := make(map[string]source.Event, len(events))
	for _, e := range events {
		out[e.Identity.Document] = e
	}
	return out
}

// TestRealCorpusFixtureCollects is the portable smoke test: every event
// produced from the fixture corpus must validate, and the corpus must
// produce a plausible mix, on any machine -- no ~/.claude/projects required.
func TestRealCorpusFixtureCollects(t *testing.T) {
	events := collectRealCorpus(t)
	if len(events) == 0 {
		t.Fatal("got no events from the fixture corpus")
	}
	var human, machine int
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Errorf("event %+v does not validate: %v", e, err)
		}
		switch e.State {
		case StateHuman:
			human++
		case StateMachine:
			machine++
		default:
			t.Errorf("event %+v has state %q", e, e.State)
		}
	}
	if human == 0 || machine == 0 {
		t.Errorf("got %d human, %d machine -- want a mix, the fixture includes both", human, machine)
	}
	// The fixture's four files hold 9 + 2 + 2 + 8 = 21 raw records, three of
	// which are replays deduped away (h1 in agent-acompact-1.jsonl, h2 and h3
	// in agent-aside_question-1.jsonl), leaving exactly 18 distinct
	// documents. This exact count is what catches a file being read only
	// partway -- h3's replay sits second-to-last in an 8-record subagent
	// file, so a bug that silently stopped reading a subagent file early
	// would still leave h3 correctly StateHuman (its untouched original in
	// session-1.jsonl is read in full either way) while quietly losing the
	// subagent's own later records, such as aq5. A human/machine mix check
	// alone would not notice that loss; a fixed total does.
	if len(events) != 18 {
		t.Errorf("got %d events, want exactly 18", len(events))
	}
	by := byDocument(events)
	for _, doc := range []string{"aq1", "aq2", "aq3", "aq4", "aq5"} {
		if _, ok := by[doc]; !ok {
			t.Errorf("missing event for %s -- the aside_question subagent's own record after h3's mid-file replay was lost", doc)
		}
	}
	t.Logf("%d events: %d human, %d machine", len(events), human, machine)
}

// TestAsideQuestionReplayMidFileStaysHuman is the real-data-derived
// regression this corpus exists for: h3 is replayed not at the start of
// agent-aside_question-1.jsonl but as its second-to-last record, after
// several rounds of the subagent's own tool use -- the shape real inspection
// found. It must still collapse to one StateHuman event, the same as h2's
// leading-position replay and h1's compaction replay.
func TestAsideQuestionReplayMidFileStaysHuman(t *testing.T) {
	by := byDocument(collectRealCorpus(t))

	for _, doc := range []string{"h1", "h2", "h3", "h4"} {
		e, ok := by[doc]
		if !ok {
			t.Fatalf("no event for %s", doc)
		}
		if e.State != StateHuman {
			t.Errorf("%s state = %q, want %q", doc, e.State, StateHuman)
		}
	}

	// h3's only occurrence in the raw data is StateHuman (session-1.jsonl) and
	// StateMachine (its mid-file replay in the aside_question subagent,
	// machine-shaped purely by being in a subagents/ file). Confirming there
	// is exactly one surviving event for it is what proves the mid-file
	// replay was actually deduped rather than, say, silently dropped for an
	// unrelated reason that happens to also leave one event behind.
	var forH3 int
	for _, e := range collectRealCorpus(t) {
		if e.Identity.Document == "h3" {
			forH3++
		}
	}
	if forH3 != 1 {
		t.Fatalf("got %d events for h3, want exactly 1 (deduped)", forH3)
	}
}

// TestOrdinaryTaskSubagentDispatchIsMachineNotHuman checks the fixture's
// non-replay subagent case: d1's dispatch prompt is plain-string content in a
// subagents/ file, the same shape a real human prompt takes, and must still
// classify as machine because nothing types it.
func TestOrdinaryTaskSubagentDispatchIsMachineNotHuman(t *testing.T) {
	by := byDocument(collectRealCorpus(t))
	d1, ok := by["d1"]
	if !ok {
		t.Fatal("no event for d1")
	}
	if d1.State != StateMachine {
		t.Errorf("d1 state = %q, want %q -- an unattended dispatch prompt is not a human turn", d1.State, StateMachine)
	}
}

// TestSubagentFoldsIntoParentLocatorInRealCorpus checks folding against the
// fixture corpus specifically, since transcripts_test.go's own version of
// this check uses a different, hand-built fixture.
func TestSubagentFoldsIntoParentLocatorInRealCorpus(t *testing.T) {
	events := collectRealCorpus(t)
	locators := map[string]bool{}
	for _, e := range events {
		locators[e.Locator] = true
	}
	if len(locators) != 1 {
		t.Errorf("fixture corpus events attribute to %d distinct locators, want 1 (folded): %v", len(locators), locators)
	}
}

// TestToolResultShapedRecordsAreMachineInRealCorpus checks both real-shaped
// tool_result records the fixture carries: tr1 in the main session and aq3
// inside the aside_question subagent.
func TestToolResultShapedRecordsAreMachineInRealCorpus(t *testing.T) {
	by := byDocument(collectRealCorpus(t))
	for _, doc := range []string{"tr1", "aq3"} {
		e, ok := by[doc]
		if !ok {
			t.Fatalf("no event for %s", doc)
		}
		if e.State != StateMachine {
			t.Errorf("%s state = %q, want %q -- a tool_result-shaped user record is never a human turn", doc, e.State, StateMachine)
		}
	}
}
