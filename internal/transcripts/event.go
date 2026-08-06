package transcripts

import (
	"encoding/json"
	"time"
)

// State values this source writes to Event.State. See the package doc for
// why this lives on State rather than Cause.
const (
	// StateHuman marks a record a person typed: proof of attention, in the
	// same sense the task that created this package uses the phrase.
	StateHuman = "human"
	// StateMachine marks a record the assistant, a tool, or an unattended
	// subagent produced. It is not evidence anyone was at the machine.
	StateMachine = "machine"
)

// record is the narrow projection of a transcript line this package needs.
// The files hold far more (message bodies, tool payloads), but decoding only
// these fields keeps the parse honest about what an event actually requires.
type record struct {
	Type      string         `json:"type"`
	Timestamp time.Time      `json:"timestamp"`
	CWD       string         `json:"cwd"`
	UUID      string         `json:"uuid"`
	Message   *recordMessage `json:"message"`
}

// recordMessage is the part of a record's message this package inspects to
// tell a human turn from a tool result relayed back to the model as a
// "user"-role turn. Content is left raw because it takes two different JSON
// shapes in real transcripts: a bare string for most human-typed prompts,
// and an array of typed content blocks everywhere else.
type recordMessage struct {
	Content json.RawMessage `json:"content"`
}

// contentBlock is the one field this package needs from a content block:
// which kind it is, so a tool_result can be told apart from a text block.
type contentBlock struct {
	Type string `json:"type"`
}

// isHumanTurn reports whether rec is a person typing, as opposed to the
// model, a tool, or an unattended subagent.
//
// Three things all have to hold:
//
//   - rec is not from a subagents/ file. A subagent's own first record is a
//     "user" record with plain string content -- the same shape a real
//     human prompt takes -- but nothing types it: it is the orchestrating
//     agent's own dispatch, running unattended. Only the file's path says
//     so; see the package doc for why isSidechain cannot be trusted here.
//   - rec.Type is "user". Assistant and tool records are never human turns,
//     because an agent produces both unattended.
//   - the message content carries no tool_result block. The Anthropic
//     Messages API represents a tool's result as a "user"-role turn fed
//     back to the model, and in a 40-transcript, 3,510-record sample this
//     shape was 3,229 of the "user" records sampled -- the overwhelming
//     majority, not an edge case. A content array that mixes a tool_result
//     with a trailing text block ("Tool loaded.", written when a skill or
//     tool is auto-loaded) is still disqualified: the presence of any
//     tool_result block means the harness produced the turn, whatever else
//     rides along with it.
func isHumanTurn(rec record, isSubagent bool) bool {
	if isSubagent || rec.Type != "user" || rec.Message == nil {
		return false
	}
	content := rec.Message.Content
	if len(content) == 0 {
		return false
	}

	// A bare JSON string is the shape a literal typed prompt takes, and
	// carries no block structure to inspect for a tool_result.
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return true
	}

	var blocks []contentBlock
	if err := json.Unmarshal(content, &blocks); err != nil || len(blocks) == 0 {
		// A shape this package does not recognise is not confidently a
		// human turn -- claiming attention on a guess would be worse than
		// under-counting it.
		return false
	}
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return false
		}
	}
	return true
}
