package adjustments_test

import (
	"encoding/csv"
	"os"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/adjustments"
)

// Constructed cases sit BESIDE real-data checks; neither substitutes for the
// other. This project has already had an over-general supersession rule pass
// against the entire live Biome stream — deleting nothing, purely because no
// interval happened to share an endpoint with a bare stop — and it would have
// begun destroying records the first time a second source arrived. The
// constructed cases in adjustments_test.go are four to eight hand-written
// intervals. These are 381 real ones.
//
// The fixture is the one internal/sessions already keeps, read from there
// rather than copied so that the two packages cannot drift onto different
// "real" data. internal/sessions/calendar_realdata_test.go attributes it to the
// live store on 2026-07-31 at gap 15m, lead-in 5m, tail-out 5m, presence
// knowledgec-backlit, ignoring knowledgec-islocked and knowledgec-mediausage.
//
// Those parameters are ATTRIBUTED here rather than asserted: the CSV carries
// only start, end, event count and margin, so nothing in it can confirm them,
// and the live table has since moved on. Measured 2026-08-01: the fixture holds
// 381 sittings totalling 248.69h with 21.56h of margin (8.67%), while
// derived_session in the live store at computed_at 2026-08-01T13:32:33Z holds
// 1179 sittings totalling 1001.91h with 773.70h of margin (77.2%) at those same
// stated parameters. Both are reproducible — the first from this file, the
// second with
// `sqlite3 "file:~/.timetattle/timetattle.db?mode=ro" "SELECT COUNT(*), SUM((julianday(ended_at)-julianday(started_at))*24), SUM(margin_micros)/1e6/3600.0 FROM derived_session"`.
// The corpus grew between the two; nothing here depends on which is current,
// because every figure below is a difference taken within one of them.
const realFixture = "../sessions/testdata/realsessions.csv"

func realSpans(t *testing.T) []adjustments.Span {
	t.Helper()
	f, err := os.Open(realFixture)
	if err != nil {
		t.Skipf("no real session fixture at %s: %v", realFixture, err)
	}
	defer f.Close()

	rows, err := csv.NewReader(f).ReadAll()
	if err != nil {
		t.Fatalf("read %s: %v", realFixture, err)
	}
	out := make([]adjustments.Span, 0, len(rows))
	for i, row := range rows {
		if len(row) != 4 {
			t.Fatalf("row %d has %d fields, want 4", i+1, len(row))
		}
		start, err := time.Parse("2006-01-02T15:04:05.000000Z", row[0])
		if err != nil {
			t.Fatalf("row %d start: %v", i+1, err)
		}
		end, err := time.Parse("2006-01-02T15:04:05.000000Z", row[1])
		if err != nil {
			t.Fatalf("row %d end: %v", i+1, err)
		}
		out = append(out, adjustments.Span{Start: start, End: end})
	}
	return out
}

func totalOf(spans []adjustments.Span) time.Duration {
	var sum time.Duration
	for _, s := range spans {
		sum += s.End.Sub(s.Start)
	}
	return sum
}

// The state every day is in today, over the whole real corpus. An overlay with
// nothing in it must be EXACTLY the identity — not approximately, and not
// "within a rounding".
//
// This is the check that would catch a fold whose union quietly merged real
// sittings that merely abut. 381 real spans include plenty of near-touches that
// four hand-written intervals do not.
func TestAnEmptyOverlayIsTheIdentityOnTheRealCorpus(t *testing.T) {
	real := realSpans(t)
	if len(real) != 381 {
		t.Fatalf("the fixture holds %d sittings, want 381; the figures below are quoted against that corpus", len(real))
	}
	before := totalOf(real)

	got := adjustments.Fold(nil, nil).Engaged(real)

	if len(got) != len(real) {
		t.Errorf("an empty overlay turned %d sittings into %d", len(real), len(got))
	}
	if after := totalOf(got); after != before {
		t.Errorf("an empty overlay moved the total by %v (%v to %v)", after-before, before, after)
	}
	for i := range got {
		if !got[i].Start.Equal(real[i].Start) || !got[i].End.Equal(real[i].End) {
			t.Fatalf("sitting %d moved from %v..%v to %v..%v",
				i, real[i].Start, real[i].End, got[i].Start, got[i].End)
		}
	}
}

