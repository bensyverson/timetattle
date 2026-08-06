package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Biome capture had the same fail-fast shape as the harvest command above
// it, twice over: one unreadable stream returned before the others were tried,
// and inside a stream one unreadable segment returned before the rest of that
// stream's segments were captured.
//
// Both matter for the same reason the command-level fix does. The segments are
// perishable — macOS fills 512 KB in place and eventually discards the oldest —
// so a run that abandons five readable segments because the sixth is locked
// maximises loss rather than minimising it. The sixth is also the likeliest to
// be unreadable: it is the active one macOS is writing.

// lockDir makes a directory this process cannot list, the closest reproduction
// of a lost Full Disk Access grant that touches nothing outside the test.
func lockDir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o700) })
	if _, err := os.ReadDir(path); err == nil {
		t.Skip("this process can list a 0000 directory, so an unreadable stream cannot be simulated")
	}
}

func lockFile(t *testing.T, path string) {
	t.Helper()
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o600) })
	if f, err := os.Open(path); err == nil {
		f.Close()
		t.Skip("this process can open a 0000 file, so an unreadable segment cannot be simulated")
	}
}

// TestTakeStreamsKeepsCapturingWhenOneStreamCannotBeRead adds a second stream
// for the length of the test. Streams holds one entry today, so the bug is
// latent rather than live, and a fixture is the only way to pin it before a
// second stream is added and quietly inherits it.
func TestTakeStreamsKeepsCapturingWhenOneStreamCannotBeRead(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()

	readable, locked := "Readable.Stream", "Locked.Stream"
	original := Streams
	Streams = []string{locked, readable}
	t.Cleanup(func() { Streams = original })

	writeStream(t, root, readable, map[string][]byte{"segment-a": fakeSegment("a", 3)})
	lockDir(t, filepath.Join(root, locked, "local"))

	results, err := TakeStreams(root, dir)
	if err == nil {
		t.Fatal("TakeStreams reported success with an unreadable stream")
	}
	if !strings.Contains(err.Error(), locked) {
		t.Errorf("the error does not name the stream that failed: %v", err)
	}
	if len(results) != 1 || results[0].Stream != readable {
		t.Fatalf("TakeStreams returned %v, want one result for %s; the unreadable stream cost the readable one",
			results, readable)
	}
	if len(results[0].Captured) != 1 {
		t.Errorf("%s captured %d segments, want 1", readable, len(results[0].Captured))
	}
}

// TestOneUnreadableSegmentDoesNotCostTheOthers is the same property one level
// down, and the one that would bite in practice: the newest segment is the one
// macOS is writing to.
func TestOneUnreadableSegmentDoesNotCostTheOthers(t *testing.T) {
	root := t.TempDir()
	dir := t.TempDir()

	local := writeStream(t, root, Streams[0], map[string][]byte{
		"segment-a": fakeSegment("a", 3),
		"segment-b": fakeSegment("b", 3),
		"segment-c": fakeSegment("c", 3),
	})
	lockFile(t, filepath.Join(local, "segment-b"))

	results, err := TakeStreams(root, dir)
	if err == nil {
		t.Fatal("TakeStreams reported success with an unreadable segment")
	}
	if !strings.Contains(err.Error(), "segment-b") {
		t.Errorf("the error does not name the segment that failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("TakeStreams returned %d results, want 1", len(results))
	}
	if got := len(results[0].Captured); got != 2 {
		t.Errorf("captured %d segments, want 2; one unreadable segment cost the readable ones", got)
	}
	for _, want := range []string{"segment-a", "segment-c"} {
		if !slicesContainsPrefix(results[0].Captured, filepath.Join(dir, Streams[0], want)) {
			t.Errorf("%s was not captured: %v", want, results[0].Captured)
		}
	}
}

func slicesContainsPrefix(paths []string, prefix string) bool {
	for _, p := range paths {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
