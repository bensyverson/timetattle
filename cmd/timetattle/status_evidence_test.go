package main

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/store"

	_ "modernc.org/sqlite"
)

// WHETHER EVIDENCE ARRIVED, which is the question status could not ask.
//
// The defect, measured on the live store on 2026-08-13 at 15:04Z: the newest
// harvest run was 1h59m old, the newest event was 12h12m old, and the report
// read "every source ran and succeeded within the last 6h00m, and every capture
// held" and exited zero. Every word of that was true and none of it was the
// thing a reader wanted to know.
//
// The whole difficulty is that twelve hours of no evidence at ten in the
// morning is an ordinary night and the same twelve hours at two in the
// afternoon is a collector that has died, and nothing in the store separates
// them by the clock. So these tests come in pairs: a quiet stretch nobody
// recorded through must raise nothing, and a source silent while others record
// must be reported.

// THE FIRST HALF OF THE CRITERION. Everything stops together overnight, the
// harvest goes on running to schedule, and the report must not invent thirteen
// stalled sources out of a night's sleep. This is the shape that would fire
// every single morning if silence were measured against the clock.
func TestAQuietNightRaisesNoSilenceAtAll(t *testing.T) {
	f := statusEvidence(t, evidenceFixture{
		// A working day: both sources record together, hour by hour, so that
		// each has a long history of very short quiet spells.
		events: map[string][]time.Duration{
			biomeAppusage:     workingDay(0, 9*time.Hour, 5*time.Minute),
			claudeTranscripts: workingDay(0, 9*time.Hour, 5*time.Minute),
			knowledgecBacklit: workingDay(0, 9*time.Hour, 5*time.Minute),
		},
		// The harvest carries on through the night, exactly as the installed
		// four-hourly agent does, and finds nothing each time.
		harvests: []time.Duration{13 * time.Hour, 17 * time.Hour, 21 * time.Hour},
	})

	// Half past midnight, twelve and a half hours after the last thing anybody
	// recorded and half an hour after a harvest that ingested nothing.
	out, err := f.report(t, 21*time.Hour+30*time.Minute)
	t.Logf("a quiet night:\n%s", out)

	if err != nil {
		t.Errorf("status failed over a night in which nothing was recorded anywhere: %v", err)
	}
	if strings.Contains(out, "recorded nothing while the store went on advancing") {
		t.Errorf("a source was reported as silent across a night in which NO source recorded, so "+
			"the frontier never moved and nothing witnessed anybody's silence:\n%s", out)
	}
	// And the reading must still be there. Refusing to raise a fault is not the
	// same as saying nothing: the twelve hours are the figure a reader came for,
	// and the report must hand it over along with the reason it will not judge it.
	if !strings.Contains(out, "12h30m") {
		t.Errorf("the report never says how old the newest evidence is, which is the figure that "+
			"was missing on 2026-08-13:\n%s", out)
	}
	if !strings.Contains(out, "idle") {
		t.Errorf("the report does not say that an idle machine and a stalled collection are not "+
			"distinguishable in this record:\n%s", out)
	}
}

// THE SECOND HALF, AND THE ONE THAT MUST FIRE. knowledgec-appusage stopped on
// the live store at 2026-08-11T00:28:45Z while its three sibling knowledgeC
// streams — read out of the same snapshot, in the same invocations, all of them
// status ok — went on recording to 2026-08-13T02:52:12Z. That is not an absence:
// the other sources are the witness that the machine was alive and this one was
// silent through it.
//
// The fixture is that SHAPE and not that store. A test that read the live store
// would pass or fail on what happened to be on this machine this morning.
func TestASourceSilentWhileOthersRecordIsReported(t *testing.T) {
	f := statusEvidence(t, evidenceFixture{
		events: map[string][]time.Duration{
			// Two sources record right through, hourly.
			biomeAppusage:     workingDay(0, 40*time.Hour, 15*time.Minute),
			knowledgecBacklit: workingDay(0, 40*time.Hour, 15*time.Minute),
			// The third keeps up for the first four hours and then stops.
			claudeTranscripts: workingDay(0, 4*time.Hour, 5*time.Minute),
		},
		harvests: []time.Duration{40 * time.Hour},
	})

	out, err := f.report(t, 40*time.Hour+30*time.Minute)
	t.Logf("one source silent while two others record:\n%s", out)

	if !strings.Contains(out, "recorded nothing while the store went on advancing") {
		t.Errorf("a source that said nothing for 36 hours while two others recorded every quarter "+
			"of an hour was not reported:\n%s", out)
	}
	if !strings.Contains(out, claudeTranscripts) {
		t.Errorf("the report never names the source that stopped:\n%s", out)
	}
	if strings.Contains(out, biomeAppusage+":") {
		t.Errorf("a source that is recording every quarter of an hour was reported as silent:\n%s", out)
	}
	// It states the reading and refuses the diagnosis, exactly as the stale
	// verdict does. Nothing in the store says whether a source had nothing to
	// say or has stopped working, and a report that picked one would be
	// concluding something it never saw.
	if !strings.Contains(out, "nothing to say") {
		t.Errorf("the report asserts a cause it cannot have observed, or does not say it cannot:\n%s", out)
	}
	if strings.Contains(out, "every source ran and succeeded") {
		t.Errorf("the unqualified all-clear was printed over a source that has stopped recording:\n%s", out)
	}
	// The exit status is deliberately unchanged: see statusQuietSlack's own
	// comment, and the note on job qeuMF. This pins the decision so that
	// changing it is a deliberate act with a test to update.
	if err != nil {
		t.Errorf("status set the exit status on a silent source, which is a change to its "+
			"documented contract that was not asked for: %v", err)
	}
}

