//go:build realdata

// This file is a CORPUS CHECK — in fact a corpus WRITER. It reads this
// machine's live Biome stream, so `go test ./...` does not compile it at all.
//
// To regenerate testdata/realfocus.segb:
//
//	REGENERATE_BIOME_FIXTURE=1 go test -tags realdata \
//		-run TestGenerateRealFocusFixture ./internal/biome/
//
// Two gates, deliberately. The tag keeps the live stream out of the ordinary
// suite; the environment variable keeps a checked-in file from being rewritten
// by anybody who merely runs the corpus suite. realfixture_test.go carries the
// provenance of what it writes, and is on the untagged side because the
// fixture's description has to survive on machines that no longer hold the
// donor segments.

package biome

import (
	"encoding/binary"
	"hash/crc32"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestGenerateRealFocusFixture writes testdata/realfocus.segb from live donor
// segments. It is not part of the portable suite: it requires this
// machine's real Biome stream, and mutates a checked-in file, so it only
// runs when explicitly asked.
func TestGenerateRealFocusFixture(t *testing.T) {
	if os.Getenv("REGENERATE_BIOME_FIXTURE") == "" {
		t.Skip("set REGENERATE_BIOME_FIXTURE=1 to regenerate testdata/realfocus.segb from this machine's real Biome stream")
	}
	dir, err := DefaultAppUsageDir()
	if err != nil {
		t.Fatalf("DefaultAppUsageDir: %v", err)
	}

	specs := realFixtureSpecs()
	payloads := make([][]byte, len(specs))
	stamps := make([]time.Time, len(specs))
	for i, spec := range specs {
		raw, err := realPayload(filepath.Join(dir, spec.segment), spec.index)
		if err != nil {
			t.Fatalf("donor record %s#%d: %v", spec.segment, spec.index, err)
		}
		payloads[i] = anonymizeRealPayload(t, raw, spec.bundle, spec.at)
		stamps[i] = spec.at
	}

	seg := buildSegmentFromPayloads(t, at(-10), payloads, stamps)
	if err := os.WriteFile(realFixturePath, seg, 0o644); err != nil {
		t.Fatalf("write %s: %v", realFixturePath, err)
	}
	t.Logf("wrote %s (%d bytes, %d records)", realFixturePath, len(seg), len(specs))
}

// TestGenerateRealDeletedFixture writes testdata/realdeleted.segb: a segment
// carrying a real tombstoned prefix. Same two gates as the fixture above.
//
//	REGENERATE_BIOME_FIXTURE=1 go test -tags realdata \
//		-run TestGenerateRealDeletedFixture ./internal/biome/
//
// It needs a live segment macOS has actually deleted records from, which is
// not a state a machine is in most of the time — a freshly installed one has
// nothing 28 days old to delete yet. It skips saying so rather than writing a
// fixture built from nothing.
func TestGenerateRealDeletedFixture(t *testing.T) {
	if os.Getenv("REGENERATE_BIOME_FIXTURE") == "" {
		t.Skip("set REGENERATE_BIOME_FIXTURE=1 to regenerate testdata/realdeleted.segb from this machine's real Biome stream")
	}
	dir, err := DefaultAppUsageDir()
	if err != nil {
		t.Fatalf("DefaultAppUsageDir: %v", err)
	}
	path, data := findDeletedSegment(t, dir)
	t.Logf("donor segment %s", filepath.Base(path))

	// The real extents of the first realDeletedCount deleted records. These are
	// what this fixture exists to carry: the cursor has to walk them to land on
	// the first surviving record, and a hand-written chain would only prove the
	// parser agrees with the test that wrote it.
	count := int(binary.LittleEndian.Uint32(data[4:8]))
	ends := make([]uint32, realDeletedCount)
	for i := range realDeletedCount {
		if i >= count {
			t.Fatalf("donor holds %d entries, need %d", count, realDeletedCount)
		}
		entryAt := len(data) - trailerEntrySize*(i+1)
		entry := data[entryAt : entryAt+trailerEntrySize]
		if state := binary.LittleEndian.Uint32(entry[4:8]); state != stateDeleted {
			t.Fatalf("donor record %d is in state %d, want %d", i, state, stateDeleted)
		}
		ends[i] = binary.LittleEndian.Uint32(entry[0:4])
	}

	specs := realDeletedSpecs()
	payloads := make([][]byte, len(specs))
	stamps := make([]time.Time, len(specs))
	for i, spec := range specs {
		raw, err := realPayload(filepath.Join(dir, spec.segment), spec.index)
		if err != nil {
			t.Fatalf("donor record %s#%d: %v", spec.segment, spec.index, err)
		}
		payloads[i] = anonymizeRealPayload(t, raw, spec.bundle, spec.at)
		stamps[i] = spec.at
	}

	seg := buildTombstonedSegment(t, at(-10), ends, payloads, stamps)
	if err := os.WriteFile(realDeletedPath, seg, 0o644); err != nil {
		t.Fatalf("write %s: %v", realDeletedPath, err)
	}
	t.Logf("wrote %s (%d bytes, %d deleted + %d written records)",
		realDeletedPath, len(seg), len(ends), len(payloads))
}

// findDeletedSegment returns the first segment in dir macOS has deleted
// records from. The header is the cheap answer: byte 24 counts them, and is 0
// on every segment nothing has been removed from.
func findDeletedSegment(t *testing.T, dir string) (string, []byte) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil || len(data) < headerSize || string(data[:4]) != magic {
			continue
		}
		if binary.LittleEndian.Uint32(data[24:28]) > 0 {
			return path, data
		}
	}
	t.Skip("no segment in this machine's Biome stream has had records deleted, so there are no real tombstone extents to build a fixture from")
	return "", nil
}

