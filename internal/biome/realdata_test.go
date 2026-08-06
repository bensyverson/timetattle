//go:build realdata

// This file is a CORPUS CHECK. Its assertions are claims about the data on one
// particular machine, so `go test ./...` does not compile it at all. Run these
// deliberately:
//
//	go test -tags realdata ./...
//
// docs/content/docs/testing.md says what each of the two runs buys, and
// internal/corpusgate is what stops the boundary eroding again.

package biome

import (
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"testing"
	"time"
)

// recorded is the zone the spike's wall-clock figures were written down in.
// Pinning it keeps the cross-checks below meaningful on a machine set to some
// other zone, since the instants themselves are absolute.
var recorded = time.FixedZone("recorded", -5*60*60)

// TestRealSegmentsParse is the check that matters most: the framing either
// describes what macOS actually writes or it does not, and a synthetic fixture
// cannot tell us which. Every record's checksum is verified during parsing, so
// reaching the end of a real segment without an error is not a weak signal.
func TestRealSegmentsParse(t *testing.T) {
	segments := realSegments(t)

	var (
		total    int
		earliest time.Time
		latest   time.Time
	)
	for _, seg := range segments {
		if len(seg.changes) == 0 {
			t.Errorf("%s holds no records", seg.path)
			continue
		}
		for i, c := range seg.changes {
			if c.bundle == "" {
				t.Fatalf("%s record %d has no bundle identifier", seg.path, i)
			}
			// A record read against the Apple epoch by mistake lands three
			// decades away, which no plausible tolerance would hide.
			if c.at.Year() < 2015 || c.at.After(time.Now().AddDate(0, 0, 1)) {
				t.Fatalf("%s record %d is dated %s, which is not a plausible instant", seg.path, i, c.at)
			}
			if i > 0 && c.at.Before(seg.changes[i-1].at) {
				t.Errorf("%s record %d is dated %s, before record %d at %s",
					seg.path, i, c.at, i-1, seg.changes[i-1].at)
			}
		}
		total += len(seg.changes)
		if earliest.IsZero() || seg.changes[0].at.Before(earliest) {
			earliest = seg.changes[0].at
		}
		if last := seg.changes[len(seg.changes)-1].at; last.After(latest) {
			latest = last
		}
	}
	t.Logf("%d segments, %d records, %s to %s",
		len(segments), total, earliest.In(recorded), latest.In(recorded))
}

// TestKnownSegmentMatchesTheSpike checks the parser against figures recorded
// by hand before it existed. The 13:49:40 Motion focus is the one that proves
// the epoch was read correctly: against the Apple epoch it would read 2056.
func TestKnownSegmentMatchesTheSpike(t *testing.T) {
	dir := realDir(t)
	path := filepath.Join(dir, knownSegment)
	if _, err := os.Stat(path); err != nil {
		// Pruning is the usual cause and it is not the one this observed.
		// Carry the error: a permission failure and an absence are different
		// findings, and a message that names only the first hides the second.
		t.Skipf("cannot stat %s: %v", path, err)
	}

	seg, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}

	// The spike counted 1,196 records while macOS was still appending to this
	// segment, so the figures below describe that prefix. Records already
	// written never change.
	const spiked = 1196
	if len(seg.changes) < spiked {
		t.Fatalf("got %d records, want at least the %d the spike counted", len(seg.changes), spiked)
	}
	t.Logf("%d records, %d when the spike ran", len(seg.changes), spiked)
	seg.changes = seg.changes[:spiked]

	want := []struct {
		wall      string
		bundle    string
		frontmost bool
	}{
		{"2026-07-28 10:31:08", "us.zoom.xos", true},
		{"2026-07-28 10:31:10", "us.zoom.xos", false},
		{"2026-07-28 10:31:10", "com.granola.app", true},
	}
	for i, w := range want {
		got := seg.changes[i]
		if wall := got.at.In(recorded).Format("2006-01-02 15:04:05"); wall != w.wall {
			t.Errorf("record %d is dated %s, want %s", i, wall, w.wall)
		}
		if got.bundle != w.bundle || got.frontmost != w.frontmost {
			t.Errorf("record %d = %s frontmost=%v, want %s frontmost=%v",
				i, got.bundle, got.frontmost, w.bundle, w.frontmost)
		}
	}

	var motion []focusChange
	for _, c := range seg.changes {
		if c.bundle == "com.apple.motionapp" {
			motion = append(motion, c)
		}
	}
	if len(motion) != 28 {
		t.Errorf("got %d com.apple.motionapp records, want 28", len(motion))
	}
	if len(motion) > 0 {
		if wall := motion[0].at.In(recorded).Format("2006-01-02 15:04:05"); wall != "2026-07-28 13:49:40" {
			t.Errorf("first Motion record is dated %s, want 2026-07-28 13:49:40", wall)
		}
	}
}