// A correction removes what it covers and NOTHING else, measured against the
// real corpus rather than asserted. An over-general rule shows up here as a
// number: the residual has to equal the length of the one sitting removed.
func TestACorrectionOverOneRealSittingRemovesExactlyThatSitting(t *testing.T) {
	real := realSpans(t)
	before := totalOf(real)

	// The longest real sitting, so the measurement is the least likely to be
	// swallowed by a rounding and the most likely to overlap its neighbours if
	// the fold is wrong.
	longest := 0
	for i, s := range real {
		if s.End.Sub(s.Start) > real[longest].End.Sub(real[longest].Start) {
			longest = i
		}
	}
	target := real[longest]
	length := target.End.Sub(target.Start)

	log := []adjustments.Adjustment{{
		ID: 1, Start: target.Start, End: target.End,
		Assertion: adjustments.NotEngaged, Origin: "a-person",
		RecordedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}}
	got := adjustments.Fold(nil, log).Engaged(real)

	if residual := before - totalOf(got); residual != length {
		t.Errorf("removing one %v sitting changed the total by %v; "+
			"the fold is taking time it was not given", length, residual)
	}
	if len(got) != len(real)-1 {
		t.Errorf("removing one sitting left %d, want %d", len(got), len(real)-1)
	}
}

// Conservation over real shapes. A boundary at the midpoint of every real
// sitting must double the count and move the total by exactly zero.
//
// A split that changed the wall clock would be a silent restatement of every
// hour in the corpus, and it is the failure a hand-written pair of intervals is
// least likely to expose: 381 real spans carry microsecond endpoints and
// sub-second lengths, where an off-by-one in the cut has somewhere to hide.
func TestBoundariesAtEveryRealMidpointConserveTheTotal(t *testing.T) {
	real := realSpans(t)
	before := totalOf(real)

	recordedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	var log []adjustments.Adjustment
	var cuttable int
	for i, s := range real {
		mid := s.Start.Add(s.End.Sub(s.Start) / 2)
		// A zero-length or one-microsecond sitting has no interior instant, so
		// it cannot be cut and must not be counted as one that was.
		if !mid.After(s.Start) || !mid.Before(s.End) {
			continue
		}
		cuttable++
		log = append(log, adjustments.Adjustment{
			ID: int64(i + 1), Start: mid, End: mid,
			Assertion: adjustments.Boundary, Origin: "a-person", RecordedAt: recordedAt,
		})
	}
	if cuttable == 0 {
		t.Fatal("no real sitting has an interior instant; the test would be vacuous")
	}

	got := adjustments.Fold(nil, log).Engaged(real)

	if after := totalOf(got); after != before {
		t.Errorf("splitting %d real sittings moved the total by %v (%v to %v); "+
			"a boundary must change the count and never the wall clock",
			cuttable, after-before, before, after)
	}
	if want := len(real) + cuttable; len(got) != want {
		t.Errorf("splitting %d of %d real sittings produced %d spans, want %d",
			cuttable, len(real), len(got), want)
	}
}

// The residual is the model error, over the real corpus. This is the number
// task PkY6I fits against, and it is only meaningful because the machine's
// answer is left alone: the same slice comes back unchanged after the fold.
func TestTheRealCorpusIsUntouchedByFolding(t *testing.T) {
	real := realSpans(t)
	before := totalOf(real)
	count := len(real)

	log := []adjustments.Adjustment{{
		ID: 1, Start: real[0].Start, End: real[len(real)-1].End,
		Assertion: adjustments.NotEngaged, Origin: "a-person",
		RecordedAt: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
	}}
	// An assertion spanning the whole corpus: the most destructive thing a
	// person could record, and it must still leave the machine's answer intact.
	if got := adjustments.Fold(nil, log).Engaged(real); len(got) != 0 {
		t.Errorf("asserting the whole corpus was not work left %d spans", len(got))
	}
	if len(real) != count || totalOf(real) != before {
		t.Errorf("folding edited the machine's answer: %d spans totalling %v, want %d totalling %v",
			len(real), totalOf(real), count, before)
	}
}
