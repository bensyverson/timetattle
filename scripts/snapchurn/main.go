// Command snapchurn measures how much of a knowledgeC snapshot corpus is a
// repeat of what the corpus already holds, and at what granularity.
//
// # Why it exists
//
// Snapshot storage is what caps the harvest cadence. The proposal it was
// written to test — content-address knowledgeC snapshots the way
// internal/snapshot/vault.go and biome.go already content-address their
// sources, so a run that would write bytes already on disk writes nothing —
// is obviously correct and, on its own, not obviously sufficient. Whether it
// is sufficient depends entirely on how often a knowledgeC snapshot is
// bit-for-bit what the last one was, and that is a measurement, not an
// argument. This is the measurement.
//
// It reports three separate things, and keeping them apart is the point:
//
//   - WHOLE-SNAPSHOT redundancy. How many files on disk are byte-identical to
//     another file on disk. This is exactly what a write-time digest check
//     would have prevented, so it is the yield of that check against the
//     history actually recorded — no more and no less.
//
//   - ROW redundancy. How many ZOBJECT row-instances the corpus stores versus
//     how many DISTINCT rows it holds. Two snapshots taken an hour apart differ
//     by a few dozen rows out of twenty-odd thousand, so this number is far
//     larger than the first, and it is the size of the prize a finer-grained
//     scheme would be going after.
//
//   - PAIRWISE churn. For each consecutive pair of snapshots, the elapsed
//     time and the rows added and removed between them. This is what says
//     whether the corpus grows by accretion or by replacement, and it is the
//     only one of the three that observes DELETION at all.
//
// # What it cannot see, and says so
//
// The corpus is a record of when a harvest RAN, not of what the source was
// doing between runs. Snapshots here sit anywhere from eleven seconds to two
// days apart, so no pair of them observes an hour of source activity directly.
// The -cadence projection therefore does not measure a cadence that was never
// run: it reads row creation times out of the newest snapshot and counts how
// many cadence windows in the retained span saw no row created at all.
//
// That projection is an UPPER BOUND on how often a content check would skip a
// write, and the bound is one-sided for a reason worth stating: a window with
// no creation can still change contents, because rows also age out of
// knowledgeC's retention window, and an aged-out row changes the snapshot as
// surely as a new one does. The pairwise section measures deletion where the
// corpus can see it; the projection cannot, and the two must not be quoted as
// though they were one figure.
//
// Usage:
//
//	go run ./scripts/snapchurn                    # the live snapshot directory
//	go run ./scripts/snapchurn -dir path/to/dir   # somewhere else
//	go run ./scripts/snapchurn -cadence 4h        # project a different schedule
//	go run ./scripts/snapchurn -pairs             # per-pair churn detail
package main

import (
	"compress/gzip"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	_ "modernc.org/sqlite"

	"github.com/bensyverson/timetattle/internal/snapshot"
)

// coreDataEpoch is 2001-01-01 UTC; Core Data stores seconds from there.
const coreDataEpoch = 978307200

// rowDigest is a truncated SHA-256 of one ZOBJECT row's column values. 16
// bytes is 128 bits, which is the same margin biome.go's digestLength keeps
// and for the same reason: a collision here would be counted as a repeat.
type rowDigest [16]byte

// snap is one snapshot file, read once.
type snap struct {
	name string
	// taken is the instant in the file name, which is when the harvest ran.
	taken time.Time
	// gzBytes is the size on disk.
	gzBytes int64
	// gzSum is the digest of the compressed file exactly as stored, which is
	// the thing a write-time check would compare.
	gzSum [32]byte
	// plainSum is the digest of the decompressed database. It is reported
	// beside gzSum so that a compressor that ever stops being deterministic
	// shows up as a disagreement between the two rather than as silence.
	plainSum [32]byte
	rows     []rowDigest
	set      map[rowDigest]struct{}
	// creations are the ZCREATIONDATE values, in Unix seconds, of every row.
	creations []int64
}

