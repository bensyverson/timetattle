// Package biome reads the focus history macOS keeps in its Biome event
// streams.
//
// Biome is the undocumented store behind Screen Time. Its
// ScreenTime.AppUsage stream is a run of 512 KB binary segments, each holding
// tens of thousands of records saying that an application became frontmost or
// gave that position up, to the millisecond. On the machine this was built
// against it reached back three weeks unbroken — far deeper than knowledgeC,
// which prunes /app/usage to under ten days, and at far finer resolution than
// anything a user could reconstruct from memory.
//
// # What a pair of records means
//
// Focus is exclusive, so the records interleave rather than nest: the instant
// one application resigns is the instant the next takes over, to the same
// microsecond. A working interval is therefore a start paired with the *next*
// stop carrying the same bundle identifier, not with the next stop of any
// kind.
//
// # Records that do not pair, and why they are not dropped
//
// Roughly one stop in sixty has no start before it. This is not corruption.
// Checked against knowledgeC's own /app/usage table, each one lines up with a
// session that began when the machine woke: the application was already
// frontmost, so nothing "became frontmost" and macOS wrote no start. In one
// measured case Safari resigned at 09:02:00, the machine slept, and the next
// record was a bare Safari stop at 09:07:09 — for which knowledgeC shows a
// fresh two-second session beginning at 09:07:07.
//
// The tempting repair is to date such a stop from the record before it. That
// is precisely wrong: the gap is sleep, and doing so would have charged five
// minutes of an idle machine to Safari in place of two seconds of use. So an
// unpaired record keeps the instant it has, reports the other side as unknown,
// and says why — which is the whole of what was observed. The mirror case is
// ordinary rather than rare: the newest segment usually ends with a start,
// because something is frontmost right now.
//
// One thing that account gets wrong, and it matters for anything that reasons
// about the missing span: a bare stop is CAUSED by the wake but DATED at the
// sleep. The absent start belongs to the moment the machine resumed; the stop
// itself is written whenever that application finally resigns focus, and 74 of
// the 385 sit within four seconds of the leading edge of a recorded dark block,
// none deeper. So the implied interval runs from a wake to a sleep and can be
// long, with the record sitting at its end rather than its beginning.
//
// The reason matters more than it looks, because the two need opposite
// treatment and are indistinguishable afterwards. A start at the end of the
// run is a horizon and the next harvest completes it. A bare stop never
// completes; its repair is bounding against presence, which needs two sources
// at once and therefore belongs above this package. A live stream of 48,047
// records is 23,832 pairs, 382 bare stops and exactly one horizon.
//
// The converse half of that story — that a stop is always written *at* sleep, so
// a paired interval never quietly contains one — is the assumption every long
// duration here rests on, and it has been tested rather than assumed. Across
// 23,328 paired intervals and 93 recorded sleeps totalling 265h, no interval
// encloses a sleep by either of two independent tests. So an interval's duration
// can be taken at face value and needs no sleep correction above this package.
// See project/private/2026-07-29-sleep-asymmetry.md and scripts/sleepcheck.
//
// # What this package does not do
//
// It reports focus changes and nothing else. Whether two intervals twenty
// minutes apart belong to one working session is inference, calibrated
// elsewhere against ground truth, and a source that decided it locally would
// make that calibration impossible to perform consistently.
package biome

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// appUsageID identifies this source on every event it produces.
const appUsageID = "biome-appusage"

// appUsagePath is where macOS keeps the stream, under the user's home.
const appUsagePath = "Library/Biome/streams/restricted/ScreenTime.AppUsage/local"

// AppUsage collects focus intervals from the ScreenTime.AppUsage stream.
//
// A sibling stream, App.InFocus, describes the same thing. It was last
// appended to a day before this was written and appears to be on its way out,
// so it is noted here rather than depended on.
type AppUsage struct {
	// dir is empty on the package-level default instance registered by init,
	// deliberately: init runs once per process, before a test's
	// t.Setenv("HOME", ...) has any effect, so resolving DefaultAppUsageDir
	// eagerly here would bake in whatever machine's real home directory
	// happened to exist at process start, immune to every later test's own
	// isolation. resolveDir resolves it fresh on every Collect instead.
	dir string
}

// NewAppUsage returns a source reading the segments in dir.
func NewAppUsage(dir string) *AppUsage { return &AppUsage{dir: dir} }

// NewDefaultAppUsage returns a source that resolves the current user's Biome
// directory freshly on every Collect, rather than once at construction — see
// the dir field's doc comment for why that distinction matters.
func NewDefaultAppUsage() *AppUsage { return &AppUsage{} }

// resolveDir returns the directory this instance reads, resolving
// DefaultAppUsageDir on every call when none was given explicitly.
func (a *AppUsage) resolveDir() (string, error) {
	if a.dir != "" {
		return a.dir, nil
	}
	return DefaultAppUsageDir()
}

// ID implements source.Source.
func (a *AppUsage) ID() string { return appUsageID }

// Kind implements source.Source.
func (a *AppUsage) Kind() source.Kind { return source.KindBiome }

