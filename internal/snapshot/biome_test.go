package snapshot

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// segmentSize is the 512 KB macOS pre-allocates for every Biome segment. The
// fixtures here are that size deliberately: it is the whole reason a size
// comparison cannot detect that a segment has changed.
const segmentSize = 512 * 1024

// fakeSegment builds a fixture the size of a real segment whose written prefix
// is derived from seed. It is deliberately not a valid SEGB file — capture must
// preserve bytes without understanding them, and a fixture that parsed would
// let an interpreting implementation pass.
func fakeSegment(seed string, records int) []byte {
	out := make([]byte, segmentSize)
	copy(out, "SEGB")
	for i := range records {
		copy(out[32+i*64:], seed+"-record-"+string(rune('a'+i%26)))
	}
	return out
}

// writeStream drops fixtures into <root>/<stream>/local, the way macOS lays a
// Biome stream out.
func writeStream(t *testing.T, root, stream string, files map[string][]byte) string {
	t.Helper()
	local := filepath.Join(root, stream, "local")
	if err := os.MkdirAll(local, 0o700); err != nil {
		t.Fatalf("create %s: %v", local, err)
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(local, name), data, 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return local
}

// captured lists the file names a run has left under dir for one stream.
func captured(t *testing.T, dir, stream string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, stream))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read capture dir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names
}

// restore decompresses one captured segment back to the bytes macOS held.
func restore(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open capture: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("capture %s is not gzip: %v", path, err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("decompress %s: %v", path, err)
	}
	return data
}

// walk returns every regular file under dir, so a privacy assertion can look at
// the whole tree rather than at the one place it expects trouble.
func walk(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			out = append(out, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	slices.Sort(out)
	return out
}

// TestTakeStreamsPreservesSegmentsVerbatim: capture is insurance, so what comes
// back out must be what macOS held, byte for byte. Anything less means a bug in
// the collector has already cost us history.
func TestTakeStreamsPreservesSegmentsVerbatim(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	want := map[string][]byte{
		"804992150463800": fakeSegment("first", 9),
		"806945468674832": fakeSegment("second", 4),
	}
	writeStream(t, root, "ScreenTime.AppUsage", want)

	out := filepath.Join(dir, "captures")
	results, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d stream results, want 1", len(results))
	}
	if results[0].Stream != "ScreenTime.AppUsage" {
		t.Errorf("Stream = %q, want ScreenTime.AppUsage", results[0].Stream)
	}
	if len(results[0].Captured) != 2 {
		t.Errorf("Captured = %v, want both segments", results[0].Captured)
	}

	names := captured(t, out, "ScreenTime.AppUsage")
	if len(names) != 2 {
		t.Fatalf("capture directory holds %v, want two segments", names)
	}
	for _, name := range names {
		got := restore(t, filepath.Join(out, "ScreenTime.AppUsage", name))
		segment, _, _ := strings.Cut(name, "-")
		if !bytes.Equal(got, want[segment]) {
			t.Errorf("%s did not round-trip: %d bytes back, want %d", name, len(got), len(want[segment]))
		}
	}
}

// TestTakeStreamsRecapturesASegmentFilledInPlace is the point of the whole
// package.
//
// Segments are pre-allocated at 512 KB and filled in place, and their mtime was
// measured lagging their newest record by as much as twenty hours. So a run
// that trusted size, or existence, or mtime would freeze the live segment at
// whatever partial contents it happened to have when first seen, and lose every
// record written afterwards — permanently, and without saying a word.
func TestTakeStreamsRecapturesASegmentFilledInPlace(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	local := writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 4),
	})
	path := filepath.Join(local, "806945468674832")

	out := filepath.Join(dir, "captures")
	if _, err := TakeStreams(root, out); err != nil {
		t.Fatalf("first TakeStreams: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat segment: %v", err)
	}

	// Grow the segment exactly as macOS does: more records, same file length,
	// and — because the pages have not been flushed — the same mtime.
	grown := fakeSegment("live", 11)
	if err := os.WriteFile(path, grown, 0o600); err != nil {
		t.Fatalf("grow segment: %v", err)
	}
	if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
		t.Fatalf("restore mtime: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat grown segment: %v", err)
	}
	if after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		t.Fatalf("fixture failed to reproduce the trap: size %d->%d, mtime %v->%v",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}

	if _, err := TakeStreams(root, out); err != nil {
		t.Fatalf("second TakeStreams: %v", err)
	}

	names := captured(t, out, "ScreenTime.AppUsage")
	if len(names) != 2 {
		t.Fatalf("capture directory holds %v, want the partial segment and the grown one", names)
	}
	var seen [][]byte
	for _, name := range names {
		seen = append(seen, restore(t, filepath.Join(out, "ScreenTime.AppUsage", name)))
	}
	if !slices.ContainsFunc(seen, func(b []byte) bool { return bytes.Equal(b, grown) }) {
		t.Error("the grown segment was never captured; every record written after the first run is lost")
	}
	if !slices.ContainsFunc(seen, func(b []byte) bool { return bytes.Equal(b, fakeSegment("live", 4)) }) {
		t.Error("the earlier capture was overwritten; a capture destroyed is history destroyed")
	}
}

