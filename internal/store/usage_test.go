package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
	"github.com/bensyverson/timetattle/internal/transcripts"
	"github.com/bensyverson/timetattle/internal/usage"
)

const transcriptSource = "claude-transcripts"

// spent is a transcript-shaped observation: an instant, the record it is about,
// and a payload naming the session and what the model spent on it.
func spent(at time.Time, uuid, session string, tokens *usage.Tokens) source.Event {
	e := source.NewPoint(at, source.Identity{Document: uuid}, transcriptSource)
	e.State = "machine"
	payload, err := usage.Record{Session: session, Tokens: tokens}.Encode()
	if err != nil {
		panic(err)
	}
	e.Payload = payload
	return e
}

func tokens(in, out, cacheRead, cacheCreation int64) *usage.Tokens {
	return &usage.Tokens{
		Input:         new(in),
		Output:        new(out),
		CacheRead:     new(cacheRead),
		CacheCreation: new(cacheCreation),
	}
}

func instant(minute int) time.Time {
	return time.Date(2026, 8, 7, 9, minute, 0, 0, time.UTC)
}

// harvest writes one source's events through a fresh run, the way a real
// harvest does, and reports how many the store considered new.
func harvest(t *testing.T, f fixture, events []source.Event) int {
	t.Helper()
	ctx := context.Background()
	run, err := f.BeginRun(ctx, store.RunSpec{SourceID: transcriptSource, Input: "/transcripts"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	added, err := run.Write(ctx, events)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	return added
}

// attribute runs the resolution pass with a fixed locator-to-project map, the
// way a registry would.
func attribute(t *testing.T, f fixture, projects map[string]string) {
	t.Helper()
	_, err := f.ResolveProjects(context.Background(), func(locator string) (store.Attributed, bool, error) {
		project, ok := projects[locator]
		if !ok {
			return store.Attributed{}, false, nil
		}
		return store.Attributed{Client: "a-client", Project: project}, true, nil
	})
	if err != nil {
		t.Fatalf("ResolveProjects: %v", err)
	}
}

// payloadDigest is every row's identity and payload, in id order, as one
// string. Two digests that compare equal are two stores holding the same rows
// with the same payloads — which is what "nothing changed" has to mean.
func payloadDigest(t *testing.T, f fixture) string {
	t.Helper()
	rows, err := f.raw(t).Query(`SELECT id, source_id, document, state, started_at,
		COALESCE(payload, '<none>') FROM event ORDER BY id`)
	if err != nil {
		t.Fatalf("read the rows back: %v", err)
	}
	defer rows.Close()

	var out strings.Builder
	for rows.Next() {
		var (
			id                                     int64
			src, document, state, started, payload string
		)
		if err := rows.Scan(&id, &src, &document, &state, &started, &payload); err != nil {
			t.Fatalf("scan: %v", err)
		}
		fmt.Fprintf(&out, "%d|%s|%s|%s|%s|%s\n", id, src, document, state, started, payload)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("read the rows back: %v", err)
	}
	return out.String()
}

func countRows(t *testing.T, f fixture) int {
	t.Helper()
	var n int
	if err := f.raw(t).QueryRow(`SELECT COUNT(*) FROM event`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

// THE PROPERTY THIS WHOLE DESIGN EXISTS FOR. A transcript is re-read in full on
// every harvest — internal/transcripts has no watermark — so a payload that
// reached the dedup key would insert the entire corpus again every time.
func TestHarvestingTheSameEventsTwiceWithPayloadsChangesNothing(t *testing.T) {
	f := newStore(t)
	events := []source.Event{
		spent(instant(0), "u1", "session-a", nil),
		spent(instant(1), "u2", "session-a", tokens(2, 214, 0, 46724)),
		spent(instant(2), "u3", "session-b", tokens(5, 91, 12000, 0)),
	}

	if added := harvest(t, f, events); added != len(events) {
		t.Fatalf("the first harvest stored %d of %d events", added, len(events))
	}
	before, rowsBefore := payloadDigest(t, f), countRows(t, f)

	added := harvest(t, f, events)
	after, rowsAfter := payloadDigest(t, f), countRows(t, f)

	if added != 0 {
		t.Errorf("the second harvest of the same transcripts stored %d new events; re-reading is how this "+
			"source works, so a re-read must store nothing", added)
	}
	if rowsBefore != rowsAfter {
		t.Errorf("the event count went %d -> %d across two harvests of identical evidence", rowsBefore, rowsAfter)
	}
	if before != after {
		t.Errorf("the rows changed across two harvests of identical evidence:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// The same property END TO END, through the real parser rather than through
// events a test built: a transcript corpus on disk, collected and written
// twice, exactly as two harvests of an unchanged corpus would.
//
// It is not the same test as the one above and neither replaces the other. That
// one poses the events directly, so it says what the STORE does with a payload.
// This one goes through internal/transcripts, so it also says that the payload
// a re-parse produces is byte-identical to the first — a collector that encoded
// its JSON non-deterministically (a map, a set, an iteration order) would pass
// the first test and duplicate nothing here either, but would leave the two
// harvests disagreeing about what the record said.
func TestCollectingAndStoringOneTranscriptCorpusTwiceChangesNothing(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "-work-project")
	if err := os.MkdirAll(filepath.Join(project, "session-a", "subagents"), 0o755); err != nil {
		t.Fatalf("build the corpus: %v", err)
	}
	write := func(path string, lines ...string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	write(filepath.Join(project, "session-a.jsonl"),
		`{"type":"user","timestamp":"2026-08-07T09:00:00Z","cwd":"/work/project","sessionId":"session-a","uuid":"u1","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","timestamp":"2026-08-07T09:00:05Z","cwd":"/work/project","sessionId":"session-a","uuid":"u2","message":{"role":"assistant","usage":{"input_tokens":2,"output_tokens":214,"cache_read_input_tokens":0,"cache_creation_input_tokens":46724}}}`,
	)
	write(filepath.Join(project, "session-a", "subagents", "agent-1.jsonl"),
		`{"type":"assistant","timestamp":"2026-08-07T09:01:00Z","cwd":"/work/project/sub","sessionId":"session-a","uuid":"u3","message":{"role":"assistant","usage":{"input_tokens":5,"output_tokens":91,"cache_read_input_tokens":12000,"cache_creation_input_tokens":0}}}`,
	)
	// A replayed human turn, which is the case internal/transcripts resolves
	// before the store sees it. Harvesting twice must not turn it into two
	// rows either.
	write(filepath.Join(project, "session-a", "subagents", "agent-acompact-1.jsonl"),
		`{"type":"user","timestamp":"2026-08-07T09:00:00Z","cwd":"/work/project","sessionId":"session-a","uuid":"u1","message":{"role":"user","content":"do the thing"}}`,
	)

	ctx := context.Background()
	reader := transcripts.NewReader(root)
	f := newStore(t)

	collectAndWrite := func() int {
		t.Helper()
		events, err := reader.Collect(ctx, time.Time{})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("collected %d events from the corpus, want 3", len(events))
		}
		return harvest(t, f, events)
	}

	if added := collectAndWrite(); added != 3 {
		t.Fatalf("the first harvest stored %d of 3 events", added)
	}
	before := payloadDigest(t, f)

	if added := collectAndWrite(); added != 0 {
		t.Errorf("the second harvest of an unchanged corpus stored %d new events", added)
	}
	if after := payloadDigest(t, f); after != before {
		t.Errorf("the rows changed across two harvests of an unchanged corpus:\nbefore:\n%s\nafter:\n%s",
			before, after)
	}

	got, err := f.Usage(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Observations != 3 || got.Sessions != 1 {
		t.Errorf("the corpus rolled up to %d observations across %d sessions, want 3 and 1",
			got.Observations, got.Sessions)
	}
	if got.Output.Records != 2 || got.Output.Total != 305 {
		t.Errorf("output came back as %d records totalling %d, want 2 totalling 305",
			got.Output.Records, got.Output.Total)
	}
	// The human turn spent nothing, and that is a third state: it carries a
	// payload (its session) while stating no figure at all.
	if got.Observations-got.Output.Records != 1 {
		t.Errorf("%d observations state no output figure, want 1", got.Observations-got.Output.Records)
	}
}

// The measured form of the trap this task exists to navigate, posed as the
// naive design would pose it: the figure folded into State, which IS part of
// event_identity.
//
// The mechanism is not hypothetical. Two readings of ONE transcript record
// genuinely disagree about State on this corpus — 6,873 uuids appear in more
// than one file (scripts/sessioncensus, 2026-08-07), which is why
// internal/transcripts has a dedupeReplays rule at all. That rule works because
// State has two values and one of them is preferable; a token count has neither
// property, so a disagreement between two readings has nowhere to be resolved
// and lands in the store as a second observation of one record.
func TestAFigureCarriedInStateMakesASecondReadingASecondRow(t *testing.T) {
	naive := func(at time.Time, uuid string, output int64) source.Event {
		e := source.NewPoint(at, source.Identity{Document: uuid}, transcriptSource)
		e.State = fmt.Sprintf("machine tokens=%d", output)
		return e
	}

	f := newStore(t)
	harvest(t, f, []source.Event{naive(instant(0), "u1", 214)})
	harvest(t, f, []source.Event{naive(instant(0), "u1", 215)})
	if got := countRows(t, f); got != 2 {
		t.Fatalf("the naive design stored %d rows for one record read twice, want 2; if this is no longer 2 "+
			"the demonstration below has stopped demonstrating anything", got)
	}

	// The same two readings with the figure in the payload, where identity
	// cannot see it.
	g := newStore(t)
	harvest(t, g, []source.Event{spent(instant(0), "u1", "session-a", tokens(0, 214, 0, 0))})
	harvest(t, g, []source.Event{spent(instant(0), "u1", "session-a", tokens(0, 215, 0, 0))})
	if got := countRows(t, g); got != 1 {
		t.Fatalf("the payload design stored %d rows for one record read twice, want 1", got)
	}

	// And the row that is there is the FIRST reading, unrewritten. Events are
	// observations: a later read does not edit one, it stands beside it or it
	// is dropped by dedup.
	var payload string
	if err := g.raw(t).QueryRow(`SELECT payload FROM event`).Scan(&payload); err != nil {
		t.Fatalf("read the payload back: %v", err)
	}
	if !strings.Contains(payload, `"output":214`) {
		t.Fatalf("the stored payload is %s; the second reading rewrote the first, and an event is never rewritten", payload)
	}
}

// A row stored before this source emitted payloads carries none, for ever, and
// that is not a zero. The column must be NULL rather than an empty string, and
// the roll-up must report nothing observed rather than nothing spent.
func TestARowStoredWithoutAPayloadReadsAsNothingRecorded(t *testing.T) {
	f := newStore(t)
	plain := source.NewPoint(instant(0), source.Identity{Document: "u1"}, transcriptSource)
	plain.State = "machine"
	harvest(t, f, []source.Event{plain})

	var payload sql.NullString
	if err := f.raw(t).QueryRow(`SELECT payload FROM event`).Scan(&payload); err != nil {
		t.Fatalf("read the payload column: %v", err)
	}
	if payload.Valid {
		t.Fatalf("the payload column holds %q; an event with nothing extra to say must store NULL, because "+
			"an empty string is a payload that asserts emptiness", payload.String)
	}

	got, err := f.Usage(context.Background(), store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Observations != 0 {
		t.Errorf("the roll-up counted %d observations over a store with no payloads at all", got.Observations)
	}
	if got.Output.Records != 0 {
		t.Errorf("the roll-up counted %d output-token observations where nothing was recorded", got.Output.Records)
	}
}

func TestUsageCountsSessionsAndSumsTokensForAPeriod(t *testing.T) {
	f := newStore(t)
	harvest(t, f, []source.Event{
		spent(instant(0), "u1", "session-a", nil),
		spent(instant(1), "u2", "session-a", tokens(2, 214, 1000, 500)),
		spent(instant(2), "u3", "session-b", tokens(3, 100, 2000, 700)),
		spent(instant(30), "u4", "session-c", tokens(9, 9, 9, 9)),
	})

	got, err := f.Usage(context.Background(), store.UsageFilter{From: instant(0), To: instant(2)})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Observations != 3 {
		t.Errorf("counted %d observations in the window, want 3", got.Observations)
	}
	if got.Sessions != 2 {
		t.Errorf("counted %d sessions in the window, want 2; the fourth record is outside it", got.Sessions)
	}
	for _, c := range []struct {
		name  string
		got   store.Tokens
		count int
		total int64
	}{
		{"input", got.Input, 2, 5},
		{"output", got.Output, 2, 314},
		{"cache_read", got.CacheRead, 2, 3000},
		{"cache_creation", got.CacheCreation, 2, 1200},
	} {
		if c.got.Records != c.count || c.got.Total != c.total {
			t.Errorf("%s came back as %d records totalling %d, want %d totalling %d",
				c.name, c.got.Records, c.got.Total, c.count, c.total)
		}
	}
}

// What a session count for a SCOPE means, pinned. A session is not confined to
// one project — 286 of 1,590 sessions in this machine's corpus carry records
// from more than one working directory (scripts/sessioncensus, 2026-08-07) — so
// a session that touched two projects counts once in each, and the per-project
// counts do not partition the whole. Anything else would either lose the
// session from one project or invent a rule for which project owns it.
func TestASessionThatTouchedTwoProjectsCountsInBoth(t *testing.T) {
	f := newStore(t)
	first := spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0))
	first.Locator = "/work/quarry"
	second := spent(instant(1), "u2", "session-a", tokens(2, 20, 0, 0))
	second.Locator = "/work/lantern"
	harvest(t, f, []source.Event{first, second})
	attribute(t, f, map[string]string{"/work/quarry": "quarry", "/work/lantern": "lantern"})

	ctx := context.Background()
	for project, wantTotal := range map[string]int64{"quarry": 10, "lantern": 20} {
		got, err := f.Usage(ctx, store.UsageFilter{Project: project})
		if err != nil {
			t.Fatalf("Usage for %s: %v", project, err)
		}
		if got.Sessions != 1 {
			t.Errorf("%s counted %d sessions, want 1", project, got.Sessions)
		}
		if got.Output.Total != wantTotal {
			t.Errorf("%s totalled %d output tokens, want %d", project, got.Output.Total, wantTotal)
		}
	}

	whole, err := f.Usage(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage over everything: %v", err)
	}
	if whole.Sessions != 1 {
		t.Fatalf("the store counted %d sessions in total, want 1; one session touching two projects is one "+
			"session, and the per-project counts sum to more than it on purpose", whole.Sessions)
	}
}

// The roll-up reads the canonical view, so an observation a later harvest
// completed is counted once rather than twice. Transcript records are points
// and are never superseded today; the rule is structural because a rule saying
// "remember to collapse" gets forgotten, and the failure is a silently doubled
// figure.
func TestUsageDoesNotCountASupersededObservationTwice(t *testing.T) {
	f := newStore(t)
	horizon := source.NewOpenEnd(instant(0), source.UnknownHorizon,
		source.Identity{App: "com.example.forge"}, transcriptSource)
	horizon.Payload = `{"session":"session-a","tokens":{"output":10}}`
	complete := source.NewInterval(instant(0), instant(5),
		source.Identity{App: "com.example.forge"}, transcriptSource)
	complete.Payload = `{"session":"session-a","tokens":{"output":10}}`
	harvest(t, f, []source.Event{horizon, complete})

	got, err := f.Usage(context.Background(), store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Observations != 1 || got.Output.Total != 10 {
		t.Fatalf("counted %d observations totalling %d output tokens; the horizon and the interval that "+
			"completed it are one observation at two completenesses", got.Observations, got.Output.Total)
	}
}

// The Go struct tags and the SQL paths are the same fact written twice, and
// only one of them is checked by a compiler. A path that names a key the
// encoder does not write extracts NULL from every row, which SUMs to nothing
// and reports a store that recorded no tokens at all.
func TestEveryJSONPathTheRollUpQueriesResolvesInAnEncodedPayload(t *testing.T) {
	f := newStore(t)
	harvest(t, f, []source.Event{spent(instant(0), "u1", "session-a", tokens(1, 2, 3, 4))})

	for path, want := range map[string]string{
		usage.PathSession:       "session-a",
		usage.PathInput:         "1",
		usage.PathOutput:        "2",
		usage.PathCacheRead:     "3",
		usage.PathCacheCreation: "4",
	} {
		var got sql.NullString
		if err := f.raw(t).QueryRow(
			`SELECT json_extract(payload, ?) FROM event`, path).Scan(&got); err != nil {
			t.Fatalf("extract %s: %v", path, err)
		}
		if !got.Valid || got.String != want {
			t.Errorf("json_extract(payload, %q) came back as %v, want %q; the path and the key the encoder "+
				"writes have drifted apart", path, got, want)
		}
	}
}

func TestUsageDistinguishesAFigureNobodyStatedFromAStatedZero(t *testing.T) {
	f := newStore(t)
	harvest(t, f, []source.Event{
		spent(instant(0), "u1", "session-a", &usage.Tokens{Output: usage.Stated(0)}),
		spent(instant(1), "u2", "session-a", &usage.Tokens{Output: usage.Stated(5)}),
	})

	got, err := f.Usage(context.Background(), store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Output.Records != 2 || got.Output.Total != 5 {
		t.Errorf("output came back as %d records totalling %d, want 2 totalling 5; a stated zero is an "+
			"observation and must be counted as one", got.Output.Records, got.Output.Total)
	}
	if got.Input.Records != 0 {
		t.Errorf("input came back as %d records; neither record stated one, and nothing was recorded is not a zero",
			got.Input.Records)
	}
	if got.Input.Total != 0 {
		t.Errorf("input totalled %d over records that stated nothing", got.Input.Total)
	}
}

// The ACCESS PATH of the roll-up, which is a third property alongside what it
// returns and what it stores, and is invisible to tests of either. Migration
// 0014 declines to add an index for this query and says so; that decision is
// only defensible while the period bound is served by the anchor index 0013
// already built, which happens only while the two spell COALESCE(started_at,
// ended_at) identically.
func TestTheUsageRollUpBoundsThePeriodThroughAnIndexRatherThanAScan(t *testing.T) {
	query, args := store.UsageQuery(store.UsageFilter{From: instant(0), To: instant(30)})
	plan := planFor(t, query, args...)
	if strings.Contains(plan, "SCAN canonical_event") || strings.Contains(plan, "SCAN e") {
		t.Errorf("a period-bounded roll-up scans every event:\n%s\n"+
			"the bound is spelled to match event_anchor from migration 0013; check that it still is", plan)
	}
	t.Logf("period-bounded roll-up plan:\n%s", plan)

	whole, wholeArgs := store.UsageQuery(store.UsageFilter{})
	t.Logf("unbounded roll-up plan:\n%s", planFor(t, whole, wholeArgs...))
}

func TestUsageCanBeAskedAboutOneSource(t *testing.T) {
	f := newStore(t)
	other := source.NewPoint(instant(0), source.Identity{Document: "b1"}, "biome-appusage")
	other.Payload = `{"session":"not-a-claude-session","tokens":{"output":99}}`
	harvest(t, f, []source.Event{spent(instant(1), "u1", "session-a", tokens(0, 5, 0, 0)), other})

	got, err := f.Usage(context.Background(), store.UsageFilter{Source: transcriptSource})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if got.Observations != 1 || got.Output.Total != 5 {
		t.Fatalf("asking about one source counted %d observations totalling %d output tokens, want 1 and 5",
			got.Observations, got.Output.Total)
	}
}
