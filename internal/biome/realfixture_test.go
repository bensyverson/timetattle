package biome

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/bensyverson/timetattle/internal/source"
)

// testdata/realfocus.segb is a small, checked-in SEGB segment built from
// genuine record bytes captured on Ben's machine, not from buildSegment's
// hand-rolled encoder. TestRealSegmentsParse (realdata_test.go) exists
// because "a synthetic fixture cannot tell us" whether the framing this
// package assumes is the framing macOS actually writes — that argument holds
// regardless of how well buildSegment currently matches reality, and only a
// fixture derived from real bytes closes it on a machine that has no Biome
// stream at all.
//
// Provenance, recorded here because which applications were used when is
// itself the kind of personal record fixture_test.go already avoids
// committing — so this describes donor records by shape, not by the real
// bundle identifier or segment contents beyond the record index needed to
// relocate them:
//
//   - segment 806945468674832, records 0-1: a real start/stop pair for an
//     11-character bundle identifier, the shortest payload (26 bytes)
//     observed in six segments and 53,746 records.
//   - segment 806945468674832, records 2-3: a real start/stop pair for a
//     15-character bundle identifier, whose start shares its timestamp
//     with record 1's stop — focus is exclusive, so a real hand-off
//     between two applications is instantaneous, not a gap.
//   - segment 806945468674832, records 21-22: a real pair for a
//     29-character bundle identifier, the longest payload (44 bytes)
//     observed.
//   - segment 805311376158243, records 896-897: a real start/stop pair for
//     a 14-character bundle identifier. Its field 5 is 0. Every other
//     record examined across the whole corpus has field 5 = 1, which is
//     what fixture_test.go's encodeFocusChange always writes — buildSegment
//     could not have told us field 5 is capable of being anything else.
//     This is the one respect in which the real corpus disagrees with what
//     the synthetic generator produces; everything else it was checked
//     against (field order, wire types, value ranges, string encoding)
//     matched exactly.
//
// Anonymization: each record's field 1 (frontmost) and field 5 bytes are
// kept byte-for-byte as macOS wrote them — a boolean flag and an unexplained
// marker carry no personal information, and preserving them exactly is the
// entire point of pulling this fixture from real bytes rather than writing
// it by hand. Field 2 (timestamp) and field 3 (bundle identifier) are always
// replaced before anything touches disk or a test log: the timestamp with an
// arbitrary fixture instant, the bundle with a placeholder from the table
// below. No wall-clock time or application name from the real stream is
// retained anywhere in the fixture or in this file.
//
//	real bundle (never written to the fixture)  ->  placeholder
//	an 11-character messaging/calling app        ->  com.example.callapp
//	a 15-character notes app                     ->  com.example.notesapp
//	a 29-character 3-D modelling app              -> com.example.reallylongbundleidentifierforfixture
//	a 14-character writing app                   ->  com.example.editorapp (the field-5-is-0 pair)
//
// To regenerate:
//
//	REGENERATE_BIOME_FIXTURE=1 go test -tags realdata \
//		-run TestGenerateRealFocusFixture ./internal/biome/
//
// on a machine with the
// segments named in realFixtureSpecs still present (macOS prunes old
// segments, so this will stop working eventually — walk DefaultAppUsageDir,
// pick fresh donor records covering the shortest, longest and field-5=0
// cases, then update the provenance comment and the recordSpec table
// together). The regenerator reads the two segments above, locates the eight
// records by index, replaces their timestamp and bundle fields, and writes
// testdata/realfocus.segb through the same container encoder every other
// fixture in this package uses.
const realFixturePath = "testdata/realfocus.segb"

// knownSegment is the segment the spike characterised, and the one most of the
// fixture's donor records came out of. It lives on this side of the realdata
// tag because the committed fixture's provenance is part of the fixture: the
// tagged file that reads the live segment (realdata_test.go) is compiled only
// under `-tags realdata`, and a constant naming which segment the bytes came
// from must survive on every machine, whether or not macOS has since pruned it.
const knownSegment = "806945468674832"

