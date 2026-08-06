package biome

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"
)

func TestReadSegmentReadsEveryRecordInOrder(t *testing.T) {
	dir := t.TempDir()
	path := writeSegment(t, dir, "806945468674832", buildSegment(t, at(0),
		start(0, "us.zoom.xos"),
		stop(2, "us.zoom.xos"),
		start(2, "com.granola.app"),
	))

	seg, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	if len(seg.changes) != 3 {
		t.Fatalf("got %d changes, want 3", len(seg.changes))
	}
	want := []focusChange{
		{at: at(0), bundle: "us.zoom.xos", frontmost: true},
		{at: at(2), bundle: "us.zoom.xos"},
		{at: at(2), bundle: "com.granola.app", frontmost: true},
	}
	for i, w := range want {
		got := seg.changes[i]
		if !got.at.Equal(w.at) || got.bundle != w.bundle || got.frontmost != w.frontmost {
			t.Errorf("change %d = %+v, want %+v", i, got, w)
		}
	}
}

// Record lengths vary with the bundle identifier, so the parser must take each
// record's extent from the trailer rather than assuming a stride.
func TestReadSegmentHandlesRecordsOfDifferentLengths(t *testing.T) {
	dir := t.TempDir()
	path := writeSegment(t, dir, "1", buildSegment(t, at(0),
		start(0, "a"),
		stop(1, "a"),
		start(1, "com.adobe.LightroomClassicCC7"),
		stop(2, "com.adobe.LightroomClassicCC7"),
		start(2, "com.apple.motionapp"),
	))

	seg, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	if len(seg.changes) != 5 {
		t.Fatalf("got %d changes, want 5", len(seg.changes))
	}
	if seg.changes[4].bundle != "com.apple.motionapp" {
		t.Errorf("last bundle = %q, want com.apple.motionapp", seg.changes[4].bundle)
	}
}

func TestReadSegmentReportsCreationTime(t *testing.T) {
	dir := t.TempDir()
	created := at(-30)
	path := writeSegment(t, dir, "1", buildSegment(t, created, start(0, "com.apple.finder")))

	seg, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	if !seg.created.Equal(created) {
		t.Errorf("created = %s, want %s", seg.created, created)
	}
}

// A rolled-over segment ends with a zero-length entry that carries no record.
func TestReadSegmentIgnoresTheSealingEntry(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"), stop(5, "com.apple.finder"))
	path := writeSegment(t, dir, "1", seal(t, seg, at(60)))

	got, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	if len(got.changes) != 2 {
		t.Fatalf("got %d changes, want 2", len(got.changes))
	}
}

// The record timestamp is Unix epoch seconds while the trailer entry beside it
// is Apple epoch seconds. Reading either with the other's offset lands in 1996
// or 2056, so this pins the instant that reaches the caller.
func TestReadSegmentReadsRecordTimestampsAsUnixEpoch(t *testing.T) {
	dir := t.TempDir()
	moment := time.Date(2026, 7, 28, 18, 49, 40, 675000000, time.UTC)
	path := writeSegment(t, dir, "1", buildSegment(t, moment,
		focusChange{at: moment, bundle: "com.apple.motionapp", frontmost: true}))

	seg, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	if len(seg.changes) != 1 {
		t.Fatalf("got %d changes, want 1", len(seg.changes))
	}
	got := seg.changes[0].at.UTC()
	if got.Year() != 2026 {
		t.Fatalf("read year %d, want 2026 — the record timestamp was read against the wrong epoch", got.Year())
	}
	if d := got.Sub(moment); d < -time.Millisecond || d > time.Millisecond {
		t.Errorf("timestamp = %s, want %s (off by %s)", got, moment, d)
	}
}

func TestReadSegmentRejectsAFileThatIsNotSEGB(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"))
	copy(seg[0:4], "JUNK")
	path := writeSegment(t, dir, "1", seg)

	requireErrorNaming(t, mustFail(readSegment(path)), path, "SEGB", "byte 0")
}

func TestReadSegmentRejectsAFileTooShortForAHeader(t *testing.T) {
	dir := t.TempDir()
	path := writeSegment(t, dir, "1", []byte("SEGB"))

	requireErrorNaming(t, mustFail(readSegment(path)), path)
}

