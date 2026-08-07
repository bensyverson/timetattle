package store_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
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

// unpaid is what a harvest stored before the source emitted payloads at all:
// the same observation, with nothing riding along.
func unpaid(events []source.Event) []source.Event {
	out := make([]source.Event, len(events))
	for i, e := range events {
		e.Payload = ""
		out[i] = e
	}
	return out
}

func backfill(t *testing.T, s *store.Store, events []source.Event, opts store.BackfillOptions) store.Backfill {
	t.Helper()
	if opts.Source == "" {
		opts.Source = transcriptSource
	}
	got, err := s.BackfillPayloads(context.Background(), events, opts)
	if err != nil {
		t.Fatalf("BackfillPayloads: %v", err)
	}
	checkLedgers(t, got)
	return got
}

// checkLedgers holds the two ledgers this report is made of. Every offered
// record lands in exactly one bucket, and so does every row that carried no
// payload when the pass began — the shape internal/sessions.Stats and
// store.Attribution both use, and the reason a count in this report can be
// questioned rather than merely believed.
func checkLedgers(t *testing.T, b store.Backfill) {
	t.Helper()
	if got := b.Matched + b.Unstored + b.RepeatOffers; got != b.Offered {
		t.Errorf("the record ledger does not balance: matched %d + unstored %d + repeat offers %d = %d, "+
			"but %d records were offered", b.Matched, b.Unstored, b.RepeatOffers, got, b.Offered)
	}
	if got := b.Fillable + b.NothingToState + b.AlreadyCarrying; got != b.Matched {
		t.Errorf("the matched records do not split cleanly: fillable %d + nothing-to-state %d + already carrying %d = %d, "+
			"but %d records matched a row", b.Fillable, b.NothingToState, b.AlreadyCarrying, got, b.Matched)
	}
	if got := b.Fillable + b.NothingToState + b.Unmatched; got != b.RowsWithoutAPayload {
		t.Errorf("the row ledger does not balance: fillable %d + nothing-to-state %d + unmatched %d = %d, "+
			"but %d rows carried no payload", b.Fillable, b.NothingToState, b.Unmatched, got, b.RowsWithoutAPayload)
	}
}

// storedPayload is what one row says, by the identity a caller can name.
func storedPayload(t *testing.T, f fixture, document string) sql.NullString {
	t.Helper()
	var payload sql.NullString
	err := f.raw(t).QueryRow(`SELECT payload FROM event WHERE document = ?`, document).Scan(&payload)
	if err != nil {
		t.Fatalf("read the payload of %s: %v", document, err)
	}
	return payload
}