// recordSpec names one donor record: which real segment and index to pull
// the frontmost/field-5 bytes from, and what to replace its timestamp and
// bundle with.
type recordSpec struct {
	segment string
	index   int
	bundle  string
	at      time.Time
}

func realFixtureSpecs() []recordSpec {
	return []recordSpec{
		{knownSegment, 0, "com.example.callapp", at(0)},
		{knownSegment, 1, "com.example.callapp", at(2)},
		{knownSegment, 2, "com.example.notesapp", at(2)},
		{knownSegment, 3, "com.example.notesapp", at(9)},
		{"805311376158243", 896, "com.example.editorapp", at(20)},
		{"805311376158243", 897, "com.example.editorapp", at(25)},
		{knownSegment, 21, "com.example.reallylongbundleidentifierforfixture", at(30)},
		{knownSegment, 22, "com.example.reallylongbundleidentifierforfixture", at(35)},
	}
}

// TestRealFocusFixtureParses is the portable half of what
// TestRealSegmentsParse (realdata_test.go) proves: that this package's
// framing describes real SEGB bytes. It runs on any machine, real Biome
// stream or not.
func TestRealFocusFixtureParses(t *testing.T) {
	seg, err := readSegment(realFixturePath)
	if err != nil {
		t.Fatalf("readSegment(%s): %v", realFixturePath, err)
	}
	if len(seg.changes) != 8 {
		t.Fatalf("got %d changes, want 8", len(seg.changes))
	}

	wantBundles := map[string]bool{
		"com.example.callapp":                              true,
		"com.example.notesapp":                             true,
		"com.example.editorapp":                            true,
		"com.example.reallylongbundleidentifierforfixture": true,
	}
	seen := map[string]bool{}
	for _, c := range seg.changes {
		if !wantBundles[c.bundle] {
			t.Errorf("unexpected bundle %q in fixture", c.bundle)
		}
		seen[c.bundle] = true
	}
	for b := range wantBundles {
		if !seen[b] {
			t.Errorf("fixture is missing bundle %q", b)
		}
	}

	// The exclusive-focus hand-off: record 1 (callapp stop) and record 2
	// (notesapp start) share a timestamp, exactly as the real donor records
	// did.
	if !seg.changes[1].at.Equal(seg.changes[2].at) {
		t.Errorf("stop/start hand-off is not instantaneous: %s vs %s", seg.changes[1].at, seg.changes[2].at)
	}
}

// testdata/realdeleted.segb carries what macOS leaves behind when BiomeAgent
// deletes records that have aged past 28 days: the record bytes are zeroed,
// the index entries survive in state 3 with their extents intact, and the
// header records how many went. It is the portable half of
// TestRealSegmentsAgreeWithTheTombstoneStream.
//
// What is real in it, and it is a short list because a deleted record has
// almost nothing left to be real: the twelve extents of the deleted prefix,
// taken verbatim from 804992150463800 as macOS left them, and therefore the
// byte at which the first surviving record begins. That last is the whole
// point. A parser that fails to walk the deleted extents starts reading record
// twelve inside the zeroed hole, and the fixture catches it because the
// checksum of a real record read at the wrong offset does not match.
//
// Anonymization: the deleted records themselves need none, because macOS has
// already destroyed their contents — zero bytes are what is on disk. Their
// extents are lengths and nothing else, and twelve of them are kept rather
// than all 946 so that the fixture carries a sample of record lengths rather
// than a distribution of them. Every timestamp is replaced: an index entry's
// timestamp survives deletion, and when a machine was in use is exactly the
// personal record this package's fixtures avoid committing. The four surviving
// records are donor records anonymized the same way realfocus.segb's are, with
// the placeholder bundles from the table above.
//
// To regenerate, on a machine whose Biome stream has had records deleted:
//
//	REGENERATE_BIOME_FIXTURE=1 go test -tags realdata \
//		-run TestGenerateRealDeletedFixture ./internal/biome/
const realDeletedPath = "testdata/realdeleted.segb"

