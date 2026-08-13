// Package knowledgec reads presence and application usage out of a preserved
// knowledgeC snapshot.
//
// knowledgeC is the SQLite database behind macOS's own usage reporting, and it
// is the only source Timetattle has that DIRECTLY OBSERVES AN INTERVAL. Almost
// everything else yields instants: a commit happens at a moment, a file is saved
// at a moment. The backlight going on and off is a span, observed as a span, and
// that is what makes it the denominator for everything else.
//
// # Why this reads a snapshot and never the live database
//
// knowledgeC prunes itself by two independent mechanisms, whichever binds
// first. Most streams expire by AGE: /display/isBacklit and /device/isLocked
// were measured across 43 preserved snapshots on 2026-08-13 holding a shortest
// retained span of 27.86 and 27.51 days and a median of 28.39 and 28.38, which
// is a 28-day window and nothing to do with how busy the machine was.
// /app/usage additionally expires by ROW COUNT, and that is the one that bites:
// macOS lets it grow — 26 of those 43 snapshots held more than 10,000 rows and
// the largest held 11,871 — then trims it back to exactly 10,000, oldest first.
// So 10,000 is a FLOOR rather than a ceiling, and a reader seeing more than
// 10,000 rows is reading a genuinely larger table.
//
// Because the count is what evicts, /app/usage's window moves with the
// workload: 10,000 rows at the typical 824 a day is 12.14 days, matching the
// 12.24-day median retained span actually observed, but at the busiest day
// measured (2,798 rows) the same cap holds only 3.57 days. A specific row was
// watched die between 8.92 and 9.46 days. Reproduce all of it with
// `go run ./scripts/streamseries -streams /app/usage -days`.
//
// That is why internal/snapshot copies the database, strips it to
// the ZOBJECT table and gzips it on every harvest. This package reads those
// files. Reading the live database instead would mean a second Full Disk Access
// path to get wrong, and would tie collection to whatever macOS had not yet
// deleted at the instant the collector ran; a snapshot is a fixed thing that can
// be re-read as often as anyone likes and gives the same answer every time.
//
// Both forms open transparently: a .db.gz as a harvest writes it, or a plain .db
// somebody has already decompressed.
//
// # The four streams, and what each is worth
//
// /display/isBacklit is presence, and is the reason the harvester exists.
// /device/isLocked corroborates it. /app/usage duplicates what Biome already
// reports far better and is collected for cross-checking only. /app/mediaUsage
// is unevaluated.
//
// # The backlight has FOUR states and the stream carries two of them
//
// A snapshot holds rows asserting isBacklit=1 and rows asserting isBacklit=0.
// What neither covers is UNOBSERVED — a span the sampler said nothing about,
// which is an absence of evidence and not evidence of absence. A fourth, BEYOND
// THE SNAPSHOT'S REACH, is not in the stream at all and is a property of when the
// harvest ran.
//
// This package stores the first two as rows carrying their reading, which is what
// keeps the other two available: unobserved is the ABSENCE of a row, so it needs
// no representation and cannot be accidentally collapsed. Conflating it with
// darkness deletes real work — on 2026-07-14 the stream emits nothing of either
// value between 12:35:20 and 14:02:24 while Biome records dense application
// switching throughout, and reading that silence as dark loses 1h27m. Every query
// written before this was understood collapsed it by selecting WHERE value = 1.
//
// # What this package deliberately does not do
//
// It does not merge, deduplicate, resolve or tidy anything. The rows are passed
// through as they are found, and three properties of the real data are the
// reason:
//
//   - EXACT DUPLICATES exist. Three identical off rows sit at 2026-07-10
//     15:22:32. They carry no information beyond the first, and the store's
//     natural key collapses them, so events_new is legitimately below events_seen
//     even on a fresh database.
//   - CONTRADICTORY ROWS exist. At 2026-07-14 09:01:12 there is both an
//     isBacklit=1 and an isBacklit=0 row with identical endpoints. Those are two
//     observations, which is exactly why the reading is part of the store's key.
//     Which one wins where they overlap is a READ-TIME decision, made against the
//     whole picture; deciding it here would bake one answer into the archive
//     where nobody could revisit it. It moves observed presence by 4.4% overall
//     and by 37% of a single measured day, so it is not a detail.
//   - ROWS NEST. 636 on-rows sit inside longer off-rows in one real snapshot.
//     Interval merging is arithmetic for a reader that knows what question is
//     being asked, and a collector does not.
//
// # Every event is an interval, including the ones with no duration
//
// knowledgeC reports a start and an end for every row in these streams, so the
// shape is Interval even where the two are equal — and 113 of 1,670 presence rows
// in a real snapshot are. Shape reflects what the source CLAIMS to have observed,
// never the duration that happened to be measured.
//
// This is not a nicety, and it has a price rather than a rationale. A
// zero-length row satisfies both the point and the interval CHECK in the schema,
// and shape is part of an event's identity in the store, so a collector
// branching on measured duration files the same observation twice — once as a
// point, once as an interval — and deduplication cannot tell they are one thing.
// Committed deliberately against the real snapshot, that mistake stores 583 of
// 14,404 observations twice: see
// TestShapeChosenByDurationWouldDoubleStoreZeroLengthRows in internal/store.
//
// # Timestamps
//
// Core Data counts seconds from 2001-01-01, 978307200 seconds after the Unix
// epoch. Reading one against the wrong epoch lands three decades away, which is
// at least loud. Instants are returned in LOCAL time, matching the other readers
// of local sources: internal/store normalises to UTC on write, and a reader that
// pre-normalised would leave every printed time and every day bucket in this
// package's callers silently shifted.
//
// # No collector here registers itself
//
// internal/biome points at one fixed directory macOS maintains, so it can join
// the package-level registry at init. There is no equivalent path here: a harvest
// writes a NEW snapshot every time it runs, so the file to read is a decision the
// caller makes, on the day. NewestSnapshot is the usual way to make it.
package knowledgec