// fileDigest is the bytes of the database itself, which is how "wrote nothing"
// is proved rather than asserted: a report saying it changed nothing is exactly
// what a pass that changed something would also print.
func fileDigest(t *testing.T, path string) string {
	t.Helper()
	sum := sha256.New()
	for _, p := range []string{path, path + "-wal"} {
		content, err := os.ReadFile(p)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("read %s: %v", p, err)
		}
		sum.Write(content)
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// THE HEART OF THIS PASS. A row stored before its source emitted payloads gets
// the one its record always stated, and a row that already carries a payload
// keeps it: the fill is `WHERE payload IS NULL`, and never an overwrite.
func TestTheBackfillFillsARowThatCarriesNoPayloadAndOnlyThat(t *testing.T) {
	f := newStore(t)
	first := spent(instant(0), "u1", "session-a", tokens(2, 214, 0, 46724))
	second := spent(instant(1), "u2", "session-b", tokens(5, 91, 12000, 0))

	// u1 arrived before payloads existed; u2 arrived after.
	harvest(t, f, append(unpaid([]source.Event{first}), second))

	if payload := storedPayload(t, f, "u1"); payload.Valid {
		t.Fatalf("u1 carries %q before the backfill; the fixture is not posing the case it exists for", payload.String)
	}

	// The second reading of u2 disagrees with the stored one, which is exactly
	// what an overwrite would destroy.
	rereadSecond := spent(instant(1), "u2", "session-b", tokens(5, 92, 12000, 0))
	got := backfill(t, f.Store, []source.Event{first, rereadSecond}, store.BackfillOptions{})

	if got.Filled != 1 || got.Fillable != 1 {
		t.Errorf("the pass filled %d of %d fillable rows, want 1 of 1", got.Filled, got.Fillable)
	}
	if got.AlreadyCarrying != 1 {
		t.Errorf("%d rows already carried a payload, want 1", got.AlreadyCarrying)
	}
	if payload := storedPayload(t, f, "u1"); !payload.Valid || !strings.Contains(payload.String, `"output":214`) {
		t.Errorf("u1 came back as %v; the backfill did not write the payload its record stated", payload)
	}
	if payload := storedPayload(t, f, "u2"); !strings.Contains(payload.String, `"output":91`) {
		t.Errorf("u2 came back as %q; a row that already carried a payload was rewritten by a later reading, "+
			"and an observation is never rewritten", payload.String)
	}
}

// Idempotence, which is the property this task turns on: the second run is
// offered exactly the same evidence and must change nothing at all — not the
// row count, and not one byte of one payload.
func TestASecondBackfillOfTheSameEvidenceChangesNothing(t *testing.T) {
	f := newStore(t)
	events := []source.Event{
		spent(instant(0), "u1", "session-a", nil),
		spent(instant(1), "u2", "session-a", tokens(2, 214, 0, 46724)),
		spent(instant(2), "u3", "session-b", tokens(5, 91, 12000, 0)),
	}
	harvest(t, f, unpaid(events))

	first := backfill(t, f.Store, events, store.BackfillOptions{})
	if first.Filled != 3 {
		t.Fatalf("the first pass filled %d of 3 rows", first.Filled)
	}
	digest, rows, file := payloadDigest(t, f), countRows(t, f), fileDigest(t, f.path)

	second := backfill(t, f.Store, events, store.BackfillOptions{})

	if second.Filled != 0 {
		t.Errorf("the second pass filled %d rows; a backfill that has already run has nothing left to fill", second.Filled)
	}
	if second.AlreadyCarrying != 3 {
		t.Errorf("the second pass found %d rows already carrying a payload, want 3; a pass that matched nothing "+
			"would also report filling nothing, and the two are not the same fact", second.AlreadyCarrying)
	}
	if got := countRows(t, f); got != rows {
		t.Errorf("the event count went %d -> %d across two backfills of identical evidence", rows, got)
	}
	if got := payloadDigest(t, f); got != digest {
		t.Errorf("the rows changed across two backfills of identical evidence:\nbefore:\n%s\nafter:\n%s", digest, got)
	}
	if got := fileDigest(t, f.path); got != file {
		t.Errorf("the database file changed across the second backfill; it had nothing to write and wrote something")
	}
}

// Absence stays absence. A row whose transcript is no longer on disk is offered
// no record, so nothing matches it, and it keeps the NULL that says nothing was
// recorded — never a zero, which would be a figure nobody observed.
func TestARowWhoseRecordIsGoneKeepsItsNullPayloadAndIsCountedUnmatched(t *testing.T) {
	f := newStore(t)
	kept := spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0))
	gone := spent(instant(1), "u2", "session-a", tokens(2, 20, 0, 0))
	harvest(t, f, unpaid([]source.Event{kept, gone}))

	// Only the surviving transcript is re-read.
	got := backfill(t, f.Store, []source.Event{kept}, store.BackfillOptions{})

	if got.Unmatched != 1 {
		t.Errorf("%d rows went unmatched, want 1: the record for u2 is no longer on disk", got.Unmatched)
	}
	if got.Filled != 1 {
		t.Errorf("the pass filled %d rows, want 1", got.Filled)
	}
	if payload := storedPayload(t, f, "u2"); payload.Valid {
		t.Fatalf("u2 came back carrying %q; a row nothing re-read must keep its NULL, because a payload "+
			"nobody read is not an observation", payload.String)
	}

	rolled, err := f.Usage(context.Background(), store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if rolled.Observations != 1 || rolled.Output.Records != 1 || rolled.Output.Total != 10 {
		t.Errorf("the roll-up reports %d observations and %d output records totalling %d, want 1, 1 and 10; "+
			"the unmatched row must be absent from the figures rather than a zero in them",
			rolled.Observations, rolled.Output.Records, rolled.Output.Total)
	}
}