// TestRealCollectProducesUsableIntervals runs the whole source over the real
// stream, which is the only place the cross-segment pairing is exercised
// against segments macOS actually rolled over.
func TestRealCollectProducesUsableIntervals(t *testing.T) {
	dir := realDir(t)

	events, err := NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("got no events from a populated stream")
	}

	var (
		spans   int
		instant int
		longest time.Duration
	)
	for _, e := range events {
		if err := e.Validate(); err != nil {
			t.Fatalf("event %+v is invalid: %v", e, err)
		}
		if e.Duration() == 0 {
			instant++
			continue
		}
		spans++
		if e.Duration() > longest {
			longest = e.Duration()
		}
	}
	// An interval longer than a day means a start was paired with a stop
	// across an idle gap, which is the failure this source exists to avoid.
	if longest > 24*time.Hour {
		t.Errorf("longest interval is %s, which is not plausible focus", longest)
	}
	t.Logf("%d intervals with duration, %d observations without one, longest %s", spans, instant, longest)
}

// TestRealSegmentsAgreeWithTheTombstoneStream is what makes "state 3 means
// deleted" a reading of the format rather than a story about one file. Beside
// the segments macOS keeps a tombstone/ directory holding another SEGB stream,
// and its records name — in words, as strings — the segment each deletion came
// from, the byte offset of the index entry it removed, and how long that
// record's payload was. Neither file knows anything about the other's
// interpretation, so agreement between them is evidence and a matching count
// in one file alone is not.
//
// It asserts three things about every state-3 entry the machine holds: the
// tombstone stream names it, the length it records matches the length the
// segment's own offset chain implies, and the timestamp matches bit for bit.
func TestRealSegmentsAgreeWithTheTombstoneStream(t *testing.T) {
	dir := realDir(t)
	deleted := realDeletedEntries(t, dir)
	if len(deleted) == 0 {
		t.Skip("no segment in this stream carries a state-3 entry, so there is no deletion to cross-check")
	}
	tombstones := realTombstones(t, dir)
	if len(tombstones) == 0 {
		t.Skipf("%d records are marked deleted but no tombstone stream was found beside the segments", len(deleted))
	}

	if len(tombstones) != len(deleted) {
		t.Errorf("the tombstone stream holds %d records but %d index entries are marked deleted", len(tombstones), len(deleted))
	}
	matched := 0
	for _, d := range deleted {
		got, ok := tombstones[d.key]
		if !ok {
			t.Errorf("index entry for %s record %d is deleted, but no tombstone record names it", d.segment, d.index)
			continue
		}
		if got.length != d.length {
			t.Errorf("%s record %d: the tombstone says %d payload bytes, the segment's offsets imply %d",
				d.segment, d.index, got.length, d.length)
			continue
		}
		if got.stampBits != d.stampBits {
			t.Errorf("%s record %d: the tombstone is dated %#x, the index entry %#x",
				d.segment, d.index, got.stampBits, d.stampBits)
			continue
		}
		matched++
	}
	t.Logf("%d deleted records, %d tombstone records, %d matched on both length and timestamp; actors %v",
		len(deleted), len(tombstones), matched, tombstoneActors(tombstones))
}

// deletedEntry is one state-3 index entry, described by what the segment alone
// says about it.
type deletedEntry struct {
	segment   string
	index     int
	key       string // segment name and index-entry offset, as the tombstone names it
	length    uint64 // payload bytes, from the segment's own offset chain
	stampBits uint64
}

// realDeletedEntries walks the index of every segment without parsing a single
// record, because the records this is about have been zeroed.
func realDeletedEntries(t *testing.T, dir string) []deletedEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []deletedEntry
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		count := int(binary.LittleEndian.Uint32(data[4:8]))
		cursor := 0
		for i := range count {
			at := len(data) - trailerEntrySize*(i+1)
			entry := data[at : at+trailerEntrySize]
			end := int(binary.LittleEndian.Uint32(entry[0:4]))
			state := binary.LittleEndian.Uint32(entry[4:8])
			begin := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
			if state == stateDeleted {
				out = append(out, deletedEntry{
					segment:   e.Name(),
					index:     i,
					key:       tombstoneKey(e.Name(), uint64(at-headerSize)),
					length:    uint64(end - begin - recordPrefixSize),
					stampBits: binary.LittleEndian.Uint64(entry[8:16]),
				})
			}
			if end != 0 {
				cursor = end
			}
		}
	}
	return out
}