import (
	"compress/gzip"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/bensyverson/timetattle/internal/source"

	// The driver is pure Go, so Timetattle needs no C toolchain to build.
	_ "modernc.org/sqlite"
)

// The stream names as knowledgeC spells them in ZSTREAMNAME.
const (
	streamBacklight  = "/display/isBacklit"
	streamLocked     = "/device/isLocked"
	streamAppUsage   = "/app/usage"
	streamMediaUsage = "/app/mediaUsage"
)

// The source identifiers, which appear on every event and in every harvest_run
// row. One per stream rather than one per database, so a stream that stalls is
// visible as its own run reporting nothing.
const (
	backlightID  = "knowledgec-backlit"
	lockedID     = "knowledgec-islocked"
	appUsageID   = "knowledgec-appusage"
	mediaUsageID = "knowledgec-mediausage"
)

// coreDataEpoch is 2001-01-01 in Unix seconds.
const coreDataEpoch = 978307200

// snapshotPrefix and snapshotSuffix are how internal/snapshot names what it
// writes: knowledgeC-20260729T211413.db.gz.
const (
	snapshotPrefix = "knowledgeC-"
	snapshotSuffix = ".db.gz"
)

// Backlight collects presence from /display/isBacklit.
//
// This is the only direct observation Timetattle has of a person being at the
// machine, and it is machine-wide: it is an upper bound for any single project
// rather than an attribution of anything. State is "on" for isBacklit=1 and "off"
// for isBacklit=0, and both are stored — see the package comment for why keeping
// the negative observation is what makes "the sampler said nothing" expressible.
type Backlight struct{ reader }

// NewBacklight returns a source reading the snapshot at path.
func NewBacklight(path string) *Backlight {
	return &Backlight{reader{
		path:   path,
		id:     backlightID,
		stream: streamBacklight,
		read:   boolean("on", "off"),
	}}
}