// A record that states nothing about itself has no payload to give, and the row
// stays NULL. It is counted apart from the unmatched rows: this one WAS re-read
// and had nothing to say, which is not the same fact as never having been read.
func TestARecordStatingNothingLeavesItsRowUnfilled(t *testing.T) {
	f := newStore(t)
	silent := source.NewPoint(instant(0), source.Identity{Document: "u1"}, transcriptSource)
	silent.State = "machine"
	harvest(t, f, []source.Event{silent})

	got := backfill(t, f.Store, []source.Event{silent}, store.BackfillOptions{})

	if got.NothingToState != 1 || got.Fillable != 0 || got.Unmatched != 0 {
		t.Errorf("a record stating nothing was reported as %d nothing-to-state, %d fillable, %d unmatched; "+
			"want 1, 0 and 0", got.NothingToState, got.Fillable, got.Unmatched)
	}
	if payload := storedPayload(t, f, "u1"); payload.Valid {
		t.Errorf("the row came back carrying %q; a record with nothing to state must leave NULL rather than "+
			"an empty payload asserting emptiness", payload.String)
	}
}

// A dry run is the real run with the write withheld, and its numbers are
// therefore a prediction rather than an estimate: the integrator runs it against
// the live store first and the real pass has to do exactly what it said.
func TestADryRunWritesNothingAndReportsWhatTheRealRunThenDoes(t *testing.T) {
	f := newStore(t)
	events := []source.Event{
		spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0)),
		spent(instant(1), "u2", "session-a", nil),
		spent(instant(2), "u3", "session-b", tokens(3, 30, 0, 0)),
	}
	harvest(t, f, unpaid(events[:2]))
	harvest(t, f, []source.Event{events[2]})

	digest, file := payloadDigest(t, f), fileDigest(t, f.path)
	dry := backfill(t, f.Store, events, store.BackfillOptions{DryRun: true})

	if !dry.DryRun {
		t.Error("the report does not say it came from a dry run, so a capture of it cannot be told from a real one")
	}
	if dry.Filled != 0 {
		t.Errorf("a dry run reported filling %d rows; it must write nothing", dry.Filled)
	}
	// Stated as a figure and not only as an agreement: two rows can be filled
	// and the third already carries a payload. A pass that did nothing at all
	// would agree with itself perfectly.
	if dry.Fillable != 2 || dry.AlreadyCarrying != 1 {
		t.Errorf("the dry run reported %d fillable rows and %d already carrying one, want 2 and 1",
			dry.Fillable, dry.AlreadyCarrying)
	}
	if got := payloadDigest(t, f); got != digest {
		t.Errorf("a dry run changed the rows:\nbefore:\n%s\nafter:\n%s", digest, got)
	}
	if got := fileDigest(t, f.path); got != file {
		t.Error("a dry run changed the bytes of the database file")
	}

	actual := backfill(t, f.Store, events, store.BackfillOptions{})
	if actual.Filled != dry.Fillable {
		t.Errorf("the dry run predicted %d fillable rows and the real run filled %d", dry.Fillable, actual.Filled)
	}
	for _, c := range []struct {
		name           string
		predicted, did int
	}{
		{"offered", dry.Offered, actual.Offered},
		{"matched", dry.Matched, actual.Matched},
		{"fillable", dry.Fillable, actual.Fillable},
		{"nothing to state", dry.NothingToState, actual.NothingToState},
		{"already carrying", dry.AlreadyCarrying, actual.AlreadyCarrying},
		{"unstored", dry.Unstored, actual.Unstored},
		{"rows without a payload", dry.RowsWithoutAPayload, actual.RowsWithoutAPayload},
		{"unmatched", dry.Unmatched, actual.Unmatched},
	} {
		if c.predicted != c.did {
			t.Errorf("the dry run said %s %d and the real run reported %d; the two must come from one traversal",
				c.name, c.predicted, c.did)
		}
	}
}

