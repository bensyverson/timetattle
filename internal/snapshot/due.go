package snapshot

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// snapshotTimeLayout is the instant a snapshot's name carries, in the local
// zone: compress stamps it with the time.Time the caller passed Take, which is
// time.Now(), and Due reads it back with the same layout. One constant so the
// writer and the reader cannot drift apart.
const snapshotTimeLayout = "20060102T150405"

// Decision is whether this run should preserve knowledgeC, and what the
// snapshot directory already holds.
//
// # Why the snapshot has a schedule of its own
//
// The harvest cadence is a sampling argument about the MTIME sources, which
// decay continuously: a file records only its last modification, so work done
// today on a file last touched three weeks ago does not add evidence for that
// day, it MOVES it. Evidence not scanned before the file is touched again is
// gone and no later harvest recovers it. internal/filedays carries the
// measurement — one scan found 1,773 files bearing a given day and the same
// scan hours later found 513 — and it is why the harvest wants to run every
// hour.
//
// knowledgeC does not benefit from that. Its binding floor is /app/usage,
// capped near 10,000 rows, and a four-hourly schedule already puts about sixty
// captures inside that window; anything still inside it comes back whole
// whenever the next harvest runs. So an hourly SNAPSHOT buys knowledgeC
// essentially nothing and costs the most of any capture in the harvest: about
// 1.5 MB written afresh whenever the extracted rows differ at all, which at
// hourly projects to 9.85 GB a year against 3.23 GB at four-hourly (measured
// 2026-08-12 with `go run ./scripts/snapchurn -cadence 1h` and `-cadence 4h`,
// after write-time deduplication; both are UPPER bounds on what dedup saves,
// because a row can age out of retention inside an otherwise quiet window).
//
// Biome and the Motion vault are deliberately NOT throttled. Both are already
// content-addressed segment by segment and file by file, so a run over an
// unchanged source writes nothing at all; they cost a read, not a write, and a
// throttle would buy nothing while delaying evidence.
//
// # What a refused snapshot still has to say
//
// Everything. Take, Newest and At are all set on both answers, because a run
// that skipped the snapshot BY POLICY is a third outcome — distinct from one
// that wrote a snapshot, and distinct from one that found the contents already
// held. The caller records it as such (store.CaptureSkipped) and still ingests
// Newest, so the four knowledgeC streams keep getting a harvest_run row on
// every run rather than falling silent for three hours in four.
type Decision struct {
	// Take is whether this run should write a snapshot.
	Take bool
	// Newest is the path of the most recent snapshot already in the directory,
	// and "" when there is none at all. It is set whether or not Take is: a
	// throttled run reads it, and a taking run is about to sit beside it.
	Newest string
	// At is when Newest was taken, read out of its name, and the zero time when
	// there is no snapshot at all.
	At time.Time
}