// THE MUTATION, and it is what proves the rule is not merely "no evidence is a
// fault". The same two stores are judged with the witness taken away: every
// source stops together. The first store must still report and the second must
// not, or the check cannot tell an idle machine from a stalled one and the whole
// construction is decoration.
func TestSilenceIsReportedOnlyWhileSomethingElseIsRecording(t *testing.T) {
	witnessed := statusEvidence(t, evidenceFixture{
		events: map[string][]time.Duration{
			biomeAppusage:     workingDay(0, 40*time.Hour, 15*time.Minute),
			claudeTranscripts: workingDay(0, 4*time.Hour, 5*time.Minute),
		},
		harvests: []time.Duration{40 * time.Hour},
	})
	// The identical history with the witness removed: biome stops when
	// claude-transcripts does, so the store's frontier freezes at 4h and the 36
	// hours that follow are hours nobody recorded through.
	unwitnessed := statusEvidence(t, evidenceFixture{
		events: map[string][]time.Duration{
			biomeAppusage:     workingDay(0, 4*time.Hour, 15*time.Minute),
			claudeTranscripts: workingDay(0, 4*time.Hour, 5*time.Minute),
		},
		harvests: []time.Duration{40 * time.Hour},
	})

	with, _ := witnessed.report(t, 40*time.Hour+30*time.Minute)
	without, _ := unwitnessed.report(t, 40*time.Hour+30*time.Minute)
	t.Logf("with a witness:\n%s\nwithout one:\n%s", with, without)

	if !strings.Contains(with, "recorded nothing while the store went on advancing") {
		t.Errorf("a source silent for 36h while another recorded was not reported:\n%s", with)
	}
	if strings.Contains(without, "recorded nothing while the store went on advancing") {
		t.Errorf("the SAME 36 hours of silence was reported when no other source recorded through "+
			"them either — so the rule is measuring the clock, and it will fire every morning:\n%s",
			without)
	}
	// And the second store is not silently green. It has a reading of its own,
	// and the report owes the reader that reading and the reason it stops there.
	if !strings.Contains(without, "36h30m") {
		t.Errorf("the report never says how old the newest evidence is:\n%s", without)
	}
}

// A source with nothing recorded inside its own collected era gets NO bound and
// is not judged. blender-backups and motion-autosave on the live store are both
// this: every event either of them holds is a file modification time from before
// this machine ever ran a harvest, so nothing recorded says what quiet looks
// like for them.
//
// It must render as neither healthy nor faulty. Absence of a basis is a third
// state, and this file's whole subject is refusing to collapse it into either
// neighbour.
func TestASourceWithNoRecordedQuietIsNotJudgedEitherWay(t *testing.T) {
	f := statusEvidence(t, evidenceFixture{
		events: map[string][]time.Duration{
			biomeAppusage:     workingDay(0, 40*time.Hour, 15*time.Minute),
			claudeTranscripts: workingDay(0, 40*time.Hour, 15*time.Minute),
			// One event, at the very start, and nothing since.
			blenderBackupsID: {0},
		},
		harvests: []time.Duration{40 * time.Hour},
	})

	out, err := f.report(t, 40*time.Hour+30*time.Minute)
	t.Logf("a source with no recorded quiet of its own:\n%s", out)

	if err != nil {
		t.Errorf("status failed over a source it has no basis for judging: %v", err)
	}
	if !strings.Contains(out, "nothing here says what quiet looks like") {
		t.Errorf("a source with no closed quiet spell of its own is not reported as unjudged:\n%s", out)
	}
	if !strings.Contains(out, blenderBackupsID) {
		t.Errorf("the unjudged source is not named:\n%s", out)
	}
	if strings.Contains(out, blenderBackupsID+": recorded nothing while") {
		t.Errorf("a source with no basis for a quiet period was judged against one anyway:\n%s", out)
	}
}

