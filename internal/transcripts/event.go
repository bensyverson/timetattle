package transcripts

import (
	"encoding/json"
	"time"

	"github.com/bensyverson/timetattle/internal/usage"
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
	SessionID string         `json:"sessionId"`
	Message   *recordMessage `json:"message"`
}

// recordMessage is the part of a record's message this package inspects: the
// content, to tell a human turn from a tool result relayed back to the model as
// a "user"-role turn, and the usage the model reported. Content is left raw
// because it takes two different JSON shapes in real transcripts: a bare string
// for most human-typed prompts, and an array of typed content blocks everywhere
// else.
type recordMessage struct {
	Content json.RawMessage `json:"content"`
	Usage   *recordUsage    `json:"usage"`
}

// recordUsage is the four figures of a record's usage object this package
// keeps, out of the ten a real one carries.
//
// The pointers are the point. Every one of the 402,803 records carrying a usage
// object in this machine's corpus on 2026-08-07 stated all four (measured with
// scripts/sessioncensus), several of them explicitly zero — and an explicit
// zero is an observation, while a figure the record never mentioned is not. A
// plain int64 cannot tell those apart, and would publish "read nothing from
// cache" for a record that said nothing about cache.
//
// What is deliberately NOT read: server_tool_use, service_tier, inference_geo,
// speed, and the per-iteration breakdown. The outer figures are taken exactly
// as the record states them rather than recomputed from `iterations`, which is
// not always their sum — 199 of 164,455 records carrying iterations disagree
// with their own outer figures (same measurement) — and the outer figure is the
// one the record asserts about itself.
type recordUsage struct {
	Input         *int64 `json:"input_tokens"`
	Output        *int64 `json:"output_tokens"`
	CacheRead     *int64 `json:"cache_read_input_tokens"`
	CacheCreation *int64 `json:"cache_creation_input_tokens"`
}

// payload is what this record says about itself beyond when it happened: the
// session it belonged to, and what the model spent on it.
//
// It returns the empty record where there is nothing to say, which the store
// writes as no payload at all. A record naming no session and reporting no
// usage is not an error and is still an event: that a record exists at an
// instant is the observation, and the payload is what rides along with it.
func (rec record) payload() usage.Record {
	out := usage.Record{Session: rec.SessionID}
	if rec.Message == nil || rec.Message.Usage == nil {
		return out
	}
	u := *rec.Message.Usage
	if u.Input == nil && u.Output == nil && u.CacheRead == nil && u.CacheCreation == nil {
		// A usage object that states none of the four figures records
		// nothing about tokens. An empty tokens object would assert that it
		// did.
		return out
	}
	out.Tokens = &usage.Tokens{
		Input:         u.Input,
		Output:        u.Output,
		CacheRead:     u.CacheRead,
		CacheCreation: u.CacheCreation,
	}
	return out
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