// Locked collects /device/isLocked, which corroborates presence.
//
// # The 1=locked mapping is an inference, not a proven fact
//
// It is read from the field's own name and nothing else. The evidence available
// for it is indirect and weaker than a single figure suggests. Measured against
// knowledgeC-20260729T211413.db.gz, of the 674 rows asserting isLocked=1:
//
//	487 (72%) begin inside a row asserting isBacklit=0
//	268 (40%) begin inside a row asserting isBacklit=1
//	  2       begin where the backlight sampler said nothing
//
// Those do not sum to 674 because backlight rows nest and contradict each other,
// so one instant is covered by rows of both values — which is precisely why the
// second line is not the complement of the first and why "72% correlated" is a
// generous reading of it. It is consistent with 1=locked and does not establish
// it.
//
// So this stream is safe to STORE, and not yet safe to draw presence FROM. If
// something downstream comes to depend on the polarity, the mapping needs an
// experiment — lock the machine at a known instant and read the row back — rather
// than another correlation.
type Locked struct{ reader }

// NewLocked returns a source reading the snapshot at path.
func NewLocked(path string) *Locked {
	return &Locked{reader{
		path:   path,
		id:     lockedID,
		stream: streamLocked,
		read:   boolean("locked", "unlocked"),
	}}
}

// AppUsage collects /app/usage, which is NOT the application attribution path.
//
// internal/biome is. Biome reaches back three weeks unbroken at millisecond
// resolution, where this stream is trimmed to a 10,000-row floor giving under ten
// days; and this stream has been seen to stop, TWICE.
//
// The first stall: it wrote its last row on 2026-07-27 at 14:28:03, produced
// nothing at all on 07-28, and then resumed on its own on 07-29 — 806 rows by
// 21:02, verified in knowledgeC-20260729T211413.db.gz. An earlier reading of that
// stall as a permanent retirement is corrected in
// project/private/2026-07-28-source-corrections.md §1.
//
// The second: it wrote its last row on 2026-08-10 at 19:28:31 and ten
// consecutive snapshots over the following 63h50m held the identical newest
// timestamp while /display/isBacklit recorded normally throughout. Note that
// 2026-07-27 was a Monday and 2026-07-29 a Wednesday, so NEITHER stall is a
// quiet weekend — both ran through working days on a machine that was in use.
// Against this stream's own cadence (median 8s, p99 13m54s, measured from the
// source rather than from the store) they are 61x and 276x the p99. Do not judge
// such a freeze against the stream's LONGEST gap: that gap is the July stall, so
// the comparison is a fault divided by a fault and always looks reassuring.
//
// Both stalls were observed with the table at exactly 10,000 rows, immediately
// after a trim. With one trim witnessed and one inferred that is a hypothesis
// for the next occurrence, not a cause.
//
// What survives all of it is the reason for the architecture rather than the
// story: a source that has demonstrably gone silent for days at a time cannot be
// the one that says which application held the screen. It is collected because it
// is cheap and because cross-checking two independent readings of the same fact
// is how the bare-stop analysis was validated at all.
//
// It also lags. Rows were measured arriving 23 hours behind /display/isBacklit,
// with creation times equal to event times, so a row appears long after the work
// it describes. Collect therefore reads the whole stream out of whatever snapshot
// it is given and filters only on the event's own end — never on when the row
// turned up. See the note on Collect.
type AppUsage struct{ reader }

// NewAppUsage returns a source reading the snapshot at path.
func NewAppUsage(path string) *AppUsage {
	return &AppUsage{reader{
		path:   path,
		id:     appUsageID,
		stream: streamAppUsage,
		read:   application,
	}}
}

