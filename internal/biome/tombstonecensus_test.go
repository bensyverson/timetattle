package biome

import (
	"strconv"
	"strings"
	"testing"
)

// tombstoneKey identifies the record a tombstone refers to. The tombstone
// carries the segment's name and the offset of the index entry measured from
// the end of the 32-byte header, which is the one relationship in this file
// that was read off four sample records rather than proved across all of them
// — so the assertions in the corpus check test length and timestamp
// independently, and this key only has to be consistent, not meaningful.
func tombstoneKey(segment string, offset uint64) string {
	return segment + "@" + strconv.FormatUint(offset, 10)
}

// segmentOfTombstoneKey recovers the segment name a key was built from, or the
// empty string for a key this code does not understand.
//
// It cuts at the LAST separator because the offset is what follows it and a
// segment name is not guaranteed to be free of one. An empty return is not a
// segment with an empty name: it means the key could not be read, and the
// census below counts it as neither live nor rotated rather than excusing it.
func segmentOfTombstoneKey(key string) string {
	i := strings.LastIndex(key, "@")
	if i < 0 {
		return ""
	}
	return key[:i]
}

// tombstoneCensus splits tombstone keys by whether the segment each one names is
// still in the directory.
//
// A rotated record is the ordinary case and carries no evidentiary weight: the
// index entry it describes left with its segment, so there is nothing for it to
// agree or disagree with. A live record is the opposite — its segment is still
// there, so its entry should be marked deleted, and if it is not, that is a real
// disagreement between two independently written files.
//
// Keys that parse as neither are returned in neither count. That is deliberate:
// silently folding an unreadable key into "rotated" would let a parser fault
// masquerade as routine retention, which is this repository's most-repeated bug.
func tombstoneCensus(keys []string, present map[string]bool) (live, rotated int) {
	for _, k := range keys {
		segment := segmentOfTombstoneKey(k)
		if segment == "" {
			continue
		}
		if present[segment] {
			live++
			continue
		}
		rotated++
	}
	return live, rotated
}

// The tombstone stream outlives the segments it describes, and the census below
// is what keeps that from reading as a discrepancy.
//
// TestRealSegmentsAgreeWithTheTombstoneStream once asserted a bijection --
// len(tombstones) == len(deleted) -- between the tombstone stream and the index
// entries currently marked deleted. That equality is true only INSTANTANEOUSLY:
// it holds in the moment a segment's deletions are tombstoned, and stops holding
// the moment that segment ages past the retention window and leaves the
// directory. Its tombstones remain; the entries they name do not. Measured on
// one live stream on 2026-08-13, 18,004 tombstone records stood against 1,950
// deleted entries, and all 1,950 matched on length and on timestamp bit for bit.
// The count was the only thing that disagreed, and it was the count that was
// wrong.
//
// These tests are UNTAGGED on purpose. The classification is arithmetic over
// keys and needs no corpus, so it runs on a machine that has never seen this
// data -- which is the whole point of the split that internal/corpusgate
// enforces. Only the reading of the live directories stays behind the tag.

func TestATombstoneNamingASegmentStillOnDiskCountsAsLive(t *testing.T) {
	keys := []string{
		tombstoneKey("805311376158243", 64),
		tombstoneKey("805311376158243", 128),
	}
	present := map[string]bool{"805311376158243": true}

	live, rotated := tombstoneCensus(keys, present)
	if live != 2 || rotated != 0 {
		t.Errorf("both tombstones name a segment still on disk: got %d live and %d rotated, want 2 and 0", live, rotated)
	}
}

// This is the case that broke the old invariant, and it is the regression this
// file exists to catch. A tombstone whose segment has rotated off disk has
// nothing left to match against, and that is routine rather than a fault.
func TestATombstoneWhoseSegmentHasRotatedAwayIsNotADiscrepancy(t *testing.T) {
	keys := []string{
		tombstoneKey("805311376158243", 64),
		tombstoneKey("804992150463800", 64),
		tombstoneKey("804992150463800", 128),
	}
	present := map[string]bool{"805311376158243": true}

	live, rotated := tombstoneCensus(keys, present)
	if live != 1 {
		t.Errorf("one tombstone names the segment still on disk: got %d live, want 1", live)
	}
	if rotated != 2 {
		t.Errorf("two tombstones name a segment that has left the directory: got %d rotated, want 2", rotated)
	}
}

// A segment name is not required to be numeric and the offset is decimal, so the
// split has to come from the right rather than the left. Splitting on the FIRST
// separator would take a name containing one and report a segment that does not
// exist, which would then be classified rotated and silently excused.
func TestTheSegmentIsReadFromTheLastSeparatorNotTheFirst(t *testing.T) {
	key := tombstoneKey("weird@name", 4096)
	if got := segmentOfTombstoneKey(key); got != "weird@name" {
		t.Errorf("segment of %q: got %q, want %q", key, got, "weird@name")
	}
}

// A key that carries no separator at all is not a segment name with an empty
// offset -- it is a key this code does not understand, and saying so is the
// difference between an observation and a guess.
func TestAKeyWithNoSeparatorIsNotReadAsASegment(t *testing.T) {
	if got := segmentOfTombstoneKey("805311376158243"); got != "" {
		t.Errorf("a key with no separator: got %q, want the empty string", got)
	}
}