// realDeletedCount is how many deleted extents the fixture carries.
const realDeletedCount = 12

// deletedStamp dates the i-th deleted entry. Deleted records are the oldest a
// segment holds, so these sit before the surviving records rather than among
// them, and they are arbitrary fixture instants rather than anything real.
func deletedStamp(i int) time.Time { return at(-100 + i) }

// realDeletedSpecs names the surviving records that follow the deleted prefix.
func realDeletedSpecs() []recordSpec {
	return []recordSpec{
		{knownSegment, 0, "com.example.callapp", at(0)},
		{knownSegment, 1, "com.example.callapp", at(2)},
		{knownSegment, 2, "com.example.notesapp", at(2)},
		{knownSegment, 3, "com.example.notesapp", at(9)},
	}
}

// TestRealDeletedFixtureReadsPastTheTombstones is the portable proof that
// state 3 is handled as macOS writes it, on real extents, on any machine.
func TestRealDeletedFixtureReadsPastTheTombstones(t *testing.T) {
	seg, err := readSegment(realDeletedPath)
	if err != nil {
		t.Fatalf("readSegment(%s): %v", realDeletedPath, err)
	}
	if seg.deleted != realDeletedCount {
		t.Errorf("got deleted = %d, want %d", seg.deleted, realDeletedCount)
	}
	if len(seg.changes) != len(realDeletedSpecs()) {
		t.Fatalf("got %d changes, want %d", len(seg.changes), len(realDeletedSpecs()))
	}
	for i, spec := range realDeletedSpecs() {
		if c := seg.changes[i]; c.bundle != spec.bundle || !c.at.Equal(spec.at) {
			t.Errorf("change %d = %s at %s, want %s at %s", i, c.bundle, c.at, spec.bundle, spec.at)
		}
	}
}

// TestRealDeletedFixtureCollectsOnlyTheSurvivingIntervals runs it through the
// whole source. A deleted record must contribute no event at all — not an
// event with an unknown edge, which is what a record that was never written
// contributes, and not a fabricated one.
func TestRealDeletedFixtureCollectsOnlyTheSurvivingIntervals(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(realDeletedPath)
	if err != nil {
		t.Fatalf("read %s: %v", realDeletedPath, err)
	}
	writeSegment(t, dir, "1", data)

	events, err := NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []source.Event{
		source.NewInterval(at(0), at(2), source.Identity{App: "com.example.callapp"}, "biome-appusage"),
		source.NewInterval(at(2), at(9), source.Identity{App: "com.example.notesapp"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}

// TestRealFocusFixtureCollectsUsableIntervals runs the fixture through the
// whole source, the portable half of TestRealCollectProducesUsableIntervals.
func TestRealFocusFixtureCollectsUsableIntervals(t *testing.T) {
	dir := t.TempDir()
	data, err := os.ReadFile(realFixturePath)
	if err != nil {
		t.Fatalf("read %s: %v", realFixturePath, err)
	}
	writeSegment(t, dir, "1", data)

	events, err := NewAppUsage(dir).Collect(context.Background(), time.Time{})
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	want := []source.Event{
		source.NewInterval(at(0), at(2), source.Identity{App: "com.example.callapp"}, "biome-appusage"),
		source.NewInterval(at(2), at(9), source.Identity{App: "com.example.notesapp"}, "biome-appusage"),
		source.NewInterval(at(20), at(25), source.Identity{App: "com.example.editorapp"}, "biome-appusage"),
		source.NewInterval(at(30), at(35), source.Identity{App: "com.example.reallylongbundleidentifierforfixture"}, "biome-appusage"),
	}
	requireEvents(t, events, want)
}