func main() {
	dir := flag.String("dir", "", "snapshot directory (default: ~/.timetattle/snapshots)")
	cadence := flag.Duration("cadence", time.Hour, "cadence to project a content check against")
	window := flag.Duration("window", 14*24*time.Hour, "trailing span the projection is quoted over")
	pairs := flag.Bool("pairs", false, "print per-pair churn")
	flag.Parse()

	if err := run(*dir, *cadence, *window, *pairs, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(dir string, cadence, window time.Duration, pairs bool, out io.Writer) error {
	if dir == "" {
		d, err := snapshot.DefaultDir()
		if err != nil {
			return err
		}
		dir = d
	}
	if cadence <= 0 {
		return fmt.Errorf("a cadence of %s is not a schedule anything could run on", cadence)
	}

	paths, err := filepath.Glob(filepath.Join(dir, "knowledgeC-*.db.gz"))
	if err != nil {
		return fmt.Errorf("list snapshots in %s: %w", dir, err)
	}
	if len(paths) == 0 {
		// Nothing to measure is not the same as a corpus with no redundancy,
		// and printing zeroes would read as the latter.
		return fmt.Errorf("no knowledgeC snapshots in %s; there is nothing to measure", dir)
	}
	sort.Strings(paths)

	snaps := make([]*snap, 0, len(paths))
	for _, p := range paths {
		s, err := readSnap(p)
		if err != nil {
			return err
		}
		snaps = append(snaps, s)
	}
	sort.Slice(snaps, func(i, j int) bool { return snaps[i].taken.Before(snaps[j].taken) })

	reportWhole(out, dir, snaps)
	reportRows(out, snaps)
	reportPairs(out, snaps, pairs)
	reportProjection(out, snaps[len(snaps)-1], cadence, window)
	return nil
}

// readSnap decompresses one snapshot and digests it whole and row by row.
func readSnap(path string) (*snap, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	s := &snap{name: filepath.Base(path), gzBytes: info.Size()}

	stamp := strings.TrimPrefix(strings.TrimSuffix(s.name, ".db.gz"), "knowledgeC-")
	// A collision counter suffix (-1, -2) follows the timestamp.
	if i := strings.IndexByte(stamp, '-'); i >= 0 {
		stamp = stamp[:i]
	}
	// The name is stamped in local time by snapshot.Take; parsing it as local
	// keeps the projection's window arithmetic honest against the row times,
	// which are absolute.
	if s.taken, err = time.ParseInLocation("20060102T150405", stamp, time.Local); err != nil {
		return nil, fmt.Errorf("read the instant out of %s: %w", s.name, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	s.gzSum = sha256.Sum256(raw)

	plain, err := gunzip(raw)
	if err != nil {
		return nil, fmt.Errorf("decompress %s: %w", s.name, err)
	}
	s.plainSum = sha256.Sum256(plain)

	tmp, err := os.CreateTemp("", "snapchurn-*.db")
	if err != nil {
		return nil, fmt.Errorf("stage %s: %w", s.name, err)
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(plain); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("stage %s: %w", s.name, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("stage %s: %w", s.name, err)
	}

	if err := digestRows(tmp.Name(), s); err != nil {
		return nil, fmt.Errorf("%s: %w", s.name, err)
	}
	return s, nil
}

func gunzip(raw []byte) ([]byte, error) {
	zr, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// digestRows hashes every ZOBJECT row's column values.
//
// The digest covers the whole row rather than ZUUID, and that is the same
// argument biome.go makes about segment names: a stable identifier can be
// revisited with different contents behind it — knowledgeC extends an
// interval's ZENDDATE in place while an app stays in focus — so treating two
// rows with one UUID as one row would count a changed row as a repeat and
// report redundancy that is not there.
func digestRows(path string, s *snap) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer db.Close()

	rows, err := db.Query(`SELECT * FROM ZOBJECT ORDER BY Z_PK`)
	if err != nil {
		return fmt.Errorf("read ZOBJECT: %w", err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		return fmt.Errorf("read ZOBJECT columns: %w", err)
	}
	creation := -1
	for i, c := range cols {
		if strings.EqualFold(c, "ZCREATIONDATE") {
			creation = i
		}
	}

	s.set = make(map[rowDigest]struct{})
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("scan a ZOBJECT row: %w", err)
		}
		d := digestValues(vals)
		s.rows = append(s.rows, d)
		s.set[d] = struct{}{}
		if creation >= 0 {
			if secs, ok := asSeconds(vals[creation]); ok {
				s.creations = append(s.creations, secs+coreDataEpoch)
			}
		}
	}
	return rows.Err()
}

// digestValues hashes one row. Each value is preceded by a type tag so that a
// NULL, an empty string and a zero can never hash alike — collapsing those
// three is how a redundancy count comes to overstate itself.
func digestValues(vals []any) rowDigest {
	h := sha256.New()
	var num [8]byte
	for _, v := range vals {
		switch t := v.(type) {
		case nil:
			h.Write([]byte{0})
		case int64:
			h.Write([]byte{1})
			binary.LittleEndian.PutUint64(num[:], uint64(t))
			h.Write(num[:])
		case float64:
			h.Write([]byte{2})
			binary.LittleEndian.PutUint64(num[:], uint64(int64(t*1e6)))
			h.Write(num[:])
		case string:
			h.Write([]byte{3})
			h.Write([]byte(t))
		case []byte:
			h.Write([]byte{4})
			h.Write(t)
		case bool:
			h.Write([]byte{5})
			if t {
				h.Write([]byte{1})
			} else {
				h.Write([]byte{0})
			}
		case time.Time:
			h.Write([]byte{6})
			binary.LittleEndian.PutUint64(num[:], uint64(t.UnixMicro()))
			h.Write(num[:])
		default:
			h.Write([]byte{7})
			fmt.Fprintf(h, "%v", t)
		}
		h.Write([]byte{0xff})
	}
	var d rowDigest
	copy(d[:], h.Sum(nil))
	return d
}

func asSeconds(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case float64:
		return int64(t), true
	case time.Time:
		return t.Unix() - coreDataEpoch, true
	}
	return 0, false
}

func reportWhole(out io.Writer, dir string, snaps []*snap) {
	seen := make(map[[32]byte]int)
	var total, repeat int64
	repeats := 0
	for _, s := range snaps {
		total += s.gzBytes
		if seen[s.gzSum] > 0 {
			repeats++
			repeat += s.gzBytes
		}
		seen[s.gzSum]++
	}
	// The two digests are taken over the same evidence through different
	// layers. They must agree on how many distinct snapshots there are; if
	// they ever do not, the compressor has stopped being a pure function of
	// its input and every conclusion below is suspect.
	plain := make(map[[32]byte]struct{})
	for _, s := range snaps {
		plain[s.plainSum] = struct{}{}
	}

	fmt.Fprintf(out, "WHOLE-SNAPSHOT REDUNDANCY  %s\n", dir)
	fmt.Fprintf(out, "  %d snapshots, %.1f MB on disk\n", len(snaps), mb(total))
	fmt.Fprintf(out, "  %d distinct by compressed bytes, %d distinct by decompressed bytes\n", len(seen), len(plain))
	if len(seen) != len(plain) {
		fmt.Fprintf(out, "  WARNING: those two disagree, so the compressed form is not a\n"+
			"           function of the contents alone and a digest taken over it\n"+
			"           would skip fewer writes than it should.\n")
	}
	fmt.Fprintf(out, "  %d files repeat a digest already on disk, %.1f MB (%.1f%% of the corpus)\n",
		repeats, mb(repeat), pct(repeat, total))
	fmt.Fprintf(out, "  A write-time digest check would have skipped exactly those %d writes.\n\n", repeats)
}

func reportRows(out io.Writer, snaps []*snap) {
	distinct := make(map[rowDigest]struct{})
	var instances int
	for _, s := range snaps {
		instances += len(s.rows)
		for d := range s.set {
			distinct[d] = struct{}{}
		}
	}
	fmt.Fprintf(out, "ROW REDUNDANCY\n")
	fmt.Fprintf(out, "  %d ZOBJECT row-instances stored across the corpus\n", instances)
	fmt.Fprintf(out, "  %d distinct rows actually held\n", len(distinct))
	if len(distinct) > 0 {
		fmt.Fprintf(out, "  %.1fx redundancy: %.1f%% of stored rows are a row the corpus already had\n",
			float64(instances)/float64(len(distinct)),
			pct(int64(instances-len(distinct)), int64(instances)))
	}
	fmt.Fprintf(out, "  This is NOT what a whole-snapshot digest recovers. It is the size of\n"+
		"  the prize a row-level scheme would be going after, and it is reported\n"+
		"  here so the two are never quoted as one number.\n\n")
}

func reportPairs(out io.Writer, snaps []*snap, detail bool) {
	if len(snaps) < 2 {
		return
	}
	var identical, withAdds, withRemovals int
	var addTotal, removeTotal int
	var w *tabwriter.Writer
	if detail {
		w = tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "  from\tto\telapsed\tadded\tremoved")
	}
	for i := 1; i < len(snaps); i++ {
		prev, cur := snaps[i-1], snaps[i]
		added, removed := 0, 0
		for d := range cur.set {
			if _, ok := prev.set[d]; !ok {
				added++
			}
		}
		for d := range prev.set {
			if _, ok := cur.set[d]; !ok {
				removed++
			}
		}
		addTotal += added
		removeTotal += removed
		if added == 0 && removed == 0 {
			identical++
		}
		if added > 0 {
			withAdds++
		}
		if removed > 0 {
			withRemovals++
		}
		if detail {
			fmt.Fprintf(w, "  %s\t%s\t%s\t%d\t%d\n",
				prev.taken.Format("01-02 15:04"), cur.taken.Format("01-02 15:04"),
				cur.taken.Sub(prev.taken).Round(time.Minute), added, removed)
		}
	}
	if detail {
		w.Flush()
	}
	fmt.Fprintf(out, "PAIRWISE CHURN  (%d consecutive pairs)\n", len(snaps)-1)
	fmt.Fprintf(out, "  %d pairs identical in every row\n", identical)
	fmt.Fprintf(out, "  %d pairs added rows (%d rows total), %d pairs removed rows (%d rows total)\n",
		withAdds, addTotal, withRemovals, removeTotal)
	fmt.Fprintf(out, "  Removal is the half a creation-time projection cannot see: rows age out\n"+
		"  of knowledgeC's retention window with no new row written, and that changes\n"+
		"  a snapshot as surely as an addition does.\n\n")
}

// reportProjection counts the cadence windows in which no ZOBJECT row was
// created, which bounds how often a write-time content check would skip.
//
// # Why this is windowed, and what the unwindowed figure is
//
// The retained span is NOT the observation span. /discoverability/signals
// carries rows created in August 2024, two years before every other stream's
// oldest row, so measuring across the full retained span puts 17,527 hourly
// windows in the denominator when only about 700 of them cover a period this
// machine was being recorded at all. Measured 2026-08-12 that produces "92.8%
// of windows saw no row created", which is arithmetically true and answers a
// question nobody asked: it is overwhelmingly a statement about 2024 and 2025,
// when nothing was writing.
//
// Both figures are therefore printed, the trailing window first as the one to
// quote and the whole span beside it, because a clipped window that quietly
// excludes the interesting population is the other way to be wrong here.
func reportProjection(out io.Writer, newest *snap, cadence, window time.Duration) {
	fmt.Fprintf(out, "PROJECTION AT %s  (from %s)\n", cadence, newest.name)
	if len(newest.creations) == 0 {
		fmt.Fprintf(out, "  The newest snapshot carries no row creation times, so nothing can be\n"+
			"  projected. That is an absence of measurement, not a quiet zero.\n")
		return
	}
	secs := append([]int64(nil), newest.creations...)
	slices.Sort(secs)
	last := secs[len(secs)-1]

	trailing := project(secs, cadence, last-int64(window/time.Second))
	whole := project(secs, cadence, secs[0])

	fmt.Fprintf(out, "  TRAILING %s, %s to %s\n", window,
		time.Unix(trailing.first, 0).Format(time.DateTime), time.Unix(last, 0).Format(time.DateTime))
	fmt.Fprintf(out, "    %d windows, %d saw no row created (%.1f%%)\n",
		trailing.windows, trailing.empty, pct(trailing.empty, trailing.windows))
	fmt.Fprintf(out, "    So a write-time digest check skips AT MOST %.1f%% of runs at this cadence,\n"+
		"    and fewer once retention drops a row inside an otherwise quiet window.\n"+
		"    The remaining %.1f%% of runs each write a full snapshot.\n",
		pct(trailing.empty, trailing.windows), 100-pct(trailing.empty, trailing.windows))
	fmt.Fprintf(out, "  WHOLE RETAINED SPAN, %s to %s (%.1f days)\n",
		time.Unix(whole.first, 0).Format(time.DateTime), time.Unix(last, 0).Format(time.DateTime),
		float64(last-whole.first)/86400)
	fmt.Fprintf(out, "    %d windows, %d saw no row created (%.1f%%)\n",
		whole.windows, whole.empty, pct(whole.empty, whole.windows))
	fmt.Fprintf(out, "    Quote the trailing figure. This one is mostly a statement about the\n"+
		"    years before anything was recording, and is printed so that the\n"+
		"    exclusion is visible rather than silent.\n")
}

type projection struct {
	first   int64
	windows int64
	empty   int64
}

func project(sorted []int64, cadence time.Duration, from int64) projection {
	step := int64(cadence / time.Second)
	i := sort.Search(len(sorted), func(k int) bool { return sorted[k] >= from })
	if i == len(sorted) {
		return projection{first: from}
	}
	first, last := sorted[i], sorted[len(sorted)-1]
	filled := make(map[int64]struct{})
	for _, s := range sorted[i:] {
		filled[(s-first)/step] = struct{}{}
	}
	windows := (last-first)/step + 1
	return projection{first: first, windows: windows, empty: windows - int64(len(filled))}
}

func mb(b int64) float64 { return float64(b) / (1 << 20) }

func pct(part, whole int64) float64 {
	if whole == 0 {
		return 0
	}
	return 100 * float64(part) / float64(whole)
}
