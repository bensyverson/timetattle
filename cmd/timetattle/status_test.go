package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"
)

// `timetattle status` exists because the failure this task was opened for was
// invisible for over a day. The scheduled harvest wrote its error to
// ~/.timetattle/harvest.err, launchd carried on, and nothing else changed: the
// store simply stopped advancing. The data to notice that was already in
// harvest_run — status, error, events_seen, events_new, one row per source per
// invocation — and there was no way to ask for it.
//
// The property these tests exist for is the standing one in CLAUDE.md: absence
// of a row is not an observation. "This source has never run" and "this source
// ran and found nothing" are different facts and must render differently.

// statusLine returns the report's line for one source, which is what a reader
// actually looks at.
func statusLine(t *testing.T, report, sourceID string) string {
	t.Helper()
	for line := range strings.SplitSeq(report, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), sourceID+" ") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatalf("no line for %s in:\n%s", sourceID, report)
	return ""
}

// TestStatusTellsNeverRunFromRanAndFoundNothing is the worked example of both
// states in one report. The four knowledgeC streams are read out of a snapshot
// holding no rows at all — a real success with nothing in it — while the
// registered sources are never read, so they have no row of any kind.
func TestStatusTellsNeverRunFromRanAndFoundNothing(t *testing.T) {
	empty := writeKnowledgeC(t, filepath.Join(t.TempDir(), "kc.db"))
	st, storePath := newStore(t)
	if err := ingest(context.Background(), st, snapshotIngestions(empty), io.Discard); err != nil {
		t.Fatalf("ingesting an empty snapshot: %v", err)
	}

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{store: storePath, snapshots: t.TempDir(), stale: time.Hour, now: time.Now()})
	got := out.String()
	t.Logf("report:\n%s", got)

	ran := statusLine(t, got, kcBacklight)
	if !strings.Contains(ran, "ok") {
		t.Errorf("%s ran and found nothing, and its line does not say ok: %q", kcBacklight, ran)
	}
	if strings.Contains(ran, "never") {
		t.Errorf("%s ran and found nothing, and its line reads as never run: %q", kcBacklight, ran)
	}

	never := statusLine(t, got, claudeTranscripts)
	if !strings.Contains(never, "never") {
		t.Errorf("%s has no harvest_run row at all, and its line does not say so: %q", claudeTranscripts, never)
	}
	if ran == never {
		t.Errorf("a source that ran and found nothing renders identically to one that never ran: %q", ran)
	}
	// Never having run is reported, not alarmed about: a source that has never
	// run may simply not exist on this machine, and a check that cries wolf is
	// a check nobody reads — which is the failure being fixed.
	if err != nil {
		t.Errorf("status failed on a store whose every run succeeded: %v", err)
	}
}

// TestStatusReportsAFailedLatestRun is the case that was silent for a day.
func TestStatusReportsAFailedLatestRun(t *testing.T) {
	p := newPartialHarvest(t)
	src := unreadable(t, filepath.Join(p.dir, "knowledgeC.db"))
	if err := p.run(t, src); err == nil {
		t.Fatal("harvest reported success with an unreadable knowledgeC")
	}

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{store: p.storePath, snapshots: p.snapDir, stale: time.Hour, now: time.Now()})
	got := out.String()
	t.Logf("report:\n%s", got)

	if err == nil {
		t.Error("status exited zero with four failed sources in the store")
	}
	for _, id := range []string{kcBacklight, kcLocked, kcAppUsage, kcMediaUsage} {
		line := statusLine(t, got, id)
		if !strings.Contains(line, "failed") {
			t.Errorf("%s failed and its line does not say so: %q", id, line)
		}
	}
	if !strings.Contains(got, src) {
		t.Errorf("the report does not say why the runs failed; it never names %s:\n%s", src, got)
	}
}

// statusPerRepositoryRuns writes what a source that runs once per repository
// leaves in harvest_run: one run per repository, within one invocation, with one
// repository lost. Two repositories are read through and hold real events, so
// the totals the report prints are reconcilable against the store rather than
// asserted from the fixture's own arithmetic.
//
// The lost repository is deliberately not the last one written. Taking the last
// row of the group and calling it the source's result reported this exact
// history as ok.
func statusPerRepositoryRuns(t *testing.T) string {
	t.Helper()
	st, path := newStore(t)
	ctx := context.Background()
	at := time.Date(2026, 7, 30, 9, 0, 0, 0, time.UTC)

	repos := []struct {
		path   string
		events int
	}{
		{"/repo/acorn", 2},
		{"/repo/birch", 0}, // lost
		{"/repo/timetattle", 3},
	}
	for i, repo := range repos {
		run, err := st.BeginRun(ctx, store.RunSpec{SourceID: gitlogCommitsID, Input: repo.path})
		if err != nil {
			t.Fatalf("BeginRun for %s: %v", repo.path, err)
		}
		if repo.events == 0 {
			if err := run.Fail(ctx, fmt.Errorf("cannot read %s/.git: operation not permitted", repo.path)); err != nil {
				t.Fatalf("Fail for %s: %v", repo.path, err)
			}
			continue
		}
		var events []source.Event
		for j := range repo.events {
			events = append(events, source.NewPoint(
				at.Add(time.Duration(i)*time.Hour+time.Duration(j)*time.Minute),
				source.Identity{Document: repo.path}, gitlogCommitsID))
		}
		if _, err := run.Write(ctx, events); err != nil {
			t.Fatalf("Write for %s: %v", repo.path, err)
		}
		if err := run.Finish(ctx); err != nil {
			t.Fatalf("Finish for %s: %v", repo.path, err)
		}
	}
	st.Close()
	return path
}

