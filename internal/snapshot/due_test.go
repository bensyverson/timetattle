package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// every4h is the interval the installed schedule throttles knowledgeC to.
const every4h = 4 * time.Hour

// strDigest is a name-shaped digest. Its value is irrelevant to Due, which
// never opens a file; only the shape of the name is.
const strDigest = "0123456789abcdef0123456789abcdef"

// at parses a wall-clock stamp in the zone snapshot names are written in.
//
// Local rather than UTC because snapshot.Take stamps a name with time.Now(),
// which is local, so a test reasoning in UTC would be reasoning about names
// this package never writes.
func at(t *testing.T, stamp string) time.Time {
	t.Helper()
	when, err := time.ParseInLocation(snapshotTimeLayout, stamp, time.Local)
	if err != nil {
		t.Fatalf("parse %q: %v", stamp, err)
	}
	return when
}

// holding writes empty files with the given snapshot names into a new
// directory. Only the names matter here: Due reads the directory and never
// opens anything.
func holding(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", n, err)
		}
	}
	return dir
}

func TestDueTakesTheFirstSnapshotIntoAnEmptyDirectory(t *testing.T) {
	d, err := Due(holding(t), at(t, "20260813T040000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due refused the first snapshot into an empty directory")
	}
	if d.Newest != "" || !d.At.IsZero() {
		t.Errorf("Due named %q taken at %v in an empty directory", d.Newest, d.At)
	}
}

// A directory that does not exist yet is the ordinary state of a machine that
// has never harvested, and is a different fact from one that cannot be read.
func TestDueTakesTheFirstSnapshotWhenTheDirectoryDoesNotExistYet(t *testing.T) {
	d, err := Due(filepath.Join(t.TempDir(), "never-created"), at(t, "20260813T040000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due refused the first snapshot when the directory did not exist")
	}
}

func TestDueRefusesASecondSnapshotInsideTheInterval(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T040000-"+strDigest+".db.gz")
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if d.Take {
		t.Error("Due took a second snapshot one hour into a four-hour interval")
	}
}

func TestDueTakesOneOnceTheIntervalHasPassed(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T040000-"+strDigest+".db.gz")
	d, err := Due(dir, at(t, "20260813T080000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due refused a snapshot four hours after the last one")
	}
}

// THE GUARD, and the reason Due compares hour slots rather than instants.
//
// These two stamps are real. They are consecutive scheduled runs on this
// machine on 2026-08-13, read out of the live snapshot directory: the 00:00
// slot fired at 00:00:01 and the 04:00 slot fired at 04:00:00, one second SHORT
// of four hours apart. A throttle comparing instants refuses that snapshot,
// waits for the 05:00 harvest, and turns a four-hourly cadence into a
// five-hourly one — quietly, and in the direction that loses evidence.
func TestDueIsNotDefeatedByASlotThatFiresASecondEarly(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T000001-"+strDigest+".db.gz")
	now := at(t, "20260813T040000")
	if gap := now.Sub(at(t, "20260813T000001")); gap >= every4h {
		t.Fatalf("this case tests nothing: the stamps are %v apart, not under %v", gap, every4h)
	}
	d, err := Due(dir, now, every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due refused the 04:00 snapshot because the 00:00 one fired one second late")
	}
}

