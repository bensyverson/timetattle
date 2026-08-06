package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// recordedStatus used to be two SQL queries against harvest_run, written in
// this package because internal/store had no reader. It is now Go over the rows
// Store.Runs returns, and this is the arithmetic that moved.
//
// It needs its own tests because the real corpus cannot exercise it: on the
// store this was reconciled against — 36 harvest_run rows across 8 sources —
// every source has succeeded and none has ever failed, so the counting below
// was reproduced exactly and proved nothing about the branch that matters. The
// incident `timetattle status` was written for was three consecutive failures.
//
// > Corrected 2026-08-01, when the aggregation below moved from rows to
// > harvests: that store now holds 55 rows across 11 sources, and 5 of them did
// > fail — biome-appusage and the four knowledgeC streams, when a reinstall
// > revoked Full Disk Access. Every one of those five belongs to a source with
// > a single input, so the corpus still cannot produce a group holding both a
// > success and a failure, and the partial branch is exercised nowhere but
// > here. See project/private/2026-08-01-per-repository-runs.md.

// statusRun is one harvest_run row, at a minute offset from a fixed base so
// that the order runs began is unambiguous.
//
// It leaves finished_at at the zero time, which is a run that never finished —
// so every row it builds is an invocation of its own, which is what the tests
// below it were written against.
func statusRun(id int64, source string, minute int, status store.Status, failure string) store.Run {
	base := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	return store.Run{
		ID:        id,
		SourceID:  source,
		StartedAt: base.Add(time.Duration(minute) * time.Minute),
		Status:    status,
		Error:     failure,
	}
}

// A source that runs once per repository writes one harvest_run row per
// repository, and the rows below are the shape of that. Taking the last row of
// the group and calling it the source's result reported 81 of 932 events seen
// on 2026-08-01, and — the half that matters — would have printed ok for a run
// in which four repositories out of five were lost.

// statusInput is one repository's row within an invocation.
type statusInput struct {
	input   string
	status  store.Status
	seen    int
	added   int
	failure string
}

// statusInvocation builds the rows one `timetattle harvest` writes for one
// source, one per input, chained: each run begins when the previous finished.
//
// That chaining is not decoration. It is what the ingestion loop does — the
// next BeginRun follows the previous Finish with nothing between them but a
// Fprintf — and it is the only thing in harvest_run that says which rows came
// out of the same invocation. Measured on the live store on 2026-08-01
// (scripts are not needed; the query is in the task): the largest gap between
// two consecutive runs of one invocation is 0.0014s, and the smallest gap
// between two invocations is 17.1s.
func statusInvocation(source string, at time.Time, inputs ...statusInput) []store.Run {
	out := make([]store.Run, 0, len(inputs))
	start := at
	for _, in := range inputs {
		finished := start.Add(20 * time.Millisecond)
		// A run still marked running never finished, and its row says so with
		// no finished_at — which is also what ends its invocation.
		recorded := finished
		if in.status == store.StatusRunning {
			recorded = time.Time{}
		}
		out = append(out, store.Run{
			SourceID:   source,
			Input:      in.input,
			StartedAt:  start,
			FinishedAt: recorded,
			Status:     in.status,
			Error:      in.failure,
			EventsSeen: in.seen,
			EventsNew:  in.added,
		})
		start = finished.Add(time.Millisecond)
	}
	return out
}

// statusLastStart is when the newest run of an invocation began, which is the
// instant the whole invocation is dated by: a harvest is as recent as the last
// thing it read.
func statusLastStart(invocation []store.Run) time.Time {
	return invocation[len(invocation)-1].StartedAt
}

// statusHistory concatenates invocations in the order they ran and numbers the
// rows, which is the order and the numbering Store.Runs guarantees.
func statusHistory(invocations ...[]store.Run) []store.Run {
	var out []store.Run
	for _, inv := range invocations {
		for _, run := range inv {
			run.ID = int64(len(out) + 1)
			out = append(out, run)
		}
	}
	return out
}

// gitlogCommits is the live 2026-08-01 08:30 harvest, repository by repository.
// The store holds 930 gitlog-commits events, which is exactly the sum of the
// new column here, and status reported 81 seen and 79 new.
func gitlogCommits() []statusInput {
	return []statusInput{
		{input: "/Users/ben/git/acorn", status: store.StatusOK, seen: 55, added: 55},
		{input: "/Users/ben/git/birch", status: store.StatusOK, seen: 484, added: 484},
		{input: "/Users/ben/git/cypress", status: store.StatusOK, seen: 294, added: 294},
		{input: "/Users/ben/git/dogwood", status: store.StatusOK, seen: 18, added: 18},
		{input: "/Users/ben/git/timetattle", status: store.StatusOK, seen: 81, added: 79},
	}
}