// A silence shorter than one harvest interval is never reported, however
// unprecedented it is, because evidence cannot arrive faster than the harvest
// brings it in. The floor comes from the installed schedule for the same reason
// the staleness bound does — it is the one number about this machine's cadence
// that cannot go stale, and it follows the agent down to hourly with nothing
// here edited.
func TestASilenceShorterThanOneHarvestIntervalIsNotReported(t *testing.T) {
	f := statusEvidence(t, evidenceFixture{
		events: map[string][]time.Duration{
			// A source recording every minute for four hours: its longest
			// recorded quiet is a minute, so four times that is four minutes.
			claudeTranscripts: workingDay(0, 4*time.Hour, time.Minute),
			biomeAppusage:     workingDay(0, 5*time.Hour, time.Minute),
		},
		harvests: []time.Duration{5 * time.Hour},
	})

	// An hour of silence, which is fifteen times its own longest quiet spell and
	// well past four times it — and a quarter of the interval the harvest runs
	// on, so nothing yet says the source has stopped rather than that the next
	// harvest has not run.
	out, err := f.report(t, 5*time.Hour+10*time.Minute)
	t.Logf("a silence shorter than one harvest interval:\n%s", out)

	if err != nil {
		t.Errorf("status failed over an hour of quiet on a four-hourly harvest: %v", err)
	}
	if strings.Contains(out, "recorded nothing while the store went on advancing") {
		t.Errorf("an hour of silence was reported on a machine that harvests every four hours, so "+
			"evidence had not yet had a chance to arrive:\n%s", out)
	}
}

// With no installed schedule there is no floor to derive, so the silence check
// is NOT made — and the report says so rather than judging against a floor
// nobody derived. That is b7TYN's rule for the staleness bound applied to this
// check: a bound reconciled against nothing produces a clean report
// indistinguishable from a correct one.
func TestWithNoScheduleTheSilenceCheckIsNotMadeAndSaysSo(t *testing.T) {
	f := statusEvidence(t, evidenceFixture{
		events: map[string][]time.Duration{
			biomeAppusage:     workingDay(0, 40*time.Hour, 15*time.Minute),
			claudeTranscripts: workingDay(0, 4*time.Hour, 5*time.Minute),
		},
		harvests: []time.Duration{40 * time.Hour},
	})
	f.plist = filepath.Join(t.TempDir(), "nothing.plist")

	out, _ := f.report(t, 40*time.Hour+30*time.Minute)
	t.Logf("a silent source with no installed schedule:\n%s", out)

	if strings.Contains(out, "recorded nothing while the store went on advancing") {
		t.Errorf("a source was called silent against a floor derived from a schedule that is not "+
			"installed:\n%s", out)
	}
	if !strings.Contains(out, "which sources have gone silent was not checked") {
		t.Errorf("the report does not say that the silence check went unmade:\n%s", out)
	}
}

// A store that holds no evidence at all is not a store whose evidence is
// infinitely old. It is the absence of a reading, and the report says so rather
// than printing an age counted from the year one.
func TestAStoreWithNoEvidenceIsNotInfinitelyStale(t *testing.T) {
	f := statusEvidence(t, evidenceFixture{harvests: []time.Duration{0}})

	out, _ := f.report(t, time.Hour)
	t.Logf("a store with runs and no events:\n%s", out)

	if strings.Contains(out, "ago") && strings.Contains(out, "evidence:  newest") {
		t.Errorf("an empty store was given an age for its newest evidence:\n%s", out)
	}
	if !strings.Contains(out, "no source has ever stored an event") {
		t.Errorf("the report does not say that the store holds no evidence at all:\n%s", out)
	}
}

// The fixture, and the identifiers it uses. They are source ids the program
// already knows, so that nothing here invents a name the rest of the code would
// not recognise.
const (
	biomeAppusage     = "biome-appusage"
	knowledgecBacklit = "knowledgec-backlit"
)

// evidenceFixture is a store to be seeded: which source recorded at which
// offsets from the fixture's own zero, and at which offsets a harvest ran and
// ingested nothing.
type evidenceFixture struct {
	events   map[string][]time.Duration
	harvests []time.Duration
}

// seeded is a seeded store, with the instant its timeline starts from.
type seeded struct {
	path  string
	base  time.Time
	plist string
}