// The other side of the same guard: absorbing jitter must not become absorbing
// an hour. A snapshot taken by hand at 03:58 does not entitle the 04:00
// scheduled run to write a near-duplicate two minutes later.
func TestDueRefusesASnapshotTwoMinutesAfterAManualOne(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T035800-"+strDigest+".db.gz")
	d, err := Due(dir, at(t, "20260813T040000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if d.Take {
		t.Error("Due took a snapshot two minutes after the one already on disk")
	}
}

// A refused snapshot still has to name the file the run should read, or the
// harvest has nothing to ingest and the four knowledgeC streams lose their row.
func TestDueNamesTheSnapshotAThrottledRunShouldStillRead(t *testing.T) {
	name := "knowledgeC-20260813T040000-" + strDigest + ".db.gz"
	dir := holding(t, name)
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if d.Take {
		t.Fatal("Due took a snapshot one hour into the interval")
	}
	if want := filepath.Join(dir, name); d.Newest != want {
		t.Errorf("Due named %q, want %q", d.Newest, want)
	}
	if want := at(t, "20260813T040000"); !d.At.Equal(want) {
		t.Errorf("Due dated the newest snapshot %v, want %v", d.At, want)
	}
}

func TestDueChoosesTheNewestOfSeveral(t *testing.T) {
	dir := holding(t,
		"knowledgeC-20260812T200000-"+strDigest+".db.gz",
		"knowledgeC-20260813T000001-"+strDigest+".db.gz",
		"knowledgeC-20260813T040000-"+strDigest+".db.gz",
	)
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if want := at(t, "20260813T040000"); !d.At.Equal(want) {
		t.Errorf("Due dated the newest snapshot %v, want %v", d.At, want)
	}
}

// An unreadable directory is an error and never a skip. The two differ by
// exactly the thing that matters: a directory that cannot be read, treated as
// "nothing is due", would stop preserving knowledgeC for ever and report
// nothing at all — and knowledgeC is the source no later run can recover.
// heldSnapshot takes the same position for the same reason.
func TestDueOnAnUnreadableDirectoryIsAnErrorAndNotASkip(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T040000-"+strDigest+".db.gz")
	if err := os.Chmod(dir, 0o000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if _, err := Due(dir, at(t, "20260813T050000"), every4h); err == nil {
		t.Error("Due read an unreadable directory without error")
	}
}

// Snapshots written before content addressing carry no digest in their name.
// The whole live corpus is that shape — 42 files on 2026-08-13, not one of them
// digested — so a parser that only read the new shape would see an empty
// directory and write a snapshot on every single run.
func TestDueReadsSnapshotsWrittenBeforeContentAddressing(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T040000.db.gz")
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if d.Take {
		t.Error("Due ignored an undigested snapshot and took another one an hour later")
	}
}

// createUnique appends a counter when two runs land in the same second with
// different contents. That name is a snapshot like any other.
func TestDueReadsACollisionSuffixedName(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T040000-"+strDigest+"-1.db.gz")
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if d.Take {
		t.Error("Due ignored a collision-suffixed snapshot")
	}
}

// The Biome and Motion vault captures write into subdirectories of the same
// directory, and neither is throttled. Counting one as a knowledgeC snapshot
// would suppress the capture this throttle exists to schedule.
func TestDueIgnoresTheOtherCapturesInTheSameDirectory(t *testing.T) {
	dir := holding(t)
	for _, sub := range []string{"biome", "motion-vault", "filedays"} {
		if err := os.Mkdir(filepath.Join(dir, sub), 0o700); err != nil {
			t.Fatalf("Mkdir %s: %v", sub, err)
		}
	}
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due mistook another capture's directory for a knowledgeC snapshot")
	}
}

// A name nobody can date is not a snapshot anybody can schedule against, so it
// cannot hold the throttle off. Erring toward taking one costs a file; erring
// toward skipping costs evidence that no later run recovers.
func TestDueTakesOneWhenNoNameInTheDirectoryCanBeDated(t *testing.T) {
	dir := holding(t, "knowledgeC-not-a-date.db.gz", "knowledgeC-.db.gz", "README")
	d, err := Due(dir, at(t, "20260813T050000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due let an undatable name hold the throttle off")
	}
}

// No interval is no throttle. It is how a run says "capture regardless", and it
// must not become "capture never".
func TestDueWithNoIntervalAlwaysTakes(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T040000-"+strDigest+".db.gz")
	for _, every := range []time.Duration{0, -time.Hour} {
		d, err := Due(dir, at(t, "20260813T040001"), every)
		if err != nil {
			t.Fatalf("Due(every=%v): %v", every, err)
		}
		if !d.Take {
			t.Errorf("Due(every=%v) throttled a run that asked for no throttle", every)
		}
		if d.Newest == "" {
			t.Errorf("Due(every=%v) did not name the snapshot already on disk", every)
		}
	}
}

// A snapshot stamped after now is not evidence that one was taken recently — a
// clock that moved, or a zone change, produces it — and a negative elapsed must
// not be read as "no time has passed".
func TestDueTakesOneWhenTheNewestSnapshotIsStampedInTheFuture(t *testing.T) {
	dir := holding(t, "knowledgeC-20260813T120000-"+strDigest+".db.gz")
	d, err := Due(dir, at(t, "20260813T040000"), every4h)
	if err != nil {
		t.Fatalf("Due: %v", err)
	}
	if !d.Take {
		t.Error("Due was held off by a snapshot stamped in the future")
	}
}

// TestDueOverHourlyRunsKeepsTheFourHourlyCadence is the measurement the
// throttle exists for, and the one a mutation of the hour-slot comparison
// breaks with a number.
//
// It replays 336 hourly harvests — two weeks, the span scripts/snapchurn
// projects over — against a throttle set to four hours, with each run starting
// a few seconds either side of the hour the way launchd actually delivers them.
// 84 snapshots is the four-hourly cadence the storage figure of ~3.2 GB/yr was
// measured at; 336 is what an unthrottled hourly harvest writes, and 9.85 GB/yr
// is what that costs.
func TestDueOverHourlyRunsKeepsTheFourHourlyCadence(t *testing.T) {
	dir := t.TempDir()
	// How late each run starts, cycling. Never early: launchd fires a calendar
	// slot at or after its time, never before it, so the whole jitter a
	// scheduled harvest can show is a delay. The first three are the range the
	// live corpus shows (+0s to +67s); the 20 minutes stands for a slot launchd
	// deferred over a sleep and delivered on the wake, which is the case a
	// throttle built on a fixed tolerance rather than on hour slots gets wrong.
	//
	// The cycle matters more than the values. It makes consecutive runs
	// DECREASE their offset — 67s then 0s — which is exactly what put the live
	// 00:00:01 and 04:00:00 pair 3h59m59s apart.
	offsets := []time.Duration{time.Second, 0, 67 * time.Second, 20 * time.Minute, 5 * time.Second, 0}

	start := at(t, "20260801T000000")
	taken := 0
	for run := range 336 {
		now := start.Add(time.Duration(run)*time.Hour + offsets[run%len(offsets)])
		d, err := Due(dir, now, every4h)
		if err != nil {
			t.Fatalf("Due at run %d: %v", run, err)
		}
		if !d.Take {
			continue
		}
		taken++
		name := "knowledgeC-" + now.Format(snapshotTimeLayout) + "-" + strDigest + ".db.gz"
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if want := 336 / 4; taken != want {
		t.Errorf("336 hourly runs throttled to %v wrote %d snapshots, want %d", every4h, taken, want)
	}
}
