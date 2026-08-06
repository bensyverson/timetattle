//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package manifest_test

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
	"github.com/bensyverson/timetattle/internal/manifest"
	"github.com/bensyverson/timetattle/internal/source"
	"github.com/bensyverson/timetattle/internal/timestamped"
)

// The tests in this file run against whatever is actually on the machine, and
// skip cleanly when it is not there. That is the only way to find out whether
// these manifests describe real files rather than invented ones — a synthetic
// fixture would only prove that the fixture matches the pattern that generated
// it. They follow the pattern in internal/biome/realdata_test.go.

// groundTruth is the day this project has independent ground truth for. Motion
// work is confirmed at 06:00-06:50 and 14:01-14:22, and Biome holds Motion
// focus records covering both blocks.
var groundTruth = time.Date(2026, 7, 28, 0, 0, 0, 0, time.Local)

// collect reads one built-in source against the real filesystem.
func collect(t *testing.T, id string) []source.Event {
	t.Helper()
	sources, err := manifest.Builtin()
	if err != nil {
		t.Fatalf("Builtin() = %v", err)
	}
	spec := find(t, sources, id)

	if spec.Discovery == manifest.DiscoverySpotlight {
		if _, err := exec.LookPath("mdfind"); err != nil {
			t.Skipf("source %q discovers through Spotlight and mdfind is not on PATH: %v", id, err)
		}
	} else if _, err := os.Stat(spec.Files.Root); err != nil {
		t.Skipf("source %q has no root at %s: %v", id, spec.Files.Root, err)
	}

	reader, err := timestamped.New(spec)
	if err != nil {
		t.Fatalf("timestamped.New(%q) = %v", id, err)
	}
	events, err := reader.Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect(%q) = %v", id, err)
	}
	return events
}

// TestMotionManifestReadsTheRealVault is the check that the built-in describes
// the vault as it is rather than as it was imagined.
func TestMotionManifestReadsTheRealVault(t *testing.T) {
	events := collect(t, "motion-autosave")
	if len(events) == 0 {
		t.Skip("the motion-autosave source collected no events")
	}

	days := make(map[string]int)
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v is invalid: %v", e, err)
		}
		if e.Identity.App != "com.apple.motionapp" {
			t.Fatalf("event %+v carries app %q; without it nothing can be joined to focus",
				e, e.Identity.App)
		}
		// Declared by the manifest and carried on every real event, because
		// nothing about the file recovers it afterwards. A constructed fixture
		// proves the wiring; only the real vault proves the shipped manifest
		// actually says it.
		if e.Cause != source.CauseTimer {
			t.Fatalf("event for %q reports cause %q, want %q; Motion autosaves on a wall-clock timer",
				e.Identity.Document, e.Cause, source.CauseTimer)
		}
		// The composition name is the document, taken from the filename with
		// its timestamp match removed; several real ones contain spaces and
		// dashes. An empty document means the timestamp pattern stopped
		// matching the vault's layout.
		if e.Identity.Document == "" {
			t.Error("event has no document; the timestamp pattern did not match its filename")
		}
		// Project is filled by a later resolution pass, not by this reader.
		if e.Identity.Project != "" {
			t.Errorf("event for %q carries project %q, want empty; that is a later pass's job",
				e.Identity.Document, e.Identity.Project)
		}
		// A filename timestamp read as UTC rather than local would shift every
		// point by the machine's offset, which is exactly the error the join
		// below would otherwise blame on Biome.
		if e.Start.Year() < 2000 || e.Start.After(time.Now().AddDate(0, 0, 1)) {
			t.Fatalf("event for %q is dated %s, which is not a plausible instant", e.Identity.Document, e.Start)
		}
		days[e.Start.Format("2006-01-02")]++
	}
	t.Logf("%d autosaves across %d days", len(events), len(days))
}

// TestBlenderManifestFindsBackupsThroughSpotlight exercises the other discovery
// strategy end to end. .blend1 files are scattered wherever their .blend lives,
// which is why this source cannot be a bounded glob.
func TestBlenderManifestFindsBackupsThroughSpotlight(t *testing.T) {
	events := collect(t, "blender-backups")
	if len(events) == 0 {
		t.Skip("the blender-backups source collected no events")
	}
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v is invalid: %v", e, err)
		}
		if e.Identity.App != "org.blenderfoundation.blender" {
			t.Fatalf("event %+v carries app %q", e, e.Identity.App)
		}
		if e.Cause != source.CauseUserAction {
			t.Fatalf("event for %q reports cause %q, want %q; a .blend1 is written by a save",
				e.Identity.Document, e.Cause, source.CauseUserAction)
		}
		if e.Start.Year() < 2000 || e.Start.After(time.Now().AddDate(0, 0, 1)) {
			t.Fatalf("event for %q is dated %s, which is not a plausible instant", e.Identity.Document, e.Start)
		}
	}
	t.Logf("%d .blend1 backups, oldest %s, newest %s",
		len(events), events[0].Start.Format(time.RFC3339), events[len(events)-1].Start.Format(time.RFC3339))
}

