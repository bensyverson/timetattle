package biome

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"math"
	"os"
	"time"
)

// The SEGB layout, derived from the bytes rather than from documentation.
//
// A segment is a fixed 512 KB file in three parts. A 32-byte header, records
// growing forwards from byte 32, and an index of those records growing
// *backwards* from the end of the file. The unwritten middle is what a segment
// still has room for. Nothing in a record says how long it is, which is why
// scanning for plausible-looking bytes is the wrong way in: the index is the
// only authority on where one record ends and the next begins.
//
//	header    0  "SEGB"
//	          4  uint32  number of index entries
//	          8  float64 Apple-epoch seconds, when the segment was created
//	         16  uint32  10, unchanging across every segment observed
//	         20  uint32  index of the last deleted record, 0xffffffff for none
//	         24  uint32  how many records have been deleted, 0 for none
//	         28  four zero bytes
//
//	entry i, at len(file) - 16*(i+1)
//	          0  uint32  offset one past the end of record i, counted from 32
//	          4  uint32  state; 1 written, 3 deleted, 4 sealed
//	          8  float64 Apple-epoch seconds, the same instant the record holds
//
//	record    0  uint32  CRC-32 (IEEE) of the bytes that follow the prefix
//	          4  four zero bytes
//	          8  protobuf payload
//
// Records begin on a four-byte boundary, so each one starts at the previous
// entry's end offset rounded up. The checksum is what makes this a reading
// rather than a guess: walking a real stream this way validated all 47,029
// records across six segments, which no misreading of the framing could do.
//
// # How state 3 was read
//
// It was not inferred from the one file that carried it. macOS rewrote segment
// 804992150463800 in place on 2026-08-03 at 11:27 local, and a snapshot taken
// on 07-29 and another on 08-03 hold both sides of that rewrite, so the two
// were subtracted rather than guessed at. Between them, and nothing else in
// 524,288 bytes changed:
//
//   - Entries 0 to 945 went from state 1 to state 3. Their end offsets and
//     their timestamps were untouched, so the framing of every record after
//     them still resolves.
//   - The 37,331 payload bytes those entries index went from 31,333 non-zero
//     bytes to zero non-zero bytes. The records are not hidden; they are gone.
//   - Header byte 20 went from 0xffffffff to 945 and byte 24 from 0 to 946.
//
// Beside the segments, in a tombstone/ subdirectory, macOS keeps another SEGB
// stream, and it says in words what happened. It holds exactly 946 records,
// each naming the segment "804992150463800" as a string, the byte offset of
// one index entry, the length of the payload that entry indexed, and the actor
// "BiomeAgent". All 946 carry a timestamp that is bit-for-bit one of the 946
// state-3 entries' timestamps, and all 946 lengths match the length that
// segment's own offset chain implies, with no mismatches in either direction.
// Two independent files agreeing 946 times on which records went is what makes
// this a reading of the format rather than a story about one file.
//
// The mechanism follows from the timestamps and constrains how the parser may
// treat a tombstone. The deleted records span 2026-07-06T00:55:50Z to
// 2026-07-06T16:27:17Z; the deletion ran at 2026-08-03 11:27 local, which is
// 2026-07-06 11:27 local plus 28 days. Retention is by age, records are stored
// oldest first, so a deleted run is a prefix — which parseSegment requires,
// because a hole in the middle would let a start pair with a stop on the far
// side of it and report an interval nobody lived. See
// project/private/2026-08-03-segb-tombstones.md.
//
// This is all one version of the format. Every one of the seven segments and
// the tombstone stream carries "SEGB" at byte 0, the value 10 at byte 16, a
// 32-byte header and 16-byte index entries, so nothing here has begun emitting
// a second SEGB variant. Measured with scripts/segbindex.
const (
	magic            = "SEGB"
	headerSize       = 32
	trailerEntrySize = 16
	recordPrefixSize = 8
	recordAlignment  = 4

	// stateWritten marks an entry that indexes a real record. A segment macOS
	// has finished with carries one further entry in another state, of zero
	// length, dated when its successor was created.
	stateWritten = 1

	// stateDeleted marks an entry whose record macOS has removed. The entry
	// survives with its extent and its timestamp; the record's own bytes are
	// zeroed. See "How state 3 was read" above for what proves it, and
	// parseSegment for why the cursor must still advance across one.
	stateDeleted = 3

	// stateNone is what an entry that has never been written looks like. The
	// header only counts entries that exist, so finding one is how a
	// truncated segment gives itself away: the index it promised is gone and
	// what remains where it should be is unwritten space.
	stateNone = 0

	// appleEpochOffset is the gap in seconds between 1970-01-01 and
	// 2001-01-01. The index entries count from the latter and the records
	// themselves from the former, which is the single most dangerous thing
	// about this format: confusing them is a thirty-year error that still
	// produces a plausible-looking date.
	appleEpochOffset = 978307200

	// stampTolerance is how far a record's timestamp may sit from the one in
	// its index entry. They agreed exactly in every record observed, so this
	// is slack for floating-point conversion and nothing else — an epoch
	// confusion misses it by three decades.
	stampTolerance = time.Second
)

