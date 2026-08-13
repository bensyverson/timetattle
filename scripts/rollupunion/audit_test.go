package main

import (
	"errors"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/metrics"
	"github.com/bensyverson/timetattle/internal/sessions"
	"github.com/bensyverson/timetattle/internal/source"
)

var day = time.Date(2026, 3, 4, 0, 0, 0, 0, time.Local)

func at(h, m int) time.Time {
	return day.Add(time.Duration(h)*time.Hour + time.Duration(m)*time.Minute)
}

// shippedParams are the defaults `timetattle metrics` runs at, so a figure here
// is the figure the shipped command would produce on the same events.
func shippedParams() sessions.Params {
	return sessions.Params{
		Gap:             15 * time.Minute,
		LeadIn:          5 * time.Minute,
		TailOut:         5 * time.Minute,
		PresenceSources: []string{"knowledgec-backlit"},
	}
}

func clientPoint(when time.Time, client, project string) source.Event {
	e := source.NewPoint(when, source.Identity{Client: client, Project: project}, "claude-transcripts")
	e.Locator = "/Users/you/git/" + project
	return e
}

func onlyRollUp(t *testing.T, events []source.Event) rollup {
	t.Helper()
	r, err := metrics.Compute(events, shippedParams())
	if err != nil {
		t.Fatalf("the corpus was refused, so there is no report to audit: %v", err)
	}
	got := audit(r)
	if len(got) != 1 {
		t.Fatalf("expected one client in the audit, got %d", len(got))
	}
	return got[0]
}

// A client whose projects never interleave holds exactly the clock its projects
// hold, and the doc comment on metrics.Client — that a roll-up is the union of
// its projects' spans — is true of it. This is the case that made the claim
// look safe, and it has to keep reading zero or the instrument is measuring
// noise rather than the phenomenon.
func TestNoClockIsHeldByAClientAloneWhenItsProjectsDoNotInterleave(t *testing.T) {
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 5), "atelier", "loom"),
		clientPoint(at(9, 10), "atelier", "loom"),
		clientPoint(at(14, 0), "atelier", "kiln"),
		clientPoint(at(14, 5), "atelier", "kiln"),
	}
	got := onlyRollUp(t, events)
	if got.Excess != 0 {
		t.Errorf("excess is %v, want 0: no gap here is bridged at the client level and not at the project level", got.Excess)
	}
	if got.Orphan != 0 {
		t.Errorf("orphan is %v, want 0", got.Orphan)
	}
	if got.Mismatch != 0 {
		t.Errorf("the audit recomputed %v of engaged clock against the report's own %v", got.Union+got.Excess, got.Engaged)
	}
	// 8:55-9:15 and 13:55-14:10, at both levels, which is a roll-up that is a
	// union AND a sum because nothing overlaps and nothing is bridged.
	if want := 35 * time.Minute; got.Engaged != want {
		t.Errorf("client engaged %v, want %v", got.Engaged, want)
	}
	if want := 35 * time.Minute; got.Summed != want {
		t.Errorf("projects summed %v, want %v", got.Summed, want)
	}
	if got.Margin != 0 {
		t.Errorf("margin is %v, want 0", got.Margin)
	}
}

// THE MEASUREMENT THIS TOOL EXISTS FOR, on a corpus the shipped guard ACCEPTS.
//
// The guard compares the client against the SUM of its projects; the doc
// comment claims something strictly stronger, that the client is their UNION.
// The two come apart as soon as a client's projects both overlap somewhere and
// interleave somewhere else: the overlap gives the sum enough surplus to keep
// the guard quiet while the interleave hands the client clock no project of its
// own holds.
//
// Alternating every 12 minutes puts each project's own observations 24 minutes
// apart — more than the 15-minute gap, so neither project bridges its own
// silence — while the client's are 12 apart and bridge all of it. The three
// bridged stretches are 2 minutes each. The concurrent block at 12:00 is
// counted once for the client and twice by the sum, which is the surplus.
func TestAClientCanHoldClockNoProjectOfItsOwnHoldsWhileTheGuardStillPasses(t *testing.T) {
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
		clientPoint(at(12, 0), "atelier", "loom"),
		clientPoint(at(12, 0), "atelier", "kiln"),
	}
	got := onlyRollUp(t, events)
	if want := 6 * time.Minute; got.Excess != want {
		t.Errorf("excess is %v, want %v", got.Excess, want)
	}
	if got.ExcessRuns != 3 {
		t.Errorf("the excess falls in %d stretches, want 3", got.ExcessRuns)
	}
	if want := 2 * time.Minute; got.LargestExcess != want {
		t.Errorf("largest excess stretch is %v, want %v", got.LargestExcess, want)
	}
	if want := 56 * time.Minute; got.Engaged != want {
		t.Errorf("client engaged %v, want %v", got.Engaged, want)
	}
	if want := 50 * time.Minute; got.Union != want {
		t.Errorf("client clock a project of its own also holds is %v, want %v", got.Union, want)
	}
	if want := 60 * time.Minute; got.Summed != want {
		t.Errorf("projects summed %v, want %v", got.Summed, want)
	}
	if want := 4 * time.Minute; got.Margin != want {
		t.Errorf("margin is %v, want %v; the guard must still pass on this corpus", got.Margin, want)
	}
	if got.Orphan != 0 {
		t.Errorf("orphan is %v, want 0", got.Orphan)
	}
	if got.Mismatch != 0 {
		t.Errorf("mismatch is %v, want 0", got.Mismatch)
	}
}