// TestTakeStreamsSkipsSegmentsAlreadyHeld: settled segments are the bulk of the
// stream and must not be rewritten on every run.
func TestTakeStreamsSkipsSegmentsAlreadyHeld(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"804992150463800": fakeSegment("first", 9),
		"805311376158243": fakeSegment("second", 9),
		"806945468674832": fakeSegment("third", 2),
	})

	out := filepath.Join(dir, "captures")
	first, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("first TakeStreams: %v", err)
	}
	if got := len(first[0].Captured); got != 3 {
		t.Fatalf("first run captured %d segments, want 3", got)
	}
	if first[0].Unchanged != 0 {
		t.Errorf("first run reported %d unchanged, want 0", first[0].Unchanged)
	}

	before := captured(t, out, "ScreenTime.AppUsage")
	second, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("second TakeStreams: %v", err)
	}
	if got := len(second[0].Captured); got != 0 {
		t.Errorf("second run captured %d segments, want 0 — nothing had changed", got)
	}
	if second[0].Unchanged != 3 {
		t.Errorf("second run reported %d unchanged, want 3", second[0].Unchanged)
	}
	if second[0].Bytes != 0 {
		t.Errorf("second run wrote %d bytes, want 0", second[0].Bytes)
	}
	if after := captured(t, out, "ScreenTime.AppUsage"); !slices.Equal(before, after) {
		t.Errorf("capture directory changed from %v to %v on a run with nothing to do", before, after)
	}
}

// TestTakeStreamsCapturesOnlyNamedStreams is a privacy guard, and the reason
// the streams are named one by one rather than globbed. The restricted
// directory holds over 150 streams including Safari navigations, Mail
// harvesting and Siri history, none of which time tracking needs.
func TestTakeStreamsCapturesOnlyNamedStreams(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("wanted", 3),
	})
	writeStream(t, root, "Safari.History", map[string][]byte{
		"806945468674832": []byte("https://example.com/private-browsing"),
	})
	writeStream(t, root, "App.InFocus", map[string][]byte{
		"806865752322564": []byte("a sibling stream we deliberately do not keep"),
	})
	writeStream(t, root, "Siri.Interaction", map[string][]byte{
		"806945468674832": []byte("what did I ask Siri"),
	})

	out := filepath.Join(dir, "captures")
	results, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}
	for _, r := range results {
		if !slices.Contains(Streams, r.Stream) {
			t.Errorf("captured stream %q, which is not in Streams", r.Stream)
		}
	}

	for _, path := range walk(t, out) {
		if strings.Contains(path, "Safari") || strings.Contains(path, "Siri") || strings.Contains(path, "InFocus") {
			t.Errorf("capture wrote %s, from a stream Timetattle does not use", path)
		}
		data := restore(t, path)
		for _, secret := range []string{"private-browsing", "what did I ask Siri", "sibling stream"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Errorf("%s contains %q, from a stream Timetattle does not use", path, secret)
			}
		}
	}
}

// TestStreamsNamesOnlyTheAppUsageStream pins the list itself. Widening it is a
// privacy decision and should not be possible to make by accident.
func TestStreamsNamesOnlyTheAppUsageStream(t *testing.T) {
	if !slices.Equal(Streams, []string{"ScreenTime.AppUsage"}) {
		t.Errorf("Streams = %v; widening this copies more of ~/Library/Biome and needs a deliberate decision", Streams)
	}
}