// MediaUsage collects /app/mediaUsage, which is unevaluated.
//
// It is collected because it is cheap and perishable, not because anything is
// known about it. In one real snapshot it is 1,495 rows across eight bundle
// identifiers, 88% of them Safari, which is consistent with video playback in a
// browser and equally consistent with several other things. Nothing should be
// inferred from it until somebody measures what it actually tracks.
type MediaUsage struct{ reader }

// NewMediaUsage returns a source reading the snapshot at path.
func NewMediaUsage(path string) *MediaUsage {
	return &MediaUsage{reader{
		path:   path,
		id:     mediaUsageID,
		stream: streamMediaUsage,
		read:   application,
	}}
}

// reading turns one row's value columns into what the event is about and what
// was read. The bool reports whether anything true can be said at all.
type reading func(value sql.NullInt64, text sql.NullString) (source.Identity, string, bool)

// boolean reads a flag stream, where the reading is the whole of the
// observation and the event is about no application.
//
// A value that is neither 0 nor 1 keeps its own decimal spelling rather than
// being dropped or forced into one of the two. It has never occurred — 2,909 rows
// across both flag streams in knowledgeC-20260729T211413.db.gz are all 0 or 1 —
// and if it ever does, an unrecognised reading that was stored can be looked at
// later, while one that was dropped is indistinguishable from a sampler that
// never spoke.
func boolean(whenSet, whenClear string) reading {
	return func(value sql.NullInt64, _ sql.NullString) (source.Identity, string, bool) {
		if !value.Valid {
			// The one row nothing true can be said about. It is not the set
			// reading, not the clear one, and calling it either would invent an
			// observation rather than record one.
			return source.Identity{}, "", false
		}
		switch value.Int64 {
		case 1:
			return source.Identity{}, whenSet, true
		case 0:
			return source.Identity{}, whenClear, true
		default:
			return source.Identity{}, strconv.FormatInt(value.Int64, 10), true
		}
	}
}

// application reads a stream whose subject is a bundle identifier and which takes
// no scalar reading. The identifier is what joins these events to evidence from
// anywhere else, so it is load-bearing rather than a label.
func application(_ sql.NullInt64, text sql.NullString) (source.Identity, string, bool) {
	return source.Identity{App: text.String}, "", true
}

// reader is the shared mechanism. Every one of these streams is the same query
// against a different ZSTREAMNAME, and they differ only in how a row's value
// columns are read — so the differences live in a function and there is one
// implementation of the walk.
type reader struct {
	path   string
	id     string
	stream string
	read   reading
}

// ID implements source.Source.
func (r reader) ID() string { return r.id }

// Kind implements source.Source.
func (r reader) Kind() source.Kind { return source.KindKnowledgeC }