var statusNoon = time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

// statusLater is an instant far enough after every fixture below that any run
// still marked running in one of them has been there longer than any tolerance
// derived from 20ms rows. It is what "the harvest that wrote this is gone" looks
// like to the clock, and it is passed wherever a test means a run that died
// rather than a run in flight.
var statusLater = statusNoon.Add(24 * time.Hour)

// statusFinished is one completed run of a chosen duration, which is what the
// tolerance for a still-running run is derived from.
func statusFinished(source, input string, at time.Time, took time.Duration) []store.Run {
	return []store.Run{{
		SourceID:   source,
		Input:      input,
		StartedAt:  at,
		FinishedAt: at.Add(took),
		Status:     store.StatusOK,
	}}
}

// statusUnfinished is one run that began and has no finished_at: a harvest in
// flight and a harvest that died leave exactly the same row, which is the whole
// difficulty.
func statusUnfinished(source, input string, at time.Time) []store.Run {
	return []store.Run{{
		SourceID:  source,
		Input:     input,
		StartedAt: at,
		Status:    store.StatusRunning,
	}}
}

func TestRecordedStatusTotalsEveryRepositoryASourceRead(t *testing.T) {
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon, gitlogCommits()...))

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.seen != 932 || got.added != 930 {
		t.Errorf("seen=%d new=%d, want 932 and 930 — the total across all five repositories, "+
			"not the last repository's 81 and 79", got.seen, got.added)
	}
	if got.inputs != 5 {
		t.Errorf("inputs = %d, want 5; a reader cannot tell one repository from five without it", got.inputs)
	}
	if got.status != string(store.StatusOK) {
		t.Errorf("status = %q, want ok: every repository succeeded", got.status)
	}
	if got.failedInputs != 0 {
		t.Errorf("failedInputs = %d on a run where nothing failed", got.failedInputs)
	}
}

// The dangerous half. Four repositories succeeding and one failing must never
// render as ok, and the failure must not be hidden by where it happens to fall
// in the group: the broken repository here is NOT the last row, so the previous
// last-row-wins rule reported this exact history as a clean success.
func TestRecordedStatusIsPartialWhenOneRepositoryFailed(t *testing.T) {
	inputs := gitlogCommits()
	inputs[1] = statusInput{
		input:   "/Users/ben/git/birch",
		status:  store.StatusFailed,
		failure: "cannot read /Users/ben/git/birch/.git: operation not permitted",
	}
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon, inputs...))

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.status == string(store.StatusOK) {
		t.Fatal("status is ok on a run that lost a repository; that is the failure this task exists to end")
	}
	if got.status != statusPartial {
		t.Errorf("status = %q, want %q", got.status, statusPartial)
	}
	if got.failedInputs != 1 || got.inputs != 5 {
		t.Errorf("failedInputs=%d of inputs=%d, want 1 of 5", got.failedInputs, got.inputs)
	}
	// The survivors' evidence is still evidence, and is still counted.
	if got.seen != 448 || got.added != 446 {
		t.Errorf("seen=%d new=%d, want 448 and 446 — the four repositories that were read", got.seen, got.added)
	}
	if !strings.Contains(got.failure, "birch") {
		t.Errorf("failure = %q, and does not name the repository that broke", got.failure)
	}
}

func TestRecordedStatusIsFailedWhenEveryRepositoryFailed(t *testing.T) {
	inputs := gitlogCommits()
	for i := range inputs {
		inputs[i] = statusInput{input: inputs[i].input, status: store.StatusFailed, failure: "git: not found"}
	}
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon, inputs...))

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.status != string(store.StatusFailed) {
		t.Errorf("status = %q, want failed: no repository was read at all", got.status)
	}
	if got.failedInputs != 5 {
		t.Errorf("failedInputs = %d, want 5", got.failedInputs)
	}
}