const gitlogCommitsID = "gitlog-commits"

// TestStatusTotalsARepositoryWideSourceAndSaysWhenItIsOnlyPartlyRead is the
// task in one report. Before this, the line read "1 seen, 1 new, ok" for a
// harvest that saw five events across three repositories and lost one of them.
func TestStatusTotalsARepositoryWideSourceAndSaysWhenItIsOnlyPartlyRead(t *testing.T) {
	path := statusPerRepositoryRuns(t)

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{store: path, snapshots: t.TempDir(), stale: time.Hour, now: time.Now()})
	got := out.String()
	t.Logf("report:\n%s", got)

	line := statusLine(t, got, gitlogCommitsID)
	if !strings.Contains(line, " 5 ") {
		t.Errorf("the line does not total the five events the run saw across three repositories: %q", line)
	}
	if strings.Contains(line, " ok ") {
		t.Errorf("a run that lost a repository is reported as ok: %q", line)
	}
	if !strings.Contains(line, statusPartial) {
		t.Errorf("the line does not say the run was only partly read: %q", line)
	}
	if err == nil {
		t.Error("status exited zero on a harvest that lost a repository")
	}
	if strings.Contains(got, "every source ran and succeeded") {
		t.Errorf("the summary claims every source succeeded while a repository was lost:\n%s", got)
	}
	// Which repository broke, and why, is the question that follows.
	if !strings.Contains(got, "/repo/birch") {
		t.Errorf("the report never names the repository that was lost:\n%s", got)
	}
}

// The default report is scanned quickly and stays one line per source; the
// per-repository detail is a flag away, and is where "which repo broke" is
// answered in full.
func TestStatusListsEachRepositoryWhenAsked(t *testing.T) {
	path := statusPerRepositoryRuns(t)

	var brief, full bytes.Buffer
	_ = reportStatus(&brief, statusOptions{store: path, snapshots: t.TempDir(), stale: time.Hour, now: time.Now()})
	_ = reportStatus(&full, statusOptions{store: path, snapshots: t.TempDir(), stale: time.Hour, now: time.Now(), inputs: true})
	t.Logf("detailed report:\n%s", full.String())

	for _, repo := range []string{"/repo/acorn", "/repo/birch", "/repo/timetattle"} {
		if !strings.Contains(full.String(), repo) {
			t.Errorf("-inputs never mentions %s:\n%s", repo, full.String())
		}
	}
	if strings.Contains(brief.String(), "/repo/acorn") {
		t.Errorf("the default report lists a repository that read through, which is what the flag is for:\n%s", brief.String())
	}
}

// TestStatusReportsAStaleSchedule is the signal that would have caught this
// within the first missed interval rather than on the second day.
func TestStatusReportsAStaleSchedule(t *testing.T) {
	empty := writeKnowledgeC(t, filepath.Join(t.TempDir(), "kc.db"))
	st, storePath := newStore(t)
	if err := ingest(context.Background(), st, snapshotIngestions(empty), io.Discard); err != nil {
		t.Fatalf("ingesting an empty snapshot: %v", err)
	}

	var out bytes.Buffer
	// Two days after the only run, against a schedule that runs twice a day.
	err := reportStatus(&out, statusOptions{
		store:     storePath,
		snapshots: t.TempDir(),
		stale:     24 * time.Hour,
		now:       time.Now().Add(48 * time.Hour),
	})
	got := out.String()
	t.Logf("report:\n%s", got)
	if err == nil {
		t.Error("status exited zero on a store whose newest run is two days old")
	}
	if !strings.Contains(got, "stale") {
		t.Errorf("the report does not use the word stale:\n%s", got)
	}
}