// WHERE THE DISPUTED CLOCK IS REPORTED TODAY, which is what makes the choice
// concrete rather than doctrinal. A stretch a client holds and no project of
// its own holds is a stretch the segment carries NO project for at all unless
// some OTHER client's project happens to cover it — and a segment with no
// project is exactly what the report files under UNATTRIBUTED PRESENCE, reason
// "silence the gap threshold bridged". The same minutes are therefore printed
// twice in one report: as a client's engaged hours, and as presence the report
// says it could not attribute.
//
// Here the third project belongs to a second client and covers the first
// bridged stretch, so 2 of the 6 disputed minutes are attributed to somebody
// and 4 are not.
func TestTheAuditSaysHowMuchOfTheDisputedClockIsAlsoReportedUnattributed(t *testing.T) {
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
		clientPoint(at(12, 0), "atelier", "loom"),
		clientPoint(at(12, 0), "atelier", "kiln"),
		clientPoint(at(9, 6), "foundry", "anvil"),
	}
	r, err := metrics.Compute(events, shippedParams())
	if err != nil {
		t.Fatalf("the corpus was refused, so there is no report to audit: %v", err)
	}
	rows := audit(r)
	if len(rows) != 2 {
		t.Fatalf("expected two clients in the audit, got %d", len(rows))
	}
	got := rows[0]
	if got.Client != "atelier" {
		t.Fatalf("the first row is %q, not the interleaving client", got.Client)
	}
	if want := 6 * time.Minute; got.Excess != want {
		t.Errorf("excess is %v, want %v", got.Excess, want)
	}
	if want := 4 * time.Minute; got.AloneAndUnattributed != want {
		t.Errorf("disputed clock the report also calls unattributed is %v, want %v",
			got.AloneAndUnattributed, want)
	}
}

// The opposite direction, which metrics.Compute cannot produce today and which
// this tool must still be able to SEE. A client's evidence is a superset of any
// one project's, so re-clustering it can only ever extend the spans; if a
// project were ever active where its client was not, the roll-up would be
// missing clock rather than holding extra, and an instrument that only looked
// one way would report a clean answer it had not measured.
func TestTheAuditSeesProjectClockAClientDoesNotHold(t *testing.T) {
	r := metrics.Report{
		Projects: []metrics.Project{{Name: "loom", Engaged: metrics.Span{Total: 20 * time.Minute}}},
		Clients:  []metrics.Client{{Name: "atelier", Projects: []string{"loom"}, Engaged: metrics.Span{Total: 10 * time.Minute}}},
		Segments: []metrics.Segment{
			{Start: at(9, 0), End: at(9, 10), Projects: []string{"loom"}, Clients: []string{"atelier"}},
			{Start: at(9, 10), End: at(9, 20), Projects: []string{"loom"}},
		},
	}
	got := audit(r)
	if len(got) != 1 {
		t.Fatalf("expected one client in the audit, got %d", len(got))
	}
	if want := 10 * time.Minute; got[0].Orphan != want {
		t.Errorf("orphan is %v, want %v", got[0].Orphan, want)
	}
	if got[0].Excess != 0 {
		t.Errorf("excess is %v, want 0", got[0].Excess)
	}
	if got[0].Mismatch != 0 {
		t.Errorf("mismatch is %v, want 0", got[0].Mismatch)
	}
}