// The mirror image of the understated count, and the reason the total is scoped
// to one invocation rather than to every row a source has ever written. Two
// harvests of the same five repositories saw 932 events each; a source that
// reported 1864 would be as wrong as one reporting 81, in the direction that
// looks like success.
func TestRecordedStatusDoesNotSumAcrossInvocations(t *testing.T) {
	second := statusInvocation("gitlog-commits", statusNoon.Add(12*time.Hour), gitlogCommits()...)
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon, gitlogCommits()...), second)

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.seen != 932 || got.added != 930 {
		t.Errorf("seen=%d new=%d across two harvests of the same five repositories, want 932 and 930", got.seen, got.added)
	}
	if got.inputs != 5 {
		t.Errorf("inputs = %d, want 5", got.inputs)
	}
	if !got.startedAt.Equal(statusLastStart(second)) {
		t.Errorf("startedAt = %s, want the newest run of the latest invocation", got.startedAt)
	}
}

// knowledgeC reads a snapshot whose filename carries the timestamp of the
// capture, so its input is different on every single harvest. Grouping a
// source's rows by input alone — rather than by the invocation that wrote them
// — would treat six harvests as six repositories and add up six overlapping
// reads of the same database.
func TestRecordedStatusDoesNotSumASourceWhoseInputChangesEveryRun(t *testing.T) {
	runs := statusHistory(
		statusInvocation("knowledgec-appusage", statusNoon,
			statusInput{input: "/snapshots/knowledgeC-20260801T080435.db.gz", status: store.StatusOK, seen: 11507, added: 100}),
		statusInvocation("knowledgec-appusage", statusNoon.Add(20*time.Minute),
			statusInput{input: "/snapshots/knowledgeC-20260801T082928.db.gz", status: store.StatusOK, seen: 11526, added: 9}),
	)

	got := recordedStatus(runs, statusLater)["knowledgec-appusage"]
	if got.seen != 11526 || got.added != 9 {
		t.Errorf("seen=%d new=%d, want the latest harvest's 11526 and 9 rather than two harvests added together",
			got.seen, got.added)
	}
	if got.inputs != 1 {
		t.Errorf("inputs = %d, want 1: one snapshot was read, not two repositories", got.inputs)
	}
}

// "Three failures since it last succeeded" is a count of harvests, and it is
// the figure that turns a broken source into a broken schedule. Counting rows
// instead would report a five-repository source that has failed three times as
// having failed fifteen.
func TestRecordedStatusCountsFailuresByHarvestNotByRepository(t *testing.T) {
	broken := gitlogCommits()
	for i := range broken {
		broken[i] = statusInput{input: broken[i].input, status: store.StatusFailed, failure: "git: not found"}
	}
	worked := statusInvocation("gitlog-commits", statusNoon, gitlogCommits()...)
	runs := statusHistory(
		worked,
		statusInvocation("gitlog-commits", statusNoon.Add(12*time.Hour), broken...),
		statusInvocation("gitlog-commits", statusNoon.Add(24*time.Hour), broken...),
		statusInvocation("gitlog-commits", statusNoon.Add(36*time.Hour), broken...),
	)

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.failedSince != 3 {
		t.Errorf("failedSince = %d, want 3 failed harvests rather than 15 failed repository reads", got.failedSince)
	}
	if !got.everWorked || !got.lastOK.Equal(statusLastStart(worked)) {
		t.Errorf("lastOK = %s (everWorked=%v), want the invocation in which every repository succeeded",
			got.lastOK, got.everWorked)
	}
}

// A run that lost one repository has not succeeded, so it cannot be the answer
// to "when did this last work". The distinction is the whole point of the
// partial state: a source stuck at four repositories of five for a week would
// otherwise report a fresh "last ok" every twelve hours.
func TestRecordedStatusDoesNotCountAPartialHarvestAsASuccess(t *testing.T) {
	broken := gitlogCommits()
	broken[0] = statusInput{input: broken[0].input, status: store.StatusFailed, failure: "git: not found"}
	worked := statusInvocation("gitlog-commits", statusNoon, gitlogCommits()...)
	runs := statusHistory(
		worked,
		statusInvocation("gitlog-commits", statusNoon.Add(12*time.Hour), broken...),
	)

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if !got.lastOK.Equal(statusLastStart(worked)) {
		t.Errorf("lastOK = %s, want the earlier harvest in which every repository was read", got.lastOK)
	}
	if got.failedSince != 1 {
		t.Errorf("failedSince = %d, want 1: the partial harvest is a failure of this source", got.failedSince)
	}
}

