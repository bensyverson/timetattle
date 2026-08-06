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

// The fixtures here encode segments the same way macOS does, byte for byte.
// Building them rather than committing a copy of the real stream keeps a
// personal record of which applications were used when out of the repository,
// and lets a test describe corruption that no real segment contains.

// fixtureSize matches the 512 KB macOS allocates for every segment. Most of it
// is unused padding between the last record and the trailer, and reproducing
// that proves the parser locates the trailer from the end of the file rather
// than from wherever the records happen to stop.
const fixtureSize = 512 * 1024

// stateSealed marks the zero-length entry macOS appends when it stops writing
// to a segment and rolls over to a new one.
const stateSealed = 4

// buildSegment encodes a well-formed segment holding changes.
func buildSegment(t *testing.T, created time.Time, changes ...focusChange) []byte {
	t.Helper()
	payloads := make([][]byte, len(changes))
	for i, c := range changes {
		payloads[i] = encodeFocusChange(c)
	}
	return buildSegmentFromPayloads(t, created, payloads, timesOf(changes))
}

// buildSegmentFromPayloads encodes payloads verbatim, so a test can describe a
// record the encoder would never produce.
func buildSegmentFromPayloads(t *testing.T, created time.Time, payloads [][]byte, stamps []time.Time) []byte {
	t.Helper()
	if len(payloads) != len(stamps) {
		t.Fatalf("fixture has %d payloads but %d timestamps", len(payloads), len(stamps))
	}

	out := make([]byte, fixtureSize)
	copy(out[0:4], magic)
	binary.LittleEndian.PutUint32(out[4:8], uint32(len(payloads)))
	binary.LittleEndian.PutUint64(out[8:16], math.Float64bits(appleSeconds(created)))
	binary.LittleEndian.PutUint32(out[16:20], 10)
	binary.LittleEndian.PutUint32(out[20:24], 0xffffffff)

	cursor := 0
	for i, payload := range payloads {
		start := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
		end := start + recordPrefixSize + len(payload)
		if headerSize+end > fixtureSize-trailerEntrySize*len(payloads) {
			t.Fatalf("fixture record %d does not fit in %d bytes", i, fixtureSize)
		}
		rec := out[headerSize+start : headerSize+end]
		binary.LittleEndian.PutUint32(rec[0:4], crc32.ChecksumIEEE(payload))
		copy(rec[recordPrefixSize:], payload)

		entry := fixtureSize - trailerEntrySize*(i+1)
		binary.LittleEndian.PutUint32(out[entry:entry+4], uint32(end))
		binary.LittleEndian.PutUint32(out[entry+4:entry+8], stateWritten)
		binary.LittleEndian.PutUint64(out[entry+8:entry+16], math.Float64bits(appleSeconds(stamps[i])))
		cursor = end
	}
	return out
}

// seal appends the zero-length trailer entry macOS writes when it rolls a
// segment over, whose timestamp is when the successor segment was created.
func seal(t *testing.T, seg []byte, at time.Time) []byte {
	t.Helper()
	out := make([]byte, len(seg))
	copy(out, seg)
	count := binary.LittleEndian.Uint32(out[4:8])
	entry := len(out) - trailerEntrySize*int(count+1)
	binary.LittleEndian.PutUint32(out[entry:entry+4], 0)
	binary.LittleEndian.PutUint32(out[entry+4:entry+8], stateSealed)
	binary.LittleEndian.PutUint64(out[entry+8:entry+16], math.Float64bits(appleSeconds(at)))
	binary.LittleEndian.PutUint32(out[4:8], count+1)
	return out
}

// tombstone reproduces what BiomeAgent does to a segment when a record ages
// out of the 28-day retention window, byte for byte as it was observed doing
// it to 804992150463800 on 2026-08-03: the record's own bytes are zeroed, its
// index entry keeps its end offset and its timestamp but changes state to 3,
// and the header gains the index of the last deleted record at byte 20 and the
// count of them at byte 24. Records are stored oldest first and retention
// deletes oldest first, so the deleted run is a prefix; markAt exists only so
// that a test can build the non-prefix shape macOS has never been seen to
// write, and assert the parser refuses it.
func tombstone(t *testing.T, seg []byte, first, n int) []byte {
	t.Helper()
	out := make([]byte, len(seg))
	copy(out, seg)
	count := int(binary.LittleEndian.Uint32(out[4:8]))
	if first+n > count {
		t.Fatalf("fixture holds %d records, cannot tombstone %d from %d", count, n, first)
	}
	cursor := 0
	for i := range count {
		entry := len(out) - trailerEntrySize*(i+1)
		end := int(binary.LittleEndian.Uint32(out[entry : entry+4]))
		begin := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
		if i >= first && i < first+n {
			binary.LittleEndian.PutUint32(out[entry+4:entry+8], stateDeleted)
			for j := headerSize + begin; j < headerSize+end; j++ {
				out[j] = 0
			}
		}
		cursor = end
	}
	binary.LittleEndian.PutUint32(out[20:24], uint32(first+n-1))
	binary.LittleEndian.PutUint32(out[24:28], uint32(n))
	return out
}

// restate rewrites one index entry's state, so a test can describe a state
// this parser has never seen rather than one it has.
func restate(t *testing.T, seg []byte, index int, state uint32) []byte {
	t.Helper()
	out := make([]byte, len(seg))
	copy(out, seg)
	entry := len(out) - trailerEntrySize*(index+1)
	binary.LittleEndian.PutUint32(out[entry+4:entry+8], state)
	return out
}

// encodeFocusChange writes the four protobuf fields a real record carries.
func encodeFocusChange(c focusChange) []byte {
	var frontmost byte
	if c.frontmost {
		frontmost = 1
	}
	out := []byte{fieldFrontmost<<3 | wireVarint, frontmost, fieldTimestamp<<3 | wireFixed64}
	out = binary.LittleEndian.AppendUint64(out, math.Float64bits(unixSeconds(c.at)))
	out = append(out, fieldBundle<<3|wireBytes, byte(len(c.bundle)))
	out = append(out, c.bundle...)
	return append(out, 5<<3|wireVarint, 1)
}

func timesOf(changes []focusChange) []time.Time {
	out := make([]time.Time, len(changes))
	for i, c := range changes {
		out[i] = c.at
	}
	return out
}

func appleSeconds(t time.Time) float64 { return unixSeconds(t) - appleEpochOffset }

func unixSeconds(t time.Time) float64 {
	return float64(t.Unix()) + float64(t.Nanosecond())/1e9
}

// writeSegment drops a fixture into dir under a name shaped like a real one.
func writeSegment(t *testing.T, dir, name string, seg []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, seg, 0o600); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
	return path
}

// start and stop build the two kinds of record, at whole seconds past a fixed
// instant so that a test reads as a timeline rather than as epoch arithmetic.
var fixtureEpoch = time.Date(2026, 7, 28, 10, 31, 8, 0, time.UTC)

func at(seconds int) time.Time { return fixtureEpoch.Add(time.Duration(seconds) * time.Second) }

func start(seconds int, bundle string) focusChange {
	return focusChange{at: at(seconds), bundle: bundle, frontmost: true}
}

func stop(seconds int, bundle string) focusChange {
	return focusChange{at: at(seconds), bundle: bundle}
}