// The fields of a record's payload. Anything else is skipped, as protobuf
// requires: field 5 is a varint that is almost always 1, with no observed
// correlation to anything, and reading meaning into it would be invention.
const (
	fieldFrontmost = 1
	fieldTimestamp = 2
	fieldBundle    = 3

	wireVarint  = 0
	wireFixed64 = 1
	wireBytes   = 2
	wireFixed32 = 5
)

// focusChange is one record: an application either became frontmost or gave
// that position up, at an instant.
type focusChange struct {
	at        time.Time
	bundle    string
	frontmost bool
}

// segment is one parsed SEGB file.
type segment struct {
	path    string
	created time.Time
	changes []focusChange
	deleted int
}

// readSegment parses the segment stored at path.
func readSegment(path string) (segment, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return segment{}, fmt.Errorf("read Biome segment %s: %w", path, err)
	}
	return parseSegment(path, data)
}

// parseSegment reads data as a segment. Every failure names the file and the
// byte it was reading, because a segment is an opaque half-megabyte and a
// report that does not say where to look is a report nobody can act on.
func parseSegment(path string, data []byte) (segment, error) {
	if len(data) < headerSize {
		return segment{}, fmt.Errorf("%s is %d bytes, too short to hold a %d-byte SEGB header", path, len(data), headerSize)
	}
	if got := string(data[:len(magic)]); got != magic {
		return segment{}, fmt.Errorf("%s at byte 0: expected the SEGB magic, found %q", path, got)
	}

	count := int64(binary.LittleEndian.Uint32(data[4:8]))
	created := appleTime(math.Float64frombits(binary.LittleEndian.Uint64(data[8:16])))

	// Where the index begins is a function of how many records the header
	// claims, so a truncated or overstated segment is caught here rather than
	// by reading whatever happens to lie at the end of the file.
	indexAt := int64(len(data)) - count*trailerEntrySize
	if indexAt < headerSize {
		return segment{}, fmt.Errorf("%s at byte 4: header claims %d records, whose index would begin at byte %d of a %d-byte file",
			path, count, indexAt, len(data))
	}

	seg := segment{path: path, created: created, changes: make([]focusChange, 0, count)}
	cursor := 0
	for i := range count {
		entryAt := int64(len(data)) - trailerEntrySize*(i+1)
		entry := data[entryAt : entryAt+trailerEntrySize]
		end := int(binary.LittleEndian.Uint32(entry[0:4]))
		state := binary.LittleEndian.Uint32(entry[4:8])
		stamped := appleTime(math.Float64frombits(binary.LittleEndian.Uint64(entry[8:16])))

		// An entry that indexes nothing must not advance the cursor either,
		// or every record after it would be read at the wrong offset.
		deleted := false
		switch {
		case state == stateWritten:
		case state == stateDeleted:
			// Retention removes the oldest records and a segment stores its
			// records oldest first, so what macOS writes is a prefix. A hole
			// anywhere else would mean deletion is driven by something other
			// than age, and pair() joins a start to the next stop carrying the
			// same bundle: across a hole, that is an interval spanning the
			// hole, reported as if it had been observed. Measured on a
			// six-record fixture, removing this check turns 15 seconds of one
			// application and 9h59m55s of another into a single 10h0m10s
			// interval charged to the first — see
			// TestReadSegmentRejectsATombstoneThatIsNotAPrefix. Stopping is
			// the same call the unknown-state guard below makes.
			if len(seg.changes) > 0 {
				return segment{}, fmt.Errorf("%s: the index entry for record %d, at byte %d, says the record was deleted, but record %d before it was not; deletion has only ever removed the oldest records of a segment",
					path, i, entryAt, i-1)
			}
			deleted = true
		case state == stateNone:
			return segment{}, fmt.Errorf("%s: the header counts %d records but the index entry for record %d, at byte %d, has never been written",
				path, count, i, entryAt)
		case end != 0:
			// A state this parser does not know is tolerable while it indexes
			// nothing, because only written entries have ever carried a
			// record. One that claims bytes is the index disagreeing with
			// itself, and guessing at what it meant would be worse than
			// stopping.
			return segment{}, fmt.Errorf("%s: the index entry for record %d, at byte %d, is in unknown state %d yet claims %d bytes of record",
				path, i, entryAt, state, end)
		default:
			continue
		}

		begin := (cursor + recordAlignment - 1) &^ (recordAlignment - 1)
		from, to := int64(headerSize+begin), int64(headerSize+end)
		if end < begin || to > indexAt {
			return segment{}, fmt.Errorf("%s: record %d, indexed by the entry at byte %d, spans bytes %d to %d, outside the records this segment holds",
				path, i, entryAt, from, to)
		}
		// A deleted record's bytes are zeroed but its extent survives, and
		// every record after it is measured from that extent. So the cursor
		// still advances; only the contents are gone. The count is carried out
		// of the parser because a record macOS deleted was observed and is
		// gone, which is not the same fact as a record that was never written.
		if deleted {
			seg.deleted++
			cursor = end
			continue
		}
		record := data[from:to]
		if len(record) < recordPrefixSize {
			return segment{}, fmt.Errorf("%s: record %d at byte %d is %d bytes, too short for its %d-byte prefix",
				path, i, from, len(record), recordPrefixSize)
		}

		payload := record[recordPrefixSize:]
		if want, got := binary.LittleEndian.Uint32(record[0:4]), crc32.ChecksumIEEE(payload); got != want {
			return segment{}, fmt.Errorf("%s: record %d at byte %d stores checksum %#08x but its contents hash to %#08x",
				path, i, from, want, got)
		}

		change, err := parseFocusChange(payload)
		if err != nil {
			return segment{}, fmt.Errorf("%s: record %d at byte %d: %w", path, i, from, err)
		}
		// The two halves of a segment date every record twice, in two
		// different epochs. Requiring them to agree turns the format's most
		// dangerous ambiguity into something the parser cannot get wrong
		// quietly.
		if d := change.at.Sub(stamped); d > stampTolerance || d < -stampTolerance {
			return segment{}, fmt.Errorf("%s: record %d at byte %d is dated %s but its index entry is dated %s",
				path, i, from, change.at.Format(time.RFC3339Nano), stamped.Format(time.RFC3339Nano))
		}

		seg.changes = append(seg.changes, change)
		cursor = end
	}
	return seg, nil
}