// The registry is a list of directories and nothing stops the same one being
// registered twice, which would plan two identical reads in one invocation. The
// second read sees the same commits as the first and adds nothing, so summing
// both would double the seen count and report evidence that was never there.
func TestRecordedStatusCountsARepositoryReadTwiceInOneHarvestOnce(t *testing.T) {
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon,
		statusInput{input: "/Users/ben/git/acorn", status: store.StatusOK, seen: 55, added: 55},
		statusInput{input: "/Users/ben/git/acorn", status: store.StatusOK, seen: 55, added: 0},
	))

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.inputs != 1 {
		t.Errorf("inputs = %d, want 1: one repository was registered twice, not two repositories", got.inputs)
	}
	if got.seen != 55 {
		t.Errorf("seen = %d, want 55 rather than one repository counted twice", got.seen)
	}
}

// A harvest that died on the fourth of five repositories leaves that row marked
// running for ever. The source read three repositories and did not read the
// other two, which is partial — and the row that says a process died must still
// be reachable, because it is a different diagnosis from a source that failed.
func TestRecordedStatusIsPartialWhenAHarvestDiedPartwayThroughTheRepositories(t *testing.T) {
	inputs := gitlogCommits()[:4]
	inputs[3] = statusInput{input: inputs[3].input, status: store.StatusRunning}
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon, inputs...))

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if got.status != statusPartial {
		t.Errorf("status = %q, want %q: three repositories were read and the fourth never finished",
			got.status, statusPartial)
	}
	if got.failedInputs != 0 {
		t.Errorf("failedInputs = %d; a run that never finished did not fail, and saying it did "+
			"hides a process that died", got.failedInputs)
	}
	// What the inputs column renders. Counting what was read rather than what
	// failed is the difference between 3/4 and 4/4 here, and 4/4 beside the
	// word partial is the report arguing with itself.
	if got.okInputs != 3 {
		t.Errorf("okInputs = %d of %d, want 3: the fourth repository was never read", got.okInputs, got.inputs)
	}
	var stuck bool
	for _, d := range got.detail {
		if d.input == inputs[3].input && d.status == statusStranded {
			stuck = true
		}
	}
	if !stuck {
		t.Errorf("no detail row says %s was left marked running: %+v", inputs[3].input, got.detail)
	}
}

// Everything below is the difference between a harvest that is running right
// now and one that died, which harvest_run cannot state and both of which are a
// row with no finished_at. Until 2026-08-01 the report called both of them "a
// harvest that died partway" and exited non-zero, so running `timetattle status`
// from a prompt while the 12-hourly job happened to be mid-run produced a
// failure that was not merely conservative but false. A check that cries wolf is
// a check nobody reads.
//
// The rule is a tolerance derived from the source's OWN completed runs, and the
// numbers it is calibrated against are the live store on 2026-08-01, 76
// harvest_run rows: for identical work on the same input, the observed spread
// between the fastest and slowest completed run reaches 6.11x —
// claude-transcripts read ~722,000 events on each of five runs and took between
// 12.403s and 75.752s to do it.

// One history, two instants, two answers. The row is identical in both — a
// started_at and no finished_at — so the only thing that can separate a harvest
// in flight from one that died is how long it has been there, and both readings
// are asserted together because either one alone is half the criterion.
//
// The dead reading is harvest_run row 28 of the live store: claude-transcripts,
// started 2026-08-01T13:07:13Z, no finished_at, stranded when its process died.
func TestRecordedStatusTellsARunInFlightFromARunThatDied(t *testing.T) {
	began := statusNoon.Add(time.Hour)
	runs := statusHistory(
		statusFinished(claudeTranscripts, "/Users/ben/.claude/projects", statusNoon, 75752*time.Millisecond),
		statusUnfinished(claudeTranscripts, "/Users/ben/.claude/projects", began),
	)

	live := recordedStatus(runs, began.Add(12*time.Second))[claudeTranscripts]
	if live.status != string(store.StatusRunning) {
		t.Errorf("status = %q twelve seconds in, want %q: the row says running and nothing yet contradicts it",
			live.status, store.StatusRunning)
	}
	dead := recordedStatus(runs, began.Add(2*time.Hour+23*time.Minute))[claudeTranscripts]
	if dead.status != statusStranded {
		t.Errorf("status = %q after 2h23m at running, want %q; the harvest that wrote it is long gone",
			dead.status, statusStranded)
	}
	if live.status == dead.status {
		t.Errorf("the same row reads %q at twelve seconds and at 2h23m; nothing in the report can then "+
			"tell a live harvest from a dead one", live.status)
	}
}