// The dry run is offered through a store opened READ-ONLY, which is how the
// integrator should run it: the promise is then structural rather than a flag
// somebody has to have read correctly.
func TestADryRunRunsThroughAStoreOpenedReadOnly(t *testing.T) {
	f := newStore(t)
	events := []source.Event{spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0))}
	harvest(t, f, unpaid(events))

	reader, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", f.path, err)
	}
	defer reader.Close()

	got := backfill(t, reader, events, store.BackfillOptions{DryRun: true})
	if got.Fillable != 1 || got.Filled != 0 {
		t.Errorf("a read-only dry run reported %d fillable and %d filled, want 1 and 0", got.Fillable, got.Filled)
	}
}

// And a real pass through the same handle is refused in words, rather than by
// SQLite's "attempt to write a readonly database", which names neither the store
// nor why it was opened that way.
func TestABackfillThroughAStoreOpenedReadOnlyIsRefused(t *testing.T) {
	f := newStore(t)
	events := []source.Event{spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0))}
	harvest(t, f, unpaid(events))

	reader, err := store.OpenReadOnly(f.path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", f.path, err)
	}
	defer reader.Close()

	_, err = reader.BackfillPayloads(context.Background(), events, store.BackfillOptions{Source: transcriptSource})
	if err == nil {
		t.Fatal("a backfill through a read-only store was accepted")
	}
	if !strings.Contains(err.Error(), f.path) {
		t.Errorf("the refusal is %q and does not name the store it refused to write", err)
	}
}

// Two offered readings of ONE record resolve the way the insert path resolves
// them: the first wins, and the second is reported rather than silently
// swallowed. This cannot arise from internal/transcripts, whose dedupeReplays
// keys on (Document, Start) — both of which are inside event_identity — but the
// store must not depend on a caller's rule to stay honest about what it wrote.
func TestTwoOfferedReadingsOfOneRecordFillWithTheFirst(t *testing.T) {
	f := newStore(t)
	first := spent(instant(0), "u1", "session-a", tokens(0, 214, 0, 0))
	second := spent(instant(0), "u1", "session-a", tokens(0, 215, 0, 0))
	harvest(t, f, unpaid([]source.Event{first}))

	got := backfill(t, f.Store, []source.Event{first, second}, store.BackfillOptions{})

	if got.RepeatOffers != 1 || got.DisagreeingRepeatOffers != 1 {
		t.Errorf("two readings of one record were reported as %d repeat offers, %d of them disagreeing; want 1 and 1",
			got.RepeatOffers, got.DisagreeingRepeatOffers)
	}
	if payload := storedPayload(t, f, "u1"); !strings.Contains(payload.String, `"output":214`) {
		t.Errorf("the row came back as %q; the FIRST reading is the one the insert path would have kept",
			payload.String)
	}
}

// Safe to interrupt. Every batch is its own transaction, so a pass killed
// partway leaves rows filled and rows not filled and nothing in between — and
// because the pass is idempotent, running it again finishes the job and lands on
// exactly the store one uninterrupted pass would have produced.
func TestABackfillInterruptedPartwayIsCompletedByRunningItAgain(t *testing.T) {
	events := []source.Event{
		spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0)),
		spent(instant(1), "u2", "session-a", tokens(2, 20, 0, 0)),
		spent(instant(2), "u3", "session-b", tokens(3, 30, 0, 0)),
	}

	whole := newStore(t)
	harvest(t, whole, unpaid(events))
	backfill(t, whole.Store, events, store.BackfillOptions{})

	// The same store, filled by a pass that died after its first batch and was
	// then run again from the beginning.
	torn := newStore(t)
	harvest(t, torn, unpaid(events))
	partial := backfill(t, torn.Store, events[:1], store.BackfillOptions{})
	if partial.Filled != 1 {
		t.Fatalf("the interrupted pass filled %d rows, want 1", partial.Filled)
	}
	resumed := backfill(t, torn.Store, events, store.BackfillOptions{})
	if resumed.Filled != 2 {
		t.Errorf("the resumed pass filled %d rows, want 2: the row the first pass reached is already done",
			resumed.Filled)
	}
	if resumed.AlreadyCarrying != 1 {
		t.Errorf("the resumed pass found %d rows already carrying a payload, want 1", resumed.AlreadyCarrying)
	}

	if got, want := payloadDigest(t, torn), payloadDigest(t, whole); got != want {
		t.Errorf("an interrupted-then-resumed backfill and an uninterrupted one produced different stores:\n"+
			"resumed:\n%s\nuninterrupted:\n%s", got, want)
	}
}

