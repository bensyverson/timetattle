package main

import (
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

func at(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func claim(start, end string, task, db string) source.Event {
	ev := source.NewInterval(at(start), at(end), source.Identity{Document: task, Project: "p"}, "job-claims")
	ev.State = "claim"
	ev.Locator = db
	return ev
}

func jobEvent(atStr, task, db, typ string) source.Event {
	ev := source.NewPoint(at(atStr), source.Identity{Document: task, Project: "p"}, "job-events")
	ev.State = typ
	ev.Locator = db
	return ev
}

func backlit(start, end, state string) source.Event {
	ev := source.NewInterval(at(start), at(end), source.Identity{}, "knowledgec-backlit")
	ev.State = state
	return ev
}

// TestTerminationOf is the three-state rule the brief demands: a claim ended by
// `done` is an OBSERVATION that the work finished, a claim ended by `released`
// or `canceled` is an OBSERVATION that it was given up, and a claim with no
// terminator recorded at its end is NEITHER. Collapsing the last two is the bug
// this project has shipped three times.
func TestTerminationOf(t *testing.T) {
	db := "/x/.jobs.db"
	events := []source.Event{
		jobEvent("2026-07-10T10:30:00Z", "a", db, "done"),
		jobEvent("2026-07-10T11:30:00Z", "b", db, "released"),
		jobEvent("2026-07-10T12:30:00Z", "c", db, "canceled"),
		// `job done` in the newer tool writes both rows at one instant. That
		// is one act of finishing, not a finish and an abandonment.
		jobEvent("2026-07-10T13:30:00Z", "d", db, "done"),
		jobEvent("2026-07-10T13:30:00Z", "d", db, "released"),
		// A terminator on ANOTHER database's task with the same short id must
		// not terminate this one. Short ids collide freely across projects.
		jobEvent("2026-07-10T14:30:00Z", "e", "/y/.jobs.db", "done"),
	}
	index := newTerminators(events)

	cases := []struct {
		name string
		c    source.Event
		want termination
	}{
		{"done", claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", db), termDone},
		{"released", claim("2026-07-10T11:00:00Z", "2026-07-10T11:30:00Z", "b", db), termAbandoned},
		{"canceled", claim("2026-07-10T12:00:00Z", "2026-07-10T12:30:00Z", "c", db), termAbandoned},
		{"done and released at one instant", claim("2026-07-10T13:00:00Z", "2026-07-10T13:30:00Z", "d", db), termDone},
		{"terminator belongs to another database", claim("2026-07-10T14:00:00Z", "2026-07-10T14:30:00Z", "e", db), termUnobserved},
		{"nothing recorded at the end", claim("2026-07-10T15:00:00Z", "2026-07-10T15:30:00Z", "f", db), termUnobserved},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := terminationOf(tc.c, index); got != tc.want {
				t.Errorf("terminationOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestTerminationOfUnbounded: a claim that expired is UNBOUNDED in the store.
// An expiry says only that nobody spoke, and its timestamp is when some later
// command happened to notice.
func TestTerminationOfUnbounded(t *testing.T) {
	ev := source.NewOpenEnd(at("2026-07-10T10:00:00Z"), source.UnknownLost, source.Identity{Document: "a"}, "job-claims")
	ev.Locator = "/x/.jobs.db"
	if got := terminationOf(ev, newTerminators(nil)); got != termUnobserved {
		t.Errorf("terminationOf(lost) = %q, want %q", got, termUnobserved)
	}
}

// TestPresenceOf is the second three-state rule, and the one the sleepcheck
// clipping bug was about. Never sampled is not the same fact as sampled dark.
func TestPresenceOf(t *testing.T) {
	slack := time.Minute

	cases := []struct {
		name    string
		c       source.Event
		samples []source.Event
		want    presenceVerdict
	}{
		{
			name: "lit throughout",
			c:    claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", "/x/.jobs.db"),
			samples: []source.Event{
				backlit("2026-07-10T09:00:00Z", "2026-07-10T11:00:00Z", "on"),
			},
			want: presenceLit,
		},
		{
			name: "screen observed dark in the middle",
			c:    claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", "/x/.jobs.db"),
			samples: []source.Event{
				backlit("2026-07-10T09:00:00Z", "2026-07-10T10:10:00Z", "on"),
				backlit("2026-07-10T10:10:00Z", "2026-07-10T10:25:00Z", "off"),
				backlit("2026-07-10T10:25:00Z", "2026-07-10T11:00:00Z", "on"),
			},
			want: presenceDark,
		},
		{
			name:    "sampler silent for the whole claim",
			c:       claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", "/x/.jobs.db"),
			samples: nil,
			want:    presenceUnsampled,
		},
		{
			// THE REGRESSION FOR THE CLIPPED WINDOW. The sampler stops 26
			// seconds into the claim. A tool that clips to the span the two
			// sources share would call this fully lit; it is 29m34s of
			// nothing recorded.
			name: "sampling stops inside the claim",
			c:    claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", "/x/.jobs.db"),
			samples: []source.Event{
				backlit("2026-07-10T09:00:00Z", "2026-07-10T10:00:26Z", "on"),
			},
			want: presenceUnsampled,
		},
		{
			// Unsampled outranks dark: if part of the span was never sampled
			// we cannot assert the person was there, whatever the sampled
			// part says.
			name: "both a dark stretch and an unsampled stretch",
			c:    claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", "/x/.jobs.db"),
			samples: []source.Event{
				backlit("2026-07-10T09:00:00Z", "2026-07-10T10:05:00Z", "on"),
				backlit("2026-07-10T10:05:00Z", "2026-07-10T10:20:00Z", "off"),
			},
			want: presenceUnsampled,
		},
		{
			// A hole shorter than the slack is sampling jitter, not an
			// absence, and must not disqualify the claim.
			name: "hole inside the slack",
			c:    claim("2026-07-10T10:00:00Z", "2026-07-10T10:30:00Z", "a", "/x/.jobs.db"),
			samples: []source.Event{
				backlit("2026-07-10T09:00:00Z", "2026-07-10T10:10:00Z", "on"),
				backlit("2026-07-10T10:10:30Z", "2026-07-10T11:00:00Z", "on"),
			},
			want: presenceLit,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := presenceOf(tc.c, tc.samples, slack)
			if got != tc.want {
				t.Errorf("presenceOf = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestPresenceOfShortClaimCannotPassOnNothing is a regression for a defect this
// tool shipped in its first run. The slack is an ABSOLUTE duration, so a claim
// shorter than the slack has unsampled time below the slack whatever the data
// says — including when there is no data at all. 243 claims survived the first
// cascade and the surviving subset was reported as reaching back to 2026-04-20,
// which is ten weeks before knowledgec-backlit has a single row. Vacuously.
func TestPresenceOfShortClaimCannotPassOnNothing(t *testing.T) {
	cases := []struct {
		name    string
		c       source.Event
		samples []source.Event
	}{
		{
			name:    "claim shorter than the slack, no sample at all",
			c:       claim("2026-04-20T18:01:55Z", "2026-04-20T18:02:09Z", "a", "/x/.jobs.db"),
			samples: nil,
		},
		{
			// Sampling covers a THIRD of the span. Absolute slack alone would
			// wave this through; a third of a reading is not a corroboration.
			name: "minority of the span sampled",
			c:    claim("2026-07-10T10:00:00Z", "2026-07-10T10:01:30Z", "a", "/x/.jobs.db"),
			samples: []source.Event{
				backlit("2026-07-10T10:00:00Z", "2026-07-10T10:00:30Z", "on"),
			},
		},
		{
			name:    "zero-length claim, claimed and done in one second",
			c:       claim("2026-07-10T10:00:00Z", "2026-07-10T10:00:00Z", "a", "/x/.jobs.db"),
			samples: []source.Event{backlit("2026-07-10T09:00:00Z", "2026-07-10T11:00:00Z", "on")},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, _, _ := presenceOf(tc.c, tc.samples, time.Minute)
			if got != presenceUnsampled {
				t.Errorf("presenceOf = %q, want %q", got, presenceUnsampled)
			}
		})
	}
}

// TestPresenceOfReportsUnwindowedDurations: the verdict alone hides the size of
// the problem, and the size is what a reader needs to judge it.
func TestPresenceOfReportsUnwindowedDurations(t *testing.T) {
	c := claim("2026-07-10T10:00:00Z", "2026-07-10T11:00:00Z", "a", "/x/.jobs.db")
	samples := []source.Event{
		backlit("2026-07-10T09:00:00Z", "2026-07-10T10:20:00Z", "on"),
		backlit("2026-07-10T10:20:00Z", "2026-07-10T10:35:00Z", "off"),
		// 10:35 to 10:50 is never sampled at all.
		backlit("2026-07-10T10:50:00Z", "2026-07-10T11:30:00Z", "on"),
	}
	_, unsampled, dark := presenceOf(c, samples, time.Minute)
	if want := 15 * time.Minute; unsampled != want {
		t.Errorf("unsampled = %s, want %s", unsampled, want)
	}
	if want := 15 * time.Minute; dark != want {
		t.Errorf("dark = %s, want %s", dark, want)
	}
}

// TestIsCoEmitted guards the lesson the first claim-lag measurement paid for: a
// source can be co-emitted with the act being measured without being that
// source. `job claim` WRITES .jobs.db, and internal/fsobserve observes it.
func TestIsCoEmitted(t *testing.T) {
	cases := []struct {
		locator string
		want    bool
	}{
		{"/Users/you/git/acorn/.jobs.db", true},
		{"/Users/you/git/acorn/.jobs.db-wal", true},
		{"/Users/you/git/acorn/.jobs.db-shm", true},
		{"/Users/you/git/acorn/internal/jobs.go", false},
		{"/Users/you/git/acorn/docs/jobs.dbdesign.md", false},
		{"", false},
	}
	for _, tc := range cases {
		ev := source.NewPoint(at("2026-07-10T10:00:00Z"), source.Identity{}, "fs-observations")
		ev.Locator = tc.locator
		if got := isCoEmitted(ev); got != tc.want {
			t.Errorf("isCoEmitted(%q) = %v, want %v", tc.locator, got, tc.want)
		}
	}
}

// TestCascadeIsMonotone: every stage is a filter, so no stage may ever admit a
// claim an earlier one rejected. If that stops holding, a "surviving fraction"
// stops meaning anything.
func TestCascadeIsMonotone(t *testing.T) {
	rows := []row{
		{term: termDone, artifacts: 3, presence: presenceLit, dur: 5 * time.Minute},
		{term: termDone, artifacts: 0, presence: presenceLit, dur: 5 * time.Minute},
		{term: termAbandoned, artifacts: 3, presence: presenceLit, dur: 5 * time.Minute},
		{term: termUnobserved, artifacts: 3, presence: presenceUnsampled, dur: 5 * time.Minute},
		{term: termDone, artifacts: 3, presence: presenceDark, dur: 5 * time.Minute},
		{term: termDone, artifacts: 3, presence: presenceLit, dur: 3 * time.Hour},
	}
	stages := cascade(rows, time.Hour, ruleLit)
	if len(stages) < 2 {
		t.Fatalf("cascade returned %d stages, want at least 2", len(stages))
	}
	for i := 1; i < len(stages); i++ {
		if stages[i].n > stages[i-1].n {
			t.Errorf("stage %q admits %d, more than stage %q at %d",
				stages[i].name, stages[i].n, stages[i-1].name, stages[i-1].n)
		}
	}
	if last := stages[len(stages)-1]; last.n != 1 {
		t.Errorf("final survivors = %d, want 1", last.n)
	}
}

// TestCascadeHonoursThePresenceRule. "Corroborated" and "not contradicted" are
// different claims about the world and they select different subsets. The
// strict rule needs the screen observed lit; the loose rule only needs it never
// observed dark, which admits every claim the sampler never reached. Both are
// defensible and they must not be silently interchangeable.
func TestCascadeHonoursThePresenceRule(t *testing.T) {
	rows := []row{
		{term: termDone, artifacts: 1, presence: presenceLit, dur: time.Minute},
		{term: termDone, artifacts: 1, presence: presenceUnsampled, dur: time.Minute},
		{term: termDone, artifacts: 1, presence: presenceDark, dur: time.Minute},
	}
	strict := cascade(rows, time.Hour, ruleLit)
	if got := strict[len(strict)-1].n; got != 1 {
		t.Errorf("survivors under %q = %d, want 1", ruleLit, got)
	}
	loose := cascade(rows, time.Hour, ruleNotDark)
	if got := loose[len(loose)-1].n; got != 2 {
		t.Errorf("survivors under %q = %d, want 2", ruleNotDark, got)
	}
}

// TestCascadeWithNoLengthCutoff. The cutoff turned out not to survive its own
// derivation — see the length profile in the file header — so it ships
// disabled, and disabled has to mean "admit everything" rather than "admit
// nothing", which is what a naive `dur <= 0` comparison would do.
func TestCascadeWithNoLengthCutoff(t *testing.T) {
	rows := []row{
		{term: termDone, artifacts: 1, presence: presenceLit, dur: 3 * time.Minute},
		{term: termDone, artifacts: 1, presence: presenceLit, dur: 3 * time.Hour},
	}
	off := cascade(rows, 0, ruleLit)
	if got := off[len(off)-1].n; got != 2 {
		t.Errorf("survivors with the cutoff disabled = %d, want 2", got)
	}
	on := cascade(rows, time.Hour, ruleLit)
	if got := on[len(on)-1].n; got != 1 {
		t.Errorf("survivors with a 1h cutoff = %d, want 1", got)
	}
}

// TestArtifactSet. The artifact sources have to be selectable, because they are
// not equally durable and a lead measured against one is not the same
// measurement as a lead measured against another. gitlog-commits records an
// instant permanently; fs-observations reads a file's CURRENT mtime, and its
// first harvest in the live store ran on 2026-08-01, so everything it holds
// from before then is a single snapshot in which every earlier save of a file
// has been overwritten by the last one.
//
// An unknown name must be an ERROR. A typo that silently yields zero artifacts
// would report every claim as artifact-free, which is the silent-zero bug this
// project has shipped in six tools.
func TestArtifactSet(t *testing.T) {
	got, err := artifactSet("gitlog-commits,gitlog-reflog", false)
	if err != nil {
		t.Fatalf("artifactSet: %v", err)
	}
	if len(got) != 2 || !got["gitlog-commits"] || !got["gitlog-reflog"] {
		t.Errorf("artifactSet = %v, want exactly the two gitlog sources", got)
	}

	withT, err := artifactSet("gitlog-commits", true)
	if err != nil {
		t.Fatalf("artifactSet: %v", err)
	}
	if !withT["claude-transcripts"] {
		t.Error("-with-transcripts did not add claude-transcripts")
	}

	if _, err := artifactSet("gitlog-comits", false); err == nil {
		t.Error("a misspelled source was accepted; it would report every claim artifact-free")
	}
	if _, err := artifactSet("knowledgec-backlit", false); err == nil {
		t.Error("a presence source was accepted as an artifact source")
	}
	if _, err := artifactSet("", false); err == nil {
		t.Error("an empty source list was accepted; it can only ever measure nothing")
	}
}

// TestCheckPresenceReadings. presenceOf calls an interval "lit" only when its
// State reads "on", which is knowledgec-backlit's vocabulary. Point a
// -presence-sources at biome-appusage, whose State is not a backlight reading
// at all, and every interval becomes SAMPLED-BUT-NOT-LIT — the live store then
// reports 433 claims as observed-dark and three survivors, which looks like a
// devastating finding and is a units error. A source whose readings this tool
// cannot interpret has to be refused by name.
func TestCheckPresenceReadings(t *testing.T) {
	backlitEvents := []source.Event{
		backlit("2026-07-10T09:00:00Z", "2026-07-10T10:00:00Z", "on"),
		backlit("2026-07-10T10:00:00Z", "2026-07-10T11:00:00Z", "off"),
	}
	if err := checkPresenceReadings([]string{"knowledgec-backlit"}, backlitEvents); err != nil {
		t.Errorf("knowledgec-backlit refused: %v", err)
	}

	appusage := source.NewInterval(at("2026-07-10T09:00:00Z"), at("2026-07-10T10:00:00Z"),
		source.Identity{App: "com.apple.Safari"}, "biome-appusage")
	appusage.State = ""
	err := checkPresenceReadings([]string{"biome-appusage"}, []source.Event{appusage})
	if err == nil {
		t.Fatal("biome-appusage was accepted; every interval would be scored as observed-dark")
	}
	if !strings.Contains(err.Error(), "biome-appusage") {
		t.Errorf("error does not name the source: %v", err)
	}

	// A source named but never seen is its own failure: it can only ever
	// report every claim as unsampled, which would look like a finding.
	if err := checkPresenceReadings([]string{"knowledgec-backlit"}, nil); err == nil {
		t.Error("a presence source with no events at all was accepted")
	}
}

// TestInWindow. The strict and loose presence rules select subsets covering
// DIFFERENT DATES — knowledgec-backlit's first row is 2026-07-02 and claims
// begin 2026-04-20 — so comparing their medians directly compares the rules
// and the calendar at once. This project's rule is that two figures nobody has
// subtracted are not a finding, so the window has to be settable.
func TestInWindow(t *testing.T) {
	rows := []row{
		{start: at("2026-04-20T18:00:00Z")},
		{start: at("2026-07-10T18:00:00Z")},
		{start: at("2026-08-01T18:00:00Z")},
	}
	cases := []struct {
		name         string
		since, until time.Time
		want         int
	}{
		{"no window", time.Time{}, time.Time{}, 3},
		{"since only", at("2026-07-02T00:00:00Z"), time.Time{}, 2},
		{"until only", time.Time{}, at("2026-07-31T00:00:00Z"), 2},
		{"both", at("2026-07-02T00:00:00Z"), at("2026-07-31T00:00:00Z"), 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(inWindow(rows, tc.since, tc.until)); got != tc.want {
				t.Errorf("inWindow = %d rows, want %d", got, tc.want)
			}
		})
	}
}

// TestLengthBucket: the cutoff on claim length has to be DERIVED, and the
// derivation is the rate at which a claim of a given length turns out to have
// been held across an observed absence. That needs stable buckets.
func TestLengthBucket(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{30 * time.Second, "0-5m"},
		{5 * time.Minute, "0-5m"},
		{5*time.Minute + time.Second, "5-15m"},
		{15 * time.Minute, "5-15m"},
		{20 * time.Minute, "15-30m"},
		{45 * time.Minute, "30-60m"},
		{90 * time.Minute, "60m+"},
	}
	for _, tc := range cases {
		if got := lengthBucket(tc.d); got != tc.want {
			t.Errorf("lengthBucket(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestCascadeDropIsAMutation: dropping one stage must be a measurement of what
// that stage is worth, not a no-op. A filter that changes nothing when removed
// is a filter that is not doing anything, and this is how that gets caught.
func TestCascadeDropIsAMutation(t *testing.T) {
	rows := []row{
		{term: termDone, artifacts: 3, presence: presenceLit, dur: 5 * time.Minute},
		{term: termAbandoned, artifacts: 3, presence: presenceLit, dur: 5 * time.Minute},
	}
	full := cascade(rows, time.Hour, ruleLit)
	dropped := cascadeWithout(rows, time.Hour, ruleLit, stageFinished)

	if full[len(full)-1].n != 1 {
		t.Fatalf("full cascade survivors = %d, want 1", full[len(full)-1].n)
	}
	if got := dropped[len(dropped)-1].n; got != 2 {
		t.Errorf("survivors without the finished stage = %d, want 2", got)
	}
	for _, s := range dropped {
		if s.name == stageFinished {
			t.Errorf("cascadeWithout still ran stage %q", stageFinished)
		}
	}
}