// The tolerance tracks the source rather than a number chosen today, which is
// what stops it crying wolf again as the transcript corpus grows. Both instants
// here are well past the floor, so only the derived part can separate them:
// 75.752s x 10 is 12m37.52s, and 12m is inside it while 13m is not.
func TestRecordedStatusDerivesTheToleranceFromTheSourcesOwnSlowestRun(t *testing.T) {
	began := statusNoon.Add(time.Hour)
	runs := statusHistory(
		statusFinished(claudeTranscripts, "/Users/ben/.claude/projects", statusNoon, 12403*time.Millisecond),
		statusFinished(claudeTranscripts, "/Users/ben/.claude/projects", statusNoon.Add(30*time.Minute), 75752*time.Millisecond),
		statusUnfinished(claudeTranscripts, "/Users/ben/.claude/projects", began),
	)

	if got := recordedStatus(runs, began.Add(12*time.Minute))[claudeTranscripts]; got.status != string(store.StatusRunning) {
		t.Errorf("status = %q after 12m, want running: ten times the slowest run of this source is 12m37s",
			got.status)
	}
	if got := recordedStatus(runs, began.Add(13*time.Minute))[claudeTranscripts]; got.status != statusStranded {
		t.Errorf("status = %q after 13m, want %q: that is past ten times the slowest run of this source",
			got.status, statusStranded)
	}
}

// A source whose runs take under a second gets the floor rather than ten times
// nothing. knowledgec-appusage's slowest completed run on the live store is
// 0.634s, and ten times that is 6.34s — a figure a loaded machine clears without
// anything being wrong. 60s is past it and must not be called dead.
func TestRecordedStatusGivesAFastSourceAFloorRatherThanTenTimesNothing(t *testing.T) {
	began := statusNoon.Add(time.Hour)
	runs := statusHistory(
		statusFinished(kcAppUsage, "/snapshots/knowledgeC-20260801T130435.db.gz", statusNoon, 634*time.Millisecond),
		statusUnfinished(kcAppUsage, "/snapshots/knowledgeC-20260801T140435.db.gz", began),
	)

	if got := recordedStatus(runs, began.Add(60*time.Second))[kcAppUsage]; got.status != string(store.StatusRunning) {
		t.Errorf("status = %q after 60s, want running: ten times 0.634s is 6.34s and that is far too tight to judge by",
			got.status)
	}
	if got := recordedStatus(runs, began.Add(6*time.Minute))[kcAppUsage]; got.status != statusStranded {
		t.Errorf("status = %q after 6m, want %q: that is past the floor", got.status, statusStranded)
	}
}

// Absence of a completed run is not an observation of a short one. The first
// harvest on a machine reads the whole corpus and is by far the slowest run that
// will ever happen, with nothing recorded to bound it — so there is no basis for
// presuming it dead, and the report must not invent one.
//
// Two sources, the same twelve hours at running, and only one of them judged.
// The difference is the precedent and nothing else, which is the whole claim;
// asserting the unjudged one alone would pass against a report that never
// presumed anything dead at all. Staleness is what catches the unjudged one if
// it never finishes, and TestStatusStaleCatchesARunNothingCanJudge holds that.
func TestRecordedStatusWillNotPresumeARunDeadWithNothingToJudgeItBy(t *testing.T) {
	began := statusNoon.Add(time.Hour)
	runs := statusHistory(
		statusFinished(kcAppUsage, "/snapshots/knowledgeC-20260801T130435.db.gz", statusNoon, 634*time.Millisecond),
		statusUnfinished(kcAppUsage, "/snapshots/knowledgeC-20260801T140435.db.gz", began),
		statusUnfinished(claudeTranscripts, "/Users/ben/.claude/projects", began),
	)

	got := recordedStatus(runs, began.Add(12*time.Hour))
	if got[kcAppUsage].status != statusStranded {
		t.Errorf("knowledgec-appusage status = %q twelve hours at running with a completed run behind it, want %q",
			got[kcAppUsage].status, statusStranded)
	}
	if got[claudeTranscripts].status == statusStranded {
		t.Errorf("claude-transcripts status = %q on the only run it has ever had; nothing recorded says how "+
			"long a first read of the whole corpus should take, and a guess dressed as a measurement is the bug",
			got[claudeTranscripts].status)
	}
	if got[claudeTranscripts].hasRunLimit {
		t.Error("hasRunLimit is true with no completed run to derive a limit from")
	}
}