// The audit sums the same clock the report already summed, so the two must
// agree to the nanosecond. They are two computations of one figure and this is
// the subtraction that says so; without it a change to how segments carry
// client membership could quietly make every figure above meaningless.
func TestTheAuditReportsWhenItsOwnTotalDisagreesWithTheReports(t *testing.T) {
	r := metrics.Report{
		Projects: []metrics.Project{{Name: "loom", Engaged: metrics.Span{Total: 10 * time.Minute}}},
		Clients:  []metrics.Client{{Name: "atelier", Projects: []string{"loom"}, Engaged: metrics.Span{Total: 25 * time.Minute}}},
		Segments: []metrics.Segment{
			{Start: at(9, 0), End: at(9, 10), Projects: []string{"loom"}, Clients: []string{"atelier"}},
		},
	}
	got := audit(r)
	if len(got) != 1 {
		t.Fatalf("expected one client in the audit, got %d", len(got))
	}
	if want := -15 * time.Minute; got[0].Mismatch != want {
		t.Errorf("mismatch is %v, want %v", got[0].Mismatch, want)
	}
}

// THE INTERLEAVING CORPUS IS NO LONGER REFUSED, and this sweep counts refusals,
// so it has to be told apart from a window that measured nothing.
//
// Until 2026-08-13 this corpus was the pinned refusal: a client credited 46m
// against projects summing to 40m, and no report at all. The guard that produced
// it encoded an invariant that is false wherever a client's projects interleave,
// and it was replaced by one that cannot be provoked by any corpus — see
// internal/metrics/rollupcost_test.go and
// project/2026-08-13-rollup-reclustering-resolved.md.
func TestTheInterleavingCorpusIsNoLongerRefused(t *testing.T) {
	events := []source.Event{
		clientPoint(at(9, 0), "atelier", "loom"),
		clientPoint(at(9, 12), "atelier", "kiln"),
		clientPoint(at(9, 24), "atelier", "loom"),
		clientPoint(at(9, 36), "atelier", "kiln"),
	}
	r, err := metrics.Compute(events, shippedParams())
	if err != nil {
		t.Fatalf("the corpus that used to be refused must now compute: %v", err)
	}
	rows := audit(r)
	if len(rows) != 1 {
		t.Fatalf("expected one client in the audit, got %d", len(rows))
	}
	if want := -6 * time.Minute; rows[0].Margin != want {
		t.Errorf("margin %v, want %v; a negative margin used to mean no report and now means "+
			"only that the client bridged clock its projects could not", rows[0].Margin, want)
	}
}

// WHEN A WINDOW IS REFUSED, THERE IS NO REPORT — Compute returns the zero value
// and every figure above is unavailable. The error text is then the only
// surviving measurement of how far the two levels diverged, so this tool reads
// its two durations back out rather than printing "refused" and losing them. The
// distinction the whole project turns on applies here too: a window with no
// figures because the report was refused is not a window that measured zero.
//
// THE REFUSAL IS BUILT HERE RATHER THAN PROVOKED, and that is a statement about
// the check rather than a shortcut. The roll-up guard that closes Compute since
// 2026-08-13 compares a client owning ONE project against that project, which is
// the same evidence clustered twice, so no corpus can make it fire — only a
// defect in the clustering can. The literal below is copied from
// internal/metrics/metrics.go, and if the two ever drift apart the sweep does
// NOT silently count zero refusals: an error it cannot parse aborts the sweep
// with that error, which is the loud failure and not the quiet one.
func TestTheRefusalsOwnNumbersAreReadBackOutOfIt(t *testing.T) {
	err := errors.New("client atelier is credited 51h39m27.261521s against the 48h1m24.29735s " +
		"held by its only project loom; a client with one project is that project's own " +
		"evidence clustered again and can never exceed it")
	credited, held, ok := parseRefusal(err)
	if !ok {
		t.Fatalf("the refusal was not recognised as the roll-up guard's: %v", err)
	}
	// The figures are the live-store ones the replaced guard was proved with: a
	// one-project client inflated by clustering the client level at gap 30m
	// while the projects stayed at 15m, caught at 3h38m2.96s.
	if want := 51*time.Hour + 39*time.Minute + 27*time.Second + 261521*time.Microsecond; credited != want {
		t.Errorf("credited %v, want %v", credited, want)
	}
	if want := 48*time.Hour + 1*time.Minute + 24*time.Second + 297350*time.Microsecond; held != want {
		t.Errorf("held by the project %v, want %v", held, want)
	}
}

// An unrelated failure must NOT be read as a roll-up refusal. Every other check
// in Compute also returns an error, and a tool that reported one of those as a
// guard refusal would be manufacturing the very finding it exists to count.
func TestAnUnrelatedFailureIsNotReadAsARollUpRefusal(t *testing.T) {
	if _, _, ok := parseRefusal(errors.New("the segments cover 1h against 2h of sittings")); ok {
		t.Error("a segmentation failure was read as the roll-up guard's refusal")
	}
}