// tombstoneRecord is what one record of the tombstone stream says.
type tombstoneRecord struct {
	actor     string
	length    uint64
	stampBits uint64
}

// tombstoneKey identifies the record a tombstone refers to. The tombstone
// carries the segment's name and the offset of the index entry measured from
// the end of the 32-byte header, which is the one relationship in this file
// that was read off four sample records rather than proved across all of them
// — so the assertions above check length and timestamp independently, and this
// key only has to be consistent, not meaningful.
func tombstoneKey(segment string, offset uint64) string {
	return segment + "@" + strconv.FormatUint(offset, 10)
}

// realTombstones reads the SEGB stream in the tombstone/ subdirectory. Its
// container framing is the segments' own, checksums included, but its payload
// is a different message: field 1 the segment name, field 2 the index-entry
// offset, field 3 the payload length, field 5 the actor, field 6 the
// timestamp. Reading it here rather than in the package keeps a second
// undocumented schema out of production code, where nothing needs it.
func realTombstones(t *testing.T, dir string) map[string]tombstoneRecord {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, "tombstone"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatalf("read the tombstone directory beside %s: %v", dir, err)
	}
	out := map[string]tombstoneRecord{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, "tombstone", e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		if len(data) < headerSize || string(data[:len(magic)]) != magic {
			t.Fatalf("%s does not carry the SEGB magic, so the tombstone stream is not the format this assumes", path)
		}
		count := int(binary.LittleEndian.Uint32(data[4:8]))
		cursor := 0
		for i := range count {
			at := len(data) - trailerEntrySize*(i+1)
			entry := data[at : at+trailerEntrySize]
			end := int(binary.LittleEndian.Uint32(entry[0:4]))
			if binary.LittleEndian.Uint32(entry[4:8]) != stateWritten {
				if end != 0 {
					cursor = end
				}
				continue
			}
			begin := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
			record := data[headerSize+begin : headerSize+end]
			payload := record[recordPrefixSize:]
			if want, got := binary.LittleEndian.Uint32(record[0:4]), crc32.ChecksumIEEE(payload); got != want {
				t.Fatalf("%s tombstone record %d stores checksum %#08x but hashes to %#08x", path, i, want, got)
			}
			name, offset, rec := parseTombstone(t, path, i, payload)
			out[tombstoneKey(name, offset)] = rec
			cursor = end
		}
	}
	return out
}

// parseTombstone reads the handful of fields a tombstone record carries,
// skipping anything else exactly as parseFocusChange does.
func parseTombstone(t *testing.T, path string, index int, payload []byte) (string, uint64, tombstoneRecord) {
	t.Helper()
	var (
		name   string
		offset uint64
		rec    tombstoneRecord
		i      int
	)
	for i < len(payload) {
		key, n := binary.Uvarint(payload[i:])
		if n <= 0 {
			t.Fatalf("%s tombstone record %d: the field key at offset %d is not a varint", path, index, i)
		}
		i += n
		field, wire := key>>3, key&7
		switch wire {
		case wireVarint:
			v, n := binary.Uvarint(payload[i:])
			if n <= 0 {
				t.Fatalf("%s tombstone record %d: field %d is not a varint", path, index, field)
			}
			i += n
			switch field {
			case 2:
				offset = v
			case 3:
				rec.length = v
			}
		case wireFixed64:
			if field == 6 {
				rec.stampBits = binary.LittleEndian.Uint64(payload[i : i+8])
			}
			i += 8
		case wireBytes:
			size, n := binary.Uvarint(payload[i:])
			if n <= 0 {
				t.Fatalf("%s tombstone record %d: the length of field %d is not a varint", path, index, field)
			}
			i += n
			switch field {
			case 1:
				name = string(payload[i : i+int(size)])
			case 5:
				rec.actor = string(payload[i : i+int(size)])
			}
			i += int(size)
		case wireFixed32:
			i += 4
		default:
			t.Fatalf("%s tombstone record %d: field %d has wire type %d", path, index, field, wire)
		}
	}
	return name, offset, rec
}

func tombstoneActors(t map[string]tombstoneRecord) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range t {
		if !seen[r.actor] {
			seen[r.actor] = true
			out = append(out, r.actor)
		}
	}
	sort.Strings(out)
	return out
}

func realDir(t *testing.T) string {
	t.Helper()
	dir, err := DefaultAppUsageDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("no readable ScreenTime.AppUsage stream at %s: %v", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			return dir
		}
	}
	t.Skipf("%s holds no segments", dir)
	return ""
}

func realSegments(t *testing.T) []segment {
	t.Helper()
	dir := realDir(t)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []segment
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		seg, err := readSegment(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("readSegment: %v", err)
		}
		out = append(out, seg)
	}
	return out
}