// A source part-way through its repositories is not a source that lost them.
// This is the same false alarm wearing the other label: three repositories read
// and the fourth in flight used to render as partial and exit non-zero, which is
// what every live harvest of a five-repository machine looks like.
func TestRecordedStatusIsRunningNotPartialWhileAHarvestIsStillReadingRepositories(t *testing.T) {
	inputs := gitlogCommits()[:4]
	inputs[3] = statusInput{input: inputs[3].input, status: store.StatusRunning}
	// A completed harvest first, so the source has a tolerance to be judged
	// against at all; without one nothing below could be called dead either.
	runs := statusHistory(
		statusInvocation("gitlog-commits", statusNoon, gitlogCommits()...),
		statusInvocation("gitlog-commits", statusNoon.Add(time.Hour), inputs...),
	)

	got := recordedStatus(runs, statusNoon.Add(time.Hour).Add(3*time.Second))["gitlog-commits"]
	if got.status == statusPartial {
		t.Errorf("status = %q three seconds into a harvest; a repository still being read has not been lost",
			got.status)
	}
	if got.status != string(store.StatusRunning) {
		t.Errorf("status = %q, want running", got.status)
	}
	if got.okInputs != 3 {
		t.Errorf("okInputs = %d of %d, want 3 read so far", got.okInputs, got.inputs)
	}
}

// A repository that genuinely failed is still a failure while the harvest that
// hit it is still going. The in-flight rule may not swallow it: the run that
// failed has failed, whatever the process is doing now.
//
// This one was green during the red stage, because the rule it guards did not
// exist yet and the old code reached partial by a different route. It is kept as
// a mutation guard rather than a red test, and it is proved the way this project
// proves guards: dropping the failedInputs == 0 condition from the in-flight
// branch of statusAcross turns this from partial to running, which is a lost
// repository reported as work in progress.
func TestRecordedStatusStillReportsAFailureInsideAHarvestStillRunning(t *testing.T) {
	inputs := gitlogCommits()[:4]
	inputs[1] = statusInput{input: inputs[1].input, status: store.StatusFailed, failure: "operation not permitted"}
	inputs[3] = statusInput{input: inputs[3].input, status: store.StatusRunning}
	runs := statusHistory(
		statusInvocation("gitlog-commits", statusNoon, gitlogCommits()...),
		statusInvocation("gitlog-commits", statusNoon.Add(time.Hour), inputs...),
	)

	got := recordedStatus(runs, statusNoon.Add(time.Hour).Add(3*time.Second))["gitlog-commits"]
	if got.status != statusPartial {
		t.Errorf("status = %q, want %q: one repository was refused, and a harvest still running "+
			"does not un-fail it", got.status, statusPartial)
	}
	if got.failedInputs != 1 {
		t.Errorf("failedInputs = %d, want 1", got.failedInputs)
	}
}

// Per-repository detail is the answer to the only question worth asking once
// something is broken, and it has to survive the aggregation to be printed.
func TestRecordedStatusKeepsEachRepositorySeparately(t *testing.T) {
	inputs := gitlogCommits()
	inputs[1] = statusInput{input: "/Users/ben/git/birch", status: store.StatusFailed, failure: "operation not permitted"}
	runs := statusHistory(statusInvocation("gitlog-commits", statusNoon, inputs...))

	got := recordedStatus(runs, statusLater)["gitlog-commits"]
	if len(got.detail) != 5 {
		t.Fatalf("detail covers %d repositories, want 5", len(got.detail))
	}
	var broken statusInputStatus
	for _, d := range got.detail {
		if d.input == "/Users/ben/git/birch" {
			broken = d
		}
	}
	if broken.status != string(store.StatusFailed) {
		t.Errorf("the detail for birch says %q, want failed", broken.status)
	}
	if !strings.Contains(broken.failure, "not permitted") {
		t.Errorf("the detail for birch does not say why it failed: %q", broken.failure)
	}
}