// Collect returns every row of this stream that had not ended before since.
//
// The whole stream is read on every run and filtered on the event's own end.
// Nothing here consults when a row arrived, and there is no column that would
// say: knowledgeC writes rows well after the events they describe — /app/usage
// was measured 23 hours behind /display/isBacklit — so a harvest that advanced a
// watermark to the newest event it had seen would step over everything that
// arrived afterwards, permanently and without a symptom.
//
// The cost is that the same evidence arrives many times, which is why
// deduplication is by content at the store rather than positional here. Re-read a
// generous overlapping window; it is free.
//
// Unlike internal/biome there is no unpaired case to carry: knowledgeC reports
// both endpoints for every row in these streams, so every event is a complete
// interval and since is the whole of the filtering.
func (r reader) Collect(ctx context.Context, since time.Time) ([]source.Event, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	db, done, err := open(r.path)
	if err != nil {
		return nil, err
	}
	defer done()
	defer db.Close()

	// Ordered so that two reads of one snapshot produce the same slice. SQLite
	// makes no promise without it, and a test comparing two collections would
	// then fail for a reason that is not about the data.
	rows, err := db.QueryContext(ctx, `SELECT ZSTARTDATE, ZENDDATE, ZVALUEINTEGER, ZVALUESTRING
		FROM ZOBJECT WHERE ZSTREAMNAME = ? ORDER BY ZSTARTDATE, ZENDDATE`, r.stream)
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w", r.stream, r.path, err)
	}
	defer rows.Close()

	var events []source.Event
	skipped := 0
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		var (
			start, end sql.NullFloat64
			value      sql.NullInt64
			text       sql.NullString
		)
		if err := rows.Scan(&start, &end, &value, &text); err != nil {
			return nil, fmt.Errorf("read a %s row from %s: %w", r.stream, r.path, err)
		}
		// A row missing an endpoint is not an interval, and this source has no
		// shape that would describe it. None exist: all 14,404 rows across the
		// four streams of a real snapshot carry both.
		if !start.Valid || !end.Valid {
			skipped++
			continue
		}
		identity, state, usable := r.read(value, text)
		if !usable {
			skipped++
			continue
		}

		from, to := instant(start.Float64), instant(end.Float64)
		if to.Before(from) {
			// knowledgeC has never written one. It cannot be stored — the
			// schema refuses an interval that ends before it starts — and
			// repairing it here would be inventing a span.
			skipped++
			continue
		}
		if !since.IsZero() && to.Before(since) {
			continue
		}

		e := source.NewInterval(from, to, identity, r.id)
		e.State = state
		// Locator stays empty. It is where evidence was read from when the
		// evidence is ABOUT a file, and neither presence nor application usage
		// is; the snapshot these came from is recorded once on the harvest run
		// rather than on every row.
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read %s rows from %s: %w", r.stream, r.path, err)
	}
	if skipped > 0 {
		// Reported rather than swallowed. A row this reader cannot describe is
		// a fact about the source changing under us, and the number is how
		// anybody would notice.
		slog.Warn("skipped knowledgeC rows that carried no usable observation",
			"source", r.id, "stream", r.stream, "snapshot", r.path, "rows", skipped)
	}
	return events, nil
}

// instant converts Core Data seconds to a local instant.
//
// Rounded to the microsecond, which is what the store keeps and what the sources
// resolve. Every row in a real snapshot carries a whole number of seconds today —
// verified across all 14,404 rows of the four streams — so this differs from the
// simpler truncation in scripts/joinday nowhere at present; it is written this way
// so that a fractional value, if one ever appears, moves an event by nothing
// rather than by up to a second.
func instant(seconds float64) time.Time {
	const micro = 1e6
	total := int64(math.Round(seconds * micro))
	return time.Unix(coreDataEpoch+total/micro, (total%micro)*int64(time.Microsecond))
}

// open opens a snapshot, transparently decompressing a .gz into a temporary file
// first. SQLite cannot read a gzip stream in place, and harvested snapshots are
// always gzipped, so the alternative is every caller gunzipping by hand and
// remembering to clean up.
//
// The returned function removes anything that was staged and must be called even
// when the database is closed some other way.
func open(path string) (*sql.DB, func(), error) {
	noop := func() {}
	if err := readable(path); err != nil {
		return nil, noop, err
	}

	// Not named "source": that identifier is the events package this file
	// depends on, and shadowing it here would be a trap for the next edit.
	dbPath := path
	cleanup := noop
	if strings.HasSuffix(path, ".gz") {
		staged, remove, err := decompress(path)
		if err != nil {
			return nil, noop, err
		}
		dbPath, cleanup = staged, remove
	}

	db, err := sql.Open("sqlite", "file:"+dbPath+"?mode=ro")
	if err != nil {
		cleanup()
		return nil, noop, fmt.Errorf("open snapshot %s: %w", path, err)
	}
	if err := checkSchema(db, path); err != nil {
		db.Close()
		cleanup()
		return nil, noop, err
	}
	return db, cleanup, nil
}

