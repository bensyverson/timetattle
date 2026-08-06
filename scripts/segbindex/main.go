// Command segbindex reports the SEGB header and index of Biome segments
// without parsing a single record payload.
//
// It exists because the index is the only authority on where records are, and
// the failure that produced it was invisible from the records: on 2026-08-03
// macOS rewrote 946 index entries of one segment from state 1 to state 3 and
// zeroed the records they pointed at, and internal/biome refused the whole
// stream because it had never seen state 3. Anything that changes the SHAPE of
// a stream — a new state, a second SEGB version, a header field that stops
// being 0xffffffff — shows up here and nowhere else.
//
// Run it against the live stream to see what states are in play, and against
// two snapshots of the same segment to see what macOS did between them:
//
//	go run ./scripts/segbindex                            # the current user's stream
//	go run ./scripts/segbindex -dir path/to               # a copied stream
//	go run ./scripts/segbindex -entries old.segb new.segb # every entry, to diff
//
// Like the probes in scripts/spike, this is an analysis tool rather than
// product behaviour and is exempt from the project's red/green TDD rule.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/bensyverson/timetattle/internal/biome"
)

// The offsets this reads. They are restated here rather than imported because
// the whole point of this tool is to be usable when internal/biome's model of
// the format has turned out to be wrong.
const (
	headerSize = 32
	entrySize  = 16
	appleEpoch = 978307200
)

// stateNames are the states whose meaning has been established. Anything else
// prints as its number, which is the signal to go and read the bytes.
var stateNames = map[uint32]string{
	0: "unwritten",
	1: "written",
	3: "deleted",
	4: "sealed",
}

func main() {
	dir := flag.String("dir", "", "directory of Biome segments (default: the current user's ScreenTime.AppUsage stream)")
	entries := flag.Bool("entries", false, "print every index entry rather than a summary, for diffing two copies of a segment")
	flag.Parse()

	if err := run(*dir, *entries, flag.Args()); err != nil {
		fmt.Fprintf(os.Stderr, "segbindex: %v\n", err)
		os.Exit(1)
	}
}

func run(dir string, entries bool, files []string) error {
	paths, err := gather(dir, files)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return fmt.Errorf("no segments to read")
	}
	for _, path := range paths {
		if err := report(path, entries); err != nil {
			fmt.Printf("%s: %v\n\n", path, err)
		}
	}
	return nil
}

// gather resolves what to read: explicit file arguments win, then -dir, then
// the current user's stream. A tombstone/ subdirectory is included, because it
// is a SEGB stream too and reading it is how a deletion is confirmed against
// something other than the segment that suffered it.
func gather(dir string, files []string) ([]string, error) {
	if len(files) > 0 {
		return files, nil
	}
	if dir == "" {
		var err error
		if dir, err = biome.DefaultAppUsageDir(); err != nil {
			return nil, err
		}
	}
	found, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var paths []string
	for _, e := range found {
		path := filepath.Join(dir, e.Name())
		if !e.IsDir() {
			paths = append(paths, path)
			continue
		}
		sub, err := os.ReadDir(path)
		if err != nil {
			continue
		}
		for _, s := range sub {
			if !s.IsDir() {
				paths = append(paths, filepath.Join(path, s.Name()))
			}
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func report(path string, everyEntry bool) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(data) < headerSize {
		return fmt.Errorf("%d bytes, too short for a header", len(data))
	}
	magic := string(data[0:4])
	count := int(binary.LittleEndian.Uint32(data[4:8]))
	created := appleTime(math.Float64frombits(binary.LittleEndian.Uint64(data[8:16])))
	fmt.Printf("%s\n", path)
	fmt.Printf("  %d bytes, magic %q, %d entries, created %s\n", len(data), magic, count, created.Format(time.RFC3339))
	fmt.Printf("  header  16=%d  20=%s  24=%d  28=%d\n",
		binary.LittleEndian.Uint32(data[16:20]),
		lastDeleted(binary.LittleEndian.Uint32(data[20:24])),
		binary.LittleEndian.Uint32(data[24:28]),
		binary.LittleEndian.Uint32(data[28:32]))

	indexAt := len(data) - count*entrySize
	if count < 0 || indexAt < headerSize {
		return fmt.Errorf("header claims %d entries, whose index would begin at byte %d", count, indexAt)
	}

	// Runs rather than a per-entry dump, because a segment holds thousands of
	// entries and what matters is where the state changes, not what each one
	// says.
	type run struct {
		state    uint32
		from, to int
		firstEnd uint32
		lastEnd  uint32
		at       time.Time
		until    time.Time
	}
	var runs []run
	for i := range count {
		at := len(data) - entrySize*(i+1)
		e := data[at : at+entrySize]
		end := binary.LittleEndian.Uint32(e[0:4])
		state := binary.LittleEndian.Uint32(e[4:8])
		stamp := appleTime(math.Float64frombits(binary.LittleEndian.Uint64(e[8:16])))
		if everyEntry {
			fmt.Printf("  [%6d] byte=%d end=%d state=%d stamp=%s\n", i, at, end, state, stamp.Format(time.RFC3339Nano))
		}
		if n := len(runs); n > 0 && runs[n-1].state == state {
			runs[n-1].to, runs[n-1].lastEnd, runs[n-1].until = i, end, stamp
			continue
		}
		runs = append(runs, run{state: state, from: i, to: i, firstEnd: end, lastEnd: end, at: stamp, until: stamp})
	}
	for _, r := range runs {
		fmt.Printf("  records %d-%d (%d) state %d %-9s  bytes %d-%d  %s to %s\n",
			r.from, r.to, r.to-r.from+1, r.state, stateNames[r.state],
			r.firstEnd, r.lastEnd, r.at.Format(time.RFC3339), r.until.Format(time.RFC3339))
	}
	fmt.Println()
	return nil
}

// lastDeleted renders header byte 20, which holds the index of the last
// deleted record and 0xffffffff when nothing has been deleted. Printing the
// sentinel as a number invites reading it as four billion records.
func lastDeleted(v uint32) string {
	if v == 0xffffffff {
		return "none"
	}
	return fmt.Sprintf("%d", v)
}

func appleTime(seconds float64) time.Time {
	whole, frac := math.Modf(seconds + appleEpoch)
	return time.Unix(int64(whole), int64(math.Round(frac*1e9))).UTC()
}