func TestRecordedStatusCountsTheFailuresSinceASourceLastSucceeded(t *testing.T) {
	// The incident exactly: worked, then failed three times running.
	runs := []store.Run{
		statusRun(1, "knowledgec-appusage", 0, store.StatusOK, ""),
		statusRun(2, "knowledgec-appusage", 60, store.StatusFailed, "operation not permitted"),
		statusRun(3, "knowledgec-appusage", 120, store.StatusFailed, "operation not permitted"),
		statusRun(4, "knowledgec-appusage", 180, store.StatusFailed, "operation not permitted"),
	}

	got := recordedStatus(runs, statusLater)["knowledgec-appusage"]
	if !got.ran {
		t.Fatal("ran is false for a source with four recorded runs")
	}
	if got.status != string(store.StatusFailed) {
		t.Errorf("status = %q, want the LATEST run's failed", got.status)
	}
	if got.failedSince != 3 {
		t.Errorf("failedSince = %d, want 3", got.failedSince)
	}
	if !got.everWorked {
		t.Error("everWorked is false for a source that succeeded an hour before it broke")
	}
	if want := runs[0].StartedAt; !got.lastOK.Equal(want) {
		t.Errorf("lastOK = %s, want %s", got.lastOK, want)
	}
}

// A source that has never once succeeded and a source that broke this morning
// are different diagnoses: the first is a wiring problem that has cost nothing,
// the second is evidence draining out of a window that does not refill. Every
// failure counts for the first, which is the true answer rather than zero.
func TestRecordedStatusCountsEveryFailureWhenASourceHasNeverSucceeded(t *testing.T) {
	runs := []store.Run{
		statusRun(1, "motion-autosave", 0, store.StatusFailed, "no vault"),
		statusRun(2, "motion-autosave", 60, store.StatusFailed, "no vault"),
	}

	got := recordedStatus(runs, statusLater)["motion-autosave"]
	if got.everWorked {
		t.Error("everWorked is true for a source that has never succeeded")
	}
	if !got.lastOK.IsZero() {
		t.Errorf("lastOK = %s on a source that has never succeeded; the zero time is what says so", got.lastOK)
	}
	if got.failedSince != 2 {
		t.Errorf("failedSince = %d, want both failures counted", got.failedSince)
	}
}

// Failures BEFORE the last success are history, not the current outage.
func TestRecordedStatusIgnoresFailuresOlderThanTheLastSuccess(t *testing.T) {
	runs := []store.Run{
		statusRun(1, "biome-appusage", 0, store.StatusFailed, "gone"),
		statusRun(2, "biome-appusage", 60, store.StatusFailed, "gone"),
		statusRun(3, "biome-appusage", 120, store.StatusOK, ""),
		statusRun(4, "biome-appusage", 180, store.StatusFailed, "gone again"),
	}

	got := recordedStatus(runs, statusLater)["biome-appusage"]
	if got.failedSince != 1 {
		t.Errorf("failedSince = %d, want only the failure after the success", got.failedSince)
	}
	if want := runs[2].StartedAt; !got.lastOK.Equal(want) {
		t.Errorf("lastOK = %s, want %s", got.lastOK, want)
	}
}

// The latest run is the last one in the order they began, which is what
// Store.Runs returns them in. Taking the first, or the newest success, would
// report a source that broke this afternoon as healthy.
func TestRecordedStatusTakesTheLatestRunOfEachSource(t *testing.T) {
	runs := []store.Run{
		statusRun(1, "biome-appusage", 0, store.StatusOK, ""),
		statusRun(2, "kc-backlight", 0, store.StatusOK, ""),
		statusRun(3, "biome-appusage", 60, store.StatusRunning, ""),
	}

	got := recordedStatus(runs, statusLater)
	if len(got) != 2 {
		t.Fatalf("recordedStatus covered %d sources, want 2", len(got))
	}
	if got["biome-appusage"].status != string(store.StatusRunning) {
		t.Errorf("biome-appusage status = %q, want the latest run's running",
			got["biome-appusage"].status)
	}
	if want := runs[2].StartedAt; !got["biome-appusage"].startedAt.Equal(want) {
		t.Errorf("biome-appusage startedAt = %s, want the latest run's %s",
			got["biome-appusage"].startedAt, want)
	}
	if got["kc-backlight"].status != string(store.StatusOK) {
		t.Errorf("kc-backlight status = %q, want ok", got["kc-backlight"].status)
	}
}

// Absence of a row is not an observation. No rows means no sources, and never a
// map full of zeros that would render as measurements.
func TestRecordedStatusReportsNothingWhenNothingHasRun(t *testing.T) {
	if got := recordedStatus(nil, statusLater); len(got) != 0 {
		t.Errorf("recordedStatus(nil, statusLater) covered %d sources, want none", len(got))
	}
}
