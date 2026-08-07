package store

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/usage"
)

// UsageFilter selects the observations a usage roll-up is computed over: a
// scope, and a period.
//
// From and To bound the period the same way store.Filter does — inclusively,
// by overlap, on whichever endpoint an event actually has — so that a roll-up
// and an event read of the same window are answering about the same rows.
type UsageFilter struct {
	From time.Time
	To   time.Time
	// Project is the scope. The empty string is every project AND the
	// events attributed to none, which is the honest total: a transcript
	// record in an unregistered directory is still a record.
	Project string
	// Source narrows to one source. Empty is all of them. Only
	// claude-transcripts writes payloads today, and naming it is how a
	// caller says so rather than assuming it.
	Source string
}

// Tokens is one figure's roll-up: how many observations stated it, and what
// they add up to.
//
// The count is not decoration and it is not the same as the total. A total of
// zero over 400 observations means four hundred records reported spending
// nothing; a total of zero over NO observations means nothing was ever
// recorded, and this project has shipped the collapse of those two three times.
// Only the pair distinguishes them.
type Tokens struct {
	// Records is how many observations stated this figure at all.
	Records int
	// Total is what those observations add up to. It is meaningless, rather
	// than zero, when Records is zero.
	Total int64
}

// Usage is what was spent within a scope and a period.
//
// The four figures are kept apart and never added together. They are not
// interchangeable — cache reads are the cheapest tokens there are and cache
// writes the dearest — and on this machine's own corpus cache reads outweigh
// output tokens 224 to 1, so a single "tokens" number would be that one figure
// wearing a general name.
type Usage struct {
	// Observations is how many stored events carried a payload at all. Zero
	// means NOTHING WAS RECORDED for this scope and period — not that
	// nothing was spent. Every caller that prints a figure from this struct
	// has to say which of the two it is looking at.
	Observations int
	// Sessions is how many DISTINCT Claude Code sessions those observations
	// belonged to.
	//
	// It is a count of sessions that TOUCHED the scope, and it does not
	// partition. A session is not confined to one project — 286 of 1,590
	// sessions in this machine's corpus carry records from more than one
	// working directory, measured 2026-08-07 with scripts/sessioncensus —
	// so a session that touched two projects is counted once in each, and
	// adding the per-project counts overshoots the whole. The alternative,
	// assigning each session to one owning project, would have to invent a
	// rule (first record? most records? most time?) and would then report a
	// project as untouched by a session that genuinely worked in it.
	//
	// The same applies to a period: a session running across midnight is
	// counted in both days it touched.
	Sessions int
	// The four figures, as the records stated them.
	Input         Tokens
	Output        Tokens
	CacheRead     Tokens
	CacheCreation Tokens
}

// Usage rolls up the payloads within a scope and a period.
//
// It reads canonical_event, the same view Events reads, so an observation a
// later harvest completed is counted once rather than twice. Rows carrying no
// payload are excluded rather than counted as zeroes — see Observations.
//
// What it does NOT do is attach money to any of this. No transcript carries a
// cost, so a dollar figure would be these counts multiplied by a pricing table
// somebody has to maintain, and a maintained table is not an observation.
func (s *Store) Usage(ctx context.Context, f UsageFilter) (Usage, error) {
	query, args := usageQuery(f)

	var out Usage
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(
		&out.Observations, &out.Sessions,
		&out.Input.Records, &out.Input.Total,
		&out.Output.Records, &out.Output.Total,
		&out.CacheRead.Records, &out.CacheRead.Total,
		&out.CacheCreation.Records, &out.CacheCreation.Total,
	); err != nil {
		return Usage{}, fmt.Errorf("roll up usage from %s: %w", s.path, err)
	}
	return out, nil
}

// usageQuery is the SQL of a roll-up and the arguments that go with it.
//
// It is one function rather than a string built at the call site for the reason
// eventsQuery is: the access path is a property of the TEXT, and a plan test
// asserting against a copy of the query would be asserting about the copy. See
// TestTheUsageRollUpBoundsThePeriodThroughAnIndexRatherThanAScan.
func usageQuery(f UsageFilter) (string, []any) {
	// payload IS NOT NULL is the whole population, and it is stated first
	// because every figure below is about rows that recorded something.
	where := []string{`payload IS NOT NULL`}
	var args []any
	if !f.From.IsZero() {
		// COALESCE, not ended_at: on an event seen only as it began, the
		// one endpoint there is serves as both. Spelled as store.events
		// spells it, so a roll-up and an event read agree about a window.
		where = append(where, `COALESCE(ended_at, started_at) >= ?`)
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		where = append(where, `COALESCE(started_at, ended_at) <= ?`)
		args = append(args, formatTime(f.To))
	}
	if f.Project != "" {
		where = append(where, `project = ?`)
		args = append(args, f.Project)
	}
	if f.Source != "" {
		where = append(where, `source_id = ?`)
		args = append(args, f.Source)
	}

	query := `SELECT COUNT(*), COUNT(DISTINCT ` + extract(usage.PathSession) + `), ` +
		figure(usage.PathInput) + `, ` +
		figure(usage.PathOutput) + `, ` +
		figure(usage.PathCacheRead) + `, ` +
		figure(usage.PathCacheCreation) +
		` FROM canonical_event WHERE ` + strings.Join(where, " AND ")
	return query, args
}

// extract is one JSON path lookup against the payload column.
//
// The path is interpolated rather than bound as a parameter, and that is safe
// for one reason worth naming: every path this package passes is a constant
// declared in internal/usage, none of them comes from a caller, and
// TestEveryJSONPathTheRollUpQueriesResolvesInAnEncodedPayload runs each one
// against a payload the encoder actually wrote. Interpolating keeps the
// argument list positional-by-filter alone, which is the part a later edit is
// likely to get wrong.
func extract(path string) string {
	return `json_extract(payload, '` + path + `')`
}

// figure is one token figure's pair of aggregates: how many rows stated it, and
// what they add up to.
//
// COALESCE around both because SUM over no rows is NULL, and a NULL scanned
// into an int is an error rather than a zero — so a scope with nothing recorded
// would fail the read instead of reporting, correctly, that nothing was
// recorded.
func figure(path string) string {
	return `COALESCE(SUM(` + extract(path) + ` IS NOT NULL), 0), COALESCE(SUM(` + extract(path) + `), 0)`
}