// A truncated segment keeps its header, so the record count still promises an
// index — but the index lived at the end of the file and went with the part
// that was lost. Reporting nothing here would look exactly like a machine
// nobody had used.
func TestReadSegmentRejectsATruncatedSegment(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"), stop(5, "com.apple.finder"))

	for _, keep := range []int{64, len(seg) / 2, len(seg) - trailerEntrySize} {
		path := writeSegment(t, dir, "1", seg[:keep])
		requireErrorNaming(t, mustFail(readSegment(path)), path, "byte")
	}
}

func TestReadSegmentRejectsARecordCountThatCannotFit(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"))
	binary.LittleEndian.PutUint32(seg[4:8], 1<<30)
	path := writeSegment(t, dir, "1", seg)

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte")
}

// Flipping the frontmost flag leaves a record that still parses as protobuf,
// so only the checksum can tell that the bytes changed underneath us. The
// first record starts at byte 32; byte 41 is its flag value.
func TestReadSegmentRejectsARecordWhoseChecksumDoesNotMatch(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"))
	seg[41] ^= 0x01
	path := writeSegment(t, dir, "1", seg)

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte 32", "checksum")
}

func TestReadSegmentRejectsARecordExtendingPastTheTrailer(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"))
	binary.LittleEndian.PutUint32(seg[len(seg)-trailerEntrySize:], 1<<20)
	path := writeSegment(t, dir, "1", seg)

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte")
}

func TestReadSegmentRejectsARecordShorterThanItsPrefix(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.apple.finder"))
	binary.LittleEndian.PutUint32(seg[len(seg)-trailerEntrySize:], 4)
	path := writeSegment(t, dir, "1", seg)

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte 32")
}

func TestReadSegmentRejectsAPayloadThatIsNotProtobuf(t *testing.T) {
	dir := t.TempDir()
	// Wire type 7 does not exist.
	path := writeSegment(t, dir, "1", buildSegmentFromPayloads(t, at(0),
		[][]byte{{0x0f, 0xff, 0xff}}, []time.Time{at(0)}))

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte 32")
}

func TestReadSegmentRejectsARecordWithNoBundleIdentifier(t *testing.T) {
	dir := t.TempDir()
	payload := encodeFocusChange(start(0, "com.apple.finder"))
	// Drop the bundle field, leaving the flag, the timestamp and field five.
	trimmed := append(append([]byte{}, payload[:11]...), payload[11+2+len("com.apple.finder"):]...)
	path := writeSegment(t, dir, "1", buildSegmentFromPayloads(t, at(0),
		[][]byte{trimmed}, []time.Time{at(0)}))

	requireErrorNaming(t, mustFail(readSegment(path)), path, "bundle", "byte 32")
}

func TestReadSegmentRejectsARecordWithNoTimestamp(t *testing.T) {
	dir := t.TempDir()
	payload := encodeFocusChange(start(0, "com.apple.finder"))
	trimmed := append(append([]byte{}, payload[:2]...), payload[11:]...)
	path := writeSegment(t, dir, "1", buildSegmentFromPayloads(t, at(0),
		[][]byte{trimmed}, []time.Time{at(0)}))

	requireErrorNaming(t, mustFail(readSegment(path)), path, "timestamp", "byte 32")
}

// A record's own Unix timestamp and its trailer entry's Apple timestamp name
// the same instant in every one of the 47,029 records observed on a real
// machine, so a disagreement means the two halves of the segment no longer
// describe each other.
func TestReadSegmentRejectsARecordDisagreeingWithItsTrailerEntry(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegmentFromPayloads(t, at(0),
		[][]byte{encodeFocusChange(start(0, "com.apple.finder"))},
		[]time.Time{at(3600)})
	path := writeSegment(t, dir, "1", seg)

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte 32")
}

func TestReadSegmentNamesAMissingFile(t *testing.T) {
	path := t.TempDir() + "/absent"
	requireErrorNaming(t, mustFail(readSegment(path)), path)
}

// A deleted record's bytes are gone, but its index entry keeps the end offset
// the records after it are measured from. Reading past a tombstone without
// advancing the cursor would start the next record inside the hole, and the
// checksum would then fail on a record nothing is wrong with — so proving the
// survivors parse is proving the cursor moved.
func TestReadSegmentReadsTheRecordsAfterATombstone(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0),
		start(0, "com.example.callapp"),
		stop(2, "com.example.callapp"),
		start(2, "com.example.reallylongbundleidentifierforfixture"),
		stop(9, "com.example.reallylongbundleidentifierforfixture"),
	)
	path := writeSegment(t, dir, "804992150463800", tombstone(t, seg, 0, 2))

	got, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	want := []focusChange{
		{at: at(2), bundle: "com.example.reallylongbundleidentifierforfixture", frontmost: true},
		{at: at(9), bundle: "com.example.reallylongbundleidentifierforfixture"},
	}
	if len(got.changes) != len(want) {
		t.Fatalf("got %d changes, want %d", len(got.changes), len(want))
	}
	for i, w := range want {
		if c := got.changes[i]; !c.at.Equal(w.at) || c.bundle != w.bundle || c.frontmost != w.frontmost {
			t.Errorf("change %d = %+v, want %+v", i, c, w)
		}
	}
}