// readable turns the two errors that mean something specific into messages that
// say what to do about them.
//
// A missing file is an ERROR here, and this is the one place this package
// deliberately parts company with internal/biome. Biome reads a fixed directory
// macOS either maintains or does not, so its absence means the source is not
// installed and reporting nothing is the truth. A snapshot path is chosen by the
// caller, from files this program itself wrote; a path that is not there is a
// wrong path, and answering it with an empty harvest would make a typo look
// exactly like a day with no evidence.
func readable(path string) error {
	f, err := os.Open(path)
	if err == nil {
		f.Close()
		return nil
	}
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("no knowledgeC snapshot at %s: %w\n\n"+
			"Snapshots are written by `timetattle harvest`. Run one, or name a snapshot "+
			"that exists — an absent file is not the same as a stream with nothing to report.", path, err)
	case errors.Is(err, fs.ErrPermission):
		// Snapshots live under the user's own directory, so this normally means
		// the path given is macOS's live knowledgeC.db rather than a snapshot of
		// it. That read is gated, and it is gated per binary.
		return fmt.Errorf("cannot read %s: %w\n\n"+
			"If that is macOS's own knowledgeC.db, this package does not read it — pass a "+
			"snapshot from `timetattle harvest` instead. If it is a snapshot, this is almost\n"+
			"always a missing Full Disk Access grant. Open System Settings > Privacy &\n"+
			"Security > Full Disk Access, add the program running Timetattle, then restart it.",
			path, err)
	default:
		return fmt.Errorf("cannot read %s: %w", path, err)
	}
}

// decompress stages a gzipped snapshot as a temporary file.
func decompress(path string) (string, func(), error) {
	noop := func() {}

	in, err := os.Open(path)
	if err != nil {
		return "", noop, fmt.Errorf("open snapshot %s: %w", path, err)
	}
	defer in.Close()

	zr, err := gzip.NewReader(in)
	if err != nil {
		return "", noop, fmt.Errorf("decompress snapshot %s: %w", path, err)
	}
	defer zr.Close()

	tmp, err := os.CreateTemp("", "timetattle-knowledgec-*.db")
	if err != nil {
		return "", noop, fmt.Errorf("stage snapshot %s: %w", path, err)
	}
	remove := func() { os.Remove(tmp.Name()) }
	if _, err := io.Copy(tmp, zr); err != nil {
		tmp.Close()
		remove()
		return "", noop, fmt.Errorf("stage snapshot %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		remove()
		return "", noop, fmt.Errorf("stage snapshot %s: %w", path, err)
	}
	return tmp.Name(), remove, nil
}

// checkSchema fails early on a file that is not a knowledgeC snapshot, so that
// pointing at the wrong database says so rather than reporting a source with
// nothing to say.
func checkSchema(db *sql.DB, path string) error {
	var name string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='ZOBJECT'`).Scan(&name)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%s has no ZOBJECT table; is it a knowledgeC snapshot?", path)
	}
	if err != nil {
		return fmt.Errorf("read the schema of %s: %w", path, err)
	}
	return nil
}

// NewestSnapshot is the most recently harvested snapshot in dir.
//
// Snapshot names carry a timestamp, so lexical order is chronological order and
// no file needs opening to choose between them. The suffix is checked as well as
// the prefix: the directory also holds a biome/ subdirectory, and anything a
// person drops in there must not be mistaken for history.
//
// This is a helper for callers, not a default. There is no fixed snapshot to
// read — a harvest writes a new one every time — so which file a collection reads
// is a decision made on the day, and it is recorded on the harvest run.
func NewestSnapshot(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read snapshots from %s: %w\n\nRun `timetattle harvest` first.", dir, err)
	}

	var best string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, snapshotPrefix) || !strings.HasSuffix(name, snapshotSuffix) {
			continue
		}
		if name > best {
			best = name
		}
	}
	if best == "" {
		return "", fmt.Errorf("%s holds no %s*%s snapshots; run `timetattle harvest` first",
			dir, snapshotPrefix, snapshotSuffix)
	}
	return filepath.Join(dir, best), nil
}
