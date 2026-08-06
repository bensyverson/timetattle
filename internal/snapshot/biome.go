package snapshot

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Streams names the Biome streams a harvest preserves, one by one.
//
// This list is short on purpose and must stay that way.
// ~/Library/Biome/streams/restricted holds 153 streams on the machine this was
// written against, among them Safari navigations, Mail harvesting, Siri
// interactions and Bluetooth proximity. Copying the parent directory, or
// matching a pattern against it, would put all of that in a Timetattle
// directory forever in exchange for one stream's worth of value. Naming each
// stream makes widening the capture a decision somebody has to make on purpose.
//
// App.InFocus is deliberately absent. It is a sibling stream describing the
// same focus changes, and it is not a second opinion worth keeping: its newest
// segment was last written 2026-07-27 12:22 and has not been appended to since,
// while ScreenTime.AppUsage stayed current — the same pattern knowledgeC's
// /app/usage showed the day before, which there turned out to be a stall rather
// than a retirement, so this is a reason to watch it. It also carries a remote/
// directory holding other devices' data, which nothing here wants. If
// ScreenTime.AppUsage ever stops in its turn, revisit this; capturing a dead
// stream now would cost privacy and disk for nothing.
var Streams = []string{"ScreenTime.AppUsage"}

const (
	// biomeStreamRoot is where macOS keeps the restricted streams.
	biomeStreamRoot = "Library/Biome/streams/restricted"

	// segmentLeaf is the subdirectory holding this machine's own segments.
	// Some streams also carry a remote/ directory of other devices' data,
	// which is never read.
	segmentLeaf = "local"

	// captureSuffix marks a finished capture. Anything else in the capture
	// directory is wreckage from an interrupted run.
	captureSuffix = ".segb.gz"

	// digestLength is how much of the SHA-256 goes in the file name: 32 hex
	// characters, 128 bits. A collision would mean skipping a capture, which
	// is the exact permanent loss this package exists to prevent, so the
	// margin is deliberately absurd rather than merely sufficient.
	digestLength = 32

	// maxSegmentBytes bounds a read. Segments have been 512 KB in every
	// version observed; this exists so that a future macOS writing something
	// enormous fails with a sentence instead of an out-of-memory kill.
	maxSegmentBytes = 64 << 20

	// settleReads is how many times a segment may change under us before the
	// run gives up on it. See readSettled.
	settleReads = 5
)

// StreamResult describes what one run did with one stream.
type StreamResult struct {
	// Stream is the Biome stream name, e.g. ScreenTime.AppUsage.
	Stream string
	// Captured lists the files this run wrote.
	Captured []string
	// Unchanged counts segments whose bytes were already held.
	Unchanged int
	// Bytes is the compressed size of what this run wrote.
	Bytes int64
}

// TakeStreams copies the Biome segments under root into dir, skipping any whose
// exact contents are already held.
//
// # Why a content digest, and not anything cheaper
//
// Biome pre-allocates every segment at 512 KB and fills it in place. All six
// segments on the machine this was written against are exactly 524288 bytes,
// including the one still being written. That rules out the two detectors
// anyone reaches for first, and the failure of each is silent:
//
//   - Size. Constant by construction. A size comparison reports "unchanged"
//     for the lifetime of the stream.
//   - Existence. `if !exists { copy }` freezes the live segment at whatever
//     partial contents it had when first seen and loses every record written
//     afterwards, with no error and no gap anyone could notice.
//
// Modification time looks like the honest answer and is not. Measured across
// all six segments, mtime lags the last record inside the segment every time,
// and never leads it: by 3m59s at best and by 19h25m at worst
// (806264107237530, mtime 2026-07-22 15:59:36 against a last record at
// 2026-07-23 11:24:16). Whatever the mechanism — the pattern is what a
// memory-mapped file looks like, where the kernel stamps mtime on page flush
// rather than on write — the consequence is fixed: mtime systematically
// under-reports how fresh a segment is, so two runs either side of a stale
// window see an unchanged mtime over changed bytes. That is the same silent
// loss in a more convincing disguise.
//
// What remains is the contents themselves. Each segment is read and hashed,
// and the digest is the file name. A segment whose name is already present is
// byte-for-byte what we hold and is not written again. Reading the whole
// stream costs about 3 MB per run, which is not worth optimising away.
//
// # The cost this does not avoid
//
// Criterion 77L asks that captured segments are not recopied wholesale, and
// that cannot be fully true of the newest segment: it grows in place, so its
// contents genuinely differ on most runs and re-copying is the only way not to
// lose the difference. What is achieved is the narrower and correct property —
// no segment is ever stored twice for the same contents. In practice five of
// six segments are read and skipped, and the active one is stored again at
// roughly 20-140 KB compressed until a successor supersedes it and it settles
// on its own. At twice a day that is around 50 MB a year for the whole stream.
//
// # No manifest
//
// State is the capture directory. A segment is held if a file named for its
// digest exists, which os.Stat answers. A separate record of what was captured
// could disagree with the files, and the files are the thing that matters.
//
// # Partial failure
//
// A stream that cannot be read loses only itself, and the results describe
// every stream that could be captured. The error is returned alongside them,
// not instead of them: these segments are perishable, and abandoning the
// readable streams because one is unreadable maximises the loss rather than
// minimising it. Streams holds one entry today, so this is a property being
// held rather than one being exercised — which is why it is tested against a
// fixture that adds a second.
func TakeStreams(root, dir string) ([]StreamResult, error) {
	var (
		results []StreamResult
		errs    []error
	)
	for _, stream := range Streams {
		res, err := takeStream(root, stream, dir)
		if err != nil {
			errs = append(errs, err)
		}
		if res == nil {
			continue
		}
		results = append(results, *res)
	}
	return results, errors.Join(errs...)
}