// TestStatusOnAStoreThatDoesNotExist keeps the report read-only. A status check
// that creates the thing it is checking would report a healthy empty store on a
// machine where the harvest has never once run.
func TestStatusOnAStoreThatDoesNotExist(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "nothing", "timetattle.db")

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{store: missing, snapshots: dir, stale: time.Hour, now: time.Now()})
	if err == nil {
		t.Error("status reported success against a store that does not exist")
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Errorf("status created %s; a read-only check must not", missing)
	}
	if !strings.Contains(out.String(), "never") {
		t.Errorf("the report does not say that no harvest has ever run:\n%s", out.String())
	}
}

// statusMidHarvest leaves a store in the state a harvest is in while it is
// still running: one completed run of a source, and a second run of it begun and
// not finished. That is a row with a started_at and no finished_at — which is
// also, and identically, what a harvest that died leaves behind. Only the clock
// separates them, so the same store is read twice below at two different
// instants and must produce two different reports.
//
// It returns the store path and the instant the unfinished run began.
func statusMidHarvest(t *testing.T) (string, time.Time) {
	t.Helper()
	st, path := newStore(t)
	ctx := context.Background()

	const projects = "/Users/ben/.claude/projects"
	done, err := st.BeginRun(ctx, store.RunSpec{SourceID: claudeTranscripts, Input: projects})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := done.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	// The run in flight. Nothing finishes it, which is the point.
	inFlight, err := st.BeginRun(ctx, store.RunSpec{SourceID: claudeTranscripts, Input: projects})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	st.Close()
	return path, inFlight.StartedAt
}

// TestStatusDoesNotReportALiveHarvestAsADeadOne is the first half of the
// criterion, and the defect. Before 2026-08-01 this report said "1 source left
// marked running, which is a harvest that died partway" and exited non-zero at a
// harvest that was running perfectly well twelve seconds in.
func TestStatusDoesNotReportALiveHarvestAsADeadOne(t *testing.T) {
	path, began := statusMidHarvest(t)

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), stale: 24 * time.Hour, now: began.Add(12 * time.Second),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	if err != nil {
		t.Errorf("status failed twelve seconds into a harvest that is running normally: %v", err)
	}
	if strings.Contains(got, "died partway") {
		t.Errorf("the report calls a harvest that started twelve seconds ago one that died partway:\n%s", got)
	}
	if !strings.Contains(got, "in progress") {
		t.Errorf("the report never says a harvest is in progress, so a reader cannot tell why "+
			"a source has no result yet:\n%s", got)
	}
	// Nor may it claim the unfinished source succeeded. Silence is not the fix
	// here: "every source ran and succeeded" over a source still reading would be
	// the false negative traded for the false positive.
	if strings.Contains(got, "every source ran and succeeded") {
		t.Errorf("the summary claims every source succeeded while one is still reading:\n%s", got)
	}
}

// TestStatusStillReportsAHarvestThatDiedPartway is the second half, over the
// same store. Fixing the false alarm by never reporting a stranded run would be
// the absence-is-not-an-observation collapse this project has shipped twice.
func TestStatusStillReportsAHarvestThatDiedPartway(t *testing.T) {
	path, began := statusMidHarvest(t)

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), stale: 24 * time.Hour, now: began.Add(3 * time.Hour),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	if err == nil {
		t.Error("status exited zero over a run that has been marked running for three hours")
	}
	if !strings.Contains(got, "died partway") {
		t.Errorf("the report never says the harvest died partway:\n%s", got)
	}
	line := statusLine(t, got, claudeTranscripts)
	if !strings.Contains(line, statusStranded) {
		t.Errorf("the source line does not say the run is %s: %q", statusStranded, line)
	}
	// The completed run in this fixture took under a millisecond, so what ruled
	// the stranded verdict is the five-minute floor and not ten times anything.
	// Saying otherwise would be the report quoting arithmetic it did not do, in
	// the one paragraph that states a conclusion nothing observed.
	if strings.Contains(got, "times its slowest completed run") {
		t.Errorf("the report justifies the verdict as a multiple of the slowest run, when the "+
			"slowest run here is under a millisecond and the floor is what decided it:\n%s", got)
	}
}