// parseFocusChange reads the handful of protobuf fields a record carries.
//
// This is written out rather than generated because the alternative is a
// protobuf dependency for four fields of a schema Apple does not publish and
// could change without telling anyone. Unknown fields are skipped, as the
// encoding requires, so a field appearing in a later macOS costs nothing.
func parseFocusChange(payload []byte) (focusChange, error) {
	var (
		change focusChange
		dated  bool
		i      int
	)
	for i < len(payload) {
		keyAt := i
		key, n := binary.Uvarint(payload[i:])
		if n <= 0 {
			return focusChange{}, fmt.Errorf("the field key at offset %d is not a varint", keyAt)
		}
		i += n
		field, wire := key>>3, key&7

		switch wire {
		case wireVarint:
			v, n := binary.Uvarint(payload[i:])
			if n <= 0 {
				return focusChange{}, fmt.Errorf("field %d at offset %d is not a varint", field, i)
			}
			i += n
			if field == fieldFrontmost {
				change.frontmost = v == 1
			}
		case wireFixed64:
			if i+8 > len(payload) {
				return focusChange{}, fmt.Errorf("field %d at offset %d needs 8 bytes but only %d remain", field, i, len(payload)-i)
			}
			if field == fieldTimestamp {
				change.at = unixTime(math.Float64frombits(binary.LittleEndian.Uint64(payload[i : i+8])))
				dated = true
			}
			i += 8
		case wireBytes:
			size, n := binary.Uvarint(payload[i:])
			if n <= 0 {
				return focusChange{}, fmt.Errorf("the length of field %d at offset %d is not a varint", field, i)
			}
			i += n
			if size > uint64(len(payload)-i) {
				return focusChange{}, fmt.Errorf("field %d at offset %d claims %d bytes but only %d remain", field, i, size, len(payload)-i)
			}
			if field == fieldBundle {
				change.bundle = string(payload[i : i+int(size)])
			}
			i += int(size)
		case wireFixed32:
			if i+4 > len(payload) {
				return focusChange{}, fmt.Errorf("field %d at offset %d needs 4 bytes but only %d remain", field, i, len(payload)-i)
			}
			i += 4
		default:
			return focusChange{}, fmt.Errorf("field %d at offset %d has wire type %d, which protobuf does not define", field, keyAt, wire)
		}
	}

	if !dated {
		return focusChange{}, errors.New("the record carries no timestamp")
	}
	if change.bundle == "" {
		return focusChange{}, errors.New("the record carries no bundle identifier")
	}
	return change, nil
}

// appleTime reads seconds counted from 2001-01-01, as the index entries and
// the segment header do.
func appleTime(seconds float64) time.Time { return unixTime(seconds + appleEpochOffset) }

// unixTime reads seconds counted from 1970-01-01, as the records do. The
// fractional part is kept: focus changes land within a second of each other
// often enough that truncating would merge distinct events.
func unixTime(seconds float64) time.Time {
	whole, frac := math.Modf(seconds)
	return time.Unix(int64(whole), int64(math.Round(frac*1e9))).UTC()
}