// TestTakeStreamsIgnoresTheTombstoneDirectory: Biome keeps a tombstone
// directory beside the segments, and descending into it would copy whatever
// macOS chose to put there.
func TestTakeStreamsIgnoresTheTombstoneDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	local := writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 3),
	})
	if err := os.MkdirAll(filepath.Join(local, "tombstone"), 0o700); err != nil {
		t.Fatalf("create tombstone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(local, "tombstone", "deleted"), []byte("tombstone contents"), 0o600); err != nil {
		t.Fatalf("write tombstone entry: %v", err)
	}

	out := filepath.Join(dir, "captures")
	results, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}
	if got := len(results[0].Captured); got != 1 {
		t.Errorf("captured %d files, want only the one segment", got)
	}
	for _, path := range walk(t, out) {
		if strings.Contains(path, "tombstone") {
			t.Errorf("capture descended into the tombstone directory: %s", path)
		}
	}
}

// TestTakeStreamsMissingStreamIsNotAFailure: a machine that has never written
// this stream is indistinguishable from one where macOS has moved it again, and
// neither is an error a user could act on. The stream appearing later must then
// be picked up without anything having to be reset.
func TestTakeStreamsMissingStreamIsNotAFailure(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	out := filepath.Join(dir, "captures")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("create root: %v", err)
	}

	results, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams on an absent stream: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("got %v, want no results for a stream that is not there", results)
	}

	writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 3),
	})
	results, err = TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams once the stream exists: %v", err)
	}
	if len(results) != 1 || len(results[0].Captured) != 1 {
		t.Fatalf("got %v, want the segment captured once the stream appeared", results)
	}
}

// TestTakeStreamsUnreadableStreamIsActionable: macOS reports a missing Full
// Disk Access grant as a bare permission error, which tells an operator
// nothing about what to do next.
func TestTakeStreamsUnreadableStreamIsActionable(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	local := writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 3),
	})
	if err := os.Chmod(local, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { os.Chmod(local, 0o700) })
	if entries, err := os.ReadDir(local); err == nil {
		t.Skipf("directory readable despite mode 0o000 (%d entries); test is meaningless as this user", len(entries))
	}

	_, err := TakeStreams(root, filepath.Join(dir, "captures"))
	if err == nil {
		t.Fatal("TakeStreams succeeded on an unreadable stream; a silent failure loses data without saying so")
	}
	if !strings.Contains(err.Error(), "Full Disk Access") {
		t.Errorf("error %q does not mention Full Disk Access, so it is not actionable", err)
	}
	if !strings.Contains(err.Error(), local) {
		t.Errorf("error %q does not name the unreadable path %s", err, local)
	}
}

// TestTakeStreamsLeavesNoPartialFiles: a capture is named for the digest of its
// contents, so a half-written file under a finished name would be skipped by
// every later run — a permanent hole that looks like a success.
func TestTakeStreamsLeavesNoPartialFiles(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"804992150463800": fakeSegment("first", 9),
		"806945468674832": fakeSegment("second", 3),
	})

	out := filepath.Join(dir, "captures")
	results, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}

	files := walk(t, out)
	if len(files) != 2 {
		t.Errorf("capture directory holds %v, want exactly the two segments", files)
	}
	var total int64
	for _, path := range files {
		if !strings.HasSuffix(path, ".segb.gz") {
			t.Errorf("%s is not a finished capture", path)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		total += info.Size()
	}
	if results[0].Bytes != total {
		t.Errorf("Result.Bytes = %d, the files on disk total %d", results[0].Bytes, total)
	}
}

// TestTakeStreamsDoesNotModifyTheSource: Biome belongs to macOS.
func TestTakeStreamsDoesNotModifyTheSource(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	local := writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 3),
	})
	path := filepath.Join(local, "806945468674832")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	results, err := TakeStreams(root, filepath.Join(dir, "captures"))
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}
	if len(results) != 1 || len(results[0].Captured) != 1 {
		t.Fatalf("got %v, want the segment captured; an untouched source proves nothing if nothing was read", results)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		t.Errorf("segment changed: size %d->%d, mtime %v->%v",
			before.Size(), after.Size(), before.ModTime(), after.ModTime())
	}
}