// buildTombstonedSegment writes a segment whose first len(ends) records are
// deleted — zeroed bytes, state 3, and the real extents macOS left behind in
// the index — followed by payloads as ordinary written records. The header's
// two deletion fields are filled in exactly as macOS fills them.
func buildTombstonedSegment(t *testing.T, created time.Time, ends []uint32, payloads [][]byte, stamps []time.Time) []byte {
	t.Helper()
	total := len(ends) + len(payloads)
	out := make([]byte, fixtureSize)
	copy(out[0:4], magic)
	binary.LittleEndian.PutUint32(out[4:8], uint32(total))
	binary.LittleEndian.PutUint64(out[8:16], math.Float64bits(appleSeconds(created)))
	binary.LittleEndian.PutUint32(out[16:20], 10)
	binary.LittleEndian.PutUint32(out[20:24], uint32(len(ends)-1))
	binary.LittleEndian.PutUint32(out[24:28], uint32(len(ends)))

	// The deleted prefix. Nothing is written into the record area: a deleted
	// record's bytes are zeroed, and the fixture is already zero there.
	cursor := 0
	for i, end := range ends {
		if int(end) < cursor {
			t.Fatalf("donor extent %d (%d) runs backwards from %d", i, end, cursor)
		}
		entry := fixtureSize - trailerEntrySize*(i+1)
		binary.LittleEndian.PutUint32(out[entry:entry+4], end)
		binary.LittleEndian.PutUint32(out[entry+4:entry+8], stateDeleted)
		binary.LittleEndian.PutUint64(out[entry+8:entry+16], math.Float64bits(appleSeconds(deletedStamp(i))))
		cursor = int(end)
	}

	for i, payload := range payloads {
		start := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
		end := start + recordPrefixSize + len(payload)
		if headerSize+end > fixtureSize-trailerEntrySize*total {
			t.Fatalf("fixture record %d does not fit in %d bytes", i, fixtureSize)
		}
		rec := out[headerSize+start : headerSize+end]
		binary.LittleEndian.PutUint32(rec[0:4], crc32.ChecksumIEEE(payload))
		copy(rec[recordPrefixSize:], payload)

		entry := fixtureSize - trailerEntrySize*(len(ends)+i+1)
		binary.LittleEndian.PutUint32(out[entry:entry+4], uint32(end))
		binary.LittleEndian.PutUint32(out[entry+4:entry+8], stateWritten)
		binary.LittleEndian.PutUint64(out[entry+8:entry+16], math.Float64bits(appleSeconds(stamps[i])))
		cursor = end
	}
	return out
}

// realPayload reads one raw, still-real record's payload straight from a
// segment file, without going through readSegment: the point of this fixture
// is bytes readSegment has not yet touched.
func realPayload(path string, index int) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	count := int(binary.LittleEndian.Uint32(data[4:8]))
	cursor := 0
	for i := 0; i <= index && i < count; i++ {
		entryAt := len(data) - trailerEntrySize*(i+1)
		entry := data[entryAt : entryAt+trailerEntrySize]
		end := int(binary.LittleEndian.Uint32(entry[0:4]))
		state := binary.LittleEndian.Uint32(entry[4:8])
		if state != stateWritten {
			cursor = end
			continue
		}
		begin := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
		if i == index {
			from, to := headerSize+begin, headerSize+end
			payload := make([]byte, to-from-recordPrefixSize)
			copy(payload, data[from+recordPrefixSize:to])
			return payload, nil
		}
		cursor = end
	}
	return nil, os.ErrNotExist
}

// anonymizeRealPayload replaces a real record's timestamp and bundle with
// fixture values while keeping its frontmost and field-5 bytes exactly as
// macOS wrote them. It assumes the field layout this package has observed in
// every one of 53,746 real records: field 1 (varint, 2 bytes), field 2
// (fixed64, 9 bytes), field 3 (length-prefixed bytes), field 5 (varint,
// 2 bytes), in that order with no other fields — and fails loudly rather
// than silently miscopying if a donor record does not match.
func anonymizeRealPayload(t *testing.T, raw []byte, bundle string, ts time.Time) []byte {
	t.Helper()
	if len(raw) < 13 || raw[0] != fieldFrontmost<<3|wireVarint || raw[2] != fieldTimestamp<<3|wireFixed64 {
		t.Fatalf("donor record does not match the expected field-1/field-2 layout: % x", raw)
	}
	bundleLen := int(raw[12])
	if raw[11] != fieldBundle<<3|wireBytes || 13+bundleLen+2 != len(raw) {
		t.Fatalf("donor record does not match the expected field-3/field-5 layout: % x", raw)
	}
	frontmost := raw[0:2]
	field5 := raw[13+bundleLen:]

	out := append([]byte{}, frontmost...)
	out = append(out, fieldTimestamp<<3|wireFixed64)
	out = binary.LittleEndian.AppendUint64(out, math.Float64bits(unixSeconds(ts)))
	out = append(out, fieldBundle<<3|wireBytes, byte(len(bundle)))
	out = append(out, bundle...)
	out = append(out, field5...)
	return out
}