// The source is named by the caller and every offered record has to belong to
// it. A transcript payload written onto a Biome row would be a fact from one
// source recorded against another's observation, and nothing downstream could
// ever notice.
func TestTheBackfillRefusesARecordFromAnotherSource(t *testing.T) {
	f := newStore(t)
	stray := source.NewPoint(instant(0), source.Identity{Document: "b1"}, "biome-appusage")
	stray.State = "machine"
	stray.Payload = `{"session":"session-a"}`
	harvest(t, f, []source.Event{stray})

	_, err := f.BackfillPayloads(context.Background(), []source.Event{stray},
		store.BackfillOptions{Source: transcriptSource})
	if err == nil {
		t.Fatal("a record from another source was accepted into a backfill of claude-transcripts")
	}
	if !strings.Contains(err.Error(), "biome-appusage") {
		t.Errorf("the refusal is %q and does not name the source it refused", err)
	}
}

// The shape guard migration 0014 put on UPDATE covers this new write path, which
// is the first thing in the project that updates a payload. A payload that is
// not a JSON object reads back through json_extract as a store that recorded
// nothing, so it has to be refused here as it is on insert.
func TestABackfillPayloadThatIsNotAJSONObjectIsRefused(t *testing.T) {
	f := newStore(t)
	plain := source.NewPoint(instant(0), source.Identity{Document: "u1"}, transcriptSource)
	plain.State = "machine"
	harvest(t, f, []source.Event{plain})

	malformed := plain
	malformed.Payload = `[1,2,3]`
	if _, err := f.BackfillPayloads(context.Background(), []source.Event{malformed},
		store.BackfillOptions{Source: transcriptSource}); err == nil {
		t.Fatal("a payload that is not a JSON object was written by the backfill")
	}
	if payload := storedPayload(t, f, "u1"); payload.Valid {
		t.Errorf("the row came back carrying %q after a refused batch; the transaction did not roll back",
			payload.String)
	}
}

// The ACCESS PATH, which is a third property beside what the pass writes and
// what it reports, and is invisible to tests of either. Both statements name the
// seven columns of event_identity in the spelling migration 0001 indexed them
// with — COALESCE and all — because that is what lets SQLite answer them from
// the unique index. Written any other way they return the same rows and scan
// 822,387 of them per record offered.
func TestTheBackfillFindsItsRowThroughTheIdentityIndexRatherThanAScan(t *testing.T) {
	for name, query := range map[string]string{
		"match": store.MatchIdentityQuery,
		"fill":  store.FillPayloadQuery,
	} {
		args := []any{transcriptSource, "point", stamp(instant(0)), stamp(instant(0)), "", "u1", "machine"}
		if name == "fill" {
			args = append([]any{`{"session":"session-a"}`}, args...)
		}
		plan := planFor(t, query, args...)
		if !strings.Contains(plan, "event_identity") {
			t.Errorf("the %s statement does not ride event_identity:\n%s\n"+
				"it is spelled to match the index migration 0001 creates; check that it still is", name, plan)
		}
		t.Logf("%s plan:\n%s", name, plan)
	}
}