// takeStream captures one stream, or reports nil if this machine does not have
// it. A stream that is not there is indistinguishable from an application
// nobody installed, or from macOS having moved the data again; none of those is
// something an operator could act on. A stream that refuses to be read is
// different, and says so.
//
// A segment that refuses to be read costs that segment and nothing else, and
// the result still describes everything captured beside it. This is the case
// that bites in practice rather than the stream-level one: the segment most
// likely to be locked, or to be changing under a read, is the newest — the one
// macOS is actively writing — and losing five settled segments to protect
// nobody from that would be the worst possible trade for perishable evidence.
func takeStream(root, stream, dir string) (*StreamResult, error) {
	local := filepath.Join(root, stream, segmentLeaf)
	entries, err := os.ReadDir(local)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case errors.Is(err, fs.ErrPermission):
		return nil, fmt.Errorf("cannot read %s: %w\n\n"+
			"This is almost always a missing Full Disk Access grant. Open\n"+
			"System Settings > Privacy & Security > Full Disk Access, add the\n"+
			"program running Timetattle (your terminal, or the timetattle\n"+
			"binary itself when run from launchd), then restart it.", local, err)
	case err != nil:
		return nil, fmt.Errorf("cannot read the Biome stream at %s: %w", local, err)
	}

	into := filepath.Join(dir, stream)
	res := StreamResult{Stream: stream}
	var errs []error
	for _, entry := range entries {
		// Biome keeps a tombstone directory beside the segments. Nothing
		// descends into it: what is captured is the segments, named, and
		// whatever macOS decides to file under tombstone is not that.
		if entry.IsDir() {
			continue
		}

		path := filepath.Join(local, entry.Name())
		data, err := readSettled(path, func() ([]byte, error) { return readSegmentFile(path) })
		if err != nil {
			errs = append(errs, err)
			continue
		}

		sum := sha256.Sum256(data)
		name := entry.Name() + "-" + hex.EncodeToString(sum[:])[:digestLength] + captureSuffix
		dst := filepath.Join(into, name)

		switch _, err := os.Stat(dst); {
		case err == nil:
			res.Unchanged++
			continue
		case !errors.Is(err, fs.ErrNotExist):
			errs = append(errs, fmt.Errorf("cannot check for an existing capture at %s: %w", dst, err))
			continue
		}

		// A destination that cannot be created is not a property of any one
		// segment, and retrying it per segment would return the same error as
		// many times as the stream has files. The stream stops here, keeping
		// whatever it had already written.
		if err := os.MkdirAll(into, 0o700); err != nil {
			errs = append(errs, fmt.Errorf("create capture directory %s: %w", into, err))
			break
		}
		n, err := writeCapture(dst, data)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		res.Captured = append(res.Captured, dst)
		res.Bytes += n
	}
	return &res, errors.Join(errs...)
}