// Collect returns the focus intervals that had not ended before since,
// together with every observation whose end was never seen at all.
//
// Every segment is read on every run, whatever since is. Segments are only a
// few megabytes in total, and an interval routinely begins in one and ends in
// the next — reading a subset would silently cut those in half.
func (a *AppUsage) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	segments, err := a.segments(ctx)
	if err != nil {
		return nil, err
	}

	// Ordering by the creation time inside the header rather than by the file
	// name keeps the name's Apple-epoch microseconds out of this package
	// entirely, and holds up if macOS ever names segments differently.
	sort.SliceStable(segments, func(i, j int) bool {
		if segments[i].created.Equal(segments[j].created) {
			return segments[i].path < segments[j].path
		}
		return segments[i].created.Before(segments[j].created)
	})

	var changes []focusChange
	for _, seg := range segments {
		changes = append(changes, seg.changes...)
	}

	events := pair(changes)

	// An interval that was still running when the last harvest finished is
	// not old news, so what since removes is what had already ended. An event
	// whose end was never observed has not ended at all, and so has reached no
	// instant at which it could become old news — filtering those on the start
	// they do have would drop the record saying what is on screen right now,
	// on every single harvest.
	if since.IsZero() {
		return events, nil
	}
	kept := events[:0]
	for _, e := range events {
		if e.End.IsZero() || !e.End.Before(since) {
			kept = append(kept, e)
		}
	}
	return kept, nil
}

// segments reads every segment in the directory.
//
// A directory that is not there is not a failure: it is what a machine
// without this stream looks like, and it is indistinguishable from an
// uninstalled application. A directory that refuses to be read is different,
// and says so.
func (a *AppUsage) segments(ctx context.Context) ([]segment, error) {
	dir, err := a.resolveDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("cannot read %s: %w\n\n"+
			"This is almost always a missing Full Disk Access grant. Open\n"+
			"System Settings > Privacy & Security > Full Disk Access, add the\n"+
			"program running Timetattle, then restart it.", dir, err)
	case err != nil:
		return nil, fmt.Errorf("read the Biome stream at %s: %w", dir, err)
	}

	var out []segment
	for _, entry := range entries {
		// Biome keeps a tombstone directory beside the segments.
		if entry.IsDir() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seg, err := readSegment(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, seg)
	}
	return out, nil
}

// pair turns a chronological run of focus changes into intervals.
//
// The bookkeeping is one open start per bundle identifier rather than a
// single previous record, because applications interleave. Anything that
// cannot be paired becomes an event at its own instant with no duration: see
// the package comment for why inventing the missing end would be worse than
// admitting it is missing.
func pair(changes []focusChange) []source.Event {
	open := make(map[string]time.Time)
	events := make([]source.Event, 0, len(changes)/2)

	for _, c := range changes {
		if c.frontmost {
			// Two starts with nothing between them cannot both be closed by
			// the eventual stop, and stretching the first over the second
			// would double-count the overlap. The earlier one is lost rather
			// than a horizon: records already sit past it, so no later
			// harvest can supply the stop.
			if began, ok := open[c.bundle]; ok {
				events = append(events, openEnd(began, c.bundle, source.UnknownLost))
			}
			open[c.bundle] = c.at
			continue
		}

		began, ok := open[c.bundle]
		switch {
		case !ok:
			// The machine woke with this application already frontmost, so
			// nothing became frontmost and macOS wrote no start.
			events = append(events, openStart(c.at, c.bundle, source.UnknownSleep))
		case c.at.Before(began):
			// A stop before the start it would close describes no span of
			// time. Real segments are strictly ordered, so this means the
			// file is telling us something impossible rather than something
			// surprising — there is no usable start, and unlike sleep we
			// cannot say why.
			events = append(events, openStart(c.at, c.bundle, source.UnknownLost))
		default:
			delete(open, c.bundle)
			events = append(events, source.NewInterval(began, c.at, source.Identity{App: c.bundle}, appUsageID))
		}
	}

	// Whatever is still frontmost when the records run out. This is the only
	// unpaired case a later harvest completes, which is why it is marked as a
	// horizon and the others are not.
	for bundle, began := range open {
		events = append(events, openEnd(began, bundle, source.UnknownHorizon))
	}

	// Ordering keys on the endpoint that was observed rather than on Start,
	// because a bare stop has no start: sorting those by Start would gather
	// every one of them at the zero time, decades ahead of the run.
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].Anchor().Equal(events[j].Anchor()) {
			return events[i].Identity.App < events[j].Identity.App
		}
		return events[i].Anchor().Before(events[j].Anchor())
	})
	return events
}

// openStart records a focus change seen only as it ended, and openEnd one seen
// only as it began. Both report no duration, which is the whole of what was
// observed: see the package comment for why inventing the missing edge from
// the surrounding records would be worse than admitting it is missing.
func openStart(end time.Time, bundle string, why source.Unknown) source.Event {
	return source.NewOpenStart(end, why, source.Identity{App: bundle}, appUsageID)
}

func openEnd(start time.Time, bundle string, why source.Unknown) source.Event {
	return source.NewOpenEnd(start, why, source.Identity{App: bundle}, appUsageID)
}

// DefaultAppUsageDir is where macOS keeps the stream for the current user.
func DefaultAppUsageDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, appUsagePath), nil
}

// init puts this source in the package-level registry, so a caller collects
// from it without knowing it exists.
//
// A machine with no home directory has no stream to point at, and registering
// a source with nowhere to read would turn that into a per-harvest error
// message about something nobody can fix. Registrability is checked here,
// but the directory itself is not kept — see NewDefaultAppUsage's doc comment.
func init() {
	if _, err := DefaultAppUsageDir(); err != nil {
		return
	}
	source.Register(NewDefaultAppUsage())
}