// THE PROPERTY THE WHOLE TASK IS FOR, end to end and through the real parser: a
// store harvested before payloads existed and backfilled afterwards holds
// exactly what a store harvested with them holds. If those two ever disagree,
// every figure computed from history is a different figure from the same one
// computed going forward, and nothing downstream could tell.
func TestAStoreBackfilledAfterwardsHoldsWhatAStoreHarvestedWithPayloadsHolds(t *testing.T) {
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
	// The replayed human turn, which internal/transcripts resolves before
	// either store sees it. Its resolution is by PREFERENCE (a human reading
	// beats a machine copy of it) and is therefore the same in both passes;
	// see the design document for the case that is not, and why no fixture
	// here poses it.
	write(filepath.Join(project, "session-a", "subagents", "agent-acompact-1.jsonl"),
		`{"type":"user","timestamp":"2026-08-07T09:00:00Z","cwd":"/work/project","sessionId":"session-a","uuid":"u1","message":{"role":"user","content":"do the thing"}}`,
	)

	ctx := context.Background()
	reader := transcripts.NewReader(root)
	collect := func() []source.Event {
		t.Helper()
		events, err := reader.Collect(ctx, time.Time{})
		if err != nil {
			t.Fatalf("collect: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("collected %d events from the corpus, want 3", len(events))
		}
		return events
	}

	// One store harvested as harvests work today.
	forward := newStore(t)
	harvest(t, forward, collect())

	// One store harvested as harvests worked before payloads existed, then
	// backfilled from a second, independent read of the same corpus.
	history := newStore(t)
	harvest(t, history, unpaid(collect()))
	got := backfill(t, history.Store, collect(), store.BackfillOptions{})
	if got.Filled != 3 {
		t.Fatalf("the backfill filled %d of 3 rows", got.Filled)
	}

	if a, b := payloadDigest(t, history), payloadDigest(t, forward); a != b {
		t.Errorf("a backfilled store and a store harvested with payloads disagree:\nbackfilled:\n%s\nharvested:\n%s", a, b)
	}

	rolled, err := history.Usage(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	forwardRoll, err := forward.Usage(ctx, store.UsageFilter{})
	if err != nil {
		t.Fatalf("Usage: %v", err)
	}
	if rolled != forwardRoll {
		t.Errorf("the two stores roll up differently: backfilled %+v, harvested %+v", rolled, forwardRoll)
	}
	if rolled.Observations != 3 || rolled.Sessions != 1 || rolled.Output.Total != 305 {
		t.Errorf("the backfilled store rolls up to %d observations across %d sessions totalling %d output tokens, "+
			"want 3, 1 and 305", rolled.Observations, rolled.Sessions, rolled.Output.Total)
	}
}

// Records the collector produced for observations the store has never seen are
// NOT inserted. A backfill fills rows; a harvest stores them, and a pass that
// quietly did both would write events whose provenance is a harvest run that
// never happened.
func TestTheBackfillDoesNotStoreARecordTheStoreNeverSaw(t *testing.T) {
	f := newStore(t)
	stored := spent(instant(0), "u1", "session-a", tokens(1, 10, 0, 0))
	fresh := spent(instant(1), "u2", "session-a", tokens(2, 20, 0, 0))
	harvest(t, f, unpaid([]source.Event{stored}))

	got := backfill(t, f.Store, []source.Event{stored, fresh}, store.BackfillOptions{})

	if got.Unstored != 1 {
		t.Errorf("%d offered records matched no row, want 1", got.Unstored)
	}
	if rows := countRows(t, f); rows != 1 {
		t.Fatalf("the store holds %d rows after a backfill, want 1; the pass inserted an event", rows)
	}
}

// A payload written by the backfill is read back by the same JSON paths the
// roll-up queries, which is the only thing that makes a filled row count for
// anything. Written by any other encoder, every figure would extract NULL and
// the store would report that history recorded nothing — the exact collapse this
// project has shipped three times.
func TestAFilledRowAnswersTheJSONPathsTheRollUpQueries(t *testing.T) {
	f := newStore(t)
	events := []source.Event{spent(instant(0), "u1", "session-a", tokens(1, 2, 3, 4))}
	harvest(t, f, unpaid(events))
	backfill(t, f.Store, events, store.BackfillOptions{})

	for path, want := range map[string]string{
		usage.PathSession:       "session-a",
		usage.PathInput:         "1",
		usage.PathOutput:        "2",
		usage.PathCacheRead:     "3",
		usage.PathCacheCreation: "4",
	} {
		var got sql.NullString
		if err := f.raw(t).QueryRow(`SELECT json_extract(payload, ?) FROM event`, path).Scan(&got); err != nil {
			t.Fatalf("extract %s: %v", path, err)
		}
		if !got.Valid || got.String != want {
			t.Errorf("json_extract(payload, %q) came back as %v, want %q", path, got, want)
		}
	}
}
