package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/store"
)

// THE DEFECT THESE TESTS EXIST FOR. `timetattle status` bound its staleness
// check to a hard-coded 24h and knew nothing about the schedule it was
// checking. The installed agent runs every 12h, so the age of the newest run
// only reached 24h at the instant the NEXT run was already due: one skipped
// harvest could not be reported, by construction, and it took two. That is why
// status printed "every source ran and succeeded within the last 24h00m" on
// 2026-08-12, a day on which no harvest row, no capture and no event was
// stamped at all.
//
// The bound is now derived from the installed launchd plist, which is the
// schedule rather than a record of what someone meant to install. The property
// under all of it is the standing one in CLAUDE.md: a plist that is missing,
// unreadable or carries no scheduling key is NOTHING RECORDED, and must render
// differently from both a healthy schedule and a stale one. Falling back to 24h
// and printing a verdict anyway would be the same defect one level down.

// plistWith wraps a dict body in the XML a launchd plist actually carries,
// DOCTYPE and all, so the parser is exercised against the real shape.
func plistWith(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>com.example.harvest</string>
	<key>ProgramArguments</key>
	<array>
		<string>/usr/local/bin/timetattle</string>
		<string>harvest</string>
	</array>
` + body + `	<key>RunAtLoad</key>
	<true/>
</dict>
</plist>
`
}

// writePlist puts a FIXTURE plist on disk and returns its path. Nothing in this
// file reads ~/Library/LaunchAgents: a test that read the installed schedule
// would pass or fail according to what this machine happens to have installed
// this morning, which is not a test of anything.
func writePlist(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "com.example.harvest.plist")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing the fixture plist: %v", err)
	}
	return path
}

// The interval the installed agent carries today.
const fixtureStartInterval = `	<key>StartInterval</key>
	<integer>43200</integer>
`

// TestTheBoundComesFromStartInterval is the schedule as it is installed today.
func TestTheBoundComesFromStartInterval(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(fixtureStartInterval)))

	if s.state != scheduleFound {
		t.Fatalf("a plist carrying StartInterval was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.key != "StartInterval" {
		t.Errorf("the bound is attributed to %q, not to the key it came from", s.key)
	}
	if s.interval != 12*time.Hour {
		t.Errorf("43200 seconds read as %s, want 12h", s.interval)
	}
	bound, ok := s.bound()
	if !ok {
		t.Fatal("a schedule that was read yields no bound")
	}
	// One interval and a half. Two intervals is the defect: the age would only
	// reach the bound as the next run fell due, so one miss could never be
	// reported and it took two.
	if bound != 18*time.Hour {
		t.Errorf("the bound on a 12h schedule is %s, want 18h — anything at or above 24h cannot "+
			"report a single skipped run", bound)
	}
}

// TestTheBoundComesFromAnEvenCalendarSchedule is the shape the installed plist
// is being changed to in parallel: fixed times of day, so a machine that was
// asleep runs on wake rather than drifting its phase.
func TestTheBoundComesFromAnEvenCalendarSchedule(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(`	<key>StartCalendarInterval</key>
	<array>
		<dict>
			<key>Hour</key>
			<integer>9</integer>
			<key>Minute</key>
			<integer>0</integer>
		</dict>
		<dict>
			<key>Hour</key>
			<integer>21</integer>
			<key>Minute</key>
			<integer>0</integer>
		</dict>
	</array>
`)))

	if s.state != scheduleFound {
		t.Fatalf("a plist carrying StartCalendarInterval was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.key != "StartCalendarInterval" {
		t.Errorf("the bound is attributed to %q, not to the key it came from", s.key)
	}
	if s.interval != 12*time.Hour {
		t.Errorf("09:00 and 21:00 daily read as an interval of %s, want 12h", s.interval)
	}
	if s.uneven() {
		t.Error("a schedule whose two fires are twelve hours apart either way is reported as uneven")
	}
	if bound, _ := s.bound(); bound != 18*time.Hour {
		t.Errorf("the bound on a twice-daily schedule is %s, want 18h", bound)
	}
}

// TestTheBoundComesFromTheSixSlotScheduleInstalledToday is the schedule this
// machine now carries: six StartCalendarInterval slots at 00, 04, 08, 12, 16
// and 20 local, minute 0, with StartInterval gone entirely.
//
// It is a fixture and not the live plist. The point of the whole file is that
// the bound follows the installed schedule with nothing here edited: the same
// code that derived 18h from the 12h StartInterval derives 6h from this,
// because the schedule changed and the derivation did not.
func TestTheBoundComesFromTheSixSlotScheduleInstalledToday(t *testing.T) {
	var slots strings.Builder
	slots.WriteString("\t<key>StartCalendarInterval</key>\n\t<array>\n")
	for _, hour := range []int{0, 4, 8, 12, 16, 20} {
		fmt.Fprintf(&slots, "\t\t<dict>\n\t\t\t<key>Hour</key>\n\t\t\t<integer>%d</integer>\n"+
			"\t\t\t<key>Minute</key>\n\t\t\t<integer>0</integer>\n\t\t</dict>\n", hour)
	}
	slots.WriteString("\t</array>\n")

	s := readSchedule(writePlist(t, plistWith(slots.String())))

	if s.state != scheduleFound {
		t.Fatalf("the installed six-slot schedule was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.interval != 4*time.Hour {
		t.Errorf("six slots four hours apart read as an interval of %s, want 4h", s.interval)
	}
	if s.uneven() {
		t.Error("six evenly spaced slots are reported as uneven")
	}
	if bound, _ := s.bound(); bound != 6*time.Hour {
		t.Errorf("the bound on the installed schedule is %s, want 6h", bound)
	}
}

// A single dict rather than an array of them. launchd accepts both shapes for
// the same key, and a parser that handled only the array would read a
// once-a-day schedule as no schedule at all — which is the absence collapse
// this whole task is about.
func TestACalendarScheduleMayBeASingleDict(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(`	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>3</integer>
		<key>Minute</key>
		<integer>30</integer>
	</dict>
`)))

	if s.state != scheduleFound {
		t.Fatalf("a StartCalendarInterval written as one dict was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.interval != 24*time.Hour {
		t.Errorf("a single daily fire read as an interval of %s, want 24h", s.interval)
	}
	if bound, _ := s.bound(); bound != 36*time.Hour {
		t.Errorf("the bound on a daily schedule is %s, want 36h", bound)
	}
}

// A Weekday turns the cycle from a day into a week. Measuring the gaps over a
// single day would report a weekly schedule as a daily one and call a store
// stale six days out of seven.
func TestAWeeklyCalendarScheduleIsMeasuredOverAWeekAndNotADay(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(`	<key>StartCalendarInterval</key>
	<dict>
		<key>Weekday</key>
		<integer>1</integer>
		<key>Hour</key>
		<integer>9</integer>
		<key>Minute</key>
		<integer>0</integer>
	</dict>
`)))

	if s.state != scheduleFound {
		t.Fatalf("a weekly StartCalendarInterval was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.interval != 7*24*time.Hour {
		t.Errorf("one fire a week read as an interval of %s, want 168h", s.interval)
	}
}

// TestAnUnevenCalendarScheduleIsBoundedByItsWidestGapAndSaysSo is the honest
// limit of a single derived bound, stated rather than hidden.
//
// Fires at 09:00, 12:00 and 15:00 are three hours apart in the day and eighteen
// apart overnight. The bound has to clear the widest gap or the report would
// call every night stale — and that means a run skipped in the tight stretch is
// not reported until the wide one has elapsed, which is the very blindness
// being fixed, wearing a calendar. It is reported in words for that reason: a
// blindness a reader can see is not the same defect as one nobody stated.
func TestAnUnevenCalendarScheduleIsBoundedByItsWidestGapAndSaysSo(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(`	<key>StartCalendarInterval</key>
	<array>
		<dict>
			<key>Hour</key>
			<integer>9</integer>
			<key>Minute</key>
			<integer>0</integer>
		</dict>
		<dict>
			<key>Hour</key>
			<integer>12</integer>
			<key>Minute</key>
			<integer>0</integer>
		</dict>
		<dict>
			<key>Hour</key>
			<integer>15</integer>
			<key>Minute</key>
			<integer>0</integer>
		</dict>
	</array>
`)))

	if s.state != scheduleFound {
		t.Fatalf("an uneven StartCalendarInterval was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.interval != 18*time.Hour {
		t.Errorf("the widest gap of 09:00/12:00/15:00 read as %s, want 18h", s.interval)
	}
	if s.narrowest != 3*time.Hour {
		t.Errorf("the narrowest gap read as %s, want 3h", s.narrowest)
	}
	if !s.uneven() {
		t.Error("a schedule with a 3h gap and an 18h gap is not reported as uneven, so nothing tells " +
			"a reader the bound is blind to a run skipped in the tight stretch")
	}
}

// TestAMissingPlistIsNotASilentTwentyFourHours is the absence case, and the one
// most likely to be got wrong. There is no schedule to derive from, so there is
// no bound — not a default one.
func TestAMissingPlistIsNotASilentTwentyFourHours(t *testing.T) {
	s := readSchedule(filepath.Join(t.TempDir(), "nothing.plist"))

	if s.state != scheduleAbsent {
		t.Errorf("a plist that is not there reads as %v, want absent", s.state)
	}
	if bound, ok := s.bound(); ok {
		t.Errorf("a machine with no installed schedule yields a bound of %s; nothing recorded "+
			"can say how fresh this store ought to be", bound)
	}
}

// A plist nothing can parse is a third fact again: not a schedule, and not the
// observation that there is none.
func TestAnUnreadablePlistIsNotASilentTwentyFourHours(t *testing.T) {
	s := readSchedule(writePlist(t, "<plist><dict><key>Start"))

	if s.state != scheduleUnreadable {
		t.Errorf("a truncated plist reads as %v, want unreadable", s.state)
	}
	if s.err == nil {
		t.Error("a plist that could not be parsed carries no reason why")
	}
	if _, ok := s.bound(); ok {
		t.Error("a plist that could not be parsed still produced a bound")
	}
}

// A plist that parses and schedules nothing. launchd runs such an agent at load
// and never again, so there is no cadence to be late against — which is not the
// same fact as a healthy schedule, and must not print like one.
func TestAPlistWithNoSchedulingKeyIsNotASilentTwentyFourHours(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith("")))

	if s.state != scheduleUnscheduled {
		t.Errorf("a plist with neither scheduling key reads as %v, want unscheduled", s.state)
	}
	if _, ok := s.bound(); ok {
		t.Error("a plist that schedules nothing still produced a bound")
	}
}

// Zero is not an interval, and a bound of zero would call every store stale.
func TestAZeroStartIntervalIsNotASchedule(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(`	<key>StartInterval</key>
	<integer>0</integer>
`)))

	if s.state == scheduleFound {
		t.Error("StartInterval 0 was read as a schedule")
	}
	if _, ok := s.bound(); ok {
		t.Error("StartInterval 0 produced a bound")
	}
}

// Both keys at once is what a half-finished reinstall leaves behind, and
// launchd does not document which one wins. The report takes the wider of the
// two, because a bound that is too wide delays the news and a bound that is too
// tight discredits the whole report — and it names both keys so that a reader
// can see the plist has two schedules in it.
func TestAPlistCarryingBothKeysIsBoundedByTheWiderAndNamesBoth(t *testing.T) {
	s := readSchedule(writePlist(t, plistWith(fixtureStartInterval+`	<key>StartCalendarInterval</key>
	<dict>
		<key>Hour</key>
		<integer>3</integer>
		<key>Minute</key>
		<integer>0</integer>
	</dict>
`)))

	if s.state != scheduleFound {
		t.Fatalf("a plist carrying both keys was not read as a schedule: %v (%v)", s.state, s.err)
	}
	if s.interval != 24*time.Hour {
		t.Errorf("a 12h StartInterval beside a daily StartCalendarInterval bound the report at an "+
			"interval of %s, want the wider 24h", s.interval)
	}
	if !strings.Contains(s.key, "StartInterval") || !strings.Contains(s.key, "StartCalendarInterval") {
		t.Errorf("the report attributes the bound to %q and does not say the plist carries both keys", s.key)
	}
}

// statusOneGoodRun leaves a store holding a single harvest that read one input
// and succeeded, and returns the store path and the instant it ran.
func statusOneGoodRun(t *testing.T) (string, time.Time) {
	t.Helper()
	st, path := newStore(t)
	ctx := context.Background()
	run, err := st.BeginRun(ctx, store.RunSpec{SourceID: claudeTranscripts, Input: "(fixture)"})
	if err != nil {
		t.Fatalf("BeginRun: %v", err)
	}
	if err := run.Finish(ctx); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	st.Close()
	return path, run.StartedAt
}

// TestASingleSkippedHarvestOnATwelveHourScheduleTripsStatus is the regression
// test for the defect, and it carries its own mutation: the same store at the
// same instant is judged twice, once against the derived bound and once against
// the 24h that used to be hard-coded. The second must NOT report anything. If
// it ever does, this test has stopped proving that the fix changed the outcome.
func TestASingleSkippedHarvestOnATwelveHourScheduleTripsStatus(t *testing.T) {
	path, ran := statusOneGoodRun(t)
	plist := writePlist(t, plistWith(fixtureStartInterval))

	// Six and a half hours past a fire that should have happened twelve hours
	// after the last one, and five and a half hours before the next is due.
	skipped := ran.Add(18*time.Hour + 30*time.Minute)

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), plist: plist, now: skipped,
	})
	got := out.String()
	t.Logf("one skipped harvest:\n%s", got)

	if err == nil {
		t.Error("status exited zero 18h30m into a 12h schedule, which is one whole harvest skipped")
	}
	if !strings.Contains(got, "the schedule is stale") {
		t.Errorf("the report does not say the schedule is stale:\n%s", got)
	}
	if strings.Contains(got, "every source ran and succeeded") {
		t.Errorf("the all-clear was printed over a skipped harvest:\n%s", got)
	}
	// AND IT MAY NOT CALL IT A FAULT. An installed cadence is what launchd was
	// asked for, not a promise about wall-clock time: launchd fires a missed
	// calendar slot within a second of the machine waking, but it cannot run at
	// all while the machine is off, so a laptop closed over a weekend leaves a
	// three-day hole that is nobody's fault. harvest_run holds only what ran,
	// and cannot tell that from a harvest that was missed on a machine that was
	// up the whole time. Stating the age is an observation; calling it a
	// failure of the schedule would be the report concluding something it never
	// saw, which is the rule this whole task exists under.
	if !strings.Contains(got, "not running") {
		t.Errorf("the stale verdict does not say that the record cannot tell a missed harvest "+
			"from a machine that was not running:\n%s", got)
	}

	// The mutation. This is what the report said before the bound was derived,
	// and it is the whole reason a day went unnoticed.
	var old bytes.Buffer
	oldErr := reportStatus(&old, statusOptions{
		store: path, snapshots: t.TempDir(), plist: plist, stale: 24 * time.Hour, now: skipped,
	})
	t.Logf("the same store against the 24h that used to be hard-coded:\n%s", old.String())
	if oldErr != nil {
		t.Errorf("the 24h bound reported something at 18h30m, so this test no longer demonstrates "+
			"the defect it exists for: %v", oldErr)
	}
	if strings.Contains(old.String(), "the schedule is stale") {
		t.Error("the 24h bound called this stale, so the fix changed nothing")
	}

	// And it must not fire early either. A check that cried wolf at thirteen
	// hours would be read once and ignored after that.
	var fresh bytes.Buffer
	freshErr := reportStatus(&fresh, statusOptions{
		store: path, snapshots: t.TempDir(), plist: plist, now: ran.Add(13 * time.Hour),
	})
	if freshErr != nil {
		t.Errorf("status failed 13h into a 12h schedule, which is one hour of ordinary launchd "+
			"deferral and nothing else: %v", freshErr)
	}
	if strings.Contains(fresh.String(), "the schedule is stale") {
		t.Errorf("the report calls a store stale an hour after its schedule was due:\n%s", fresh.String())
	}
}

// TestStatusNamesTheBoundAndWhereItCameFrom. A report that prints a verdict
// without naming the bound that produced it is a clean report indistinguishable
// from a correct one, which is the failure mode CLAUDE.md names three times.
func TestStatusNamesTheBoundAndWhereItCameFrom(t *testing.T) {
	path, ran := statusOneGoodRun(t)
	plist := writePlist(t, plistWith(fixtureStartInterval))

	var out bytes.Buffer
	_ = reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), plist: plist, now: ran.Add(time.Hour),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	for _, want := range []string{"12h00m", "18h00m", "StartInterval", plist} {
		if !strings.Contains(got, want) {
			t.Errorf("the report never mentions %q, so a reader cannot tell which schedule "+
				"produced the verdict:\n%s", want, got)
		}
	}
}

// TestAnExplicitStaleOverridesTheInstalledScheduleAndSaysSo. The flag still
// wins — but a bound that silently replaced the installed one would leave a
// reader unable to tell which number the verdict came from.
func TestAnExplicitStaleOverridesTheInstalledScheduleAndSaysSo(t *testing.T) {
	path, ran := statusOneGoodRun(t)
	plist := writePlist(t, plistWith(fixtureStartInterval))

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), plist: plist, stale: time.Hour, now: ran.Add(2 * time.Hour),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	if err == nil {
		t.Error("status exited zero two hours into a -stale of one hour")
	}
	if !strings.Contains(got, "the schedule is stale") {
		t.Errorf("the explicit -stale did not decide the verdict:\n%s", got)
	}
	if !strings.Contains(got, "-stale") {
		t.Errorf("the report never says the bound was given on the command line:\n%s", got)
	}
	if !strings.Contains(got, "18h00m") {
		t.Errorf("the report never says what the explicit bound overrode, so nothing distinguishes "+
			"it from the installed schedule agreeing:\n%s", got)
	}
}

// TestStatusJudgesNoFreshnessWithNoScheduleToJudgeItBy is criterion szg, and the
// bug being fixed one level down. With no installed schedule there is no bound;
// the report must say so, must not call the store fresh, and must not call it
// stale either. Three outcomes, three renderings.
func TestStatusJudgesNoFreshnessWithNoScheduleToJudgeItBy(t *testing.T) {
	path, ran := statusOneGoodRun(t)
	missing := filepath.Join(t.TempDir(), "nothing.plist")

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), plist: missing, now: ran.Add(72 * time.Hour),
	})
	got := out.String()
	t.Logf("three days on, with no schedule installed:\n%s", got)

	if strings.Contains(got, "the schedule is stale") {
		t.Errorf("the report called a store stale against a schedule it never read:\n%s", got)
	}
	if strings.Contains(got, "every source ran and succeeded within") {
		t.Errorf("the report printed the all-clear over a freshness check it did not make:\n%s", got)
	}
	if !strings.Contains(got, "not checked") {
		t.Errorf("the report does not say that freshness went unchecked:\n%s", got)
	}
	if !strings.Contains(got, missing) {
		t.Errorf("the report never names the path it looked for a schedule at:\n%s", got)
	}
	// Not an alarm. A machine that harvests by hand is a machine with no
	// installed schedule, and a check that cries wolf is a check nobody reads.
	if err != nil {
		t.Errorf("status failed on a machine with no schedule installed: %v", err)
	}

	// The distinction that matters: this must not render as the healthy report.
	var healthy bytes.Buffer
	_ = reportStatus(&healthy, statusOptions{
		store: path, snapshots: t.TempDir(), plist: writePlist(t, plistWith(fixtureStartInterval)),
		now: ran.Add(time.Hour),
	})
	if got == healthy.String() {
		t.Error("a store nothing can judge renders identically to one judged fresh")
	}
}

// A plist that cannot be read is the one of the three that sets the exit status,
// on the same footing as an unreadable capture_run table: the report could not
// make a check it is supposed to make, and swallowing that would leave the whole
// verdict resting on a reading nobody took.
func TestAnUnreadableScheduleSetsTheExitStatus(t *testing.T) {
	path, ran := statusOneGoodRun(t)
	broken := writePlist(t, "<plist><dict><key>Start")

	var out bytes.Buffer
	err := reportStatus(&out, statusOptions{
		store: path, snapshots: t.TempDir(), plist: broken, now: ran.Add(time.Hour),
	})
	got := out.String()
	t.Logf("report:\n%s", got)

	if err == nil {
		t.Error("status exited zero over a schedule it could not read")
	}
	if !strings.Contains(got, "not checked") {
		t.Errorf("the report does not say that freshness went unchecked:\n%s", got)
	}
	if strings.Contains(got, "every source ran and succeeded") {
		t.Errorf("the all-clear was printed over a schedule that could not be read:\n%s", got)
	}
}

// The default is the installed agent's own plist, and it must be derived rather
// than typed twice. This asserts the shape only — it never reads the file, which
// would make the suite depend on what this machine has installed.
func TestTheDefaultSchedulePathIsTheInstalledAgent(t *testing.T) {
	path, err := defaultSchedulePath()
	if err != nil {
		t.Fatalf("defaultSchedulePath: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("Library", "LaunchAgents", harvestAgentLabel+".plist")) {
		t.Errorf("the default schedule path is %q, which is not where scripts/install-agent.sh "+
			"writes the agent", path)
	}
}
