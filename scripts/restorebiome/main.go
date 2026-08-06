// Command restorebiome rebuilds a readable Biome stream from captured
// segments.
//
// A harvest keeps every distinct set of contents a segment ever had, so the
// capture directory holds several versions of the segment that was live at the
// time. Pointing a reader straight at it would count those records more than
// once. This picks the newest version of each segment — newest by the capture
// file's own modification time, which Timetattle wrote and can therefore trust,
// unlike Biome's — and decompresses it under its original name.
//
// The result is laid out exactly as macOS lays out a live stream, so anything
// that reads one reads this:
//
//	go run ./scripts/restorebiome -out /tmp/stream
//	go run ./scripts/appfocus -dir /tmp/stream/ScreenTime.AppUsage/local
//
// Like the probes in scripts/spike, this is a recovery and analysis tool rather
// than product behaviour and is exempt from the project's red/green TDD rule.
package main

import (
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/bensyverson/timetattle/internal/snapshot"
)

func main() {
	dir := flag.String("dir", "", "captured segments (default: ~/.timetattle/snapshots/biome)")
	out := flag.String("out", "", "where to write the rebuilt stream (required)")
	flag.Parse()

	if err := run(*dir, *out); err != nil {
		fmt.Fprintf(os.Stderr, "restorebiome: %v\n", err)
		os.Exit(1)
	}
}

func run(dir, out string) error {
	if out == "" {
		return fmt.Errorf("-out is required; it names a directory to write the rebuilt stream into")
	}
	if dir == "" {
		var err error
		if dir, err = snapshot.DefaultBiomeDir(); err != nil {
			return err
		}
	}

	streams, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read captures at %s: %w", dir, err)
	}
	for _, stream := range streams {
		if !stream.IsDir() {
			continue
		}
		if err := restoreStream(filepath.Join(dir, stream.Name()), filepath.Join(out, stream.Name(), "local")); err != nil {
			return err
		}
	}
	return nil
}

func restoreStream(from, to string) error {
	entries, err := os.ReadDir(from)
	if err != nil {
		return fmt.Errorf("read %s: %w", from, err)
	}
	if err := os.MkdirAll(to, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", to, err)
	}

	// Keep only the most recently captured version of each segment.
	newest := map[string]os.DirEntry{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".segb.gz") {
			continue
		}
		segment, _, _ := strings.Cut(e.Name(), "-")
		best, seen := newest[segment]
		if !seen {
			newest[segment] = e
			continue
		}
		a, err := e.Info()
		if err != nil {
			return err
		}
		b, err := best.Info()
		if err != nil {
			return err
		}
		if a.ModTime().After(b.ModTime()) {
			newest[segment] = e
		}
	}

	for segment, e := range newest {
		n, err := decompress(filepath.Join(from, e.Name()), filepath.Join(to, segment))
		if err != nil {
			return err
		}
		fmt.Printf("%s  %d bytes  from %s\n", segment, n, e.Name())
	}
	return nil
}

func decompress(src, dst string) (int64, error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()
	zr, err := gzip.NewReader(in)
	if err != nil {
		return 0, fmt.Errorf("%s is not gzip: %w", src, err)
	}
	defer zr.Close()

	out, err := os.Create(dst)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, zr)
	if err != nil {
		out.Close()
		return 0, err
	}
	return n, out.Close()
}