// TestMotionAutosavesJoinBiomeFocus is the whole point of shipping these
// manifests: a Motion autosave says WHICH composition was open, Biome says WHEN
// Motion held focus, and neither answers "how long on this composition" alone.
// source.Event.Contains performs the join and requires Identity.App to match on
// both sides, so this also proves the manifest's "app" field is load-bearing.
//
// The assertion is deliberately loose about exact counts and strict about
// magnitude. A point is expected to occasionally land just outside an interval,
// because Motion's autosave timer fires on wall-clock time whether or not the
// application is frontmost; a point landing HOURS away would mean an epoch or
// timezone error, which is the failure worth catching.
func TestMotionAutosavesJoinBiomeFocus(t *testing.T) {
	points := onDay(collect(t, "motion-autosave"), groundTruth)
	if len(points) == 0 {
		t.Skipf("the motion-autosave source collected no events dated %s", groundTruth.Format("2006-01-02"))
	}

	dir, err := biome.DefaultAppUsageDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	if _, err := os.ReadDir(dir); err != nil {
		t.Skipf("no readable ScreenTime.AppUsage stream at %s: %v", dir, err)
	}
	focus, err := biome.NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("biome Collect: %v", err)
	}

	var intervals []source.Event
	for _, e := range onDay(focus, groundTruth) {
		if e.Identity.App == "com.apple.motionapp" && e.Shape == source.Interval && e.Duration() > 0 {
			intervals = append(intervals, e)
		}
	}
	if len(intervals) == 0 {
		t.Skipf("%s carries no com.apple.motionapp focus interval dated %s",
			dir, groundTruth.Format("2006-01-02"))
	}

	var inside, outside int
	for _, p := range points {
		hit := false
		for _, iv := range intervals {
			if iv.Contains(p) {
				hit = true
				t.Logf("inside:  %s %-24s within %s-%s",
					p.Start.In(time.Local).Format("15:04:05"), p.Identity.Document,
					iv.Start.In(time.Local).Format("15:04:05"), iv.End.In(time.Local).Format("15:04:05"))
				break
			}
		}
		if hit {
			inside++
			continue
		}
		outside++
		gap, nearest := nearestGap(p, intervals)
		t.Logf("outside: %s %-24s nearest %s-%s, %s away",
			p.Start.In(time.Local).Format("15:04:05"), p.Identity.Document,
			nearest.Start.In(time.Local).Format("15:04:05"), nearest.End.In(time.Local).Format("15:04:05"),
			gap.Round(time.Second))
		// Ten minutes is not a calibrated threshold and nothing downstream
		// should use one; it is wide enough that an autosave firing during an
		// app switch passes and narrow enough that a zone error cannot.
		if gap > 10*time.Minute {
			t.Errorf("autosave of %s at %s is %s from the nearest Motion focus interval; "+
				"a gap this size means the two sources disagree about the clock, not about the work",
				p.Identity.Document, p.Start.In(time.Local).Format("15:04:05"), gap.Round(time.Second))
		}
	}

	if inside == 0 {
		t.Errorf("none of the %d autosaves on %s fell inside any of the %d Motion focus intervals",
			len(points), groundTruth.Format("2006-01-02"), len(intervals))
	}
	t.Logf("%s: %d autosaves, %d Motion focus intervals, %d inside, %d outside",
		groundTruth.Format("2006-01-02"), len(points), len(intervals), inside, outside)
}

// onDay returns the events that begin on the given local day.
func onDay(events []source.Event, day time.Time) []source.Event {
	var out []source.Event
	want := day.Format("2006-01-02")
	for _, e := range events {
		if e.Start.In(time.Local).Format("2006-01-02") == want {
			out = append(out, e)
		}
	}
	return out
}

// nearestGap returns how far a point sits from the closest interval, and which
// one. A point outside every interval is a finding worth describing rather than
// a number to round away.
func nearestGap(p source.Event, intervals []source.Event) (time.Duration, source.Event) {
	var (
		best    time.Duration
		nearest source.Event
	)
	for i, iv := range intervals {
		var d time.Duration
		switch {
		case p.Start.Before(iv.Start):
			d = iv.Start.Sub(p.Start)
		case p.Start.After(iv.End):
			d = p.Start.Sub(iv.End)
		}
		if i == 0 || d < best {
			best, nearest = d, iv
		}
	}
	return best, nearest
}