// TestStatusStaleCatchesARunNothingCanJudge is the backstop under the one case
// the tolerance deliberately refuses to rule on. A source whose only row is
// unfinished has no completed run to be measured against, so it is never
// presumed dead — and if it never finishes, the newest run in the store stops
// advancing and staleness reports it. That is what makes the refusal safe.
func TestStatusStaleCatchesARunNothingCanJudge(t *testing.T) {
	st, path := newStore(t)
	ctx := context.Background()
	begun, err := st.BeginRun(ctx, store.RunSpec{SourceID: claudeTranscripts, Input: "/Users/ben/.claude/projects"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	st.Close()

	var soon, later bytes.Buffer
	soonErr := reportStatus(&soon, statusOptions{
		store: path, snapshots: t.TempDir(), stale: 24 * time.Hour, now: begun.StartedAt.Add(time.Hour),
	})
	lateErr := reportStatus(&later, statusOptions{
		store: path, snapshots: t.TempDir(), stale: 24 * time.Hour, now: begun.StartedAt.Add(48 * time.Hour),
	})
	t.Logf("an hour in:\n%s\ntwo days in:\n%s", soon.String(), later.String())

	if soonErr != nil {
		t.Errorf("status failed an hour into the first run a source has ever had, which nothing "+
			"recorded can say is too long: %v", soonErr)
	}
	if strings.Contains(soon.String(), "died partway") {
		t.Errorf("the report presumes a first-ever run dead with no completed run to judge it by:\n%s", soon.String())
	}
	if lateErr == nil {
		t.Error("status exited zero two days after a run that never finished; staleness is the backstop " +
			"that makes refusing to judge it safe, and it did not fire")
	}
	if !strings.Contains(later.String(), "stale") {
		t.Errorf("the report does not use the word stale two days on:\n%s", later.String())
	}
}

// TestStatusCountsWhatIsPreservedOnDisk covers the two captures that produce no
// events and therefore no harvest_run row: the Motion vault and the filedays
// scan. Without this they are invisible to the report, which would leave the
// same hole one level over.
func TestStatusCountsWhatIsPreservedOnDisk(t *testing.T) {
	p := newPartialHarvest(t)
	if err := p.run(t, populatedSnapshot(t)); err != nil {
		t.Logf("harvest reported: %v", err)
	}

	var out bytes.Buffer
	_ = reportStatus(&out, statusOptions{store: p.storePath, snapshots: p.snapDir, stale: time.Hour, now: time.Now()})
	got := out.String()
	t.Logf("report:\n%s", got)

	for _, want := range []string{"knowledgeC", "Biome", "Motion vault", "filedays"} {
		if !strings.Contains(got, want) {
			t.Errorf("the report never mentions the %s capture:\n%s", want, got)
		}
	}
}

// FITTING THE TERMINAL. Ben's terminal is 80 columns, and every row of this
// table was 89 — so every line wrapped, and a table that wraps is not a table.
//
// The rule the fix follows: a column shrinks to its own content before any
// column loses a character, and the SOURCE NAME is the only thing allowed to
// lose one. Every measured column is a count, and a count clipped to two digits
// is a wrong number rather than a shortened one.
func TestTheStatusTableFitsAnEightyColumnTerminal(t *testing.T) {
	path := statusPerRepositoryRuns(t)

	var out bytes.Buffer
	_ = reportStatus(atWidth{&out, 80}, statusOptions{
		store: path, snapshots: t.TempDir(), stale: time.Hour, now: time.Now(),
	})
	got := out.String()
	t.Logf("report:\n%s", got)
	fits(t, 80, "status", got)
}

// Shortening a name until two rows read alike is worse than wrapping: four
// knowledgeC streams truncated to twelve columns are four identical lines, and
// a reader cannot tell which one is broken. The name column must therefore stop
// shrinking before it collapses the rows into each other.
func TestTheStatusTableKeepsEverySourceDistinguishableAtEightyColumns(t *testing.T) {
	st, path := newStore(t)
	st.Close()

	var out bytes.Buffer
	_ = reportStatus(atWidth{&out, 80}, statusOptions{
		store: path, snapshots: t.TempDir(), stale: time.Hour, now: time.Now(),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	seen := map[string]string{}
	for line := range strings.SplitSeq(got, "\n") {
		if !strings.Contains(line, "never run") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(strings.TrimSpace(line), "  ", 2)[0])
		if prior, dup := seen[name]; dup {
			t.Errorf("two sources both render as %q, so the table cannot say which one is "+
				"which:\n%s\n%s", name, prior, line)
		}
		seen[name] = line
	}
	if len(seen) < 4 {
		t.Fatalf("only %d never-run rows were found; the fixture is not exercising the table", len(seen))
	}
}

// A pipe has no width and MUST get every column. The primary reader of this CLI
// is an agent, and truncating what an agent reads corrupts it silently.
func TestTheStatusTableIsNeverShortenedIntoAPipe(t *testing.T) {
	path := statusPerRepositoryRuns(t)

	var out bytes.Buffer
	_ = reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), stale: time.Hour, now: time.Now(),
	})
	got := out.String()
	if strings.Contains(got, "…") {
		t.Errorf("piped output was truncated:\n%s", got)
	}
	if n, line := widest(got); n <= 80 {
		t.Errorf("the piped table's widest line is %d columns (%q); this fixture is supposed to "+
			"overflow eighty, so the test is not proving anything", n, line)
	}
}