// report runs status against the fixture at base+after.
func (s seeded) report(t *testing.T, after time.Duration) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: s.path, snapshots: t.TempDir(), plist: s.plist, now: s.base.Add(after),
	})
	return out.String(), err
}

// workingDay is one source recording every step from first to last inclusive,
// which is what gives it a history of its own quiet spells to be judged against.
//
// Enough of them to clear statusQuietSample, and generated rather than written
// out: the sample floor exists because the maximum of a handful of draws says
// nothing about a distribution, and a fixture that dodged it would be testing a
// rule the program does not have.
func workingDay(first, last, step time.Duration) []time.Duration {
	var out []time.Duration
	for at := first; at <= last; at += step {
		out = append(out, at)
	}
	return out
}

// statusEvidence seeds a store from the fixture and returns it ready to report.
//
// Every event is written by a harvest run that began at the fixture's zero, so
// every one of them falls inside its source's collected era — the boundary
// Store.Evidence derives from harvest_run. An event dated before that boundary
// is archaeology rather than a reading about collection, and is excluded there
// deliberately; a fixture that produced one by accident would be silently
// testing nothing.
func statusEvidence(t *testing.T, f evidenceFixture) seeded {
	t.Helper()
	st, path := newStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Truncate(time.Second)
	for id, offsets := range f.events {
		run, err := st.BeginRun(ctx, store.RunSpec{SourceID: id, Input: "(fixture)"})
		if err != nil {
			t.Fatalf("begin a run of %s: %v", id, err)
		}
		events := make([]source.Event, 0, len(offsets))
		for i, at := range offsets {
			events = append(events, source.NewPoint(base.Add(at),
				source.Identity{Document: fmt.Sprintf("%s-%d.md", id, i)}, id))
		}
		if _, err := run.Write(ctx, events); err != nil {
			t.Fatalf("write %d events of %s: %v", len(events), id, err)
		}
		if err := run.Finish(ctx); err != nil {
			t.Fatalf("finish a run of %s: %v", id, err)
		}
	}
	// The later harvests, which ran and ingested nothing — the shape of the
	// 09:00 and 13:00 runs of 2026-08-13, both of which read over a million
	// events and found none new.
	for _, at := range f.harvests {
		run, err := st.BeginRun(ctx, store.RunSpec{SourceID: biomeAppusage, Input: "(fixture)"})
		if err != nil {
			t.Fatalf("begin an empty run: %v", err)
		}
		if err := run.Finish(ctx); err != nil {
			t.Fatalf("finish an empty run: %v", err)
		}
		// The run's own started_at is the clock's, and the report is asked for
		// a fixed instant instead; what this loop is for is that a harvest
		// exists at all, so the staleness check has something recent to read.
		statusBackdateRun(t, path, run.ID, base.Add(at))
	}
	st.Close()

	return seeded{path: path, base: base, plist: writePlist(t, plistWith(fixtureFourHourly))}
}

// statusBackdateRun moves one harvest_run row to a chosen instant.
//
// BeginRun stamps started_at from the clock, deliberately -- it is recording
// when a harvest actually began -- so a fixture that needs a harvest to have run
// at a stated offset has to write the row itself. internal/store's own tests do
// the same, for the same reason.
func statusBackdateRun(t *testing.T, path string, id int64, at time.Time) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open %s directly: %v", path, err)
	}
	defer db.Close()
	stamp := at.UTC().Format("2006-01-02T15:04:05.000000Z")
	if _, err := db.Exec(`UPDATE harvest_run SET started_at = ?, finished_at = ? WHERE id = ?`,
		stamp, stamp, id); err != nil {
		t.Fatalf("date harvest_run %d at %s: %v", id, at, err)
	}
}

// fixtureFourHourly is the six-slot schedule the agent carries today, as a
// fixture. Nothing in this file reads the live plist: the suite must not pass or
// fail on what this machine happens to have installed.
const fixtureFourHourly = `	<key>StartCalendarInterval</key>
	<array>
		<dict><key>Hour</key><integer>0</integer><key>Minute</key><integer>0</integer></dict>
		<dict><key>Hour</key><integer>4</integer><key>Minute</key><integer>0</integer></dict>
		<dict><key>Hour</key><integer>8</integer><key>Minute</key><integer>0</integer></dict>
		<dict><key>Hour</key><integer>12</integer><key>Minute</key><integer>0</integer></dict>
		<dict><key>Hour</key><integer>16</integer><key>Minute</key><integer>0</integer></dict>
		<dict><key>Hour</key><integer>20</integer><key>Minute</key><integer>0</integer></dict>
	</array>
`