// TestTakeStreamsCreatesItsDirectory keeps a scheduled run working on a machine
// where nothing has captured anything yet.
func TestTakeStreamsCreatesItsDirectory(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 3),
	})

	out := filepath.Join(dir, "does", "not", "exist")
	results, err := TakeStreams(root, out)
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}
	if _, err := os.Stat(results[0].Captured[0]); err != nil {
		t.Errorf("capture not written into a created directory: %v", err)
	}
}

// TestTakeStreamsCompressesWhatItKeeps: a 512 KB segment is mostly the
// zero-filled space macOS pre-allocated for records not yet written.
func TestTakeStreamsCompressesWhatItKeeps(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "biome")
	writeStream(t, root, "ScreenTime.AppUsage", map[string][]byte{
		"806945468674832": fakeSegment("live", 20),
	})

	results, err := TakeStreams(root, filepath.Join(dir, "captures"))
	if err != nil {
		t.Fatalf("TakeStreams: %v", err)
	}
	if results[0].Bytes >= segmentSize/4 {
		t.Errorf("captured %d bytes for a %d-byte segment; compression is not working", results[0].Bytes, segmentSize)
	}
}

func TestDefaultBiomePaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	root, err := DefaultBiomeRoot()
	if err != nil {
		t.Fatalf("DefaultBiomeRoot: %v", err)
	}
	if want := filepath.Join(home, "Library/Biome/streams/restricted"); root != want {
		t.Errorf("DefaultBiomeRoot = %q, want %q", root, want)
	}
	dir, err := DefaultBiomeDir()
	if err != nil {
		t.Fatalf("DefaultBiomeDir: %v", err)
	}
	if want := filepath.Join(home, ".timetattle", "snapshots", "biome"); dir != want {
		t.Errorf("DefaultBiomeDir = %q, want %q", dir, want)
	}
}

// TestReadSettledRetriesUntilTwoReadsAgree: the newest segment is written while
// it is read, and a read spanning an append yields a file whose header counts
// records its index does not list. Because a capture is named for its digest,
// storing that torn copy would occupy a name no later run revisits.
func TestReadSettledRetriesUntilTwoReadsAgree(t *testing.T) {
	reads := [][]byte{
		[]byte("torn"),
		[]byte("also torn"),
		[]byte("whole"),
		[]byte("whole"),
	}
	var n int
	got, err := readSettled("806945468674832", func() ([]byte, error) {
		b := reads[n]
		n++
		return b, nil
	})
	if err != nil {
		t.Fatalf("readSettled: %v", err)
	}
	if string(got) != "whole" {
		t.Errorf("readSettled = %q, want the contents that stopped changing", got)
	}
	if n != len(reads) {
		t.Errorf("readSettled made %d reads, want %d — it stopped before the file settled", n, len(reads))
	}
}

// TestReadSettledGivesUpRatherThanStoringATornCopy. Focus changes arrive
// seconds apart and these reads take microseconds, so a segment that never
// settles is not a busy machine; it is something this code does not understand,
// and a named error beats a plausible-looking capture.
func TestReadSettledGivesUpRatherThanStoringATornCopy(t *testing.T) {
	var n int
	_, err := readSettled("806945468674832", func() ([]byte, error) {
		n++
		return fmt.Appendf(nil, "always different %d", n), nil
	})
	if err == nil {
		t.Fatal("readSettled succeeded on a file that never settled")
	}
	if !strings.Contains(err.Error(), "806945468674832") {
		t.Errorf("error %q does not name the segment", err)
	}
	if n > 16 {
		t.Errorf("readSettled made %d reads before giving up; it should not spin", n)
	}
}

// TestReadSettledReportsAFailedRead: a segment that becomes unreadable partway
// through must not be quietly treated as one that never settled, or a
// permission problem would be reported as an unstable file.
func TestReadSettledReportsAFailedRead(t *testing.T) {
	want := errors.New("permission denied")
	var n int
	// The first read succeeds; the retry does not. Only an implementation that
	// reads more than once can see this at all.
	_, err := readSettled("806945468674832", func() ([]byte, error) {
		n++
		if n == 1 {
			return []byte("first"), nil
		}
		return nil, want
	})
	if !errors.Is(err, want) {
		t.Errorf("readSettled = %v, want the read error passed through", err)
	}
}