// Due reports whether a knowledgeC snapshot is due in dir at now, given the
// smallest interval allowed between two snapshots.
//
// An every of zero or less is no throttle: every run takes one. That is how a
// caller says "capture regardless", and it must never quietly become "capture
// never".
//
// # Why hour slots and not instants
//
// The comparison is between now and the newest snapshot TRUNCATED TO THE HOUR,
// not between the instants themselves, and that is load-bearing rather than
// cosmetic.
//
// launchd delivers a calendar slot near the top of the hour, not on it. The
// live corpus on 2026-08-13 holds three consecutive scheduled runs of the
// four-hourly agent stamped 00:00:01, 04:00:00 and 08:01:07. The first two are
// 3h59m59s apart. A throttle comparing instants refuses that 04:00 snapshot by
// one second, waits for the 05:00 harvest, and from then on the snapshot walks
// an hour later whenever a run is late: a four-hour policy delivering a longer
// cadence, silently, in the direction that loses evidence.
//
// That is a measurement, not an argument. Replace the truncation with a bare
// now.Sub(at) and TestDueOverHourlyRunsKeepsTheFourHourlyCadence writes 75
// snapshots over 336 hourly runs instead of 84 — nine captures of a
// self-pruning source dropped in a fortnight — and
// TestDueIsNotDefeatedByASlotThatFiresASecondEarly fails on the real 00:00:01
// and 04:00:00 pair.
//
// Truncating both endpoints to the hour makes the comparison say what the
// schedule means: the snapshot's own cadence is counted in hourly slots,
// because the harvest that drives it fires on the hour. It absorbs any amount
// of sub-hour jitter without ever letting two snapshots land inside the same
// interval — a run at 03:58 and the scheduled 04:00 are one hour-step apart,
// not four, so the second is still refused.
//
// The consequence for a sub-hour every is stated rather than hidden: the hour
// is the finest grain this can express, so an every under an hour throttles
// nothing beyond a second snapshot inside one hour.
//
// # What is an error, and what is an observation
//
// A directory that does not exist yet is an OBSERVATION: this machine has never
// preserved knowledgeC, and the first snapshot is due. A directory that exists
// and cannot be READ is an error, and is never reported as "not due". Those two
// differ by exactly the thing that matters — a permissions problem read as "not
// due" would stop preserving the one source no later run can recover, for ever,
// while reporting nothing at all. heldSnapshot takes the same position for the
// same reason.
//
// A name that cannot be dated is skipped, and a directory in which NOTHING can
// be dated is due. That is the safe direction: an unrecognised name costs a
// duplicate file, and treating it as recent would cost a capture.
func Due(dir string, now time.Time, every time.Duration) (Decision, error) {
	newest, at, err := newestSnapshot(dir)
	if err != nil {
		return Decision{}, err
	}
	d := Decision{Newest: newest, At: at}
	switch {
	case every <= 0, at.IsZero():
		d.Take = true
	case at.After(now):
		// A snapshot stamped after now is not evidence that one was taken
		// recently. A clock that moved or a zone change produces it, and a
		// negative elapsed must not read as "no time has passed".
		d.Take = true
	default:
		d.Take = now.Truncate(time.Hour).Sub(at.Truncate(time.Hour)) >= every
	}
	return d, nil
}

// newestSnapshot is the latest snapshot in dir and when it was taken, or "" and
// the zero time when the directory holds none.
//
// The answer is the greatest PARSED instant rather than the last name in the
// listing. os.ReadDir sorts by name and a snapshot name begins with a
// fixed-width stamp, so the two agree today; they would stop agreeing the
// moment a name gained a prefix, and the parsed answer is the one that means
// what it says.
func newestSnapshot(dir string) (string, time.Time, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		// Never harvested on this machine. An observation, and the first
		// snapshot is due.
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, fmt.Errorf(
			"read the snapshot directory %s to see when knowledgeC was last preserved: %w", dir, err)
	}

	var (
		newest string
		at     time.Time
	)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		when, ok := snapshotTakenAt(e.Name())
		if !ok || !when.After(at) {
			continue
		}
		newest, at = filepath.Join(dir, e.Name()), when
	}
	return newest, at, nil
}

// snapshotTakenAt reads the instant out of a knowledgeC snapshot's name, and
// reports whether the name was one.
//
// Both shapes are read. Snapshots written before content addressing are
// knowledgeC-<stamp>.db.gz and every one of the 42 files in the live corpus on
// 2026-08-13 is that shape; snapshots written since are
// knowledgeC-<stamp>-<digest>.db.gz, with an optional -<n> that createUnique
// appends when two runs land in the same second. Only the stamp is read, so all
// three forms date the same way and a fourth form that ever appears is refused
// rather than guessed at.
//
// The stamp is local, because that is what compress writes.
func snapshotTakenAt(name string) (time.Time, bool) {
	rest, ok := strings.CutPrefix(name, snapshotPrefix)
	if !ok || len(rest) < len(snapshotTimeLayout) {
		return time.Time{}, false
	}
	when, err := time.ParseInLocation(snapshotTimeLayout, rest[:len(snapshotTimeLayout)], time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return when, true
}