// A record macOS deleted was observed and is gone; a record nothing ever wrote
// was never observed at all. Collapsing those two into "no event" is the bug
// this project keeps shipping, so the count of the first is carried out of the
// parser rather than inferred from the shortfall in the second.
func TestReadSegmentCountsTombstonedRecordsSeparatelyFromTheOnesItRead(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0),
		start(0, "com.example.callapp"),
		stop(2, "com.example.callapp"),
		start(2, "com.example.notesapp"),
	)
	path := writeSegment(t, dir, "804992150463800", tombstone(t, seg, 0, 2))

	got, err := readSegment(path)
	if err != nil {
		t.Fatalf("readSegment: %v", err)
	}
	if got.deleted != 2 {
		t.Errorf("got deleted = %d, want 2", got.deleted)
	}
	if len(got.changes) != 1 {
		t.Errorf("got %d changes, want 1", len(got.changes))
	}
}

// Retention deletes the oldest records and a segment stores its records oldest
// first, so what macOS writes is always a prefix. A hole anywhere else would
// mean deletion is driven by something other than age, and the parser's model
// of the format would be wrong — which is the same call the unknown-state
// guard makes, for the same reason.
//
// The cost is measured rather than asserted, on exactly these six records.
// With the guard removed, this fixture collects ONE event: com.example.callapp
// for 10h0m10s. The truth is 15 seconds of callapp and 9h59m55s of notesapp,
// so the hole does not lose 9h59m55s, it MOVES it onto the wrong application
// and reports it as observed. Losing records is recoverable; publishing ten
// hours nobody spent is not.
//
// A sweep of the real 804992150463800 punched a 946-record hole at ten offsets
// (100 through 8000) and fabricated nothing at any of them — a stranded start
// is usually closed as UnknownLost by the same application's next start, and
// fabrication needs the next surviving record for that bundle to be a stop
// instead. So this is a shape that is uncommon rather than one that is
// hypothetical, and the guard is cheap. See project/private/2026-08-03-segb-tombstones.md.
func TestReadSegmentRejectsATombstoneThatIsNotAPrefix(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0),
		start(0, "com.example.callapp"),
		stop(5, "com.example.callapp"),
		start(5, "com.example.notesapp"),
		stop(36000, "com.example.notesapp"),
		start(36000, "com.example.callapp"),
		stop(36010, "com.example.callapp"),
	)
	path := writeSegment(t, dir, "804992150463800", tombstone(t, seg, 1, 4))

	requireErrorNaming(t, mustFail(readSegment(path)), path, "byte", "deleted")
}

// The guard this incident tested: a state the parser does not know, which
// claims bytes, must still stop the read. Learning what 3 means does not mean
// tolerating 5. This test is expected to pass before the change as well as
// after — it exists to pin behaviour that must survive, not to describe
// behaviour being added.
func TestReadSegmentStillRejectsAnUnknownStateThatClaimsBytes(t *testing.T) {
	dir := t.TempDir()
	seg := buildSegment(t, at(0), start(0, "com.example.callapp"), stop(2, "com.example.callapp"))

	for _, state := range []uint32{2, 5, 6, 1 << 20} {
		path := writeSegment(t, dir, "804992150463800", restate(t, seg, 0, state))
		requireErrorNaming(t, mustFail(readSegment(path)), path, "unknown state", "byte")
	}
}

func mustFail(_ segment, err error) error { return err }

// requireErrorNaming asserts that an error was returned and that it names the
// file, plus any further substrings the caller cares about. Naming the file
// and the offset is the difference between a report an operator can act on and
// one they cannot.
func requireErrorNaming(t *testing.T, err error, path string, substrings ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("got no error, want one")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error %q does not name %s", err, path)
	}
	for _, s := range substrings {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q does not mention %q", err, s)
		}
	}
}
