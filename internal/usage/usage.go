// Package usage is the vocabulary of an event payload: what a Claude Code
// transcript record says about itself beyond when it happened.
//
// Two facts travel here, and they are the two Ben asked for on 2026-08-07 —
// which SESSION a record belonged to, and what the model SPENT on it. Dollar
// cost deliberately does not: no transcript carries one, and deriving it would
// mean multiplying these figures by a pricing table somebody has to maintain,
// which is a different kind of claim from an observation.
//
// # Why this is a package of its own
//
// Exactly two places need to agree about the shape: internal/transcripts
// writes it, and internal/store's usage roll-up reads it back with SQL. Nothing
// between them is in a position to notice a disagreement — a key renamed on one
// side and not the other returns NULL from json_extract, which SUMs to nothing
// and reports a store that recorded no tokens at all. The keys and the JSON
// paths therefore live in one file, the paths are exported for the SQL to use
// rather than retyped there, and TestTheDeclaredJSONPathsMatchTheKeysTheEncoderWrites
// holds the two together.
//
// # Why the figures are pointers
//
// An absent figure and a zero figure are different facts, and this project has
// shipped the collapse of the two three times. Every one of the 402,803 records
// carrying a usage object in this machine's own corpus on 2026-08-07 stated all
// four figures, several of them explicitly zero (an assistant turn that read
// nothing from cache states cache_read_input_tokens: 0, which is an
// observation). A plain int64 would encode both of those as 0 and publish "no
// cache tokens" for a record that never mentioned cache tokens at all.
//
// # Why the payload is JSON text rather than columns
//
// See migration 0014. In short: event.payload is opaque to the store, it sits
// outside event_identity, and the alternative — token columns on the event
// table — would make a source-agnostic schema carry one collector's vocabulary.
package usage

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// The JSON paths the store's SQL extracts. They are declared here, beside the
// struct tags that produce them, because they are the same fact written twice
// and only one of the two is checked by the Go compiler.
const (
	// PathSession is the session a record belonged to.
	PathSession = "$.session"
	// PathInput is the tokens sent that the model had not seen before.
	PathInput = "$.tokens.input"
	// PathOutput is the tokens the model generated.
	PathOutput = "$.tokens.output"
	// PathCacheRead is the tokens served from the prompt cache.
	PathCacheRead = "$.tokens.cache_read"
	// PathCacheCreation is the tokens written into the prompt cache.
	PathCacheCreation = "$.tokens.cache_creation"
)

// Record is one transcript record's payload.
//
// It is the whole of what a payload may say today. It is deliberately small:
// the transcripts hold message bodies, tool arguments and tool results, and
// none of that belongs in an event store whose subject is WHEN work happened.
type Record struct {
	// Session is the conversation the record belonged to, taken from the
	// record's own sessionId field.
	//
	// It is stored per record rather than once per file, and that is not
	// redundancy. A session is not confined to one working directory —
	// measured 2026-08-07 with scripts/sessioncensus, 286 of 1,590 sessions
	// carry records from more than one — so "which sessions touched this
	// project" is only answerable from the records that resolved to it.
	Session string `json:"session,omitempty"`
	// Tokens is what the model spent on this record, and is nil for the
	// records that spend nothing: a human turn, a tool result, a hook.
	Tokens *Tokens `json:"tokens,omitempty"`
}

// Tokens is a record's usage figures, named as this project names them and
// mapped from the transcript's own longer field names:
//
//	input          <- input_tokens
//	output         <- output_tokens
//	cache_read     <- cache_read_input_tokens
//	cache_creation <- cache_creation_input_tokens
//
// The four are kept separate and are never added together here. They are not
// interchangeable — cache_read is the cheapest token there is and cache_creation
// the dearest, and on this machine's corpus cache_read outweighs output tokens
// 224 to 1 (45,254,724,396 against 202,300,176, scripts/sessioncensus,
// 2026-08-07). A single "tokens" total would be dominated by the cheapest
// figure in it and would mean nothing.
type Tokens struct {
	Input         *int64 `json:"input,omitempty"`
	Output        *int64 `json:"output,omitempty"`
	CacheRead     *int64 `json:"cache_read,omitempty"`
	CacheCreation *int64 `json:"cache_creation,omitempty"`
}

// Stated returns a figure a record actually stated, including zero.
//
// It exists so that a caller writes usage.Stated(0) where the source said zero
// and leaves the field nil where the source said nothing, and so that the
// difference is visible at the call site rather than hidden in a pointer
// literal.
//
// It is deliberately NOT marked //go:fix inline. Inlining it spells every
// figure new(n), which is the same pointer and none of the meaning: the point
// of the name is that a reader of the call site can see that the source SAID
// this, zero included.
func Stated(n int64) *int64 { return new(n) }

// Encode renders a record as the payload text an event carries.
//
// A record that says nothing encodes to the empty string, which the store
// writes as NULL. That is the whole "nothing was recorded" case: an empty JSON
// object would be a payload asserting emptiness, and there is no such
// observation.
func (r Record) Encode() (string, error) {
	if r.Session == "" && r.Tokens == nil {
		return "", nil
	}
	encoded, err := json.Marshal(r)
	if err != nil {
		return "", fmt.Errorf("encode the payload for session %q: %w", r.Session, err)
	}
	return string(encoded), nil
}

// Decode reads a payload back.
//
// An absent payload decodes to an empty record and no error: a row that carries
// none is a row that recorded nothing, which is ordinary. A payload that is
// present and unreadable is an error, because the alternative is a fault
// reported as silence.
func Decode(payload string) (Record, error) {
	if payload == "" {
		return Record{}, nil
	}
	var r Record
	if err := json.Unmarshal([]byte(payload), &r); err != nil {
		return Record{}, fmt.Errorf("read the payload %s: %w", truncate(payload), err)
	}
	return r, nil
}

// truncate keeps an unreadable payload out of an error message at full length.
// A transcript payload is short, but the error path is reached by definition
// when the content is not what this package expects.
func truncate(payload string) string {
	const limit = 120
	if len(payload) <= limit {
		return strconv.Quote(payload)
	}
	return strconv.Quote(payload[:limit]) + "..."
}