// readSegmentFile reads a whole segment into memory.
//
// It is read in one go rather than streamed because the digest has to describe
// the same bytes that get stored; hashing one read and copying a second would
// name the capture after contents it does not contain.
func readSegmentFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("cannot read the Biome segment %s: %w\n\n"+
				"This is almost always a missing Full Disk Access grant.", path, err)
		}
		return nil, fmt.Errorf("cannot read the Biome segment %s: %w", path, err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxSegmentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the Biome segment %s: %w", path, err)
	}
	if len(data) > maxSegmentBytes {
		return nil, fmt.Errorf("the Biome segment %s is larger than %d bytes; segments have always been 512 KB, so this is not the format this was built against",
			path, maxSegmentBytes)
	}
	return data, nil
}

// readSettled returns the contents of a segment once two consecutive reads
// agree.
//
// The newest segment is being written while it is read, and a read that spans
// an append would store a file whose two halves describe different moments: a
// header counting records the index does not yet list, which the reader in
// internal/biome rejects outright. Because the capture is named for its digest,
// such a file would also occupy a name no later run would revisit. Reading
// twice costs half a megabyte and turns that into a retry.
//
// Giving up is better than storing the torn copy. A segment that changes across
// five consecutive reads is not a busy machine — focus changes arrive seconds
// apart and these reads take microseconds — it is something this code does not
// understand.
func readSettled(path string, read func() ([]byte, error)) ([]byte, error) {
	prev, err := read()
	if err != nil {
		return nil, err
	}
	for range settleReads {
		next, err := read()
		if err != nil {
			return nil, err
		}
		if bytes.Equal(prev, next) {
			return next, nil
		}
		prev = next
	}
	return nil, fmt.Errorf("the Biome segment %s changed during each of %d consecutive reads, so no consistent copy could be taken; re-run the harvest",
		path, settleReads+1)
}

// writeCapture gzips data into dst, and returns the size written.
//
// It stages inside the capture directory rather than beside it, which departs
// from how knowledgeC snapshots are written and does so deliberately. A
// snapshot is named for the time it was taken, so an interrupted one is merely
// a bad file that the next run replaces. A capture is named for the digest of
// its contents, so an interrupted one is a name every later run will skip: a
// permanent hole that looks exactly like a success. Only a rename within the
// destination filesystem is atomic, hence a staging file that lives there,
// hidden, and is removed on any failure.
func writeCapture(dst string, data []byte) (int64, error) {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".partial-*")
	if err != nil {
		return 0, fmt.Errorf("stage a capture in %s: %w", dir, err)
	}
	staged := tmp.Name()
	defer os.Remove(staged) // a no-op once the rename below has succeeded

	fail := func(format string, args ...any) (int64, error) {
		tmp.Close()
		return 0, fmt.Errorf(format, args...)
	}

	zw, _ := gzip.NewWriterLevel(tmp, gzip.BestCompression)
	if _, err := zw.Write(data); err != nil {
		return fail("compress a capture for %s: %w", dst, err)
	}
	if err := zw.Close(); err != nil {
		return fail("finish compressing a capture for %s: %w", dst, err)
	}
	// Without this the rename can outlive the bytes across a power loss,
	// leaving an empty file under a name that claims to hold a segment.
	if err := tmp.Sync(); err != nil {
		return fail("flush a capture for %s: %w", dst, err)
	}
	info, err := tmp.Stat()
	if err != nil {
		return fail("size a capture for %s: %w", dst, err)
	}
	if err := tmp.Close(); err != nil {
		return 0, fmt.Errorf("write a capture for %s: %w", dst, err)
	}

	// Rename can overwrite, and here that is safe rather than merely
	// tolerated: the name is the digest of the contents, so anything already
	// under it is these same bytes.
	if err := os.Rename(staged, dst); err != nil {
		return 0, fmt.Errorf("move a capture into %s: %w", dst, err)
	}
	return info.Size(), nil
}

// DefaultBiomeRoot is where macOS keeps the restricted Biome streams for the
// current user. Only the streams in Streams are ever read from it.
func DefaultBiomeRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("determine home directory: %w", err)
	}
	return filepath.Join(home, biomeStreamRoot), nil
}

// BiomeDir is where captured Biome segments live under a snapshot directory.
// Captures sit beside knowledgeC snapshots rather than in a directory of their
// own so that one path is the whole of what a harvest has preserved.
func BiomeDir(dir string) string { return filepath.Join(dir, "biome") }

// DefaultBiomeDir is where Timetattle preserves captured Biome segments.
func DefaultBiomeDir() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return BiomeDir(dir), nil
}
